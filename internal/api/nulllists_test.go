package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// Whether any response sends null where its contract promises a list.
//
// This exists because of a crash, and the crash is worth recording. The FHIR lab converted a message,
// the operator clicked Decisions, and the view died with "Cannot read properties of null (reading
// 'length')". The cause was a nil Go slice: encoding/json writes nil as null, the TypeScript interface
// declared a non-nullable array, and so the contract was a lie in exactly one circumstance - when there
// was nothing to report.
//
// That is the part worth dwelling on. The trigger was a message that converted perfectly. Every
// deliberately broken input produced findings, populated the slice and worked. The clean input, which is
// what somebody demonstrating the product would paste, was the only one that broke. An audit found four
// more of the same shape, each triggered by its own success case: a replay that found no differences, a
// document whose narrative agreed with its entries, a channel list read from a healthy directory.
//
// So this checks the property rather than the six instances: no response body may contain a JSON null
// anywhere. Perfuse omits absent scalars with omitempty rather than sending null, so a null in a
// response is either a nil slice or a nil map, and both are this bug.

// findNulls returns the JSON paths of every null in a document.
func findNulls(v any, path string, out *[]string) {
	switch t := v.(type) {
	case nil:
		*out = append(*out, path)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // so a failure names the same field every run
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			findNulls(t[k], child, out)
		}
	case []any:
		for i, item := range t {
			findNulls(item, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// assertNoNulls fails if the body contains a null anywhere.
func assertNoNulls(t *testing.T, what string, body []byte) {
	t.Helper()

	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s: response is not JSON: %v", what, err)
	}

	var nulls []string
	findNulls(doc, "", &nulls)
	if len(nulls) > 0 {
		t.Errorf("%s sent null at %s.\n\nA nil Go slice or map marshals to null, but the client "+
			"contract declares a list, so the client dereferences it and crashes. Initialise it "+
			"empty at construction - []T{} - rather than leaving it nil.",
			what, strings.Join(nulls, ", "))
	}
}

// The FHIR lab, on a message that converts cleanly. This is the exact case that crashed: notes and
// findings are both empty precisely because nothing went wrong.
func TestFHIRConversionSendsEmptyListsNotNull(t *testing.T) {
	h := newHarness(t)

	// Deliberately a well-formed message. A broken one populates the lists and passes for the wrong
	// reason, which is how this defect survived.
	clean := "MSH|^~\\&|LAB|HOSP|EHR|HOSP|20260825120000||ADT^A01|MSG00001|P|2.5\r" +
		"EVN|A01|20260825120000\r" +
		"PID|1||12345^^^HOSP^MR||DOE^JOHN^A||19800101|M\r" +
		"PV1|1|I|WARD^1^01\r"

	rec := h.do("admin", "POST", "/api/inspect/fhir", map[string]any{"message": clean})
	if rec.Code != http.StatusOK {
		t.Fatalf("convert returned %d: %s", rec.Code, rec.Body.String())
	}

	assertNoNulls(t, "POST /api/inspect/fhir", rec.Body.Bytes())

	// And specifically the two fields the interface dereferences without a guard, so a regression is
	// named rather than merely counted.
	var body struct {
		Notes      *json.RawMessage `json:"notes"`
		Validation struct {
			Findings *json.RawMessage `json:"findings"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]*json.RawMessage{
		"notes":               body.Notes,
		"validation.findings": body.Validation.Findings,
	} {
		if raw == nil {
			t.Errorf("%s is absent; the interface reads it as an array", name)
			continue
		}
		if string(*raw) == "null" {
			t.Errorf("%s is null, want []", name)
		}
	}
}

// The status response, which the dashboard polls continuously.
func TestStatusSendsEmptyListsNotNull(t *testing.T) {
	h := newHarness(t)

	rec := h.do("admin", "GET", "/api/status", nil)
	if rec.Code == http.StatusServiceUnavailable {
		// This harness builds a server without an engine, so status has nothing to report. The sweep
		// below covers the endpoint when it is available; skipping is honest rather than passing.
		t.Skip("harness runs without an engine, so there is no status to check")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", rec.Code, rec.Body.String())
	}
	assertNoNulls(t, "GET /api/status", rec.Body.Bytes())
}

// A sweep over the read endpoints that need no input, so an endpoint added later is covered by
// listing it in one place rather than by writing another test.
// TestReadEndpointsSendEmptyListsNotNull sweeps the read endpoints, and now actually reaches them.
//
// # What was wrong with this test
//
// It listed fourteen endpoints and checked seven. The others returned 503 because the harness had no settings store and no engine
// wired, and the loop treated any non-200 as "not a list-bearing success" and skipped - so the summary said PASS while half the
// list went unexamined. A skipped subtest is indistinguishable from a passing one unless somebody reads the verbose output, and
// nobody reads the verbose output of a passing test.
//
// That is how a null reached the settings screen and crashed it. /api/settings/schema was on this list the whole time.
//
// # What it does now
//
// The harness is given a real settings store, so the endpoint answers. Anything that still cannot answer has to be named below with
// a reason, and an unexpected status fails rather than skips. The point is that silence is no longer an option: either the endpoint
// is checked, or somebody has written down why it cannot be.
func TestReadEndpointsSendEmptyListsNotNull(t *testing.T) {
	h := newHarness(t)

	// Endpoints that cannot answer in this harness, each with the reason. Kept deliberately short: every entry here is coverage
	// this test is not providing, which is exactly the situation that let the settings crash through.
	cannotAnswer := map[string]string{
		"/api/status":   "needs a running engine",
		"/api/queue":    "needs a running engine",
		"/api/metrics":  "needs a running engine",
		"/api/messages": "needs a message store with retention configured",
		"/api/alerts":   "needs an alert evaluator",
	}

	paths := []string{
		"/api/channels",
		"/api/status",
		"/api/alerts",
		"/api/queue",
		"/api/metrics",
		"/api/audit",
		"/api/messages",
		"/api/certificates",
		"/api/fhir/versions",
		"/api/document/types",
		"/api/settings/schema",
		"/api/branding",
		"/api/passkeys",
		"/api/tokens",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			rec := h.do("admin", "GET", p, nil)

			if reason, known := cannotAnswer[p]; known {
				if rec.Code == http.StatusOK {
					t.Errorf("%s answered after all - remove it from cannotAnswer so it is checked (reason given: %s)", p, reason)
				}

				t.Skipf("not exercised here: %s", reason)
			}

			// No silent skip. An endpoint that stops answering is either a routing mistake or a stale entry in the list, and
			// both are worth a failure - the previous version of this treated a 404 as a reason to look away.
			if rec.Code != http.StatusOK {
				t.Fatalf("returned %d, want 200. If it genuinely cannot answer in this harness, add it to cannotAnswer with the reason", rec.Code)
			}

			assertNoNulls(t, "GET "+p, rec.Body.Bytes())
		})
	}
}
