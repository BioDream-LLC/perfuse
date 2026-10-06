package publichealth

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The Reportability Response: what a public health agency (through AIMS, or its own system) answers an eICR with. It says
// whether the eICR was processed and, for each condition it triggered on, whether that condition is reportable, to which
// agency and how soon. eCR 2.1.2 defines it as a FHIR document, sent back as a message to the eICR's source endpoint.

const (
	rrValues     = "urn:oid:2.16.840.1.114222.4.5.274" // the RR value sets' code system (RRVS1 ...)
	rrCodes      = "urn:oid:2.16.840.1.114222.4.5.232" // the RR codes (RR4, RR7 ...)
	messageTypes = "http://hl7.org/fhir/us/ecr/CodeSystem/us-ph-message-types-codesystem"
	snomed       = "http://snomed.info/sct"
	loinc        = "http://loinc.org"
	ucum         = "http://unitsofmeasure.org"

	// EventCaseReport and EventReportabilityResponse are the MessageHeader events of the two eCR messages.
	EventCaseReport            = "eicr-case-report-message"
	EventReportabilityResponse = "reportability-response-message"
)

// The codes of the RR value sets an RR is built from, with the displays PHIN VADS gives them.
var rrDisplay = map[string]string{
	"RRVS1":  "Reportable",
	"RRVS2":  "May be reportable",
	"RRVS3":  "Not reportable",
	"RRVS4":  "No rule met",
	"RRVS5":  "Patient home address",
	"RRVS6":  "Provider facility address",
	"RRVS7":  "Both patient home address and provider facility address",
	"RRVS14": "Action required",
	"RRVS15": "Information only",
	"RRVS16": "Action requested",
	"RRVS17": "Immediate action required",
	"RRVS18": "Immediate action requested",
	"RRVS19": "eICR processed",
	"RRVS20": "eICR was processed - with a warning",
	"RRVS21": "eICR was processed - with a severe warning",
	"RRVS22": "eICR was not processed - error",
}

// Agency is the public health agency a test receiver plays: who answers, and how to reach it. eCR requires an agency's
// address and phone on every organisation in the response.
type Agency struct {
	Name       string `json:"name" yaml:"name"`
	Phone      string `json:"phone" yaml:"phone"`
	Email      string `json:"email,omitempty" yaml:"email,omitempty"`
	Line       string `json:"line" yaml:"line"`
	City       string `json:"city" yaml:"city"`
	State      string `json:"state" yaml:"state"`
	PostalCode string `json:"postalCode" yaml:"postalCode"`
	// Endpoint is the agency's own FHIR base, named as the source of the response.
	Endpoint string `json:"endpoint" yaml:"endpoint"`
}

// Missing lists what the agency configuration lacks that eCR requires.
func (a Agency) Missing() []string {
	var out []string
	for _, f := range []struct{ name, v string }{{"name", a.Name}, {"phone", a.Phone}, {"line", a.Line}, {"city", a.City},
		{"state", a.State}, {"postalCode", a.PostalCode}, {"endpoint", a.Endpoint}} {
		if strings.TrimSpace(f.v) == "" {
			out = append(out, f.name)
		}
	}
	return out
}

// ReportabilityResponse is what an RR says, read into plain fields.
type ReportabilityResponse struct {
	// ID is the RR document's own identifier.
	ID string `json:"id"`
	// EICR is the identifier of the eICR document it answers (the eICR Bundle's identifier).
	EICR string `json:"eicr"`
	// Status is the eICR processing status: RRVS19 processed, RRVS20/21 with warnings, RRVS22 not processed.
	Status        string `json:"status"`
	StatusDisplay string `json:"statusDisplay,omitempty"`
	// Received is when the agency received the eICR, if it said.
	Received string `json:"received,omitempty"`
	// Summary is the agency's plain-language summary for the clinician.
	Summary string `json:"summary,omitempty"`
	// Priority is the summary's action priority (RRVS14 to RRVS18).
	Priority   string         `json:"priority,omitempty"`
	Conditions []RRCondition  `json:"conditions"`
	Patient    string         `json:"patient,omitempty"`
	Document   map[string]any `json:"-"`
}

