package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/edifact"
	"github.com/biodream-llc/perfuse/internal/trace"
)

// contrlSeq numbers the CONTRL interchanges this process sends.
var contrlSeq atomic.Uint64

// handleEDIFACT reads an interchange, checks its envelope, filters it, optionally splits it, and delivers it unchanged.
// With acknowledge: contrl it returns a CONTRL saying whether the interchange was accepted.
func (c *Channel) handleEDIFACT(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataEDIFACT))
	started := time.Now()
	opts := c.cfg.EDIFACT

	if repaired, perr := c.runTextScripts(ctx, raw); perr != nil {
		c.count(func(s *Stats) { s.Failed++ })
		c.lastOutcome.set(Failed)
		c.record(ctx, MessageRecord{Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw, Outcome: Failed,
			Error: perr.Error(), Duration: time.Since(started)})
		return nil, perr
	} else {
		raw = repaired
	}

	unreadable := func(err error) ([]byte, error) {
		c.count(func(s *Stats) { s.Unparseable++ })
		c.lastOutcome.set(Unparseable)
		span.SetString("perfuse.outcome", string(Unparseable))
		span.SetError(err)
		c.record(ctx, MessageRecord{Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: raw, Outcome: Unparseable,
			Error: err.Error(), Duration: time.Since(started)})
		c.log.Warn("an EDIFACT interchange could not be read", "error", err.Error())
		return nil, err
	}

	ic, err := edifact.Parse(raw)
	if err != nil {
		return unreadable(err)
	}
	contrl := func(action edifact.Action) []byte {
		if opts == nil || opts.Acknowledge != config.EDIFACTAckCONTRL {
			return nil
		}
		return edifact.CONTRL(ic, action, fmt.Sprintf("PF%010d", contrlSeq.Add(1)), time.Now()).Bytes()
	}
	if mode := opts.EnvelopeMode(); mode != "ignore" {
		if probs := edifact.Check(ic); len(probs) > 0 {
			list := make([]string, len(probs))
			for i, p := range probs {
				list[i] = p.String()
			}
			if mode == "require" {
				_, err := unreadable(fmt.Errorf("edifact: the envelope does not match its contents (%s), which is how a "+
					"transfer cut short shows", strings.Join(list, "; ")))
				return contrl(edifact.Rejected), err
			}
			c.log.Warn("an EDIFACT envelope does not match its contents; accepted as edifact.envelope is warn",
				"problems", strings.Join(list, "; "))
		}
	}

	pieces := []*edifact.Interchange{ic}
	if opts != nil && opts.Split {
		pieces = edifact.Split(ic)
	}
	var failed []string
	for _, piece := range pieces {
		body := raw
		if len(pieces) > 1 {
			body = piece.Bytes()
		}
		if err := c.deliverEDIFACT(ctx, span, started, piece, body); err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return contrl(edifact.Rejected), fmt.Errorf("the EDIFACT interchange could not be delivered: %s", strings.Join(failed, "; "))
	}
	return contrl(edifact.Accepted), nil
}

// deliverEDIFACT filters and delivers one interchange, and records it.
func (c *Channel) deliverEDIFACT(ctx context.Context, span *trace.Span, started time.Time, ic *edifact.Interchange, body []byte) error {
	c.count(func(s *Stats) { s.Received++ })
	msgs := ic.AllMessages()
	record := MessageRecord{Channel: c.cfg.Name, ReceivedAt: started.UTC(), Raw: body, ControlID: ic.Ref()}
	if len(msgs) == 1 {
		record.ControlID = ic.Ref() + "/" + msgs[0].Ref()
	}
	summary := edifact.Describe(ic)
	log := c.log.With("bytes", len(body), "interchange", summary)

	if f := c.cfg.EDIFACTFilter(); f != nil {
		match, err := f.Eval(ic)
		if err != nil {
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			record.Outcome, record.Error, record.Duration = Failed, "edifact filter failed: "+err.Error(), time.Since(started)
			c.record(ctx, record)
			log.Error("the edifact channel filter failed to evaluate", "err", err)
			return err
		}
		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)
			record.Outcome, record.Duration = Filtered, time.Since(started)
			c.record(ctx, record)
			log.Info("an EDIFACT interchange was excluded by the channel filter")
			return nil
		}
	}

	outcome, errs, deliveries := c.deliver(ctx, nil, body, log, func(d *config.Destination) (bool, error) {
		f := d.EDIFACTFilterExpr()
		if f == nil {
			return false, fmt.Errorf("the filter was not compiled for this channel's data type")
		}
		return f.Eval(ic)
	})
	record.Outcome, record.Deliveries, record.Duration = outcome, deliveries, time.Since(started)
	if len(errs) > 0 {
		record.Error = joinErrors(errs)
	}
	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	c.record(ctx, record)
	log.Info("EDIFACT interchange handled", "outcome", outcome, "took", time.Since(started).Round(time.Millisecond))
	if outcome == Failed {
		return fmt.Errorf("%s", record.Error)
	}
	return nil
}
