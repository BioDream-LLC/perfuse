package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/trace"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Handling an HL7 v3 channel.
//
// # Where this sits between the other paths
//
// Closer to HL7 v2 than to X12, because v3 does have a synchronous acknowledgement - MCCI_IN000002UV01, returned in the response body
// - and a sending application is generally waiting on it. Closer to DICOM in that no parsed v2 message exists, so nothing that
// assumes segments and fields can run.
//
// So it gets its own function for the same reason DICOM does: a nil v2 message travelling through code written on the assumption that
// one exists produces something obscure at the far end rather than an error here.
//
// # What it can do
//
// Filter, using v3 paths, which is the whole point of the path language. Route to any destination that carries bytes. Acknowledge.
//
// What it cannot do is transform, and that is refused at load rather than skipped here.

// handleHL7v3 processes one HL7 v3 document.
func (c *Channel) handleHL7v3(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataHL7v3))

	// The shadow comparison, deferred so it happens once the message has finished the live path however it finished -
	// including the early returns for unparseable input and an excluded message, which are exactly the ones a filter change
	// affects.
	//
	// Here rather than in Channel.handle, which is where it used to be for v2 alone. handle dispatches to this function before
	// reaching that defer, so a shadow on this channel validated at load and then never ran: a configuration option that
	// silently did nothing. The v3 loader even carried a comment saying shadow mode worked on a v3 channel.
	if c.shadow != nil {
		defer c.observeShadow(ctx, raw)
	}

	started := time.Now()

	// hl7v3.Parse rather than xtree.Parse directly, for two reasons beyond convenience.
	//
	// It unwraps a SOAP envelope, which is how IHE actually carries these: a channel receiving ITI-47 over HTTP gets the
	// envelope, not the interaction, and requiring an author to strip it by hand would mean a script in every such channel.
	// And it uses xtree underneath, so the picker and this path agree about what a document contains - including the refusal
	// of external entities, which matters because this is where a hostile document arrives.
	msg, err := hl7v3.Parse(raw)
	if err != nil {
		// Covers both malformed XML and well-formed XML that is not a v3 interaction. Treated identically, because
		// neither can be acknowledged: a v3 acknowledgement names the message it answers, and neither gives us an
		// identifier we can trust.
		return c.rejectHL7v3(ctx, span, raw, started, err)
	}

	// The filter reads the tree, which the message already carries - so there is one parse rather than two.
	root := msg.Root

	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
	}
	describeHL7v3(&record, msg, root)

	log := c.log.With(
		"interaction", record.MessageType,
		"message_id", record.ControlID,
	)

	// The filter runs before anything is delivered, and a fault in it is not a pass.
	//
	// A filter that errors and lets the message through would defeat the point of having one; a filter that errors and
	// silently drops it would lose clinical data. So a fault is recorded as a fault and the sender is told, which is the
	// same choice the v2 path makes.
	//
	// # This branch cannot currently fire, and that is deliberate rather than an oversight
	//
	// Everything a v3 filter could fail on is now settled when the channel loads: paths are parsed and both spellings of a
	// pattern are compiled by config validation, so a filter that reached here is one that already worked. Match errors only
	// on a nil document, and the document here came from a successful parse.
	//
	// Kept because it is the difference between a future eval-time failure being handled and being a panic in a hospital's
	// interface engine, and because the alternative reading - deleting it as unreachable - would silently make the next
	// construct that can fail at evaluation a message-passing-through rather than a message-refused.
	//
	// Written down because a plant proved it unreachable, and finding that out is what led to compiling =~ at load time.
	if filter := c.cfg.HL7v3Filter(); filter != nil {
		pass, err := filter.Match(root)
		if err != nil {
			record.Outcome = Failed
			record.Error = fmt.Sprintf("filter fault: %v", err)
			record.Duration = time.Since(started)

			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			span.SetError(err)
			c.record(ctx, record)

			log.Error("the filter could not be evaluated", "err", err)

			return c.acknowledgeHL7v3(msg, Failed, record.Error), nil
		}

		if !pass {
			record.Outcome = Filtered
			record.Duration = time.Since(started)

			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)
			span.SetString("perfuse.outcome", string(Filtered))
			c.record(ctx, record)

			// Acknowledged positively, matching the v2 rule that a filtered message is an accepted one. The sender
			// did nothing wrong and a negative acknowledgement would make them retry a message we deliberately
			// excluded.
			return c.acknowledgeHL7v3(msg, Filtered, ""), nil
		}
	}

	// Transformations run after the filter and before delivery, matching the v2 order.
	//
	// The order is not arbitrary. Filtering first means a message being excluded is never transformed, so a step that
	// would fail on it cannot fail a message nobody wanted - and it means the filter reads what the sender sent rather
	// than what we made of it, which is what somebody reading the channel expects.
	//
	// A step that fails does not deliver. A half-transformed clinical message sent as though it were complete is worse
	// than one that did not go: the receiver has no way to know it is looking at a partial record, and the sender is
	// told it succeeded.
	if steps := c.cfg.HL7v3Steps(); steps.Len() > 0 {
		if err := steps.Apply(root); err != nil {
			record.Outcome = Failed
			record.Error = fmt.Sprintf("transformation fault: %v", err)
			record.Duration = time.Since(started)

			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			span.SetError(err)
			c.record(ctx, record)

			log.Error("a transformation step could not be applied", "err", err)

			return c.acknowledgeHL7v3(msg, Failed, record.Error), nil
		}

		// Re-serialised, because the destinations send bytes and the steps changed the tree rather than the bytes.
		//
		// The stored payload is deliberately the transformed document rather than what arrived. That is the message
		// this channel actually sent, and a replay has to reproduce what the receiver got - storing the original
		// would make replay a different operation from the one being replayed.
		// Indented by two, matching what the acknowledgement builder writes, so a transformed document and a
		// generated one look the same to anybody reading a stored payload.
		//
		// Marshal cannot fail: it walks a tree that was built by parsing valid XML and mutated through accessors
		// that cannot produce an invalid name. There is deliberately no error branch here rather than an
		// unreachable one - a branch that cannot fire is a branch nobody can test.
		out := root.Marshal(2)
		raw = out
		record.Raw = out

		// The interaction and identifier are re-read, because a step is allowed to change them and the record
		// should describe what was sent. A channel that rewrites an identifier and reports the old one gives
		// somebody tracing a message two different answers.
		describeHL7v3(&record, msg, root)
	}

	// Scripts run after the declarative steps, matching the v2 order.
	//
	// The order is the same for the same reason: a declarative step is readable from the configuration and a script is
	// not, so anybody reasoning about the channel can account for the steps and then ask what the script did to the
	// result. Reversing it would mean the steps operated on something no configuration describes.
	if c.cfg.ScriptEngine() != nil {
		staged, err := c.runTreeScriptStage(root, raw)
		if err != nil {
			record.Outcome = Failed
			record.Error = err.Error()
			record.Duration = time.Since(started)

			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			span.SetError(err)
			c.record(ctx, record)

			log.Error("a script could not be run", "err", err)

			return c.acknowledgeHL7v3(msg, Failed, record.Error), nil
		}

		if !staged.Accepted {
			record.Outcome = Filtered
			record.Duration = time.Since(started)

			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)
			span.SetString("perfuse.outcome", string(Filtered))
			c.record(ctx, record)

			// Acknowledged positively, matching the rule that a filtered message is an accepted one: the
			// sender did nothing wrong and a negative acknowledgement would make them retry a message this
			// channel deliberately excluded.
			return c.acknowledgeHL7v3(msg, Filtered, ""), nil
		}

		if len(staged.Raw) > 0 && !bytes.Equal(staged.Raw, raw) {
			raw = staged.Raw
			record.Raw = staged.Raw
			describeHL7v3(&record, msg, root)
		}
	}

	// No parsed v2 message is passed, because there is none. Destination filters are refused on a v3 channel at load
	// time for exactly this reason.
	outcome, errs, deliveries := c.deliver(ctx, nil, raw, log, nil)

	record.Outcome = outcome
	record.Deliveries = deliveries
	record.Duration = time.Since(started)

	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		record.Error = strings.Join(msgs, "; ")
	}

	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	c.record(ctx, record)

	return c.acknowledgeHL7v3(msg, outcome, record.Error), nil
}

