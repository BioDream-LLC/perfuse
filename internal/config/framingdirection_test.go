package config

import (
	"strings"
	"testing"
)

// A framing the socket code cannot handle has to be refused when the channel loads.
//
// Both of these validated cleanly and then failed every message at runtime, in a way
// that pointed at the wrong thing:
//
//   - A tcp destination framed as mllp reported the channel valid, then failed each
//     delivery with "framing mllp cannot be written here", retried five times, and
//     dropped the message. The operator was told the destination was unreachable when
//     the receiver was up and listening.
//   - A tcp source framed as mllp reported the channel valid, logged that it was
//     listening with framing=mllp, then reset every connection with "framing mllp
//     cannot be read here" and acknowledged nothing.
//
// MLLP is the obvious framing for someone moving HL7 over a socket to reach for, and
// it is genuinely supported - by the mllp source and destination types, which own the
// connection and the acknowledgement. The mistake is easy to make and was expensive to
// diagnose, so the error names the answer.

func tcpDestFramed(mode string) *TCPDest {
	d := &TCPDest{Address: "127.0.0.1:9999"}
	d.Framing = mode
	return d
}

func TestTCPDestinationRefusesFramingItCannotSend(t *testing.T) {
	errs := tcpDestFramed("mllp").Validate()
	if !mentions(errs, "cannot be sent") {
		t.Fatalf("a tcp destination framed as mllp was accepted. It validates and then fails "+
			"every delivery. Errors were: %v", errs)
	}
	if !mentions(errs, "type mllp") {
		t.Errorf("the refusal does not name the destination type that does work, which is the "+
			"only thing that turns this from a rejection into an answer. Errors were: %v", errs)
	}
}

func TestTCPDestinationStillAcceptsFramingItCanSend(t *testing.T) {
	// The guard above must not have been written so broadly that it rejects the modes
	// that work. Without this, refusing everything would pass.
	for _, mode := range []string{"delimited", "fixed", "length", "whole"} {
		d := tcpDestFramed(mode)
		switch mode {
		case "delimited":
			d.Delimiter = "\\r"
		case "fixed":
			d.RecordLength = 256
		case "length":
			d.LengthBytes = 4
		}
		if errs := d.Validate(); mentions(errs, "cannot be sent") {
			t.Errorf("framing %q is writable but was refused: %v", mode, errs)
		}
	}
}

func TestTCPSourceRefusesFramingItCannotRead(t *testing.T) {
	s := &TCPSource{Listen: "127.0.0.1:9998"}
	s.Framing = "mllp"
	errs := s.Validate()
	if !mentions(errs, "cannot be read") {
		t.Fatalf("a tcp source framed as mllp was accepted. It validates, logs that it is "+
			"listening, and then resets every connection. Errors were: %v", errs)
	}
	if !mentions(errs, "type mllp") {
		t.Errorf("the refusal does not name the source type that does work: %v", errs)
	}
}

func TestTCPSourceStillAcceptsFramingItCanRead(t *testing.T) {
	for _, mode := range []string{"delimited", "fixed", "length", "whole"} {
		s := &TCPSource{Listen: "127.0.0.1:9998"}
		s.Framing = mode
		switch mode {
		case "delimited":
			s.Delimiter = "\\r"
		case "fixed":
			s.RecordLength = 256
		case "length":
			s.LengthBytes = 4
		}
		if errs := s.Validate(); mentions(errs, "cannot be read") {
			t.Errorf("framing %q is readable but was refused: %v", mode, errs)
		}
	}
}

func mentions(errs []error, want string) bool {
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}
