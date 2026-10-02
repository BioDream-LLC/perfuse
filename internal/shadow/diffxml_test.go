package shadow

import (
	"strings"
	"testing"
)

const diffLive = `<?xml version="1.0"?>
<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72.5.1" extension="MSG1"/>
  <controlActProcess>
    <subject>
      <patient>
        <id root="1.2.3" extension="MRN12345"/>
        <id root="4.5.6" extension="123456789"/>
        <statusCode code="active"/>
        <patientPerson>
          <name>
            <given>Marie</given>
            <family>Dubois</family>
          </name>
          <administrativeGenderCode code="F"/>
          <birthTime value="19551014"/>
        </patientPerson>
      </patient>
    </subject>
  </controlActProcess>
</PRPA_IN201306UV02>`

// TestDiffXMLComparesAttributesNotJustText is the property a v2-shaped diff would miss entirely.
//
// In v3 a value is almost always an attribute. A diff comparing only element text would report two documents as identical when
// the birth date, the gender and every identifier had changed - and would report a candidate as safe to promote.
func TestDiffXMLComparesAttributesNotJustText(t *testing.T) {
	candidate := strings.Replace(diffLive, `value="19551014"`, `value="19700101"`, 1)

	got := DiffXML([]byte(diffLive), []byte(candidate), DiffOptions{})
	if len(got) != 1 {
		t.Fatalf("got %d differences, want 1: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Path, "birthTime@value") {
		t.Errorf("the path is %q, want it to name the attribute", got[0].Path)
	}
	if got[0].Live != "19551014" || got[0].Candidate != "19700101" {
		t.Errorf("the values are %q and %q", got[0].Live, got[0].Candidate)
	}
}

// TestDiffXMLReportsPathsTheFilterCanUse covers the notation.
//
// A diff reporting paths in a notation nothing else accepts makes somebody translate by hand, which is where the mistakes are.
// These have to be resolvable by the v3 path language: element names separated by slashes, an occurrence in round brackets, an
// attribute after an at sign.
func TestDiffXMLReportsPathsTheFilterCanUse(t *testing.T) {
	// Change the second identifier, so the path has to carry an occurrence to be unambiguous.
	candidate := strings.Replace(diffLive, `extension="123456789"`, `extension="987654321"`, 1)

	got := DiffXML([]byte(diffLive), []byte(candidate), DiffOptions{})
	if len(got) != 1 {
		t.Fatalf("got %d differences, want 1: %+v", len(got), got)
	}

	path := got[0].Path
	if !strings.Contains(path, "id(2)@extension") {
		t.Errorf("the path is %q; it should carry an occurrence, since id repeats and without one it "+
			"addresses the wrong identifier", path)
	}
	// Round brackets, matching the v3 path language, not square ones.
	if strings.Contains(path, "[") {
		t.Errorf("the path %q uses square brackets, which the v3 path language does not accept", path)
	}
}

// TestDiffXMLDistinguishesRemovedFromEmptied covers a v3-specific distinction.
//
// An element that was removed and an element that was emptied are different clinical statements. A diff showing both as an empty
// string would hide the one that matters - and which one that is depends on the feed, so the diff must not choose.
func TestDiffXMLDistinguishesRemovedFromEmptied(t *testing.T) {
	removed := strings.Replace(diffLive, `<birthTime value="19551014"/>`, ``, 1)
	emptied := strings.Replace(diffLive, `<birthTime value="19551014"/>`, `<birthTime/>`, 1)

	// The element's own path, without the attribute, is what says whether the element still exists.
	element := "/PRPA_IN201306UV02/controlActProcess/subject/patient/patientPerson/birthTime"

	find := func(got []FieldDifference, path string) *FieldDifference {
		for i := range got {
			if got[i].Path == path {
				return &got[i]
			}
		}

		return nil
	}

	t.Run("removing the element says the element went", func(t *testing.T) {
		got := DiffXML([]byte(diffLive), []byte(removed), DiffOptions{})

		d := find(got, element)
		if d == nil {
			t.Fatalf("no difference names the element itself, only its attribute - so a reader cannot "+
				"tell a removed element from an emptied one: %+v", got)
		}
		if d.Candidate != "(removed)" {
			t.Errorf("the element difference says %q rather than reporting it removed", d.Candidate)
		}
	})

	t.Run("emptying the element does not say it went", func(t *testing.T) {
		got := DiffXML([]byte(diffLive), []byte(emptied), DiffOptions{})

		// The value went, which is a difference.
		if d := find(got, element+"@value"); d == nil || d.Candidate != "(removed)" {
			t.Errorf("emptying the element did not report the value going: %+v", got)
		}

		// The element did not, which must not be reported as a difference at all.
		if d := find(got, element); d != nil {
			t.Errorf("an emptied element is reported as though the element itself changed, which is a "+
				"different clinical statement: %+v", *d)
		}
	})
}

// TestDiffXMLIgnoresFormatting is what makes this worth having over a text comparison.
//
// Whitespace, attribute order and self-closing form all change when a document is re-serialised. A textual comparison would
// report a candidate that changed nothing as having rewritten every line.
func TestDiffXMLIgnoresFormatting(t *testing.T) {
	reformatted := strings.ReplaceAll(diffLive, "\n  ", "\n\t\t")
	reformatted = strings.Replace(reformatted, `<statusCode code="active"/>`,
		`<statusCode code="active"></statusCode>`, 1)
	reformatted = strings.Replace(reformatted, `<id root="1.2.3" extension="MRN12345"/>`,
		`<id extension="MRN12345" root="1.2.3"/>`, 1)

	if got := DiffXML([]byte(diffLive), []byte(reformatted), DiffOptions{}); len(got) != 0 {
		t.Errorf("reformatting produced %d differences, so a candidate that changed nothing would be "+
			"reported as rewriting the document: %+v", len(got), got)
	}
}

// TestDiffXMLHonoursIgnore covers the option that makes a shadow usable in practice.
func TestDiffXMLHonoursIgnore(t *testing.T) {
	candidate := strings.Replace(diffLive, `extension="MSG1"`, `extension="MSG2"`, 1)

	// The message identifier differs on every message and says nothing about whether a change is safe.
	got := DiffXML([]byte(diffLive), []byte(candidate), DiffOptions{
		Ignore: map[string]bool{strings.ToUpper("/PRPA_IN201306UV02/id@extension"): true},
	})

	if len(got) != 0 {
		t.Errorf("an ignored path was reported anyway: %+v", got)
	}
}

// TestDiffXMLOnUnparseableInputReportsNothingRatherThanEverything pins the failure direction.
//
// Reporting nothing is the safe answer: the caller already counts a message neither version could read, and inventing a full set
// of differences from a document that could not be parsed would look like a candidate that rewrites everything.
func TestDiffXMLOnUnparseableInputReportsNothingRatherThanEverything(t *testing.T) {
	if got := DiffXML([]byte(diffLive), []byte("not xml at all"), DiffOptions{}); len(got) != 0 {
		t.Errorf("an unparseable candidate produced %d differences: %+v", len(got), got)
	}
	if got := DiffXML([]byte("<<<"), []byte(diffLive), DiffOptions{}); len(got) != 0 {
		t.Errorf("an unparseable live document produced %d differences: %+v", len(got), got)
	}
}

// TestDiffXMLIsSorted covers reproducibility.
//
// Both sides are collected into maps, and a Go map ranges in an order nobody chose. A report that reorders itself between runs
// cannot be compared with the previous one, which is the main thing somebody does with it.
func TestDiffXMLIsSorted(t *testing.T) {
	candidate := strings.NewReplacer(
		`extension="MRN12345"`, `extension="MRN99999"`,
		`code="F"`, `code="M"`,
		`<family>Dubois</family>`, `<family>DUBOIS</family>`,
	).Replace(diffLive)

	first := DiffXML([]byte(diffLive), []byte(candidate), DiffOptions{})
	if len(first) < 3 {
		t.Fatalf("expected at least three differences, got %d", len(first))
	}

	for i := 0; i < 20; i++ {
		again := DiffXML([]byte(diffLive), []byte(candidate), DiffOptions{})
		for j := range first {
			if again[j].Path != first[j].Path {
				t.Fatalf("run %d ordered the differences differently: %q then %q",
					i, first[j].Path, again[j].Path)
			}
		}
	}
}
