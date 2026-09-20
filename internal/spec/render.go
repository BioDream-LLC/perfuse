package spec

import (
	"fmt"
	"github.com/biodream-llc/perfuse/internal/codeset"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7dict"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// transformStep is an alias so the main file reads without the package prefix everywhere.
type transformStep = transform.Step

// mappingOf renders a translation table in full.
//
// In full deliberately. A code translation is the part of a specification a receiving vendor most
// needs and the part most often wrong in a hand-written one, because somebody transcribed thirty
// rows into prose. Generated from the channel, it cannot be transcribed wrongly.
func mappingOf(st transform.Step, tables *codeset.Set) *Mapping {
	if st.Map == nil {
		return nil
	}

	m := &Mapping{Path: st.Map.Path}

	// A step may hold its table inline or name a shared one. Both are resolved here.
	//
	// Only the inline map was read before, so a channel using a shared table documented an empty translation - zero rows under the
	// heading a receiving vendor relies on most. As a table it was quiet enough to miss; as a sentence it says "holds 0 entries; a
	// value not listed is passed through unchanged", which is a confident, specific and false statement about the part of the
	// interface most likely to be wrong.
	entries := st.Map.Table
	strict := st.Map.Strict
	fallback := st.Map.Default

	if st.Map.Use != "" {
		shared, found := lookupTable(tables, st.Map.Use)
		if !found {
			// Said, not guessed. The alternative is describing a translation this cannot see, and a specification that invents a
			// mapping is worse than one that admits it is missing.
			m.Unmatched = "this step uses the shared table " + st.Map.Use +
				", which is not available here, so its entries are not listed"

			return m
		}

		entries = map[string]string{}
		for _, e := range shared.Entries {
			entries[e.From] = e.To
		}
		strict = shared.Strict
		fallback = shared.Default
		if m.Name == "" {
			m.Name = st.Map.Use
		}
	}

	if seg, field, ok := splitPath(st.Map.Path); ok {
		if def, found := hl7dict.LookupField(seg, field); found {
			m.Name = def.Name
		}
	}

	// Sorted, so the same channel always documents its table in the same order. A Go map ranges
	// randomly, and a specification that reordered itself on every generation could not be
	// diffed - which is most of what a generated document is for.
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		m.From = append(m.From, k)
		m.To = append(m.To, entries[k])
	}

	switch {
	case strict:
		m.Unmatched = "a value not listed is an error, and the message is not delivered"
	case fallback != "":
		m.Unmatched = fmt.Sprintf("a value not listed becomes %q", fallback)
	default:
		m.Unmatched = "a value not listed is passed through unchanged"
	}

	return m
}

// lookupTable finds a shared table by name.
func lookupTable(tables *codeset.Set, name string) (*codeset.Table, bool) {
	if tables == nil {
		return nil, false
	}

	return tables.Table(name)
}

// describeTransport says where a destination sends, without credentials.
//
// Never a DSN, never a token. A specification is a document that gets emailed to a vendor, which
// is the last place a password should end up.
// DescribeTransport says where a destination sends, in one phrase.
//
// Exported because three places were describing destinations independently and the CLI's version knew only about
// addresses and directories - so every other destination type printed a blank where its target should be. A single
// function means a new destination type is described everywhere at once, or nowhere, rather than in two places out
// of three.
func DescribeTransport(d config.Destination) string { return describeTransport(d) }