// Reportable reports whether any condition was determined reportable or possibly reportable.
func (r *ReportabilityResponse) Reportable() bool {
	for _, c := range r.Conditions {
		for _, d := range c.Determinations {
			if d.Determination == "RRVS1" || d.Determination == "RRVS2" {
				return true
			}
		}
	}
	return false
}

// RRCondition is one condition the agency considered, with what it decided per jurisdiction.
type RRCondition struct {
	System         string            `json:"system,omitempty"`
	Code           string            `json:"code,omitempty"`
	Display        string            `json:"display,omitempty"`
	Determinations []RRDetermination `json:"determinations"`
}

// RRDetermination is one jurisdiction's decision on a condition.
type RRDetermination struct {
	// Determination is RRVS1 reportable, RRVS2 may be reportable, RRVS3 not reportable, RRVS4 no rule met.
	Determination string `json:"determination"`
	Display       string `json:"display,omitempty"`
	Reason        string `json:"reason,omitempty"`
	// Location says whose address made it this jurisdiction's: RRVS5 patient, RRVS6 facility, RRVS7 both.
	Location string `json:"location,omitempty"`
	Agency   string `json:"agency,omitempty"`
	// Timeframe is how soon it must be reported, as UCUM ("24 h").
	Timeframe string `json:"timeframe,omitempty"`
}

// ParseRR reads a Reportability Response: an eCR message carrying one, or the RR document itself.
func ParseRR(raw []byte) (*ReportabilityResponse, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if doc["resourceType"] != "Bundle" {
		return nil, fmt.Errorf("expected a Bundle, got %v", doc["resourceType"])
	}
	if doc["type"] == "message" {
		header, _, err := messageParts(doc)
		if err != nil {
			return nil, err
		}
		if ev := messageEvent(header); ev != EventReportabilityResponse {
			return nil, fmt.Errorf("the message is a %q, not a reportability response", ev)
		}
		focus := focusDocument(doc, header)
		if focus == nil {
			return nil, fmt.Errorf("the message's focus is not an RR document in the message")
		}
		doc = focus
	}
	if doc["type"] != "document" {
		return nil, fmt.Errorf("expected an RR document or an eCR message, got a %v bundle", doc["type"])
	}
	entries := arr(doc, "entry")
	if len(entries) == 0 {
		return nil, fmt.Errorf("the document is empty")
	}
	comp, _ := entries[0].(map[string]any)["resource"].(map[string]any)
	if comp == nil || comp["resourceType"] != "Composition" || codeOf(comp["type"], loinc) != "88085-6" {
		return nil, fmt.Errorf("the document's first entry is not a Reportability Response composition (LOINC 88085-6)")
	}
	resolve := resolver(doc)
	out := &ReportabilityResponse{Document: doc, Conditions: []RRCondition{}}
	if id, ok := doc["identifier"].(map[string]any); ok {
		out.ID = str(id, "value")
	}
	if s, ok := comp["subject"].(map[string]any); ok {
		out.Patient = str(s, "reference")
	}
	for _, s := range arr(comp, "section") {
		sec := s.(map[string]any)
		switch codeOf(sec["code"], loinc) {
		case "88082-3": // the eICR section
			for _, ex := range arr(sec, "extension") {
				e := ex.(map[string]any)
				switch str(e, "url") {
				case ecrBase + "rr-eicr-processing-status-extension":
					for _, sub := range arr(e, "extension") {
						se := sub.(map[string]any)
						if str(se, "url") == "eICRProcessingStatus" {
							if obs := resolve(se["valueReference"]); obs != nil {
								out.Status = codeOf(obs["code"], rrValues)
								out.StatusDisplay = displayOf(obs["code"], rrValues)
							}
						}
					}
				case ecrBase + "rr-eicr-receipt-time-extension":
					out.Received = str(e, "valueDateTime")
				}
			}
			for _, en := range arr(sec, "entry") {
				ref := en.(map[string]any)
				if id, ok := ref["identifier"].(map[string]any); ok && out.EICR == "" {
					out.EICR = str(id, "value")
				}
				if dr := resolve(ref); dr != nil && out.EICR == "" {
					if mi, ok := dr["masterIdentifier"].(map[string]any); ok {
						out.EICR = str(mi, "value")
					}
				}
			}
		case "55112-7": // the summary section
			for _, ex := range arr(sec, "extension") {
				if e := ex.(map[string]any); str(e, "url") == ecrBase+"rr-priority-extension" {
					out.Priority = codeOf(e["valueCodeableConcept"], rrValues)
				}
			}
			for _, en := range arr(sec, "entry") {
				obs := resolve(en)
				if obs == nil {
					continue
				}
				switch {
				case codeOf(obs["code"], snomed) == "304561000":
					out.Summary = str(obs, "valueString")
				case codeOf(obs["code"], snomed) == "64572001" || codeOf(obs["code"], loinc) == "75323-6":
					out.Conditions = append(out.Conditions, readCondition(obs, resolve))
				}
			}
		}
	}
	if out.Status == "" {
		return nil, fmt.Errorf("the RR has no eICR processing status, which eCR requires")
	}
	return out, nil
}

