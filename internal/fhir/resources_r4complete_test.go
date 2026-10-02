package fhir

import (
	"sort"
	"testing"
)

// r4ResourceTypes is every resource type defined by FHIR R4 (4.0.1).
//
// Written out rather than derived, because there is nothing to derive it from. The specification's resource list is an HTML page,
// and a test that fetched it would fail when the network was down and pass when HL7 reorganised the page. A hand-written list is
// checkable by a person against http://hl7.org/fhir/R4/resourcelist.html and changes only when the specification does, which for
// a published normative release is never.
//
// R4 rather than R5 because R4 is DefaultVersion and what US Core and the CMS rules consume. R5 adds types this list does not
// name; when a channel needs one, it goes in a second list rather than being quietly added to this one, because "complete for R4"
// and "complete for R5" are different claims and a test that blurs them cannot support either.
var r4ResourceTypes = []string{
	"Account", "ActivityDefinition", "AdverseEvent", "AllergyIntolerance", "Appointment",
	"AppointmentResponse", "AuditEvent", "Basic", "Binary", "BiologicallyDerivedProduct",
	"BodyStructure", "Bundle", "CapabilityStatement", "CarePlan", "CareTeam", "CatalogEntry",
	"ChargeItem", "ChargeItemDefinition", "Claim", "ClaimResponse", "ClinicalImpression",
	"CodeSystem", "Communication", "CommunicationRequest", "CompartmentDefinition", "Composition",
	"ConceptMap", "Condition", "Consent", "Contract", "Coverage", "CoverageEligibilityRequest",
	"CoverageEligibilityResponse", "DetectedIssue", "Device", "DeviceDefinition", "DeviceMetric",
	"DeviceRequest", "DeviceUseStatement", "DiagnosticReport", "DocumentManifest",
	"DocumentReference", "EffectEvidenceSynthesis", "Encounter", "Endpoint", "EnrollmentRequest",
	"EnrollmentResponse", "EpisodeOfCare", "EventDefinition", "Evidence", "EvidenceVariable",
	"ExampleScenario", "ExplanationOfBenefit", "FamilyMemberHistory", "Flag", "Goal",
	"GraphDefinition", "Group", "GuidanceResponse", "HealthcareService", "ImagingStudy",
	"Immunization", "ImmunizationEvaluation", "ImmunizationRecommendation", "ImplementationGuide",
	"InsurancePlan", "Invoice", "Library", "Linkage", "List", "Location", "Measure", "MeasureReport",
	"Media", "Medication", "MedicationAdministration", "MedicationDispense", "MedicationKnowledge",
	"MedicationRequest", "MedicationStatement", "MedicinalProduct", "MedicinalProductAuthorization",
	"MedicinalProductContraindication", "MedicinalProductIndication", "MedicinalProductIngredient",
	"MedicinalProductInteraction", "MedicinalProductManufactured", "MedicinalProductPackaged",
	"MedicinalProductPharmaceutical", "MedicinalProductUndesirableEffect", "MessageDefinition",
	"MessageHeader", "MolecularSequence", "NamingSystem", "NutritionOrder", "Observation",
	"ObservationDefinition", "OperationDefinition", "OperationOutcome", "Organization",
	"OrganizationAffiliation", "Parameters", "Patient", "PaymentNotice", "PaymentReconciliation",
	"Person", "PlanDefinition", "Practitioner", "PractitionerRole", "Procedure", "Provenance",
	"Questionnaire", "QuestionnaireResponse", "RelatedPerson", "RequestGroup", "ResearchDefinition",
	"ResearchElementDefinition", "ResearchStudy", "ResearchSubject", "RiskAssessment",
	"RiskEvidenceSynthesis", "Schedule", "SearchParameter", "ServiceRequest", "Slot", "Specimen",
	"SpecimenDefinition", "StructureDefinition", "StructureMap", "Subscription", "Substance",
	"SubstanceNucleicAcid", "SubstancePolymer", "SubstanceProtein", "SubstanceReferenceInformation",
	"SubstanceSourceMaterial", "SubstanceSpecification", "SupplyDelivery", "SupplyRequest", "Task",
	"TerminologyCapabilities", "TestReport", "TestScript", "ValueSet", "VerificationResult",
	"VisionPrescription",
}

// notStoredButImplemented are the R4 types that exist as Go types and deliberately have no storage.
//
// Each needs a reason, because this map is the only way for a resource to be absent from the registry and still pass. An entry
// added without a reason turns a completeness test back into the list of excuses it replaced.
var notStoredButImplemented = map[string]string{
	"ValueSet":   "projected from the channel mapping tables rather than stored, so it has operation routes and no storage - see TestTheProjectedTerminologyTypesAreNotStored",
	"ConceptMap": "projected from the channel mapping tables rather than stored, for the same reason as ValueSet",
	"Parameters": "the input and output wrapper for FHIR operations, never itself persisted - a POST of one is a call, not a create",
}

