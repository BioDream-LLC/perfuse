package cms0057

import (
	"fmt"
	"strings"
	"time"
)

// Audience is who a payer API is serving, which decides what of a member's record they may receive.
type Audience string

const (
	// PatientAccess is the member, through an app they chose. They receive everything, cost-sharing included.
	PatientAccess Audience = "patient"
	// ProviderAccess is an in-network provider with a treatment relationship. CMS-0057 excludes provider remittances and the
	// member's cost-sharing, and prior authorisations for drugs.
	ProviderAccess Audience = "provider"
	// PayerToPayer is another payer the member has moved to or also holds. The same exclusions as ProviderAccess, and denied
	// prior authorisations as well.
	PayerToPayer Audience = "payer"
)

// costSharing are the adjudication and total categories that are a member's cost-sharing or a remittance to a provider:
// what the rule takes out of the Provider Access and Payer-to-Payer APIs (42 CFR 422.121(a)(1)(i) and (b)(2)(i)).
var costSharing = map[string]bool{
	"copay": true, "deductible": true, "coinsurance": true, "memberliability": true,
	"paidbypatient": true, "paidbypatientcash": true, "paidbypatientother": true, "paidbypatienthealthaccount": true,
	"paidtopatient": true, "paidtoprovider": true, "priorpayerpaid": true, "discount": true, "benefit": true,
	"eligible": true, "noncovered": true, "drugcost": true,
}

// Redact prepares one resource for an audience. It returns nil when the resource must not be sent at all.
//
// Applied to ExplanationOfBenefit only; every other resource type is returned unchanged. The input is not modified.
func Redact(resource map[string]any, aud Audience) map[string]any {
	if aud == PatientAccess || resource["resourceType"] != "ExplanationOfBenefit" {
		return resource
	}
	out := cloneMap(resource)

	if out["use"] == "preauthorization" {
		if isDrugPriorAuth(out) {
			return nil
		}
		if aud == PayerToPayer && priorAuthDenied(out) {
			return nil
		}

		return out
	}

	// A claim: the payment and every amount that is cost-sharing or a remittance go; the services, codes, dates and
	// providers - the clinical content the rule is about - stay.
	delete(out, "payment")
	out["total"] = keepNonCost(asSlice(out["total"]))
	if len(asSlice(out["total"])) == 0 {
		delete(out, "total")
	}
	out["adjudication"] = keepNonCost(asSlice(out["adjudication"]))
	if len(asSlice(out["adjudication"])) == 0 {
		delete(out, "adjudication")
	}
	var items []any
	for _, it := range asSlice(out["item"]) {
		m := cloneMap(asMap(it))
		m["adjudication"] = keepNonCost(asSlice(m["adjudication"]))
		if len(asSlice(m["adjudication"])) == 0 {
			delete(m, "adjudication")
		}
		items = append(items, m)
	}
	if items != nil {
		out["item"] = items
	}
	// The profile no longer holds: the CARIN profiles require the amounts just removed. Saying so is more honest than
	// sending a resource that claims a profile it now fails.
	if meta, ok := out["meta"].(map[string]any); ok {
		m := cloneMap(meta)
		delete(m, "profile")
		out["meta"] = m
	}

	return out
}

func keepNonCost(list []any) []any {
	var out []any
	for _, a := range list {
		m := asMap(a)
		if costSharing[firstCode(m["category"])] {
			continue
		}
		// Adjustment reasons carry an amount the member or the provider was charged; the reason may stay, the money goes.
		if _, has := m["amount"]; has && firstCode(m["category"]) == "adjustmentreason" {
			c := cloneMap(m)
			delete(c, "amount")
			out = append(out, c)
			continue
		}
		out = append(out, m)
	}

	return out
}

func isDrugPriorAuth(eob map[string]any) bool {
	for _, it := range asSlice(eob["item"]) {
		ps := asMap(asMap(it)["productOrService"])
		for _, c := range asSlice(ps["coding"]) {
			sys, _ := asMap(c)["system"].(string)
			code, _ := asMap(c)["code"].(string)
			if strings.Contains(sys, "ndc") || strings.Contains(sys, "rxnorm") || looksLikeDrug(code) {
				return true
			}
		}
	}

	return false
}

