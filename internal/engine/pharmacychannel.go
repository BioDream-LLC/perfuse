package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/eprescribe"
	"github.com/biodream-llc/perfuse/internal/ncpdp"
	"github.com/biodream-llc/perfuse/internal/trace"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// handlePharmacy delivers an NCPDP claim or a SCRIPT prescription.
//
// # Why these share a path
//
// Neither has HL7 segments, so both need their own route for the reason DICOM does: a nil parsed v2 message travelling
// through code that assumes one exists produces something obscure at the far end rather than an error here.
//
// They share it because what the engine does with them is the same. Both are parsed to find out whether they are what
// they claim to be, both are recorded with a description a human can read, and both are delivered whole. The formats are
// utterly different; the handling is not.
//
// # Why they are parsed at all when nothing transforms them
//
// A channel moving prescriptions could treat them as raw bytes and would be simpler for it. It is worth parsing anyway,
// because a raw channel can never report Unparseable - so a truncated prescription, an empty claim, or an XML document
// that is not SCRIPT at all would be delivered as faithfully as a good one, and the first anybody would know is a
// pharmacy asking why it received half a message.
//
// Parsing here means a malformed message stops at this server, where somebody can see it, rather than at a pharmacy or a
// payer, where the reply is a phone call.
//
// # What is deliberately not done
//
// Nothing is corrected. A prescription missing a substitution flag, or a schedule II with refills on it, is reported and
// still delivered if the channel is a forwarding channel - because this server did not author it and quietly editing a
// prescription in transit is worse than passing on a bad one somebody can see. Validation is available to a channel that
// authors prescriptions through the API; a pipe does not silently rewrite what flows through it.
func (c *Channel) handlePharmacy(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(c.cfg.Type()))

	started := time.Now()
	isScript := c.cfg.Type() == config.DataScript

	// The preprocessor, before parsing. That order is the whole reason a preprocessor exists: a transmission with a trailing NUL,
	// a byte order mark from a partner's exporter, or a terminator that arrived as a line feed cannot be parsed until something
	// repairs it.
	//
	// Here rather than in Channel.handle, which reaches its script stage only after dispatching away from it - the same reason
	// the shadow observation had to move.
	if repaired, perr := c.runTextScripts(ctx, raw); perr != nil {
		c.count(func(s *Stats) { s.Failed++ })
		c.lastOutcome.set(Failed)
		c.record(ctx, MessageRecord{
			Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw,
			Outcome: Failed, Error: perr.Error(), Duration: time.Since(started),
		})

		return nil, perr
	} else {
		raw = repaired
	}

	if len(raw) == 0 {
		return nil, c.pharmacyUnparseable(ctx, span, started, raw,
			fmt.Errorf("the payload is empty, and an empty %s is never something a sender meant", describePharmacy(isScript)))
	}

	// Parsed for the description and the sanity check, not to change anything.
	summary, controlID, claim, err := describeMessage(raw, isScript)
	if err != nil {
		return nil, c.pharmacyUnparseable(ctx, span, started, raw, err)
	}

	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		// The sender's own identifier where the format has one: a SCRIPT MessageID or a claim's prescription number.
		// Taken rather than invented, so it can be matched against the sending system when somebody asks.
		ControlID: controlID,
	}

	log := c.log.With("bytes", len(raw), "message", summary)

	// The channel filter, for NCPDP. Evaluated against the whole transmission rather than per claim: a transmission
	// carries up to four claims for one patient and is answered by one response, so excluding part of it would leave the
	// switch expecting a reply for a claim that was never sent.
	if f := c.cfg.NCPDPFilter(); f != nil && claim != nil {
		match, err := f.Eval(claim)
		if err != nil {
			// A filter that cannot decide is a configuration fault, not the pharmacy's. Failing is honest: it makes the
			// sender retry and keeps the claim where somebody can see it.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("the ncpdp channel filter failed to evaluate", "err", err)

			record.Outcome = Failed
			record.Error = fmt.Sprintf("ncpdp filter failed: %v", err)
			record.Duration = time.Since(started)
			c.record(ctx, record)
			return nil, nil
		}

		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			// Recorded rather than dropped silently. A pharmacy asking why a claim was never adjudicated needs an answer.
			log.Info("a pharmacy transmission was excluded by the channel filter")

			record.Outcome = Filtered
			record.Duration = time.Since(started)
			c.record(ctx, record)
			return nil, nil
		}
	}

	// The channel filter, for SCRIPT. Parsed here rather than in describeMessage because the tree is only needed when a
	// filter exists, and parsing every prescription twice to serve the channels that have no filter would be waste.
	if f := c.cfg.ScriptFilter(); f != nil {
		tree, terr := eprescribe.ParseTree(raw)
		if terr != nil {
			// The message already parsed once in describeMessage, so reaching here means the tree parse disagreed with
			// the typed one. Failing rather than passing: a filter that cannot be evaluated must not deliver the
			// messages it existed to exclude.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("a prescription could not be read as a tree for filtering", "err", terr)

			record.Outcome = Failed
			record.Error = fmt.Sprintf("script filter could not read the message: %v", terr)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return nil, nil
		}

		match, err := f.Eval(tree)
		if err != nil {
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("the script channel filter failed to evaluate", "err", err)

			record.Outcome = Failed
			record.Error = fmt.Sprintf("script filter failed: %v", err)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return nil, nil
		}

		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			// Recorded rather than dropped. A prescriber asking why a prescription never reached the pharmacy needs an
			// answer, and for a controlled substance that question may be asked by a regulator.
			log.Info("a prescription was excluded by the channel filter")

			record.Outcome = Filtered
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return nil, nil
		}
	}

	// The transformations, after the filter. A transmission the channel excludes should not be changed on its way to
	// being dropped, and a step written against what arrived should see what arrived.
	if steps := c.cfg.NCPDP.Steps(); steps != nil && claim != nil {
		next, changes, err := steps.Apply(claim)
		if err != nil {
			// Stopped rather than delivered untransformed. A step that fails means the transmission is not what the
			// channel was configured to send, and sending it anyway would deliver a claim nobody asked for.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("an ncpdp transformation step failed", "err", err)

			record.Outcome = Failed
			record.Error = fmt.Sprintf("ncpdp transformation failed: %v", err)
			record.Duration = time.Since(started)
			c.record(ctx, record)
			return nil, nil
		}

		// Rebuilt only when something actually changed, so a channel whose steps did not fire delivers the sender's own
		// bytes untouched rather than a normalised copy of them.
		//
		// # What rebuilding costs
		//
		// Unlike the X12 path, which edits and re-parses while keeping the original bytes, a transformed transmission here
		// is written afresh from the parsed form. Anything the parser does not model is therefore normalised away: field
		// padding is rewritten to the standard widths, and a group separator written as a leader before the first
		// transaction comes back as a separator between transactions, because that is what Build emits.
		//
		// That is a real difference and it is deliberate - the alternative is byte surgery on a format whose header is
		// fixed width and whose segments are length-sensitive, which is how a transmission ends up well formed and wrong.
		// It is also unverified against a live pharmacy switch, which is the honest limit on this claim.
		if len(changes) > 0 {
			next.SetHeaderCount()
			rebuilt, berr := ncpdp.Build(*next)
			if berr != nil {
				// A transformed transmission that will not build is a step that produced something invalid - an
				// over-long field, a segment the standard has no place for. Refusing beats sending it.
				c.count(func(s *Stats) { s.Failed++ })
				c.lastOutcome.set(Failed)
				log.Error("a transformed transmission would not build", "err", berr)

				record.Outcome = Failed
				record.Error = fmt.Sprintf("the transformed transmission would not build: %v", berr)
				record.Duration = time.Since(started)
				c.record(ctx, record)
				return nil, nil
			}

			// The recorded raw becomes the transformed bytes, because that is what was sent and a destination file has to
			// match what the message store says was delivered. What arrived is still recoverable through shadow and a
			// replay.
			raw = rebuilt
			record.Raw = rebuilt
			claim = next

			// Paths and descriptions, never values. An NCPDP field is a cardholder identifier or a drug.
			paths := make([]string, 0, len(changes))
			for _, ch := range changes {
				paths = append(paths, ch.Path)
			}
			log.Info("ncpdp transformations applied", "changes", len(changes), "paths", strings.Join(paths, ","))
		}
	}

	// The filter and transformer scripts, after the declarative steps.
	//
	// One branch, two bindings. A claim is addressed by the standard's own two-character field identifiers through the path
	// binding; a prescription is XML and takes the tree binding v3 uses, because paths would offer it a vocabulary with no
	// prescription in them. What the two have in common is the set of things that can come out - a refusal, a failure, or a
	// possibly-changed payload - and that agreement is what lets the reporting be written once.
	//
	// This was two branches, which cost four copies of the eight lines that record a failure and two of the eight that record a
	// refusal. The bindings still differ and always will; only the reporting is shared.
	//
	// Both bindings take both languages. That was not true when this branch was written - the path binding was Lua-only, on the
	// grounds that a second binding would answer a neutralised write differently - and it stopped being true once those answers
	// moved onto PathMessage where both languages call them. Worth knowing when choosing a binding for a new format: the choice
	// is about vocabulary now, not about which languages the format can be scripted in.
	// Steps count as well as scripts, or a SCRIPT channel with declarative transformations and no scripts would skip this stage
	// and deliver the prescription unchanged - validated, reported, and inert.
	if c.cfg.FilterScript() != nil || c.cfg.TransformerScript() != nil || c.cfg.ScriptSteps().Len() > 0 {
		var (
			stage pharmacyScriptStage
			serr  error
		)

		switch {
		case claim != nil:
			stage, serr = c.runClaimScripts(claim, raw)
		case isScript:
			stage, serr = c.runPrescriptionScripts(raw)
		default:
			// Neither form parsed, which describeMessage has already refused above. Accepted and unchanged rather than a
			// second failure, because reporting one problem twice makes the first report look like two faults.
			stage = pharmacyScriptStage{accepted: true, raw: raw}
		}

		switch {
		case serr != nil:
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("a pharmacy script failed", "err", serr)

			record.Outcome = Failed
			record.Error = serr.Error()
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return nil, nil

		case !stage.accepted:
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)
			log.Info("a pharmacy transmission was excluded by the filter script")

			record.Outcome = Filtered
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return nil, nil

		case stage.changed:
			raw = stage.raw
			record.Raw = stage.raw

			// Only the claim binding hands back a reparsed form, because only it has one: the tree binding's output is
			// bytes and the destination filters below take their own evaluator.
			if stage.claim != nil {
				claim = stage.claim
			}

			log.Info("script transformations applied", "detail", stage.summary)
		}
	}

	// No parsed HL7 form handed downstream: there is none. Destination filters are supplied their own evaluator, so an
	// NCPDP destination filter is evaluated against the transmission rather than skipped.
	outcome, errs, deliveries := c.deliver(ctx, nil, raw, log, func(d *config.Destination) (bool, error) {
		f := d.NCPDPFilterExpr()
		if f == nil {
			return false, fmt.Errorf("the filter was not compiled for this channel's data type")
		}
		if claim == nil {
			return false, fmt.Errorf("there is no parsed transmission to evaluate against")
		}
		return f.Eval(claim)
	})

	record.Outcome = outcome
	record.Deliveries = deliveries
	record.Duration = time.Since(started)
	if len(errs) > 0 {
		record.Error = joinErrors(errs)
	}

	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	span.SetString("perfuse.pharmacy_message", summary)
	c.record(ctx, record)

	log.Info("pharmacy message handled",
		"outcome", outcome, "took", time.Since(started).Round(time.Millisecond))

	// No acknowledgement bytes. A SCRIPT VERIFY and an NCPDP response are both real messages a real system composes from
	// its own data, and inventing one here would tell the sender its prescription was accepted by a pharmacy that has
	// never seen it.
	if outcome == Failed {
		return nil, fmt.Errorf("the %s could not be delivered: %s", describePharmacy(isScript), record.Error)
	}
	return nil, nil
}

