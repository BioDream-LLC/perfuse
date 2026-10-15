package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// mcpHarness is the API with MCP switched on and a token per test.
func mcpHarness(t *testing.T, on bool, perMinute int) *harness {
	t.Helper()
	h := newHarness(t)
	h.server.MCP = &MCPSettings{Enabled: func() bool { return on }, CallsPerMinute: func() int { return perMinute }}
	h.handler = h.server.Handler()
	return h
}

func mcpToken(t *testing.T, h *harness, label string, role store.Role, scopes ...string) string {
	t.Helper()
	tok, err := h.store.CreateAPIToken(t.Context(), label, role, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) > 0 {
		if err := h.store.ScopeUnchecked(store.DefaultTenant).SetAPITokenScopes(t.Context(), label, scopes); err != nil {
			t.Fatal(err)
		}
	}
	return tok
}

func mcpPost(h *harness, auth, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

type rpcOut struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Tools           []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *rpcError `json:"error"`
}

func rpc(t *testing.T, h *harness, tok, method, params string) rpcOut {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	rec := mcpPost(h, tok, body+"}")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
	}
	var out rpcOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v\n%s", method, err, rec.Body.String())
	}
	return out
}

func toolNames(o rpcOut) string {
	var n []string
	for _, t := range o.Result.Tools {
		n = append(n, t.Name)
	}
	return strings.Join(n, " ")
}

func TestMCPIsOffUntilSwitchedOnAndTakesOnlyTokens(t *testing.T) {
	off := mcpHarness(t, false, 60)
	tok := mcpToken(t, off, "agent", store.RoleViewer)
	if rec := mcpPost(off, tok, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); rec.Code != http.StatusNotFound {
		t.Errorf("switched off: %d", rec.Code)
	}

	h := mcpHarness(t, true, 60)
	if rec := mcpPost(h, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credential: %d", rec.Code)
	}
	// A person's browser session is refused: an agent is given exactly a token's access, never a person's.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("X-Perfuse-Request", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: h.tokens["admin"]})
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a browser session: %d", rec.Code)
	}
	tok = mcpToken(t, h, "agent", store.RoleViewer)
	if rec := mcpPost(h, tok, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Origin", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("another origin: %d", rec.Code)
	}
	if rec := mcpPost(h, tok, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "MCP-Protocol-Version", "1999-01-01"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown protocol version: %d", rec.Code)
	}
}

func TestMCPHandshakeListsToolsByRoleAndScope(t *testing.T) {
	h := mcpHarness(t, true, 60)
	viewer := mcpToken(t, h, "reader", store.RoleViewer)
	init := rpc(t, h, viewer, "initialize", `{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test-agent","version":"1"}}`)
	if init.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("negotiated %q", init.Result.ProtocolVersion)
	}
	if rec := mcpPost(h, viewer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); rec.Code != http.StatusAccepted {
		t.Errorf("a notification: %d", rec.Code)
	}
	list := toolNames(rpc(t, h, viewer, "tools/list", ""))
	if !strings.Contains(list, "search_messages") || !strings.Contains(list, "get_message") || strings.Contains(list, "retry_delivery") {
		t.Errorf("viewer tools: %s", list)
	}

	monitor := mcpToken(t, h, "wall", store.RoleMonitor)
	list = toolNames(rpc(t, h, monitor, "tools/list", ""))
	if strings.Contains(list, "get_message") || strings.Contains(list, "search_messages") || !strings.Contains(list, "queue") {
		t.Errorf("monitor tools: %s", list)
	}

	// An admin token without the scope still cannot act; with it, it can.
	admin := mcpToken(t, h, "ops", store.RoleAdmin)
	if list := toolNames(rpc(t, h, admin, "tools/list", "")); strings.Contains(list, "stop_channel") {
		t.Errorf("admin without mcp:write: %s", list)
	}
	writer := mcpToken(t, h, "ops-writer", store.RoleAdmin, store.ScopeMCPWrite)
	if list := toolNames(rpc(t, h, writer, "tools/list", "")); !strings.Contains(list, "stop_channel") || !strings.Contains(list, "retry_delivery") {
		t.Errorf("admin with mcp:write: %s", list)
	}
	if out := rpc(t, h, admin, "tools/call", `{"name":"stop_channel","arguments":{"name":"x"}}`); !out.Result.IsError ||
		!strings.Contains(out.Result.Content[0].Text, "mcp:write") {
		t.Errorf("a write without the scope: %+v", out.Result)
	}

	if out := rpc(t, h, viewer, "no/such", ""); out.Error == nil || out.Error.Code != -32601 {
		t.Errorf("an unknown method: %+v", out)
	}
}