func priorAuthDenied(eob map[string]any) bool {
	denied, decided := 0, 0
	for _, it := range asSlice(eob["item"]) {
		for _, a := range asSlice(asMap(it)["adjudication"]) {
			for _, e := range extensions(asMap(a)) {
				if e["url"] != pdexBase+"extension-reviewAction" {
					continue
				}
				for _, sub := range asSlice(e["extension"]) {
					s := asMap(sub)
					if s["url"] == pdexBase+"extension-reviewActionCode" {
						decided++
						if firstCode(s["valueCodeableConcept"]) == "A3" {
							denied++
						}
					}
				}
			}
		}
	}

	return decided > 0 && denied == decided
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}

	return out
}

// Member matching, the HRex $member-match operation the Payer-to-Payer API starts with.

// MatchCandidate is a Coverage the payer holds that might be the member's, with the Patient it covers.
type MatchCandidate struct {
	Coverage map[string]any
	Patient  map[string]any
}

// MatchRequest is what the requesting payer sent.
type MatchRequest struct {
	Patient         map[string]any
	CoverageToMatch map[string]any
	CoverageToLink  map[string]any
	Consent         map[string]any
}

// ParseMatchRequest reads the HRex member-match input Parameters.
func ParseMatchRequest(params map[string]any) (*MatchRequest, error) {
	if params["resourceType"] != "Parameters" {
		return nil, fmt.Errorf("$member-match takes a Parameters resource, not %v", params["resourceType"])
	}
	req := &MatchRequest{}
	for _, p := range asSlice(params["parameter"]) {
		m := asMap(p)
		res := asMap(m["resource"])
		switch m["name"] {
		case "MemberPatient":
			req.Patient = res
		case "CoverageToMatch":
			req.CoverageToMatch = res
		case "CoverageToLink":
			req.CoverageToLink = res
		case "Consent":
			req.Consent = res
		}
	}
	if len(req.Patient) == 0 || req.Patient["resourceType"] != "Patient" {
		return nil, fmt.Errorf("MemberPatient is required and must be a Patient")
	}
	if len(req.CoverageToMatch) == 0 || req.CoverageToMatch["resourceType"] != "Coverage" {
		return nil, fmt.Errorf("CoverageToMatch is required and must be a Coverage")
	}

	return req, nil
}

// MatchKeys are the identifiers to look a coverage up by: the subscriber id and every Coverage.identifier value.
func (r *MatchRequest) MatchKeys() (subscriberID string, identifiers []string) {
	subscriberID, _ = r.CoverageToMatch["subscriberId"].(string)
	for _, id := range asSlice(r.CoverageToMatch["identifier"]) {
		if v, ok := asMap(id)["value"].(string); ok && v != "" {
			identifiers = append(identifiers, v)
		}
	}

	return subscriberID, identifiers
}

// CheckConsent applies HRex's consent rules: active, permitting disclosure, and in force today.
func CheckConsent(consent map[string]any, now time.Time) error {
	if len(consent) == 0 {
		return fmt.Errorf("a Consent is required: the Payer-to-Payer API needs the member's permission to share")
	}
	if consent["status"] != "active" {
		return fmt.Errorf("the Consent is %v, not active", consent["status"])
	}
	prov := asMap(consent["provision"])
	if t, _ := prov["type"].(string); t != "" && t != "permit" {
		return fmt.Errorf("the Consent's provision is %q, not permit", t)
	}
	if p := asMap(prov["period"]); len(p) > 0 {
		today := now.UTC().Format("2006-01-02")
		if s, _ := p["start"].(string); s != "" && s[:min(10, len(s))] > today {
			return fmt.Errorf("the Consent does not start until %s", s)
		}
		if e, _ := p["end"].(string); e != "" && e[:min(10, len(e))] < today {
			return fmt.Errorf("the Consent expired on %s", e)
		}
	}

	return nil
}

// Match finds the one candidate whose patient is the member described.
//
// Demographics are compared after the identifier lookup, not instead of it: the subscriber id narrows to a few coverages, and
// the family name and date of birth must then agree. Both are required, because a match made on an identifier alone hands one
// person's record to another payer whenever an identifier is mistyped. Given names are compared by initial, which survives
// "Bob" for "Robert" less well than a human would but never matches two different initials; gender, when both sides state it,
// must agree.
//
// More than one surviving candidate is not a match. HRex says to return no match rather than guess, and a payer that guesses
// discloses a stranger's claims.
func Match(req *MatchRequest, candidates []MatchCandidate) (*MatchCandidate, string) {
	family, given, birth, gender := demographics(req.Patient)
	if family == "" || birth == "" {
		return nil, "MemberPatient needs a family name and a birth date to match on"
	}
	var found []*MatchCandidate
	for i := range candidates {
		c := &candidates[i]
		if status, _ := c.Coverage["status"].(string); status != "" && status != "active" && status != "cancelled" {
			continue
		}
		f, g, b, s := demographics(c.Patient)
		if !strings.EqualFold(f, family) || b != birth {
			continue
		}
		if given != "" && g != "" && !strings.EqualFold(given[:1], g[:1]) {
			continue
		}
		if gender != "" && s != "" && gender != s {
			continue
		}
		found = append(found, c)
	}
	switch len(found) {
	case 0:
		return nil, "no member matches the coverage and demographics supplied"
	case 1:
		return found[0], ""
	}

	return nil, fmt.Sprintf("%d members match, so none is returned; a match must be unique", len(found))
}

