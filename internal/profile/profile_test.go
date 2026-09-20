package profile

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The property this package must never break: a profile is not a way to read patient data.
//
// Everything else here is a convenience. This is the constraint that decides whether the feature
// is publishable at all, so it gets the first and most thorough test.

func msg(control, name, mrn, sex, dob string, extra ...string) []byte {
	b := fmt.Sprintf("MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819120000||ADT^A01|%s|P|2.5\r"+
		"PID|1||%s^^^MRN||%s||%s|%s\r", control, mrn, name, dob, sex)
	for _, e := range extra {
		b += e + "\r"
	}
	return []byte(b)
}

func corpus() [][]byte {
	return [][]byte{
		msg("C1", "SMITH^JOHN", "0001234", "1", "19700101"),
		msg("C2", "JONES^MARY", "0001235", "2", "19800202"),
		msg("C3", "O'BRIEN^SEAN", "0001236", "1", "19900303"),
		msg("C4", "LEE^ANNA", "0001237", "2", "20000404"),
	}
}

func TestAProfileNeverQuotesPatientData(t *testing.T) {
	// The whole design constraint. Counts, rates and shapes are reported; values are not,
	// except where a code table already made them public.
	rep := Build(corpus())

	body, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)

	for _, secret := range []string{
		"SMITH", "JOHN", "JONES", "MARY", "O'BRIEN", "SEAN", "LEE", "ANNA",
		"0001234", "0001235", "0001236", "0001237",
		"19700101", "19800202", "19900303", "20000404",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("the profile contains %q, which is patient data: a profile that leaks "+
				"values is a way to read the feed rather than a description of it", secret)
		}
	}
}

func TestAProfileDoesReportCodedValues(t *testing.T) {
	// The deliberate exception. A code table value is not identifying, and which codes a
	// sender actually uses is the single most useful fact in a profile - without it, an
	// integrator has to go and ask.
	rep := Build(corpus())

	pid := findSegment(t, rep, "PID")
	sex := findField(t, pid, "PID-8")

	if len(sex.Codes) == 0 {
		t.Fatal("PID-8 is table-constrained and its codes were not reported, which removes the " +
			"most useful thing in the profile")
	}
	seen := map[string]int{}
	for _, c := range sex.Codes {
		seen[c.Code] = c.Count
	}
	if seen["1"] != 2 || seen["2"] != 2 {
		t.Errorf("code counts = %v, want two of each", seen)
	}
}

func TestAProfileCountsFillRatesAgainstTheSegment(t *testing.T) {
	// OBX-5 populated in 30% of messages means something entirely different from OBX-5
	// populated in 30% of messages that have an OBX at all.
	messages := [][]byte{
		msg("C1", "A^B", "1", "1", "19700101", "OBX|1|NM|GLU||5.5"),
		msg("C2", "C^D", "2", "2", "19700101", "OBX|1|NM|GLU||6.1"),
		msg("C3", "E^F", "3", "1", "19700101"), // no OBX at all
		msg("C4", "G^H", "4", "2", "19700101"), // no OBX at all
	}

	rep := Build(messages)

	obx := findSegment(t, rep, "OBX")
	if obx.Messages != 2 {
		t.Fatalf("OBX appears in %d messages, want 2", obx.Messages)
	}
	if obx.Rate < 0.49 || obx.Rate > 0.51 {
		t.Errorf("OBX rate = %v, want about half the corpus", obx.Rate)
	}

	// OBX-5 is populated in both messages that have an OBX, so its fill rate is 100% even
	// though only half the corpus carries it.
	f := findField(t, obx, "OBX-5")
	if f.FillRate < 0.999 {
		t.Errorf("OBX-5 fill rate = %v; it is populated in every message that has an OBX, and "+
			"reporting it against the whole corpus would suggest the sender omits it", f.FillRate)
	}
}

