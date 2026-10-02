package fhir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"R4": R4, "r4": R4, "4.0.1": R4, "4.0": R4,
		"R4B": R4B, "4.3.0": R4B,
		"R5": R5, "5.0.0": R5, "": DefaultVersion,
	}
	for in, want := range cases {
		got, err := ParseVersion(in)
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", in, got, want)
		}
	}

	// R6 is in ballot. Refusing it with an explanation is better than producing a
	// guess at an unpublished specification.
	if _, err := ParseVersion("R6"); err == nil {
		t.Error("R6 was accepted; it is not published")
	} else if !strings.Contains(err.Error(), "ballot") {
		t.Errorf("the R6 error does not explain why: %v", err)
	}

	if _, err := ParseVersion("DSTU2"); err == nil {
		t.Error("an unsupported version was accepted")
	}
}

func TestDefaultIsLatestPublished(t *testing.T) {
	// R4 is the default because the resource structs are R4-shaped and US Core,
	// CMS interoperability rules, and the vast majority of production EHRs speak R4.
	// A server declaring R5 while serving R4-shaped resources causes clients to parse
	// the wrong wire format. R5 remains selectable for explicit use.
	if DefaultVersion != R4 {
		t.Errorf("DefaultVersion = %q, want R4, the release matching the resource shape", DefaultVersion)
	}
	if !R4.Before(R5) {
		t.Error("R4 should sort before R5")
	}
	if !R4.IsR4Family() || !R4B.IsR4Family() || R5.IsR4Family() {
		t.Error("IsR4Family is wrong")
	}
}

func samplePatient() *Patient {
	p := &Patient{
		Identifier: []Identifier{{
			System: "http://example.org/mrn",
			Value:  "MRN1",
			Type:   NewCodeableConcept(SystemIdentifierType, "MR", "Medical record number"),
		}},
		Name:      []HumanName{{Family: "Doe", Given: []string{"Jane"}, Use: "official"}},
		Gender:    "female",
		BirthDate: "1980-01-01",
	}
	p.SetResourceID("p1")
	return p
}

func TestMarshalIncludesResourceType(t *testing.T) {
	raw, err := Marshal(samplePatient(), R5)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	if tree["resourceType"] != "Patient" {
		t.Errorf("resourceType = %v", tree["resourceType"])
	}
	if tree["id"] != "p1" {
		t.Errorf("id = %v", tree["id"])
	}
}

func TestChoiceElementRejected(t *testing.T) {
	// FHIR allows exactly one member of a choice group. Emitting two produces a
	// resource a server rejects without saying why, so it fails here instead.
	p := samplePatient()
	p.DeceasedBoolean = Bool(true)
	p.DeceasedDateTime = "2026-01-01T00:00:00Z"

	if _, err := Marshal(p, R5); err == nil {
		t.Fatal("a resource setting both halves of deceased[x] was serialised")
	} else if !strings.Contains(err.Error(), "choice") {
		t.Errorf("the error does not explain the problem: %v", err)
	}

	// And validation surfaces it rather than leaving it to serialisation.
	result := Validate(p, R5)
	if result.Valid() {
		t.Error("validation passed a resource with two values in one choice element")
	}
}

func TestObservationChoiceElement(t *testing.T) {
	o := &Observation{Status: "final", Code: NewCodeableConcept(SystemLOINC, "718-7", "Hemoglobin")}
	o.SetResourceID("o1")
	o.ValueString = Str("text")
	o.ValueQuantity = &Quantity{Value: Float(1)}

	if _, err := Marshal(o, R5); err == nil {
		t.Error("an observation with two values was serialised")
	}
}

