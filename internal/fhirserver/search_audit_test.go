package fhirserver

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// newAuditServer is a helper that creates a test server for audit tests.
func newAuditServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(store, "http://example.test/fhir",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Auth = OpenAuth{}

	return srv, srv.Handler()
}

// ----- DEFECT 1: Token search system| (system with any code) -----
//
// Per FHIR spec, a token search for "http://loinc.org|" means "any code in the
// system http://loinc.org". The current code treats it as system="http://loinc.org"
// AND value="" which matches nothing.
func TestTokenSearch_SystemPipeAnyCode(t *testing.T) {
	_, h := newAuditServer(t)

	// Create an observation with a code in http://loinc.org system.
	obs := `{
		"resourceType": "Observation",
		"status": "final",
		"code": {
			"coding": [{"system": "http://loinc.org", "code": "85354-9"}]
		},
		"subject": {"reference": "Patient/p1"}
	}`
	rec := do(t, h, http.MethodPost, "/Observation", obs)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create obs = %d: %s", rec.Code, rec.Body.String())
	}

	// Search with system| (any code in system) - FHIR spec says this must match.
	rec = do(t, h, http.MethodGet, "/Observation?code=http://loinc.org|", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("system| search (any code in system) returned total=%v, want 1; body:\n%s",
			total, rec.Body.String())
	}
}

// ----- DEFECT 2: isDateParam is incomplete -----
//
// Date parameters beyond "date", "birthdate", and "_lastUpdated" are not treated
// as dates, so prefix operators (ge, le, gt, lt) are compared as literal strings.
// A search for authoredon=ge2026-01-01 looks for the literal value "ge2026-01-01"
// instead of applying >= comparison.
func TestDateSearch_AuthoredOnWithPrefix(t *testing.T) {
	_, h := newAuditServer(t)

	// Create a patient first so we can reference them.
	pat := `{"resourceType": "Patient", "name": [{"family": "Test"}]}`
	rec := do(t, h, http.MethodPost, "/Patient", pat)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create patient = %d: %s", rec.Code, rec.Body.String())
	}

	// Create a MedicationRequest with an authored date.
	mr := `{
		"resourceType": "MedicationRequest",
		"status": "active",
		"intent": "order",
		"authoredOn": "2026-06-15",
		"subject": {"reference": "Patient/p1"}
	}`
	rec = do(t, h, http.MethodPost, "/MedicationRequest", mr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create medreq = %d: %s", rec.Code, rec.Body.String())
	}

	// Search with ge prefix on authoredon - should find it.
	rec = do(t, h, http.MethodGet, "/MedicationRequest?authoredon=ge2026-01-01", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("authoredon=ge2026-01-01 returned total=%v, want 1; the ge prefix was not applied as a date comparison",
			total)
	}
}

// ----- DEFECT 3: isReferenceParam is incomplete -----
//
// Reference parameters beyond "patient", "subject", and "encounter" are not
// treated as references, so a type-qualified search like
// practitioner=Practitioner/123 is compared literally instead of stripping the
// type. The index stores bare IDs, so the search finds nothing.
func TestReferenceSearch_PractitionerQualified(t *testing.T) {
	_, h := newAuditServer(t)

	// Create a practitioner.
	prac := `{"resourceType": "Practitioner", "name": [{"family": "Smith"}]}`
	rec := do(t, h, http.MethodPost, "/Practitioner", prac)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create practitioner = %d: %s", rec.Code, rec.Body.String())
	}
	pracID := tree(t, rec)["id"].(string)

	// Create a PractitionerRole pointing at that practitioner.
	role := `{
		"resourceType": "PractitionerRole",
		"practitioner": {"reference": "Practitioner/` + pracID + `"}
	}`
	rec = do(t, h, http.MethodPost, "/PractitionerRole", role)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create role = %d: %s", rec.Code, rec.Body.String())
	}

	// Search with type-qualified reference (how real clients send it).
	rec = do(t, h, http.MethodGet, "/PractitionerRole?practitioner=Practitioner/"+pracID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("practitioner=Practitioner/%s returned total=%v, want 1; type-qualified reference not handled for this param",
			pracID, total)
	}
}

// ----- DEFECT 4: Partial date search -----
//
// A search for a year like "2026" should match all dates in that year. Without
// partial date handling, a search for date=2026 against "2026-06-15T10:00:00Z"
// would use LIKE '2026%' only if isDateParam returns true. For the 3 recognized
// date params this works, but verify it explicitly.
func TestDateSearch_PartialYearMatch(t *testing.T) {
	_, h := newAuditServer(t)

	// Create a patient first.
	pat := `{"resourceType": "Patient", "name": [{"family": "Tester"}]}`
	rec := do(t, h, http.MethodPost, "/Patient", pat)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create patient = %d: %s", rec.Code, rec.Body.String())
	}

	// Create an observation with a full date.
	obs := `{
		"resourceType": "Observation",
		"status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "12345-6"}]},
		"subject": {"reference": "Patient/p1"},
		"effectiveDateTime": "2026-08-15T10:30:00Z"
	}`
	rec = do(t, h, http.MethodPost, "/Observation", obs)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create obs = %d: %s", rec.Code, rec.Body.String())
	}

	// Search for partial year - should match.
	rec = do(t, h, http.MethodGet, "/Observation?date=2026", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("date=2026 (partial year) returned total=%v, want 1", total)
	}

	// Search for partial month - should match.
	rec = do(t, h, http.MethodGet, "/Observation?date=2026-08", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body = tree(t, rec)
	total, _ = body["total"].(float64)
	if total != 1 {
		t.Errorf("date=2026-08 (partial month) returned total=%v, want 1", total)
	}
}

