package ncpdp

import (
	"strings"
	"testing"
)

// claimTransmission builds a billing request with a repeating DUR segment and two claim segments, which is the shape that
// makes repeats worth addressing.
func claimTransmission(t *testing.T) *Message {
	t.Helper()

	m := Message{
		Header: Header{
			BIN:                    "610279",
			VersionRelease:         "D0",
			TransactionCode:        TxBilling,
			ProcessorControlNumber: "P012345678",
			TransactionCount:       "1",

			ServiceProviderIDQualifier:    "01",
			ServiceProviderID:             "1234567890",
			DateOfService:                 "20260829",
			SoftwareVendorCertificationID: "PERFUSE001",
		},
		Transactions: []Transaction{{
			Segments: []Segment{
				{ID: SegPatient, Fields: []Field{
					{ID: "C4", Value: "19551014"},
					{ID: "C5", Value: "OKONKWO"},
				}},
				{ID: SegInsurance, Fields: []Field{
					{ID: "C2", Value: "MEMBER12345"},
				}},
				{ID: SegClaim, Fields: []Field{
					{ID: "D7", Value: "00093721410"},
					{ID: "E7", Value: "30"},
					{ID: "D5", Value: "30"},
				}},
				{ID: SegClaim, Fields: []Field{
					{ID: "D7", Value: "00378180505"},
					{ID: "E7", Value: "90"},
					{ID: "D5", Value: "90"},
				}},
				{ID: SegDUR, Fields: []Field{
					{ID: "E4", Value: "DD"},
					{ID: "E4", Value: "TD"},
				}},
			},
		}},
	}
	return &m
}

func TestAPathReadsTheHeaderByItsFieldIdentifier(t *testing.T) {
	m := claimTransmission(t)

	for path, want := range map[string]string{
		"A1": "610279",
		"A3": TxBilling,
		"B1": "1234567890",
		"D1": "20260829",
		"AK": "PERFUSE001",
	} {
		p, err := ParsePath(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got := Read(*m, p)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", path, got, want)
		}
	}
}

func TestABareFieldIdentifierSearchesEverySegment(t *testing.T) {
	m := claimTransmission(t)

	p, err := ParsePath("D7")
	if err != nil {
		t.Fatal(err)
	}
	// Both claim segments, in wire order. A transmission carrying two drugs has two product codes, and a filter asking
	// about either must see both.
	got := Read(*m, p)
	if len(got) != 2 || got[0] != "00093721410" || got[1] != "00378180505" {
		t.Errorf("D7 = %v, want both product codes", got)
	}
}

func TestASegmentQualifierPicksOneSegment(t *testing.T) {
	m := claimTransmission(t)

	p, err := ParsePath("07(2)-D7")
	if err != nil {
		t.Fatal(err)
	}
	got := Read(*m, p)
	if len(got) != 1 || got[0] != "00378180505" {
		t.Errorf("07(2)-D7 = %v, want the second product code", got)
	}

	// Unqualified by occurrence, the same segment identifier addresses both.
	p, err = ParsePath("07-D7")
	if err != nil {
		t.Fatal(err)
	}
	if got := Read(*m, p); len(got) != 2 {
		t.Errorf("07-D7 = %v, want both", got)
	}
}

func TestARepeatingFieldReturnsEveryValue(t *testing.T) {
	m := claimTransmission(t)

	p, err := ParsePath("08-E4")
	if err != nil {
		t.Fatal(err)
	}
	// Two DUR conflicts. Reading only the first would make a filter looking for a therapeutic duplication miss it when a
	// drug-drug interaction was reported first, which is the case that matters.
	got := Read(*m, p)
	if len(got) != 2 || got[0] != "DD" || got[1] != "TD" {
		t.Errorf("08-E4 = %v, want both conflict codes", got)
	}
}

func TestABadPathIsRefused(t *testing.T) {
	for _, bad := range []string{
		"",
		"D",           // one character
		"D1X",         // three
		"1D",          // does not begin with a letter
		"7-D7",        // segment identifier is not two digits
		"AA-D7",       // segment identifier is not digits
		"07(0)-D7",    // occurrences count from 1
		"07(x)-D7",    // not a number
		"07(2-D7",     // unclosed
		"07-",         // no field
		"07-D",        // field too short
		"D1-D1",       // segment identifier is not two digits
		"//birthTime", // an HL7 v3 path
		"PID-3",       // an HL7 v2 path
	} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) was accepted", bad)
		}
	}
}

func TestTheFilterUsesTheSharedGrammar(t *testing.T) {
	m := claimTransmission(t)

	for _, src := range []string{
		`A3 == "B1"`,
		`A1 == "610279"`,
		`D1 == "20260829"`,
		`D7 == "00378180505"`,   // the second claim, matched through repeats
		`07(1)-E7 == "30"`,      // the first claim's quantity
		`08-E4 in ["TD", "XX"]`, // the second DUR conflict
		`C2 =~ "^MEMBER"`,
		`D7 exists`,
		`not C2 empty`,
		`E7 > 20`, // numeric ordering, unlike v3
		`A3 == "B1" and 07-D5 == "30"`,
		`ZZ empty`, // a field the transmission does not carry
	} {
		f, err := ParseFilter(src)
		if err != nil {
			t.Fatalf("%s: did not compile: %v", src, err)
		}
		ok, err := f.Eval(m)
		if err != nil {
			t.Fatalf("%s: did not evaluate: %v", src, err)
		}
		if !ok {
			t.Errorf("%s: did not match", src)
		}
	}
}

func TestTheFilterExcludesWhatItShould(t *testing.T) {
	m := claimTransmission(t)

	for _, src := range []string{
		`A3 == "B2"`,
		`D7 == "00000000000"`,
		`ZZ exists`,
		`07(1)-E7 > 100`,
		`C2 =~ "^GUEST"`,
	} {
		f, err := ParseFilter(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		ok, err := f.Eval(m)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if ok {
			t.Errorf("%s: matched, but it should not have", src)
		}
	}
}

func TestAFilterNamingABadPathIsRefusedAtCompileTime(t *testing.T) {
	// Compiled at load, so a mistyped field refuses the channel rather than producing a filter that silently never
	// matches - which for a pharmacy channel means every claim excluded or every claim let through.
	_, err := ParseFilter(`NOTAFIELD == "x"`)
	if err == nil {
		t.Fatal("a filter with an unaddressable path was accepted")
	}
	if !strings.Contains(err.Error(), "field identifier") {
		t.Errorf("the error should explain what a field identifier looks like, got: %v", err)
	}
}
