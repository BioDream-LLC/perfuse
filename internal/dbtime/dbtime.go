// Package dbtime formats timestamps so that sorting them as text sorts them as time.
//
// Every store in Perfuse keeps timestamps as TEXT in SQLite and relies on string comparison for
// ordering and for range queries: ORDER BY received_at DESC, WHERE expires_at < ?, WHERE at >= ?.
// That is a reasonable design - it keeps a database somebody can read with the sqlite3 shell - but it
// only works if the text form is fixed width.
//
// time.RFC3339Nano is not. Its documentation says it removes trailing zeros from the seconds field, so
// the same clock produces strings of different lengths:
//
//	20:56:33.12345Z    <- 123450 microseconds, trailing zero dropped
//	20:56:33.123456Z   <- 123456 microseconds
//
// Compared as text, the first is *greater*, because after the shared prefix "12345" it has 'Z' where
// the second has '6', and 'Z' sorts above every digit. So the earlier instant sorts later. Roughly one
// adjacent pair in ten is affected, which is why it presented as an intermittent test failure rather
// than as an obviously broken feature.
//
// What it actually broke: the audit log and the message browser both order by a timestamp and can
// therefore list entries from the same second in the wrong order. An audit trail is a compliance
// artefact and a message browser is how somebody reconstructs what happened, so in both cases the
// order is the point.
//
// Layout below uses .000000000 rather than .999999999. Both print nanoseconds; the difference is that
// zeros are significant in the first and dropped in the second.
package dbtime

import "time"

// Layout is the stored form: RFC 3339 with a fixed nine-digit fraction.
//
// Fixed width is the whole requirement. Any layout where every timestamp is the same length and the
// fields run most-significant first would do; this one is chosen because it stays valid RFC 3339, so
// existing readers and the sqlite3 shell are unaffected.
const Layout = "2006-01-02T15:04:05.000000000Z07:00"

// Format renders a timestamp for storage, in UTC.
//
// UTC is forced rather than assumed. A timestamp written with an offset would compare against one
// written in UTC by comparing the text, and "+01:00" against "Z" is not a comparison of instants.
func Format(t time.Time) string {
	return t.UTC().Format(Layout)
}

// Parse reads a stored timestamp.
//
// Uses RFC3339Nano deliberately, which accepts any number of fractional digits. That means rows
// written before this package existed still read correctly - the fix changes what is written, and old
// data does not become unreadable.
func Parse(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// SortsCorrectly reports whether text ordering agrees with chronological ordering for two instants.
//
// Exported for tests, in this package rather than in each store's, because the property belongs to the
// format and proving it once is enough.
func SortsCorrectly(a, b time.Time) bool {
	fa, fb := Format(a), Format(b)
	switch {
	case a.Before(b):
		return fa < fb
	case b.Before(a):
		return fb < fa
	default:
		return fa == fb
	}
}
