// Package sqldb opens database connections for the database source and
// destination.
//
// The drivers are registered here and nowhere else, so there is exactly one place
// that decides what Perfuse can connect to.
package sqldb

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	// Registered for their side effects. All three are pure Go, which is what keeps
	// Perfuse a single static binary with no JVM, no ODBC layer and no native
	// client library to install on the server.
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

// driverNames maps the name used in a channel to the registered driver.
//
// The channel-facing names are deliberately plain. Somebody writing a channel
// should not have to know that the SQL Server driver is called "sqlserver" by one
// project and "mssql" by another.
var driverNames = map[string]string{
	"postgres":  "postgres",
	"mysql":     "mysql",
	"sqlserver": "sqlserver",
	"sqlite":    "sqlite",
}

// envPattern matches a ${VAR} reference in a DSN.
var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Open resolves a DSN and opens a pool. It does not connect.
//
// database/sql opens connections lazily, so a wrong password or an unreachable host is
// not detected here. Both callers ping immediately afterwards and refuse the channel at
// load, because otherwise it would start cleanly, report itself healthy, and fail on
// the first message hours later when nobody is connecting the two events. The ping
// belongs to them and not to this function: their errors name the channel and carry a
// redacted DSN, which is what an operator needs and what this function cannot know.
//
// The timeout bounds SQLite's busy_timeout. It is not a connection timeout; drivers
// take that in the DSN.
func Open(driver, dsn string, maxOpen int, timeout time.Duration) (*sql.DB, error) {
	registered, ok := driverNames[driver]
	if !ok {
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}

	resolved, err := ResolveDSN(dsn)
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// SQLite needs two pragmas that no other driver does, and this package is the third
	// place in the tree to need them. internal/sqlitedb and internal/store each set
	// journal_mode(WAL) and a busy_timeout, with a written rationale; this function did
	// not, so a database channel pointed at a SQLite file got the default rollback
	// journal and a zero busy timeout. Any concurrent write then returns SQLITE_BUSY
	// immediately instead of waiting, which is a failure that appears only under load
	// and looks like a flake. It was found as exactly that: a CI failure in
	// TestDatabaseSourceMarksRowsSoTheyAreNotResent, where the test read a row while
	// the channel's own after_query updated it.
	if registered == "sqlite" {
		resolved = withSQLitePragmas(resolved, timeout)
	}

	db, err := sql.Open(registered, resolved)
	if err != nil {
		// Never wrap the DSN into the error. It contains the password.
		return nil, fmt.Errorf("opening the %s connection: %w", driver, err)
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	// Bounded lifetime because a firewall or a load balancer between here and the
	// database will silently drop an idle connection, and the failure then lands on
	// a message rather than on a reconnect.
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// No ping here, deliberately. Both callers - the database source and the database
	// destination - ping immediately after this returns, and their errors are better
	// than any this function could produce: they name the channel and include the DSN
	// redacted, so an operator with two database destinations can tell which one failed.
	// A ping here pre-empts that with a message carrying neither.
	//
	// The doc comment above this function used to claim the ping happened here, which
	// was a description of what the callers do attached to the function that does not.
	// Adding one to make the comment true replaced two good errors with one poor one,
	// and TestDatabaseSenderRefusesAnUnreachableDatabaseAtLoad failed on exactly that.

	return db, nil
}

// withSQLitePragmas adds WAL and a busy timeout to a SQLite DSN, leaving any the
// caller already set alone.
//
// Appending unconditionally would override a deliberate choice, and modernc's driver
// applies repeated _pragma values in order, so a duplicate silently wins over the
// caller's.
func withSQLitePragmas(dsn string, timeout time.Duration) string {
	// A bare path is a valid SQLite DSN and is not a URL, so it is turned into one
	// before query parameters can be attached.
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}

	base, query, _ := strings.Cut(dsn, "?")
	existing, err := url.ParseQuery(query)
	if err != nil {
		// An unparseable query is the caller's to keep. Silently discarding it would be
		// worse than not adding the pragmas.
		return dsn
	}

	have := strings.Join(existing["_pragma"], " ")
	if !strings.Contains(have, "journal_mode") {
		existing.Add("_pragma", "journal_mode(WAL)")
	}
	if !strings.Contains(have, "busy_timeout") {
		existing.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", timeout.Milliseconds()))
	}

	return base + "?" + existing.Encode()
}

// ResolveDSN substitutes ${VAR} references from the environment.
//
// This exists so a channel file can name a credential without containing one.
// Channels are meant to live in version control, and a DSN with a literal password
// in it is a credential in git that will outlive everyone who remembers putting it
// there.
func ResolveDSN(dsn string) (string, error) {
	var missing []string

	out := envPattern.ReplaceAllStringFunc(dsn, func(m string) string {
		name := envPattern.FindStringSubmatch(m)[1]
		val, ok := os.LookupEnv(name)
		if !ok || val == "" {
			missing = append(missing, name)
			return ""
		}
		return val
	})

	if len(missing) > 0 {
		// Named, because the alternative is a connection failure that looks like a
		// wrong password. The variable name is not a secret; its value is.
		return "", fmt.Errorf("the database DSN refers to environment variable(s) %s, "+
			"which are not set (or are empty). Set them in the environment Perfuse runs "+
			"in; the channel file is right to name them rather than contain them",
			strings.Join(missing, ", "))
	}
	return out, nil
}

// Redact removes the credential from a DSN so it can go in a log or an error.
//
// Errors from a database connector are shown in the interface, written to the log
// and stored against a message. A DSN reaching any of those is a password reaching
// all of them.
func Redact(dsn string) string {
	out := dsn

	// key=value forms: postgres and sqlserver.
	for _, key := range []string{"password", "pwd", "passwd"} {
		re := regexp.MustCompile(`(?i)\b` + key + `\s*=\s*('[^']*'|"[^"]*"|[^\s;]*)`)
		out = re.ReplaceAllString(out, key+"=****")
	}

	// URL forms: user:secret@host.
	out = regexp.MustCompile(`(://[^:/?#\s]+):([^@/\s]*)@`).ReplaceAllString(out, "$1:****@")

	// The mysql form, which has no scheme at all: user:secret@tcp(host:3306)/db.
	//
	// Guarded on the absence of "://" because the pattern otherwise matches the
	// scheme of a URL DSN with no password - "postgres://interface@host" became
	// "postgres:****@host", which is not a redaction, it is corruption of the one
	// string somebody needs in order to fix the connection.
	if !strings.Contains(out, "://") {
		out = regexp.MustCompile(`^([^:/@\s]+):([^@\s]*)@`).ReplaceAllString(out, "$1:****@")
	}

	return out
}

// Describe returns a short description of a driver, for the interface.
func Describe(driver string) string {
	switch driver {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL or MariaDB"
	case "sqlserver":
		return "Microsoft SQL Server"
	case "sqlite":
		return "SQLite"
	}
	return driver
}

// Placeholder returns the bind placeholder for a driver at position n, counting
// from one.
//
// Used only where Perfuse builds a statement itself. A statement written by a
// person is never rewritten: guessing wrong would bind a value to the wrong column
// and write data that looks entirely valid.
func Placeholder(driver string, n int) string {
	switch driver {
	case "postgres":
		return fmt.Sprintf("$%d", n)
	case "sqlserver":
		return fmt.Sprintf("@p%d", n)
	default:
		return "?"
	}
}
