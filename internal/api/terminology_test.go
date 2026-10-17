package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/terminology"
)

func withTerminology(t *testing.T, h *harness) *terminology.Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/term.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	ts, err := terminology.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	h.server.Terminology = ts
	return ts
}

// A second person signs off a table edition, a new version takes over on its date, and a draft is never used.
func TestTableEditionsAndSignOff(t *testing.T) {
	h := newHarness(t)
	if err := h.server.Settings.Set(map[string]any{"data.tableApproval": true}); err != nil {
		t.Fatal(err)
	}
	ts := withTerminology(t, h)
	if _, err := ts.Import(t.Context(), terminology.ImportRequest{Format: terminology.FormatCSV, System: "urn:test:sex",
		Version: "1"}, strings.NewReader("code,display\nM,male\nF,female\n")); err != nil {
		t.Fatal(err)
	}
	ts.NoteUnmapped("", "adt", "sex", "2")

	v1 := map[string]any{"name": "sex", "describes": "local sex codes to the receiver's", "version": "1",
		"targetSystem": "urn:test:sex", "entries": []map[string]any{{"from": "1", "to": "M"}, {"from": "2", "to": "X"}}}
	res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", v1)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "draft") || !strings.Contains(res.Body.String(), "not codes in urn:test:sex: X") {
		t.Fatalf("save: %d %s", res.Code, res.Body.String())
	}
	if res := h.do(string(store.RoleEditor), "POST", "/api/codesets/table/approve", map[string]any{"name": "sex", "version": "1"}); res.Code != http.StatusConflict ||
		!strings.Contains(res.Body.String(), "somebody else") {
		t.Fatalf("self-approval: %d %s", res.Code, res.Body.String())
	}
	if res := h.do(string(store.RoleAdmin), "POST", "/api/codesets/table/approve", map[string]any{"name": "sex", "version": "1"}); res.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", res.Code, res.Body.String())
	}
	// Approved: the value it now maps leaves the unmapped list.
	if list, _ := ts.ListUnmapped(t.Context(), "", nil); len(list) != 0 {
		t.Errorf("unmapped after approval: %+v", list)
	}
	// An approved edition cannot be changed in place.
	if res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", v1); res.Code != http.StatusConflict {
		t.Errorf("edit in place: %d %s", res.Code, res.Body.String())
	}
	v2 := map[string]any{"name": "sex", "describes": "local sex codes to the receiver's", "version": "2",
		"entries": []map[string]any{{"from": "1", "to": "M"}, {"from": "2", "to": "F"}}}
	if res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", v2); res.Code != http.StatusOK {
		t.Fatalf("v2: %d %s", res.Code, res.Body.String())
	}
	if res := h.do(string(store.RoleAdmin), "POST", "/api/codesets/table/approve",
		map[string]any{"name": "sex", "version": "2", "effectiveFrom": "2099-01-01"}); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "from 2099-01-01") {
		t.Fatalf("approve with a date: %d %s", res.Code, res.Body.String())
	}
	got := decodeBody[tableEditions](t, h.do(string(store.RoleViewer), "GET", "/api/codesets/table?name=sex", nil))
	if got.InForce != "1" || len(got.Editions) != 2 || len(got.Editions[0].UnknownTo) != 1 {
		t.Errorf("editions %+v", got)
	}
	diff := h.do(string(store.RoleViewer), "GET", "/api/codesets/diff?name=sex&from=1&to=2", nil)
	if !strings.Contains(diff.Body.String(), `"after":"F"`) {
		t.Errorf("diff %s", diff.Body.String())
	}
	list := h.do(string(store.RoleViewer), "GET", "/api/codesets", nil)
	if !strings.Contains(list.Body.String(), `"inForce":"1"`) || !strings.Contains(list.Body.String(), `"editions":[`) {
		t.Errorf("listing %s", list.Body.String())
	}
	// A draft can be deleted; an approved edition only retired.
	if res := h.do(string(store.RoleEditor), "POST", "/api/codesets/table/delete-draft", map[string]any{"name": "sex", "version": "1"}); res.Code != http.StatusConflict {
		t.Errorf("deleting an approved edition: %d", res.Code)
	}
	if res := h.do(string(store.RoleEditor), "POST", "/api/codesets/table/retire", map[string]any{"name": "sex", "version": "2"}); res.Code != http.StatusOK {
		t.Errorf("retire: %d %s", res.Code, res.Body.String())
	}
}

