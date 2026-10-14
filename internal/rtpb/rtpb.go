// Package rtpb answers NCPDP Real-Time Prescription Benefit requests from a formulary: what a drug will cost this member at
// this pharmacy, whether it needs a prior authorization or a step first, and which alternatives cost less.
//
// CMS-0062-P proposes RTPB for impacted payers from 1 October 2027. The XML is NCPDP RTPB v13 (PBv13). NCPDP's schema is
// licensed, so element names and order follow the public Surescripts RTPB examples and NCPDP's public RTPB Implementation
// Recommendations (coverage restriction codes and the coverage status each maps to); a response is not checked against
// the XSD here, and a network certification is where that happens.
package rtpb

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/formulary"
)

// Version is the RTPB version this package reads and writes.
const Version = "PBv13"

// Request is an RTPBRequest message, as much of it as answering needs.
type Request struct {
	XMLName xml.Name `xml:"Message"`
	Header  Header   `xml:"RTPBHeader"`
	Body    struct {
		Request *RequestBody `xml:"RTPBRequest"`
	} `xml:"RTPBBody"`
}

// Header is the RTPB header.
type Header struct {
	To                 Party  `xml:"To"`
	From               Party  `xml:"From"`
	MessageID          string `xml:"MessageID"`
	RelatesToMessageID string `xml:"RelatesToMessageID,omitempty"`
	SentTime           string `xml:"SentTime"`
}

// Party is a To or From.
type Party struct {
	Primary   ID  `xml:"Primary"`
	Secondary *ID `xml:"Secondary,omitempty"`
	Tertiary  *ID `xml:"Tertiary,omitempty"`
}

// ID is an identification with its qualifier.
type ID struct {
	Identification string `xml:"Identification"`
	Qualifier      string `xml:"Qualifier"`
}

// RequestBody is the RTPBRequest.
type RequestBody struct {
	Patient struct {
		Name struct {
			LastName  string `xml:"LastName"`
			FirstName string `xml:"FirstName"`
		} `xml:"Name"`
		DateOfBirth   string `xml:"DateOfBirth"`
		StateProvince string `xml:"StateProvince"`
		PostalCode    string `xml:"PostalCode"`
	} `xml:"Patient"`
	BenefitsCoordination struct {
		PBMMemberID string `xml:"PBMMemberID"`
		GroupID     string `xml:"GroupID"`
	} `xml:"BenefitsCoordination"`
	RequestedProduct struct {
		Product    Product   `xml:"Product"`
		Quantity   *Quantity `xml:"Quantity"`
		DaysSupply string    `xml:"DaysSupply"`
		Diagnosis  []struct {
			Code      string `xml:"Code"`
			Qualifier string `xml:"Qualifier"`
		} `xml:"Diagnosis"`
	} `xml:"RequestedProduct"`
	Prescriber struct {
		Identification struct {
			NPI string `xml:"NPI"`
		} `xml:"Identification"`
	} `xml:"Prescriber"`
	Pharmacy *Pharmacy `xml:"Pharmacy"`
}

// Product names a drug by NDC.
type Product struct {
	DrugCoded struct {
		NDC string `xml:"NDC"`
	} `xml:"DrugCoded"`
}

// Quantity is an amount and its unit.
type Quantity struct {
	Value                 string `xml:"Value"`
	CodeListQualifier     string `xml:"CodeListQualifier"`
	QuantityUnitOfMeasure struct {
		Code string `xml:"Code"`
	} `xml:"QuantityUnitOfMeasure"`
}

// Pharmacy is where the prescription would be filled.
type Pharmacy struct {
	PharmacyType   string `xml:"PharmacyType,omitempty"`
	Identification struct {
		NCPDPID string `xml:"NCPDPID,omitempty"`
		NPI     string `xml:"NPI,omitempty"`
	} `xml:"Identification"`
	BusinessName string `xml:"BusinessName,omitempty"`
}

