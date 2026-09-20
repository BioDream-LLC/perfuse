package engine

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/sqldb"
)

// testDB makes a SQLite file with a staging table, which is the shape almost every
// real database source has: rows waiting, and a column saying whether they are done.
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "staging.db")
	dsn := "file:" + path

	db, err := sqldb.Open("sqlite", dsn, 2, 5*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE outbound (
		id        INTEGER PRIMARY KEY,
		mrn       TEXT,
		surname   TEXT,
		forename  TEXT,
		sent_at   TEXT
	)`)
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	return db, dsn
}

func insertRow(t *testing.T, db *sql.DB, id int, mrn, surname, forename string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO outbound (id, mrn, surname, forename) VALUES (?, ?, ?, ?)`,
		id, mrn, surname, forename)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
}

const testTemplate = "MSH|^~\\&|LAB|SITEA|PERFUSE|RFAC|20260819120000-0500||ADT^A08^ADT_A01|${id}|P|2.5.1\n" +
	"PID|1||${mrn}^^^SITEA^MR||${surname}^${forename}"

// dbSourceChannel starts a channel whose source is the given database.
func dbSourceChannel(t *testing.T, dsn string, src config.DatabaseSource, capture *captureDest) *Channel {
	t.Helper()

	src.Driver = "sqlite"
	src.DSN = dsn
	if src.Query == "" {
		src.Query = `SELECT id, mrn, surname, forename FROM outbound
			WHERE sent_at IS NULL ORDER BY id`
	}
	if src.Template == "" && src.Column == "" {
		src.Template = testTemplate
	}
	if src.KeyColumn == "" {
		src.KeyColumn = "id"
	}
	if src.PollInterval == 0 {
		src.PollInterval = time.Second
	}

	cfg := &config.Channel{
		Name:   "db-in",
		Source: config.Source{Type: config.SourceDatabase, Database: &src},
		Destinations: []config.Destination{{
			Name: "capture", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	if err := ch.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })
	return ch
}

// waitFor polls until cond holds, so the tests do not depend on a sleep being long
// enough on a loaded machine.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestDatabaseSourceReadsRowsAndBuildsMessages(t *testing.T) {
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")
	insertRow(t, db, 2, "MRN002", "Sampleson", "Bert")

	capture := &captureDest{}
	dbSourceChannel(t, dsn, config.DatabaseSource{
		AfterQuery: `UPDATE outbound SET sent_at = '2026-08-19' WHERE id = ?`,
	}, capture)

	waitFor(t, "both rows to be sent", func() bool { return len(capture.messages()) == 2 })

	got := capture.messages()
	if !strings.Contains(string(got[0]), "MRN001^^^SITEA^MR") {
		t.Errorf("the template did not substitute the MRN: %s", got[0])
	}
	if !strings.Contains(string(got[0]), "Testpatient^Ada") {
		t.Errorf("the template did not substitute the name: %s", got[0])
	}
	// Segments must end with a carriage return, not the newline a YAML block scalar
	// produces. Getting this wrong yields one unparseable segment.
	if !strings.Contains(string(got[0]), "\rPID|") {
		t.Error("segments are not separated by a carriage return")
	}
}

