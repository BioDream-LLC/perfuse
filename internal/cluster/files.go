package cluster

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Files keeps directories the same on every server of a cluster, through the shared store: the channel files, the
// settings and alert files, sign-in configuration. Each server keeps its own copy on its own disk, so a channel loads from
// a file as it always has, and a server that loses the store keeps what it had.
//
// A change made on any server (through the console, by hand, by a git pull) is noticed at the next scan and sent to the
// store; every other server writes it into its own copy at its next look and is told which file changed, so a running
// channel restarts with it. A deletion travels the same way. When the same file changes on two servers between looks, the
// one that reached the store first wins, and the other's version is kept beside it as NAME.conflict-NODE for a person
// to compare.
type Files struct {
	db    *sql.DB
	node  string
	areas []Area
	log   *slog.Logger
	// Changed is told which files another server changed here (area, slash-separated path, deleted).
	Changed func(area, rel string, deleted bool)

	mu      sync.Mutex
	synced  map[string]string // area/rel -> sha last agreed with the store ("" for deleted)
	version int64             // the highest store version written here
	cache   map[string]fileStamp
}

// Area is a directory, or some files in one, kept the same everywhere.
type Area struct {
	Name string
	Dir  string
	// Keep chooses the files, by slash-separated path within Dir. Nil keeps every regular file.
	Keep func(rel string) bool
}

type fileStamp struct {
	size int64
	mod  time.Time
	sha  string
}

// MaxFileSize bounds one replicated file: these are configuration, and a bigger one is a mistake (a database copied into
// the channels directory) that should not be shipped to every server.
const MaxFileSize = 8 << 20

// NewFiles prepares the table.
func NewFiles(ctx context.Context, db *sql.DB, node string, areas []Area, log *slog.Logger) (*Files, error) {
	if log == nil {
		log = slog.Default()
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cluster_files (area TEXT NOT NULL, path TEXT NOT NULL,
		content BYTEA, sha TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, version BIGINT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL, updated_by TEXT NOT NULL, PRIMARY KEY (area, path))`); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS cluster_files_version`); err != nil {
		return nil, err
	}
	return &Files{db: db, node: node, areas: areas, log: log, synced: map[string]string{}, cache: map[string]fileStamp{}}, nil
}

