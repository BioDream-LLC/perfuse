// Package cms0057 holds the payer-side pieces of the CMS Interoperability and Prior Authorization final rule (CMS-0057-F)
// that are about data rather than transport: adjudicated claims as CARIN Blue Button ExplanationOfBenefit resources, prior
// authorisations as Da Vinci PDex ExplanationOfBenefit resources, HRex member matching for the Payer-to-Payer API, the
// member list behind the Provider Access API, and the prior authorisation metrics a payer has to publish every year.
//
// The FHIR endpoints that serve these live in fhirserver; this package decides what goes in them.
//
// Drugs are out of scope throughout, as they are in the rule's prior authorisation provisions: no pharmacy
// ExplanationOfBenefit, no formulary, no NCPDP.
package cms0057

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// The implementation guide versions this package writes to. Named once so the capability statement, the documentation
// and the profiles in meta.profile cannot disagree.
const (
	CARINVersion = "2.2.0"
	PDexVersion  = "2.2.0"
	HRexVersion  = "1.2.0"
	ATRVersion   = "2.1.0"
	PASVersion   = "2.2.1"

	carinBase = "http://hl7.org/fhir/us/carin-bb/StructureDefinition/"
	carinCS   = "http://hl7.org/fhir/us/carin-bb/CodeSystem/"
)

// Code systems, spelled once.
const (
	sysClaimType     = "http://terminology.hl7.org/CodeSystem/claim-type"
	sysAdjudication  = "http://terminology.hl7.org/CodeSystem/adjudication"
	sysPayeeType     = "http://terminology.hl7.org/CodeSystem/payeetype"
	sysDiagType      = "http://terminology.hl7.org/CodeSystem/ex-diagnosistype"
	sysCareTeamRole  = "http://terminology.hl7.org/CodeSystem/claimcareteamrole"
	sysRelatedClaim  = "http://terminology.hl7.org/CodeSystem/ex-relatedclaimrelationship"
	sysV20203        = "http://terminology.hl7.org/CodeSystem/v2-0203"
	sysSubscriberRel = "http://terminology.hl7.org/CodeSystem/subscriber-relationship"
	sysCoverageClass = "http://terminology.hl7.org/CodeSystem/coverage-class"
	sysDataAbsent    = "http://terminology.hl7.org/CodeSystem/data-absent-reason"
	sysNPI           = "http://hl7.org/fhir/sid/us-npi"
	sysTIN           = "urn:oid:2.16.840.1.113883.4.4"
	sysICD10CM       = "http://hl7.org/fhir/sid/icd-10-cm"
	sysICD9CM        = "http://hl7.org/fhir/sid/icd-9-cm"
	sysICD10PCS      = "http://www.cms.gov/Medicare/Coding/ICD10"
	sysCPT           = "http://www.ama-assn.org/go/cpt"
	sysHCPCS         = "https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
	sysHIPPS         = "https://www.cms.gov/Medicare/Medicare-Fee-for-Service-Payment/ProspMedicareFeeSvcPmtGen/HIPPSCodes"
	sysPOS           = "https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set"
	sysRevenue       = "https://www.nubc.org/CodeSystem/RevenueCodes"
	sysTypeOfBill    = "https://www.nubc.org/CodeSystem/TypeOfBill"
	sysPointOfOrigin = "https://www.nubc.org/CodeSystem/PointOfOrigin"
	sysAdmitType     = "https://www.nubc.org/CodeSystem/PriorityTypeOfAdmitOrVisit"
	sysDischarge     = "https://www.nubc.org/CodeSystem/PatDischargeStatus"
	sysMSDRG         = "https://www.cms.gov/Medicare/Medicare-Fee-for-Service-Payment/AcuteInpatientPPS/MS-DRG-Classifications-and-Software"
	sysPOA           = "https://www.cms.gov/Medicare/Medicare-Fee-for-Service-Payment/HospitalAcqCond/Coding"
	sysCARC          = "https://x12.org/codes/claim-adjustment-reason-codes"
	sysRARC          = "https://x12.org/codes/remittance-advice-remark-codes"
	sysTaxonomy      = "http://nucc.org/provider-taxonomy"
)

// CARINOptions are the facts a CARIN ExplanationOfBenefit needs that no X12 transaction carries.
type CARINOptions struct {
	// IdentifierSystem is the payer's namespace for the identifiers it assigns - member ids and claim numbers. A URI such as
	// https://healthplan.example.org/fhir/identifier. Empty leaves identifier.system out, which FHIR permits; inventing one would
	// give every payer using this the same namespace, which is worse than none.
	IdentifierSystem string

	// NetworkStatus is the billing provider's network status for this payer: "innetwork" or "outofnetwork". Neither the 837
	// nor the 835 says, and it decides what a member pays, so it is the payer's to supply. Empty reports the benefit payment
	// status as "other" and says so in the notes.
	NetworkStatus string

	// Now stamps meta.lastUpdated and is the fallback for EOB.created. Zero means time.Now.
	Now time.Time

	// BaseURL, when set, gives every bundle entry a fullUrl of BaseURL/Type/id - the server the bundle is going to - so that
	// the references between the resources resolve inside the bundle as well as after it is loaded. Empty leaves fullUrl
	// out, which a transaction of PUTs permits.
	BaseURL string
}

