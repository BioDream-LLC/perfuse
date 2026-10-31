package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/tefca"
)

// TEFCA is the server's TEFCA participant, set at start-up; nil when the server does not participate.
var TEFCA *tefca.Participant

// TEFCASender asks the Facilitated FHIR partner for the records of each message's patient.
//
// The patient is read as the patient index reads it: an HL7 v2 PID, or a FHIR Patient on a raw channel. The query goes
// by identifier when there is one, and by name with date of birth otherwise; a name alone is refused by the exchange,
// because a wrong match on a name does not fail, it returns somebody else's record. Every attempt is audited in the
// TEFCA trail, refused ones included.
//
// The Bundle the partner returns is this destination's reply, so a response transformer can read it, and is handed to
// the channel results_to names, when there is one, as a message of its own.
type TEFCASender struct {
	cfg    config.TEFCADestination
	name   string
	router ChannelRouter
}

// NewTEFCASender builds the sender. The participant is the server's, so a channel can load where there is none; it fails
// on the first message instead, saying why.
func NewTEFCASender(d config.Destination, router ChannelRouter) (*TEFCASender, error) {
	if d.TEFCA == nil {
		return nil, fmt.Errorf("destination %q is a tefca destination with no tefca block", d.Name)
	}
	if d.TEFCA.ResultsTo != "" && router == nil {
		return nil, fmt.Errorf("%w, so destination %q cannot hand its results to %q", ErrNoRouter, d.Name, d.TEFCA.ResultsTo)
	}
	return &TEFCASender{cfg: *d.TEFCA, name: d.Name, router: router}, nil
}

// QueryFor reads the patient a message is about into a query.
func tefcaQueryFor(msg []byte, purpose string) (tefca.QueryRequest, error) {
	var e *mpi.Extracted
	var err error
	if t := bytes.TrimSpace(msg); len(t) > 0 && t[0] == '{' {
		e, err = mpi.FromFHIR(t)
	} else {
		e, err = mpi.FromHL7(msg, mpi.FromOptions{})
	}
	if err != nil {
		return tefca.QueryRequest{}, fmt.Errorf("tefca: the message names no patient to ask about: %w", err)
	}
	r := e.Record
	q := tefca.QueryRequest{Purpose: purpose, Gender: r.Sex}
	if r.Value != "" {
		q.PatientID = r.Value
		// An identifier from a system the partner can recognise is sent with it. A local namespace this server made
		// up (urn:perfuse:aa:...) means nothing to anybody else, so the value goes alone.
		if r.System != "" && !strings.HasPrefix(r.System, "urn:perfuse:") {
			q.PatientID = r.System + "|" + r.Value
		}
	}
	if r.Family != "" {
		q.PatientName = r.Family
	}
	if d := r.BirthDate; len(d) == 8 && !strings.Contains(d, "-") {
		q.DOB = d[:4] + "-" + d[4:6] + "-" + d[6:]
	} else {
		q.DOB = d
	}
	return q, nil
}

// Send asks the partner and passes on what it returns.
func (s *TEFCASender) Send(ctx context.Context, msg []byte) error {
	_, err := s.SendForResponse(ctx, msg)
	return err
}

// SendForResponse asks the partner and returns the Bundle it answered with.
func (s *TEFCASender) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	if TEFCA == nil {
		return nil, errors.New("tefca: this server is not a TEFCA participant (Settings, TEFCA)")
	}
	q, err := tefcaQueryFor(msg, s.cfg.Purpose)
	if err != nil {
		return nil, err
	}
	bundle, err := TEFCA.QueryFHIR(ctx, q)
	if err != nil {
		return nil, err
	}
	if s.cfg.ResultsTo != "" {
		if err := s.router.Route(ctx, s.cfg.ResultsTo, bundle.Raw); err != nil {
			return bundle.Raw, fmt.Errorf("tefca: the results could not be handed to channel %q: %w", s.cfg.ResultsTo, err)
		}
	}
	return bundle.Raw, nil
}

// Describe says where this destination asks.
func (s *TEFCASender) Describe() string {
	if TEFCA != nil && TEFCA.Exchange() != nil {
		return "TEFCA Facilitated FHIR query to " + TEFCA.Exchange().Partner() + " for " + s.cfg.Purpose
	}
	return "a TEFCA Facilitated FHIR query for " + s.cfg.Purpose
}

// Close releases nothing; the exchange is the server's.
func (s *TEFCASender) Close() error { return nil }
