package equiv

import (
	"fmt"
	"strings"
	"testing"
)

// The feature this package exists for is the grouping. A diff of 400,000 messages that produces 400,000
// findings gets closed unread, so the tests lead with the case that proves grouping works.

func msg(control string, fields ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b,
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A01^ADT_A01|%s|P|2.5.1\r", control)
	b.WriteString("PID|1||MRN1^^^SITEA^MR||Frost^Ivy||19910228|F\r")
	for _, f := range fields {
		b.WriteString(f)
		b.WriteString("\r")
	}
	return []byte(b.String())
}

func TestThreeThousandDifferencesFromOneCauseIsOneFinding(t *testing.T) {
	// The whole reason this package exists. Without grouping the report is the raw data again.
	left := map[string][]byte{}
	right := map[string][]byte{}

	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("C%04d", i)
		// Every message differs in exactly one place, and always the same place: one facility code that
		// was not carried across. That is one problem, not three thousand.
		left[id] = msg(id, "PV1|1|I|WARD^ROOM^BED||||||||||||||||VN"+id)
		right[id] = msg(id, "PV1|1|O|WARD^ROOM^BED||||||||||||||||VN"+id)
	}

	report := Compare(left, right, Options{LeftName: "Mirth", RightName: "Perfuse"})

	if got := len(report.Findings); got != 1 {
		t.Fatalf("findings = %d, want 1: the grouping did not collapse one cause", got)
	}
	f := report.Findings[0]
	if f.Count != 3000 {
		t.Errorf("count = %d, want 3000", f.Count)
	}
	if f.Path != "PV1-2" {
		t.Errorf("path = %q, want PV1-2", f.Path)
	}
	if f.DistinctPairs != 1 {
		t.Errorf("distinct pairs = %d, want 1: a single consistent substitution", f.DistinctPairs)
	}
	if got := len(f.Examples); got != maxExamples {
		t.Errorf("examples = %d, want %d: a list of 3000 ids is not an example", got, maxExamples)
	}
	if report.Summary.Differing != 3000 {
		t.Errorf("differing = %d, want 3000", report.Summary.Differing)
	}
}

func TestTheHeadlineStatesTheDenominator(t *testing.T) {
	// "Three differences" means nothing. "Three out of four hundred thousand" is the entire argument.
	left := map[string][]byte{}
	right := map[string][]byte{}

	for i := 0; i < 4000; i++ {
		id := fmt.Sprintf("C%04d", i)
		left[id] = msg(id)
		right[id] = msg(id)
	}
	// Three that differ.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("D%d", i)
		left[id] = msg(id, "OBX|1|ST|GLU||100")
		right[id] = msg(id, "OBX|1|ST|GLU||101")
	}

	report := Compare(left, right, Options{LeftName: "Mirth", RightName: "Perfuse"})
	head := report.Headline()

	if !strings.Contains(head, "4,000") && !strings.Contains(head, "4003") && !strings.Contains(head, "4,003") {
		t.Errorf("the headline does not state the denominator: %q", head)
	}
	if !strings.Contains(head, "Mirth") || !strings.Contains(head, "Perfuse") {
		t.Errorf("the headline calls the sides something unhelpful: %q", head)
	}
}

func TestIdenticalOutputAgrees(t *testing.T) {
	left := map[string][]byte{"C1": msg("C1"), "C2": msg("C2")}
	right := map[string][]byte{"C1": msg("C1"), "C2": msg("C2")}

	report := Compare(left, right, Options{})
	if !report.Agreed() {
		t.Fatalf("identical output did not agree: %s", report.Headline())
	}
	if report.Summary.Identical != 2 {
		t.Errorf("identical = %d, want 2", report.Summary.Identical)
	}
	if !strings.Contains(report.Headline(), "identical") {
		t.Errorf("the headline buries the good news: %q", report.Headline())
	}
}

