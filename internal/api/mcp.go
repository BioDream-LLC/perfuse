package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The Model Context Protocol server, at /mcp.
//
// An AI agent connects with an API token and is given tools: the server's status, the channels, message search, a message,
// the patient search across formats, the queue, the alerts, and a FHIR search. Every tool is answered by the REST route or
// FHIR endpoint a person's screen uses, called with the agent's own token, so the agent gets exactly what that token is
// allowed - its role, its channel groups, its tenant - and nothing is decided twice. Every call is audited.
//
// Reading is the default. A tool that changes something (retry a queued delivery, reprocess a message, start or stop a
// channel) needs the token's mcp:write scope as well as the role the route needs, so a token issued for a reporting agent
// cannot be talked into acting.
//
// Transport: MCP's Streamable HTTP, answered as JSON (the specification lets a server answer every request with
// application/json and never open an event stream; this one does that). Protocol versions 2025-06-18, 2025-03-26 and
// 2024-11-05 are accepted.

// MCPSettings switch MCP on and bound it.
type MCPSettings struct {
	// Enabled is the switch; without it /mcp answers 404.
	Enabled func() bool
	// CallsPerMinute bounds tool calls per token; 0 is 60.
	CallsPerMinute func() int
	// Origins are browser origins allowed to call /mcp, beside this server's own. Requests with no Origin (agents, CLIs)
	// are allowed; a browser page on another origin is refused, which is what MCP asks of a server for DNS rebinding.
	Origins []string
}

const mcpProtocol = "2025-06-18"