func describeTransport(d config.Destination) string {
	switch d.Type {
	case config.DestinationMLLP:
		out := "HL7 over MLLP to " + d.Address
		if d.TLS != nil && d.TLS.Enabled {
			out += ", encrypted with TLS"
		}
		return out

	case config.DestinationFile:
		return "written as files to " + d.Dir

	case config.DestinationHTTP:
		if d.HTTP != nil {
			method := d.HTTP.Method
			if method == "" {
				method = "POST"
			}
			return method + " to " + d.HTTP.URL
		}
		return "posted to an HTTP endpoint"

	case config.DestinationFHIR:
		if d.FHIR != nil {
			version := d.FHIR.Version
			if version == "" {
				version = "the default release"
			}
			return "converted to FHIR (" + version + ") and posted to " + d.FHIR.URL
		}
		return "converted to FHIR"

	case config.DestinationCDA:
		if d.CDA != nil && d.CDA.Dir != "" {
			return "converted to a CDA document in " + d.CDA.Dir
		}
		return "converted to a CDA document"

	case config.DestinationDatabase:
		if d.Database != nil {
			// The statement, not the connection string. The statement is what a receiving
			// team needs to review; the DSN is a credential.
			return "inserted into a " + d.Database.Driver + " database"
		}
		return "written to a database"

	case config.DestinationS3:
		// The bucket and key pattern, never the credentials. A specification gets emailed to a vendor.
		if d.S3 != nil {
			where := d.S3.Bucket
			if d.S3.Endpoint != "" {
				where += " at " + d.S3.Endpoint
			}
			return fmt.Sprintf("stored as an object in %s", where)
		}
		return "stored as an object in S3"

	case config.DestinationBroker:
		if d.Broker != nil {
			out := fmt.Sprintf("published to %s on the broker at %s", d.Broker.Destination, d.Broker.Addr)
			if !d.Broker.IsPersistent() {
				out += ", not persistently"
			}
			return out
		}
		return "published to a message broker"

	case config.DestinationJavaScript:
		// The line count rather than the script. A specification is read by someone deciding whether an interface does
		// what they need, and a page of JavaScript in the middle of it answers a different question.
		if d.JavaScript != nil {
			lines := strings.Count(strings.TrimSpace(d.JavaScript.Script), "\n") + 1
			return fmt.Sprintf("handled by a script of %d line(s) rather than sent anywhere", lines)
		}
		return "handled by a script"

	case config.DestinationDICOM:
		if d.DICOM != nil {
			return fmt.Sprintf("stored to the archive at %s as %s", d.DICOM.Address, d.DICOM.CalledAE)
		}
		return "stored to a DICOM archive"

	case config.DestinationSOAP:
		// The endpoint and the action, never the credentials. A specification gets emailed to a vendor.
		if d.SOAP != nil {
			if d.SOAP.Action != "" {
				return fmt.Sprintf("sent as a SOAP request to %s, action %s", d.SOAP.URL, d.SOAP.Action)
			}
			return fmt.Sprintf("sent as a SOAP request to %s", d.SOAP.URL)
		}
		return "sent as a SOAP request"

	case config.DestinationDocument:
		if d.Document != nil {
			format := "PDF"
			if d.Document.Format == config.DocumentText {
				format = "text"
			}
			return fmt.Sprintf("rendered as a %s document into %s", format, d.Document.Dir)
		}
		return "rendered as a document"

	case config.DestinationFTP:
		// The scheme is named because "ftp" and "ftps" are a meaningful difference to whoever reads a
		// specification, and never the password.
		if d.FTP != nil {
			scheme := "FTPS"
			if d.FTP.Security == config.FTPSecurityNone {
				scheme = "FTP (unencrypted)"
			}
			return fmt.Sprintf("uploaded over %s to %s in %s", scheme, d.FTP.Host, d.FTP.Dir)
		}
		return "uploaded to an FTP server"

	case config.DestinationSFTP:
		if d.SFTP != nil {
			return fmt.Sprintf("uploaded to %s in %s", d.SFTP.Host, d.SFTP.Dir)
		}
		return "uploaded to an SFTP server"

	case config.DestinationSMTP:
		if d.SMTP != nil {
			what := "a summary"
			if d.SMTP.Attach {
				what = "the message itself"
			}
			return fmt.Sprintf("emailed to %d recipient(s) as %s",
				len(d.SMTP.Recipients()), what)
		}
		return "emailed"

	case config.DestinationChannel:
		// Named as an internal hop rather than as a delivery. A receiving system reading this
		// specification is not being told about a network endpoint they can test; they are being told
		// this feed continues somewhere else in the same server.
		if d.Channel != nil {
			return fmt.Sprintf("handed to the %q channel in this server, which applies its own rules",
				d.Channel.Name)
		}
		return "handed to another channel in this server"
	}

	return string(d.Type)
}

