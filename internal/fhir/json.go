package fhir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Serialisation.
//
// The resource structs are R5-shaped, so producing R4 means applying the
// differences between the two releases. Doing that here, in one place, is
// deliberate: the alternative is two parallel sets of structs that drift until
// something is R5 in one and R4 in the other and nobody notices until a hospital
// rejects a message.
//
// Only the differences that affect the resources this package produces are
// handled. Anything else would be untested code claiming conformance it does not
// have.

// Marshal renders a resource as JSON for the given FHIR version.
func Marshal(r Resource, version Version) ([]byte, error) {
	if !version.Valid() {
		return nil, fmt.Errorf("fhir: cannot serialise for unsupported version %q", version)
	}

	// Round-trip through a generic tree so version differences can be applied as
	// field renames and type changes without a second set of structs.
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	if tree["resourceType"] == nil || tree["resourceType"] == "" {
		tree["resourceType"] = r.ResourceTypeName()
	}

	if err := applyChoiceRules(tree, r.ResourceTypeName()); err != nil {
		return nil, err
	}
	if version.IsR4Family() {
		downgradeToR4(tree, r.ResourceTypeName())
	}

	return json.Marshal(tree)
}

// MarshalIndent is Marshal with indentation, for anything a human will read.
func MarshalIndent(r Resource, version Version) ([]byte, error) {
	compact, err := Marshal(r, version)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// MarshalBundle renders a bundle, converting every contained resource to the same
// version. Bundle needs its own path because entry.resource is polymorphic and
// the entries have to be converted individually.
func MarshalBundle(b *Bundle, version Version) ([]byte, error) {
	if !version.Valid() {
		return nil, fmt.Errorf("fhir: cannot serialise for unsupported version %q", version)
	}

	out := map[string]any{
		"resourceType": "Bundle",
		"type":         b.Type,
	}
	if b.ID != "" {
		out["id"] = b.ID
	}
	if b.Timestamp != "" {
		out["timestamp"] = b.Timestamp
	}
	if b.Total != nil {
		out["total"] = *b.Total
	}
	if b.Meta != nil {
		metaRaw, err := json.Marshal(b.Meta)
		if err != nil {
			return nil, err
		}
		var meta map[string]any
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return nil, err
		}
		out["meta"] = meta
	}
	if b.Identifier != nil {
		idRaw, err := json.Marshal(b.Identifier)
		if err != nil {
			return nil, err
		}
		var id map[string]any
		if err := json.Unmarshal(idRaw, &id); err != nil {
			return nil, err
		}
		out["identifier"] = id
	}
	if len(b.Link) > 0 {
		links := make([]any, 0, len(b.Link))
		for _, l := range b.Link {
			links = append(links, map[string]any{"relation": l.Relation, "url": l.URL})
		}
		out["link"] = links
	}

	entries := make([]any, 0, len(b.Entry))
	for _, e := range b.Entry {
		entry := map[string]any{}
		if e.FullURL != "" {
			entry["fullUrl"] = e.FullURL
		}
		if e.Resource != nil {
			resRaw, err := Marshal(e.Resource, version)
			if err != nil {
				return nil, err
			}
			var res map[string]any
			if err := json.Unmarshal(resRaw, &res); err != nil {
				return nil, err
			}
			entry["resource"] = res
		}
		if e.Request != nil {
			req := map[string]any{"method": e.Request.Method, "url": e.Request.URL}
			if e.Request.IfNoneExist != "" {
				req["ifNoneExist"] = e.Request.IfNoneExist
			}
			if e.Request.IfMatch != "" {
				req["ifMatch"] = e.Request.IfMatch
			}
			entry["request"] = req
		}
		if e.Response != nil {
			resp := map[string]any{"status": e.Response.Status}
			if e.Response.Location != "" {
				resp["location"] = e.Response.Location
			}
			if e.Response.Etag != "" {
				resp["etag"] = e.Response.Etag
			}
			entry["response"] = resp
		}
		if e.Search != nil {
			search := map[string]any{}
			if e.Search.Mode != "" {
				search["mode"] = e.Search.Mode
			}
			if e.Search.Score != nil {
				search["score"] = *e.Search.Score
			}
			entry["search"] = search
		}
		entries = append(entries, entry)
	}
	if len(entries) > 0 {
		out["entry"] = entries
	}

	return json.Marshal(out)
}