// CARINResult is one claim converted.
type CARINResult struct {
	// ClaimNumber is the payer's claim control number (835 CLP07), which becomes the CARIN unique claim identifier.
	ClaimNumber string `json:"claimNumber"`
	// Profile is the CARIN profile the ExplanationOfBenefit claims conformance to.
	Profile string `json:"profile"`
	// Bundle is a FHIR transaction of PUTs: the ExplanationOfBenefit with the Patient, Coverage, Organizations and
	// Practitioners it references. PUT with ids derived from the source identifiers, so loading the same claim twice
	// updates rather than duplicates.
	Bundle map[string]any `json:"bundle"`
	// Notes says what was assumed or left out, in words.
	Notes []string `json:"notes"`
}

// ConvertClaims pairs each claim in an 837 with its adjudication in an 835 and converts the pairs.
//
// Paired by the patient account number (CLM01 / CLP01), which is the one value the 835 is required to echo back. A claim the 835
// does not mention is refused rather than converted, because a CARIN ExplanationOfBenefit is an adjudicated claim: without the
// adjudication there is no unique claim identifier, no payment and no member liability - and an EOB with those invented is
// worse than one that does not exist.
func ConvertClaims(raw837, raw835 []byte, opt CARINOptions) ([]CARINResult, []string, error) {
	m837, err := x12.Parse(raw837)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the 837: %w", err)
	}
	claims, err := x12.ParseClaims(m837)
	if err != nil {
		return nil, nil, err
	}
	m835, err := x12.Parse(raw835)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the 835: %w", err)
	}

	// Every 835 transaction set, because a payer's remittance file often holds several.
	sets, err := m835.Split()
	if err != nil {
		return nil, nil, fmt.Errorf("splitting the 835: %w", err)
	}
	type paid struct {
		remit *x12.Remittance
		claim *x12.RemittanceClaim
	}
	byAccount := map[string]paid{}
	for _, set := range sets {
		if !strings.Contains(strings.Join(set.TransactionSets(), ","), "835") {
			continue
		}
		r, err := x12.ParseERA(set)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the 835: %w", err)
		}
		for i := range r.Claims {
			byAccount[r.Claims[i].PatientAccountNumber] = paid{remit: r, claim: &r.Claims[i]}
		}
	}
	if len(byAccount) == 0 {
		return nil, nil, fmt.Errorf("the second file contains no 835 claim payment (CLP) to pair with")
	}

	var (
		out     []CARINResult
		skipped []string
	)
	for i := range claims {
		p, ok := byAccount[claims[i].PatientAccountNumber]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("claim %s is not in the 835, so it has not been adjudicated and was not converted",
				claims[i].PatientAccountNumber))
			continue
		}
		res, err := ToCARIN(&claims[i], p.claim, p.remit, opt)
		if err != nil {
			return nil, nil, fmt.Errorf("claim %s: %w", claims[i].PatientAccountNumber, err)
		}
		out = append(out, *res)
	}
	if len(out) == 0 {
		return nil, skipped, fmt.Errorf("none of the %d claims in the 837 appears in the 835", len(claims))
	}

	return out, skipped, nil
}

// ToCARIN converts one adjudicated claim.
func ToCARIN(c *x12.Claim, rc *x12.RemittanceClaim, remit *x12.Remittance, opt CARINOptions) (*CARINResult, error) {
	if strings.TrimSpace(opt.IdentifierSystem) == "" {
		return nil, fmt.Errorf("CARIN requires the member identifier to name the payer's identifier system; " +
			"set it to a URI the payer owns, such as https://<your domain>/fhir/member-id")
	}
	if rc == nil || rc.PayerClaimControlNumber == "" {
		return nil, fmt.Errorf("the 835 gives no payer claim control number (CLP07), which CARIN requires as the unique claim identifier")
	}
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	b := &carinBuilder{c: c, rc: rc, remit: remit, opt: opt, now: now.UTC()}

	return b.build()
}

type carinBuilder struct {
	c     *x12.Claim
	rc    *x12.RemittanceClaim
	remit *x12.Remittance
	opt   CARINOptions
	now   time.Time

	entries []any
	ids     map[string]bool
	notes   []string
}

func (b *carinBuilder) note(format string, args ...any) {
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
}

// meta names the profile with its version, which CARIN's EOB invariants require ("meta.profile with canonical and major.minor.
// version"): a reader holding an EOB needs to know which edition of the rules it was written to.
func (b *carinBuilder) meta(profile string) map[string]any {
	return map[string]any{
		"lastUpdated": b.now.Format(time.RFC3339),
		"profile":     []any{carinBase + profile + "|" + CARINVersion},
	}
}

// add puts a resource in the bundle once, keyed by type and id.
func (b *carinBuilder) add(r map[string]any) string {
	ref := r["resourceType"].(string) + "/" + r["id"].(string)
	if b.ids == nil {
		b.ids = map[string]bool{}
	}
	if b.ids[ref] {
		return ref
	}
	b.ids[ref] = true
	entry := map[string]any{
		"resource": r,
		"request":  map[string]any{"method": "PUT", "url": ref},
	}
	if base := strings.TrimRight(b.opt.BaseURL, "/"); base != "" {
		entry["fullUrl"] = base + "/" + ref
	}
	b.entries = append(b.entries, entry)

	return ref
}

func (b *carinBuilder) identifier(typeSystem, typeCode, value string) map[string]any {
	id := map[string]any{
		"type":  codeable(typeSystem, typeCode, ""),
		"value": value,
	}
	if b.opt.IdentifierSystem != "" {
		id["system"] = b.opt.IdentifierSystem
	}

	return id
}

