package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/trace"
)

// handleRaw delivers a payload Perfuse does not parse.
//
// # Why this path exists
//
// A great deal of what a site actually wants to automate is moving a file it does not need understood: a PDF report to a
// portal, a nightly zip to a partner's SFTP server, an image to an archive, a proprietary export a downstream system
// knows how to read. Requiring the engine to parse those would rule all of it out.
//
// # Why it cannot be unparseable
//
// Nothing about a raw payload can fail to parse, because nothing parses it. That has one consequence worth stating: a
// raw channel will never report Unparseable, so an empty or truncated file is delivered exactly as faithfully as a good
// one. The engine has no way to tell the difference and does not pretend to. What protects against a truncated file is
// the source's settle rule, not this path - which is why a raw channel reading a directory should not have stable_for
// turned off.
//
// The one thing refused here is an empty payload, because that is never a file somebody meant to send and delivering
// zero bytes to a downstream system is how an overwrite destroys yesterday's report.
func (c *Channel) handleRaw(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataRaw))

	started := time.Now()

	// The preprocessor. On a raw channel this is the only script that can change anything, and it is the one the old refusal
	// specifically said would be meaningful here: a raw payload is text rather than a tree, which is exactly what a
	// preprocessor reads and returns.
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
		// Counted as unparseable, which is the closest honest outcome: nothing was delivered and something was wrong.
		// Delivering it would let an empty file overwrite yesterday's report at the far end.
		err := fmt.Errorf("the payload is empty, and nothing downstream benefits from being sent zero bytes")

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
		return nil, err
	}

	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		// No control ID, because there is nowhere for one to come from. Left empty rather than invented: a synthesised
		// identifier looks like it came from the sending system and cannot be matched against anything there.
		Segments: 0,
	}

	log := c.log.With("bytes", len(raw))

	// No parsed form, because there is none. Filters and transformations are refused on a raw channel at load for
	// exactly this reason - they would find no structure and silently do nothing on every message.
	outcome, errs, deliveries := c.deliver(ctx, nil, raw, log, nil)

	record.Outcome = outcome
	record.Deliveries = deliveries
	record.Duration = time.Since(started)

	if len(errs) > 0 {
		record.Error = joinErrors(errs)
	}

	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	c.record(ctx, record)

	log.Info("raw payload handled",
		"outcome", outcome, "took", time.Since(started).Round(time.Millisecond))

	// No acknowledgement bytes. There is no format to build one in, and inventing one would be answering in a protocol
	// the sender does not speak.
	if outcome == Failed {
		return nil, fmt.Errorf("the payload could not be delivered: %s", record.Error)
	}
	return nil, nil
}
