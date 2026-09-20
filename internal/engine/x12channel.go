package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/trace"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// Handling an X12 channel.
//
// Kept beside the HL7 path rather than folded into it, because the two differ in the one
// place that matters most: X12 has no synchronous acknowledgement. HL7 answers every
// message with an ACK or a NAK on the same connection, and a great deal of the HL7 path
// exists to decide which. X12 acknowledges out of band, with a 997 or 999 sent back later
// as its own transaction, so there is nothing to return and nothing to decide.
//
// Threading that difference through the HL7 function would mean a nil acknowledgement
// travelling through code written on the assumption that one always exists, and the first
// symptom would be an empty MSA segment reaching a hospital.
//
// Everything that is genuinely shared - recording, delivery, queueing, metrics, tracing -
// is called rather than copied.

// handleX12 processes one X12 interchange.
func (c *Channel) handleX12(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataX12))

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

	// The preprocessor, before parsing, which is the whole reason it exists: an interchange from a partner who prefixes a byte
	// order mark, or one whose segment terminator arrived as a line feed, cannot be parsed until something repairs it.
	//
	// Runs here rather than in Channel.handle for the same reason the shadow does - handle reaches its script stage only after
	// dispatching away from it.
	repaired, err := c.runTextScripts(ctx, raw)
	if err != nil {
		c.count(func(s *Stats) { s.Failed++ })
		c.lastOutcome.set(Failed)
		c.record(ctx, MessageRecord{
			Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
			Outcome: Failed, Error: err.Error(), Duration: time.Since(started),
		})

		return nil, err
	}
	raw = repaired

	msg, err := x12.Parse(raw)
	if err != nil {
		// Nothing to acknowledge against: an acknowledgement has to name the control
		// number it answers, and a file that did not parse has none we can trust.
		return c.rejectX12(ctx, span, raw, started, Unparseable, err, nil, nil)
	}

	// The envelope check, which is the reason to prefer this over a passthrough.
	//
	// A truncated 837 parses perfectly well - it is simply missing claims - so without
	// this the file is accepted, forwarded, and nobody finds out until a payer reports
	// fewer claims than were sent.
	validation := msg.Validate()
	policy := c.cfg.X12.Policy()

	var envelopeWarning string
	switch policy {
	case config.EnvelopeRequire:
		if err := validation.Err(); err != nil {
			// Reported as unparseable rather than failed, and the distinction matters
			// more than the label does.
			//
			// Failed means something downstream refused the message, which an HTTP
			// source turns into a 502 - and a 502 tells the sender to retry. Retrying a
			// truncated claims file never helps, so that is an infinite loop against a
			// clearinghouse feed. Unparseable produces a 400 and the instruction the
			// sender actually needs: this file is broken, fix it, do not resend it
			// unchanged.
			//
			// It is a stretch, because the file did parse - it simply claims to contain
			// things it does not. The honest model is a separate Rejected outcome
			// meaning "understood and refused on its own merits", which would also
			// serve HL7 schema validation when that arrives. That is deliberately
			// deferred: it needs a metric, a store column, an API field and a series on
			// the dashboard, and one caller does not justify it yet. The recorded error
			// text carries the precise reason either way.
			// The interchange parsed, so it can be answered: this is a truncated or
			// miscounted file and the partner needs to know which.
			return c.rejectX12(ctx, span, raw, started, Unparseable, err, msg, validation.Problems)
		}
		if w := validation.Warnings(); len(w) > 0 {
			envelopeWarning = strings.Join(w, "; ")
		}
	case config.EnvelopeWarn:
		if err := validation.Err(); err != nil {
			envelopeWarning = err.Error()
		}
		if w := validation.Warnings(); len(w) > 0 {
			if envelopeWarning != "" {
				envelopeWarning += "; "
			}
			envelopeWarning += strings.Join(w, "; ")
		}
	case config.EnvelopeIgnore:
		// Nothing. Named to be uncomfortable in the configuration for this reason.
	}

	if envelopeWarning != "" {
		// Logged and attached to the message, not silently dropped. An envelope fault a
		// site has chosen to tolerate is still the first thing to look at when a
		// reconciliation does not balance.
		c.log.Warn("x12 envelope check reported problems",
			"policy", string(policy), "problems", envelopeWarning)
		span.SetString("x12.envelope_problems", envelopeWarning)
	}

	// Metadata only: transaction set, control numbers, the interchange sender. No
	// element values, because an 837 element is a diagnosis or a charge and a span
	// attribute is how that reaches an observability platform with no agreement
	// covering it.
	span.SetString("x12.version", msg.Version())
	span.SetString("x12.transaction_sets", strings.Join(msg.TransactionSets(), ","))
	span.SetInt("x12.segments", int64(msg.SegmentCount()))

	if !c.cfg.X12.ShouldSplit() {
		return c.deliverX12(ctx, msg, raw, started, envelopeWarning, validation.Problems)
	}

	parts, err := msg.Split()
	if err != nil {
		// Refusing is right. A split that cannot be done means the file is not what it
		// claims to be, and forwarding it whole would deliver something the channel was
		// configured not to deliver.
		return c.rejectX12(ctx, span, raw, started, Unparseable,
			fmt.Errorf("splitting the interchange failed: %w", err), msg, validation.Problems)
	}

	c.log.Info("x12 interchange split", "sets", len(parts), "bytes", len(raw))
	span.SetInt("x12.transaction_set_count", int64(len(parts)))

	// Each transaction set becomes a message in its own right: its own record, its own
	// metrics, its own row in the interface. That is the point of splitting - an
	// operator looking for one claim should find one claim, not a file of four hundred.
	outcomes := make([]Outcome, 0, len(parts))
	for _, part := range parts {
		partStarted := time.Now()
		// A split channel cannot acknowledge - validation refuses the combination, because an acknowledgement is a
		// statement about a whole interchange and a partner receiving one per transaction set cannot reconcile them.
		// Passing nil problems here makes that explicit rather than relying on the configuration check alone.
		if _, err := c.deliverX12(ctx, part, part.Raw(), partStarted, envelopeWarning, nil); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, c.lastOutcome.get())
	}

	c.lastOutcome.set(combineOutcomes(outcomes))

	// Nothing to return. X12 acknowledges out of band.
	return nil, nil
}

