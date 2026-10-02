package x12

import (
	"fmt"
	"testing"
)

// A realistic 835 remittance with two claims, multiple service lines, adjustments,
// and a provider-level take-back.
const testERA = "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
	"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
	"ST*835*0001*005010X221A1~" +
	"BPR*I*1200.50*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
	"TRN*1*EFT123456*1111111111~" +
	"DTM*405*20260824~" +
	"N1*PR*ACME HEALTH PLAN*XV*1234567890~" +
	"N1*PE*DOWNTOWN MEDICAL*XX*9876543210~" +
	"LX*1~" +
	"CLP*ACCT-001*1*500.00*400.50*50.00*12*PAYER-REF-001~" +
	"NM1*QC*1*SMITH*JOHN****MI*MEM123456~" +
	"DTM*232*20260801~" +
	"DTM*233*20260801~" +
	"SVC*HC:99213*150.00*120.50**1~" +
	"CAS*CO*45*29.50~" +
	"DTM*472*20260801~" +
	"LQ*HE*N130~" +
	"SVC*HC:99214:25*200.00*180.00**1~" +
	"CAS*CO*45*10.00*0*253*10.00~" +
	"CAS*PR*2*50.00~" +
	"DTM*472*20260801~" +
	"SVC*HC:36415*150.00*100.00**1~" +
	"CAS*CO*45*50.00~" +
	"DTM*472*20260801~" +
	"CLP*ACCT-002*4*300.00*0.00*0.00*MC*PAYER-REF-002~" +
	"NM1*QC*1*DOE*JANE****MI*MEM789012~" +
	"DTM*232*20260815~" +
	"CAS*CO*96*300.00~" +
	"SVC*HC:99215*300.00*0.00**1~" +
	"CAS*CO*96*300.00~" +
	"DTM*472*20260815~" +
	"LQ*HE*MA04~" +
	"PLB*9876543210*20261231*L6:ORIG-CLAIM-99*-25.00~" +
	"SE*30*0001~" +
	"GE*1*1~" +
	"IEA*1*000000001~"

