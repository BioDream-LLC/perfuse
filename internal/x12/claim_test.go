package x12

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) *Message {
	t.Helper()
	raw, err := os.ReadFile("../cms0057/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return m
}

func TestParseProfessionalClaim(t *testing.T) {
	claims, err := ParseClaims(readFixture(t, "837p.x12"))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(claims))
	}
	c := claims[0]
	if c.Kind != ClaimProfessional || c.PatientAccountNumber != "PATACCT001" || c.ChargeAmount != 250 {
		t.Fatalf("claim header wrong: %+v", c)
	}
	if !c.PatientIsSubscriber || c.Patient.ID != "MBR123456" || c.Patient.BirthDate != "19800215" || c.Relationship != "18" {
		t.Fatalf("patient wrong: %+v rel=%s", c.Patient, c.Relationship)
	}
	if c.BillingProvider.ID != "1234567893" || c.BillingProvider.TaxID != "123456789" || c.BillingProvider.Taxonomy != "207Q00000X" {
		t.Fatalf("billing provider wrong: %+v", c.BillingProvider)
	}
	if c.Payer.ID != "EHP01" || c.Payer.LastName != "EXAMPLE HEALTH PLAN" {
		t.Fatalf("payer wrong: %+v", c.Payer)
	}
	if c.FacilityCode != "11" || c.MedicalRecordNumber != "MRN0099" || c.Rendering.ID != "1497758544" || c.Rendering.Taxonomy != "207Q00000X" {
		t.Fatalf("claim details wrong: %+v", c)
	}
	if len(c.Diagnoses) != 2 || c.Diagnoses[0].Qualifier != "ABK" || c.Diagnoses[0].Code != "J029" {
		t.Fatalf("diagnoses wrong: %+v", c.Diagnoses)
	}
	if len(c.Lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(c.Lines))
	}
	l := c.Lines[0]
	if l.ProcedureCode != "99213" || len(l.Modifiers) != 1 || l.Modifiers[0] != "25" || l.ChargeAmount != 150 ||
		l.ServiceDateFrom != "20250221" || l.LineControlNumber != "LINE001" || len(l.DiagnosisPointers) != 2 {
		t.Fatalf("line 1 wrong: %+v", l)
	}
}

func TestParseInstitutionalClaimWithDependent(t *testing.T) {
	claims, err := ParseClaims(readFixture(t, "837i.x12"))
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	if c.Kind != ClaimInstitutional || c.PatientIsSubscriber || c.Patient.FirstName != "CHARLIE" || c.Relationship != "19" {
		t.Fatalf("dependent not read: %+v rel=%s", c.Patient, c.Relationship)
	}
	if c.Subscriber.ID != "MBR123456" {
		t.Fatalf("subscriber lost: %+v", c.Subscriber)
	}
	if c.TypeOfBill() != "0111" || !c.IsInpatient() {
		t.Fatalf("type of bill %q inpatient=%v", c.TypeOfBill(), c.IsInpatient())
	}
	if c.StatementFrom != "20250401" || c.StatementTo != "20250404" || c.AdmissionDate != "202504010830" {
		t.Fatalf("dates wrong: %+v", c)
	}
	if c.AdmissionType != "1" || c.AdmissionSource != "7" || c.DischargeStatus != "01" || c.DRG != "343" {
		t.Fatalf("institutional codes wrong: %+v", c)
	}
	if len(c.Diagnoses) != 4 || c.Diagnoses[0].PresentOnAdmit != "Y" || c.Diagnoses[2].PresentOnAdmit != "N" {
		t.Fatalf("diagnoses wrong: %+v", c.Diagnoses)
	}
	if len(c.Procedures) != 1 || c.Procedures[0].Code != "0DTJ4ZZ" || c.Procedures[0].Date != "20250401" {
		t.Fatalf("procedures wrong: %+v", c.Procedures)
	}
	if c.Attending.ID != "1922083377" || c.Operating.ID != "1871234567" {
		t.Fatalf("providers wrong")
	}
	if len(c.Lines) != 2 || c.Lines[0].RevenueCode != "0120" || c.Lines[0].Quantity != 3 || c.Lines[1].ProcedureCode != "44970" {
		t.Fatalf("lines wrong: %+v", c.Lines)
	}
}

func TestParseClaimsSkipsOtherPayerLoops(t *testing.T) {
	raw := string(mustRead(t, "837p.x12"))
	// A 2320/2330 other-payer block inserted before the first line: its NM1*IL and NM1*PR describe someone else's coverage.
	raw = replaceOnce(raw, "LX*1~", "SBR*S*01*OTHERGRP******CI~NM1*IL*1*OTHERPERSON*PAT****MI*OTHER999~NM1*PR*2*OTHER PLAN*****PI*OTH01~LX*1~")
	m, err := ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseClaims(m)
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	if c.Subscriber.ID != "MBR123456" || c.Payer.ID != "EHP01" || len(c.Lines) != 2 {
		t.Fatalf("other-payer loop leaked into the claim: sub=%s payer=%s lines=%d", c.Subscriber.ID, c.Payer.ID, len(c.Lines))
	}
}

func TestParseClaimsRefusesDental(t *testing.T) {
	raw := replaceOnce(string(mustRead(t, "837p.x12")), "ST*837*0001*005010X222A1~", "ST*837*0001*005010X224A2~")
	m, err := ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseClaims(m); err == nil {
		t.Fatal("a dental 837 was read as if it were professional")
	}
}

func TestParseERAReceivedDateAndAllowed(t *testing.T) {
	r, err := ParseERA(readFixture(t, "835p.x12"))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Claims[0]
	if c.ReceivedDate != "20250305" || c.PayerClaimControlNumber != "EHPCLAIM20250001" {
		t.Fatalf("claim wrong: %+v", c)
	}
	if !c.ServiceLines[0].HasAllowed || c.ServiceLines[0].AllowedAmount != 120 || c.ServiceLines[0].LineControlNumber != "LINE001" {
		t.Fatalf("line wrong: %+v", c.ServiceLines[0])
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../cms0057/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}

	return s
}
