package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/config"
)

// pgpSender is a destination with a pgp block: what it would deliver is encrypted and signed first, and the transport
// sends that instead.
type pgpSender struct {
	inner Sender
	cfg   *config.PGPSend
}

func (s *pgpSender) Send(ctx context.Context, msg []byte) error {
	out, err := s.cfg.Seal(msg)
	if err != nil {
		return err
	}
	return s.inner.Send(ctx, out)
}

func (s *pgpSender) Describe() string { return s.inner.Describe() + " (OpenPGP)" }
func (s *pgpSender) Close() error     { return s.inner.Close() }

// pgpResponder keeps a sender's reply available when it has one, so a response transformer still sees it.
type pgpResponder struct {
	*pgpSender
	responder Responder
}

func (s *pgpResponder) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	out, err := s.cfg.Seal(msg)
	if err != nil {
		return nil, err
	}
	return s.responder.SendForResponse(ctx, out)
}

// bodySealer is a sender that writes named files or objects. It seals the final bytes itself, after naming the file from the
// plain message and framing it, so a file name built from the control ID still works and the framing is inside the
// encryption where the receiver expects it.
type bodySealer interface {
	SetSeal(func([]byte) ([]byte, error))
}

// seal applies a sender's seal, when it has one.
func seal(f func([]byte) ([]byte, error), body []byte) ([]byte, error) {
	if f == nil {
		return body, nil
	}
	return f(body)
}

// wrapPGP puts a pgp block's protection in front of a destination's sender.
func wrapPGP(c *config.PGPSend, inner Sender) Sender {
	s := &pgpSender{inner: inner, cfg: c}
	if r, ok := inner.(Responder); ok {
		return &pgpResponder{pgpSender: s, responder: r}
	}
	return s
}

// ErrPGPRefused marks a received message that could not be opened with PGP: the sender's problem (a wrong key, no
// signature, not encrypted), so an HTTP sender is told 400 and why, rather than 500.
var ErrPGPRefused = errors.New("refused")

type pgpOpenedKey struct{}

// withPGPOpened marks a context whose messages come from a file the source has already opened with PGP.
func withPGPOpened(ctx context.Context) context.Context {
	return context.WithValue(ctx, pgpOpenedKey{}, true)
}

func pgpOpened(ctx context.Context) bool { v, _ := ctx.Value(pgpOpenedKey{}).(bool); return v }

// openPGP opens what a source received when the source has a pgp block, and returns it unchanged otherwise.
func (c *Channel) openPGP(raw []byte) ([]byte, error) {
	p := c.cfg.Source.PGP
	if p == nil {
		return raw, nil
	}
	plain, signedBy, err := p.Open(raw)
	if err != nil {
		c.log.Warn("a received message could not be opened with PGP, and was refused", "reason", err.Error())
		return nil, fmt.Errorf("%w: %w", ErrPGPRefused, err)
	}
	if signedBy != "" {
		c.log.Debug("pgp signature verified", "signed_by", signedBy)
	}
	return plain, nil
}
