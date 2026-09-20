package v2fhir

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Fixtures are built by field number. See fixtures_test.go for why.

var adtA01 = message(
	segment("MSH", mshFields("ADT^A01^ADT_A01", "CTRL1", "20260818120000-0500")),
	segment("EVN", map[int]string{1: "A01", 2: "20260818115900-0500"}),
	segment("PID", map[int]string{
		1:  "1",
		3:  "MRN123456^^^SITEA^MR~999887777^^^SSA^SS",
		5:  "Doe^Jane^Q^^Ms.^^L",
		7:  "19800101",
		8:  "F",
		11: "123 Main St^Apt 4^Birmingham^AL^35205^USA^H",
		13: "2055551234^^PH",
	}),
	segment("PV1", map[int]string{
		1:  "1",
		2:  "I",
		3:  "ICU^0201^01^SITEA",
		7:  "1234^Attending^Adam^^^Dr.",
		10: "MED",
		19: "V0012345^^^SITEA^VN",
		44: "20260818120000-0500",
	}),
)

var oruR01 = message(
	segment("MSH", mshFields("ORU^R01^ORU_R01", "CTRL2", "20260818130000-0500")),
	segment("PID", map[int]string{
		1: "1",
		3: "MRN123456^^^SITEA^MR",
		5: "Doe^Jane^Q",
		7: "19800101",
		8: "F",
	}),
	segment("OBR", map[int]string{
		1:  "1",
		2:  "ORD987",
		3:  "FILL654",
		4:  "CBC^Complete Blood Count^LN",
		7:  "20260818113000-0500",
		14: "20260818114500-0500",
		15: "BLD^Blood^HL70070",
		16: "1234^Pathologist^Paul",
		22: "20260818125900-0500",
		25: "F",
	}),
	segment("OBX", map[int]string{
		1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 5: "13.5", 6: "g/dL",
		7: "12.0-16.0", 8: "N", 11: "F", 14: "20260818120000-0500",
	}),
	segment("OBX", map[int]string{
		1: "2", 2: "NM", 3: "789-8^Erythrocytes^LN", 5: "4.52", 6: "10*6/uL",
		7: "4.20-5.40", 8: "N", 11: "F", 14: "20260818120000-0500",
	}),
	segment("OBX", map[int]string{
		1: "3", 2: "NM", 3: "6690-2^Leukocytes^LN", 5: "14.2", 6: "10*3/uL",
		7: "4.0-11.0", 8: "H", 11: "F", 14: "20260818120000-0500",
	}),
	segment("OBX", map[int]string{
		1: "4", 2: "ST", 3: "11156-7^Comment^LN", 5: "Specimen slightly hemolysed", 11: "F",
	}),
)

func mustParse(t *testing.T, raw string) *hl7.Message {
	t.Helper()
	m, err := hl7.ParseString(raw)
	if err != nil {
		t.Fatalf("parsing the v2 message: %v", err)
	}
	return m
}