// destinationPaths lists the paths a destination filter reads.
func destinationPaths(d config.Destination) []string {
	if d.Filter == "" {
		return nil
	}
	if e := d.FilterExpr(); e != nil {
		return e.Paths()
	}
	return pathsInCondition(d.Filter)
}

// pathsInCondition extracts path-looking tokens from an expression.
//
// A fallback for when a compiled expression is not available. It is approximate on purpose and
// errs towards listing too much: overstating what an interface depends on makes a specification
// cautious, while understating it makes the document wrong in the direction that causes outages.
func pathsInCondition(cond string) []string {
	var out []string
	seen := map[string]bool{}

	for _, token := range strings.FieldsFunc(cond, func(r rune) bool {
		return r == ' ' || r == '(' || r == ')' || r == ',' || r == '\'' || r == '"' ||
			r == '=' || r == '!' || r == '<' || r == '>' || r == '[' || r == ']'
	}) {
		seg, _, ok := splitPath(token)
		if !ok || len(seg) < 2 || len(seg) > 3 {
			continue
		}
		if _, described := hl7dict.LookupSegment(seg); !described {
			continue
		}
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

// caveats states what the document cannot tell you.
//
// This is the section that makes the rest trustworthy. A generated specification that quietly
// omitted the existence of a script would describe half an interface as though it were all of it,
// and somebody would build against it.
func caveats(c *config.Channel, doc *Document) []string {
	var out []string

	if c.Scripts != nil {
		out = append(out,
			"This channel runs JavaScript as well as the steps listed above. Scripts can read and "+
				"change any part of a message, so this document describes what the configuration "+
				"does and cannot describe what the scripts do. Treat the field lists as a minimum.")
	}

	if c.Type() == config.DataX12 {
		out = append(out,
			"This is an X12 channel. It returns no acknowledgement in the response; a 997 or 999 "+
				"is sent back separately as its own interchange.")
	}

	if c.Type() == config.DataHL7v3 {
		// The filter is described here rather than in the field lists, because those are built from v2 paths and a v3
		// filter would be absent from them - which would make a document say a channel forwards everything when it
		// does not.
		if expr := c.HL7v3.FilterExpression(); expr != "" {
			out = append(out, fmt.Sprintf(
				"This is an HL7 v3 channel. It forwards only messages matching %s. The field lists below "+
					"describe HL7 v2 paths and do not apply.", expr))
		} else {
			out = append(out,
				"This is an HL7 v3 channel with no filter, so every message is forwarded. The field lists "+
					"below describe HL7 v2 paths and do not apply.")
		}

		// The transformations are described here for the same reason as the filter: the field tables below are
		// built from v2 paths, so a v3 step would be absent from them and the document would say the channel
		// passes messages through unchanged when it does not. A specification that understates what a channel
		// does to clinical data is worse than no specification.
		if steps := c.HL7v3.Transformations; len(steps) > 0 {
			out = append(out, fmt.Sprintf("It changes the content of each message in %s before sending it:",
				plural(len(steps), "step", "steps")))
			for i, st := range steps {
				out = append(out, fmt.Sprintf("  %d. %s", i+1, DescribeV3Step(st)))
			}
		}

		if !c.Scripts.Empty() {
			// Same caveat as a v2 channel, and for the same reason: a script can read and change any part of
			// a document, so a specification listing only the declarative steps would describe part of the
			// pipeline as though it were all of it.
			out = append(out, "It also runs JavaScript, which this document cannot describe. What a scripted "+
				"interface does has to be established by reading the script.")
		}

		if c.HL7v3.ShouldAcknowledge() {
			out = append(out,
				"It answers each message with an MCCI_IN000002UV01 acknowledgement in the response body.")
		} else {
			// Worth stating, because a sender expecting a reply and receiving none will usually retry.
			out = append(out,
				"It returns no acknowledgement. A sender expecting one will normally retry, so this "+
					"suits only a feed that does not.")
		}
	}

	// doc.Reads and doc.Writes are built from HL7 v2 paths, so a v3 channel's steps are absent from both and this
	// sentence fired regardless of how much the channel changed. The generated document said, two lines apart, that the
	// channel masks a birth date and that it passes messages through exactly as they arrive.
	//
	// Worth more than a tidy-up. This is the sentence somebody pastes into a change request as evidence that nothing
	// happens to the data, and it was false on every v3 channel with a transformation.
	v3Changes := 0
	if c.Type() == config.DataHL7v3 && c.HL7v3 != nil {
		v3Changes = len(c.HL7v3.Transformations)
	}

	if len(doc.Reads) == 0 && len(doc.Writes) == 0 && c.Scripts == nil && v3Changes == 0 {
		out = append(out,
			"This channel does not read or change any field. Messages are passed through exactly "+
				"as they arrive, which means it is a transport rather than an interface.")
	}

	for _, d := range c.Destinations {
		if !d.Queue.IsEnabled() && d.Retry.Attempts == 0 {
			out = append(out, fmt.Sprintf(
				"The %s destination has no queue and no retries, so a message that cannot be "+
					"delivered at the moment it arrives is recorded as failed and not sent again.",
				d.Name))
		}
	}

	if c.Source.Ack.When == config.AckOnReceipt {
		out = append(out,
			"The sender is acknowledged as soon as a message is accepted, before any destination "+
				"has been written. An acknowledged message can therefore still be lost if this "+
				"process stops with work outstanding.")
	}

	return out
}

// describeArrival says how messages reach the interface, without credentials.
func describeArrival(c *config.Channel) string {
	src := c.Source

	switch src.Type {
	case config.SourceMLLP:
		out := "the sender connects to " + src.Listen + " and sends HL7 over MLLP"
		if src.TLS != nil && src.TLS.Enabled {
			out += ", encrypted with TLS"
			if src.TLS.RequireClientCert {
				// Worth stating in a specification: it is the setting most likely to stop a
				// new sender connecting at all, and the failure looks like a network fault.
				out += ", and must present a client certificate"
			}
		}
		return out

	case config.SourceBroker:
		if src.Broker == nil {
			return "messages are read from a message broker"
		}
		out := fmt.Sprintf("messages are read from %s on the broker at %s", src.Broker.Destination, src.Broker.Addr)
		if src.Broker.Selector != "" {
			// The selector is named because it decides which messages this interface sees, which is exactly the sort of thing
			// a reader of a specification needs. It contains field names rather than patient data.
			out += ", matching " + src.Broker.Selector
		}
		return out

	case config.SourceDICOMQuery:
		if src.DICOMQuery == nil {
			return "an imaging archive is polled for studies"
		}
		q := src.DICOMQuery
		out := fmt.Sprintf("the imaging archive at %s is asked every %s which %s it holds",
			q.Address, q.Interval, strings.ToLower(q.ResolvedLevel())+" records")
		if q.Window > 0 {
			out += fmt.Sprintf(", looking back %s", q.Window)
		}
		// The match keys are named but their values are not. A value can be a patient identifier, and a specification is a
		// document people paste into tickets and email to vendors.
		if len(q.Match) > 0 {
			out += ", matching on " + strings.Join(sortedKeys(q.Match), ", ")
		}
		return out

	case config.SourceDICOM:
		if src.DICOM == nil {
			return "a modality or archive sends imaging objects"
		}
		out := "a modality or archive stores imaging objects to " + src.DICOM.Listen
		if src.DICOM.AETitle != "" {
			out += ", addressed to " + src.DICOM.AETitle
		}
		return out

	case config.SourceSOAP:
		// The endpoint and how the message is carried, never the credentials. A specification gets emailed out.
		if src.SOAP == nil {
			return "the sender posts a SOAP envelope"
		}
		out := "the sender posts a SOAP envelope to " + src.SOAP.Listen + src.SOAP.SOAPPath()
		if src.SOAP.Element != "" {
			out += ", with the message in a " + src.SOAP.Element + " element"
		}
		if src.SOAP.Base64 {
			out += ", base64 encoded"
		}
		return out

	case config.SourceHTTP:
		if src.HTTP == nil {
			return "the sender posts messages over HTTP"
		}
		out := "the sender posts messages to " + src.HTTP.Listen + src.HTTP.Path
		if src.HTTP.Token != "" {
			out += ", presenting an agreed token"
		}
		return out

	case config.SourceDatabase:
		if src.Database == nil {
			return "messages are read from a database"
		}
		return fmt.Sprintf("messages are read from a %s database every %s",
			src.Database.Driver, src.Database.PollInterval)

	case config.SourceSFTP:
		if src.SFTP == nil {
			return "message files are collected from an SFTP server"
		}
		return fmt.Sprintf(
			"message files are collected from %s in %s every %s, and moved to %s once read",
			src.SFTP.Host, src.SFTP.Dir, src.SFTP.PollInterval, src.SFTP.MoveTo)

	case config.SourceJavaScript:
		if src.JavaScript == nil {
			return "a script generates messages on a timer"
		}
		interval := src.JavaScript.PollInterval
		if interval == 0 {
			interval = config.DefaultJSSourcePollInterval
		}
		return fmt.Sprintf("a script runs every %s and feeds its output into the channel as messages", interval)
	}

	return string(src.Type)
}

// describePromise says what the sender is told, which is the contract.
func describePromise(c *config.Channel) string {
	if c.Type() == config.DataX12 {
		return "nothing in the response. An acknowledgement is a 997 or 999 returned separately " +
			"as its own interchange"
	}

	if c.Source.Ack.When == config.AckOnReceipt {
		return "acknowledged as soon as the message is accepted, before any destination has been " +
			"written"
	}
	return "acknowledged once every destination has been written, so an acknowledgement means " +
		"the message arrived"
}

// sortedKeys lists a map's keys in order.
//
// Sorted because a specification gets diffed, and Go maps range randomly enough that an unsorted list would make every
// regeneration look like a change.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// plural writes a count with the right noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}

	return fmt.Sprintf("%d %s", n, many)
}

// DescribeV3Step says what one HL7 v3 transformation step does, in words.
//
// Words rather than the YAML, because the reader of a specification is often not the person who wrote the channel - it is
// somebody at the other end of the feed asking what happens to their data, or an auditor asking what is changed.
//
// The three removals are described distinctly and deliberately. "Clears" and "records that no value is available" and
// "removes" read almost the same in a summary, and they are not the same: a masked address is a decision somebody made, and
// a downstream system that fills the gap from another source has undone it.
func DescribeV3Step(st hl7v3.Step) string {
	var what string

	switch {
	case st.Set != nil:
		what = fmt.Sprintf("sets %s to %q", st.Set.Path, st.Set.Value)
	case st.Copy != nil:
		what = fmt.Sprintf("copies %s to %s", st.Copy.From, st.Copy.To)
	case st.Clear != nil:
		what = fmt.Sprintf("empties %s, without stating why it is empty", st.Clear.Path)
	case st.NullFlavor != nil:
		what = fmt.Sprintf("removes the value at %s and records the reason as %s (%s)",
			st.NullFlavor.Path, st.NullFlavor.Reason, hl7v3.NullFlavor(st.NullFlavor.Reason).Explain())
	case st.Remove != nil:
		what = fmt.Sprintf("removes the element %s entirely, meaning it does not apply", st.Remove.Path)
	case st.Map != nil:
		missing := "leaves an untranslated value unchanged"
		switch st.Map.OnMissing {
		case "clear":
			missing = "empties an untranslated value"
		case "fail":
			missing = "refuses the message if a value is not in the table"
		}
		what = fmt.Sprintf("translates %s through the %s table, and %s", st.Map.Path, st.Map.Table, missing)
	case st.Replace != nil:
		what = fmt.Sprintf("rewrites %s, replacing %s with %q", st.Replace.Path, st.Replace.From, st.Replace.To)
	case st.Trim != nil:
		what = fmt.Sprintf("removes surrounding whitespace from %s", st.Trim.Path)
	case st.Case != nil:
		what = fmt.Sprintf("converts %s to %s case", st.Case.Path, st.Case.To)
	default:
		// Unreachable through a loaded channel, since a step with no action is refused at load. Described rather
		// than omitted so that a step this function has not been taught cannot silently vanish from a document
		// somebody is relying on to be complete.
		what = "does something this description does not yet cover"
	}

	if st.When != "" {
		what += fmt.Sprintf(", but only when %s", st.When)
	}
	if st.Description != "" {
		what += fmt.Sprintf(" — %s", st.Description)
	}

	return what
}
