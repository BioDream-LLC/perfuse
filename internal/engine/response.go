package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/script"
)

// Reading what a receiver said back.
//
// This is Mirth's response transformer, and it is the last of the script hooks a migration needs. It
// exists because "did the delivery succeed" is often not a question the transport can answer. An MLLP
// receiver returns an application acknowledgement whose meaning is in its text; an HTTP receiver
// returns 200 with an error document. In both cases the transport succeeded and the message did not
// arrive, and without somewhere to inspect the reply there is no way to say so.
//
// # An optional interface rather than a new signature
//
// Sender.Send reports an error and nothing else, which is right for the destinations that genuinely
// have nothing to say - a file has no reply. Rather than widen that signature and force every sender to
// return a nil response for ever, senders that do receive something implement Responder as well.
//
// A destination with a response transformer whose sender is not a Responder is refused at load. The
// alternative is a script that never runs on a channel whose file plainly contains it, which is the
// failure mode this codebase refuses everywhere else.

// Responder is a Sender that can report what the receiver said.
type Responder interface {
	// SendForResponse delivers the message and returns the receiver's reply.
	//
	// The reply is returned even when the error is non-nil, because the reply is usually what explains
	// the error - a negative acknowledgement carries the reason in its text.
	SendForResponse(ctx context.Context, msg []byte) ([]byte, error)
}

// responseVerdict is what a response transformer decided.
type responseVerdict struct {
	// Failed says the delivery should be treated as a failure regardless of what the transport
	// reported.
	Failed bool

	// Reason is what to record. Empty when the script did not give one.
	Reason string
}

// runResponseTransformer runs a destination's response transformer over a reply.
//
// The script sees the reply as a tree in msg when it parses as HL7, and always sees it as text in
// response. Returning false, or throwing, marks the delivery failed; anything else leaves the
// transport's own verdict alone.
func (c *Channel) runResponseTransformer(
	ctx context.Context, d *destination, reply []byte, sendErr error,
) (responseVerdict, error) {
	s := d.responseScript
	if s == nil {
		return responseVerdict{}, nil
	}

	engine := c.cfg.Scripts.Engine()
	if engine == nil {
		return responseVerdict{}, fmt.Errorf(
			"a response transformer was compiled but there is no engine to run it")
	}

	sctx := &script.Context{
		Raw: string(reply),
		Log: func(level, message string) {
			c.logScript("response:"+d.cfg.Name, level, message)
		},
	}

	// The reply is offered as a tree when it is one. An acknowledgement is an HL7 message, so most
	// response transformers want to read MSA-1, and making them parse a string by hand would be a
	// pointless difference from every other script hook.
	//
	// When it does not parse - an HTTP body, a plain-text error - msg stays null and the script uses
	// the text. Failing here instead would mean a receiver returning something unexpected could not be
	// inspected, which is exactly when inspection matters.
	if tree, err := hl7xml.FromRaw(reply); err == nil {
		sctx.Message = tree
	}

	// What the transport thought, so a script can tell a negative acknowledgement apart from a refused
	// connection without guessing.
	if sendErr != nil {
		sctx.SourceMap = script.NewSharedMap()
		sctx.SourceMap.Put("transportError", sendErr.Error())
	}

	result, err := engine.Run(s, sctx)
	if err != nil {
		// A response transformer that throws marks the delivery failed. It ran because somebody wanted
		// the reply checked, and a check that could not be carried out is not a pass.
		return responseVerdict{
			Failed: true,
			Reason: "the response transformer failed: " + err.Error(),
		}, nil
	}

	if !result.Accept {
		reason := "the response transformer rejected the reply"
		// A script that logged something said why in the log; the last line is nearly always the
		// explanation, and repeating it in the outcome saves reading two places.
		if n := len(result.Logs); n > 0 {
			if line := strings.TrimSpace(result.Logs[n-1].Message); line != "" {
				reason = line
			}
		}
		return responseVerdict{Failed: true, Reason: reason}, nil
	}

	return responseVerdict{}, nil
}

// sendOnce performs one delivery attempt, including the response transformer when there is one.
//
// Kept beside the transformer rather than inline in the retry loop, because the interesting decision -
// a transport that succeeded and a receiver that did not accept - is easy to miss when it is three
// lines inside a loop about backoff.
func (c *Channel) sendOnce(ctx context.Context, d *destination, raw []byte) error {
	if d.responseScript == nil {
		return d.sender.Send(ctx, raw)
	}

	// Guaranteed by the check when the destination was built, so a failure here is a programming
	// mistake rather than a configuration one.
	responder, ok := d.sender.(Responder)
	if !ok {
		return fmt.Errorf("destination %q cannot report a reply", d.cfg.Name)
	}

	reply, sendErr := responder.SendForResponse(ctx, raw)

	verdict, err := c.runResponseTransformer(ctx, d, reply, sendErr)
	if err != nil {
		return err
	}

	if verdict.Failed {
		// The script's verdict wins over the transport's. That is the entire point: a receiver
		// answering 200 with an error document, or an application acknowledgement that says no, both
		// look like success to the transport.
		return fmt.Errorf("%s", verdict.Reason)
	}

	// Returned unchanged when the script accepted, so a transport failure the script chose not to
	// override still fails - and is retried like any other.
	return sendErr
}