func convert(t *testing.T, raw string, opts Options) *Result {
	t.Helper()
	if opts.Timezone == nil {
		opts.Timezone = time.UTC
	}
	res, err := Convert(mustParse(t, raw), opts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return res
}

// find returns the first resource of a type in the bundle.
func find[T fhir.Resource](t *testing.T, res *Result) T {
	t.Helper()
	var zero T
	for _, e := range res.Bundle.Entry {
		if typed, ok := e.Resource.(T); ok {
			return typed
		}
	}
	t.Fatalf("no %T in the bundle; got %v", zero, res.ResourceCounts())
	return zero
}

func findAll[T fhir.Resource](res *Result) []T {
	var out []T
	for _, e := range res.Bundle.Entry {
		if typed, ok := e.Resource.(T); ok {
			out = append(out, typed)
		}
	}
	return out
}

func TestADTProducesPatientAndEncounter(t *testing.T) {
	res := convert(t, adtA01, Options{
		DefaultIdentifierSystem: "urn:oid:1.2.3.4",
		AssigningAuthoritySystems: map[string]string{
			"SITEA": "http://sitea.example.org/mrn",
		},
	})

	counts := res.ResourceCounts()
	if counts["Patient"] != 1 {
		t.Errorf("got %d patients, want 1 (%v)", counts["Patient"], counts)
	}
	if counts["Encounter"] != 1 {
		t.Errorf("got %d encounters, want 1 (%v)", counts["Encounter"], counts)
	}
	if res.Bundle.Type != fhir.BundleTransaction {
		t.Errorf("bundle type = %q, want transaction", res.Bundle.Type)
	}
}

func TestPatientMapping(t *testing.T) {
	res := convert(t, adtA01, Options{
		AssigningAuthoritySystems: map[string]string{
			"SITEA": "http://sitea.example.org/mrn",
			"SSA":   "http://hl7.org/fhir/sid/us-ssn",
		},
	})
	p := find[*fhir.Patient](t, res)

	// Both identifiers must survive. A patient commonly has an MRN and a national
	// identifier, and dropping either breaks matching for whoever needed that one.
	if len(p.Identifier) != 2 {
		t.Fatalf("got %d identifiers, want 2: %+v", len(p.Identifier), p.Identifier)
	}
	if p.Identifier[0].Value != "MRN123456" {
		t.Errorf("first identifier = %q", p.Identifier[0].Value)
	}
	if p.Identifier[0].System != "http://sitea.example.org/mrn" {
		t.Errorf("identifier system = %q, want the configured URI", p.Identifier[0].System)
	}
	if p.Identifier[0].Type == nil || p.Identifier[0].Type.Coding[0].Code != "MR" {
		t.Errorf("identifier type was not carried through: %+v", p.Identifier[0].Type)
	}

	if len(p.Name) != 1 {
		t.Fatalf("got %d names, want 1", len(p.Name))
	}
	if p.Name[0].Family != "Doe" {
		t.Errorf("family = %q", p.Name[0].Family)
	}
	if strings.Join(p.Name[0].Given, " ") != "Jane Q" {
		t.Errorf("given = %v, want Jane and Q", p.Name[0].Given)
	}
	if p.Name[0].Use != "official" {
		t.Errorf("name use = %q, want official for an L name", p.Name[0].Use)
	}

	if p.Gender != "female" {
		t.Errorf("gender = %q, want female", p.Gender)
	}
	if p.BirthDate != "1980-01-01" {
		t.Errorf("birthDate = %q, want 1980-01-01", p.BirthDate)
	}

	if len(p.Address) != 1 {
		t.Fatalf("got %d addresses, want 1", len(p.Address))
	}
	if p.Address[0].City != "Birmingham" || p.Address[0].State != "AL" {
		t.Errorf("address = %+v", p.Address[0])
	}
	if p.Address[0].Use != "home" {
		t.Errorf("address use = %q, want home", p.Address[0].Use)
	}

	if len(p.Telecom) == 0 {
		t.Error("no telecom was mapped from PID-13")
	}
}

func TestSSNIsFlagged(t *testing.T) {
	// An SSN is an identifier a deployment may be obliged not to store, so the
	// conversion has to point it out rather than quietly forward it.
	res := convert(t, adtA01, Options{})

	var found bool
	for _, n := range res.Notes {
		if strings.Contains(n.Message, "social security number") {
			found = true
		}
	}
	if !found {
		t.Error("an SSN identifier was mapped without a warning")
	}
}

func TestUnknownAssigningAuthorityIsWarned(t *testing.T) {
	// A bare MRN with no namespace is ambiguous between facilities, and that is
	// worth saying rather than silently emitting.
	res := convert(t, adtA01, Options{})

	var found bool
	for _, n := range res.Notes {
		if n.Severity == "warning" && strings.Contains(n.Message, "no configured system URI") {
			found = true
		}
	}
	if !found {
		t.Errorf("an unmapped assigning authority produced no warning: %+v", res.Notes)
	}
}

func TestEncounterMapping(t *testing.T) {
	res := convert(t, adtA01, Options{
		AssigningAuthoritySystems: map[string]string{"SITEA": "http://sitea.example.org/mrn"},
	})
	e := find[*fhir.Encounter](t, res)

	// An A01 is an admission, so the visit is in progress. This is the mapping
	// that most often goes wrong, because the trigger event describes what
	// happened rather than the resulting state.
	if e.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress for an A01", e.Status)
	}

	if len(e.Class) != 1 {
		t.Fatalf("got %d classes, want 1", len(e.Class))
	}
	if e.Class[0].Coding[0].Code != "IMP" {
		t.Errorf("class = %q, want IMP for patient class I", e.Class[0].Coding[0].Code)
	}

	if len(e.Identifier) == 0 || e.Identifier[0].Value != "V0012345" {
		t.Errorf("visit number was not carried into the identifier: %+v", e.Identifier)
	}
	if e.Subject == nil || !strings.HasPrefix(e.Subject.Reference, "Patient/") {
		t.Errorf("subject = %+v, want a reference to the patient", e.Subject)
	}
	if len(e.Location) == 0 {
		t.Error("PV1-3 was not mapped to a location")
	}
	if len(e.Participant) == 0 {
		t.Error("the attending physician in PV1-7 was not mapped to a participant")
	}
}