func TestEncounterR4Downgrade(t *testing.T) {
	e := &Encounter{
		Status:       "completed",
		Class:        []CodeableConcept{*NewCodeableConcept(SystemActCode, "IMP", "inpatient encounter")},
		ActualPeriod: &Period{Start: "2026-08-18T12:00:00Z", End: "2026-08-19T09:00:00Z"},
		Admission:    &EncounterAdmission{DischargeDisposition: TextOnly("home")},
		Participant: []EncounterParticipant{{
			Actor: Ref("Practitioner", "prac1"),
		}},
		Location: []EncounterLocation{{
			Location: Ref("Location", "loc1"),
			Form:     TextOnly("ward"),
		}},
	}
	e.SetResourceID("e1")

	r5Raw, err := Marshal(e, R5)
	if err != nil {
		t.Fatal(err)
	}
	r4Raw, err := Marshal(e, R4)
	if err != nil {
		t.Fatal(err)
	}

	var r5, r4 map[string]any
	if err := json.Unmarshal(r5Raw, &r5); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r4Raw, &r4); err != nil {
		t.Fatal(err)
	}

	// Field renames. Emitting the R5 name in an R4 resource loses the value
	// silently, because the resource still validates structurally.
	if _, ok := r5["actualPeriod"]; !ok {
		t.Error("R5 lacks actualPeriod")
	}
	if _, ok := r4["period"]; !ok {
		t.Error("R4 lacks period")
	}
	if _, ok := r4["actualPeriod"]; ok {
		t.Error("R4 kept the R5 field name")
	}
	if _, ok := r4["hospitalization"]; !ok {
		t.Error("R4 lacks hospitalization")
	}
	if _, ok := r4["admission"]; ok {
		t.Error("R4 kept R5's admission")
	}

	// Type change: R4's class is one Coding, R5's a list of CodeableConcept.
	class4, ok := r4["class"].(map[string]any)
	if !ok {
		t.Fatalf("R4 class is %T, want a single Coding", r4["class"])
	}
	if class4["code"] != "IMP" {
		t.Errorf("R4 class code = %v, want IMP", class4["code"])
	}
	if _, ok := r5["class"].([]any); !ok {
		t.Errorf("R5 class is %T, want an array", r5["class"])
	}

	// Status value set change.
	if r5["status"] != "completed" {
		t.Errorf("R5 status = %v", r5["status"])
	}
	if r4["status"] != "finished" {
		t.Errorf("R4 status = %v, want finished", r4["status"])
	}

	// Nested renames.
	if parts, ok := r4["participant"].([]any); ok && len(parts) > 0 {
		part := parts[0].(map[string]any)
		if _, ok := part["individual"]; !ok {
			t.Error("R4 participant lacks individual")
		}
		if _, ok := part["actor"]; ok {
			t.Error("R4 participant kept R5's actor")
		}
	}
	if locs, ok := r4["location"].([]any); ok && len(locs) > 0 {
		loc := locs[0].(map[string]any)
		if _, ok := loc["physicalType"]; !ok {
			t.Error("R4 location lacks physicalType")
		}
	}
}

func TestEncounterStatusMappingCoversR5ValueSet(t *testing.T) {
	// Every R5 status must map to something valid in R4, or an R4 consumer gets a
	// code outside its required binding.
	for status := range encounterStatusesR5 {
		mapped, ok := encounterStatusR5ToR4[status]
		if !ok {
			t.Errorf("R5 status %q has no R4 mapping", status)
			continue
		}
		if !encounterStatusesR4[mapped] {
			t.Errorf("R5 %q maps to %q, which is not in the R4 value set", status, mapped)
		}
	}
}

func TestValidatePatient(t *testing.T) {
	result := Validate(samplePatient(), R5)
	if !result.Valid() {
		t.Errorf("a well-formed patient failed validation: %+v", result.Findings)
	}

	// An invalid gender is a required-binding error, not a warning.
	bad := samplePatient()
	bad.Gender = "yes"
	result = Validate(bad, R5)
	if result.Valid() {
		t.Error("an invalid gender was accepted")
	}

	// A malformed date is an error.
	bad = samplePatient()
	bad.BirthDate = "18/08/2026"
	if Validate(bad, R5).Valid() {
		t.Error("a non-FHIR date was accepted")
	}

	// No identifier is a US Core warning, not an error: the resource is still
	// valid FHIR.
	bare := &Patient{Name: []HumanName{{Family: "Doe"}}}
	bare.SetResourceID("p2")
	result = Validate(bare, R5)
	if !result.Valid() {
		t.Errorf("a patient with no identifier should be valid FHIR: %+v", result.Findings)
	}
	if _, warnings, _ := result.Counts(); warnings == 0 {
		t.Error("a patient with no identifier produced no US Core warning")
	}
}