func TestParseERA(t *testing.T) {
	m, err := ParseString(testERA)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	// Payment info.
	if r.PaymentAmount != 1200.50 {
		t.Errorf("PaymentAmount = %v, want 1200.50", r.PaymentAmount)
	}
	if r.PaymentMethod != "ACH" {
		t.Errorf("PaymentMethod = %q, want ACH", r.PaymentMethod)
	}
	if r.CheckNumber != "EFT123456" {
		t.Errorf("CheckNumber = %q, want EFT123456", r.CheckNumber)
	}
	if r.PaymentDate != "20260824" {
		t.Errorf("PaymentDate = %q, want 20260824", r.PaymentDate)
	}
	if r.ProductionDate != "20260824" {
		t.Errorf("ProductionDate = %q, want 20260824", r.ProductionDate)
	}

	// Payer/payee.
	if r.PayerName != "ACME HEALTH PLAN" {
		t.Errorf("PayerName = %q", r.PayerName)
	}
	if r.PayerNPI != "1234567890" {
		t.Errorf("PayerNPI = %q", r.PayerNPI)
	}
	if r.PayeeName != "DOWNTOWN MEDICAL" {
		t.Errorf("PayeeName = %q", r.PayeeName)
	}
	if r.PayeeNPI != "9876543210" {
		t.Errorf("PayeeNPI = %q", r.PayeeNPI)
	}

	// Claims.
	if len(r.Claims) != 2 {
		t.Fatalf("got %d claims, want 2", len(r.Claims))
	}

	// Claim 1: paid.
	c1 := r.Claims[0]
	if c1.PatientAccountNumber != "ACCT-001" {
		t.Errorf("claim 1 account = %q", c1.PatientAccountNumber)
	}
	if c1.ClaimStatus != "1" {
		t.Errorf("claim 1 status = %q, want 1 (processed primary)", c1.ClaimStatus)
	}
	if c1.ChargeAmount != 500.00 {
		t.Errorf("claim 1 charge = %v", c1.ChargeAmount)
	}
	if c1.PaymentAmount != 400.50 {
		t.Errorf("claim 1 payment = %v", c1.PaymentAmount)
	}
	if c1.PatientResponsibility != 50.00 {
		t.Errorf("claim 1 patient resp = %v", c1.PatientResponsibility)
	}
	if c1.PayerClaimControlNumber != "PAYER-REF-001" {
		t.Errorf("claim 1 payer ref = %q", c1.PayerClaimControlNumber)
	}
	if c1.PatientLastName != "SMITH" || c1.PatientFirstName != "JOHN" {
		t.Errorf("claim 1 patient = %q %q", c1.PatientLastName, c1.PatientFirstName)
	}
	if c1.PatientMemberID != "MEM123456" {
		t.Errorf("claim 1 member ID = %q", c1.PatientMemberID)
	}
	if c1.StatementFromDate != "20260801" {
		t.Errorf("claim 1 from date = %q", c1.StatementFromDate)
	}

	// Claim 1 service lines.
	if len(c1.ServiceLines) != 3 {
		t.Fatalf("claim 1: got %d service lines, want 3", len(c1.ServiceLines))
	}

	svc1 := c1.ServiceLines[0]
	if svc1.ProcedureCode != "99213" {
		t.Errorf("svc1 code = %q, want 99213", svc1.ProcedureCode)
	}
	if svc1.CodeQualifier != "HC" {
		t.Errorf("svc1 qualifier = %q, want HC", svc1.CodeQualifier)
	}
	if svc1.ChargeAmount != 150.00 {
		t.Errorf("svc1 charge = %v", svc1.ChargeAmount)
	}
	if svc1.PaymentAmount != 120.50 {
		t.Errorf("svc1 payment = %v", svc1.PaymentAmount)
	}
	if len(svc1.Adjustments) != 1 {
		t.Fatalf("svc1 adjustments = %d, want 1", len(svc1.Adjustments))
	}
	if svc1.Adjustments[0].GroupCode != "CO" || svc1.Adjustments[0].ReasonCode != "45" {
		t.Errorf("svc1 adj = %+v", svc1.Adjustments[0])
	}
	if svc1.Adjustments[0].Amount != 29.50 {
		t.Errorf("svc1 adj amount = %v, want 29.50", svc1.Adjustments[0].Amount)
	}
	if svc1.ServiceDate != "20260801" {
		t.Errorf("svc1 date = %q", svc1.ServiceDate)
	}
	if len(svc1.RemarkCodes) != 1 || svc1.RemarkCodes[0].Code != "N130" {
		t.Errorf("svc1 remarks = %+v", svc1.RemarkCodes)
	}

	// Service line 2: two CAS segments (CO and PR).
	svc2 := c1.ServiceLines[1]
	if svc2.ProcedureCode != "99214" {
		t.Errorf("svc2 code = %q", svc2.ProcedureCode)
	}
	if svc2.ProcedureModifier != "25" {
		t.Errorf("svc2 modifier = %q, want 25", svc2.ProcedureModifier)
	}
	if len(svc2.Adjustments) != 3 {
		t.Errorf("svc2 adjustments = %d, want 3 (two CO reasons + one PR)", len(svc2.Adjustments))
	}

	// Claim 2: denied (status 4).
	c2 := r.Claims[1]
	if c2.ClaimStatus != "4" {
		t.Errorf("claim 2 status = %q, want 4 (denied)", c2.ClaimStatus)
	}
	if c2.PaymentAmount != 0 {
		t.Errorf("claim 2 payment = %v, want 0", c2.PaymentAmount)
	}
	if c2.PatientLastName != "DOE" {
		t.Errorf("claim 2 patient = %q", c2.PatientLastName)
	}
	if len(c2.ServiceLines) != 1 {
		t.Fatalf("claim 2: got %d service lines, want 1", len(c2.ServiceLines))
	}
	if c2.ServiceLines[0].ProcedureCode != "99215" {
		t.Errorf("claim 2 svc code = %q", c2.ServiceLines[0].ProcedureCode)
	}
	if len(c2.ServiceLines[0].RemarkCodes) != 1 || c2.ServiceLines[0].RemarkCodes[0].Code != "MA04" {
		t.Errorf("claim 2 svc remarks = %+v", c2.ServiceLines[0].RemarkCodes)
	}

	// Provider-level adjustments.
	if len(r.ProviderAdjustments) != 1 {
		t.Fatalf("got %d provider adjustments, want 1", len(r.ProviderAdjustments))
	}
	plb := r.ProviderAdjustments[0]
	if plb.AdjustmentReason != "L6" {
		t.Errorf("PLB reason = %q, want L6 (interest)", plb.AdjustmentReason)
	}
	if plb.ReferenceID != "ORIG-CLAIM-99" {
		t.Errorf("PLB ref = %q", plb.ReferenceID)
	}
	if plb.Amount != -25.00 {
		t.Errorf("PLB amount = %v, want -25.00", plb.Amount)
	}
}

// ────────────────────────────────────────────────────────────────────────────────
// DEFECT TESTS: proving bugs in era.go
// ────────────────────────────────────────────────────────────────────────────────

