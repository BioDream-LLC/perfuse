package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/trace"
)

// handleDICOM routes one imaging object.
//
// Its own path for the same reason X12 has one: almost everything on the HL7 path assumes segments and fields, and an
// imaging object has neither. Threading it through would mean a nil parsed message travelling through code written on the
// assumption that one always exists.
//
// The difference from X12 is what replaces the acknowledgement. X12 has none; DICOM has one, but it belongs to the
// association rather than the message, so it is produced by the server after this returns - which is why this returns nil
// bytes and reports success or failure through the error instead.
func (c *Channel) handleDICOM(ctx context.Context, raw []byte) ([]byte, error) {
	// Parsed here only when nothing upstream did it. An object arriving over an association has no meta group, so its
	// transfer syntax comes from the negotiation rather than from the bytes - and guessing produces "the data set ends
	// part way through an element", which is what happened the first time a real study went through this path.
	return c.handleDICOMParsed(ctx, raw, nil)
}

// handleDICOMParsed routes an object, using a data set already parsed with the negotiated transfer syntax when there is
// one.
func (c *Channel) handleDICOMParsed(ctx context.Context, raw []byte, parsed *dicom.DataSet) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataDICOM))

	started := time.Now()

	ds, err := parsed, error(nil)
	if ds == nil {
		// A file rather than a network object: it carries its own meta group, so Parse can read the syntax from it.
		ds, err = dicom.Parse(raw)
	}
	if err != nil {
		c.count(func(s *Stats) { s.Unparseable++ })
		c.log.Warn("rejected an unreadable imaging object", "err", err, "bytes", len(raw))

		record := MessageRecord{
			Channel:    c.cfg.Name,
			ReceivedAt: started.UTC(),
			Raw:        raw,
			Outcome:    Unparseable,
			Error:      err.Error(),
			Duration:   time.Since(started),
		}
		c.lastOutcome.set(Unparseable)
		span.SetString("perfuse.outcome", string(Unparseable))
		span.SetError(err)
		c.record(ctx, record)

		return nil, err
	}

	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Segments:   len(ds.Elements),
	}
	describeDICOM(&record, ds)

	log := c.log.With(
		"sop_instance", record.ControlID,
		"modality", record.MessageType,
	)

	// The named steps, before delivery.
	//
	// Named rather than a path writer because the pixel data is in here: a general tag writer can set any tag to any bytes, and a
	// mistake produces an image that opens and is wrong rather than a message that is rejected. Each action knows which tags it
	// touches and none can reach the pixels.
	//
	// internal/dicom carried these for a while with no caller at all - the actions, dicom.Apply, and tests - which is the shape
	// the queue's section 7 names: an implementation with no caller is indistinguishable from a feature that does not exist. This
	// is the call that made it one.
	if steps := c.cfg.DICOMSteps(); len(steps) > 0 {
		next, changes, aerr := dicom.Apply(ds, steps)
		if aerr != nil {
			// Failed rather than delivered unchanged. A de-identify step that did not run means an object still carrying
			// patient identifiers is about to leave the hospital, and that is not something to log and continue past.
			c.count(func(s *Stats) { s.Failed++ })
			c.lastOutcome.set(Failed)
			log.Error("an imaging transformation failed", "err", aerr)

			record.Outcome = Failed
			record.Error = aerr.Error()
			record.Duration = time.Since(started)
			span.SetString("perfuse.outcome", string(Failed))
			span.SetError(aerr)
			c.record(ctx, record)

			return nil, fmt.Errorf("imaging transformation failed: %w", aerr)
		}

		if len(changes) > 0 {
			// Re-encoded in the syntax it arrived in. Choosing a different one would be a second change nobody asked for,
			// and a receiver negotiating a specific syntax is entitled to get it back.
			encoded, eerr := dicom.Encode(next.Elements, next.TransferSyntax)
			if eerr != nil {
				c.count(func(s *Stats) { s.Failed++ })
				c.lastOutcome.set(Failed)
				log.Error("a transformed imaging object could not be re-encoded", "err", eerr)

				record.Outcome = Failed
				record.Error = fmt.Sprintf("the transformed object would not encode: %v", eerr)
				record.Duration = time.Since(started)
				span.SetString("perfuse.outcome", string(Failed))
				c.record(ctx, record)

				return nil, fmt.Errorf("the transformed object would not encode: %w", eerr)
			}

			raw = encoded
			record.Raw = encoded
			ds = next

			log.Info("imaging transformations applied", "changes", len(changes))
		}
	}

	// No parsed HL7 form is passed, because there is none. Destination filters and HL7 transformations are refused on a DICOM
	// channel at load time for exactly this reason.
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

	if outcome == Failed {
		// Reported so the association layer can tell the sending modality. A store that failed downstream must not be
		// acknowledged as stored, or the modality deletes its copy and the study is gone.
		return nil, fmt.Errorf("delivery failed: %s", record.Error)
	}

	return nil, nil
}

// describeDICOM fills the identifying fields of a record from an imaging object.
//
// The SOP instance UID goes in ControlID and the modality in MessageType, so the message browser, the search box and the
// metrics labels all work unchanged. Reusing the fields rather than adding parallel ones means a DICOM channel appears in
// every existing view instead of needing each one taught about imaging.
//
// Nothing here is a patient identifier. The accession number is the one an operator searches by during an investigation,
// and it is not a name or a medical record number - which matters, because these values end up in metric labels and logs.
func describeDICOM(record *MessageRecord, ds *dicom.DataSet) {
	record.ControlID = ds.Text(dicom.TagSOPInstanceUID)
	record.MessageType = ds.Text(dicom.TagModality)
	record.TriggerEvent = ds.Text(dicom.TagAccessionNumber)

	if record.ControlID == "" {
		// An object with no SOP instance UID is unusual and worth noticing, but not worth refusing: it can still be
		// relayed, and the receiving archive will make its own decision.
		record.ControlID = "(no SOP instance UID)"
	}
}
