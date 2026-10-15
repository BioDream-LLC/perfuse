package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/sof"
)

const testView = `{"resourceType":"ViewDefinition","name":"pts","url":"http://example.org/vd/pts","resource":"Patient",
 "select":[{"column":[{"name":"id","path":"getResourceKey()"},{"name":"family","path":"name.family.first()"}]}]}`

func TestViews(t *testing.T) {
	h := newHarness(t)
	h.server.ViewsDir = filepath.Join(h.dir, "views")
	var gotPatients []string
	h.server.RunView = func(_ context.Context, v *sof.View, patients []string, limit int) ([]sof.Row, bool, error) {
		gotPatients = patients
		return []sof.Row{{"pt1", "DOE"}}, false, nil
	}
	h.handler = h.server.Handler()

	if rr := h.do("viewer", "PUT", "/api/views/pts", map[string]any{"view": json.RawMessage(testView)}); rr.Code != 403 {
		t.Errorf("a viewer saved a view: %d", rr.Code)
	}
	if rr := h.do("editor", "PUT", "/api/views/other", map[string]any{"view": json.RawMessage(testView)}); rr.Code != 400 {
		t.Errorf("a name that does not match: %d", rr.Code)
	}
	if rr := h.do("editor", "PUT", "/api/views/bad", map[string]any{"view": json.RawMessage(`{"resource":"Patient","select":[{"column":[{"name":"x","path":"@@"}]}]}`)}); rr.Code != 400 {
		t.Errorf("an invalid view: %d", rr.Code)
	}
	if rr := h.do("editor", "PUT", "/api/views/..%2Fx", map[string]any{"view": json.RawMessage(testView)}); rr.Code != 400 {
		t.Errorf("a path as a name: %d", rr.Code)
	}
	if rr := h.do("editor", "PUT", "/api/views/pts", map[string]any{"view": json.RawMessage(testView)}); rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	rr := h.do("viewer", "GET", "/api/views", nil)
	var list struct {
		Views    []viewSummary `json:"views"`
		Examples []sof.Example `json:"examples"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Views) != 1 || list.Views[0].Resource != "Patient" || len(list.Examples) < 5 {
		t.Fatalf("%s", rr.Body)
	}
	if raw, ok := h.server.ViewLibrary()(context.Background(), "", "http://example.org/vd/pts|1.0"); !ok || !strings.Contains(string(raw), "getResourceKey") {
		t.Error("the FHIR endpoint cannot resolve the view by canonical")
	}
	if _, ok := h.server.ViewLibrary()(context.Background(), "ViewDefinition/pts", ""); !ok {
		t.Error("by reference")
	}

	// Over the store, and over pasted resources.
	rr = h.do("viewer", "POST", "/api/views/run", map[string]any{"view": json.RawMessage(testView), "patient": "Patient/pt1"})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"DOE"`) || len(gotPatients) != 1 || gotPatients[0] != "pt1" {
		t.Errorf("store: %d %s %v", rr.Code, rr.Body, gotPatients)
	}
	rr = h.do("viewer", "POST", "/api/views/run", map[string]any{"view": json.RawMessage(testView),
		"resources": `{"resourceType":"Patient","id":"x","name":[{"family":"SMITH"}]}` + "\n" + `{"resourceType":"Patient","id":"y"}`})
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"SMITH"`) || !strings.Contains(rr.Body.String(), `"pasted"`) {
		t.Errorf("pasted: %d %s", rr.Code, rr.Body)
	}
	rr = h.do("viewer", "POST", "/api/views/run", map[string]any{"view": json.RawMessage(testView), "format": "csv",
		"resources": `[{"resourceType":"Patient","id":"x","name":[{"family":"SMITH"}]}]`})
	if rr.Code != 200 || rr.Body.String() != "id,family\nx,SMITH\n" || !strings.Contains(rr.Header().Get("Content-Disposition"), "pts.csv") {
		t.Errorf("csv: %d %q", rr.Code, rr.Body)
	}
	if rr := h.do("viewer", "POST", "/api/views/run", map[string]any{"view": json.RawMessage(testView), "resources": "not json"}); rr.Code != 422 {
		t.Errorf("garbage resources: %d", rr.Code)
	}
	if rr := h.do("editor", "DELETE", "/api/views/pts", nil); rr.Code != 200 {
		t.Errorf("delete: %d", rr.Code)
	}
	if rr := h.do("editor", "DELETE", "/api/views/pts", nil); rr.Code != 404 {
		t.Errorf("delete twice: %d", rr.Code)
	}
}