// describeMessage parses far enough to say what arrived, and returns the sender's identifier for it.
// describeMessage parses and summarises a pharmacy message.
//
// The parsed NCPDP transmission is returned rather than discarded, because a channel filter needs it. Nil for SCRIPT,
// which is XML and has no filter yet.
func describeMessage(raw []byte, isScript bool) (summary, controlID string, claim *ncpdp.Message, err error) {
	if isScript {
		m, err := eprescribe.Parse(raw)
		if err != nil {
			return "", "", nil, err
		}
		t, err := m.Type()
		if err != nil {
			return "", "", nil, err
		}
		name := eprescribe.TypeName(t)
		if name == "" {
			// A type this server does not have a name for is still a valid message. Reported by its code rather than
			// refused, because refusing would block a message type added to the standard after this was written.
			name = string(t)
		}
		return name, m.Header.MessageID, nil, nil
	}

	m, err := ncpdp.Parse(raw)
	if err != nil {
		return "", "", nil, err
	}

	name := ncpdp.TransactionName(m.Header.TransactionCode)
	if name == "" {
		name = fmt.Sprintf("transaction %s", m.Header.TransactionCode)
	}
	if len(m.Transactions) > 1 {
		name = fmt.Sprintf("%s, %d claims", name, len(m.Transactions))
	}

	// The prescription number, where there is one. A transmission of four claims has four, so only a single claim
	// contributes an identifier - a made-up composite would match nothing at either end.
	var id string
	if len(m.Transactions) == 1 {
		if seg, ok := m.Transactions[0].Segment(ncpdp.SegClaim); ok {
			id, _ = seg.Get(ncpdp.FieldPrescriptionRefNumber)
		}
	}
	return name, id, &m, nil
}

