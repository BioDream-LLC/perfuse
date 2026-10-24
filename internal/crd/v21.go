package crd

import (
	"encoding/json"

	"github.com/biodream-llc/perfuse/internal/davinci"
)

// CRD 2.1.0 for a client that asks for it (davinci-crd.requestedVersion "2.1"), was registered as speaking it, or meets a
// server set to it. coverage-information is built in CRD 2.2.1's form and rewritten where 2.1.0 spells it differently,
// checked by the HL7 validator against CRD 2.1.0 (conformance/davinci21):
//
//   - indeterminate, 2.2.1's answer for "could not tell", is not a 2.1.0 code: covered and pa-needed say conditional, with
//     info-needed OTH, and the reason says why;
//   - 2.1.0's reasons are only gold-card and detail-code, so a membership or technical reason goes as text alone;
//   - a detail has no category in 2.1.0, and its code is from CRD's own temp code system;
//   - contact is a ContactPoint in 2.1.0 (a ContactDetail in 2.2.1), one per phone, email or address.

const crdTemp = "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp"

// version is the CRD release to answer this request in: what it asks for, then its client's registration, then the
// server's setting.
func (r *Rules) version(req *Request) string {
	var asked string
	_ = json.Unmarshal(req.Extension["davinci-crd.requestedVersion"], &asked)
	return davinci.Pick(asked, req.ClientVersion, r.DaVinciVersion)
}

// forVersion is a coverage-information extension as a client of version v reads it.
func forVersion(ext map[string]any, v string) map[string]any {
	if v != davinci.V21 {
		return ext
	}
	var out []any
	indeterminate, hasInfo := false, false
	for _, e := range listOf(ext["extension"]) {
		em, _ := e.(map[string]any)
		switch str(em["url"]) {
		case "covered", "pa-needed", "doc-needed":
			if em["valueCode"] == "indeterminate" {
				indeterminate = true
				e = map[string]any{"url": em["url"], "valueCode": "conditional"}
			}
		case "info-needed":
			hasInfo = true
		case "reason":
			cc, _ := em["valueCodeableConcept"].(map[string]any)
			keep := false
			for _, c := range listOf(cc["coding"]) {
				cm, _ := c.(map[string]any)
				keep = keep || cm["code"] == "gold-card" || cm["code"] == "detail-code"
			}
			if !keep {
				text := str(cc["text"])
				if text == "" {
					for _, c := range listOf(cc["coding"]) {
						cm, _ := c.(map[string]any)
						if d := str(cm["display"]); d != "" {
							text = d
						}
					}
				}
				e = map[string]any{"url": "reason", "valueCodeableConcept": map[string]any{"text": text}}
			}
		case "detail":
			var parts []any
			for _, p := range listOf(em["extension"]) {
				pm, _ := p.(map[string]any)
				switch str(pm["url"]) {
				case "category":
					continue
				case "code":
					cc, _ := pm["valueCodeableConcept"].(map[string]any)
					var codings []any
					for _, c := range listOf(cc["coding"]) {
						cm, _ := c.(map[string]any)
						codings = append(codings, map[string]any{"system": crdTemp, "code": cm["code"]})
					}
					p = map[string]any{"url": "code", "valueCodeableConcept": map[string]any{"coding": codings}}
				}
				parts = append(parts, p)
			}
			e = map[string]any{"url": "detail", "extension": parts}
		case "contact":
			detail, _ := em["valueContactDetail"].(map[string]any)
			for _, t := range listOf(detail["telecom"]) {
				out = append(out, map[string]any{"url": "contact", "valueContactPoint": t})
			}
			continue
		}
		out = append(out, e)
	}
	if indeterminate && !hasInfo {
		// crd-ci-q3: a conditional answer says what is needed; OTH, with the reason the answer already carries.
		out = append(out, map[string]any{"url": "info-needed", "valueCode": "OTH"})
	}
	return map[string]any{"url": ext["url"], "extension": out}
}

func listOf(v any) []any { l, _ := v.([]any); return l }
