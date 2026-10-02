package dicom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

// QueryRequest is one C-FIND.
type QueryRequest struct {
	// Level is which rung of the hierarchy answers come back on. Required.
	Level QueryLevel

	// Match are the keys being matched. An empty value means "return this field" rather than "match empty".
	//
	// That distinction is the whole of how DICOM querying works and the thing newcomers get wrong: an element present
	// with a value filters, an element present but empty is a request for that field, and an element absent means the
	// archive need not return it at all.
	Match []Element

	// PatientRoot uses the patient root information model instead of study root.
	PatientRoot bool

	// Limit stops after this many matches, cancelling the request. Zero means no limit.
	//
	// Worth having because a mistyped query at IMAGE level against a real archive returns hundreds of thousands of
	// responses, and the difference between a bug and an outage is whether something stops it.
	Limit int
}

// QueryResult is one match.
type QueryResult struct {
	// DataSet is the identifier the archive returned.
	DataSet *DataSet

	// Inexact reports that the archive did not support every optional key in the query.
	//
	// Surfaced rather than swallowed because it changes what the result means: the archive answered a broader question
	// than the one asked, so some responses may not match the key that was ignored.
	Inexact bool
}

// ErrQueryRefused reports that the remote refused the query itself.
var ErrQueryRefused = errors.New("dicom: the query was refused")

// Find runs a C-FIND and returns the matches.
//
// Mirth has no C-FIND connector at all - it has been requested since 2015 - so this has no parity to match and is judged
// against the standard and against DCMTK instead.
//
// Results are collected rather than streamed. An archive answering a study-level query returns tens to hundreds of matches,
// which is small, and the callers that matter here - a channel source, an operator's search - want the set rather than a
// callback. FindFunc exists for the case where that is the wrong shape.
func (c *Client) Find(ctx context.Context, req QueryRequest) ([]QueryResult, error) {
	var out []QueryResult

	err := c.FindFunc(ctx, req, func(r QueryResult) error {
		out = append(out, r)
		return nil
	})

	return out, err
}

// errStopQuery ends a query early without making it an error.
var errStopQuery = errors.New("stop")

// FindFunc runs a C-FIND and calls fn for each match.
//
// Returning an error from fn stops the query and releases the association. It is reported to the caller, because a
// consumer that gave up part way through has not seen a complete answer and code that cannot tell the difference will
// treat a partial result as the whole set.
func (c *Client) FindFunc(ctx context.Context, req QueryRequest, fn func(QueryResult) error) error {
	if !req.Level.Valid() {
		return fmt.Errorf("dicom: %q is not a query level; use PATIENT, STUDY, SERIES or IMAGE", req.Level)
	}
	if req.Level == LevelPatient && !req.PatientRoot {
		// Refused rather than silently switched. A patient-level query under the study root model is not defined, and an
		// archive's response to one ranges from an error to an empty result that reads as "no such patient".
		return fmt.Errorf("dicom: a PATIENT level query needs the patient root information model")
	}

	sopClass := StudyRootQueryFIND
	if req.PatientRoot {
		sopClass = PatientRootQueryFIND
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	contexts := []PresentationContext{{
		ID:               1,
		AbstractSyntax:   sopClass,
		TransferSyntaxes: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
	}}

	accepted, maxLength, err := c.associate(conn, contexts)
	if err != nil {
		return err
	}

	usable, ok := firstAccepted(accepted)
	if !ok {
		// The specific and common case: an archive that stores images but does not answer queries. Said plainly, because
		// "association rejected" sends people to check the network when the network is fine.
		return fmt.Errorf("%w: the remote accepted the association but refused the query context, so it does not "+
			"support C-FIND under the %s information model", ErrQueryRefused, modelName(req.PatientRoot))
	}

	syntax := orDefault(usable.Accepted, ImplicitVRLittleEndian)

	// The query level goes in the identifier, not the command. It is a key like any other, which is why an archive can
	// reject it for being absent rather than for being wrong.
	identifier := append([]Element{
		{Tag: TagQueryRetrieveLevel, VR: "CS", Value: []byte(req.Level)},
	}, req.Match...)

	query, err := Encode(identifier, syntax)
	if err != nil {
		return err
	}

	const messageID = 1
	if err := writePDataPDUs(conn, usable.ID, findRQ(sopClass, messageID), true, maxLength); err != nil {
		return err
	}
	if err := writePDataPDUs(conn, usable.ID, query, false, maxLength); err != nil {
		return err
	}

	count := 0
	for {
		cmd, data, err := readResponse(conn, maxLength)
		if err != nil {
			return err
		}

		if !IsPending(cmd.Status) {
			// The final response. Success ends the query; anything else is a refusal, and the status text is the only
			// explanation the archive gives.
			if cmd.Status == statusCancelled {
				return nil
			}
			if !IsSuccess(cmd.Status) {
				return fmt.Errorf("%w: the archive answered %s after %d match(es)",
					ErrQueryRefused, StatusText(cmd.Status), count)
			}
			return c.release(conn)
		}

		if len(data) == 0 {
			// A pending response with no identifier is malformed, but tolerated rather than fatal: it costs nothing to
			// ignore, and dying here would discard the matches already collected.
			continue
		}

		ds, err := ParseDataSet(data, syntax)
		if err != nil {
			return fmt.Errorf("dicom: a match could not be read: %w", err)
		}

		count++
		if err := fn(QueryResult{DataSet: ds, Inexact: cmd.Status == statusPendingInexact}); err != nil {
			_ = c.cancel(conn, usable.ID, messageID, maxLength)
			if errors.Is(err, errStopQuery) {
				return nil
			}
			return err
		}

		if req.Limit > 0 && count >= req.Limit {
			// Cancelled rather than abandoned. Dropping the connection leaves the archive matching for a query nobody is
			// reading, and some archives hold the association open waiting.
			if err := c.cancel(conn, usable.ID, messageID, maxLength); err != nil {
				return err
			}
			return c.drainAfterCancel(conn, maxLength)
		}
	}
}

// cancel sends a C-CANCEL for an outstanding request.
func (c *Client) cancel(w io.Writer, contextID byte, messageID uint16, maxLength uint32) error {
	return writePDataPDUs(w, contextID, commandSet([]Element{
		{Tag: tagCommandField, Value: uint16Value(cmdCCancelRQ)},
		{Tag: tagMessageIDRespondingTo, Value: uint16Value(messageID)},
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0101)},
	}), true, maxLength)
}

// drainAfterCancel reads until the archive acknowledges a cancellation.
//
// Necessary rather than tidy: the archive may already have queued several matches when the cancel arrives, and leaving
// them unread means releasing the association with data still in flight, which some archives treat as an abort and log as
// a failure against the calling AE title.
func (c *Client) drainAfterCancel(conn net.Conn, maxLength uint32) error {
	for {
		cmd, _, err := readResponse(conn, maxLength)
		if err != nil {
			// The archive closed instead of answering. Not an error worth reporting: the caller got the matches it asked
			// for, and the cancellation was the point.
			return nil
		}
		if !IsPending(cmd.Status) {
			return c.release(conn)
		}
	}
}

// modelName names an information model for an error message.
func modelName(patientRoot bool) string {
	if patientRoot {
		return "patient root"
	}
	return "study root"
}
