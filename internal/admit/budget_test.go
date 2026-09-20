package admit

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fdlimit"
)

// The arithmetic has to behave at limits no machine here will offer, which is the point of testing it
// separately from reading the real one.
func TestBudgetAtEveryScale(t *testing.T) {
	cases := []struct {
		name string
		soft uint64
	}{
		{"a hostile container", 64},
		{"the old unix default", 256},
		{"a common linux default", 1024},
		{"a tuned server", 10240},
		{"a large server", 1048576},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := budgetFrom(fdlimit.Report{Soft: c.soft, Hard: c.soft})

			if b.Total <= 0 {
				t.Fatalf("total came out at %d, so nothing could ever be delivered", b.Total)
			}
			if b.PerDestination <= 0 {
				t.Fatalf("per-destination came out at %d", b.PerDestination)
			}

			// The property that matters: one destination may never hold the whole budget, or a single hung
			// receiver takes every other interface with it.
			if b.PerDestination > b.Total {
				t.Errorf("per-destination %d exceeds the total %d", b.PerDestination, b.Total)
			}
			if b.Total > minTotal && b.PerDestination == b.Total {
				t.Errorf("one destination may hold the entire budget of %d, so a hung receiver blocks "+
					"everything else", b.Total)
			}

			// It must stay inside the descriptors it actually has, once the reservation is honoured.
			if uint64(b.Total) > c.soft {
				t.Errorf("total %d exceeds the %d descriptors available", b.Total, c.soft)
			}

			if b.Explanation == "" {
				t.Error("no explanation, so an operator seeing this limit engage has nothing to read")
			}
			t.Logf("soft=%d -> total=%d perDest=%d: %s", c.soft, b.Total, b.PerDestination, b.Explanation)
		})
	}
}

// A raise must be reported, because "why did this get faster after a restart" is a question with an answer.
func TestExplanationSaysWhenTheLimitWasRaised(t *testing.T) {
	b := budgetFrom(fdlimit.Report{Before: 256, Soft: 10240, Hard: 10240, Raised: true})
	if !strings.Contains(b.Explanation, "raised") {
		t.Errorf("a raise is not mentioned: %s", b.Explanation)
	}
	if !strings.Contains(b.Explanation, "256") || !strings.Contains(b.Explanation, "10240") {
		t.Errorf("the explanation does not show both numbers: %s", b.Explanation)
	}
}

// A platform with no limit still gets one, because descriptors were never the only reason to bound this.
func TestUnlimitedPlatformIsStillBounded(t *testing.T) {
	b := budgetFrom(fdlimit.Report{Unlimited: true})
	if b.Total <= 0 || b.PerDestination <= 0 {
		t.Fatalf("an unbounded platform produced total=%d perDest=%d", b.Total, b.PerDestination)
	}
	if b.PerDestination >= b.Total {
		t.Errorf("per-destination %d is not below the total %d", b.PerDestination, b.Total)
	}
	if !strings.Contains(b.Explanation, "no descriptor limit") {
		t.Errorf("the explanation does not say the platform has no limit: %s", b.Explanation)
	}
}

// Plan must work on this machine, whatever it happens to allow.
func TestPlanReadsTheRealLimit(t *testing.T) {
	b, r := Plan()
	if b.Total <= 0 {
		t.Fatalf("planning against the real limit produced total=%d (report %+v)", b.Total, r)
	}
	t.Logf("this machine: %s", b.Explanation)
}

// An environment too small to work must say so at startup.
//
// This is the difference between a support call and no support call. A container with a soft limit of 64
// cannot run this: the database, the listeners and the interface want more than that between them before a
// single message moves. Told at startup, an operator raises LimitNOFILE. Not told, they get "too many open
// files" from SQLite during the first busy hour and nothing to connect it to a container setting.
func TestATooSmallLimitIsFlaggedAndExplained(t *testing.T) {
	for _, soft := range []uint64{16, 64, 200, 256} {
		b := budgetFrom(fdlimit.Report{Soft: soft, Hard: soft})

		if !b.TooTight {
			t.Errorf("a limit of %d was not flagged as too tight", soft)
		}
		if !strings.Contains(b.Explanation, "Raise the limit") {
			t.Errorf("at %d the explanation does not say what to do: %s", soft, b.Explanation)
		}
		// It must name the mechanism, because "raise the limit" is not actionable on its own.
		if !strings.Contains(b.Explanation, "LimitNOFILE") || !strings.Contains(b.Explanation, "ulimit") {
			t.Errorf("at %d the explanation does not name how to raise it: %s", soft, b.Explanation)
		}
		// And it must still deliver something rather than refusing to work.
		if b.Total <= 0 {
			t.Errorf("at %d nothing could be delivered at all", soft)
		}
	}
}

// A healthy limit must not be flagged, or the warning becomes noise nobody reads.
func TestAHealthyLimitIsNotFlagged(t *testing.T) {
	for _, soft := range []uint64{1024, 10240, 65536} {
		if b := budgetFrom(fdlimit.Report{Soft: soft, Hard: soft}); b.TooTight {
			t.Errorf("a limit of %d was wrongly flagged as too tight: %s", soft, b.Explanation)
		}
	}
}
