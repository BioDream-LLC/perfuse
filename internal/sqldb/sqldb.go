// Package sqldb opens database connections for the database source and
// destination.
//
// The drivers are registered here and nowhere else, so there is exactly one place
// that decides what Perfuse can connect to.
package sqldb

import (
	"database/sql"
	"fmt"
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

// Open resolves a DSN and opens a pool.
//
// The pool is verified with a ping before it is returned. database/sql opens
// connections lazily, so without this a channel with a wrong password starts
// cleanly, reports itself healthy, and fails on the first message hours later when
// nobody is connecting the two events.
func Open(driver, dsn string, maxOpen int, timeout time.Duration) (*sql.DB, error) {
	registered, ok := driverNames[driver]
	if !ok {
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}

	resolved, err := ResolveDSN(dsn)
	if err != nil {
		return nil, err
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

	return db, nil
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
