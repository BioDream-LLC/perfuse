package epa

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/formulary"
)

type outMessage struct {
	XMLName xml.Name   `xml:"Message"`
	Attrs   []xml.Attr `xml:",any,attr"`
	Header  outHeader  `xml:"Header"`
	Body    outBody    `xml:"Body"`
}

type outHeader struct {
	To                 Endpoint `xml:"To"`
	From               Endpoint `xml:"From"`
	MessageID          string   `xml:"MessageID"`
	RelatesToMessageID string   `xml:"RelatesToMessageID"`
	SentTime           string   `xml:"SentTime"`
}

type outBody struct {
	InitiationResponse *outPA `xml:"PAInitiationResponse,omitempty"`
	Response           *outPA `xml:"PAResponse,omitempty"`
	AppealResponse     *outPA `xml:"PAAppealResponse,omitempty"`
	CancelResponse     *outPA `xml:"PACancelResponse,omitempty"`
}

type outPA struct {
	PAReferenceID        string      `xml:"PAReferenceID"`
	BenefitsCoordination []Raw       `xml:"BenefitsCoordination"`
	Patient              *Raw        `xml:"Patient,omitempty"`
	Pharmacy             *Raw        `xml:"Pharmacy,omitempty"`
	Prescriber           *Raw        `xml:"Prescriber,omitempty"`
	Medication           *Raw        `xml:"MedicationPrescribed,omitempty"`
	Response             outResponse `xml:"Response"`
}

type outResponse struct {
	Status outStatus `xml:"ResponseStatus"`
}

type outStatus struct {
	Open     *Open     `xml:"Open,omitempty"`
	Approved *Approved `xml:"Approved,omitempty"`
	Denied   *Denied   `xml:"Denied,omitempty"`
	Closed   *Closed   `xml:"Closed,omitempty"`
}

func raw(inner string) *Raw {
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	return &Raw{Inner: inner}
}

// reply writes the response to a request: addressed back to its sender, quoting its MessageID, echoing who and which
// drug as the request gave them, and carrying the status.
func (s *Service) reply(m *Message, pa *PA, kind Kind, _ *ResponseDetail, status any) []byte {
	now := s.now()
	from := m.Header.To
	if s.PayerID != "" {
		from = Endpoint{Qualifier: "PY", Value: s.PayerID}
	}
	body := &outPA{PAReferenceID: pa.PAReferenceID, Patient: raw(pa.Patient.Inner), Prescriber: raw(pa.Prescriber.Inner),
		Medication: raw(pa.Medication.Inner)}
	for _, b := range pa.BenefitsCoordination {
		if r := raw(b.Inner); r != nil {
			body.BenefitsCoordination = append(body.BenefitsCoordination, *r)
		}
	}
	if pa.Pharmacy != nil {
		body.Pharmacy = raw(pa.Pharmacy.Inner)
	}
	switch v := status.(type) {
	case *Open:
		body.Response.Status.Open = v
	case *Approved:
		body.Response.Status.Approved = v
	case *Denied:
		body.Response.Status.Denied = v
	case *Closed:
		body.Response.Status.Closed = v
	}
	out := outMessage{Attrs: m.Attrs, Header: outHeader{To: m.Header.From, From: from, MessageID: newCaseID() + strconv.FormatInt(now.UnixMilli(), 36),
		RelatesToMessageID: m.Header.MessageID, SentTime: now.Format("2006-01-02T15:04:05.0Z")}}
	switch kind {
	case InitiationResponse:
		out.Body.InitiationResponse = body
	case Response:
		out.Body.Response = body
	case AppealResponse:
		out.Body.AppealResponse = body
	case CancelResponse:
		out.Body.CancelResponse = body
	}
	b, err := xml.MarshalIndent(out, "", "  ")
	if err != nil {
		// Every field is a string or a struct of strings, so this cannot happen; say so loudly if it ever does.
		panic(fmt.Sprintf("epa: writing a response: %v", err))
	}
	return append([]byte(xml.Header), append(b, '\n')...)
}

