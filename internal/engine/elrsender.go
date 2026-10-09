package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/publichealth"
)

// SoftwareVersion is Perfuse's version as the SFT segment of an ELR message names it; the server sets it at start-up.
var SoftwareVersion = "dev"

// elrSender is a destination with an elr block: it reshapes each lab result into an ELR 2.5.1 message and hands that to the
// destination's own sender, the transport (MLLP, HTTP, SFTP, a file) being whatever the state takes.
type elrSender struct {
	inner    Sender
	cfg      publichealth.ELRConfig
	triggers *publichealth.TriggerSet
	log      *slog.Logger
}

func newELRSender(c *config.ELRDestination, inner Sender, log *slog.Logger) (*elrSender, error) {
	cfg, err := publichealth.LoadELRConfig(c.Config)
	if err != nil {
		return nil, fmt.Errorf("elr.config: %w", err)
	}
	triggers := publichealth.BuiltinTriggers()
	if c.RCTC != "" {
		if triggers, err = publichealth.LoadTriggers(c.RCTC); err != nil {
			return nil, fmt.Errorf("elr.rctc: %w", err)
		}
	}
	return &elrSender{inner: inner, cfg: cfg, triggers: triggers, log: log}, nil
}

// build returns the ELR message for msg, or nil when nothing in it is reportable.
func (s *elrSender) build(msg []byte) ([]byte, error) {
	m, err := hl7.Parse(msg)
	if err != nil {
		return nil, fmt.Errorf("elr: the message could not be read: %w", err)
	}
	opts := s.cfg.Options(time.Now(), publichealth.Software{Vendor: "BioDream LLC", Version: SoftwareVersion, Name: "Perfuse",
		BinaryID: "perfuse-" + SoftwareVersion})
	r, err := publichealth.BuildELR(m, s.triggers, opts)
	if errors.Is(err, publichealth.ErrNothingReportable) {
		// Most lab results are not reportable, and leaving them out is the destination working: the state asked for
		// reportable results only.
		s.log.Debug("elr: nothing reportable, not sent", "control_id", m.ControlID())
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("elr: the result is reportable but no ELR message could be built: %w", err)
	}
	for _, t := range r.Triggers {
		s.log.Info("elr: reportable result", "control_id", m.ControlID(), "code", t.Code, "system", t.System, "condition", t.Condition)
	}
	for _, n := range r.Notes {
		s.log.Warn("elr note", "detail", n, "control_id", m.ControlID())
	}
	return r.Message, nil
}

func (s *elrSender) Send(ctx context.Context, msg []byte) error {
	out, err := s.build(msg)
	if err != nil || out == nil {
		return err
	}
	return s.inner.Send(ctx, out)
}

// elrResponder is an elrSender over a sender that returns the receiver's reply, so a response transformer on an MLLP or
// SOAP destination still sees the state's acknowledgement.
type elrResponder struct {
	*elrSender
	responder Responder
}

func (s *elrResponder) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	out, err := s.build(msg)
	if err != nil || out == nil {
		return nil, err
	}
	return s.responder.SendForResponse(ctx, out)
}

func (s *elrSender) Describe() string { return s.inner.Describe() + " (ELR 2.5.1)" }
func (s *elrSender) Close() error     { return s.inner.Close() }

// wrapELR puts an elr block's reshaping in front of a destination's sender.
func wrapELR(c *config.ELRDestination, inner Sender, log *slog.Logger) (Sender, error) {
	s, err := newELRSender(c, inner, log)
	if err != nil {
		return nil, err
	}
	if r, ok := inner.(Responder); ok {
		return &elrResponder{elrSender: s, responder: r}, nil
	}
	return s, nil
}