func TestValuesAreWithheldUnlessAsked(t *testing.T) {
	// Real traffic is patient data and a report is a thing people paste into tickets.
	left := map[string][]byte{"C1": msg("C1", "OBX|1|ST|GLU||Frostbite")}
	right := map[string][]byte{"C1": msg("C1", "OBX|1|ST|GLU||Sunburn")}

	report := Compare(left, right, Options{})
	if report.IncludesContent {
		t.Error("the report claims to include content when it was not asked to")
	}
	for _, f := range report.Findings {
		if f.Left != "" || f.Right != "" {
			t.Errorf("a value leaked into the report: %q / %q", f.Left, f.Right)
		}
	}

	// And enabled deliberately, because eventually somebody does need to see one.
	withContent := Compare(left, right, Options{IncludeContent: true})
	if !withContent.IncludesContent {
		t.Error("the report does not record that it holds content")
	}
	found := false
	for _, f := range withContent.Findings {
		if f.Left != "" || f.Right != "" {
			found = true
		}
	}
	if !found {
		t.Error("content was asked for and not provided")
	}
}

func TestAMissingSegmentIsOneFindingNotTwentyFields(t *testing.T) {
	// One missing PID would otherwise appear as twenty missing fields: the same cause, twenty times, in
	// a report whose entire purpose is to report each cause once.
	left := map[string][]byte{"C1": msg("C1", "PV1|1|I|WARD^ROOM^BED|R|||1234^Who^Doctor|||||||||||VN1")}
	right := map[string][]byte{"C1": []byte(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A01^ADT_A01|C1|P|2.5.1\r" +
			"PID|1||MRN1^^^SITEA^MR||Frost^Ivy||19910228|F\r")}

	report := Compare(left, right, Options{})

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(report.Findings), report.Findings)
	}
	if report.Findings[0].Kind != SegmentMissingOnRight {
		t.Errorf("kind = %q, want a missing segment", report.Findings[0].Kind)
	}
	if report.Findings[0].Path != "PV1" {
		t.Errorf("path = %q, want PV1", report.Findings[0].Path)
	}
}

func TestAbsentAndEmptyAreDifferentFindings(t *testing.T) {
	// In an update message an empty field means "no change" and an absent one means "never sent".
	// Collapsing them hides the difference that matters most on exactly the messages where it matters.
	leftEmpty := map[string][]byte{"C1": []byte(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A08|C1|P|2.5.1\r" +
			"PID|1||MRN1||Frost^Ivy||19910228|\r")}
	rightValued := map[string][]byte{"C1": []byte(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A08|C1|P|2.5.1\r" +
			"PID|1||MRN1||Frost^Ivy||19910228|F\r")}

	report := Compare(leftEmpty, rightValued, Options{})
	if len(report.Findings) == 0 {
		t.Fatal("an empty field against a populated one was not reported")
	}

	var kinds []string
	for _, f := range report.Findings {
		kinds = append(kinds, string(f.Kind))
	}
	joined := strings.Join(kinds, ",")
	if strings.Contains(joined, string(ValueDiffers)) && !strings.Contains(joined, "empty") {
		t.Errorf("an empty field was reported as an ordinary value difference: %v", kinds)
	}
}

func TestAMessageOnlyOneSideProducedIsCountedSeparately(t *testing.T) {
	// Usually a filter that does not agree, which is worse than a mapping fault and easy to overlook
	// when it is mixed in with field differences.
	left := map[string][]byte{"C1": msg("C1"), "C2": msg("C2")}
	right := map[string][]byte{"C1": msg("C1")}

	report := Compare(left, right, Options{LeftName: "Mirth", RightName: "Perfuse"})

	if len(report.Summary.OnlyLeft) != 1 || report.Summary.OnlyLeft[0] != "C2" {
		t.Errorf("only-left = %v, want [C2]", report.Summary.OnlyLeft)
	}
	if report.Agreed() {
		t.Error("a message the other side never produced counted as agreement")
	}
	if !strings.Contains(report.Headline(), "the other did not") {
		t.Errorf("the headline does not mention the missing message: %q", report.Headline())
	}
	// And it is not counted as compared, because it was not.
	if report.Summary.Compared != 1 {
		t.Errorf("compared = %d, want 1", report.Summary.Compared)
	}
}