// deliverX12 records and delivers one interchange.
func (c *Channel) deliverX12(
	ctx context.Context, msg *x12.Message, raw []byte, started time.Time,
	envelopeWarning string, problems []x12.Problem,
) ([]byte, error) {
	c.count(func(s *Stats) { s.Received++ })

	// The channel filter runs before the transformations, matching the HL7 order: a message the channel does not want
	// should not be changed on its way to being dropped, and a filter written against the arriving interchange should be
	// evaluated against what arrived.
	//
	// Per transaction set for the same reason the steps are: on a split channel, CLM01 means this claim.
	if f := c.cfg.X12Filter(); f != nil {
		match, err := f.Eval(msg)
		if err != nil {
			// A filter that cannot decide is a configuration fault, not the sender's fault. Failing is honest: it makes
			// the partner resend and keeps the claim at its origin rather than dropping it here.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			c.log.Error("the x12 channel filter failed to evaluate", "err", err)

			failed := MessageRecord{
				Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
				Segments: msg.SegmentCount(), Outcome: Failed,
				Error: fmt.Sprintf("x12 filter failed: %v", err), Duration: time.Since(started),
			}
			describeX12(&failed, msg)
			c.record(ctx, failed)
			return nil, nil
		}

		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			filtered := MessageRecord{
				Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
				Segments: msg.SegmentCount(), Outcome: Filtered, Duration: time.Since(started),
			}
			describeX12(&filtered, msg)
			c.record(ctx, filtered)

			// Recorded rather than discarded silently. A partner asking why a claim never arrived needs an answer, and
			// "the filter excluded it" is one.
			c.log.Info("an x12 interchange was excluded by the channel filter")
			return nil, nil
		}
	}

	// Transformations run here rather than in handleX12, so that a split channel transforms each transaction set on its
	// own. A path like CLM01 then means this claim's control number, which is what somebody writing it intends. Applied
	// before the split it would mean the first claim in the file, and on a 400-claim 837 the other 399 would go out
	// untouched while the trace showed a change.
	//
	// Recorded raw becomes the transformed bytes, because that is what was sent and a destination file has to match what
	// the message store says was delivered. What arrived is still recoverable: shadow keeps it, and a replay runs the
	// steps again from the original.
	if steps := c.cfg.X12.Steps(); steps.Len() > 0 {
		transformed, changes, err := steps.Apply(msg)
		if err != nil {
			// Stopped rather than delivered untransformed. A step that fails means the interchange is not what the
			// configuration assumed, and sending the original would send a claim the channel was configured to change.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)

			failed := MessageRecord{
				Channel:    c.cfg.Name,
				ReceivedAt: started.UTC(),
				Raw:        raw,
				Segments:   msg.SegmentCount(),
				Outcome:    Failed,
				Error:      fmt.Sprintf("x12 transformation failed: %v", err),
				Duration:   time.Since(started),
			}
			describeX12(&failed, msg)
			c.record(ctx, failed)

			c.log.Error("an x12 transformation failed", "err", err)
			return nil, nil
		}

		if len(changes) > 0 {
			msg, raw = transformed, transformed.Raw()
			// Paths and the step's own description, never the values. An X12 element is a diagnosis code or a charge,
			// and a log line is how that reaches somewhere with no agreement covering it.
			paths := make([]string, 0, len(changes))
			for _, ch := range changes {
				paths = append(paths, ch.Path)
			}
			c.log.Info("x12 transformations applied", "changes", len(changes), "paths", strings.Join(paths, ","))
		}
	}

	// The filter and transformer scripts, after the declarative steps and in that order.
	//
	// After the steps because that is the order every other format uses: the declarative edits are the configuration, and a
	// script exists for what they cannot express, so it should see the result of them rather than racing them.
	//
	// Addressed by path rather than as a tree. CLM01 is what an implementation guide calls that element and what the filter
	// and the steps already use, so a rule can move between a step and a script without being rewritten.
	if c.cfg.FilterScript() != nil || c.cfg.TransformerScript() != nil {
		next, stage, err := runPathScriptStage(c, msg, x12.Accessor{})
		if err != nil {
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)

			failed := MessageRecord{
				Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
				Segments: msg.SegmentCount(), Outcome: Failed,
				Error: err.Error(), Duration: time.Since(started),
			}
			describeX12(&failed, msg)
			c.record(ctx, failed)
			c.log.Error("an x12 script failed", "err", err)

			return nil, nil
		}

		if !stage.Accepted {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			filtered := MessageRecord{
				Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
				Segments: msg.SegmentCount(), Outcome: Filtered, Duration: time.Since(started),
			}
			describeX12(&filtered, msg)
			c.record(ctx, filtered)
			c.log.Info("an x12 interchange was excluded by the filter script")

			return nil, nil
		}

		if len(stage.ScriptPaths) > 0 {
			// Raw is taken from the script's message, because that is what will be sent and the store has to match the
			// destination. Paths only, never values: an X12 element is a diagnosis code or a charge.
			msg, raw = next, next.Raw()
			c.log.Info("x12 script transformations applied",
				"changes", len(stage.ScriptPaths), "paths", strings.Join(stage.ScriptPaths, ","))
		}
	}

	// The metrics are not touched here. record() calls recordOutcome, which counts
	// received alongside the outcome, so incrementing here as well would report twice
	// the traffic - and a claims dashboard that overstates volume is one nobody can
	// reconcile against the clearinghouse's own numbers.
	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Segments:   msg.SegmentCount(),
		Error:      envelopeWarning,
	}
	describeX12(&record, msg)

	log := c.log.With(
		"control_id", record.ControlID,
		"transaction_set", record.MessageType,
	)

	// The message is passed to delivery without a parsed HL7 form, because there is
	// none. Destination filters are refused on an X12 channel at load time for exactly
	// this reason.
	// Destination filters are compiled against X12 paths, so the evaluator is supplied here rather than left to the HL7
	// path. This is how one interchange fans out to different partners - claims over a threshold to one clearinghouse,
	// the rest to another.
	outcome, errs, deliveries := c.deliver(ctx, nil, raw, log, func(d *config.Destination) (bool, error) {
		f := d.X12FilterExpr()
		if f == nil {
			return true, nil
		}
		return f.Eval(msg)
	})

	record.Outcome = outcome
	record.Deliveries = deliveries
	record.Duration = time.Since(started)
	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		joined := strings.Join(msgs, "; ")
		if record.Error != "" {
			record.Error += "; " + joined
		} else {
			record.Error = joined
		}
	}

	c.lastOutcome.set(outcome)
	c.record(ctx, record)

	// An acknowledgement, when the channel is configured for one.
	//
	// Off by default, and that default is deliberate rather than laziness: X12 acknowledgement is classically
	// asynchronous - a clearinghouse receives an 837 by file drop and returns a 999 hours later as its own interchange.
	// A partner who is not expecting one and receives it may treat it as an unsolicited file.
	//
	// But real-time transactions do work this way. A CAQH CORE 270 over HTTP holds the connection open for its answer,
	// and returning the acknowledgement in the response body is the only way to answer it. Which is why this is
	// configured, and why validation refuses it on a source with no open connection to answer on.
	if ack := c.acknowledgeX12(msg, problems, outcome); ack != nil {
		return ack, nil
	}

	return nil, nil
}

