package hl7v3

import (
	"strings"
	"testing"
)

// The messages here are the real shapes from IHE ITI transactions rather than invented XML, because the whole difficulty of
// v3 is that the nesting differs between interactions and an invented example would agree with whatever the parser happened
// to do.
//
// PRPA_IN201306UV02 is the PDQ query response (ITI-47) and PRPA_IN201301UV02 is patient registry record added. They nest the
// patient differently, which is the specific thing that breaks a parser written against one of them.

// pdqResponse is a PDQ query response: patient under registrationEvent/subject1/patient/patientPerson.
const pdqResponse = `<?xml version="1.0" encoding="UTF-8"?>
<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3" ITSVersion="XML_1.0">
  <id root="1.2.840.114350.1.13.99998.8734" extension="MSG00001"/>
  <creationTime value="20260822081500-0500"/>
  <versionCode code="V3-2008N"/>
  <interactionId root="2.16.840.1.113883.1.6" extension="PRPA_IN201306UV02"/>
  <processingCode code="P"/>
  <processingModeCode code="T"/>
  <acceptAckCode code="NE"/>
  <receiver typeCode="RCV">
    <device classCode="DEV" determinerCode="INSTANCE">
      <id root="1.2.3.4.5.6.7"/>
      <name>Requesting System</name>
    </device>
  </receiver>
  <sender typeCode="SND">
    <device classCode="DEV" determinerCode="INSTANCE">
      <id root="2.16.840.1.113883.3.72.6.2"/>
      <name>Registry</name>
      <softwareName>OpenPIXPDQ</softwareName>
      <asAgent classCode="AGNT">
        <representedOrganization classCode="ORG" determinerCode="INSTANCE">
          <id root="1.3.6.1.4.1.21367.2010.1.2.300"/>
        </representedOrganization>
      </asAgent>
    </device>
  </sender>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <code code="PRPA_TE201306UV02" codeSystem="2.16.840.1.113883.1.6"/>
    <effectiveTime value="20260822081500"/>
    <subject typeCode="SUBJ" contextConductionInd="false">
      <registrationEvent classCode="REG" moodCode="EVN">
        <id nullFlavor="NA"/>
        <statusCode code="active"/>
        <subject1 typeCode="SBJ">
          <patient classCode="PAT">
            <id root="1.3.6.1.4.1.21367.2005.3.7" extension="PIX1234"/>
            <id root="2.16.840.1.113883.4.1" extension="123-45-6789"
                assigningAuthorityName="SSA"/>
            <statusCode code="active"/>
            <patientPerson>
              <name use="L">
                <given>Maria</given>
                <family qualifier="BR">Garcia</family>
                <family qualifier="SP">Lopez</family>
              </name>
              <name use="P">
                <given>Mari</given>
                <family>Garcia</family>
              </name>
              <telecom value="tel:+1-205-555-0143" use="MC"/>
              <telecom value="mailto:mgarcia@example.org" use="HP"/>
              <administrativeGenderCode code="F"
                  codeSystem="2.16.840.1.113883.5.1" displayName="Female"/>
              <birthTime value="19710304"/>
              <deceasedInd value="false"/>
              <multipleBirthInd value="true"/>
              <multipleBirthOrderNumber value="2"/>
              <addr use="HP">
                <streetAddressLine>1200 Highland Avenue</streetAddressLine>
                <city>Birmingham</city>
                <state>AL</state>
                <postalCode>35205</postalCode>
                <country>USA</country>
              </addr>
              <addr use="BAD">
                <streetAddressLine>44 Old Mill Road</streetAddressLine>
                <city>Bessemer</city>
                <state>AL</state>
                <postalCode>35020</postalCode>
              </addr>
              <maritalStatusCode code="M" codeSystem="2.16.840.1.113883.5.2"/>
              <languageCommunication>
                <languageCode code="es"/>
                <preferenceInd value="true"/>
              </languageCommunication>
              <languageCommunication>
                <languageCode code="en"/>
              </languageCommunication>
            </patientPerson>
            <providerOrganization classCode="ORG" determinerCode="INSTANCE">
              <id root="1.3.6.1.4.1.21367.2010.1.2.300"/>
              <name>St Josephs</name>
            </providerOrganization>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
    <queryAck>
      <queryId root="1.2.840.114350.1.13.28.1.18.5.999" extension="Q001"/>
      <queryResponseCode code="OK"/>
    </queryAck>
  </controlActProcess>
</PRPA_IN201306UV02>`

