package admit

import (
	"fmt"
	"sync"

	"github.com/biodream-llc/perfuse/internal/fdlimit"
)

// Budget works out how much concurrency this process can afford, and shows the arithmetic.
//
// Derived rather than configured, because a limit somebody has to set is a limit nobody sets until after
// the outage. The numbers come from the descriptor limit the process actually has, which is knowable at
// startup and is the resource that runs out first.
//
// The arithmetic is returned as prose alongside the numbers so it can go straight into the startup log.
// A limit that engages without ever having explained itself is indistinguishable from a bug, and the
// person reading the log at three in the morning did not choose it.
type Budget struct {
	Limits

	// Descriptors is what the process may open in total, after any raise.
	Descriptors uint64

	// Reserved is the allowance held back for everything that is not a delivery.
	Reserved uint64

	// Explanation is one line saying how the limits were arrived at.
	Explanation string

	// TooTight is set when the descriptor limit is smaller than what this process needs before it delivers
	// anything at all.
	//
	// Worth saying out loud at startup rather than discovering later. Below the reservation the arithmetic
	// has nothing left to divide, so the limits fall back to their minimums - which is a guess, and a guess
	// that may still be more than the environment can honour. An operator who is told at startup can raise
	// the limit before it matters; one who is not gets "too many open files" from the database during the
	// first busy hour and no reason to connect it to a container setting.
	TooTight bool
}

// reserved is what is kept back for everything that is not an outbound delivery.
//
// Listeners, the database and its journals, the web interface and its connections, log files, the metrics
// scraper, the TLS certificate reloader, and whatever a future feature opens without asking. Deliberately
// generous: the cost of reserving too much is some throughput at the very top end, and the cost of
// reserving too little is the failure this package exists to prevent.
const reserved = 256

// perDestinationShare is how much of the total any single destination may hold.
//
// A sixteenth, floored and capped below. The purpose is not throughput, it is blast radius: one receiver
// that stops answering must not be able to consume the budget and take every other interface with it.
// Sixteen is a judgement, not a measurement - it leaves room for a genuinely busy destination while
// keeping fifteen sixteenths of the process available to everything else.
const perDestinationShare = 16

// Minimums, so a hostile environment produces a small limit rather than a useless one.
//
// A container with a soft limit of 64 should still deliver messages, slowly, rather than refuse everything
// because the arithmetic came out at zero.
const (
	minTotal          = 16
	minPerDestination = 4
	maxPerDestination = 256
)

// Plan reads the descriptor limit, raising it if it can, and returns what to permit.
func Plan() (Budget, fdlimit.Report) {
	r := fdlimit.Raise()
	return budgetFrom(r), r
}

var (
	sharedOnce   sync.Once
	sharedCtrl   *Controller
	sharedBudget Budget
	sharedReport fdlimit.Report
)

// Shared returns the one controller for this process, planning it on first use.
//
// A singleton on purpose, and the reason is worth stating because a package-level variable usually is not.
// The resource being protected is the process's file descriptor table. Anything that builds its own
// controller believes it has the whole table, so two of them permit twice what the process can afford and
// ten permit ten times - which is not a limit, it is arithmetic that happens to be smaller than infinity.
//
// There are three places that start channels: the engine used by perfuse run, the runtime used by the
// server, and one runtime per tenant when multi-tenancy is on. Making the budget a process-level fact means
// none of them can be wired up wrongly, rather than each having to remember to pass the same pointer along.
//
// New remains available for tests, which want a controller with limits they chose and no interference from
// whatever this machine happens to allow.
func Shared() (*Controller, Budget, fdlimit.Report) {
	sharedOnce.Do(func() {
		sharedBudget, sharedReport = Plan()
		sharedCtrl = New(sharedBudget.Limits)
	})
	return sharedCtrl, sharedBudget, sharedReport
}

// budgetFrom is the arithmetic, separated so it can be tested at limits no machine will offer.
func budgetFrom(r fdlimit.Report) Budget {
	available := r.Available()

	var total uint64
	tooTight := false
	if available > reserved {
		total = available - reserved
	} else {
		// Nothing left after the reservation. The minimums below are a floor rather than a plan, so this is
		// flagged instead of being quietly accepted.
		tooTight = true
	}
	if total < minTotal {
		total = minTotal
	}

	perDest := total / perDestinationShare
	if perDest < minPerDestination {
		perDest = minPerDestination
	}
	if perDest > maxPerDestination {
		perDest = maxPerDestination
	}
	// A per-destination limit above the total is not a limit. This matters at the small end, where the
	// minimums can otherwise cross over.
	if perDest > total {
		perDest = total
	}

	b := Budget{
		Limits:      Limits{Total: int(total), PerDestination: int(perDest)},
		Descriptors: available,
		Reserved:    reserved,
		TooTight:    tooTight,
	}

	switch {
	case tooTight:
		b.Explanation = fmt.Sprintf(
			"this process may open only %d file descriptors, which is fewer than the %d it wants to keep "+
				"aside for the database, the interface and its listeners. Delivery is limited to %d in "+
				"flight and %d per destination so that it works at all, but this installation is one busy "+
				"hour away from \"too many open files\" in an unrelated place. Raise the limit: LimitNOFILE "+
				"in the systemd unit, or ulimit -n before starting",
			available, reserved, b.Total, b.PerDestination)
	case r.Unlimited:
		b.Explanation = fmt.Sprintf(
			"this platform has no descriptor limit, so deliveries are capped at %d in flight and %d per "+
				"destination anyway - every one holds a message in memory and a connection at the far end",
			b.Total, b.PerDestination)
	case r.Raised:
		b.Explanation = fmt.Sprintf(
			"raised the descriptor limit from %d to %d, reserved %d for everything that is not a delivery, "+
				"so %d deliveries may be in flight and %d to any one destination",
			r.Before, available, reserved, b.Total, b.PerDestination)
	default:
		b.Explanation = fmt.Sprintf(
			"the descriptor limit is %d, reserved %d for everything that is not a delivery, so %d deliveries "+
				"may be in flight and %d to any one destination",
			available, reserved, b.Total, b.PerDestination)
	}

	return b
}
