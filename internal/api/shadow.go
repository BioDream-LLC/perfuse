package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/store"
)

// shadowSummary is one channel's comparison, for the list.
type shadowSummary struct {
	Channel   string `json:"channel"`
	Candidate string `json:"candidate"`
	Compared  int64  `json:"compared"`
	Differed  int64  `json:"differed"`
	Verdict   string `json:"verdict"`
}

// handleShadows lists every channel currently shadowing a candidate.
func (s *Server) handleShadows(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Runtime == nil {
		s.fail(w, r, http.StatusNotImplemented,
			"no engine is running, so nothing is being shadowed")
		return
	}

	out := []shadowSummary{}
	for _, ch := range s.Runtime.RunningChannels() {
		rep := ch.ShadowReport()
		if rep == nil {
			continue
		}
		out = append(out, shadowSummary{
			Channel:   rep.Channel,
			Candidate: rep.Candidate,
			Compared:  rep.Stats.Compared,
			Differed:  rep.Stats.Differed,
			Verdict:   rep.Verdict,
		})
	}

	s.ok(w, map[string]any{"shadows": out})
}

// handleShadow returns one channel's full comparison, differences included.
func (s *Server) handleShadow(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Runtime == nil {
		s.fail(w, r, http.StatusNotImplemented, "no engine is running")
		return
	}

	name := r.PathValue("name")
	ch, ok := s.Runtime.Channel(name)
	if !ok {
		s.fail(w, r, http.StatusNotFound, "channel "+name+" is not running")
		return
	}

	rep := ch.ShadowReport()
	if rep == nil {
		// Not an error. A channel without a shadow is the normal case, and the interface
		// needs to be able to ask without treating the answer as a fault.
		s.ok(w, map[string]any{
			"channel": name,
			"shadow":  nil,
			"note": "this channel is not shadowing a candidate. Add a shadow block " +
				"naming a candidate channel file to compare one against it.",
		})
		return
	}

	s.ok(w, rep)
}

// RunningChannels returns the channels currently started.
//
// Copied under the lock rather than handing out the map, so a caller iterating it
// cannot race a channel being started or stopped underneath them.
func (rt *Runtime) RunningChannels() []*engine.Channel {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	out := make([]*engine.Channel, 0, len(rt.running))
	for _, ch := range rt.running {
		out = append(out, ch)
	}
	return out
}

// Channel returns one running channel by name.
func (rt *Runtime) Channel(name string) (*engine.Channel, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	ch, ok := rt.running[name]
	return ch, ok
}
