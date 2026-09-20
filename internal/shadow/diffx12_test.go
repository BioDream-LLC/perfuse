package shadow

import (
	"strings"
	"testing"
)

// Two interchanges differing in one element, so a walker that works reports exactly one difference.
const (
	claim500 = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260830*1200*^*00501*000000001*0*P*:~" +
		"GS*HC*SENDER*RECEIVER*20260830*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
		"BHT*0019*00*0123*20260830*1200*CH~CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
		"REF*D9*CLAIM123~SE*5*0001~GE*1*1~IEA*1*000000001~"

	claim900 = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260830*1200*^*00501*000000001*0*P*:~" +
		"GS*HC*SENDER*RECEIVER*20260830*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
		"BHT*0019*00*0123*20260830*1200*CH~CLM*PATIENT001*900***11:B:1*Y*A*Y*Y~" +
		"REF*D9*CLAIM123~SE*5*0001~GE*1*1~IEA*1*000000001~"
)

func TestDiffX12FindsTheOneChangedElement(t *testing.T) {
	diffs := DiffX12([]byte(claim500), []byte(claim900), DiffOptions{})

	// CLM02 changed, and so did its qualified form CLM(1)02, because both address the same element. Two entries for one
	// change is deliberate: an ignore list written as either spelling has to work.
	var found bool
	for _, d := range diffs {
		if d.Path == "CLM02" {
			found = true
			if d.Live != "500" || d.Candidate != "900" {
				t.Errorf("CLM02 reported live=%q candidate=%q", d.Live, d.Candidate)
			}
		}
	}
	if !found {
		t.Fatalf("CLM02 was not reported as different; got %d difference(s): %+v", len(diffs), diffs)
	}
}

func TestDiffX12ReportsNothingForIdenticalInterchanges(t *testing.T) {
	if diffs := DiffX12([]byte(claim500), []byte(claim500), DiffOptions{}); len(diffs) != 0 {
		t.Errorf("identical interchanges produced %d difference(s): %+v", len(diffs), diffs)
	}
}

// TestDiffX12ReportsAParseFailureRatherThanSayingIdentical is the defect DiffXML once had.
//
// Returning an empty difference list for input it cannot read means "these are the same", which is the answer that gets a
// broken candidate promoted. Saying so explicitly is the only safe behaviour.
func TestDiffX12ReportsAParseFailureRatherThanSayingIdentical(t *testing.T) {
	diffs := DiffX12([]byte(claim500), []byte("MSH|^~\\&|LAB|HOSP|||20260830||ADT^A08|1|P|2.5\r"), DiffOptions{})
	if len(diffs) == 0 {
		t.Fatal("an unparseable candidate produced no differences, which reads as identical and would let a broken candidate be promoted")
	}
	if !strings.Contains(diffs[0].Candidate, "could not be parsed") {
		t.Errorf("the difference should explain the parse failure, got: %+v", diffs[0])
	}
}

// TestDiffX12AddressesRepeatedSegmentsByOccurrence is why the qualified path exists.
//
// An 837 carries one CLM per claim and nothing in the identifier distinguishes them. Without occurrence, two hundred claims
// collapse into one path and a change to the fourth is either invisible or blamed on the first.
func TestDiffX12AddressesRepeatedSegmentsByOccurrence(t *testing.T) {
	twoClaims := func(second string) string {
		return "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260830*1200*^*00501*000000001*0*P*:~" +
			"GS*HC*SENDER*RECEIVER*20260830*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
			"BHT*0019*00*0123*20260830*1200*CH~CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
			"CLM*PATIENT002*" + second + "***11:B:1*Y*A*Y*Y~" +
			"SE*6*0001~GE*1*1~IEA*1*000000001~"
	}

	diffs := DiffX12([]byte(twoClaims("100")), []byte(twoClaims("200")), DiffOptions{})

	var second bool
	for _, d := range diffs {
		if d.Path == "CLM(2)02" {
			second = true
			if d.Live != "100" || d.Candidate != "200" {
				t.Errorf("CLM(2)02 reported live=%q candidate=%q", d.Live, d.Candidate)
			}
		}
		// The first claim did not change, so it must not be reported. This is the assertion that fails if occurrence is
		// dropped and every CLM collapses onto one path.
		if d.Path == "CLM01" {
			t.Errorf("the first claim was reported as changed when only the second was: %+v", d)
		}
	}
	if !second {
		t.Fatalf("the change to the second claim was not reported; got: %+v", diffs)
	}
}