func TestEncounterStatusPerTriggerEvent(t *testing.T) {
	cases := map[string]string{
		"A01": "in-progress",
		"A03": "completed",
		"A04": "in-progress",
		"A05": "planned",
		"A08": "in-progress",
		"A11": "cancelled",
		// A13 cancels a discharge, so the visit is open again. Treating it as
		// finished would leave a discharged patient who is still in a bed.
		"A13": "in-progress",
		"A21": "on-hold",
	}

	for event, want := range cases {
		res := convert(t, adtWithEvent(event), Options{})

		encounters := findAll[*fhir.Encounter](res)
		if len(encounters) == 0 {
			t.Errorf("%s produced no encounter", event)
			continue
		}
		if encounters[0].Status != want {
			t.Errorf("%s encounter status = %q, want %q", event, encounters[0].Status, want)
		}
	}
}

func TestPersonLevelMessagesHaveNoEncounter(t *testing.T) {
	// An A28 carries person information with no visit, so inventing an encounter
	// would assert a hospital stay that never happened.
	for _, event := range []string{"A28", "A31"} {
		res := convert(t, adtWithEvent(event), Options{})

		if got := len(findAll[*fhir.Encounter](res)); got != 0 {
			t.Errorf("%s produced %d encounters, want 0", event, got)
		}
		if len(findAll[*fhir.Patient](res)) != 1 {
			t.Errorf("%s did not produce a patient", event)
		}
	}
}

func TestORUProducesReportAndObservations(t *testing.T) {
	res := convert(t, oruR01, Options{DefaultIdentifierSystem: "urn:oid:1.2.3.4"})

	counts := res.ResourceCounts()
	if counts["DiagnosticReport"] != 1 {
		t.Errorf("got %d reports, want 1 (%v)", counts["DiagnosticReport"], counts)
	}
	if counts["Observation"] != 4 {
		t.Errorf("got %d observations, want 4 (%v)", counts["Observation"], counts)
	}
	if counts["Specimen"] != 1 {
		t.Errorf("got %d specimens, want 1 (%v)", counts["Specimen"], counts)
	}

	report := find[*fhir.DiagnosticReport](t, res)
	if len(report.Result) != 4 {
		t.Errorf("the report references %d results, want 4", len(report.Result))
	}
	if report.Status != "final" {
		t.Errorf("report status = %q, want final for OBR-25 of F", report.Status)
	}
	if report.Code == nil || report.Code.Coding[0].System != fhir.SystemLOINC {
		t.Errorf("report code was not recognised as LOINC: %+v", report.Code)
	}
	if len(report.Specimen) == 0 {
		t.Error("the report does not reference the specimen")
	}
}