// rejectHL7v3 records a document that could not be read.
//
// No acknowledgement, deliberately. A v3 acknowledgement carries the identifier of the message it answers, and a document we could
// not parse has none we can trust - so an acknowledgement would either invent one or leave it blank, and a receiver matching replies
// to requests would be unable to place it. The transport reports the failure instead: an HTTP source answers 400, which is
// unambiguous.
func (c *Channel) rejectHL7v3(
	ctx context.Context,
	span *trace.Span,
	raw []byte,
	started time.Time,
	cause error,
) ([]byte, error) {
	c.count(func(s *Stats) { s.Unparseable++ })
	c.log.Warn("rejected an unreadable v3 document", "err", cause, "bytes", len(raw))

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Outcome:    Unparseable,
		Error:      cause.Error(),
		Duration:   time.Since(started),
	}

	c.lastOutcome.set(Unparseable)
	span.SetString("perfuse.outcome", string(Unparseable))
	span.SetError(cause)
	c.record(ctx, record)

	return nil, cause
}

// acknowledgeHL7v3 builds the acknowledgement for an outcome.
//
// Delegates to hl7v3.AckFor, which already holds the outcome mapping. That mapping is deliberately the same as the v2 and X12 paths,
// including the two that surprise people - a filtered message is accepted, and a queued one is too - and keeping it in one place is
// what stops the protocols drifting. A site running both would otherwise see the same situation reported two different ways and have
// to learn which engine said what.
func (c *Channel) acknowledgeHL7v3(msg *hl7v3.Message, outcome Outcome, detail string) []byte {
	if !c.cfg.HL7v3.ShouldAcknowledge() {
		return nil
	}

	// The sender's own wish is honoured. AcceptAckCode NE means never acknowledge, and answering anyway is how two systems
	// end up acknowledging each other's acknowledgements.
	if !msg.WantsAcknowledgement() {
		return nil
	}

	ack, err := hl7v3.AckFor(msg, v3Outcome(outcome), c.cfg.HL7v3.SenderOID, detail)
	if err != nil {
		// Logged and dropped rather than returned as a failure. The message itself has already been handled - possibly
		// delivered - and turning a formatting problem in the reply into a delivery failure would make the sender retry
		// something that already arrived.
		c.log.Error("could not build a v3 acknowledgement", "err", err)

		return nil
	}

	return ack
}