func (b *carinBuilder) build() (*CARINResult, error) {
	c, rc := b.c, b.rc

	payerRef := b.payer()
	patientRef := b.patient()
	coverageRef := b.coverage(patientRef, payerRef)
	providerRef := b.organization(c.BillingProvider)

	profile := "C4BB-ExplanationOfBenefit-Professional-NonClinician"
	claimType := "professional"
	if c.Kind == x12.ClaimInstitutional {
		claimType = "institutional"
		profile = "C4BB-ExplanationOfBenefit-Outpatient-Institutional"
		if c.IsInpatient() {
			profile = "C4BB-ExplanationOfBenefit-Inpatient-Institutional"
		}
	}

	eob := map[string]any{
		"resourceType": "ExplanationOfBenefit",
		"id":           fhirID("eob", rc.PayerClaimControlNumber),
		"meta":         b.meta(profile),
		"identifier":   []any{b.identifier(carinCS+"C4BBIdentifierType", "uc", rc.PayerClaimControlNumber)},
		"status":       "active",
		// The claim-type version is part of CARIN's pattern for the institutional profiles, so it is always written.
		"type": map[string]any{"coding": []any{map[string]any{
			"system": sysClaimType, "version": "1.0.1", "code": claimType,
		}}},
		"use":       "claim",
		"patient":   map[string]any{"reference": patientRef},
		"insurer":   map[string]any{"reference": payerRef},
		"provider":  map[string]any{"reference": providerRef},
		"outcome":   "complete",
		"insurance": []any{map[string]any{"focal": true, "coverage": map[string]any{"reference": coverageRef}}},
	}

	// A reversal (CLP02 22) is the payer taking back an earlier payment. It is not a new claim, and showing it as an
	// active one would tell a member they had been billed twice.
	if rc.ClaimStatus == "22" {
		eob["status"] = "cancelled"
		b.note("the 835 reports this claim as a reversal of a previous payment (CLP02 22), so the ExplanationOfBenefit is cancelled")
	}

	if c.Kind == x12.ClaimInstitutional {
		sub := "outpatient"
		if c.IsInpatient() {
			sub = "inpatient"
		}
		eob["subType"] = codeable(carinCS+"C4BBInstitutionalClaimSubType", sub, "")
	}

	// The billable period: the statement dates on an institutional claim, the span of the service lines on a professional one.
	from, to := c.StatementFrom, c.StatementTo
	for _, l := range c.Lines {
		if from == "" || (l.ServiceDateFrom != "" && l.ServiceDateFrom < from) {
			from = l.ServiceDateFrom
		}
		if l.ServiceDateTo > to {
			to = l.ServiceDateTo
		}
	}
	if from == "" {
		from, to = rc.StatementFromDate, rc.StatementToDate
	}
	if from == "" {
		return nil, fmt.Errorf("neither the 837 nor the 835 gives a date of service, and CARIN requires the billable period's start")
	}
	period := map[string]any{"start": fhirDate(from)}
	if to != "" {
		period["end"] = fhirDate(to)
	}
	eob["billablePeriod"] = period

	// created is when this explanation was produced: the 835's production date, which is when the payer finished adjudicating.
	switch {
	case b.remit != nil && b.remit.ProductionDate != "":
		eob["created"] = fhirDate(b.remit.ProductionDate)
	case b.remit != nil && b.remit.PaymentDate != "":
		eob["created"] = fhirDate(b.remit.PaymentDate)
	default:
		eob["created"] = b.now.Format(time.RFC3339)
		b.note("the 835 has no production date (DTM*405), so the ExplanationOfBenefit's created date is the time of conversion")
	}

	// A replacement or void (CLM05-3 7 or 8) points at the claim it replaces, by the payer's number in REF*F8.
	if (c.FrequencyCode == "7" || c.FrequencyCode == "8") && c.OriginalReference != "" {
		eob["related"] = []any{map[string]any{
			"relationship": codeable(sysRelatedClaim, "prior", ""),
			"reference":    b.identifier(carinCS+"C4BBIdentifierType", "uc", c.OriginalReference),
		}}
	}

	eob["payee"] = map[string]any{
		"type":  codeable(sysPayeeType, "provider", ""),
		"party": map[string]any{"reference": providerRef},
	}

	eob["supportingInfo"] = b.supportingInfo(providerRef)
	if ct := b.careTeam(); len(ct) > 0 {
		eob["careTeam"] = ct
	}

	diagnoses, pointer, err := b.diagnoses()
	if err != nil {
		return nil, err
	}
	eob["diagnosis"] = diagnoses
	if procs := b.procedures(); len(procs) > 0 {
		eob["procedure"] = procs
	}

	items, totals := b.items(pointer)
	eob["item"] = items

	// Header adjudication: the network status the CARIN profiles slice for, and on an institutional claim the benefit
	// payment status and any claim-level adjustment reasons, which is where an institutional 835 usually puts them.
	var header []any
	if s := b.opt.NetworkStatus; s == "innetwork" || s == "outofnetwork" {
		header = append(header, discriminator("billingnetworkstatus", s))
	}
	if c.Kind == x12.ClaimInstitutional {
		header = append(header, discriminator("benefitpaymentstatus", b.benefitStatus()))
		for _, a := range rc.Adjustments {
			header = append(header, adjustment(a))
		}
	}
	if len(header) > 0 {
		eob["adjudication"] = header
	}

	// Claim-level adjustments are part of the totals whether or not there are service lines in the 835.
	for _, a := range rc.Adjustments {
		totals.addAdjustment(a)
	}
	eob["total"] = totals.render(rc)

	eob["payment"] = b.payment()

	b.add(eob)
	if b.opt.NetworkStatus == "" {
		b.note("neither X12 transaction carries the provider's network status, so the benefit payment status is \"other\"; set the network status to report it")
	}
	if !c.PatientIsSubscriber {
		b.note("the patient is a dependent; the member identifier is the subscriber's (%s), which is what the 837 carries", c.Subscriber.ID)
	}

	// The ExplanationOfBenefit first, because it is what the bundle is about.
	sort.SliceStable(b.entries, func(i, j int) bool {
		ri := b.entries[i].(map[string]any)["resource"].(map[string]any)["resourceType"]
		return ri == "ExplanationOfBenefit"
	})

	return &CARINResult{
		ClaimNumber: rc.PayerClaimControlNumber,
		Profile:     carinBase + profile,
		Bundle: map[string]any{
			"resourceType": "Bundle",
			"type":         "transaction",
			"entry":        b.entries,
		},
		Notes: b.notes,
	}, nil
}

