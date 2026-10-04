package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/publichealth"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

type eicrRequest struct {
	Message  string                `json:"message"`
	System   string                `json:"system,omitempty"`
	Timezone string                `json:"timezone,omitempty"`
	Facility publichealth.Facility `json:"facility"`
}

// handleEICR builds the eCR case report a message would trigger, without storing or sending anything: what a case reporting
// destination would send for it, and why, or why it would send nothing.
func (s *Server) handleEICR(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req eicrRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		s.fail(w, r, http.StatusBadRequest, "no message was supplied")
		return
	}
	var loc *time.Location
	if req.Timezone != "" {
		l, err := time.LoadLocation(req.Timezone)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "unknown timezone "+req.Timezone)
			return
		}
		loc = l
	}
	m, err := hl7.Parse([]byte(normaliseTerminators(req.Message)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the message is not valid HL7 v2: "+err.Error())
		return
	}
	triggers := s.ECRTriggers
	if triggers == nil {
		triggers = publichealth.BuiltinTriggers()
	}
	report, err := publichealth.FromV2(m, triggers, v2fhir.Options{DefaultIdentifierSystem: req.System, Timezone: loc},
		publichealth.EICROptions{Now: time.Now(), Facility: req.Facility})
	if report == nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{
		"reportable":      len(report.Triggers) > 0,
		"triggers":        report.Triggers,
		"notes":           report.Notes,
		"triggerSource":   triggers.Source,
		"triggerCodes":    triggers.Len(),
		"facilityMissing": req.Facility.Missing(),
	}
	if err != nil {
		out["reason"] = err.Error()
	}
	if report.Bundle != nil {
		out["bundle"] = report.Bundle
	}
	s.ok(w, out)
}
