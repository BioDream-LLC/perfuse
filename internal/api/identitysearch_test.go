package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
)

// An HL7 v2 admission with identity where a real feed puts it.
const findAdmission = "MSH|^~\\&|EPIC|HOSP|PERFUSE|DEST|20260921120000||ADT^A01^ADT_A01|FIND001|P|2.5.1\r" +
	"PID|1||MRN0012345^^^HOSP^MR||FIXTURETON^BRAVO||19700101|M\r"

// A FHIR Patient for the same person, so one search has to cross formats.
const findPatient = `{"resourceType":"Patient","identifier":[{"value":"MRN0012345"}],` +
	`"name":[{"family":"Fixtureton","given":["Bravo"]}],"birthDate":"1970-01-01"}`

// findHarness is a message harness with identity indexing switched on.
//
// A failure here is a failure of the endpoint rather than of indexing, so the switch is set
// explicitly. Left at its zero value the store would index nothing and every test below would
// pass by finding nothing, which is the shape of a test suite that tests nothing.
func findHarness(t *testing.T) *harness {
	t.Helper()

	h := newMessageHarness(t)
	if h.server.Runtime == nil || h.server.Runtime.Messages == nil {
		t.Fatal("this harness has no message store")
	}
	h.server.Runtime.Messages.StorePayloads = true
	h.server.Runtime.Messages.IndexIdentity = true
	return h
}

func recordPayload(t *testing.T, h *harness, channel, controlID, raw string) {
	t.Helper()

	if _, err := h.server.Runtime.Messages.Record(context.Background(), &msgstore.Message{
		Channel:    channel,
		ControlID:  controlID,
		ReceivedAt: time.Now().UTC(),
		Outcome:    msgstore.Delivered,
		Size:       len(raw),
		Raw:        []byte(raw),
	}); err != nil {
		t.Fatal(err)
	}
}

type findResponse struct {
	Matches []struct {
		Message      msgstore.Message `json:"message"`
		MatchedKind  string           `json:"matchedKind"`
		MatchedValue string           `json:"matchedValue"`
	} `json:"matches"`
	Total   int      `json:"total"`
	Indexed bool     `json:"indexed"`
	Kinds   []string `json:"kinds"`
}

func find(t *testing.T, h *harness, body any) findResponse {
	t.Helper()

	rec := h.do("admin", http.MethodPost, "/api/messages/find", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/messages/find returned %d: %s", rec.Code, rec.Body.String())
	}
	return decodeBody[findResponse](t, rec)
}

// The endpoint finds a message by an identifier inside it.
func TestFindingAMessageByMRNThroughTheAPI(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	got := find(t, h, map[string]any{"term": "MRN0012345"})

	if got.Total != 1 || len(got.Matches) != 1 {
		t.Fatalf("total=%d matches=%d, want 1 and 1", got.Total, len(got.Matches))
	}
	if got.Matches[0].Message.ControlID != "FIND001" {
		t.Errorf("found control ID %q, want FIND001", got.Matches[0].Message.ControlID)
	}
	// The reason, so a page of similar messages says which field was hit.
	if got.Matches[0].MatchedKind != "patient_id" {
		t.Errorf("matchedKind = %q, want patient_id", got.Matches[0].MatchedKind)
	}
	if !got.Indexed {
		t.Error("indexed is false although the store has indexing on")
	}
	if len(got.Kinds) == 0 {
		t.Error("kinds is empty, so the console has nothing to build its filter from")
	}
}

// One search, two formats. This is the reason the feature exists.
func TestOneAPISearchCrossesFormats(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)
	recordPayload(t, h, "fhir-api", "FIND002", findPatient)

	got := find(t, h, map[string]any{"term": "MRN0012345"})

	if got.Total != 2 {
		t.Fatalf("total=%d, want 2: an HL7 v2 admission and a FHIR Patient for the same MRN",
			got.Total)
	}
	channels := map[string]bool{}
	for _, m := range got.Matches {
		channels[m.Message.Channel] = true
	}
	for _, want := range []string{"adt", "fhir-api"} {
		if !channels[want] {
			t.Errorf("nothing from %q; got %v", want, channels)
		}
	}
}

// Searching by name, typed the way a person types it rather than the way HL7 writes it.
func TestFindingByNameAsTyped(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	for _, term := range []string{"Fixtureton Bravo", "fixtureton, bravo", "FIXTURETON BRAVO"} {
		if got := find(t, h, map[string]any{"term": term}); got.Total != 1 {
			t.Errorf("searching %q found %d, want 1", term, got.Total)
		}
	}
}

