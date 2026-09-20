package fhirserver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Search.
//
// Only the parameters people actually use are supported, and the capability
// statement says which. A server that accepts an unknown search parameter and
// silently ignores it returns the wrong patients, and the client has no way to
// tell — so an unrecognised parameter is an error here.

// indexEntry is one searchable value.
type indexEntry struct {
	param  string
	value  string
	system string

	// refType is the resource type a reference points at, empty when the value is not a reference.
	//
	// Kept beside the id rather than folded into the value, so a query can ask for either. FHIR defines both forms:
	// subject=Patient/123 is type-qualified and subject=123 is not, and a client sends whichever its library builds.
	refType string

	// valueRaw is the value before case folding, kept for the :exact modifier.
	//
	// :exact means no prefix matching and no case folding, so the folded value cannot answer it. Stored beside the folded
	// value rather than instead of it, because folding at query time would put LOWER() around every ordinary string search
	// and turn the common query into a table scan to serve the rare one.
	valueRaw string
}

// SearchParams lists the parameters supported per resource type. This is the same
// list the capability statement advertises, so the two cannot disagree.
var SearchParams = map[string][]string{
	"Patient": {
		"_id", "_lastUpdated", "identifier", "family", "given", "name",
		"birthdate", "gender",
	},
	"Encounter": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "status", "class", "date",
	},
	"Observation": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "encounter",
		"code", "category", "status", "date",
	},
	"DiagnosticReport": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "encounter",
		"code", "category", "status", "date",
	},
	"Practitioner":   {"_id", "_lastUpdated", "identifier", "family", "given", "name"},
	"Organization":   {"_id", "_lastUpdated", "identifier", "name"},
	"Location":       {"_id", "_lastUpdated", "identifier", "name", "status"},
	"Specimen":       {"_id", "_lastUpdated", "identifier", "patient", "subject", "status", "type"},
	"ServiceRequest": {"_id", "_lastUpdated", "identifier", "patient", "subject", "status", "code"},

	// US Core. Each list is the parameters US Core marks as mandatory for that resource, plus the ones this server can
	// support without a new index shape - a parameter advertised and not implemented is worse than one absent, because a
	// client builds a query from the capability statement and gets an error it was told could not happen.
	"Condition": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "encounter",
		"category", "code", "clinical-status", "onset-date", "recorded-date",
	},
	"MedicationRequest": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "encounter",
		"status", "intent", "authoredon",
	},
	"AllergyIntolerance": {
		"_id", "_lastUpdated", "identifier", "patient", "encounter",
		"clinical-status", "code", "criticality", "date",
	},
	"Immunization": {
		"_id", "_lastUpdated", "identifier", "patient", "encounter",
		"status", "vaccine-code", "date",
	},
	"Procedure": {
		"_id", "_lastUpdated", "identifier", "patient", "subject", "encounter",
		"status", "code", "category", "date",
	},
	"DocumentReference": {
		"_id", "_lastUpdated", "identifier", "patient", "subject",
		"status", "type", "category", "date",
	},

	// Additional resource types.
	"Medication":               {"_id", "_lastUpdated", "code", "status"},
	"MedicationStatement":      {"_id", "_lastUpdated", "patient", "subject", "status"},
	"MedicationDispense":       {"_id", "_lastUpdated", "patient", "subject", "status"},
	"MedicationAdministration": {"_id", "_lastUpdated", "patient", "subject", "status"},
	"Coverage":                 {"_id", "_lastUpdated", "patient", "beneficiary", "status"},
	"Claim":                    {"_id", "_lastUpdated", "patient", "status", "use", "created"},
	"ExplanationOfBenefit":     {"_id", "_lastUpdated", "patient", "status", "use", "created"},
	"CarePlan":                 {"_id", "_lastUpdated", "patient", "subject", "encounter", "status", "category"},
	"CareTeam":                 {"_id", "_lastUpdated", "patient", "subject", "encounter", "status"},
	"Goal":                     {"_id", "_lastUpdated", "patient", "subject", "lifecycle-status"},
	"Device":                   {"_id", "_lastUpdated", "identifier", "patient", "type", "status"},
	"RelatedPerson":            {"_id", "_lastUpdated", "identifier", "patient", "name"},
	"PractitionerRole":         {"_id", "_lastUpdated", "identifier", "practitioner", "organization", "specialty"},
	"Appointment":              {"_id", "_lastUpdated", "patient", "status", "date"},
	"Consent":                  {"_id", "_lastUpdated", "patient", "status", "category"},
	"Composition":              {"_id", "_lastUpdated", "patient", "subject", "encounter", "type", "status", "date"},
	"FamilyMemberHistory":      {"_id", "_lastUpdated", "patient", "status"},
	"Communication":            {"_id", "_lastUpdated", "patient", "subject", "encounter", "status"},
	"Task":                     {"_id", "_lastUpdated", "patient", "status", "intent", "code"},
	"Provenance":               {"_id", "_lastUpdated", "target", "recorded"},
	"QuestionnaireResponse":    {"_id", "_lastUpdated", "patient", "subject", "encounter", "questionnaire", "status", "authored"},
	"Media":                    {"_id", "_lastUpdated", "patient", "subject", "encounter", "status", "created"},

	// Foundation & infrastructure
	"StructureDefinition":   {"_id", "_lastUpdated", "url", "name", "status", "type"},
	"CapabilityStatement":   {"_id", "_lastUpdated", "url", "name", "status", "fhirversion"},
	"CodeSystem":            {"_id", "_lastUpdated", "url", "name", "status"},
	"NamingSystem":          {"_id", "_lastUpdated", "name", "status", "kind"},
	"ImplementationGuide":   {"_id", "_lastUpdated", "url", "name", "status"},
	"SearchParameter":       {"_id", "_lastUpdated", "url", "name", "code", "status", "type"},
	"OperationDefinition":   {"_id", "_lastUpdated", "url", "name", "code", "status"},
	"CompartmentDefinition": {"_id", "_lastUpdated", "url", "name", "code", "status"},
	"GraphDefinition":       {"_id", "_lastUpdated", "url", "name", "status"},
	"StructureMap":          {"_id", "_lastUpdated", "url", "name", "status"},
	"MessageDefinition":     {"_id", "_lastUpdated", "url", "name", "status", "category"},
	"MessageHeader":         {"_id", "_lastUpdated"},
	"Subscription":          {"_id", "_lastUpdated", "status", "criteria"},
	"Binary":                {"_id", "_lastUpdated", "contenttype"},

	// Security
	"AuditEvent": {"_id", "_lastUpdated", "action", "outcome", "date"},

	// Clinical
	"AdverseEvent":       {"_id", "_lastUpdated", "subject", "actuality", "date"},
	"DetectedIssue":      {"_id", "_lastUpdated", "identifier", "patient", "status", "severity"},
	"ClinicalImpression": {"_id", "_lastUpdated", "identifier", "subject", "encounter", "status", "date"},
	"RiskAssessment":     {"_id", "_lastUpdated", "identifier", "subject", "encounter", "status"},
	"BodyStructure":      {"_id", "_lastUpdated", "identifier", "patient"},
	"Flag":               {"_id", "_lastUpdated", "identifier", "subject", "status"},
	"List":               {"_id", "_lastUpdated", "identifier", "subject", "encounter", "source", "status", "mode", "date"},
	"EpisodeOfCare":      {"_id", "_lastUpdated", "identifier", "patient", "status"},
	"NutritionOrder":     {"_id", "_lastUpdated", "identifier", "patient", "encounter", "status", "datetime"},
	"VisionPrescription": {"_id", "_lastUpdated", "identifier", "patient", "encounter", "status", "datewritten"},
	"DeviceRequest":      {"_id", "_lastUpdated", "identifier", "subject", "encounter", "status", "intent", "authored-on"},
	"DeviceUseStatement": {"_id", "_lastUpdated", "identifier", "subject", "device", "status"},
	"SupplyRequest":      {"_id", "_lastUpdated", "identifier", "status", "date"},
	"SupplyDelivery":     {"_id", "_lastUpdated", "identifier", "patient", "status"},
	"RequestGroup":       {"_id", "_lastUpdated", "identifier", "subject", "encounter", "status", "intent", "authored"},
	"GuidanceResponse":   {"_id", "_lastUpdated", "subject", "encounter", "status"},

	// Diagnostics
	"ImagingStudy":      {"_id", "_lastUpdated", "identifier", "subject", "patient", "encounter", "status", "started"},
	"MolecularSequence": {"_id", "_lastUpdated", "identifier", "patient", "type"},

	// Medications
	"MedicationKnowledge":        {"_id", "_lastUpdated", "code", "status"},
	"ImmunizationEvaluation":     {"_id", "_lastUpdated", "identifier", "patient", "status", "date"},
	"ImmunizationRecommendation": {"_id", "_lastUpdated", "identifier", "patient", "date"},

	// Financial
	"ClaimResponse":               {"_id", "_lastUpdated", "identifier", "patient", "insurer", "request", "status", "outcome", "created"},
	"CoverageEligibilityRequest":  {"_id", "_lastUpdated", "identifier", "patient", "provider", "insurer", "status", "created"},
	"CoverageEligibilityResponse": {"_id", "_lastUpdated", "identifier", "patient", "insurer", "request", "status", "outcome", "created"},
	"EnrollmentRequest":           {"_id", "_lastUpdated", "identifier", "status"},
	"EnrollmentResponse":          {"_id", "_lastUpdated", "identifier", "status"},
	"PaymentNotice":               {"_id", "_lastUpdated", "identifier", "status", "created"},
	"PaymentReconciliation":       {"_id", "_lastUpdated", "identifier", "status", "created", "outcome"},
	"Account":                     {"_id", "_lastUpdated", "identifier", "status", "name"},
	"ChargeItem":                  {"_id", "_lastUpdated", "identifier", "subject", "code", "status"},
	"ChargeItemDefinition":        {"_id", "_lastUpdated", "url", "status"},
	"Contract":                    {"_id", "_lastUpdated", "identifier", "status"},
	"InsurancePlan":               {"_id", "_lastUpdated", "identifier", "status", "name"},
	"Invoice":                     {"_id", "_lastUpdated", "identifier", "subject", "status", "date"},

	// Workflow & scheduling
	"Schedule":            {"_id", "_lastUpdated", "identifier"},
	"Slot":                {"_id", "_lastUpdated", "identifier", "schedule", "status", "start"},
	"AppointmentResponse": {"_id", "_lastUpdated", "identifier", "appointment", "actor", "part-status"},
	"PlanDefinition":      {"_id", "_lastUpdated", "identifier", "url", "name", "title", "status", "date"},
	"ActivityDefinition":  {"_id", "_lastUpdated", "identifier", "url", "name", "status", "date"},
	"EventDefinition":     {"_id", "_lastUpdated", "url", "name", "status"},
	"Questionnaire":       {"_id", "_lastUpdated", "identifier", "url", "name", "title", "status", "date"},

	// Research & evidence
	"ResearchStudy":           {"_id", "_lastUpdated", "identifier", "title", "status"},
	"ResearchSubject":         {"_id", "_lastUpdated", "identifier", "individual", "study", "status"},
	"Measure":                 {"_id", "_lastUpdated", "identifier", "url", "name", "title", "status"},
	"MeasureReport":           {"_id", "_lastUpdated", "identifier", "subject", "status", "date", "measure"},
	"Library":                 {"_id", "_lastUpdated", "identifier", "url", "name", "title", "status"},
	"Evidence":                {"_id", "_lastUpdated", "identifier", "url", "name", "status"},
	"EvidenceVariable":        {"_id", "_lastUpdated", "identifier", "url", "name", "status"},
	"RiskEvidenceSynthesis":   {"_id", "_lastUpdated", "identifier", "url", "name", "status"},
	"EffectEvidenceSynthesis": {"_id", "_lastUpdated", "identifier", "url", "name", "status"},

	// Additional clinical & admin
	"Endpoint":               {"_id", "_lastUpdated", "identifier", "status", "name", "connection-type"},
	"HealthcareService":      {"_id", "_lastUpdated", "identifier", "name"},
	"Group":                  {"_id", "_lastUpdated", "identifier", "type", "name"},
	"Person":                 {"_id", "_lastUpdated", "identifier", "gender", "birthdate"},
	"Linkage":                {"_id", "_lastUpdated"},
	"Basic":                  {"_id", "_lastUpdated", "identifier", "subject", "created"},
	"DeviceMetric":           {"_id", "_lastUpdated", "identifier", "source", "category"},
	"DeviceDefinition":       {"_id", "_lastUpdated", "identifier"},
	"Substance":              {"_id", "_lastUpdated", "identifier", "code", "status"},
	"SubstanceSpecification": {"_id", "_lastUpdated"},
	"MedicinalProduct":       {"_id", "_lastUpdated", "identifier"},

	// The seventeen types that completed R4 coverage. Parameters follow what each resource carries: several of these have no
	// searchable top-level field beyond their id, and claiming one would advertise a search that always returns nothing.
	"BiologicallyDerivedProduct":        {"_id", "_lastUpdated", "identifier", "status"},
	"DocumentManifest":                  {"_id", "_lastUpdated", "identifier", "status", "subject", "created", "author", "recipient"},
	"MedicinalProductContraindication":  {"_id", "_lastUpdated", "subject"},
	"MedicinalProductIndication":        {"_id", "_lastUpdated", "subject"},
	"MedicinalProductIngredient":        {"_id", "_lastUpdated", "identifier"},
	"MedicinalProductInteraction":       {"_id", "_lastUpdated", "subject"},
	"MedicinalProductManufactured":      {"_id", "_lastUpdated"},
	"MedicinalProductPackaged":          {"_id", "_lastUpdated", "identifier", "subject"},
	"MedicinalProductPharmaceutical":    {"_id", "_lastUpdated", "identifier"},
	"MedicinalProductUndesirableEffect": {"_id", "_lastUpdated", "subject"},
	"ResearchDefinition":                {"_id", "_lastUpdated", "identifier", "url", "version", "name", "title", "status", "date", "publisher"},
	"ResearchElementDefinition":         {"_id", "_lastUpdated", "identifier", "url", "version", "name", "title", "status", "date", "publisher"},
	"SubstanceNucleicAcid":              {"_id", "_lastUpdated"},
	"SubstancePolymer":                  {"_id", "_lastUpdated"},
	"SubstanceProtein":                  {"_id", "_lastUpdated"},
	"SubstanceReferenceInformation":     {"_id", "_lastUpdated"},
	"SubstanceSourceMaterial":           {"_id", "_lastUpdated"},
	"MedicinalProductAuthorization":     {"_id", "_lastUpdated", "identifier", "subject"},
	"CommunicationRequest":              {"_id", "_lastUpdated", "identifier", "subject", "encounter", "status", "authored"},
	"VerificationResult":                {"_id", "_lastUpdated", "status"},
	"OrganizationAffiliation":           {"_id", "_lastUpdated", "identifier", "primary-organization"},
	"CatalogEntry":                      {"_id", "_lastUpdated", "identifier", "status", "date"},
	"TestScript":                        {"_id", "_lastUpdated", "url", "name", "status"},
	"TestReport":                        {"_id", "_lastUpdated", "name", "status", "result"},
	"TerminologyCapabilities":           {"_id", "_lastUpdated", "url", "name", "status"},
	"ExampleScenario":                   {"_id", "_lastUpdated", "identifier", "url", "name", "status"},
	"ObservationDefinition":             {"_id", "_lastUpdated", "identifier", "code"},
	"SpecimenDefinition":                {"_id", "_lastUpdated"},
}