// pdqQuery is a PDQ query by demographics (ITI-47).
const pdqQuery = `<?xml version="1.0"?>
<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3" ITSVersion="XML_1.0">
  <id root="1.2.840.114350.1.13.28.1.18.5.999" extension="Q001"/>
  <creationTime value="20260822081459-0500"/>
  <interactionId root="2.16.840.1.113883.1.6" extension="PRPA_IN201305UV02"/>
  <processingCode code="P"/>
  <processingModeCode code="T"/>
  <acceptAckCode code="AL"/>
  <receiver typeCode="RCV">
    <device classCode="DEV" determinerCode="INSTANCE">
      <id root="2.16.840.1.113883.3.72.6.2"/>
    </device>
  </receiver>
  <sender typeCode="SND">
    <device classCode="DEV" determinerCode="INSTANCE">
      <id root="1.2.3.4.5.6.7"/>
    </device>
  </sender>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <code code="PRPA_TE201305UV02" codeSystem="2.16.840.1.113883.1.6"/>
    <queryByParameter>
      <queryId root="1.2.840.114350.1.13.28.1.18.5.999" extension="Q001"/>
      <statusCode code="new"/>
      <responseModalityCode code="R"/>
      <responsePriorityCode code="I"/>
      <initialQuantity value="10"/>
      <parameterList>
        <livingSubjectAdministrativeGender>
          <value code="F" codeSystem="2.16.840.1.113883.5.1"/>
          <semanticsText>LivingSubject.administrativeGender</semanticsText>
        </livingSubjectAdministrativeGender>
        <livingSubjectBirthTime>
          <value value="19710304"/>
          <semanticsText>LivingSubject.birthTime</semanticsText>
        </livingSubjectBirthTime>
        <livingSubjectName>
          <value>
            <given>Maria</given>
            <family>Garcia</family>
          </value>
          <semanticsText>LivingSubject.name</semanticsText>
        </livingSubjectName>
      </parameterList>
    </queryByParameter>
  </controlActProcess>
</PRPA_IN201305UV02>`

func TestTheEnvelopeIsReadFromARealPDQResponse(t *testing.T) {
	m, err := Parse([]byte(pdqResponse))
	if err != nil {
		t.Fatal(err)
	}

	if m.InteractionID != "PRPA_IN201306UV02" {
		t.Errorf("interaction is %q", m.InteractionID)
	}
	// The interaction identifier comes from the root element name, not from the interactionId element - which is the
	// thing about v3 that surprises everybody arriving from v2, where the message type is a field.
	if m.ID.Extension != "MSG00001" || m.ID.Root != "1.2.840.114350.1.13.99998.8734" {
		t.Errorf("message id is %s", m.ID)
	}
	if m.Sender.SoftwareName != "OpenPIXPDQ" {
		t.Errorf("sender software is %q", m.Sender.SoftwareName)
	}
	// The organisation is what is worth authorising on, because devices get replaced and organisations do not.
	if m.Sender.OrganizationID.Root != "1.3.6.1.4.1.21367.2010.1.2.300" {
		t.Errorf("sender organisation is %s", m.Sender.OrganizationID)
	}
	if !m.IsProduction() {
		t.Error("a message marked P should read as production")
	}
	// acceptAckCode NE means the sender does not want an acknowledgement.
	if m.WantsAcknowledgement() {
		t.Error("acceptAckCode NE should mean no acknowledgement is wanted")
	}
	if m.ControlAct == nil || m.ControlAct.Code.Code != "PRPA_TE201306UV02" {
		t.Errorf("control act code was not read: %+v", m.ControlAct)
	}
}

func TestPatientDemographicsComeOutOfAPDQResponse(t *testing.T) {
	m, err := Parse([]byte(pdqResponse))
	if err != nil {
		t.Fatal(err)
	}

	p := m.ExtractPatient()
	if p == nil {
		t.Fatal("no patient was found in a message that plainly contains one")
	}

	// Both identifiers, because that is the entire point of PIX: the same person has a number in each system.
	if len(p.IDs) != 2 {
		t.Fatalf("expected 2 identifiers, got %d: %v", len(p.IDs), p.IDs)
	}
	if id, ok := p.IDIn("2.16.840.1.113883.4.1"); !ok || id.Extension != "123-45-6789" {
		t.Errorf("the social security identifier was not found by its authority: %v %v", id, ok)
	}
	if _, ok := p.IDIn("9.9.9.9"); ok {
		t.Error("an identifier was returned for an authority that assigned none")
	}

	if len(p.Names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(p.Names))
	}

	// Two family names in the order the sender gave them. This is the case that flattening to Family/Given loses, and
	// getting it wrong prints a patient's name backwards on a wristband.
	legal := p.PrimaryName()
	if got := legal.Formatted(); got != "Maria Garcia Lopez" {
		t.Errorf("the legal name formatted as %q, losing the sender's part order", got)
	}
	if len(legal.Family) != 2 || legal.Family[0] != "Garcia" || legal.Family[1] != "Lopez" {
		t.Errorf("both family names should be kept in order: %v", legal.Family)
	}
	if legal.Parts[1].Qualifier != "BR" {
		t.Errorf("the birth-name qualifier was lost: %+v", legal.Parts[1])
	}

	if p.Gender.Code != "F" || p.Gender.System != "2.16.840.1.113883.5.1" {
		t.Errorf("gender is %s", p.Gender)
	}
	if p.BirthTime.Precision != PrecisionDay {
		t.Errorf("a birth date given as YYYYMMDD should be day precision, got %s", p.BirthTime.Precision)
	}
	if p.Deceased {
		t.Error("deceasedInd false should not read as deceased")
	}

	// Twins with the same surname and birth date are the classic false-positive in demographic matching, and the birth
	// order is often the only thing that separates them.
	if !p.MultipleBirth || p.MultipleBirthOrder != "2" {
		t.Errorf("multiple birth was not read: %v %q", p.MultipleBirth, p.MultipleBirthOrder)
	}

	if len(p.LanguageCodes) != 2 || p.LanguageCodes[0] != "es" {
		t.Errorf("languages are %v; this decides whether an interpreter is booked", p.LanguageCodes)
	}
	if p.StatusCode != "active" {
		t.Errorf("status is %q", p.StatusCode)
	}
	if p.ProviderOrganizationID.Root != "1.3.6.1.4.1.21367.2010.1.2.300" {
		t.Errorf("provider organisation is %s", p.ProviderOrganizationID)
	}
}

