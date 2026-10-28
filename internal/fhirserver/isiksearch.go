package fhirserver

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Search parameters the German hospital standard (ISiK Stufe 5) requires, beyond what the server already had. gematik's
// test suite reads each one's name and type from the capability statement and then searches by it.
//
// Most are a plain path and go through the same path indexer as everything else. Three are not: _tag (every type), two
// composites (an Observation's code with its value, a ValueSet's context type with its value) and near (a Location's
// position, by distance).

var isikPathParams = map[string][]pathParam{
	"Account":            {{"patient", "reference", []string{"subject"}}, {"type", "token", []string{"type"}}},
	"AllergyIntolerance": {{"category", "token", []string{"category"}}, {"type", "token", []string{"type"}}},
	"Appointment": {
		{"actor", "reference", []string{"participant.actor"}}, {"service-type", "token", []string{"serviceType"}},
		{"slot", "reference", []string{"slot"}}, {"specialty", "token", []string{"specialty"}},
	},
	"Condition": {{"related", "reference", []string{"extension[http://hl7.org/fhir/StructureDefinition/condition-related].valueReference"}}},
	"Coverage":  {{"subscriber", "reference", []string{"subscriber"}}, {"type", "token", []string{"type"}}},
	"DocumentReference": {
		{"creation", "date", []string{"content.attachment.creation"}}, {"encounter", "reference", []string{"context.encounter"}},
	},
	"Encounter": {
		{"account", "reference", []string{"account"}}, {"service-provider", "reference", []string{"serviceProvider"}},
		{"date-start", "date", []string{"period.start"}}, {"end-date", "date", []string{"period.end"}},
	},
	"HealthcareService": {{"active", "token", []string{"active"}}},
	"List":              {{"item", "reference", []string{"entry.item"}}},
	"Location":          {{"operational-status", "token", []string{"operationalStatus"}}},
	"Medication": {
		{"form", "token", []string{"form"}}, {"ingredient", "reference", []string{"ingredient.itemReference"}},
		{"ingredient-code", "token", []string{"ingredient.itemCodeableConcept"}}, {"lot-number", "token", []string{"batch.lotNumber"}},
	},
	"MedicationAdministration": {
		{"code", "token", []string{"medicationCodeableConcept"}}, {"context", "reference", []string{"context"}},
		{"effective-time", "date", []string{"effectiveDateTime", "effectivePeriod"}},
		{"medication", "reference", []string{"medicationReference"}}, {"performer", "reference", []string{"performer.actor"}},
	},
	"MedicationRequest": {
		{"code", "token", []string{"medicationCodeableConcept"}}, {"date", "date", []string{"dosageInstruction.timing.event"}},
		{"requester", "reference", []string{"requester"}},
	},
	"MedicationStatement": {
		{"code", "token", []string{"medicationCodeableConcept"}}, {"context", "reference", []string{"context"}},
		{"effective", "date", []string{"effectiveDateTime", "effectivePeriod"}}, {"medication", "reference", []string{"medicationReference"}},
	},
	"Observation":  {{"combo-code", "token", []string{"code", "component.code"}}, {"component-code", "token", []string{"component.code"}}},
	"Organization": {{"active", "token", []string{"active"}}},
	"Patient":      {{"address-country", "string", []string{"address.country"}}},
	"Practitioner": {
		{"address", "string", []string{"address.line", "address.city", "address.state", "address.postalCode", "address.country", "address.text"}},
		{"gender", "token", []string{"gender"}},
	},
	"RelatedPerson": {
		{"address", "string", []string{"address.line", "address.city", "address.state", "address.postalCode", "address.country", "address.text"}},
		{"address-city", "string", []string{"address.city"}}, {"address-country", "string", []string{"address.country"}},
		{"address-postalcode", "string", []string{"address.postalCode"}},
	},
	"RiskAssessment": {
		{"date", "date", []string{"occurrenceDateTime", "occurrencePeriod"}}, {"patient", "reference", []string{"subject"}},
		{"risk", "token", []string{"prediction.qualitativeRisk"}},
	},
	"Schedule": {
		{"active", "token", []string{"active"}}, {"actor", "reference", []string{"actor"}},
		{"service-type", "token", []string{"serviceType"}}, {"specialty", "token", []string{"specialty"}},
	},
	"Slot":     {{"start", "date", []string{"start"}}},
	"ValueSet": {{"version", "token", []string{"version"}}},
}

