package x12

import (
	"fmt"
	"strings"
)

// The 837 health care claim: professional (005010X222A1) and institutional (005010X223A2).
//
// Read structurally, loop by loop, the same way ParseERA reads an 835 - because the questions a payer has to answer from a
// claim under CMS-0057 ("which diagnoses", "which lines", "who rendered it") are questions about loops, and a delimiter split
// cannot tell the subscriber's NM1 from the other payer's NM1 two loops further down.
//
//	2000A  HL*..*20   billing provider          NM1*85
//	2000B  HL*..*22   subscriber      SBR       NM1*IL, NM1*PR (payer)
//	2000C  HL*..*23   patient         PAT       NM1*QC          (only when the patient is not the subscriber)
//	2300              claim           CLM, DTP, CL1, REF, HI
//	2310x             claim providers NM1*71/72/82/77/DN/DK/DQ
//	2320/2330         other payers    SBR, NM1*IL/PR             (skipped: these describe coordination of benefits)
//	2400              service line    LX, SV1 or SV2, DTP*472, REF*6R
//
// Dental (837D) is refused by name rather than read as if it were professional.

// ClaimKind is which 837 implementation guide a claim follows.
type ClaimKind string

const (
	ClaimProfessional  ClaimKind = "professional"
	ClaimInstitutional ClaimKind = "institutional"
)

// ClaimParty is a person or organisation named in an 837.
type ClaimParty struct {
	EntityType  string // NM102: 1 person, 2 organisation
	LastName    string // NM103 (or the organisation name)
	FirstName   string // NM104
	MiddleName  string // NM105
	IDQualifier string // NM108: XX NPI, MI member id, PI payer id
	ID          string // NM109
	TaxID       string // REF*EI
	Address     []string
	City        string
	State       string
	PostalCode  string
	BirthDate   string // DMG02, CCYYMMDD
	Gender      string // DMG03: M, F, U
	Taxonomy    string // PRV03
}

// Name renders the party's name for display.
func (p ClaimParty) Name() string {
	if p.EntityType == "1" {
		return strings.TrimSpace(strings.Join([]string{p.FirstName, p.MiddleName, p.LastName}, " "))
	}

	return p.LastName
}

// ClaimCode is one coded diagnosis or procedure from an HI segment.
type ClaimCode struct {
	Qualifier      string // ABK principal dx, ABF other dx, ABJ admitting, APR reason for visit, ABN external cause, BBR/BBQ ICD-10-PCS
	Code           string // as sent, without the decimal point
	Date           string // procedure date, CCYYMMDD
	PresentOnAdmit string // HI component 9: Y, N, U, W, 1
}

// ClaimLine is one 2400 service line.
type ClaimLine struct {
	Number             int
	RevenueCode        string // SV201, institutional
	ProcedureQualifier string // HC (CPT/HCPCS), HP (HIPPS), ...
	ProcedureCode      string
	Modifiers          []string
	ChargeAmount       float64
	UnitBasis          string // UN units, MJ minutes
	Quantity           float64
	PlaceOfService     string // SV105, professional; empty means the claim's
	DiagnosisPointers  []int  // SV107, professional
	ServiceDateFrom    string
	ServiceDateTo      string
	LineControlNumber  string // REF*6R
}

