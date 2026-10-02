package fhir

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarshalVersioned_MedicationRequest_R4(t *testing.T) {
	mr := &MedicationRequest{
		Status: "active",
		Intent: "order",
		MedicationCodeableConcept: &CodeableConcept{
			Coding: []Coding{{System: SystemRxNorm, Code: "860975", Display: "amoxicillin 250 MG"}},
			Text:   "amoxicillin 250 MG",
		},
	}
	mr.SetResourceID("mr1")

	raw, err := MarshalVersioned(mr, R4)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// R4: medication is expressed as medicationCodeableConcept
	if !strings.Contains(body, "medicationCodeableConcept") {
		t.Errorf("R4 output missing medicationCodeableConcept:\n%s", body)
	}
	// R4 must NOT have the R5 "medication" as a bare key
	if strings.Contains(body, `"medication"`) && !strings.Contains(body, `"medicationCodeableConcept"`) {
		t.Errorf("R4 output should not have bare \"medication\" key:\n%s", body)
	}
}

func TestMarshalVersioned_MedicationRequest_R5(t *testing.T) {
	mr := &MedicationRequest{
		Status: "active",
		Intent: "order",
		MedicationCodeableConcept: &CodeableConcept{
			Coding: []Coding{{System: SystemRxNorm, Code: "860975", Display: "amoxicillin 250 MG"}},
			Text:   "amoxicillin 250 MG",
		},
	}
	mr.SetResourceID("mr1")

	raw, err := MarshalVersioned(mr, R5)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// R5: medication is a CodeableReference, so the key is "medication"
	if !strings.Contains(body, `"medication"`) {
		t.Errorf("R5 output missing \"medication\" (CodeableReference):\n%s", body)
	}
	// R5 must NOT have the R4 choice-type fields
	if strings.Contains(body, "medicationCodeableConcept") {
		t.Errorf("R5 output should not have medicationCodeableConcept:\n%s", body)
	}
	if strings.Contains(body, "medicationReference") {
		t.Errorf("R5 output should not have medicationReference:\n%s", body)
	}

	// Verify the medication is a CodeableReference with concept
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	med, ok := tree["medication"].(map[string]any)
	if !ok {
		t.Fatalf("medication is not an object: %v", tree["medication"])
	}
	if _, ok := med["concept"]; !ok {
		t.Errorf("medication CodeableReference missing concept field: %v", med)
	}
}

func TestMarshalVersioned_MedicationRequest_R5_Reference(t *testing.T) {
	mr := &MedicationRequest{
		Status:              "active",
		Intent:              "order",
		MedicationReference: &Reference{Reference: "Medication/med1"},
	}
	mr.SetResourceID("mr2")

	raw, err := MarshalVersioned(mr, R5)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	med, ok := tree["medication"].(map[string]any)
	if !ok {
		t.Fatalf("medication is not an object: %v", tree["medication"])
	}
	if _, ok := med["reference"]; !ok {
		t.Errorf("medication CodeableReference missing reference field: %v", med)
	}
}

func TestMarshalVersioned_Procedure_R4(t *testing.T) {
	proc := &Procedure{
		Status:            "completed",
		PerformedDateTime: "2026-08-22T14:32:00Z",
		ReasonCode:        []CodeableConcept{{Text: "suspected fracture"}},
	}
	proc.SetResourceID("proc1")

	raw, err := MarshalVersioned(proc, R4)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	if !strings.Contains(body, "performedDateTime") {
		t.Errorf("R4 output missing performedDateTime:\n%s", body)
	}
	if strings.Contains(body, "occurrenceDateTime") {
		t.Errorf("R4 output should not have occurrenceDateTime:\n%s", body)
	}
	if !strings.Contains(body, "reasonCode") {
		t.Errorf("R4 output missing reasonCode:\n%s", body)
	}
}

