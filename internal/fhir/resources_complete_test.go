package fhir

import (
	"encoding/json"
	"testing"
)

// TestResourceTypeNameConsistency verifies every resource type defined in
// resources_complete.go returns the correct name from ResourceTypeName().
// This catches copy-paste errors across 87+ resource types.
func TestResourceTypeNameConsistency(t *testing.T) {
	cases := []struct {
		name string
		r    Resource
	}{
		// Foundation — infrastructure
		{"StructureDefinition", &StructureDefinition{}},
		{"CapabilityStatement", &CapabilityStatement{}},
		{"CodeSystem", &CodeSystem{}},
		{"NamingSystem", &NamingSystem{}},
		{"ImplementationGuide", &ImplementationGuide{}},
		{"SearchParameter", &SearchParameter{}},
		{"OperationDefinition", &OperationDefinition{}},
		{"CompartmentDefinition", &CompartmentDefinition{}},
		{"GraphDefinition", &GraphDefinition{}},
		{"StructureMap", &StructureMap{}},
		{"MessageDefinition", &MessageDefinition{}},
		{"MessageHeader", &MessageHeader{}},
		{"Subscription", &Subscription{}},
		{"Binary", &Binary{}},

		// Security
		{"AuditEvent", &AuditEvent{}},

		// Clinical
		{"AdverseEvent", &AdverseEvent{}},
		{"DetectedIssue", &DetectedIssue{}},
		{"ClinicalImpression", &ClinicalImpression{}},
		{"RiskAssessment", &RiskAssessment{}},
		{"BodyStructure", &BodyStructure{}},
		{"Flag", &Flag{}},
		{"List", &List{}},
		{"EpisodeOfCare", &EpisodeOfCare{}},
		{"NutritionOrder", &NutritionOrder{}},
		{"VisionPrescription", &VisionPrescription{}},
		{"DeviceRequest", &DeviceRequest{}},
		{"DeviceUseStatement", &DeviceUseStatement{}},
		{"SupplyRequest", &SupplyRequest{}},
		{"SupplyDelivery", &SupplyDelivery{}},
		{"RequestGroup", &RequestGroup{}},
		{"GuidanceResponse", &GuidanceResponse{}},

		// Diagnostics
		{"ImagingStudy", &ImagingStudy{}},
		{"MolecularSequence", &MolecularSequence{}},

		// Medications
		{"MedicationKnowledge", &MedicationKnowledge{}},
		{"ImmunizationEvaluation", &ImmunizationEvaluation{}},
		{"ImmunizationRecommendation", &ImmunizationRecommendation{}},

		// Financial
		{"ClaimResponse", &ClaimResponse{}},
		{"CoverageEligibilityRequest", &CoverageEligibilityRequest{}},
		{"CoverageEligibilityResponse", &CoverageEligibilityResponse{}},
		{"EnrollmentRequest", &EnrollmentRequest{}},
		{"EnrollmentResponse", &EnrollmentResponse{}},
		{"PaymentNotice", &PaymentNotice{}},
		{"PaymentReconciliation", &PaymentReconciliation{}},
		{"Account", &Account{}},
		{"ChargeItem", &ChargeItem{}},
		{"ChargeItemDefinition", &ChargeItemDefinition{}},
		{"Contract", &Contract{}},
		{"InsurancePlan", &InsurancePlan{}},
		{"Invoice", &Invoice{}},

		// Workflow
		{"Schedule", &Schedule{}},
		{"Slot", &Slot{}},
		{"AppointmentResponse", &AppointmentResponse{}},
		{"PlanDefinition", &PlanDefinition{}},
		{"ActivityDefinition", &ActivityDefinition{}},
		{"EventDefinition", &EventDefinition{}},
		{"Questionnaire", &Questionnaire{}},

		// Research
		{"ResearchStudy", &ResearchStudy{}},
		{"ResearchSubject", &ResearchSubject{}},
		{"Measure", &Measure{}},
		{"MeasureReport", &MeasureReport{}},
		{"Library", &Library{}},
		{"Evidence", &Evidence{}},
		{"EvidenceVariable", &EvidenceVariable{}},
		{"RiskEvidenceSynthesis", &RiskEvidenceSynthesis{}},
		{"EffectEvidenceSynthesis", &EffectEvidenceSynthesis{}},

		// Additional clinical
		{"Endpoint", &Endpoint{}},
		{"HealthcareService", &HealthcareService{}},
		{"Group", &Group{}},
		{"Person", &Person{}},
		{"Linkage", &Linkage{}},
		{"Basic", &Basic{}},
		{"DeviceMetric", &DeviceMetric{}},
		{"DeviceDefinition", &DeviceDefinition{}},
		{"Substance", &Substance{}},
		{"SubstanceSpecification", &SubstanceSpecification{}},
		{"MedicinalProduct", &MedicinalProduct{}},
		{"MedicinalProductAuthorization", &MedicinalProductAuthorization{}},

		// Communication and documentation
		{"CommunicationRequest", &CommunicationRequest{}},
		{"VerificationResult", &VerificationResult{}},
		{"OrganizationAffiliation", &OrganizationAffiliation{}},
		{"CatalogEntry", &Catalog{}}, // Fixed: was returning "Catalog" but R4 resource type is "CatalogEntry"
		{"TestScript", &TestScript{}},
		{"TestReport", &TestReport{}},
		{"TerminologyCapabilities", &TerminologyCapabilities{}},
		{"ExampleScenario", &ExampleScenario{}},
		{"ObservationDefinition", &ObservationDefinition{}},
		{"SpecimenDefinition", &SpecimenDefinition{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.r.ResourceTypeName()
			if got != tc.name {
				t.Errorf("ResourceTypeName() = %q, want %q", got, tc.name)
			}
		})
	}
}

