package sqlitedb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// PostgreSQL is the store a cluster shares: two or more servers pointed at one database, so that whichever is running a
// channel finds the queue, the messages and the accounts the others left.
//
// Every store in Perfuse was written for SQLite, and most of what they say is standard SQL. The rest is translated here,
// once, at the connection, so the stores keep one text of each statement instead of two that drift:
//
//   - ? placeholders become $1, $2 ...;
//   - INSERT OR IGNORE becomes INSERT ... ON CONFLICT DO NOTHING;
//   - LIKE becomes ILIKE, because SQLite's LIKE ignores case and PostgreSQL's does not, and a search that found "Smith"
//     yesterday must find it on the cluster;
//   - IFNULL becomes COALESCE;
//   - in table definitions, INTEGER becomes BIGINT (PostgreSQL's INTEGER is 32 bits, and a millisecond clock is not),
//     INTEGER PRIMARY KEY a BIGSERIAL, BLOB BYTEA, REAL DOUBLE PRECISION, and COLLATE NOCASE and WITHOUT ROWID go;
//   - a Go bool is written as 1 or 0 and a time.Time in the fixed-width text form every store compares (dbtime), as
//     SQLite stored them.
//
// What cannot be translated without knowing the table (INSERT OR REPLACE, LastInsertId, GLOB, strftime) is written so
// both databases read it the same way, at the few places it occurs; IsPostgres tells a store which it is talking to.

// IsPostgresURL reports whether a database target is a PostgreSQL URL rather than a file.
func IsPostgresURL(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	return strings.HasPrefix(t, "postgres://") || strings.HasPrefix(t, "postgresql://")
}

// IsPostgres reports whether db is a PostgreSQL pool opened here.
func IsPostgres(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(pgDriver)
	return ok
}

// pgDriver is the translating driver. Its Open is used only when a pool is opened by name; Open below uses a connector.
type pgDriver struct{}

func (pgDriver) Open(name string) (driver.Conn, error) {
	c, err := pq.NewConnector(name)
	if err != nil {
		return nil, err
	}
	conn, err := c.Connect(context.Background())
	if err != nil {
		return nil, err
	}
	return &pgConn{Conn: conn}, nil
}

type pgConnector struct{ base *pq.Connector }

func (c pgConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pgConn{Conn: conn}, nil
}

func (pgConnector) Driver() driver.Driver { return pgDriver{} }

// openPostgres opens one pool for a PostgreSQL URL. Reads and writes share it: PostgreSQL runs them side by side, which is
// the reason SQLite needed two pools.
func openPostgres(target string, readers int) (*DB, error) {
	c, err := pq.NewConnector(target)
	if err != nil {
		return nil, fmt.Errorf("sqlitedb: %s: %w", RedactURL(target), err)
	}
	db := sql.OpenDB(pgConnector{base: c})
	db.SetMaxOpenConns(max(4, readers*2))
	db.SetMaxIdleConns(max(2, readers))
	if testing.Testing() {
		// A test suite opens hundreds of stores and closes few of them.
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(1)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitedb: cannot reach %s: %w", RedactURL(target), err)
	}
	if err := pgPrepare(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitedb: %s: preparing the database (the account needs CREATE on it, or an administrator runs "+
			"CREATE EXTENSION citext once): %w", RedactURL(target), err)
	}
	return &DB{Write: db, Read: db, Path: RedactURL(target), memory: true}, nil
}

// RedactURL hides the password in a database URL, for logs and error messages.
func RedactURL(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return "postgres://…"
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	q := u.Query()
	if q.Has("password") {
		q.Set("password", "xxxxx")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// pgConn translates each statement before lib/pq sees it.
type pgConn struct{ driver.Conn }

func (c *pgConn) Prepare(query string) (driver.Stmt, error) { return c.Conn.Prepare(Translate(query)) }

func (c *pgConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, Translate(query))
	}
	return c.Conn.Prepare(Translate(query))
}

