package fhirserver

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/formulary"
)

// The Da Vinci US Drug Formulary 2.1.0 searches, against the sample formulary as the server publishes it.
func TestDrugFormularySearches(t *testing.T) {
	srv, h := newTestServer(t)
	f, err := os.Open("../../examples/formulary/formulary.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := formulary.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range file.Resources("2026-10-14T12:00:00Z") {
		raw, _ := json.Marshal(r)
		put(t, srv.Store, string(raw))
	}
	const lovenox = `"id": "FormularyItem-STD2026-854236"`
	for _, c := range []struct{ query, want, not string }{
		{"/Basic?code=formulary-item&formulary=InsurancePlan/FormularySTD2026&drug-tier=preferred-brand", lovenox, `"FormularyItem-STD2026-860975"`},
		{"/Basic?subject:MedicationKnowledge.code=854236", lovenox, ""},
		{"/Basic?status=active&pharmacy-benefit-type=3-month-in-mail", `"FormularyItem-STD2026-860975"`, lovenox},
		// A period is matched by overlap (ge, le); eq would ask for a period inside one day.
		{"/Basic?period=ge2026-06-01", lovenox, ""},
		{"/Basic?period=le2025-06-01", "", lovenox},
		{"/Basic?_id=FormularyItem-STD2026-854236&_include=Basic:subject", `"id": "FormularyDrug-854236"`, ""},
		{"/MedicationKnowledge?code=http://www.nlm.nih.gov/research/umls/rxnorm|1180930", `"id": "FormularyDrug-854236"`, ""},
		{"/InsurancePlan?formulary-coverage=InsurancePlan/FormularySTD2026", `"id": "PayerInsurancePlanGOLD2026"`, ""},
		{"/InsurancePlan?type=DRUGPOL&period=ge2026-03-01", `"id": "FormularySTD2026"`, ""},
		{"/InsurancePlan?coverage-type=DRUGPOL&_include=InsurancePlan:formulary-coverage", `"id": "FormularySTD2026"`, ""},
		{"/InsurancePlan?identifier=GOLD2026", `"id": "PayerInsurancePlanGOLD2026"`, ""},
	} {
		rec := do(t, h, http.MethodGet, c.query, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || (c.want != "" && !strings.Contains(body, c.want)) || (c.not != "" && strings.Contains(body, c.not)) {
			t.Errorf("%s: %d, want %q and not %q in %.300s", c.query, rec.Code, c.want, c.not, body)
		}
	}
}
