package engine

import (
	"context"

	"github.com/biodream-llc/perfuse/internal/x12xml"
)

// x12XMLSender hands a destination the interchange as the XML Mirth Connect and Open Integration Engine write for X12,
// for a receiver built against what Mirth sent it (x12_as: xml).
type x12XMLSender struct{ inner Sender }

func (s *x12XMLSender) Send(ctx context.Context, msg []byte) error {
	out, err := x12xml.ToXML(msg)
	if err != nil {
		return err
	}
	return s.inner.Send(ctx, out)
}

func (s *x12XMLSender) Describe() string { return s.inner.Describe() + " (as X12 XML)" }
func (s *x12XMLSender) Close() error     { return s.inner.Close() }

type x12XMLResponder struct {
	*x12XMLSender
	responder Responder
}

func (s *x12XMLResponder) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	out, err := x12xml.ToXML(msg)
	if err != nil {
		return nil, err
	}
	return s.responder.SendForResponse(ctx, out)
}

// wrapX12XML puts the conversion in front of a destination's sender, keeping its ability to report a reply.
func wrapX12XML(inner Sender) Sender {
	s := &x12XMLSender{inner: inner}
	if r, ok := inner.(Responder); ok {
		return &x12XMLResponder{x12XMLSender: s, responder: r}
	}
	return s
}