func TestValidateDateTimeNeedsTimezone(t *testing.T) {
	// A dateTime with a time and no offset is the mistake people make converting
	// v2 timestamps, and the message should say so.
	o := &Observation{
		Status:            "final",
		Code:              NewCodeableConcept(SystemLOINC, "718-7", "Hemoglobin"),
		ValueString:       Str("x"),
		EffectiveDateTime: "2026-08-18T12:00:00",
	}
	o.SetResourceID("o1")

	result := Validate(o, R5)
	if result.Valid() {
		t.Fatal("a dateTime with no timezone was accepted")
	}
	var explained bool
	for _, f := range result.Findings {
		if strings.Contains(f.Message, "timezone") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("the error does not mention the timezone: %+v", result.Findings)
	}

	// With an offset it is fine.
	o.EffectiveDateTime = "2026-08-18T12:00:00-05:00"
	if !Validate(o, R5).Valid() {
		t.Errorf("a valid dateTime was rejected: %+v", Validate(o, R5).Findings)
	}
}

func TestValidateObservationValueRules(t *testing.T) {
	base := func() *Observation {
		o := &Observation{
			Status: "final",
			Code:   NewCodeableConcept(SystemLOINC, "718-7", "Hemoglobin"),
		}
		o.SetResourceID("o1")
		return o
	}

	// No value and no reason: a reader cannot tell "not measured" from "measured
	// as nothing", which is worth a warning.
	result := Validate(base(), R5)
	if _, warnings, _ := result.Counts(); warnings == 0 {
		t.Error("an observation with no value and no dataAbsentReason produced no warning")
	}

	// Both is contradictory and is an error.
	both := base()
	both.ValueString = Str("x")
	both.DataAbsentReason = NewCodeableConcept(SystemDataAbsentReason, "unknown", "Unknown")
	if Validate(both, R5).Valid() {
		t.Error("a value together with dataAbsentReason was accepted")
	}

	// A unit with no UCUM code cannot be converted by a receiver.
	uncoded := base()
	uncoded.ValueQuantity = &Quantity{Value: Float(13.5), Unit: "g/dL"}
	result = Validate(uncoded, R5)
	var flagged bool
	for _, f := range result.Findings {
		if strings.Contains(f.Message, "UCUM") {
			flagged = true
		}
	}
	if !flagged {
		t.Error("a unit with no UCUM code produced no warning")
	}
}

func TestValidateEncounterPeriodOrder(t *testing.T) {
	e := &Encounter{
		Status:       "completed",
		Class:        []CodeableConcept{*NewCodeableConcept(SystemActCode, "IMP", "inpatient")},
		Subject:      Ref("Patient", "p1"),
		ActualPeriod: &Period{Start: "2026-08-19T09:00:00Z", End: "2026-08-18T12:00:00Z"},
	}
	e.SetResourceID("e1")

	if Validate(e, R5).Valid() {
		t.Error("an encounter that ends before it starts was accepted")
	}
}

func TestValidateBundleTransactionEntries(t *testing.T) {
	// A transaction entry with no request gives the server no instruction.
	b := &Bundle{Type: BundleTransaction}
	b.SetResourceID("b1")
	b.Entry = []BundleEntry{{Resource: samplePatient()}}

	result := Validate(b, R5)
	if result.Valid() {
		t.Error("a transaction entry with no request was accepted")
	}

	// With a request it is fine, and the contained resource is validated too.
	b.Entry[0].Request = &BundleRequest{Method: "PUT", URL: "Patient/p1"}
	b.Entry[0].FullURL = "urn:uuid:p1"
	if !Validate(b, R5).Valid() {
		t.Errorf("a well-formed transaction was rejected: %+v", Validate(b, R5).Findings)
	}

	// An invalid contained resource fails the bundle, with the path saying which
	// entry.
	b.Entry[0].Resource.(*Patient).Gender = "invalid"
	result = Validate(b, R5)
	if result.Valid() {
		t.Fatal("a bundle containing an invalid resource passed")
	}
	var located bool
	for _, f := range result.Findings {
		if strings.HasPrefix(f.Path, "Bundle.entry[0]") {
			located = true
		}
	}
	if !located {
		t.Errorf("the finding does not say which entry was at fault: %+v", result.Findings)
	}
}

func TestValidateResourceID(t *testing.T) {
	p := samplePatient()
	p.ID = "not a valid id!"
	if Validate(p, R5).Valid() {
		t.Error("an invalid resource id was accepted")
	}
}

