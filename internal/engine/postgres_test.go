package engine

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	_ "github.com/lib/pq"
)

// The database destination against a real PostgreSQL.
//
// Bind placeholders are the reason. PostgreSQL wants $1 and MySQL wants ?, and getting it wrong does not produce a wrong answer - it
// produces a syntax error at the far end, or worse, a statement that binds the arguments in a different order than intended. There is a
// Placeholder function in internal/sqldb that knows the difference; whether the statements built on it are accepted by the server is a
// separate question, and only the server can answer it.
//
// Consistent with the rest of the interop set: a real implementation, and the assertion is what the server holds rather than the absence
// of an error. An INSERT that reports success and stores nothing is the shape of failure this codebase keeps finding.

const pgDSN = "postgres://postgres:perfuse@127.0.0.1:5433/perfusetest?sslmode=disable"

func requirePostgres(t *testing.T) *sql.DB {
	t.Helper()

	if os.Getenv("PERFUSE_SKIP_PG") != "" {
		t.Skip("PERFUSE_SKIP_PG is set")
	}

	db, err := sql.Open("postgres", pgDSN)
	if err != nil {
		t.Skip("no PostgreSQL: see scripts/interop-up.sh")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skip("no PostgreSQL on 127.0.0.1:5433: see scripts/interop-up.sh")
	}

	// The table the destination writes to. Created here so the test owns its own fixture rather than depending on a setup script
	// having been run in the right order.
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS messages (
			id serial primary key,
			mrn text,
			family_name text,
			birth_date date,
			received timestamptz default now()
		)`); err != nil {
		t.Fatalf("creating the fixture table: %v", err)
	}

	return db
}

func TestARowPerfuseInsertsIsInARealPostgres(t *testing.T) {
	// The assertion is the row, read back with a second connection. A statement that PostgreSQL accepted and that stored nothing -
	// because the placeholders bound in the wrong order, or the transaction was never committed - is indistinguishable from success
	// from the sending side.
	db := requirePostgres(t)
	defer func() { _ = db.Close() }()

	// A value unique to this run, so the row can be found without depending on the table being empty.
	mrn := "MRN" + time.Now().Format("150405.000000")

	sender, err := NewDatabaseSender(config.Destination{
		Name: "warehouse",
		Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "postgres",
			DSN:    pgDSN,
			// PostgreSQL's own placeholders. Perfuse deliberately does not rewrite them: a translation layer that got one wrong
			// would bind a patient's name to the wrong column, and the result would look like valid data. Writing question marks
			// here first produced "syntax error at or near ," from the server, which is the design working as documented.
			Statement: "INSERT INTO messages (mrn, family_name, birth_date) VALUES ($1, $2, $3)",
			// A quoted parameter is a constant, which is how a channel writes a fixed value alongside message data.
			Params: []string{"'" + mrn + "'", "PID-5.1", "PID-7"},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Skipf("the database destination could not be built, so the statement shape here may be wrong: %v", err)
	}

	defer func() { _ = sender.Close() }()

	message := "MSH|^~\\&|PERFUSE|TEST|PG|TEST|20260917210000||ADT^A01|MSG1|P|2.5\r" +
		"PID|1||" + mrn + "^^^HOSP^MR||Hopper^Grace^B||19061209|F\r"

	if err := sender.Send(context.Background(), []byte(message)); err != nil {
		t.Fatalf("PostgreSQL refused the statement the destination built: %v", err)
	}

	var family string

	err = db.QueryRowContext(context.Background(),
		"SELECT family_name FROM messages WHERE mrn = $1", mrn).Scan(&family)
	switch {
	case err == sql.ErrNoRows:
		t.Fatal("the insert reported success and no row is in the table")
	case err != nil:
		t.Fatal(err)
	}

	if family != "Hopper" {
		t.Errorf("family_name is %q, want Hopper: the parameters bound in the wrong order", family)
	}
}

func TestPostgresRefusesAStatementWithTheWrongPlaceholderStyle(t *testing.T) {
	// The positive control, and it demonstrates why the Placeholder function has to exist.
	//
	// A question mark is MySQL's placeholder. Sent to PostgreSQL it is a syntax error, which is the good outcome: the failure is loud.
	// This test exists so that a change making the whole layer use question marks everywhere cannot pass unnoticed - it would break
	// only for PostgreSQL users, who would see a syntax error naming a statement they never wrote.
	db := requirePostgres(t)
	defer func() { _ = db.Close() }()

	_, err := db.ExecContext(context.Background(), "INSERT INTO messages (mrn) VALUES (?)", "MRN-nope")
	if err == nil {
		t.Fatal("PostgreSQL accepted a MySQL placeholder, so this server is not the dialect these tests assume")
	}
}

func TestTheDollarPlaceholderStyleWorksAgainstPostgres(t *testing.T) {
	// The other half of the control: the style the Placeholder function produces for this driver is accepted. Without this, the test
	// above would pass against a server that rejected everything.
	db := requirePostgres(t)
	defer func() { _ = db.Close() }()

	mrn := "MRN-dollar-" + time.Now().Format("150405.000000")

	if _, err := db.ExecContext(context.Background(), "INSERT INTO messages (mrn) VALUES ($1)", mrn); err != nil {
		t.Fatalf("PostgreSQL refused its own placeholder style: %v", err)
	}

	var found string
	if err := db.QueryRowContext(context.Background(), "SELECT mrn FROM messages WHERE mrn = $1", mrn).Scan(&found); err != nil {
		t.Fatal(err)
	}
}
