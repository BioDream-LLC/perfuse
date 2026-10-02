package cms0057

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The inputs are the Da Vinci PAS 2.2.1 guide's own examples, so the conversion is tested against what a real PAS
// implementation produces rather than against a shape written here to agree with the converter.

func TestPriorAuthFromPASExamples(t *testing.T) {
	claim, _ := os.ReadFile("testdata/pas-claim-referral.json")
	for _, tc := range []struct{ file, want string }{
		{"pas-response-referral.json", "approved"},
		{"pas-response-pending.json", "pended"},
	} {
		resp, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		res, err := PriorAuthFromJSON(resp, claim, time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if res.Decision != tc.want {
			t.Errorf("%s: decision %q, want %q", tc.file, res.Decision, tc.want)
		}
		eob := res.ExplanationOfBenefit
		if eob["use"] != "preauthorization" || eob["insurance"] == nil || eob["item"] == nil {
			t.Fatalf("%s: incomplete EOB %v", tc.file, eob)
		}
		if dir := os.Getenv("CARIN_OUT"); dir != "" {
			raw, _ := json.MarshalIndent(eob, "", "  ")
			_ = os.WriteFile(filepath.Join(dir, "pdex-"+tc.file), raw, 0o644)
		}
	}
}

func TestPriorAuthNeedsCoverage(t *testing.T) {
	resp, _ := os.ReadFile("testdata/pas-response-referral.json")
	if _, err := PriorAuthFromJSON(resp, nil, time.Time{}); err == nil {
		t.Fatal("converted without any coverage, which ExplanationOfBenefit requires")
	}
}

func TestPriorAuthRefusesAClaim(t *testing.T) {
	cr := map[string]any{"resourceType": "ClaimResponse", "use": "claim"}
	if _, err := ToPDexPriorAuth(cr, nil, time.Time{}); err == nil {
		t.Fatal("a claim adjudication was converted as a prior authorisation")
	}
}
