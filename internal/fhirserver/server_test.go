package fhirserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, fhir.R5)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(store, "http://example.test/fhir",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Open, so these tests exercise FHIR behaviour rather than authentication - which has its own file.
	//
	// Set explicitly rather than relied on as a default. Every one of these tests failed when authentication was
	// added, which is the safe default doing its job: the server refuses when nobody has said who may in.
	srv.Auth = OpenAuth{}

	return srv, srv.Handler()
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		var raw []byte
		switch v := body.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			var err error
			raw, err = json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/fhir+json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func tree(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func samplePatientJSON(mrn string) string {
	return `{
		"resourceType": "Patient",
		"identifier": [{"system": "http://example.org/mrn", "value": "` + mrn + `",
			"type": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MR"}]}}],
		"name": [{"family": "Doe", "given": ["Jane"], "use": "official"}],
		"gender": "female",
		"birthDate": "1980-01-01"
	}`
}

func TestCapabilityStatement(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodGet, "/metadata", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentType {
		t.Errorf("content type = %q, want %q", ct, contentType)
	}

	body := tree(t, rec)
	if body["resourceType"] != "CapabilityStatement" {
		t.Errorf("resourceType = %v", body["resourceType"])
	}
	if body["fhirVersion"] != string(fhir.R5) {
		t.Errorf("fhirVersion = %v, want %v", body["fhirVersion"], fhir.R5)
	}

	// The statement must say what is missing. One that overstates the server is
	// worse than none, because a client trusts it.
	rest := body["rest"].([]any)[0].(map[string]any)
	doc, _ := rest["documentation"].(string)
	for _, want := range []string{"Not implemented", "history", "chained search"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the documentation does not mention %q: %q", want, doc)
		}
	}

	// And the advertised search parameters must be the ones actually implemented.
	resources := rest["resource"].([]any)
	for _, r := range resources {
		res := r.(map[string]any)
		resourceType := res["type"].(string)
		advertised := map[string]bool{}
		for _, p := range res["searchParam"].([]any) {
			advertised[p.(map[string]any)["name"].(string)] = true
		}
		for _, actual := range SearchParams[resourceType] {
			if !advertised[actual] {
				t.Errorf("%s implements %q but does not advertise it", resourceType, actual)
			}
		}
		if len(advertised) != len(SearchParams[resourceType]) {
			t.Errorf("%s advertises %d parameters but implements %d",
				resourceType, len(advertised), len(SearchParams[resourceType]))
		}
	}
}

func TestCreateReadUpdateDelete(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	created := tree(t, rec)
	id := created["id"].(string)
	if id == "" {
		t.Fatal("no id was assigned")
	}
	if rec.Header().Get("Location") == "" {
		t.Error("no Location header on create")
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag, so a client cannot detect a later change")
	}

	// Meta must be stamped, or a stored resource cannot say when it changed.
	meta := created["meta"].(map[string]any)
	if meta["versionId"] != "1" {
		t.Errorf("versionId = %v, want 1", meta["versionId"])
	}
	if meta["lastUpdated"] == "" {
		t.Error("lastUpdated was not set")
	}

	rec = do(t, h, http.MethodGet, "/Patient/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec.Code, rec.Body.String())
	}
	if tree(t, rec)["id"] != id {
		t.Error("read returned a different resource")
	}

	// An update bumps the version rather than silently overwriting.
	updated := strings.Replace(samplePatientJSON("MRN1"), `"female"`, `"male"`, 1)
	rec = do(t, h, http.MethodPut, "/Patient/"+id, updated)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	body := tree(t, rec)
	if body["gender"] != "male" {
		t.Errorf("gender = %v after update", body["gender"])
	}
	if body["meta"].(map[string]any)["versionId"] != "2" {
		t.Errorf("versionId = %v after update, want 2", body["meta"].(map[string]any)["versionId"])
	}

	rec = do(t, h, http.MethodDelete, "/Patient/"+id, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}

	// A deleted resource is 410, not 404. The client resending it needs to know
	// the difference between "gone" and "never existed".
	rec = do(t, h, http.MethodGet, "/Patient/"+id, nil)
	if rec.Code != http.StatusGone {
		t.Errorf("read after delete = %d, want 410", rec.Code)
	}

	// Deleting again is not an error: the end state the client wanted holds.
	rec = do(t, h, http.MethodDelete, "/Patient/"+id, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("second delete = %d, want 204", rec.Code)
	}
}

func TestCreateIsIdempotentOnIdentifier(t *testing.T) {
	// Posting the same patient twice must not produce two patients, because a
	// retrying feed is normal and a registry full of duplicates is not recoverable
	// without manual merging.
	_, h := newTestServer(t)

	first := tree(t, do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1")))
	second := tree(t, do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1")))

	if first["id"] != second["id"] {
		t.Errorf("the same patient got two ids: %v and %v", first["id"], second["id"])
	}

	rec := do(t, h, http.MethodGet, "/Patient", nil)
	total := tree(t, rec)["total"].(float64)
	if total != 1 {
		t.Errorf("total = %v after posting the same patient twice, want 1", total)
	}
}

func TestIDMismatchRefused(t *testing.T) {
	// Guessing which id the client meant is how a resource is written under the
	// wrong identity.
	_, h := newTestServer(t)

	body := `{"resourceType":"Patient","id":"different","name":[{"family":"Doe"}]}`
	rec := do(t, h, http.MethodPut, "/Patient/expected", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a mismatched id = %d, want 400", rec.Code)
	}
}

func TestWrongTypeForEndpointRefused(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/Observation", samplePatientJSON("MRN1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("posting a Patient to /Observation = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Patient") {
		t.Error("the error does not say what was posted")
	}
}

func TestInvalidResourceRejectedWithOperationOutcome(t *testing.T) {
	// A FHIR server that accepts anything is convenient until somebody queries the
	// data and finds half of it unusable.
	_, h := newTestServer(t)

	bad := `{"resourceType":"Patient","gender":"yes","name":[{"family":"Doe"}]}`
	rec := do(t, h, http.MethodPost, "/Patient", bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an invalid resource = %d, want 400", rec.Code)
	}

	body := tree(t, rec)
	if body["resourceType"] != "OperationOutcome" {
		t.Errorf("resourceType = %v, want OperationOutcome", body["resourceType"])
	}
	issues := body["issue"].([]any)
	if len(issues) == 0 {
		t.Fatal("no issues")
	}
	first := issues[0].(map[string]any)
	if first["severity"] != "error" {
		t.Errorf("severity = %v", first["severity"])
	}
	if first["expression"] == nil {
		t.Error("no expression, so a client cannot locate the problem")
	}
}

func TestValidateOperationDoesNotStore(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/Patient/$validate", samplePatientJSON("MRN1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("$validate on a valid resource = %d: %s", rec.Code, rec.Body.String())
	}
	if tree(t, rec)["resourceType"] != "OperationOutcome" {
		t.Error("$validate did not return an OperationOutcome")
	}

	// Nothing should have been stored.
	rec = do(t, h, http.MethodGet, "/Patient", nil)
	if total := tree(t, rec)["total"].(float64); total != 0 {
		t.Errorf("$validate stored the resource: total = %v", total)
	}

	rec = do(t, h, http.MethodPost, "/Patient/$validate",
		`{"resourceType":"Patient","gender":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("$validate on an invalid resource = %d, want 400", rec.Code)
	}
}

func TestSearchByIdentifier(t *testing.T) {
	_, h := newTestServer(t)

	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN2"))

	rec := do(t, h, http.MethodGet, "/Patient?identifier=http://example.org/mrn|MRN1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	if body["resourceType"] != "Bundle" || body["type"] != "searchset" {
		t.Errorf("not a searchset: %v %v", body["resourceType"], body["type"])
	}
	if total := body["total"].(float64); total != 1 {
		t.Fatalf("total = %v, want 1", total)
	}

	entry := body["entry"].([]any)[0].(map[string]any)
	if entry["search"].(map[string]any)["mode"] != "match" {
		t.Error("the entry does not say it is a match")
	}
	if entry["fullUrl"] == "" {
		t.Error("no fullUrl on the entry")
	}

	// A bare value matches without the system, and a wrong system matches nothing.
	if total := tree(t, do(t, h, http.MethodGet, "/Patient?identifier=MRN1", nil))["total"].(float64); total != 1 {
		t.Errorf("bare identifier search total = %v, want 1", total)
	}
	if total := tree(t, do(t, h, http.MethodGet, "/Patient?identifier=http://wrong|MRN1", nil))["total"].(float64); total != 0 {
		t.Errorf("wrong system total = %v, want 0", total)
	}
}

func TestSearchByNameIsPrefixMatch(t *testing.T) {
	_, h := newTestServer(t)

	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))

	// FHIR string search is a prefix match, and it is case insensitive.
	for _, query := range []string{"family=Doe", "family=doe", "family=Do", "given=Jane", "name=jan"} {
		rec := do(t, h, http.MethodGet, "/Patient?"+query, nil)
		if total := tree(t, rec)["total"].(float64); total != 1 {
			t.Errorf("search %q total = %v, want 1", query, total)
		}
	}
	if total := tree(t, do(t, h, http.MethodGet, "/Patient?family=Smith", nil))["total"].(float64); total != 0 {
		t.Error("a non-matching name returned results")
	}
}

func TestSearchCombinesParametersWithAnd(t *testing.T) {
	_, h := newTestServer(t)

	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	do(t, h, http.MethodPost, "/Patient",
		`{"resourceType":"Patient","identifier":[{"system":"http://example.org/mrn","value":"MRN9"}],
		  "name":[{"family":"Smith"}],"gender":"male"}`)

	// Two different parameters AND together.
	rec := do(t, h, http.MethodGet, "/Patient?family=Doe&gender=female", nil)
	if total := tree(t, rec)["total"].(float64); total != 1 {
		t.Errorf("AND search total = %v, want 1", total)
	}
	rec = do(t, h, http.MethodGet, "/Patient?family=Doe&gender=male", nil)
	if total := tree(t, rec)["total"].(float64); total != 0 {
		t.Errorf("contradictory AND search total = %v, want 0", total)
	}

	// Repeating one parameter ORs its values.
	rec = do(t, h, http.MethodGet, "/Patient?family=Doe&family=Smith", nil)
	if total := tree(t, rec)["total"].(float64); total != 2 {
		t.Errorf("OR search total = %v, want 2", total)
	}
}

func TestUnsupportedSearchParameterIsRefused(t *testing.T) {
	// A server that ignores an unknown parameter returns the wrong resources and
	// the client cannot tell. In clinical data that means acting on somebody
	// else's results.
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodGet, "/Patient?favourite-colour=blue", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown search parameter = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "favourite-colour") {
		t.Error("the error does not name the offending parameter")
	}
	if !strings.Contains(rec.Body.String(), "supported") {
		t.Error("the error does not say what is supported")
	}

	// A modifier that is not implemented is refused, because dropping one silently changes the query. :exact, :contains
	// and :missing are implemented now; anything else must still be refused rather than ignored.
	rec = do(t, h, http.MethodGet, "/Patient?family:phonetic=Doe", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unimplemented search modifier = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ":exact") {
		t.Error("the refusal does not name the modifiers that are supported, " +
			"which is the one thing the client needs in order to fix the query")
	}

	// And a supported modifier is accepted, so the refusal above is about the modifier being unknown rather than about
	// modifiers in general.
	if rec := do(t, h, http.MethodGet, "/Patient?family:exact=Doe", nil); rec.Code != http.StatusOK {
		t.Errorf("family:exact = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownResourceType(t *testing.T) {
	_, h := newTestServer(t)

	if rec := do(t, h, http.MethodGet, "/Flumox", nil); rec.Code != http.StatusNotFound {
		t.Errorf("search on an unknown type = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/Flumox/1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("read on an unknown type = %d, want 404", rec.Code)
	}
}

func TestSearchPaging(t *testing.T) {
	_, h := newTestServer(t)

	for i := 0; i < 12; i++ {
		do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN"+string(rune('A'+i))))
	}

	rec := do(t, h, http.MethodGet, "/Patient?_count=5", nil)
	body := tree(t, rec)
	if total := body["total"].(float64); total != 12 {
		t.Errorf("total = %v, want 12", total)
	}
	if entries := body["entry"].([]any); len(entries) != 5 {
		t.Errorf("got %d entries, want 5", len(entries))
	}

	// A next link only when there is a next page, so a client can page without
	// guessing.
	var hasNext bool
	for _, l := range body["link"].([]any) {
		if l.(map[string]any)["relation"] == "next" {
			hasNext = true
		}
	}
	if !hasNext {
		t.Error("no next link on a paged result")
	}

	rec = do(t, h, http.MethodGet, "/Patient?_count=5&_offset=10", nil)
	if entries := tree(t, rec)["entry"].([]any); len(entries) != 2 {
		t.Errorf("last page has %d entries, want 2", len(entries))
	}

	// _count is bounded, because an unbounded one asks the server to load
	// everything into memory.
	rec = do(t, h, http.MethodGet, "/Patient?_count=999999", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("large _count = %d", rec.Code)
	}
}

func TestTransactionBundle(t *testing.T) {
	_, h := newTestServer(t)

	bundle := `{
		"resourceType": "Bundle",
		"type": "transaction",
		"entry": [
			{"fullUrl": "urn:uuid:p1",
			 "resource": ` + samplePatientJSON("MRN1") + `,
			 "request": {"method": "PUT", "url": "Patient/p1"}},
			{"fullUrl": "urn:uuid:e1",
			 "resource": {"resourceType":"Encounter","id":"e1","status":"in-progress",
				"class":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v3-ActCode","code":"IMP"}]}],
				"subject":{"reference":"Patient/p1"}},
			 "request": {"method": "PUT", "url": "Encounter/e1"}}
		]
	}`

	rec := do(t, h, http.MethodPost, "/", bundle)
	if rec.Code != http.StatusOK {
		t.Fatalf("transaction = %d: %s", rec.Code, rec.Body.String())
	}

	body := tree(t, rec)
	if body["type"] != "transaction-response" {
		t.Errorf("type = %v, want transaction-response", body["type"])
	}
	entries := body["entry"].([]any)
	if len(entries) != 2 {
		t.Fatalf("got %d response entries, want 2", len(entries))
	}
	for i, e := range entries {
		resp := e.(map[string]any)["response"].(map[string]any)
		if !strings.HasPrefix(resp["status"].(string), "20") {
			t.Errorf("entry %d status = %v", i, resp["status"])
		}
		if resp["location"] == "" {
			t.Errorf("entry %d has no location", i)
		}
	}

	// Both resources must actually be there.
	if rec := do(t, h, http.MethodGet, "/Patient/p1", nil); rec.Code != http.StatusOK {
		t.Errorf("the patient from the transaction is missing: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/Encounter/e1", nil); rec.Code != http.StatusOK {
		t.Errorf("the encounter from the transaction is missing: %d", rec.Code)
	}
}

func TestTransactionRejectsBeforeWritingAnything(t *testing.T) {
	// Validating everything first is what stops a late failure leaving earlier
	// entries applied.
	_, h := newTestServer(t)

	bundle := `{
		"resourceType": "Bundle",
		"type": "transaction",
		"entry": [
			{"resource": ` + samplePatientJSON("MRN1") + `,
			 "request": {"method": "PUT", "url": "Patient/good"}},
			{"resource": {"resourceType":"Patient","id":"bad","gender":"nonsense"},
			 "request": {"method": "PUT", "url": "Patient/bad"}}
		]
	}`

	rec := do(t, h, http.MethodPost, "/", bundle)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a transaction with an invalid entry = %d, want 400", rec.Code)
	}

	// The valid entry must not have been applied.
	if rec := do(t, h, http.MethodGet, "/Patient/good", nil); rec.Code == http.StatusOK {
		t.Error("a failed transaction still wrote its first entry")
	}

	// And the outcome says which entry was at fault.
	if !strings.Contains(rec.Body.String(), "entry[1]") {
		t.Logf("outcome: %s", rec.Body.String())
	}
}

func TestNonBundlePostToRootRefused(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/", samplePatientJSON("MRN1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("posting a Patient to the base URL = %d, want 400", rec.Code)
	}
}

func TestReadOnlyMode(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.ReadOnly = true
	h := srv.Handler()

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/Patient"},
		{http.MethodPut, "/Patient/x"},
		{http.MethodDelete, "/Patient/x"},
		{http.MethodPost, "/"},
	} {
		rec := do(t, h, c.method, c.path, samplePatientJSON("MRN1"))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s in read-only mode = %d, want 405", c.method, c.path, rec.Code)
		}
	}

	// Reads still work.
	if rec := do(t, h, http.MethodGet, "/metadata", nil); rec.Code != http.StatusOK {
		t.Errorf("metadata in read-only mode = %d", rec.Code)
	}
}

func TestWrongContentTypeRefused(t *testing.T) {
	_, h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/Patient",
		strings.NewReader(samplePatientJSON("MRN1")))
	req.Header.Set("Content-Type", "application/xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("XML content type = %d, want 415", rec.Code)
	}
}

func TestEmptyBodyRefused(t *testing.T) {
	_, h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/Patient", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/fhir+json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", rec.Code)
	}
}

func TestObservationSearchByCodeAndPatient(t *testing.T) {
	_, h := newTestServer(t)

	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	patientID := tree(t, do(t, h, http.MethodGet, "/Patient?identifier=MRN1", nil))["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)["id"].(string)

	obs := `{"resourceType":"Observation","status":"final",
		"code":{"coding":[{"system":"http://loinc.org","code":"718-7","display":"Hemoglobin"}]},
		"subject":{"reference":"Patient/` + patientID + `"},
		"valueQuantity":{"value":13.5,"unit":"g/dL","system":"http://unitsofmeasure.org","code":"g/dL"},
		"effectiveDateTime":"2026-08-18T12:00:00Z"}`

	rec := do(t, h, http.MethodPost, "/Observation", obs)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create observation = %d: %s", rec.Code, rec.Body.String())
	}

	// Finding results for a patient is the query that makes the store useful.
	rec = do(t, h, http.MethodGet, "/Observation?patient="+patientID, nil)
	if total := tree(t, rec)["total"].(float64); total != 1 {
		t.Errorf("search by patient total = %v, want 1", total)
	}

	rec = do(t, h, http.MethodGet, "/Observation?code=http://loinc.org|718-7", nil)
	if total := tree(t, rec)["total"].(float64); total != 1 {
		t.Errorf("search by LOINC code total = %v, want 1", total)
	}

	rec = do(t, h, http.MethodGet,
		"/Observation?patient="+patientID+"&code=http://loinc.org|718-7&status=final", nil)
	if total := tree(t, rec)["total"].(float64); total != 1 {
		t.Errorf("combined search total = %v, want 1", total)
	}

	rec = do(t, h, http.MethodGet, "/Observation?code=http://loinc.org|9999-9", nil)
	if total := tree(t, rec)["total"].(float64); total != 0 {
		t.Errorf("search for a different code total = %v, want 0", total)
	}
}

func TestSearchIndexRebuiltOnUpdate(t *testing.T) {
	// Patching an index means working out which values disappeared, and getting
	// that wrong leaves a resource findable by a value it no longer has.
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	id := tree(t, rec)["id"].(string)

	renamed := `{"resourceType":"Patient",
		"identifier":[{"system":"http://example.org/mrn","value":"MRN1"}],
		"name":[{"family":"Smith","given":["Jane"]}],"gender":"female"}`
	if rec := do(t, h, http.MethodPut, "/Patient/"+id, renamed); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	if total := tree(t, do(t, h, http.MethodGet, "/Patient?family=Smith", nil))["total"].(float64); total != 1 {
		t.Error("the new name is not searchable")
	}
	if total := tree(t, do(t, h, http.MethodGet, "/Patient?family=Doe", nil))["total"].(float64); total != 0 {
		t.Error("the old name is still searchable after being changed")
	}
}

func TestDeletedResourceIsNotSearchable(t *testing.T) {
	_, h := newTestServer(t)

	rec := do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	id := tree(t, rec)["id"].(string)

	do(t, h, http.MethodDelete, "/Patient/"+id, nil)

	if total := tree(t, do(t, h, http.MethodGet, "/Patient?identifier=MRN1", nil))["total"].(float64); total != 0 {
		t.Error("a deleted resource is still returned by search")
	}
}

func TestCountsForDashboard(t *testing.T) {
	srv, h := newTestServer(t)

	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN1"))
	do(t, h, http.MethodPost, "/Patient", samplePatientJSON("MRN2"))

	counts, err := srv.Store.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["Patient"] != 2 {
		t.Errorf("Patient count = %d, want 2", counts["Patient"])
	}
}

// TestTheConfiguredPageSizeAppliesOnlyWhenTheClientDidNotAsk covers the live fhir.pageSize setting.
//
// Two properties, and the second is the one worth having. A configured default must be used when a client gives no
// _count, and it must be ignored when the client gives one - a client that asks for ten and silently receives fifty
// cannot page, because its next offset is wrong and it will skip or repeat records without any error to notice.
func TestTheConfiguredPageSizeAppliesOnlyWhenTheClientDidNotAsk(t *testing.T) {
	srv, h := newTestServer(t)
	srv.DefaultCountFn = func() int { return 7 }

	// Enough resources that a page boundary is visible.
	for i := 0; i < 12; i++ {
		rec := do(t, h, http.MethodPost, "/Patient", json.RawMessage(samplePatientJSON(fmt.Sprintf("MRN%02d", i))))
		if rec.Code != http.StatusCreated {
			t.Fatalf("creating patient %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	entries := func(t *testing.T, path string) int {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		got, ok := tree(t, rec)["entry"].([]any)
		if !ok {
			return 0
		}
		return len(got)
	}

	t.Run("no _count uses the configured default", func(t *testing.T) {
		if got := entries(t, "/Patient"); got != 7 {
			t.Errorf("returned %d entries, want the configured 7", got)
		}
	})

	t.Run("an explicit _count wins over the configured default", func(t *testing.T) {
		if got := entries(t, "/Patient?_count=3"); got != 3 {
			t.Errorf("returned %d entries for _count=3, want 3 - a server setting is overriding what the "+
				"client asked for, which breaks its paging silently", got)
		}
	})
}