// v3Outcome converts an engine outcome to the hl7v3 package's own.
//
// Two enumerations rather than one, because the engine's outcome is about delivery and the hl7v3 one is about what to tell a sender.
// Converting explicitly here means a new engine outcome cannot silently acquire an acknowledgement meaning nobody chose.
func v3Outcome(outcome Outcome) hl7v3.Outcome {
	switch outcome {
	case Delivered:
		return hl7v3.OutcomeDelivered
	case Filtered:
		return hl7v3.OutcomeFiltered
	case Queued:
		return hl7v3.OutcomeQueued
	case PartiallyDelivered:
		return hl7v3.OutcomePartial
	case Unparseable:
		return hl7v3.OutcomeUnparseable
	default:
		return hl7v3.OutcomeFailed
	}
}

// describeHL7v3 fills the identifying fields of a record from a v3 message.
//
// Reusing ControlID, MessageType and TriggerEvent rather than adding parallel fields, so a v3 channel appears in the message browser,
// the search box and the metric labels without each of them being taught about v3.
//
// Nothing here is a patient identifier. The interaction and the message identifier are what an operator searches by during an
// investigation, and both are safe in a metric label - which matters, because that is where these end up.
func describeHL7v3(record *MessageRecord, msg *hl7v3.Message, root *xtree.Node) {
	record.ControlID = msg.ID.Extension
	record.MessageType = msg.InteractionID

	if record.MessageType == "" {
		// The root element is named for the interaction by convention, so it is a reasonable fallback and better than
		// a blank column in the browser.
		record.MessageType = rootElementName(root)
	}
	if record.ControlID == "" {
		record.ControlID = "(no message id)"
	}
}

// rootElementName returns the document's root element without a namespace prefix.
func rootElementName(root *xtree.Node) string {
	if root == nil {
		return ""
	}

	name := root.Name
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}

	return name
}
