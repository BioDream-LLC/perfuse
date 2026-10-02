package api

import (
	"testing"
	"time"
)

func verdict(minutesAgo int, judged bool, violations int) contractVerdict {
	return contractVerdict{
		At:         time.Now().Add(-time.Duration(minutesAgo) * time.Minute),
		Judged:     judged,
		Violations: violations,
	}
}

func TestNotJudgedIsNotHolding(t *testing.T) {
	// The distinction the whole feature rests on. "We have no evidence" must never read as "all well", which is the
	// failure mode of every monitoring system that reports green when its input has stopped.
	if verdict(0, false, 0).Holding() {
		t.Error("an unjudged verdict reports as holding")
	}
	if !verdict(0, true, 0).Holding() {
		t.Error("a judged verdict with no violations does not report as holding")
	}
	if verdict(0, true, 1).Holding() {
		t.Error("a verdict with violations reports as holding")
	}
}

func TestTheTrendFindsWhenAFailureStarted(t *testing.T) {
	// The first question after "what changed" is "when did this start", and a current state cannot answer it.
	h := newContractHistory()
	for _, v := range []contractVerdict{
		verdict(50, true, 0),
		verdict(40, true, 0),
		verdict(30, true, 0),
		verdict(20, true, 3), // started here
		verdict(10, true, 3),
		verdict(0, true, 3),
	} {
		h.Record("adt-in", v)
	}

	trend := h.Trend("adt-in")

	if trend.Checks != 6 {
		t.Errorf("checks = %d, want 6", trend.Checks)
	}
	if trend.Failing != 3 {
		t.Errorf("failing = %d, want 3", trend.Failing)
	}
	if trend.ChangedAt == nil {
		t.Fatal("no change point found")
	}
	if since := time.Since(*trend.ChangedAt); since < 19*time.Minute || since > 21*time.Minute {
		t.Errorf("the change is dated %s ago, want about 20 minutes", since.Round(time.Minute))
	}
	if trend.FromStartOfWindow {
		t.Error("the change was found inside the window, so it is a measurement rather than a floor")
	}
	if trend.Flapping {
		t.Error("one transition reported as flapping")
	}
}

func TestAWindowThatAgreesThroughoutIsAFloorNotAMeasurement(t *testing.T) {
	// If every recorded check failed, the real start is earlier than anything held. Saying "failing since" with the
	// oldest timestamp would be asserting something unknown, so it is flagged as a floor.
	h := newContractHistory()
	for i := 5; i >= 0; i-- {
		h.Record("adt-in", verdict(i*10, true, 2))
	}

	trend := h.Trend("adt-in")

	if !trend.FromStartOfWindow {
		t.Error("a uniformly failing window is not flagged as running back past the window")
	}
	if trend.ChangedAt == nil {
		t.Error("no timestamp given even as a floor")
	}
	if trend.Failing != trend.Checks {
		t.Errorf("failing = %d of %d, want all", trend.Failing, trend.Checks)
	}
}

func TestFlappingIsDistinguishedFromBreakingOnce(t *testing.T) {
	// A feed alternating between holding and failing is a different problem from one that broke and stayed broken,
	// and the same failure count describes both. A single "failing since" sentence would misrepresent it.
	h := newContractHistory()
	for _, v := range []contractVerdict{
		verdict(60, true, 0),
		verdict(50, true, 2),
		verdict(40, true, 0),
		verdict(30, true, 2),
		verdict(20, true, 0),
		verdict(10, true, 2),
	} {
		h.Record("flappy", v)
	}

	trend := h.Trend("flappy")

	if !trend.Flapping {
		t.Error("five transitions not reported as flapping")
	}
	if trend.Failing != 3 {
		t.Errorf("failing = %d, want 3", trend.Failing)
	}
}

func TestTheWindowIsBounded(t *testing.T) {
	// This lives in memory, and a long-running instance would otherwise accumulate for months.
	h := newContractHistory()
	for i := 0; i < maxContractHistory*3; i++ {
		h.Record("busy", verdict(0, true, 0))
	}

	if got := h.Trend("busy").Checks; got != maxContractHistory {
		t.Errorf("kept %d verdicts, want the window of %d", got, maxContractHistory)
	}
}

func TestOldestEntriesAreTheOnesDropped(t *testing.T) {
	// Dropping the newest would make the trend permanently stale, which is worse than a short window.
	h := newContractHistory()
	h.Record("busy", verdict(999, true, 5))
	for i := 0; i < maxContractHistory; i++ {
		h.Record("busy", verdict(0, true, 0))
	}

	trend := h.Trend("busy")
	for _, v := range trend.Verdicts {
		if v.Violations == 5 {
			t.Fatal("the oldest verdict survived; the window is dropping from the wrong end")
		}
	}
}

func TestHistoryScopesItsClaimToWhenItStartedWatching(t *testing.T) {
	// Without this a page saying "failing for 20 minutes" after a 20-minute-old restart would assert something the
	// instance cannot know.
	h := newContractHistory()
	h.Record("adt-in", verdict(0, true, 1))

	trend := h.Trend("adt-in")
	if trend.ObservedSince.IsZero() {
		t.Error("the trend does not say when observation began")
	}
	if time.Since(trend.ObservedSince) > time.Minute {
		t.Error("observedSince looks wrong for a history created in this test")
	}
}

func TestAnEmptyTrendClaimsNothing(t *testing.T) {
	trend := newContractHistory().Trend("never-checked")

	if trend.Checks != 0 {
		t.Errorf("checks = %d, want 0", trend.Checks)
	}
	if trend.ChangedAt != nil {
		t.Error("a channel with no checks reports a change point")
	}
	if trend.Flapping {
		t.Error("a channel with no checks reports flapping")
	}
}

func TestForgettingAChannelDropsItsTrend(t *testing.T) {
	// A channel removed and re-added must not inherit the old trend and appear to have been failing since before it
	// existed.
	h := newContractHistory()
	h.Record("gone", verdict(10, true, 4))
	h.Forget("gone")

	if got := h.Trend("gone").Checks; got != 0 {
		t.Errorf("after forgetting, %d verdicts remain", got)
	}
}

func TestRecoveryIsDatedToo(t *testing.T) {
	// "It started working again at 4pm" is as useful as when it broke, and a trend that only dated failures would
	// leave somebody unable to correlate a fix with its effect.
	h := newContractHistory()
	for _, v := range []contractVerdict{
		verdict(40, true, 3),
		verdict(30, true, 3),
		verdict(20, true, 0), // recovered here
		verdict(10, true, 0),
	} {
		h.Record("adt-in", v)
	}

	trend := h.Trend("adt-in")

	if trend.ChangedAt == nil {
		t.Fatal("no change point for a recovery")
	}
	if since := time.Since(*trend.ChangedAt); since < 19*time.Minute || since > 21*time.Minute {
		t.Errorf("recovery dated %s ago, want about 20 minutes", since.Round(time.Minute))
	}
	if trend.Failing != 2 {
		t.Errorf("failing = %d, want 2 - the window still contains the failures", trend.Failing)
	}
}