// MarshalBundleIndent is MarshalBundle with indentation.
func MarshalBundleIndent(b *Bundle, version Version) ([]byte, error) {
	compact, err := MarshalBundle(b, version)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// choiceGroups lists the choice-of-type element groups per resource. FHIR allows
// exactly one of each group; emitting two is invalid and is the kind of error a
// receiving server rejects with no useful explanation.
var choiceGroups = map[string][][]string{
	"Patient": {
		{"deceasedBoolean", "deceasedDateTime"},
		{"multipleBirthBoolean", "multipleBirthInteger"},
	},
	"Observation": {
		{
			"valueQuantity", "valueCodeableConcept", "valueString", "valueBoolean",
			"valueInteger", "valueRange", "valueRatio", "valueDateTime", "valuePeriod",
		},
		{"effectiveDateTime", "effectivePeriod"},
	},
	"DiagnosticReport": {
		{"effectiveDateTime", "effectivePeriod"},
	},
	"Specimen": {
		{"collectedDateTime", "collectedPeriod"},
	},
}

// applyChoiceRules rejects a resource that sets more than one member of a choice
// group. Failing here is better than producing a resource a server will refuse
// for reasons it will not explain.
func applyChoiceRules(tree map[string]any, resourceType string) error {
	for _, group := range choiceGroups[resourceType] {
		var present []string
		for _, field := range group {
			if _, ok := tree[field]; ok {
				present = append(present, field)
			}
		}
		if len(present) > 1 {
			sort.Strings(present)
			return fmt.Errorf(
				"fhir: %s sets %d members of a choice element (%v); FHIR allows exactly one",
				resourceType, len(present), present)
		}
	}

	// Observation components carry the same value[x] rule.
	if resourceType == "Observation" {
		if components, ok := tree["component"].([]any); ok {
			for i, c := range components {
				comp, ok := c.(map[string]any)
				if !ok {
					continue
				}
				var present []string
				for _, field := range []string{"valueQuantity", "valueCodeableConcept", "valueString"} {
					if _, ok := comp[field]; ok {
						present = append(present, field)
					}
				}
				if len(present) > 1 {
					return fmt.Errorf("fhir: Observation.component[%d] sets %v; FHIR allows one value", i, present)
				}
			}
		}
	}
	return nil
}

// encounterStatusR5ToR4 maps the R5 status value set onto R4's.
//
// R5 replaced several codes. "discharged" and "completed" both collapse to
// "finished" in R4, and "discontinued" to "stopped". Emitting an R5 code in an R4
// resource produces a resource that fails validation on a required binding.
var encounterStatusR5ToR4 = map[string]string{
	"planned":          "planned",
	"in-progress":      "in-progress",
	"on-hold":          "onleave",
	"discharged":       "finished",
	"completed":        "finished",
	"cancelled":        "cancelled",
	"discontinued":     "cancelled",
	"entered-in-error": "entered-in-error",
	"unknown":          "unknown",
}

// downgradeToR4 rewrites an R5-shaped resource tree into R4 form.
func downgradeToR4(tree map[string]any, resourceType string) {
	switch resourceType {
	case "Encounter":
		// R4 calls it period, R5 actualPeriod.
		rename(tree, "actualPeriod", "period")

		// R4 calls it hospitalization, R5 admission.
		rename(tree, "admission", "hospitalization")

		// R4's class is a single Coding; R5's is a list of CodeableConcept.
		if classes, ok := tree["class"].([]any); ok && len(classes) > 0 {
			if first, ok := classes[0].(map[string]any); ok {
				if codings, ok := first["coding"].([]any); ok && len(codings) > 0 {
					tree["class"] = codings[0]
				} else if text, ok := first["text"].(string); ok {
					// No coding, only text. R4's class is a Coding with no text
					// element, so the text becomes the display.
					tree["class"] = map[string]any{"display": text}
				} else {
					delete(tree, "class")
				}
			}
		}

		if status, ok := tree["status"].(string); ok {
			if mapped, found := encounterStatusR5ToR4[status]; found {
				tree["status"] = mapped
			}
		}

		// R5's serviceType is CodeableReference; R4's is CodeableConcept.
		if services, ok := tree["serviceType"].([]any); ok && len(services) > 0 {
			if first, ok := services[0].(map[string]any); ok {
				if concept, ok := first["concept"]; ok {
					tree["serviceType"] = concept
				} else {
					delete(tree, "serviceType")
				}
			}
		}

		// R5 nests the actor under participant; R4 has individual.
		if participants, ok := tree["participant"].([]any); ok {
			for _, p := range participants {
				part, ok := p.(map[string]any)
				if !ok {
					continue
				}
				rename(part, "actor", "individual")
			}
		}

		// R5 renamed EncounterLocation.form; R4 calls it physicalType.
		if locations, ok := tree["location"].([]any); ok {
			for _, l := range locations {
				loc, ok := l.(map[string]any)
				if !ok {
					continue
				}
				rename(loc, "form", "physicalType")
			}
		}

	case "ServiceRequest":
		// R5's code is CodeableReference; R4's is CodeableConcept.
		if code, ok := tree["code"].(map[string]any); ok {
			if concept, ok := code["concept"]; ok {
				tree["code"] = concept
			} else {
				delete(tree, "code")
			}
		}

	case "Specimen":
		if collection, ok := tree["collection"].(map[string]any); ok {
			// R5's bodySite is CodeableReference; R4's is CodeableConcept.
			if site, ok := collection["bodySite"].(map[string]any); ok {
				if concept, ok := site["concept"]; ok {
					collection["bodySite"] = concept
				} else {
					delete(collection, "bodySite")
				}
			}
		}

	case "Medication":
		// R5: ingredient[].item is a CodeableReference.
		// R4: ingredient[].item[x] is itemCodeableConcept or itemReference (choice type).
		if ingredients, ok := tree["ingredient"].([]any); ok {
			for _, ing := range ingredients {
				ingredient, ok := ing.(map[string]any)
				if !ok {
					continue
				}
				flattenIngredientItemToR4(ingredient)
			}
		}

	case "MedicationStatement", "MedicationDispense", "MedicationAdministration":
		// R5 carries the medication in a single CodeableReference named medication. R4 has no
		// such datatype and splits it into the medicationCodeableConcept / medicationReference
		// choice, so the populated half becomes the corresponding R4 field.
		//
		// MedicationRequest is absent from this list on purpose: its struct already holds the
		// two R4 fields separately, and upgradeToR5 combines them in the other direction.
		flattenMedicationToR4(tree)
	}

	// The profile claimed in meta is version-specific, so an R5 profile URL must
	// not be asserted on an R4 resource.
	if meta, ok := tree["meta"].(map[string]any); ok {
		if profiles, ok := meta["profile"].([]any); ok {
			kept := make([]any, 0, len(profiles))
			for _, p := range profiles {
				if s, ok := p.(string); ok && !containsSubstring(s, "/R5/") {
					kept = append(kept, s)
				}
			}
			if len(kept) == 0 {
				delete(meta, "profile")
			} else {
				meta["profile"] = kept
			}
		}
	}
}

func rename(tree map[string]any, from, to string) {
	if v, ok := tree[from]; ok {
		tree[to] = v
		delete(tree, from)
	}
}

// flattenMedicationToR4 splits an R5 medication CodeableReference into the R4 choice fields.
//
// A CodeableReference may carry a concept, a reference, or both. R4 has no way to express both,
// so the concept wins when both are present: a coded medication is what a receiver can act on
// without resolving another resource, and dropping the reference loses less than dropping the code.
func flattenMedicationToR4(tree map[string]any) {
	med, ok := tree["medication"].(map[string]any)
	if !ok {
		return
	}
	delete(tree, "medication")

	if concept, ok := med["concept"]; ok {
		tree["medicationCodeableConcept"] = concept
		return
	}
	if ref, ok := med["reference"]; ok {
		tree["medicationReference"] = ref
	}
}

// flattenIngredientItemToR4 splits an R5 ingredient item CodeableReference into the R4 choice fields.
//
// R5: Medication.ingredient.item is a CodeableReference.
// R4: Medication.ingredient.item[x] is itemCodeableConcept or itemReference.
func flattenIngredientItemToR4(ingredient map[string]any) {
	item, ok := ingredient["item"].(map[string]any)
	if !ok {
		return
	}
	delete(ingredient, "item")

	if concept, ok := item["concept"]; ok {
		ingredient["itemCodeableConcept"] = concept
		return
	}
	if ref, ok := item["reference"]; ok {
		ingredient["itemReference"] = ref
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOfSub(s, sub) >= 0
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// UnmarshalResource parses a resource, dispatching on resourceType.
//
// Unknown resource types are an error rather than a silently empty struct,
// because accepting something we cannot interpret and then acting as though it
// was understood is worse than refusing it.
func UnmarshalResource(data []byte) (Resource, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("fhir: not valid JSON: %w", err)
	}
	if probe.ResourceType == "" {
		return nil, fmt.Errorf("fhir: no resourceType, so this is not a FHIR resource")
	}

	build, ok := resourceConstructors[probe.ResourceType]
	if !ok {
		return nil, fmt.Errorf("fhir: resource type %q is not implemented", probe.ResourceType)
	}
	target := build()

	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("fhir: reading %s: %w", probe.ResourceType, err)
	}

	// Normalise codes that differ between releases, so the structs hold one representation.
	//
	// A downgrade already existed for output and nothing handled input, which meant a server declaring R4 rejected R4 data - a
	// client sending the legal R4 status "finished" got a 400 saying it was not in the value set. Accepting either release's
	// spelling and storing one is what makes the R4 default usable rather than merely honest.
	upgradeCodes(target)

	return target, nil
}

// upgradeCodes normalises inbound release-specific codes to the representation the structs hold.
//
// A switch rather than an interface method, deliberately. A method would put "which release did this code come from" onto every
// resource type, including the fourteen that have no such codes, and the one that forgot to implement it would silently accept
// anything.
func upgradeCodes(r Resource) {
	switch v := r.(type) {
	case *Encounter:
		v.Status = upgradeEncounterStatus(v.Status)
	}
}

// SupportedResourceTypes lists what this package can read and write.
func SupportedResourceTypes() []string {
	out := make([]string, 0, len(resourceConstructors))
	for name := range resourceConstructors {
		out = append(out, name)
	}
	// Sorted, because Go maps range randomly and this list reaches a capability statement - a document clients cache
	// and diff, which cannot reorder itself between requests.
	sort.Strings(out)

	return out
}

// resourceConstructors is the one list of resource types this package understands.
//
// One table rather than a switch beside a slice, and the reason is a bug this replaced: the two had drifted. Six types were added to the
// unmarshaller and to the search parameters, so the capability statement advertised them, while the router still consulted the older slice
// and answered 404 - a server promising in its own metadata exactly what it then refused.
//
// A map from name to constructor keeps the two answers derived from one fact. Adding a type here makes it parseable and supported at the
// same instant, and there is no second place to forget.
var resourceConstructors = map[string]func() Resource{
	"Patient": func() Resource { return &Patient{} },

	// The nineteen that completed R4. See resources_r4rest.go for why they were missing and why they are here now.
	"BiologicallyDerivedProduct":        func() Resource { return &BiologicallyDerivedProduct{} },
	"CoverageEligibilityResponse":       func() Resource { return &CoverageEligibilityResponse{} },
	"DocumentManifest":                  func() Resource { return &DocumentManifest{} },
	"MedicinalProductAuthorization":     func() Resource { return &MedicinalProductAuthorization{} },
	"MedicinalProductContraindication":  func() Resource { return &MedicinalProductContraindication{} },
	"MedicinalProductIndication":        func() Resource { return &MedicinalProductIndication{} },
	"MedicinalProductIngredient":        func() Resource { return &MedicinalProductIngredient{} },
	"MedicinalProductInteraction":       func() Resource { return &MedicinalProductInteraction{} },
	"MedicinalProductManufactured":      func() Resource { return &MedicinalProductManufactured{} },
	"MedicinalProductPackaged":          func() Resource { return &MedicinalProductPackaged{} },
	"MedicinalProductPharmaceutical":    func() Resource { return &MedicinalProductPharmaceutical{} },
	"MedicinalProductUndesirableEffect": func() Resource { return &MedicinalProductUndesirableEffect{} },
	"ResearchDefinition":                func() Resource { return &ResearchDefinition{} },
	"ResearchElementDefinition":         func() Resource { return &ResearchElementDefinition{} },
	"SubstanceNucleicAcid":              func() Resource { return &SubstanceNucleicAcid{} },
	"SubstancePolymer":                  func() Resource { return &SubstancePolymer{} },
	"SubstanceProtein":                  func() Resource { return &SubstanceProtein{} },
	"SubstanceReferenceInformation":     func() Resource { return &SubstanceReferenceInformation{} },
	"SubstanceSourceMaterial":           func() Resource { return &SubstanceSourceMaterial{} },
	"Encounter":                         func() Resource { return &Encounter{} },
	"Observation":                       func() Resource { return &Observation{} },
	"DiagnosticReport":                  func() Resource { return &DiagnosticReport{} },
	"Practitioner":                      func() Resource { return &Practitioner{} },
	"Organization":                      func() Resource { return &Organization{} },
	"Location":                          func() Resource { return &Location{} },
	"Specimen":                          func() Resource { return &Specimen{} },
	"ServiceRequest":                    func() Resource { return &ServiceRequest{} },
	"Condition":                         func() Resource { return &Condition{} },
	"MedicationRequest":                 func() Resource { return &MedicationRequest{} },
	"AllergyIntolerance":                func() Resource { return &AllergyIntolerance{} },
	"Immunization":                      func() Resource { return &Immunization{} },
	"Procedure":                         func() Resource { return &Procedure{} },
	"DocumentReference":                 func() Resource { return &DocumentReference{} },
	"Bundle":                            func() Resource { return &Bundle{} },
	"OperationOutcome":                  func() Resource { return &OperationOutcome{} },

	// Additional resource types for comprehensive healthcare integration.
	"Medication":               func() Resource { return &Medication{} },
	"MedicationStatement":      func() Resource { return &MedicationStatement{} },
	"MedicationDispense":       func() Resource { return &MedicationDispense{} },
	"MedicationAdministration": func() Resource { return &MedicationAdministration{} },
	"Coverage":                 func() Resource { return &Coverage{} },
	"Claim":                    func() Resource { return &Claim{} },
	"ExplanationOfBenefit":     func() Resource { return &ExplanationOfBenefit{} },
	"CarePlan":                 func() Resource { return &CarePlan{} },
	"CareTeam":                 func() Resource { return &CareTeam{} },
	"Goal":                     func() Resource { return &Goal{} },
	"Device":                   func() Resource { return &Device{} },
	"RelatedPerson":            func() Resource { return &RelatedPerson{} },
	"PractitionerRole":         func() Resource { return &PractitionerRole{} },
	"Appointment":              func() Resource { return &Appointment{} },
	"Consent":                  func() Resource { return &Consent{} },
	"Composition":              func() Resource { return &Composition{} },
	"FamilyMemberHistory":      func() Resource { return &FamilyMemberHistory{} },
	"Communication":            func() Resource { return &Communication{} },
	"Task":                     func() Resource { return &Task{} },
	"Provenance":               func() Resource { return &Provenance{} },
	"QuestionnaireResponse":    func() Resource { return &QuestionnaireResponse{} },
	"Media":                    func() Resource { return &Media{} },

	// Foundation & infrastructure
	"StructureDefinition":   func() Resource { return &StructureDefinition{} },
	"CapabilityStatement":   func() Resource { return &CapabilityStatement{} },
	"CodeSystem":            func() Resource { return &CodeSystem{} },
	"NamingSystem":          func() Resource { return &NamingSystem{} },
	"ImplementationGuide":   func() Resource { return &ImplementationGuide{} },
	"SearchParameter":       func() Resource { return &SearchParameter{} },
	"OperationDefinition":   func() Resource { return &OperationDefinition{} },
	"CompartmentDefinition": func() Resource { return &CompartmentDefinition{} },
	"GraphDefinition":       func() Resource { return &GraphDefinition{} },
	"StructureMap":          func() Resource { return &StructureMap{} },
	"MessageDefinition":     func() Resource { return &MessageDefinition{} },
	"MessageHeader":         func() Resource { return &MessageHeader{} },
	"Subscription":          func() Resource { return &Subscription{} },
	"Binary":                func() Resource { return &Binary{} },

	// Security
	"AuditEvent": func() Resource { return &AuditEvent{} },

	// Clinical
	"AdverseEvent":       func() Resource { return &AdverseEvent{} },
	"DetectedIssue":      func() Resource { return &DetectedIssue{} },
	"ClinicalImpression": func() Resource { return &ClinicalImpression{} },
	"RiskAssessment":     func() Resource { return &RiskAssessment{} },
	"BodyStructure":      func() Resource { return &BodyStructure{} },
	"Flag":               func() Resource { return &Flag{} },
	"List":               func() Resource { return &List{} },
	"EpisodeOfCare":      func() Resource { return &EpisodeOfCare{} },
	"NutritionOrder":     func() Resource { return &NutritionOrder{} },
	"VisionPrescription": func() Resource { return &VisionPrescription{} },
	"DeviceRequest":      func() Resource { return &DeviceRequest{} },
	"DeviceUseStatement": func() Resource { return &DeviceUseStatement{} },
	"SupplyRequest":      func() Resource { return &SupplyRequest{} },
	"SupplyDelivery":     func() Resource { return &SupplyDelivery{} },
	"RequestGroup":       func() Resource { return &RequestGroup{} },
	"GuidanceResponse":   func() Resource { return &GuidanceResponse{} },

	// Diagnostics
	"ImagingStudy":      func() Resource { return &ImagingStudy{} },
	"MolecularSequence": func() Resource { return &MolecularSequence{} },

	// Medications
	"MedicationKnowledge":        func() Resource { return &MedicationKnowledge{} },
	"ImmunizationEvaluation":     func() Resource { return &ImmunizationEvaluation{} },
	"ImmunizationRecommendation": func() Resource { return &ImmunizationRecommendation{} },

	// Financial
	"ClaimResponse":              func() Resource { return &ClaimResponse{} },
	"CoverageEligibilityRequest": func() Resource { return &CoverageEligibilityRequest{} },
	"EnrollmentRequest":          func() Resource { return &EnrollmentRequest{} },
	"EnrollmentResponse":         func() Resource { return &EnrollmentResponse{} },
	"PaymentNotice":              func() Resource { return &PaymentNotice{} },
	"PaymentReconciliation":      func() Resource { return &PaymentReconciliation{} },
	"Account":                    func() Resource { return &Account{} },
	"ChargeItem":                 func() Resource { return &ChargeItem{} },
	"ChargeItemDefinition":       func() Resource { return &ChargeItemDefinition{} },
	"Contract":                   func() Resource { return &Contract{} },
	"InsurancePlan":              func() Resource { return &InsurancePlan{} },
	"Invoice":                    func() Resource { return &Invoice{} },

	// Workflow & scheduling
	"Schedule":            func() Resource { return &Schedule{} },
	"Slot":                func() Resource { return &Slot{} },
	"AppointmentResponse": func() Resource { return &AppointmentResponse{} },
	"PlanDefinition":      func() Resource { return &PlanDefinition{} },
	"ActivityDefinition":  func() Resource { return &ActivityDefinition{} },
	"EventDefinition":     func() Resource { return &EventDefinition{} },
	"Questionnaire":       func() Resource { return &Questionnaire{} },

	// Research & evidence
	"ResearchStudy":           func() Resource { return &ResearchStudy{} },
	"ResearchSubject":         func() Resource { return &ResearchSubject{} },
	"Measure":                 func() Resource { return &Measure{} },
	"MeasureReport":           func() Resource { return &MeasureReport{} },
	"Library":                 func() Resource { return &Library{} },
	"Evidence":                func() Resource { return &Evidence{} },
	"EvidenceVariable":        func() Resource { return &EvidenceVariable{} },
	"RiskEvidenceSynthesis":   func() Resource { return &RiskEvidenceSynthesis{} },
	"EffectEvidenceSynthesis": func() Resource { return &EffectEvidenceSynthesis{} },

	// Additional clinical & admin
	"Endpoint":                func() Resource { return &Endpoint{} },
	"HealthcareService":       func() Resource { return &HealthcareService{} },
	"Group":                   func() Resource { return &Group{} },
	"Person":                  func() Resource { return &Person{} },
	"Linkage":                 func() Resource { return &Linkage{} },
	"Basic":                   func() Resource { return &Basic{} },
	"DeviceMetric":            func() Resource { return &DeviceMetric{} },
	"DeviceDefinition":        func() Resource { return &DeviceDefinition{} },
	"Substance":               func() Resource { return &Substance{} },
	"SubstanceSpecification":  func() Resource { return &SubstanceSpecification{} },
	"MedicinalProduct":        func() Resource { return &MedicinalProduct{} },
	"CommunicationRequest":    func() Resource { return &CommunicationRequest{} },
	"VerificationResult":      func() Resource { return &VerificationResult{} },
	"OrganizationAffiliation": func() Resource { return &OrganizationAffiliation{} },
	"CatalogEntry":            func() Resource { return &Catalog{} },
	"TestScript":              func() Resource { return &TestScript{} },
	"TestReport":              func() Resource { return &TestReport{} },
	"TerminologyCapabilities": func() Resource { return &TerminologyCapabilities{} },
	"ExampleScenario":         func() Resource { return &ExampleScenario{} },
	"ObservationDefinition":   func() Resource { return &ObservationDefinition{} },
	"SpecimenDefinition":      func() Resource { return &SpecimenDefinition{} },
}

// encounterStatusR4ToR5 maps R4's status value set onto R5's, for input.
//
// The downgrade above handles output. Nothing handled input, so a server declaring R4 rejected R4 data: a client sending the
// perfectly legal R4 status "finished" got a 400 saying it was not in the value set, which is the opposite of the defect the R4
// default was meant to fix. Rejecting valid data is worse than serving an unexpected shape, because the client cannot work around
// it at all.
//
// Two of these are lossy and it is worth being explicit about which. R5 removed "arrived" and "triaged" without replacements, so
// both become "in-progress" - the encounter is under way, which is true, but the distinction is gone and will not come back on
// output. That is a real loss of fidelity and the alternative is refusing messages a receiving system considers valid.
var encounterStatusR4ToR5 = map[string]string{
	"planned":          "planned",
	"arrived":          "in-progress",
	"triaged":          "in-progress",
	"in-progress":      "in-progress",
	"onleave":          "on-hold",
	"finished":         "completed",
	"cancelled":        "cancelled",
	"entered-in-error": "entered-in-error",
	"unknown":          "unknown",
}

// upgradeEncounterStatus normalises an inbound encounter status to the R5 code the structs hold.
//
// Only R4-only codes are translated. A code that exists in both value sets is left alone, so "planned" and "in-progress" are not
// round-tripped through a map for no reason - and more importantly, an R5 code arriving on an R4-configured server is still
// understood rather than rejected for not being R4.
func upgradeEncounterStatus(status string) string {
	// Already an R5 code: nothing to do. Checked first so a code valid in both sets never takes the translation path.
	if encounterStatusesR5[status] {
		return status
	}

	if mapped, ok := encounterStatusR4ToR5[status]; ok {
		return mapped
	}

	// Unrecognised in either set. Returned unchanged so validation reports it by name, rather than silently becoming
	// "unknown" - which would turn a typo into a stored encounter whose status nobody asked for.
	return status
}
