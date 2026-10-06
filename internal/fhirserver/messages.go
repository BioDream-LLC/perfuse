package fhirserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// MessageHandler processes a FHIR message posted to $process-message: the raw Bundle of type message. It returns the response
// message to answer with synchronously, or nil to acknowledge it with an OperationOutcome. A *MessageError refuses it with a
// status; any other error is a 500.
type MessageHandler func(ctx context.Context, message []byte) (map[string]any, error)

// MessageError is a refused message: what was wrong with it, and the status to answer with.
type MessageError struct {
	Status  int
	Code    string // an OperationOutcome issue type: invalid, not-supported, business-rule ...
	Message string
}

func (e *MessageError) Error() string { return e.Message }

// handleProcessMessage is the system-level $process-message. It answers only when something in this process handles
// messages, eCR above all; otherwise it says so rather than accepting a message nobody will read.
func (s *Server) handleProcessMessage(w http.ResponseWriter, r *http.Request) {
	if s.Messages == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"$process-message is not enabled on this server")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	reply, err := s.Messages(r.Context(), body)
	var me *MessageError
	switch {
	case errors.As(err, &me):
		code := me.Code
		if code == "" {
			code = "invalid"
		}
		status := me.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		s.writeOutcome(w, r, status, fhir.SeverityError, code, me.Message)
	case err != nil:
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception", err.Error())
	case reply != nil:
		s.writeJSON(w, http.StatusOK, reply)
	default:
		s.writeOutcome(w, r, http.StatusOK, fhir.SeverityInformation, "informational", "the message was accepted")
	}
}