func demographics(p map[string]any) (family, given, birth, gender string) {
	for _, n := range asSlice(p["name"]) {
		nm := asMap(n)
		if use, _ := nm["use"].(string); use == "old" {
			continue
		}
		family, _ = nm["family"].(string)
		if gs := asSlice(nm["given"]); len(gs) > 0 {
			given, _ = gs[0].(string)
		}
		if family != "" {
			break
		}
	}
	birth, _ = p["birthDate"].(string)
	gender, _ = p["gender"].(string)

	return strings.TrimSpace(family), strings.TrimSpace(given), birth, gender
}

// MatchResponse builds the HRex member-match output Parameters.
//
// MemberIdentifier is the member's identifier as this payer knows it: the Patient's member-number identifier (type MB) when it
// has one, otherwise the subscriber id from the Coverage.
func MatchResponse(c *MatchCandidate, patientID string) map[string]any {
	var ident map[string]any
	for _, id := range asSlice(c.Patient["identifier"]) {
		m := asMap(id)
		if firstCode(m["type"]) == "MB" {
			ident = m
			break
		}
	}
	if ident == nil {
		sub, _ := c.Coverage["subscriberId"].(string)
		ident = map[string]any{
			"type":  map[string]any{"coding": []any{map[string]any{"system": sysV20203, "code": "MB"}}},
			"value": sub,
		}
	}
	params := []any{map[string]any{"name": "MemberIdentifier", "valueIdentifier": ident}}
	if patientID != "" {
		params = append(params, map[string]any{"name": "MemberId", "valueReference": map[string]any{"reference": "Patient/" + patientID}})
	}

	return map[string]any{
		"resourceType": "Parameters",
		"meta":         map[string]any{"profile": []any{"http://hl7.org/fhir/us/davinci-hrex/StructureDefinition/hrex-parameters-member-match-out"}},
		"parameter":    params,
	}
}

// ProviderOptedOut reports whether a Consent records a member's opt-out of the Provider Access API: an active PDex provider
// consent whose provision denies.
func ProviderOptedOut(consent map[string]any, now time.Time) bool {
	if consent["status"] != "active" {
		return false
	}
	purpose := false
	for _, c := range asSlice(consent["category"]) {
		for _, cd := range asSlice(asMap(c)["coding"]) {
			if asMap(cd)["code"] == "provider-access" {
				purpose = true
			}
		}
	}
	if !purpose {
		return false
	}
	prov := asMap(consent["provision"])
	if prov["type"] != "deny" {
		return false
	}
	if p := asMap(prov["period"]); len(p) > 0 {
		today := now.UTC().Format("2006-01-02")
		if s, _ := p["start"].(string); s != "" && s[:min(10, len(s))] > today {
			return false
		}
		if e, _ := p["end"].(string); e != "" && e[:min(10, len(e))] < today {
			return false
		}
	}

	return true
}

// GroupMembers lists the Patient ids a Group currently holds: members not marked inactive and whose period, if any, covers
// today.
func GroupMembers(group map[string]any, now time.Time) []string {
	today := now.UTC().Format("2006-01-02")
	var out []string
	seen := map[string]bool{}
	for _, m := range asSlice(group["member"]) {
		mm := asMap(m)
		if inactive, _ := mm["inactive"].(bool); inactive {
			continue
		}
		if p := asMap(mm["period"]); len(p) > 0 {
			if s, _ := p["start"].(string); s != "" && s[:min(10, len(s))] > today {
				continue
			}
			if e, _ := p["end"].(string); e != "" && e[:min(10, len(e))] < today {
				continue
			}
		}
		ref, _ := asMap(mm["entity"])["reference"].(string)
		id, ok := strings.CutPrefix(ref, "Patient/")
		if !ok || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}

	return out
}
