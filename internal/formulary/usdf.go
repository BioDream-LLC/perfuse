package formulary

import (
	"fmt"
	"sort"
)

// The Da Vinci US Drug Formulary 2.1.0 (STU2) form of a formulary file.
//
// Each plan is a PayerInsurancePlan, each formulary a Formulary (both InsurancePlan), each drug a FormularyDrug
// (MedicationKnowledge) and each entry a FormularyItem (Basic) tying a drug to a formulary with its tier and limits. Ids are
// derived from the file's ids, so publishing the same file twice writes the same resources rather than duplicates, and a
// drug listed on two formularies is one FormularyDrug with two FormularyItems.

const (
	usdf    = "http://hl7.org/fhir/us/davinci-drug-formulary/"
	usdfSD  = usdf + "StructureDefinition/"
	usdfCS  = usdf + "CodeSystem/"
	tierCS  = usdfCS + "usdf-DrugTierCS-TEMPORARY-TRIAL-USE"
	benCS   = usdfCS + "usdf-PharmacyBenefitTypeCS-TEMPORARY-TRIAL-USE"
	costCS  = usdfCS + "usdf-BenefitCostTypeCS-TEMPORARY-TRIAL-USE"
	shareCS = usdfCS + "usdf-CostShareOptionCS-TEMPORARY-TRIAL-USE"
	planCS  = "http://terminology.hl7.org/CodeSystem/insurance-plan-type"
	actCS   = "http://terminology.hl7.org/CodeSystem/v3-ActCode"
	prodCS  = "http://hl7.org/fhir/us/davinci-pdex-plan-net/CodeSystem/InsuranceProductTypeCS"
	itemCS  = usdfCS + "usdf-InsuranceItemTypeCS"
)

// Extension URLs a Drug Formulary client reads.
const (
	ExtFormularyReference = usdfSD + "usdf-FormularyReference-extension"
	ExtAvailabilityStatus = usdfSD + "usdf-AvailabilityStatus-extension"
	ExtAvailabilityPeriod = usdfSD + "usdf-AvailabilityPeriod-extension"
	ExtPharmacyBenefit    = usdfSD + "usdf-PharmacyBenefitType-extension"
	ExtDrugTier           = usdfSD + "usdf-DrugTierID-extension"
	ExtPriorAuthorization = usdfSD + "usdf-PriorAuthorization-extension"
	ExtPANewStarts        = usdfSD + "usdf-PriorAuthorizationNewStartsOnly-extension"
	ExtStepTherapy        = usdfSD + "usdf-StepTherapyLimit-extension"
	ExtSTNewStarts        = usdfSD + "usdf-StepTherapyLimitNewStartsOnly-extension"
	ExtQuantityLimit      = usdfSD + "usdf-QuantityLimit-extension"
	ExtQuantityDetail     = usdfSD + "usdf-QuantityLimitDetail-extension"
	ExtAdditionalInfo     = usdfSD + "usdf-AdditionalCoverageInformation-extension"
)

var tierDisplay = map[string]string{
	"generic": "Generic", "preferred": "Preferred", "non-preferred": "Non-Preferred", "preferred-generic": "Preferred Generic",
	"non-preferred-generic": "Non-Preferred Generic", "specialty": "Specialty", "brand": "Brand",
	"preferred-brand": "Preferred Brand", "non-preferred-brand": "Non-Preferred Brand",
	"zero-cost-share-preventative": "Zero Cost Share Preventative", "medical-service": "Medical Service",
}

var benefitDisplay = map[string]string{
	"1-month-in-retail": "1 month in network retail", "1-month-out-retail": "1 month out of network retail",
	"1-month-in-mail": "1 month in network mail order", "1-month-out-mail": "1 month out of network mail order",
	"3-month-in-retail": "3 month in network retail", "3-month-out-retail": "3 month out of network retail",
	"3-month-in-mail": "3 month in network mail order", "3-month-out-mail": "3 month out of network mail order",
}

var shareDisplay = map[string]string{
	"after-deductible": "After Deductible", "before-deductible": "Before Deductible", "no-charge": "No Charge",
	"no-charge-after-deductible": "No Charge After Deductible", "charge": "Charge",
	"coinsurance-not-applicable": "Coinsurance Not Applicable", "copay-not-applicable": "Copay Not Applicable",
	"deductible-waived": "Deductible Waived",
}

func cc(system, code, display string) map[string]any {
	c := map[string]any{"system": system, "code": code}
	if display != "" {
		c["display"] = display
	}
	return map[string]any{"coding": []any{c}}
}

func meta(profile, updated string) map[string]any {
	return map[string]any{"profile": []any{usdfSD + profile}, "lastUpdated": updated}
}

