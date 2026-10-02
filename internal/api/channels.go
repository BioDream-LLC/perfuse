package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/config"
)

// ChannelRepo reads and writes channel definitions as files.
//
// This is the whole point of the design: the browser edits files. A channel built
// by clicking and a channel written by hand are the same artifact, so git holds
// the history, a diff shows what changed, and exporting to share is just handing
// someone the file.
type ChannelRepo struct {
	// Dir is where channel files live.
	Dir string

	// mu serialises writes. Two editors saving the same channel at once would
	// otherwise interleave and produce a file that is neither version.
	mu sync.Mutex
}

// Repository errors.
var (
	// ErrChannelNotFound means no channel of that name exists.
	ErrChannelNotFound = errors.New("channel not found")
	// ErrChannelExists means a channel of that name already exists.
	ErrChannelExists = errors.New("a channel with that name already exists")
	// ErrInvalidName means the name cannot be used as a filename.
	ErrInvalidName = errors.New("invalid channel name")
)

// ValidationFailure carries every problem found in a definition, so the front end
// can show them all at once.
type ValidationFailure struct {
	Problems []string
}

func (e *ValidationFailure) Error() string {
	return "invalid channel definition: " + strings.Join(e.Problems, "; ")
}

// NewChannelRepo prepares a repository, creating the directory if needed.
func NewChannelRepo(dir string) (*ChannelRepo, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &ChannelRepo{Dir: dir}, nil
}

// ChannelSummary is the listing view.
type ChannelSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Group is what this channel is filed under. Empty means ungrouped, which is a legitimate answer
	// and not a missing value - a site with nine channels should not be made to invent a taxonomy.
	Group        string   `json:"group,omitempty"`
	Enabled      bool     `json:"enabled"`
	Listen       string   `json:"listen"`
	SourceType   string   `json:"sourceType"`
	AckWhen      string   `json:"ackWhen"`
	Filter       string   `json:"filter,omitempty"`
	Destinations []string `json:"destinations"`
	Reads        []string `json:"reads,omitempty"`
	File         string   `json:"file"`
}

// List returns every channel, valid or not.
//
// An invalid file is reported rather than hidden: a channel that will not load is
// exactly what an operator needs to see, and omitting it would make a broken file
// look like a missing one.
func (r *ChannelRepo) List() (valid []ChannelSummary, broken map[string]string, err error) {
	paths, err := r.files()
	if err != nil {
		return nil, nil, err
	}

	broken = map[string]string{}
	for _, path := range paths {
		c, loadErr := config.LoadFile(path)
		if loadErr != nil {
			broken[filepath.Base(path)] = loadErr.Error()
			continue
		}
		valid = append(valid, summarise(c, path))
	}

	sort.Slice(valid, func(i, j int) bool { return valid[i].Name < valid[j].Name })
	return valid, broken, nil
}

func summarise(c *config.Channel, path string) ChannelSummary {
	s := ChannelSummary{
		Name:        c.Name,
		Description: c.Description,
		Group:       c.Group,
		Enabled:     c.IsEnabled(),
		Listen:      c.Source.Listen,
		SourceType:  string(c.Source.Type),
		AckWhen:     string(c.Source.AckWhen()),
		Filter:      c.Filter,
		Reads:       c.Paths(),
		File:        filepath.Base(path),
	}
	for _, d := range c.Destinations {
		s.Destinations = append(s.Destinations, d.Name)
	}
	return s
}

