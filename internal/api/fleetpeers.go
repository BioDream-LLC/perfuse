package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/store"
	"gopkg.in/yaml.v3"
)

// Adding another instance to the fleet view, from the interface.
//
// The Fleet section had no controls at all. It explained what a fleet was, said "this is a fleet of one", and
// printed a command to run in a terminal on the other machine. Everything after that - putting the token
// somewhere, writing a peers file, restarting - was undocumented on screen and impossible from here.
//
// Found by the control sweep, which reported the section as having nothing operable in it. That was the whole
// finding: a page that could only be read, about a feature that could only be configured elsewhere.

// peerView is one peer as the interface sees it.
//
// The token is deliberately absent. It is a credential for another instance, and echoing it back to every
// administrator who opens this page turns one leaked session into access to the whole fleet. Whether one is set
// is reported, because that is what somebody needs to know.
type peerView struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	HasToken     bool   `json:"hasToken"`
	AllowControl bool   `json:"allowControl"`
}

type peerListResponse struct {
	// Peers is never null: the interface reads .length on it, and an empty fleet is the normal starting state.
	Peers []peerView `json:"peers"`

	// Writable reports whether peers can be changed here at all. False when the server was given no place to
	// store them, which is worth saying rather than letting a save fail.
	Writable bool `json:"writable"`

	// File is where they are stored, so somebody managing configuration in version control knows what to commit.
	File string `json:"file,omitempty"`
}

type peerWriteRequest struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Token        string `json:"token"`
	AllowControl bool   `json:"allowControl"`
}

// handleListPeers returns the fleet's peers, without their tokens.
func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	resp := peerListResponse{
		Peers:    []peerView{},
		Writable: s.PeersFile != "",
		File:     s.PeersFile,
	}

	if s.Fleet != nil {
		for _, p := range s.Fleet.Peers() {
			resp.Peers = append(resp.Peers, peerView{
				Name:         p.Name,
				URL:          p.URL,
				HasToken:     p.Token != "",
				AllowControl: p.AllowControl,
			})
		}
	}

	s.ok(w, resp)
}

// handleAddPeer adds or replaces one peer.
func (s *Server) handleAddPeer(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req peerWriteRequest
	if !s.decode(w, r, &req) {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.Token = strings.TrimSpace(req.Token)

	if req.Name == "" {
		// Required because a URL is not a name anybody recognises at three in the morning, which is when this
		// page gets read.
		s.fail(w, r, http.StatusBadRequest, "give this instance a name you will recognise in a hurry")
		return
	}
	if err := checkPeerURL(req.URL); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Token == "" {
		s.fail(w, r, http.StatusBadRequest,
			"a read-only token from the other instance is needed; create one there under Users, then paste it here")
		return
	}

	if s.Fleet == nil || s.PeersFile == "" {
		s.fail(w, r, http.StatusConflict,
			"this server has nowhere to store fleet peers, so they cannot be changed here")
		return
	}

	// Refused rather than allowed to point at itself.
	//
	// A self-referencing peer polls this instance through its own HTTP stack on a timer and reports it twice in
	// the fleet view, once as "this server" and once as a peer - which looks like two machines and is one.
	if s.SelfURL != "" && sameHost(req.URL, s.SelfURL) {
		s.fail(w, r, http.StatusBadRequest,
			"that address is this instance; a fleet watches other servers, and this one is already shown")
		return
	}

	existing := s.Fleet.Peers()
	replaced := false
	for i := range existing {
		if strings.EqualFold(existing[i].Name, req.Name) {
			existing[i] = peers.Peer{
				Name:         req.Name,
				URL:          req.URL,
				Token:        req.Token,
				AllowControl: req.AllowControl,
			}
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, peers.Peer{
			Name:         req.Name,
			URL:          req.URL,
			Token:        req.Token,
			AllowControl: req.AllowControl,
		})
	}

	if err := s.savePeers(existing); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "fleet.peer.add",
		Target: req.Name,
		Detail: fmt.Sprintf("%s, control %v", req.URL, req.AllowControl),
		IP:     clientIP(r),
	})

	s.ok(w, map[string]any{"name": req.Name, "replaced": replaced})
}

// handleRemovePeer stops watching one instance.
func (s *Server) handleRemovePeer(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	if s.Fleet == nil || s.PeersFile == "" {
		s.fail(w, r, http.StatusConflict, "this server has no fleet configuration to change")
		return
	}

	before := s.Fleet.Peers()
	kept := make([]peers.Peer, 0, len(before))
	for _, p := range before {
		if !strings.EqualFold(p.Name, name) {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(before) {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("no peer called %q is being watched", name))
		return
	}

	if err := s.savePeers(kept); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "fleet.peer.remove",
		Target: name, IP: clientIP(r),
	})

	s.ok(w, map[string]any{"removed": name})
}

// savePeers writes the peer list and applies it without a restart.
//
// Written first, applied second. If the write fails the running configuration is untouched, so a full disk
// produces a refusal rather than a fleet that works until the next restart and then silently loses a peer.
func (s *Server) savePeers(list []peers.Peer) error {
	cfg := peers.Config{Label: s.currentFleetLabel(), Peers: list}

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	header := []byte("# Other Perfuse instances watched from this one.\n" +
		"#\n" +
		"# Written by the Fleet section. Tokens in here are credentials for those instances, so this file\n" +
		"# deserves the same protection as a password.\n")

	tmp := s.PeersFile + ".tmp"
	// 0600: the tokens in here are credentials for other servers.
	if err := os.WriteFile(tmp, append(header, raw...), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.PeersFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	s.Fleet.SetPeers(list)
	return nil
}

// currentFleetLabel reads the label under the lock that guards it.
//
// The field's own comment says the race detector is right to object to reading it without one, and one reader was
// doing exactly that. An accessor means the next reader cannot get it wrong.
func (s *Server) currentFleetLabel() string {
	s.fleetLabelMu.Lock()
	defer s.fleetLabelMu.Unlock()
	return s.FleetLabel
}

// checkPeerURL refuses addresses that cannot work, with the reason.
func checkPeerURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("the other instance's address is needed, for example https://perfuse-02.hospital.internal:8443")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("that address cannot be read: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		// Named rather than assumed. A pasted "perfuse-02:8443" parses without error and then fails every poll
		// with something unhelpful about a missing protocol.
		return fmt.Errorf("the address needs to start with https:// or http://")
	}
	if u.Host == "" {
		return fmt.Errorf("the address has no host in it")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Host) {
		// Not refused, because segregated hospital networks are a real deployment and this product supports
		// them. But the token is a credential and it will cross that network in the clear.
		return fmt.Errorf("that address is plain HTTP, so the token would cross the network unencrypted; " +
			"use https:// unless this is loopback")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// sameHost reports whether two addresses point at the same place, well enough to catch the obvious case.
func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}