// indexEntries extracts the searchable values from a resource.
func indexEntries(r fhir.Resource) []indexEntry {
	var out []indexEntry

	// add records one indexed value, folding case for string parameters.
	//
	// The folding lives here rather than at the call sites. It was previously written out as strings.ToLower at every place a
	// name or a text value was indexed, which is six copies of one rule - and a seventh call site that forgot would index a
	// value no lowercase query could ever match, with nothing to catch it. Callers now pass the value as it appears in the
	// resource.
	add := func(param, value, system string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}

		entry := indexEntry{param: param, value: value, system: system, valueRaw: value}
		if isStringParam(param) {
			entry.value = strings.ToLower(value)
		}

		out = append(out, entry)
	}

	// addRef indexes a reference with the type it points at.
	//
	// A separate helper rather than a third argument on add, because every reference must carry its type and an optional
	// argument would let one be forgotten - which is the defect this fixes, twelve call sites of it.
	addRef := func(param string, ref *fhir.Reference) {
		id := referenceID(ref)
		if strings.TrimSpace(id) == "" {
			return
		}
		out = append(out, indexEntry{param: param, value: id, refType: referenceType(ref)})
	}

	addIdentifiers := func(ids []fhir.Identifier) {
		for _, id := range ids {
			add("identifier", id.Value, id.System)
		}
	}

	addCodeable := func(param string, concepts ...*fhir.CodeableConcept) {
		for _, concept := range concepts {
			if concept == nil {
				continue
			}
			for _, coding := range concept.Coding {
				add(param, coding.Code, coding.System)
			}
			// Text is indexed too, because a local result with no code is still
			// something somebody will search for.
			add(param, concept.Text, "")
		}
	}

	addNames := func(names []fhir.HumanName) {
		for _, n := range names {
			add("family", n.Family, "")
			add("name", n.Family, "")
			for _, g := range n.Given {
				add("given", g, "")
				add("name", g, "")
			}
		}
	}

	switch v := r.(type) {
	case *fhir.Patient:
		addIdentifiers(v.Identifier)
		addNames(v.Name)
		add("gender", v.Gender, "")
		add("birthdate", v.BirthDate, "")

	case *fhir.Encounter:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		for i := range v.Class {
			addCodeable("class", &v.Class[i])
		}
		if v.ActualPeriod != nil {
			add("date", v.ActualPeriod.Start, "")
		}

	case *fhir.Observation:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		addCodeable("code", v.Code)
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}
		add("date", v.EffectiveDateTime, "")

	case *fhir.DiagnosticReport:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		addCodeable("code", v.Code)
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}
		add("date", v.EffectiveDateTime, "")

	case *fhir.Practitioner:
		addIdentifiers(v.Identifier)
		addNames(v.Name)

	case *fhir.Organization:
		addIdentifiers(v.Identifier)
		add("name", v.Name, "")

	case *fhir.Location:
		addIdentifiers(v.Identifier)
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.Specimen:
		addIdentifiers(v.Identifier)
		if v.AccessionIdentifier != nil {
			add("identifier", v.AccessionIdentifier.Value, v.AccessionIdentifier.System)
		}
		add("status", v.Status, "")
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addCodeable("type", v.Type)

	case *fhir.ServiceRequest:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		if v.Code != nil {
			addCodeable("code", v.Code.Concept)
		}

	case *fhir.Condition:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		addCodeable("code", v.Code)
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}
		// clinical-status rather than status, which is the parameter name US Core uses and is worth matching
		// exactly: a client generated from the specification sends this spelling and nothing else.
		addCodeable("clinical-status", v.ClinicalStatus)
		add("onset-date", v.OnsetDateTime, "")
		add("recorded-date", v.RecordedDate, "")

	case *fhir.MedicationRequest:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("intent", v.Intent, "")
		add("authoredon", v.AuthoredOn, "")

	case *fhir.AllergyIntolerance:
		addIdentifiers(v.Identifier)
		// Patient rather than Subject: AllergyIntolerance names the field patient, and an allergy is never
		// recorded against a group or a location.
		addRef("patient", v.Patient)
		addRef("encounter", v.Encounter)
		addCodeable("code", v.Code)
		addCodeable("clinical-status", v.ClinicalStatus)
		add("criticality", v.Criticality, "")
		add("date", v.RecordedDate, "")

	case *fhir.Immunization:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		addCodeable("vaccine-code", v.VaccineCode)
		add("date", v.OccurrenceDateTime, "")

	case *fhir.Procedure:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		addCodeable("code", v.Code)
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}
		// The period start when there is no single instant, so a procedure recorded as a range is still findable
		// by date rather than falling out of every date search.
		add("date", v.PerformedDateTime, "")
		if v.PerformedPeriod != nil {
			add("date", v.PerformedPeriod.Start, "")
		}

	case *fhir.DocumentReference:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		add("status", v.Status, "")
		addCodeable("type", v.Type)
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}
		add("date", v.Date, "")

	case *fhir.Medication:
		addCodeable("code", v.Code)
		add("status", v.Status, "")

	case *fhir.MedicationStatement:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		add("status", v.Status, "")

	case *fhir.MedicationDispense:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		add("status", v.Status, "")

	case *fhir.MedicationAdministration:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		add("status", v.Status, "")

	case *fhir.Coverage:
		addRef("patient", v.Beneficiary)
		addRef("beneficiary", v.Beneficiary)
		add("status", v.Status, "")

	case *fhir.Claim:
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		add("use", v.Use, "")
		add("created", v.Created, "")

	case *fhir.ExplanationOfBenefit:
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		add("use", v.Use, "")
		add("created", v.Created, "")

	case *fhir.CarePlan:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}

	case *fhir.CareTeam:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")

	case *fhir.Goal:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		add("lifecycle-status", v.LifecycleStatus, "")

	case *fhir.Device:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		addCodeable("type", v.Type)

	case *fhir.RelatedPerson:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		for i := range v.Name {
			add("name", v.Name[i].Family, "")
			for _, g := range v.Name[i].Given {
				add("name", g, "")
			}
		}

	case *fhir.PractitionerRole:
		addIdentifiers(v.Identifier)
		addRef("practitioner", v.Practitioner)
		addRef("organization", v.Organization)
		for i := range v.Specialty {
			addCodeable("specialty", &v.Specialty[i])
		}

	case *fhir.Appointment:
		addRef("patient", nil)
		add("status", v.Status, "")
		add("date", v.Start, "")
		for _, p := range v.Participant {
			addRef("patient", p.Actor)
		}

	case *fhir.Consent:
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		for i := range v.Category {
			addCodeable("category", &v.Category[i])
		}

	case *fhir.Composition:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		addCodeable("type", v.Type)
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.FamilyMemberHistory:
		addRef("patient", v.Patient)
		add("status", v.Status, "")

	case *fhir.Communication:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")

	case *fhir.Task:
		addRef("patient", v.For)
		add("status", v.Status, "")
		add("intent", v.Intent, "")
		addCodeable("code", v.Code)

	case *fhir.Provenance:
		for i := range v.Target {
			addRef("target", &v.Target[i])
		}
		add("recorded", v.Recorded, "")

	case *fhir.QuestionnaireResponse:
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("questionnaire", v.Questionnaire, "")
		add("status", v.Status, "")
		add("authored", v.Authored, "")

	case *fhir.Media:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Subject)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("created", v.CreatedDT, "")

	// ── Foundation & infrastructure ──────────────────────────────────────────

	case *fhir.StructureDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("type", v.Type, "")

	case *fhir.CapabilityStatement:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("fhirversion", v.FHIRVersion, "")

	case *fhir.CodeSystem:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.NamingSystem:
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("kind", v.Kind, "")

	case *fhir.ImplementationGuide:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.SearchParameter:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("code", v.Code, "")
		add("status", v.Status, "")
		add("type", v.Type, "")

	case *fhir.OperationDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("code", v.Code, "")
		add("status", v.Status, "")

	case *fhir.CompartmentDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("code", v.Code, "")
		add("status", v.Status, "")

	case *fhir.GraphDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.StructureMap:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.MessageDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("category", v.Category, "")

	case *fhir.MessageHeader:
		// MessageHeader typically has no identifier; indexed by event.

	case *fhir.Subscription:
		add("status", v.Status, "")
		add("criteria", v.Criteria, "")

	case *fhir.Binary:
		add("contenttype", v.ContentType, "")

	// ── Security ─────────────────────────────────────────────────────────────

	case *fhir.AuditEvent:
		add("action", v.Action, "")
		add("outcome", v.Outcome, "")
		add("date", v.Recorded, "")

	// ── Clinical ─────────────────────────────────────────────────────────────

	case *fhir.AdverseEvent:
		addRef("subject", v.Subject)
		add("actuality", v.Actuality, "")
		add("date", v.Date, "")

	case *fhir.DetectedIssue:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		add("severity", v.Severity, "")

	case *fhir.ClinicalImpression:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.RiskAssessment:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")

	case *fhir.BodyStructure:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)

	case *fhir.Flag:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		add("status", v.Status, "")

	case *fhir.List:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		addRef("source", v.Source)
		add("status", v.Status, "")
		add("mode", v.Mode, "")
		add("date", v.Date, "")

	case *fhir.EpisodeOfCare:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("status", v.Status, "")

	case *fhir.NutritionOrder:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("datetime", v.DateTime, "")

	case *fhir.VisionPrescription:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("datewritten", v.DateWritten, "")

	case *fhir.DeviceRequest:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("intent", v.Intent, "")
		add("authored-on", v.AuthoredOn, "")

	case *fhir.DeviceUseStatement:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("device", v.Device)
		add("status", v.Status, "")

	case *fhir.SupplyRequest:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("date", v.AuthoredOn, "")

	case *fhir.SupplyDelivery:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("status", v.Status, "")

	case *fhir.RequestGroup:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("intent", v.Intent, "")
		add("authored", v.AuthoredOn, "")

	case *fhir.GuidanceResponse:
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")

	// ── Diagnostics ──────────────────────────────────────────────────────────

	case *fhir.ImagingStudy:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("patient", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("started", v.Started, "")

	case *fhir.MolecularSequence:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("type", v.Type, "")

	// ── Medications ──────────────────────────────────────────────────────────

	case *fhir.MedicationKnowledge:
		addCodeable("code", v.Code)
		add("status", v.Status, "")

	case *fhir.ImmunizationEvaluation:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.ImmunizationRecommendation:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		add("date", v.Date, "")

	// ── Financial ────────────────────────────────────────────────────────────

	case *fhir.ClaimResponse:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("insurer", v.Insurer)
		addRef("request", v.Request)
		add("status", v.Status, "")
		add("outcome", v.Outcome, "")
		add("created", v.Created, "")

	case *fhir.CoverageEligibilityRequest:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("provider", v.Provider)
		addRef("insurer", v.Insurer)
		add("status", v.Status, "")
		add("created", v.Created, "")

	case *fhir.CoverageEligibilityResponse:
		addIdentifiers(v.Identifier)
		addRef("patient", v.Patient)
		addRef("insurer", v.Insurer)
		addRef("request", v.Request)
		add("status", v.Status, "")
		add("outcome", v.Outcome, "")
		add("created", v.Created, "")

	case *fhir.EnrollmentRequest:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")

	case *fhir.EnrollmentResponse:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")

	case *fhir.PaymentNotice:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("created", v.Created, "")

	case *fhir.PaymentReconciliation:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("created", v.Created, "")
		add("outcome", v.Outcome, "")

	case *fhir.Account:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("name", v.Name, "")

	case *fhir.ChargeItem:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addCodeable("code", v.Code)
		add("status", v.Status, "")

	case *fhir.ChargeItemDefinition:
		add("url", v.URL, "")
		add("status", v.Status, "")

	case *fhir.Contract:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")

	case *fhir.InsurancePlan:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("name", v.Name, "")

	case *fhir.Invoice:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		add("status", v.Status, "")
		add("date", v.Date, "")

	// ── Workflow & scheduling ─────────────────────────────────────────────────

	case *fhir.Schedule:
		addIdentifiers(v.Identifier)

	case *fhir.Slot:
		addIdentifiers(v.Identifier)
		addRef("schedule", v.Schedule)
		add("status", v.Status, "")
		add("start", v.Start, "")

	case *fhir.AppointmentResponse:
		addIdentifiers(v.Identifier)
		addRef("appointment", v.Appointment)
		addRef("actor", v.Actor)
		add("part-status", v.ParticipantStatus, "")

	case *fhir.PlanDefinition:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("title", v.Title, "")
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.ActivityDefinition:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.EventDefinition:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.Questionnaire:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("title", v.Title, "")
		add("status", v.Status, "")
		add("date", v.Date, "")

	// ── Research & evidence ──────────────────────────────────────────────────

	case *fhir.ResearchStudy:
		addIdentifiers(v.Identifier)
		add("title", v.Title, "")
		add("status", v.Status, "")

	case *fhir.ResearchSubject:
		addIdentifiers(v.Identifier)
		addRef("individual", v.Individual)
		addRef("study", v.Study)
		add("status", v.Status, "")

	case *fhir.Measure:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("title", v.Title, "")
		add("status", v.Status, "")

	case *fhir.MeasureReport:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		add("status", v.Status, "")
		add("date", v.Date, "")
		add("measure", v.Measure, "")

	case *fhir.Library:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("title", v.Title, "")
		add("status", v.Status, "")

	case *fhir.Evidence:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.EvidenceVariable:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.RiskEvidenceSynthesis:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.EffectEvidenceSynthesis:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	// ── Additional clinical & admin ──────────────────────────────────────────

	case *fhir.Endpoint:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("name", v.Name, "")
		add("connection-type", v.ConnectionType.Code, "")

	case *fhir.HealthcareService:
		addIdentifiers(v.Identifier)
		add("name", v.Name, "")

	case *fhir.Group:
		addIdentifiers(v.Identifier)
		add("type", v.Type, "")
		add("name", v.Name, "")

	case *fhir.Person:
		addIdentifiers(v.Identifier)
		add("gender", v.Gender, "")
		add("birthdate", v.BirthDate, "")

	case *fhir.Linkage:
		// Linkage has no common search params beyond _id.

	case *fhir.Basic:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		add("created", v.Created, "")

	case *fhir.DeviceMetric:
		addIdentifiers(v.Identifier)
		addRef("source", v.Source)
		add("category", v.Category, "")

	case *fhir.DeviceDefinition:
		addIdentifiers(v.Identifier)

	case *fhir.Substance:
		addIdentifiers(v.Identifier)
		addCodeable("code", v.Code)
		add("status", v.Status, "")

	case *fhir.SubstanceSpecification:
		// Rare resource, minimal search fields.

	case *fhir.MedicinalProduct:
		addIdentifiers(v.Identifier)

	case *fhir.MedicinalProductAuthorization:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)

	case *fhir.CommunicationRequest:
		addIdentifiers(v.Identifier)
		addRef("subject", v.Subject)
		addRef("encounter", v.Encounter)
		add("status", v.Status, "")
		add("authored", v.AuthoredOn, "")

	case *fhir.VerificationResult:
		add("status", v.Status, "")

	case *fhir.OrganizationAffiliation:
		addIdentifiers(v.Identifier)
		addRef("primary-organization", v.Organization)

	case *fhir.Catalog:
		addIdentifiers(v.Identifier)
		add("status", v.Status, "")
		add("date", v.Date, "")

	case *fhir.TestScript:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.TestReport:
		add("name", v.Name, "")
		add("status", v.Status, "")
		add("result", v.Result, "")

	case *fhir.TerminologyCapabilities:
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.ExampleScenario:
		addIdentifiers(v.Identifier)
		add("url", v.URL, "")
		add("name", v.Name, "")
		add("status", v.Status, "")

	case *fhir.ObservationDefinition:
		addIdentifiers(v.Identifier)
		addCodeable("code", v.Code)

	case *fhir.SpecimenDefinition:
		// Minimal search fields.
	}

	return out
}

