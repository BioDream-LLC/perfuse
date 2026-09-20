package profile

import (
	"fmt"
	"strings"
	"testing"
)

// Drift detection is only useful if it is quiet. A report that lists forty changes every week
// trains people to close it, and the one real change is then invisible. So the tests here are as
// much about what is not reported as what is.

// feed builds a corpus with controllable characteristics.
func feed(n int, sex string, extra string) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C%04d|P|2.5\r"+
			"PID|1||%07d^^^MRN||NAME^GIVEN||19700101|%s\r", i, i, sex)
		if extra != "" {
			body += extra + "\r"
		}
		out = append(out, []byte(body))
	}
	return out
}

func TestAnUnchangedFeedReportsNothing(t *testing.T) {
	// The most important case. A report that cries wolf on an unchanged feed is worse than no
	// report, because it teaches people to ignore the one that matters.
	before := Build(feed(200, "M", ""))
	after := Build(feed(200, "M", ""))

	cmp := Compare(before, after)
	if !cmp.Stable {
		t.Fatalf("an unchanged feed reported %d change(s): %+v", len(cmp.Changes), cmp.Changes)
	}
}

func TestOrdinaryVariationIsNotReported(t *testing.T) {
	// A field populated in 96% of messages one week and 94% the next has not changed; it is the
	// same field with different traffic through it.
	before := Build(withMissingDOB(200, 8))
	after := Build(withMissingDOB(200, 12))

	cmp := Compare(before, after)
	for _, c := range cmp.Changes {
		if c.Kind == FillRateMoved {
			t.Errorf("a two-point movement was reported as a change: %s", c.What)
		}
	}
}

func TestARealDropInFillRateIsReportedAsBreaking(t *testing.T) {
	// A field something depends on becoming sometimes-absent is the commonest cause of a
	// downstream failure that nothing announced.
	before := Build(withMissingDOB(200, 0))
	after := Build(withMissingDOB(200, 80))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == FillRateMoved && strings.Contains(c.Where, "PID-7") {
			found = true
			if !c.Breaking {
				t.Error("a field becoming unreliable was not marked breaking")
			}
			if !strings.Contains(c.What, "less often") {
				t.Errorf("the change does not say which direction: %s", c.What)
			}
		}
	}
	if !found {
		t.Errorf("the drop in PID-7 was not reported: %+v", cmp.Changes)
	}
}

func TestANewCodeIsReportedAsBreaking(t *testing.T) {
	// The change that makes a strict mapping fail, and the one no check on message volume can
	// possibly see: the feed keeps flowing at the same rate, carrying a value nothing maps.
	before := Build(feed(200, "M", ""))

	// Half the messages now carry a sex code never seen before.
	mixed := append(feed(100, "M", ""), feed(100, "X", "")...)
	after := Build(mixed)

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == CodeAppeared && strings.Contains(c.Where, "PID-8") {
			found = true
			if !c.Breaking {
				t.Error("a new code was not marked breaking")
			}
			if !strings.Contains(c.What, `"X"`) {
				t.Errorf("the change does not name the new code: %s", c.What)
			}
		}
	}
	if !found {
		t.Errorf("the new code was not reported: %+v", cmp.Changes)
	}
}

func TestACodeNoLongerSentIsReportedButNotBreaking(t *testing.T) {
	// Nothing downstream fails because a value stopped arriving. It is reported because it
	// usually means somebody changed something upstream.
	before := Build(append(feed(100, "M", ""), feed(100, "F", "")...))
	after := Build(feed(200, "M", ""))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == CodeVanished {
			found = true
			if c.Breaking {
				t.Error("a code no longer being sent was marked breaking")
			}
		}
	}
	if !found {
		t.Errorf("the missing code was not reported: %+v", cmp.Changes)
	}
}

func TestANewSegmentIsReported(t *testing.T) {
	// Often the first visible sign that something changed at the sending system.
	before := Build(feed(200, "M", ""))
	after := Build(feed(200, "M", "ZPI|1|something new"))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == SegmentAppeared && c.Where == "ZPI" {
			found = true
			if !strings.Contains(c.What, "locally defined") {
				t.Errorf("the change does not say it is a local segment: %s", c.What)
			}
		}
	}
	if !found {
		t.Errorf("the new segment was not reported: %+v", cmp.Changes)
	}
}

func TestAVanishedSegmentIsBreaking(t *testing.T) {
	// Something was reading it, and now there is nothing to read.
	before := Build(feed(200, "M", "NK1|1|CONTACT^NAME|SPO"))
	after := Build(feed(200, "M", ""))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == SegmentVanished && c.Where == "NK1" {
			found = true
			if !c.Breaking {
				t.Error("a vanished segment was not marked breaking")
			}
		}
	}
	if !found {
		t.Errorf("the vanished segment was not reported: %+v", cmp.Changes)
	}
}

func TestGrowingRepetitionIsBreaking(t *testing.T) {
	// A mapping that takes one value is silently dropping the others, which is the worst kind
	// of failure: nothing errors and the data is wrong.
	before := Build(withPID(200, "PID|1||1111111^^^MRN||NAME^GIVEN||19700101|M"))
	after := Build(withPID(200, "PID|1||1111111^^^MRN~2222222^^^MR2~3333333^^^MR3||NAME^GIVEN||19700101|M"))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == RepetitionGrew {
			found = true
			if !c.Breaking {
				t.Error("growing repetition was not marked breaking")
			}
		}
	}
	if !found {
		t.Errorf("the growing repetition was not reported: %+v", cmp.Changes)
	}
}

