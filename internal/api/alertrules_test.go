package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/alerts"
)

// harnessWithRules points the harness at a rules file inside the test directory and returns its path.
func harnessWithRules(t *testing.T, contents string) (*harness, string) {
	t.Helper()

	h := newHarness(t)
	path := filepath.Join(h.dir, "alerts.yaml")

	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.server.SettingsPaths.Alerts = path

	return h, path
}

// seedRule is a starting file. Not an empty list, because the loader treats a file with no rules as a mistake - which is a decision
// worth keeping and one the editor now surfaces rather than tripping over.
const seedRule = "rules:\n  - kind: error-rate\n    threshold: 0.2\n"

func TestAlertRulesAreReadableWithEverythingNeededToEditThem(t *testing.T) {
	h, _ := harnessWithRules(t, "rules:\n  - kind: queue-depth\n    threshold: 200\n    for: 5m\n")

	rec := h.do("admin", "GET", "/api/alerts/rules", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got alertRulesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Rules) != 1 || got.Rules[0].Kind != "queue-depth" {
		t.Fatalf("rules came back as %+v", got.Rules)
	}

	// A duration as something an operator recognises. Sending Go's nanoseconds would make the form do arithmetic on a value it is
	// about to show, which is how 5m ends up on screen as 300000000000.
	if got.Rules[0].For != "5m0s" {
		t.Errorf("for came back as %q, want a readable duration", got.Rules[0].For)
	}

	// The catalogue has to travel with the rules. Without it the form has to keep its own list of kinds, and a list in the browser
	// is a list that will drift from the engine and start offering rules that cannot fire.
	if len(got.Kinds) != len(alerts.KnownKinds) {
		t.Errorf("got %d kinds and the engine knows %d", len(got.Kinds), len(alerts.KnownKinds))
	}
	if !got.Writable {
		t.Error("a server with a rules file reported the rules as not writable")
	}
}

func TestEveryKnownKindCanBeSavedThroughTheAPI(t *testing.T) {
	// The check that makes the editor trustworthy. Every kind the catalogue offers must survive being posted, written to a file and
	// read back by the loader the engine uses - which is the whole journey a rule takes. A kind that is offered and cannot be saved
	// is a form that refuses its own options, and that has happened in this codebase more than once.
	for _, info := range alerts.KindCatalogue() {
		t.Run(string(info.Kind), func(t *testing.T) {
			h, path := harnessWithRules(t, seedRule)

			body := map[string]any{
				"rules": []map[string]any{
					{"kind": string(info.Kind), "threshold": info.Default, "for": "5m", "severity": "warning"},
				},
			}

			rec := h.do("admin", "PUT", "/api/alerts/rules", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
			}

			// Read through the real loader, not by parsing the response. What matters is that the engine can read what was written.
			loaded, err := alerts.LoadFile(path)
			if err != nil {
				t.Fatalf("the file written by the editor does not load: %v", err)
			}
			if len(loaded) != 1 {
				t.Fatalf("the file holds %d rules, want 1", len(loaded))
			}
			if loaded[0].Kind != info.Kind {
				t.Errorf("saved %s and the file holds %s", info.Kind, loaded[0].Kind)
			}
			if loaded[0].Threshold != info.Default {
				t.Errorf("saved threshold %v and the file holds %v", info.Default, loaded[0].Threshold)
			}
		})
	}
}

func TestSavingRulesRefusesAKindTheEngineCannotEvaluate(t *testing.T) {
	h, path := harnessWithRules(t, seedRule)

	body := map[string]any{"rules": []map[string]any{{"kind": "error_rate", "threshold": 0.1}}}

	rec := h.do("admin", "PUT", "/api/alerts/rules", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for a misspelled kind: %s", rec.Code, rec.Body.String())
	}

	// The message has to name the rule and list what is allowed, because the operator's next action is to fix it.
	if body := rec.Body.String(); !strings.Contains(body, "rule 1") || !strings.Contains(body, "error-rate") {
		t.Errorf("the refusal does not say which rule is wrong or what is allowed: %s", body)
	}

	// And nothing may be written. A refusal that has already replaced the file would delete the rules somebody was relying on.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "error_rate") {
		t.Error("the rules were refused and written anyway")
	}
}

func TestSavingRulesRefusesAnUnreadableDuration(t *testing.T) {
	h, _ := harnessWithRules(t, seedRule)

	body := map[string]any{"rules": []map[string]any{{"kind": "queue-depth", "threshold": 10, "for": "five minutes"}}}

	rec := h.do("admin", "PUT", "/api/alerts/rules", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "5m") {
		t.Errorf("the refusal does not show what a length of time looks like: %s", rec.Body.String())
	}
}

