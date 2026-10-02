package profile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
)

// realTraffic returns messages containing distinctive values, so a leak is unmistakable.
func realTraffic() [][]byte {
	var out [][]byte
	for i, p := range []struct{ mrn, last, first, dob, addr string }{
		{"MRN8811247", "ABERNETHY-QUILLFEATHER", "PERSEPHONE", "19470312", "14 LLANFAIRPWLL CRESCENT"},
		{"MRN9930518", "VONDERHAAR", "BARTHOLOMEW", "19551129", "77 ZUIDERZEE TERRACE"},
		{"MRN7742093", "OYELARAN-OYEYINKA", "TEMITOPE", "19881005", "3 KIRKCUDBRIGHT WYND"},
	} {
		event := []string{"A01", "A08", "A03"}[i%3]
		out = append(out, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPICADT|ST-MARGARETS|PERFUSE|CLINIC|20260101120000||ADT^%s|CTRL%d|P|2.5\r"+
				"EVN|%s|20260101120000\r"+
				"PID|1||%s^^^ST-MARGARETS^MR||%s^%s||%s|F|||%s^^EDINBURGH^^EH1 1AA\r"+
				"PV1|1|I|WARD7^12^01||||DR^MCILWRAITH\r",
			event, i, event, p.mrn, p.last, p.first, p.dob, p.addr)))
	}
	return out
}

// The property the whole feature rests on.
//
// Generation works from the profile, and a profile does not contain the messages it was built from - only fill rates,
// shapes, lengths and code values. So no patient can survive into the output, and that is a stronger guarantee than
// de-identifying real messages, which is a process somebody can do incompletely.
//
// Asserted directly rather than argued, because "it should be safe by construction" is how leaks happen.
//
// The first version of this test had no teeth. I proved it by making the generator return a real surname for PID-5.1 and
// the test still passed - because the profiler records whole fields, PID-5, and never had a PID-5.1 to match. An
// assertion aimed at a path that does not exist passes for the wrong reason, which is the same defect as the stale
// verdict found in the builder tests. It is now aimed at the paths the profile actually carries.
func TestNothingFromTheRealTrafficSurvivesIntoGeneratedMessages(t *testing.T) {
	report := Build(realTraffic())

	generated, err := Generate(report, GenerateOptions{Count: 60, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	all := string(joinAll(generated))

	// Every identifying value from the source corpus.
	for _, secret := range []string{
		"MRN8811247", "MRN9930518", "MRN7742093",
		"ABERNETHY-QUILLFEATHER", "VONDERHAAR", "OYELARAN-OYEYINKA",
		"PERSEPHONE", "BARTHOLOMEW", "TEMITOPE",
		"19470312", "19551129", "19881005",
		"LLANFAIRPWLL", "ZUIDERZEE", "KIRKCUDBRIGHT",
		"EDINBURGH", "EH1 1AA", "MCILWRAITH",
		// The sending system's own names too. A generated message that claims to come from the real application is one
		// somebody can mistake for real traffic, and these get emailed to vendors.
		"EPICADT", "ST-MARGARETS",
	} {
		if strings.Contains(all, secret) {
			t.Errorf("%q from the real traffic appears in the generated corpus", secret)
		}
	}
}

// A generated message that does not parse sends somebody hunting a bug in their interface that is really in the corpus.
func TestEveryGeneratedMessageParses(t *testing.T) {
	report := Build(realTraffic())

	generated, err := Generate(report, GenerateOptions{Count: 40, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 40 {
		t.Fatalf("got %d messages, want 40", len(generated))
	}

	for i, raw := range generated {
		m, err := hl7.Parse(raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v\n%s", i, err, raw)
		}
		msgType, event, _ := m.Type()
		if msgType == "" || event == "" {
			t.Errorf("message %d has no type: %q", i, raw)
		}
	}
}

// The trigger event mix is part of the shape.
//
// A feed described as ADT routinely carries several trigger events, and an even spread is what a naive generator
// produces. Testing against an even spread exercises an interface quite differently from testing against the real mix.
func TestTheTriggerEventMixFollowsWhatWasObserved(t *testing.T) {
	// Ninety A08s and ten A01s.
	var corpus [][]byte
	for i := range 100 {
		event := "A08"
		if i < 10 {
			event = "A01"
		}
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|S|F|R|RF|20260101120000||ADT^%s|C%d|P|2.5\r"+
				"PID|1||%d^^^F^MR||SURNAME^GIVEN||19800101|M\r", event, i, 1000+i)))
	}

	generated, err := Generate(Build(corpus), GenerateOptions{Count: 200, Seed: 11})
	if err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for _, raw := range generated {
		m, err := hl7.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, event, _ := m.Type()
		counts[event]++
	}

	if counts["A08"] == 0 || counts["A01"] == 0 {
		t.Fatalf("both events should appear: %v", counts)
	}
	// A08 dominant, as observed. Loose bounds, because the point is the proportion is respected at all rather than
	// that a random draw hits an exact figure.
	if counts["A08"] <= counts["A01"] {
		t.Errorf("A08 was 90%% of the real feed but is not dominant in the corpus: %v", counts)
	}
}

// A rare trigger event must still appear, because those are usually the ones nobody was told about.
func TestARareEventIsStillRepresented(t *testing.T) {
	var corpus [][]byte
	for i := range 200 {
		event := "A08"
		if i == 0 {
			event = "A40" // a merge, 0.5% of the feed, and the one that breaks things
		}
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|S|F|R|RF|20260101120000||ADT^%s|C%d|P|2.5\r"+
				"PID|1||%d^^^F^MR||SURNAME^GIVEN||19800101|M\r", event, i, 1000+i)))
	}

	generated, err := Generate(Build(corpus), GenerateOptions{Count: 400, Seed: 5})
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range generated {
		if strings.Contains(string(raw), "ADT^A40") {
			return
		}
	}
	t.Error("a trigger event seen once in 200 messages never appeared in 400 generated ones; " +
		"rare events are the ones that break interfaces")
}

