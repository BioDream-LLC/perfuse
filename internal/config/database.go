package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DatabaseSource reads rows from a database and turns each one into a message.
//
// This is how a great many hospital interfaces actually start. A department system
// has no HL7 capability, so somebody writes rows into a staging table and the
// interface engine polls it. It is unglamorous and it is everywhere.
//
// It is also the single commonest way a Mirth channel stalls, and the reason is
// almost always the same shape: a row that cannot be processed is read, fails,
// and is read again, forever, because nothing marked it. The channel looks alive.
// The queue looks empty. Nothing moves, and every message behind the bad row waits
// for a row that will never succeed.
//
// Most of the design here is about that.
type DatabaseSource struct {
	// Driver names the database. See SupportedDrivers.
	Driver string `yaml:"driver"`

	// DSN is the connection string. Prefer an environment variable reference over
	// a literal, because this normally contains a password and channels are meant
	// to live in git.
	DSN string `yaml:"dsn"`

	// Query selects the rows waiting to be sent. Required.
	//
	// It must be a SELECT. A poll that quietly performed writes would run on a
	// schedule with no record of what it changed.
	Query string `yaml:"query"`

	// AfterQuery marks a row as processed, and runs with the row's key bound to
	// it. Strongly recommended.
	//
	// Without it, every poll re-reads every row the query still matches, so the
	// query itself has to exclude processed rows some other way. If neither is
	// true the channel resends the same rows forever, which is a duplicate storm
	// rather than a stall and is arguably worse.
	AfterQuery string `yaml:"after_query,omitempty"`

	// KeyColumn identifies a row for AfterQuery, quarantine and de-duplication.
	// Required when AfterQuery is set.
	KeyColumn string `yaml:"key_column,omitempty"`

	// Template builds a message from a row. Either this or Column is required.
	//
	// Column references use ${column_name}. Missing columns are an error at load
	// rather than an empty string in a clinical message.
	Template string `yaml:"template,omitempty"`

	// Column names a column that already contains a complete HL7 message, for the
	// common case where something upstream built it and just needs it delivered.
	Column string `yaml:"column,omitempty"`

	// PollInterval defaults to 10s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`

	// BatchSize caps the rows taken per poll. Defaults to 100.
	//
	// Bounded because an unbounded first poll against a table somebody has been
	// filling for a year would read all of it into memory and then send all of it,
	// which is how a migration floods a live receiver.
	BatchSize int `yaml:"batch_size,omitempty"`

	// MaxAttempts is how many times a single row may fail before it is
	// quarantined and the poll moves on. Defaults to 3.
	//
	// This is the setting that prevents the stall. Retrying forever is not
	// resilience; it is one bad row stopping a hospital feed.
	MaxAttempts int `yaml:"max_attempts,omitempty"`

	// QueryTimeout bounds a single poll. Defaults to 30s.
	QueryTimeout time.Duration `yaml:"query_timeout,omitempty"`
}

// DatabaseDestination writes each message to a database.
type DatabaseDestination struct {
	// Driver names the database. See SupportedDrivers.
	Driver string `yaml:"driver"`

	// DSN is the connection string.
	DSN string `yaml:"dsn"`

	// Statement is executed once per message, with Params bound to it in order.
	//
	// Placeholders are the driver's own: $1 for postgres, ? for mysql and sqlite,
	// @p1 for sqlserver. Perfuse does not rewrite them, because a translation layer
	// that got a placeholder wrong would bind a patient's name to the wrong column
	// and the result would look like valid data.
	Statement string `yaml:"statement"`

	// Params are HL7 paths or ${...} expressions, bound to the statement in order.
	//
	// Values are always bound as parameters and never interpolated into the SQL.
	// That is not only about injection: O'Brien is a common name, and a
	// concatenated query breaks on the apostrophe.
	Params []string `yaml:"params,omitempty"`

	// Timeout bounds a single statement. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// MaxOpenConns caps the pool. Defaults to 4.
	MaxOpenConns int `yaml:"max_open_conns,omitempty"`
}

// SupportedDrivers lists the databases Perfuse can talk to, and what each one is
// usually found behind.
var SupportedDrivers = map[string]string{
	"postgres":  "PostgreSQL, and anything wire-compatible with it",
	"mysql":     "MySQL and MariaDB",
	"sqlserver": "Microsoft SQL Server, which most hospital departmental systems sit on",
	"sqlite":    "SQLite, mostly useful for testing a channel",
}