func TestSavingRulesOnAServerWithoutARulesFileSaysWhatToDo(t *testing.T) {
	h := newHarness(t)
	h.server.SettingsPaths.Alerts = ""

	rec := h.do("admin", "PUT", "/api/alerts/rules", map[string]any{"rules": []map[string]any{}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
	}

	// "Nowhere to save" is not an actionable message on its own, and this is the one failure here an operator can fix in a minute.
	if !strings.Contains(rec.Body.String(), "-alerts") {
		t.Errorf("the refusal does not say how to give the server a rules file: %s", rec.Body.String())
	}
}

func TestAnEditorMayNotChangeAlertRules(t *testing.T) {
	h, _ := harnessWithRules(t, seedRule)

	if rec := h.do("editor", "PUT", "/api/alerts/rules", map[string]any{"rules": []map[string]any{}}); rec.Code != http.StatusForbidden {
		t.Errorf("an editor changing the alert rules got %d, want 403", rec.Code)
	}

	// Reading is deliberately allowed at every level: knowing what is being watched is part of reading the state of the system.
	if rec := h.do("viewer", "GET", "/api/alerts/rules", nil); rec.Code != http.StatusOK {
		t.Errorf("a viewer reading the alert rules got %d, want 200", rec.Code)
	}
}

func TestRulesSurviveARoundTripUnchanged(t *testing.T) {
	// Reads the rules, saves them straight back, and requires the file to mean the same thing.
	//
	// This is the check that catches an editor quietly dropping a field it does not display. Somebody opens the screen to change one
	// threshold, presses save, and a destination narrowing or a disabled flag they never touched is gone - and the rule now matches
	// every destination, or starts firing again. Nothing on screen would say so.
	original := `rules:
  - kind: queue-depth
    channel: adt
    destination: archive
    threshold: 250
    for: 10m
    severity: critical
  - kind: below-rhythm
    channel: labs
    threshold: 0.75
    disabled: true
`

	h, path := harnessWithRules(t, original)

	rec := h.do("admin", "GET", "/api/alerts/rules", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading: got %d", rec.Code)
	}

	var read alertRulesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}

	if rec := h.do("admin", "PUT", "/api/alerts/rules", map[string]any{"rules": read.Rules}); rec.Code != http.StatusOK {
		t.Fatalf("saving: got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := alerts.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	before, err := alerts.ParseRules("original", []byte(original))
	if err != nil {
		t.Fatal(err)
	}

	if len(after) != len(before) {
		t.Fatalf("started with %d rules and ended with %d", len(before), len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("rule %d changed by being read and saved:\n before %+v\n after  %+v", i+1, before[i], after[i])
		}
	}
}

func TestSavingRulesKeepsTheFileReadableByTheEngine(t *testing.T) {
	h, path := harnessWithRules(t, seedRule)

	body := map[string]any{
		"rules": []map[string]any{
			{"kind": "error-rate", "threshold": 0.05, "channel": "adt", "severity": "critical"},
			{"kind": "no-traffic", "threshold": 1, "for": "15m"},
		},
	}
	if rec := h.do("admin", "PUT", "/api/alerts/rules", body); rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A header saying where the file came from, because somebody will find this file in a repository and wonder whether editing it by
	// hand is allowed. It is, and it says so.
	if !strings.Contains(string(data), "alert rules editor") {
		t.Errorf("the written file does not say what wrote it:\n%s", data)
	}

	loaded, err := alerts.LoadFile(path)
	if err != nil {
		t.Fatalf("the file the editor wrote does not load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d rules, want 2", len(loaded))
	}
}

func TestSavingRulesDoesNotWidenFilePermissions(t *testing.T) {
	h, path := harnessWithRules(t, seedRule)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if rec := h.do("admin", "PUT", "/api/alerts/rules", map[string]any{"rules": []map[string]any{{"kind": "queue-depth", "threshold": 5}}}); rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Writing through a temporary file is what makes the reload safe, and a temporary file is created with the process umask. Without
	// carrying the mode across, saving a rule from the interface would quietly make an operations file world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions became %o after saving, want 600", perm)
	}
}

func TestDeletingEveryRuleIsRefusedWithAWayForward(t *testing.T) {
	h, path := harnessWithRules(t, seedRule)

	rec := h.do("admin", "PUT", "/api/alerts/rules", map[string]any{"rules": []map[string]any{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 when every rule is deleted: %s", rec.Code, rec.Body.String())
	}

	// The refusal has to offer both ways out, because "at least one rule" on its own reads like a limitation rather than a choice.
	// One route gets the built-in rules back, the other keeps a tuned threshold while switching the rule off.
	body := rec.Body.String()
	if !strings.Contains(body, "-alerts") {
		t.Errorf("the refusal does not say how to get the built-in rules back: %s", body)
	}
	if !strings.Contains(body, "disable") {
		t.Errorf("the refusal does not mention disabling rules instead: %s", body)
	}

	// And the existing rules must still be there. A refusal that had already emptied the file would be the outage it is preventing.
	loaded, err := alerts.LoadFile(path)
	if err != nil {
		t.Fatalf("the rules file was damaged by a refused save: %v", err)
	}
	if len(loaded) != 1 {
		t.Errorf("the file holds %d rules after a refused save, want the original 1", len(loaded))
	}
}
