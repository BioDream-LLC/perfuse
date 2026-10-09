package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/vpn"
)

func TestTheVPNTileAndConnectionSheet(t *testing.T) {
	h := newHarness(t)
	out := "p: #1, ESTABLISHED, IKEv2, a_i* b_r\n  remote 'p' @ 203.0.113.10[4500]\n  c: #1, reqid 1, INSTALLED, TUNNEL, ESP:AES_GCM_16-256\n"
	h.server.VPN = &vpn.Monitor{File: &vpn.File{Tunnels: []*vpn.Tunnel{{Name: "p", Partner: "Partner Co",
		StrongSwan: &vpn.StrongSwanSource{Connection: "p"}, Theirs: vpn.Settings{PeerAddress: "198.51.100.1"}}}},
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }}
	h.handler = h.server.Handler()

	f := figures(t, h, "vpn-tunnels")["vpn-tunnels"]
	if got := rowsText(f); !strings.Contains(got, "Partner Co (p)=up[ok]") || len(f.Links) != 1 || f.Links[0].Href != "/api/vpn/p/sheet" {
		t.Errorf("tile: %s %+v", got, f.Links)
	}
	rec := h.do("viewer", http.MethodGet, "/api/vpn/p/sheet", nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "vpn-p.md") ||
		!strings.Contains(rec.Body.String(), "| Peer (outside) address | 203.0.113.10 | 198.51.100.1 |") {
		t.Errorf("sheet: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("viewer", http.MethodGet, "/api/vpn/nope/sheet", nil); rec.Code != 404 {
		t.Errorf("unknown tunnel: %d", rec.Code)
	}
	if rec := h.do("viewer", http.MethodGet, "/api/vpn", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"up"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
}