// TestGuidanceResponseIdentifierCardinality verifies GuidanceResponse.Identifier
// is modelled as a slice (0..*) per FHIR R4, not a single pointer which silently
// drops all but one identifier.
func TestGuidanceResponseIdentifierCardinality(t *testing.T) {
	input := `{
		"resourceType": "GuidanceResponse",
		"id": "gr-1",
		"status": "success",
		"identifier": [
			{"system": "urn:a", "value": "id-1"},
			{"system": "urn:b", "value": "id-2"}
		]
	}`
	r, err := UnmarshalResource([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalResource: %v", err)
	}
	gr, ok := r.(*GuidanceResponse)
	if !ok {
		t.Fatalf("type = %T, want *GuidanceResponse", r)
	}
	// In R4, identifier is 0..* — it must be a slice to hold multiple values.
	// The test below will compile only once the field is changed to []Identifier.
	if len(gr.Identifier) != 2 {
		t.Errorf("len(Identifier) = %d, want 2 (cardinality 0..*)", len(gr.Identifier))
	}
}

// survives Marshal → Unmarshal with the resourceType preserved and the
// concrete Go type recovered.
// allCompleteResources returns one zero value of every resource type declared in this file, so
// that a check meant to cover all of them cannot silently cover a subset.
func allCompleteResources() []Resource {
	return []Resource{
		&StructureDefinition{},
		&CapabilityStatement{},
		&CodeSystem{},
		&NamingSystem{},
		&ImplementationGuide{},
		&SearchParameter{},
		&OperationDefinition{},
		&CompartmentDefinition{},
		&GraphDefinition{},
		&StructureMap{},
		&MessageDefinition{},
		&MessageHeader{},
		&Subscription{},
		&Binary{},
		&AuditEvent{},
		&AdverseEvent{},
		&DetectedIssue{},
		&ClinicalImpression{},
		&RiskAssessment{},
		&BodyStructure{},
		&Flag{},
		&List{},
		&EpisodeOfCare{},
		&NutritionOrder{},
		&VisionPrescription{},
		&DeviceRequest{},
		&DeviceUseStatement{},
		&SupplyRequest{},
		&SupplyDelivery{},
		&RequestGroup{},
		&GuidanceResponse{},
		&ImagingStudy{},
		&MolecularSequence{},
		&MedicationKnowledge{},
		&ImmunizationEvaluation{},
		&ImmunizationRecommendation{},
		&ClaimResponse{},
		&CoverageEligibilityRequest{},
		&CoverageEligibilityResponse{},
		&EnrollmentRequest{},
		&EnrollmentResponse{},
		&PaymentNotice{},
		&PaymentReconciliation{},
		&Account{},
		&ChargeItem{},
		&ChargeItemDefinition{},
		&Contract{},
		&InsurancePlan{},
		&Invoice{},
		&Schedule{},
		&Slot{},
		&AppointmentResponse{},
		&PlanDefinition{},
		&ActivityDefinition{},
		&EventDefinition{},
		&Questionnaire{},
		&ResearchStudy{},
		&ResearchSubject{},
		&Measure{},
		&MeasureReport{},
		&Library{},
		&Evidence{},
		&EvidenceVariable{},
		&RiskEvidenceSynthesis{},
		&EffectEvidenceSynthesis{},
		&Endpoint{},
		&HealthcareService{},
		&Group{},
		&Person{},
		&Linkage{},
		&Basic{},
		&DeviceMetric{},
		&DeviceDefinition{},
		&Substance{},
		&SubstanceSpecification{},
		&MedicinalProduct{},
		&MedicinalProductAuthorization{},
		&CommunicationRequest{},
		&VerificationResult{},
		&OrganizationAffiliation{},
		&Catalog{},
		&TestScript{},
		&TestReport{},
		&TerminologyCapabilities{},
		&ExampleScenario{},
		&ObservationDefinition{},
		&SpecimenDefinition{},
	}
}

func TestResourceRoundTrip(t *testing.T) {
	resources := allCompleteResources()

	for _, r := range resources {
		typeName := r.ResourceTypeName()
		t.Run(typeName, func(t *testing.T) {
			// Set an ID so the resource has content
			r.SetResourceID("test-" + typeName)

			data, err := Marshal(r, R4)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", typeName, err)
			}

			// Verify resourceType is in the JSON
			var raw map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("json.Unmarshal raw: %v", err)
			}
			gotType, ok := raw["resourceType"].(string)
			if !ok {
				t.Fatalf("resourceType missing from marshalled %s", typeName)
			}
			if gotType != typeName {
				t.Errorf("resourceType in JSON = %q, want %q", gotType, typeName)
			}

			// Unmarshal and verify type is preserved
			parsed, err := UnmarshalResource(data)
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", typeName, err)
			}
			if parsed.ResourceTypeName() != typeName {
				t.Errorf("after round-trip: ResourceTypeName() = %q, want %q",
					parsed.ResourceTypeName(), typeName)
			}
			if parsed.ResourceID() != "test-"+typeName {
				t.Errorf("after round-trip: ResourceID() = %q, want %q",
					parsed.ResourceID(), "test-"+typeName)
			}
		})
	}
}

// A resource type name has to agree in three places: the type's own ResourceTypeName, the
// constructor registry that turns a name back into a value, and the server's search parameter
// table. Renaming one and not the others is worse than the original error, because the resource
// then cannot be constructed by name at all - it is refused as unimplemented rather than merely
// carrying the wrong name.
func TestEveryResourceTypeNameHasAConstructor(t *testing.T) {
	for _, r := range allCompleteResources() {
		name := r.ResourceTypeName()
		t.Run(name, func(t *testing.T) {
			got, err := UnmarshalResource([]byte(`{"resourceType":"` + name + `","id":"x"}`))
			if err != nil {
				t.Fatalf("%s has no constructor registered under its own name: %v", name, err)
			}
			if got.ResourceTypeName() != name {
				t.Errorf("constructor for %q produced a %q", name, got.ResourceTypeName())
			}
		})
	}
}
