package api

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Configuring a shadow, which could previously only be done by editing YAML.
//
// # Why this exists
//
// The builder's drift guard excused the whole shadow block with the reason "configured from the shadow tab, where the candidate can
// be chosen from a list of real channels". That was a good reason and it was not true: the shadow tab was read-only, there was no
// endpoint to write a shadow, and the only way to start one was to edit a channel file by hand.
//
// A guard excuse resting on a capability that does not exist is worse than no excuse, because it silences the check that would have
// found the gap. This is the second one found today; the first was a doc comment claiming four TEFCA exchange patterns that made no
// network call.
//
// # Why the candidate is picked from a list
//
// The original reason still holds. A shadow names a candidate channel file, and typing that path from memory gets it wrong in a way
// that fails at load rather than at save - so somebody sees a channel disappear and does not connect it to what they just typed. The
// handler therefore refuses a candidate that is not a channel this instance knows about, and says which ones it does.

// shadowRequest is what the interface sends to start or change a comparison.
type shadowRequest struct {
	// Candidate is the name of the channel to compare against, not a path. The server resolves it, because a name is what the
	// interface can offer from a list and a path is what somebody mistypes.
	Candidate string `json:"candidate"`

	// Sample is a percentage, 1 to 100, because a fraction between 0 and 1 typed into a box is the mistake that makes a shadow
	// silently observe almost nothing. Zero means unset and is treated as everything.
	Sample int `json:"sample"`

	// Ignore is field paths to exclude, which is the setting that makes a shadow usable at all on a channel that stamps a
	// timestamp - without it every message differs and the report says nothing.
	Ignore []string `json:"ignore"`

	// Compare limits the comparison to these paths. Empty compares everything.
	Compare []string `json:"compare"`

	// MaxDifferences bounds how many differing messages are kept. Zero means the default.
	MaxDifferences int `json:"maxDifferences"`
}

// handleSaveShadow starts or changes a channel's comparison against a candidate.
func (s *Server) handleSaveShadow(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	var req shadowRequest
	if !s.decode(w, r, &req) {
		return
	}

	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	candidate := strings.TrimSpace(req.Candidate)
	if candidate == "" {
		s.fail(w, r, http.StatusBadRequest,
			"a comparison needs a candidate channel to compare against")

		return
	}
	if candidate == name {
		// Caught here rather than at load, because the message a loader gives for this is about a cycle and does not say the
		// obvious thing.
		s.fail(w, r, http.StatusBadRequest,
			"a channel cannot shadow itself, because there would be nothing to compare")

		return
	}

	// The candidate has to be a channel that exists. A shadow naming a file that is not there makes the live channel invalid, and
	// Perfuse refuses a channel wholesale when anything it references is broken - so a typo does not produce a warning, it makes
	// the channel disappear.
	// Broken channels are deliberately included as candidates.
	//
	// A channel that does not currently load is exactly the sort of thing somebody is trying to bring up, and refusing to compare
	// against it would make the tool unavailable at the moment it is most useful. The shadow itself will report the failure.
	others, broken, err := channels.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	known := make([]string, 0, len(others))
	found := false

	for _, c := range others {
		if c.Name == name {
			continue
		}

		known = append(known, c.Name)

		if c.Name == candidate {
			found = true
		}
	}

	for _, b := range broken {
		if b == name {
			continue
		}

		if b == candidate {
			found = true
		}

		known = append(known, b)
	}

	sort.Strings(known)

	if !found {
		s.fail(w, r, http.StatusBadRequest,
			"there is no channel called "+candidate+" to compare against", known...)

		return
	}

	if req.Sample < 0 || req.Sample > 100 {
		s.fail(w, r, http.StatusBadRequest,
			"the share of messages to compare is a percentage, so it has to be between 1 and 100")

		return
	}

	shadow := &config.Shadow{
		Channel:        candidate + ".yaml",
		Ignore:         trimmed(req.Ignore),
		Compare:        trimmed(req.Compare),
		MaxDifferences: req.MaxDifferences,
	}

	// Stored as a fraction because that is what the file format uses, and entered as a percentage because that is what somebody
	// means. Sending 5 for five percent into a field that wants 0.05 is the mistake this conversion exists to prevent: the file
	// loads, nothing complains, and every message is shadowed.
	if req.Sample > 0 && req.Sample < 100 {
		shadow.Sample = float64(req.Sample) / 100
	}

	cfg.Shadow = shadow

	if err := channels.Save(cfg); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "shadow.save",
		Target: name, Detail: "comparing against " + candidate, IP: clientIP(r),
	})

	s.ok(w, map[string]any{
		"channel":   name,
		"candidate": candidate,
		"file":      filepath.Base(shadow.Channel),
	})
}

// handleStopShadow removes a channel's comparison.
func (s *Server) handleStopShadow(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	if cfg.Shadow == nil {
		// Not an error. Stopping something that is not running is the state the caller wanted, and reporting it as a failure
		// makes an interface show a red box for a button that did what it said.
		s.ok(w, map[string]any{"channel": name, "wasComparing": false})

		return
	}

	cfg.Shadow = nil

	if err := channels.Save(cfg); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "shadow.stop",
		Target: name, IP: clientIP(r),
	})

	// The candidate file is deliberately left alone. It is meant to become the live channel, so deleting it because somebody
	// stopped comparing would throw away the work the comparison existed to validate.
	s.ok(w, map[string]any{"channel": name, "wasComparing": true})
}

// trimmed drops blank entries and surrounding space, and returns nil for nothing.
//
// nil rather than an empty slice because these are omitempty in the file format, and an empty list would write "ignore: []" into a
// channel - which reads as a decision to ignore nothing in particular rather than as an absent setting.
func trimmed(in []string) []string {
	out := make([]string, 0, len(in))

	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}
