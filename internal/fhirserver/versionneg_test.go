package fhirserver

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

func newTestServerWithVersion(t *testing.T, version fhir.Version) (*Server, http.Handler) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, version)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(store, "http://example.test/fhir",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Auth = OpenAuth{}

	return srv, srv.Handler()
}

// TestVersionNegotiation_DefaultIsR4 proves that without a fhirVersion in Accept,
// the response uses R4 field names when the store is configured for R4.
func TestVersionNegotiation_DefaultIsR4(t *testing.T) {
	_, h := newTestServerWithVersion(t, fhir.R4)

	// Create a MedicationRequest with an R4 shape.
	mr := `{
		"resourceType": "MedicationRequest",
		"status": "active",
		"intent": "order",
		"medicationCodeableConcept": {
			"coding": [{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "860975", "display": "amoxicillin 250 MG"}],
			"text": "amoxicillin 250 MG"
		},
		"subject": {"reference": "Patient/p1"}
	}`
	rec := do(t, h, http.MethodPost, "/MedicationRequest", mr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Read it back with default Accept (no fhirVersion).
	rec = do(t, h, http.MethodGet, "/MedicationRequest/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Default R4: medicationCodeableConcept must be present.
	if !strings.Contains(body, "medicationCodeableConcept") {
		t.Errorf("default R4 response should contain medicationCodeableConcept:\n%s", body)
	}
	// Must NOT have bare "medication" key (R5 shape).
	if strings.Contains(body, `"medication"`) && !strings.Contains(body, "medicationCodeableConcept") {
		t.Errorf("default R4 response should not have bare medication key:\n%s", body)
	}
}

// TestVersionNegotiation_R5TransformsMedication proves that requesting fhirVersion=5.0.0
// transforms MedicationRequest.medication[x] to the R5 CodeableReference shape.
func TestVersionNegotiation_R5TransformsMedication(t *testing.T) {
	_, h := newTestServerWithVersion(t, fhir.R4)

	mr := `{
		"resourceType": "MedicationRequest",
		"status": "active",
		"intent": "order",
		"medicationCodeableConcept": {
			"coding": [{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "860975", "display": "amoxicillin 250 MG"}],
			"text": "amoxicillin 250 MG"
		},
		"subject": {"reference": "Patient/p1"}
	}`
	rec := do(t, h, http.MethodPost, "/MedicationRequest", mr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Read it back requesting R5.
	req := httptest.NewRequest(http.MethodGet, "/MedicationRequest/"+id, nil)
	req.Header.Set("Accept", "application/fhir+json; fhirVersion=5.0.0")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()

	// R5: should have "medication" as CodeableReference, not medicationCodeableConcept.
	if strings.Contains(body, "medicationCodeableConcept") {
		t.Errorf("R5 response should not have medicationCodeableConcept:\n%s", body)
	}
	if !strings.Contains(body, `"medication"`) {
		t.Errorf("R5 response should have \"medication\" key:\n%s", body)
	}

	// Verify it's a CodeableReference with concept inside.
	var tree map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &tree)
	med, ok := tree["medication"].(map[string]any)
	if !ok {
		t.Fatalf("medication is not a map: %T", tree["medication"])
	}
	if _, ok := med["concept"]; !ok {
		t.Errorf("medication should have concept field: %v", med)
	}
}

// TestVersionNegotiation_R5TransformsProcedure proves that Procedure.performed becomes
// occurrence in R5.
func TestVersionNegotiation_R5TransformsProcedure(t *testing.T) {
	_, h := newTestServerWithVersion(t, fhir.R4)

	proc := `{
		"resourceType": "Procedure",
		"status": "completed",
		"code": {"text": "appendectomy"},
		"subject": {"reference": "Patient/p1"},
		"performedDateTime": "2026-08-22T14:32:00Z"
	}`
	rec := do(t, h, http.MethodPost, "/Procedure", proc)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Read as R5.
	req := httptest.NewRequest(http.MethodGet, "/Procedure/"+id, nil)
	req.Header.Set("Accept", "application/fhir+json; fhirVersion=5.0.0")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()

	if !strings.Contains(body, "occurrenceDateTime") {
		t.Errorf("R5 response should have occurrenceDateTime:\n%s", body)
	}
	if strings.Contains(body, "performedDateTime") {
		t.Errorf("R5 response should not have performedDateTime:\n%s", body)
	}
}

// TestVersionNegotiation_CapabilityReflectsVersion ensures the capability statement
// advertises the version the client requested.
func TestVersionNegotiation_CapabilityReflectsVersion(t *testing.T) {
	_, h := newTestServerWithVersion(t, fhir.R4)

	// Request metadata with R4.
	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	req.Header.Set("Accept", "application/fhir+json; fhirVersion=4.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metadata = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["fhirVersion"] != string(fhir.R4) {
		t.Errorf("fhirVersion = %v, want %s", body["fhirVersion"], fhir.R4)
	}

	// Request metadata with R5.
	req2 := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	req2.Header.Set("Accept", "application/fhir+json; fhirVersion=5.0.0")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("metadata = %d: %s", rec2.Code, rec2.Body.String())
	}
	var body2 map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &body2)
	if body2["fhirVersion"] != string(fhir.R5) {
		t.Errorf("fhirVersion = %v, want %s", body2["fhirVersion"], fhir.R5)
	}
}

// Ensure do helper compiles properly with the unused import guard.
var _ = bytes.Compare
