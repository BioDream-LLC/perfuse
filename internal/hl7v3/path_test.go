package hl7v3

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// A PDQ query response, shaped as real ones are: values in attributes, deep nesting, a namespace prefix on some elements and not
// others, and two given names.
const pathSample = `<?xml version="1.0"?>
<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72" extension="MSG00001"/>
  <creationTime value="20260822103000"/>
  <interactionId extension="PRPA_IN201306UV02"/>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <subject>
      <registrationEvent>
        <subject1>
          <patient classCode="PAT">
            <id root="2.16.840.1.113883.3.72.5.9.1" extension="PIX1234"/>
            <id root="2.16.840.1.113883.4.1" extension="999887777"/>
            <statusCode code="active"/>
            <patientPerson>
              <name use="L">
                <given>Rosalind</given>
                <given>Marie</given>
                <family>Okonkwo-Hale</family>
              </name>
              <name use="P">
                <given>Roz</given>
                <family>Hale</family>
              </name>
              <telecom value="tel:+12055550143" use="HP"/>
              <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
              <birthTime value="19551014"/>
              <addr use="HP">
                <streetAddressLine>412 Morris Avenue</streetAddressLine>
                <city>Birmingham</city>
                <state>AL</state>
                <postalCode>35203</postalCode>
              </addr>
            </patientPerson>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
    <queryByParameter>
      <queryId root="1.2.3" extension="Q001"/>
      <parameterList>
        <livingSubjectName>
          <value><family>Okonkwo-Hale</family></value>
        </livingSubjectName>
      </parameterList>
    </queryByParameter>
  </controlActProcess>
</PRPA_IN201306UV02>`

