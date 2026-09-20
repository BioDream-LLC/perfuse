package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// medsDoc builds a document with one medications section, so a test can vary only what matters.
func medsDoc(root, mrn, family string, meds ...string) string {
	var entries strings.Builder
	for _, m := range meds {
		parts := strings.SplitN(m, "|", 3)
		code, name, status := parts[0], parts[1], "active"
		if len(parts) == 3 {
			status = parts[2]
		}
		entries.WriteString(fmt.Sprintf(`
      <entry><substanceAdministration classCode="SBADM" moodCode="EVN">
        <statusCode code="%s"/>
        <consumable><manufacturedProduct><manufacturedMaterial>
          <code code="%s" codeSystem="2.16.840.1.113883.6.88" displayName="%s"/>
        </manufacturedMaterial></manufacturedProduct></consumable>
      </substanceAdministration></entry>`, status, code, name))
	}

	var idElem string
	if root != "" {
		idElem = fmt.Sprintf(`<id root="%s" extension="%s"/>`, root, mrn)
	}

	return fmt.Sprintf(`<?xml version="1.0"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole>%s
    <patient><name><given>Ada</given><family>%s</family></name>
    <administrativeGenderCode code="F"/><birthTime value="19151210"/></patient>
  </patientRole></recordTarget>
  <custodian><assignedCustodian><representedCustodianOrganization>
    <name>Example Hospital</name>
  </representedCustodianOrganization></assignedCustodian></custodian>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.1.1"/>
      <code code="10160-0" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Medications</title>
      <text>See coded entries.</text>%s
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`, idElem, family, entries.String())
}

type reconcileOut struct {
	Report struct {
		Added     []map[string]any `json:"added"`
		Removed   []map[string]any `json:"removed"`
		Changed   []map[string]any `json:"changed"`
		Unchanged []map[string]any `json:"unchanged"`
		Conflicts []map[string]any `json:"conflicts"`
	} `json:"report"`
	Patient struct {
		SamePatient bool   `json:"samePatient"`
		Explanation string `json:"explanation"`
	} `json:"patient"`
}

func reconcile(t *testing.T, h *harness, before, after string) reconcileOut {
	t.Helper()
	res := h.do("viewer", http.MethodPost, "/api/document/reconcile",
		map[string]any{"before": before, "after": after})
	if res.Code != http.StatusOK {
		t.Fatalf("reconciling returned %d: %s", res.Code, res.Body.String())
	}
	var out reconcileOut
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not readable: %v", err)
	}
	return out
}

const rootA = "2.16.840.1.113883.19.5"

// A medication stopped in one document and still active in the other.
//
// This is the case the whole feature exists for. Both documents are conformant, both are internally consistent, and
// they contradict each other about whether the patient is currently taking a drug. Whichever one the receiving
// clinician happens to read decides what they believe.
func TestAMedicationActiveInOneDocumentAndStoppedInTheOtherIsFlagged(t *testing.T) {
	h := newHarness(t)

	before := medsDoc(rootA, "MRN-1", "Lovelace", "855332|warfarin sodium 5 MG|active")
	after := medsDoc(rootA, "MRN-1", "Lovelace", "855332|warfarin sodium 5 MG|completed")

	out := reconcile(t, h, before, after)

	if len(out.Report.Conflicts) == 0 {
		t.Fatalf("a drug active in one document and completed in the other was not flagged as a conflict; "+
			"added=%d removed=%d changed=%d unchanged=%d",
			len(out.Report.Added), len(out.Report.Removed), len(out.Report.Changed), len(out.Report.Unchanged))
	}
}

