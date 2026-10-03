package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/shl"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SMART Health Links and Cards: "Kill the Clipboard".
//
// Two directions. Receiving: a patient shows a QR code at check-in, and this reads the link, fetches and decrypts what it points at,
// verifies any SMART Health Cards inside against their issuers' keys, and can hand the FHIR content to a channel - which is where it
// goes on into the EHR. Sharing: the visit record goes back the same way, as a link this server hosts.
//
// The hosted side is built so the server cannot read what it hosts. The key is made for the link and handed back once, inside the link;
// the server keeps only the ciphertext. A passcode, when set, is hashed, and ten wrong ones disable the link.

// shlTickets are one-time, short-lived file locations, for a receiver that asks for files by location rather than embedded.
type shlTickets struct {
	mu sync.Mutex
	m  map[string]shlTicket
}

type shlTicket struct {
	jwe     string
	expires time.Time
}

func (t *shlTickets) issue(jwe string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	id := base64.RawURLEncoding.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]shlTicket{}
	}
	now := time.Now()
	for k, v := range t.m {
		if now.After(v.expires) {
			delete(t.m, k)
		}
	}
	t.m[id] = shlTicket{jwe: jwe, expires: now.Add(time.Minute)}
	return id
}

func (t *shlTickets) redeem(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[id]
	delete(t.m, id)
	if !ok || time.Now().After(v.expires) {
		return "", false
	}
	return v.jwe, true
}

// publicBase is where a patient's phone reaches this server.
func (s *Server) publicBase() string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	return strings.TrimRight(s.SelfURL, "/")
}

type shlCreateRequest struct {
	Label string `json:"label"`
	// Content is the file: a FHIR resource (usually a Bundle), or a .smart-health-card file.
	Content     json.RawMessage `json:"content"`
	ContentType string          `json:"contentType"`
	Passcode    string          `json:"passcode"`
	// ExpiresInDays defaults to 30. Zero is not "never": a link a patient can lose should not work for ever.
	ExpiresInDays int `json:"expiresInDays"`
}

func (s *Server) handleCreateSHL(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req shlCreateRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.ContentType == "" {
		req.ContentType = "application/fhir+json"
	}
	switch req.ContentType {
	case "application/fhir+json", "application/smart-health-card":
	default:
		s.fail(w, r, http.StatusBadRequest, "contentType must be application/fhir+json or application/smart-health-card")
		return
	}
	var probe map[string]any
	if err := json.Unmarshal(req.Content, &probe); err != nil {
		s.fail(w, r, http.StatusBadRequest, "content must be a JSON object: a FHIR resource or a .smart-health-card file")
		return
	}
	if req.ContentType == "application/fhir+json" && probe["resourceType"] == nil {
		s.fail(w, r, http.StatusBadRequest, "content has no resourceType, so it is not a FHIR resource")
		return
	}
	if len(req.Label) > 80 {
		s.fail(w, r, http.StatusBadRequest, "the label is limited to 80 characters by the specification")
		return
	}
	if req.Passcode != "" && len(req.Passcode) < 4 {
		s.fail(w, r, http.StatusBadRequest, "a passcode needs at least four characters")
		return
	}
	days := req.ExpiresInDays
	if days == 0 {
		days = 30
	}
	if days < 1 || days > 365 {
		s.fail(w, r, http.StatusBadRequest, "expiresInDays must be 1 to 365")
		return
	}
	base := s.publicBase()
	if base == "" {
		s.fail(w, r, http.StatusConflict, "this server does not know its public address; start it with -public-url")
		return
	}

	id, key := shl.NewID(), shl.NewKey()
	jwe, err := shl.Encrypt(req.Content, key, req.ContentType)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	exp := time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := s.storeFor(sess).CreateSHLink(r.Context(), id, req.Label, req.ContentType, jwe, req.Passcode, &exp, sess.Username); err != nil {
		s.failErr(w, r, err)
		return
	}

	link := &shl.Link{URL: base + "/shl/" + id, Key: key, Exp: exp.Unix(), Label: req.Label}
	if req.Passcode != "" {
		link.Flag = "P"
	}
	encoded := link.Encode()
	qr, err := shl.EncodeQR(encoded, shl.ECCMedium)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "shl.create", Target: id,
		Detail: fmt.Sprintf("%s, %d day(s), passcode %v", req.ContentType, days, req.Passcode != ""), IP: clientIP(r)})

	warning := ""
	if strings.HasPrefix(base, "http://") {
		warning = "This server's public address is plain HTTP. A patient's app will refuse the link, and the manifest would cross the " +
			"network unencrypted; give -public-url an https address."
	}
	s.ok(w, map[string]any{"id": id, "link": encoded, "qrSvg": qr.SVG(), "expiresAt": exp, "warning": warning,
		"note": "This is the only time the link is shown. The server keeps the encrypted file and not the key, so it cannot show it again."})
}