func TestAProfileNoticesANonStandardSegment(t *testing.T) {
	// These are the segments no vendor specification mentions and every integration has to
	// handle.
	messages := [][]byte{
		msg("C1", "A^B", "1", "1", "19700101", "ZPI|1|local-value"),
		msg("C2", "C^D", "2", "2", "19700101", "ZPI|1|another"),
	}

	rep := Build(messages)

	z := findSegment(t, rep, "ZPI")
	if z.Standard {
		t.Error("ZPI was reported as a standard segment")
	}

	if !hasNote(rep, "the standard does not define") {
		t.Errorf("no note about the non-standard segment: %v", rep.Notes)
	}
}

func TestAProfileNoticesAFieldThatIsUsuallyButNotAlwaysThere(t *testing.T) {
	// The single most common cause of a three-in-the-morning call: the specification said
	// required, the sender mostly sends it, and one message a week does not.
	var messages [][]byte
	for i := 0; i < 9; i++ {
		messages = append(messages, msg(fmt.Sprintf("C%d", i), "A^B", "123", "1", "19700101"))
	}
	// One message with no date of birth.
	messages = append(messages, []byte("MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C9|P|2.5\r"+
		"PID|1||123^^^MRN||A^B|||1\r"))

	rep := Build(messages)

	if !hasNote(rep, "not all of them") {
		t.Errorf("no note about the partly-populated field: %v", rep.Notes)
	}
}

func TestAProfileNoticesARepeatingField(t *testing.T) {
	// A mapping that treats a repeating field as one value silently takes the first.
	messages := [][]byte{
		[]byte("MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\r" +
			"PID|1||111^^^MRN~222^^^MRN2~333^^^MRN3||A^B\r"),
	}

	rep := Build(messages)

	pid := findSegment(t, rep, "PID")
	f := findField(t, pid, "PID-3")
	if f.MaxRepeats != 3 {
		t.Errorf("PID-3 max repeats = %d, want 3", f.MaxRepeats)
	}
	if !hasNote(rep, "repeats") {
		t.Errorf("no note about the repeating field: %v", rep.Notes)
	}
}

func TestAProfileNoticesMoreThanOneMessageType(t *testing.T) {
	// The commonest wrong assumption about a feed. A filter written for one type silently
	// ignores the rest.
	messages := [][]byte{
		[]byte("MSH|^~\\&|E|H|L|L|20260819||ADT^A01|C1|P|2.5\rPID|1||1\r"),
		[]byte("MSH|^~\\&|E|H|L|L|20260819||ADT^A08|C2|P|2.5\rPID|1||2\r"),
		[]byte("MSH|^~\\&|E|H|L|L|20260819||ORU^R01|C3|P|2.5\rPID|1||3\r"),
	}

	rep := Build(messages)

	if len(rep.Types) != 3 {
		t.Fatalf("types = %+v, want 3", rep.Types)
	}
	// Trigger events must be distinguished: ADT^A01 and ADT^A08 are different feeds as far
	// as anybody building against them is concerned.
	labels := map[string]bool{}
	for _, tc := range rep.Types {
		labels[tc.Type] = true
	}
	for _, want := range []string{"ADT^A01", "ADT^A08", "ORU^R01"} {
		if !labels[want] {
			t.Errorf("%s is missing from %v", want, labels)
		}
	}
	if !hasNote(rep, "different message types") {
		t.Errorf("no note about the message types: %v", rep.Notes)
	}
}

func TestAProfileCountsUnreadableMessagesAndSaysSo(t *testing.T) {
	// A corpus with unreadable messages in it is not a corpus to draw conclusions from, and
	// the report has to say that before anything else.
	messages := append(corpus(), []byte("this is not a message"))

	rep := Build(messages)
	if rep.Unreadable != 1 {
		t.Errorf("unreadable = %d, want 1", rep.Unreadable)
	}
	if rep.Messages != 4 {
		t.Errorf("messages = %d, want 4", rep.Messages)
	}
	if !hasNote(rep, "could not be parsed") {
		t.Errorf("the report does not mention the unreadable message: %v", rep.Notes)
	}
}