// Additions and removals must be the right way round, or the report is confidently backwards.
func TestAdditionsAndRemovalsAreNotInverted(t *testing.T) {
	h := newHarness(t)

	before := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	after := medsDoc(rootA, "MRN-1", "Lovelace",
		"860975|metformin 500 MG|active", "855332|warfarin sodium 5 MG|active")

	out := reconcile(t, h, before, after)

	if len(out.Report.Added) != 1 {
		t.Fatalf("want 1 addition, got %d (removed %d)", len(out.Report.Added), len(out.Report.Removed))
	}
	if got := fmt.Sprint(out.Report.Added[0]["medication"]); !strings.Contains(strings.ToLower(got), "warfarin") {
		t.Errorf("the added medication is reported as %q, want the warfarin", got)
	}
	if len(out.Report.Removed) != 0 {
		t.Errorf("nothing was removed, but %d removals were reported", len(out.Report.Removed))
	}
	if len(out.Report.Unchanged) != 1 {
		t.Errorf("the metformin should be unchanged, got %d unchanged", len(out.Report.Unchanged))
	}
}

// Two different patients must not be reconciled silently.
//
// Comparing two people's medication lists produces a report full of additions and removals that looks exactly like
// one patient whose therapy changed completely. It is an easy mistake to make with two files on a desktop.
func TestTwoDifferentPatientsAreNotSilentlyReconciled(t *testing.T) {
	h := newHarness(t)

	before := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	after := medsDoc(rootA, "MRN-2", "Lovelace", "855332|warfarin sodium 5 MG|active")

	out := reconcile(t, h, before, after)

	if out.Patient.SamePatient {
		t.Error("two different identifiers under the same authority were reported as the same patient")
	}
	if strings.TrimSpace(out.Patient.Explanation) == "" {
		t.Error("no explanation was given, so nothing tells the reader why to distrust the report")
	}
}

// The same number from two different authorities is two different patients.
//
// This is the comparison a confident tool gets wrong. An identifier is a root plus an extension: the root names who
// issued the number. "MRN 12345" from two hospitals is a coincidence, not a match, and treating it as one is the
// commonest cause of a wrongly merged record.
func TestTheSameNumberFromDifferentAuthoritiesIsNotAMatch(t *testing.T) {
	h := newHarness(t)

	before := medsDoc("2.16.840.1.113883.19.5", "12345", "Lovelace", "860975|metformin 500 MG|active")
	after := medsDoc("2.16.840.1.113883.19.9", "12345", "Lovelace", "860975|metformin 500 MG|active")

	out := reconcile(t, h, before, after)

	if out.Patient.SamePatient {
		t.Error("the same number from two different assigning authorities was treated as the same patient")
	}
	if !strings.Contains(strings.ToLower(out.Patient.Explanation), "authority") {
		t.Errorf("the explanation does not mention the assigning authority: %q", out.Patient.Explanation)
	}
}

// A matching identifier under a shared authority is a match, or the warning becomes noise nobody reads.
func TestAMatchingIdentifierUnderASharedAuthorityIsAMatch(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	out := reconcile(t, h, doc, doc)

	if !out.Patient.SamePatient {
		t.Errorf("two documents for the same patient were not recognised: %q", out.Patient.Explanation)
	}
}

// Empty results must be empty arrays, never null.
//
// A nil slice marshals to null and the interface reads a length from it. "No changes" and "the field is missing"
// are different answers and only one is true.
func TestReconcileResultsAreArraysWhenEmpty(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	res := h.do("viewer", http.MethodPost, "/api/document/reconcile", map[string]any{"before": doc, "after": doc})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	body := res.Body.String()
	for _, field := range []string{"added", "removed", "changed", "conflicts"} {
		if strings.Contains(body, `"`+field+`":null`) {
			t.Errorf("%s is null rather than an empty array", field)
		}
	}
}

// One document alone is a refusal that says which one is missing.
func TestReconcileRefusesASingleDocument(t *testing.T) {
	h := newHarness(t)

	doc := medsDoc(rootA, "MRN-1", "Lovelace", "860975|metformin 500 MG|active")
	res := h.do("viewer", http.MethodPost, "/api/document/reconcile", map[string]any{"after": doc})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "before") {
		t.Errorf("the refusal does not say what is missing: %s", res.Body.String())
	}
}
