package store

import (
	"context"
	"testing"
)

// The migration has to work on a populated database, not just an empty one. An
// installation that upgrades in place and loses its logins is worse than one that
// refuses to start, because the operator finds out by being unable to sign in.

func TestMigrationIsIdempotent(t *testing.T) {
	// migrate runs on every open. Running it twice must be a no-op, or the second
	// start of any server fails.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := st.migrate(ctx); err != nil {
			t.Fatalf("migrate run %d: %v", i+2, err)
		}
	}
}

func TestSchemaVersionAdvances(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	got, err := st.schemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != len(migrations) {
		t.Errorf("schema version = %d, want %d", got, len(migrations))
	}
}

func TestSchemaVersionIsRecordedOnceNotAppended(t *testing.T) {
	// setSchemaVersion updates in place. If it inserted instead, schema_version
	// would gain a row per start and the LIMIT 1 read would eventually return a
	// stale version, re-running migrations against a schema that already has them.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := st.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var rows int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("schema_version has %d rows, want 1", rows)
	}
}

func TestExistingUsersSurviveTheTenancyMigration(t *testing.T) {
	// The upgrade path for every installation that exists today. The users table is
	// rebuilt rather than altered, because username was globally UNIQUE and SQLite
	// cannot drop that. A rebuild that dropped the rows would lock the operator out
	// of their own engine.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// Simulate the pre-tenancy state: rewind the version and put the old table
	// back, then let the migration run against real rows.
	if _, err := st.db.ExecContext(ctx, `DROP TABLE users`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TABLE users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL,
		role          TEXT    NOT NULL,
		disabled      INTEGER NOT NULL DEFAULT 0,
		created_at    TEXT    NOT NULL,
		last_login    TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, disabled, created_at)
		VALUES ('operator', 'hash-1', 'admin', 0, '2020-01-01T00:00:00Z'),
		       ('viewer-bob', 'hash-2', 'viewer', 1, '2021-06-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TABLE audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TABLE audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, user_id INTEGER,
		username TEXT NOT NULL, action TEXT NOT NULL, target TEXT, detail TEXT, ip TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO audit (at, username, action) VALUES ('2020-01-02T00:00:00Z','operator','login')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE schema_version SET version = 0`); err != nil {
		t.Fatal(err)
	}

	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrating a populated database: %v", err)
	}

	// Every user still there, with their role, disabled flag and id intact. The id
	// matters: sessions and audit rows reference it.
	type row struct {
		id       int64
		tenant   string
		username string
		hash     string
		role     string
		disabled int
	}
	rows, err := st.db.QueryContext(ctx, `SELECT id, tenant_id, username, password_hash, role, disabled FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.tenant, &r.username, &r.hash, &r.role, &r.disabled); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d users after migrating, want 2", len(got))
	}
	if got[0].username != "operator" || got[0].role != "admin" || got[0].hash != "hash-1" {
		t.Errorf("first user came through as %+v", got[0])
	}
	if got[1].disabled != 1 {
		t.Error("the disabled flag was lost")
	}
	for _, r := range got {
		// Assigned to the default tenant, not left null. A null tenant is a row
		// every scoped query silently excludes, which presents as "all my users
		// disappeared".
		if r.tenant != DefaultTenant {
			t.Errorf("user %q has tenant %q, want %q", r.username, r.tenant, DefaultTenant)
		}
	}
	if got[0].id >= got[1].id {
		t.Error("user ids were not preserved in order")
	}

	// The audit row survived and was assigned too.
	var auditTenant string
	if err := st.db.QueryRowContext(ctx, `SELECT tenant_id FROM audit LIMIT 1`).Scan(&auditTenant); err != nil {
		t.Fatal(err)
	}
	if auditTenant != DefaultTenant {
		t.Errorf("audit tenant = %q, want %q", auditTenant, DefaultTenant)
	}
}

func TestTwoTenantsCanEachHaveAnAdmin(t *testing.T) {
	// The reason the users table was rebuilt. Under the old globally-unique
	// constraint this is the first thing that fails, and it fails immediately -
	// every organisation names its first account "admin".
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	for _, id := range []string{"acme", "beta"} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO tenants (id, name, disabled, created_at) VALUES (?, ?, 0, datetime('now'))`,
			id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
			 VALUES (?, 'admin', 'hash', 'admin', datetime('now'))`, id); err != nil {
			t.Fatalf("creating an admin for %q: %v", id, err)
		}
	}

	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username = 'admin'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("got %d admins, want 2 - one per tenant", n)
	}
}

func TestUsernameIsStillUniqueWithinATenant(t *testing.T) {
	// Per-tenant, not abandoned. Two "admin" accounts in one organisation would make
	// the audit trail ambiguous about who did what.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
		 VALUES ('main', 'dup', 'h', 'viewer', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	_, err = st.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
		 VALUES ('main', 'dup', 'h', 'viewer', datetime('now'))`)
	if err == nil {
		t.Error("a duplicate username within one tenant was accepted")
	}
}

func TestUsernameUniquenessIsStillCaseInsensitive(t *testing.T) {
	// COLLATE NOCASE had to survive the rebuild. Without it, "Admin" and "admin"
	// become two accounts in the same organisation, which is a login somebody did
	// not expect to exist.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
		 VALUES ('main', 'Casey', 'h', 'viewer', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
		 VALUES ('main', 'casey', 'h', 'viewer', datetime('now'))`); err == nil {
		t.Error("a case-variant duplicate was accepted within one tenant")
	}
}

func TestDeletingATenantRemovesItsUsers(t *testing.T) {
	// ON DELETE CASCADE, so removing a tenant does not leave logins that belong to
	// nobody but still authenticate.
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO tenants (id, name, disabled, created_at) VALUES ('gone', 'Gone', 0, datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO users (tenant_id, username, password_hash, role, created_at)
		 VALUES ('gone', 'someone', 'h', 'viewer', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = 'gone'`); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE tenant_id = 'gone'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d users survived their tenant being deleted", n)
	}
}

func TestTheDefaultTenantExists(t *testing.T) {
	// A fresh single-tenant installation must have somewhere for its rows to belong
	// without the operator ever hearing the word "tenant".
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var name string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT name FROM tenants WHERE id = ?`, DefaultTenant).Scan(&name); err != nil {
		t.Fatalf("the default tenant is missing: %v", err)
	}
	if name == "" {
		t.Error("the default tenant has no display name")
	}
}