// SearchQuery is a parsed search request.
type SearchQuery struct {
	ResourceType string
	// Criteria maps a parameter to the values it must match. Repeating a parameter
	// means OR within that parameter and AND between parameters, which is what
	// FHIR specifies.
	Criteria map[string][]string
	Count    int
	Offset   int
	SortDesc bool

	// Chains are the chained criteria, such as patient.family=Dubois.
	//
	// Kept apart from Criteria because they are resolved before the main query rather than compiled into it, and
	// folding them in would mean the rest of Search had to know which entries were real parameters.
	Chains []ChainedCriterion

	// Modified are criteria carrying a modifier: name:exact, name:contains, birthdate:missing.
	//
	// Kept apart from Criteria for the same reason Chains are. The launch context narrowing writes directly into Criteria to
	// restrict a search to one patient, and an entry there that meant "absent" rather than "equal to" would make that
	// narrowing mean something else - a security boundary quietly changed by a search parameter.
	Modified []ModifiedCriterion

	// Includes are the _include and _revinclude specifications, in the order given.
	//
	// Order is preserved because it is the order the client wrote, and a bundle is read by a person as often as by a
	// program.
	Includes []IncludeSpec

	// CountGiven records that the client asked for a page size, as opposed to taking whatever this server
	// defaults to.
	//
	// The distinction matters because the default is configurable and a client's explicit _count is not
	// something a server setting should override - a client that asks for ten and silently gets fifty cannot
	// page correctly. So this exists to let the caller apply its default only where the client expressed no
	// preference, and to keep ParseSearch a pure function of the URL.
	CountGiven bool
}

