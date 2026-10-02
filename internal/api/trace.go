package api

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Tracing one message through one channel.
//
// POST /api/channels/{name}/trace with either a pasted message or the id of a stored one.
//
// Editor, not viewer. The trace itself is read-only and cannot deliver anything, but it accepts an
// arbitrary message body and runs a channel's compiled filter and transformations over it, and
// accepting somebody's message to run configuration against is closer to editing than to reading.
// It also shows field values, so it is not a weaker permission than reading a stored message.

type traceRequest struct {
	// Message is a pasted HL7 message. Either this or ID is required.
	Message string `json:"message,omitempty"`
	// ID names a stored message to trace instead, which is the common case: something went
	// wrong, it is in the message list, and the question is why.
	ID int64 `json:"id,omitempty"`
}

func (s *Server) handleTraceMessage(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	var req traceRequest
	if !s.decode(w, r, &req) {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	raw, arrived, ok := s.traceSubject(w, r, sess, req)
	if !ok {
		return
	}

	// When the message came from the store, the trace says whether the channel has changed since it arrived.
	//
	// Without this the tool answers a subtly different question from the one asked. Somebody investigating why a
	// message was delivered, on a channel whose filter was tightened yesterday, is shown a trace saying it would be
	// filtered - and reasonably concludes the delivery record is wrong.
	var tr *engine.Trace
	if arrived.IsZero() {
		tr, err = engine.TraceMessage(r.Context(), cfg, raw)
	} else {
		tr, err = engine.TraceStoredMessage(r.Context(), cfg, raw, arrived, s.channelChangedAt(channels, name))
	}
	if err != nil {
		// A refusal to trace is a 400 with the reason, not a 500. Tracing an X12 channel is not
		// a fault; it is a thing this cannot do, and the message says why.
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.ok(w, tr)
}

// traceSubject resolves the message to trace.
// traceSubject resolves the message to trace, and when it arrived.
//
// A zero time means the message was pasted rather than stored, so there is nothing it can be stale against.
func (s *Server) traceSubject(w http.ResponseWriter, r *http.Request, sess *store.Session, req traceRequest) ([]byte, time.Time, bool) {
	if req.ID != 0 {
		rt, ok := s.runtimeFor(w, r, sess)
		if !ok {
			return nil, time.Time{}, false
		}
		if !s.requireMessages(w, r) {
			return nil, time.Time{}, false
		}
		m, err := rt.Messages.Get(r.Context(), string(sess.TenantID), req.ID)
		if err != nil {
			s.failErr(w, r, err)
			return nil, time.Time{}, false
		}
		if len(m.Raw) == 0 {
			s.fail(w, r, http.StatusConflict,
				"that message was recorded without its body, so there is nothing to trace")
			return nil, time.Time{}, false
		}
		return m.Raw, m.ReceivedAt, true
	}

	if strings.TrimSpace(req.Message) == "" {
		s.fail(w, r, http.StatusBadRequest,
			"send either a message to trace or the id of a stored one")
		return nil, time.Time{}, false
	}

	// A message pasted out of a log or an email arrives with line feeds instead of carriage
	// returns. Refusing it on that basis would make the tool useless for the case it exists to
	// serve, which is somebody holding a message that caused a problem.
	return []byte(normaliseTerminators(req.Message)), time.Time{}, true
}

// channelChangedAt reports when a channel's definition was last modified.
//
// Read from the file's modification time rather than from git, because git history is optional - a deployment that keeps its
// channels in a plain directory has no commits and would get no warning at all, which is the deployment most likely to have
// somebody editing files by hand.
//
// A zero time when it cannot be determined, which suppresses the warning. Guessing "changed" would put a caveat on every
// trace and train people to ignore it.
func (s *Server) channelChangedAt(channels *ChannelRepo, name string) time.Time {
	path, err := channels.PathFor(name)
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}

	return info.ModTime()
}