// TestEveryR4ResourceTypeIsAccountedFor is the completeness guard.
//
// # What it is for
//
// Coverage used to be whatever had been needed so far, and there was no way to answer "can Perfuse accept this resource type?"
// except by trying it. Nineteen types were missing, almost all of them drug regulatory and substance chemistry data that never
// travels in clinical traffic - a defensible gap, but one nobody could see the edge of.
//
// This test makes the claim checkable in one place: every R4 resource type either has a constructor, so the server can accept,
// store and return it, or is named above with a reason it has none. There is no third state, which is the point. A type cannot
// be quietly missing.
//
// # Why this is not a count
//
// An earlier version of this idea compared totals, and a count answers the wrong question - 127 of 146 sounds close to complete
// and would still be useless if the nineteen absent were Patient, Observation and MedicationRequest. Naming every type means the
// failure message says which one, and the sibling test TestTheClinicallyEssentialTypesArePresent keeps guarding the stronger
// property that the types a real feed carries are present.
func TestEveryR4ResourceTypeIsAccountedFor(t *testing.T) {
	for _, name := range r4ResourceTypes {
		t.Run(name, func(t *testing.T) {
			_, hasConstructor := resourceConstructors[name]
			reason, deliberate := notStoredButImplemented[name]

			switch {
			case hasConstructor && deliberate:
				t.Errorf("%s has a constructor and is also listed as not stored (%q). One or the other is wrong: "+
					"either it is stored, and the entry should be removed, or it is not, and the constructor makes the "+
					"capability statement advertise storage that does not exist", name, reason)
			case !hasConstructor && !deliberate:
				t.Errorf("%s is an R4 resource type with no constructor and no recorded reason for having none. "+
					"Add it to resourceConstructors so the server can accept it, or to notStoredButImplemented with "+
					"the reason it cannot be stored", name)
			}
		})
	}
}

// TestNothingIsExemptedThatDoesNotExist stops the exemption map being used to wave away a type that was never written.
//
// Without this, adding a name to notStoredButImplemented would satisfy the guard above whether or not the Go type existed - which
// is the same defect the guard was written to remove, one level up. Every exempted type must be constructible as a Resource, so
// the exemption means "implemented but not stored" and cannot come to mean "not implemented".
func TestNothingIsExemptedThatDoesNotExist(t *testing.T) {
	exempt := map[string]Resource{
		"ValueSet":   &ValueSet{},
		"ConceptMap": &ConceptMap{},
		"Parameters": &Parameters{},
	}

	if len(exempt) != len(notStoredButImplemented) {
		t.Fatalf("%d types are exempted but %d are checked here. Every exemption needs a Go type in this map, "+
			"or an exemption could stand in for a type that was never written", len(notStoredButImplemented), len(exempt))
	}

	for name, r := range exempt {
		if _, ok := notStoredButImplemented[name]; !ok {
			t.Errorf("%s is checked here but is not exempted, so this entry is stale", name)

			continue
		}

		if got := r.ResourceTypeName(); got != name {
			t.Errorf("%s reports its type as %q, so it is not the resource it is exempted as", name, got)
		}
	}
}

// TestTheR4ListIsSortedAndFreeOfDuplicates keeps the list above reviewable.
//
// A person checking it against the specification reads down two columns. Out of order or repeated entries make that impossible,
// and a duplicate would also inflate the apparent coverage while hiding a genuine absence.
func TestTheR4ListIsSortedAndFreeOfDuplicates(t *testing.T) {
	if !sort.StringsAreSorted(r4ResourceTypes) {
		for i := 1; i < len(r4ResourceTypes); i++ {
			if r4ResourceTypes[i-1] > r4ResourceTypes[i] {
				t.Fatalf("not sorted at %d: %q before %q", i, r4ResourceTypes[i-1], r4ResourceTypes[i])
			}
		}
	}

	seen := make(map[string]bool, len(r4ResourceTypes))
	for _, n := range r4ResourceTypes {
		if seen[n] {
			t.Errorf("%s appears twice", n)
		}

		seen[n] = true
	}

	// R4 defines 146 resource types. Stated as a number as well as a list because a deletion is invisible in a list this long:
	// removing a line would silently narrow what the guard checks, and the guard would still pass.
	if len(r4ResourceTypes) != 146 {
		t.Errorf("the list has %d entries, want 146. If R4 genuinely gained a type, this number moves with the list; "+
			"if a line was deleted by accident, restore it", len(r4ResourceTypes))
	}
}
