package hl7v3

import (
	"strings"
	"testing"
)

// recordAdded is PRPA_IN201301UV02, patient registry record added.
//
// Included specifically because it nests the patient differently from the query response, and because it exercises the null
// flavours. A parser written against only the PDQ response returns nothing useful from this, and the failure is silent.
const recordAdded = `<?xml version="1.0"?>
<PRPA_IN201301UV02 xmlns="urn:hl7-org:v3" ITSVersion="XML_1.0">
  <id root="1.2.840.114350.1.13.99998.8734" extension="MSG00042"/>
  <creationTime value="202608220830"/>
  <interactionId root="2.16.840.1.113883.1.6" extension="PRPA_IN201301UV02"/>
  <processingCode code="D"/>
  <processingModeCode code="T"/>
  <acceptAckCode code="AL"/>
  <receiver typeCode="RCV">
    <device classCode="DEV" determinerCode="INSTANCE"><id root="9.9.9.9"/></device>
  </receiver>
  <sender typeCode="SND">
    <device classCode="DEV" determinerCode="INSTANCE"><id root="8.8.8.8"/></device>
  </sender>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <code code="PRPA_TE201301UV02" codeSystem="2.16.840.1.113883.1.6"/>
    <effectiveTime value="20260822082959"/>
    <author typeCode="AUT">
      <assignedDevice classCode="ASSIGNED">
        <id root="1.1.1.1" extension="REG-CLERK-7"/>
      </assignedDevice>
    </author>
    <subject typeCode="SUBJ">
      <registrationEvent classCode="REG" moodCode="EVN">
        <statusCode code="active"/>
        <subject1 typeCode="SBJ">
          <patient classCode="PAT">
            <id root="1.3.6.1.4.1.21367.2005.3.7" extension="NEW0001"/>
            <statusCode code="active"/>
            <patientPerson>
              <name>Robert Turner</name>
              <administrativeGenderCode nullFlavor="UNK"/>
              <birthTime nullFlavor="ASKU"/>
              <addr nullFlavor="MSK"/>
              <telecom nullFlavor="NASK"/>
              <maritalStatusCode nullFlavor="NAV"/>
              <deceasedTime value="20260801"/>
            </patientPerson>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
  </controlActProcess>
</PRPA_IN201301UV02>`

// TestAWithheldValueIsNotTheSameAsAMissingOne is what this package exists for.
//
// Collapsing a null flavour into an empty string loses a clinical fact. A downstream system given an empty birth date where
// the source said "asked, and the patient did not know" will treat it as missing data to be chased, and a demographic match
// that should have been left unresolved gets resolved by somebody guessing.
func TestAWithheldValueIsNotTheSameAsAMissingOne(t *testing.T) {
	m, err := Parse([]byte(recordAdded))
	if err != nil {
		t.Fatal(err)
	}

	p := m.ExtractPatient()
	if p == nil {
		t.Fatal("no patient found")
	}

	// Asked, and the answer was not known. Distinct from unknown, because it says the gap has been chased once already.
	if p.BirthTime.Presence != Null {
		t.Errorf("birth time presence is %s, so the reason it is empty was lost", p.BirthTime.Presence)
	}
	if p.BirthTime.NullFlavor != AskedButUnknown {
		t.Errorf("birth time null flavour is %q", p.BirthTime.NullFlavor)
	}
	if got := p.BirthTime.NullFlavor.Explain(); !strings.Contains(got, "asked") {
		t.Errorf("the explanation does not mention asking: %q", got)
	}

	// Withheld deliberately. Never to be treated as missing: a masked address on a patient under a protection order is a
	// decision somebody made, and filling it in from another source undoes it.
	if len(p.Addresses) != 1 || p.Addresses[0].Presence != Null {
		t.Fatalf("the masked address was not kept as null: %+v", p.Addresses)
	}
	if p.Addresses[0].NullFlavor != Masked {
		t.Errorf("address null flavour is %q", p.Addresses[0].NullFlavor)
	}

	// Nobody asked, which is the one actually worth chasing.
	if len(p.Telecoms) != 1 || p.Telecoms[0].NullFlavor != NotAsked {
		t.Errorf("telecom null flavour is %+v", p.Telecoms)
	}

	// Temporarily unavailable: asking again later may work.
	if p.MaritalStatus.NullFlavor != TemporarilyUnavailable {
		t.Errorf("marital status null flavour is %q", p.MaritalStatus.NullFlavor)
	}

	// And an ordinary unknown, which is different from all of the above.
	if p.Gender.NullFlavor != Unknown {
		t.Errorf("gender null flavour is %q", p.Gender.NullFlavor)
	}

	// All five are Null rather than Absent. Absent means nobody mentioned the field at all, and this message mentions
	// every one of them.
	for name, presence := range map[string]Presence{
		"birthTime": p.BirthTime.Presence,
		"gender":    p.Gender.Presence,
		"marital":   p.MaritalStatus.Presence,
	} {
		if presence == Absent {
			t.Errorf("%s reads as absent, but the message states a reason for it being empty", name)
		}
	}
}