func TestNumericObservationWithUnitsAndRange(t *testing.T) {
	res := convert(t, oruR01, Options{})
	observations := findAll[*fhir.Observation](res)
	if len(observations) < 3 {
		t.Fatalf("got %d observations", len(observations))
	}

	hgb := observations[0]
	if hgb.Code.Coding[0].Code != "718-7" {
		t.Errorf("first observation code = %q, want 718-7", hgb.Code.Coding[0].Code)
	}
	if hgb.Code.Coding[0].System != fhir.SystemLOINC {
		t.Errorf("LN was not mapped to the LOINC system: %q", hgb.Code.Coding[0].System)
	}
	if hgb.ValueQuantity == nil {
		t.Fatal("a numeric result did not produce a quantity")
	}
	if *hgb.ValueQuantity.Value != 13.5 {
		t.Errorf("value = %v, want 13.5", *hgb.ValueQuantity.Value)
	}
	if hgb.ValueQuantity.Unit != "g/dL" {
		t.Errorf("unit = %q", hgb.ValueQuantity.Unit)
	}
	// The UCUM code is what lets a receiver convert or compare the value safely.
	if hgb.ValueQuantity.Code != "g/dL" || hgb.ValueQuantity.System != fhir.SystemUCUM {
		t.Errorf("UCUM coding = %q %q, want g/dL in the UCUM system",
			hgb.ValueQuantity.System, hgb.ValueQuantity.Code)
	}

	if len(hgb.ReferenceRange) != 1 {
		t.Fatalf("got %d reference ranges, want 1", len(hgb.ReferenceRange))
	}
	rr := hgb.ReferenceRange[0]
	if rr.Low == nil || *rr.Low.Value != 12.0 || rr.High == nil || *rr.High.Value != 16.0 {
		t.Errorf("reference range was not parsed into low and high: %+v", rr)
	}
	if rr.Text != "12.0-16.0" {
		t.Errorf("the original range text was not kept: %q", rr.Text)
	}
}

func TestCellCountUnitsAreMappedToUCUM(t *testing.T) {
	// Cell counts are the units laboratories write half a dozen ways, and getting
	// them wrong changes the magnitude of a result.
	res := convert(t, oruR01, Options{})
	observations := findAll[*fhir.Observation](res)

	byCode := map[string]*fhir.Observation{}
	for _, o := range observations {
		if o.Code != nil && len(o.Code.Coding) > 0 {
			byCode[o.Code.Coding[0].Code] = o
		}
	}

	rbc := byCode["789-8"]
	if rbc == nil || rbc.ValueQuantity == nil {
		t.Fatal("erythrocyte count is missing")
	}
	if rbc.ValueQuantity.Code != "10*6/uL" {
		t.Errorf("RBC unit code = %q, want 10*6/uL", rbc.ValueQuantity.Code)
	}

	wbc := byCode["6690-2"]
	if wbc == nil || wbc.ValueQuantity == nil {
		t.Fatal("leukocyte count is missing")
	}
	if wbc.ValueQuantity.Code != "10*3/uL" {
		t.Errorf("WBC unit code = %q, want 10*3/uL", wbc.ValueQuantity.Code)
	}
	// The high flag has to survive, because it is the part a clinician reacts to.
	if len(wbc.Interpretation) == 0 || wbc.Interpretation[0].Coding[0].Code != "H" {
		t.Errorf("abnormal flag was lost: %+v", wbc.Interpretation)
	}
}

func TestUnknownUnitIsNotGuessed(t *testing.T) {
	// Guessing a UCUM code can turn a normal result into an alarming one, so an
	// unrecognised unit keeps its text and produces a warning.
	res := convert(t, oruWithFirstOBX(map[int]string{
		1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 5: "13.5",
		6: "WIDGETS/FORTNIGHT", 7: "12.0-16.0", 8: "N", 11: "F",
	}), Options{})

	observations := findAll[*fhir.Observation](res)
	if len(observations) == 0 {
		t.Fatal("no observations")
	}
	q := observations[0].ValueQuantity
	if q == nil {
		t.Fatal("no quantity")
	}
	if q.Unit != "WIDGETS/FORTNIGHT" {
		t.Errorf("the original unit text was not kept: %q", q.Unit)
	}
	if q.Code != "" || q.System != "" {
		t.Errorf("a UCUM code was invented for an unknown unit: %q %q", q.System, q.Code)
	}

	var warned bool
	for _, n := range res.Notes {
		if strings.Contains(n.Message, "no known UCUM code") {
			warned = true
		}
	}
	if !warned {
		t.Error("an unmappable unit produced no warning")
	}
}

