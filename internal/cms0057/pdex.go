package cms0057

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Prior authorisations, for the Patient Access, Provider Access and Payer-to-Payer APIs.
//
// CMS-0057 requires all three to make prior authorisation information available - the decision, its status, the dates and the
// items covered - and the Da Vinci PDex guide says how: an ExplanationOfBenefit with use "preauthorization" in the
// pdex-priorauthorization profile. A payer running Da Vinci PAS already holds the decision as a PAS ClaimResponse (and the
// request as a PAS Claim), so the conversion is from those two.
//
// Denied requests are converted like any other. The Payer-to-Payer API excludes them, and that exclusion belongs to the export
// rather than here: the member is entitled to see a denial through the Patient Access API.

const (
	pdexBase = "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/"
	pasBase  = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/"

	// PDexPriorAuthProfile is the profile the converted ExplanationOfBenefit claims.
	PDexPriorAuthProfile = pdexBase + "pdex-priorauthorization"

	sysServiceType   = "https://x12.org/codes/service-type-codes"
	sysPASServiceTyp = "https://codesystem.x12.org/005010/1365"
	sysPDexAdjDisc   = "http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PDexAdjudicationDiscriminator"
)

// The PAS item extensions PDex reuses unchanged: its profile slices item.extension by these PAS URLs.
var pasItemExtensions = map[string]bool{
	pasBase + "extension-itemTraceNumber":               true,
	pasBase + "extension-itemPreAuthIssueDate":          true,
	pasBase + "extension-itemPreAuthPeriod":             true,
	pasBase + "extension-authorizationNumber":           true,
	pasBase + "extension-administrationReferenceNumber": true,
	pasBase + "extension-itemAuthorizedDetail":          true,
	pasBase + "extension-itemAuthorizedProvider":        true,
}

// PriorAuthResult is one prior authorisation converted.
type PriorAuthResult struct {
	ExplanationOfBenefit map[string]any `json:"explanationOfBenefit"`
	// Decision summarises the item decisions in words: approved, denied, pended, partly approved.
	Decision string   `json:"decision"`
	Notes    []string `json:"notes"`
}

// PriorAuthFromJSON accepts what a PAS exchange leaves behind: a ClaimResponse on its own, a PAS response Bundle, or a Bundle
// holding both the Claim and the ClaimResponse. claimJSON is an optional separate Claim (or request Bundle).
func PriorAuthFromJSON(responseJSON, claimJSON []byte, now time.Time) (*PriorAuthResult, error) {
	var cr, claim map[string]any
	if err := pickResources(responseJSON, &cr, &claim); err != nil {
		return nil, err
	}
	if len(claimJSON) > 0 {
		var ignored map[string]any
		if err := pickResources(claimJSON, &ignored, &claim); err != nil {
			return nil, fmt.Errorf("the PAS request: %w", err)
		}
	}
	if cr == nil {
		return nil, fmt.Errorf("no ClaimResponse found; paste the PAS response (the ClaimResponse or the Bundle that carries it)")
	}

	return ToPDexPriorAuth(cr, claim, now)
}

func pickResources(raw []byte, cr, claim *map[string]any) error {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	take := func(r map[string]any) {
		switch r["resourceType"] {
		case "ClaimResponse":
			if *cr == nil {
				*cr = r
			}
		case "Claim":
			if *claim == nil {
				*claim = r
			}
		}
	}
	switch doc["resourceType"] {
	case "Bundle":
		entries, _ := doc["entry"].([]any)
		for _, e := range entries {
			if r, ok := asMap(e)["resource"].(map[string]any); ok {
				take(r)
			}
		}
	case "ClaimResponse", "Claim":
		take(doc)
	default:
		return fmt.Errorf("expected a ClaimResponse, a Claim or a Bundle, got %v", doc["resourceType"])
	}

	return nil
}