// TestADeathDateWithoutTheIndicatorStillMeansDead covers a real asymmetry.
//
// The message sets deceasedTime and never sets deceasedInd. Reading only the boolean would treat this patient as living, and
// the consequence is an appointment letter to a bereaved family.
func TestADeathDateWithoutTheIndicatorStillMeansDead(t *testing.T) {
	m, err := Parse([]byte(recordAdded))
	if err != nil {
		t.Fatal(err)
	}

	p := m.ExtractPatient()
	if !p.Deceased {
		t.Error("a patient with a death date but no deceasedInd reads as living")
	}
	if p.DeceasedTime.Presence != Present {
		t.Errorf("the death date was not read: %+v", p.DeceasedTime)
	}
}

// TestANameWithNoPartsIsStillAName covers senders that hold a name as one string.
func TestANameWithNoPartsIsStillAName(t *testing.T) {
	m, err := Parse([]byte(recordAdded))
	if err != nil {
		t.Fatal(err)
	}

	p := m.ExtractPatient()
	name := p.PrimaryName()
	if name.Presence != Present {
		t.Fatal("an unstructured name was discarded")
	}
	if name.Formatted() != "Robert Turner" {
		t.Errorf("formatted as %q", name.Formatted())
	}
}

// TestANonProductionMessageSaysSoLoudly covers the field that decides whether fictional patients reach a live record.
func TestANonProductionMessageSaysSoLoudly(t *testing.T) {
	m, err := Parse([]byte(recordAdded))
	if err != nil {
		t.Fatal(err)
	}

	if m.IsProduction() {
		t.Error("a message marked D should not read as production")
	}
	if !strings.Contains(m.Describe(), "PROCESSING CODE") {
		t.Errorf("the description does not flag the processing code: %q", m.Describe())
	}

	// And the description must carry no patient content, because it goes in a log.
	for _, leak := range []string{"Robert", "Turner", "NEW0001"} {
		if strings.Contains(m.Describe(), leak) {
			t.Errorf("the description leaks patient data (%q): %q", leak, m.Describe())
		}
	}
}

// TestTheAuthorIsFoundWhereverItIsNested covers the control act author.
func TestTheAuthorIsFoundWhereverItIsNested(t *testing.T) {
	m, err := Parse([]byte(recordAdded))
	if err != nil {
		t.Fatal(err)
	}

	if m.ControlAct == nil {
		t.Fatal("no control act")
	}
	if m.ControlAct.AuthorID.Extension != "REG-CLERK-7" {
		t.Errorf("author is %s", m.ControlAct.AuthorID)
	}
	// The effective time is when the act happened, as distinct from when the message was built. They differ whenever a
	// system batches or retries, and it is the effective time that belongs in a clinical record.
	if m.ControlAct.EffectiveTime.Presence != Present {
		t.Error("the effective time was not read")
	}
	if m.CreationTime.Precision != PrecisionMinute {
		t.Errorf("creationTime YYYYMMDDHHMM should be minute precision, got %s", m.CreationTime.Precision)
	}
}

