package api

import (
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/store"
)

// fleetResponse is the whole fleet as one payload.
type fleetResponse struct {
	// Configured reports whether any peers are set up at all. Without it the interface cannot tell "no peers
	// configured" from "peers configured and all unreachable", which are opposite situations.
	Configured bool `json:"configured"`

	// Self is this instance, reported the same way as any peer so the interface renders one list rather than
	// special-casing the machine it happens to be talking to. Symmetric by construction: there is no master here.
	Self fleetMember `json:"self"`

	Peers []peers.Status `json:"peers"`

	Roll peers.Roll `json:"roll"`

	// PollEverySeconds lets the interface say how fresh these numbers can possibly be, rather than implying they
	// are live.
	PollEverySeconds float64 `json:"pollEverySeconds"`

	// AnyControllable reports whether remote control is permitted for any peer, so the interface can omit the
	// buttons entirely rather than showing ones that will be refused.
	AnyControllable bool `json:"anyControllable"`
}

// fleetMember is this instance shaped like a peer status.
type fleetMember struct {
	Name         string             `json:"name"`
	Reachability peers.Reachability `json:"reachability"`
	Version      string             `json:"version,omitempty"`
	Health       *peers.Health      `json:"health,omitempty"`
	CheckedAt    time.Time          `json:"checkedAt"`
	Summary      string             `json:"summary"`
}

// handleFleet returns the fleet view.
//
// Viewer role: this is monitoring, and the payload carries counts only.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	self := s.selfReport(r)

	selfMember := fleetMember{
		Name:         self.Label,
		Reachability: peers.Reachable,
		Version:      self.Version,
		Health:       &self.Health,
		CheckedAt:    self.Now,
	}
	// Reusing the peer summary wording for this instance, so the sentence describing the local server and the
	// sentence describing a remote one cannot drift apart and start meaning different things.
	selfMember.Summary = peers.Status{
		Reachability: peers.Reachable,
		Health:       &self.Health,
	}.Summary()

	out := fleetResponse{
		Self:             selfMember,
		PollEverySeconds: peers.DefaultPollEvery.Seconds(),
		// Initialised here rather than on the configured path, because the unconfigured path returns early and is the
		// common case - a single instance with no fleet. Guarding only the configured branch fixed the rare case and
		// left the one every new install hits, which is how this reached a browser as `null.length`.
		Peers: []peers.Status{},
	}

	if s.Fleet == nil {
		// A single instance is still a fleet of one, and reporting it that way means the interface has one code
		// path rather than an empty state that has to invent numbers.
		out.Roll = peers.Rollup(self, nil)
		s.ok(w, out)
		return
	}

	statuses := s.Fleet.Statuses()

	// Configured now means "at least one peer", not "a fleet object exists".
	//
	// It used to be the same thing, because the fleet was only created when a peers file was given. Now it is
	// always created so peers can be added from the interface, and reporting configured for a fleet of one made
	// the section render its populated layout - which has nowhere to add a peer. The original intent survives:
	// this still distinguishes "no peers" from "peers configured and all unreachable".
	out.Configured = len(statuses) > 0
	if statuses != nil {
		out.Peers = statuses
	}
	out.Roll = peers.Rollup(self, statuses)
	out.AnyControllable = s.Fleet.Controllable()
	out.PollEverySeconds = s.Fleet.PollEverySeconds()

	s.ok(w, out)
}
