package eprescribe

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/drug"
)

// The field this package exists to protect.
//
// Zero means substitution is allowed and one means dispense as written, which reads backwards to anybody
// expecting a flag where one means yes. Getting it wrong dispenses a generic where the prescriber required
// the brand, or refuses a substitution that was permitted and charges the patient for the difference.
func TestSubstitutionZeroMeansAllowedAndOneMeansDispenseAsWritten(t *testing.T) {
	allowed, err := ParseSubstitution("0")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allowed() {
		t.Error("0 was read as forbidding substitution, which refuses a substitution the prescriber permitted")
	}

	dispenseAsWritten, err := ParseSubstitution("1")
	if err != nil {
		t.Fatal(err)
	}
	if dispenseAsWritten.Allowed() {
		t.Error("1 was read as permitting substitution, which dispenses a generic where the brand was required")
	}

	// And the words must not be ambiguous either, since this is what a log line shows.
	if !strings.Contains(dispenseAsWritten.String(), "as written") {
		t.Errorf("1 described as %q", dispenseAsWritten.String())
	}
}

// There is no safe default, so there is no default.
func TestAnAbsentSubstitutionFlagIsRefusedRatherThanDefaulted(t *testing.T) {
	got, err := ParseSubstitution("")
	if err == nil {
		t.Fatal("a missing substitution flag was given a value")
	}
	if got != SubstitutionUnset {
		t.Errorf("got %v, want unset", got)
	}
	// The refusal must say why both defaults are wrong, or somebody will add one.
	for _, want := range []string{"generic", "required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not explain the risk (%q missing): %v", want, err)
		}
	}
}

func TestAnUnknownSubstitutionValueIsRefused(t *testing.T) {
	for _, in := range []string{"2", "Y", "N", "true"} {
		if _, err := ParseSubstitution(in); err == nil {
			t.Errorf("substitution %q was accepted", in)
		}
	}
}

// A schedule II prescription with refills is well formed and must not be forwarded. A pharmacy has to
// refuse it, and the prescriber finds out from the patient.
func TestAScheduleTwoPrescriptionWithRefillsIsRefused(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "CII"
	m.Body.NewRx.Medication.NumberOfRefills = "5"
	m.Body.NewRx.Prescriber.DEANumber = "AB1234563"

	refusals, _ := Check(m)
	if !hasField(refusals, "NumberOfRefills") {
		t.Fatalf("a schedule II prescription with five refills was accepted; refusals were %v", refusals)
	}
	// And Build must not produce it, or a channel authoring one would send it.
	if _, err := Build(m); err == nil {
		t.Error("Build wrote a schedule II prescription with refills")
	}
}

func TestAScheduleTwoPrescriptionWithNoRefillsIsFine(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "CII"
	m.Body.NewRx.Medication.NumberOfRefills = "0"
	m.Body.NewRx.Prescriber.DEANumber = "AB1234563"

	refusals, _ := Check(m)
	if len(refusals) != 0 {
		t.Errorf("a valid schedule II prescription was refused: %v", refusals)
	}
}

// A controlled substance with no DEA number cannot be filled, so forwarding it sends the patient to a
// counter to be turned away.
func TestAControlledSubstanceWithoutADEANumberIsRefused(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "CIV"
	m.Body.NewRx.Prescriber.DEANumber = ""

	refusals, _ := Check(m)
	if !hasField(refusals, "Prescriber/DEANumber") {
		t.Fatalf("a controlled prescription with no DEA number was accepted; refusals were %v", refusals)
	}

	// The same prescription uncontrolled needs no DEA number.
	m.Body.NewRx.Medication.Coded.DEASchedule = ""
	refusals, _ = Check(m)
	if hasField(refusals, "Prescriber/DEANumber") {
		t.Error("an uncontrolled prescription was required to carry a DEA number")
	}
}

// A DEA number in the identification block counts, since senders put it in either place.
func TestADEANumberInTheIdentificationBlockIsAccepted(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "CIII"
	m.Body.NewRx.Prescriber.DEANumber = ""
	m.Body.NewRx.Prescriber.Identification = &Identification{DEANumber: "AB1234563"}

	refusals, _ := Check(m)
	if hasField(refusals, "Prescriber/DEANumber") {
		t.Errorf("a DEA number in the identification block was not seen: %v", refusals)
	}
}

// Refusals and warnings are separate, because a missing telephone number is not a controlled substance
// with refills and a caller given one list will treat them alike.
func TestWarningsDoNotBlockAPrescription(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.DaysSupply = ""
	m.Body.NewRx.Medication.WrittenDate = nil

	refusals, warnings := Check(m)
	if len(refusals) != 0 {
		t.Errorf("a prescription missing only optional fields was refused: %v", refusals)
	}
	if len(warnings) < 2 {
		t.Errorf("got %d warnings, want at least 2 (days supply and written date): %v", len(warnings), warnings)
	}
	// Days supply matters because the plan uses it to judge whether a refill is too soon.
	if !hasField(warnings, "DaysSupply") {
		t.Error("a missing days supply was not mentioned, and it is what a refill-too-soon rejection turns on")
	}
	// And it must still be sendable.
	if _, err := Build(m); err != nil {
		t.Errorf("Build refused a prescription that only had warnings: %v", err)
	}
}

