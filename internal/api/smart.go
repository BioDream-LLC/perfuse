package api

// Users → SMART apps: the built-in SMART authorization server's registered apps and the people who sign in to authorize
// them, edited in the console instead of by hand in YAML. Secrets and password hashes never leave the server: a client secret
// is shown once, when it is generated, and a password is only ever set.

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/biodream-llc/perfuse/internal/smartauth"
	"github.com/biodream-llc/perfuse/internal/store"
)

type smartClientView struct {
	ID           string         `json:"id"`
	Name         string         `json:"name,omitempty"`
	Kind         string         `json:"kind"`
	Scopes       []string       `json:"scopes"`
	RedirectURIs []string       `json:"redirectUris,omitempty"`
	LaunchURL    string         `json:"launchUrl,omitempty"`
	JWKSURI      string         `json:"jwksUri,omitempty"`
	JWKS         map[string]any `json:"jwks,omitempty"`
	HasSecret    bool           `json:"hasSecret,omitempty"`
}

type smartUserView struct {
	Username    string `json:"username"`
	Name        string `json:"name,omitempty"`
	FHIRUser    string `json:"fhirUser"`
	OIDCSubject string `json:"oidcSubject,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

// smartEdits serializes edits, so two administrators saving at once cannot each write a list missing the other's change.
var smartEdits sync.Mutex

func (s *Server) requireSMART(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	if s.SMART == nil {
		s.fail(w, r, http.StatusNotFound, "the SMART authorization server is not running (serve -fhir -smart-clients)")
		return false
	}
	return s.requirePlatform(w, r, sess, "the SMART authorization server")
}

func (s *Server) handleSMARTDirectory(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.SMART == nil {
		s.ok(w, map[string]any{"enabled": false})
		return
	}
	if !s.requirePlatform(w, r, sess, "the SMART authorization server") {
		return
	}
	clients := []smartClientView{}
	for _, c := range s.SMART.ClientList() {
		clients = append(clients, smartClientView{ID: c.ID, Name: c.Name, Kind: c.Kind, Scopes: c.Scopes, RedirectURIs: c.RedirectURIs,
			LaunchURL: c.LaunchURL, JWKSURI: c.JWKSURI, JWKS: c.JWKS, HasSecret: c.SecretHash != ""})
	}
	users := []smartUserView{}
	for _, u := range s.SMART.UserList() {
		users = append(users, smartUserView{Username: u.Username, Name: u.Name, FHIRUser: u.FHIRUser, OIDCSubject: u.OIDCSubject,
			HasPassword: u.PasswordHash != ""})
	}
	body := map[string]any{"enabled": true, "issuer": s.SMART.Issuer, "clients": clients, "users": users,
		"signIn": s.SMARTUsersFile != "", "clientsFile": s.SMARTClientsFile != ""}
	if s.SMART.Upstream != nil {
		body["upstream"] = map[string]string{"label": s.SMART.Upstream.Label, "issuer": s.SMART.Upstream.Provider.Issuer,
			"redirectUri": s.SMART.UpstreamRedirect()}
	}
	s.ok(w, body)
}

func (s *Server) handleSMARTClientSave(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSMART(w, r, sess) {
		return
	}
	smartEdits.Lock()
	defer smartEdits.Unlock()
	var in struct {
		smartClientView
		// Secret sets a confidential-symmetric client's secret; empty keeps the one it has, or generates one for a new client.
		Secret string `json:"secret"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	c := &smartauth.Client{ID: id, Name: strings.TrimSpace(in.Name), Kind: in.Kind, Scopes: trimAll(in.Scopes),
		RedirectURIs: trimAll(in.RedirectURIs), LaunchURL: strings.TrimSpace(in.LaunchURL), JWKSURI: strings.TrimSpace(in.JWKSURI), JWKS: in.JWKS}
	list := s.SMART.ClientList()
	at := slices.IndexFunc(list, func(o *smartauth.Client) bool { return o.ID == id })
	generated := ""
	if c.Kind == smartauth.KindSymmetric {
		switch {
		case in.Secret != "":
			c.SecretHash, _ = store.HashPassword(in.Secret)
		case at >= 0 && list[at].SecretHash != "":
			c.SecretHash = list[at].SecretHash
		default:
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			generated = base64.RawURLEncoding.EncodeToString(b)
			c.SecretHash, _ = store.HashPassword(generated)
		}
	}
	if at >= 0 {
		list[at] = c
	} else {
		list = append(list, c)
	}
	saved, err := smartauth.SaveClients(s.SMARTClientsFile, list)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.SMART.SetClients(saved)
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "smart.client.save", Target: id, IP: clientIP(r)})
	body := map[string]any{"id": id}
	if generated != "" {
		body["secret"] = generated
	}
	s.ok(w, body)
}

func (s *Server) handleSMARTClientDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSMART(w, r, sess) {
		return
	}
	smartEdits.Lock()
	defer smartEdits.Unlock()
	id := r.PathValue("id")
	list := slices.DeleteFunc(s.SMART.ClientList(), func(c *smartauth.Client) bool { return c.ID == id })
	saved, err := smartauth.SaveClients(s.SMARTClientsFile, list)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.SMART.SetClients(saved)
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "smart.client.delete", Target: id, IP: clientIP(r)})
	s.ok(w, map[string]any{"deleted": id})
}

func (s *Server) handleSMARTUserSave(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSMART(w, r, sess) {
		return
	}
	smartEdits.Lock()
	defer smartEdits.Unlock()
	if s.SMARTUsersFile == "" {
		s.fail(w, r, http.StatusConflict, "people sign in only with -smart-users; start the server with a users file to add them")
		return
	}
	var in struct {
		smartUserView
		// Password sets the password; empty keeps the one the person has.
		Password string `json:"password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	name := r.PathValue("username")
	u := &smartauth.User{Username: name, Name: strings.TrimSpace(in.Name), FHIRUser: strings.TrimSpace(in.FHIRUser),
		OIDCSubject: strings.TrimSpace(in.OIDCSubject)}
	list := s.SMART.UserList()
	at := slices.IndexFunc(list, func(o *smartauth.User) bool { return o.Username == name })
	switch {
	case in.Password != "":
		if len(in.Password) < 12 {
			s.fail(w, r, http.StatusUnprocessableEntity, "a password needs at least 12 characters")
			return
		}
		u.PasswordHash, _ = store.HashPassword(in.Password)
	case at >= 0:
		u.PasswordHash = list[at].PasswordHash
	}
	if at >= 0 {
		list[at] = u
	} else {
		list = append(list, u)
	}
	saved, err := smartauth.SaveUsers(s.SMARTUsersFile, list)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.SMART.SetUsers(saved)
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "smart.user.save", Target: name, IP: clientIP(r)})
	s.ok(w, map[string]any{"username": name})
}

func (s *Server) handleSMARTUserDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSMART(w, r, sess) {
		return
	}
	smartEdits.Lock()
	defer smartEdits.Unlock()
	name := r.PathValue("username")
	list := slices.DeleteFunc(s.SMART.UserList(), func(u *smartauth.User) bool { return u.Username == name })
	saved, err := smartauth.SaveUsers(s.SMARTUsersFile, list)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.SMART.SetUsers(saved)
	_ = s.storeFor(sess).Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "smart.user.delete", Target: name, IP: clientIP(r)})
	s.ok(w, map[string]any{"deleted": name})
}

func trimAll(v []string) []string {
	var out []string
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
