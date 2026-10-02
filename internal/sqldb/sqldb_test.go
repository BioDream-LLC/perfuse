package sqldb

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolveDSNSubstitutesEnvironmentVariables(t *testing.T) {
	t.Setenv("SQLDB_TEST_PW", "hunter2")
	t.Setenv("SQLDB_TEST_HOST", "db.internal")

	got, err := ResolveDSN("postgres://interface:${SQLDB_TEST_PW}@${SQLDB_TEST_HOST}/records")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://interface:hunter2@db.internal/records" {
		t.Errorf("ResolveDSN = %q", got)
	}
}

func TestResolveDSNLeavesADSNWithoutReferencesAlone(t *testing.T) {
	in := "server=10.0.0.5;user id=sa;database=lab"
	got, err := ResolveDSN(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("ResolveDSN changed a DSN with no references: %q", got)
	}
}

func TestResolveDSNNamesEveryMissingVariable(t *testing.T) {
	os.Unsetenv("SQLDB_TEST_ABSENT_A")
	os.Unsetenv("SQLDB_TEST_ABSENT_B")

	_, err := ResolveDSN("postgres://${SQLDB_TEST_ABSENT_A}:${SQLDB_TEST_ABSENT_B}@db/records")
	if err == nil {
		t.Fatal("missing variables should be an error")
	}
	// Both, not just the first. Reporting one at a time turns a single fix into
	// several restarts.
	for _, name := range []string{"SQLDB_TEST_ABSENT_A", "SQLDB_TEST_ABSENT_B"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}

func TestResolveDSNTreatsAnEmptyVariableAsMissing(t *testing.T) {
	t.Setenv("SQLDB_TEST_EMPTY", "")

	_, err := ResolveDSN("postgres://user:${SQLDB_TEST_EMPTY}@db/records")
	// An empty variable is almost always an unset one that something exported anyway,
	// and connecting with an empty password produces an authentication error that
	// looks like a wrong credential rather than a missing one.
	if err == nil {
		t.Fatal("an empty variable should be reported like a missing one")
	}
}

func TestRedactRemovesCredentialsFromEveryDSNShape(t *testing.T) {
	cases := []struct {
		name, in, secret string
	}{
		{"postgres url", "postgres://interface:hunter2@db.internal/records", "hunter2"},
		{"sqlserver url", "sqlserver://sa:P%40ssw0rd@10.0.0.5:1433?database=lab", "P%40ssw0rd"},
		{"mysql dsn", "interface:hunter2@tcp(db.internal:3306)/records", "hunter2"},
		{"keyword lower", "server=10.0.0.5;user id=sa;password=hunter2;database=lab", "hunter2"},
		{"keyword capital", "server=10.0.0.5;Password=hunter2;database=lab", "hunter2"},
		{"pwd form", "user=interface pwd=hunter2 host=db", "hunter2"},
		{"quoted", `server=10.0.0.5;password='hun;ter2';database=lab`, "hun;ter2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Errorf("Redact(%q) = %q, which still contains the password", tc.in, got)
			}
			// Marked rather than removed, so whoever has to fix the connection can still
			// recognise the DSN.
			if !strings.Contains(got, "****") {
				t.Errorf("Redact(%q) = %q, with nothing showing something was removed",
					tc.in, got)
			}
		})
	}
}

func TestRedactLeavesAPasswordlessDSNIntact(t *testing.T) {
	// The bug this pins: the mysql pattern user:secret@host also matches the scheme
	// of a URL, so "postgres://interface@db" became "postgres:****@db". That is not a
	// redaction, it is corruption of the one string somebody needs to fix the
	// connection, and it would only ever be noticed by whoever was already debugging.
	cases := []string{
		"postgres://interface@db.internal/records?sslmode=verify-full",
		"sqlserver://10.0.0.5:1433?database=lab",
		"file:/var/lib/perfuse/staging.db",
		"server=10.0.0.5;user id=sa;database=lab",
	}

	for _, in := range cases {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, but there was no password to remove", in, got)
		}
	}
}

func TestRedactKeepsTheHostVisible(t *testing.T) {
	// The host is the part that matters in an error. Redacting it too would make
	// "cannot reach the database" unactionable.
	got := Redact("postgres://interface:hunter2@db.internal:5432/records")
	if !strings.Contains(got, "db.internal:5432") {
		t.Errorf("Redact removed the host: %q", got)
	}
	if !strings.Contains(got, "records") {
		t.Errorf("Redact removed the database name: %q", got)
	}
}

func TestOpenRefusesAnUnknownDriver(t *testing.T) {
	_, err := Open("oracle", "whatever", 1, time.Second)
	if err == nil {
		t.Fatal("an unsupported driver should be refused")
	}
	if !strings.Contains(err.Error(), "oracle") {
		t.Errorf("the error should name the driver: %v", err)
	}
}

func TestOpenDoesNotPutTheDSNInAnError(t *testing.T) {
	// Errors from here reach the log, the interface, and a stored message record.
	_, err := Open("postgres", "this is not a valid dsn password=hunter2", 1, time.Second)
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the password leaked into the error: %v", err)
	}
}

func TestOpenResolvesTheDSNBeforeConnecting(t *testing.T) {
	os.Unsetenv("SQLDB_TEST_NOPE")

	_, err := Open("postgres", "postgres://u:${SQLDB_TEST_NOPE}@db/r", 1, time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	// Reported as a missing variable, not as a connection failure. The two have
	// completely different fixes and a connection error would send somebody to the
	// network team.
	if !strings.Contains(err.Error(), "SQLDB_TEST_NOPE") {
		t.Errorf("the error should name the variable: %v", err)
	}
}

func TestPlaceholderMatchesEachDialect(t *testing.T) {
	cases := []struct{ driver, want string }{
		{"postgres", "$1"},
		{"sqlserver", "@p1"},
		{"mysql", "?"},
		{"sqlite", "?"},
	}
	for _, tc := range cases {
		if got := Placeholder(tc.driver, 1); got != tc.want {
			t.Errorf("Placeholder(%q) = %q, want %q", tc.driver, got, tc.want)
		}
	}
}

func TestDescribeNamesSomethingRecognisable(t *testing.T) {
	// These strings go in front of people, so "sqlserver" should read as the product
	// somebody would recognise on an invoice.
	if got := Describe("sqlserver"); !strings.Contains(got, "SQL Server") {
		t.Errorf("Describe(sqlserver) = %q", got)
	}
	if got := Describe("mysql"); !strings.Contains(got, "MariaDB") {
		t.Errorf("Describe(mysql) should mention MariaDB, since it is the same driver: %q", got)
	}
	// An unknown driver returns its own name rather than an empty string, so a log
	// line never reads "cannot reach the  database".
	if got := Describe("whatever"); got != "whatever" {
		t.Errorf("Describe(whatever) = %q", got)
	}
}

func TestOpenWorksAgainstSQLite(t *testing.T) {
	// One end-to-end open, so the driver registration itself is covered rather than
	// only the string handling around it.
	db, err := Open("sqlite", "file:"+t.TempDir()+"/t.db", 2, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (a TEXT)`); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}
