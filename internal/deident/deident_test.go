package deident

import (
	"strings"
	"testing"
	"time"
)

// The one test that decides whether this feature can exist: no original identifying value may
// appear in the output. Everything else here is about the corpus still being useful.

func salt() []byte { return []byte("a-salt-of-at-least-sixteen-bytes") }

func newScrubber(t *testing.T) *Scrubber {
	t.Helper()
	sc, err := New(Options{Salt: salt()})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// realistic is a message with every category of identifier HIPAA names.
const realistic = "MSH|^~\\&|EPIC|ST JOSEPH|LAB|LAB|20260819143000||ADT^A01|MSG00042|P|2.5\r" +
	"EVN|A01|20260819143000|||OPERATOR99\r" +
	"PID|1|4471234|0001234567^^^MRN~987654321^^^SSN||SMITH^JOHN^QUINCY||19700315|M||2106-3|" +
	"742 EVERGREEN TERRACE^APT 4^SPRINGFIELD^IL^62701||(217)555-0143|(217)555-0199|EN|M|CHR|" +
	"ACC00998877|123-45-6789\r" +
	"PV1|1|I|ICU^BED3^ST JOSEPH||||DRSMITH^SMITH^ALAN|||SUR||||1|||DRJONES^JONES^BETH|" +
	"INP|VISIT445566|||||||||||||||||||||||||20260819143000|20260822090000\r" +
	"IN1|1|PLAN01|CO123|BLUE CROSS|1 INSURER WAY^^CHICAGO^IL^60601|||||||||||" +
	"SMITH^JOHN^QUINCY|SEL|19700315|742 EVERGREEN TERRACE^^SPRINGFIELD^IL^62701|||||||||||||||||" +
	"POLICY99887766\r" +
	"NK1|1|SMITH^JANE|SPO|742 EVERGREEN TERRACE^^SPRINGFIELD^IL^62701|(217)555-0143\r"

// identifiers are the values that must not survive.
var identifiers = []string{
	"SMITH", "JOHN", "QUINCY", "JANE", "ALAN", "BETH", "JONES",
	"0001234567", "987654321", "4471234", "ACC00998877", "123-45-6789",
	"742 EVERGREEN TERRACE", "EVERGREEN", "SPRINGFIELD", "62701",
	"(217)555-0143", "(217)555-0199", "555-0143",
	"19700315", "OPERATOR99", "DRSMITH", "DRJONES",
	"VISIT445566", "POLICY99887766", "BED3",
}

func TestNoIdentifierSurvives(t *testing.T) {
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, secret := range identifiers {
		if strings.Contains(got, secret) {
			t.Errorf("the scrubbed message still contains %q", secret)
		}
	}
}

func TestTheSamePatientKeepsTheSamePseudonym(t *testing.T) {
	// Referential integrity, which is what separates a useful corpus from noise. A scrubber
	// that randomised each occurrence would make every message a different patient, and the
	// corpus could not test a merge, an update or a readmission.
	sc := newScrubber(t)

	first, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	// The same patient, a later event.
	second, err := sc.Message([]byte(strings.Replace(realistic, "ADT^A01", "ADT^A03", 1)))
	if err != nil {
		t.Fatal(err)
	}

	idA := fieldOf(t, string(first), "PID", 3)
	idB := fieldOf(t, string(second), "PID", 3)

	if idA == "" {
		t.Fatal("PID-3 was emptied; a corpus with no patient identifier cannot join anything")
	}
	if idA != idB {
		t.Errorf("the same patient got two different identifiers: %q and %q", idA, idB)
	}

	nameA := fieldOf(t, string(first), "PID", 5)
	nameB := fieldOf(t, string(second), "PID", 5)
	if nameA != nameB {
		t.Errorf("the same patient got two different names: %q and %q", nameA, nameB)
	}
}

func TestDifferentPatientsGetDifferentPseudonyms(t *testing.T) {
	sc := newScrubber(t)

	first, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(realistic, "0001234567", "0009999999", 1)
	second, err := sc.Message([]byte(other))
	if err != nil {
		t.Fatal(err)
	}

	if fieldOf(t, string(first), "PID", 3) == fieldOf(t, string(second), "PID", 3) {
		t.Error("two different patients were given the same identifier")
	}
}

func TestShapeIsPreserved(t *testing.T) {
	// A receiver parsing a record number as an integer, or a fixed-width column that truncates,
	// must behave the same on the corpus as on the feed.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}

	id := fieldOf(t, string(out), "PID", 3)
	first := strings.SplitN(id, "^", 2)[0]

	if len(first) != len("0001234567") {
		t.Errorf("the identifier changed length: %q against 0001234567", first)
	}
	for _, r := range first {
		if r < '0' || r > '9' {
			t.Errorf("a numeric identifier became %q, which is not all digits", first)
			break
		}
	}
	if !strings.HasPrefix(first, "0") {
		t.Errorf("the leading zero was lost: %q. Length and leading zeroes are frequently "+
			"load-bearing, and a scrubber that dropped them would quietly fix bugs the corpus "+
			"exists to reproduce", first)
	}
}

func TestNameStructureSurvives(t *testing.T) {
	// PID-5 is family^given^middle. Replacing the joined string would produce one long token
	// where a parser expects three components, and a corpus that broke name parsing would not
	// be testing the same thing as the feed.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}

	name := fieldOf(t, string(out), "PID", 5)
	if got := strings.Count(name, "^"); got != 2 {
		t.Errorf("PID-5 is %q with %d separators, want the original three components", name, got)
	}
}

func TestDateIntervalsSurvive(t *testing.T) {
	// The reason dates are shifted rather than replaced. Length of stay, age at admission and
	// the time between results are all clinically meaningful and are what a time-based mapping
	// is tested on.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	admit := fieldOf(t, got, "PV1", 44)
	discharge := fieldOf(t, got, "PV1", 45)

	if admit == "" || discharge == "" {
		t.Fatalf("admit %q discharge %q: both are needed to check the interval", admit, discharge)
	}
	if admit[:8] == "20260819" {
		t.Error("the admit date was not shifted")
	}

	// Three days in the original, and three days after shifting.
	if days := dayGap(t, admit[:8], discharge[:8]); days != 3 {
		t.Errorf("length of stay became %d days, want 3", days)
	}
}

func TestTheTimeOfDaySurvivesADateShift(t *testing.T) {
	// Shifting by whole days keeps the time, which matters: a feed where results arrive at
	// 03:00 tests something a feed where they arrive at noon does not.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}

	admit := fieldOf(t, string(out), "PV1", 44)
	if !strings.HasSuffix(admit, "143000") {
		t.Errorf("PV1-44 is %q; the time of day should be unchanged", admit)
	}
}

func TestCodedValuesAreKept(t *testing.T) {
	// A corpus without codes cannot test a mapping, and a code table value identifies nobody.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if sex := fieldOf(t, got, "PID", 8); sex != "M" {
		t.Errorf("PID-8 became %q, want M kept", sex)
	}
	if class := fieldOf(t, got, "PV1", 2); class != "I" {
		t.Errorf("PV1-2 became %q, want I kept", class)
	}
	if rel := fieldOf(t, got, "NK1", 3); rel != "SPO" {
		t.Errorf("NK1-3 became %q, want SPO kept", rel)
	}
	if !strings.Contains(got, "ADT^A01") {
		t.Error("the message type was not kept, so the corpus cannot be routed")
	}
	if !strings.Contains(got, "|2.5|") && !strings.Contains(got, "2.5") {
		t.Error("the HL7 version was not kept")
	}
}