// Get returns one channel definition as the generic map the front end edits,
// along with the raw YAML.
func (r *ChannelRepo) Get(name string) (*config.Channel, error) {
	path, err := r.pathFor(name)
	if err != nil {
		return nil, err
	}
	c, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// RawYAML returns the file contents, for the export button and the advanced
// editor.
func (r *ChannelRepo) RawYAML(name string) ([]byte, error) {
	path, err := r.pathFor(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Validate parses and checks a definition without saving it, so the front end can
// tell someone what is wrong before they commit to it.
func (r *ChannelRepo) Validate(raw []byte) (*config.Channel, error) {
	c, err := config.Load(strings.NewReader(string(raw)), "(unsaved)")
	if err != nil {
		return nil, &ValidationFailure{Problems: splitProblems(err)}
	}
	return c, nil
}

// Create writes a new channel. It refuses to overwrite an existing one.
func (r *ChannelRepo) Create(raw []byte) (*config.Channel, error) {
	c, err := r.Validate(raw)
	if err != nil {
		return nil, err
	}

	filename, err := filenameFor(c.Name)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.assertNameFree(c.Name, ""); err != nil {
		return nil, err
	}

	path := filepath.Join(r.Dir, filename)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrChannelExists, filename)
	}

	if err := writeFileAtomic(path, raw); err != nil {
		return nil, err
	}
	return c, nil
}

// Update replaces an existing channel.
//
// Renaming is allowed: the file is written under the new name and the old one
// removed, so the filename keeps matching the channel name.
func (r *ChannelRepo) Update(name string, raw []byte) (*config.Channel, error) {
	c, err := r.Validate(raw)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	oldPath, err := r.pathFor(name)
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(c.Name, name) {
		if err := r.assertNameFree(c.Name, name); err != nil {
			return nil, err
		}
	}

	newFilename, err := filenameFor(c.Name)
	if err != nil {
		return nil, err
	}
	newPath := filepath.Join(r.Dir, newFilename)

	if err := writeFileAtomic(newPath, raw); err != nil {
		return nil, err
	}
	if newPath != oldPath {
		if err := os.Remove(oldPath); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Delete removes a channel file.
func (r *ChannelRepo) Delete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	path, err := r.pathFor(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// assertNameFree checks no other file already defines this channel name. Two
// channels sharing a name make logs and metrics ambiguous, so it is refused here
// rather than at startup.
func (r *ChannelRepo) assertNameFree(name, ignoring string) error {
	valid, _, err := r.List()
	if err != nil {
		return err
	}
	for _, c := range valid {
		if strings.EqualFold(c.Name, name) && !strings.EqualFold(c.Name, ignoring) {
			return fmt.Errorf("%w: %q is defined in %s", ErrChannelExists, c.Name, c.File)
		}
	}
	return nil
}

// pathFor finds the file defining a channel, matching on the name inside the file
// rather than assuming the filename.
// PathFor returns the file backing a channel. Exported for the history handlers,
// which need the filename rather than the parsed channel.
func (r *ChannelRepo) PathFor(name string) (string, error) { return r.pathFor(name) }

// TableFiles lists the shared mapping table files in the channels directory.
//
// Named by convention rather than by being referenced, so a table nobody uses yet is still found. Without this
// the only way to discover a table was through a channel that loaded it, which made a newly created one
// invisible in the section that had just created it.
func (r *ChannelRepo) TableFiles() []string {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		// The convention the loader and the documentation already use. Not guessed: config.go treats
		// .codeset.yaml as a companion file so it is never mistaken for a channel definition.
		if strings.HasSuffix(name, ".codeset.yaml") || strings.HasSuffix(name, ".codeset.yml") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ReadTables loads a shared mapping table file, returning an empty set when it does not exist yet.
//
// Absent is not an error here: the first table written to a server creates the file, and treating "no file" as a
// failure would make creating the first one impossible through the same path that creates the second.
func (r *ChannelRepo) ReadTables(file string) (*codeset.Set, error) {
	if err := safeSidecarName(file); err != nil {
		return nil, err
	}

	path := filepath.Join(r.Dir, file)
	raw, err := os.ReadFile(path) // #nosec G304 - a name this repository has just validated
	if err != nil {
		if os.IsNotExist(err) {
			return &codeset.Set{Tables: []codeset.Table{}}, nil
		}
		return nil, err
	}

	var set codeset.Set
	if err := yaml.Unmarshal(raw, &set); err != nil {
		// Refused rather than silently overwritten. Somebody's hand-written tables file that this cannot parse
		// is still their file, and replacing it with one table would destroy the rest.
		return nil, fmt.Errorf("%s cannot be read, so it will not be overwritten: %w", file, err)
	}
	if set.Tables == nil {
		set.Tables = []codeset.Table{}
	}
	return &set, nil
}

// WriteTables replaces a shared mapping table file.
func (r *ChannelRepo) WriteTables(file string, body []byte) error {
	if err := safeSidecarName(file); err != nil {
		return err
	}

	// Parsed before landing. Unlike a channel file there are no relative references inside a tables file, so
	// checking the bytes is sufficient here - and a tables file that will not load takes every channel using it
	// down with it.
	var check codeset.Set
	if err := yaml.Unmarshal(body, &check); err != nil {
		return fmt.Errorf("%w: that is not a tables file: %v", ErrInvalidName, err)
	}

	path := filepath.Join(r.Dir, file)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// safeSidecarName refuses anything that would write outside the channels directory.
//
// The name arrives in a request. A separator or a parent reference would put a file wherever the caller liked,
// which for a process that may run as a service is a way to overwrite something that matters.
func safeSidecarName(file string) error {
	if file == "" {
		return fmt.Errorf("%w: a companion file needs a name", ErrInvalidName)
	}
	if file != filepath.Base(file) || strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") {
		return fmt.Errorf("%w: %q must be a plain file name, with no directories in it", ErrInvalidName, file)
	}
	if !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
		return fmt.Errorf("%w: %q must end in .yaml", ErrInvalidName, file)
	}
	return nil
}

// WriteSidecar writes a companion file beside a channel definition.
//
// Contracts, code sets and the like live next to the channel that references them, because a channel and the
// things it depends on travel together: copying one file to another server and leaving its contract behind
// produces a channel that will not load at all, which is a confusing way to learn about a missing file.
//
// The name is deliberately restricted rather than trusted. It arrives from a request, and a path with a
// separator or a parent reference in it would write outside the channels directory - which for a process that
// may be running as a service is a way to overwrite something that matters. Only a plain file name is allowed.
func (r *ChannelRepo) WriteSidecar(channel, file string, body []byte) error {
	if err := safeSidecarName(file); err != nil {
		return err
	}

	channelPath, err := r.pathFor(channel)
	if err != nil {
		return err
	}

	target := filepath.Join(filepath.Dir(channelPath), file)

	// Written to a temporary file and moved into place.
	//
	// A half-written contract is worse than no contract: a channel referencing something unparseable is
	// refused wholesale, so an interrupted write does not corrupt one file, it takes the channel off the air.
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Save writes a channel definition back, keeping its existing file.
//
// Distinct from Update, which takes raw YAML from a person. This takes a definition the server has changed
// itself - attaching a contract that was just written, for instance - and re-serialises it.
func (r *ChannelRepo) Save(cfg *config.Channel) error {
	if cfg == nil {
		return fmt.Errorf("%w: nothing to save", ErrInvalidName)
	}

	path, err := r.pathFor(cfg.Name)
	if err != nil {
		return err
	}

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	// Validated after landing, not before, and rolled back if it fails.
	//
	// Validating the bytes on their own does not work: a channel may reference companion files by relative
	// path, and those resolve against the channel file's directory. Checked in isolation there is no such
	// directory, so a perfectly good contract reference fails with "no such file or directory" - which is what
	// this code did on its first outing.
	//
	// So the file is written, loaded from where it actually lives, and put back if that load fails. An invalid
	// channel file does not warn: Perfuse refuses a channel wholesale when anything it references is broken,
	// so the channel would vanish from the interface entirely.
	previous, readErr := os.ReadFile(path) // #nosec G304 - a path this repository already owns

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if _, err := config.LoadFile(path); err != nil {
		// Put back what was there. If the original could not be read there is nothing to restore, and saying
		// so is better than leaving a broken file with no explanation.
		if readErr == nil {
			if restoreErr := os.WriteFile(path, previous, 0o600); restoreErr != nil {
				return fmt.Errorf("%s is now invalid and could not be restored: %w (original error: %v)",
					filepath.Base(path), restoreErr, err)
			}
			return fmt.Errorf("that change would make the channel invalid, so it was not kept: %w", err)
		}
		return fmt.Errorf("the saved channel is not valid: %w", err)
	}

	return nil
}

func (r *ChannelRepo) pathFor(name string) (string, error) {
	paths, err := r.files()
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		c, err := config.LoadFile(path)
		if err != nil {
			continue
		}
		if strings.EqualFold(c.Name, name) {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrChannelNotFound, name)
}

func (r *ChannelRepo) files() ([]string, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if companionFile(e.Name()) {
			continue
		}

		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
			out = append(out, filepath.Join(r.Dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// companionSuffixes are files that live beside the channels and are not channels.
//
// A mapping table is documented as living in a file beside the channels, because that is how it travels with them into version control
// and into a container image. The loader read every .yaml in the directory and tried to parse each one as a channel, so following the
// documented layout produced a broken channel.
//
// This was invisible until channel load failures started being reported, at which point an installation doing exactly the right thing
// got a warning at startup and a non-zero broken-channels gauge. A false alarm is worse than no alarm: it teaches an operator that the
// warning means nothing, and the next one will be real.
//
// Matched on suffix rather than on content. Deciding by whether a file happens to parse as a channel would mean a genuinely broken
// channel file was quietly reclassified as something else, which is the fault this is meant to prevent.
var companionSuffixes = []string{".codeset.yaml", ".codeset.yml"}

// companionFile reports whether a filename is a companion rather than a channel.
func companionFile(name string) bool {
	lower := strings.ToLower(name)

	for _, suffix := range companionSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}

	return false
}

// filenameFor derives a filename from a channel name.
//
// The name comes from a browser, so it must not be able to choose a path. Only
// letters, digits, dashes and underscores survive.
func filenameFor(name string) (string, error) {
	cleaned := make([]byte, 0, len(name))
	for i := 0; i < len(name) && len(cleaned) < 64; i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			cleaned = append(cleaned, c)
		case c >= 'A' && c <= 'Z':
			cleaned = append(cleaned, c+('a'-'A'))
		case c == ' ' || c == '.':
			cleaned = append(cleaned, '-')
		}
	}
	trimmed := strings.Trim(string(cleaned), "-_")
	if trimmed == "" {
		return "", fmt.Errorf("%w: %q has no usable characters for a filename", ErrInvalidName, name)
	}

	// Windows device names, which are reserved whatever extension follows them.
	//
	// A channel called con would be written to con.yaml, and on Windows that is the console device rather than a
	// file: the write succeeds, the bytes go nowhere, and reading it back finds nothing. Hospitals run Windows, so
	// this is a real name for somebody to choose - aux and prn especially, which read as ordinary abbreviations.
	//
	// Suffixed rather than refused, because the channel name itself is perfectly legitimate and only the filename is
	// a problem. The name in the file is unchanged.
	if isWindowsDeviceName(trimmed) {
		trimmed += "-channel"
	}

	return trimmed + ".yaml", nil
}

// isWindowsDeviceName reports whether a base filename is reserved by Windows.
//
// Checked on every platform rather than only on Windows, so that a channel directory created on Linux can still be
// opened by a Windows installation. A name that works on one and not the other is worse than one that works on neither.
func isWindowsDeviceName(name string) bool {
	switch strings.ToLower(name) {
	case "con", "prn", "aux", "nul":
		return true
	}

	// COM1 to COM9 and LPT1 to LPT9. COM0 and LPT0 are not reserved.
	if len(name) == 4 {
		prefix := strings.ToLower(name[:3])
		digit := name[3]
		if (prefix == "com" || prefix == "lpt") && digit >= '1' && digit <= '9' {
			return true
		}
	}

	return false
}

// writeFileAtomic writes via a temporary file and a rename, so a crash or a full
// disk cannot leave a half-written channel that fails to load.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".perfuse-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	defer func() {
		// Best effort cleanup if anything below failed.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// MarshalChannel renders a channel definition as YAML for the front end to save.
func MarshalChannel(v any) ([]byte, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// splitProblems turns a multi-line validation error into one entry per problem.
func splitProblems(err error) []string {
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = []string{err.Error()}
	}
	return out
}