// An unknown kind is refused rather than quietly matching nothing.
//
// A typo in a field name would otherwise return an empty result, and an empty result to a
// patient search reads as "this server never saw that patient" - a clinical conclusion drawn
// from a misspelled parameter.
func TestAnUnknownIdentityKindIsRefused(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	rec := h.do("admin", http.MethodPost, "/api/messages/find",
		map[string]any{"term": "MRN0012345", "kind": "patientid"})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// A term that normalises to nothing is refused.
func TestAnEmptyTermIsRefusedByTheAPI(t *testing.T) {
	h := findHarness(t)

	for _, term := range []string{"", "  ", "!!"} {
		rec := h.do("admin", http.MethodPost, "/api/messages/find", map[string]any{"term": term})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("term %q returned %d, want 400", term, rec.Code)
		}
	}
}

// A malformed timestamp is refused rather than dropped.
//
// Ignoring it would widen the search silently, and the result would be read as covering the
// range that was asked for.
func TestABadTimestampIsRefused(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	for _, field := range []string{"since", "until"} {
		rec := h.do("admin", http.MethodPost, "/api/messages/find",
			map[string]any{"term": "MRN0012345", field: "last Tuesday"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s=%q returned %d, want 400", field, "last Tuesday", rec.Code)
		}
	}
}

// Searching for a patient must be written to the audit trail.
//
// This is a lookup of a person by name or medical record number, which is the access an audit
// asks about by name: who looked up whom, and when.
func TestAPatientSearchIsAudited(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	find(t, h, map[string]any{"term": "MRN0012345"})

	entries, err := h.store.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.Action != "message.identity.search" {
			continue
		}
		// The term itself, because an entry saying only that somebody ran a patient search
		// cannot answer which patient was looked up, which is the question being asked.
		if e.Target != "MRN0012345" {
			t.Errorf("audit target = %q, want the term that was searched", e.Target)
		}
		if e.Username == "" {
			t.Error("the audit entry names no user")
		}
		return
	}
	t.Errorf("no message.identity.search entry in the audit trail; got %d entries", len(entries))
}

// A search that finds nothing must be audited too.
//
// Somebody working down a list of names to see which ones this server has heard of is doing
// exactly what an audit trail exists to reveal, and recording only the searches that matched
// would hide it entirely.
func TestASearchThatFoundNothingIsStillAudited(t *testing.T) {
	h := findHarness(t)

	find(t, h, map[string]any{"term": "MRN9999999"})

	entries, err := h.store.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "message.identity.search" && e.Target == "MRN9999999" {
			return
		}
	}
	t.Error("a patient search that matched nothing left no audit entry, so working through " +
		"a list of names to see which this server knows would leave no trace")
}

// A viewer can search; the endpoint is a read.
func TestAViewerCanSearchByIdentity(t *testing.T) {
	h := findHarness(t)
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	rec := h.do("viewer", http.MethodPost, "/api/messages/find",
		map[string]any{"term": "MRN0012345"})
	if rec.Code != http.StatusOK {
		t.Errorf("a viewer got %d: %s", rec.Code, rec.Body.String())
	}
}

// The response must say when nothing is indexed, so an empty result is not read as an answer.
func TestTheResponseSaysWhenNothingIsIndexed(t *testing.T) {
	h := newMessageHarness(t)
	h.server.Runtime.Messages.StorePayloads = true
	h.server.Runtime.Messages.IndexIdentity = false
	recordPayload(t, h, "adt", "FIND001", findAdmission)

	got := find(t, h, map[string]any{"term": "MRN0012345"})

	if got.Total != 0 {
		t.Errorf("total=%d, want 0", got.Total)
	}
	if got.Indexed {
		t.Error("indexed is true although indexing is off, so an empty result is " +
			"indistinguishable from a patient this server never saw")
	}
}

// Matches must never be null in JSON.
func TestMatchesIsAnEmptyArrayNotNull(t *testing.T) {
	h := findHarness(t)

	rec := h.do("admin", http.MethodPost, "/api/messages/find", map[string]any{"term": "nobody"})
	if rec.Code != http.StatusOK {
		t.Fatalf("returned %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"matches":[]`) {
		t.Errorf("matches is not an empty array in %s", body)
	}
}

var _ = store.RoleViewer
