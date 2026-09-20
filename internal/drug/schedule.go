package drug

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────────────────────
// Controlled substances
// ──────────────────────────────────────────────────────────────────────────────

// Schedule is a DEA controlled substance schedule.
//
// Modelled rather than carried as a string because the schedule decides whether a prescription may be
// refilled at all, and how it may be transmitted. Those are rules a router has to apply, not
// annotations to pass along.
type Schedule string

const (
	// ScheduleNone is a medicine that is not controlled.
	ScheduleNone Schedule = ""
	// ScheduleI has no accepted medical use and cannot be prescribed.
	ScheduleI Schedule = "CI"
	// ScheduleII may not be refilled. A further supply needs a new prescription.
	ScheduleII Schedule = "CII"
	// ScheduleIII may be refilled up to five times within six months.
	ScheduleIII Schedule = "CIII"
	// ScheduleIV may be refilled up to five times within six months.
	ScheduleIV Schedule = "CIV"
	// ScheduleV may be refilled up to five times within six months.
	ScheduleV Schedule = "CV"
)

// ParseSchedule reads a schedule written any of the ways prescribers write it.
//
// Roman numerals, arabic digits, with or without a C, with or without punctuation. All of these appear
// in real messages, and a strict reader would reject a valid prescription over the difference between
// CII and C-2.
func ParseSchedule(s string) (Schedule, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.NewReplacer("-", "", ".", "", " ", "", "SCHEDULE", "", "CLASS", "").Replace(t)
	t = strings.TrimPrefix(t, "C")

	switch t {
	case "":
		return ScheduleNone, nil
	case "I", "1":
		return ScheduleI, nil
	case "II", "2":
		return ScheduleII, nil
	case "III", "3":
		return ScheduleIII, nil
	case "IV", "4":
		return ScheduleIV, nil
	case "V", "5":
		return ScheduleV, nil
	}
	// Deliberately not defaulting to uncontrolled. Treating an unrecognised schedule as "not a
	// controlled substance" would let a typo turn an opioid into something refillable eleven times.
	return "", fmt.Errorf("%q is not a DEA schedule; expected I to V, written as CII, C-II, 2 or schedule II", s)
}

// Controlled reports whether the medicine is scheduled.
func (s Schedule) Controlled() bool { return s != ScheduleNone }

// Prescribable reports whether the medicine may be prescribed at all.
//
// Schedule I has no accepted medical use, so a prescription carrying one is not a prescription with a
// problem in it - it is a message that should never have been sent, and passing it on would have a
// pharmacy trying to fill it.
func (s Schedule) Prescribable() bool { return s != ScheduleI }

// MaxRefills is the federal limit on refills for this schedule.
//
// Federal only, and the second return value says so. States set their own limits and several are
// stricter; a channel that treats this number as the answer will be wrong in those states. It is
// returned as a limit to check against rather than a value to fill in, because the difference decides
// whether an over-limit prescription is corrected silently or reported.
func (s Schedule) MaxRefills() (limit int, federalOnly bool) {
	switch s {
	case ScheduleI:
		return 0, true
	case ScheduleII:
		// No refills at all. A further supply requires a new prescription, which is the rule most often
		// broken by a system that copies a refill count across from a non-controlled template.
		return 0, true
	case ScheduleIII, ScheduleIV, ScheduleV:
		return 5, true
	default:
		// Uncontrolled medicines have no federal refill limit. Returning zero here would read as "no
		// refills allowed", which is the opposite of the truth, so the caller is told there is no limit
		// by being told this is not a federal question.
		return -1, false
	}
}

// RefillsAllowed checks a requested refill count against the federal limit.
//
// Returns the reason rather than a boolean, because "this cannot be refilled" and "this may be refilled
// five times, not eleven" need different handling and a caller given only false will treat them alike.
func (s Schedule) RefillsAllowed(refills int) error {
	if refills < 0 {
		return fmt.Errorf("a refill count cannot be negative")
	}
	if !s.Prescribable() {
		return fmt.Errorf("a schedule %s substance cannot be prescribed", strings.TrimPrefix(string(s), "C"))
	}
	limit, federal := s.MaxRefills()
	if !federal {
		return nil
	}
	if s == ScheduleII && refills > 0 {
		return fmt.Errorf("a schedule II prescription may not be refilled, and this one asks for %d; a further supply needs a new prescription", refills)
	}
	if refills > limit {
		return fmt.Errorf("a schedule %s prescription may be refilled %d times under federal law, and this one asks for %d; some states allow fewer",
			strings.TrimPrefix(string(s), "C"), limit, refills)
	}
	return nil
}

// RequiresEPCS reports whether transmitting this electronically needs identity proofing and two-factor
// signing.
//
// True for everything scheduled. A channel that forwards a controlled prescription without the signing
// having happened produces a message a pharmacy must refuse, and the prescriber finds out from the
// patient rather than from us.
func (s Schedule) RequiresEPCS() bool { return s.Controlled() }
