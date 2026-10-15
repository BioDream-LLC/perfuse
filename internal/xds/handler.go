package xds

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Actor is which IHE actor an endpoint plays, and so which transactions it answers.
type Actor string

// The actors this server plays.
const (
	// ActorRepository answers ITI-41 Provide and Register and ITI-43 Retrieve.
	ActorRepository Actor = "repository"
	// ActorRegistry answers ITI-18 Stored Query and ITI-42 Register.
	ActorRegistry Actor = "registry"
	// ActorRespondingGateway answers XCA ITI-38 Cross Gateway Query and ITI-39 Cross Gateway Retrieve.
	ActorRespondingGateway Actor = "responding-gateway"
)

// Handler serves one actor's SOAP endpoint.
type Handler struct {
	Registry *Registry
	Actor    Actor
	Log      *slog.Logger
	// Allow decides whether the caller may make this transaction (a submission needs more than a query); nil allows.
	Allow func(r *http.Request, action string) error
	// Audit is told of every transaction: the action, what it concerned, and whether it succeeded.
	Audit func(r *http.Request, action, target string, ok bool)
}

func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Handler) actions() map[string]bool {
	switch h.Actor {
	case ActorRepository:
		return map[string]bool{ActionProvideAndRegister: true, ActionRetrieve: true}
	case ActorRegistry:
		return map[string]bool{ActionStoredQuery: true, ActionRegister: true}
	case ActorRespondingGateway:
		return map[string]bool{ActionCrossGatewayQuery: true, ActionCrossGatewayRetr: true}
	}
	return nil
}

func reply(w http.ResponseWriter, status int, b []byte, ct string) {
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "an IHE endpoint takes SOAP 1.2 by POST", http.StatusMethodNotAllowed)
		return
	}
	env, err := ReadEnvelope(r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		b, ct := Fault("", "", "Sender", err.Error())
		reply(w, http.StatusBadRequest, b, ct)
		return
	}
	if !h.actions()[env.Action] {
		var known []string
		for a := range h.actions() {
			known = append(known, a)
		}
		b, ct := Fault("", env.MessageID, "Sender", fmt.Sprintf("this %s endpoint does not answer %q; it answers %s", h.Actor, env.Action,
			strings.Join(known, " and ")))
		reply(w, http.StatusBadRequest, b, ct)
		return
	}
	if h.Allow != nil {
		if err := h.Allow(r, env.Action); err != nil {
			b, ct := Fault("", env.MessageID, "Sender", err.Error())
			reply(w, http.StatusForbidden, b, ct)
			return
		}
	}
	out := Outgoing{Action: env.Action + "Response", RelatesTo: env.MessageID}
	target, ok := "", true
	switch env.Action {
	case ActionProvideAndRegister:
		out.Body, target, ok = h.provideAndRegister(r.Context(), env)
	case ActionRegister:
		out.Body, target, ok = h.register(r.Context(), env)
	case ActionStoredQuery, ActionCrossGatewayQuery:
		out.Body, target, ok = h.query(r.Context(), env)
	case ActionRetrieve, ActionCrossGatewayRetr:
		out.Body, out.Attachments, target, ok = h.retrieve(r.Context(), env)
		out.MTOM = true
	}
	if h.Audit != nil {
		h.Audit(r, env.Action, target, ok)
	}
	b, ct := out.Write()
	reply(w, http.StatusOK, b, ct)
}

func marshal(v any) []byte {
	b, err := xml.Marshal(v)
	if err != nil {
		// Every field is a string or a struct of strings, so this does not happen; if it ever does, say so plainly.
		return []byte(`<rs:RegistryResponse xmlns:rs="` + NSRS + `" status="` + ResponseFailure + `"/>`)
	}
	return b
}