func TestAProfileIsStableAcrossRuns(t *testing.T) {
	// Go maps range randomly. Without sorting, two profiles of the same corpus would differ
	// and anybody comparing them - which is the entire point of drift detection - would see
	// changes that are not there.
	first, err := json.Marshal(Build(corpus()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(Build(corpus()))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(next) {
			t.Fatal("two profiles of the same corpus differ, so no comparison between them " +
				"can be trusted")
		}
	}
}

func TestAProfileReportsFieldsInFieldOrder(t *testing.T) {
	// A segment reads in field order, and a list jumping from 3 to 11 to 5 is hard to scan.
	rep := Build(corpus())
	pid := findSegment(t, rep, "PID")

	last := 0
	for _, f := range pid.Fields {
		n := fieldNumber(f.Path)
		if n < last {
			t.Errorf("PID fields are out of order: %s came after field %d", f.Path, last)
		}
		last = n
	}
}

func TestAProfileDoesNotProfileTheDelimiters(t *testing.T) {
	// MSH-1 and MSH-2 are the separators themselves. Profiling them reports a field whose
	// values are punctuation and says nothing about the feed.
	rep := Build(corpus())
	msh := findSegment(t, rep, "MSH")

	for _, f := range msh.Fields {
		if f.Path == "MSH-1" || f.Path == "MSH-2" {
			t.Errorf("%s was profiled; it holds the delimiters, not data", f.Path)
		}
	}
}

func TestDistinctCountingIsCapped(t *testing.T) {
	// Counting without a cap means holding every medical record number in the corpus in a
	// map, which turns a profile into a copy of the feed.
	var messages [][]byte
	for i := 0; i < maxDistinct+50; i++ {
		messages = append(messages, msg(fmt.Sprintf("C%d", i), "A^B",
			fmt.Sprintf("MRN%05d", i), "1", "19700101"))
	}

	rep := Build(messages)
	pid := findSegment(t, rep, "PID")
	f := findField(t, pid, "PID-3")

	if !f.DistinctCapped {
		t.Error("distinct counting was not capped")
	}
	if f.Distinct > maxDistinct {
		t.Errorf("distinct = %d, above the cap of %d", f.Distinct, maxDistinct)
	}
}

func TestShapeDistinguishesADateFromANumber(t *testing.T) {
	rep := Build(corpus())
	pid := findSegment(t, rep, "PID")

	dob := findField(t, pid, "PID-7")
	if dob.Shape != ShapeDate {
		t.Errorf("PID-7 shape = %q, want a date", dob.Shape)
	}
}

func TestPercentagesDoNotRoundARareCaseToNever(t *testing.T) {
	// A case occurring once in a thousand messages is the one that breaks an interface six
	// weeks after go-live. Reporting it as 0.0% reads as "never", which is the opposite.
	if got := pct(0.0005); got != "under 0.1%" {
		t.Errorf("pct(0.0005) = %q", got)
	}
	if got := pct(1); got != "100%" {
		t.Errorf("pct(1) = %q", got)
	}
	if got := pct(0.5); got != "50.0%" {
		t.Errorf("pct(0.5) = %q", got)
	}
}

func TestAnEmptyCorpusSaysSoRatherThanLookingClean(t *testing.T) {
	rep := Build(nil)
	if rep.Messages != 0 {
		t.Errorf("messages = %d", rep.Messages)
	}
	if !hasNote(rep, "nothing to say") {
		t.Errorf("an empty profile did not say it was empty: %v", rep.Notes)
	}
}

func findSegment(t *testing.T, rep *Report, id string) Segment {
	t.Helper()
	for _, s := range rep.Segments {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("%s is not in the profile", id)
	return Segment{}
}

func findField(t *testing.T, seg Segment, path string) Field {
	t.Helper()
	for _, f := range seg.Fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not in the profile of %s", path, seg.ID)
	return Field{}
}

func hasNote(rep *Report, substr string) bool {
	for _, n := range rep.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}