// ToPDexPriorAuth converts a PAS ClaimResponse, with the PAS Claim it answers when available.
func ToPDexPriorAuth(cr, claim map[string]any, now time.Time) (*PriorAuthResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	var notes []string
	note := func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) }

	if cr["use"] != nil && cr["use"] != "preauthorization" {
		return nil, fmt.Errorf("the ClaimResponse's use is %q, not preauthorization; this is not a prior authorisation", cr["use"])
	}

	eob := map[string]any{
		"resourceType": "ExplanationOfBenefit",
		"meta": map[string]any{
			"lastUpdated": now.UTC().Format(time.RFC3339),
			// Unversioned: the profile's own pattern is the bare canonical, and a versioned one does not match it.
			"profile": []any{PDexPriorAuthProfile},
		},
		"status": firstNonNil(cr["status"], "active"),
		"use":    "preauthorization",
	}
	if id, ok := cr["id"].(string); ok && id != "" {
		eob["id"] = "pa-" + id
		if len(eob["id"].(string)) > 64 {
			eob["id"] = fhirID("pa", id)
		}
	}

	copyField := func(to, from string, sources ...map[string]any) {
		for _, s := range sources {
			if s == nil {
				continue
			}
			if v, ok := s[from]; ok && v != nil {
				eob[to] = v
				return
			}
		}
	}
	copyField("identifier", "identifier", cr, claim)
	copyField("type", "type", cr, claim)
	copyField("subType", "subType", cr, claim)
	copyField("patient", "patient", cr, claim)
	copyField("created", "created", cr, claim)
	copyField("insurer", "insurer", cr, claim)
	copyField("provider", "requestor", cr)
	if eob["provider"] == nil {
		copyField("provider", "provider", claim)
	}
	copyField("priority", "priority", claim)
	copyField("outcome", "outcome", cr)
	copyField("preAuthRef", "preAuthRef", cr)
	copyField("diagnosis", "diagnosis", claim)
	copyField("careTeam", "careTeam", claim)
	copyField("billablePeriod", "billablePeriod", claim)
	copyField("claim", "request", cr)
	if p, ok := cr["preAuthPeriod"].(map[string]any); ok {
		eob["preAuthRefPeriod"] = []any{p}
	}
	if id, ok := cr["id"].(string); ok && id != "" {
		eob["claimResponse"] = map[string]any{"reference": "ClaimResponse/" + id}
	}

	for _, req := range []string{"type", "patient", "created", "insurer", "provider", "outcome"} {
		if eob[req] == nil {
			return nil, fmt.Errorf("an ExplanationOfBenefit requires %s, and neither the ClaimResponse nor the Claim has it", req)
		}
	}

	// Careteam entries drop the PAS-only extension that scopes them to the claim; PDex has no slot for it.
	if ct, ok := eob["careTeam"].([]any); ok {
		eob["careTeam"] = stripExtensions(ct, map[string]bool{pasBase + "extension-careTeamClaimScope": true})
	}
	if dx, ok := eob["diagnosis"].([]any); ok {
		eob["diagnosis"] = stripExtensions(dx, map[string]bool{pasBase + "extension-diagnosisRecordedDate": true})
	}

	// insurance is required by ExplanationOfBenefit itself (1..*), and only the request says which coverage was used.
	insurance := insuranceFrom(claim)
	if insurance == nil {
		insurance = insuranceFrom(cr)
	}
	if insurance == nil {
		return nil, fmt.Errorf("an ExplanationOfBenefit requires the coverage it was decided under; " +
			"the PAS ClaimResponse does not carry it, so supply the PAS Claim (the request) as well")
	}
	eob["insurance"] = insurance

	// The level of service, which PAS carries on the Claim under its own URL.
	if claim != nil {
		for _, e := range extensions(claim) {
			if e["url"] == pasBase+"extension-levelOfServiceCode" || e["url"] == pasBase+"extension-levelOfServiceType" {
				eob["extension"] = []any{map[string]any{
					"url": pdexBase + "extension-levelOfServiceCode", "valueCodeableConcept": e["valueCodeableConcept"],
				}}
			}
		}
	}

	claimItems := map[int]map[string]any{}
	if claim != nil {
		items, _ := claim["item"].([]any)
		for _, it := range items {
			m := asMap(it)
			claimItems[toInt(m["sequence"])] = m
		}
	}

	crItems, _ := cr["item"].([]any)
	if len(crItems) == 0 {
		return nil, fmt.Errorf("the ClaimResponse has no items, so there is no decision to convert")
	}
	decisions := map[string]int{}
	var out []any
	for _, raw := range crItems {
		ri := asMap(raw)
		seq := toInt(ri["itemSequence"])
		ci := claimItems[seq]
		item := map[string]any{"sequence": seq}

		// Category: the X12 service type. PAS writes it under the X12 code list's own URI; PDex binds the same codes under
		// x12.org, so the system is renamed and the code kept.
		if ci != nil {
			if cat, ok := ci["category"].(map[string]any); ok {
				item["category"] = renameSystem(cat, sysPASServiceTyp, sysServiceType)
			}
		}

		item["productOrService"] = productFrom(ci, ri)
		if ci != nil {
			for _, k := range []string{"servicedDate", "servicedPeriod", "quantity", "modifier", "revenue"} {
				if v, ok := ci[k]; ok {
					item[k] = v
				}
			}
		}
		if item["servicedDate"] == nil && item["servicedPeriod"] == nil {
			for _, e := range extensions(ri) {
				if e["url"] == pasBase+"extension-itemRequestedServiceDate" {
					if p, ok := e["valuePeriod"]; ok {
						item["servicedPeriod"] = p
					} else if d, ok := e["valueDate"]; ok {
						item["servicedDate"] = d
					}
				}
			}
		}

		var ext []any
		for _, e := range extensions(ri) {
			if pasItemExtensions[e["url"].(string)] {
				ext = append(ext, e)
			}
		}
		if len(ext) > 0 {
			item["extension"] = ext
		}

		adj, decision, err := adjudicationFor(ri, ci, eob["created"], seq)
		if err != nil {
			return nil, err
		}
		decisions[decision]++
		if adj != nil {
			item["adjudication"] = adj
		} else {
			note("item %d has no requested amount or quantity, so its decision (%s) is carried by the outcome rather than an "+
				"adjudication", seq, decision)
		}
		out = append(out, item)
	}
	eob["item"] = out

	if claim == nil {
		note("no PAS Claim was supplied, so the items carry no procedure codes, dates or diagnoses beyond what the ClaimResponse holds")
	}

	return &PriorAuthResult{ExplanationOfBenefit: eob, Decision: summariseDecisions(decisions), Notes: notes}, nil
}