// Claim is one 2300 claim with the providers and member it belongs to.
type Claim struct {
	Kind ClaimKind

	// Created is BHT04, the date the provider's system produced the transaction.
	Created string

	BillingProvider ClaimParty
	Subscriber      ClaimParty
	Payer           ClaimParty

	// Patient is the subscriber again when PatientIsSubscriber, so callers never need to branch.
	Patient             ClaimParty
	PatientIsSubscriber bool
	// Relationship is the patient's relationship to the subscriber as X12 codes it: 18 self, 01 spouse, 19 child, G8 other.
	Relationship string

	PayerResponsibility string // SBR01: P primary, S secondary, T tertiary...
	GroupNumber         string // SBR03
	GroupName           string // SBR04
	FilingIndicator     string // SBR09

	PatientAccountNumber string  // CLM01
	ChargeAmount         float64 // CLM02
	// FacilityCode is CLM05-1: the place of service on a professional claim, the type-of-bill facility code on an
	// institutional one. FrequencyCode is CLM05-3.
	FacilityCode  string
	FacilityQual  string
	FrequencyCode string

	MedicalRecordNumber string // REF*EA
	OriginalReference   string // REF*F8, the payer's number for the claim this one replaces

	StatementFrom string // DTP*434
	StatementTo   string
	AdmissionDate string // DTP*435
	DischargeHour string // DTP*096

	AdmissionType   string // CL101
	AdmissionSource string // CL102 (point of origin)
	DischargeStatus string // CL103

	DRG string // HI*DR

	Diagnoses  []ClaimCode
	Procedures []ClaimCode

	Attending       ClaimParty // NM1*71
	Operating       ClaimParty // NM1*72
	Rendering       ClaimParty // NM1*82
	Referring       ClaimParty // NM1*DN
	Supervising     ClaimParty // NM1*DQ
	ServiceFacility ClaimParty // NM1*77 (professional) or NM1*77 / 2310E (institutional)

	Lines []ClaimLine
}

// TypeOfBill is the four-character NUBC type of bill: a leading zero, the facility code and the frequency.
//
// Empty on a professional claim, which has none.
func (c *Claim) TypeOfBill() string {
	if c.Kind != ClaimInstitutional || c.FacilityCode == "" {
		return ""
	}

	return "0" + c.FacilityCode + c.FrequencyCode
}

// IsInpatient reports whether an institutional claim is inpatient, read from the type of bill.
//
// The second digit of the facility code is the bill classification: 1 is inpatient (Part A) and 2 inpatient (Part B only).
// Everything else - outpatient, other, intermediate care, clinic - is not an inpatient stay.
func (c *Claim) IsInpatient() bool {
	if c.Kind != ClaimInstitutional || len(c.FacilityCode) < 2 {
		return false
	}
	class := c.FacilityCode[1]

	return class == '1' || class == '2'
}