func TestTextObservation(t *testing.T) {
	res := convert(t, oruR01, Options{})
	observations := findAll[*fhir.Observation](res)

	var comment *fhir.Observation
	for _, o := range observations {
		if o.Code != nil && len(o.Code.Coding) > 0 && o.Code.Coding[0].Code == "11156-7" {
			comment = o
		}
	}
	if comment == nil {
		t.Fatal("the ST observation is missing")
	}
	if comment.ValueString == nil {
		t.Fatalf("an ST result did not produce valueString: %+v", comment)
	}
	if *comment.ValueString != "Specimen slightly hemolysed" {
		t.Errorf("valueString = %q", *comment.ValueString)
	}
	if comment.ValueQuantity != nil {
		t.Error("a text result must not become a quantity")
	}
}

func TestNumericTypeWithNonNumericValueStaysText(t *testing.T) {
	// The sender said NM and sent text. Coercing it would invent a number.
	res := convert(t, oruWithFirstOBX(map[int]string{
		1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 5: "DETECTED",
		6: "g/dL", 7: "12.0-16.0", 11: "F",
	}), Options{})

	observations := findAll[*fhir.Observation](res)
	if len(observations) == 0 {
		t.Fatal("no observations")
	}
	o := observations[0]
	if o.ValueString == nil || *o.ValueString != "DETECTED" {
		t.Errorf("value = %+v, want the original text", o.ValueString)
	}
	if o.ValueQuantity != nil {
		t.Error("a non-numeric value became a quantity")
	}

	var warned bool
	for _, n := range res.Notes {
		if strings.Contains(n.Message, "is not a number") {
			warned = true
		}
	}
	if !warned {
		t.Error("the type mismatch produced no warning")
	}
}

func TestMissingValueProducesDataAbsentReason(t *testing.T) {
	// A result that arrived with no value is different from one nobody sent, and
	// saying which is the point.
	res := convert(t, oruWithFirstOBX(map[int]string{
		1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 6: "g/dL",
		7: "12.0-16.0", 11: "F",
	}), Options{})

	observations := findAll[*fhir.Observation](res)
	if len(observations) == 0 {
		t.Fatal("no observations")
	}
	if observations[0].DataAbsentReason == nil {
		t.Error("a result with no value has no dataAbsentReason")
	}
	if observations[0].ValueQuantity != nil {
		t.Error("a value was invented for an empty result")
	}
}

func TestTimestampsBecomeValidFHIR(t *testing.T) {
	// v2 timestamps are not ISO 8601. Copying them through is the most common
	// reason a converted resource is rejected.
	res := convert(t, adtA01, Options{})
	e := find[*fhir.Encounter](t, res)

	if e.ActualPeriod == nil {
		t.Fatal("no period")
	}
	if !strings.HasPrefix(e.ActualPeriod.Start, "2026-08-18T12:00:00") {
		t.Errorf("period start = %q, want a converted ISO timestamp", e.ActualPeriod.Start)
	}
	if !strings.Contains(e.ActualPeriod.Start, "-05:00") {
		t.Errorf("the offset from the v2 timestamp was lost: %q", e.ActualPeriod.Start)
	}

	// The whole bundle must validate, which is the real test of the conversion.
	result := fhir.Validate(e, fhir.R5)
	for _, f := range result.Findings {
		if f.Severity == fhir.Error {
			t.Errorf("converted encounter is invalid: %s at %s", f.Message, f.Path)
		}
	}
}

func TestTimestampWithoutOffsetIsReported(t *testing.T) {
	// v2 permits a local time with no offset and FHIR does not, so something has
	// to supply one. Saying which was applied is the difference between a
	// conversion a clinician can trust and one they cannot.
	res := convert(t, adtWithoutOffsets(), Options{Timezone: time.UTC})

	var reported bool
	for _, n := range res.Notes {
		if strings.Contains(n.Message, "had no timezone") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("a timestamp with no offset was converted silently: %+v", res.Notes)
	}
}

