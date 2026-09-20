package hl7

import (
	"fmt"
	"strings"
	"testing"
)

// A synthetic ADT^A01. Every identifier here is invented.
const adtA01 = "MSH|^~\\&|SENDAPP|SENDFAC|RECVAPP|RECVFAC|20260818120000||ADT^A01^ADT_A01|MSG00001|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN123456^^^SENDFAC^MR~999887777^^^SSA^SS||Doe^Jane^Q^^Ms.^^L||19800101|F|||123 Main St^Apt 4^Birmingham^AL^35205^USA\r" +
	"PV1|1|I|ICU^0201^01^SENDFAC||||1234^Attending^Adam^^^Dr.|||MED||||||||V001\r"

func mustParse(t *testing.T, s string) *Message {
	t.Helper()
	m, err := ParseString(s)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	return m
}

func TestParseSeparators(t *testing.T) {
	m := mustParse(t, adtA01)
	sep := m.Separators()

	if sep.Field != '|' || sep.Component != '^' || sep.Repeat != '~' ||
		sep.Escape != '\\' || sep.Subcomponent != '&' {
		t.Errorf("separators = %+v, want the standard set", sep)
	}
	if got, want := sep.EncodingCharacters(), "^~\\&"; got != want {
		t.Errorf("EncodingCharacters() = %q, want %q", got, want)
	}
}

func TestParseCustomSeparators(t *testing.T) {
	// Some senders use non-standard delimiters. They must be read from MSH,
	// never assumed.
	msg := "MSH#@!$%#SENDAPP#SENDFAC#RECV#FAC#20260818##ADT@A01#MSG1#P#2.5.1\r" +
		"PID#1##MRN1##Doe@Jane\r"
	m := mustParse(t, msg)

	sep := m.Separators()
	if sep.Field != '#' || sep.Component != '@' || sep.Repeat != '!' ||
		sep.Escape != '$' || sep.Subcomponent != '%' {
		t.Fatalf("separators = %+v", sep)
	}
	if got := m.MustGet("PID-5.2"); got != "Jane" {
		t.Errorf("PID-5.2 = %q, want %q", got, "Jane")
	}
}

func TestSegmentIndex(t *testing.T) {
	m := mustParse(t, adtA01)

	if got, want := m.SegmentCount(), 4; got != want {
		t.Errorf("SegmentCount() = %d, want %d", got, want)
	}
	if got, want := strings.Join(m.SegmentNames(), ","), "MSH,EVN,PID,PV1"; got != want {
		t.Errorf("SegmentNames() = %q, want %q", got, want)
	}
	if _, ok := m.Segment("PID", 1); !ok {
		t.Error("PID not found")
	}
	if _, ok := m.Segment("PID", 2); ok {
		t.Error("found a second PID that does not exist")
	}
	if _, ok := m.Segment("OBX", 1); ok {
		t.Error("found an OBX that does not exist")
	}
}

// TestMSHFieldNumbering covers the classic HL7 parser bug. MSH-1 is the field
// separator and MSH-2 is the encoding characters, so MSH field numbers are
// shifted by one against every other segment. Getting this wrong shifts every
// MSH value by one position, which is the sort of defect that reaches a chart.
func TestMSHFieldNumbering(t *testing.T) {
	m := mustParse(t, adtA01)
	msh, ok := m.Segment("MSH", 1)
	if !ok {
		t.Fatal("no MSH")
	}

	cases := []struct {
		field int
		want  string
	}{
		{1, "|"},
		{2, "^~\\&"},
		{3, "SENDAPP"},
		{4, "SENDFAC"},
		{5, "RECVAPP"},
		{6, "RECVFAC"},
		{7, "20260818120000"},
		{8, ""},
		{10, "MSG00001"},
		{11, "P"},
		{12, "2.5.1"},
	}
	for _, c := range cases {
		if got := msh.Field(c.field).String(); got != c.want {
			t.Errorf("MSH-%d = %q, want %q", c.field, got, c.want)
		}
	}

	if got, want := msh.Field(9).Raw(), "ADT^A01^ADT_A01"; got != want {
		t.Errorf("MSH-9 = %q, want %q", got, want)
	}
}

