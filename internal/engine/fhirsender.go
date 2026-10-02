package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// FHIRSender converts an HL7 v2 message to FHIR and posts it to a FHIR server.
//
// This is the destination that makes the engine useful beyond v2: a hospital feed
// arrives as HL7 v2 and lands in a FHIR store, with no separate integration
// project in between.
//
// It sends a transaction bundle rather than individual resources, so a patient and
// their encounter land together or not at all. Posting them separately leaves an
// encounter referring to a patient that does not exist if the second call fails.
type FHIRSender struct {
	cfg    config.FHIRDestination
	client *http.Client
	log    *slog.Logger

	version  fhir.Version
	location *time.Location
	opts     v2fhir.Options

	// Counters for the dashboard. Conversion problems are worth surfacing
	// separately from transport problems, because they mean different things: one
	// is a mapping gap and the other is a network or server fault.
	converted    atomic.Int64
	convertFails atomic.Int64
	rejected     atomic.Int64
	sent         atomic.Int64
	warnings     atomic.Int64
}

// NewFHIRSender builds a sender from configuration.
func NewFHIRSender(d config.Destination, log *slog.Logger) (*FHIRSender, error) {
	if d.FHIR == nil {
		return nil, fmt.Errorf("destination %q has type fhir but no fhir block", d.Name)
	}
	if log == nil {
		log = slog.Default()
	}

	// Defaults to the release the structs actually populate, not to the newest one.
	//
	// This was fhir.DefaultVersion, which is R5, so a destination that did not name a version sent resources declaring 5.0.0
	// while carrying R4 field spellings - MedicationRequest.medicationCodeableConcept rather than R5's medication. A receiver
	// parsing it as R5 finds no medication and renders an empty list, which is indistinguishable from a patient on no
	// medications. The same defect I fixed on the serving side, in the opposite direction, on the surface where the data leaves.
	//
	// The guard I wrote for the serving side checked that no *flag* default was a release literal. This is a channel
	// configuration default rather than a flag, so it sat outside what the test looked at.
	version := fhir.ResourceShapeVersion
	if d.FHIR.Version != "" {
		parsed, err := fhir.ParseVersion(d.FHIR.Version)
		if err != nil {
			return nil, err
		}
		version = parsed
	}

	location := time.UTC
	if d.FHIR.Timezone != "" {
		loc, err := time.LoadLocation(d.FHIR.Timezone)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
		location = loc
	}

	s := &FHIRSender{
		cfg:      *d.FHIR,
		log:      log.With("destination", d.Name, "fhir_version", version.Name()),
		version:  version,
		location: location,
		client: &http.Client{
			Timeout: d.Timeout,
			// A redirect on a write would silently repost patient data somewhere
			// the configuration did not name.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
			},
		},
		opts: v2fhir.Options{
			Version:                   version,
			BaseURL:                   strings.TrimRight(d.FHIR.URL, "/"),
			AssigningAuthoritySystems: d.FHIR.IdentifierSystems,
			DefaultIdentifierSystem:   d.FHIR.DefaultIdentifierSystem,
			ClaimUSCore:               d.FHIR.ClaimUSCore,
			Timezone:                  location,
		},
	}
	return s, nil
}

// FHIRStats reports what this sender has done.
type FHIRStats struct {
	Converted        int64 `json:"converted"`
	ConversionFailed int64 `json:"conversionFailed"`
	Rejected         int64 `json:"rejected"`
	Sent             int64 `json:"sent"`
	Warnings         int64 `json:"warnings"`
}

// Stats returns a snapshot.
func (s *FHIRSender) Stats() FHIRStats {
	return FHIRStats{
		Converted:        s.converted.Load(),
		ConversionFailed: s.convertFails.Load(),
		Rejected:         s.rejected.Load(),
		Sent:             s.sent.Load(),
		Warnings:         s.warnings.Load(),
	}
}

