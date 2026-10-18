package fhirserver

import (
	"encoding/json"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
)

func summaryServer(t *testing.T) *Server {
	t.Helper()
	db, err := sqlitedb.OpenSQL(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	put(t, store, `{"resourceType": "Patient", "id": "p1", "name": [{"family": "Evrard", "given": ["Camille"]}], "birthDate": "1962-03-14",
		"identifier": [{"system": "urn:mrn", "value": "M1"}]}`)
	put(t, store, `{"resourceType": "Patient", "id": "p2", "name": [{"family": "Other"}]}`)
	put(t, store, `{"resourceType": "Practitioner", "id": "dr1", "name": [{"family": "Okafor"}]}`)
	put(t, store, `{"resourceType": "Condition", "id": "c1", "subject": {"reference": "Patient/p1"},
		"clinicalStatus": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": "active"}]},
		"code": {"coding": [{"system": "http://snomed.info/sct", "code": "44054006", "display": "Type 2 diabetes mellitus"}]}}`)
	put(t, store, `{"resourceType": "Condition", "id": "c2", "subject": {"reference": "Patient/p1"},
		"clinicalStatus": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": "resolved"}]},
		"code": {"text": "Appendicitis"}}`)
	put(t, store, `{"resourceType": "Condition", "id": "c3", "subject": {"reference": "Patient/p1"},
		"verificationStatus": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/condition-ver-status", "code": "entered-in-error"}]},
		"code": {"text": "Recorded by mistake"}}`)
	put(t, store, `{"resourceType": "Immunization", "id": "i1", "status": "completed", "patient": {"reference": "Patient/p1"},
		"vaccineCode": {"coding": [{"system": "http://hl7.org/fhir/sid/cvx", "code": "08", "display": "Hep B, adolescent or pediatric"}]},
		"occurrenceDateTime": "2026-10-14"}`)
	put(t, store, `{"resourceType": "Observation", "id": "o1", "status": "final", "subject": {"reference": "Patient/p1"},
		"category": [{"coding": [{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "laboratory"}]}],
		"code": {"text": "HbA1c"}, "valueQuantity": {"value": 7.1, "unit": "%"},
		"performer": [{"reference": "Practitioner/dr1"}]}`)
	put(t, store, `{"resourceType": "Condition", "id": "other", "subject": {"reference": "Patient/p2"}, "code": {"text": "Not theirs"}}`)

	srv := NewServer(store, "http://example.test/fhir", slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Auth = OpenAuth{}
	return srv
}

type summaryBundle struct {
	ResourceType string `json:"resourceType"`
	Type         string `json:"type"`
	Identifier   struct{ System, Value string }
	Meta         struct{ Profile []string }
	Entry        []struct {
		FullURL  string         `json:"fullUrl"`
		Resource map[string]any `json:"resource"`
	} `json:"entry"`
}

func getSummary(t *testing.T, srv *Server, path string) (int, summaryBundle, string) {
	t.Helper()
	rec := do(t, srv.Handler(), http.MethodGet, path, nil)
	var b summaryBundle
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return rec.Code, b, rec.Body.String()
}

func sectionByCode(t *testing.T, comp map[string]any, code string) map[string]any {
	t.Helper()
	for _, s := range comp["section"].([]any) {
		sm := s.(map[string]any)
		if codeOf(sm["code"]) == code {
			return sm
		}
	}
	return nil
}

func TestTheSummaryIsAnIPSDocumentWithTheCompositionFirst(t *testing.T) {
	srv := summaryServer(t)
	code, b, body := getSummary(t, srv, "/Patient/p1/$summary")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if b.Type != "document" || b.Identifier.Value == "" || len(b.Meta.Profile) == 0 || b.Meta.Profile[0] != ipsProfile {
		t.Fatalf("not an IPS document bundle: %+v", b)
	}
	if b.Entry[0].Resource["resourceType"] != "Composition" {
		t.Fatalf("the first entry is %v", b.Entry[0].Resource["resourceType"])
	}
	for _, e := range b.Entry {
		if !strings.HasPrefix(e.FullURL, "http://example.test/fhir/") {
			t.Errorf("fullUrl %q is not absolute", e.FullURL)
		}
	}

	comp := b.Entry[0].Resource
	if codeOf(comp["type"]) != "60591-5" {
		t.Errorf("composition type %v", comp["type"])
	}

	// Active problem in Problems, resolved one in past illness, the mistaken one nowhere.
	problems := sectionByCode(t, comp, "11450-4")
	past := sectionByCode(t, comp, "11348-0")
	if problems == nil || past == nil {
		t.Fatal("problems or past illness section missing")
	}
	if !strings.Contains(asJSON(problems), "Condition/c1") || strings.Contains(asJSON(problems), "Condition/c2") {
		t.Errorf("problems: %s", asJSON(problems))
	}
	if !strings.Contains(asJSON(past), "Condition/c2") {
		t.Errorf("past illness: %s", asJSON(past))
	}
	if strings.Contains(body, "Recorded by mistake") || strings.Contains(body, "Not theirs") {
		t.Error("an entered-in-error record, or another patient's, is in the summary")
	}

	// The required sections with nothing in them say so, with the coded reason, rather than reading as "none".
	allergies := sectionByCode(t, comp, "48765-2")
	if allergies == nil || codeOf(allergies["emptyReason"]) != "unavailable" ||
		!strings.Contains(asJSON(allergies), "does not mean there is none") {
		t.Errorf("empty allergies section: %s", asJSON(allergies))
	}
	if sectionByCode(t, comp, "10160-0") == nil {
		t.Error("the required medication section is missing")
	}

	// The narrative a person reads names the facts.
	if !strings.Contains(asJSON(sectionByCode(t, comp, "30954-2")), "HbA1c: 7.1 %") {
		t.Errorf("results narrative: %s", asJSON(sectionByCode(t, comp, "30954-2")))
	}

	// What entries point at is included, so the document reads on its own; and the author resolves.
	types := map[string]bool{}
	for _, e := range b.Entry {
		types[e.Resource["resourceType"].(string)+"/"+e.Resource["id"].(string)] = true
	}
	for _, want := range []string{"Patient/p1", "Practitioner/dr1", "Device/" + summaryAuthorID, "Immunization/i1"} {
		if !types[want] {
			t.Errorf("the bundle lacks %s", want)
		}
	}
}

func TestTheSummaryCanBeAskedForByIdentifier(t *testing.T) {
	srv := summaryServer(t)
	code, b, body := getSummary(t, srv, "/Patient/$summary?identifier=urn:mrn|M1")
	if code != http.StatusOK || b.Type != "document" {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, _ := getSummary(t, srv, "/Patient/$summary?identifier=urn:mrn|NOPE"); code != http.StatusNotFound {
		t.Errorf("an unknown identifier answered %d", code)
	}
}

func TestTheSummaryRefusesWhatItDoesNotDo(t *testing.T) {
	srv := summaryServer(t)
	for path, want := range map[string]int{
		"/Patient/p1/$summary?profile=http://example.org/other": http.StatusBadRequest,
		"/Patient/p1/$summary?_since=2020-01-01T00:00:00Z":      http.StatusBadRequest,
		"/Patient/nobody/$summary":                              http.StatusNotFound,
		"/Patient/$summary":                                     http.StatusBadRequest,
	} {
		if code, _, body := getSummary(t, srv, path); code != want {
			t.Errorf("%s answered %d, want %d: %.200s", path, code, want, body)
		}
	}
}

func TestAPatientScopedTokenCannotSummariseAnotherPatient(t *testing.T) {
	srv := summaryServer(t)
	_, err := srv.Store.Summary(t.Context(), "p2", srv.BaseURL, &Caller{Patient: "p1"}, time.Now())
	if err == nil {
		// The store refuses too; the handler's 403 is the first line, this is the second.
		t.Fatal("a token for p1 got a summary of p2")
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