func period(start, end string) map[string]any {
	p := map[string]any{}
	if start != "" {
		p["start"] = start
	}
	if end != "" {
		p["end"] = end
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

// FormularyID is the InsurancePlan id of a formulary, PlanID of a plan, DrugID of a FormularyDrug and ItemID of a FormularyItem.
func FormularyID(id string) string { return "Formulary" + id }
func PlanID(id string) string      { return "PayerInsurancePlan" + id }
func DrugID(rxnorm string) string  { return "FormularyDrug-" + rxnorm }
func ItemID(formulary, rxnorm string) string {
	return fmt.Sprintf("FormularyItem-%s-%s", formulary, rxnorm)
}

// Resources returns the file as Drug Formulary resources (JSON objects), in an order a store can write them: plans and
// formularies, then drugs, then items. updated is the lastUpdated every one carries, which the profiles require.
func (f *File) Resources(updated string) []map[string]any {
	var plans, drugs, items []map[string]any
	for _, fm := range f.Formularies {
		r := map[string]any{
			"resourceType": "InsurancePlan", "id": FormularyID(fm.ID), "meta": meta("usdf-Formulary", updated),
			"identifier": []any{map[string]any{"value": fm.ID}}, "status": "active",
			"type": []any{cc(actCS, "DRUGPOL", "")}, "name": fm.Name,
			"plan": []any{map[string]any{"type": cc(planCS, "drug", "Drug")}},
		}
		if p := period(fm.Start, fm.End); p != nil {
			r["period"] = p
		}
		plans = append(plans, r)
	}
	for _, pl := range f.Plans {
		fm, _ := f.Formulary(pl.Formulary)
		r := map[string]any{
			"resourceType": "InsurancePlan", "id": PlanID(pl.ID), "meta": meta("usdf-PayerInsurancePlan", updated),
			"identifier": []any{map[string]any{"value": pl.ID}}, "status": "active",
			"type": []any{cc(prodCS, pl.Type, "")}, "name": pl.Name,
			"coverage": []any{map[string]any{
				"extension": []any{map[string]any{"url": ExtFormularyReference,
					"valueReference": map[string]any{"reference": "InsurancePlan/" + FormularyID(pl.Formulary)}}},
				"type":    cc(actCS, "DRUGPOL", ""),
				"benefit": []any{map[string]any{"type": cc(planCS, "drug", "Drug")}},
			}},
			"plan": []any{planCosts(pl)},
		}
		if fm != nil {
			if p := period(fm.Start, fm.End); p != nil {
				r["period"] = p
			}
		}
		var contacts []any
		if pl.Phone != "" {
			contacts = append(contacts, map[string]any{
				"purpose": cc("http://terminology.hl7.org/CodeSystem/contactentity-type", "PATINF", ""),
				"telecom": []any{map[string]any{"system": "phone", "value": pl.Phone}}})
		}
		if pl.Website != "" {
			contacts = append(contacts, map[string]any{
				"purpose": cc(usdfCS+"usdf-PlanContactTypeCS-TEMPORARY-TRIAL-USE", "FORMULARY", ""),
				"name":    map[string]any{"text": pl.Name + " formulary"},
				"telecom": []any{map[string]any{"system": "url", "value": pl.Website}}})
		}
		if len(contacts) > 0 {
			r["contact"] = contacts
		}
		plans = append(plans, r)
	}
	seen := map[string]bool{}
	for _, fm := range f.Formularies {
		for _, d := range fm.Drugs {
			if d.NotCovered {
				// The Drug Formulary API lists what a formulary covers; an excluded drug is answered as not covered by RTPB
				// and ePA from the file, and is simply absent here.
				continue
			}
			if !seen[d.RxNorm] {
				seen[d.RxNorm] = true
				code := cc(RxNorm, d.RxNorm, d.Display)
				code["coding"] = append(code["coding"].([]any), map[string]any{"system": RxNorm, "code": d.Group, "display": d.GroupDisplay})
				drugs = append(drugs, map[string]any{
					"resourceType": "MedicationKnowledge", "id": DrugID(d.RxNorm), "meta": meta("usdf-FormularyDrug", updated),
					"code": code, "status": "active",
				})
			}
			items = append(items, item(fm, d, updated))
		}
	}
	sort.SliceStable(drugs, func(i, j int) bool { return drugs[i]["id"].(string) < drugs[j]["id"].(string) })
	out := append(plans, drugs...)
	return append(out, items...)
}

func planCosts(pl Plan) map[string]any {
	plan := map[string]any{"type": cc(planCS, "drug", "Drug")}
	byBenefit := map[string][]any{}
	var order []string
	for _, c := range pl.Costs {
		var costs []any
		q := []any{cc(shareCS, c.Qualifier, shareDisplay[c.Qualifier])}
		if c.Copay != nil {
			costs = append(costs, map[string]any{"type": cc(costCS, "copay", "Copay"), "qualifiers": q,
				"value": map[string]any{"value": *c.Copay, "unit": "$", "system": "urn:iso:std:iso:4217", "code": "USD"}})
		}
		if c.Coinsurance != nil {
			costs = append(costs, map[string]any{"type": cc(costCS, "coinsurance", "Coinsurance"), "qualifiers": q,
				"value": map[string]any{"value": *c.Coinsurance, "system": "http://unitsofmeasure.org", "code": "%"}})
		}
		if _, ok := byBenefit[c.Benefit]; !ok {
			order = append(order, c.Benefit)
		}
		byBenefit[c.Benefit] = append(byBenefit[c.Benefit], map[string]any{
			"type": cc(tierCS, c.Tier, tierDisplay[c.Tier]), "cost": costs})
	}
	var specific []any
	for _, b := range order {
		specific = append(specific, map[string]any{"category": cc(benCS, b, benefitDisplay[b]), "benefit": byBenefit[b]})
	}
	if len(specific) == 0 {
		// The profile requires a specificCost; a plan that states no costs says so, rather than inventing one.
		specific = []any{map[string]any{"category": cc(benCS, "1-month-in-retail", benefitDisplay["1-month-in-retail"]),
			"benefit": []any{map[string]any{"type": cc(tierCS, "generic", "Generic"), "cost": []any{map[string]any{
				"type": cc(costCS, "copay", "Copay"), "qualifiers": []any{cc(shareCS, "charge", "Charge")}}}}}}}
	}
	plan["specificCost"] = specific
	return plan
}

func item(fm Formulary, d Drug, updated string) map[string]any {
	ext := []any{
		map[string]any{"url": ExtFormularyReference, "valueReference": map[string]any{"reference": "InsurancePlan/" + FormularyID(fm.ID)}},
		map[string]any{"url": ExtAvailabilityStatus, "valueCode": "active"},
	}
	for _, b := range d.Benefits {
		ext = append(ext, map[string]any{"url": ExtPharmacyBenefit, "valueCodeableConcept": cc(benCS, b, benefitDisplay[b])})
	}
	ext = append(ext, map[string]any{"url": ExtDrugTier, "valueCodeableConcept": cc(tierCS, d.Tier, tierDisplay[d.Tier])})
	if p := period(fm.Start, fm.End); p != nil {
		ext = append(ext, map[string]any{"url": ExtAvailabilityPeriod, "valuePeriod": p})
	}
	ext = append(ext,
		map[string]any{"url": ExtPriorAuthorization, "valueBoolean": d.PriorAuthorization},
		map[string]any{"url": ExtStepTherapy, "valueBoolean": d.StepTherapy},
		map[string]any{"url": ExtQuantityLimit, "valueBoolean": d.QuantityLimit != nil})
	if d.PriorAuthorization {
		ext = append(ext, map[string]any{"url": ExtPANewStarts, "valueBoolean": d.PANewStartsOnly})
	}
	if d.StepTherapy {
		ext = append(ext, map[string]any{"url": ExtSTNewStarts, "valueBoolean": d.STNewStartsOnly})
	}
	if q := d.QuantityLimit; q != nil {
		var detail []any
		// Timing.repeat.count is a whole number; a fractional limit (2.5 mL a day, say) is stated as a daily maximum only.
		if q.Quantity == float64(int(q.Quantity)) {
			detail = append(detail, map[string]any{"url": "Rolling", "valueTiming": map[string]any{"repeat": map[string]any{
				"count": int(q.Quantity), "period": q.Days, "periodUnit": "d"}}})
		}
		if q.MaxDaily > 0 {
			detail = append(detail, map[string]any{"url": "MaximumDaily", "valueQuantity": map[string]any{"value": q.MaxDaily}})
		}
		if len(detail) > 0 {
			ext = append(ext, map[string]any{"url": ExtQuantityDetail, "extension": detail})
		}
	}
	if d.Notes != "" {
		ext = append(ext, map[string]any{"url": ExtAdditionalInfo, "valueMarkdown": d.Notes})
	}
	return map[string]any{
		"resourceType": "Basic", "id": ItemID(fm.ID, d.RxNorm), "meta": meta("usdf-FormularyItem", updated),
		"extension": ext, "code": cc(itemCS, "formulary-item", "Formulary Item"),
		"subject": map[string]any{"reference": "MedicationKnowledge/" + DrugID(d.RxNorm)},
	}
}