func (c *pgConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	q := Translate(query)
	// LastInsertId, which lib/pq does not offer: an INSERT into a table with a serial id is run with RETURNING id, so a
	// store written for SQLite gets the new row's id the way it always has.
	if table := insertTable(q); table != "" && !reReturning.MatchString(q) {
		if col := c.serialID(ctx, table); col != "" {
			return c.execReturning(ctx, query, col, args)
		}
	}
	return e.ExecContext(ctx, q, args)
}

func (c *pgConn) execReturning(ctx context.Context, query, col string, args []driver.NamedValue) (driver.Result, error) {
	{
		rows, err := c.QueryContext(ctx, query+" RETURNING "+col, args)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var res pgResult
		dest := make([]driver.Value, 1)
		for {
			if err := rows.Next(dest); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			res.rows++
			if id, ok := dest[0].(int64); ok {
				res.id = id
			}
		}
		return res, nil
	}
}

type pgResult struct{ id, rows int64 }

func (r pgResult) LastInsertId() (int64, error) { return r.id, nil }
func (r pgResult) RowsAffected() (int64, error) { return r.rows, nil }

var (
	reInsertInto = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\b`)
	serialIDs    sync.Map // table -> the serial column, "" for none
)

func insertTable(q string) string {
	if m := reInsertInto.FindStringSubmatch(q); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// serialID names a table's serial column (id, or the rowid added above), asked once per table; "" for none.
func (c *pgConn) serialID(ctx context.Context, table string) string {
	if v, ok := serialIDs.Load(table); ok {
		return v.(string)
	}
	qc, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return ""
	}
	rows, err := qc.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema()
		AND table_name = $1 AND column_name IN ('id', 'rowid') AND column_default LIKE 'nextval(%' ORDER BY column_name`,
		[]driver.NamedValue{{Ordinal: 1, Value: table}})
	if err != nil {
		return ""
	}
	dest := make([]driver.Value, 1)
	col := ""
	if rows.Next(dest) == nil {
		switch v := dest[0].(type) {
		case string:
			col = v
		case []byte:
			col = string(v)
		}
	}
	_ = rows.Close()
	serialIDs.Store(table, col)
	return col
}

func (c *pgConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, Translate(query), args)
}

func (c *pgConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // the fallback database/sql itself uses
}