// TestParseFloat_UnparseableAmountMustError proves DEFECT #1:
// parseFloat silently returns 0 for garbage input. In a financial context, 0 is a
// plausible payment amount (denied claims pay $0.00), so a corrupt amount that becomes
// zero is indistinguishable from a real denial. The parser must surface an error.
func TestParseFloat_UnparseableAmountMustError(t *testing.T) {
	// A realistic scenario: an ERA arrives with a garbled CLP04 (paid amount).
	// The parser should reject it, not silently record a $0 payment.
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*1200.50*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*EFT123456*1111111111~" +
		"DTM*405*20260824~" +
		"N1*PR*ACME HEALTH*XV*1234567890~" +
		"N1*PE*PROVIDER*XX*9876543210~" +
		"LX*1~" +
		"CLP*ACCT-001*1*500.00*GARBAGE*50.00*12*REF001~" + // <-- "GARBAGE" in CLP04
		"SVC*HC:99213*150.00*120.50**1~" +
		"CAS*CO*45*29.50~" +
		"DTM*472*20260801~" +
		"SE*12*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		// Good: the parser rejected it.
		return
	}

	// If no error, the parser silently turned "GARBAGE" into 0.
	if len(r.Claims) > 0 && r.Claims[0].PaymentAmount == 0 {
		t.Fatalf("DEFECT: unparseable payment amount 'GARBAGE' was silently converted to 0.00; "+
			"this is indistinguishable from a real $0 denial. Got claim payment=%v, want an error",
			r.Claims[0].PaymentAmount)
	}
}

// TestCAS_AllSixTripletsAreRead proves whether the parser reads all six
// adjustment triplets from a CAS segment. A weak model reads the first and drops the rest.
func TestCAS_AllSixTripletsAreRead(t *testing.T) {
	// CAS with all 6 triplets populated (group + 6*(reason+amount+quantity) = 19 elements)
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*100.00*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*CHK999*1111111111~" +
		"DTM*405*20260824~" +
		"N1*PR*PAYER*XV*1234567890~" +
		"N1*PE*PAYEE*XX*9876543210~" +
		"LX*1~" +
		"CLP*ACCT-100*1*1000.00*100.00*0.00*12*CTRL100~" +
		"SVC*HC:99213*1000.00*100.00**1~" +
		"CAS*CO*1*10.00*1*2*20.00*2*3*30.00*3*4*40.00*4*5*50.00*5*6*60.00*6~" +
		"DTM*472*20260801~" +
		"SE*12*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	if len(r.Claims) == 0 || len(r.Claims[0].ServiceLines) == 0 {
		t.Fatal("no claims/service lines parsed")
	}

	adjs := r.Claims[0].ServiceLines[0].Adjustments
	if len(adjs) != 6 {
		t.Fatalf("DEFECT: CAS with 6 triplets yielded %d adjustments, want 6. "+
			"Triplets 2-6 are being dropped.", len(adjs))
	}

	// Verify each triplet's values.
	for i, adj := range adjs {
		wantReason := fmt.Sprintf("%d", i+1)
		wantAmount := float64((i + 1) * 10)
		wantQty := float64(i + 1)

		if adj.ReasonCode != wantReason {
			t.Errorf("triplet %d: reason=%q, want %q", i+1, adj.ReasonCode, wantReason)
		}
		if adj.Amount != wantAmount {
			t.Errorf("triplet %d: amount=%v, want %v", i+1, adj.Amount, wantAmount)
		}
		if adj.Quantity != wantQty {
			t.Errorf("triplet %d: quantity=%v, want %v", i+1, adj.Quantity, wantQty)
		}
		if adj.GroupCode != "CO" {
			t.Errorf("triplet %d: group=%q, want CO", i+1, adj.GroupCode)
		}
	}
}

