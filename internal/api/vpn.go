package api

import (
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/vpn"
)

// handleVPN is every listed tunnel's state and whether its two sides' settings agree.
func (s *Server) handleVPN(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.VPN == nil {
		s.ok(w, map[string]any{"tunnels": []vpn.Status{}})
		return
	}
	if !s.requirePlatform(w, r, sess, "VPN tunnel state") {
		return
	}
	s.ok(w, map[string]any{"tunnels": s.VPN.Statuses(r.Context())})
}

// handleVPNSheet is one partner's connection sheet, Markdown, to send them. It holds no pre-shared key.
func (s *Server) handleVPNSheet(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	t := s.VPN.Tunnel(r.PathValue("name"))
	if t == nil {
		s.fail(w, r, http.StatusNotFound, "no tunnel of that name is listed in the -vpn file")
		return
	}
	if !s.requirePlatform(w, r, sess, "VPN tunnel state") {
		return
	}
	name := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			return c
		}
		return '-'
	}, t.Name)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="vpn-`+name+`.md"`)
	_, _ = w.Write([]byte(vpn.Sheet(t, s.VPN.Status(r.Context(), t))))
}