func TestMCPToolsAnswerThroughTheAPIWithTheTokensAccessAndAreAudited(t *testing.T) {
	h := mcpHarness(t, true, 60)
	viewer := mcpToken(t, h, "reader", store.RoleViewer)
	out := rpc(t, h, viewer, "tools/call", `{"name":"list_channels","arguments":{}}`)
	if out.Result.IsError || len(out.Result.Content) == 0 {
		t.Fatalf("list_channels: %+v", out.Result)
	}
	// A route the token's role cannot reach is refused there, not decided again here.
	monitor := mcpToken(t, h, "wall", store.RoleMonitor)
	if out := rpc(t, h, monitor, "tools/call", `{"name":"get_message","arguments":{"id":1}}`); !out.Result.IsError {
		t.Errorf("a monitor reading a message: %+v", out.Result)
	}
	if out := rpc(t, h, viewer, "tools/call", `{"name":"get_channel","arguments":{}}`); !out.Result.IsError ||
		!strings.Contains(out.Result.Content[0].Text, "name is required") {
		t.Errorf("a missing argument: %+v", out.Result)
	}
	if out := rpc(t, h, viewer, "tools/call", `{"name":"fhir_search","arguments":{"resourceType":"../api"}}`); !out.Result.IsError {
		t.Errorf("a resource type that is a path: %+v", out.Result)
	}
	entries, err := h.store.ListAudit(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, e := range entries {
		if e.Action == "mcp.tool" {
			tools = append(tools, e.Username+" "+e.Target+" "+strings.Fields(e.Detail)[0])
		}
	}
	all := strings.Join(tools, "\n")
	for _, want := range []string{"token:reader list_channels ok", "token:wall get_message refused", "token:reader get_channel invalid"} {
		if !strings.Contains(all, want) {
			t.Errorf("no audit entry %q in\n%s", want, all)
		}
	}
}

func TestMCPCallsAreRateLimitedPerToken(t *testing.T) {
	h := mcpHarness(t, true, 3)
	tok := mcpToken(t, h, "loop", store.RoleViewer)
	other := mcpToken(t, h, "calm", store.RoleViewer)
	var limited int
	for i := 0; i < 5; i++ {
		out := rpc(t, h, tok, "tools/call", `{"name":"server_status"}`)
		if out.Result.IsError && strings.Contains(out.Result.Content[0].Text, "a minute") {
			limited++
		}
	}
	if limited != 2 {
		t.Errorf("%d of 5 calls limited, want 2", limited)
	}
	if out := rpc(t, h, other, "tools/call", `{"name":"server_status"}`); out.Result.IsError && strings.Contains(out.Result.Content[0].Text, "a minute") {
		t.Error("one token's loop limited another token")
	}
}

func TestMonitorTokensNeverSeeMessageBodies(t *testing.T) {
	in := map[string]any{"items": []any{map[string]any{"id": 1, "raw": "MSH|^~\\&|PATIENT", "channel": "adt",
		"deliveries": []any{map[string]any{"response": "MSA|AA", "status": "ok"}}}}}
	b, _ := json.Marshal(stripBodies(in))
	if strings.Contains(string(b), "MSH") || strings.Contains(string(b), "MSA") || !strings.Contains(string(b), "adt") {
		t.Errorf("%s", b)
	}
}