var mcpProtocols = map[string]bool{"2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpTool is one tool: its schema for the agent, the role and scope it needs, and how it is answered.
type mcpTool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	Role        store.Role
	Writes      bool
	// Call turns the arguments into one request to the REST API or the FHIR endpoint.
	Call func(args map[string]any) (method, path string, body any, fhir bool, err error)
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func argStr(a map[string]any, k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func argInt(a map[string]any, k string, def, max int) int {
	n := def
	switch v := a[k].(type) {
	case float64:
		n = int(v)
	case string:
		if p, err := strconv.Atoi(v); err == nil {
			n = p
		}
	}
	if n <= 0 {
		n = def
	}
	if n > max {
		n = max
	}
	return n
}

func query(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func needed(a map[string]any, keys ...string) error {
	for _, k := range keys {
		if argStr(a, k) == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}

var mcpTools = []mcpTool{
	{Name: "server_status", Title: "Server status", Role: store.RoleMonitor,
		Description: "Whether the engine is running: channels running and stopped, message counts and errors.",
		Schema:      obj(map[string]any{}),
		Call: func(map[string]any) (string, string, any, bool, error) {
			return http.MethodGet, "/api/status", nil, false, nil
		}},
	{Name: "list_channels", Title: "List channels", Role: store.RoleMonitor,
		Description: "Every channel this token can see, with its source, destinations, whether it runs and its counts.",
		Schema:      obj(map[string]any{}),
		Call: func(map[string]any) (string, string, any, bool, error) {
			return http.MethodGet, "/api/channels", nil, false, nil
		}},
	{Name: "get_channel", Title: "Get a channel", Role: store.RoleViewer,
		Description: "One channel's configuration and status.",
		Schema:      obj(map[string]any{"name": strProp("The channel's name.")}, "name"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "name"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodGet, "/api/channels/" + url.PathEscape(argStr(a, "name")), nil, false, nil
		}},
	{Name: "search_messages", Title: "Search messages", Role: store.RoleViewer,
		Description: "Messages received, newest first, without their contents: channel, outcome (delivered, filtered, error, ...), " +
			"message type and event, control ID, sender, free text, and since (RFC 3339). Use get_message for one message's contents.",
		Schema: obj(map[string]any{"channel": strProp("Only this channel."), "outcome": strProp("delivered, filtered, error, ..."),
			"type": strProp("Message type, such as ADT or ORU."), "event": strProp("Trigger event, such as A01."),
			"controlId": strProp("The message control ID."), "sender": strProp("The sending application or facility."),
			"search": strProp("Free text found in the message."), "since": strProp("RFC 3339 time."),
			"limit": intProp("At most this many; 1 to 100, default 20.")}),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			return http.MethodGet, "/api/messages" + query("channel", argStr(a, "channel"), "outcome", argStr(a, "outcome"),
				"type", argStr(a, "type"), "event", argStr(a, "event"), "controlId", argStr(a, "controlId"),
				"sender", argStr(a, "sender"), "search", argStr(a, "search"), "since", argStr(a, "since"),
				"limit", strconv.Itoa(argInt(a, "limit", 20, 100))), nil, false, nil
		}},
	{Name: "get_message", Title: "Get a message", Role: store.RoleViewer,
		Description: "One message with its contents and what happened to it at each destination. It holds patient data.",
		Schema:      obj(map[string]any{"id": intProp("The message's id, from search_messages.")}, "id"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "id"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodGet, "/api/messages/" + url.PathEscape(argStr(a, "id")), nil, false, nil
		}},
	{Name: "find_patient", Title: "Find a patient's messages", Role: store.RoleViewer,
		Description: "Messages about one patient across HL7 v2, FHIR, DICOM and X12, by MRN, name, birth date, accession or " +
			"claim number. kind narrows which identifier the term is (empty searches all).",
		Schema: obj(map[string]any{"term": strProp("The identifier or name."), "kind": strProp("mrn, name, dob, accession, claim, ..."),
			"channel": strProp("Only this channel."), "limit": intProp("At most this many; 1 to 100, default 20.")}, "term"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "term"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodPost, "/api/messages/find", map[string]any{"term": argStr(a, "term"), "kind": argStr(a, "kind"),
				"channel": argStr(a, "channel"), "limit": argInt(a, "limit", 20, 100)}, false, nil
		}},
	{Name: "queue", Title: "Delivery queue", Role: store.RoleMonitor,
		Description: "Deliveries waiting or failed, with each destination's depth and the age of its oldest item. state is " +
			"pending, failed or dead.",
		Schema: obj(map[string]any{"channel": strProp("Only this channel."), "destination": strProp("Only this destination."),
			"state": strProp("pending, failed or dead."), "limit": intProp("At most this many; 1 to 100, default 20.")}),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			return http.MethodGet, "/api/queue" + query("channel", argStr(a, "channel"), "destination", argStr(a, "destination"),
				"state", argStr(a, "state"), "limit", strconv.Itoa(argInt(a, "limit", 20, 100))), nil, false, nil
		}},
	{Name: "alerts", Title: "Alerts", Role: store.RoleMonitor,
		Description: "Alerts raised and not yet resolved, most severe first.",
		Schema:      obj(map[string]any{}),
		Call: func(map[string]any) (string, string, any, bool, error) {
			return http.MethodGet, "/api/alerts", nil, false, nil
		}},
	{Name: "fhir_search", Title: "Search the FHIR store", Role: store.RoleViewer,
		Description: "A FHIR search on this server's FHIR endpoint, with the token's own access there: resourceType and the " +
			"search parameters as a query string (patient=123&status=final). Answers a searchset Bundle.",
		Schema: obj(map[string]any{"resourceType": strProp("Patient, Observation, Encounter, ..."),
			"query": strProp("Search parameters, as a URL query string without the ?.")}, "resourceType"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			t := argStr(a, "resourceType")
			if t == "" || strings.ContainsAny(t, "/?#.") || t[0] < 'A' || t[0] > 'Z' {
				return "", "", nil, false, fmt.Errorf("resourceType must be a FHIR resource type, such as Patient")
			}
			q := strings.TrimPrefix(argStr(a, "query"), "?")
			if _, err := url.ParseQuery(q); err != nil {
				return "", "", nil, false, fmt.Errorf("query is not a URL query string: %v", err)
			}
			path := "/" + t
			if q != "" {
				path += "?" + q
			}
			return http.MethodGet, path, nil, true, nil
		}},

	// Tools that change something: the token's role and the mcp:write scope.
	{Name: "retry_delivery", Title: "Retry a delivery", Role: store.RoleOperator, Writes: true,
		Description: "Retry a queued delivery now: one item by id, or every item of a channel's destination.",
		Schema: obj(map[string]any{"id": intProp("The queue item's id."), "channel": strProp("A channel, to retry a whole destination."),
			"destination": strProp("The destination, with channel.")}),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			id, _ := strconv.ParseInt(argStr(a, "id"), 10, 64)
			if id <= 0 && (argStr(a, "channel") == "" || argStr(a, "destination") == "") {
				return "", "", nil, false, fmt.Errorf("give id, or channel and destination")
			}
			return http.MethodPost, "/api/queue/retry", map[string]any{"id": id, "channel": argStr(a, "channel"),
				"destination": argStr(a, "destination")}, false, nil
		}},
	{Name: "reprocess_message", Title: "Reprocess a message", Role: store.RoleOperator, Writes: true,
		Description: "Send a stored message through its channel again, as if it had just arrived.",
		Schema:      obj(map[string]any{"id": intProp("The message's id.")}, "id"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "id"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodPost, "/api/messages/" + url.PathEscape(argStr(a, "id")) + "/reprocess", map[string]any{}, false, nil
		}},
	{Name: "start_channel", Title: "Start a channel", Role: store.RoleAdmin, Writes: true,
		Description: "Start a stopped channel.",
		Schema:      obj(map[string]any{"name": strProp("The channel's name.")}, "name"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "name"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodPost, "/api/channels/" + url.PathEscape(argStr(a, "name")) + "/start", map[string]any{}, false, nil
		}},
	{Name: "stop_channel", Title: "Stop a channel", Role: store.RoleAdmin, Writes: true,
		Description: "Stop a running channel. Messages sent to it meanwhile are refused by its source, not lost by Perfuse.",
		Schema:      obj(map[string]any{"name": strProp("The channel's name.")}, "name"),
		Call: func(a map[string]any) (string, string, any, bool, error) {
			if err := needed(a, "name"); err != nil {
				return "", "", nil, false, err
			}
			return http.MethodPost, "/api/channels/" + url.PathEscape(argStr(a, "name")) + "/stop", map[string]any{}, false, nil
		}},
}

func findTool(name string) (mcpTool, bool) {
	for _, t := range mcpTools {
		if t.Name == name {
			return t, true
		}
	}
	return mcpTool{}, false
}

// mcpLimiter is a token bucket per caller.
type mcpLimiter struct {
	mu   sync.Mutex
	left map[string]float64
	at   map[string]time.Time
}

func (l *mcpLimiter) allow(who string, perMinute int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.left == nil {
		l.left, l.at = map[string]float64{}, map[string]time.Time{}
	}
	now := time.Now()
	left, ok := l.left[who]
	if !ok {
		left = float64(perMinute)
	} else {
		left = min(float64(perMinute), left+now.Sub(l.at[who]).Minutes()*float64(perMinute))
	}
	l.at[who] = now
	if left < 1 {
		l.left[who] = left
		return false
	}
	l.left[who] = left - 1
	return true
}

func (s *Server) registerMCP(mux *recordingMux) {
	h := s.require(store.RoleMonitor, s.handleMCP)
	mux.Handle("POST /mcp", h)
	// No event stream is offered: every answer is JSON. MCP has a client that finds GET refused fall back to that. Both are
	// behind authentication like POST, so an unauthenticated caller cannot learn whether MCP is on.
	mux.Handle("GET /mcp", s.require(store.RoleMonitor, func(w http.ResponseWriter, r *http.Request, _ *store.Session) {
		if !s.mcpOn() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server answers every request as JSON and opens no event stream", http.StatusMethodNotAllowed)
	}))
	mux.Handle("DELETE /mcp", s.require(store.RoleMonitor, func(w http.ResponseWriter, _ *http.Request, _ *store.Session) {
		// Sessions hold nothing to end.
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.Handle("GET /api/mcp", s.require(store.RoleViewer, s.handleMCPStatus))
}

func (s *Server) mcpOn() bool {
	return s.MCP != nil && s.MCP.Enabled != nil && s.MCP.Enabled()
}

func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	type toolRow struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Role        string `json:"role"`
		Writes      bool   `json:"writes"`
	}
	tools := make([]toolRow, 0, len(mcpTools))
	for _, t := range mcpTools {
		tools = append(tools, toolRow{t.Name, t.Title, t.Description, string(t.Role), t.Writes})
	}
	per := 60
	if s.MCP != nil && s.MCP.CallsPerMinute != nil && s.MCP.CallsPerMinute() > 0 {
		per = s.MCP.CallsPerMinute()
	}
	s.ok(w, map[string]any{"enabled": s.mcpOn(), "url": s.selfURL() + "/mcp", "protocol": mcpProtocol, "tools": tools,
		"callsPerMinute": per, "fhir": s.FHIRHandler != nil})
}

func (s *Server) mcpOriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && u.Host == r.Host {
		return true
	}
	if s.MCP != nil {
		for _, o := range s.MCP.Origins {
			if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.mcpOn() {
		http.NotFound(w, r)
		return
	}
	if !authenticatedWithBearer(r) {
		// MCP is for machines. A person's browser session reaching here would be an agent running with that person's full
		// access, unaudited as a machine; the token is how an agent is given exactly what it should have.
		w.Header().Set("WWW-Authenticate", `Bearer realm="perfuse-mcp"`)
		http.Error(w, "the MCP server takes an API token (Authorization: Bearer ...), not a browser session", http.StatusUnauthorized)
		return
	}
	if !s.mcpOriginOK(r) {
		http.Error(w, "this origin may not call the MCP server", http.StatusForbidden)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !mcpProtocols[v] {
		http.Error(w, "MCP-Protocol-Version "+v+" is not supported; this server speaks "+mcpProtocol, http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	trimmed := bytes.TrimSpace(raw)
	batch := len(trimmed) > 0 && trimmed[0] == '['
	var reqs []rpcRequest
	if batch {
		err = json.Unmarshal(trimmed, &reqs)
	} else {
		var one rpcRequest
		err = json.Unmarshal(trimmed, &one)
		reqs = []rpcRequest{one}
	}
	if err != nil || len(reqs) == 0 {
		s.writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32700, Message: "the body is not JSON-RPC 2.0"}})
		return
	}
	var out []rpcResponse
	for _, req := range reqs {
		resp, notify := s.mcpOne(r, sess, req)
		if !notify {
			out = append(out, resp)
		}
	}
	if len(out) == 0 {
		// Only notifications and responses: accepted, nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	for _, req := range reqs {
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", newMCPSession())
		}
	}
	if batch {
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	s.writeJSON(w, http.StatusOK, out[0])
}

func newMCPSession() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (s *Server) mcpOne(r *http.Request, sess *store.Session, req rpcRequest) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	notify := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{Code: -32600, Message: "a JSON-RPC 2.0 request has jsonrpc \"2.0\" and a method"}
		return resp, notify && req.Method == ""
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpProtocol
		if mcpProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "mcp.connect",
			Target: strings.TrimSpace(p.ClientInfo.Name + " " + p.ClientInfo.Version), IP: clientIP(r)})
		resp.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "perfuse", "title": "Perfuse", "version": s.Version},
			"instructions": "Perfuse is a healthcare integration engine. These tools read its channels, messages, queue, alerts " +
				"and FHIR store with this token's own access; messages and FHIR resources hold patient data, so fetch only what " +
				"the task needs. Tools that change something need the token's mcp:write scope.",
		}
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return resp, true
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		var list []any
		for _, t := range mcpTools {
			if !sess.Role.AtLeast(t.Role) || t.Writes && !sess.HasScope(store.ScopeMCPWrite) {
				continue
			}
			list = append(list, map[string]any{"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.Schema,
				"annotations": map[string]any{"readOnlyHint": !t.Writes, "destructiveHint": t.Name == "stop_channel",
					"openWorldHint": false}})
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			resp.Error = &rpcError{Code: -32602, Message: "tools/call needs params.name"}
			return resp, notify
		}
		resp.Result = s.mcpCall(r, sess, p.Name, p.Arguments)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "no method " + req.Method + " on this server"}
	}
	return resp, notify
}

