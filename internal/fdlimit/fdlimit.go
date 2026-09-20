// Package fdlimit finds out how many file descriptors this process may open, and raises the limit if it
// can.
//
// This exists because of the shape of the failure it prevents. Every inbound connection is a descriptor,
// and so is every outbound delivery in flight. A receiver that has stopped answering holds a descriptor
// for the whole retry budget - with the defaults, five attempts at a thirty second timeout plus fifteen
// seconds of backoff, so nearly three minutes. A few hundred connections against one hung receiver is
// therefore a few hundred descriptors held for minutes.
//
// Running out does not produce a tidy error in the place that ran out. It produces "too many open files"
// somewhere else entirely: the database cannot open its journal, the web interface stops accepting, a log
// file cannot be written. The engine appears to have broken in three unrelated places at once, and the
// real cause is a partner system that went quiet. That is the support call this package exists to avoid.
//
// Two things happen at startup. The soft limit is raised to the hard limit, which usually removes the
// problem outright - the soft default is often a few hundred while the hard limit is tens of thousands.
// Then whatever is available is reported, so the engine can bound its own concurrency inside it rather
// than discovering it by hitting it.
package fdlimit

// Report says what the limits were and what they became.
type Report struct {
	// Soft is the effective limit after any raise: the number of descriptors this process may open.
	Soft uint64

	// Hard is the ceiling the soft limit may be raised to without privileges.
	Hard uint64

	// Before is the soft limit as found, so a log line can say what changed.
	Before uint64

	// Raised is whether the soft limit was moved.
	Raised bool

	// Unlimited is set where the platform has no such limit, which is Windows.
	//
	// Reported rather than faked as a large number, because a caller deciding how much concurrency to
	// permit should know the difference between a generous limit and no limit at all.
	Unlimited bool

	// Err records why a raise failed, when one was attempted and did not work.
	//
	// Not fatal. A process that cannot raise its own limit still runs perfectly well inside the limit it
	// has; it has less room, and the caller should bound itself accordingly.
	Err error
}

// Available returns the number of descriptors to plan within.
//
// On a platform with no limit this returns a deliberately finite number rather than something enormous.
// Concurrency bounded by an enormous figure is unbounded in practice, and descriptors are not the only
// reason to bound it: every delivery in flight also holds a message in memory and a connection at the far
// end, and the far end has limits this process cannot see.
func (r Report) Available() uint64 {
	const whenThereIsNoLimit = 65536
	if r.Unlimited || r.Soft == 0 {
		return whenThereIsNoLimit
	}
	return r.Soft
}
