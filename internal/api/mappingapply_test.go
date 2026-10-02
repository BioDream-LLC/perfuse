package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Tests for acting on a mapping suggestion.
//
// The mapper suggested and nothing could be done with a suggestion: somebody read "MSH-4 to Organization.name, 87%" and typed it into
// the builder themselves, which is where a transcription error enters a mapping that was suggested correctly.
//
// Most of what follows guards the abstention design rather than the happy path. The engine declining is the feature - a confident wrong
// mapping in a clinical system is worse than no mapping - and the way to lose that is to make approval convenient.

func TestApprovedMappingsBecomeStepsOnTheChannel(t *testing.T) {
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel": "adt-inbound",
		"mappings": []any{
			map[string]any{"source": "ZPI-1", "target": "PID-3.1", "confidence": 87, "reasoning": "same shape and name"},
			map[string]any{"source": "ZPI-2", "target": "PID-7"},
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	// Read back through the real loader, because the only thing that matters is whether the channel Perfuse loads now has the steps.
	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Transformations) != 2 {
		t.Fatalf("got %d transformations, want 2", len(cfg.Transformations))
	}

	first := cfg.Transformations[0]
	if first.Copy == nil {
		t.Fatal("the first step is not a copy")
	}
	if first.Copy.From != "ZPI-1" || first.Copy.To != "PID-3.1" {
		t.Errorf("copy = %s to %s, want ZPI-1 to PID-3.1", first.Copy.From, first.Copy.To)
	}

	// The confidence goes into the description. A step nobody can account for six months later is the reason that field exists, and
	// "suggested at 87%" is the difference between a mapping that was reviewed and one that appeared.
	if !strings.Contains(first.Description, "87%") {
		t.Errorf("the step does not record where it came from: %q", first.Description)
	}
	if !strings.Contains(first.Description, "approved by hand") {
		t.Errorf("the step does not record that a person approved it: %q", first.Description)
	}
}

func TestAnAbstentionCannotBeApproved(t *testing.T) {
	// The assertion this whole feature turns on. An abstention is the engine saying it does not know, so approving one is not a
	// judgement call somebody is entitled to make from this screen - the interface does not offer it, and a request containing one
	// means the two disagree.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel": "adt-inbound",
		"mappings": []any{
			map[string]any{"source": "DateOfBirth", "target": "PID-29", "abstained": true},
		},
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an abstention was approved: %d %s", res.Code, res.Body.String())
	}

	var out struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.Error, "does not know") {
		t.Errorf("the refusal does not say what an abstention means: %q", out.Error)
	}

	// Named, so somebody can see which one was refused rather than being told the request failed.
	if len(out.Problems) != 1 || !strings.Contains(out.Problems[0], "DateOfBirth") {
		t.Errorf("the refusal does not name the abstention: %#v", out.Problems)
	}

	// And nothing was written. A partial application would be the worst outcome: some mappings in, no record of which.
	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Transformations) != 0 {
		t.Errorf("the channel was modified by a request that was refused: %#v", cfg.Transformations)
	}
}

func TestAnAbstentionRefusesTheWholeRequest(t *testing.T) {
	// Checked before anything is written, so one abstention among good mappings refuses all of them rather than applying the rest.
	//
	// Deliberate: the alternative is a channel holding some of what somebody approved with nothing saying which, and the fix is then
	// to work out what happened rather than to press the button again.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel": "adt-inbound",
		"mappings": []any{
			map[string]any{"source": "ZPI-1", "target": "PID-3.1", "confidence": 92},
			map[string]any{"source": "DateOfBirth", "target": "PID-29", "abstained": true},
		},
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}

	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Transformations) != 0 {
		t.Errorf("a refused request applied %d of its mappings", len(cfg.Transformations))
	}
}