func TestConvertedBundleValidates(t *testing.T) {
	for name, raw := range map[string]string{"ADT^A01": adtA01, "ORU^R01": oruR01} {
		res := convert(t, raw, Options{
			DefaultIdentifierSystem: "urn:oid:1.2.3.4",
			AssigningAuthoritySystems: map[string]string{
				"SITEA": "http://sitea.example.org/mrn",
			},
		})

		for _, version := range []fhir.Version{fhir.R4, fhir.R5} {
			result := fhir.Validate(res.Bundle, version)
			for _, f := range result.Findings {
				if f.Severity == fhir.Error {
					t.Errorf("%s in %s: %s at %s", name, version.Name(), f.Message, f.Path)
				}
			}
		}
	}
}

func TestDeterministicIDs(t *testing.T) {
	// A random id per conversion turns every retry into a new resource, and a feed
	// that retries produces duplicates somebody then merges by hand.
	first := convert(t, adtA01, Options{})
	second := convert(t, adtA01, Options{})

	firstPatient := find[*fhir.Patient](t, first)
	secondPatient := find[*fhir.Patient](t, second)

	if firstPatient.ID != secondPatient.ID {
		t.Errorf("converting the same message twice gave different patient ids: %q and %q",
			firstPatient.ID, secondPatient.ID)
	}

	firstEnc := find[*fhir.Encounter](t, first)
	secondEnc := find[*fhir.Encounter](t, second)
	if firstEnc.ID != secondEnc.ID {
		t.Errorf("encounter ids differ between runs: %q and %q", firstEnc.ID, secondEnc.ID)
	}
}

func TestTransactionEntriesUpsert(t *testing.T) {
	// Without a conditional upsert every A08 creates another copy of the patient.
	res := convert(t, adtA01, Options{
		AssigningAuthoritySystems: map[string]string{"SITEA": "http://sitea.example.org/mrn"},
	})

	var patientEntry *fhir.BundleEntry
	for i := range res.Bundle.Entry {
		if res.Bundle.Entry[i].Resource.ResourceTypeName() == "Patient" {
			patientEntry = &res.Bundle.Entry[i]
		}
	}
	if patientEntry == nil {
		t.Fatal("no patient entry")
	}
	if patientEntry.Request == nil {
		t.Fatal("the patient entry has no request, so the server has no instruction")
	}
	if patientEntry.Request.Method != "PUT" {
		t.Errorf("method = %q, want PUT so a repeat message updates rather than duplicates",
			patientEntry.Request.Method)
	}
	if !strings.Contains(patientEntry.Request.IfNoneExist, "identifier=") {
		t.Errorf("no conditional match on identifier: %q", patientEntry.Request.IfNoneExist)
	}
	if patientEntry.FullURL == "" {
		t.Error("no fullUrl, so references between entries cannot be resolved")
	}
}

func TestVersionDifferencesInOutput(t *testing.T) {
	res := convert(t, adtA01, Options{})
	e := find[*fhir.Encounter](t, res)

	r5, err := fhir.Marshal(e, fhir.R5)
	if err != nil {
		t.Fatal(err)
	}
	r4, err := fhir.Marshal(e, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}

	var r5Tree, r4Tree map[string]any
	if err := json.Unmarshal(r5, &r5Tree); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r4, &r4Tree); err != nil {
		t.Fatal(err)
	}

	// R5 calls it actualPeriod, R4 calls it period. Emitting the wrong one gives a
	// resource that validates structurally and silently loses the visit dates.
	if _, ok := r5Tree["actualPeriod"]; !ok {
		t.Error("R5 output has no actualPeriod")
	}
	if _, ok := r4Tree["period"]; !ok {
		t.Error("R4 output has no period")
	}
	if _, ok := r4Tree["actualPeriod"]; ok {
		t.Error("R4 output still uses the R5 field name")
	}

	// R4's class is a single Coding; R5's is an array of CodeableConcept.
	if _, ok := r5Tree["class"].([]any); !ok {
		t.Errorf("R5 class is not an array: %T", r5Tree["class"])
	}
	if _, ok := r4Tree["class"].(map[string]any); !ok {
		t.Errorf("R4 class is not a single Coding: %T", r4Tree["class"])
	}
}