// TestSVCAttributionToCorrectClaim proves that service lines are attributed to the
// correct parent claim. An off-by-one in loop handling would attribute a service line
// to the previous claim.
func TestSVCAttributionToCorrectClaim(t *testing.T) {
	// Three claims, each with a different number of service lines.
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*600.00*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*CHK001*1111111111~" +
		"DTM*405*20260824~" +
		"N1*PR*PAYER*XV*1234567890~" +
		"N1*PE*PAYEE*XX*9876543210~" +
		"LX*1~" +
		// Claim 1: 1 service line.
		"CLP*CLAIM-A*1*200.00*150.00*0.00*12*REF-A~" +
		"SVC*HC:99211*200.00*150.00**1~" +
		"CAS*CO*45*50.00~" +
		"DTM*472*20260801~" +
		// Claim 2: 3 service lines.
		"CLP*CLAIM-B*1*400.00*300.00*0.00*12*REF-B~" +
		"SVC*HC:99212*100.00*80.00**1~" +
		"CAS*CO*45*20.00~" +
		"DTM*472*20260802~" +
		"SVC*HC:99213*150.00*120.00**1~" +
		"CAS*CO*45*30.00~" +
		"DTM*472*20260802~" +
		"SVC*HC:99214*150.00*100.00**1~" +
		"CAS*CO*45*50.00~" +
		"DTM*472*20260802~" +
		// Claim 3: 2 service lines.
		"CLP*CLAIM-C*1*300.00*150.00*0.00*12*REF-C~" +
		"SVC*HC:99215*200.00*100.00**1~" +
		"CAS*CO*45*100.00~" +
		"DTM*472*20260803~" +
		"SVC*HC:36415*100.00*50.00**1~" +
		"CAS*CO*45*50.00~" +
		"DTM*472*20260803~" +
		"SE*28*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	if len(r.Claims) != 3 {
		t.Fatalf("got %d claims, want 3", len(r.Claims))
	}

	// Claim A must have exactly 1 service line.
	if got := len(r.Claims[0].ServiceLines); got != 1 {
		t.Errorf("DEFECT: Claim-A has %d service lines, want 1 (off-by-one attribution)", got)
	}
	if r.Claims[0].PatientAccountNumber != "CLAIM-A" {
		t.Errorf("claim 0 account = %q, want CLAIM-A", r.Claims[0].PatientAccountNumber)
	}
	if r.Claims[0].ServiceLines[0].ProcedureCode != "99211" {
		t.Errorf("claim A svc0 code = %q, want 99211", r.Claims[0].ServiceLines[0].ProcedureCode)
	}

	// Claim B must have exactly 3 service lines.
	if got := len(r.Claims[1].ServiceLines); got != 3 {
		t.Errorf("DEFECT: Claim-B has %d service lines, want 3 (off-by-one attribution)", got)
	}
	if r.Claims[1].PatientAccountNumber != "CLAIM-B" {
		t.Errorf("claim 1 account = %q, want CLAIM-B", r.Claims[1].PatientAccountNumber)
	}
	// Check the last SVC under claim B is attributed correctly.
	if r.Claims[1].ServiceLines[2].ProcedureCode != "99214" {
		t.Errorf("claim B svc2 code = %q, want 99214", r.Claims[1].ServiceLines[2].ProcedureCode)
	}

	// Claim C must have exactly 2 service lines.
	if got := len(r.Claims[2].ServiceLines); got != 2 {
		t.Errorf("DEFECT: Claim-C has %d service lines, want 2 (off-by-one attribution)", got)
	}
	if r.Claims[2].PatientAccountNumber != "CLAIM-C" {
		t.Errorf("claim 2 account = %q, want CLAIM-C", r.Claims[2].PatientAccountNumber)
	}
}

// TestPLBSignConvention proves that PLB amounts preserve the sign from the file.
// A take-back is negative (money recouped from the provider). The parser must not
// negate it or take the absolute value.
func TestPLBSignConvention(t *testing.T) {
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*100.00*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*CHK001*1111111111~" +
		"DTM*405*20260824~" +
		"N1*PR*PAYER*XV*1234567890~" +
		"N1*PE*PAYEE*XX*9876543210~" +
		"LX*1~" +
		"CLP*ACCT*1*200.00*100.00*0.00*12*REF~" +
		"SVC*HC:99213*200.00*100.00**1~" +
		"DTM*472*20260801~" +
		// Two PLB entries: one negative (take-back) and one positive (credit).
		"PLB*9876543210*20261231*WO:ORIG-1*-50.00*FB:ORIG-2*25.00~" +
		"SE*12*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	if len(r.ProviderAdjustments) != 2 {
		t.Fatalf("got %d PLB adjustments, want 2", len(r.ProviderAdjustments))
	}

	plb1 := r.ProviderAdjustments[0]
	if plb1.Amount != -50.00 {
		t.Errorf("DEFECT: PLB take-back amount = %v, want -50.00 (sign inverted?)", plb1.Amount)
	}
	if plb1.AdjustmentReason != "WO" {
		t.Errorf("PLB1 reason = %q, want WO", plb1.AdjustmentReason)
	}
	if plb1.ReferenceID != "ORIG-1" {
		t.Errorf("PLB1 refID = %q, want ORIG-1", plb1.ReferenceID)
	}

	plb2 := r.ProviderAdjustments[1]
	if plb2.Amount != 25.00 {
		t.Errorf("DEFECT: PLB credit amount = %v, want 25.00", plb2.Amount)
	}
	if plb2.AdjustmentReason != "FB" {
		t.Errorf("PLB2 reason = %q, want FB", plb2.AdjustmentReason)
	}
}

