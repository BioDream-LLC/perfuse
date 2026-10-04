package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/publichealth"
)

// ecrSender is the case reporting half of a fhir destination: it decides whether a message is reportable and builds the eCR
// message that goes to the agency.
type ecrSender struct {
	triggers *publichealth.TriggerSet
	facility publichealth.Facility
	source   string

	reported      atomic.Int64
	notReportable atomic.Int64
}

func newECRSender(c *config.ECRDestination) (*ecrSender, error) {
	triggers := publichealth.BuiltinTriggers()
	if c.RCTC != "" {
		t, err := publichealth.LoadTriggers(c.RCTC)
		if err != nil {
			return nil, fmt.Errorf("ecr.rctc: %w", err)
		}
		triggers = t
	}
	f := c.Facility
	return &ecrSender{
		triggers: triggers,
		source:   c.Source,
		facility: publichealth.Facility{Name: f.Name, NPI: f.NPI, Phone: f.Phone, Line: f.Line, City: f.City, State: f.State, PostalCode: f.PostalCode},
	}, nil
}

// ECRStats counts what a case reporting destination did: reports sent, and messages it read and found nothing reportable in.
type ECRStats struct {
	Reported      int64 `json:"reported"`
	NotReportable int64 `json:"notReportable"`
}

// ECRStats is nil for an ordinary FHIR destination.
func (s *FHIRSender) ECRStats() *ECRStats {
	if s.ecr == nil {
		return nil
	}
	return &ECRStats{Reported: s.ecr.reported.Load(), NotReportable: s.ecr.notReportable.Load()}
}

func (s *FHIRSender) sendCaseReport(ctx context.Context, m *hl7.Message) error {
	report, err := publichealth.FromV2(m, s.ecr.triggers, s.opts, publichealth.EICROptions{Now: time.Now(), Facility: s.ecr.facility})
	if err != nil {
		if report != nil && len(report.Triggers) == 0 {
			// Most messages are not reportable, and that is the destination working, not failing.
			s.ecr.notReportable.Add(1)
			s.log.Debug("not reportable", "control_id", m.ControlID())
			return nil
		}
		s.convertFails.Add(1)
		return fmt.Errorf("the message is reportable but no case report could be built: %w", err)
	}
	s.converted.Add(1)
	for _, n := range report.Notes {
		s.warnings.Add(1)
		s.log.Warn("case report note", "detail", n, "control_id", m.ControlID())
	}
	_, event, _ := m.Type()
	msg, err := publichealth.ReportingBundle(report, publichealth.ReportingOptions{
		Destination: s.cfg.URL, Source: s.ecr.source, Event: publichealth.EventFor(event, report.Triggers),
	})
	if err != nil {
		s.convertFails.Add(1)
		return err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := s.postTo(ctx, strings.TrimRight(s.cfg.URL, "/")+"/$process-message", body); err != nil {
		return err
	}
	s.sent.Add(1)
	s.ecr.reported.Add(1)
	var codes []string
	for _, t := range report.Triggers {
		codes = append(codes, t.Code)
	}
	// The trigger codes are logged, not the patient: they say why a report went, which is what an audit asks.
	s.log.Info("case report sent", "triggers", strings.Join(codes, ","), "control_id", m.ControlID())
	return nil
}