func TestMarshalVersioned_Procedure_R5(t *testing.T) {
	proc := &Procedure{
		Status:            "completed",
		PerformedDateTime: "2026-08-22T14:32:00Z",
		ReasonCode:        []CodeableConcept{{Text: "suspected fracture"}},
	}
	proc.SetResourceID("proc1")

	raw, err := MarshalVersioned(proc, R5)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// R5: performed becomes occurrence
	if !strings.Contains(body, "occurrenceDateTime") {
		t.Errorf("R5 output missing occurrenceDateTime:\n%s", body)
	}
	if strings.Contains(body, "performedDateTime") {
		t.Errorf("R5 output should not have performedDateTime:\n%s", body)
	}

	// R5: reasonCode becomes reason (CodeableReference)
	if !strings.Contains(body, `"reason"`) {
		t.Errorf("R5 output missing \"reason\":\n%s", body)
	}
	if strings.Contains(body, "reasonCode") {
		t.Errorf("R5 output should not have reasonCode:\n%s", body)
	}

	// Verify reason is CodeableReference with concept
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	reasons, ok := tree["reason"].([]any)
	if !ok || len(reasons) == 0 {
		t.Fatalf("reason is not a non-empty array: %v", tree["reason"])
	}
	first, ok := reasons[0].(map[string]any)
	if !ok {
		t.Fatal("reason[0] is not an object")
	}
	if _, ok := first["concept"]; !ok {
		t.Errorf("reason[0] missing concept: %v", first)
	}
}

func TestMarshalVersioned_AllergyIntolerance_R4(t *testing.T) {
	ai := &AllergyIntolerance{
		Type: "allergy",
		Code: &CodeableConcept{Text: "peanut"},
	}
	ai.SetResourceID("ai1")

	raw, err := MarshalVersioned(ai, R4)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}

	// R4: type is a string code
	if _, ok := tree["type"].(string); !ok {
		t.Errorf("R4 AllergyIntolerance.type should be a string, got %T: %v", tree["type"], tree["type"])
	}
}

func TestMarshalVersioned_AllergyIntolerance_R5(t *testing.T) {
	ai := &AllergyIntolerance{
		Type: "allergy",
		Code: &CodeableConcept{Text: "peanut"},
	}
	ai.SetResourceID("ai1")

	raw, err := MarshalVersioned(ai, R5)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}

	// R5: type is a CodeableConcept
	typeVal, ok := tree["type"].(map[string]any)
	if !ok {
		t.Fatalf("R5 AllergyIntolerance.type should be a CodeableConcept (map), got %T: %v", tree["type"], tree["type"])
	}
	coding, ok := typeVal["coding"].([]any)
	if !ok || len(coding) == 0 {
		t.Errorf("R5 AllergyIntolerance.type.coding missing or empty: %v", typeVal)
	}
}

func TestNegotiateVersion_AcceptHeader(t *testing.T) {
	cases := []struct {
		accept   string
		want     Version
		default_ Version
	}{
		{"application/fhir+json; fhirVersion=5.0.0", R5, R4},
		{"application/fhir+json; fhirVersion=4.0.1", R4, R5},
		{"application/fhir+json", R4, R4},
		{"", R4, R4},
		{"application/fhir+json; fhirVersion=R5", R5, R4},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/Patient/1", nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		got := NegotiateVersion(req, c.default_)
		if got != c.want {
			t.Errorf("Accept=%q default=%s: got %s, want %s", c.accept, c.default_, got, c.want)
		}
	}
}

func TestNegotiateVersion_FormatParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Patient/1?_format=application%2Ffhir%2Bjson%3BfhirVersion%3D5.0.0", nil)
	got := NegotiateVersion(req, R4)
	if got != R5 {
		t.Errorf("_format param: got %s, want R5", got)
	}
}

func TestNegotiateVersion_AcceptTakesPrecedence(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Patient/1?_format=application/fhir+json;+fhirVersion=4.0.1", nil)
	req.Header.Set("Accept", "application/fhir+json; fhirVersion=5.0.0")
	got := NegotiateVersion(req, R4)
	if got != R5 {
		t.Errorf("Accept should take precedence over _format: got %s, want R5", got)
	}
}