// placeholderPattern matches a ${column} reference in a template.
var placeholderPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

// selectPattern checks that a query reads rather than writes. Deliberately loose:
// it is a guard against a mistake, not a security boundary, since the DSN's own
// credentials are what actually limit what a query can do.
var selectPattern = regexp.MustCompile(`(?is)^\s*(--[^\n]*\n|/\*.*?\*/|\s)*(select|with)\b`)

func (d *DatabaseSource) applyDefaults() {
	if d.PollInterval == 0 {
		d.PollInterval = 10 * time.Second
	}
	if d.BatchSize == 0 {
		d.BatchSize = 100
	}
	if d.MaxAttempts == 0 {
		d.MaxAttempts = 3
	}
	if d.QueryTimeout == 0 {
		d.QueryTimeout = 30 * time.Second
	}
}

// Validate checks a database source.
func (d *DatabaseSource) Validate() error {
	d.applyDefaults()

	if d.Driver == "" {
		return errors.New("database.driver is required (" + driverList() + ")")
	}
	if _, ok := SupportedDrivers[d.Driver]; !ok {
		return fmt.Errorf("unsupported database driver %q: supported drivers are %s",
			d.Driver, driverList())
	}
	if strings.TrimSpace(d.DSN) == "" {
		return errors.New("database.dsn is required")
	}
	if strings.TrimSpace(d.Query) == "" {
		return errors.New("database.query is required: there is nothing to poll without it")
	}
	if !selectPattern.MatchString(d.Query) {
		// A poll running an UPDATE or a DELETE on a timer, with no record of what it
		// touched, is not something to discover afterwards.
		return errors.New("database.query must be a SELECT (or a WITH): " +
			"use after_query for the statement that marks a row processed, so the " +
			"change happens once per row and is recorded against that row")
	}

	if d.Template == "" && d.Column == "" {
		return errors.New("database needs either template (to build a message from " +
			"columns) or column (naming a column that already holds a whole message)")
	}
	if d.Template != "" && d.Column != "" {
		return errors.New("database has both template and column, and only one can " +
			"apply: template builds a message from columns, column takes one whole")
	}

	if d.Template != "" {
		refs := placeholderPattern.FindAllStringSubmatch(d.Template, -1)
		if len(refs) == 0 {
			return errors.New("database.template references no columns, so every row " +
				"would produce the same message: reference a column with ${column_name}")
		}
		for _, r := range refs {
			if strings.TrimSpace(r[1]) == "" {
				return errors.New("database.template contains an empty ${} reference")
			}
		}
		if !strings.Contains(d.Template, "MSH") {
			return errors.New("database.template does not contain an MSH segment, and " +
				"a message without one cannot be parsed, acknowledged or stored")
		}
	}

	if d.AfterQuery != "" {
		if d.KeyColumn == "" {
			// Without a key there is nothing to bind, so the statement would have to
			// name rows some other way - and a statement that marks rows without
			// naming them will eventually mark one that was never read.
			return errors.New("database.after_query needs key_column, naming the " +
				"column that identifies a row, so the statement marks exactly the row " +
				"that was processed and no other")
		}
		if !strings.Contains(d.AfterQuery, "?") &&
			!strings.Contains(d.AfterQuery, "$1") &&
			!strings.Contains(d.AfterQuery, "@p1") {
			return errors.New("database.after_query has no parameter placeholder, so " +
				"it does not depend on which row was processed: it would mark every " +
				"matching row each time one message was sent")
		}
		if selectPattern.MatchString(d.AfterQuery) {
			return errors.New("database.after_query is a SELECT, so it marks nothing " +
				"and every row would be read again on the next poll")
		}
	} else {
		// Allowed, because a query can exclude processed rows by itself, but it is
		// worth being explicit that something has to.
		if d.KeyColumn == "" {
			return errors.New("database has no after_query and no key_column, so " +
				"nothing records that a row was sent: either add after_query to mark " +
				"rows, or set key_column so Perfuse can remember which keys it has " +
				"already seen")
		}
	}

	if d.PollInterval < time.Second {
		return fmt.Errorf("database.poll_interval is %s: under a second this is a "+
			"busy loop against the database rather than a poll", d.PollInterval)
	}
	if d.BatchSize < 1 {
		return fmt.Errorf("database.batch_size is %d, so no rows would be read", d.BatchSize)
	}
	if d.BatchSize > 10000 {
		return fmt.Errorf("database.batch_size is %d: a batch that large is read into "+
			"memory and then sent at once, which floods the receiver it is sent to",
			d.BatchSize)
	}
	if d.MaxAttempts < 1 {
		return fmt.Errorf("database.max_attempts is %d, so a row would be quarantined "+
			"before it was ever tried", d.MaxAttempts)
	}
	if d.QueryTimeout <= 0 {
		return errors.New("database.query_timeout must be positive: without it a poll " +
			"blocked on a lock never returns and the channel stops silently")
	}
	return nil
}