// TestAnIdentifierNeedsBothParts is the comparison that merges patient records when it is wrong.
//
// Two hospitals both number a patient 12345. An extension without its root is a number, not an identifier, and code that
// compares extensions alone will merge records across organisations - the worst failure mode in patient identity work.
func TestAnIdentifierNeedsBothParts(t *testing.T) {
	a := II{Root: "1.1.1", Extension: "12345", Presence: Present}
	b := II{Root: "2.2.2", Extension: "12345", Presence: Present}
	same := II{Root: "1.1.1", Extension: "12345", Presence: Present}

	if a.Equal(b) {
		t.Error("identifiers from different authorities compared equal; this merges patients across hospitals")
	}
	if !a.Equal(same) {
		t.Error("identical identifiers did not compare equal")
	}

	// Two absent identifiers are not the same identifier. Treating them as equal would make every patient with no
	// national number match every other one.
	absent := II{Presence: Absent}
	if absent.Equal(II{Presence: Absent}) {
		t.Error("two absent identifiers compared equal; every patient without one would match every other")
	}

	// Nor does a null one match.
	null := II{Presence: Null, NullFlavor: Unknown}
	if null.Equal(null) {
		t.Error("two unknown identifiers compared equal")
	}
}

// TestACodeWithoutItsSystemIsNotAComparison covers the same hazard for coded values.
func TestACodeWithoutItsSystemIsNotAComparison(t *testing.T) {
	icd9 := Coded{Code: "250", System: "2.16.840.1.113883.6.42", Presence: Present}
	other := Coded{Code: "250", System: "2.16.840.1.113883.6.96", Presence: Present}

	if icd9.Equal(other) {
		t.Error("the same code in different systems compared equal")
	}

	// A display name must never affect a comparison: display names drift between releases while codes do not.
	relabelled := Coded{Code: "250", System: "2.16.840.1.113883.6.42",
		DisplayName: "Diabetes mellitus", Presence: Present}
	if !icd9.Equal(relabelled) {
		t.Error("a display name changed the outcome of a code comparison")
	}
}

func TestTimestampPrecisionIsKept(t *testing.T) {
	cases := []struct {
		raw       string
		precision Precision
		offset    bool
	}{
		{"1971", PrecisionYear, false},
		{"197103", PrecisionMonth, false},
		{"19710304", PrecisionDay, false},
		{"1971030412", PrecisionHour, false},
		{"197103041215", PrecisionMinute, false},
		{"19710304121530", PrecisionSecond, false},
		{"19710304121530.500", PrecisionFraction, false},
		{"19710304121530-0500", PrecisionSecond, true},
		{"19710304121530+05:30", PrecisionSecond, true},
		{"19710304121530Z", PrecisionSecond, true},
	}

	for _, c := range cases {
		ts := ParseTimestamp(c.raw)
		if ts.Presence != Present {
			t.Errorf("%s did not parse: %+v", c.raw, ts)
			continue
		}
		if ts.Precision != c.precision {
			t.Errorf("%s parsed as %s precision, expected %s", c.raw, ts.Precision, c.precision)
		}
		// Whether the sender stated an offset is worth knowing rather than assuming. This is the most common cause of
		// an appointment landing an hour out, and the message either said or it did not.
		if ts.HasOffset != c.offset {
			t.Errorf("%s offset flag is %v, expected %v", c.raw, ts.HasOffset, c.offset)
		}
	}

	// A year given to the day is not the same claim as one given to the year. A demographic match that treats "1971" as
	// the first of January will match the wrong patient with a January birthday.
	if ParseTimestamp("1971").Precision == ParseTimestamp("19710101").Precision {
		t.Error("a year and a full date read as the same precision")
	}
}

// TestABadTimestampDoesNotStopAMessage covers a deliberate leniency.
//
// A message whose only fault is one malformed date is usually still worth delivering, so an unparseable value becomes
// null-with-reason OTH - which is v3's own way of saying "there is a value here and it is not a permitted one".
func TestABadTimestampDoesNotStopAMessage(t *testing.T) {
	for _, raw := range []string{"not-a-date", "19x10304", "12", ""} {
		ts := ParseTimestamp(raw)
		if ts.Presence == Present {
			t.Errorf("%q parsed as a real timestamp: %+v", raw, ts)
		}
		if raw != "" && ts.NullFlavor != Other {
			t.Errorf("%q became %q rather than OTH", raw, ts.NullFlavor)
		}
	}
}