func readCondition(obs map[string]any, resolve func(any) map[string]any) RRCondition {
	c := RRCondition{Determinations: []RRDetermination{}}
	if v, ok := obs["valueCodeableConcept"].(map[string]any); ok {
		for _, cd := range arr(v, "coding") {
			m := cd.(map[string]any)
			c.System, c.Code, c.Display = str(m, "system"), str(m, "code"), str(m, "display")
			break
		}
		if c.Display == "" {
			c.Display = str(v, "text")
		}
	}
	for _, mem := range arr(obs, "hasMember") {
		info := resolve(mem)
		if info == nil {
			continue
		}
		d := RRDetermination{Location: codeOf(info["code"], rrValues)}
		for _, ex := range arr(info, "extension") {
			e := ex.(map[string]any)
			switch str(e, "url") {
			case ecrBase + "us-ph-determination-of-reportability-extension":
				d.Determination = codeOf(e["valueCodeableConcept"], rrValues)
				d.Display = rrDisplay[d.Determination]
			case ecrBase + "us-ph-determination-of-reportability-reason-extension":
				d.Reason = str(e, "valueString")
			}
		}
		for _, p := range arr(info, "performer") {
			if org := resolve(p); org != nil && codeOf(firstOf(org["type"]), rrCodes) == "RR8" {
				d.Agency = str(org, "name")
			}
		}
		for _, comp := range arr(info, "component") {
			cm := comp.(map[string]any)
			if codeOf(cm["code"], rrCodes) == "RR4" {
				if q, ok := cm["valueQuantity"].(map[string]any); ok {
					d.Timeframe = strings.TrimSpace(fmt.Sprint(q["value"]) + " " + str(q, "code"))
				}
			}
		}
		c.Determinations = append(c.Determinations, d)
	}
	return c
}