func TestAnUnnamedFieldIsScrubbedNotKept(t *testing.T) {
	// Deny by default, which is the whole security argument. A scrubber built from a list of
	// fields to remove is safe only for the messages its author examined.
	sc := newScrubber(t)

	// PID-33 is the last update timestamp; nothing names it in the rules.
	msg := "MSH|^~\\&|E|F|R|R|20260819||ADT^A01|C1|P|2.5\r" +
		"PID|1||0001234^^^MRN||DOE^JANE|||||||||||||||||||||||||||||SECRETVALUE\r"

	out, err := sc.Message([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SECRETVALUE") {
		t.Error("a field no rule names was passed through, which defeats deny-by-default")
	}
	if sc.Stats().UnknownFields == 0 {
		t.Error("the unnamed field was not counted, so a run cannot report how well its rules " +
			"describe the feed")
	}
}

func TestALocalSegmentIsDroppedByDefault(t *testing.T) {
	// A Z-segment is where a site puts the data that did not fit anywhere else, which in
	// practice means names, notes and identifiers.
	sc := newScrubber(t)

	msg := "MSH|^~\\&|E|F|R|R|20260819||ADT^A01|C1|P|2.5\r" +
		"PID|1||0001234^^^MRN||DOE^JANE\r" +
		"ZPI|1|PATIENT NOTES SAYING SOMETHING PRIVATE\r"

	out, err := sc.Message([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if strings.Contains(got, "ZPI") {
		t.Error("a Z-segment survived the default configuration")
	}
	if strings.Contains(got, "PRIVATE") {
		t.Error("the Z-segment contents survived")
	}
	if sc.Stats().DroppedSegments != 1 {
		t.Errorf("dropped segments = %d, want 1", sc.Stats().DroppedSegments)
	}
}

func TestAMessageThatDoesNotParseIsRefused(t *testing.T) {
	// Passing it through unchanged would be the single leak in an otherwise clean corpus, and
	// the worst possible failure: the corpus would have been certified safe.
	sc := newScrubber(t)

	if _, err := sc.Message([]byte("this is not a message")); err == nil {
		t.Fatal("an unparseable message was accepted")
	}
	if sc.Stats().Unreadable != 1 {
		t.Error("the unreadable message was not counted")
	}
}

func TestAScrubWithoutASaltIsRefused(t *testing.T) {
	// A predictable salt produces pseudonyms anybody can reproduce, so the output is reversible
	// by guessing while appearing scrubbed.
	for _, bad := range [][]byte{nil, {}, []byte("short")} {
		if _, err := New(Options{Salt: bad}); err == nil {
			t.Errorf("a salt of %d bytes was accepted", len(bad))
		}
	}
}

func TestTheSameSaltGivesTheSameCorpus(t *testing.T) {
	// Reproducibility, so a corpus can be regenerated rather than archived.
	a, err := New(Options{Salt: salt()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{Salt: salt()})
	if err != nil {
		t.Fatal(err)
	}

	first, err := a.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("the same salt produced two different results")
	}
}

func TestADifferentSaltGivesAnUnrelatedCorpus(t *testing.T) {
	// So two organisations scrubbing the same feed cannot compare pseudonyms and re-identify
	// anybody by intersection.
	a, _ := New(Options{Salt: salt()})
	b, _ := New(Options{Salt: []byte("an-entirely-different-salt-here")})

	first, _ := a.Message([]byte(realistic))
	second, _ := b.Message([]byte(realistic))

	if string(first) == string(second) {
		t.Error("two different salts produced the same result")
	}
}

func TestTheRuleSetCanBeRead(t *testing.T) {
	// A rule list is read by whoever has to sign off on sharing the output, so every entry
	// explains itself.
	sc := newScrubber(t)
	rules := sc.Rules()

	if len(rules) < 40 {
		t.Errorf("only %d rules; the default set should cover the common segments", len(rules))
	}
	withReason := 0
	for _, r := range rules {
		if strings.Contains(r, "—") {
			withReason++
		}
	}
	if withReason < len(rules)-2 {
		t.Errorf("only %d of %d rules explain themselves", withReason, len(rules))
	}
}

func TestTheOutputStillParses(t *testing.T) {
	// A corpus that cannot be read is not a corpus.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}

	// Round-tripped through the scrubber a second time: if the output parses, this succeeds.
	sc2 := newScrubber(t)
	if _, err := sc2.Message(out); err != nil {
		t.Fatalf("the scrubbed message does not parse: %v", err)
	}
}

// fieldOf reads one field out of a raw message, for assertions.
func fieldOf(t *testing.T, msg, segment string, field int) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\r") {
		if !strings.HasPrefix(line, segment+"|") {
			continue
		}
		parts := strings.Split(line, "|")
		if field < len(parts) {
			return parts[field]
		}
		return ""
	}
	return ""
}

// dayGap returns the whole days between two yyyymmdd strings.
func dayGap(t *testing.T, from, to string) int {
	t.Helper()
	a := parseDay(t, from)
	b := parseDay(t, to)
	return int(b.Sub(a).Hours() / 24)
}

func parseDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("20060102", s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return d
}

func TestAKeptFieldCanExplainAnApparentLeak(t *testing.T) {
	// Recording the reasoning from a real-corpus check, because it is the trap anybody
	// verifying a scrubber falls into.
	//
	// Searching the whole output for every original value reports hits that are not leaks. The
	// sending facility is kept deliberately - it names an organisation, not a person - so a
	// patient whose town matches the hospital's town produces a hit from a field that was in
	// fact scrubbed. The question that settles it is not "does this value appear anywhere" but
	// "does it still appear in the field it came from".
	sc := newScrubber(t)

	msg := "MSH|^~\\&|EPIC|SPRINGFIELD GENERAL|LAB|LAB|20260819||ADT^A01|C1|P|2.5\r" +
		"PID|1||0001234^^^MRN||DOE^JANE|||||742 ELM STREET^^SPRINGFIELD^IL^62701\r"

	out, err := sc.Message([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// The facility survives, by design.
	if !strings.Contains(got, "SPRINGFIELD GENERAL") {
		t.Error("the sending facility was scrubbed; it names an organisation, not a person, and " +
			"a corpus needs it to be routable")
	}

	// The address does not, which is the assertion that matters. Checked in place rather than
	// across the whole message, for exactly the reason above.
	address := fieldOf(t, got, "PID", 11)
	if strings.Contains(address, "ELM") || strings.Contains(address, "742") {
		t.Errorf("the street address survived in PID-11: %q", address)
	}
	if strings.Contains(address, "62701") {
		t.Errorf("the postcode survived in PID-11: %q", address)
	}
}

func TestEveryDateInTheOutputIsAValidDate(t *testing.T) {
	// A real run turned a date of birth into 76677303, because PID-7 was not named and the
	// default randomised it into eight digits that are not a date. A corpus carrying impossible
	// dates breaks anything that parses them, which is most of what a corpus is for.
	sc := newScrubber(t)

	out, err := sc.Message([]byte(realistic))
	if err != nil {
		t.Fatal(err)
	}

	dob := fieldOf(t, string(out), "PID", 7)
	if dob == "" {
		t.Fatal("the date of birth was emptied; age is clinically significant and a corpus " +
			"without it cannot test a demographics mapping")
	}
	if dob == "19700315" {
		t.Fatal("the date of birth was not changed at all")
	}
	if _, err := time.Parse("20060102", dob[:8]); err != nil {
		t.Errorf("the date of birth became %q, which is not a date", dob)
	}
}

func TestAnUnnamedTimestampIsShiftedNotRandomised(t *testing.T) {
	// The general fix, checked through a field no rule names.
	sc := newScrubber(t)

	// OBR-6 is the requested date and is not in the rule set.
	msg := "MSH|^~\\&|E|F|R|R|20260819||ORU^R01|C1|P|2.5\r" +
		"PID|1||0001234^^^MRN||DOE^JANE|||||||\r" +
		"OBR|1|||GLU|||20260401120000\r"

	out, err := sc.Message([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}

	got := fieldOf(t, string(out), "OBR", 7)
	if got == "" {
		t.Fatal("OBR-7 was emptied")
	}
	if got == "20260401120000" {
		t.Fatal("OBR-7 was not changed")
	}
	if _, err := time.Parse("20060102", got[:8]); err != nil {
		t.Errorf("OBR-7 became %q, which is not a date", got)
	}
	if !strings.HasSuffix(got, "120000") {
		t.Errorf("OBR-7 is %q; the time of day should survive a date shift", got)
	}
}

func TestARecordNumberIsNotMistakenForADate(t *testing.T) {
	// The shape test has to be strict, or a seven or eight digit identifier gets shifted as a
	// date. Shifting one would still de-identify it, but it would also change its length
	// characteristics for no reason.
	if looksLikeTimestamp("1234567") {
		t.Error("a seven-digit value was treated as a timestamp")
	}
	if looksLikeTimestamp("20261340") {
		t.Error("an impossible date was treated as a timestamp")
	}
	if !looksLikeTimestamp("20260819") {
		t.Error("a plain date was not recognised")
	}
	if !looksLikeTimestamp("20260819143000") {
		t.Error("a full timestamp was not recognised")
	}
}