func TestR4StatusMapping(t *testing.T) {
	// R5 replaced several encounter status codes. Emitting an R5 code in an R4
	// resource fails a required binding.
	res := convert(t, adtWithEvent("A03"), Options{})
	e := find[*fhir.Encounter](t, res)

	if e.Status != "completed" {
		t.Fatalf("R5 status = %q, want completed", e.Status)
	}

	r4, err := fhir.Marshal(e, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(r4, &tree); err != nil {
		t.Fatal(err)
	}
	if tree["status"] != "finished" {
		t.Errorf("R4 status = %v, want finished (R5's completed has no R4 equivalent by that name)",
			tree["status"])
	}
}

func TestUnmappedMessageTypeStillProducesPatient(t *testing.T) {
	res := convert(t, message(
		segment("MSH", mshFields("SIU^S12", "CTRL9", "20260818120000-0500")),
		segment("PID", map[int]string{1: "1", 3: "MRN1^^^SITEA^MR", 5: "Doe^Jane"}),
	), Options{})

	if len(findAll[*fhir.Patient](res)) != 1 {
		t.Error("an unmapped message type produced no patient at all")
	}
	var warned bool
	for _, n := range res.Notes {
		if strings.Contains(n.Message, "is not mapped") {
			warned = true
		}
	}
	if !warned {
		t.Error("an unmapped message type produced no warning")
	}
}

func TestMessageWithNoTypeIsRejected(t *testing.T) {
	m := mustParse(t, "MSH|^~\\&|A|B|C|D|20260818||^|CTRL|P|2.5.1\rPID|1||MRN1\r")
	if _, err := Convert(m, Options{}); err == nil {
		t.Error("a message with no type in MSH-9 was converted anyway")
	}
}

func TestUSCoreProfileOnlyWhenAsked(t *testing.T) {
	// Asserting a profile that has not been checked is worse than asserting none.
	plain := convert(t, adtA01, Options{})
	if p := find[*fhir.Patient](t, plain); p.Meta != nil && len(p.Meta.Profile) > 0 {
		t.Errorf("a profile was claimed without being asked for: %v", p.Meta.Profile)
	}

	claimed := convert(t, adtA01, Options{ClaimUSCore: true})
	p := find[*fhir.Patient](t, claimed)
	if p.Meta == nil || len(p.Meta.Profile) == 0 {
		t.Fatal("ClaimUSCore did not add a profile")
	}
	if !strings.Contains(p.Meta.Profile[0], "us-core-patient") {
		t.Errorf("profile = %v", p.Meta.Profile)
	}
}

func TestJSONOutputIsValidJSON(t *testing.T) {
	res := convert(t, oruR01, Options{})
	raw, err := res.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("the bundle is not valid JSON: %v", err)
	}
	if tree["resourceType"] != "Bundle" {
		t.Errorf("resourceType = %v", tree["resourceType"])
	}
	entries, ok := tree["entry"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatal("no entries in the serialised bundle")
	}

	// Every entry must carry its resourceType, or a server cannot dispatch it.
	for i, e := range entries {
		entry, _ := e.(map[string]any)
		resource, _ := entry["resource"].(map[string]any)
		if resource == nil || resource["resourceType"] == "" {
			t.Errorf("entry %d has no resourceType", i)
		}
	}
}

func TestNotesAreActionable(t *testing.T) {
	// A note that does not say where it came from cannot be acted on.
	res := convert(t, oruR01, Options{})
	if len(res.Notes) == 0 {
		t.Skip("no notes were produced for this message")
	}
	for i, n := range res.Notes {
		if n.Message == "" {
			t.Errorf("note %d has no message", i)
		}
		if n.Source == "" && n.Target == "" {
			t.Errorf("note %d (%q) names neither a source nor a target", i, n.Message)
		}
		switch n.Severity {
		case "info", "warning", "error":
		default:
			t.Errorf("note %d has severity %q", i, n.Severity)
		}
	}
}
