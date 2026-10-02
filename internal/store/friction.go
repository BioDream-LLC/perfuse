package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Friction is what the product refused to do, recorded so that difficulty using it can be counted rather than guessed at.
//
// Why this exists. Every judgement in this repository about whether Perfuse is easy to use comes from reasoning about software written
// here, which is the same circularity that let a thousand self-agreeing SAML tests pass while no real identity provider could sign
// anybody in. The queue carried an item for three days saying to hand the binary to one real person and watch - and there is nobody to
// hand it to.
//
// The requirement was never a stranger. It was evidence that did not come from the author. A refusal is exactly that: the operator
// wanted something, the server said no, and neither party was guessing. Counting refusals is the closest thing to watching somebody
// struggle that a program can do for itself.
//
// What is deliberately not recorded: anything the operator typed. A validation message names a field and a rule, and that is the useful
// part; the value that broke the rule is very often patient data. The table holds the route, the status, and the server's own words.
type Friction struct {
	ID int64 `json:"id"`

	At time.Time `json:"at"`

	// Route is the method and path, with identifiers removed, so that twenty refusals on twenty different channels group into one
	// finding rather than twenty.
	Route string `json:"route"`

	// Status is the HTTP status. Kept separate from the message because "not found" and "will not validate" are different problems
	// that sometimes share wording.
	Status int `json:"status"`

	// Message is the server's own sentence, verbatim. Verbatim matters: a summary written later would be this author describing his
	// own error messages, which is the circularity the table exists to escape.
	Message string `json:"message"`

	// Problems is the number of per-field validation problems returned with it. A refusal naming eleven problems at once is a
	// different experience from one naming a single typo, and the count is the cheapest way to tell them apart.
	Problems int `json:"problems"`

	Username string `json:"username,omitempty"`
}

// RecordFriction appends one refusal.
//
// Best effort by design. A failure to record friction must never turn into a second error for the operator, who is already being told
// no about something else. The error is returned for tests and otherwise ignored by callers.
func (s *Store) RecordFriction(ctx context.Context, f Friction) error {
	if s == nil || s.db == nil {
		return nil
	}

	at := f.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO friction (at, route, status, message, problems, username) VALUES (?, ?, ?, ?, ?, ?)`,
		at.UTC().Format(time.RFC3339Nano), f.Route, f.Status, f.Message, f.Problems, f.Username)

	return err
}

// FrictionGroup is refusals that share a message, counted.
//
// Grouped rather than listed because the question worth answering is which message an operator hits most, and a list of two hundred
// rows answers a different one.
type FrictionGroup struct {
	Route    string    `json:"route"`
	Status   int       `json:"status"`
	Message  string    `json:"message"`
	Count    int       `json:"count"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Problems int       `json:"problems"`
}

// TopFriction returns the most frequently hit refusals, worst first.
//
// Ordered by count and then by the message, so the answer is stable. Go maps range randomly and an unstable order here would make two
// readings of the same data disagree, which is how a report stops being believed.
func (s *Store) TopFriction(ctx context.Context, limit int) ([]FrictionGroup, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT route, status, message, COUNT(*) AS n, MIN(at), MAX(at), MAX(problems)
		 FROM friction
		 GROUP BY route, status, message
		 ORDER BY n DESC, message ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	// Empty, not nil. A nil slice marshals to null and every caller then has to handle two shapes of "nothing".
	out := []FrictionGroup{}

	for rows.Next() {
		var (
			g            FrictionGroup
			first, last  string
			problemsNull sql.NullInt64
		)

		if err := rows.Scan(&g.Route, &g.Status, &g.Message, &g.Count, &first, &last, &problemsNull); err != nil {
			return nil, err
		}

		g.First = parseStoredTime(first)
		g.Last = parseStoredTime(last)
		g.Problems = int(problemsNull.Int64)

		out = append(out, g)
	}

	return out, rows.Err()
}

// FrictionCount is the total number of refusals recorded.
func (s *Store) FrictionCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM friction`).Scan(&n)

	return n, err
}

// TrimFriction keeps the most recent keep rows and deletes the rest.
//
// A table that grows without bound on a long-running server eventually becomes a reason to turn the feature off, and a feature that
// gets turned off records nothing.
func (s *Store) TrimFriction(ctx context.Context, keep int) error {
	if keep <= 0 {
		return nil
	}

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM friction WHERE id NOT IN (SELECT id FROM friction ORDER BY id DESC LIMIT ?)`, keep)

	return err
}

// parseStoredTime reads a timestamp written by RecordFriction.
func parseStoredTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}

	return time.Time{}
}

// NormaliseRoute strips identifiers out of a path so that refusals group.
//
// Without this, every refusal on a different channel is its own finding and nothing ever reaches a count above one - which would make
// the report technically accurate and useless.
func NormaliseRoute(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if looksLikeAnIdentifier(p) {
			parts[i] = "{id}"
		}
	}

	return method + " /" + strings.Join(parts, "/")
}

// looksLikeAnIdentifier guesses whether a path segment names a particular thing rather than a kind of thing.
func looksLikeAnIdentifier(segment string) bool {
	if segment == "" {
		return false
	}

	// A UUID, or anything with a digit in it, or anything long enough to be a generated name. Channel names are user-chosen and can be
	// anything, which is why length is part of the test: "channels/my-adt-feed-from-the-lab" is an identifier, "channels" is not.
	digits := 0

	for _, r := range segment {
		if r >= '0' && r <= '9' {
			digits++
		}
	}

	if digits > 0 && (len(segment) > 8 || digits >= 2) {
		return true
	}

	return strings.Count(segment, "-") >= 2 || len(segment) > 24
}

// FirstAuditAt returns when an action first happened, or nil if it never has.
//
// Used to date the steps of the first-run funnel from records the audit trail already keeps, rather than writing new milestone rows.
// That distinction is deliberate: a milestone this software records in order to describe its own ease of use is worth less than one it
// has to go and find in evidence kept for another reason.
func (s *Store) FirstAuditAt(ctx context.Context, action string) (*time.Time, error) {
	var raw sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(at) FROM audit WHERE action = ?`, action).Scan(&raw)
	if err != nil {
		return nil, err
	}

	if !raw.Valid || raw.String == "" {
		// Never happened. Distinguished from an error, because "no channel has ever been created" is the most interesting thing this
		// report can say and must not present as a fault.
		return nil, nil
	}

	at := parseStoredTime(raw.String)
	if at.IsZero() {
		return nil, nil
	}

	return &at, nil
}

// FrictionCountBefore counts refusals recorded before a moment.
//
// How many times the product said no on the way to a working channel. A count of nought would be a pleasant surprise; anything else is
// the number this whole file exists to produce.
func (s *Store) FrictionCountBefore(ctx context.Context, when time.Time) (int, error) {
	var n int

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM friction WHERE at < ?`, when.UTC().Format(time.RFC3339Nano)).Scan(&n)

	return n, err
}