func (b *carinBuilder) payer() string {
	p := b.c.Payer
	name := p.LastName
	if name == "" && b.remit != nil {
		name = b.remit.PayerName
	}
	key := p.ID
	if key == "" {
		key = name
	}
	org := map[string]any{
		"resourceType": "Organization",
		"id":           fhirID("payer", key),
		"meta":         b.meta("C4BB-Organization"),
		"active":       true,
		"name":         name,
	}
	if p.ID != "" {
		org["identifier"] = []any{map[string]any{
			"type":  codeable(carinCS+"C4BBIdentifierType", "payerid", ""),
			"value": p.ID,
		}}
	}

	return b.add(org)
}

func (b *carinBuilder) organization(p x12.ClaimParty) string {
	key := p.ID
	if key == "" {
		key = p.LastName
	}
	org := map[string]any{
		"resourceType": "Organization",
		"id":           fhirID("org", key),
		"meta":         b.meta("C4BB-Organization"),
		"active":       true,
		"name":         p.Name(),
	}
	var ids []any
	if p.IDQualifier == "XX" && p.ID != "" {
		b.checkNPI(p)
		ids = append(ids, map[string]any{"type": codeable(sysV20203, "NPI", ""), "system": sysNPI, "value": p.ID})
	}
	if p.TaxID != "" {
		ids = append(ids, map[string]any{"type": codeable(sysV20203, "TAX", ""), "system": sysTIN, "value": p.TaxID})
	}
	if len(ids) > 0 {
		org["identifier"] = ids
	}
	if addr := address(p); addr != nil {
		org["address"] = []any{addr}
	}

	return b.add(org)
}

// provider returns a reference to a practitioner or organisation, by the NM102 entity type.
func (b *carinBuilder) provider(p x12.ClaimParty) string {
	if p.EntityType != "1" {
		return b.organization(p)
	}
	key := p.ID
	if key == "" {
		key = p.Name()
	}
	pr := map[string]any{
		"resourceType": "Practitioner",
		"id":           fhirID("pr", key),
		"meta":         b.meta("C4BB-Practitioner"),
		"name":         []any{humanName(p)},
	}
	if p.IDQualifier == "XX" && p.ID != "" {
		b.checkNPI(p)
		pr["identifier"] = []any{map[string]any{"type": codeable(sysV20203, "NPI", ""), "system": sysNPI, "value": p.ID}}
	}

	return b.add(pr)
}

func (b *carinBuilder) patient() string {
	c := b.c
	p := c.Patient
	memberID := c.Subscriber.ID
	if p.IDQualifier == "MI" && p.ID != "" {
		memberID = p.ID
	}
	// A dependent shares the subscriber's member id, so the resource id adds what tells them apart.
	key := memberID
	if !c.PatientIsSubscriber {
		key = memberID + "-" + p.FirstName + "-" + p.BirthDate
	}
	pt := map[string]any{
		"resourceType": "Patient",
		"id":           fhirID("pt", key),
		"meta":         b.meta("C4BB-Patient"),
		"identifier": []any{map[string]any{
			"type": map[string]any{"coding": []any{map[string]any{
				"system": sysV20203, "version": "5.0.0", "code": "MB",
			}}},
			"value": memberID,
		}},
		"name": []any{humanName(p)},
	}
	if b.opt.IdentifierSystem != "" {
		pt["identifier"].([]any)[0].(map[string]any)["system"] = b.opt.IdentifierSystem
	}
	if g := gender(p.Gender); g != "" {
		pt["gender"] = g
	}
	if p.BirthDate != "" {
		pt["birthDate"] = fhirDate(p.BirthDate)
	}
	if addr := address(p); addr != nil {
		pt["address"] = []any{addr}
	}

	return b.add(pt)
}

func (b *carinBuilder) coverage(patientRef, payerRef string) string {
	c := b.c
	cov := map[string]any{
		"resourceType": "Coverage",
		"id":           fhirID("cov", c.Subscriber.ID+"-"+c.Payer.ID+"-"+c.GroupNumber+"-"+strings.TrimPrefix(patientRef, "Patient/")),
		"meta":         b.meta("C4BB-Coverage"),
		"status":       "active",
		"subscriberId": c.Subscriber.ID,
		"beneficiary":  map[string]any{"reference": patientRef},
		"relationship": codeable(sysSubscriberRel, relationship(c.Relationship), ""),
		"payor":        []any{map[string]any{"reference": payerRef}},
	}
	if c.GroupNumber != "" {
		class := map[string]any{"type": codeable(sysCoverageClass, "group", ""), "value": c.GroupNumber}
		if c.GroupName != "" {
			class["name"] = c.GroupName
		}
		cov["class"] = []any{class}
	}

	return b.add(cov)
}