func toolError(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": true}
}

func (s *Server) mcpCall(r *http.Request, sess *store.Session, name string, args map[string]any) map[string]any {
	t, ok := findTool(name)
	audit := func(outcome string) {
		a, _ := json.Marshal(args)
		detail := string(a)
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "mcp.tool", Target: name,
			Detail: outcome + " " + detail, IP: clientIP(r)})
	}
	if !ok {
		audit("unknown")
		return toolError("there is no tool " + name)
	}
	per := 60
	if s.MCP.CallsPerMinute != nil && s.MCP.CallsPerMinute() > 0 {
		per = s.MCP.CallsPerMinute()
	}
	if !s.mcpLimit.allow(sess.Username, per) {
		audit("rate-limited")
		return toolError(fmt.Sprintf("this token has made more than %d tool calls a minute; wait and try again", per))
	}
	if !sess.Role.AtLeast(t.Role) {
		audit("refused")
		return toolError(fmt.Sprintf("%s needs the %s role, and this token is %s", name, t.Role, sess.Role))
	}
	if t.Writes && !sess.HasScope(store.ScopeMCPWrite) {
		audit("refused")
		return toolError(name + " changes something, and this token has no mcp:write scope; issue one with it under " +
			"Administer › Users › Machine credentials to let an agent act")
	}
	if args == nil {
		args = map[string]any{}
	}
	method, path, body, fhir, err := t.Call(args)
	if err != nil {
		audit("invalid")
		return toolError(err.Error())
	}
	status, out := s.mcpDispatch(r, sess, method, path, body, fhir)
	if status >= 400 {
		audit(strconv.Itoa(status))
		return toolError(fmt.Sprintf("%d: %s", status, mcpErrorText(out)))
	}
	audit("ok")
	var structured any
	if json.Unmarshal(out, &structured) != nil {
		structured = nil
	}
	if sess.Role == store.RoleMonitor {
		structured = stripBodies(structured)
		out, _ = json.Marshal(structured)
	}
	text := string(out)
	if len(text) > 200_000 {
		text = text[:200_000] + "\n… (cut at 200 KB; narrow the search)"
	}
	res := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": false}
	if m, ok := structured.(map[string]any); ok {
		res["structuredContent"] = m
	}
	return res
}

