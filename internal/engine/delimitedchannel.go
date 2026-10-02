package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/delimited"
	"github.com/biodream-llc/perfuse/internal/trace"
)

// handleDelimited routes a delimited document.
//
// Its own path for the same reason X12 and DICOM have theirs: nothing on the HL7 path applies to a row of columns.
//
// What is different here is that one arriving document can become many messages. A file of five thousand results is either one
// message or five thousand, and which it is changes what an acknowledgement means, what appears in the browser, and whether one
// malformed row loses a night's transfer.
func (c *Channel) handleDelimited(ctx context.Context, raw []byte) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)
	span.SetString("perfuse.data_type", string(config.DataDelimited))

	started := time.Now()

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

	settings, err := c.cfg.Delimited.Settings()
	if err != nil {
		// Should be unreachable, because validation resolves the settings at load. Reported rather than ignored so that a
		// channel constructed without validation fails loudly instead of parsing with defaults nobody chose.
		return nil, fmt.Errorf("the delimited settings are unusable: %w", err)
	}

	records, err := delimited.Parse(raw, settings)
	if err != nil {
		c.count(func(s *Stats) { s.Unparseable++ })
		c.log.Warn("rejected an unreadable delimited document", "err", err, "bytes", len(raw))

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

	if len(records) == 0 {
		// Not an error. An empty file from a nightly export means there was nothing to send, which is ordinary - and
		// recording it as unparseable would raise an alert every quiet night.
		c.log.Info("the delimited document contained no rows", "bytes", len(raw))
		return nil, nil
	}

	if !c.cfg.Delimited.Splits() {
		return c.deliverDelimited(ctx, raw, records, started, 0)
	}

	// Split. Each row is delivered on its own, so one bad row does not take the rest with it, and the outcome reported to the
	// sender is the worst of them - because telling a sender a file succeeded when four hundred rows failed is worse than
	// telling it something went wrong.
	failed := 0
	for _, record := range records {
		rowBytes := []byte(rowText(record, settings))
		if _, err := c.deliverDelimited(ctx, rowBytes, []delimited.Record{record}, time.Now(), record.Line); err != nil {
			failed++
			c.log.Error("a row could not be delivered", "line", record.Line, "err", err)
		}
	}

	c.log.Info("delimited document handled",
		"rows", len(records),
		"failed", failed,
		"took", time.Since(started).Round(time.Millisecond),
	)

	if failed > 0 {
		return nil, fmt.Errorf("%d of %d row(s) could not be delivered", failed, len(records))
	}

	return nil, nil
}