// TestAKnownBadAddressIsNotOfferedAsSomewhereToWrite covers use="BAD".
//
// The sender has already said post to that address comes back. A caller asking for the home address wants somewhere to send
// something, so offering the known-bad one would send an appointment letter into the void.
func TestAKnownBadAddressIsNotOfferedAsSomewhereToWrite(t *testing.T) {
	m, err := Parse([]byte(pdqResponse))
	if err != nil {
		t.Fatal(err)
	}

	p := m.ExtractPatient()
	if len(p.Addresses) != 2 {
		t.Fatalf("both addresses should be kept: %d", len(p.Addresses))
	}

	home := p.HomeAddress()
	if !strings.Contains(home.Formatted(), "Highland") {
		t.Errorf("home address is %q, which should be the deliverable one", home.Formatted())
	}
	if strings.Contains(home.Formatted(), "Old Mill") {
		t.Error("the address the sender flagged as undeliverable was offered as somewhere to write")
	}

	// But it is still in the record, because the fact that it is bad is itself information.
	if !p.Addresses[1].Undeliverable() {
		t.Error("the BAD use was lost, so nothing downstream can tell that address was tried")
	}

	// The check above passes whether or not the undeliverable filter works, because the good address is marked HP and is
	// returned before the filter is ever reached. Removing the filter left it green, which is the third time tonight a
	// test asserted the arrangement of an example rather than the property.
	//
	// So: the same patient with only a bad address, where the filter is the only thing that can produce the right answer.
	onlyBad := Patient{Addresses: []Address{
		{Use: "BAD", Presence: Present, Parts: []AddressPart{{Type: "city", Value: "Bessemer"}}},
	}}
	if got := onlyBad.HomeAddress(); got.Presence == Present {
		t.Errorf("a patient whose only address is known-bad was given somewhere to write: %q", got.Formatted())
	}

	// And with the bad one listed first, so position cannot be what produces the right answer either.
	badFirst := Patient{Addresses: []Address{
		{Use: "BAD", Presence: Present, Parts: []AddressPart{{Type: "city", Value: "Bessemer"}}},
		{Use: "H", Presence: Present, Parts: []AddressPart{{Type: "city", Value: "Birmingham"}}},
	}}
	if got := badFirst.HomeAddress().Formatted(); got != "Birmingham" {
		t.Errorf("with the bad address listed first, HomeAddress returned %q", got)
	}
}

func TestAQueryComesOutOfAPDQRequest(t *testing.T) {
	m, err := Parse([]byte(pdqQuery))
	if err != nil {
		t.Fatal(err)
	}

	q := m.ExtractQuery()
	if q == nil {
		t.Fatal("no query was found")
	}
	if q.ID.Extension != "Q001" {
		t.Errorf("query id is %s", q.ID)
	}

	// A limit the asker stated. Worth honouring: an unlimited query against a national registry both fails to help the
	// asker and discloses everybody who shares a surname.
	if q.InitialQuantity != "10" {
		t.Errorf("initial quantity is %q", q.InitialQuantity)
	}

	gender, ok := q.Parameter("livingSubjectAdministrativeGender")
	if !ok || len(gender.Values) != 1 || gender.Values[0] != "F" {
		t.Errorf("gender parameter is %+v", gender)
	}

	birth, ok := q.Parameter("livingSubjectBirthTime")
	if !ok || len(birth.Values) != 1 || birth.Values[0] != "19710304" {
		t.Errorf("birth time parameter is %+v", birth)
	}

	// A name parameter is made of parts rather than an attribute, so its text has to be assembled.
	name, ok := q.Parameter("livingSubjectName")
	if !ok || len(name.Values) != 1 || !strings.Contains(name.Values[0], "Garcia") {
		t.Errorf("name parameter is %+v", name)
	}

	// The housekeeping elements are not criteria and must not appear as parameters, or a channel filtering on the
	// parameter list would see "new" as something somebody searched for.
	for _, unwanted := range []string{"queryId", "statusCode", "initialQuantity", "responseModalityCode"} {
		if _, present := q.Parameter(unwanted); present {
			t.Errorf("%s was reported as a search criterion", unwanted)
		}
	}
}
