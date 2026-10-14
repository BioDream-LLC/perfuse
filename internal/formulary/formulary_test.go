package formulary

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/fhir"
)

func sample(t *testing.T) *File {
	t.Helper()
	f, err := os.Open("../../examples/formulary/formulary.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestTheSampleFormularyLoads(t *testing.T) {
	f := sample(t)
	fm, ok := f.Formulary("STD2026")
	if !ok || len(fm.Drugs) != 5 {
		t.Fatalf("got %+v", fm)
	}
	d, err := fm.Find(NDC, "00075-0620-01")
	if err != nil || d.RxNorm != "854236" {
		t.Fatalf("by NDC: %v %v", d, err)
	}
	if d2, err := fm.Find(NDC, "0075-0620-01"); err != nil || d2 != d {
		t.Errorf("the ten-digit 4-4-2 form of the same NDC: %v", err)
	}
	if _, err := fm.Find(RxNorm, "999"); err != ErrNotListed {
		t.Errorf("an unlisted drug: %v", err)
	}
	if got := strings.Join(d.Requirements(), "; "); got != "prior authorization; quantity limit of 30 per 30 days" {
		t.Errorf("requirements %q", got)
	}
	if over, why := d.ExceedsLimit(60, 30); !over || !strings.Contains(why, "per 30 days") {
		t.Errorf("60 in 30 days: %v %q", over, why)
	}
	if over, why := d.ExceedsLimit(30, 10); !over || !strings.Contains(why, "a day") {
		t.Errorf("30 in 10 days is 3 a day: %v %q", over, why)
	}
	if over, _ := d.ExceedsLimit(90, 90); over {
		t.Error("90 for 90 days is the 30-per-30 limit in proportion")
	}
	if over, _ := d.ExceedsLimit(30, 30); over {
		t.Error("30 in 30 days is within the limit")
	}
	if over, _ := d.ExceedsLimit(20, 10); over {
		t.Error("20 over 10 days is 2 a day, which is the maximum")
	}
	p, _ := f.Plan("GOLD2026")
	if c, ok := p.Cost("1-month-in-retail", "specialty"); !ok || c.Coinsurance == nil || *c.Coinsurance != 25 {
		t.Errorf("specialty cost %+v", c)
	}
}

func TestAFormularyFileIsCheckedWhole(t *testing.T) {
	_, err := Load(strings.NewReader(`
payer: { name: "" }
plans:
  - { id: "bad id", name: P, formulary: NOPE, costs: [{ benefit: weekly, tier: gold }] }
formularies:
  - id: F1
    name: F
    start: "2026-13-01"
    drugs:
      - { rxnorm: "12a", display: "", tier: cheap }
      - { rxnorm: "5", display: X, tier: generic, ndc: ["1234567890"], not_covered: true, prior_authorization: true, alternatives: ["5"] }
      - { rxnorm: "5", display: Y, tier: generic, group: "6", group_display: G }
default_plan: GHOST
`))
	inv, ok := err.(*Invalid)
	if !ok {
		t.Fatalf("got %v", err)
	}
	all := strings.Join(inv.Problems, "\n")
	for _, want := range []string{"payer.name", "plans[0].id", "names no formulary", "weekly", "gold", "copay or a coinsurance",
		"2026-13-01", "12a", "display is required", "cheap", "1234567890", "nothing to authorize", "own alternative",
		"listed twice", "GHOST"} {
		if !strings.Contains(all, want) {
			t.Errorf("no problem mentions %q:\n%s", want, all)
		}
	}
	if _, err := Load(strings.NewReader("payer: { name: X }\nsurprise: 1\n")); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Errorf("an unknown key: %v", err)
	}
}

// Every resource is one the FHIR model reads back with nothing lost, carrying its Drug Formulary profile.
func TestTheFormularyIsPublishedAsDrugFormularyResources(t *testing.T) {
	res := sample(t).Resources("2026-10-14T12:00:00Z")
	counts := map[string]int{}
	for _, r := range res {
		raw, _ := json.Marshal(r)
		parsed, err := fhir.UnmarshalResource(raw)
		if err != nil {
			t.Fatalf("%s/%s: %v", r["resourceType"], r["id"], err)
		}
		back, _ := fhir.Marshal(parsed, fhir.R4)
		var got map[string]any
		_ = json.Unmarshal(back, &got)
		delete(got, "meta")
		want := map[string]any{}
		_ = json.Unmarshal(raw, &want)
		delete(want, "meta")
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("%s/%s does not survive the model:\n got %s\nwant %s", r["resourceType"], r["id"], g, w)
		}
		counts[r["resourceType"].(string)]++
	}
	if counts["InsurancePlan"] != 2 || counts["MedicationKnowledge"] != 4 || counts["Basic"] != 4 {
		t.Errorf("counts %v", counts)
	}
	for _, r := range res {
		if strings.Contains(r["id"].(string), "1049640") {
			t.Errorf("%s/%s publishes a drug the formulary excludes", r["resourceType"], r["id"])
		}
	}
	if os.Getenv("USDF_OUT") != "" {
		for _, r := range res {
			b, _ := json.MarshalIndent(r, "", " ")
			_ = os.WriteFile(os.Getenv("USDF_OUT")+"/"+r["resourceType"].(string)+"-"+r["id"].(string)+".json", b, 0o600)
		}
	}
}
