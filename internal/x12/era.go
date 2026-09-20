package x12

import (
	"fmt"
	"strconv"
	"strings"
)

// Package-level 835 ERA (Electronic Remittance Advice) parsing.
//
// An 835 carries what a payer decided about claims: which were paid, which were denied,
// how much was allowed, what adjustments were made, and at what service-line level. This
// is the data a billing office needs to post payments and work denials.
//
// The structure of an 835, simplified:
//
//	ISA/GS/ST  — envelope
//	  BPR      — financial information (total payment, method, banking)
//	  TRN      — reassociation trace (check/EFT number)
//	  DTM      — production date
//	  N1 loop  — payer and payee identification
//	  LX loop  — header number (groups claims by provider)
//	    CLP loop — one per claim
//	      SVC loop — one per service line within the claim
//	        CAS  — adjustments (why a line was reduced or denied)
//	        DTM  — dates
//	        AMT  — amounts
//	        QTY  — quantities
//	    PLB     — provider-level adjustments (take-backs, interest, etc.)
//	SE/GE/IEA  — envelope close
//
// The parser returns structured data at three levels: the remittance (payment overall),
// each claim, and each service line. Every field is named for what the billing office
// calls it, not what the X12 element code is.

// Remittance is one parsed 835 transaction set: a single payment or explanation.
type Remittance struct {
	// Payment information from BPR.
	PaymentAmount float64 // BPR02: total payment
	PaymentMethod string  // BPR04: ACH, CHK, FWT, etc.
	PaymentDate   string  // BPR16: date funds released

	// Check or EFT trace from TRN.
	CheckNumber string // TRN02: check/EFT reference
	PayerID     string // TRN03: payer identifier

	// Production date from DTM (when the ERA was generated).
	ProductionDate string

	// Payer identification from N1*PR loop.
	PayerName string
	PayerNPI  string

	// Payee identification from N1*PE loop.
	PayeeName string
	PayeeNPI  string
	PayeeTIN  string

	// Claims in this remittance.
	Claims []RemittanceClaim

	// Provider-level adjustments from PLB.
	ProviderAdjustments []ProviderAdjustment
}

// RemittanceClaim is one claim within a remittance (CLP loop).
type RemittanceClaim struct {
	// CLP01: patient account number (what the provider submitted).
	PatientAccountNumber string
	// CLP02: claim status code (1=processed primary, 2=processed secondary, etc.).
	ClaimStatus string
	// CLP03: total charge amount submitted.
	ChargeAmount float64
	// CLP04: total payment amount for this claim.
	PaymentAmount float64
	// CLP05: patient responsibility amount.
	PatientResponsibility float64
	// CLP06: claim filing indicator (e.g. 12=PPO, MC=Medicaid).
	FilingIndicator string
	// CLP07: payer claim control number (their internal reference).
	PayerClaimControlNumber string

	// Patient info from NM1*QC within the CLP loop.
	PatientLastName  string
	PatientFirstName string
	PatientMemberID  string

	// Service lines within this claim.
	ServiceLines []ServiceLine

	// Claim-level adjustments (CAS segments directly under CLP, not under SVC).
	Adjustments []Adjustment

	// Claim-level dates.
	StatementFromDate string // DTM*232
	StatementToDate   string // DTM*233
}

// ServiceLine is one service within a claim (SVC loop).
type ServiceLine struct {
	// SVC01: procedure code (composite — code qualifier + code).
	ProcedureCode     string // e.g. "99213" (the CPT/HCPCS)
	CodeQualifier     string // HC=CPT, AD=ADA dental, etc.
	ProcedureModifier string // modifier if present

	// SVC02: charge amount for this line.
	ChargeAmount float64
	// SVC03: payment amount for this line.
	PaymentAmount float64
	// SVC04: revenue code (for institutional claims).
	RevenueCode string
	// SVC05: units paid.
	UnitsPaid float64
	// SVC07: original procedure code if different from what was paid.
	OriginalProcedure string

	// Service-line adjustments.
	Adjustments []Adjustment

	// Service dates.
	ServiceDate string // DTM*472

	// Remark codes from LQ segments.
	RemarkCodes []RemarkCode
}

// Adjustment is one adjustment reason (from CAS segments).
//
// CAS segments carry the *why* of every dollar that wasn't paid: contractual obligation,
// patient responsibility, CO (contractual), PR (patient), OA (other), PI (payer initiated),
// CR (corrections/reversals). Each group can have up to six reason/amount pairs.
type Adjustment struct {
	// GroupCode: CO, PR, OA, PI, CR.
	GroupCode string
	// ReasonCode: CARC (Claim Adjustment Reason Code), e.g. "45" = charges exceed fee schedule.
	ReasonCode string
	// Amount adjusted.
	Amount float64
	// Quantity affected (units denied, if applicable).
	Quantity float64
}

// RemarkCode is a RARC (Remittance Advice Remark Code) from LQ segments.
type RemarkCode struct {
	Qualifier string // HE=explanation, RX=reject
	Code      string // e.g. "N130" = "payment adjusted based on multiple surgery rules"
}

