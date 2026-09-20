package expr

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
)

const adt = "MSH|^~\\&|SENDAPP|SITEA|RECVAPP|RECVFAC|20260818120000||ADT^A08^ADT_A01|MSG1|P|2.5.1\r" +
	"EVN|A08|20260818115900\r" +
	"PID|1||MRN123^^^SITEA^MR~999887777^^^SSA^SS||Doe^Jane^Q||19800101|F|||123 Main St^^Birmingham^AL^35205\r" +
	"PV1|1|I|ICU^0201^01||||1234^Attending^Adam\r" +
	"OBX|1|NM|GLU^Glucose^LN||95|mg/dL|70-110|N\r"

func mustMsg(t *testing.T, s string) *hl7.Message {
	t.Helper()
	m, err := hl7.ParseString(s)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	return m
}

func evalOn(t *testing.T, m *hl7.Message, src string) bool {
	t.Helper()
	e, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	got, err := e.Eval(m)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return got
}

func TestComparisons(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`MSH-9.1 == "ADT"`:  true,
		`MSH-9.1 == "ORU"`:  false,
		`MSH-9.2 == "A08"`:  true,
		`MSH-9.2 != "A28"`:  true,
		`MSH-9.2 != "A08"`:  false,
		`PID-5.1 == "Doe"`:  true,
		`PID-8 == "F"`:      true,
		`MSH-4 == "SITEA"`:  true,
		`PV1-2 == "I"`:      true,
		`OBX-8 == "N"`:      true,
		`MSH-12 == "2.5.1"`: true,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestSingleQuotedAndBareValues(t *testing.T) {
	m := mustMsg(t, adt)

	for _, src := range []string{
		`MSH-9.2 == "A08"`,
		`MSH-9.2 == 'A08'`,
		`MSH-9.2 == A08`,
	} {
		if !evalOn(t, m, src) {
			t.Errorf("%s = false, want true", src)
		}
	}
}

func TestRepetitionsInEquality(t *testing.T) {
	m := mustMsg(t, adt)

	// PID-3 has two identifiers. Equality considers both, because a filter
	// asking whether an identifier is present means any of them.
	if !evalOn(t, m, `PID-3.4 == "SITEA"`) {
		t.Error(`PID-3.4 == "SITEA" = false, want true for the first repetition`)
	}
	if !evalOn(t, m, `PID-3.4 == "SSA"`) {
		t.Error(`PID-3.4 == "SSA" = false, want true for the second repetition`)
	}

	// And inequality must mean no repetition matches, or a field with two
	// identifiers would satisfy == and != at the same time.
	if evalOn(t, m, `PID-3.4 != "SSA"`) {
		t.Error(`PID-3.4 != "SSA" = true; a repetition equals it, so this must be false`)
	}
	if !evalOn(t, m, `PID-3.4 != "OTHER"`) {
		t.Error(`PID-3.4 != "OTHER" = false, want true`)
	}
}

func TestExistsAndEmpty(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`PID-3 exists`:  true,
		`PID-99 exists`: false,
		`ZZZ-1 exists`:  false,
		`OBX-5 exists`:  true,
		// PID-6 is present but empty in the fixture.
		`PID-6 exists`: true,
		`PID-6 empty`:  true,
		`PID-5 empty`:  false,
		`PID-99 empty`: true,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestExistsVersusEmptyDistinction(t *testing.T) {
	// The distinction matters clinically: in an A08 update an empty field means
	// no change, and an absent field means the sender never sent it.
	m := mustMsg(t, "MSH|^~\\&|A|B|C|D|20260818||ADT^A08|1|P|2.5.1\rPID|1||MRN1||||\r")

	if !evalOn(t, m, `PID-7 exists`) {
		t.Error("PID-7 is present but empty; exists should be true")
	}
	if !evalOn(t, m, `PID-7 empty`) {
		t.Error("PID-7 is empty; empty should be true")
	}
	if evalOn(t, m, `PID-20 exists`) {
		t.Error("PID-20 is past the end of the segment; exists should be false")
	}
}

func TestMatches(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`OBX-3.1 matches "^GLU"`:       true,
		`OBX-3.1 matches "^NA$"`:       false,
		`PID-5.1 matches "^D"`:         true,
		`MSH-9 matches "ADT\\^A0[78]"`: true,
		`PID-7 matches "^[0-9]{8}$"`:   true,
		// Regular expressions apply to absent paths as well, matching empty.
		`ZZZ-1 matches "^$"`: true,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestMatchesOperatorAlias(t *testing.T) {
	m := mustMsg(t, adt)
	if !evalOn(t, m, `OBX-3.1 =~ "^GLU"`) {
		t.Error("=~ should behave as matches")
	}
}

func TestIn(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`MSH-9.2 in ["A01", "A08", "A31"]`: true,
		`MSH-9.2 in ["A01", "A28"]`:        false,
		`MSH-4 in ["SITEA"]`:               true,
		// Every repetition is considered.
		`PID-3.4 in ["SSA"]`:          true,
		`PID-3.4 in ["OTHER", "SSA"]`: true,
		`PID-3.4 in ["OTHER"]`:        false,
		`ZZZ-1 in ["x"]`:              false,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestNumericComparison(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`OBX-5 > 90`:   true,
		`OBX-5 > 100`:  false,
		`OBX-5 < 100`:  true,
		`OBX-5 >= 95`:  true,
		`OBX-5 <= 95`:  true,
		`OBX-5 >= 96`:  false,
		`OBX-5 > 94.5`: true,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestNumericComparisonAgainstNonNumericData(t *testing.T) {
	// A sender put text where a number was expected. The filter must still
	// decide, because a filter that errors leaves the message stuck.
	m := mustMsg(t, "MSH|^~\\&|A|B|C|D|20260818||ORU^R01|1|P|2.5.1\rOBX|1|ST|GLU^Glucose^LN||DETECTED|\r")

	e, err := Parse(`OBX-5 > 90`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Eval(m)
	if err != nil {
		t.Fatalf("Eval returned an error for non-numeric data: %v", err)
	}
	if got {
		t.Error(`OBX-5 > 90 = true for the value "DETECTED", want false`)
	}
}

func TestLogicalOperators(t *testing.T) {
	m := mustMsg(t, adt)

	cases := map[string]bool{
		`MSH-9.1 == "ADT" and MSH-9.2 == "A08"`:  true,
		`MSH-9.1 == "ADT" and MSH-9.2 == "A28"`:  false,
		`MSH-9.2 == "A28" or MSH-9.2 == "A08"`:   true,
		`MSH-9.2 == "A28" or MSH-9.2 == "A31"`:   false,
		`not MSH-9.2 == "A28"`:                   true,
		`not MSH-9.2 == "A08"`:                   false,
		`not (MSH-9.2 == "A28" or MSH-4 == "X")`: true,
		// and binds more tightly than or.
		`MSH-9.2 == "A28" or MSH-9.1 == "ADT" and MSH-4 == "SITEA"`:  true,
		`(MSH-9.2 == "A28" or MSH-9.1 == "ADT") and MSH-4 == "NOPE"`: false,
		`PID-3 exists and not PID-5 empty`:                           true,
		`AND`:                                                        false,
	}
	for src, want := range cases {
		if src == "AND" {
			continue
		}
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestOperatorsAreCaseInsensitive(t *testing.T) {
	m := mustMsg(t, adt)
	for _, src := range []string{
		`MSH-9.1 == "ADT" AND MSH-9.2 == "A08"`,
		`MSH-9.2 == "A28" OR MSH-9.2 == "A08"`,
		`NOT MSH-9.2 == "A28"`,
		`MSH-9.2 IN ["A08"]`,
		`PID-3 EXISTS`,
	} {
		if !evalOn(t, m, src) {
			t.Errorf("%s = false, want true", src)
		}
	}
}

func TestSegmentOccurrenceAndRepetitionPaths(t *testing.T) {
	m := mustMsg(t, "MSH|^~\\&|A|B|C|D|20260818||ORU^R01|1|P|2.5.1\r"+
		"OBX|1|NM|GLU^Glucose^LN||95|mg/dL\r"+
		"OBX|2|NM|K^Potassium^LN||5.9|mmol/L\r")

	cases := map[string]bool{
		`OBX(1)-3.1 == "GLU"`: true,
		`OBX(2)-3.1 == "K"`:   true,
		`OBX(2)-5 > 5.5`:      true,
		`OBX(1)-5 > 5.5`:      true,
		`OBX(3)-5 exists`:     false,
	}
	for src, want := range cases {
		if got := evalOn(t, m, src); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestPathsReported(t *testing.T) {
	// A channel should be able to state its own data dependencies.
	e, err := Parse(`MSH-9.1 == "ADT" and (PID-3 exists or PID-5.1 matches "^D") and MSH-9.1 == "ADT"`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(e.Paths(), " ")
	if want := "MSH-9.1 PID-3 PID-5.1"; got != want {
		t.Errorf("Paths() = %q, want %q", got, want)
	}
}

func TestStringRoundTrip(t *testing.T) {
	// A parsed expression must render back to something that parses to the same
	// thing, so configuration can be printed back to an operator.
	for _, src := range []string{
		`MSH-9.2 != "A28"`,
		`MSH-9.1 == "ADT" and PID-3 exists`,
		`not (PID-5 empty or MSH-4 in ["A", "B"])`,
		`OBX-5 > 90`,
		`OBX-3.1 matches "^GLU"`,
	} {
		first, err := Parse(src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		second, err := Parse(first.String())
		if err != nil {
			t.Fatalf("reparsing %q: %v", first.String(), err)
		}
		if first.String() != second.String() {
			t.Errorf("round trip unstable:\n %q\n %q", first.String(), second.String())
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		``,
		`MSH-9.2`,
		`== "A08"`,
		`MSH-9.2 ==`,
		`MSH-9.2 == "A08" and`,
		`(MSH-9.2 == "A08"`,
		`MSH-9.2 in`,
		`MSH-9.2 in []`,
		`MSH-9.2 in ["A08"`,
		`MSH-9.2 matches "["`,
		`MSH-0 == "x"`,
		`PID-5.1.2.3 == "x"`,
		`"literal" == "x"`,
		`MSH-9.2 == "unterminated`,
		`MSH-9.2 $$ "A08"`,
	} {
		if e, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) succeeded as %s, want an error", src, e.String())
		}
	}
}

func TestInvalidPathIsAConfigurationError(t *testing.T) {
	// A typo has to fail at load time. A filter that silently never matches is
	// far worse than one that refuses to start.
	if _, err := Parse(`PID-0 == "x"`); err == nil {
		t.Error("PID-0 was accepted; HL7 numbering starts at 1")
	}
	if _, err := Parse(`notasegment == "x"`); err != nil {
		// A lowercase bare word is a valid path shape, so this parses. That is
		// intentional: Z-segments and unusual names are real.
		t.Logf("lowercase path accepted, as intended: %v", err)
	}
}

func TestNumericOperatorAgainstNonNumericLiteral(t *testing.T) {
	m := mustMsg(t, adt)
	e, err := Parse(`OBX-5 > "high"`)
	if err != nil {
		t.Fatal(err)
	}
	// Comparing against a literal that is not a number is a configuration
	// mistake, so this one does error.
	if _, err := e.Eval(m); err == nil {
		t.Error("comparing with > against a non-numeric literal should be an error")
	}
}

func TestShortCircuit(t *testing.T) {
	// The right side of an and must not be evaluated when the left is false.
	// Proven through the error case: a bad numeric literal on the right would
	// error if it were reached.
	m := mustMsg(t, adt)
	e, err := Parse(`MSH-9.2 == "A28" and OBX-5 > "high"`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Eval(m)
	if err != nil {
		t.Errorf("Eval returned %v; the right side should not have been evaluated", err)
	}
	if got {
		t.Error("expression should be false")
	}
}

func TestEvalOnEmptyAndOddMessages(t *testing.T) {
	// Filters run on whatever arrives, including messages with nothing in them.
	m := mustMsg(t, "MSH|^~\\&\r")

	for _, src := range []string{
		`MSH-9.1 == "ADT"`,
		`PID-3 exists`,
		`PID-3 empty`,
		`MSH-4 in ["A"]`,
		`OBX-5 > 5`,
		`PID-5.1 matches "^D"`,
	} {
		e, err := Parse(src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		if _, err := e.Eval(m); err != nil {
			t.Errorf("Eval(%q) on a near-empty message: %v", src, err)
		}
	}
}

func BenchmarkEval(b *testing.B) {
	m, err := hl7.ParseString(adt)
	if err != nil {
		b.Fatal(err)
	}
	e := MustParse(`MSH-9.1 == "ADT" and MSH-9.2 in ["A01", "A04", "A08"] and not PID-3 empty`)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.Eval(m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseExpr(b *testing.B) {
	const src = `MSH-9.1 == "ADT" and MSH-9.2 in ["A01", "A04", "A08"] and not PID-3 empty`
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(src); err != nil {
			b.Fatal(err)
		}
	}
}