func TestAnUnparseableOutputIsTheMostSeriousFinding(t *testing.T) {
	// Not a mapping problem. A broken output, and it must not be buried under date formats.
	left := map[string][]byte{}
	right := map[string][]byte{}

	// Fifty ordinary differences.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("C%02d", i)
		left[id] = msg(id, "OBX|1|ST|GLU||100")
		right[id] = msg(id, "OBX|1|ST|GLU||101")
	}
	// And one output that is not a message at all.
	left["BAD"] = msg("BAD")
	right["BAD"] = []byte("<html>500 Internal Server Error</html>")

	report := Compare(left, right, Options{})

	if len(report.Findings) == 0 {
		t.Fatal("no findings")
	}
	if report.Findings[0].Kind != Unparseable {
		t.Errorf("first finding = %q, want the unparseable one first; it was buried",
			report.Findings[0].Kind)
	}
}

func TestExpectedDifferencesCanBeIgnored(t *testing.T) {
	// Two engines legitimately disagree about some fields. Without a way to say so, the report is
	// dominated by differences nobody intends to fix, and the real ones are invisible.
	left := map[string][]byte{"C1": []byte(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A01|C1|P|2.5.1\r" +
			"PID|1||MRN1||Frost^Ivy\r")}
	right := map[string][]byte{"C1": []byte(
		"MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819999999||ADT^A01|C1|P|2.5.1\r" +
			"PID|1||MRN1||Frost^Ivy\r")}

	noisy := Compare(left, right, Options{})
	if len(noisy.Findings) == 0 {
		t.Fatal("the timestamp difference was not detected at all")
	}

	quiet := Compare(left, right, Options{IgnorePaths: []string{"MSH-7"}})
	if len(quiet.Findings) != 0 {
		t.Errorf("an ignored path was still reported: %+v", quiet.Findings)
	}
	if !quiet.Agreed() {
		t.Error("with the only difference ignored, the two sides should agree")
	}
}

func TestManyDistinctPairsMeansItIsDataDependent(t *testing.T) {
	// One pair means a constant substitution and a one-line fix. Many means a rule is needed, and
	// saying which is more useful than the count of affected messages.
	left := map[string][]byte{}
	right := map[string][]byte{}

	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("C%02d", i)
		left[id] = msg(id, fmt.Sprintf("OBX|1|ST|GLU||%d", 100+i))
		right[id] = msg(id, fmt.Sprintf("OBX|1|ST|GLU||%d", 200+i))
	}

	report := Compare(left, right, Options{})
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(report.Findings))
	}
	if report.Findings[0].DistinctPairs != 20 {
		t.Errorf("distinct pairs = %d, want 20: this is data-dependent, not a constant",
			report.Findings[0].DistinctPairs)
	}
}

func TestFindingOrderIsStable(t *testing.T) {
	// These reports get diffed between runs to see whether a fix worked, and an unstable order makes
	// that diff useless.
	left := map[string][]byte{"C1": msg("C1", "OBX|1|ST|GLU||100|mg", "PV1|1|I|W^R^B")}
	right := map[string][]byte{"C1": msg("C1", "OBX|1|ST|GLU||101|mmol", "PV1|1|O|W^R^B")}

	var first []string
	for run := 0; run < 8; run++ {
		report := Compare(left, right, Options{})
		var order []string
		for _, f := range report.Findings {
			order = append(order, f.Path+" "+string(f.Kind))
		}
		if run == 0 {
			first = order
			continue
		}
		if strings.Join(order, "|") != strings.Join(first, "|") {
			t.Fatalf("order changed between runs:\n%v\n%v", first, order)
		}
	}
}

func TestKeyOfUsesTheControlId(t *testing.T) {
	key, err := KeyOf(msg("ABC123"))
	if err != nil {
		t.Fatal(err)
	}
	if key != "ABC123" {
		t.Errorf("key = %q, want ABC123", key)
	}
}

func TestAMessageWithNoControlIdRefusesToBePaired(t *testing.T) {
	// Pairing it with the wrong partner would manufacture differences that do not exist, which is the
	// worst thing this package could do.
	_, err := KeyOf([]byte("MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819080000||ADT^A01||P|2.5.1\r"))
	if err == nil {
		t.Fatal("a message with no control id was given a key")
	}
	if !strings.Contains(err.Error(), "MSH-10") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestNothingComparedSaysSoPlainly(t *testing.T) {
	// An empty comparison reporting "identical on all 0 messages" would be true and deeply misleading.
	report := Compare(map[string][]byte{}, map[string][]byte{}, Options{})
	if !strings.Contains(report.Headline(), "nothing was compared") {
		t.Errorf("an empty comparison read as success: %q", report.Headline())
	}
}
