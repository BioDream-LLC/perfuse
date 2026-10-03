package x12

import (
	"strings"
	"testing"
	"time"
)

var fixed = time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)

func TestA270IsAWellFormedInterchangeThePayerCanRead(t *testing.T) {
	raw, err := BuildEligibilityInquiry(EligibilityRequest{
		Envelope:     Envelope{SenderID: "CLINIC01", ReceiverID: "PAYER01", ControlNumber: 42, Now: fixed},
		Payer:        Person{LastName: "Springfield Health Plan", ID: "SHP01"},
		Provider:     Person{LastName: "Riverside Clinic", ID: "1234567893"},
		Subscriber:   Person{LastName: "DOE", FirstName: "JANE", ID: "MBR123456", DOB: "19800101", Gender: "F"},
		ServiceTypes: []string{"30", "98"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("our own 270 does not parse: %v\n%s", err, raw)
	}
	if v := m.Validate(); len(v.Problems) > 0 {
		t.Errorf("envelope problems: %+v", v.Problems)
	}
	for _, want := range []string{"GS*HS*CLINIC01*PAYER01", "ST*270*0042*005010X279A1", "BHT*0022*13*", "HL*3*2*22*0",
		"NM1*IL*1*DOE*JANE****MI*MBR123456", "DMG*D8*19800101*F", "DTP*291*D8*20261003", "EQ*30~EQ*98~", "TRN*1*", "*00501*000000042*"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in\n%s", want, strings.ReplaceAll(string(raw), "~", "~\n"))
		}
	}
	if got := m.Segments("SE"); len(got) != 1 || got[0].Element(1).String() != "14" {
		t.Errorf("SE01 does not count the segments: %v", got)
	}
}

func TestA270ForADependentPutsTheTraceAndEQOnTheDependent(t *testing.T) {
	raw, err := BuildEligibilityInquiry(EligibilityRequest{
		Envelope:   Envelope{SenderID: "C", ReceiverID: "P", Now: fixed},
		Payer:      Person{LastName: "Plan", ID: "P1"},
		Provider:   Person{LastName: "Clinic", ID: "1234567893"},
		Subscriber: Person{LastName: "DOE", FirstName: "JOHN", ID: "MBR1"},
		Dependent:  &Person{LastName: "DOE", FirstName: "AMY", DOB: "20150505"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "HL*3*2*22*1~NM1*IL*1*DOE*JOHN****MI*MBR1~HL*4*3*23*0~TRN*1*") || !strings.Contains(s, "NM1*03*1*DOE*AMY~DMG*D8*20150505~DTP*291*D8*20261003~EQ*30~") {
		t.Errorf("dependent structure:\n%s", strings.ReplaceAll(s, "~", "~\n"))
	}
}

func TestA270RefusesWhatAPayerWouldReject(t *testing.T) {
	base := EligibilityRequest{Envelope: Envelope{SenderID: "C", ReceiverID: "P"}, Payer: Person{LastName: "Plan", ID: "P1"},
		Provider: Person{LastName: "Clinic", ID: "1234567893"}, Subscriber: Person{LastName: "DOE", ID: "MBR1"}}
	bad := base
	bad.Provider.ID = "1234567890"
	if _, err := BuildEligibilityInquiry(bad); err == nil || !strings.Contains(err.Error(), "check digit") {
		t.Errorf("a bad NPI: %v", err)
	}
	bad = base
	bad.Subscriber.LastName = "DOE*SMITH"
	if _, err := BuildEligibilityInquiry(bad); err == nil {
		t.Error("a delimiter in a name was accepted")
	}
	bad = base
	bad.Subscriber.ID = ""
	if _, err := BuildEligibilityInquiry(bad); err == nil || !strings.Contains(err.Error(), "member ID") {
		t.Errorf("no member ID: %v", err)
	}
}

// A synthetic 271 in the shape payers return: active coverage, a PPO plan, in- and out-of-network cost sharing for 30, remaining
// deductible, and a handful of service types.
const sample271 = "ISA*00*          *00*          *ZZ*PAYER01        *ZZ*CLINIC01       *261003*0931*^*00501*000000043*0*T*:~" +
	"GS*HB*PAYER01*CLINIC01*20261003*0931*43*X*005010X279A1~ST*271*0043*005010X279A1~BHT*0022*11*TRACE42*20261003*0931~" +
	"HL*1**20*1~NM1*PR*2*Springfield Health Plan*****PI*SHP01~HL*2*1*21*1~NM1*1P*2*Riverside Clinic*****XX*1234567893~" +
	"HL*3*2*22*0~TRN*2*TRACE42*9CLINIC010~NM1*IL*1*DOE*JANE****MI*MBR123456~DMG*D8*19800101*F~DTP*346*D8*20260101~" +
	"EB*1*IND*30^1^33^35^47^48^50^86^88^98^AL^MH^UC*PR*Gold PPO 2000~" +
	"EB*B*IND*30***27*25*****Y~EB*B*IND*30***27*60*****N~" +
	"EB*A*IND*30*****.2****Y~EB*A*IND*30*****.4****N~" +
	"EB*C*IND*30***23*2000*****Y~EB*C*IND*30***29*800*****Y~EB*C*IND*30***23*4000*****N~EB*C*IND*30***29*4000*****N~" +
	"EB*G*FAM*30***23*12000*****Y~MSG*Includes medical and pharmacy~" +
	"SE*23*0043~GE*1*43~IEA*1*000000043~"

func TestA271IsAssembledIntoTheAnswerAFrontDeskWants(t *testing.T) {
	m, err := Parse([]byte(sample271))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsEligibilityResponse() {
		t.Fatal("not recognised as a 271")
	}
	if p := m.Validate().Problems; len(p) > 0 {
		t.Fatalf("the fixture's own envelope is wrong: %+v", p)
	}
	e, err := m.ReadEligibility()
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "active" || e.Plan != "Gold PPO 2000" || e.Subscriber.ID != "MBR123456" || e.Subscriber.DOB != "19800101" ||
		e.Payer.ID != "SHP01" || e.Trace != "TRACE42" {
		t.Errorf("read: %+v", e)
	}
	joined := strings.Join(e.Summary, "\n")
	for _, want := range []string{"JANE DOE: coverage active under Gold PPO 2000.", "Co-payment in network: $25.", "Coinsurance out of network: 40%.",
		"Individual deductible remaining in network: $800.", "Family out-of-pocket maximum in network: $12000."} {
		if !strings.Contains(joined, want) {
			t.Errorf("summary lacks %q:\n%s", want, joined)
		}
	}
	if b := e.Benefits[len(e.Benefits)-1]; len(b.Messages) != 1 {
		t.Errorf("the MSG was not attached to its benefit: %+v", b)
	}
	for _, f := range e.CheckCORE() {
		if !f.Met {
			t.Errorf("CORE requirement not met on a complete response: %+v", f)
		}
	}
}

func TestCOREReportsWhatAThinResponseLeftOut(t *testing.T) {
	thin := strings.Replace(sample271, "EB*1*IND*30^1^33^35^47^48^50^86^88^98^AL^MH^UC*PR*Gold PPO 2000~", "EB*1*IND*30*PR*Gold PPO 2000~", 1)
	thin = strings.Replace(thin, "EB*C*IND*30***29*800*****Y~", "", 1)
	m, _ := Parse([]byte(thin))
	e, _ := m.ReadEligibility()
	unmet := map[string]string{}
	for _, f := range e.CheckCORE() {
		if !f.Met {
			unmet[f.Requirement] = f.Detail
		}
	}
	if _, ok := unmet["Deductible remaining, in network"]; !ok {
		t.Errorf("a missing remaining deductible was not reported: %+v", unmet)
	}
	if d, ok := unmet["The base service types (1, 33, 35, 47, 48, 50, 86, 88, 98, AL, MH, UC)"]; !ok || !strings.Contains(d, "AL") {
		t.Errorf("missing service types were not named: %+v", unmet)
	}
	if len(unmet) != 2 {
		t.Errorf("unexpected findings: %+v", unmet)
	}
}

func TestARejected271SaysWhy(t *testing.T) {
	rej := "ISA*00*          *00*          *ZZ*P              *ZZ*C              *261003*0931*^*00501*000000044*0*T*:~" +
		"GS*HB*P*C*20261003*0931*44*X*005010X279A1~ST*271*0044*005010X279A1~BHT*0022*11*T1*20261003*0931~HL*1**20*1~NM1*PR*2*Plan*****PI*P1~" +
		"HL*2*1*21*1~NM1*1P*2*Clinic*****XX*1234567893~HL*3*2*22*0~NM1*IL*1*DOE*JANE****MI*BAD~AAA*N**75*C~SE*10*0044~GE*1*44~IEA*1*000000044~"
	m, err := Parse([]byte(rej))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := m.ReadEligibility()
	if len(e.Rejections) != 1 || e.Rejections[0].Meaning != "subscriber/insured not found" || e.Rejections[0].FollowUp != "please correct and resubmit" {
		t.Fatalf("%+v", e.Rejections)
	}
	if !strings.Contains(e.Summary[0], "subscriber/insured not found") {
		t.Errorf("summary: %v", e.Summary)
	}
	if f := e.CheckCORE(); len(f) != 1 || f[0].Met {
		t.Errorf("core on a rejection: %+v", f)
	}
}

func TestA276NamesTheClaimTheWayAPayerFindsIt(t *testing.T) {
	raw, err := BuildClaimStatusRequest(ClaimStatusRequest{
		Envelope:       Envelope{SenderID: "CLINIC01", ReceiverID: "PAYER01", ControlNumber: 7, Now: fixed},
		Payer:          Person{LastName: "Springfield Health Plan", ID: "SHP01"},
		Provider:       Person{LastName: "Riverside Clinic", ID: "1234567893"},
		Subscriber:     Person{LastName: "DOE", FirstName: "JANE", ID: "MBR123456"},
		PatientAccount: "PCN0042", PayerClaimNumber: "EHPCLAIM20250001", ChargeAmount: "250.00", ServiceFrom: "20260915",
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Validate(); len(v.Problems) > 0 {
		t.Errorf("envelope problems: %+v", v.Problems)
	}
	for _, want := range []string{"GS*HR*", "ST*276*0007*005010X212", "BHT*0010*13*", "HL*4*3*22*0", "REF*1K*EHPCLAIM20250001", "REF*EJ*PCN0042",
		"AMT*T3*250.00", "DTP*472*RD8*20260915-20260915", "NM1*41*2*Riverside Clinic*****46*1234567893"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q", want)
		}
	}
	if _, err := BuildClaimStatusRequest(ClaimStatusRequest{Envelope: Envelope{SenderID: "a", ReceiverID: "b"}}); err == nil ||
		!strings.Contains(err.Error(), "patient account") {
		t.Errorf("an empty 276: %v", err)
	}
}

const sample834 = "ISA*00*          *00*          *ZZ*SPONSOR        *ZZ*PAYER01        *261003*0931*^*00501*000000050*0*T*:~" +
	"GS*BE*SPONSOR*PAYER01*20261003*0931*50*X*005010X220A1~ST*834*0050*005010X220A1~BGN*00*REF1*20261003*0931****2~" +
	"N1*P5*Acme Manufacturing*FI*123456789~N1*IN*Springfield Health Plan*FI*987654321~" +
	"INS*Y*18*021*28*A***FT~REF*0F*MBR123456~NM1*IL*1*DOE*JANE****34*123456789~DMG*D8*19800101*F~" +
	"HD*021**HLT*GOLDPPO*FAM~DTP*348*D8*20261101~HD*021**DEN*DENTAL1*FAM~DTP*348*D8*20261101~" +
	"INS*N*19*021*28*A~REF*0F*MBR123456~REF*23*MBR123456-02~NM1*IL*1*DOE*AMY~DMG*D8*20150505*F~HD*021**HLT*GOLDPPO*FAM~DTP*348*D8*20261101~" +
	"INS*Y*18*024*07*A~REF*0F*MBR999~NM1*IL*1*ROE*RICHARD~HD*024**HLT*GOLDPPO*IND~DTP*349*D8*20260930~" +
	"SE*25*0050~GE*1*50~IEA*1*000000050~"

func TestAn834IsReadIntoMembersAndTheirCoverage(t *testing.T) {
	m, err := Parse([]byte(sample834))
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.ReadEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	if e.Sponsor.LastName != "Acme Manufacturing" || e.Payer.LastName != "Springfield Health Plan" || len(e.Members) != 3 {
		t.Fatalf("%+v", e)
	}
	jane, amy, richard := e.Members[0], e.Members[1], e.Members[2]
	if !jane.Subscriber || jane.Relationship != "self" || jane.Action != "addition" || jane.SubscriberID != "MBR123456" ||
		len(jane.Coverages) != 2 || jane.Coverages[1].Line != "dental" || jane.Coverages[0].Begin != "20261101" {
		t.Errorf("jane: %+v", jane)
	}
	if amy.Subscriber || amy.Relationship != "child" || amy.MemberID != "MBR123456-02" || amy.Person.DOB != "20150505" {
		t.Errorf("amy: %+v", amy)
	}
	if richard.Action != "cancellation or termination" || richard.Coverages[0].End != "20260930" {
		t.Errorf("richard: %+v", richard)
	}
}
