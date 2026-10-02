package cms0057

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testSystem is a payer identifier namespace for tests. Not example.org, which the HL7 validator refuses outright.
const testSystem = "https://fhir.springfield-health-plan.org/identifier"

func convertFixture(t *testing.T, claim, remit string, opt CARINOptions) []CARINResult {
	t.Helper()
	c, err := os.ReadFile(filepath.Join("testdata", claim))
	if err != nil {
		t.Fatal(err)
	}
	r, err := os.ReadFile(filepath.Join("testdata", remit))
	if err != nil {
		t.Fatal(err)
	}
	if opt.Now.IsZero() {
		opt.Now = time.Date(2025, 5, 2, 12, 0, 0, 0, time.UTC)
	}
	res, _, err := ConvertClaims(c, r, opt)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

// writeForValidator leaves each resource as its own file under $CARIN_OUT, for the HL7 validator run in docs/verification.md.
func writeForValidator(t *testing.T, name string, res []CARINResult) {
	dir := os.Getenv("CARIN_OUT")
	if dir == "" {
		return
	}
	for i, r := range res {
		// The whole bundle as well, as a collection with fullUrls, so references between the resources resolve.
		var entries []any
		for _, e := range r.Bundle["entry"].([]any) {
			res := e.(map[string]any)["resource"].(map[string]any)
			entries = append(entries, map[string]any{
				"fullUrl":  "https://fhir.springfield-health-plan.org/r4/" + res["resourceType"].(string) + "/" + res["id"].(string),
				"resource": res,
			})
		}
		raw, _ := json.MarshalIndent(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name+"-bundle-"+string(rune('a'+i))+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, e := range r.Bundle["entry"].([]any) {
			resource := e.(map[string]any)["resource"].(map[string]any)
			raw, _ := json.MarshalIndent(resource, "", "  ")
			file := filepath.Join(dir, name+"-"+resource["resourceType"].(string)+"-"+resource["id"].(string)+".json")
			if err := os.WriteFile(file, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func eobOf(t *testing.T, r CARINResult) map[string]any {
	t.Helper()
	for _, e := range r.Bundle["entry"].([]any) {
		res := e.(map[string]any)["resource"].(map[string]any)
		if res["resourceType"] == "ExplanationOfBenefit" {
			return res
		}
	}
	t.Fatal("no ExplanationOfBenefit in the bundle")

	return nil
}

func TestCARINProfessional(t *testing.T) {
	res := convertFixture(t, "837p.x12", "835p.x12", CARINOptions{NetworkStatus: "innetwork", IdentifierSystem: testSystem})
	writeForValidator(t, "professional", res)
	if len(res) != 1 || res[0].ClaimNumber != "EHPCLAIM20250001" {
		t.Fatalf("got %+v", res)
	}
	eob := eobOf(t, res[0])
	if eob["meta"].(map[string]any)["profile"].([]any)[0] != carinBase+"C4BB-ExplanationOfBenefit-Professional-NonClinician|"+CARINVersion {
		t.Fatalf("profile: %v", eob["meta"])
	}
	dx := eob["diagnosis"].([]any)
	if got := dx[0].(map[string]any)["diagnosisCodeableConcept"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"]; got != "J02.9" {
		t.Fatalf("ICD-10-CM code written without its decimal point: %v", got)
	}
	totals := map[string]float64{}
	for _, x := range eob["total"].([]any) {
		m := x.(map[string]any)
		code := m["category"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"].(string)
		totals[code] = m["amount"].(map[string]any)["value"].(float64)
	}
	want := map[string]float64{"submitted": 250, "eligible": 200, "coinsurance": 25, "copay": 20, "discount": 50, "paidtoprovider": 155, "memberliability": 45}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("total %s = %v, want %v (all: %v)", k, totals[k], v, totals)
		}
	}
	if eob["payment"].(map[string]any)["date"] != "2025-03-15" {
		t.Fatalf("payment date: %v", eob["payment"])
	}
}

func TestCARINInpatient(t *testing.T) {
	res := convertFixture(t, "837i.x12", "835i.x12", CARINOptions{NetworkStatus: "innetwork", IdentifierSystem: testSystem})
	writeForValidator(t, "inpatient", res)
	eob := eobOf(t, res[0])
	if eob["meta"].(map[string]any)["profile"].([]any)[0] != carinBase+"C4BB-ExplanationOfBenefit-Inpatient-Institutional|"+CARINVersion {
		t.Fatalf("profile: %v", eob["meta"])
	}
	found := map[string]bool{}
	for _, s := range eob["supportingInfo"].([]any) {
		found[s.(map[string]any)["category"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"].(string)] = true
	}
	for _, want := range []string{"clmrecvddate", "typeofbill", "pointoforigin", "admtype", "discharge-status", "drg", "admissionperiod"} {
		if !found[want] {
			t.Errorf("supportingInfo %s missing", want)
		}
	}
	if len(eob["procedure"].([]any)) != 1 {
		t.Fatalf("procedure missing")
	}
}

func TestCARINOutpatientFromTypeOfBill(t *testing.T) {
	c, _ := os.ReadFile("testdata/837i.x12")
	r, _ := os.ReadFile("testdata/835i.x12")
	c = []byte(replaceAll(string(c), "CLM*PATACCT002*12500***11:A:1", "CLM*PATACCT002*12500***13:A:1"))
	res, _, err := ConvertClaims(c, r, CARINOptions{NetworkStatus: "outofnetwork", IdentifierSystem: testSystem})
	if err != nil {
		t.Fatal(err)
	}
	writeForValidator(t, "outpatient", res)
	eob := eobOf(t, res[0])
	if eob["meta"].(map[string]any)["profile"].([]any)[0] != carinBase+"C4BB-ExplanationOfBenefit-Outpatient-Institutional|"+CARINVersion {
		t.Fatalf("a 013x type of bill was not read as outpatient: %v", eob["meta"])
	}
}

func TestCARINRefusesUnadjudicatedClaim(t *testing.T) {
	c, _ := os.ReadFile("testdata/837p.x12")
	r, _ := os.ReadFile("testdata/835i.x12")
	if _, _, err := ConvertClaims(c, r, CARINOptions{IdentifierSystem: testSystem}); err == nil {
		t.Fatal("a claim with no matching 835 payment was converted")
	}
}

func TestCARINRequiresIdentifierSystem(t *testing.T) {
	c, _ := os.ReadFile("testdata/837p.x12")
	r, _ := os.ReadFile("testdata/835p.x12")
	if _, _, err := ConvertClaims(c, r, CARINOptions{}); err == nil {
		t.Fatal("converted with no identifier system, which CARIN's Patient profile requires")
	}
}

func TestValidNPI(t *testing.T) {
	for npi, want := range map[string]bool{"1234567893": true, "1497758544": true, "1922083376": false, "123": false, "12345678a3": false} {
		if ValidNPI(npi) != want {
			t.Errorf("ValidNPI(%q) = %v", npi, !want)
		}
	}
}

func TestCARINIsIdempotent(t *testing.T) {
	a := convertFixture(t, "837p.x12", "835p.x12", CARINOptions{IdentifierSystem: testSystem})
	b := convertFixture(t, "837p.x12", "835p.x12", CARINOptions{IdentifierSystem: testSystem})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the same claim converted twice produced different output, so a reload would duplicate it")
	}
}

func replaceAll(s, old, new string) string {
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}