// Warnings reports things worth saying at every start.
func (d *DatabaseSource) Warnings() []string {
	var out []string

	if looksLikeAPassword(d.DSN) {
		out = append(out, "the database DSN appears to contain a password as literal "+
			"text. Channels are meant to live in version control, so prefer ${ENV_VAR}")
	}
	if d.AfterQuery == "" {
		out = append(out, "no after_query is set, so nothing in the database records "+
			"that a row was sent. Perfuse tracks keys itself, which works, but the "+
			"record is lost if its database is rebuilt and every row would be resent")
	}
	if !strings.Contains(strings.ToLower(d.Query), "order by") {
		// Without ORDER BY the database may return rows in any order, and for
		// clinical events order is meaning: an admission before its discharge.
		out = append(out, "database.query has no ORDER BY, so rows may arrive in any "+
			"order the database finds convenient. Order the query by whatever "+
			"establishes sequence, usually the insert time or the key")
	}
	if d.MaxAttempts > 10 {
		out = append(out, fmt.Sprintf("max_attempts is %d, so a row that cannot be "+
			"processed holds up the rows behind it for %d polls before the channel "+
			"moves on", d.MaxAttempts, d.MaxAttempts))
	}
	return out
}

// Validate checks a database destination.
func (d *DatabaseDestination) Validate() error {
	if d.Timeout == 0 {
		d.Timeout = 30 * time.Second
	}
	if d.MaxOpenConns == 0 {
		d.MaxOpenConns = 4
	}

	if d.Driver == "" {
		return errors.New("database.driver is required (" + driverList() + ")")
	}
	if _, ok := SupportedDrivers[d.Driver]; !ok {
		return fmt.Errorf("unsupported database driver %q: supported drivers are %s",
			d.Driver, driverList())
	}
	if strings.TrimSpace(d.DSN) == "" {
		return errors.New("database.dsn is required")
	}
	if strings.TrimSpace(d.Statement) == "" {
		return errors.New("database.statement is required")
	}
	if selectPattern.MatchString(d.Statement) {
		return errors.New("database.statement is a SELECT, so the message would be " +
			"read against and then discarded, and the destination would report every " +
			"message as delivered while writing nothing")
	}

	if strings.Contains(d.Statement, "${") {
		// The whole point of params is that values are bound, not pasted. A template
		// in the SQL means the value becomes part of the statement, and then a
		// patient named O'Brien is a syntax error and a patient named something
		// worse is an injection.
		return errors.New("database.statement contains a ${...} reference: values " +
			"belong in params, bound as parameters, not built into the SQL. A name " +
			"containing an apostrophe would break the statement, and a hostile value " +
			"would rewrite it")
	}

	// The placeholder style has to match the driver, and this is checked here rather than left to the server.
	//
	// Perfuse deliberately does not rewrite placeholders - a translation layer that got one wrong would bind a patient's name to the
	// wrong column and produce data that looks valid. The cost of that decision is that a mismatch is only found when a message
	// arrives, and what the server says then is "syntax error at or near ," naming a statement somebody did write, which sends them
	// looking for a typo rather than at the dialect.
	//
	// Found by sending a question-mark statement to a real PostgreSQL. The decision not to rewrite is right; discovering it at three
	// in the morning is not.
	if problem := placeholderStyleMismatch(d.Driver, d.Statement); problem != "" {
		return errors.New(problem)
	}

	n := countPlaceholders(d.Statement)
	if n > 0 && n != len(d.Params) {
		return fmt.Errorf("database.statement has %d placeholder(s) and %d param(s): "+
			"a mismatch binds values to the wrong columns, which writes data that "+
			"looks valid", n, len(d.Params))
	}
	if n == 0 && len(d.Params) > 0 {
		return fmt.Errorf("database has %d param(s) but the statement has no "+
			"placeholders, so none of them would be used", len(d.Params))
	}
	if n == 0 && len(d.Params) == 0 {
		return errors.New("database.statement has no placeholders, so it writes the " +
			"same fixed row for every message and none of the message reaches the " +
			"database")
	}

	for i, p := range d.Params {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("database.params[%d] is empty", i)
		}
	}
	if d.Timeout <= 0 {
		return errors.New("database.timeout must be positive")
	}
	if d.MaxOpenConns < 1 {
		return fmt.Errorf("database.max_open_conns is %d", d.MaxOpenConns)
	}
	return nil
}