// acknowledgeX12 builds the acknowledgement for an interchange, or nil when the channel does not acknowledge.
//
// Never returns an error. A channel that has already accepted and delivered a message must not then fail because it could
// not phrase its reply - that would turn a delivered message into a reported failure, and an HTTP source would answer 502,
// which tells the partner to resend something that already went through. A failure here is logged loudly and the request is
// answered with the transport's own success instead.
func (c *Channel) acknowledgeX12(msg *x12.Message, problems []x12.Problem, outcome Outcome) []byte {
	level := c.cfg.X12.Acknowledgement()
	if level == "" {
		return nil
	}

	// The status reflects what actually happened to the message, not only what the envelope looked like. A file that
	// validated perfectly and then failed every destination has not been accepted in any sense the partner cares about.
	status := x12.StatusFor(problems)
	switch outcome {
	case Failed, Unparseable:
		status = x12.StatusRejected
	case PartiallyDelivered:
		// Some destinations took it. Accepted-with-errors rather than rejected, because rejecting it would have the
		// partner resend a file that partly went through, and duplicate claims are worse than a late one.
		if status == x12.StatusAccepted {
			status = x12.StatusAcceptedWithErrors
		}
	}

	ack, err := x12.Ack(x12.AckOptions{
		Original:        msg,
		Level:           x12.AckLevel(level),
		Status:          status,
		Problems:        problems,
		SenderID:        c.cfg.X12.AckSenderID,
		SenderQualifier: c.cfg.X12.AckSenderQualifier,
		ControlNumber:   c.nextAckControlNumber(),
	})
	if err != nil {
		// Loud, because a partner expecting an acknowledgement and receiving the transport's bare success will
		// eventually resend, and this log line is the only thing that explains why.
		c.log.Error("could not build the x12 acknowledgement, so the trading partner will not be answered",
			"level", level, "err", err)

		return nil
	}

	return ack
}