// BuildRR is a test agency's answer to an eCR message: the Reportability Response document, wrapped in the eCR message that
// goes back to the eICR's source endpoint. Each condition the agency's trigger codes find in the eICR is answered reportable
// to the agency, within 24 hours for a condition Perfuse's built-in list marks immediate and 72 hours otherwise.
//
// These are a stand-in's rules, not a jurisdiction's: a real agency decides reportability with the RCKMS rules for the
// patient's and the facility's jurisdictions. What this checks is the exchange - that a sender's eICR is read, answered with
// an RR that eCR's profiles accept, and that the RR is linked back to the eICR it answers.
func BuildRR(message []byte, triggers *TriggerSet, agency Agency, now time.Time) (map[string]any, *ReportabilityResponse, error) {
	if triggers == nil {
		triggers = BuiltinTriggers()
	}
	if missing := agency.Missing(); len(missing) > 0 {
		return nil, nil, fmt.Errorf("the agency configuration is missing %s", strings.Join(missing, ", "))
	}
	if now.IsZero() {
		now = time.Now()
	}
	var msg map[string]any
	if err := json.Unmarshal(message, &msg); err != nil {
		return nil, nil, fmt.Errorf("not JSON: %w", err)
	}
	if msg["resourceType"] != "Bundle" || msg["type"] != "message" {
		return nil, nil, fmt.Errorf("expected an eCR message (a Bundle of type message)")
	}
	header, _, err := messageParts(msg)
	if err != nil {
		return nil, nil, err
	}
	if ev := messageEvent(header); ev != EventCaseReport {
		return nil, nil, fmt.Errorf("the message is a %q, not an eICR case report", ev)
	}
	replyTo := ""
	if src, ok := header["source"].(map[string]any); ok {
		replyTo = str(src, "endpoint")
	}
	if replyTo == "" {
		return nil, nil, fmt.Errorf("the message has no source endpoint to send the response to")
	}
	eicr := focusDocument(msg, header)
	if eicr == nil {
		return nil, nil, fmt.Errorf("the message's focus is not an eICR document in the message")
	}
	eicrID := ""
	if id, ok := eicr["identifier"].(map[string]any); ok {
		eicrID = str(id, "value")
	}
	entries := arr(eicr, "entry")
	comp, _ := entries[0].(map[string]any)["resource"].(map[string]any)
	if comp == nil || comp["resourceType"] != "Composition" || codeOf(comp["type"], loinc) != "55751-2" || eicrID == "" {
		return nil, nil, fmt.Errorf("the focus is not an eICR (a document with an identifier whose composition is LOINC 55751-2)")
	}
	resolve := resolver(eicr)
	patient := resolve(comp["subject"])
	facility := resolve(comp["custodian"])
	encounter := resolve(comp["encounter"])
	if patient == nil || facility == nil || encounter == nil {
		return nil, nil, fmt.Errorf("the eICR's patient, encounter or custodian is not in the document")
	}
	raw, _ := json.Marshal(eicr)
	found, err := FindTriggers(raw, triggers)
	if err != nil {
		return nil, nil, err
	}

	rrID := fhir.DeterministicUUID("rr-document", eicrID)
	u := func(kind string) string { return "urn:uuid:" + fhir.DeterministicUUID("rr-"+kind, eicrID) }
	stamp := now.UTC().Format(time.RFC3339)
	patientURL, facilityURL, encounterURL := urlOf(eicr, patient), urlOf(eicr, facility), urlOf(eicr, encounter)

	agencyOrg := func(kind, code, display string) map[string]any {
		o := map[string]any{
			"resourceType": "Organization",
			"id":           fhir.DeterministicUUID("rr-org-"+kind, agency.Name),
			"meta":         profile(ecrBase + "us-ph-organization"),
			"active":       true,
			"name":         agency.Name,
			"telecom":      agencyTelecom(agency),
			"address": []any{map[string]any{"line": []any{agency.Line}, "city": agency.City, "state": agency.State,
				"postalCode": agency.PostalCode, "country": "US"}},
		}
		if code != "" {
			o["meta"] = profile(ecrBase + "rr-" + kind + "-organization")
			o["type"] = []any{map[string]any{"coding": []any{map[string]any{"system": rrCodes, "code": code, "display": display}}}}
		}
		return o
	}
	author := agencyOrg("author", "", "")
	authoring := agencyOrg("rules-authoring-agency", "RR12", "Rules Authoring Agency")
	routing := agencyOrg("routing-entity", "RR7", "Routing Entity")
	responsible := agencyOrg("responsible-agency", "RR8", "Responsible Agency")
	orgURL := func(o map[string]any) string { return "urn:uuid:" + str(o, "id") }

	// Whose address makes the condition this agency's: the patient's home, the facility, or both.
	location := "RRVS6"
	patientHere, facilityHere := inState(patient, agency.State), inState(facility, agency.State)
	switch {
	case patientHere && facilityHere:
		location = "RRVS7"
	case patientHere:
		location = "RRVS5"
	}

	out := &ReportabilityResponse{ID: "urn:uuid:" + rrID, EICR: eicrID, Status: "RRVS19", StatusDisplay: rrDisplay["RRVS19"],
		Received: stamp, Patient: patientURL, Conditions: []RRCondition{}}
	var extra []any // condition and reportability-information observations, after the composition's own entries
	var conditionRefs []any
	var names []string
	urgent := false
	for _, cond := range distinctConditions(found) {
		hours := 72
		if cond.immediate {
			hours, urgent = 24, true
		}
		infoURL := u("info-" + cond.key)
		info := map[string]any{
			"resourceType": "Observation",
			"id":           strings.TrimPrefix(infoURL, "urn:uuid:"),
			"meta":         profile(ecrBase + "rr-reportability-information-observation"),
			"extension": []any{
				map[string]any{"url": ecrBase + "us-ph-determination-of-reportability-extension", "valueCodeableConcept": rrConcept("RRVS1")},
				map[string]any{"url": ecrBase + "us-ph-determination-of-reportability-reason-extension",
					"valueString": "A trigger code for this condition was found in the eICR (test agency rule)"},
			},
			"status": "final",
			"code":   rrConcept(location),
			"performer": []any{
				map[string]any{"reference": orgURL(authoring), "display": agency.Name},
				map[string]any{"reference": orgURL(routing), "display": agency.Name},
				map[string]any{"reference": orgURL(responsible), "display": agency.Name},
			},
			"component": []any{map[string]any{
				"code":          map[string]any{"coding": []any{map[string]any{"system": rrCodes, "code": "RR4", "display": "Timeframe to report (urgency)"}}},
				"valueQuantity": map[string]any{"value": hours, "unit": "h", "system": ucum, "code": "h"},
			}},
		}
		condURL := u("condition-" + cond.key)
		value := map[string]any{"text": cond.name}
		if cond.snomed != "" {
			value["coding"] = []any{map[string]any{"system": snomed, "code": cond.snomed}}
		}
		condition := map[string]any{
			"resourceType": "Observation",
			"id":           strings.TrimPrefix(condURL, "urn:uuid:"),
			"meta":         profile(ecrBase + "rr-relevant-reportable-condition-observation"),
			"status":       "final",
			"code": map[string]any{"coding": []any{
				map[string]any{"system": snomed, "code": "64572001"}, map[string]any{"system": loinc, "code": "75323-6"},
			}, "text": "Condition"},
			"valueCodeableConcept": value,
			"hasMember":            []any{map[string]any{"reference": infoURL}},
		}
		extra = append(extra, map[string]any{"fullUrl": condURL, "resource": condition}, map[string]any{"fullUrl": infoURL, "resource": info})
		conditionRefs = append(conditionRefs, map[string]any{"reference": condURL, "display": cond.name})
		names = append(names, cond.name)
		out.Conditions = append(out.Conditions, RRCondition{System: map[bool]string{true: snomed}[cond.snomed != ""], Code: cond.snomed,
			Display: cond.name, Determinations: []RRDetermination{{Determination: "RRVS1", Display: rrDisplay["RRVS1"], Location: location,
				Agency: agency.Name, Timeframe: fmt.Sprintf("%d h", hours)}}})
	}

	statusURL := u("status")
	status := map[string]any{
		"resourceType": "Observation",
		"id":           strings.TrimPrefix(statusURL, "urn:uuid:"),
		"meta":         profile(ecrBase + "rr-eicr-processing-status-observation"),
		"status":       "final",
		"code":         rrConcept("RRVS19"),
	}
	eicrRefURL := u("eicr-reference")
	eicrRef := map[string]any{
		"resourceType":     "DocumentReference",
		"id":               strings.TrimPrefix(eicrRefURL, "urn:uuid:"),
		"masterIdentifier": map[string]any{"system": "urn:ietf:rfc:3986", "value": eicrID},
		"status":           "current",
		"type":             map[string]any{"coding": []any{map[string]any{"system": loinc, "code": "55751-2"}}, "text": "Public health case report"},
		"subject":          map[string]any{"reference": patientURL},
		"content":          []any{map[string]any{"attachment": map[string]any{"contentType": "application/fhir+json", "url": eicrID}}},
	}

	subjectText := "No condition this agency's trigger codes cover was found in the case report."
	if len(names) > 0 {
		subjectText = "Public Health Reporting Communication: one or more conditions are reportable, or may be reportable, to public health."
	}
	sections := []any{
		map[string]any{
			"title": "Reportability Response Subject Section",
			"code":  map[string]any{"coding": []any{map[string]any{"system": loinc, "code": "88084-9"}}},
			"text":  narrative(subjectText),
		},
		map[string]any{
			"title": "Electronic Initial Case Report Section",
			"extension": []any{
				map[string]any{"url": ecrBase + "rr-eicr-processing-status-extension", "extension": []any{
					map[string]any{"url": "eICRProcessingStatus", "valueReference": map[string]any{"reference": statusURL}},
				}},
				map[string]any{"url": ecrBase + "rr-eicr-receipt-time-extension", "valueDateTime": stamp},
			},
			"code":  map[string]any{"coding": []any{map[string]any{"system": loinc, "code": "88082-3"}}},
			"entry": []any{map[string]any{"reference": eicrRefURL, "identifier": map[string]any{"system": "urn:ietf:rfc:3986", "value": eicrID}}},
		},
	}
	if len(names) > 0 {
		priority := "RRVS16"
		if urgent {
			priority = "RRVS18"
		}
		out.Priority = priority
		out.Summary = fmt.Sprintf("Your organization electronically submitted an initial case report. %s %s reportable to %s.",
			strings.Join(names, ", "), map[bool]string{true: "is", false: "are"}[len(names) == 1], agency.Name)
		summaryURL := u("summary")
		summary := map[string]any{
			"resourceType": "Observation",
			"id":           strings.TrimPrefix(summaryURL, "urn:uuid:"),
			"meta":         profile(ecrBase + "rr-summary"),
			"status":       "final",
			"code":         map[string]any{"coding": []any{map[string]any{"system": snomed, "code": "304561000", "display": "Informing health care professional (procedure)"}}},
			"valueString":  out.Summary,
		}
		sections = append(sections, map[string]any{
			"title":     "Reportability Response Summary Section",
			"extension": []any{map[string]any{"url": ecrBase + "rr-priority-extension", "valueCodeableConcept": rrConcept(priority)}},
			"code":      map[string]any{"coding": []any{map[string]any{"system": loinc, "code": "55112-7"}}},
			"text":      narrative(out.Summary),
			"entry":     append([]any{map[string]any{"reference": summaryURL}}, conditionRefs...),
		})
		extra = append([]any{map[string]any{"fullUrl": summaryURL, "resource": summary}}, extra...)
	}

	composition := map[string]any{
		"resourceType": "Composition",
		"id":           rrID,
		"meta":         profile(ecrBase + "rr-composition"),
		"text":         narrative("Reportability Response: " + subjectText),
		"extension": []any{
			map[string]any{"url": "http://hl7.org/fhir/StructureDefinition/composition-clinicaldocument-versionNumber", "valueString": "1"},
			map[string]any{"url": ecrBase + "us-ph-information-recipient-extension", "valueReference": map[string]any{"reference": facilityURL}},
		},
		"identifier": map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + rrID},
		"status":     "final",
		"type":       map[string]any{"coding": []any{map[string]any{"system": loinc, "code": "88085-6"}}},
		"subject":    map[string]any{"reference": patientURL},
		"encounter":  map[string]any{"reference": encounterURL},
		"date":       stamp,
		"author":     []any{map[string]any{"reference": orgURL(author)}},
		"title":      "Reportability Response",
		"custodian":  map[string]any{"reference": orgURL(author)},
		"relatesTo": []any{map[string]any{"code": "transforms", "targetIdentifier": map[string]any{
			"system": "urn:ietf:rfc:3986", "value": eicrID}}},
		"section": sections,
	}
	docEntries := []any{map[string]any{"fullUrl": "urn:uuid:" + rrID, "resource": composition}}
	docEntries = append(docEntries,
		map[string]any{"fullUrl": patientURL, "resource": patient},
		map[string]any{"fullUrl": facilityURL, "resource": facility},
		map[string]any{"fullUrl": encounterURL, "resource": encounter},
		map[string]any{"fullUrl": statusURL, "resource": status},
		map[string]any{"fullUrl": eicrRefURL, "resource": eicrRef},
		map[string]any{"fullUrl": orgURL(author), "resource": author},
	)
	docEntries = append(docEntries, extra...)
	// The encounter is eCR's eicr-encounter, which needs its location; what it points to comes too, so the RR resolves in itself.
	have := map[string]bool{}
	for _, e := range docEntries {
		have[str(e.(map[string]any), "fullUrl")] = true
	}
	for _, ref := range referencesIn(encounter) {
		if r := resolve(map[string]any{"reference": ref}); r != nil {
			if u := urlOf(eicr, r); !have[u] {
				have[u] = true
				docEntries = append(docEntries, map[string]any{"fullUrl": u, "resource": r})
			}
		}
	}
	if len(names) > 0 {
		for _, o := range []map[string]any{authoring, routing, responsible} {
			docEntries = append(docEntries, map[string]any{"fullUrl": orgURL(o), "resource": o})
		}
	}
	document := map[string]any{
		"resourceType": "Bundle",
		"meta":         profile(ecrBase + "rr-document-bundle"),
		"identifier":   map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + fhir.DeterministicUUID("rr-bundle", eicrID)},
		"type":         "document",
		"timestamp":    stamp,
		"entry":        docEntries,
	}
	out.ID = str(document["identifier"].(map[string]any), "value")
	out.Document = document

	// The message: from the agency, to the eICR's source endpoint, in response to the eICR's message.
	docURL := u("document-entry")
	headerID := fhir.DeterministicUUID("rr-header", eicrID)
	reason := header["reason"]
	if reason == nil {
		reason = map[string]any{"coding": []any{map[string]any{
			"system": "http://hl7.org/fhir/us/ecr/CodeSystem/us-ph-triggerdefinition-namedevents", "code": "manual-notification"}}}
	}
	rh := map[string]any{
		"resourceType": "MessageHeader",
		"id":           headerID,
		"meta":         profile(ecrBase + "us-ph-messageheader"),
		"extension":    []any{map[string]any{"url": ecrBase + "us-ph-message-processing-category-extension", "valueCode": "notification"}},
		"eventCoding":  map[string]any{"system": messageTypes, "code": EventReportabilityResponse},
		"destination":  []any{map[string]any{"endpoint": replyTo}},
		"sender":       map[string]any{"reference": orgURL(author)},
		"source":       map[string]any{"name": agency.Name, "software": "Perfuse (test agency)", "endpoint": agency.Endpoint},
		"reason":       reason,
		"response":     map[string]any{"identifier": str(header, "id"), "code": "ok"},
		"focus":        []any{map[string]any{"reference": docURL}},
	}
	reply := map[string]any{
		"resourceType": "Bundle",
		"meta":         profile(ecrBase + "us-ph-reporting-bundle"),
		"identifier":   map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:uuid:" + fhir.DeterministicUUID("rr-message", eicrID)},
		"type":         "message",
		"timestamp":    stamp,
		"entry": []any{
			map[string]any{"fullUrl": "urn:uuid:" + headerID, "resource": rh},
			map[string]any{"fullUrl": docURL, "resource": document},
			map[string]any{"fullUrl": orgURL(author), "resource": author},
		},
	}
	return reply, out, nil
}