// deliverDelimited records and delivers one message, which is either a whole document or a single row.
func (c *Channel) deliverDelimited(ctx context.Context, raw []byte, records []delimited.Record,
	started time.Time, line int) ([]byte, error) {

	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
		Segments:   len(records),
	}
	describeDelimited(&record, records, line)

	log := c.log.With("control_id", record.ControlID)

	msg := delimited.Message(records)

	// The filter runs before the transformations, which is the order every other format uses. Filtering after transforming
	// would mean the expression tests values the sender never sent, so a rule written against the incoming feed silently
	// stops matching the day a step changes the column it reads.
	if filter := c.cfg.Delimited.FilterExpr(); filter != nil {
		match, err := filter.Eval(msg)
		if err != nil {
			// A filter that cannot be evaluated is a failure, not a pass. Treating it as a pass would deliver rows the
			// filter existed to exclude, which is the more dangerous of the two directions.
			record.Outcome = Unparseable
			record.Error = fmt.Errorf("the delimited filter could not be evaluated: %w", err).Error()
			record.Duration = time.Since(started)
			c.lastOutcome.set(Unparseable)
			c.record(ctx, record)

			return nil, err
		}

		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			record.Outcome = Filtered
			record.Duration = time.Since(started)
			c.record(ctx, record)

			// Recorded rather than discarded. On a split channel this is per row, so a file of five thousand can report
			// four thousand delivered and a thousand excluded - and somebody asking why a patient is missing gets an
			// answer instead of an absence.
			log.Info("a delimited row was excluded by the channel filter", "line", line)

			return nil, nil
		}
	}

	// Transformations run per message, which on a split channel means per row. That is what makes a path like PatientID mean
	// this row's identifier rather than the first row's, and it is why Set refuses to write across records: with split off
	// there is no single row to mean.
	if steps := c.cfg.Delimited.Steps(); steps.Len() > 0 {
		transformed, changes, err := steps.Apply(msg)
		if err != nil {
			record.Outcome = Failed
			record.Error = fmt.Errorf("a delimited transformation step failed: %w", err).Error()
			record.Duration = time.Since(started)
			c.lastOutcome.set(Failed)
			c.record(ctx, record)

			return nil, err
		}

		if len(changes) > 0 {
			msg = transformed

			// Raw becomes the transformed bytes, because that is what is sent and a destination file has to match what
			// the message store says was delivered.
			settings, err := c.cfg.Delimited.Settings()
			if err != nil {
				return nil, err
			}
			// rowText rather than writeRow: writeRow emits the fields and no line ending, so building the raw form from
			// it concatenated the rows into one line. The first end-to-end run produced
			// "P001,REDACTED,ICUP003,REDACTED,ICU" in the destination file, which is not a delimited document.
			var b strings.Builder
			for _, r := range msg {
				b.WriteString(rowText(r, settings))
			}
			raw = []byte(b.String())
			record.Raw = raw

			paths := make([]string, 0, len(changes))
			for _, ch := range changes {
				paths = append(paths, ch.Path)
			}
			log.Info("delimited transformations applied", "changes", len(changes), "paths", strings.Join(paths, ","))
		}
	}

	// The filter and transformer scripts, after the declarative steps.
	//
	// On a split channel this runs per row, which is what makes a path mean this row's column rather than the first row's - the
	// same reason the declarative steps run here. With split off the message is the whole document and delimited.Set refuses a
	// write across rows, so a script gets the same refusal a step would.
	if c.cfg.FilterScript() != nil || c.cfg.TransformerScript() != nil {
		next, stage, serr := runPathScriptStage(c, msg, delimited.Accessor{})
		if serr != nil {
			record.Outcome = Failed
			record.Error = serr.Error()
			record.Duration = time.Since(started)
			c.lastOutcome.set(Failed)
			c.record(ctx, record)

			return nil, serr
		}

		if !stage.Accepted {
			c.count(func(s *Stats) { s.Filtered++ })
			c.lastOutcome.set(Filtered)

			record.Outcome = Filtered
			record.Duration = time.Since(started)
			c.record(ctx, record)
			log.Info("a delimited row was excluded by the filter script", "line", line)

			return nil, nil
		}

		if len(stage.ScriptPaths) > 0 {
			msg = next

			// Settings resolved here rather than captured, because this runs in deliverDelimited where the value from
			// handleDelimited is out of scope. Validation resolves them at load, so a failure here is unreachable in
			// practice and reported rather than ignored.
			settings, serr := c.cfg.Delimited.Settings()
			if serr != nil {
				return nil, fmt.Errorf("the delimited settings are unusable: %w", serr)
			}

			var b strings.Builder
			for _, r := range msg {
				b.WriteString(rowText(r, settings))
			}
			raw = []byte(b.String())
			record.Raw = raw

			log.Info("delimited script transformations applied",
				"changes", len(stage.ScriptPaths), "paths", strings.Join(stage.ScriptPaths, ","))
		}
	}

	// No parsed HL7 form, because there is none. The HL7-shaped filter and transformations stay refused on a delimited
	// channel at load; what ran above is the delimited pair, which addresses columns.
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
	c.record(ctx, record)

	if outcome == Failed {
		return nil, fmt.Errorf("delivery failed: %s", record.Error)
	}

	return nil, nil
}

// describeDelimited fills the identifying fields of a record.
//
// A delimited row has no message type, control identifier or trigger event, so what goes in those fields is chosen rather than
// read. The line number is the identifier because it is the only thing that makes a row findable in the file it came from - and
// "which row failed" is the first question anybody asks.
//
// Nothing here is taken from a column. A column value could be a patient name, and these fields reach metric labels and logs.
func describeDelimited(record *MessageRecord, records []delimited.Record, line int) {
	record.MessageType = "delimited"

	switch {
	case line > 0:
		record.ControlID = fmt.Sprintf("line %d", line)
	case len(records) == 1:
		record.ControlID = fmt.Sprintf("line %d", records[0].Line)
	default:
		record.ControlID = fmt.Sprintf("%d rows", len(records))
	}
}

// rowText rebuilds one row as text.
//
// Rebuilt rather than sliced out of the original bytes. Slicing would be faster and would carry the original quoting through,
// but it cannot survive a quoted value containing a newline - and a row that silently loses half its content is worse than one
// that is re-encoded slightly differently.
//
// No header. That was the first attempt and it was wrong: a destination that appends produced a file with the header repeated
// before every row, which is not a readable CSV. Found by looking at the file rather than by reasoning about it.
//
// So the column names are metadata rather than payload - available for routing and naming, absent from the bytes. A site that
// wants one file with one header should not split; that is what the setting is for, and it is a clearer answer than a header
// emitted sometimes.
func rowText(record delimited.Record, settings delimited.Settings) string {
	var b strings.Builder
	writeRow(&b, record.Fields, settings)
	b.WriteString("\n")

	return b.String()
}

func writeRow(b *strings.Builder, fields []string, settings delimited.Settings) {
	for i, field := range fields {
		if i > 0 {
			b.WriteRune(settings.Delimiter)
		}

		// Quoted only when it has to be, so a value that needed no quoting arrives looking as it did. Doubling an embedded
		// quote is what the format requires and what every reader expects.
		needsQuote := settings.Quote != 0 && (strings.ContainsRune(field, settings.Delimiter) ||
			strings.ContainsRune(field, settings.Quote) ||
			strings.ContainsAny(field, "\r\n"))

		if !needsQuote {
			b.WriteString(field)
			continue
		}

		q := string(settings.Quote)
		b.WriteString(q)
		b.WriteString(strings.ReplaceAll(field, q, q+q))
		b.WriteString(q)
	}
}