func (c *pgConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *pgConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *pgConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// CheckNamedValue writes values the way SQLite stored them, so a column means the same on both.
func (c *pgConn) CheckNamedValue(nv *driver.NamedValue) error {
	switch v := nv.Value.(type) {
	case bool:
		if v {
			nv.Value = int64(1)
		} else {
			nv.Value = int64(0)
		}
		return nil
	case time.Time:
		nv.Value = dbtime.Format(v)
		return nil
	case *time.Time:
		if v == nil {
			nv.Value = nil
		} else {
			nv.Value = dbtime.Format(*v)
		}
		return nil
	}
	return driver.ErrSkip
}

var (
	translations sync.Map // query -> translated query

	reInsertIgnore = regexp.MustCompile(`(?i)\bINSERT\s+OR\s+IGNORE\s+INTO\b`)
	reIfNull       = regexp.MustCompile(`(?i)\bIFNULL\s*\(`)
	reInstr        = regexp.MustCompile(`(?i)\binstr\s*\(`)
	reChar         = regexp.MustCompile(`(?i)\bchar\s*\(`)
	reBlobText     = regexp.MustCompile(`(?i)\bCAST\s*\(\s*(raw|payload)\s+AS\s+TEXT\s*\)`)
	reBlobLike     = regexp.MustCompile(`(?i)(^|[^a-z0-9_.(])(raw|payload)\s+(NOT\s+)?ILIKE\b`)
	rePlusZero     = regexp.MustCompile(`\b([a-z_][a-z0-9_]*)\+0\b`)
	reLike         = regexp.MustCompile(`(?i)\bLIKE\b`)
	reReturning    = regexp.MustCompile(`(?i)\bRETURNING\b`)
	reDDL          = regexp.MustCompile(`(?i)^\s*(CREATE\s+(TABLE|INDEX|UNIQUE\s+INDEX|TEMP|TEMPORARY)|ALTER\s+TABLE)\b`)
	reSerial       = regexp.MustCompile(`(?i)\bINTEGER\s+PRIMARY\s+KEY(\s+AUTOINCREMENT)?\b`)
	reInteger      = regexp.MustCompile(`(?i)\bINTEGER\b`)
	reReal         = regexp.MustCompile(`(?i)\bREAL\b`)
	reBlob         = regexp.MustCompile(`(?i)\bBLOB\b`)
	reNoCase       = regexp.MustCompile(`(?i)\s*\bCOLLATE\s+NOCASE\b`)
	reWithoutRowid = regexp.MustCompile(`(?i)\)\s*WITHOUT\s+ROWID\s*$`)
	reAutoinc      = regexp.MustCompile(`(?i)\s*\bAUTOINCREMENT\b`)
	reCIText       = regexp.MustCompile(`(?i)\bTEXT\b([^,()]*?)\s*\bCOLLATE\s+NOCASE\b`)
	reNow          = regexp.MustCompile(`(?i)\bdatetime\s*\(\s*'now'\s*\)`)
	rePragmaFK     = regexp.MustCompile(`(?i)^\s*PRAGMA\s+foreign_keys\b`)
	reTableInfo    = regexp.MustCompile(`(?i)\bpragma_table_info\s*\(\s*('[a-z0-9_]+'|\?)\s*\)`)
	reEpoch        = regexp.MustCompile(`(?i)\bstrftime\s*\(\s*'%s'\s*,\s*([a-z_][a-z0-9_.]*)\s*\)`)
	reCastInteger  = regexp.MustCompile(`(?i)\bAS\s+INTEGER\s*\)`)
	reCreateTable  = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\b`)
	rePrimaryKey   = regexp.MustCompile(`(?i)\bPRIMARY\s+KEY\b`)
)

// The tables an index's queries name are case-insensitive by CITEXT, the extension PostgreSQL ships for exactly this:
// COLLATE NOCASE columns (usernames, tenant names) keep their meaning, unique and equal regardless of case.
//
// Two functions stand in for what SQLite does by itself: perfuse_text reads a BLOB as text (UTF-8, else Latin-1, else
// escaped), where SQLite's CAST(raw AS TEXT) reads the bytes as they are, and PostgreSQL's would print them in hex, so a
// payload search would find nothing; perfuse_num reads the leading number of a text, as SQLite's x+0 does.
var pgSetup = []string{
	`CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`,
	`CREATE OR REPLACE FUNCTION public.perfuse_text(b bytea) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $f$
	BEGIN
		BEGIN RETURN convert_from(b, 'UTF8'); EXCEPTION WHEN others THEN NULL; END;
		BEGIN RETURN convert_from(b, 'LATIN1'); EXCEPTION WHEN others THEN NULL; END;
		RETURN encode(b, 'escape');
	END $f$`,
	`CREATE OR REPLACE FUNCTION public.perfuse_num(t text) RETURNS numeric LANGUAGE sql IMMUTABLE AS $f$
		SELECT COALESCE(CAST(NULLIF(substring(t from '^\s*[-+]?[0-9]*\.?[0-9]+'), '') AS numeric), 0)
	$f$`,
}

// Translate rewrites one SQLite statement for PostgreSQL. Literals, quoted identifiers and comments are left alone.
func Translate(query string) string {
	if v, ok := translations.Load(query); ok {
		return v.(string)
	}
	out := translate(query)
	translations.Store(query, out)
	return out
}

func translate(query string) string {
	query = reNow.ReplaceAllString(query, `to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')`)
	// A table's columns, as SQLite's table-valued pragma lists them.
	query = reTableInfo.ReplaceAllString(query,
		`(SELECT column_name AS name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1) AS pragma_columns`)
	// Seconds since 1970 of a stored timestamp, which every store writes as RFC 3339 text.
	query = reEpoch.ReplaceAllString(query, `CAST(EXTRACT(EPOCH FROM CAST($1 AS TIMESTAMPTZ)) AS BIGINT)`)
	type segment struct {
		code bool
		text string
	}
	var segs []segment
	var b strings.Builder
	flush := func(code bool) {
		if b.Len() > 0 {
			segs = append(segs, segment{code, b.String()})
			b.Reset()
		}
	}
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\'' || ch == '"':
			flush(true)
			j := i + 1
			for j < len(query) {
				if query[j] == ch {
					if j+1 < len(query) && query[j+1] == ch {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(query) {
				j = len(query) - 1
			}
			segs = append(segs, segment{false, query[i : j+1]})
			i = j
		case ch == '-' && i+1 < len(query) && query[i+1] == '-':
			flush(true)
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = len(query) - i
			}
			segs = append(segs, segment{false, query[i : i+j]})
			i += j - 1
		case ch == '/' && i+1 < len(query) && query[i+1] == '*':
			flush(true)
			j := strings.Index(query[i+2:], "*/")
			end := len(query)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			segs = append(segs, segment{false, query[i:end]})
			i = end - 1
		default:
			b.WriteByte(ch)
		}
	}
	flush(true)

	var code strings.Builder
	for _, s := range segs {
		if s.code {
			code.WriteString(s.text)
		}
	}
	ddl := reDDL.MatchString(code.String())
	ignore := reInsertIgnore.MatchString(code.String())

	if rePragmaFK.MatchString(query) {
		// PostgreSQL always enforces foreign keys; SQLite needs asking, per connection.
		return `SELECT 1`
	}
	n := 0
	var out strings.Builder
	for _, s := range segs {
		if !s.code {
			out.WriteString(s.text)
			continue
		}
		t := s.text
		if ignore {
			t = reInsertIgnore.ReplaceAllString(t, "INSERT INTO")
		}
		t = reIfNull.ReplaceAllString(t, "COALESCE(")
		t = reInstr.ReplaceAllString(t, "strpos(")
		if !ddl {
			t = reChar.ReplaceAllString(t, "chr(")
			// raw and payload are the BLOB columns (messages, queue, payloads) a search reads as text.
			t = reBlobText.ReplaceAllString(t, "perfuse_text($1)")
			t = rePlusZero.ReplaceAllString(t, "perfuse_num($1)")
		}
		t = reNow.ReplaceAllString(t, `to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')`)
		t = reLike.ReplaceAllString(t, "ILIKE")
		if !ddl {
			t = reBlobLike.ReplaceAllString(t, "${1}perfuse_text($2) ${3}ILIKE")
			// In a query, COLLATE NOCASE compares a column that is CITEXT on PostgreSQL, which already ignores case.
			t = reNoCase.ReplaceAllString(t, "")
			// SQLite's INTEGER is 64 bits, and so must the cast be: seconds and milliseconds outgrow 32.
			t = reCastInteger.ReplaceAllString(t, "AS BIGINT)")
		}
		if ddl {
			t = reSerial.ReplaceAllString(t, "BIGSERIAL PRIMARY KEY")
			t = reCIText.ReplaceAllString(t, "CITEXT$1")
			t = reInteger.ReplaceAllString(t, "BIGINT")
			t = reReal.ReplaceAllString(t, "DOUBLE PRECISION")
			t = reBlob.ReplaceAllString(t, "BYTEA")
			t = reNoCase.ReplaceAllString(t, "")
			t = reAutoinc.ReplaceAllString(t, "")
			t = reWithoutRowid.ReplaceAllString(t, ")")
		}
		var w strings.Builder
		for i := 0; i < len(t); i++ {
			if t[i] == '?' {
				n++
				fmt.Fprintf(&w, "$%d", n)
				continue
			}
			w.WriteByte(t[i])
		}
		out.WriteString(w.String())
	}
	res := out.String()
	// Every SQLite table without a declared key has a rowid, and a store may use it; on PostgreSQL it is a column.
	if reCreateTable.MatchString(res) && !rePrimaryKey.MatchString(res) {
		if i := strings.LastIndexByte(res, ')'); i > 0 {
			res = res[:i] + ", rowid BIGSERIAL" + res[i:]
		}
	}
	if ignore {
		trimmed := strings.TrimRight(strings.TrimSpace(res), ";")
		if loc := reReturning.FindStringIndex(trimmed); loc != nil {
			res = trimmed[:loc[0]] + "ON CONFLICT DO NOTHING " + trimmed[loc[0]:]
		} else {
			res = trimmed + " ON CONFLICT DO NOTHING"
		}
	}
	return res
}

// testSchema is where a test's database goes when the suite runs against PostgreSQL.
//
// PERFUSE_TEST_POSTGRES names a disposable database. With it set, every database a test opens by path, through Open,
// is a schema of its own there instead of a file, named after the path, so a test that closes and reopens its database
// finds its data again. The whole suite then runs against PostgreSQL without a test knowing. Never in a server: only a
// test binary (testing.Testing) reads the variable.
func testSchema(path string) (string, bool) {
	dsn := os.Getenv("PERFUSE_TEST_POSTGRES")
	if dsn == "" || !testing.Testing() || isMemory(path) || IsPostgresURL(path) {
		return "", false
	}
	abs, _ := filepath.Abs(strings.TrimPrefix(path, "file:"))
	sum := sha256.Sum256([]byte(abs))
	schema := "t_" + hex.EncodeToString(sum[:10])
	return withSearchPath(dsn, schema), true
}

// testMemory is a fresh PostgreSQL schema standing in for an in-memory database in a test binary with
// PERFUSE_TEST_POSTGRES set, or "".
func testMemory() string {
	if os.Getenv("PERFUSE_TEST_POSTGRES") == "" || !testing.Testing() {
		return ""
	}
	return TestURL("memory")
}

// TestURL is a fresh schema in PERFUSE_TEST_POSTGRES for a test that wants PostgreSQL without a path, or "" when the
// suite is not running against PostgreSQL.
func TestURL(name string) string {
	dsn := os.Getenv("PERFUSE_TEST_POSTGRES")
	if dsn == "" {
		return ""
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	_ = name
	target := withSearchPath(dsn, "t_"+hex.EncodeToString(b))
	if err := ensureSchema(target); err != nil {
		return ""
	}
	return target
}

func withSearchPath(dsn, schema string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	if !q.Has("sslmode") {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ensureSchema creates the schema a URL's search_path names, for a test database.
func ensureSchema(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	schema, _, _ := strings.Cut(u.Query().Get("search_path"), ",")
	if schema == "" {
		return nil
	}
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(schema) {
		return errors.New("sqlitedb: a test schema name must be lower case letters, digits and underscores")
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema)
	return err
}

// TestPostgres reports whether this is a test binary running the suite against PostgreSQL.
func TestPostgres() bool { return os.Getenv("PERFUSE_TEST_POSTGRES") != "" && testing.Testing() }

// pgPrepare installs the extension and functions, once per database: servers starting together take turns.
func pgPrepare(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regproc('public.perfuse_num') IS NOT NULL AND to_regtype('public.citext') IS NOT NULL`).Scan(&ok); err == nil && ok {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7212000)`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7212000)`) }()
	for _, q := range pgSetup {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// IsUniqueViolation reports whether err is a unique constraint refusing a row, from either database.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}