// adjudicationFor converts the review actions on one ClaimResponse item.
//
// PDex requires every adjudication to have a category and, for an amount category, an amount. The decision itself rides in the
// reviewAction extension, so it needs an adjudication to sit on: "submitted" with the requested amount when the request priced
// the item, and otherwise "allowedunits" with the authorised quantity.
func adjudicationFor(ri, ci map[string]any, when any, seq int) ([]any, string, error) {
	var review map[string]any
	for _, a := range asSlice(ri["adjudication"]) {
		for _, e := range extensions(asMap(a)) {
			if e["url"] == pasBase+"extension-reviewAction" {
				review = e
			}
		}
	}

	var parts []any
	decision := "pended"
	if review != nil {
		for _, sub := range asSlice(review["extension"]) {
			s := asMap(sub)
			switch s["url"] {
			case "number":
				parts = append(parts, map[string]any{"url": "number", "valueString": s["valueString"]})
			case pasBase + "extension-reviewActionCode", "code":
				cc := s["valueCodeableConcept"]
				parts = append(parts, map[string]any{"url": pdexBase + "extension-reviewActionCode", "valueCodeableConcept": cc})
				decision = decisionWord(firstCode(cc))
			case "reasonCode":
				parts = append(parts, map[string]any{"url": "reasonCode", "valueCodeableConcept": s["valueCodeableConcept"]})
			case "secondSurgicalOpinionFlag":
				parts = append(parts, map[string]any{"url": "secondSurgicalOpinionFlag", "valueBoolean": s["valueBoolean"]})
			}
		}
	}

	a := map[string]any{}
	var ext []any
	if len(parts) > 0 {
		ext = append(ext, map[string]any{"url": pdexBase + "extension-reviewAction", "extension": parts})
	}
	if when != nil {
		ext = append(ext, map[string]any{"url": pdexBase + "base-ext-when-adjudicated", "valueDateTime": when})
	}
	if len(ext) > 0 {
		a["extension"] = ext
	}

	if amount, ok := requestedAmount(ci); ok {
		a["category"] = codeable(sysAdjudication, "submitted", "")
		a["amount"] = money(amount)

		return []any{a}, decision, nil
	}
	qty, ok := authorisedQuantity(ri, ci)
	if !ok {
		// Nothing to hang the decision on. Leaving the adjudication out keeps the item valid, and the decision is still
		// there in EOB.outcome; inventing a quantity to carry it would state an authorisation nobody made.
		return nil, decision, nil
	}
	if decision == "denied" {
		qty = 0
	}
	a["category"] = codeable(sysPDexAdjDisc, "allowedunits", "")
	a["value"] = qty

	return []any{a}, decision, nil
}

func requestedAmount(ci map[string]any) (float64, bool) {
	if ci == nil {
		return 0, false
	}
	if net, ok := ci["net"].(map[string]any); ok {
		if v, ok := net["value"].(float64); ok {
			return v, true
		}
	}
	if up, ok := ci["unitPrice"].(map[string]any); ok {
		if v, ok := up["value"].(float64); ok {
			q := 1.0
			if qm, ok := ci["quantity"].(map[string]any); ok {
				if qv, ok := qm["value"].(float64); ok {
					q = qv
				}
			}
			return v * q, true
		}
	}

	return 0, false
}

