package fhirserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// contextAuth grants scopes and a launch context.
type contextAuth struct {
	scopes  []string
	patient string
}

func (a contextAuth) Authenticate(*http.Request) (*Caller, error) {
	return &Caller{Name: "patient-app", Scopes: a.scopes, Write: true, Patient: a.patient}, nil
}

func (a contextAuth) Describe() string { return "a token with a launch context" }

// observationJSON is an Observation for a named patient.
func observationJSON(patient, code string) string {
	return `{
		"resourceType": "Observation",
		"status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "` + code + `"}]},
		"subject": {"reference": "Patient/` + patient + `"}
	}`
}

// contextServer builds a server holding records for two patients, then restricts the caller to one.
//
// Seeded through an unrestricted caller and then narrowed, because seeding through the restricted one could not create the
// other patient's records - and it is the other patient's records that every one of these tests is about.
func contextServer(t *testing.T, patient string) (*Server, http.Handler) {
	t.Helper()

	srv, h := newTestServer(t)
	srv.Auth = OpenAuth{}

	for _, p := range []string{"alice", "bob"} {
		rec := do(t, h, http.MethodPut, "/Patient/"+p, json.RawMessage(samplePatientJSON("MRN-"+p)))
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("seeding patient %s: %d %s", p, rec.Code, rec.Body.String())
		}

		rec = do(t, h, http.MethodPut, "/Observation/obs-"+p,
			json.RawMessage(observationJSON(p, "1234-5")))
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("seeding observation for %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}

	srv.Auth = contextAuth{scopes: []string{"patient/*.read", "patient/*.write"}, patient: patient}

	return srv, h
}

// TestALaunchContextLimitsReadsToOnePatient is the property the whole feature exists for.
//
// Without it, an app granted patient/Observation.read can read every observation in the hospital - which is the database. The
// scopes say what kinds of resource may be touched; the context says whose, and enforcing only the first is the mistake that
// makes SMART support worse than none.
func TestALaunchContextLimitsReadsToOnePatient(t *testing.T) {
	_, h := contextServer(t, "alice")

	t.Run("the context patient can be read", func(t *testing.T) {
		if rec := do(t, h, http.MethodGet, "/Patient/alice", nil); rec.Code != http.StatusOK {
			t.Errorf("reading the context patient got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("another patient reads as absent", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/Patient/bob", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("reading another patient got %d, want 404: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("another patient's observation reads as absent", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/Observation/obs-bob", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("reading another patient's observation got %d, want 404: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("the refusal is indistinguishable from a genuine miss", func(t *testing.T) {
		// The important one. If the two differ, an app can discover which patients this hospital holds records
		// for by asking for them one at a time - and "does this hospital hold a record for this person" is the
		// question a stalker asks. It needs no read scope for the answer to be useful.
		real := do(t, h, http.MethodGet, "/Observation/obs-does-not-exist", nil)
		other := do(t, h, http.MethodGet, "/Observation/obs-bob", nil)

		if real.Code != other.Code {
			t.Errorf("a genuine miss returns %d and another patient's record returns %d, which is an "+
				"oracle for whether a record exists", real.Code, other.Code)
		}

		// Bodies differ only in the id, so both are normalised before comparing.
		norm := func(s string) string {
			s = strings.ReplaceAll(s, "obs-does-not-exist", "ID")

			return strings.ReplaceAll(s, "obs-bob", "ID")
		}
		if norm(real.Body.String()) != norm(other.Body.String()) {
			t.Errorf("the two refusals differ in wording:\n%s\n%s", real.Body.String(), other.Body.String())
		}
	})
}

// TestALaunchContextNarrowsASearch covers the path that would otherwise return everybody.
func TestALaunchContextNarrowsASearch(t *testing.T) {
	_, h := contextServer(t, "alice")

	entries := func(t *testing.T, path string) []any {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		got, _ := tree(t, rec)["entry"].([]any)

		return got
	}

	t.Run("an unqualified search returns only the context patient", func(t *testing.T) {
		got := entries(t, "/Observation")
		if len(got) != 1 {
			t.Fatalf("an unqualified Observation search returned %d entries, want 1 - a search with no "+
				"patient parameter is returning every patient", len(got))
		}
		if body := do(t, h, http.MethodGet, "/Observation", nil).Body.String(); strings.Contains(body, "bob") {
			t.Errorf("another patient's record is in the results:\n%s", body)
		}
	})

	t.Run("a patient search returns only the context patient", func(t *testing.T) {
		if got := entries(t, "/Patient"); len(got) != 1 {
			t.Errorf("a Patient search returned %d entries, want 1", len(got))
		}
	})

	t.Run("naming the context patient is allowed", func(t *testing.T) {
		if got := entries(t, "/Observation?patient=alice"); len(got) != 1 {
			t.Errorf("naming the context patient returned %d entries, want 1", len(got))
		}
	})

	t.Run("naming another patient is refused rather than narrowed", func(t *testing.T) {
		// Refused, not silently narrowed and not silently emptied. Narrowing would return alice's records as
		// though they were bob's, which is the worst of the three; returning nothing would read as "bob has no
		// observations", which is a false clinical statement.
		rec := do(t, h, http.MethodGet, "/Observation?patient=bob", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("searching for another patient got %d, want 403: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a reference form is understood", func(t *testing.T) {
		// Patient/alice and alice are the same patient. A comparison on the raw string would refuse the first
		// form, which every real client sends.
		if got := entries(t, "/Observation?subject=Patient/alice"); len(got) != 1 {
			t.Errorf("a reference-form patient returned %d entries, want 1", len(got))
		}
	})

	t.Run("a type that cannot be narrowed is refused", func(t *testing.T) {
		// Practitioner has no patient parameter, so it cannot be limited to one patient. Serving it would hand
		// a patient-context token every referring physician in the hospital, which reads as harmless until
		// somebody notices it is a staff directory.
		rec := do(t, h, http.MethodGet, "/Practitioner", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("searching Practitioner with a patient context got %d, want 403: %s",
				rec.Code, rec.Body.String())
		}
	})
}

// TestALaunchContextLimitsWrites covers create, update, delete and bundles.
func TestALaunchContextLimitsWrites(t *testing.T) {
	_, h := contextServer(t, "alice")

	t.Run("writing for the context patient works", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/Observation", json.RawMessage(observationJSON("alice", "9999-1")))
		if rec.Code != http.StatusCreated {
			t.Errorf("writing for the context patient got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("writing for another patient is refused", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/Observation", json.RawMessage(observationJSON("bob", "9999-2")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("writing for another patient got %d, want 403: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a write refusal says why, unlike a read", func(t *testing.T) {
		// The asymmetry is deliberate. A read must not reveal whether somebody else's record exists; a write is
		// the caller's own content, so there is nothing to conceal and a plain message is the only actionable one.
		rec := do(t, h, http.MethodPost, "/Observation", json.RawMessage(observationJSON("bob", "9999-3")))
		if !strings.Contains(rec.Body.String(), "subject") {
			t.Errorf("the write refusal does not explain itself:\n%s", rec.Body.String())
		}
	})

	t.Run("a bundle entry for another patient is refused", func(t *testing.T) {
		bundle := json.RawMessage(`{
			"resourceType": "Bundle",
			"type": "transaction",
			"entry": [
				{"request": {"method": "POST", "url": "/Observation"},
				 "resource": ` + observationJSON("alice", "8888-1") + `},
				{"request": {"method": "POST", "url": "/Observation"},
				 "resource": ` + observationJSON("bob", "8888-2") + `}
			]
		}`)

		rec := do(t, h, http.MethodPost, "/", bundle)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a bundle containing another patient got %d, want 403: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "entry 1") {
			t.Errorf("the refusal does not say which entry:\n%s", rec.Body.String())
		}
	})

	t.Run("deleting another patient's record looks like it was already gone", func(t *testing.T) {
		// 204, matching a genuine already-deleted resource. A distinguishable answer would let an app discover
		// which records exist for other patients by trying to delete them - a worse oracle than the read,
		// because it needs no read scope at all.
		missing := do(t, h, http.MethodDelete, "/Observation/obs-never-existed", nil)
		other := do(t, h, http.MethodDelete, "/Observation/obs-bob", nil)

		if missing.Code != other.Code {
			t.Errorf("deleting something absent returns %d and deleting another patient's record returns "+
				"%d, which is an oracle", missing.Code, other.Code)
		}
	})

	t.Run("and it was not actually deleted", func(t *testing.T) {
		// The response above says nothing happened. It has to also be true.
		srv, h2 := newTestServer(t)
		srv.Auth = OpenAuth{}
		if rec := do(t, h2, http.MethodPut, "/Observation/obs-bob",
			json.RawMessage(observationJSON("bob", "1234-5"))); rec.Code >= 300 {
			t.Fatalf("seeding: %d", rec.Code)
		}
		srv.Auth = contextAuth{scopes: []string{"patient/*.read", "patient/*.write"}, patient: "alice"}

		if rec := do(t, h2, http.MethodDelete, "/Observation/obs-bob", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete returned %d", rec.Code)
		}

		srv.Auth = OpenAuth{}
		if rec := do(t, h2, http.MethodGet, "/Observation/obs-bob", nil); rec.Code != http.StatusOK {
			t.Errorf("another patient's record was actually deleted: %d", rec.Code)
		}
	})
}

// TestAResourceWithNoSubjectIsRefusedRatherThanAllowed pins the direction of the unknown case.
//
// A resource whose subject cannot be determined is refused. The alternative fails open: a resource type this code has not
// been taught about would be readable and writable by every patient-context token, and it would look like it worked.
func TestAResourceWithNoSubjectIsRefusedRatherThanAllowed(t *testing.T) {
	_, h := contextServer(t, "alice")

	// An Observation with no subject at all.
	noSubject := json.RawMessage(`{
		"resourceType": "Observation",
		"status": "final",
		"code": {"coding": [{"system": "http://loinc.org", "code": "7777-1"}]}
	}`)

	rec := do(t, h, http.MethodPost, "/Observation", noSubject)
	if rec.Code != http.StatusForbidden {
		t.Errorf("an Observation with no subject was accepted with a patient context: %d %s - the unknown "+
			"case must fail closed", rec.Code, rec.Body.String())
	}
}

// TestATokenWithNoContextIsUnaffected covers every existing integration.
//
// A Perfuse API token has no launch context, and neither does a SMART token issued without one. Both must keep working exactly
// as before, or upgrading to this build breaks every running FHIR client.
func TestATokenWithNoContextIsUnaffected(t *testing.T) {
	srv, h := contextServer(t, "alice")
	srv.Auth = OpenAuth{}

	for _, path := range []string{"/Patient/bob", "/Observation/obs-bob", "/Practitioner"} {
		if rec := do(t, h, http.MethodGet, path, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s with no launch context got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}

// TestAReferenceSearchMatchesEitherForm covers a bug found while testing launch context, and independent of it.
//
// Every real client sends patient=Patient/123, which is the form the specification uses. The index holds bare ids, and the
// query compared the raw value - so that search matched nothing and returned an empty bundle with a 200.
//
// The status is what makes it serious. An empty result reads as "this patient has no observations", which is a false clinical
// statement delivered as a success, and nothing anywhere reports an error.
func TestAReferenceSearchMatchesEitherForm(t *testing.T) {
	srv, h := newTestServer(t)
	srv.Auth = OpenAuth{}

	if rec := do(t, h, http.MethodPut, "/Patient/alice",
		json.RawMessage(samplePatientJSON("MRN-alice"))); rec.Code >= 300 {
		t.Fatalf("seeding the patient: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, "/Observation/obs-alice",
		json.RawMessage(observationJSON("alice", "1234-5"))); rec.Code >= 300 {
		t.Fatalf("seeding the observation: %d", rec.Code)
	}

	for _, query := range []string{
		"/Observation?patient=alice",
		"/Observation?patient=Patient/alice",
		"/Observation?subject=alice",
		"/Observation?subject=Patient/alice",
		// An absolute reference, which a bundle from another server produces.
		"/Observation?subject=http://other.example.test/fhir/Patient/alice",
		// A versioned reference. The id is the segment before _history, not the version - reading the version as
		// the id would narrow the search to patient "4", which returns nothing and reads as an empty record.
		"/Observation?subject=Patient/alice/_history/1",
	} {
		rec := do(t, h, http.MethodGet, query, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s", query, rec.Code, rec.Body.String())

			continue
		}
		entries, _ := tree(t, rec)["entry"].([]any)
		if len(entries) != 1 {
			t.Errorf("GET %s returned %d entries, want 1 - this form of reference does not match, and the "+
				"empty result reads as the patient having no observations", query, len(entries))
		}
	}
}