// ParseClaims reads every claim in every 837 transaction set of an interchange.
func ParseClaims(m *Message) ([]Claim, error) {
	var (
		out      []Claim
		inSet    bool
		kind     ClaimKind
		created  string
		level    string
		billing  ClaimParty
		sub      ClaimParty
		payer    ClaimParty
		patient  ClaimParty
		sbr      Segment
		pat      string
		hasPat   bool
		claim    *Claim
		line     *ClaimLine
		party    *ClaimParty // the NM1 the following N3/N4/DMG/REF/PRV belong to
		otherPay bool        // inside 2320/2330: other-payer loops, which describe someone else's coverage
	)

	flushLine := func() {
		if claim != nil && line != nil {
			claim.Lines = append(claim.Lines, *line)
		}
		line = nil
	}
	flushClaim := func() {
		flushLine()
		if claim != nil {
			out = append(out, *claim)
		}
		claim = nil
	}

	for i := 0; i < m.SegmentCount(); i++ {
		s, _ := m.SegmentAt(i)

		if s.ID == "ST" {
			inSet = s.Element(1).String() == "837"
			if inSet {
				switch v := s.Element(3).String(); {
				case strings.Contains(v, "X222"):
					kind = ClaimProfessional
				case strings.Contains(v, "X223"):
					kind = ClaimInstitutional
				case strings.Contains(v, "X224"):
					return nil, fmt.Errorf("this is a dental claim (%s); dental 837s are not read here", v)
				default:
					kind = ""
				}
			}
			continue
		}
		if !inSet {
			continue
		}

		switch s.ID {
		case "SE":
			flushClaim()
			inSet = false

		case "BHT":
			created = s.Element(4).String()

		case "HL":
			flushClaim()
			otherPay = false
			level = s.Element(3).String()
			party = nil
			switch level {
			case "20":
				billing = ClaimParty{}
			case "22":
				sub, payer, patient = ClaimParty{}, ClaimParty{}, ClaimParty{}
				hasPat, pat = false, ""
			case "23":
				patient = ClaimParty{}
				hasPat = true
			}

		case "PRV":
			if party != nil {
				party.Taxonomy = s.Element(3).String()
			} else if level == "20" {
				billing.Taxonomy = s.Element(3).String()
			}

		case "SBR":
			if claim != nil {
				// An SBR inside the claim opens 2320: another payer's coverage. Nothing in it describes this claim's member.
				flushLine()
				otherPay = true
				party = nil
				continue
			}
			sbr = s

		case "PAT":
			pat = s.Element(1).String()

		case "NM1":
			if otherPay {
				party = nil
				continue
			}
			p := ClaimParty{
				EntityType:  s.Element(2).String(),
				LastName:    s.Element(3).String(),
				FirstName:   s.Element(4).String(),
				MiddleName:  s.Element(5).String(),
				IDQualifier: s.Element(8).String(),
				ID:          s.Element(9).String(),
			}
			party = nil
			switch q := s.Element(1).String(); {
			case q == "85" && level == "20":
				// PRV comes before NM1 in 2000A, so the taxonomy already read is kept.
				p.Taxonomy = billing.Taxonomy
				billing = p
				party = &billing
			case q == "IL" && level == "22":
				sub = p
				party = &sub
			case q == "PR" && level == "22":
				payer = p
				party = &payer
			case q == "QC" && level == "23":
				patient = p
				party = &patient
			case claim != nil && line == nil:
				switch q {
				case "71":
					claim.Attending = p
					party = &claim.Attending
				case "72":
					claim.Operating = p
					party = &claim.Operating
				case "82":
					claim.Rendering = p
					party = &claim.Rendering
				case "DN":
					claim.Referring = p
					party = &claim.Referring
				case "DQ":
					claim.Supervising = p
					party = &claim.Supervising
				case "77":
					claim.ServiceFacility = p
					party = &claim.ServiceFacility
				}
			}

		case "N3":
			if party != nil {
				party.Address = append(party.Address, nonEmpty(s.Element(1).String(), s.Element(2).String())...)
			}

		case "N4":
			if party != nil {
				party.City, party.State, party.PostalCode = s.Element(1).String(), s.Element(2).String(), s.Element(3).String()
			}

		case "DMG":
			if party != nil {
				party.BirthDate, party.Gender = s.Element(2).String(), s.Element(3).String()
			}

		case "CLM":
			flushClaim()
			otherPay = false
			party = nil
			if kind == "" {
				return nil, fmt.Errorf("the 837 does not say whether it is professional (X222) or institutional (X223) in ST03")
			}
			charge, err := parseAmount(s.Element(2).String())
			if err != nil {
				return nil, fmt.Errorf("CLM*%s charge amount: %w", s.Element(1).String(), err)
			}
			c := Claim{
				Kind: kind, Created: created,
				BillingProvider: billing, Subscriber: sub, Payer: payer,
				PayerResponsibility:  sbr.Element(1).String(),
				GroupNumber:          sbr.Element(3).String(),
				GroupName:            sbr.Element(4).String(),
				FilingIndicator:      sbr.Element(9).String(),
				PatientAccountNumber: s.Element(1).String(),
				ChargeAmount:         charge,
				FacilityCode:         s.Element(5).Component(1),
				FacilityQual:         s.Element(5).Component(2),
				FrequencyCode:        s.Element(5).Component(3),
			}
			if hasPat {
				c.Patient, c.Relationship = patient, pat
			} else {
				c.Patient, c.PatientIsSubscriber, c.Relationship = sub, true, sbr.Element(2).String()
			}
			claim = &c

		case "DTP":
			if claim == nil || otherPay {
				continue
			}
			from, to := splitRange(s.Element(3).String())
			switch q := s.Element(1).String(); {
			case line != nil && q == "472":
				line.ServiceDateFrom, line.ServiceDateTo = from, to
			case line == nil && q == "434":
				claim.StatementFrom, claim.StatementTo = from, to
			case line == nil && q == "435":
				claim.AdmissionDate = from
			case line == nil && q == "096":
				claim.DischargeHour = from
			case line == nil && q == "472":
				// A claim-level service date, which some professional senders put here instead of on each line.
				if claim.StatementFrom == "" {
					claim.StatementFrom, claim.StatementTo = from, to
				}
			}

		case "CL1":
			if claim != nil {
				claim.AdmissionType = s.Element(1).String()
				claim.AdmissionSource = s.Element(2).String()
				claim.DischargeStatus = s.Element(3).String()
			}

		case "REF":
			switch {
			case party != nil && s.Element(1).String() == "EI":
				party.TaxID = s.Element(2).String()
			case line != nil && s.Element(1).String() == "6R":
				line.LineControlNumber = s.Element(2).String()
			case claim != nil && line == nil && !otherPay && s.Element(1).String() == "EA":
				claim.MedicalRecordNumber = s.Element(2).String()
			case claim != nil && line == nil && !otherPay && s.Element(1).String() == "F8":
				claim.OriginalReference = s.Element(2).String()
			}

		case "HI":
			if claim == nil || line != nil || otherPay {
				continue
			}
			for n := 1; n <= s.ElementCount(); n++ {
				e := s.Element(n)
				if e.IsEmpty() {
					continue
				}
				code := ClaimCode{
					Qualifier:      e.Component(1),
					Code:           e.Component(2),
					Date:           e.Component(4),
					PresentOnAdmit: e.Component(9),
				}
				switch code.Qualifier {
				case "DR":
					claim.DRG = code.Code
				case "BBR", "BBQ", "BR", "BQ", "CAH":
					claim.Procedures = append(claim.Procedures, code)
				case "ABK", "ABF", "ABJ", "APR", "ABN", "BK", "BF", "BJ", "PR", "BN":
					claim.Diagnoses = append(claim.Diagnoses, code)
				}
				// Value, occurrence, condition and treatment codes (BE, BH, BI, BG, TC) are not diagnoses and are skipped.
			}

		case "LX":
			if claim == nil {
				continue
			}
			flushLine()
			otherPay = false
			party = nil
			n := 0
			_, _ = fmt.Sscanf(s.Element(1).String(), "%d", &n)
			line = &ClaimLine{Number: n}

		case "SV1":
			if line == nil {
				continue
			}
			e := s.Element(1)
			line.ProcedureQualifier, line.ProcedureCode = e.Component(1), e.Component(2)
			for c := 3; c <= 6; c++ {
				if v := e.Component(c); v != "" {
					line.Modifiers = append(line.Modifiers, v)
				}
			}
			line.ChargeAmount = parseFloat(s.Element(2).String())
			line.UnitBasis = s.Element(3).String()
			line.Quantity = parseFloat(s.Element(4).String())
			line.PlaceOfService = s.Element(5).String()
			ptr := s.Element(7)
			for c := 1; c <= ptr.ComponentCount(); c++ {
				n := 0
				if _, err := fmt.Sscanf(ptr.Component(c), "%d", &n); err == nil && n > 0 {
					line.DiagnosisPointers = append(line.DiagnosisPointers, n)
				}
			}

		case "SV2":
			if line == nil {
				continue
			}
			line.RevenueCode = s.Element(1).String()
			e := s.Element(2)
			line.ProcedureQualifier, line.ProcedureCode = e.Component(1), e.Component(2)
			for c := 3; c <= 6; c++ {
				if v := e.Component(c); v != "" {
					line.Modifiers = append(line.Modifiers, v)
				}
			}
			line.ChargeAmount = parseFloat(s.Element(3).String())
			line.UnitBasis = s.Element(4).String()
			line.Quantity = parseFloat(s.Element(5).String())
		}
	}
	flushClaim()

	if len(out) == 0 {
		if !strings.Contains(strings.Join(m.TransactionSets(), ","), "837") {
			return nil, fmt.Errorf("this interchange contains no 837 transaction set")
		}

		return nil, fmt.Errorf("the 837 contains no CLM segment, so there is no claim to read")
	}

	return out, nil
}

// splitRange reads a DTP03 that is either a single date (D8) or a range (RD8, CCYYMMDD-CCYYMMDD).
func splitRange(v string) (string, string) {
	if from, to, ok := strings.Cut(v, "-"); ok {
		return from, to
	}

	return v, v
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}

	return out
}