func TestMarshalBundleConvertsEveryEntry(t *testing.T) {
	e := &Encounter{
		Status:       "completed",
		ActualPeriod: &Period{Start: "2026-08-18T12:00:00Z"},
		Class:        []CodeableConcept{*NewCodeableConcept(SystemActCode, "IMP", "inpatient")},
	}
	e.SetResourceID("e1")

	b := &Bundle{Type: BundleTransaction}
	b.SetResourceID("b1")
	b.Entry = []BundleEntry{
		{FullURL: "urn:uuid:p1", Resource: samplePatient(),
			Request: &BundleRequest{Method: "PUT", URL: "Patient/p1"}},
		{FullURL: "urn:uuid:e1", Resource: e,
			Request: &BundleRequest{Method: "PUT", URL: "Encounter/e1"}},
	}

	raw, err := MarshalBundle(b, R4)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	entries := tree["entry"].([]any)
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}

	// The contained encounter must be downgraded too, not just the bundle wrapper.
	enc := entries[1].(map[string]any)["resource"].(map[string]any)
	if _, ok := enc["period"]; !ok {
		t.Error("the encounter inside an R4 bundle was not converted to R4")
	}
	if enc["status"] != "finished" {
		t.Errorf("contained status = %v, want finished in R4", enc["status"])
	}
}

func TestUnmarshalResource(t *testing.T) {
	raw, err := Marshal(samplePatient(), R5)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalResource(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := got.(*Patient)
	if !ok {
		t.Fatalf("got %T, want *Patient", got)
	}
	if p.ID != "p1" || p.Gender != "female" {
		t.Errorf("round trip lost data: %+v", p)
	}

	// An unknown type is refused rather than silently accepted as empty.
	if _, err := UnmarshalResource([]byte(`{"resourceType":"Flumox"}`)); err == nil {
		t.Error("an unknown resource type was accepted")
	}
	if _, err := UnmarshalResource([]byte(`{"id":"x"}`)); err == nil {
		t.Error("JSON with no resourceType was accepted as a resource")
	}
	if _, err := UnmarshalResource([]byte(`not json`)); err == nil {
		t.Error("invalid JSON was accepted")
	}
}

func TestOperationOutcomeFromValidation(t *testing.T) {
	bad := samplePatient()
	bad.Gender = "invalid"
	outcome := Validate(bad, R5).OperationOutcome()

	if len(outcome.Issue) == 0 {
		t.Fatal("no issues")
	}
	if outcome.ResourceTypeName() != "OperationOutcome" {
		t.Error("wrong resource type")
	}
	var hasError bool
	for _, issue := range outcome.Issue {
		if issue.Severity == SeverityError {
			hasError = true
			if len(issue.Expression) == 0 {
				t.Error("an issue has no expression, so a client cannot locate it")
			}
		}
	}
	if !hasError {
		t.Error("an invalid resource produced no error issue")
	}
}

func TestNewCodeableConceptKeepsTextWhenUncoded(t *testing.T) {
	// A value with no code must keep its text rather than becoming nothing.
	if c := NewCodeableConcept("", "", "Some local thing"); c == nil || c.Text != "Some local thing" {
		t.Errorf("uncoded concept = %+v", c)
	}
	if c := NewCodeableConcept("", "", ""); c != nil {
		t.Error("an empty concept should be nil rather than an empty object")
	}
	if c := TextOnly("free text"); c == nil || len(c.Coding) != 0 || c.Text != "free text" {
		t.Errorf("TextOnly = %+v", c)
	}
}

func TestUSCoreProfileURLs(t *testing.T) {
	for _, resourceType := range []string{"Patient", "Encounter", "Observation", "DiagnosticReport"} {
		if USCoreProfile(resourceType) == "" {
			t.Errorf("no US Core profile for %s", resourceType)
		}
	}
	if USCoreProfile("Flumox") != "" {
		t.Error("a profile was returned for an unknown resource type")
	}
}

func TestNonEmptyFiltersBlanks(t *testing.T) {
	got := NonEmpty("a", "", "  ", "b")
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("NonEmpty = %v", got)
	}
	if NonEmpty("", "") != nil {
		t.Error("all-blank input should give nil rather than an empty slice")
	}
}
