package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
)

// handleGenerateTestMessages produces test messages shaped like a channel's real traffic.
//
// # Why this is a viewer action
//
// Nothing is sent, and nothing real is read out. The generator works from the profile, and a profile holds fill rates,
// shapes and lengths rather than values - so this cannot return a patient even to somebody who asks for it.
//
// # Why the profile is built here rather than accepted from the caller
//
// A caller supplying a profile could ask for a corpus shaped however they liked, which is harmless, but they could also
// supply a profile with values in the code lists and have this echo them back. Building it here means the only path from
// real traffic to output is the one that has been tested not to carry anything.
func (s *Server) handleGenerateTestMessages(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}
	if !s.requireMessages(w, r) {
		return
	}

	count := 25
	if raw := r.URL.Query().Get("count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			s.fail(w, r, http.StatusBadRequest, "count must be a positive whole number")
			return
		}
		// Bounded, because this is held in memory and returned in one response. A caller asking for a million is
		// asking for the server to fall over, and the honest answer is a limit rather than a timeout.
		if n > 5000 {
			s.fail(w, r, http.StatusBadRequest,
				"count is limited to 5000 in one request; for a larger corpus, ask repeatedly with different seeds")
			return
		}
		count = n
	}

	var seed int64 = 1
	if raw := r.URL.Query().Get("seed"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "seed must be a whole number")
			return
		}
		seed = n
	}

	// The corpus the shape is learned from. More than the default profile sample, because a rare trigger event that
	// appears once in a thousand messages is exactly what a test corpus needs to contain.
	messages, err := rt.Messages.RecentPayloads(r.Context(), name, 2000)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(messages) == 0 {
		s.fail(w, r, http.StatusConflict,
			"nothing has been recorded on this channel yet, so there is no shape to copy. Test messages here are "+
				"generated from what a feed actually sends - which is the point of them - so this needs some real "+
				"traffic first.")
		return
	}

	report := profile.Build(messages)

	generated, err := profile.Generate(report, profile.GenerateOptions{
		Count: count,
		Seed:  seed,
	})
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Returned as one document with the segment terminators intact, so it can be saved and replayed as-is. Split into
	// a JSON array of strings, a caller reassembling them would have to know which terminator to use, and the common
	// wrong guess is a newline - which produces a corpus nothing can parse.
	var body strings.Builder
	for _, m := range generated {
		body.Write(m)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"messages": len(generated),
		"corpus":   body.String(),
		"learnedFrom": map[string]any{
			"messages": report.Messages,
			"types":    report.Types,
			"segments": len(report.Segments),
		},
		"seed": seed,
		// Stated in the response, not only in the interface, because whatever automates this will not read the
		// interface.
		"note": fmt.Sprintf("Generated from the shape of %d recorded messages. Contains no real values: the profile "+
			"this was built from holds fill rates, shapes and lengths, not the messages themselves. Code table values "+
			"do carry over, because which codes a sender uses is the point and a code identifies nobody. Every "+
			"message is marked as test in MSH-11.", report.Messages),
	})
}