// nextAckControlNumber gives each acknowledgement its own interchange control number.
//
// Increasing and non-repeating, because a partner that sees a duplicate control number will usually discard the interchange
// as an accidental resend - so a fixed number would mean only the first acknowledgement of the day was ever read.
//
// Restarting the engine restarts the sequence, which is a real limitation and is why this is derived from the clock rather
// than from a counter: a nine-digit number from the seconds within the year does not repeat within a run and does not go
// backwards across a restart in the way a reset counter would.
func (c *Channel) nextAckControlNumber() int {
	c.ackSeq.Add(1)

	// Seconds since the start of the year, times ten, plus a small rotating offset. Fits in nine digits and does not
	// repeat within a run.
	now := time.Now().UTC()
	base := now.YearDay()*86400 + now.Hour()*3600 + now.Minute()*60 + now.Second()

	return (base*10 + int(c.ackSeq.Load()%10)) % 1000000000
}

// rejectX12 records a refusal and returns.
func (c *Channel) rejectX12(
	ctx context.Context, span *trace.Span, raw []byte, started time.Time,
	outcome Outcome, cause error, msg *x12.Message, problems []x12.Problem,
) ([]byte, error) {
	c.count(func(s *Stats) { s.Received++ })
	switch outcome {
	case Unparseable:
		c.count(func(s *Stats) { s.Unparseable++ })
	default:
		c.count(func(s *Stats) { s.Failed++ })
	}

	c.log.Warn("rejected an x12 interchange",
		"outcome", string(outcome), "error", cause, "bytes", len(raw))

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Outcome:    outcome,
		Error:      cause.Error(),
		Duration:   time.Since(started),
	}
	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	span.SetError(cause)
	c.record(ctx, record)

	// A rejection is the case where an acknowledgement matters most, and the first version of this omitted it: a partner
	// who sent a truncated claims file received a bare HTTP 400 and no statement of what was wrong. That is the moment
	// they most need one, because a 400 does not distinguish "your file was cut short" from "your credentials are wrong",
	// and the two call for completely different action.
	//
	// The interchange may not have parsed at all, in which case there is nothing to reference and nothing can be said -
	// an acknowledgement has to name the control number it is answering.
	if msg != nil {
		if ack := c.acknowledgeX12(msg, problems, outcome); ack != nil {
			return ack, nil
		}
	}

	// Otherwise the sender is told nothing beyond the transport's own answer - an HTTP status, an SFTP failure.
	return nil, nil
}

