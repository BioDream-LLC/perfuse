package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/sqldb"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// DatabaseSender writes each message to a database.
//
// The whole of the interesting part is that values are bound as parameters and
// never built into the SQL. That is usually framed as an injection defence, and it
// is one, but the everyday reason is duller and hits sooner: O'Brien is a common
// name, and a query assembled by concatenation breaks on the apostrophe. A feed
// that works for a month and then fails for one patient is harder to diagnose than
// one that never worked.
type DatabaseSender struct {
	name   string
	cfg    *config.DatabaseDestination
	db     *sql.DB
	log    *slog.Logger
	params []paramSource

	mu    sync.Mutex
	stats DatabaseStats
}

// DatabaseStats reports what the destination has done.
type DatabaseStats struct {
	Written   int64     `json:"written"`
	Failed    int64     `json:"failed"`
	NullBound int64     `json:"nullBound"`
	LastError string    `json:"lastError,omitempty"`
	LastWrite time.Time `json:"lastWrite,omitempty"`
}

// paramSource describes where one bound value comes from.
type paramSource struct {
	// path is an HL7 path such as PID-3.1.
	path string
	// literal is used instead when the param was quoted in the configuration.
	literal string
	isLit   bool
}

// NewDatabaseSender opens the pool and prepares the parameter bindings.
func NewDatabaseSender(d config.Destination, log *slog.Logger) (*DatabaseSender, error) {
	if d.Database == nil {
		return nil, fmt.Errorf("destination %q has type database but no database block", d.Name)
	}
	if log == nil {
		log = slog.Default()
	}
	cfg := d.Database

	db, err := sqldb.Open(cfg.Driver, cfg.DSN, cfg.MaxOpenConns, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("destination %q: %w", d.Name, err)
	}

	// Verified now rather than on the first message. database/sql connects lazily,
	// so a wrong password otherwise produces a channel that starts, reports itself
	// healthy, and fails hours later when nobody connects the two events.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("destination %q cannot reach the %s database at %s: %w",
			d.Name, sqldb.Describe(cfg.Driver), sqldb.Redact(cfg.DSN), err)
	}

	s := &DatabaseSender{name: d.Name, cfg: cfg, db: db, log: log}
	for i, p := range cfg.Params {
		src := parseParam(p)
		if !src.isLit {
			// Paths are checked now rather than per message. A misspelled path would
			// otherwise bind NULL for every message, and a column of nulls is a thing
			// somebody discovers months later.
			if _, err := transform.ParsePath(src.path); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("destination %q params[%d]: %q is not a field path: %w",
					d.Name, i, src.path, err)
			}
		}
		s.params = append(s.params, src)
	}

	for _, w := range cfg.Warnings() {
		log.Warn("database destination", "destination", d.Name, "warning", w)
	}
	return s, nil
}

// parseParam decides whether a param is a literal or an HL7 path.
func parseParam(p string) paramSource {
	p = strings.TrimSpace(p)

	// A quoted param is a constant, which is how a channel writes its own name or a
	// source system code into a row alongside the message data.
	if len(p) >= 2 && ((p[0] == '\'' && p[len(p)-1] == '\'') ||
		(p[0] == '"' && p[len(p)-1] == '"')) {
		return paramSource{literal: p[1 : len(p)-1], isLit: true}
	}
	return paramSource{path: p}
}

// Describe names the destination for the interface and the log.
//
// The DSN is redacted, because this string appears on the channel page and in
// startup lines, and a DSN normally carries a password.
func (s *DatabaseSender) Describe() string {
	return fmt.Sprintf("database %s %s", sqldb.Describe(s.cfg.Driver), sqldb.Redact(s.cfg.DSN))
}

// Send writes one message.
func (s *DatabaseSender) Send(ctx context.Context, raw []byte) error {
	args := make([]any, 0, len(s.params))
	var nulls []string

	for i, p := range s.params {
		if p.isLit {
			args = append(args, p.literal)
			continue
		}
		// Resolved through the same code the transformation layer uses, so PID-3.1
		// here addresses exactly what PID-3.1 addresses in a transformation. A second
		// implementation of field addressing would eventually disagree with the first.
		val, err := transform.ValueAt(raw, p.path)
		if err != nil {
			// The message parsed on the way in, so reaching here means a transformation
			// produced something invalid. That is a channel fault, not a database one.
			s.record(false, err)
			return fmt.Errorf("reading %s to write to the database: %w", p.path, err)
		}
		if val == "" {
			// Bound as NULL rather than as an empty string. In a clinical table those
			// mean different things: NULL is "the message did not say", and '' is "the
			// message said it was blank". Collapsing them loses the distinction
			// permanently, and a report cannot tell them apart afterwards.
			args = append(args, nil)
			nulls = append(nulls, fmt.Sprintf("params[%d] %s", i, p.path))
			continue
		}
		args = append(args, val)
	}

	if len(nulls) > 0 {
		s.mu.Lock()
		s.stats.NullBound += int64(len(nulls))
		s.mu.Unlock()
		// Logged rather than failed, because an absent optional field is normal HL7
		// and refusing the message would be worse. Counted so a mapping that is always
		// null shows up as a number rather than as a column of nulls somebody notices
		// next year.
		s.log.Debug("database destination bound a null",
			"destination", s.name, "fields", strings.Join(nulls, ", "))
	}

	writeCtx := ctx
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}

	res, err := s.db.ExecContext(writeCtx, s.cfg.Statement, args...)
	if err != nil {
		s.record(false, err)
		if errors.Is(err, context.DeadlineExceeded) {
			// Distinguished because the remedy is different: a timeout is usually a lock
			// held by something else, and retrying immediately makes it worse.
			return fmt.Errorf("the database did not respond within %s, which usually "+
				"means the row or table is locked by something else: %w", s.cfg.Timeout, err)
		}
		return fmt.Errorf("writing to the %s database: %w", sqldb.Describe(s.cfg.Driver), err)
	}

	// Checked where the driver reports it. A statement that succeeds and affects
	// nothing is the quiet failure this connector has to catch: an UPDATE whose WHERE
	// clause matched no row reports success, the message is recorded as delivered,
	// and nothing was written.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		s.record(false, errors.New("no rows affected"))
		return errors.New("the statement succeeded but changed no rows, so the message " +
			"was not written. For an UPDATE this normally means the WHERE clause matched " +
			"nothing; treating it as delivered would lose the message silently")
	}

	s.record(true, nil)
	return nil
}

func (s *DatabaseSender) record(ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.stats.Written++
		s.stats.LastWrite = time.Now()
		return
	}
	s.stats.Failed++
	if err != nil {
		s.stats.LastError = err.Error()
	}
}

// Stats returns a copy of the counters.
func (s *DatabaseSender) Stats() DatabaseStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close releases the pool.
func (s *DatabaseSender) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