// TestDTMQualifiersAreDistinguished proves that DTM date qualifiers are routed to
// the correct fields and not conflated.
func TestDTMQualifiersAreDistinguished(t *testing.T) {
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*:~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*100.00*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*CHK001*1111111111~" +
		"DTM*405*20260101~" + // production date
		"N1*PR*PAYER*XV*1234567890~" +
		"N1*PE*PAYEE*XX*9876543210~" +
		"LX*1~" +
		"CLP*ACCT*1*200.00*100.00*0.00*12*REF~" +
		"DTM*232*20260201~" + // statement from
		"DTM*233*20260215~" + // statement to
		"SVC*HC:99213*200.00*100.00**1~" +
		"DTM*472*20260205~" + // service date
		"SE*13*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	if r.ProductionDate != "20260101" {
		t.Errorf("production date = %q, want 20260101", r.ProductionDate)
	}

	if len(r.Claims) == 0 {
		t.Fatal("no claims")
	}
	c := r.Claims[0]
	if c.StatementFromDate != "20260201" {
		t.Errorf("statement from = %q, want 20260201 (DTM qualifier confused?)", c.StatementFromDate)
	}
	if c.StatementToDate != "20260215" {
		t.Errorf("statement to = %q, want 20260215 (DTM qualifier confused?)", c.StatementToDate)
	}

	if len(c.ServiceLines) == 0 {
		t.Fatal("no service lines")
	}
	if c.ServiceLines[0].ServiceDate != "20260205" {
		t.Errorf("service date = %q, want 20260205 (DTM*472 conflated with claim-level DTM?)",
			c.ServiceLines[0].ServiceDate)
	}
}

// TestSeparatorsFromISAAreUsed proves the parser uses the component separator
// declared in ISA16 rather than hardcoding ':'.
func TestSeparatorsFromISAAreUsed(t *testing.T) {
	// Use '>' as component separator instead of ':'.
	raw := "ISA*00*          *00*          *ZZ*PAYERID        *ZZ*PROVID         *260824*1200*^*00501*000000001*0*P*>~" +
		"GS*HP*PAYERID*PROVID*20260824*1200*1*X*005010X221A1~" +
		"ST*835*0001*005010X221A1~" +
		"BPR*I*100.00*C*ACH*CCP*01*999999999*DA*1234567890*1111111111**01*222222222*DA*9876543210*20260824~" +
		"TRN*1*CHK001*1111111111~" +
		"DTM*405*20260824~" +
		"N1*PR*PAYER*XV*1234567890~" +
		"N1*PE*PAYEE*XX*9876543210~" +
		"LX*1~" +
		"CLP*ACCT*1*200.00*100.00*0.00*12*REF~" +
		"SVC*HC>99213>25*200.00*100.00**1~" + // composite uses '>' not ':'
		"CAS*CO*45*100.00~" +
		"DTM*472*20260801~" +
		"PLB*9876543210*20261231*L6>ORIG-CLAIM*-10.00~" + // PLB composite uses '>'
		"SE*13*0001~" +
		"GE*1*1~" +
		"IEA*1*000000001~"

	m, err := ParseString(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r, err := ParseERA(m)
	if err != nil {
		t.Fatalf("ParseERA: %v", err)
	}

	if len(r.Claims) == 0 || len(r.Claims[0].ServiceLines) == 0 {
		t.Fatal("no claims/service lines")
	}

	svc := r.Claims[0].ServiceLines[0]
	if svc.ProcedureCode != "99213" {
		t.Errorf("DEFECT: with '>' as component sep, procedure code = %q, want 99213 "+
			"(parser hardcodes ':'?)", svc.ProcedureCode)
	}
	if svc.ProcedureModifier != "25" {
		t.Errorf("DEFECT: modifier = %q, want 25", svc.ProcedureModifier)
	}
	if svc.CodeQualifier != "HC" {
		t.Errorf("DEFECT: qualifier = %q, want HC", svc.CodeQualifier)
	}

	// PLB composite must also use '>' correctly.
	if len(r.ProviderAdjustments) == 0 {
		t.Fatal("no PLB adjustments")
	}
	if r.ProviderAdjustments[0].ReferenceID != "ORIG-CLAIM" {
		t.Errorf("DEFECT: PLB reference = %q, want ORIG-CLAIM (component sep not from ISA?)",
			r.ProviderAdjustments[0].ReferenceID)
	}
}