func (h *Handler) provideAndRegister(ctx context.Context, env *Envelope) ([]byte, string, bool) {
	var req ProvideAndRegisterRequest
	if err := xml.Unmarshal(env.Body, &req); err != nil {
		resp := Response([]Problem{problem(ErrRepositoryError, "", "the body is not a ProvideAndRegisterDocumentSetRequest: %v", err)})
		return marshal(resp), "", false
	}
	docs := map[string][]byte{}
	var ps []Problem
	for i := range req.Documents {
		d := &req.Documents[i]
		if err := env.Resolve(d); err != nil {
			ps = append(ps, problem(ErrRepositoryError, d.ID, "%v", err))
			continue
		}
		docs[d.ID] = d.Content
	}
	if len(ps) > 0 {
		return marshal(Response(ps)), "", false
	}
	sub, ps := h.Registry.Register(ctx, &req.Submit, docs)
	resp := Response(ps)
	if sub == nil {
		return marshal(resp), "", false
	}
	return marshal(resp), sub.SubmissionSet.ExternalID(IDSSPatientID) + " " + sub.SubmissionSet.ExternalID(IDSSUniqueID), true
}

func (h *Handler) register(ctx context.Context, env *Envelope) ([]byte, string, bool) {
	var req SubmitObjectsRequest
	if err := xml.Unmarshal(env.Body, &req); err != nil {
		return marshal(Response([]Problem{problem(ErrRegistryError, "", "the body is not a SubmitObjectsRequest: %v", err)})), "", false
	}
	sub, ps := h.Registry.Register(ctx, &req, nil)
	if sub == nil {
		return marshal(Response(ps)), "", false
	}
	return marshal(Response(ps)), sub.SubmissionSet.ExternalID(IDSSPatientID), true
}

func (h *Handler) query(ctx context.Context, env *Envelope) ([]byte, string, bool) {
	var q AdhocQueryRequest
	if err := xml.Unmarshal(env.Body, &q); err != nil {
		resp := QueryResponse(nil, []Problem{problem(ErrRegistryError, "", "the body is not an AdhocQueryRequest: %v", err)}, "", "")
		return marshal(resp), "", false
	}
	home := ""
	if env.Action == ActionCrossGatewayQuery {
		home = h.Registry.HomeCommunityID
		if home == "" {
			resp := QueryResponse(nil, []Problem{problem(ErrUnknownCommunity, "", "this server has no home community id (-xds-home-community)")}, "", "")
			return marshal(resp), "", false
		}
		// A query naming a community answers only for this one; one naming none (a patient query) is answered here.
		if q.Query.Home != "" && q.Query.Home != home {
			resp := QueryResponse(nil, []Problem{problem(ErrUnknownCommunity, q.Query.Home, "this gateway answers for %s, not %s", home, q.Query.Home)}, "", "")
			return marshal(resp), "", false
		}
	}
	res, ps := h.Registry.Query(ctx, &q)
	target := QueryNames[q.Query.ID]
	for _, s := range q.Query.Slots {
		if strings.HasSuffix(s.Name, "PatientId") || s.Name == "$patientId" {
			target += " " + strings.Join(values(s.Values), ",")
		}
	}
	return marshal(QueryResponse(res, ps, q.ResponseOpt.ReturnType, home)), target, res != nil
}

func (h *Handler) retrieve(ctx context.Context, env *Envelope) ([]byte, []Attachment, string, bool) {
	var req RetrieveRequest
	if err := xml.Unmarshal(env.Body, &req); err != nil {
		resp := RetrieveResponse{Status: Response([]Problem{problem(ErrRepositoryError, "", "the body is not a RetrieveDocumentSetRequest: %v", err)})}
		return marshal(resp), nil, "", false
	}
	home := ""
	if env.Action == ActionCrossGatewayRetr {
		home = h.Registry.HomeCommunityID
	}
	resp, err := h.Registry.Retrieve(ctx, &req, home)
	if err != nil {
		h.log().Error("XDS retrieve failed", "error", err)
		resp = RetrieveResponse{Status: Response([]Problem{problem(ErrRepositoryError, "", "the repository could not read the document")})}
	}
	var parts []Attachment
	var targets []string
	for i := range resp.Documents {
		d := &resp.Documents[i]
		cid := fmt.Sprintf("doc%d.%s@perfuse.health", i+1, newUUID())
		parts = append(parts, Attachment{ContentID: cid, MimeType: d.MimeType, Content: d.Document.Content})
		d.Document.Inner = Include(cid)
		targets = append(targets, d.DocumentUniqueID)
	}
	return marshal(resp), parts, strings.Join(targets, " "), !resp.Status.Failed()
}

var _ = bytes.TrimSpace