// A field populated some of the time must be absent some of the time.
//
// Whether an interface copes with an absent field is exactly what wants testing, and a generator that fills everything
// tests the opposite.
func TestAnOptionalFieldIsSometimesAbsent(t *testing.T) {
	var corpus [][]byte
	for i := range 100 {
		// PID-14, the business phone number, in half the messages. Counted rather than assumed: after PID-8 there are
		// five empty fields before it.
		phone := ""
		if i%2 == 0 {
			phone = "01315550100"
		}
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|S|F|R|RF|20260101120000||ADT^A08|C%d|P|2.5\r"+
				"PID|1||%d^^^F^MR||SURNAME^GIVEN||19800101|M||||||%s\r", i, 1000+i, phone)))
	}

	generated, err := Generate(Build(corpus), GenerateOptions{Count: 60, Seed: 13})
	if err != nil {
		t.Fatal(err)
	}

	var withField, without int
	for _, raw := range generated {
		m, err := hl7.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		segs := m.Segments("PID")
		if len(segs) == 0 {
			continue
		}
		if strings.TrimSpace(segs[0].Field(14).String()) == "" {
			without++
		} else {
			withField++
		}
	}

	if withField == 0 {
		t.Error("a field present in half the real messages never appeared")
	}
	if without == 0 {
		t.Error("a field absent from half the real messages was always present, so nothing tests the absent case")
	}
}

// Codes carry over in their observed proportions, and that is the one thing that legitimately does.
//
// Which codes a sender actually uses is the most useful fact in a profile, and a code table value identifies nobody.
func TestObservedCodesAppearInTheGeneratedCorpus(t *testing.T) {
	var corpus [][]byte
	for i := range 60 {
		sex := []string{"M", "F", "U"}[i%3]
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|S|F|R|RF|20260101120000||ADT^A08|C%d|P|2.5\r"+
				"PID|1||%d^^^F^MR||SURNAME^GIVEN||19800101|%s\r", i, 1000+i, sex)))
	}

	report := Build(corpus)
	generated, err := Generate(report, GenerateOptions{Count: 120, Seed: 17})
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, raw := range generated {
		m, err := hl7.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range m.Segments("PID") {
			seen[strings.TrimSpace(s.Field(8).String())] = true
		}
	}

	for _, want := range []string{"M", "F", "U"} {
		if !seen[want] {
			t.Errorf("the sex code %q was in the real feed but never generated; got %v", want, keysOf(seen))
		}
	}
	// And nothing invented. A code the real sender never used would have somebody test a path that does not exist.
	for got := range seen {
		if got != "" && got != "M" && got != "F" && got != "U" {
			t.Errorf("generated an unobserved code %q, which tests a path the real feed never exercises", got)
		}
	}
}

// The same seed must give the same corpus.
//
// A corpus that changes on every run cannot be committed, cannot be compared against a previous result, and turns a
// failing test into a mystery.
func TestTheSameSeedProducesTheSameCorpus(t *testing.T) {
	report := Build(realTraffic())

	first, err := Generate(report, GenerateOptions{Count: 20, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(report, GenerateOptions{Count: 20, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}

	if string(joinAll(first)) != string(joinAll(second)) {
		t.Error("the same seed produced a different corpus, so nothing generated here can be committed or compared")
	}

	third, err := Generate(report, GenerateOptions{Count: 20, Seed: 43})
	if err != nil {
		t.Fatal(err)
	}
	if string(joinAll(first)) == string(joinAll(third)) {
		t.Error("a different seed produced an identical corpus, so the seed does nothing")
	}
}

// Generated messages must be marked as test messages, so one that reaches a real system is refused.
func TestGeneratedMessagesAreMarkedAsTest(t *testing.T) {
	generated, err := Generate(Build(realTraffic()), GenerateOptions{Count: 5, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range generated {
		m, err := hl7.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(m.Segments("MSH")[0].Field(11).String()); got != "T" {
			t.Errorf("message %d has processing ID %q, want T; a synthetic message claiming to be production is one "+
				"a receiver cannot refuse", i, got)
		}
	}
}

// An empty profile is refused with a reason, not silently producing nothing.
func TestGeneratingFromNothingIsRefused(t *testing.T) {
	if _, err := Generate(nil, GenerateOptions{}); err == nil {
		t.Error("generating from a nil profile was accepted")
	}
	if _, err := Generate(&Report{}, GenerateOptions{}); err == nil {
		t.Error("generating from an empty profile was accepted")
	}
}

func joinAll(msgs [][]byte) []byte {
	var out []byte
	for _, m := range msgs {
		out = append(out, m...)
	}
	return out
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