// Parse reads an RTPBRequest.
func Parse(raw []byte) (*Request, error) {
	var r Request
	if err := xml.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("this is not an RTPB message: %w", err)
	}
	if r.XMLName.Local != "Message" {
		return nil, fmt.Errorf("the root element is <%s>; an RTPB message is <Message>", r.XMLName.Local)
	}
	if r.Body.Request == nil {
		return nil, fmt.Errorf("the message has no RTPBRequest in its RTPBBody")
	}
	if strings.TrimSpace(r.Header.MessageID) == "" {
		return nil, fmt.Errorf("the RTPBHeader has no MessageID, which the response must quote back")
	}
	return &r, nil
}

// Coverage restriction codes (NCPDP ECL, F12-Y8), and the coverage status each maps to (F13-YA) by NCPDP's RTPB
// Implementation Recommendations.
const (
	RestrictionPriorAuth      = "75"  // Prior Authorization Required
	RestrictionPlanLimits     = "76"  // Plan Limitations Exceeded
	RestrictionNotCovered     = "70"  // Product/Service Not Covered - Plan/Benefit Exclusion
	RestrictionStepTherapy    = "608" // Step Therapy, Alternate Drug Therapy Required Prior To Use Of Submitted Product
	RestrictionDaysSupply     = "7X"  // Days Supply Exceeds Plan Limitation
	RestrictionPAOnFile       = "AB8" // Prior Authorization Approval on File
	StatusCovered             = "CC"
	StatusCoveredRestrictions = "CR"
	StatusNotCovered          = "NC"
)

// Reject codes (NCPDP Telecom 511-FB) for a request that cannot be priced at all.
const (
	RejectCardholder = "52" // Non-Matched Cardholder ID
	RejectProduct    = "54" // Non-Matched Product/Service ID Number
	RejectQuantity   = "E7" // M/I Quantity Dispensed
	RejectDaysSupply = "19" // M/I Days Supply
)

// Answer is what the formulary says about one request, before it is written as XML: for the GUI and for tests.
type Answer struct {
	Processed    bool     `json:"processed"`
	Reject       string   `json:"reject,omitempty"`
	Note         string   `json:"note"`
	Plan         string   `json:"plan,omitempty"`
	Requested    *Priced  `json:"requested,omitempty"`
	Alternatives []Priced `json:"alternatives,omitempty"`
}

// Priced is one drug's coverage and cost.
type Priced struct {
	NDC          string   `json:"ndc"`
	RxNorm       string   `json:"rxnorm"`
	Description  string   `json:"description"`
	Tier         string   `json:"tier"`
	Status       string   `json:"coverageStatus"`
	Formulary    string   `json:"formularyStatus"`
	Restrictions []string `json:"restrictions,omitempty"`
	// PatientPays is the member's estimated share in dollars; nil when it cannot be stated (a coinsurance without a cost).
	PatientPays *float64 `json:"patientPays,omitempty"`
	PlanCost    *float64 `json:"planCost,omitempty"`
	Message     string   `json:"message"`
	Quantity    string   `json:"quantity"`
	Unit        string   `json:"unit"`
	Days        int      `json:"days"`
	Benefit     string   `json:"benefit"`
}

// Options are what answering needs besides the formulary.
type Options struct {
	// Member finds a member's plan id in the FHIR store, or "".
	Member func(id string) string
	// PAOnFile says whether the member has an approved prior authorization for the drug (RxNorm) in force.
	PAOnFile func(member, rxnorm string) bool
}