// Send converts and posts one message.
func (s *FHIRSender) Send(ctx context.Context, raw []byte) error {
	m, err := hl7.Parse(raw)
	if err != nil {
		s.convertFails.Add(1)
		return fmt.Errorf("the message could not be parsed as HL7 v2: %w", err)
	}

	result, err := v2fhir.Convert(m, s.opts)
	if err != nil {
		s.convertFails.Add(1)
		return fmt.Errorf("conversion to FHIR failed: %w", err)
	}
	s.converted.Add(1)

	// Mapping notes are logged rather than discarded. A conversion that silently
	// dropped a field is the kind of problem discovered months later by someone
	// wondering where the data went.
	warnings := result.Warnings()
	if len(warnings) > 0 {
		s.warnings.Add(int64(len(warnings)))
		for _, note := range warnings {
			s.log.Warn("mapping note",
				"severity", note.Severity, "source", note.Source,
				"target", note.Target, "detail", note.Message,
				"control_id", m.ControlID())
		}
	}

	if len(result.Bundle.Entry) == 0 {
		// Nothing to send is not a failure, but it is worth knowing about: a
		// message type nobody mapped produces this.
		s.log.Info("conversion produced no resources",
			"type", result.MessageType, "event", result.TriggerEvent,
			"control_id", m.ControlID())
		return nil
	}

	if s.cfg.ShouldValidate() {
		validation := fhir.Validate(result.Bundle, s.version)
		errCount, warnCount, _ := validation.Counts()
		if errCount > 0 || (s.cfg.RejectOnWarning && warnCount > 0) {
			s.rejected.Add(1)
			// Refusing here is better than posting something the server will
			// reject: a transport error would hide the real, fixable problem.
			return fmt.Errorf("the converted bundle is not valid %s: %s",
				s.version.Name(), summariseFindings(validation.Findings))
		}
	}

	body, err := fhir.MarshalBundle(result.Bundle, s.version)
	if err != nil {
		s.convertFails.Add(1)
		return fmt.Errorf("serialising the bundle: %w", err)
	}

	if err := s.post(ctx, body); err != nil {
		return err
	}

	s.sent.Add(1)
	s.log.Info("sent to FHIR",
		"type", result.MessageType, "event", result.TriggerEvent,
		"resources", len(result.Bundle.Entry), "bytes", len(body),
		"control_id", m.ControlID())
	return nil
}

func (s *FHIRSender) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.cfg.URL, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	if s.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.BearerToken)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", s.cfg.URL, err)
	}
	defer resp.Body.Close()

	// The response body carries the OperationOutcome that says why a rejection
	// happened, so it is worth reading even on success paths.
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if outcome := describeOutcome(responseBody); outcome != "" {
			s.log.Debug("FHIR server response", "detail", outcome)
		}
		return nil

	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// Retrying an authentication failure will not help, and burning the retry
		// budget on it delays the alert that would.
		return fmt.Errorf("%s rejected the credentials (%s); check the token or headers",
			s.cfg.URL, resp.Status)

	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnprocessableEntity:
		detail := describeOutcome(responseBody)
		if detail == "" {
			detail = strings.TrimSpace(string(responseBody))
		}
		return fmt.Errorf("%s refused the bundle (%s): %s", s.cfg.URL, resp.Status, truncate(detail, 500))

	default:
		return fmt.Errorf("%s returned %s: %s",
			s.cfg.URL, resp.Status, truncate(strings.TrimSpace(string(responseBody)), 300))
	}
}

// Describe names the destination for logs.
func (s *FHIRSender) Describe() string {
	return fmt.Sprintf("fhir %s (%s)", s.cfg.URL, s.version.Name())
}

// Close releases idle connections.
func (s *FHIRSender) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// summariseFindings renders validation errors compactly, worst first, so a log
// line says what to fix rather than everything that is true.
func summariseFindings(findings []fhir.Finding) string {
	var parts []string
	for _, f := range findings {
		if f.Severity != fhir.Error {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", f.Path, f.Message))
		if len(parts) == 5 {
			parts = append(parts, "…")
			break
		}
	}
	if len(parts) == 0 {
		return "no error detail"
	}
	return strings.Join(parts, "; ")
}

// describeOutcome pulls the human-readable part out of an OperationOutcome.
func describeOutcome(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	resource, err := fhir.UnmarshalResource(body)
	if err != nil {
		return ""
	}
	outcome, ok := resource.(*fhir.OperationOutcome)
	if !ok {
		return ""
	}

	var parts []string
	for _, issue := range outcome.Issue {
		text := issue.Diagnostics
		if text == "" && issue.Details != nil {
			text = issue.Details.Text
		}
		if text == "" {
			text = issue.Code
		}
		parts = append(parts, fmt.Sprintf("%s: %s", issue.Severity, text))
		if len(parts) == 5 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
