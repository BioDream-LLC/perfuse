package x12

import (
	"errors"
	"strings"
	"testing"
)

// A realistic 837 professional claim interchange. The ISA is exactly 106 bytes,
// which is what makes the delimiter offsets work.
const claim837 = "ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~" +
	"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"BHT*0019*00*244579*20260819*1253*CH~" +
	"NM1*41*2*SUBMITTER NAME*****46*SUBMITTERID~" +
	"HL*1**20*1~" +
	"CLM*PATIENT-ACCT-1*500*[**11:B:1*Y*A*Y*I~" +
	"SE*6*0001~" +
	"GE*1*1~" +
	"IEA*1*000000001~"

// The same interchange with a trading partner's unusual delimiters. Legal, rare, and
// the file that breaks every parser that assumed "*".
const claimPipes = "ISA|00|          |00|          |ZZ|SUBMITTERID    |ZZ|RECEIVERID     |260819|1253|^|00501|000000001|0|P|>+" +
	"GS|HC|SUBMITTERID|RECEIVERID|20260819|1253|1|X|005010X222A1+" +
	"ST|837|0001|005010X222A1+" +
	"SE|3|0001+" +
	"GE|1|1+" +
	"IEA|1|000000001+"

func TestParseReadsDelimitersFromFixedOffsets(t *testing.T) {
	m, err := Parse([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}

	d := m.Delimiters()
	if d.Element != '*' {
		t.Errorf("element = %q", string(d.Element))
	}
	if d.Component != ':' {
		t.Errorf("component = %q", string(d.Component))
	}
	if d.Segment != '~' {
		t.Errorf("segment = %q", string(d.Segment))
	}
	if d.Repeat != '^' {
		t.Errorf("repeat = %q", string(d.Repeat))
	}
	if m.Version() != "00501" {
		t.Errorf("version = %q", m.Version())
	}
}

func TestUnusualDelimitersAreHonoured(t *testing.T) {
	// The whole reason the offsets are fixed rather than scanned. A parser that
	// assumed "*" would read this entire interchange as one element and report
	// success, which is worse than failing - a silently misparsed claims file is
	// discovered weeks later when the money does not arrive.
	m, err := Parse([]byte(claimPipes))
	if err != nil {
		t.Fatal(err)
	}

	d := m.Delimiters()
	if d.Element != '|' || d.Component != '>' || d.Segment != '+' {
		t.Fatalf("delimiters = %q %q %q, want | > +", string(d.Element), string(d.Component), string(d.Segment))
	}
	if m.SegmentCount() != 6 {
		t.Errorf("segments = %d, want 6", m.SegmentCount())
	}
	st, ok := m.Segment("ST", 1)
	if !ok {
		t.Fatal("no ST segment")
	}
	if got := st.Element(1).String(); got != "837" {
		t.Errorf("ST01 = %q, want 837", got)
	}
}

func TestNonX12InputIsRefused(t *testing.T) {
	for _, in := range []string{
		"",
		"hello",
		"MSH|^~\\&|A|B|C|D|20260819||ADT^A01|1|P|2.5.1\r",
		"GS*HC*A*B*20260819*1253*1*X*005010~",
	} {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrNotX12) {
			t.Errorf("Parse(%q) = %v, want ErrNotX12", truncate(in), err)
		}
	}
}

func TestATruncatedISAIsReportedDistinctly(t *testing.T) {
	// A short ISA almost always means the file was cut off in transfer, not that
	// somebody sent the wrong format. Those call for different actions, so they get
	// different errors.
	short := "ISA*00*          *00*          *ZZ*SUBMITTER"
	_, err := Parse([]byte(short))
	if !errors.Is(err, ErrShortISA) {
		t.Fatalf("Parse(short ISA) = %v, want ErrShortISA", err)
	}
	if !strings.Contains(err.Error(), "106") {
		t.Errorf("the error should say what length was expected: %v", err)
	}
}

func TestLeadingWhitespaceIsTolerated(t *testing.T) {
	// Files arrive with a leading newline more often than anybody would like,
	// especially after passing through a mail gateway.
	m, err := Parse([]byte("\r\n  " + claim837))
	if err != nil {
		t.Fatalf("a leading newline made the file unparseable: %v", err)
	}
	if m.Delimiters().Element != '*' {
		t.Error("delimiters were read from the wrong offset after trimming")
	}
}

func TestSegmentsPerLineAreAccepted(t *testing.T) {
	// Many partners write one segment per line for readability. Legal, because the
	// terminator ends a segment, not the newline.
	withNewlines := strings.ReplaceAll(claim837, "~", "~\n")
	m, err := Parse([]byte(withNewlines))
	if err != nil {
		t.Fatal(err)
	}
	if m.SegmentCount() != 10 {
		t.Errorf("segments = %d, want 10", m.SegmentCount())
	}
	// And the ids are clean rather than carrying a stray newline.
	for i := 0; i < m.SegmentCount(); i++ {
		s, _ := m.SegmentAt(i)
		if strings.ContainsAny(s.ID, "\r\n \t") {
			t.Errorf("segment %d has a dirty id %q", i, s.ID)
		}
	}
}

