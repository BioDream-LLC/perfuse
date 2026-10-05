package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/crd"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/store"
)

// CDS Hooks, for Da Vinci CRD: the payer's coverage requirements service.
//
// Discovery is open, as CDS Hooks requires. A service call must authenticate one of two ways: the JWT an EHR signs for each call, from
// an issuer this server trusts, with its JWKS URL - which is what the CDS Hooks specification describes - or a Perfuse API token, for a
// client that cannot sign. A call with neither is refused: a coverage answer names the patient's plan, and an open endpoint would answer
// anybody who can guess a member's details.

// CDSClient is an EHR allowed to call the CRD service with a signed JWT.
type CDSClient struct {
	// Issuer is the iss the EHR signs with.
	Issuer string `yaml:"issuer" json:"issuer"`
	// JWKSURL is where its public keys are published.
	JWKSURL string `yaml:"jwks_url" json:"jwksUrl"`

	keys *oidc.KeySet
}

// LoadCDSClients reads the trusted EHR list.
func LoadCDSClients(path string) ([]*CDSClient, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Clients []*CDSClient `yaml:"clients"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, c := range doc.Clients {
		if !strings.HasPrefix(c.Issuer, "http") || !(strings.HasPrefix(c.JWKSURL, "https://") || loopbackHTTP(c.JWKSURL)) {
			return nil, fmt.Errorf("%s: client %d needs an issuer and an https jwks_url", path, i+1)
		}
	}
	return doc.Clients, nil
}

// loopbackHTTP allows a JWKS on this machine over plain HTTP, as a test harness such as Inferno serves one: keys fetched over loopback
// cross no network, so https adds nothing there.
func loopbackHTTP(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleCDSDiscovery(w http.ResponseWriter, r *http.Request) {
	if s.CRD == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no CDS services are configured; start the server with -crd-rules"})
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, s.CRD.Discovery())
}

// cdsAuthorised accepts a Perfuse API token or a trusted EHR's JWT whose audience is this service's URL.
func (s *Server) cdsAuthorised(r *http.Request, serviceID string) (string, error) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token == "" {
		return "", errors.New("a CDS Hooks call needs a bearer JWT from a trusted EHR, or a Perfuse API token")
	}
	if sess, err := s.Store.LookupAPIToken(r.Context(), token); err == nil {
		return sess.Username, nil
	}
	if strings.Count(token, ".") != 2 {
		return "", errors.New("the bearer credential is neither an API token nor a JWT")
	}
	audience := s.publicBase() + "/cds-services/" + serviceID
	var last error = errors.New("the JWT is not from a trusted EHR")
	for _, c := range s.CDSClients {
		if c.keys == nil {
			c.keys = oidc.NewKeySet(c.JWKSURL, s.shlClient())
		}
		// A CDS Hooks JWT authenticates the EHR, not a person: iss, aud, exp, iat and jti, and no sub. Requiring a subject, as
		// sign-in rightly does, refused every call from the Inferno CRD test kit.
		claims, err := oidc.Verify(r.Context(), c.keys, token, oidc.VerifyOptions{Issuer: c.Issuer, ClientID: audience,
			Algorithms: []string{"RS384", "ES384", "RS256", "ES256"}, System: true})
		if err == nil {
			if !s.cdsJTIs.first(claims.Issuer+"|"+claims.JTI, claims.ExpiresAt) {
				return "", errors.New("this JWT has been used before (its jti repeats), so it may be a replay")
			}
			return "ehr:" + claims.Issuer, nil
		}
		last = err
	}
	return "", last
}

func (s *Server) handleCDSService(w http.ResponseWriter, r *http.Request) {
	if s.CRD == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no CDS services are configured"})
		return
	}
	id := r.PathValue("id")
	hook := strings.TrimPrefix(id, "crd-")
	known := false
	for _, h := range crd.Hooks {
		if h == hook {
			known = true
		}
	}
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such CDS service"})
		return
	}
	who, err := s.cdsAuthorised(r, id)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req crd.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request is not a CDS Hooks request: " + err.Error()})
		return
	}
	if req.Hook != hook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("this service answers %s, and the request is %s", hook, req.Hook)})
		return
	}
	resp, err := s.CRD.EvaluateContext(r.Context(), &req, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: who, Action: "crd." + hook, Target: req.HookInstance,
		Detail: fmt.Sprintf("%d card(s), %d order(s)", len(resp.Cards), len(resp.SystemActions)), IP: clientIP(r)})
	writeJSON(w, http.StatusOK, resp)
}

// crdAsk is the provider's side, in the browser: send an order to a CRD service - this one, or a payer's - and show what comes back.
type crdAsk struct {
	// URL is a remote CDS service's URL; empty means this server's own rules.
	URL string `json:"url"`
	// Token, for a remote service that takes a bearer token.
	Token    string          `json:"token"`
	Hook     string          `json:"hook"`
	Order    json.RawMessage `json:"order"`
	Coverage json.RawMessage `json:"coverage"`
	Patient  string          `json:"patientId"`
}

func (s *Server) handleCRDAsk(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var ask crdAsk
	if !s.decode(w, r, &ask) {
		return
	}
	if ask.Hook == "" {
		ask.Hook = "order-sign"
	}
	wrap := func(raw json.RawMessage) json.RawMessage {
		if len(raw) == 0 {
			return nil
		}
		b, _ := json.Marshal(map[string]any{"resourceType": "Bundle", "type": "collection",
			"entry": []any{map[string]json.RawMessage{"resource": raw}}})
		return b
	}
	key := "draftOrders"
	if ask.Hook == "appointment-book" {
		key = "appointments"
	}
	req := crd.Request{Hook: ask.Hook, HookInstance: fmt.Sprintf("perfuse-%d", time.Now().UnixNano()),
		Context:  map[string]json.RawMessage{"userId": json.RawMessage(`"Practitioner/perfuse"`), "patientId": mustJSON(ask.Patient), key: wrap(ask.Order)},
		Prefetch: map[string]json.RawMessage{"coverage": wrap(ask.Coverage)}}

	if ask.URL == "" {
		if s.CRD == nil {
			s.fail(w, r, http.StatusConflict, "this server has no CRD rules; start it with -crd-rules, or name a payer's service URL")
			return
		}
		resp, err := s.CRD.EvaluateContext(r.Context(), &req, time.Now())
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		s.ok(w, resp)
		return
	}
	if !strings.HasPrefix(ask.URL, "https://") && !s.SHLAllowHTTP {
		s.fail(w, r, http.StatusBadRequest, "a CDS service is called over https: the request carries the patient's coverage")
		return
	}
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, ask.URL, strings.NewReader(string(body)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	if ask.Token != "" {
		hreq.Header.Set("Authorization", "Bearer "+ask.Token)
	}
	res, err := s.shlClient().Do(hreq)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the CDS service could not be reached: "+err.Error())
		return
	}
	defer func() { _ = res.Body.Close() }()
	var out any
	if res.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(w, res.Body, 8<<20)).Decode(&out) != nil {
		s.fail(w, r, http.StatusBadGateway, "the CDS service answered "+res.Status)
		return
	}
	s.ok(w, out)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// dtrAsk is the console's request for a DTR questionnaire package: the order CRD answered (carrying its coverage-information) and the
// coverage, sent to this server's own DTR or to a payer's FHIR base URL.
type dtrAsk struct {
	URL      string          `json:"url"`
	Token    string          `json:"token"`
	Order    json.RawMessage `json:"order"`
	Coverage json.RawMessage `json:"coverage"`
}

func (s *Server) handleDTRPackage(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var ask dtrAsk
	if !s.decode(w, r, &ask) {
		return
	}
	params := []map[string]json.RawMessage{}
	if len(ask.Coverage) > 0 {
		params = append(params, map[string]json.RawMessage{"name": mustJSON("coverage"), "resource": ask.Coverage})
	}
	if len(ask.Order) > 0 {
		params = append(params, map[string]json.RawMessage{"name": mustJSON("order"), "resource": ask.Order})
	}
	body, _ := json.Marshal(map[string]any{"resourceType": "Parameters", "parameter": params})

	var status int
	var out []byte
	if ask.URL == "" {
		if s.DTRPackage == nil {
			s.fail(w, r, http.StatusConflict, "this server has no FHIR endpoint, so it serves no DTR; start it with -fhir, or name a payer's FHIR base URL")
			return
		}
		status, out = s.DTRPackage(r.Context(), body)
	} else {
		if !strings.HasPrefix(ask.URL, "https://") && !s.SHLAllowHTTP {
			s.fail(w, r, http.StatusBadRequest, "a payer's DTR is called over https: the request carries the patient's coverage")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ask.URL, "/")+"/Questionnaire/$questionnaire-package", strings.NewReader(string(body)))
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		hreq.Header.Set("Content-Type", "application/fhir+json")
		if ask.Token != "" {
			hreq.Header.Set("Authorization", "Bearer "+ask.Token)
		}
		res, err := s.shlClient().Do(hreq)
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "the payer's DTR could not be reached: "+err.Error())
			return
		}
		defer func() { _ = res.Body.Close() }()
		status = res.StatusCode
		out, _ = io.ReadAll(io.LimitReader(res.Body, 16<<20))
	}
	var v any
	if json.Unmarshal(out, &v) != nil {
		s.fail(w, r, http.StatusBadGateway, fmt.Sprintf("the DTR service answered %d with something that is not JSON", status))
		return
	}
	if status >= 300 {
		s.fail(w, r, http.StatusBadGateway, fmt.Sprintf("the DTR service answered %d: %s", status, string(out)))
		return
	}
	s.ok(w, v)
}

// jtiCache remembers the JWT ids seen until each token expires, so a captured CDS Hooks call cannot be replayed.
type jtiCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// first records a jti and reports whether it is new.
func (c *jtiCache) first(id string, expires time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.seen == nil {
		c.seen = map[string]time.Time{}
	}
	if len(c.seen) > 10000 {
		for k, exp := range c.seen {
			if exp.Before(now) {
				delete(c.seen, k)
			}
		}
	}
	if exp, ok := c.seen[id]; ok && exp.After(now) {
		return false
	}
	c.seen[id] = expires
	return true
}