// Warnings reports things worth saying about a database destination.
func (d *DatabaseDestination) Warnings() []string {
	var out []string
	if looksLikeAPassword(d.DSN) {
		out = append(out, "the database DSN appears to contain a password as literal "+
			"text. Prefer ${ENV_VAR}, since channels are meant to live in git")
	}
	return out
}

// countPlaceholders counts bind placeholders, whichever dialect they are in.
//
// A ? inside a quoted string would be counted wrongly, which is why the mismatch
// it can cause is an error message rather than a silent reordering.
func countPlaceholders(stmt string) int {
	// Named placeholders: $1..$n and @p1..@pn. Count distinct, since a statement may
	// legitimately use the same value twice.
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$\d+|@p\d+`).FindAllString(stmt, -1) {
		seen[strings.ToLower(m)] = true
	}
	if len(seen) > 0 {
		return len(seen)
	}
	return strings.Count(stmt, "?")
}

// looksLikeAPassword guesses whether a DSN carries a literal credential.
func looksLikeAPassword(dsn string) bool {
	if strings.Contains(dsn, "${") {
		return false
	}
	lower := strings.ToLower(dsn)
	for _, marker := range []string{"password=", "pwd=", "passwd="} {
		if i := strings.Index(lower, marker); i >= 0 {
			rest := lower[i+len(marker):]
			if rest != "" && !strings.HasPrefix(rest, ";") && !strings.HasPrefix(rest, " ") {
				return true
			}
		}
	}
	// user:secret@host
	if i := strings.Index(dsn, "://"); i >= 0 {
		authority := dsn[i+3:]
		if at := strings.Index(authority, "@"); at > 0 {
			if strings.Contains(authority[:at], ":") {
				return true
			}
		}
	}
	return false
}

func driverList() string {
	names := make([]string, 0, len(SupportedDrivers))
	for k := range SupportedDrivers {
		names = append(names, k)
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// placeholderStyleMismatch reports a statement written in the wrong dialect's placeholders, or an empty string.
//
// Only obvious cases. A question mark can appear inside a quoted string, and a statement mixing styles might have a reason nobody
// here has thought of, so this refuses only when the statement uses a style the driver certainly cannot read and none that it can.
func placeholderStyleMismatch(driver, stmt string) string {
	hasDollar := regexp.MustCompile(`\$\d+`).MatchString(stmt)
	hasAtP := regexp.MustCompile(`@p\d+`).MatchString(stmt)
	hasQuestion := strings.Contains(stmt, "?")

	switch driver {
	case "postgres", "pgx":
		if hasQuestion && !hasDollar {
			return "database.statement uses ? placeholders and the postgres driver needs $1, $2 and so on. " +
				"Perfuse does not rewrite them on purpose: a translation layer that got one wrong would bind a value to the " +
				"wrong column and write data that looks valid"
		}
	case "mysql", "sqlite", "sqlite3":
		if hasDollar && !hasQuestion {
			return "database.statement uses $1 placeholders and the " + driver + " driver needs ?"
		}

		if hasAtP && !hasQuestion {
			return "database.statement uses @p1 placeholders and the " + driver + " driver needs ?"
		}
	case "sqlserver", "mssql":
		if hasQuestion && !hasAtP {
			return "database.statement uses ? placeholders and the " + driver + " driver needs @p1, @p2 and so on"
		}

		if hasDollar && !hasAtP {
			return "database.statement uses $1 placeholders and the " + driver + " driver needs @p1, @p2 and so on"
		}
	}

	return ""
}
