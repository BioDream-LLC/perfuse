package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
)

// handleReadSample proposes a channel from pasted sample messages.
//
// # Why there is no channel name in the path
//
// This is the case where no channel exists. Somebody has the sample the laboratory attached to an email and nothing
// else. Every other route that produces a channel proposal works from traffic a channel has already handled, which
// presupposes the thing being asked for.
//
// # Why editor rather than viewer
//
// It creates nothing and deploys nothing, so viewer would be defensible. Editor because the output is a channel
// somebody is about to save, and the proposal includes a listening address - a viewer being handed a half-built
// configuration is an invitation to ask an administrator to deploy something nobody has reviewed.
func (s *Server) handleReadSample(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req struct {
		// Sample is pasted text. May hold several messages, any line ending, MLLP framing, and a covering note.
		Sample string `json:"sample"`
		// Name is what to call the channel. Optional.
		Name string `json:"name"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	if len(req.Sample) > 4<<20 {
		// Bounded because it is profiled in memory. A limit with a number is more useful than a timeout.
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			"that sample is larger than 4MB. A few messages is enough to propose a channel - more than that is "+
				"better handled by running the channel and profiling its real traffic")
		return
	}

	reading, err := profile.ReadSample(req.Sample, req.Name)
	if err != nil {
		// A 422 with the reason, not a 500. Being unable to read a sample is an ordinary outcome and the message says
		// what to try instead.
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	s.ok(w, reading)
}
