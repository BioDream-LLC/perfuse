package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/sqldb"
)

// templateRefPattern matches a ${column} reference in a message template.
var templateRefPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

// databasePoller reads rows from a database and feeds them into the channel.
//
// The design is shaped almost entirely by one failure. A database reader that meets
// a row it cannot process, and retries it forever, is the single commonest way a
// Mirth channel stalls. Nothing looks broken: the channel is started, the queue is
// empty, the log repeats. But the feed has stopped, every row behind the bad one
// waits for a row that will never succeed, and the first anyone knows is a ward
// asking why results stopped arriving overnight.
//
// So a row here gets a bounded number of attempts and is then quarantined, loudly,
// and the poll moves on. Quarantining one row and alerting is worse than processing
// it and better than every other option, which is the whole calculation.
type databasePoller struct {
	ch  *Channel
	cfg *config.DatabaseSource
	db  *sql.DB
	log *slog.Logger

	// seen remembers keys already processed, for the case where the database itself
	// records nothing. Bounded, because an interface engine that grows a set forever
	// is a different outage six months out.
	mu         sync.Mutex
	seen       map[string]time.Time
	attempts   map[string]int
	quarantine map[string]string
	stats      DatabaseSourceStats

	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

// DatabaseSourceStats reports what the poller has done.
type DatabaseSourceStats struct {
	Polls        int64     `json:"polls"`
	RowsRead     int64     `json:"rowsRead"`
	Accepted     int64     `json:"accepted"`
	Quarantined  int64     `json:"quarantined"`
	PollFailures int64     `json:"pollFailures"`
	LastPoll     time.Time `json:"lastPoll,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
}

// maxSeenKeys bounds the de-duplication set.
const maxSeenKeys = 100_000

// startDatabaseSource opens the pool and begins polling.
func (c *Channel) startDatabaseSource() error {
	cfg := c.cfg.Source.Database
	if cfg == nil {
		return fmt.Errorf("channel %q has a database source but no database block", c.cfg.Name)
	}

	db, err := sqldb.Open(cfg.Driver, cfg.DSN, 2, cfg.QueryTimeout)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		// The DSN is redacted because this error is shown in the interface, written to
		// the log and stored against the channel.
		return fmt.Errorf("channel %q cannot reach the %s database at %s: %w",
			c.cfg.Name, sqldb.Describe(cfg.Driver), sqldb.Redact(cfg.DSN), err)
	}

	p := &databasePoller{
		ch: c, cfg: cfg, db: db, log: c.log,
		seen:       make(map[string]time.Time),
		attempts:   make(map[string]int),
		quarantine: make(map[string]string),
		stop:       make(chan struct{}),
	}
	c.poller = p

	for _, w := range cfg.Warnings() {
		c.log.Warn("database source", "channel", c.cfg.Name, "warning", w)
	}
	c.log.Info("polling a database",
		"channel", c.cfg.Name,
		"database", sqldb.Describe(cfg.Driver),
		"every", cfg.PollInterval,
		"batch", cfg.BatchSize)

	p.wg.Add(1)
	go p.loop()
	return nil
}

// stopDatabaseSource stops polling and closes the pool.
func (c *Channel) stopDatabaseSource() error {
	if c.poller == nil {
		return nil
	}
	c.poller.close()
	c.poller = nil
	return nil
}

func (p *databasePoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
	if p.db != nil {
		_ = p.db.Close()
	}
}

// loop polls until stopped.
func (p *databasePoller) loop() {
	defer p.wg.Done()

	// Polled once immediately. Waiting a full interval before the first read means a
	// channel that has just been started looks broken for ten seconds, which is long
	// enough for somebody to restart it again.
	p.pollOnce()

	t := time.NewTicker(p.cfg.PollInterval)
	defer t.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.pollOnce()
		}
	}
}

// pollOnce reads a batch and processes it.
func (p *databasePoller) pollOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.QueryTimeout)
	defer cancel()

	p.mu.Lock()
	p.stats.Polls++
	p.stats.LastPoll = time.Now()
	p.mu.Unlock()

	rows, err := p.db.QueryContext(ctx, p.cfg.Query)
	if err != nil {
		p.mu.Lock()
		p.stats.PollFailures++
		p.stats.LastError = err.Error()
		p.mu.Unlock()
		// Not fatal. A database being briefly unreachable is normal, and stopping the
		// channel would need a person to start it again.
		p.ch.incMetric(metrics.DatabasePollFailures)
		p.log.Error("the database poll failed",
			"channel", p.ch.cfg.Name, "error", err)
		return
	}

	batch, err := p.readBatch(rows)
	if err != nil {
		p.mu.Lock()
		p.stats.PollFailures++
		p.stats.LastError = err.Error()
		p.mu.Unlock()
		p.ch.incMetric(metrics.DatabasePollFailures)
		p.log.Error("reading the polled rows failed",
			"channel", p.ch.cfg.Name, "error", err)
		return
	}

	if len(batch) == 0 {
		return
	}

	p.mu.Lock()
	p.stats.RowsRead += int64(len(batch))
	p.mu.Unlock()
	p.ch.addMetric(metrics.DatabaseRowsRead, int64(len(batch)))

	for _, row := range batch {
		select {
		case <-p.stop:
			return
		default:
		}
		p.processRow(ctx, row)
	}
}

// polledRow is one row, with its key and its columns.
type polledRow struct {
	key     string
	columns map[string]string
}

// readBatch reads up to BatchSize rows.
func (p *databasePoller) readBatch(rows *sql.Rows) ([]polledRow, error) {
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var out []polledRow

	for rows.Next() {
		if len(out) >= p.cfg.BatchSize {
			// Stopped at the batch size rather than reading everything. The first poll
			// against a table somebody has been filling for a year would otherwise read
			// all of it into memory and then send all of it at a live receiver.
			break
		}

		holders := make([]any, len(names))
		for i := range holders {
			holders[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}

		row := polledRow{columns: make(map[string]string, len(names))}
		for i, name := range names {
			raw := holders[i].(*sql.RawBytes)
			// A NULL column becomes an empty string here, and the template layer decides
			// what that means. RawBytes has to be copied before the next Next().
			row.columns[strings.ToLower(name)] = string(*raw)
		}
		if p.cfg.KeyColumn != "" {
			row.key = row.columns[strings.ToLower(p.cfg.KeyColumn)]
		}
		out = append(out, row)
	}

	return out, rows.Err()
}

// processRow turns one row into a message and hands it to the channel.
func (p *databasePoller) processRow(ctx context.Context, row polledRow) {
	key := row.key

	if key != "" {
		p.mu.Lock()
		_, quarantined := p.quarantine[key]
		_, already := p.seen[key]
		p.mu.Unlock()

		if quarantined {
			// Silent by design after the first report. A quarantined row matched by the
			// query still appears on every poll, and logging it every ten seconds would
			// bury everything else in the log within a day.
			return
		}
		if already {
			return
		}
	}

	if p.cfg.KeyColumn != "" && key == "" {
		// A row with no key cannot be marked, de-duplicated or quarantined, so
		// processing it guarantees it is sent again on the next poll forever.
		p.log.Error("a polled row has no value in the key column, so it cannot be "+
			"marked as processed and would be resent on every poll. It is being skipped",
			"channel", p.ch.cfg.Name, "key_column", p.cfg.KeyColumn)
		p.mu.Lock()
		p.stats.PollFailures++
		p.mu.Unlock()
		return
	}

	raw, err := p.buildMessage(row)
	if err != nil {
		p.failRow(ctx, key, err)
		return
	}

	if _, err := p.ch.handle(ctx, raw); err != nil {
		p.failRow(ctx, key, err)
		return
	}

	// Read straight after handle, which is safe because a database source has one
	// poller and it processes rows one at a time. It is the only thing feeding this
	// channel, so there is no second message whose outcome could be read here.
	switch outcome := p.ch.lastOutcome.get(); outcome {
	case Failed, Unparseable:
		p.failRow(ctx, key, fmt.Errorf("the channel reported %s", outcome))
		return
	}

	// Accepted. The row is marked only now, after the message is durably held by the
	// channel, so a crash between the two resends rather than loses. At-least-once is
	// the right choice here: a duplicate ADT can be reconciled by MSH-10, and a lost
	// admission cannot be reconciled at all.
	p.markProcessed(ctx, key)

	p.mu.Lock()
	p.stats.Accepted++
	delete(p.attempts, key)
	p.mu.Unlock()
}

// failRow counts a failure and quarantines the row once it has had its attempts.
func (p *databasePoller) failRow(ctx context.Context, key string, cause error) {
	if key == "" {
		p.log.Error("a polled row could not be processed",
			"channel", p.ch.cfg.Name, "error", cause)
		return
	}

	p.mu.Lock()
	p.attempts[key]++
	n := p.attempts[key]
	p.mu.Unlock()

	if n < p.cfg.MaxAttempts {
		p.log.Warn("a polled row could not be processed and will be tried again",
			"channel", p.ch.cfg.Name, "key", key,
			"attempt", n, "of", p.cfg.MaxAttempts, "error", cause)
		return
	}

	p.mu.Lock()
	p.quarantine[key] = cause.Error()
	p.stats.Quarantined++
	p.stats.LastError = cause.Error()
	delete(p.attempts, key)
	p.mu.Unlock()

	// The loudest line this package produces, and deliberately so. This is the moment
	// a row is abandoned, and it is the moment the alternative implementation would
	// have stalled the feed instead. Somebody has to look at it, so it says what
	// happened, what was done about it, and what to do next.
	p.log.Error("a polled row has been quarantined after failing every attempt, and "+
		"the channel has moved on to the rows behind it. Nothing else is blocked. This "+
		"row will not be tried again until the channel restarts, and it has NOT been "+
		"marked as processed in the database, so fix the row or the template and restart",
		"channel", p.ch.cfg.Name, "key", key,
		"attempts", p.cfg.MaxAttempts, "error", cause)

	p.ch.count(func(s *Stats) { s.DatabaseQuarantined++ })
	p.ch.incMetric(metrics.DatabaseQuarantined)
}

// markProcessed runs AfterQuery, or records the key locally when there is none.
func (p *databasePoller) markProcessed(ctx context.Context, key string) {
	if p.cfg.AfterQuery == "" {
		p.rememberKey(key)
		return
	}

	execCtx, cancel := context.WithTimeout(ctx, p.cfg.QueryTimeout)
	defer cancel()

	if _, err := p.db.ExecContext(execCtx, p.cfg.AfterQuery, key); err != nil {
		// This is the dangerous failure, so it is said plainly. The message has been
		// accepted and the database still shows the row as pending, which means the next
		// poll sends it again. Remembering the key locally stops the duplicate for as
		// long as this process lives, and that is the honest limit of what can be done
		// from here.
		p.log.Error("the message was accepted but the statement that marks the row as "+
			"processed failed, so the database still shows this row as pending. Perfuse "+
			"will not resend it while this process is running, but a restart before the "+
			"row is marked will send it a second time. Check after_query and the "+
			"permissions of the account in the DSN",
			"channel", p.ch.cfg.Name, "key", key, "error", err)
		p.rememberKey(key)
		p.mu.Lock()
		p.stats.LastError = err.Error()
		p.mu.Unlock()
		return
	}
	// Remembered as well as marked. Belt and braces costs a map entry and covers the
	// case where after_query succeeds but does not actually exclude the row, which is
	// a mistake in the WHERE clause nobody notices until the duplicates arrive.
	p.rememberKey(key)
}

// rememberKey records a processed key, bounded.
func (p *databasePoller) rememberKey(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.seen[key] = time.Now()

	if len(p.seen) <= maxSeenKeys {
		return
	}
	// Oldest half dropped when the set gets too big. Unbounded growth would be a
	// different outage in six months, and the keys most likely to reappear are the
	// recent ones.
	cutoff := p.medianSeenTime()
	for k, at := range p.seen {
		if at.Before(cutoff) {
			delete(p.seen, k)
		}
	}
}

// medianSeenTime approximates the middle of the seen set. Caller holds the lock.
func (p *databasePoller) medianSeenTime() time.Time {
	var oldest, newest time.Time
	for _, at := range p.seen {
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
		if at.After(newest) {
			newest = at
		}
	}
	return oldest.Add(newest.Sub(oldest) / 2)
}

// buildMessage renders a row into an HL7 message.
func (p *databasePoller) buildMessage(row polledRow) ([]byte, error) {
	if p.cfg.Column != "" {
		val := row.columns[strings.ToLower(p.cfg.Column)]
		if strings.TrimSpace(val) == "" {
			return nil, fmt.Errorf("column %q is empty, so there is no message in this row",
				p.cfg.Column)
		}
		return []byte(normaliseSegmentBreaks(val)), nil
	}

	var missing []string
	out := templateRefPattern.ReplaceAllStringFunc(p.cfg.Template, func(m string) string {
		name := strings.ToLower(strings.TrimSpace(m[2 : len(m)-1]))

		if v, ok := row.columns[name]; ok {
			return escapeHL7Value(v)
		}
		missing = append(missing, name)
		return ""
	})

	if len(missing) > 0 {
		// An error rather than an empty field. A template naming a column the query does
		// not return is a mistake, and silently substituting nothing would put a blank
		// where a patient identifier belongs.
		return nil, fmt.Errorf("the template refers to column(s) %s, which the query "+
			"does not return. The query selects: %s",
			strings.Join(missing, ", "), strings.Join(columnNames(row), ", "))
	}

	return []byte(normaliseSegmentBreaks(out)), nil
}

// escapeHL7Value escapes the delimiters so a database value cannot restructure the
// message it is put into.
//
// This is the same class of problem as SQL injection and it is easier to hit: a
// free-text comment column containing a pipe would otherwise create fields that
// were never intended, shifting every value after it into the wrong place. The
// result parses cleanly and is wrong, which is the worst way to be wrong.
func escapeHL7Value(v string) string {
	if v == "" {
		return ""
	}
	// Order matters: the escape character has to be replaced first, or the escapes
	// introduced below would themselves be escaped.
	v = strings.ReplaceAll(v, `\`, `\E\`)
	v = strings.ReplaceAll(v, "|", `\F\`)
	v = strings.ReplaceAll(v, "^", `\S\`)
	v = strings.ReplaceAll(v, "&", `\T\`)
	v = strings.ReplaceAll(v, "~", `\R\`)
	// A newline in a database value would become a segment break and invent a
	// segment. Encoded as the HL7 line break escape instead.
	v = strings.ReplaceAll(v, "\r\n", `\X0D\`)
	v = strings.ReplaceAll(v, "\n", `\X0D\`)
	v = strings.ReplaceAll(v, "\r", `\X0D\`)
	return v
}

// normaliseSegmentBreaks makes a template's newlines into HL7 segment breaks.
//
// A YAML block scalar produces \n, and HL7 segments end with \r. Requiring the
// author to write \r in a YAML file would be a trap; every template would be
// written with newlines and produce a single unparseable segment.
func normaliseSegmentBreaks(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	return strings.TrimRight(s, "\r") + "\r"
}

func columnNames(row polledRow) []string {
	names := make([]string, 0, len(row.columns))
	for k := range row.columns {
		names = append(names, k)
	}
	sortStringsAsc(names)
	return names
}

func sortStringsAsc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Stats returns a copy of the poller counters.
func (p *databasePoller) Stats() DatabaseSourceStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Quarantined lists the rows that were abandoned, so the interface can show them.
func (p *databasePoller) Quarantined() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.quarantine))
	for k, v := range p.quarantine {
		out[k] = v
	}
	return out
}