func TestMessageTypeAndControlID(t *testing.T) {
	m := mustParse(t, adtA01)

	typ, event, structure := m.Type()
	if typ != "ADT" || event != "A01" || structure != "ADT_A01" {
		t.Errorf("Type() = (%q, %q, %q), want (ADT, A01, ADT_A01)", typ, event, structure)
	}
	if got, want := m.ControlID(), "MSG00001"; got != want {
		t.Errorf("ControlID() = %q, want %q", got, want)
	}
}

func TestComponentsAndSubcomponents(t *testing.T) {
	m := mustParse(t, adtA01)

	cases := map[string]string{
		"PID-5.1":  "Doe",
		"PID-5.2":  "Jane",
		"PID-5.3":  "Q",
		"PID-5.5":  "Ms.",
		"PID-5.7":  "L",
		"PID-11.3": "Birmingham",
		"PID-11.4": "AL",
		"PID-11.5": "35205",
		"PV1-2":    "I",
		"PV1-3.1":  "ICU",
		"PV1-3.2":  "0201",
		"PV1-7.2":  "Attending",
		"PID-8":    "F",
		"PID-7":    "19800101",
	}
	for path, want := range cases {
		if got := m.MustGet(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestFieldRepetitions(t *testing.T) {
	m := mustParse(t, adtA01)

	v, err := m.Value("PID-3")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.RepeatCount(), 2; got != want {
		t.Fatalf("PID-3 RepeatCount() = %d, want %d", got, want)
	}

	// Component addressing without an explicit repetition reads the first,
	// which is how interface specifications are conventionally written.
	if got, want := m.MustGet("PID-3.1"), "MRN123456"; got != want {
		t.Errorf("PID-3.1 = %q, want %q", got, want)
	}
	if got, want := m.MustGet("PID-3(1).1"), "MRN123456"; got != want {
		t.Errorf("PID-3(1).1 = %q, want %q", got, want)
	}
	if got, want := m.MustGet("PID-3(2).1"), "999887777"; got != want {
		t.Errorf("PID-3(2).1 = %q, want %q", got, want)
	}
	if got, want := m.MustGet("PID-3(2).4"), "SSA"; got != want {
		t.Errorf("PID-3(2).4 = %q, want %q", got, want)
	}
	if got, want := m.MustGet("PID-3(1).5"), "MR"; got != want {
		t.Errorf("PID-3(1).5 = %q, want %q", got, want)
	}
	if got := m.MustGet("PID-3(3).1"); got != "" {
		t.Errorf("PID-3(3).1 = %q, want empty for a repetition that is absent", got)
	}
}

func TestSegmentOccurrence(t *testing.T) {
	msg := "MSH|^~\\&|A|B|C|D|20260818||ORU^R01|1|P|2.5.1\r" +
		"OBX|1|NM|GLU^Glucose^LN||95|mg/dL|70-110|N\r" +
		"OBX|2|NM|NA^Sodium^LN||139|mmol/L|135-145|N\r" +
		"OBX|3|NM|K^Potassium^LN||5.9|mmol/L|3.5-5.1|H\r"
	m := mustParse(t, msg)

	if got, want := len(m.Segments("OBX")), 3; got != want {
		t.Fatalf("OBX count = %d, want %d", got, want)
	}
	cases := map[string]string{
		"OBX(1)-3.2": "Glucose",
		"OBX(2)-5":   "139",
		"OBX(3)-3.1": "K",
		"OBX(3)-8":   "H",
		"OBX(3)-7":   "3.5-5.1",
	}
	for path, want := range cases {
		if got := m.MustGet(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if got := m.MustGet("OBX(4)-5"); got != "" {
		t.Errorf("OBX(4)-5 = %q, want empty", got)
	}
}

func TestAbsentAndEmptyAreBothEmpty(t *testing.T) {
	m := mustParse(t, adtA01)

	// A field beyond the end of the segment.
	if got := m.MustGet("PID-30"); got != "" {
		t.Errorf("PID-30 = %q, want empty", got)
	}
	// A segment that is not present.
	if got := m.MustGet("ZZZ-1"); got != "" {
		t.Errorf("ZZZ-1 = %q, want empty", got)
	}

	// Exists distinguishes them for callers that need to know.
	present, _ := m.Value("PID-6")
	if !present.Exists() {
		t.Error("PID-6 is present but empty; Exists() should be true")
	}
	absent, _ := m.Value("PID-30")
	if absent.Exists() {
		t.Error("PID-30 is beyond the segment; Exists() should be false")
	}
}

func TestExplicitNull(t *testing.T) {
	// Two double quotes is HL7's explicit null: delete this value, as opposed
	// to an empty field, which means no change.
	m := mustParse(t, "MSH|^~\\&|A|B|C|D|20260818||ADT^A08|1|P|2.5.1\rPID|1||MRN1||Doe^Jane||\"\"\r")

	v, err := m.Value("PID-7")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Exists() {
		t.Fatal("PID-7 should exist")
	}
	if !v.IsEmpty() {
		t.Error("explicit null should report IsEmpty() = true")
	}
	if got := v.Raw(); got != `""` {
		t.Errorf("Raw() = %q, want the literal null so callers can tell it apart", got)
	}
	if got := v.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}
}

func TestSegmentTerminators(t *testing.T) {
	body := "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|1|P|2.5.1%sPID|1||MRN1||Doe^Jane%s"
	for name, term := range map[string]string{
		"CR":   "\r",
		"LF":   "\n",
		"CRLF": "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := mustParse(t, sprintf(body, term, term))
			if got, want := m.SegmentCount(), 2; got != want {
				t.Fatalf("SegmentCount() = %d, want %d", got, want)
			}
			if got, want := m.MustGet("PID-5.2"), "Jane"; got != want {
				t.Errorf("PID-5.2 = %q, want %q", got, want)
			}
		})
	}
}

func TestMLLPFramingIsStripped(t *testing.T) {
	framed := append([]byte{mllpStart}, []byte(adtA01)...)
	framed = append(framed, mllpEnd, '\r')

	m, err := Parse(framed)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := m.SegmentCount(), 4; got != want {
		t.Errorf("SegmentCount() = %d, want %d", got, want)
	}
	if got := m.Raw()[0]; got == mllpStart {
		t.Error("MLLP start block left in the message")
	}
}

func TestRejectsNonHL7(t *testing.T) {
	for _, in := range []string{"", "MSH", "{\"resourceType\":\"Patient\"}", "PID|1||MRN1\r"} {
		if _, err := ParseString(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}

func TestUnescape(t *testing.T) {
	sep := DefaultSeparators()
	cases := map[string]string{
		`plain`:                 `plain`,
		`Smith \F\ Jones`:       `Smith | Jones`,
		`A\S\B`:                 `A^B`,
		`A\T\B`:                 `A&B`,
		`A\R\B`:                 `A~B`,
		`A\E\B`:                 `A\B`,
		`\X0D\`:                 "\r",
		`\X48493C\`:             "HI<",
		`bold\H\text\N\normal`:  `boldtextnormal`,
		`line one\.br\line two`: "line one\nline two",
		`half \ escape`:         `half \ escape`,
		`\Zunknown\`:            ``,
		`\Q9\`:                  `\Q9\`,
	}
	for in, want := range cases {
		if got := Unescape([]byte(in), sep); got != want {
			t.Errorf("Unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeRoundTrip(t *testing.T) {
	sep := DefaultSeparators()
	for _, raw := range []string{
		`plain`,
		`Smith | Jones`,
		`A^B`,
		`A&B~C`,
		`back\slash`,
		`all|of^them~at\once&here`,
	} {
		escaped := Escape(raw, sep)
		if got := Unescape([]byte(escaped), sep); got != raw {
			t.Errorf("round trip of %q gave %q via %q", raw, got, escaped)
		}
	}
}

func TestUnescapeInMessage(t *testing.T) {
	m := mustParse(t, "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|1|P|2.5.1\r"+
		"PID|1||MRN1||O\\T\\Brien^Se\\S\\an\r")

	if got, want := m.MustGet("PID-5.1"), "O&Brien"; got != want {
		t.Errorf("PID-5.1 = %q, want %q", got, want)
	}
	if got, want := m.MustGet("PID-5.2"), "Se^an"; got != want {
		t.Errorf("PID-5.2 = %q, want %q", got, want)
	}
	// Raw must still show the escape, so a message can be forwarded unchanged.
	v, _ := m.Value("PID-5.1")
	if got, want := v.Raw(), `O\T\Brien`; got != want {
		t.Errorf("Raw() = %q, want %q", got, want)
	}
}

func TestParsePath(t *testing.T) {
	cases := map[string]Path{
		"MSH-9":     {Segment: "MSH", SegmentOccurs: 1, Field: 9},
		"MSH.9.2":   {Segment: "MSH", SegmentOccurs: 1, Field: 9, Component: 2},
		"PID-5.1.2": {Segment: "PID", SegmentOccurs: 1, Field: 5, Component: 1, Subcomponent: 2},
		"PID-3(2)":  {Segment: "PID", SegmentOccurs: 1, Field: 3, FieldRepeat: 2},
		"OBX(3)-5":  {Segment: "OBX", SegmentOccurs: 3, Field: 5},
		"OBX[3]-5(2).1": {
			Segment: "OBX", SegmentOccurs: 3, Field: 5, FieldRepeat: 2, Component: 1,
		},
		"ZPD-1":  {Segment: "ZPD", SegmentOccurs: 1, Field: 1},
		"pid-5":  {Segment: "PID", SegmentOccurs: 1, Field: 5},
		"PID":    {Segment: "PID", SegmentOccurs: 1},
		"PID(2)": {Segment: "PID", SegmentOccurs: 2},
	}
	for in, want := range cases {
		got, err := ParsePath(in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePath(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestParsePathErrors(t *testing.T) {
	for _, in := range []string{"", "-5", "PID-", "PID-0", "PID-5.", "PID(0)-5", "PID(2-5", "PID-5.1.2.3", "PID-5x"} {
		if p, err := ParsePath(in); err == nil {
			t.Errorf("ParsePath(%q) = %+v, want an error", in, p)
		}
	}
}

func TestPathString(t *testing.T) {
	for _, in := range []string{"MSH-9", "MSH-9.2", "PID-5.1.2", "PID-3(2)", "OBX(3)-5", "PID"} {
		p, err := ParsePath(in)
		if err != nil {
			t.Fatalf("ParsePath(%q): %v", in, err)
		}
		if got := p.String(); got != in {
			t.Errorf("Path(%q).String() = %q", in, got)
		}
	}
}

func TestWholeSegmentPath(t *testing.T) {
	m := mustParse(t, adtA01)
	got := m.MustGet("EVN")
	if want := "EVN|A01|20260818115900"; got != want {
		t.Errorf("EVN = %q, want %q", got, want)
	}
}

func TestGetInvalidPathReturnsError(t *testing.T) {
	m := mustParse(t, adtA01)
	if _, err := m.Get("not a path"); err == nil {
		t.Error("expected an error for a malformed path")
	}
}

func TestRawIsUnmodified(t *testing.T) {
	m := mustParse(t, adtA01)
	// The parser must not rewrite the message. An interface engine has to be
	// able to forward exactly what it received.
	if got := m.String(); got != strings.TrimRight(adtA01, "\r") {
		t.Errorf("message was modified during parsing:\n got %q\nwant %q", got, adtA01)
	}
}

func TestTrailingEmptyFields(t *testing.T) {
	m := mustParse(t, "MSH|^~\\&|A|B|C|D|20260818||ADT^A01|1|P|2.5.1\rPID|1||MRN1|||||\r")
	pid, _ := m.Segment("PID", 1)

	// Trailing separators create present-but-empty fields, which must be
	// distinguishable from fields past the end.
	if !pid.Field(8).Exists() {
		t.Error("PID-8 should exist as an empty field")
	}
	if pid.Field(9).Exists() {
		t.Error("PID-9 is past the end of the segment")
	}
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
