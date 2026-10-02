package eprescribe

import (
	"encoding/xml"
	"strings"
	"testing"
)

// scriptXML marshals the shared fixture, so these tests filter the same bytes a partner would send rather than a
// hand-written document that might not match what this package produces.
func scriptXML(t *testing.T, m Message) []byte {
	t.Helper()
	raw, err := xml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return raw
}

func treeFor(t *testing.T, m Message) Tree {
	t.Helper()
	tree, err := ParseTree(scriptXML(t, m))
	if err != nil {
		t.Fatalf("ParseTree: %v", err)
	}

	return tree
}

func TestAFilterReadsAPrescription(t *testing.T) {
	tree := treeFor(t, newRx())

	for _, tc := range []struct {
		src  string
		want bool
	}{
		{`//DEASchedule exists`, false},
		{`//NumberOfRefills == "2"`, true},
		{`//NumberOfRefills == "5"`, false},
		{`//DaysSupply == "30"`, true},
		{`//NPI == "1234567893"`, true},
		{`//Gender == "M"`, true},
		{`//DrugDescription =~ "(?i)lisinopril"`, true},
		{`//NumberOfRefills in ["1", "2", "3"]`, true},
		{`//NumberOfRefills == "2" and //DaysSupply == "30"`, true},
		{`not //NumberOfRefills == "9"`, true},
		{`//BusinessName == "MAIN STREET PHARMACY"`, true},
	} {
		f, err := ParseFilter(tc.src)
		if err != nil {
			t.Errorf("%s: parse: %v", tc.src, err)

			continue
		}
		got, err := f.Eval(tree)
		if err != nil {
			t.Errorf("%s: eval: %v", tc.src, err)

			continue
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.src, got, tc.want)
		}
	}
}

// TestAControlledSubstanceCanBeFiltered is the case that motivates the feature.
//
// Routing CII prescriptions differently from ordinary ones is the commonest real reason to filter e-prescribing traffic: they
// carry different legal obligations, and a site frequently wants them on a separate audited path.
func TestAControlledSubstanceCanBeFiltered(t *testing.T) {
	controlled := newRx()
	controlled.Body.NewRx.Medication.Coded.DEASchedule = "CII"

	f, err := ParseFilter(`//DEASchedule == "CII"`)
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.Eval(treeFor(t, controlled))
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("a CII prescription did not match a filter on its schedule")
	}

	got, err = f.Eval(treeFor(t, newRx()))
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an uncontrolled prescription matched a CII filter")
	}
}

// TestAbsentIsNotEmptyOnAPrescription is the semantics borrowed from v3, asserted rather than assumed.
//
// In HL7 v2, PID-8 == "" deliberately tests for a missing field. In XML that conflation is wrong: a prescription with no
// <Note> and one with an empty <Note> are different documents, and a filter that treated them alike could not distinguish
// "the prescriber wrote nothing" from "the prescriber did not have the field".
func TestAbsentIsNotEmptyOnAPrescription(t *testing.T) {
	tree := treeFor(t, newRx())

	f, err := ParseFilter(`//Note == ""`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Eval(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error(`//Note == "" matched a document with no Note element, so absent and empty are being conflated`)
	}

	// The inverse stays true, which is the half that makes the rule usable rather than merely strict.
	f, err = ParseFilter(`//Note != ""`)
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.Eval(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error(`//Note != "" was false for an absent element`)
	}
}

// TestAnUnquotedValueIsRefused is the second borrowed semantic.
//
// v2 accepts MSH-9.2 == A08 and has a test saying so. XML content routinely contains characters the lexer reads as structure,
// so requiring quotes removes a class of confusing parse error.
func TestAnUnquotedValueIsRefused(t *testing.T) {
	if _, err := ParseFilter(`//Gender == M`); err == nil {
		t.Fatal("an unquoted value was accepted on a SCRIPT filter")
	}
}

// TestAValueInAnAttributeIsReadable is the regression for the defect this shares code with rather than copies.
//
// v3 had exactly one operator that knew a value can live in an attribute, and its own comment called that "the mistake v3
// invites". Every comparison against an attribute silently answered false for months. ElementValue and PathValues are exported
// from hl7v3 rather than reimplemented here so that a second XML format cannot inherit half the fix.
func TestAValueInAnAttributeIsReadable(t *testing.T) {
	// An element whose value is an attribute rather than text, which is how much of SCRIPT and all of v3 is written.
	tree, err := ParseTree([]byte(`<Message><Body><NewRx><Medication><WrittenDate value="20260830"/></Medication></NewRx></Body></Message>`))
	if err != nil {
		t.Fatal(err)
	}

	f, err := ParseFilter(`//WrittenDate == "20260830"`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Eval(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("a comparison against a value held in an attribute answered false, which is the v3 defect reappearing")
	}

	// exists has to agree with the comparison. In the original defect it did not: exists reported the value present while
	// every comparison against it said no, which is the combination that makes the bug so hard to see.
	f, err = ParseFilter(`//WrittenDate exists`)
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.Eval(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("exists disagreed with the comparison about the same attribute")
	}
}

// TestTheRootElementIsChecked stops a filter silently matching nothing on the wrong document.
func TestTheRootElementIsChecked(t *testing.T) {
	_, err := ParseTree([]byte(`<ClinicalDocument><recordTarget/></ClinicalDocument>`))
	if err == nil {
		t.Fatal("a CDA document was accepted as a SCRIPT message")
	}
	if !strings.Contains(err.Error(), "ClinicalDocument") {
		t.Errorf("the error should name the root element found, got: %v", err)
	}
}

func TestAnEmptyDocumentIsRefused(t *testing.T) {
	if _, err := ParseTree(nil); err == nil {
		t.Fatal("an empty document was accepted")
	}
}