// Answer prices a request.
func (o Options) Answer(f *formulary.File, req *RequestBody) Answer {
	member := strings.TrimSpace(req.BenefitsCoordination.PBMMemberID)
	plan, ok := f.PlanFor(member, req.BenefitsCoordination.GroupID, o.Member)
	if !ok {
		return Answer{Reject: RejectCardholder, Note: "No member with this PBM member ID or group is on any plan here."}
	}
	fm, _ := f.Formulary(plan.Formulary)
	rp := req.RequestedProduct
	ndc := strings.TrimSpace(rp.Product.DrugCoded.NDC)
	quantity, unit := 0.0, ""
	if rp.Quantity != nil {
		quantity, _ = strconv.ParseFloat(strings.TrimSpace(rp.Quantity.Value), 64)
		unit = rp.Quantity.QuantityUnitOfMeasure.Code
	}
	days, _ := strconv.Atoi(strings.TrimSpace(rp.DaysSupply))
	switch {
	case quantity <= 0:
		return Answer{Reject: RejectQuantity, Plan: plan.ID, Note: "The quantity is missing or not above zero."}
	case days <= 0:
		return Answer{Reject: RejectDaysSupply, Plan: plan.ID, Note: "The days supply is missing or not above zero; it is needed to price the fill."}
	}
	d, err := fm.Find(formulary.NDC, ndc)
	if err != nil && err != formulary.ErrNotListed {
		return Answer{Reject: RejectProduct, Plan: plan.ID, Note: "The NDC cannot be read: " + err.Error()}
	}
	// The pharmacy benefit is the supply length and the channel: a fill over 34 days is a three-month supply, and a mail
	// order pharmacy (PharmacyType M) is mail. Out-of-network benefits are not inferred: a request does not say.
	benefit := "1-month-in-retail"
	switch mail := req.Pharmacy != nil && strings.EqualFold(req.Pharmacy.PharmacyType, "M"); {
	case days > 34 && mail:
		benefit = "3-month-in-mail"
	case days > 34:
		benefit = "3-month-in-retail"
	case mail:
		benefit = "1-month-in-mail"
	}
	if d == nil {
		p := Priced{NDC: ndc, Description: "Not on the formulary", Status: StatusNotCovered, Formulary: "N",
			Restrictions: []string{RestrictionNotCovered}, Quantity: trim(quantity), Unit: unit, Days: days, Benefit: benefit,
			Message: "This drug is not on the " + fm.Name + "."}
		return Answer{Processed: true, Plan: plan.ID, Note: "Not on the formulary.", Requested: &p}
	}
	requested := o.price(plan, fm, d, ndc, quantity, unit, days, benefit, member)
	a := Answer{Processed: true, Plan: plan.ID, Note: "Success", Requested: &requested}
	for _, alt := range d.Alternatives {
		ad, err := fm.Find(formulary.RxNorm, alt)
		if err != nil || ad.NotCovered || len(ad.NDCs) == 0 {
			continue
		}
		a.Alternatives = append(a.Alternatives, o.price(plan, fm, ad, ad.NDCs[0], quantity, unit, days, benefit, member))
		if len(a.Alternatives) == 5 {
			// NCPDP allows ten; Surescripts recommends at most five, to keep the prescriber's screen readable.
			break
		}
	}
	return a
}