func describePharmacy(isScript bool) string {
	if isScript {
		return "prescription"
	}
	return "pharmacy claim"
}

// pharmacyUnparseable records a message that could not be read, and returns the error to report.
//
// Unparseable rather than Failed, because nothing was wrong with the delivery: the message never made sense. The
// distinction is what lets somebody tell a broken sender from a broken receiver, and they are different people.
func (c *Channel) pharmacyUnparseable(ctx context.Context, span *trace.Span, started time.Time, raw []byte, err error) error {
	c.count(func(s *Stats) { s.Unparseable++ })
	c.lastOutcome.set(Unparseable)
	span.SetString("perfuse.outcome", string(Unparseable))
	span.SetError(err)
	c.record(ctx, MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Outcome:    Unparseable,
		Error:      err.Error(),
		Duration:   time.Since(started),
	})
	return err
}

// pharmacyScriptStage is what either script binding produces.
//
// # Why this exists
//
// A claim and a prescription reach their scripts by different routes and leave by different ones: a claim goes through an
// accessor and comes back through ncpdp.Build, a prescription through an xtree and back through the stage's own serialiser. Those
// differences are real and are not going away.
//
// What they agree on is the set of results: the message was refused, something failed, or here is a payload that may have
// changed. Naming that agreement is what allows one branch and one piece of outcome reporting instead of two of each, and the
// reporting is where the duplication hurt - four copies of the same failure block drift apart one edit at a time, and the
// symptom is a message recorded with the wrong outcome rather than anything that looks like a bug.
type pharmacyScriptStage struct {
	// accepted is false only when a filter script rejected the message. A failure is an error, not a refusal.
	accepted bool

	// raw is the payload to carry forward, changed or not.
	raw []byte

	// claim is the reparsed claim, and is nil for a prescription. Only the path binding produces one, because only it parses
	// into a form the rest of the function uses.
	claim *ncpdp.Message

	// changed says whether raw differs from what came in, which decides whether anything is logged and whether the record is
	// updated. Kept separate from comparing bytes at the call site, because each binding already knows the answer and
	// recomputing it there would mean comparing payloads that can be megabytes.
	changed bool

	// summary is for the log line, since what counts as an interesting change differs: paths for a claim, and for a
	// prescription only that the document was rewritten, there being no path list to report.
	summary string
}