// DefaultCount is the page size when none is given.
const DefaultCount = 50

// MaxCount bounds a page. An unbounded _count is a way to ask a server to load its
// entire database into memory.
const MaxCount = 1000

// ParseSearch turns URL query values into a search.
//
// An unsupported parameter is an error. A server that ignores one returns the
// wrong resources and the client cannot tell, which in clinical data means acting
// on somebody else's results.
func ParseSearch(resourceType string, values map[string][]string) (*SearchQuery, error) {
	supported, ok := SearchParams[resourceType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, resourceType)
	}

	allowed := map[string]bool{}
	for _, p := range supported {
		allowed[p] = true
	}

	q := &SearchQuery{
		ResourceType: resourceType,
		Criteria:     map[string][]string{},
		Count:        DefaultCount,
		SortDesc:     true,
	}

	for key, vals := range values {
		switch key {
		case "_count":
			if len(vals) == 0 {
				continue
			}
			n, err := strconv.Atoi(vals[0])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("_count must be a non-negative number, got %q", vals[0])
			}
			if n > MaxCount {
				n = MaxCount
			}
			q.Count = n
			q.CountGiven = true
			continue

		case "_offset", "_skip":
			if len(vals) == 0 {
				continue
			}
			n, err := strconv.Atoi(vals[0])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("_offset must be a non-negative number, got %q", vals[0])
			}
			q.Offset = n
			continue

		case "_sort":
			if len(vals) > 0 && !strings.HasPrefix(vals[0], "-") {
				q.SortDesc = false
			}
			continue

		case "_format", "_pretty":
			continue

		case "_include", "_revinclude", "_include:iterate", "_revinclude:iterate",
			"_include:recurse", "_revinclude:recurse":
			// :recurse is the older spelling of :iterate, still emitted by some libraries. Accepted rather than refused,
			// because refusing the spelling a client's library produces achieves nothing except a support ticket.
			iterate := strings.Contains(key, ":iterate") || strings.Contains(key, ":recurse")
			reverse := strings.HasPrefix(key, "_revinclude")
			// Refused rather than ignored when it names something unsupported. An ignored include
			// produces a bundle missing the resources the client asked for, and a client that assumes
			// they are present renders an empty screen with no error anywhere to explain it.
			for _, v := range vals {
				if strings.TrimSpace(v) == "" {
					continue
				}
				spec, err := ParseInclude(v, reverse)
				if err != nil {
					return nil, err
				}
				if iterate {
					spec.Iterate = true
				}
				q.Includes = append(q.Includes, spec)
			}
			continue
		}

		// A chained parameter is recognised before the modifier check below, because a chain may legitimately
		// carry a colon - subject:Patient.name - and the modifier check would otherwise refuse it as an
		// unsupported modifier, which is a confusing thing to be told about a valid query.
		if crit, chained, err := parseChain(resourceType, key); chained {
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					crit.Values = append(crit.Values, v)
				}
			}
			if len(crit.Values) > 0 {
				q.Chains = append(q.Chains, crit)
			}

			continue
		}

		// A modifier is parsed here, after chains, because a chain also carries a colon.
		if crit, modified, err := parseModifier(resourceType, key); modified {
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					crit.Values = append(crit.Values, v)
				}
			}
			// A modifier with no value is dropped rather than applied. :missing with no value has no meaning, and
			// :exact= would otherwise match only the empty string, which is not what an empty query parameter means.
			if len(crit.Values) > 0 {
				// Validated here rather than at query time so a bad value is refused before any work is done, and
				// the error names the parameter the client wrote.
				if crit.Modifier == ModMissing {
					for _, v := range crit.Values {
						if _, err := missingWanted(v); err != nil {
							return nil, fmt.Errorf("%s:missing: %w", crit.Param, err)
						}
					}
				}
				q.Modified = append(q.Modified, crit)
			}

			continue
		}

		if !allowed[key] {
			sorted := append([]string(nil), supported...)
			sort.Strings(sorted)
			return nil, fmt.Errorf(
				"%s does not support the search parameter %q; supported: %s",
				resourceType, key, strings.Join(sorted, ", "))
		}

		for _, v := range vals {
			if v != "" {
				q.Criteria[key] = append(q.Criteria[key], v)
			}
		}
	}

	return q, nil
}