// The medication-carrying resources other than MedicationRequest hold their medication in a
// CodeableReference. R5 names that field "medication"; R4 has no such datatype and splits it
// into medicationCodeableConcept and medicationReference.
//
// These tests exist because the field was serialised as "medicationCodeableReference", which is
// not a field name in either release. A client asking for R4 looked for medicationCodeableConcept
// and a client asking for R5 looked for medication; both found neither and rendered an empty
// medication list, which in a clinical application is indistinguishable from a patient who takes
// no medications. That is the same defect shape as the R4/R5 mismatch this file was written for.
func TestMedicationResourcesUseSpecFieldNamesInR5(t *testing.T) {
	cases := []struct {
		name     string
		resource Resource
	}{
		{"MedicationStatement", &MedicationStatement{
			Medication: &CodeableReference{Concept: &CodeableConcept{Text: "aspirin"}},
		}},
		{"MedicationDispense", &MedicationDispense{
			Medication: &CodeableReference{Concept: &CodeableConcept{Text: "aspirin"}},
		}},
		{"MedicationAdministration", &MedicationAdministration{
			Medication: &CodeableReference{Concept: &CodeableConcept{Text: "aspirin"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.resource.SetResourceID("x1")

			data, err := MarshalVersioned(tc.resource, R5)
			if err != nil {
				t.Fatal(err)
			}
			var tree map[string]any
			if err := json.Unmarshal(data, &tree); err != nil {
				t.Fatal(err)
			}

			if _, wrong := tree["medicationCodeableReference"]; wrong {
				t.Errorf("R5 output carries medicationCodeableReference, which is not a FHIR field name: %s", data)
			}
			med, ok := tree["medication"].(map[string]any)
			if !ok {
				t.Fatalf("R5 output has no medication field: %s", data)
			}
			if _, ok := med["concept"]; !ok {
				t.Errorf("R5 medication is not a CodeableReference with concept: %s", data)
			}
		})
	}
}

func TestMedicationResourcesFlattenToR4(t *testing.T) {
	t.Run("concept becomes medicationCodeableConcept", func(t *testing.T) {
		res := &MedicationStatement{
			Medication: &CodeableReference{Concept: &CodeableConcept{Text: "aspirin"}},
		}
		res.SetResourceID("x1")

		data, err := MarshalVersioned(res, R4)
		if err != nil {
			t.Fatal(err)
		}
		var tree map[string]any
		if err := json.Unmarshal(data, &tree); err != nil {
			t.Fatal(err)
		}

		if _, wrong := tree["medicationCodeableReference"]; wrong {
			t.Errorf("R4 output carries medicationCodeableReference: %s", data)
		}
		if _, wrong := tree["medication"]; wrong {
			t.Errorf("R4 has no CodeableReference datatype, so medication must not appear: %s", data)
		}
		cc, ok := tree["medicationCodeableConcept"].(map[string]any)
		if !ok {
			t.Fatalf("R4 output has no medicationCodeableConcept: %s", data)
		}
		if cc["text"] != "aspirin" {
			t.Errorf("medicationCodeableConcept lost its value: %s", data)
		}
	})

	t.Run("reference becomes medicationReference", func(t *testing.T) {
		res := &MedicationAdministration{
			Medication: &CodeableReference{Reference: &Reference{Reference: "Medication/m1"}},
		}
		res.SetResourceID("x1")

		data, err := MarshalVersioned(res, R4)
		if err != nil {
			t.Fatal(err)
		}
		var tree map[string]any
		if err := json.Unmarshal(data, &tree); err != nil {
			t.Fatal(err)
		}

		ref, ok := tree["medicationReference"].(map[string]any)
		if !ok {
			t.Fatalf("R4 output has no medicationReference: %s", data)
		}
		if ref["reference"] != "Medication/m1" {
			t.Errorf("medicationReference lost its value: %s", data)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// DEFECT 1: Medication.ingredient.item uses invented tag "itemCodeableReference"
//
// FHIR R4 Medication.ingredient.item[x] is a choice type with two valid concrete
// field names: "itemCodeableConcept" (CodeableConcept) and "itemReference" (Reference).
// "itemCodeableReference" exists in NO FHIR release. CodeableReference is an R5 datatype
// that does not exist in R4.
//
// Result: an R4 client looking for itemCodeableConcept finds nothing.
// An R5 client looking for item (R5 renamed it) finds nothing.
// Both see a compound medication with no ingredients - a silent wrong answer.
// ──────────────────────────────────────────────────────────────────────────────

func TestMedicationIngredientSerializesValidR4FieldName(t *testing.T) {
	med := &Medication{
		Code: &CodeableConcept{Text: "compound cream"},
		Ingredient: []MedIngredient{
			{
				Item:     &CodeableReference{Concept: &CodeableConcept{Text: "hydrocortisone"}},
				IsActive: Bool(true),
			},
		},
	}
	med.SetResourceID("med1")

	data, err := MarshalVersioned(med, R4)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}

	ingredients, ok := tree["ingredient"].([]any)
	if !ok || len(ingredients) == 0 {
		t.Fatalf("R4 Medication has no ingredient array: %s", data)
	}
	first, ok := ingredients[0].(map[string]any)
	if !ok {
		t.Fatal("ingredient[0] is not an object")
	}

	// The field MUST be itemCodeableConcept - the R4 choice type field.
	// It MUST NOT be itemCodeableReference (invented, exists in no release).
	if _, bad := first["itemCodeableReference"]; bad {
		t.Errorf("DEFECT: R4 output has itemCodeableReference which is not a FHIR field name: %s", data)
	}
	if _, ok := first["itemCodeableConcept"]; !ok {
		t.Errorf("DEFECT: R4 output missing itemCodeableConcept (the real R4 field): %s", data)
	}
}

func TestMedicationIngredientReferenceSerializesToR4(t *testing.T) {
	med := &Medication{
		Code: &CodeableConcept{Text: "compound cream"},
		Ingredient: []MedIngredient{
			{
				Item: &CodeableReference{Reference: &Reference{Reference: "Substance/sub1"}},
			},
		},
	}
	med.SetResourceID("med2")

	data, err := MarshalVersioned(med, R4)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}

	ingredients, ok := tree["ingredient"].([]any)
	if !ok || len(ingredients) == 0 {
		t.Fatalf("R4 Medication has no ingredient array: %s", data)
	}
	first, ok := ingredients[0].(map[string]any)
	if !ok {
		t.Fatal("ingredient[0] is not an object")
	}

	// When reference is populated, the R4 field is itemReference.
	if _, bad := first["itemCodeableReference"]; bad {
		t.Errorf("DEFECT: R4 output has itemCodeableReference: %s", data)
	}
	if _, ok := first["itemReference"]; !ok {
		t.Errorf("DEFECT: R4 output missing itemReference (the real R4 field): %s", data)
	}
}

// Verify R5 output for Medication ingredient uses "item" with CodeableReference shape.
func TestMedicationIngredientSerializesR5Correctly(t *testing.T) {
	med := &Medication{
		Code: &CodeableConcept{Text: "compound cream"},
		Ingredient: []MedIngredient{
			{
				Item:     &CodeableReference{Concept: &CodeableConcept{Text: "hydrocortisone"}},
				IsActive: Bool(true),
			},
		},
	}
	med.SetResourceID("med3")

	data, err := MarshalVersioned(med, R5)
	if err != nil {
		t.Fatal(err)
	}

	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}

	ingredients, ok := tree["ingredient"].([]any)
	if !ok || len(ingredients) == 0 {
		t.Fatalf("R5 Medication has no ingredient array: %s", data)
	}
	first, ok := ingredients[0].(map[string]any)
	if !ok {
		t.Fatal("ingredient[0] is not an object")
	}

	// R5 uses "item" as a CodeableReference.
	item, ok := first["item"].(map[string]any)
	if !ok {
		t.Fatalf("R5 output missing item (CodeableReference): %s", data)
	}
	if _, ok := item["concept"]; !ok {
		t.Errorf("R5 item is not a CodeableReference with concept: %s", data)
	}
	// Must NOT have the R4 choice fields
	if _, bad := first["itemCodeableConcept"]; bad {
		t.Errorf("R5 output has R4 field itemCodeableConcept: %s", data)
	}
	if _, bad := first["itemCodeableReference"]; bad {
		t.Errorf("R5 output has invented field itemCodeableReference: %s", data)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DEFECT 2: NegotiateVersion silently falls back when an unsupported version is
// explicitly requested. A client that sends Accept: application/fhir+json;
// fhirVersion=6.0.0 is making an explicit demand. Silently serving R4 is the
// same defect class as serving R5 field names to an R4 client: a confident wrong
// answer. The correct behaviour is to return an error (or a non-valid Version
// the caller treats as error), not to silently succeed with the fallback.
// ──────────────────────────────────────────────────────────────────────────────

func TestNegotiateVersion_UnsupportedExplicitVersionIsNotSilentFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Patient/1", nil)
	req.Header.Set("Accept", "application/fhir+json; fhirVersion=6.0.0")

	got := NegotiateVersion(req, R4)

	// If the implementation silently returns R4 (the fallback), that's the defect.
	// A correct implementation should either:
	// (a) return a non-Valid version that the caller rejects, OR
	// (b) return an error (if the signature changes), OR
	// (c) return a sentinel that causes an error response.
	//
	// Currently it returns R4 (the fallback), which is wrong.
	if got == R4 {
		t.Errorf("DEFECT: client explicitly requested fhirVersion=6.0.0 but got silent fallback to %s; "+
			"an unsupported explicit version request must not silently succeed", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Round-trip test: upgrade-to-R5-then-downgrade-to-R4 must reproduce the R4 output.
// If it doesn't, one of the conversions is wrong.
// ──────────────────────────────────────────────────────────────────────────────

func TestVersionRoundTrip_R4_R5_R4(t *testing.T) {
	// Resources whose structs store data in CodeableReference form (R5 internal shape)
	// CAN round-trip through R5 output → UnmarshalResource → R4 output, because
	// UnmarshalResource populates the CodeableReference field from the R5 wire format.
	//
	// Resources whose structs hold R4-shaped fields (MedicationRequest, Procedure) cannot
	// round-trip this way by design: upgradeToR5 renames fields at the wire level, and
	// UnmarshalResource reads into R4-shaped struct fields. Those are tested for
	// non-mutation (stability) instead.
	cases := []struct {
		name     string
		resource Resource
	}{
		{"MedicationStatement with concept", &MedicationStatement{
			Status:     "active",
			Medication: &CodeableReference{Concept: &CodeableConcept{Text: "aspirin"}},
		}},
		{"Medication with ingredient", &Medication{
			Code: &CodeableConcept{Text: "compound cream"},
			Ingredient: []MedIngredient{
				{Item: &CodeableReference{Concept: &CodeableConcept{Text: "hydrocortisone"}}},
			},
		}},
		{"MedicationDispense with reference", &MedicationDispense{
			Status:     "completed",
			Medication: &CodeableReference{Reference: &Reference{Reference: "Medication/m1"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.resource.SetResourceID("rt1")

			// Get canonical R4 output.
			r4Out, err := MarshalVersioned(tc.resource, R4)
			if err != nil {
				t.Fatal(err)
			}

			// Get R5 output, then parse and re-marshal as R4.
			r5Out, err := MarshalVersioned(tc.resource, R5)
			if err != nil {
				t.Fatal(err)
			}

			// Parse R5 output, create a new resource, marshal as R4.
			res2, err := UnmarshalResource(r5Out)
			if err != nil {
				t.Fatalf("failed to unmarshal R5 output: %v\nR5 output: %s", err, r5Out)
			}

			r4Out2, err := MarshalVersioned(res2, R4)
			if err != nil {
				t.Fatal(err)
			}

			// Compare - they should be equivalent (modulo key ordering).
			var tree1, tree2 map[string]any
			if err := json.Unmarshal(r4Out, &tree1); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(r4Out2, &tree2); err != nil {
				t.Fatal(err)
			}

			// Re-marshal both to get consistent key order for comparison.
			norm1, _ := json.Marshal(tree1)
			norm2, _ := json.Marshal(tree2)
			if string(norm1) != string(norm2) {
				t.Errorf("round-trip mismatch:\noriginal R4: %s\nround-trip:  %s", norm1, norm2)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Verify MarshalVersioned does not mutate the resource in place.
// ──────────────────────────────────────────────────────────────────────────────

func TestMarshalVersionedDoesNotMutateResource(t *testing.T) {
	mr := &MedicationRequest{
		Status: "active",
		Intent: "order",
		MedicationCodeableConcept: &CodeableConcept{
			Coding: []Coding{{System: SystemRxNorm, Code: "860975", Display: "amoxicillin"}},
			Text:   "amoxicillin",
		},
	}
	mr.SetResourceID("mr-mut")

	// Marshal as R5 (which upgrades medication fields).
	out1, err := MarshalVersioned(mr, R5)
	if err != nil {
		t.Fatal(err)
	}

	// Marshal again - should produce identical output.
	out2, err := MarshalVersioned(mr, R5)
	if err != nil {
		t.Fatal(err)
	}

	if string(out1) != string(out2) {
		t.Errorf("second marshal produced different output (mutation detected):\nfirst:  %s\nsecond: %s", out1, out2)
	}

	// Also verify the original struct's field is still populated.
	if mr.MedicationCodeableConcept == nil {
		t.Error("MedicationCodeableConcept was mutated to nil by MarshalVersioned")
	}
}