func TestIdenticalDelimitersAreRefused(t *testing.T) {
	// A file whose element and segment separators match parses into structurally
	// plausible nonsense. Refusing beats producing it.
	bad := strings.Replace(claim837, "*:~", "*:*", 1)
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("identical element and segment delimiters were accepted")
	}
	if !strings.Contains(err.Error(), "unambiguously") {
		t.Errorf("the error should explain why, got: %v", err)
	}
}

func TestAnAlphanumericDelimiterIsRefused(t *testing.T) {
	// Almost always a misaligned ISA - a file with the wrong line endings, or one
	// that has been through a text editor. Refusing with that explanation saves an
	// afternoon.
	bad := []byte(claim837)
	bad[offElementSep] = 'A'
	_, err := Parse(bad)
	if err == nil {
		t.Fatal("a letter was accepted as the element delimiter")
	}
	if !strings.Contains(err.Error(), "misaligned") {
		t.Errorf("the error should suggest the cause, got: %v", err)
	}
}

func TestVersionsBefore00501HaveNoRepetitionSeparator(t *testing.T) {
	// ISA11 held the Interchange Control Standards Identifier, normally "U", before
	// 00501. Treating that as a repetition separator would split elements on a letter
	// that is ordinary data.
	old := strings.Replace(claim837, "*^*00501*", "*U*00401*", 1)
	m, err := Parse([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if m.Delimiters().Repeat != 0 {
		t.Errorf("a 00401 interchange reported a repetition separator %q", string(m.Delimiters().Repeat))
	}

	// And an element containing that character comes back whole.
	seg := newSegment([]byte("REF*XX*AU B"), m.Delimiters())
	got := seg.Element(2).Repetitions()
	if len(got) != 1 || got[0] != "AU B" {
		t.Errorf("Repetitions() = %q, want the whole value unsplit", got)
	}
}

func TestRepetitionsSplitFrom00501(t *testing.T) {
	m, err := Parse([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	seg := newSegment([]byte("REF*XX*A^B^C"), m.Delimiters())
	got := seg.Element(2).Repetitions()
	if len(got) != 3 || got[0] != "A" || got[2] != "C" {
		t.Errorf("Repetitions() = %q, want [A B C]", got)
	}
}

func TestElementAccessIsOneBased(t *testing.T) {
	// Implementation guides number them this way - CLM01 is the first element after
	// "CLM" - and HL7 in this codebase is one-based too. Two conventions in one
	// program is a bug waiting to happen.
	m, err := Parse([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	clm, ok := m.Segment("CLM", 1)
	if !ok {
		t.Fatal("no CLM segment")
	}
	if got := clm.Element(1).String(); got != "PATIENT-ACCT-1" {
		t.Errorf("CLM01 = %q", got)
	}
	if got := clm.Element(2).String(); got != "500" {
		t.Errorf("CLM02 = %q", got)
	}
}

func TestAMissingElementIsEmptyNotAnError(t *testing.T) {
	// Trailing empty elements are routinely omitted in X12. Returning an error for a
	// position past the end would make every caller write the same check.
	m, _ := Parse([]byte(claim837))
	clm, _ := m.Segment("CLM", 1)

	e := clm.Element(99)
	if !e.IsEmpty() {
		t.Errorf("element 99 = %q, want empty", e.String())
	}
	if e.String() != "" {
		t.Error("a missing element should render as empty")
	}
	if got := clm.Element(0).String(); got != "" {
		t.Errorf("element 0 = %q, want empty", got)
	}
	if got := clm.Element(-1).String(); got != "" {
		t.Errorf("element -1 = %q, want empty", got)
	}
}

func TestCompositeElementComponents(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	clm, _ := m.Segment("CLM", 1)

	// CLM05 is the composite place-of-service code, "11:B:1" in the fixture.
	e := clm.Element(5)
	if got := e.ComponentCount(); got != 3 {
		t.Errorf("ComponentCount() = %d, want 3", got)
	}
	if got := e.Component(1); got != "11" {
		t.Errorf("component 1 = %q, want 11", got)
	}
	if got := e.Component(3); got != "1" {
		t.Errorf("component 3 = %q, want 1", got)
	}
	if got := e.Component(4); got != "" {
		t.Errorf("component 4 = %q, want empty", got)
	}
}

func TestANonCompositeElementHasOneComponent(t *testing.T) {
	// So Element(n).Component(1) is always safe and always right, and callers do not
	// need to know whether a given element is composite in a given guide.
	m, _ := Parse([]byte(claim837))
	clm, _ := m.Segment("CLM", 1)

	e := clm.Element(1)
	if got := e.ComponentCount(); got != 1 {
		t.Errorf("ComponentCount() = %d, want 1", got)
	}
	if got := e.Component(1); got != "PATIENT-ACCT-1" {
		t.Errorf("Component(1) = %q", got)
	}
}

func TestAnEmptyElementHasNoComponents(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	hl, _ := m.Segment("HL", 1)

	// HL02 is empty in the fixture.
	e := hl.Element(2)
	if !e.IsEmpty() {
		t.Fatalf("HL02 = %q, expected empty", e.String())
	}
	if got := e.ComponentCount(); got != 0 {
		t.Errorf("ComponentCount() on an empty element = %d, want 0", got)
	}
}

func TestSegmentLookupIsOneBasedAndOrdered(t *testing.T) {
	m, _ := Parse([]byte(claim837))

	if _, ok := m.Segment("ST", 0); ok {
		t.Error("occurrence 0 returned a segment")
	}
	if _, ok := m.Segment("ST", 2); ok {
		t.Error("a second ST was found where there is one")
	}
	if _, ok := m.Segment("NOPE", 1); ok {
		t.Error("a segment that does not exist was found")
	}
}

func TestSegmentsReturnsEveryOccurrence(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	if got := len(m.Segments("NM1")); got != 1 {
		t.Errorf("NM1 count = %d, want 1", got)
	}
	if got := m.Segments("NOPE"); got != nil {
		t.Errorf("Segments for a missing id = %v, want nil", got)
	}
}

func TestSegmentAtBounds(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	if _, ok := m.SegmentAt(-1); ok {
		t.Error("SegmentAt(-1) returned a segment")
	}
	if _, ok := m.SegmentAt(m.SegmentCount()); ok {
		t.Error("SegmentAt(count) returned a segment")
	}
	if s, ok := m.SegmentAt(0); !ok || s.ID != "ISA" {
		t.Errorf("the first segment is %q, want ISA", s.ID)
	}
}

func TestTransactionSetsAnswersWhatIsInTheFile(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	got := m.TransactionSets()
	if len(got) != 1 || got[0] != "837" {
		t.Errorf("TransactionSets() = %v, want [837]", got)
	}
}

func TestTransactionSetsDeduplicatesAndKeepsOrder(t *testing.T) {
	// An interchange can legitimately carry more than one kind, and the same kind
	// more than once.
	multi := strings.Replace(claim837,
		"SE*6*0001~GE*1*1~",
		"SE*6*0001~ST*835*0002~SE*2*0002~ST*837*0003~SE*2*0003~GE*3*1~", 1)
	m, err := Parse([]byte(multi))
	if err != nil {
		t.Fatal(err)
	}
	got := m.TransactionSets()
	if len(got) != 2 || got[0] != "837" || got[1] != "835" {
		t.Errorf("TransactionSets() = %v, want [837 835]", got)
	}
}

func TestDescribeCarriesNoClinicalContent(t *testing.T) {
	// The contents of an 837 are somebody's diagnoses and charges, and a log line is
	// the easiest place for that to end up somewhere it should not.
	m, _ := Parse([]byte(claim837))
	got := m.Describe()

	for _, want := range []string{"00501", "837", "segment"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q", got, want)
		}
	}
	for _, leak := range []string{"PATIENT-ACCT-1", "500", "SUBMITTER NAME"} {
		if strings.Contains(got, leak) {
			t.Errorf("Describe() leaked %q: %s", leak, got)
		}
	}
}

func TestRawIsPreserved(t *testing.T) {
	// Needed for archival and for handing the original bytes to a destination.
	// Reconstructing from the parse would risk changing what a partner sent.
	m, _ := Parse([]byte(claim837))
	if string(m.Raw()) != claim837 {
		t.Error("Raw() did not return the original bytes")
	}
}

func TestSegmentRawAndString(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	st, _ := m.Segment("ST", 1)
	if st.String() != "ST*837*0001*005010X222A1" {
		t.Errorf("String() = %q", st.String())
	}
	if string(st.Raw()) != st.String() {
		t.Error("Raw and String disagree")
	}
	if got := st.ElementCount(); got != 3 {
		t.Errorf("ElementCount() = %d, want 3", got)
	}
}

func TestParseStringMatchesParse(t *testing.T) {
	a, err1 := Parse([]byte(claim837))
	b, err2 := ParseString(claim837)
	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v %v", err1, err2)
	}
	if a.SegmentCount() != b.SegmentCount() {
		t.Error("Parse and ParseString disagree")
	}
}

func truncate(s string) string {
	if len(s) > 30 {
		return s[:30] + "…"
	}
	return s
}