// The composites and near: indexed by their own code below, declared with their own types.
var specialParams = map[string]map[string]string{
	"Observation": {"combo-code-value-quantity": "composite"},
	"ValueSet":    {"context-type-value": "composite"},
	"Location":    {"near": "special"},
}

func init() {
	for t, defs := range isikPathParams {
		pathParams[t] = append(pathParams[t], defs...)
		for _, d := range defs {
			addSearchParam(t, d.param)
		}
	}
	for t, params := range specialParams {
		for p := range params {
			addSearchParam(t, p)
		}
	}
	// Chains ISiK makes: Coverage by its insured person's identifier, an Encounter by its account's.
	addTargets := func(t, param string, targets ...string) {
		if referenceTargets[t] == nil {
			referenceTargets[t] = map[string][]string{}
		}
		if _, ok := referenceTargets[t][param]; !ok {
			referenceTargets[t][param] = targets
		}
	}
	addTargets("Coverage", "patient", "Patient")
	addTargets("Coverage", "beneficiary", "Patient")
	addTargets("Coverage", "subscriber", "Patient", "RelatedPerson")
	addTargets("Coverage", "payor", "Organization", "Patient", "RelatedPerson")
	addTargets("Encounter", "account", "Account")
	addTargets("Encounter", "service-provider", "Organization")
	addTargets("Encounter", "location", "Location")
	addTargets("Account", "patient", "Patient")
	// _tag on every type: meta.tag, a Coding.
	for t := range SearchParams {
		pathParams[t] = append(pathParams[t], pathParam{"_tag", "token", []string{"meta.tag"}})
		addSearchParam(t, "_tag")
	}
}

func addSearchParam(resourceType, param string) {
	for _, p := range SearchParams[resourceType] {
		if p == param {
			return
		}
	}
	SearchParams[resourceType] = append(SearchParams[resourceType], param)
}

// specialEntries indexes the composites and a Location's position.
func specialEntries(resourceType string, tree map[string]any) []indexEntry {
	var out []indexEntry
	switch resourceType {
	case "Observation":
		// The code with its quantity, the observation's own and each component's.
		add := func(code, value any) {
			q, _ := value.(map[string]any)
			n, ok := q["value"].(float64)
			if !ok {
				return
			}
			for _, c := range codes(code) {
				key := c + "$" + normalNumber(n)
				out = append(out, indexEntry{param: "combo-code-value-quantity", value: key, valueRaw: key})
			}
		}
		add(tree["code"], tree["valueQuantity"])
		comps, _ := tree["component"].([]any)
		for _, c := range comps {
			cm, _ := c.(map[string]any)
			add(cm["code"], cm["valueQuantity"])
			add(tree["code"], cm["valueQuantity"]) // a blood pressure is searched by its panel code too
		}
	case "ValueSet":
		ctxs, _ := tree["useContext"].([]any)
		for _, u := range ctxs {
			um, _ := u.(map[string]any)
			typ, _ := um["code"].(map[string]any)
			left, _ := typ["code"].(string)
			if left == "" {
				continue
			}
			for _, c := range codes(um["valueCodeableConcept"]) {
				key := left + "$" + c
				out = append(out, indexEntry{param: "context-type-value", value: key, valueRaw: key})
			}
		}
	case "Location":
		pos, _ := tree["position"].(map[string]any)
		lat, ok1 := pos["latitude"].(float64)
		lon, ok2 := pos["longitude"].(float64)
		if ok1 && ok2 {
			raw := strconv.FormatFloat(lat, 'f', -1, 64) + "|" + strconv.FormatFloat(lon, 'f', -1, 64)
			out = append(out, indexEntry{param: "near", value: raw, valueRaw: raw})
		}
	}
	return out
}