// MessageEvent names an eCR message's event (eicr-case-report-message, reportability-response-message), or says why it has none.
func MessageEvent(raw []byte) (string, error) {
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", fmt.Errorf("not JSON: %w", err)
	}
	if msg["resourceType"] != "Bundle" || msg["type"] != "message" {
		return "", fmt.Errorf("$process-message takes a Bundle of type message")
	}
	header, _, err := messageParts(msg)
	if err != nil {
		return "", err
	}
	return messageEvent(header), nil
}

// RRStorageID is the id an RR document is stored under, derived from the eICR it answers, so the response to a case report is at
// an address known when the report is sent.
func RRStorageID(eicrIdentifier string) string {
	return fhir.DeterministicUUID("reportability-response", eicrIdentifier)
}

type rrConditionKey struct {
	key, name, snomed string
	immediate         bool
}

// distinctConditions groups trigger codes by condition, with a SNOMED code for each where one is known: the trigger's own, or
// the built-in list's for the same condition. The RR's condition value must be SNOMED; without one it is text.
func distinctConditions(ts []Trigger) []rrConditionKey {
	byName := map[string]*rrConditionKey{}
	var order []string
	for _, t := range ts {
		name := t.Condition
		if name == "" {
			name = t.Display
		}
		if name == "" {
			name = t.Code
		}
		c, ok := byName[name]
		if !ok {
			c = &rrConditionKey{key: fhir.DeterministicUUID("rr-condition", name), name: name}
			byName[name] = c
			order = append(order, name)
		}
		if t.System == snomed && c.snomed == "" {
			c.snomed = t.Code
		}
	}
	for _, e := range ReportableConditions {
		if c, ok := byName[e.Condition]; ok {
			if c.snomed == "" && e.System == "SNOMED" {
				c.snomed = e.Code
			}
			if e.Urgency == "immediate" {
				c.immediate = true
			}
		}
	}
	sort.Strings(order)
	out := make([]rrConditionKey, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

func messageParts(msg map[string]any) (header map[string]any, rest []any, err error) {
	entries := arr(msg, "entry")
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("the message is empty")
	}
	header, _ = entries[0].(map[string]any)["resource"].(map[string]any)
	if header == nil || header["resourceType"] != "MessageHeader" {
		return nil, nil, fmt.Errorf("a message's first entry must be its MessageHeader")
	}
	return header, entries[1:], nil
}

func messageEvent(header map[string]any) string {
	if ev, ok := header["eventCoding"].(map[string]any); ok {
		return str(ev, "code")
	}
	return str(header, "eventUri")
}

func focusDocument(msg, header map[string]any) map[string]any {
	focus := arr(header, "focus")
	if len(focus) == 0 {
		return nil
	}
	ref := str(focus[0].(map[string]any), "reference")
	for _, e := range arr(msg, "entry") {
		em := e.(map[string]any)
		if r, ok := em["resource"].(map[string]any); ok && r["resourceType"] == "Bundle" &&
			(str(em, "fullUrl") == ref || "Bundle/"+str(r, "id") == ref) {
			return r
		}
	}
	return nil
}

// resolver finds a referenced resource inside a bundle, by fullUrl or by Type/id.
func resolver(bundle map[string]any) func(any) map[string]any {
	byRef := map[string]map[string]any{}
	for _, e := range arr(bundle, "entry") {
		em := e.(map[string]any)
		r, ok := em["resource"].(map[string]any)
		if !ok {
			continue
		}
		byRef[str(em, "fullUrl")] = r
		byRef[str(r, "resourceType")+"/"+str(r, "id")] = r
		if full := str(em, "fullUrl"); full != "" {
			if parts := strings.Split(full, "/"); len(parts) >= 2 {
				byRef[parts[len(parts)-2]+"/"+parts[len(parts)-1]] = r
			}
		}
	}
	return func(ref any) map[string]any {
		m, _ := ref.(map[string]any)
		if m == nil {
			return nil
		}
		return byRef[str(m, "reference")]
	}
}

func urlOf(bundle, resource map[string]any) string {
	for _, e := range arr(bundle, "entry") {
		em := e.(map[string]any)
		if r, _ := em["resource"].(map[string]any); r != nil && str(r, "resourceType") == str(resource, "resourceType") && str(r, "id") == str(resource, "id") {
			return str(em, "fullUrl")
		}
	}
	return str(resource, "resourceType") + "/" + str(resource, "id")
}

func inState(r map[string]any, state string) bool {
	for _, a := range arr(r, "address") {
		if strings.EqualFold(strings.TrimSpace(str(a.(map[string]any), "state")), strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func agencyTelecom(a Agency) []any {
	t := []any{map[string]any{"system": "phone", "value": a.Phone}}
	if a.Email != "" {
		t = append(t, map[string]any{"system": "email", "value": a.Email})
	}
	return t
}

func rrConcept(code string) map[string]any {
	return map[string]any{"coding": []any{map[string]any{"system": rrValues, "code": code, "display": rrDisplay[code]}}}
}

func firstOf(v any) any {
	if a, ok := v.([]any); ok && len(a) > 0 {
		return a[0]
	}
	return nil
}

func codeOf(cc any, system string) string {
	m, _ := cc.(map[string]any)
	for _, c := range arr(m, "coding") {
		if cm := c.(map[string]any); str(cm, "system") == system {
			return str(cm, "code")
		}
	}
	return ""
}

func displayOf(cc any, system string) string {
	m, _ := cc.(map[string]any)
	for _, c := range arr(m, "coding") {
		if cm := c.(map[string]any); str(cm, "system") == system {
			if d := str(cm, "display"); d != "" {
				return d
			}
			return rrDisplay[str(cm, "code")]
		}
	}
	return ""
}

// referencesIn lists every reference string in a resource.
func referencesIn(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["reference"].(string); ok {
			out = append(out, r)
		}
		for _, x := range t {
			out = append(out, referencesIn(x)...)
		}
	case []any:
		for _, x := range t {
			out = append(out, referencesIn(x)...)
		}
	}
	return out
}