func (b *carinBuilder) supportingInfo(providerRef string) []any {
	c, rc := b.c, b.rc
	var out []any
	add := func(category string, fields map[string]any) {
		e := map[string]any{"sequence": len(out) + 1, "category": codeable(carinCS+"C4BBSupportingInfoType", category, "")}
		for k, v := range fields {
			e[k] = v
		}
		out = append(out, e)
	}

	if rc.ReceivedDate != "" {
		add("clmrecvddate", map[string]any{"timingDate": fhirDate(rc.ReceivedDate)})
	} else {
		b.note("the 835 has no claim received date (DTM*050), so the CARIN claim received date is absent")
	}
	if c.PatientAccountNumber != "" {
		add("patientaccountnumber", map[string]any{"valueString": c.PatientAccountNumber})
	}
	if c.MedicalRecordNumber != "" {
		add("medicalrecordnumber", map[string]any{"valueString": c.MedicalRecordNumber})
	}
	if c.ServiceFacility.LastName != "" || c.ServiceFacility.ID != "" {
		add("servicefacility", map[string]any{"valueReference": map[string]any{"reference": b.organization(c.ServiceFacility)}})
	}

	if c.Kind == x12.ClaimInstitutional {
		if tob := c.TypeOfBill(); tob != "" {
			add("typeofbill", map[string]any{"code": codeable(sysTypeOfBill, tob, "")})
		}
		if c.AdmissionSource != "" {
			add("pointoforigin", map[string]any{"code": codeable(sysPointOfOrigin, c.AdmissionSource, "")})
		}
		if c.AdmissionType != "" {
			add("admtype", map[string]any{"code": codeable(sysAdmitType, c.AdmissionType, "")})
		}
		if c.DischargeStatus != "" {
			add("discharge-status", map[string]any{"code": codeable(sysDischarge, c.DischargeStatus, "")})
		}
		drg := c.DRG
		if drg == "" {
			drg = rc.DRG
		}
		if drg != "" {
			add("drg", map[string]any{"code": codeable(sysMSDRG, drg, "")})
		}
		if c.IsInpatient() {
			admit := c.AdmissionDate
			if admit == "" {
				admit = c.StatementFrom
			}
			p := map[string]any{}
			if admit != "" {
				p["start"] = fhirDate(admit)
			}
			if c.StatementTo != "" && c.DischargeStatus != "30" {
				// Discharge status 30 is "still a patient": the statement ends but the stay does not.
				p["end"] = fhirDate(c.StatementTo)
			}
			add("admissionperiod", map[string]any{"timingPeriod": p})
		}
	}

	return out
}

func (b *carinBuilder) careTeam() []any {
	c := b.c
	var out []any
	add := func(p x12.ClaimParty, system, role string) {
		if p.ID == "" && p.LastName == "" {
			return
		}
		e := map[string]any{
			"sequence": len(out) + 1,
			"provider": map[string]any{"reference": b.provider(p)},
			"role":     codeable(system, role, ""),
		}
		if p.Taxonomy != "" {
			e["qualification"] = codeable(sysTaxonomy, p.Taxonomy, "")
		}
		out = append(out, e)
	}

	if c.Kind == x12.ClaimProfessional {
		rendering := c.Rendering
		if rendering.ID == "" && rendering.LastName == "" {
			// No 2310B means the billing provider rendered the service, which the guide states.
			rendering = c.BillingProvider
		}
		add(rendering, carinCS+"C4BBClaimCareTeamRole", "rendering")
		add(c.Referring, carinCS+"C4BBClaimCareTeamRole", "referring")
		add(c.Supervising, sysCareTeamRole, "supervisor")
	} else {
		add(c.Attending, carinCS+"C4BBClaimCareTeamRole", "attending")
		add(c.Operating, carinCS+"C4BBClaimCareTeamRole", "operating")
		add(c.Rendering, carinCS+"C4BBClaimCareTeamRole", "rendering")
		add(c.Referring, carinCS+"C4BBClaimCareTeamRole", "referring")
	}

	return out
}

// diagnoses returns the diagnosis entries and, for professional claims, a map from the claim's diagnosis position (what SV107
// points at) to the sequence number it was given.
func (b *carinBuilder) diagnoses() ([]any, map[int]int, error) {
	c := b.c
	var out []any
	pointer := map[int]int{}
	for i, d := range c.Diagnoses {
		system := sysICD10CM
		if strings.HasPrefix(d.Qualifier, "B") && !strings.HasPrefix(d.Qualifier, "BB") {
			system = sysICD9CM
		}
		var typeSystem, typeCode string
		switch c.Kind {
		case x12.ClaimProfessional:
			switch d.Qualifier {
			case "ABK", "BK":
				typeSystem, typeCode = sysDiagType, "principal"
			case "ABF", "BF":
				typeSystem, typeCode = carinCS+"C4BBClaimDiagnosisType", "secondary"
			}
		default:
			switch d.Qualifier {
			case "ABK", "BK":
				typeSystem, typeCode = sysDiagType, "principal"
			case "ABJ", "BJ":
				if c.IsInpatient() {
					typeSystem, typeCode = sysDiagType, "admitting"
				}
			case "ABF", "BF":
				typeSystem, typeCode = carinCS+"C4BBClaimDiagnosisType", "other"
			case "ABN", "BN":
				typeSystem, typeCode = carinCS+"C4BBClaimDiagnosisType", "externalcauseofinjury"
			case "APR", "PR":
				if !c.IsInpatient() {
					typeSystem, typeCode = carinCS+"C4BBClaimDiagnosisType", "patientreasonforvisit"
				}
			}
		}
		if typeCode == "" {
			b.note("diagnosis %s (%s) has no CARIN diagnosis type on a %s claim and was left out", d.Code, d.Qualifier, profileWord(c))
			continue
		}
		e := map[string]any{
			"sequence":                 len(out) + 1,
			"diagnosisCodeableConcept": codeable(system, icdWithDot(d.Code, system), ""),
			"type":                     []any{codeable(typeSystem, typeCode, "")},
		}
		if c.IsInpatient() && d.PresentOnAdmit != "" {
			e["onAdmission"] = codeable(sysPOA, d.PresentOnAdmit, "")
		}
		pointer[i+1] = len(out) + 1
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("the claim has no diagnosis CARIN can carry, and CARIN requires at least one")
	}

	return out, pointer, nil
}