func TestValuesGettingLongerIsReported(t *testing.T) {
	// This is what overflows a fixed-width column or a database field sized for the old
	// maximum, and it does so silently until something truncates.
	before := Build(withPID(200, "PID|1||1111111^^^MRN||SHORT^GIVEN||19700101|M"))
	after := Build(withPID(200,
		"PID|1||1111111^^^MRN||AVERYMUCHLONGERFAMILYNAMEINDEED^GIVEN||19700101|M"))

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == LengthGrew {
			found = true
			if !c.Breaking {
				t.Error("values getting longer was not marked breaking")
			}
			if !strings.Contains(c.What, "truncate") {
				t.Errorf("the change does not say what goes wrong: %s", c.What)
			}
		}
	}
	if !found {
		t.Errorf("the length increase was not reported: %+v", cmp.Changes)
	}
}

func TestANewMessageTypeIsReported(t *testing.T) {
	before := Build(feed(200, "M", ""))

	mixed := feed(150, "M", "")
	for i := 0; i < 50; i++ {
		mixed = append(mixed, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A08|X%04d|P|2.5\rPID|1||9%06d^^^MRN||N^G||19700101|M\r",
			i, i)))
	}
	after := Build(mixed)

	cmp := Compare(before, after)

	found := false
	for _, c := range cmp.Changes {
		if c.Kind == TypeAppeared && c.Where == "ADT^A08" {
			found = true
		}
	}
	if !found {
		t.Errorf("the new message type was not reported: %+v", cmp.Changes)
	}
}

func TestBreakingChangesComeFirst(t *testing.T) {
	// The report is read from the top and the reader may stop partway.
	before := Build(feed(200, "M", "NK1|1|CONTACT^NAME|SPO"))
	after := Build(feed(200, "X", ""))

	cmp := Compare(before, after)
	if len(cmp.Changes) < 2 {
		t.Fatalf("expected several changes, got %+v", cmp.Changes)
	}

	seenNonBreaking := false
	for _, c := range cmp.Changes {
		if !c.Breaking {
			seenNonBreaking = true
			continue
		}
		if seenNonBreaking {
			t.Error("a breaking change was listed after a non-breaking one")
		}
	}
}

func TestASmallSampleSaysItCannotBeTrusted(t *testing.T) {
	// With a handful of messages a field that looks vanished may simply not have occurred yet.
	// The comparison is still produced, because a small sample is often all anybody has.
	before := Build(feed(10, "M", ""))
	after := Build(feed(10, "M", ""))

	cmp := Compare(before, after)
	if cmp.Note == "" {
		t.Error("a ten-message comparison did not say the sample was small")
	}
	if !strings.Contains(cmp.Note, "small") {
		t.Errorf("note = %q", cmp.Note)
	}
}

func TestComparisonIsStableAcrossRuns(t *testing.T) {
	// These get diffed and pasted into tickets, so two runs over the same data must list the
	// same changes in the same order.
	before := Build(feed(200, "M", "NK1|1|CONTACT^NAME|SPO"))
	after := Build(feed(200, "X", ""))

	first := describe(Compare(before, after))
	for i := 0; i < 20; i++ {
		if got := describe(Compare(before, after)); got != first {
			t.Fatal("two comparisons of the same data listed changes differently")
		}
	}
}

func TestAMissingProfileIsNotAnEmptyComparison(t *testing.T) {
	// An empty comparison reads as "nothing changed", which is the opposite of "there was
	// nothing to compare".
	cmp := Compare(nil, Build(feed(100, "M", "")))
	if cmp.Stable {
		t.Error("a comparison against nothing reported the feed as stable")
	}
	if cmp.Note == "" {
		t.Error("no explanation was given")
	}
}

func TestTheChangeListIsBounded(t *testing.T) {
	// A feed that changed wholesale would otherwise produce a report too long to read.
	before := Build(feed(200, "M", ""))

	// A completely different message, so almost everything differs.
	var after [][]byte
	for i := 0; i < 200; i++ {
		after = append(after, []byte(fmt.Sprintf(
			"MSH|^~\\&|OTHER|ELSEWHERE|X|Y|20260819||ORU^R01|Z%04d|P|2.5\r"+
				"PID|1||%07d^^^OTHER||X^Y||19800101|F\r"+
				"OBR|1|||GLU\rOBX|1|NM|GLU||%d|mg/dL\r", i, i, i)))
	}

	cmp := Compare(before, Build(after))
	if len(cmp.Changes) > 60 {
		t.Errorf("the change list has %d entries, above the bound", len(cmp.Changes))
	}
}

// withMissingDOB builds a corpus where a given number of messages omit the date of birth.
func withMissingDOB(n, missing int) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		dob := "19700101"
		if i < missing {
			dob = ""
		}
		out = append(out, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C%04d|P|2.5\r"+
				"PID|1||%07d^^^MRN||NAME^GIVEN||%s|M\r", i, i, dob)))
	}
	return out
}

// byte2D repeats one PID line across a corpus.
func withPID(n int, pid string) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C%04d|P|2.5\r%s\r", i, pid)))
	}
	return out
}

func describe(cmp *Comparison) string {
	var b strings.Builder
	for _, c := range cmp.Changes {
		fmt.Fprintf(&b, "%v|%s|%s|%v\n", c.Kind, c.Where, c.What, c.Breaking)
	}
	return b.String()
}