// toPAS turns an ePA request into a Da Vinci PAS request Bundle: a Claim for the drug, the MedicationRequest it asks for,
// the patient, prescriber, payer and coverage, and the answers as a QuestionnaireResponse. A decision service reading PAS
// sees a drug request exactly as it sees one from an EHR's PAS client.
func toPAS(pa *PA, plan *formulary.Plan, d *formulary.Drug, caseID string, answers *QuestionSet, appeal bool, now time.Time) map[string]any {
	base := "urn:uuid:"
	id := func(kind string) string { return base + caseID + "-" + kind }
	member, _ := pa.Member()
	name := Name{}
	if n := pa.Patient.Human.Names.Name; len(n) > 0 {
		name = n[0]
	}
	patient := map[string]any{"resourceType": "Patient", "identifier": []any{map[string]any{"value": member}},
		"name":   []any{map[string]any{"family": titleCase(name.LastName), "given": nonEmpty(titleCase(name.FirstName))}},
		"gender": gender(pa.Patient.Human.Gender)}
	if dob := pa.Patient.Human.DateOfBirth; dob != "" {
		patient["birthDate"] = dob
	}
	pn := Name{}
	if n := pa.Prescriber.NonVeterinarian.Names.Name; len(n) > 0 {
		pn = n[0]
	}
	prescriber := map[string]any{"resourceType": "Practitioner",
		"identifier": []any{map[string]any{"system": "http://hl7.org/fhir/sid/us-npi", "value": pa.Prescriber.NonVeterinarian.NPI}},
		"name":       []any{map[string]any{"family": titleCase(pn.LastName), "given": nonEmpty(titleCase(pn.FirstName))}}}
	payer := map[string]any{"resourceType": "Organization", "name": "Payer", "identifier": []any{map[string]any{"value": plan.ID}}}
	coverage := map[string]any{"resourceType": "Coverage", "status": "active", "subscriberId": member,
		"beneficiary": map[string]any{"reference": id("patient")}, "payor": []any{map[string]any{"reference": id("payer")}},
		"class": []any{map[string]any{"type": map[string]any{"coding": []any{map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/coverage-class", "code": "plan"}}}, "value": plan.ID, "name": plan.Name}}}
	drugCode := map[string]any{"coding": []any{map[string]any{"system": formulary.RxNorm, "code": d.RxNorm, "display": d.Display}}}
	if ndc := strings.TrimSpace(pa.Medication.Product.DrugCoded.NDC); ndc != "" {
		drugCode["coding"] = append(drugCode["coding"].([]any), map[string]any{"system": formulary.NDC, "code": ndc})
	}
	quantity, _ := strconv.ParseFloat(strings.TrimSpace(pa.Medication.Quantity.Value), 64)
	days, _ := strconv.Atoi(strings.TrimSpace(pa.Medication.DaysSupply))
	order := map[string]any{"resourceType": "MedicationRequest", "status": "active", "intent": "order",
		"medicationCodeableConcept": drugCode, "subject": map[string]any{"reference": id("patient")},
		"requester": map[string]any{"reference": id("prescriber")}, "authoredOn": now.Format("2006-01-02")}
	if pa.Medication.Sig != "" {
		order["dosageInstruction"] = []any{map[string]any{"text": pa.Medication.Sig}}
	}
	dispense := map[string]any{}
	if quantity > 0 {
		dispense["quantity"] = map[string]any{"value": quantity}
	}
	if days > 0 {
		dispense["expectedSupplyDuration"] = map[string]any{"value": days, "unit": "days", "system": "http://unitsofmeasure.org", "code": "d"}
	}
	if len(dispense) > 0 {
		order["dispenseRequest"] = dispense
	}
	var diagnoses []any
	var reasons []any
	for i, dx := range pa.Medication.Diagnosis {
		if dx.Value == "" {
			continue
		}
		code := icd10(dx.Value)
		cc := map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": code, "display": dx.Description}}}
		reasons = append(reasons, cc)
		diagnoses = append(diagnoses, map[string]any{"sequence": i + 1, "diagnosisCodeableConcept": cc})
	}
	if len(reasons) > 0 {
		order["reasonCode"] = reasons
	}
	qty := map[string]any{"value": quantity}
	if quantity <= 0 {
		qty["value"] = 1
	}
	item := map[string]any{"sequence": 1, "productOrService": drugCode, "quantity": qty,
		"extension": []any{map[string]any{"url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService",
			"valueReference": map[string]any{"reference": id("order")}}}}
	claim := map[string]any{"resourceType": "Claim", "status": "active", "use": "preauthorization",
		"identifier": []any{map[string]any{"system": "https://perfuse.health/ncpdp/PACaseID", "value": caseID}},
		"type":       map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/claim-type", "code": "pharmacy"}}},
		"patient":    map[string]any{"reference": id("patient")}, "created": now.Format(time.RFC3339),
		"insurer": map[string]any{"reference": id("payer")}, "provider": map[string]any{"reference": id("prescriber")},
		"priority":  map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/processpriority", "code": map[bool]string{true: "stat", false: "normal"}[expedited(pa)]}}},
		"insurance": []any{map[string]any{"sequence": 1, "focal": true, "coverage": map[string]any{"reference": id("coverage")}}},
		"item":      []any{item}}
	if len(diagnoses) > 0 {
		claim["diagnosis"] = diagnoses
	}
	entries := []any{
		map[string]any{"fullUrl": id("claim"), "resource": claim},
		map[string]any{"fullUrl": id("patient"), "resource": patient},
		map[string]any{"fullUrl": id("prescriber"), "resource": prescriber},
		map[string]any{"fullUrl": id("payer"), "resource": payer},
		map[string]any{"fullUrl": id("coverage"), "resource": coverage},
		map[string]any{"fullUrl": id("order"), "resource": order},
	}
	if a := answers.Read(); len(a) > 0 {
		qr := AnswersResponse(a, d.Questionnaire, id("patient"), now)
		entries = append(entries, map[string]any{"fullUrl": id("answers"), "resource": qr})
		claim["supportingInfo"] = []any{map[string]any{"sequence": 1,
			"category":       map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASSupportingInfoType", "code": "questionnaire"}}},
			"valueReference": map[string]any{"reference": id("answers")}}}
	}
	for i, at := range pa.Attachments {
		if strings.TrimSpace(at.Data) == "" {
			continue
		}
		doc := map[string]any{"resourceType": "DocumentReference", "status": "current",
			"subject": map[string]any{"reference": id("patient")}, "description": at.Source,
			"content": []any{map[string]any{"attachment": map[string]any{"contentType": firstNonEmpty(at.MIMEType, "application/octet-stream"), "data": strings.TrimSpace(at.Data)}}}}
		key := fmt.Sprintf("attachment-%d", i+1)
		entries = append(entries, map[string]any{"fullUrl": id(key), "resource": doc})
		claim["supportingInfo"] = append(list(claim["supportingInfo"]), map[string]any{"sequence": len(list(claim["supportingInfo"])) + 1,
			"category":       map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASSupportingInfoType", "code": "additionalInformation"}}},
			"valueReference": map[string]any{"reference": id(key)}})
	}
	if appeal {
		claim["identifier"] = append(list(claim["identifier"]), map[string]any{"system": "https://perfuse.health/ncpdp/appeal", "value": caseID})
	}
	return map[string]any{"resourceType": "Bundle", "type": "collection", "timestamp": now.Format(time.RFC3339), "entry": entries,
		"identifier": map[string]any{"system": "https://perfuse.health/ncpdp/PAReferenceID", "value": pa.PAReferenceID}}
}

// addAppealNote puts the prescriber's reason for an appeal on the Claim, and links it to the request it appeals.
func addAppealNote(bundle map[string]any, note, priorPAS string) {
	claim := list(bundle["entry"])[0].(map[string]any)["resource"].(map[string]any)
	claim["supportingInfo"] = append(list(claim["supportingInfo"]), map[string]any{"sequence": len(list(claim["supportingInfo"])) + 1,
		"category":    map[string]any{"coding": []any{map[string]any{"system": "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASSupportingInfoType", "code": "freeFormMessage"}}},
		"valueString": "Appeal: " + note})
	if priorPAS != "" {
		claim["related"] = []any{map[string]any{"claim": map[string]any{"identifier": map[string]any{"value": priorPAS}},
			"relationship": map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/related-claim-relationship", "code": "prior"}}}}}
	}
}

// icd10 puts the dot back in an ICD-10-CM code: SCRIPT sends E119, FHIR's code system writes E11.9.
func icd10(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) > 3 && !strings.Contains(code, ".") {
		return code[:3] + "." + code[3:]
	}
	return code
}

func gender(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "M":
		return "male"
	case "F":
		return "female"
	case "N":
		return "other"
	}
	return "unknown"
}

func titleCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ToUpper(s) != s {
		return s
	}
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func nonEmpty(s string) []any {
	if s == "" {
		return nil
	}
	return []any{s}
}
