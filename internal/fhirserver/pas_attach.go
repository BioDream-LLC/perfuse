package fhirserver

// Receiving the documents a pended PAS request asks for: Da Vinci CDex 2.1 $submit-attachment.
//
// A pended PAS response asks for documents with a Task and one CommunicationRequest per document, each CommunicationRequest
// identified by the attachment control number the provider sends back as the TrackingId. $submit-attachment (at [base] and
// at Claim) takes them: the TrackingId names the pended request, the MemberId must be that request's member, and every
// Attachment's Content (a DocumentReference or a QuestionnaireResponse) is kept with the request, for the reviewer who
// decides it. Nothing is decided by the arrival of a document: what the document says is a person's to read.
//
// The Task's own identifier, urn:uuid:<ClaimResponse id>, is accepted as a TrackingId too, so a provider answering the Task
// rather than one CommunicationRequest is not refused.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

const cdexSubmitAttachment = "http://hl7.org/fhir/us/davinci-cdex/OperationDefinition/submit-attachment"

// PASAttachment is one document received for a prior authorization request.
type PASAttachment struct {
	ID           int64          `json:"id"`
	Tracking     string         `json:"tracking"`
	ResourceType string         `json:"resourceType"`
	Code         string         `json:"code,omitempty"`
	LineItems    []string       `json:"lineItems,omitempty"`
	Final        bool           `json:"final"`
	Received     time.Time      `json:"received"`
	Content      map[string]any `json:"content,omitempty"`
}

// pasTrackingIDs are the identifiers a response asks documents under: each CommunicationRequest's and the Task's.
func pasTrackingIDs(response map[string]any) []string {
	var out []string
	for _, e := range asSliceAny(response["entry"]) {
		res := asMapAny(asMapAny(e)["resource"])
		switch res["resourceType"] {
		case "CommunicationRequest", "Task":
			for _, id := range asSliceAny(res["identifier"]) {
				if v := str(asMapAny(id)["value"]); v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func (s *Server) handleSubmitAttachment(w http.ResponseWriter, r *http.Request) {
	if s.pasOff(w, r) {
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body["resourceType"] != "Parameters" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body must be a Parameters resource")
		return
	}
	var tracking, attachTo, member string
	final := false
	type part struct {
		code      string
		lineItems []string
		content   map[string]any
	}
	var parts []part
	var problems []string
	for _, p := range asSliceAny(body["parameter"]) {
		pm := asMapAny(p)
		switch str(pm["name"]) {
		case "TrackingId":
			tracking = str(asMapAny(pm["valueIdentifier"])["value"])
		case "AttachTo":
			attachTo = str(pm["valueCode"])
		case "MemberId":
			member = str(asMapAny(pm["valueIdentifier"])["value"])
		case "Final":
			final, _ = pm["valueBoolean"].(bool)
		case "Attachment":
			var a part
			for _, sp := range asSliceAny(pm["part"]) {
				spm := asMapAny(sp)
				switch str(spm["name"]) {
				case "LineItem":
					a.lineItems = append(a.lineItems, str(spm["valueString"]))
				case "Code":
					for _, c := range asSliceAny(asMapAny(spm["valueCodeableConcept"])["coding"]) {
						if a.code == "" {
							a.code = str(asMapAny(c)["code"])
						}
					}
				case "Content":
					a.content = asMapAny(spm["resource"])
				}
			}
			switch rt := str(a.content["resourceType"]); rt {
			case "DocumentReference", "QuestionnaireResponse":
			case "":
				problems = append(problems, fmt.Sprintf("Attachment %d has no Content resource", len(parts)+1))
			default:
				problems = append(problems, fmt.Sprintf("Attachment %d is a %s; CDex attachments are DocumentReference or QuestionnaireResponse", len(parts)+1, rt))
			}
			parts = append(parts, a)
		}
	}
	if tracking == "" {
		problems = append(problems, "TrackingId is missing: it is the attachment control number the request was pended with")
	}
	if member == "" {
		problems = append(problems, "MemberId is missing")
	}
	if len(parts) == 0 {
		problems = append(problems, "there is no Attachment")
	}
	switch attachTo {
	case "preauthorization":
	case "claim":
		problems = append(problems, "AttachTo is claim; this server takes attachments for prior authorization requests only")
	default:
		problems = append(problems, fmt.Sprintf("AttachTo must be preauthorization, not %q", attachTo))
	}
	if len(problems) > 0 {
		s.refusePAS(w, r, problems)
		return
	}
	n, err := s.saveAttachments(r.Context(), tracking, member, final, func(add func(rt, code string, lines []string, content map[string]any) error) error {
		for _, a := range parts {
			if err := add(str(a.content["resourceType"]), a.code, a.lineItems, a.content); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errUnknownTracking):
		// The same answer for an unknown tracking number and another member's: telling them apart would say whose it is.
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "not-found",
			"no prior authorization request asked for documents with this TrackingId for this member")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "OperationOutcome", "issue": []any{map[string]any{
		"severity": "information", "code": "informational",
		"diagnostics": fmt.Sprintf("%d attachment(s) received for prior authorization request %s; a reviewer decides it", n, tracking)}}})
}

var errUnknownTracking = errors.New("fhirserver: no PAS request has this tracking id")

// saveAttachments keeps documents with the request their tracking id names, checking the member is that request's.
func (s *Server) saveAttachments(ctx context.Context, tracking, member string, final bool,
	each func(add func(rt, code string, lines []string, content map[string]any) error) error) (int, error) {
	if err := s.pasReady(ctx); err != nil {
		return 0, err
	}
	tx, err := s.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var id, members string
	err = tx.QueryRowContext(ctx, `SELECT t.id, r.member FROM pas_tracking t JOIN pas_requests r ON r.id = t.id WHERE t.tracking = ?`,
		tracking).Scan(&id, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errUnknownTracking
	}
	if err != nil {
		return 0, err
	}
	// The member's identifiers are stored space-separated, each value alone and as system|value.
	if member = strings.TrimSpace(member); member == "" || !strings.Contains(members, " "+member+" ") {
		return 0, errUnknownTracking
	}
	now := s.PAS.now().UTC().UnixMilli()
	n := 0
	err = each(func(rt, code string, lines []string, content map[string]any) error {
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		ln, _ := json.Marshal(lines)
		_, err = tx.ExecContext(ctx, `INSERT INTO pas_attachments (id, tracking, resource_type, code, line_items, final, received, content)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, tracking, rt, code, string(ln), boolInt(final), now, string(raw))
		n++
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// Attachments lists the documents received for one PAS request, oldest first; withContent includes the resources.
func (s *Server) Attachments(ctx context.Context, id string, withContent bool) ([]PASAttachment, error) {
	if err := s.pasReady(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Store.db.QueryContext(ctx, `SELECT rowid, tracking, resource_type, code, line_items, final, received, content
		FROM pas_attachments WHERE id = ? ORDER BY received, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PASAttachment
	for rows.Next() {
		var a PASAttachment
		var lines, content string
		var final int
		var received int64
		if err := rows.Scan(&a.ID, &a.Tracking, &a.ResourceType, &a.Code, &lines, &final, &received, &content); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(lines), &a.LineItems)
		a.Final, a.Received = final == 1, time.UnixMilli(received).UTC()
		if withContent {
			_ = json.Unmarshal([]byte(content), &a.Content)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