// ProviderAdjustment is a provider-level take-back or credit from PLB segments.
type ProviderAdjustment struct {
	// AdjustmentReason: WO=write-off, L6=interest, FB=forward balance, etc.
	AdjustmentReason string
	// ReferenceID: identifies what it applies to (e.g. original claim number).
	ReferenceID string
	// Amount: positive = owed to payer, negative = owed to provider.
	Amount float64
	// Date the adjustment applies to.
	Date string
}

// ParseERA parses an 835 transaction set into a structured Remittance.
//
// The input should be a single transaction set (after splitting). If the interchange
// contains multiple transaction sets, split first with Split() and parse each one.
func ParseERA(m *Message) (*Remittance, error) {
	r := &Remittance{}

	segs := m.Segments("BPR")
	if len(segs) > 0 {
		bpr := segs[0]
		amt, err := parseAmount(bpr.Element(2).String())
		if err != nil {
			return nil, fmt.Errorf("BPR02 payment amount: %w", err)
		}
		r.PaymentAmount = amt
		r.PaymentMethod = bpr.Element(4).String()
		r.PaymentDate = bpr.Element(16).String()
	}

	// TRN — trace number.
	for _, trn := range m.Segments("TRN") {
		r.CheckNumber = trn.Element(2).String()
		r.PayerID = trn.Element(3).String()
	}

	// DTM — production date (qualifier 405).
	for _, dtm := range m.Segments("DTM") {
		if dtm.Element(1).String() == "405" {
			r.ProductionDate = dtm.Element(2).String()
		}
	}

	// Walk segments sequentially to handle loop structure.
	// N1 loops for payer/payee, then CLP loops for claims.
	var currentClaim *RemittanceClaim
	var currentSVC *ServiceLine
	inCLP := false

	for i := 0; i < m.SegmentCount(); i++ {
		seg, _ := m.SegmentAt(i)

		switch seg.ID {
		case "N1":
			qualifier := seg.Element(1).String()
			name := seg.Element(2).String()
			idQual := seg.Element(3).String()
			id := seg.Element(4).String()
			switch qualifier {
			case "PR": // Payer
				r.PayerName = name
				if idQual == "XV" {
					r.PayerNPI = id
				}
			case "PE": // Payee
				r.PayeeName = name
				if idQual == "XX" {
					r.PayeeNPI = id
				} else if idQual == "FI" {
					r.PayeeTIN = id
				}
			}

		case "CLP":
			// Flush previous claim.
			if currentClaim != nil {
				if currentSVC != nil {
					currentClaim.ServiceLines = append(currentClaim.ServiceLines, *currentSVC)
					currentSVC = nil
				}
				r.Claims = append(r.Claims, *currentClaim)
			}
			chargeAmt, err := parseAmount(seg.Element(3).String())
			if err != nil {
				return nil, fmt.Errorf("CLP*%s charge amount: %w", seg.Element(1).String(), err)
			}
			paymentAmt, err := parseAmount(seg.Element(4).String())
			if err != nil {
				return nil, fmt.Errorf("CLP*%s payment amount: %w", seg.Element(1).String(), err)
			}
			patientResp, err := parseAmount(seg.Element(5).String())
			if err != nil {
				return nil, fmt.Errorf("CLP*%s patient responsibility: %w", seg.Element(1).String(), err)
			}
			currentClaim = &RemittanceClaim{
				PatientAccountNumber:    seg.Element(1).String(),
				ClaimStatus:             seg.Element(2).String(),
				ChargeAmount:            chargeAmt,
				PaymentAmount:           paymentAmt,
				PatientResponsibility:   patientResp,
				FilingIndicator:         seg.Element(6).String(),
				PayerClaimControlNumber: seg.Element(7).String(),
			}
			currentSVC = nil
			inCLP = true

		case "NM1":
			if inCLP && currentClaim != nil {
				qualifier := seg.Element(1).String()
				if qualifier == "QC" { // Patient
					currentClaim.PatientLastName = seg.Element(3).String()
					currentClaim.PatientFirstName = seg.Element(4).String()
					currentClaim.PatientMemberID = seg.Element(9).String()
				}
			}

		case "SVC":
			if currentClaim != nil {
				// Flush previous service line.
				if currentSVC != nil {
					currentClaim.ServiceLines = append(currentClaim.ServiceLines, *currentSVC)
				}
				svcCharge, err := parseAmount(seg.Element(2).String())
				if err != nil {
					return nil, fmt.Errorf("SVC charge amount: %w", err)
				}
				svcPayment, err := parseAmount(seg.Element(3).String())
				if err != nil {
					return nil, fmt.Errorf("SVC payment amount: %w", err)
				}
				currentSVC = &ServiceLine{
					ChargeAmount:  svcCharge,
					PaymentAmount: svcPayment,
					RevenueCode:   seg.Element(4).String(),
					UnitsPaid:     parseFloat(seg.Element(5).String()),
				}
				// SVC01 is a composite: qualifier:code:modifier
				svc01 := seg.Element(1).String()
				parts := strings.SplitN(svc01, string(m.delim.Component), 3)
				if len(parts) >= 1 {
					currentSVC.CodeQualifier = parts[0]
				}
				if len(parts) >= 2 {
					currentSVC.ProcedureCode = parts[1]
				}
				if len(parts) >= 3 {
					currentSVC.ProcedureModifier = parts[2]
				}
				// SVC06 — original procedure if adjudicated differently.
				if seg.ElementCount() >= 7 {
					orig := seg.Element(6).String()
					if orig != "" {
						origParts := strings.SplitN(orig, string(m.delim.Component), 2)
						if len(origParts) >= 2 {
							currentSVC.OriginalProcedure = origParts[1]
						}
					}
				}
			}

		case "CAS":
			adj, err := parseCAS(seg)
			if err != nil {
				return nil, err
			}
			if currentSVC != nil {
				currentSVC.Adjustments = append(currentSVC.Adjustments, adj...)
			} else if currentClaim != nil {
				currentClaim.Adjustments = append(currentClaim.Adjustments, adj...)
			}

		case "DTM":
			qual := seg.Element(1).String()
			date := seg.Element(2).String()
			if currentSVC != nil {
				if qual == "472" {
					currentSVC.ServiceDate = date
				}
			} else if currentClaim != nil {
				switch qual {
				case "232":
					currentClaim.StatementFromDate = date
				case "233":
					currentClaim.StatementToDate = date
				}
			}

		case "LQ":
			if currentSVC != nil {
				currentSVC.RemarkCodes = append(currentSVC.RemarkCodes, RemarkCode{
					Qualifier: seg.Element(1).String(),
					Code:      seg.Element(2).String(),
				})
			}

		case "PLB":
			plb, err := parsePLB(seg, m.delim)
			if err != nil {
				return nil, err
			}
			r.ProviderAdjustments = append(r.ProviderAdjustments, plb...)
			inCLP = false

		case "SE":
			// End of transaction set.
			if currentClaim != nil {
				if currentSVC != nil {
					currentClaim.ServiceLines = append(currentClaim.ServiceLines, *currentSVC)
				}
				r.Claims = append(r.Claims, *currentClaim)
			}
			inCLP = false
		}
	}

	return r, nil
}