// Run syncs every interval until ctx ends.
func (f *Files) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := f.Sync(ctx); err != nil && ctx.Err() == nil {
			f.log.Warn("cluster: the shared configuration could not be synchronised; trying again", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sync takes what others changed, then sends what changed here.
func (f *Files) Sync(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.pull(ctx); err != nil {
		return err
	}
	return f.push(ctx)
}

func key(area, rel string) string { return area + "/" + rel }

func (f *Files) area(name string) (Area, bool) {
	for _, a := range f.areas {
		if a.Name == name {
			return a, true
		}
	}
	return Area{}, false
}

// pull writes what the store has that is newer than what was last written here.
func (f *Files) pull(ctx context.Context) error {
	rows, err := f.db.QueryContext(ctx, `SELECT area, path, content, sha, deleted, version, updated_by FROM cluster_files
		WHERE version > ? ORDER BY version`, f.version)
	if err != nil {
		return err
	}
	type change struct {
		area, rel, sha, by string
		content            []byte
		deleted            bool
		version            int64
	}
	var changes []change
	for rows.Next() {
		var c change
		var deleted int64
		if err := rows.Scan(&c.area, &c.rel, &c.content, &c.sha, &deleted, &c.version, &c.by); err != nil {
			rows.Close()
			return err
		}
		c.deleted = deleted != 0
		changes = append(changes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range changes {
		f.version = max(f.version, c.version)
		a, ok := f.area(c.area)
		if !ok || !safeRel(c.rel) || (a.Keep != nil && !a.Keep(c.rel)) {
			continue
		}
		full := filepath.Join(a.Dir, filepath.FromSlash(c.rel))
		local, _ := f.hash(full)
		k := key(c.area, c.rel)
		if local == c.sha || (c.deleted && local == "") {
			f.synced[k] = c.sha
			if c.deleted {
				f.synced[k] = ""
			}
			continue
		}
		// Changed here too since the last agreement: keep this server's version beside the one that wins.
		if last, known := f.synced[k]; local != "" && (!known || local != last) && c.by != f.node {
			if b, err := os.ReadFile(full); err == nil {
				aside := full + ".conflict-" + f.node
				_ = os.WriteFile(aside, b, 0o600)
				f.log.Warn("cluster: a file was changed here and on another server; the other server's version is kept and this "+
					"server's is beside it", "file", full, "kept", aside, "by", c.by)
			}
		}
		if c.deleted {
			if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			f.synced[k] = ""
		} else {
			if err := writeAtomic(full, c.content); err != nil {
				return err
			}
			f.synced[k] = c.sha
		}
		delete(f.cache, full)
		if f.Changed != nil && c.by != f.node {
			f.Changed(c.area, c.rel, c.deleted)
		}
	}
	return nil
}

// push sends every file that differs from what was last agreed, and every deletion.
func (f *Files) push(ctx context.Context) error {
	present := map[string]bool{}
	for _, a := range f.areas {
		files, err := f.list(a)
		if err != nil {
			return err
		}
		for _, rel := range files {
			k := key(a.Name, rel)
			present[k] = true
			full := filepath.Join(a.Dir, filepath.FromSlash(rel))
			sha, err := f.hash(full)
			if err != nil || sha == "" {
				continue
			}
			if last, ok := f.synced[k]; ok && last == sha {
				continue
			}
			content, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			if err := f.send(ctx, a.Name, rel, content, sha, false); err != nil {
				return err
			}
		}
	}
	// Deletions: files agreed with the store that are gone here.
	for k, sha := range f.synced {
		if sha == "" || present[k] {
			continue
		}
		area, rel, _ := strings.Cut(k, "/")
		if err := f.send(ctx, area, rel, nil, "", true); err != nil {
			return err
		}
	}
	return nil
}

func (f *Files) send(ctx context.Context, area, rel string, content []byte, sha string, deleted bool) error {
	d := 0
	if deleted {
		d = 1
	}
	var version int64
	err := f.db.QueryRowContext(ctx, `INSERT INTO cluster_files (area, path, content, sha, deleted, version, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, nextval('cluster_files_version'), now(), ?)
		ON CONFLICT (area, path) DO UPDATE SET content = excluded.content, sha = excluded.sha, deleted = excluded.deleted,
			version = excluded.version, updated_at = excluded.updated_at, updated_by = excluded.updated_by
		WHERE cluster_files.sha <> excluded.sha OR cluster_files.deleted <> excluded.deleted
		RETURNING version`, area, rel, content, sha, d, f.node).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil // the store already had it
	}
	if err != nil {
		return err
	}
	f.synced[key(area, rel)] = sha
	if version > 0 {
		f.log.Info("cluster: shared a configuration change", "area", area, "file", rel, "deleted", deleted)
		// Not f.version = version: a version between the last pulled and this one may be another server's change, still
		// to be read.
	}
	return nil
}

// list names an area's files, slash-separated, skipping hidden files, temporary files and conflict copies.
func (f *Files) list(a Area) ([]string, error) {
	var out []string
	err := filepath.WalkDir(a.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		name := d.Name()
		if p != a.Dir && (strings.HasPrefix(name, ".") || name == "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".swp") ||
			strings.Contains(name, ".conflict-") {
			return nil
		}
		rel, err := filepath.Rel(a.Dir, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if a.Keep != nil && !a.Keep(rel) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > MaxFileSize {
			f.log.Warn("cluster: a file too large to share was left out", "file", p, "size", info.Size())
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// hash is a file's SHA-256, "" when it does not exist, cached by size and modification time.
func (f *Files) hash(full string) (string, error) {
	info, err := os.Stat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if c, ok := f.cache[full]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c.sha, nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	f.cache[full] = fileStamp{size: info.Size(), mod: info.ModTime(), sha: sha}
	return sha, nil
}

// safeRel refuses a path from the store that would land outside its directory.
func safeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	clean := path.Clean(rel)
	return clean == rel && clean != ".." && !strings.HasPrefix(clean, "../")
}

func writeAtomic(full string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".cluster-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cluster: writing %s: %w", full, err)
	}
	return nil
}