func TestApprovingNothingIsRefused(t *testing.T) {
	// An empty list is the request a screen with no selection would send, and reporting success for it would tell somebody their
	// mappings were applied.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel": "adt-inbound", "mappings": []any{},
	})
	if res.Code != http.StatusBadRequest {
		t.Errorf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

func TestApplyingMappingsSaysTheyAreNotRunningYet(t *testing.T) {
	// A mapping in a channel file is not a mapping that is running. Somebody who believes otherwise watches traffic for a change that
	// cannot appear until the channel reloads, and concludes the feature does not work.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel":  "adt-inbound",
		"mappings": []any{map[string]any{"source": "ZPI-1", "target": "PID-3.1"}},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	if !strings.Contains(res.Body.String(), "next loads") {
		t.Errorf("the response does not say the steps are not running yet: %s", res.Body.String())
	}
}

func TestApprovedMappingsCanBecomeARecipe(t *testing.T) {
	// The other thing to do with approved mappings, and a different decision: applying changes this server, a recipe is for somebody
	// else's. The recipe format already existed with nothing producing one.
	h := newHarness(t)

	res := h.do("editor", http.MethodPost, "/api/mappings/recipe", map[string]any{
		"name":         "Epic ADT to our PID",
		"sourceSystem": "Epic 2023",
		"targetSystem": "Perfuse",
		"mappings": []any{
			map[string]any{"source": "ZPI-1", "target": "PID-3.1", "confidence": 87, "reasoning": "same shape"},
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	if cd := res.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("the recipe was not offered as a file: %q", cd)
	}

	var recipe struct {
		Format   string `json:"format"`
		Mappings []struct {
			SourcePath string `json:"sourcePath"`
			TargetPath string `json:"targetPath"`
			Approved   bool   `json:"approved"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &recipe); err != nil {
		t.Fatal(err)
	}

	if len(recipe.Mappings) != 1 {
		t.Fatalf("got %d mappings, want 1", len(recipe.Mappings))
	}

	// Marked approved, because the recipe format distinguishes approved from pending precisely so a shared recipe says which
	// mappings a human agreed to rather than which a machine proposed.
	if !recipe.Mappings[0].Approved {
		t.Error("a mapping that went through the approval endpoint is not marked approved in the recipe")
	}
}

func TestAViewerCannotApplyMappings(t *testing.T) {
	// Applying mappings writes a channel file, so it is an editor's action however much the mapper looks like a read-only tool.
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel":  "adt-inbound",
		"mappings": []any{map[string]any{"source": "ZPI-1", "target": "PID-3.1"}},
	})
	if res.Code != http.StatusForbidden {
		t.Errorf("a viewer got %d, want 403", res.Code)
	}
}

func TestAFieldNameThatIsNotAPathIsRefusedWithWhatToDoInstead(t *testing.T) {
	// The design correction the loader caught. A copy step copies between two paths inside one message, so it can only express a
	// mapping whose source is itself a path - HL7 to HL7. The mapper's source fields usually are not: they are column names, JSON
	// keys or a vendor's own field names, and "PatientMRN" is not somewhere a message keeps anything.
	//
	// The loader does refuse this, and its message is correct and useless: PATIENTMRN is not a three-character segment name. True,
	// and it says nothing about what to do. So it is refused here instead, naming the alternative.
	h := newHarness(t)

	if res := h.do("editor", http.MethodPost, "/api/channels", channelPayload{YAML: sampleYAML}); res.Code != http.StatusOK {
		t.Fatalf("creating the channel: %d %s", res.Code, res.Body.String())
	}

	res := h.do("editor", http.MethodPost, "/api/mappings/approve", map[string]any{
		"channel": "adt-inbound",
		"mappings": []any{
			map[string]any{"source": "PatientMRN", "target": "PID-3.1", "confidence": 80},
		},
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}

	var out struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	// The refusal has to say what to do instead, because the answer is a different artefact rather than a corrected keystroke.
	if !strings.Contains(out.Error, "recipe") {
		t.Errorf("the refusal does not offer the alternative: %q", out.Error)
	}
	if !strings.Contains(out.Error, "path") {
		t.Errorf("the refusal does not say what a source has to be: %q", out.Error)
	}

	// And it names the offending field, so somebody can see which of several it objected to.
	if len(out.Problems) != 1 || out.Problems[0] != "PatientMRN" {
		t.Errorf("the refusal does not name the field: %#v", out.Problems)
	}

	// Nothing written, and this matters more here than elsewhere: the loader would have refused the saved file, so a write would
	// have made the channel disappear rather than leaving a bad step in it.
	cfg, err := h.server.Channels.Get("adt-inbound")
	if err != nil {
		t.Fatalf("the channel no longer loads after a refused request: %v", err)
	}
	if len(cfg.Transformations) != 0 {
		t.Errorf("a refused request wrote %d steps", len(cfg.Transformations))
	}
}

func TestAFieldNameCanStillBecomeARecipe(t *testing.T) {
	// The other half of that refusal, and the reason it points at recipes. A mapping from a vendor's field name to an HL7 path is
	// perfectly real - it just is not something a copy step can express, and a recipe is where it belongs.
	h := newHarness(t)

	res := h.do("editor", http.MethodPost, "/api/mappings/recipe", map[string]any{
		"name":         "Epic column names to PID",
		"sourceSystem": "Epic 2023",
		"mappings": []any{
			map[string]any{"source": "PatientMRN", "target": "PID-3.1", "confidence": 80},
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("a field name was refused for a recipe as well: %d %s", res.Code, res.Body.String())
	}
}