// runClaimScripts runs an NCPDP claim's filter and transformer through the path binding.
//
// Errors are wrapped with what went wrong rather than logged here, so that the one caller decides how a failure is recorded. That
// matters more than it sounds: the two branches this replaced logged four different messages and recorded three different strings
// in the message record, and no reader could tell which combination a given failure would produce.
func (c *Channel) runClaimScripts(claim *ncpdp.Message, raw []byte) (pharmacyScriptStage, error) {
	next, stage, err := runPathScriptStage(c, claim, ncpdp.Accessor{})
	if err != nil {
		return pharmacyScriptStage{}, fmt.Errorf("an ncpdp script failed: %w", err)
	}

	if !stage.Accepted {
		return pharmacyScriptStage{}, nil
	}

	out := pharmacyScriptStage{accepted: true, raw: raw, claim: claim}
	if len(stage.ScriptPaths) == 0 {
		return out, nil
	}

	// Rebuilt from the parsed form, with the same normalisation caveat as the declarative steps.
	next.SetHeaderCount()

	rebuilt, err := ncpdp.Build(*next)
	if err != nil {
		return pharmacyScriptStage{}, fmt.Errorf("the script-transformed transmission would not build: %w", err)
	}

	out.raw = rebuilt
	out.claim = next
	out.changed = true
	out.summary = fmt.Sprintf("%d path(s): %s", len(stage.ScriptPaths), strings.Join(stage.ScriptPaths, ","))

	return out, nil
}