func (b *carinBuilder) procedures() []any {
	var out []any
	for i, p := range b.c.Procedures {
		system := sysICD10PCS
		if p.Qualifier == "BR" || p.Qualifier == "BQ" {
			system = sysICD9CM
		}
		kind := "other"
		if p.Qualifier == "BBR" || p.Qualifier == "BR" || p.Qualifier == "CAH" {
			kind = "principal"
		}
		if i > 0 && kind == "principal" {
			kind = "other"
		}
		e := map[string]any{
			"sequence":                 len(out) + 1,
			"type":                     []any{codeable(carinCS+"C4BBClaimProcedureType", kind, "")},
			"procedureCodeableConcept": codeable(system, p.Code, ""),
		}
		if p.Date != "" {
			e["date"] = fhirDate(p.Date)
		}
		out = append(out, e)
	}

	return out
}

// lineFor finds the paid line for a billed line: by line control number when both have one, otherwise by position.
func (b *carinBuilder) lineFor(i int, l x12.ClaimLine) *x12.ServiceLine {
	lines := b.rc.ServiceLines
	if l.LineControlNumber != "" {
		for j := range lines {
			if lines[j].LineControlNumber == l.LineControlNumber {
				return &lines[j]
			}
		}
	}
	if i < len(lines) && lines[i].LineControlNumber == "" {
		return &lines[i]
	}

	return nil
}

func (b *carinBuilder) items(pointer map[int]int) ([]any, *totals) {
	c := b.c
	t := &totals{}
	var out []any
	unpaired := 0

	for i, l := range c.Lines {
		item := map[string]any{"sequence": i + 1}
		if l.RevenueCode != "" {
			item["revenue"] = codeable(sysRevenue, l.RevenueCode, "")
		}
		item["productOrService"] = productOrService(l)
		if len(l.Modifiers) > 0 {
			var mods []any
			for _, m := range l.Modifiers {
				sys := sysCPT
				if m != "" && (m[0] < '0' || m[0] > '9') {
					sys = sysHCPCS
				}
				mods = append(mods, codeable(sys, m, ""))
			}
			item["modifier"] = mods
		}
		if l.ServiceDateFrom != "" {
			// Only the inpatient and professional profiles allow a period; outpatient lines take a date.
			if l.ServiceDateTo != "" && l.ServiceDateTo != l.ServiceDateFrom && (c.Kind == x12.ClaimProfessional || c.IsInpatient()) {
				item["servicedPeriod"] = map[string]any{"start": fhirDate(l.ServiceDateFrom), "end": fhirDate(l.ServiceDateTo)}
			} else {
				item["servicedDate"] = fhirDate(l.ServiceDateFrom)
			}
		} else if c.StatementFrom != "" {
			item["servicedDate"] = fhirDate(c.StatementFrom)
		}
		if c.Kind == x12.ClaimProfessional {
			pos := l.PlaceOfService
			if pos == "" {
				pos = c.FacilityCode
			}
			if pos != "" {
				item["locationCodeableConcept"] = codeable(sysPOS, pos, "")
			}
			var seq []any
			for _, p := range l.DiagnosisPointers {
				if s, ok := pointer[p]; ok {
					seq = append(seq, s)
				}
			}
			if len(seq) > 0 {
				item["diagnosisSequence"] = seq
			}
		}
		if l.Quantity > 0 {
			item["quantity"] = map[string]any{"value": l.Quantity}
		}

		adj := []any{amountAdjudication(sysAdjudication, "submitted", l.ChargeAmount)}
		if c.Kind == x12.ClaimProfessional {
			adj = append([]any{discriminator("benefitpaymentstatus", b.benefitStatus())}, adj...)
		}
		t.submitted += l.ChargeAmount

		if paid := b.lineFor(i, l); paid != nil {
			if paid.HasAllowed {
				adj = append(adj, amountAdjudication(sysAdjudication, "eligible", paid.AllowedAmount))
				t.eligible += paid.AllowedAmount
				t.eligibleLines++
			}
			adj = append(adj, amountAdjudication(carinCS+"C4BBAdjudication", "paidtoprovider", paid.PaymentAmount))
			member := 0.0
			for _, a := range paid.Adjustments {
				if code := patientShare(a); code != "" {
					adj = append(adj, amountAdjudication(categorySystem(code), code, a.Amount))
				}
				if a.GroupCode == "PR" {
					member += a.Amount
				}
				adj = append(adj, adjustment(a))
				t.addAdjustment(a)
			}
			adj = append(adj, amountAdjudication(carinCS+"C4BBAdjudication", "memberliability", member))
			for _, r := range paid.RemarkCodes {
				adj = append(adj, map[string]any{
					"category": codeable(carinCS+"C4BBAdjudicationDiscriminator", "adjustmentreason", ""),
					"reason":   codeable(sysRARC, r.Code, ""),
				})
			}
		} else if len(b.rc.ServiceLines) > 0 {
			unpaired++
		}
		item["adjudication"] = adj
		out = append(out, item)
	}

	if len(b.rc.ServiceLines) == 0 {
		b.note("the 835 adjudicated this claim as a whole, with no service lines, so payment and member liability are in the totals only")
	} else if unpaired > 0 {
		b.note("%d billed line(s) could not be paired with a paid line in the 835 and carry only the submitted amount", unpaired)
	}
	t.lines = len(c.Lines)

	return out, t
}