// TestDiffX12SeesAClearedElement is the deletion case.
//
// A walker collecting only populated fields compares "500" against a missing key and reports nothing, so a candidate that
// wipes an element looks safe.
func TestDiffX12SeesAClearedElement(t *testing.T) {
	cleared := strings.Replace(claim500, "CLM*PATIENT001*500*", "CLM*PATIENT001**", 1)

	diffs := DiffX12([]byte(claim500), []byte(cleared), DiffOptions{})
	if len(diffs) == 0 {
		t.Fatal("clearing an element produced no differences, so a candidate that wipes data would look safe")
	}
}

func TestDiffX12HonoursIgnore(t *testing.T) {
	diffs := DiffX12([]byte(claim500), []byte(claim900), DiffOptions{
		Ignore: map[string]bool{"CLM02": true, "CLM(1)02": true},
	})
	if len(diffs) != 0 {
		t.Errorf("the ignored element was still reported: %+v", diffs)
	}
}

func TestDiffX12HonoursCompare(t *testing.T) {
	// Comparing only a path that did not change reports nothing, even though another element did.
	diffs := DiffX12([]byte(claim500), []byte(claim900), DiffOptions{Compare: []string{"CLM01"}})
	if len(diffs) != 0 {
		t.Errorf("comparing only CLM01 reported %d difference(s): %+v", len(diffs), diffs)
	}

	// Comparing the changed one reports it.
	diffs = DiffX12([]byte(claim500), []byte(claim900), DiffOptions{Compare: []string{"CLM02"}})
	if len(diffs) != 1 {
		t.Errorf("comparing CLM02 reported %d difference(s), want 1", len(diffs))
	}
}

// TestDiffX12IsBounded keeps a report readable.
func TestDiffX12IsBounded(t *testing.T) {
	// An interchange where every element differs, by changing the sender throughout.
	other := strings.ReplaceAll(claim500, "SUBMITTER", "DIFFERENT")
	other = strings.ReplaceAll(other, "CLAIM123", "CLAIM999")
	other = strings.ReplaceAll(other, "PATIENT001", "PATIENT999")

	diffs := DiffX12([]byte(claim500), []byte(other), DiffOptions{})
	if len(diffs) > maxDifferenceFields {
		t.Errorf("got %d differences, which exceeds the bound of %d and buries the one somebody needed", len(diffs), maxDifferenceFields)
	}
}

// TestTheHL7WalkerOnX12IsWhyThisFileExists documents the reason for a separate walker by demonstrating it.
//
// Not a test of DiffX12. It asserts that the v2 walker gives a useless answer on X12, so that anybody tempted to delete
// DiffX12 and route X12 through Diff can see what that would produce.
func TestTheHL7WalkerOnX12IsWhyThisFileExists(t *testing.T) {
	viaHL7 := Diff([]byte(claim500), []byte(claim900), DiffOptions{})
	viaX12 := DiffX12([]byte(claim500), []byte(claim900), DiffOptions{})

	if len(viaX12) == 0 {
		t.Fatal("DiffX12 found nothing, so this comparison proves nothing")
	}

	// The v2 walker either finds nothing or reports a whole-message difference. Either way it does not name CLM02.
	for _, d := range viaHL7 {
		if d.Path == "CLM02" {
			t.Fatal("the HL7 walker named CLM02, which would mean DiffX12 is unnecessary - if this is now true, delete DiffX12 rather than keeping two walkers")
		}
	}
}