func trim(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func formularyStatus(d *formulary.Drug) string {
	switch {
	case d.NotCovered:
		return "N"
	case d.Tier == "zero-cost-share-preventative":
		return "Z"
	case strings.HasPrefix(d.Tier, "non-preferred"):
		return "O"
	default:
		return "P"
	}
}

func (o Options) price(plan *formulary.Plan, fm *formulary.Formulary, d *formulary.Drug, ndc string, quantity float64, unit string,
	days int, benefit, member string) Priced {
	p := Priced{NDC: ndc, RxNorm: d.RxNorm, Description: d.Display, Tier: d.Tier, Formulary: formularyStatus(d),
		Quantity: trim(quantity), Unit: unit, Days: days, Benefit: benefit}
	var why []string
	if d.NotCovered {
		p.Status, p.Restrictions = StatusNotCovered, []string{RestrictionNotCovered}
		p.Message = "Not covered by this plan."
		if d.Notes != "" {
			p.Message += " " + d.Notes
		}
		return p
	}
	onFile := o.PAOnFile != nil && member != "" && o.PAOnFile(member, d.RxNorm)
	if d.PriorAuthorization {
		if onFile {
			p.Restrictions = append(p.Restrictions, RestrictionPAOnFile)
			why = append(why, "a prior authorization is approved and on file")
		} else {
			p.Restrictions = append(p.Restrictions, RestrictionPriorAuth)
			why = append(why, "prior authorization required")
		}
	}
	if d.StepTherapy && !onFile {
		p.Restrictions = append(p.Restrictions, RestrictionStepTherapy)
		why = append(why, "step therapy required")
	}
	available := false
	for _, b := range d.Benefits {
		available = available || b == benefit
	}
	if !available {
		p.Restrictions = append(p.Restrictions, RestrictionDaysSupply)
		why = append(why, "not available as a "+strings.ReplaceAll(benefit, "-", " ")+" fill")
	}
	if over, reason := d.ExceedsLimit(quantity, days); over && !onFile {
		p.Restrictions = append(p.Restrictions, RestrictionPlanLimits)
		why = append(why, reason)
	}
	p.Status = StatusCovered
	for _, r := range p.Restrictions {
		if r != RestrictionPAOnFile {
			p.Status = StatusCoveredRestrictions
		}
	}
	months := math.Max(1, math.Round(float64(days)/30))
	if d.Cost30 > 0 {
		c := round2(d.Cost30 * float64(days) / 30)
		p.PlanCost = &c
	}
	if c, ok := plan.Cost(benefit, d.Tier); ok {
		switch {
		case c.Copay != nil:
			pays := *c.Copay
			if !strings.HasPrefix(benefit, "3-month") {
				pays = round2(pays * months)
			}
			p.PatientPays = &pays
		case c.Coinsurance != nil && p.PlanCost != nil:
			pays := round2(*p.PlanCost * *c.Coinsurance / 100)
			p.PatientPays = &pays
			left := round2(*p.PlanCost - pays)
			p.PlanCost = &left
		case c.Coinsurance != nil:
			why = append(why, fmt.Sprintf("%g%% coinsurance", *c.Coinsurance))
		}
		if p.PatientPays != nil && p.PlanCost != nil && c.Copay != nil {
			left := round2(math.Max(0, *p.PlanCost-*p.PatientPays))
			p.PlanCost = &left
		}
	} else {
		why = append(why, "the plan states no cost share for this tier and pharmacy")
	}
	switch {
	case len(why) == 0:
		p.Message = "Covered."
	default:
		p.Message = "Covered: " + strings.Join(why, "; ") + "."
	}
	if d.Notes != "" {
		p.Message += " " + d.Notes
	}
	return p
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Respond writes the RTPBResponse to a request.
func Respond(req *Request, a Answer, payerID string, now time.Time) []byte {
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	esc := func(s string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	from := req.Header.From.Primary
	w(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w(`<Message RTPBDatatypesVersion="%[1]s" RTPBTransportVersion="%[1]s" RTPBTransactionDomain="RTPB" RTPBTransactionVersion="%[1]s" RTPBStructuresVersion="%[1]s" RTPBECLVersion="%[1]s" RTPBVersion="%[1]s">`+"\n", Version)
	w("  <RTPBHeader>\n    <To>\n      <Primary>\n        <Identification>%s</Identification>\n        <Qualifier>%s</Qualifier>\n      </Primary>\n    </To>\n",
		esc(from.Identification), esc(from.Qualifier))
	w("    <From>\n      <Primary>\n        <Identification>%s</Identification>\n        <Qualifier>PY</Qualifier>\n      </Primary>\n    </From>\n", esc(payerID))
	w("    <MessageID>%s</MessageID>\n    <RelatesToMessageID>%s</RelatesToMessageID>\n    <SentTime>%s</SentTime>\n  </RTPBHeader>\n",
		newID(now), esc(req.Header.MessageID), now.UTC().Format("2006-01-02T15:04:05.0Z"))
	w("  <RTPBBody>\n    <RTPBResponse>\n      <Response>\n")
	if !a.Processed {
		w("        <NotProcessed>\n          <RejectCode>%s</RejectCode>\n          <Note>%s</Note>\n        </NotProcessed>\n      </Response>\n",
			esc(a.Reject), esc(a.Note))
	} else {
		w("        <Processed>\n          <Note>%s</Note>\n        </Processed>\n      </Response>\n", esc(a.Note))
		product(&b, "ResponseProduct", *a.Requested, req, esc)
		for _, alt := range a.Alternatives {
			product(&b, "ResponseAlternativeProduct", alt, req, esc)
		}
	}
	w("    </RTPBResponse>\n  </RTPBBody>\n</Message>\n")
	return b.Bytes()
}

func product(b *bytes.Buffer, element string, p Priced, req *Request, esc func(string) string) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("      <%s>\n        <Product>\n          <DrugCoded>\n            <NDC>%s</NDC>\n          </DrugCoded>\n        </Product>\n", element, esc(p.NDC))
	w("        <DrugDescription>%s</DrugDescription>\n", esc(p.Description))
	quantity := func(indent string) {
		w("%s<Quantity>\n%s  <Value>%s</Value>\n%s  <CodeListQualifier>38</CodeListQualifier>\n", indent, indent, esc(p.Quantity), indent)
		if p.Unit != "" {
			w("%s  <QuantityUnitOfMeasure>\n%s    <Code>%s</Code>\n%s  </QuantityUnitOfMeasure>\n", indent, indent, esc(p.Unit), indent)
		}
		w("%s</Quantity>\n", indent)
	}
	quantity("        ")
	w("        <PricingAndCoverages>\n")
	kind := "R"
	if strings.Contains(p.Benefit, "mail") {
		kind = "M"
	}
	if ph := req.Body.Request.Pharmacy; ph != nil {
		w("          <Pharmacy>\n            <PharmacyType>%s</PharmacyType>\n            <Identification>\n", kind)
		if ph.Identification.NCPDPID != "" {
			w("              <NCPDPID>%s</NCPDPID>\n", esc(ph.Identification.NCPDPID))
		}
		if ph.Identification.NPI != "" {
			w("              <NPI>%s</NPI>\n", esc(ph.Identification.NPI))
		}
		w("            </Identification>\n")
		if ph.BusinessName != "" {
			w("            <BusinessName>%s</BusinessName>\n", esc(ph.BusinessName))
		}
		w("          </Pharmacy>\n")
	}
	w("          <PricingAndCoverage>\n            <PricingAndCoverageIndicator>%s</PricingAndCoverageIndicator>\n", kind)
	w("            <CoverageStatusCode>%s</CoverageStatusCode>\n            <FormularyStatus>%s</FormularyStatus>\n", p.Status, p.Formulary)
	quantity("            ")
	w("            <DaysSupply>%d</DaysSupply>\n", p.Days)
	if p.PatientPays != nil {
		w("            <EstimatedPatientFinancialResponsibility>%s</EstimatedPatientFinancialResponsibility>\n", money(*p.PatientPays))
	}
	if p.PlanCost != nil {
		w("            <EstimatedNetPlanCost>%s</EstimatedNetPlanCost>\n", money(*p.PlanCost))
	}
	for _, r := range p.Restrictions {
		w("            <CoverageRestriction>\n              <CoverageRestrictionCode>%s</CoverageRestrictionCode>\n            </CoverageRestriction>\n", r)
	}
	w("            <CoverageStatusMessage>%s</CoverageStatusMessage>\n", esc(p.Message))
	w("          </PricingAndCoverage>\n        </PricingAndCoverages>\n      </%s>\n", element)
}

func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func newID(now time.Time) string {
	return fmt.Sprintf("RTPB%x", now.UnixNano())
}