func (b *carinBuilder) benefitStatus() string {
	if s := b.opt.NetworkStatus; s == "innetwork" || s == "outofnetwork" {
		return s
	}

	return "other"
}

func (b *carinBuilder) payment() map[string]any {
	rc := b.rc
	status := "paid"
	switch {
	case rc.ClaimStatus == "4":
		status = "denied"
	case rc.PaymentAmount == 0 && rc.PatientResponsibility == 0:
		status = "denied"
	default:
		for _, l := range rc.ServiceLines {
			if l.PaymentAmount == 0 && rc.PaymentAmount > 0 {
				status = "partiallypaid"
				break
			}
		}
	}
	p := map[string]any{
		"type":   codeable(carinCS+"C4BBPayerAdjudicationStatus", status, ""),
		"amount": money(rc.PaymentAmount),
	}
	if b.remit != nil && b.remit.PaymentDate != "" {
		p["date"] = fhirDate(b.remit.PaymentDate)
	}

	return p
}

// totals accumulates the claim-level amounts.
type totals struct {
	submitted     float64
	eligible      float64
	eligibleLines int
	lines         int
	deductible    float64
	coinsurance   float64
	copay         float64
	discount      float64
	seen          map[string]bool
}

func (t *totals) addAdjustment(a x12.Adjustment) {
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	switch patientShare(a) {
	case "deductible":
		t.deductible += a.Amount
		t.seen["deductible"] = true
	case "coinsurance":
		t.coinsurance += a.Amount
		t.seen["coinsurance"] = true
	case "copay":
		t.copay += a.Amount
		t.seen["copay"] = true
	}
	if a.GroupCode == "CO" && a.ReasonCode == "45" {
		t.discount += a.Amount
		t.seen["discount"] = true
	}
}

func (t *totals) render(rc *x12.RemittanceClaim) []any {
	out := []any{
		amountAdjudication(sysAdjudication, "submitted", rc.ChargeAmount),
	}
	// Eligible only when every line said, because a sum over some of them is a number nobody was charged.
	if t.lines > 0 && t.eligibleLines == t.lines {
		out = append(out, amountAdjudication(sysAdjudication, "eligible", t.eligible))
	}
	if t.seen["deductible"] {
		out = append(out, amountAdjudication(sysAdjudication, "deductible", t.deductible))
	}
	if t.seen["coinsurance"] {
		out = append(out, amountAdjudication(carinCS+"C4BBAdjudication", "coinsurance", t.coinsurance))
	}
	if t.seen["copay"] {
		out = append(out, amountAdjudication(sysAdjudication, "copay", t.copay))
	}
	if t.seen["discount"] {
		out = append(out, amountAdjudication(carinCS+"C4BBAdjudication", "discount", t.discount))
	}
	out = append(out,
		amountAdjudication(carinCS+"C4BBAdjudication", "paidtoprovider", rc.PaymentAmount),
		amountAdjudication(carinCS+"C4BBAdjudication", "memberliability", rc.PatientResponsibility),
	)

	return out
}

// patientShare names the CARIN adjudication category for a patient-responsibility adjustment, by its CARC.
//
// Only the three the codes define unambiguously: 1 deductible, 2 coinsurance, 3 copayment. Every other adjustment keeps its
// reason code and amount without being given a category that might be wrong.
func patientShare(a x12.Adjustment) string {
	if a.GroupCode != "PR" {
		return ""
	}
	switch a.ReasonCode {
	case "1":
		return "deductible"
	case "2":
		return "coinsurance"
	case "3":
		return "copay"
	}

	return ""
}

func categorySystem(code string) string {
	if code == "coinsurance" {
		return carinCS + "C4BBAdjudication"
	}

	return sysAdjudication
}

func adjustment(a x12.Adjustment) map[string]any {
	return map[string]any{
		"category": codeable(carinCS+"C4BBAdjudicationDiscriminator", "adjustmentreason", ""),
		"reason":   codeable(sysCARC, a.ReasonCode, ""),
		"amount":   money(a.Amount),
	}
}

func discriminator(category, status string) map[string]any {
	return map[string]any{
		"category": codeable(carinCS+"C4BBAdjudicationDiscriminator", category, ""),
		"reason":   codeable(carinCS+"C4BBPayerAdjudicationStatus", status, ""),
	}
}

func amountAdjudication(system, code string, amount float64) map[string]any {
	return map[string]any{"category": codeable(system, code, ""), "amount": money(amount)}
}