func mcpErrorText(b []byte) string {
	var e struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		if len(e.Problems) > 0 {
			return e.Error + ": " + strings.Join(e.Problems, "; ")
		}
		return e.Error
	}
	var oo struct {
		Issue []struct {
			Diagnostics string `json:"diagnostics"`
		} `json:"issue"`
	}
	if json.Unmarshal(b, &oo) == nil && len(oo.Issue) > 0 {
		var d []string
		for _, i := range oo.Issue {
			d = append(d, i.Diagnostics)
		}
		return strings.Join(d, "; ")
	}
	return strings.TrimSpace(string(b))
}

// stripBodies removes message contents from what a monitor token is shown, whatever a route returned: the monitor role
// sees whether things work and never patient data.
func stripBodies(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch k {
			case "raw", "payload", "body", "content", "transformed", "response":
				continue
			}
			out[k] = stripBodies(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = stripBodies(e)
		}
		return out
	}
	return v
}

// mcpDispatch answers a tool by calling the route a screen would, with the agent's own credential.
func (s *Server) mcpDispatch(r *http.Request, sess *store.Session, method, path string, body any, fhir bool) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inner, err := http.NewRequestWithContext(ctx, method, path, rd)
	if err != nil {
		return http.StatusBadRequest, []byte(err.Error())
	}
	inner.Header.Set("Authorization", r.Header.Get("Authorization"))
	inner.Header.Set("Accept", "application/json")
	inner.Header.Set("X-Perfuse-Request", "1")
	inner.RemoteAddr = r.RemoteAddr
	inner.Host = r.Host
	if body != nil {
		inner.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	switch {
	case fhir && s.FHIRHandler == nil:
		return http.StatusNotFound, []byte(`{"error":"the FHIR endpoint is off on this server (serve -fhir)"}`)
	case fhir:
		inner.Header.Set("Accept", "application/fhir+json")
		s.FHIRHandler.ServeHTTP(rec, inner)
	default:
		s.mcpRoutes.ServeHTTP(rec, inner)
	}
	return rec.Code, rec.Body.Bytes()
}