// Without sign-off, a new version saved with no date takes over today rather than clashing with the one in force.
func TestANewVersionTakesOverToday(t *testing.T) {
	h := newHarness(t)
	for _, v := range []string{"1", "2"} {
		res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", map[string]any{"name": "sex", "describes": "d",
			"version": v, "entries": []map[string]any{{"from": "1", "to": "M" + v}}})
		if res.Code != http.StatusOK {
			t.Fatalf("version %s: %d %s", v, res.Code, res.Body.String())
		}
		if v == "2" && !strings.Contains(res.Body.String(), "takes over from today") {
			t.Errorf("no note: %s", res.Body.String())
		}
	}
	got := decodeBody[tableEditions](t, h.do(string(store.RoleViewer), "GET", "/api/codesets/table?name=sex", nil))
	if got.InForce != "2" {
		t.Errorf("in force %q", got.InForce)
	}
	// A third the same day replaces the second, which had only started today.
	res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", map[string]any{"name": "sex", "describes": "d",
		"version": "3", "entries": []map[string]any{{"from": "1", "to": "M3"}}})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "only started today") {
		t.Fatalf("version 3: %d %s", res.Code, res.Body.String())
	}
	got = decodeBody[tableEditions](t, h.do(string(store.RoleViewer), "GET", "/api/codesets/table?name=sex", nil))
	if got.InForce != "3" {
		t.Errorf("in force %q", got.InForce)
	}
}

func TestTerminologyEndpoints(t *testing.T) {
	h := newHarness(t)
	withTerminology(t, h)
	upload := func(role, query, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/terminology/import?"+query, bytes.NewReader([]byte(body)))
		req.Header.Set("X-Perfuse-Request", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokens[role]})
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec
	}
	if res := upload(string(store.RoleEditor), "format=csv&system=urn:x", "code,display\nA,a\n"); res.Code != http.StatusForbidden {
		t.Errorf("an editor imported: %d", res.Code)
	}
	if res := upload(string(store.RoleAdmin), "format=csv&system=urn:wards&version=1&name=Wards", "code,display\nICU,Intensive care\nW1,West one\n"); res.Code != http.StatusOK {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	if res := upload(string(store.RoleAdmin), "format=nope", "x"); res.Code != http.StatusBadRequest {
		t.Errorf("unknown format: %d", res.Code)
	}
	if res := h.do(string(store.RoleViewer), "GET", "/api/terminology/search?system=urn:wards&q=care", nil); !strings.Contains(res.Body.String(), "ICU") {
		t.Errorf("search %s", res.Body.String())
	}
	if res := h.do(string(store.RoleViewer), "GET", "/api/terminology/lookup?system=urn:wards&code=W1", nil); !strings.Contains(res.Body.String(), "West one") {
		t.Errorf("lookup %s", res.Body.String())
	}
	if res := h.do(string(store.RoleViewer), "GET", "/api/terminology/lookup?system=urn:wards&code=W9", nil); res.Code != http.StatusNotFound {
		t.Errorf("missing code: %d", res.Code)
	}
	if res := h.do(string(store.RoleViewer), "GET", "/api/terminology/systems", nil); !strings.Contains(res.Body.String(), `"concepts":2`) {
		t.Errorf("systems %s", res.Body.String())
	}
	if res := h.do(string(store.RoleAdmin), "DELETE", "/api/terminology/systems?system=urn:wards&version=1", nil); res.Code != http.StatusOK {
		t.Errorf("delete %d", res.Code)
	}
}