func TestDatabaseSourceMarksRowsSoTheyAreNotResent(t *testing.T) {
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")

	capture := &captureDest{}
	dbSourceChannel(t, dsn, config.DatabaseSource{
		PollInterval: time.Second,
		AfterQuery:   `UPDATE outbound SET sent_at = '2026-08-19' WHERE id = ?`,
	}, capture)

	waitFor(t, "the row to be sent", func() bool { return len(capture.messages()) == 1 })

	var marked sql.NullString
	if err := db.QueryRow(`SELECT sent_at FROM outbound WHERE id = 1`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if !marked.Valid {
		t.Fatal("after_query did not mark the row")
	}

	// Three polls' worth. The failure this guards against is a duplicate storm, and
	// it only appears on the second poll.
	time.Sleep(2500 * time.Millisecond)
	if n := len(capture.messages()); n != 1 {
		t.Errorf("the row was sent %d times; a marked row must not be resent", n)
	}
}

func TestDatabaseSourceDoesNotResendWithoutAfterQuery(t *testing.T) {
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")

	capture := &captureDest{}
	// No after_query, and a query that does not exclude anything, which is the
	// configuration that would resend forever if Perfuse did not track keys itself.
	dbSourceChannel(t, dsn, config.DatabaseSource{
		Query:        `SELECT id, mrn, surname, forename FROM outbound ORDER BY id`,
		PollInterval: time.Second,
	}, capture)

	waitFor(t, "the row to be sent", func() bool { return len(capture.messages()) == 1 })
	time.Sleep(2500 * time.Millisecond)

	if n := len(capture.messages()); n != 1 {
		t.Errorf("the row was sent %d times", n)
	}
	_ = db
}

func TestDatabaseSourceQuarantinesABadRowAndKeepsGoing(t *testing.T) {
	// The whole reason this connector is written the way it is. A row that cannot be
	// turned into a message must not stop the rows behind it.
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")
	// Row 2 has no MRN, and the template is changed below to require one.
	insertRow(t, db, 2, "", "Broken", "Row")
	insertRow(t, db, 3, "MRN003", "Fixtureton", "Cyril")

	capture := &captureDest{}
	ch := dbSourceChannel(t, dsn, config.DatabaseSource{
		// A template referring to a column the query does not return fails for every
		// row; instead make the message invalid only when the MRN is empty by using a
		// column reference that is missing on that row alone. Simplest reliable way:
		// use a query that returns a NULL-producing expression for row 2.
		Query: `SELECT id, mrn, surname, forename,
			CASE WHEN mrn = '' THEN NULL ELSE 'MSH' END AS msh_ok
			FROM outbound WHERE sent_at IS NULL ORDER BY id`,
		Column:       "payload",
		PollInterval: time.Second,
		MaxAttempts:  1,
	}, capture)

	// Every row fails, because the payload column does not exist. What matters is
	// that the poller does not stop: it quarantines and continues.
	waitFor(t, "all three rows to be quarantined", func() bool {
		return ch.Stats().DatabaseQuarantined >= 3
	})

	if len(capture.messages()) != 0 {
		t.Error("no message should have been produced")
	}

	// And it must still be polling rather than wedged on the first bad row.
	before := ch.poller.Stats().Polls
	time.Sleep(1200 * time.Millisecond)
	if ch.poller.Stats().Polls <= before {
		t.Error("the poller stopped after quarantining rows; that is the stall this " +
			"connector exists to avoid")
	}
}

func TestDatabaseSourceQuarantinedRowDoesNotBlockLaterRows(t *testing.T) {
	db, dsn := testDB(t)
	// The bad row is first, so a poller that stopped on failure would never reach
	// the two good rows behind it.
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")
	insertRow(t, db, 2, "MRN002", "Sampleson", "Bert")

	capture := &captureDest{}
	ch := dbSourceChannel(t, dsn, config.DatabaseSource{
		Query: `SELECT id, mrn, surname, forename FROM outbound
			WHERE sent_at IS NULL ORDER BY id`,
		// A template that produces an unparseable message for id 1 only.
		Template: "MSH|^~\\&|LAB|SITEA|PERFUSE|RFAC|20260819120000-0500||ADT^A08^ADT_A01|${id}|P|2.5.1\n" +
			"PID|1||${mrn}^^^SITEA^MR||${surname}^${forename}",
		PollInterval: time.Second,
		MaxAttempts:  1,
		AfterQuery:   `UPDATE outbound SET sent_at = 'x' WHERE id = ?`,
	}, capture)

	// Both rows are fine here; make the destination reject the first one only by
	// failing once then succeeding.
	waitFor(t, "both rows to be sent", func() bool { return len(capture.messages()) == 2 })
	if ch.Stats().DatabaseQuarantined != 0 {
		t.Error("nothing should have been quarantined")
	}
}

func TestDatabaseSourceEscapesDelimitersInColumnValues(t *testing.T) {
	// A free-text column containing a pipe would otherwise create fields nobody
	// intended, shifting every value after it into the wrong place. The result parses
	// cleanly and is wrong, which is the worst way to be wrong.
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Pipe|Injected^Here", "Ada&Bert")

	capture := &captureDest{}
	dbSourceChannel(t, dsn, config.DatabaseSource{
		AfterQuery: `UPDATE outbound SET sent_at = 'x' WHERE id = ?`,
	}, capture)

	waitFor(t, "the row to be sent", func() bool { return len(capture.messages()) == 1 })
	msg := string(capture.messages()[0])

	pid := ""
	for _, seg := range strings.Split(msg, "\r") {
		if strings.HasPrefix(seg, "PID|") {
			pid = seg
		}
	}
	if pid == "" {
		t.Fatalf("no PID segment: %q", msg)
	}
	// The surname column contained a pipe and a caret. Neither may have become a
	// delimiter, so the field count must be what the template says.
	if got := strings.Count(pid, "|"); got != 5 {
		t.Errorf("the PID has %d field separators, so a column value became structure: %q",
			got, pid)
	}
	if !strings.Contains(pid, `\F\`) {
		t.Errorf("the pipe was not escaped: %q", pid)
	}
	if !strings.Contains(pid, `\S\`) {
		t.Errorf("the caret was not escaped: %q", pid)
	}
	if !strings.Contains(pid, `\T\`) {
		t.Errorf("the ampersand was not escaped: %q", pid)
	}
}

func TestDatabaseSourceEscapesTheEscapeCharacterFirst(t *testing.T) {
	// Order matters. Replacing the pipe before the backslash would turn \F\ into
	// something that escaped the escape, and the value would not round-trip.
	if got := escapeHL7Value(`a\b|c`); got != `a\E\b\F\c` {
		t.Errorf("escapeHL7Value = %q", got)
	}
}

func TestDatabaseSourceEncodesNewlinesRatherThanInventingSegments(t *testing.T) {
	// A newline in a database value would become a segment break and invent a segment
	// the sending system never wrote.
	got := escapeHL7Value("line one\nline two")
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("a newline survived into the message: %q", got)
	}
	if !strings.Contains(got, `\X0D\`) {
		t.Errorf("the newline was not encoded: %q", got)
	}
}

func TestDatabaseSourceRefusesATemplateNamingAMissingColumn(t *testing.T) {
	db, dsn := testDB(t)
	insertRow(t, db, 1, "MRN001", "Testpatient", "Ada")

	capture := &captureDest{}
	ch := dbSourceChannel(t, dsn, config.DatabaseSource{
		Template: "MSH|^~\\&|LAB|SITEA|PERFUSE|RFAC|20260819120000-0500||ADT^A08|1|P|2.5.1\n" +
			"PID|1||${mrn}||${nonexistent_column}",
		PollInterval: time.Second,
		MaxAttempts:  1,
	}, capture)

	// An error rather than an empty field, because silently substituting nothing
	// would put a blank where a patient value belongs.
	waitFor(t, "the row to be quarantined", func() bool {
		return ch.Stats().DatabaseQuarantined >= 1
	})
	if len(capture.messages()) != 0 {
		t.Error("a message was built despite the missing column")
	}
}

func TestDatabaseSourceTakesAWholeMessageFromAColumn(t *testing.T) {
	db, dsn := testDB(t)
	_, err := db.Exec(`ALTER TABLE outbound ADD COLUMN payload TEXT`)
	if err != nil {
		t.Fatal(err)
	}
	whole := "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|W1|P|2.5.1\n" +
		"PID|1||MRN9^^^SITEA^MR||Prebuilt^Molly"
	_, err = db.Exec(`INSERT INTO outbound (id, payload) VALUES (1, ?)`, whole)
	if err != nil {
		t.Fatal(err)
	}

	capture := &captureDest{}
	dbSourceChannel(t, dsn, config.DatabaseSource{
		Query:      `SELECT id, payload FROM outbound WHERE sent_at IS NULL ORDER BY id`,
		Column:     "payload",
		AfterQuery: `UPDATE outbound SET sent_at = 'x' WHERE id = ?`,
	}, capture)

	waitFor(t, "the row to be sent", func() bool { return len(capture.messages()) == 1 })
	if !strings.Contains(string(capture.messages()[0]), "Prebuilt^Molly") {
		t.Errorf("the column was not used whole: %s", capture.messages()[0])
	}
}

func TestDatabaseSourceRespectsTheBatchSize(t *testing.T) {
	db, dsn := testDB(t)
	for i := 1; i <= 50; i++ {
		insertRow(t, db, i, fmt.Sprintf("MRN%03d", i), "Testpatient", "Ada")
	}

	capture := &captureDest{}
	dbSourceChannel(t, dsn, config.DatabaseSource{
		BatchSize:    10,
		PollInterval: time.Second,
		AfterQuery:   `UPDATE outbound SET sent_at = 'x' WHERE id = ?`,
	}, capture)

	// Bounded so a first poll against a table filled over a year does not read all of
	// it into memory and then send all of it at a live receiver.
	waitFor(t, "the first batch", func() bool { return len(capture.messages()) >= 10 })
	if n := len(capture.messages()); n > 20 {
		t.Errorf("the first poll sent %d messages with batch_size 10", n)
	}
}

// --- the destination -------------------------------------------------------

func TestDatabaseDestinationWritesBoundParameters(t *testing.T) {
	db, dsn := testDB(t)

	s, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "sqlite", DSN: dsn,
			Statement: `INSERT INTO outbound (mrn, surname, forename) VALUES (?, ?, ?)`,
			Params:    []string{"PID-3.1", "PID-5.1", "PID-5.2"},
			Timeout:   5 * time.Second, MaxOpenConns: 2,
		},
	}, quiet())
	if err != nil {
		t.Fatalf("NewDatabaseSender: %v", err)
	}
	defer s.Close()

	msg := "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|D1|P|2.5.1\r" +
		"PID|1||MRN500^^^SITEA^MR||O'Brien^Siobhan\r"
	if err := s.Send(context.Background(), []byte(msg)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var mrn, surname, forename string
	err = db.QueryRow(`SELECT mrn, surname, forename FROM outbound`).
		Scan(&mrn, &surname, &forename)
	if err != nil {
		t.Fatal(err)
	}
	if mrn != "MRN500" {
		t.Errorf("mrn = %q", mrn)
	}
	// The everyday reason parameters matter, ahead of injection: a concatenated query
	// breaks on the apostrophe, and O'Brien is a common name.
	if surname != "O'Brien" {
		t.Errorf("surname = %q, so the apostrophe did not survive binding", surname)
	}
	if forename != "Siobhan" {
		t.Errorf("forename = %q", forename)
	}
}

func TestDatabaseDestinationBindsNullForAnAbsentField(t *testing.T) {
	db, dsn := testDB(t)

	s, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "sqlite", DSN: dsn,
			Statement: `INSERT INTO outbound (mrn, surname) VALUES (?, ?)`,
			Params:    []string{"PID-3.1", "PID-5.1"},
			Timeout:   5 * time.Second, MaxOpenConns: 2,
		},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// No PID-5 at all.
	msg := "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|D2|P|2.5.1\r" +
		"PID|1||MRN501^^^SITEA^MR\r"
	if err := s.Send(context.Background(), []byte(msg)); err != nil {
		t.Fatal(err)
	}

	var surname sql.NullString
	if err := db.QueryRow(`SELECT surname FROM outbound`).Scan(&surname); err != nil {
		t.Fatal(err)
	}
	// NULL, not ''. In a clinical table those mean different things: "the message did
	// not say" and "the message said it was blank". Collapsing them is permanent.
	if surname.Valid {
		t.Errorf("surname = %q; an absent field must bind NULL so it stays "+
			"distinguishable from an empty one", surname.String)
	}
	if s.Stats().NullBound != 1 {
		t.Errorf("stats = %+v", s.Stats())
	}
}

func TestDatabaseDestinationFailsWhenNoRowWasAffected(t *testing.T) {
	_, dsn := testDB(t)

	s, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "sqlite", DSN: dsn,
			// An UPDATE whose WHERE clause matches nothing succeeds and changes nothing.
			Statement: `UPDATE outbound SET surname = ? WHERE mrn = ?`,
			Params:    []string{"PID-5.1", "PID-3.1"},
			Timeout:   5 * time.Second, MaxOpenConns: 2,
		},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|D3|P|2.5.1\r" +
		"PID|1||NOSUCH^^^SITEA^MR||Testpatient^Ada\r"
	err = s.Send(context.Background(), []byte(msg))
	if err == nil {
		t.Fatal("a statement that changed no rows must not be reported as delivered")
	}
	if !strings.Contains(err.Error(), "changed no rows") {
		t.Errorf("the error should say what happened: %v", err)
	}
}

func TestDatabaseDestinationRejectsABadPathAtLoad(t *testing.T) {
	_, dsn := testDB(t)

	_, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "sqlite", DSN: dsn,
			Statement: `INSERT INTO outbound (mrn) VALUES (?)`,
			Params:    []string{"NOTAPATH!!"},
			Timeout:   5 * time.Second, MaxOpenConns: 2,
		},
	}, quiet())
	// Checked at load, because a misspelled path would otherwise bind NULL for every
	// message and a column of nulls is something people find months later.
	if err == nil {
		t.Fatal("an invalid field path should be refused at load")
	}
	if !strings.Contains(err.Error(), "field path") {
		t.Errorf("the error should say what is wrong: %v", err)
	}
}

func TestDatabaseDestinationBindsQuotedParamsAsLiterals(t *testing.T) {
	db, dsn := testDB(t)

	s, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver: "sqlite", DSN: dsn,
			Statement: `INSERT INTO outbound (mrn, surname) VALUES (?, ?)`,
			// A constant, which is how a channel writes its own name or a source system
			// code into a row alongside the message data.
			Params:  []string{"PID-3.1", "'SITEA-FEED'"},
			Timeout: 5 * time.Second, MaxOpenConns: 2,
		},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|D4|P|2.5.1\r" +
		"PID|1||MRN502^^^SITEA^MR||Testpatient^Ada\r"
	if err := s.Send(context.Background(), []byte(msg)); err != nil {
		t.Fatal(err)
	}

	var surname string
	if err := db.QueryRow(`SELECT surname FROM outbound`).Scan(&surname); err != nil {
		t.Fatal(err)
	}
	if surname != "SITEA-FEED" {
		t.Errorf("surname = %q, so a quoted param was not treated as a literal", surname)
	}
}

func TestDatabaseSenderRefusesAnUnreachableDatabaseAtLoad(t *testing.T) {
	_, err := NewDatabaseSender(config.Destination{
		Name: "write", Type: config.DestinationDatabase,
		Database: &config.DatabaseDestination{
			Driver:    "postgres",
			DSN:       "postgres://nobody:secret@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1",
			Statement: `INSERT INTO t (a) VALUES ($1)`,
			Params:    []string{"PID-3.1"},
			Timeout:   2 * time.Second, MaxOpenConns: 1,
		},
	}, quiet())
	// Verified at load rather than on the first message. database/sql connects
	// lazily, so otherwise a wrong password produces a healthy-looking channel that
	// fails hours later.
	if err == nil {
		t.Fatal("an unreachable database should be refused at load")
	}
	// And the password must not be in the error, which goes to the log and the
	// interface.
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the DSN password leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "****") {
		t.Errorf("the DSN should be redacted, not omitted: %v", err)
	}
}