// describeX12 fills in the metadata fields from the envelope.
//
// Deliberately reuses the existing record fields rather than adding X12-specific ones, so
// the message store, the search interface and every export work on claims traffic with no
// change. The mapping is the closest honest equivalent in each case.
func describeX12(record *MessageRecord, msg *x12.Message) {
	// The interchange control number is what a support conversation opens with, the
	// same role the HL7 control ID plays.
	if isa, ok := msg.Segment("ISA", 1); ok {
		record.ControlID = strings.TrimSpace(isa.Element(13).String())
		record.Sender = strings.TrimSpace(isa.Element(6).String())
	}

	// The transaction set is what kind of message this is: 837, 835, 834.
	sets := msg.TransactionSets()
	record.MessageType = strings.Join(sets, ",")

	// The implementation guide is the closest thing X12 has to a trigger event - it is
	// what says which flavour of an 837 this is, and a receiver routes on it.
	if gs, ok := msg.Segment("GS", 1); ok {
		record.TriggerEvent = strings.TrimSpace(gs.Element(8).String())
	}
}

// combineOutcomes reduces the outcomes of split parts to one for the file.
//
// A file where some claims went and some did not is partially delivered, which is a
// distinct operational state from either extreme: resending the whole file would
// duplicate the ones that succeeded.
func combineOutcomes(in []Outcome) Outcome {
	if len(in) == 0 {
		return Failed
	}

	delivered, failed, queued := 0, 0, 0
	for _, o := range in {
		switch o {
		case Delivered, Filtered:
			delivered++
		case Queued:
			queued++
		default:
			failed++
		}
	}

	switch {
	case failed == 0 && queued == 0:
		return Delivered
	case failed == 0:
		return Queued
	case delivered == 0 && queued == 0:
		return Failed
	default:
		return PartiallyDelivered
	}
}