// runPrescriptionScripts runs a SCRIPT prescription's filter and transformer through the tree binding.
//
// The tree is parsed here rather than reused from the channel filter above, which parses its own. Two parses of the same document
// is waste, but sharing one would mean parsing every prescription on a channel that has neither a filter nor a script - and most
// channels have neither.
func (c *Channel) runPrescriptionScripts(raw []byte) (pharmacyScriptStage, error) {
	tree, err := eprescribe.ParseTree(raw)
	if err != nil {
		// The message already parsed once in describeMessage, so reaching here means the tree parse disagreed with the typed
		// one. Failing rather than passing: a script that cannot be evaluated must not deliver the messages it existed to
		// exclude or change.
		return pharmacyScriptStage{}, fmt.Errorf("the script stage could not read the message: %w", err)
	}

	// The declarative steps first, then the scripts, which is the order every other format uses: a step is easier to read than a
	// script, so the reviewable half runs first and a script sees what the steps produced.
	//
	// On the tree this function already parsed rather than a second one. Two parses would be waste, and worse, a step applied to
	// one tree and a script to another would mean the script could not see the step's work - which is the sort of thing that reads
	// as the script being broken.
	stepsApplied := false

	if steps := c.cfg.ScriptSteps(); steps.Len() > 0 {
		if err := steps.Apply(tree.Root); err != nil {
			return pharmacyScriptStage{}, fmt.Errorf("a script transformation failed: %w", err)
		}
		stepsApplied = true
	}

	stage, err := c.runTreeScriptStage(tree.Root, raw)
	if err != nil {
		return pharmacyScriptStage{}, err
	}

	if !stage.Accepted {
		return pharmacyScriptStage{}, nil
	}

	out := pharmacyScriptStage{accepted: true, raw: stage.Raw}

	// Serialised here when only steps ran, because runTreeScriptStage returns the original bytes unless a transformer ran - it has
	// no way to know the tree was changed underneath it. Without this a channel with steps and no transformer would apply them to
	// the tree and then deliver the bytes that arrived, which is the defect shape this project keeps finding: work done, reported,
	// and discarded before it reaches the wire.
	if stepsApplied && c.cfg.TransformerScript() == nil {
		encoded := tree.Root.Marshal(2)
		if werr := xtree.WellFormed(encoded); werr != nil {
			return pharmacyScriptStage{}, fmt.Errorf("the transformed prescription is no longer valid XML: %w", werr)
		}
		out.raw = encoded
	}

	// Nothing to rebuild beyond that: the stage serialises the tree and checks the result is still well-formed XML itself, which
	// the claim binding cannot do because it has to go back through ncpdp.Build.
	if !bytes.Equal(out.raw, raw) {
		out.changed = true
		out.summary = "the document was rewritten"
	}

	return out, nil
}