// ----- Verify: Unknown search parameter is an error -----
func TestUnknownSearchParam_IsError(t *testing.T) {
	_, h := newAuditServer(t)

	rec := do(t, h, http.MethodGet, "/Patient?bogus=123", nil)
	if rec.Code == http.StatusOK {
		t.Errorf("unknown search parameter should return an error, got 200")
	}
}

// ----- Verify: _count is bounded -----
func TestCountIsBounded(t *testing.T) {
	ctx := context.Background()
	q, err := ParseSearch("Patient", map[string][]string{"_count": {"99999"}})
	if err != nil {
		t.Fatal(err)
	}
	if q.Count > MaxCount {
		t.Errorf("_count=%d exceeds MaxCount=%d", q.Count, MaxCount)
	}
	_ = ctx
}

// ----- Verify: Deterministic ordering (resource_id as tiebreaker) -----
func TestSearchOrderIsDeterministic(t *testing.T) {
	_, h := newAuditServer(t)

	// Create several patients with the same lastUpdated (as close as possible).
	for i := 0; i < 5; i++ {
		pat := `{"resourceType": "Patient", "name": [{"family": "Sorted"}]}`
		rec := do(t, h, http.MethodPost, "/Patient", pat)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create patient %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	// Search twice, verify same order.
	rec1 := do(t, h, http.MethodGet, "/Patient?name=Sorted", nil)
	rec2 := do(t, h, http.MethodGet, "/Patient?name=Sorted", nil)

	if rec1.Body.String() != rec2.Body.String() {
		t.Error("search results are not deterministically ordered between identical queries")
	}
}

// ----- Verify: Unknown modifier is an error -----
func TestUnknownModifier_IsError(t *testing.T) {
	_, h := newAuditServer(t)

	rec := do(t, h, http.MethodGet, "/Patient?name:text=Smith", nil)
	if rec.Code == http.StatusOK {
		t.Errorf("unknown modifier :text should return an error, got 200")
	}
}

// ----- Verify: String search default is starts-with (not exact, not contains) -----
func TestStringSearch_DefaultIsStartsWith(t *testing.T) {
	_, h := newAuditServer(t)

	// Create two patients.
	rec := do(t, h, http.MethodPost, "/Patient", `{"resourceType": "Patient", "name": [{"family": "Smithson"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/Patient", `{"resourceType": "Patient", "name": [{"family": "McSmith"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	// Search for "Smith" should find "Smithson" (starts-with) but NOT "McSmith" (contains).
	rec = do(t, h, http.MethodGet, "/Patient?family=Smith", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("family=Smith should match Smithson (starts-with) but not McSmith (contains); got total=%v", total)
	}
}

// ----- Verify: _sort present -----
func TestSortParameterIsAccepted(t *testing.T) {
	_, h := newAuditServer(t)

	rec := do(t, h, http.MethodGet, "/Patient?_sort=-_lastUpdated", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("_sort should be accepted, got %d", rec.Code)
	}
}

// ----- DEFECT: Token search pipe-code (|code, system-less) handling -----
//
// Per FHIR, |code (starts with pipe) means "match this code regardless of system,
// but only for entries with no system". This is different from bare "code" which
// matches any system or no system. Let's verify.
func TestTokenSearch_PipeCodeSystemless(t *testing.T) {
	_, h := newAuditServer(t)

	// Create observation with a code that has a system.
	obs1 := `{
		"resourceType": "Observation",
		"status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "12345-6"}]},
		"subject": {"reference": "Patient/p1"}
	}`
	rec := do(t, h, http.MethodPost, "/Observation", obs1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create obs1 = %d: %s", rec.Code, rec.Body.String())
	}

	// Create observation with a code that has no system.
	obs2 := `{
		"resourceType": "Observation",
		"status": "final",
		"code": {"coding": [{"code": "local-code"}]},
		"subject": {"reference": "Patient/p1"}
	}`
	rec = do(t, h, http.MethodPost, "/Observation", obs2)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create obs2 = %d: %s", rec.Code, rec.Body.String())
	}

	// Search with |local-code (system-less) should find only the second one.
	rec = do(t, h, http.MethodGet, "/Observation?code=|local-code", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	total, _ := body["total"].(float64)
	if total != 1 {
		t.Errorf("|local-code (system-less) search returned total=%v, want 1", total)
	}
}