// SearchResult is a page of matches.
type SearchResult struct {
	Resources []fhir.Resource
	Total     int
	Offset    int
	Count     int

	// Included are the resources pulled in by _include and _revinclude.
	//
	// Kept apart from Resources rather than appended to them, because the bundle must mark them differently and
	// because Total counts matches only. A client pages on Total, so counting included resources there makes the last
	// page arrive early and the client stop before it has everything.
	Included []fhir.Resource
}

// Search runs a query.
func (s *Store) Search(ctx context.Context, q *SearchQuery) (*SearchResult, error) {
	// Chains resolved first, into ordinary reference criteria, so nothing below needs to know about chaining.
	if len(q.Chains) > 0 {
		if err := s.resolveChains(ctx, q); err != nil {
			return nil, err
		}
	}

	args := []any{q.ResourceType}
	var where strings.Builder
	where.WriteString(`resource_type = ? AND deleted = 0`)

	// _id and _lastUpdated are on the resource row rather than the index.
	for param, values := range q.Criteria {
		switch param {
		case "_id":
			where.WriteString(" AND resource_id IN (" + placeholders(len(values)) + ")")
			for _, v := range values {
				args = append(args, v)
			}

		case "_lastUpdated":
			for _, v := range values {
				op, value, err := parseDatePrefix(v)
				if err != nil {
					return nil, err
				}
				where.WriteString(" AND last_updated " + op + " ?")
				args = append(args, value)
			}

		default:
			// Each parameter becomes an EXISTS against the index. Separate
			// subqueries are what makes repeated parameters AND together while
			// values within one parameter OR.
			var clause strings.Builder
			clause.WriteString(` AND EXISTS (SELECT 1 FROM fhir_search x
				WHERE x.resource_type = fhir_resources.resource_type
				  AND x.resource_id = fhir_resources.resource_id
				  AND x.param = ? AND (`)
			args = append(args, param)

			for i, v := range values {
				if i > 0 {
					clause.WriteString(" OR ")
				}
				system, value := splitToken(v)
				if isDateParam(param) {
					op, normalised, err := parseDatePrefix(v)
					if err != nil {
						return nil, err
					}
					clause.WriteString("x.value " + op + " ?")
					args = append(args, normalised)
					continue
				}
				if isReferenceParam(param) {
					// A type-qualified reference matches the type as well as the id.
					//
					// Patient/123 and Group/123 were previously the same query, because the index held a
					// bare id and this compared bare ids. A reference search that returns another
					// resource's records as the requested one's is the worst answer available here, so
					// the type is compared when the client gave one.
					//
					// When the client gave no type the id alone is matched, which is what FHIR means by
					// the unqualified form and is what a client sending ?patient=123 expects.
					clause.WriteString("x.value = ?")
					args = append(args, refIDFromString(v))

					if refType := refTypeFromString(v); refType != "" {
						// An index row with no recorded type is not matched here. It cannot be:
						// admitting it would restore exactly the ambiguity this removes. Rows are
						// re-derived on migration so there are none, and a resource written since
						// carries its type.
						clause.WriteString(" AND x.ref_type = ?")
						args = append(args, refType)
					}

					continue
				}
				if system != "" {
					if value == "" {
						// system| means "any code in this system". Match the system alone.
						clause.WriteString("x.system = ?")
						args = append(args, system)
					} else {
						clause.WriteString("(x.system = ? AND x.value = ?)")
						args = append(args, system, value)
					}
				} else {
					// Name-ish parameters are matched as prefixes, which is how
					// FHIR defines string search, and are indexed lowercase.
					if isStringParam(param) {
						clause.WriteString("x.value LIKE ?")
						args = append(args, strings.ToLower(value)+"%")
					} else {
						clause.WriteString("x.value = ?")
						args = append(args, value)
					}
				}
			}
			clause.WriteString("))")
			where.WriteString(clause.String())
		}
	}

	// Modified criteria, each its own EXISTS or NOT EXISTS against the index.
	//
	// Written after the plain criteria rather than mixed in, so the two cannot interfere: a parameter can appear both
	// plainly and with a modifier in one query, and both must apply.
	for _, crit := range q.Modified {
		clause, extra, err := modifiedClause(crit)
		if err != nil {
			return nil, err
		}
		where.WriteString(clause)
		args = append(args, extra...)
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fhir_resources WHERE `+where.String(), args...).Scan(&total); err != nil {
		return nil, err
	}

	order := "DESC"
	if !q.SortDesc {
		order = "ASC"
	}
	query := `SELECT content FROM fhir_resources WHERE ` + where.String() +
		` ORDER BY last_updated ` + order + `, resource_id ` + order + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any(nil), args...), q.Count, q.Offset)

	rows, err := s.db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := &SearchResult{Total: total, Offset: q.Offset, Count: q.Count}
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		r, err := fhir.UnmarshalResource([]byte(content))
		if err != nil {
			// A stored resource that cannot be read back is a real defect, and
			// skipping it silently would hide it.
			return nil, fmt.Errorf("fhirserver: a stored resource could not be read: %w", err)
		}
		result.Resources = append(result.Resources, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Includes resolved here rather than by the caller.
	//
	// Every route that searches would otherwise have to remember to do it, and forgetting produces a bundle that is
	// valid, has the right total, and is missing what the client asked for. This is also why Include takes the page: an
	// include over every match would return a hundred thousand patients for a page of twenty observations.
	if len(q.Includes) > 0 {
		included, err := s.Include(ctx, result.Resources, q.Includes)
		if err != nil {
			return nil, err
		}
		result.Included = included
	}

	return result, nil
}

// FindByIdentifier resolves a conditional reference, such as the ifNoneExist on a
// transaction entry.
func (s *Store) FindByIdentifier(ctx context.Context, resourceType, system, value string) (fhir.Resource, error) {
	q := &SearchQuery{
		ResourceType: resourceType,
		Criteria:     map[string][]string{"identifier": {token(system, value)}},
		Count:        2,
	}
	result, err := s.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	switch len(result.Resources) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return result.Resources[0], nil
	default:
		return nil, ErrConflict
	}
}

func token(system, value string) string {
	if system == "" {
		return value
	}
	return system + "|" + value
}

// splitToken separates a token search value into its system and code halves.
func splitToken(v string) (system, value string) {
	i := strings.LastIndex(v, "|")
	if i < 0 {
		return "", v
	}
	return v[:i], v[i+1:]
}

// parseDatePrefix handles the comparison prefixes FHIR uses on date searches.
func parseDatePrefix(v string) (op, value string, err error) {
	if len(v) < 2 {
		return "=", v, nil
	}
	switch v[:2] {
	case "eq":
		return "=", v[2:], nil
	case "ne":
		return "!=", v[2:], nil
	case "gt":
		return ">", v[2:], nil
	case "lt":
		return "<", v[2:], nil
	case "ge":
		return ">=", v[2:], nil
	case "le":
		return "<=", v[2:], nil
	case "sa":
		return ">", v[2:], nil
	case "eb":
		return "<", v[2:], nil
	}
	return "LIKE", v + "%", nil
}

func isDateParam(param string) bool {
	switch param {
	case "date", "birthdate", "_lastUpdated",
		"onset-date", "recorded-date", "authoredon", "authored-on",
		"datetime", "datewritten", "created", "authored",
		"started", "recorded":
		return true
	}
	return false
}

func isStringParam(param string) bool {
	switch param {
	case "name", "family", "given":
		return true
	}
	return false
}

// isReferenceParam reports whether a parameter names another resource.
//
// These are indexed as bare logical ids, so a query has to be reduced to the same form before it is compared. Without this,
// a search for patient=Patient/123 - which is the form the specification uses and the form every real client sends - matched
// nothing, and returned an empty bundle with a 200. An empty result reads as "this patient has no observations", which is a
// false clinical statement delivered as a success.
//
// One known limitation, stated rather than hidden: the index holds the id without the type, so subject=Group/123 and
// subject=Patient/123 are indistinguishable. Fixing that means storing the type alongside the id, which is a schema change,
// and the collision needs two resources of different types sharing a logical id.
func isReferenceParam(param string) bool {
	switch param {
	case "patient", "subject", "encounter",
		"practitioner", "organization", "beneficiary",
		"individual", "study", "target",
		"source", "device", "insurer", "request", "provider",
		"schedule", "appointment", "actor",
		"primary-organization", "For":
		return true
	}

	return false
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// SearchBundle wraps a result as a searchset bundle.
func (s *Store) SearchBundle(result *SearchResult, baseURL, resourceType string, query string) *fhir.Bundle {
	b := &fhir.Bundle{
		Type:      fhir.BundleSearchset,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Total:     fhir.Int(result.Total),
	}
	b.SetResourceID("search")

	self := strings.TrimRight(baseURL, "/") + "/" + resourceType
	if query != "" {
		self += "?" + query
	}
	b.Link = []fhir.BundleLink{{Relation: "self", URL: self}}

	// A next link only when there is a next page, so a client can page without
	// guessing.
	if result.Offset+len(result.Resources) < result.Total {
		next := fmt.Sprintf("%s/%s?_count=%d&_offset=%d",
			strings.TrimRight(baseURL, "/"), resourceType,
			result.Count, result.Offset+result.Count)
		b.Link = append(b.Link, fhir.BundleLink{Relation: "next", URL: next})
	}

	for _, r := range result.Resources {
		b.Entry = append(b.Entry, fhir.BundleEntry{
			FullURL:  fmt.Sprintf("%s/%s/%s", strings.TrimRight(baseURL, "/"), r.ResourceTypeName(), r.ResourceID()),
			Resource: r,
			Search:   &fhir.BundleEntrySearch{Mode: "match"},
		})
	}

	// Included resources are marked as such, which is not decoration.
	//
	// The mode is how a client tells what it asked for from what came along. Marking an included Patient as a match
	// would make a search for observations appear to have returned a patient, and a client counting matches or
	// rendering a result list would show it as one.
	for _, r := range result.Included {
		b.Entry = append(b.Entry, fhir.BundleEntry{
			FullURL:  fmt.Sprintf("%s/%s/%s", strings.TrimRight(baseURL, "/"), r.ResourceTypeName(), r.ResourceID()),
			Resource: r,
			Search:   &fhir.BundleEntrySearch{Mode: "include"},
		})
	}

	return b
}

// refTypeFromString reads the resource type out of a reference given in a query parameter.
//
// The query-side counterpart of referenceType, which reads it out of a stored Reference. Two functions rather than one because the inputs
// differ - a query value is a plain string and may carry a query fragment - and sharing one would mean a signature that takes either,
// which is how a caller ends up passing the wrong thing.
//
// Empty when the value names no type, which is the unqualified form and matches any type by design.
func refTypeFromString(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}

	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		ref = ref[:i]
	}

	parts := strings.Split(strings.Trim(ref, "/"), "/")
	if len(parts) < 2 {
		return ""
	}

	candidate := parts[len(parts)-2]
	// A FHIR resource type begins with a capital, which is what separates a type from a path segment such as "fhir" in
	// http://host/fhir/123. Recording that as a type would produce a query matching nothing while looking deliberate.
	if candidate == "" || candidate[0] < 'A' || candidate[0] > 'Z' {
		return ""
	}

	return candidate
}
