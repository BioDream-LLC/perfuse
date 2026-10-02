package config

import (
	"strings"
	"testing"
	"time"
)

// validDatabaseSource is the minimum that should load, so each test can break one
// thing and nothing else.
func validDatabaseSource() DatabaseSource {
	return DatabaseSource{
		Driver:     "postgres",
		DSN:        "postgres://interface@db/records",
		Query:      "SELECT id, mrn FROM outbound WHERE sent_at IS NULL ORDER BY id",
		AfterQuery: "UPDATE outbound SET sent_at = now() WHERE id = $1",
		KeyColumn:  "id",
		Template:   "MSH|^~\\&|A|B|C|D|20260819||ADT^A08|${id}|P|2.5.1\nPID|1||${mrn}",
	}
}

func TestDatabaseSourceAcceptsAReasonableConfiguration(t *testing.T) {
	src := validDatabaseSource()
	if err := src.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Defaults have to be applied, since a zero poll interval would be a busy loop
	// and a zero batch size would read nothing.
	if src.PollInterval != 10*time.Second {
		t.Errorf("poll_interval defaulted to %s", src.PollInterval)
	}
	if src.BatchSize != 100 {
		t.Errorf("batch_size defaulted to %d", src.BatchSize)
	}
	if src.MaxAttempts != 3 {
		t.Errorf("max_attempts defaulted to %d", src.MaxAttempts)
	}
}

