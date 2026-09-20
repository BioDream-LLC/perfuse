package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/shadow"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Asking what a change would have done, before making it.
//
// POST /api/channels/{name}/replay takes candidate YAML and runs it against messages already
// recorded for that channel, alongside the version running now, and reports where the two
// disagree.
//
// Gated on editor rather than viewer. It compiles arbitrary filter expressions and scripts from
// the request body, exactly as validation does, and the fact that it cannot deliver anything
// does not make compiling somebody else's script a read operation.

type replayRequest struct {
	// YAML is the candidate channel. Required.
	YAML string `json:"yaml"`

	// Limit is how many recent messages to examine. Zero uses a default.
	Limit int `json:"limit,omitempty"`

	// Ignore lists paths whose differences do not matter. Timestamps and control IDs
	// belong here: they differ on every message and say nothing about whether a change is
	// safe, so without this the report is all noise.
	Ignore []string `json:"ignore,omitempty"`

	// Compare narrows the comparison to these paths. Empty compares every populated field.
	Compare []string `json:"compare,omitempty"`
}

type replayResponse struct {
	*engine.ReplayReport

	// Available is how many stored messages the channel has bodies for, so the reader can
	// see what fraction of history was examined. A clean report over the last thousand of
	// two hundred thousand is weaker evidence than one over the last thousand of eleven
	// hundred, and the number is the only way to tell those apart.
	Available int `json:"available"`

	// Problems carries the reason a candidate could not be built at all.
	Problems []validateProblem `json:"problems,omitempty"`
}

// handleReplayChannel runs a candidate against recorded traffic.
func (s *Server) handleReplayChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	var req replayRequest
	if !s.decode(w, r, &req) {
		return
	}

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}
	messageStore := rt.Messages

	current, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	candidate, err := config.Load(bytes.NewReader([]byte(req.YAML)), "(candidate)")
	if err != nil {
		// Reported as problems rather than as a failure, because a candidate that does not
		// load yet is the normal state of a channel somebody is still editing.
		s.ok(w, replayResponse{Problems: problemsFromError(err)})
		return
	}

	messages, err := messageStore.RecentPayloads(r.Context(), name, req.Limit)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(messages) == 0 {
		// Two very different situations, and they were reported identically.
		//
		// A channel that has never received anything needs somebody to look at the feed. A channel with forty
		// thousand records and payload storage switched off needs somebody to look at a setting. Telling the
		// second person "no messages have been recorded for this channel yet" sends them to check a feed that is
		// working perfectly.
		records, countErr := messageStore.CountRecords(r.Context(), name)
		if countErr == nil && records > 0 {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf(
				"this channel has %d recorded message(s) but none of their bodies were kept, so there "+
					"is nothing to replay. Message contents are stored only while "+
					"\"Keep message contents\" is on, under Settings, Data - and a change can still "+
					"be checked by shadowing it against traffic as it arrives", records))

			return
		}

		s.fail(w, r, http.StatusConflict,
			"no messages have been recorded for this channel yet, so there is nothing to compare "+
				"against; a change can still be checked by shadowing it against traffic as it arrives")
		return
	}

	available, err := messageStore.CountPayloads(r.Context(), name)
	if err != nil {
		// Not fatal. The report is still useful without knowing the total; it just cannot
		// say what fraction was covered.
		available = len(messages)
	}

	ignore := make(map[string]bool, len(req.Ignore))
	for _, p := range req.Ignore {
		ignore[strings.ToUpper(p)] = true
	}

	report, err := engine.Replay(r.Context(), current, candidate, messages,
		shadow.DiffOptions{Compare: req.Compare, Ignore: ignore}, s.log())
	if err != nil {
		// A cancelled replay returns what it managed, which is worth having: somebody who
		// navigated away is gone, but a timeout partway through still tells them something.
		if report != nil && report.Examined > 0 {
			s.ok(w, replayResponse{ReplayReport: report, Available: available})
			return
		}
		s.failErr(w, r, err)
		return
	}

	s.ok(w, replayResponse{ReplayReport: report, Available: available})
}