// A quantity with no unit is not a quantity. Thirty of an inhaler is not thirty doses.
func TestAQuantityWithNoUnitIsRefused(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Quantity.UnitOfMeasure = nil

	refusals, _ := Check(m)
	if !hasField(refusals, "Quantity/QuantityUnitOfMeasure") {
		t.Fatalf("a quantity with no unit was accepted: %v", refusals)
	}
}

func TestAPrescriptionWithNoDirectionsIsRefused(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Sig = nil
	refusals, _ := Check(m)
	if !hasField(refusals, "Sig/SigText") {
		t.Errorf("a prescription with no directions was accepted: %v", refusals)
	}
}

// Date of birth is a refusal, not a warning: it is how a pharmacy tells two patients with the same name
// apart, and dispensing to the wrong one is what it exists to prevent.
func TestAMissingDateOfBirthIsARefusalRatherThanAWarning(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Patient.DateOfBirth = Date{}

	refusals, warnings := Check(m)
	if !hasField(refusals, "Patient/DateOfBirth") {
		t.Errorf("a missing date of birth was not refused: refusals %v, warnings %v", refusals, warnings)
	}
}

func TestTheMessageTypeComesFromWhichBodyElementIsPresent(t *testing.T) {
	m := newRx()
	got, err := m.Type()
	if err != nil {
		t.Fatal(err)
	}
	if got != NewRx {
		t.Errorf("type = %q, want %q", got, NewRx)
	}
	if TypeName(got) != "new prescription" {
		t.Errorf("NEWRX described as %q", TypeName(got))
	}
}

// Two bodies have no meaning, and acting on the first would carry one of them out.
func TestABodyWithTwoMessagesIsRefused(t *testing.T) {
	m := newRx()
	m.Body.CancelRx = m.Body.NewRx

	if _, err := m.Type(); err == nil {
		t.Fatal("a message that is both a new prescription and a cancellation was accepted")
	}
}

func TestAnEmptyBodyIsRefused(t *testing.T) {
	var m Message
	if _, err := m.Type(); err == nil {
		t.Error("an empty body was accepted")
	}
}

func TestAPrescriptionSurvivesARoundTrip(t *testing.T) {
	original := newRx()
	wire, err := Build(original)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	back, err := Parse(wire)
	if err != nil {
		t.Fatalf("Parse of our own output: %v", err)
	}

	p := back.Prescription()
	if p == nil {
		t.Fatal("the prescription did not survive")
	}
	if p.Patient.Name.Last != "SMITH" {
		t.Errorf("patient surname = %q", p.Patient.Name.Last)
	}
	if p.Medication.Description != "Lisinopril 10 MG Oral Tablet" {
		t.Errorf("drug = %q", p.Medication.Description)
	}
	if p.Medication.Quantity.Value != "30" {
		t.Errorf("quantity = %q", p.Medication.Quantity.Value)
	}
	if p.Medication.Sig.Text != "Take one tablet by mouth daily" {
		t.Errorf("sig = %q", p.Medication.Sig.Text)
	}
	// The substitution flag above all, since a round trip that loses it produces a prescription this
	// package would refuse to send.
	sub, err := ParseSubstitution(p.Medication.Substitutions)
	if err != nil {
		t.Fatalf("the substitution flag did not survive: %v", err)
	}
	if !sub.Allowed() {
		t.Error("the substitution flag inverted across a round trip")
	}
	if back.Header.MessageID != original.Header.MessageID {
		t.Errorf("message id = %q, want %q", back.Header.MessageID, original.Header.MessageID)
	}
}

// A message with no version has the receiver guess, usually at whatever it deployed last.
func TestTheVersionIsWrittenEvenWhenTheCallerDidNotSetOne(t *testing.T) {
	m := newRx()
	m.Version, m.Release = "", ""
	wire, err := Build(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `version="010"`) {
		t.Errorf("no version was written:\n%s", wire)
	}
	if !strings.Contains(string(wire), Namespace) {
		t.Error("no namespace was written")
	}
}