func TestDatabaseSourceRefusesWhatWouldFailQuietly(t *testing.T) {
	cases := []struct {
		name string
		edit func(*DatabaseSource)
		want string
	}{
		{"no driver", func(d *DatabaseSource) { d.Driver = "" }, "driver is required"},
		{"unknown driver", func(d *DatabaseSource) { d.Driver = "oracle" }, "unsupported"},
		{"no dsn", func(d *DatabaseSource) { d.DSN = "" }, "dsn is required"},
		{"no query", func(d *DatabaseSource) { d.Query = "" }, "query is required"},

		{
			// A poll running an UPDATE on a timer, with no record of what it touched, is
			// not something to find out about afterwards.
			"query that writes",
			func(d *DatabaseSource) { d.Query = "DELETE FROM outbound" },
			"must be a SELECT",
		},
		{
			"neither template nor column",
			func(d *DatabaseSource) { d.Template = ""; d.Column = "" },
			"either template",
		},
		{
			"both template and column",
			func(d *DatabaseSource) { d.Column = "payload" },
			"only one can apply",
		},
		{
			// Every row would produce a byte-identical message, which is not a feed.
			"template referencing nothing",
			func(d *DatabaseSource) { d.Template = "MSH|^~\\&|A|B|C|D|20260819||ADT^A08|1|P|2.5.1" },
			"references no columns",
		},
		{
			"template with no MSH",
			func(d *DatabaseSource) { d.Template = "PID|1||${mrn}" },
			"MSH",
		},
		{
			// Without a key there is nothing to bind, so the statement has to name rows
			// some other way, and one that names rows without binding will eventually
			// mark a row that was never read.
			"after_query with no key column",
			func(d *DatabaseSource) { d.KeyColumn = "" },
			"needs key_column",
		},
		{
			"after_query with no placeholder",
			func(d *DatabaseSource) { d.AfterQuery = "UPDATE outbound SET sent_at = now()" },
			"no parameter placeholder",
		},
		{
			"after_query that marks nothing",
			func(d *DatabaseSource) { d.AfterQuery = "SELECT $1" },
			"marks nothing",
		},
		{
			// Nothing would record that a row was sent, in the database or here.
			"nothing tracks progress",
			func(d *DatabaseSource) { d.AfterQuery = ""; d.KeyColumn = "" },
			"nothing records that a row was sent",
		},
		{
			"sub-second poll",
			func(d *DatabaseSource) { d.PollInterval = 100 * time.Millisecond },
			"busy loop",
		},
		{
			"zero batch",
			func(d *DatabaseSource) { d.BatchSize = -1 },
			"no rows would be read",
		},
		{
			// An unbounded first poll against a year of rows reads all of it into memory
			// and then sends all of it at a live receiver.
			"enormous batch",
			func(d *DatabaseSource) { d.BatchSize = 500000 },
			"floods the receiver",
		},
		{
			"no attempts",
			func(d *DatabaseSource) { d.MaxAttempts = -1 },
			"before it was ever tried",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := validDatabaseSource()
			tc.edit(&src)

			err := src.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestDatabaseSourceAllowsAWithQuery(t *testing.T) {
	// A CTE is a SELECT, and refusing it would push people into writing a view.
	src := validDatabaseSource()
	src.Query = "WITH pending AS (SELECT id, mrn FROM outbound WHERE sent_at IS NULL) " +
		"SELECT * FROM pending ORDER BY id"
	if err := src.Validate(); err != nil {
		t.Errorf("a WITH query should be allowed: %v", err)
	}
}

func TestDatabaseSourceAllowsACommentedQuery(t *testing.T) {
	src := validDatabaseSource()
	src.Query = "-- rows the department system has staged for us\n" +
		"SELECT id, mrn FROM outbound WHERE sent_at IS NULL ORDER BY id"
	if err := src.Validate(); err != nil {
		t.Errorf("a leading comment should not make a SELECT unrecognisable: %v", err)
	}
}

func TestDatabaseSourceWarnsAboutThingsThatAreLegalButRisky(t *testing.T) {
	src := validDatabaseSource()
	src.DSN = "postgres://interface:hunter2@db/records"
	src.Query = "SELECT id, mrn FROM outbound"
	src.AfterQuery = ""
	src.KeyColumn = "id"
	if err := src.Validate(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(src.Warnings(), " | ")

	// A credential in a file that is meant to live in git.
	if !strings.Contains(joined, "password") {
		t.Errorf("a literal password should warn: %v", src.Warnings())
	}
	// Order is meaning in a clinical feed: an admission before its discharge.
	if !strings.Contains(joined, "ORDER BY") {
		t.Errorf("a query with no ORDER BY should warn: %v", src.Warnings())
	}
	// Tracking keys in memory works, and the record does not survive a rebuild.
	if !strings.Contains(joined, "after_query") {
		t.Errorf("a missing after_query should warn: %v", src.Warnings())
	}
}

func TestDatabaseSourceDoesNotWarnAboutAnEnvironmentReference(t *testing.T) {
	// The whole point of ${VAR} is that it is the right thing to do, so warning about
	// it would train people to ignore the warning that matters.
	src := validDatabaseSource()
	src.DSN = "postgres://interface:${DB_PASSWORD}@db/records"
	if err := src.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, w := range src.Warnings() {
		if strings.Contains(w, "password") {
			t.Errorf("an environment reference should not warn: %q", w)
		}
	}
}

// --- the destination -------------------------------------------------------

func validDatabaseDestination() DatabaseDestination {
	return DatabaseDestination{
		Driver:    "postgres",
		DSN:       "postgres://interface@db/records",
		Statement: "INSERT INTO admissions (mrn, surname) VALUES ($1, $2)",
		Params:    []string{"PID-3.1", "PID-5.1"},
	}
}

func TestDatabaseDestinationAcceptsAReasonableConfiguration(t *testing.T) {
	d := validDatabaseDestination()
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if d.Timeout != 30*time.Second {
		t.Errorf("timeout defaulted to %s", d.Timeout)
	}
}

func TestDatabaseDestinationRefusesWhatWouldFailQuietly(t *testing.T) {
	cases := []struct {
		name string
		edit func(*DatabaseDestination)
		want string
	}{
		{"no driver", func(d *DatabaseDestination) { d.Driver = "" }, "driver is required"},
		{"no dsn", func(d *DatabaseDestination) { d.DSN = "" }, "dsn is required"},
		{"no statement", func(d *DatabaseDestination) { d.Statement = "" }, "statement is required"},

		{
			// The destination would report every message as delivered while writing
			// nothing at all.
			"statement that reads",
			func(d *DatabaseDestination) {
				d.Statement = "SELECT * FROM admissions WHERE mrn = $1"
				d.Params = []string{"PID-3.1"}
			},
			"read against and then discarded",
		},
		{
			// The everyday failure is not injection, it is O'Brien: a value built into
			// the SQL breaks the statement on an apostrophe.
			"template in the SQL",
			func(d *DatabaseDestination) {
				d.Statement = "INSERT INTO admissions (mrn) VALUES ('${PID-3.1}')"
				d.Params = nil
			},
			"bound as parameters",
		},
		{
			// A mismatch binds values to the wrong columns, and the result is data that
			// looks entirely valid.
			"too few params",
			func(d *DatabaseDestination) { d.Params = []string{"PID-3.1"} },
			"binds values to the wrong columns",
		},
		{
			"too many params",
			func(d *DatabaseDestination) {
				d.Params = []string{"PID-3.1", "PID-5.1", "PID-7.1"}
			},
			"binds values to the wrong columns",
		},
		{
			// None of the message would reach the database.
			"no placeholders at all",
			func(d *DatabaseDestination) {
				d.Statement = "INSERT INTO heartbeat (seen) VALUES (now())"
				d.Params = nil
			},
			"same fixed row for every message",
		},
		{
			"params with no placeholders",
			func(d *DatabaseDestination) {
				d.Statement = "INSERT INTO heartbeat (seen) VALUES (now())"
			},
			"none of them would be used",
		},
		{
			"empty param",
			func(d *DatabaseDestination) { d.Params = []string{"PID-3.1", "  "} },
			"is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDatabaseDestination()
			tc.edit(&d)

			err := d.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestDatabaseDestinationCountsPlaceholdersPerDialect(t *testing.T) {
	// Each style is paired with a driver that uses it. The cases used to share one driver, which stopped working when the placeholder
	// style began to be checked against the driver - a mismatch is now refused at validation rather than becoming a syntax error from
	// the server when a message arrives.
	cases := []struct {
		name   string
		driver string
		stmt   string
		params []string
	}{
		{"postgres", "postgres", "INSERT INTO t (a, b) VALUES ($1, $2)", []string{"PID-3.1", "PID-5.1"}},
		{"sqlserver", "sqlserver", "INSERT INTO t (a, b) VALUES (@p1, @p2)", []string{"PID-3.1", "PID-5.1"}},
		{"question marks", "mysql", "INSERT INTO t (a, b) VALUES (?, ?)", []string{"PID-3.1", "PID-5.1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDatabaseDestination()
			d.Driver = tc.driver
			d.Statement = tc.stmt
			d.Params = tc.params

			if err := d.Validate(); err != nil {
				t.Errorf("Validate: %v", err)
			}
		})
	}
}

func TestDatabaseDestinationAllowsAReusedPlaceholder(t *testing.T) {
	// A statement may legitimately use the same value twice, so distinct names are
	// counted rather than occurrences.
	d := validDatabaseDestination()
	d.Statement = "INSERT INTO t (mrn, also_mrn) VALUES ($1, $1)"
	d.Params = []string{"PID-3.1"}
	if err := d.Validate(); err != nil {
		t.Errorf("a reused placeholder should be allowed: %v", err)
	}
}

func TestDatabaseBlockOnlyAppliesToADatabaseConnector(t *testing.T) {
	// Silently ignoring it means somebody wrote a DSN and a statement and got
	// neither, with nothing in the output to explain why.
	ch := Channel{
		Name: "mixed",
		Source: Source{
			Type: SourceMLLP, Listen: "127.0.0.1:6661",
			Database: &DatabaseSource{Driver: "postgres"},
		},
		Destinations: []Destination{{
			Name: "out", Type: DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}
	err := ch.Validate()
	if err == nil {
		t.Fatal("a database block on an mllp source should be refused")
	}
	if !strings.Contains(err.Error(), "only applies to a database source") {
		t.Errorf("got: %v", err)
	}
}

func TestDatabaseDestinationBlockOnlyAppliesToADatabaseDestination(t *testing.T) {
	ch := Channel{
		Name:   "mixed",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:6661"},
		Destinations: []Destination{{
			Name: "out", Type: DestinationMLLP, Address: "127.0.0.1:1",
			Database: &DatabaseDestination{Driver: "postgres"},
		}},
	}
	err := ch.Validate()
	if err == nil {
		t.Fatal("a database block on an mllp destination should be refused")
	}
	if !strings.Contains(err.Error(), "only applies to a database destination") {
		t.Errorf("got: %v", err)
	}
}

func TestDatabaseSourceNeedsABlock(t *testing.T) {
	ch := Channel{
		Name:   "db",
		Source: Source{Type: SourceDatabase},
		Destinations: []Destination{{
			Name: "out", Type: DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}
	err := ch.Validate()
	if err == nil {
		t.Fatal("a database source with no block should be refused")
	}
	if !strings.Contains(err.Error(), "needs a database block") {
		t.Errorf("got: %v", err)
	}
}

func TestUnsupportedSourceTypeListsWhatIsAvailable(t *testing.T) {
	ch := Channel{
		Name: "x",
		// Deliberately a name that will not become real. This test used to say "ftp", which was a good example of a
		// close guess right up until ftp was implemented - at which point the test failed for a reason that had
		// nothing to do with what it checks.
		Source: Source{Type: "carrier-pigeon"},
		Destinations: []Destination{{
			Name: "out", Type: DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}
	err := ch.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	// A list beats a bare refusal: the commonest reason to hit this is guessing a
	// name, and the guess is usually close.
	for _, want := range []string{"mllp", "http", "database", "sftp", "file", "tcp", "ftp", "smb", "webdav"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list %q: %v", want, err)
		}
	}
}

func TestAPlaceholderStyleTheDriverCannotReadIsRefused(t *testing.T) {
	// Found against a real PostgreSQL: a statement written with question marks was accepted by every check here and refused by the
	// server with "syntax error at or near ,". That message names a statement somebody did write, so it sends them looking for a typo
	// rather than at the dialect - and it arrives when a message does, which for a nightly feed means at three in the morning.
	//
	// Perfuse still does not rewrite placeholders, and that decision is right: a translation layer that got one wrong would bind a
	// value to the wrong column and write data that looks valid. What changed is when the mistake is reported.
	for _, tc := range []struct {
		name   string
		driver string
		stmt   string
	}{
		{"question marks against postgres", "postgres", "INSERT INTO t (a) VALUES (?)"},
		{"dollars against mysql", "mysql", "INSERT INTO t (a) VALUES ($1)"},
		{"dollars against sqlserver", "sqlserver", "INSERT INTO t (a) VALUES ($1)"},
		{"question marks against sqlserver", "sqlserver", "INSERT INTO t (a) VALUES (?)"},
		{"at-p against mysql", "mysql", "INSERT INTO t (a) VALUES (@p1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := validDatabaseDestination()
			d.Driver = tc.driver
			d.Statement = tc.stmt
			d.Params = []string{"PID-3.1"}

			err := d.Validate()
			if err == nil {
				t.Fatal("accepted a placeholder style the driver cannot read")
			}

			// The message has to name the style the driver wants, because knowing which one is wrong is not the same as knowing what
			// to write instead.
			if !strings.Contains(err.Error(), "needs") {
				t.Errorf("the message does not say what the driver needs: %v", err)
			}
		})
	}
}

func TestAStatementInTheDriversOwnStyleIsAccepted(t *testing.T) {
	// The positive control. Without it the check above would pass just as well against validation that refused every statement.
	for _, tc := range []struct{ driver, stmt string }{
		{"postgres", "INSERT INTO t (a) VALUES ($1)"},
		{"mysql", "INSERT INTO t (a) VALUES (?)"},
		{"sqlite", "INSERT INTO t (a) VALUES (?)"},
		{"sqlserver", "INSERT INTO t (a) VALUES (@p1)"},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			d := validDatabaseDestination()
			d.Driver = tc.driver
			d.Statement = tc.stmt
			d.Params = []string{"PID-3.1"}

			if err := d.Validate(); err != nil {
				t.Errorf("the driver's own placeholder style was refused: %v", err)
			}
		})
	}
}