// TestSomethingThatIsNotAnInteractionIsRefused covers the front door.
func TestSomethingThatIsNotAnInteractionIsRefused(t *testing.T) {
	cases := map[string]string{
		"not XML at all":         "MSH|^~\\&|A|B\r",
		"XML but not v3":         `<?xml version="1.0"?><hello><world/></hello>`,
		"a CDA document":         `<?xml version="1.0"?><ClinicalDocument xmlns="urn:hl7-org:v3"><id root="1"/></ClinicalDocument>`,
		"interaction-ish name":   `<?xml version="1.0"?><PRPA_INSOMETHING xmlns="urn:hl7-org:v3"/>`,
		"too few domain letters": `<?xml version="1.0"?><PRP_IN201301UV02 xmlns="urn:hl7-org:v3"/>`,
	}

	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s was accepted as a v3 interaction", name)
		}
	}
}

// TestAnInteractionInsideASOAPEnvelopeIsFound covers how these really arrive.
//
// IHE carries v3 over SOAP, so a channel receiving ITI-47 over HTTP gets the envelope rather than the interaction. Requiring
// the author to strip it by hand would mean a script in every such channel.
func TestAnInteractionInsideASOAPEnvelopeIsFound(t *testing.T) {
	wrapped := `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
  <soap:Header>
    <wsa:Action xmlns:wsa="http://www.w3.org/2005/08/addressing">urn:hl7-org:v3:PRPA_IN201305UV02</wsa:Action>
  </soap:Header>
  <soap:Body>` + strings.TrimPrefix(pdqQuery, `<?xml version="1.0"?>`) + `</soap:Body>
</soap:Envelope>`

	m, err := Parse([]byte(wrapped))
	if err != nil {
		t.Fatalf("a SOAP-wrapped interaction was refused: %v", err)
	}
	if m.InteractionID != "PRPA_IN201305UV02" {
		t.Errorf("interaction is %q", m.InteractionID)
	}
	if m.ExtractQuery() == nil {
		t.Error("the query inside the envelope was not reachable")
	}
}

// TestExternalEntitiesAreNotResolved confirms this path uses the hardened reader.
//
// The XXE work was done for CDA and verified by sending payloads. What matters here is that the v3 path goes through the same
// reader rather than a fresh encoding/xml decoder, because that is the mistake that reopens it - and nothing about the
// package structure forces it.
func TestExternalEntitiesAreNotResolved(t *testing.T) {
	attack := `<?xml version="1.0"?>
<!DOCTYPE PRPA_IN201301UV02 [
  <!ENTITY secret SYSTEM "file:///etc/passwd">
]>
<PRPA_IN201301UV02 xmlns="urn:hl7-org:v3">
  <id root="1.1.1" extension="&secret;"/>
  <creationTime value="20260822"/>
</PRPA_IN201301UV02>`

	m, err := Parse([]byte(attack))
	if err != nil {
		// Refusing the document is a perfectly good answer.
		return
	}

	// If it parsed, the entity must not have been resolved to file contents.
	if strings.Contains(m.ID.Extension, "root:") || strings.Contains(string(m.Raw), "/bin/") {
		t.Errorf("an external entity was resolved: %q", m.ID.Extension)
	}
	if strings.Contains(m.ID.Extension, "passwd") {
		t.Errorf("the entity reached the parsed value: %q", m.ID.Extension)
	}
}

// TestTheOriginalBytesAreKept matters for forwarding.
//
// A v3 message is frequently passed onward unchanged, and re-serialising XML changes bytes without changing meaning - which
// breaks any signature over the document.
func TestTheOriginalBytesAreKept(t *testing.T) {
	m, err := Parse([]byte(pdqResponse))
	if err != nil {
		t.Fatal(err)
	}

	if string(m.Raw) != pdqResponse {
		t.Error("the original bytes were not preserved, so forwarding this message would alter it")
	}
}