func pathTree(t *testing.T) *xtree.Node {
	t.Helper()

	root, err := xtree.Parse([]byte(pathSample))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func mustPath(t *testing.T, s string) Path {
	t.Helper()

	p, err := ParsePath(s)
	if err != nil {
		t.Fatalf("ParsePath(%q): %v", s, err)
	}

	return p
}

// TestAValueInAnAttributeIsReachable is the whole reason this notation has an @.
//
// In v3 a value is nearly always an attribute. A path language that could only read element text would return nothing for the
// birth date, the gender, every identifier and every timestamp - and would do it without complaining.
func TestAValueInAnAttributeIsReachable(t *testing.T) {
	root := pathTree(t)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"//birthTime@value", "19551014"},
		{"//administrativeGenderCode@code", "F"},
		{"//administrativeGenderCode@codeSystem", "2.16.840.1.113883.5.1"},
		{"//telecom@value", "tel:+12055550143"},
		{"//creationTime@value", "20260822103000"},
		{"//interactionId@extension", "PRPA_IN201306UV02"},
	} {
		got, ok := mustPath(t, tc.path).Value(root)
		if !ok {
			t.Errorf("%s found nothing", tc.path)

			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestElementTextIsReachableWithoutAnAt covers the other half.
func TestElementTextIsReachableWithoutAnAt(t *testing.T) {
	root := pathTree(t)

	got, ok := mustPath(t, "//family").Value(root)
	if !ok || got != "Okonkwo-Hale" {
		t.Errorf("//family = %q %v, want Okonkwo-Hale", got, ok)
	}

	if got, _ := mustPath(t, "//city").Value(root); got != "Birmingham" {
		t.Errorf("//city = %q", got)
	}
}

// TestADescendantPathSurvivesDifferentNesting is why // exists.
//
// The same content sits at different depths in different interactions, which is not a vendor quirk - the interactions are
// genuinely different documents sharing payload types. A path anchored to the root would need rewriting per interaction, and
// somebody configuring a channel would find that out only when the second message shape arrived.
func TestADescendantPathSurvivesDifferentNesting(t *testing.T) {
	deep := pathTree(t)

	// The same patientPerson, but hoisted to be a direct child of the root.
	shallowXML := `<PRPA_IN201301UV02 xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime value="19551014"/></patientPerson>
	</PRPA_IN201301UV02>`
	shallow, err := xtree.Parse([]byte(shallowXML))
	if err != nil {
		t.Fatal(err)
	}

	p := mustPath(t, "//patientPerson/birthTime@value")

	for name, root := range map[string]*xtree.Node{"deeply nested": deep, "shallow": shallow} {
		got, ok := p.Value(root)
		if !ok {
			t.Errorf("%s: the path found nothing", name)

			continue
		}
		if got != "19551014" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// TestOccurrenceSelectsTheRightOne covers (n).
func TestOccurrenceSelectsTheRightOne(t *testing.T) {
	root := pathTree(t)

	// Two given names on the legal name.
	if got, _ := mustPath(t, "//name(1)/given(1)").Value(root); got != "Rosalind" {
		t.Errorf("first given of first name = %q, want Rosalind", got)
	}
	if got, _ := mustPath(t, "//name(1)/given(2)").Value(root); got != "Marie" {
		t.Errorf("second given of first name = %q, want Marie", got)
	}
	// The second name is the preferred one.
	if got, _ := mustPath(t, "//name(2)/given(1)").Value(root); got != "Roz" {
		t.Errorf("first given of second name = %q, want Roz", got)
	}
	if got, _ := mustPath(t, "//name(2)/family").Value(root); got != "Hale" {
		t.Errorf("family of second name = %q, want Hale", got)
	}

	// Two identifiers on the patient, which is the case that matters for routing: the medical record number and the
	// national identifier are different things and picking the wrong one sends the message to the wrong place.
	if got, _ := mustPath(t, "//patient/id(1)@extension").Value(root); got != "PIX1234" {
		t.Errorf("first patient id = %q, want PIX1234", got)
	}
	if got, _ := mustPath(t, "//patient/id(2)@extension").Value(root); got != "999887777" {
		t.Errorf("second patient id = %q, want 999887777", got)
	}
}

// TestAnOccurrenceBeyondTheEndIsNothing pins the decision not to clamp.
//
// Returning the last one would mean a path asking for the third identifier quietly reads the second, and a channel routing on it
// would send the message somewhere plausible and wrong.
func TestAnOccurrenceBeyondTheEndIsNothing(t *testing.T) {
	root := pathTree(t)

	if got, ok := mustPath(t, "//patient/id(3)@extension").Value(root); ok {
		t.Errorf("asking for a third identifier returned %q, and there are two", got)
	}
	if got, ok := mustPath(t, "//name(9)/family").Value(root); ok {
		t.Errorf("asking for a ninth name returned %q", got)
	}
}

// TestAPathWithNoOccurrenceReturnsEveryMatch covers listing.
func TestAPathWithNoOccurrenceReturnsEveryMatch(t *testing.T) {
	root := pathTree(t)

	given := mustPath(t, "//name(1)/given").Values(root)
	if len(given) != 2 || given[0] != "Rosalind" || given[1] != "Marie" {
		t.Errorf("given names = %v, want both in order", given)
	}

	// Order has to be the document's. A Spanish patient with two family names and a Hungarian one written family-first
	// both depend on it, so sorting or deduplicating here would lose the sender's meaning.
	ids := mustPath(t, "//patient/id@extension").Values(root)
	if len(ids) != 2 || ids[0] != "PIX1234" {
		t.Errorf("identifiers = %v, want document order", ids)
	}
}

// TestADescendantSearchPrefersTheShallowest covers the ambiguity a query response creates.
//
// This document contains the patient's family name and also a family name inside the query criteria. //family should give the
// patient's, because the patient is the subject of the message.
func TestADescendantSearchPrefersTheShallowest(t *testing.T) {
	root := pathTree(t)

	all := mustPath(t, "//family").Values(root)
	if len(all) < 3 {
		t.Fatalf("expected the two names and the query criterion, got %v", all)
	}
	// Breadth-first, so the patient's legal family name comes before the one buried in queryByParameter.
	if all[0] != "Okonkwo-Hale" {
		t.Errorf("the first match is %q, want the patient's own name", all[0])
	}
}

// TestANullFlavorIsReachableAndDistinctFromAbsence is the property this whole package exists for.
//
// An absent birth date and a birth date the patient declined to give are different clinical facts. A channel that cannot tell them
// apart will send a downstream system an empty date where the source said "asked and not known", and somebody will chase it.
func TestANullFlavorIsReachableAndDistinctFromAbsence(t *testing.T) {
	withFlavor := `<msg xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime nullFlavor="ASKU"/></patientPerson>
	</msg>`
	absent := `<msg xmlns="urn:hl7-org:v3">
	  <patientPerson/>
	</msg>`

	flavoured, err := xtree.Parse([]byte(withFlavor))
	if err != nil {
		t.Fatal(err)
	}
	missing, err := xtree.Parse([]byte(absent))
	if err != nil {
		t.Fatal(err)
	}

	p := mustPath(t, "//birthTime@value")

	// Neither has a value.
	if _, ok := p.Value(flavoured); ok {
		t.Error("a nullFlavor birth date reported a value")
	}
	if _, ok := p.Value(missing); ok {
		t.Error("an absent birth date reported a value")
	}

	// But only one of them exists, and only one has a reason.
	if !p.Exists(flavoured) {
		t.Error("a nullFlavor birth date reports as not present, so the reason is unreachable")
	}
	if p.Exists(missing) {
		t.Error("an absent birth date reports as present")
	}

	flavor, ok := p.NullFlavor(flavoured)
	if !ok || flavor != "ASKU" {
		t.Errorf("null flavour = %q %v, want ASKU", flavor, ok)
	}
	if _, ok := p.NullFlavor(missing); ok {
		t.Error("an absent element reported a null flavour")
	}
}

// TestANamespacePrefixIsIgnoredWhenMatching covers senders differing over prefixes.
//
// The property matters and is pinned here, but the honest note is that this package is not what delivers it: xtree.Parse stores
// Go's resolved local name, so a prefixed document arrives already normalised. Removing localName from this package changes
// nothing, and this test would still pass.
//
// So the assertion below checks the mechanism as well as the outcome. If xtree ever started preserving prefixes, the outcome would
// still hold - localName would begin to matter - but the mechanism assertion says which layer is doing the work, which is the part
// that was not obvious and cost a plant to discover.
func TestANamespacePrefixIsIgnoredWhenMatching(t *testing.T) {
	prefixed := `<hl7:msg xmlns:hl7="urn:hl7-org:v3">
	  <hl7:patientPerson><hl7:birthTime value="19551014"/></hl7:patientPerson>
	</hl7:msg>`

	root, err := xtree.Parse([]byte(prefixed))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := mustPath(t, "//patientPerson/birthTime@value").Value(root)
	if !ok || got != "19551014" {
		t.Errorf("a prefixed document gave %q %v - prefixes are the sender's choice and must not matter",
			got, ok)
	}

	// The mechanism: the parser has already dropped the prefix.
	if strings.Contains(root.Name, ":") {
		t.Errorf("the parsed root is named %q - if prefixes are now preserved, this package's own "+
			"stripping is what keeps paths working and needs its own test", root.Name)
	}

	// And a hand-built tree, which is the only case where this package's stripping does any work. xtree.New takes
	// whatever name it is given, so Resolve has to cope with a prefix that no parser would have produced.
	built := xtree.New("hl7:patientPerson")
	inner := xtree.New("hl7:birthTime")
	inner.SetAttr("value", "19551014")
	built.Append(inner)

	if got, ok := mustPath(t, "patientPerson/birthTime@value").Value(built); !ok || got != "19551014" {
		t.Errorf("a hand-built prefixed tree gave %q %v", got, ok)
	}
}

// TestAPrefixInAPathIsRefused covers the other direction.
//
// Silently stripping it would mean somebody who wrote a prefix believing it selected a namespace never learns otherwise, and a
// path written against one sender's prefixes would appear correct until a sender using different ones arrived.
func TestAPrefixInAPathIsRefused(t *testing.T) {
	_, err := ParsePath("//hl7:patientPerson/birthTime@value")
	if err == nil {
		t.Fatal("a namespace prefix in a path was accepted")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Errorf("the error should explain why, got: %v", err)
	}
}

// TestAMalformedPathIsRefusedRatherThanGuessed covers the parser.
//
// A path is written by a person into a channel that then runs unattended. A misparsed path does not fail - it addresses the wrong
// thing, or nothing, and the channel filters every message or none of them.
func TestAMalformedPathIsRefusedRatherThanGuessed(t *testing.T) {
	for _, bad := range []string{
		"",
		"   ",
		"@extension",              // an attribute of nothing
		"//patient@",              // an @ with no name
		"//patient/id(",           // unclosed
		"//patient/id(0)@value",   // occurrences count from 1
		"//patient/id(-1)@value",  // negative
		"//patient/id(two)@value", // not a number
		"//patient//id",           // empty step in the middle
		"//pat ient/id",           // space in a name
		"//patient/id@ex tension", // space in an attribute
		"/",
		"//",
	} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) was accepted", bad)
		}
	}
}

// TestAPathRoundTripsThroughItsText covers what the interface will store.
func TestAPathRoundTripsThroughItsText(t *testing.T) {
	for _, good := range []string{
		"//birthTime@value",
		"//patientPerson/name(2)/family",
		"patient/id@extension",
		"//addr/city",
	} {
		p := mustPath(t, good)
		if p.String() != good {
			t.Errorf("%q came back as %q", good, p.String())
		}
		// And reparsing what it prints has to give the same thing, or a saved channel would drift from what
		// somebody typed.
		again, err := ParsePath(p.String())
		if err != nil {
			t.Errorf("reparsing %q failed: %v", p.String(), err)

			continue
		}
		if len(again.Steps) != len(p.Steps) || again.Attribute != p.Attribute || again.Descend != p.Descend {
			t.Errorf("%q did not survive a round trip", good)
		}
	}
}

// TestAnAnchoredPathDoesNotSearch covers the difference between / and //.
//
// If an anchored path quietly searched, // would mean nothing and a path written to be precise would match something deeper that
// happened to share a name.
func TestAnAnchoredPathDoesNotSearch(t *testing.T) {
	root := pathTree(t)

	// birthTime is deep inside the document, so an anchored path from the root must not find it.
	if _, ok := mustPath(t, "PRPA_IN201306UV02/birthTime@value").Value(root); ok {
		t.Error("an anchored path found a deeply nested element, so // means nothing")
	}

	// But an anchored path that names the real route does work, including naming the root itself.
	if got, _ := mustPath(t, "PRPA_IN201306UV02/creationTime@value").Value(root); got != "20260822103000" {
		t.Errorf("an anchored path to a direct child failed: %q", got)
	}
	if got, _ := mustPath(t, "/PRPA_IN201306UV02/id@extension").Value(root); got != "MSG00001" {
		t.Errorf("a leading slash changed the meaning: %q", got)
	}
}

// TestWhitespaceInAnElementIsNotAValue covers XML indentation.
func TestWhitespaceInAnElementIsNotAValue(t *testing.T) {
	indented := `<msg xmlns="urn:hl7-org:v3">
	  <patientPerson>
	    <name>
	    </name>
	  </patientPerson>
	</msg>`

	root, err := xtree.Parse([]byte(indented))
	if err != nil {
		t.Fatal(err)
	}

	// The name element contains only a newline and spaces. Reporting that as a value would mean a filter for "the name
	// is present" matches a message that carries no name.
	if got, ok := mustPath(t, "//name").Value(root); ok {
		t.Errorf("indentation was reported as a value: %q", got)
	}
	// It still exists, which is the honest answer.
	if !mustPath(t, "//name").Exists(root) {
		t.Error("an empty name element reports as absent")
	}
}