func authorisedQuantity(ri, ci map[string]any) (float64, bool) {
	for _, e := range extensions(ri) {
		if e["url"] != pasBase+"extension-itemAuthorizedDetail" {
			continue
		}
		for _, sub := range asSlice(e["extension"]) {
			s := asMap(sub)
			if s["url"] == "quantity" {
				if q, ok := asMap(s["valueQuantity"])["value"].(float64); ok {
					return q, true
				}
			}
		}
	}
	if ci != nil {
		if q, ok := asMap(ci["quantity"])["value"].(float64); ok {
			return q, true
		}
	}

	return 0, false
}

// productFrom takes the item's procedure code when it is one PDex's value set allows - CPT, HCPCS or HIPPS - and otherwise
// reports it as not applicable. A PAS request that names only a service type, which is common for a referral, has no
// procedure, and putting the service type in as one would claim a code it is not.
func productFrom(ci, ri map[string]any) map[string]any {
	allowed := map[string]bool{sysCPT: true, sysHCPCS: true, sysHIPPS: true}
	for _, src := range []map[string]any{ci} {
		if src == nil {
			continue
		}
		if ps, ok := src["productOrService"].(map[string]any); ok {
			for _, c := range asSlice(ps["coding"]) {
				if allowed[asMap(c)["system"].(string)] {
					return ps
				}
			}
		}
	}

	return codeable(sysDataAbsent, "not-applicable", "")
}

func decisionWord(code string) string {
	switch code {
	case "A1":
		return "approved"
	case "A2":
		return "partly approved"
	case "A3":
		return "denied"
	case "A4":
		return "pended"
	case "A6":
		return "modified"
	case "C":
		return "cancelled"
	case "CT":
		return "contact the payer"
	case "NA":
		return "not required"
	}

	return "pended"
}

func summariseDecisions(d map[string]int) string {
	if len(d) == 1 {
		for k := range d {
			return k
		}
	}
	if d["approved"] > 0 && d["denied"] > 0 {
		return "partly approved"
	}
	out := ""
	for _, k := range []string{"approved", "partly approved", "modified", "denied", "pended", "contact the payer", "not required", "cancelled"} {
		if d[k] > 0 {
			if out != "" {
				out += ", "
			}
			out += strconv.Itoa(d[k]) + " " + k
		}
	}

	return out
}

func insuranceFrom(r map[string]any) []any {
	if r == nil {
		return nil
	}
	var out []any
	for _, ins := range asSlice(r["insurance"]) {
		m := asMap(ins)
		if m["coverage"] == nil {
			continue
		}
		focal := true
		if f, ok := m["focal"].(bool); ok {
			focal = f
		}
		out = append(out, map[string]any{"focal": focal, "coverage": m["coverage"]})
	}

	return out
}

func stripExtensions(list []any, drop map[string]bool) []any {
	out := make([]any, 0, len(list))
	for _, x := range list {
		m := map[string]any{}
		for k, v := range asMap(x) {
			m[k] = v
		}
		var keep []any
		for _, e := range extensions(m) {
			if !drop[e["url"].(string)] {
				keep = append(keep, e)
			}
		}
		if len(keep) > 0 {
			m["extension"] = keep
		} else {
			delete(m, "extension")
		}
		out = append(out, m)
	}

	return out
}

func renameSystem(cc map[string]any, from, to string) map[string]any {
	out := map[string]any{}
	for k, v := range cc {
		out[k] = v
	}
	var codings []any
	for _, c := range asSlice(cc["coding"]) {
		m := map[string]any{}
		for k, v := range asMap(c) {
			m[k] = v
		}
		if m["system"] == from {
			m["system"] = to
		}
		codings = append(codings, m)
	}
	out["coding"] = codings

	return out
}

func extensions(r map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range asSlice(r["extension"]) {
		if m := asMap(e); m["url"] != nil {
			out = append(out, m)
		}
	}

	return out
}

func firstCode(cc any) string {
	for _, c := range asSlice(asMap(cc)["coding"]) {
		if code, ok := asMap(c)["code"].(string); ok {
			return code
		}
	}

	return ""
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}

	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)

	return s
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}

	return 0
}

func firstNonNil(v any, fallback any) any {
	if v == nil {
		return fallback
	}

	return v
}