// An unqualified message must parse. Senders differ on whether they qualify elements, and refusing one
// would reject valid prescriptions over a prefix.
func TestAMessageWithoutTheNamespaceStillParses(t *testing.T) {
	const raw = `<?xml version="1.0"?>
<Message version="010" release="006">
  <Header>
    <To Qualifier="P">1234567</To>
    <From Qualifier="D">9876543</From>
    <MessageID>ABC123</MessageID>
    <SentTime>2026-08-28T10:00:00Z</SentTime>
  </Header>
  <Body>
    <NewRx>
      <Patient><Name><LastName>SMITH</LastName></Name><DateOfBirth><Date>1980-01-01</Date></DateOfBirth></Patient>
      <Pharmacy><NCPDPID>1234567</NCPDPID></Pharmacy>
      <Prescriber><Name><LastName>JONES</LastName></Name><NPI>1234567893</NPI></Prescriber>
      <MedicationPrescribed>
        <DrugDescription>Amoxicillin 500 MG Capsule</DrugDescription>
        <Substitutions>1</Substitutions>
        <Quantity><Value>21</Value><QuantityUnitOfMeasure><Code>C48480</Code></QuantityUnitOfMeasure></Quantity>
        <Sig><SigText>One capsule three times daily</SigText></Sig>
      </MedicationPrescribed>
    </NewRx>
  </Body>
</Message>`

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("an unqualified SCRIPT message was refused: %v", err)
	}
	if m.Header.To.Qualifier != "P" {
		t.Errorf("the To qualifier was lost: %q", m.Header.To.Qualifier)
	}
	sub, err := ParseSubstitution(m.Prescription().Medication.Substitutions)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Allowed() {
		t.Error("dispense as written was read as substitution allowed")
	}
}

func TestSomethingThatIsNotScriptIsRefused(t *testing.T) {
	for _, in := range []string{
		"",
		"not xml at all",
		`<?xml version="1.0"?><Bundle/>`,
		`<?xml version="1.0"?><Message></Message>`, // no body
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) was accepted", clip(in))
		}
	}
}

// Forwarding must not be blocked by validation. A channel passing a prescription through has not authored
// it, and refusing to re-serialise a message that arrived from elsewhere strands it instead of letting an
// operator see it.
func TestForwardingAnInvalidPrescriptionIsPossibleButDeliberate(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Substitutions = "" // would be refused

	if _, err := Build(m); err == nil {
		t.Fatal("Build accepted a prescription with no substitution flag")
	}
	if _, err := BuildUnchecked(m); err != nil {
		t.Errorf("BuildUnchecked refused to forward it: %v", err)
	}
}

// The schedule reaches the drug package, so the two agree about what a schedule permits.
func TestTheScheduleIsReadThroughTheDrugPackage(t *testing.T) {
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "C-2"
	got, err := m.Body.NewRx.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	if got != drug.ScheduleII {
		t.Errorf("schedule = %q, want %q", got, drug.ScheduleII)
	}
}

// PRN and 99 must behave alike, or a channel treats the word and the number differently.
func TestAsNeededRefillsAreTreatedTheSameWrittenEitherWay(t *testing.T) {
	for _, in := range []string{"PRN", "99", "as needed"} {
		n, given, err := parseRefills(in)
		if err != nil || !given || n != 99 {
			t.Errorf("parseRefills(%q) = (%d, %v, %v), want (99, true, nil)", in, n, given, err)
		}
	}
	// And as-needed on a schedule II is still refused, because no schedule permits it.
	m := newRx()
	m.Body.NewRx.Medication.Coded.DEASchedule = "CII"
	m.Body.NewRx.Medication.NumberOfRefills = "PRN"
	m.Body.NewRx.Prescriber.DEANumber = "AB1234563"
	refusals, _ := Check(m)
	if !hasField(refusals, "NumberOfRefills") {
		t.Errorf("as-needed refills on a schedule II were accepted: %v", refusals)
	}
}

// newRx returns a complete, valid new prescription.
func newRx() Message {
	return Message{
		Version: DefaultVersion,
		Release: DefaultRelease,
		Header: Header{
			To:                    Endpoint{Qualifier: "P", Value: "1234567"},
			From:                  Endpoint{Qualifier: "D", Value: "9876543"},
			MessageID:             "MSG-0001",
			SentTime:              "2026-08-28T10:00:00Z",
			PrescriberOrderNumber: "ORD-1",
			SenderSoftware:        SenderSoftware{Developer: "Perfuse", Product: "Perfuse", VersionRelease: "1"},
		},
		Body: Body{NewRx: &Prescription{
			Patient: Patient{
				Name:        Name{Last: "SMITH", First: "JOHN"},
				Gender:      "M",
				DateOfBirth: Date{Date: "1980-01-01"},
			},
			Pharmacy:   Pharmacy{BusinessName: "MAIN STREET PHARMACY", NCPDPID: "1234567"},
			Prescriber: Prescriber{Name: Name{Last: "JONES", First: "MARY"}, NPI: "1234567893"},
			Medication: &Medication{
				Description:     "Lisinopril 10 MG Oral Tablet",
				Coded:           &Coded{ProductCode: "00093-1036-01", ProductCodeQualifier: "ND"},
				Quantity:        &Quantity{Value: "30", UnitOfMeasure: &UnitOfMeasure{Code: "C48542"}},
				DaysSupply:      "30",
				Substitutions:   "0",
				NumberOfRefills: "2",
				Sig:             &Sig{Text: "Take one tablet by mouth daily"},
				WrittenDate:     &Date{Date: "2026-08-28"},
			},
		}},
	}
}

func hasField(ps []Problem, field string) bool {
	for _, p := range ps {
		if p.Field == field {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 30 {
		return s[:30] + "…"
	}
	return s
}
