package sqlitedb

import "testing"

func TestTranslate(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`SELECT a FROM t WHERE b = ? AND c = ?`, `SELECT a FROM t WHERE b = $1 AND c = $2`},
		{`SELECT '?' FROM t WHERE b = ?`, `SELECT '?' FROM t WHERE b = $1`},
		{`SELECT 'it''s ?' , "we?ird" FROM t WHERE b = ? -- a comment?
		AND c = ?`, `SELECT 'it''s ?' , "we?ird" FROM t WHERE b = $1 -- a comment?
		AND c = $2`},
		{`INSERT OR IGNORE INTO t (a) VALUES (?)`, `INSERT INTO t (a) VALUES ($1) ON CONFLICT DO NOTHING`},
		{`INSERT OR IGNORE INTO t (a) VALUES (?) RETURNING id`, `INSERT INTO t (a) VALUES ($1) ON CONFLICT DO NOTHING RETURNING id`},
		{`SELECT IFNULL(a, 0) FROM t WHERE name LIKE ? AND x NOT LIKE 'Like%'`, `SELECT COALESCE(a, 0) FROM t WHERE name ILIKE $1 AND x NOT ILIKE 'Like%'`},
		{`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY AUTOINCREMENT, n INTEGER NOT NULL DEFAULT 0, r REAL, b BLOB, name TEXT COLLATE NOCASE)`,
			`CREATE TABLE IF NOT EXISTS t (id BIGSERIAL PRIMARY KEY, n BIGINT NOT NULL DEFAULT 0, r DOUBLE PRECISION, b BYTEA, name CITEXT)`},
		{`CREATE TABLE k (a TEXT PRIMARY KEY, v INTEGER) WITHOUT ROWID`, `CREATE TABLE k (a TEXT PRIMARY KEY, v BIGINT)`},
		{`SELECT CAST(a AS INTEGER) FROM t`, `SELECT CAST(a AS BIGINT) FROM t`},
		{`SELECT COUNT(*) FROM pragma_table_info('fhir_search') WHERE name = 'x'`,
			`SELECT COUNT(*) FROM (SELECT column_name AS name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'fhir_search') AS pragma_columns WHERE name = 'x'`},
		{`SELECT name FROM pragma_table_info(?)`,
			`SELECT name FROM (SELECT column_name AS name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1) AS pragma_columns`},
		{`SELECT (CAST(strftime('%s', m.received_at) AS INTEGER) / ?) * ? FROM m`,
			`SELECT (CAST(CAST(EXTRACT(EPOCH FROM CAST(m.received_at AS TIMESTAMPTZ)) AS BIGINT) AS BIGINT) / $1) * $2 FROM m`},
		{`SELECT count(*) FROM messages WHERE raw LIKE '%x%' AND CAST(raw AS TEXT) LIKE ?`,
			`SELECT count(*) FROM messages WHERE perfuse_text(raw) ILIKE '%x%' AND perfuse_text(raw) ILIKE $1`},
		{`CREATE TABLE IF NOT EXISTS a (id TEXT NOT NULL, n INTEGER NOT NULL)`, `CREATE TABLE IF NOT EXISTS a (id TEXT NOT NULL, n BIGINT NOT NULL, rowid BIGSERIAL)`},
	} {
		if got := Translate(c.in); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	if got := RedactURL("postgres://perfuse:s3cret@db.example:5432/perfuse?sslmode=require"); got != "postgres://perfuse:xxxxx@db.example:5432/perfuse?sslmode=require" {
		t.Error(got)
	}
	if got := RedactURL("postgres://db.example/perfuse?password=s3cret"); got != "postgres://db.example/perfuse?password=xxxxx" {
		t.Error(got)
	}
}