func productOrService(l x12.ClaimLine) map[string]any {
	switch {
	case l.ProcedureCode == "":
		// An institutional line billed by revenue code alone, which is common for room and board. CARIN's value set
		// includes data-absent-reason not-applicable for exactly this.
		return codeable(sysDataAbsent, "not-applicable", "")
	case l.ProcedureQualifier == "HP":
		return codeable(sysHIPPS, l.ProcedureCode, "")
	case l.ProcedureCode[0] < '0' || l.ProcedureCode[0] > '9':
		// HC covers CPT and HCPCS Level II alike; a Level II code starts with a letter.
		return codeable(sysHCPCS, l.ProcedureCode, "")
	default:
		return codeable(sysCPT, l.ProcedureCode, "")
	}
}

func profileWord(c *x12.Claim) string {
	if c.Kind == x12.ClaimProfessional {
		return "professional"
	}
	if c.IsInpatient() {
		return "inpatient"
	}

	return "outpatient"
}

func codeable(system, code, display string) map[string]any {
	coding := map[string]any{"system": system, "code": code}
	if display != "" {
		coding["display"] = display
	}

	return map[string]any{"coding": []any{coding}}
}

func money(v float64) map[string]any {
	return map[string]any{"value": round2(v), "currency": "USD"}
}

func round2(v float64) float64 {
	if v < 0 {
		return -round2(-v)
	}

	return float64(int64(v*100+0.5)) / 100
}

func humanName(p x12.ClaimParty) map[string]any {
	n := map[string]any{"family": titleCase(p.LastName)}
	var given []any
	for _, g := range []string{p.FirstName, p.MiddleName} {
		if g != "" {
			given = append(given, titleCase(g))
		}
	}
	if len(given) > 0 {
		n["given"] = given
	}

	return n
}

// titleCase turns the upper case X12 is written in into the case a member expects to read their name in.
func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		parts := strings.Split(w, "-")
		for j, p := range parts {
			if p != "" {
				parts[j] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		words[i] = strings.Join(parts, "-")
	}

	return strings.Join(words, " ")
}

func address(p x12.ClaimParty) map[string]any {
	if len(p.Address) == 0 && p.City == "" {
		return nil
	}
	a := map[string]any{}
	if len(p.Address) > 0 {
		var lines []any
		for _, l := range p.Address {
			lines = append(lines, titleCase(l))
		}
		a["line"] = lines
	}
	if p.City != "" {
		a["city"] = titleCase(p.City)
	}
	if p.State != "" {
		a["state"] = p.State
	}
	if p.PostalCode != "" {
		zip := p.PostalCode
		if len(zip) == 9 {
			zip = zip[:5] + "-" + zip[5:]
		}
		a["postalCode"] = zip
	}

	return a
}

func gender(g string) string {
	switch g {
	case "M":
		return "male"
	case "F":
		return "female"
	case "U":
		return "unknown"
	}

	return ""
}

// relationship maps the X12 individual relationship code to FHIR's subscriber-relationship.
func relationship(code string) string {
	switch code {
	case "18", "":
		return "self"
	case "01":
		return "spouse"
	case "19":
		return "child"
	}

	return "other"
}

// fhirDate turns CCYYMMDD (optionally followed by a time) into a FHIR date.
func fhirDate(v string) string {
	if len(v) < 8 {
		return v
	}

	return v[0:4] + "-" + v[4:6] + "-" + v[6:8]
}

// icdWithDot writes an ICD-10-CM or ICD-9-CM diagnosis the way the code system publishes it.
//
// X12 sends diagnoses without the decimal point (J029); the code system's codes have it (J02.9), and a terminology server asked
// about J029 says it does not exist. ICD-9 E codes put the point after the fourth character.
func icdWithDot(code, system string) string {
	if strings.Contains(code, ".") {
		return code
	}
	at := 3
	if system == sysICD9CM && strings.HasPrefix(code, "E") {
		at = 4
	}
	if len(code) <= at {
		return code
	}

	return code[:at] + "." + code[at:]
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9\-.]+`)

// fhirID makes a FHIR id from a prefix and a source identifier.
//
// Deterministic, so the same claim converted twice produces the same ids and a transaction of PUTs updates rather than
// duplicates. Truncated to FHIR's 64 characters, keeping the end, which is where a long identifier usually differs.
func fhirID(prefix, value string) string {
	id := prefix + "-" + strings.Trim(unsafeID.ReplaceAllString(value, "-"), "-")
	if len(id) > 64 {
		id = prefix + "-" + id[len(id)-63+len(prefix):]
	}

	return id
}

// checkNPI notes an NPI whose check digit is wrong. US Core refuses one, and it is nearly always a keying error in the
// provider's system - worth saying in words before a validator says it as an invariant number.
func (b *carinBuilder) checkNPI(p x12.ClaimParty) {
	if !ValidNPI(p.ID) {
		b.note("the NPI %s for %s fails its check digit; US Core will reject it", p.ID, p.Name())
	}
}

// ValidNPI reports whether a ten-digit NPI has a correct Luhn check digit, computed with the 80840 prefix the standard defines.
func ValidNPI(npi string) bool {
	if len(npi) != 10 {
		return false
	}
	s := "80840" + npi[:9]
	sum := 0
	for i := len(s) - 1; i >= 0; i-- {
		d := int(s[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if (len(s)-1-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	last := int(npi[9] - '0')

	return last >= 0 && last <= 9 && (10-sum%10)%10 == last
}