// parseCAS extracts adjustments from a CAS segment.
// CAS segments carry up to 6 reason/amount pairs in groups of 3 elements:
// CAS*GroupCode*ReasonCode*Amount*Quantity*ReasonCode*Amount*Quantity...
func parseCAS(seg Segment) ([]Adjustment, error) {
	var out []Adjustment
	group := seg.Element(1).String()

	// Elements 2-4, 5-7, 8-10, 11-13, 14-16, 17-19 (up to 6 adjustments per CAS).
	for i := 2; i <= seg.ElementCount() && i+1 <= seg.ElementCount(); i += 3 {
		reason := seg.Element(i).String()
		if reason == "" {
			break
		}
		amt, err := parseAmount(seg.Element(i + 1).String())
		if err != nil {
			return nil, fmt.Errorf("CAS*%s reason %s amount: %w", group, reason, err)
		}
		var qty float64
		if i+2 <= seg.ElementCount() {
			qty = parseFloat(seg.Element(i + 2).String())
		}
		out = append(out, Adjustment{
			GroupCode:  group,
			ReasonCode: reason,
			Amount:     amt,
			Quantity:   qty,
		})
	}
	return out, nil
}

// parsePLB extracts provider-level adjustments.
// PLB*ProviderID*FiscalPeriod*AdjReason:RefID*Amount*AdjReason:RefID*Amount...
func parsePLB(seg Segment, d Delimiters) ([]ProviderAdjustment, error) {
	var out []ProviderAdjustment
	date := seg.Element(2).String()

	// Pairs start at element 3: reason:refID, amount, reason:refID, amount, ...
	for i := 3; i+1 <= seg.ElementCount(); i += 2 {
		reasonComposite := seg.Element(i).String()
		amount, err := parseAmount(seg.Element(i + 1).String())
		if err != nil {
			return nil, fmt.Errorf("PLB amount: %w", err)
		}

		parts := strings.SplitN(reasonComposite, string(d.Component), 2)
		reason := ""
		refID := ""
		if len(parts) >= 1 {
			reason = parts[0]
		}
		if len(parts) >= 2 {
			refID = parts[1]
		}

		if reason == "" {
			break
		}
		out = append(out, ProviderAdjustment{
			AdjustmentReason: reason,
			ReferenceID:      refID,
			Amount:           amount,
			Date:             date,
		})
	}
	return out, nil
}

// errBadAmount is returned when a monetary field contains unparseable text.
// A zero is a plausible payment (denied claims pay $0.00), so silently defaulting
// to zero would produce an indistinguishable-from-real wrong answer.
var errBadAmount = fmt.Errorf("x12: unparseable monetary amount")

func parseAmount(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", errBadAmount, s)
	}
	return f, nil
}

// parseFloat is kept for non-critical numeric fields (quantities) where an empty or
// absent value legitimately means zero. Monetary amounts must use parseAmount instead.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