func (s *Server) handleListSHL(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	links, err := s.storeFor(sess).ListSHLinks(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{"links": links})
}

func (s *Server) handleRevokeSHL(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	id := r.PathValue("id")
	if err := s.storeFor(sess).RevokeSHLink(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrSHLNotFound) {
			s.fail(w, r, http.StatusNotFound, "no such link, or it is already revoked")
			return
		}
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "shl.revoke", Target: id, IP: clientIP(r)})
	s.ok(w, map[string]string{"status": "revoked"})
}

// shlCORS lets an SHL viewer in a browser fetch the manifest, which the specification requires of a manifest server.
func shlCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Cache-Control", "no-store")
}

// handleSHLManifest answers a manifest request. Unauthenticated by design: the link is the credential, and its id is 256 random bits.
func (s *Server) handleSHLManifest(w http.ResponseWriter, r *http.Request) {
	shlCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req struct {
		Recipient         string `json:"recipient"`
		Passcode          string `json:"passcode"`
		EmbeddedLengthMax *int   `json:"embeddedLengthMax"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Recipient) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a manifest request is JSON with a recipient"})
		return
	}
	cty, jwe, err := s.Store.OpenSHLink(r.Context(), r.PathValue("id"), req.Passcode, req.Recipient)
	var pe *store.ErrSHLPasscode
	switch {
	case errors.As(err, &pe):
		writeJSON(w, http.StatusUnauthorized, map[string]int{"remainingAttempts": pe.Remaining})
		return
	case errors.Is(err, store.ErrSHLNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no longer valid"})
		return
	case err != nil:
		s.log().Error("an SHL manifest request failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the link could not be opened"})
		return
	}
	file := map[string]string{"contentType": cty}
	if req.EmbeddedLengthMax != nil && len(jwe) > *req.EmbeddedLengthMax {
		file["location"] = s.publicBase() + "/shl/file/" + s.shlFiles.issue(jwe)
	} else {
		file["embedded"] = jwe
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": []map[string]string{file}})
}

// handleSHLFile serves a file by a one-time location from a manifest response.
func (s *Server) handleSHLFile(w http.ResponseWriter, r *http.Request) {
	shlCORS(w)
	jwe, ok := s.shlFiles.redeem(r.PathValue("ticket"))
	if !ok {
		http.Error(w, "no longer valid", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/jose")
	_, _ = w.Write([]byte(jwe))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// shlClient fetches a stranger's link. The URL comes from a QR code, so the dial is held to the egress policy at connect time - after
// DNS, so a name that resolves to a metadata address is caught - and plain HTTP is refused unless the server was told otherwise.
func (s *Server) shlClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		return egress.Default.Check(host)
	}}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second}
	allowHTTP := s.SHLAllowHTTP
	return &http.Client{Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && !allowHTTP {
				return errors.New("redirected to plain HTTP")
			}
			return nil
		}}
}

type shlResolveRequest struct {
	Link      string `json:"link"`
	Passcode  string `json:"passcode"`
	Recipient string `json:"recipient"`
	// Channel, when set, receives each FHIR file: the route into the EHR.
	Channel string `json:"channel"`
}

type shlResolvedFile struct {
	ContentType string     `json:"contentType"`
	Summary     fhirDigest `json:"summary"`
	Content     string     `json:"content"`
	Cards       []shl.Card `json:"cards,omitempty"`
	Routed      string     `json:"routed,omitempty"`
}

// fhirDigest says what a bundle holds, for the person deciding whether to accept it.
type fhirDigest struct {
	ResourceType string         `json:"resourceType,omitempty"`
	Counts       map[string]int `json:"counts"`
	Patient      string         `json:"patient,omitempty"`
	BirthDate    string         `json:"birthDate,omitempty"`
}

func digest(raw []byte) fhirDigest {
	d := fhirDigest{Counts: map[string]int{}}
	var res struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return d
	}
	d.ResourceType = res.ResourceType
	look := func(r json.RawMessage) {
		var p struct {
			ResourceType string `json:"resourceType"`
			BirthDate    string `json:"birthDate"`
			Name         []struct {
				Family string   `json:"family"`
				Given  []string `json:"given"`
			} `json:"name"`
		}
		if json.Unmarshal(r, &p) != nil {
			return
		}
		d.Counts[p.ResourceType]++
		if p.ResourceType == "Patient" && d.Patient == "" && len(p.Name) > 0 {
			d.Patient = strings.TrimSpace(strings.Join(p.Name[0].Given, " ") + " " + p.Name[0].Family)
			d.BirthDate = p.BirthDate
		}
	}
	if res.ResourceType == "Bundle" {
		for _, e := range res.Entry {
			look(e.Resource)
		}
	} else {
		look(raw)
	}
	return d
}

func (s *Server) handleResolveSHL(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req shlResolveRequest
	if !s.decode(w, r, &req) {
		return
	}
	link, err := shl.Parse(req.Link)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// The tenant's own runtime, resolved before fetching anything: delivering a patient's records into another tenant's channel is
	// the failure that matters here.
	var runtime *Runtime
	if req.Channel != "" {
		rt, ok := s.runtimeFor(w, r, sess)
		if !ok {
			return
		}
		runtime = rt
	}
	if strings.HasPrefix(link.URL, "http://") && !s.SHLAllowHTTP {
		s.fail(w, r, http.StatusBadRequest, "the link points at plain HTTP, which is refused: a patient's records would cross the network "+
			"unencrypted")
		return
	}
	recipient := strings.TrimSpace(req.Recipient)
	if recipient == "" {
		recipient = "Perfuse (" + sess.Username + ")"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	client := s.shlClient()
	files, err := (&shl.Resolver{Client: client}).Resolve(ctx, link, recipient, req.Passcode)
	if err != nil {
		var pe *shl.PasscodeError
		if errors.As(err, &pe) {
			s.fail(w, r, http.StatusUnauthorized, err.Error())
			return
		}
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}

	out := []shlResolvedFile{}
	for _, f := range files {
		rf := shlResolvedFile{ContentType: f.ContentType}
		switch f.ContentType {
		case "application/fhir+json":
			rf.Summary, rf.Content = digest(f.Content), string(f.Content)
		case "application/smart-health-card":
			jwss, err := shl.CardsFrom(string(f.Content))
			if err == nil {
				for _, jws := range jwss {
					if c, err := shl.ReadCard(ctx, jws, shl.HTTPKeys(client)); err == nil {
						rf.Cards = append(rf.Cards, *c)
					}
				}
			}
		default:
			// A SMART access token or something newer: listed, not opened. Using a token is a FHIR client's job, not a reader's.
		}
		if req.Channel != "" && rf.Content != "" {
			if err := runtime.Route(ctx, req.Channel, []byte(rf.Content)); err != nil {
				rf.Routed = "not routed: " + err.Error()
			} else {
				rf.Routed = "delivered to " + req.Channel
			}
		}
		out = append(out, rf)
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "shl.receive",
		Target: strings.Split(link.URL, "?")[0], Detail: fmt.Sprintf("%d file(s)%s", len(out), map[bool]string{true: ", routed to " + req.Channel}[req.Channel != ""]),
		IP: clientIP(r)})
	s.ok(w, map[string]any{"label": link.Label, "files": out})
}

func (s *Server) handleVerifySHC(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req struct {
		Text string `json:"text"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	jwss, err := shl.CardsFrom(req.Text)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cards := []shl.Card{}
	for _, jws := range jwss {
		c, err := shl.ReadCard(ctx, jws, shl.HTTPKeys(s.shlClient()))
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		cards = append(cards, *c)
	}
	s.ok(w, map[string]any{"cards": cards})
}