// codes are the codes of a CodeableConcept, without their systems.
func codes(v any) []string {
	cc, _ := v.(map[string]any)
	var out []string
	codings, _ := cc["coding"].([]any)
	for _, c := range codings {
		cm, _ := c.(map[string]any)
		if code, _ := cm["code"].(string); code != "" {
			out = append(out, code)
		}
	}
	return out
}

func normalNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// normaliseComposite reduces a composite search value to the form specialEntries indexes: the left code without its
// system, "$", and the right side's code (a token) or number (a quantity, with eq and its unit dropped).
func normaliseComposite(param, v string) (string, error) {
	if specialParams["Observation"][param] == "" && specialParams["ValueSet"][param] == "" {
		return v, nil
	}
	parts := splitOr(v)
	for i, one := range parts {
		left, right, ok := strings.Cut(one, "$")
		if !ok {
			return "", fmt.Errorf("%s takes two values joined by $, as code$value", param)
		}
		if j := strings.LastIndexByte(left, '|'); j >= 0 {
			left = left[j+1:]
		}
		if param == "combo-code-value-quantity" {
			num, _, _ := strings.Cut(right, "|")
			num = strings.TrimPrefix(num, "eq")
			n, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return "", fmt.Errorf("%s: %q is not a number (only equality is supported)", param, right)
			}
			right = normalNumber(n)
		} else if j := strings.LastIndexByte(right, '|'); j >= 0 {
			right = right[j+1:]
		}
		parts[i] = left + "$" + right
	}
	return strings.Join(parts, ","), nil
}

// nearDefaultKm is the distance used when a near search gives none, which FHIR leaves to the server.
const nearDefaultKm = 50

// resolveNear turns near=lat|long[|distance[|unit]] into _id criteria, as _has is.
func (s *Store) resolveNear(ctx context.Context, q *SearchQuery) error {
	values := q.Criteria["near"]
	delete(q.Criteria, "near")
	for _, v := range values {
		f := strings.Split(v, "|")
		if len(f) < 2 {
			return fmt.Errorf("near takes latitude|longitude[|distance[|unit]]")
		}
		lat, err1 := strconv.ParseFloat(f[0], 64)
		lon, err2 := strconv.ParseFloat(f[1], 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("near: %q is not latitude|longitude", v)
		}
		km := float64(nearDefaultKm)
		if len(f) > 2 && f[2] != "" {
			d, err := strconv.ParseFloat(f[2], 64)
			if err != nil {
				return fmt.Errorf("near: %q is not a distance", f[2])
			}
			km = d
			if len(f) > 3 {
				switch f[3] {
				case "km", "":
				case "m":
					km = d / 1000
				case "[mi_i]", "mi":
					km = d * 1.609344
				default:
					return fmt.Errorf("near: the unit %q is not supported (km, m or [mi_i])", f[3])
				}
			}
		}
		rows, err := s.db.QueryContext(ctx, `SELECT resource_id, value FROM fhir_search WHERE resource_type = ? AND param = 'near'`, q.ResourceType)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id, pos string
			if rows.Scan(&id, &pos) != nil {
				continue
			}
			a, b, _ := strings.Cut(pos, "|")
			plat, _ := strconv.ParseFloat(a, 64)
			plon, _ := strconv.ParseFloat(b, 64)
			if haversineKm(lat, lon, plat, plon) <= km {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			ids = []string{chainMatchesNothingID}
		}
		q.Criteria["_id"] = append(q.Criteria["_id"], strings.Join(ids, ","))
	}
	return nil
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dlat, dlon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
