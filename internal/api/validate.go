package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/spec"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Validating a channel without saving it.
//
// The graphical builder needs to be able to ask "is this valid yet?" on every keystroke,
// and it must be able to ask without creating a file. Without this the only way to find out
// whether a channel is well formed is to try to create it, which either succeeds - leaving
// a half-finished channel on disk - or fails and teaches the user nothing until they have
// already committed.
//
// This runs the same config.Load and the same Validate that a file on disk goes through, so
// a channel that validates here loads at startup. A separate validation path that agreed
// with the real one most of the time would be worse than none: it would be trusted.

type validatePayload struct {
	YAML string `json:"yaml"`
}

// validateProblem is one thing wrong with the submitted YAML.
type validateProblem struct {
	// Line is 1-based, or 0 when the problem is not about a particular line.
	// A parse error usually knows its line; a semantic error usually does not.
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

type validateResponse struct {
	OK       bool              `json:"ok"`
	Problems []validateProblem `json:"problems"`

	// Summary describes the channel in plain language when it is valid, so the
	// builder can show what it just built rather than only that it parsed.
	Summary *channelPlain `json:"summary,omitempty"`
}

// channelPlain is a description of a channel meant to be read out loud.
//
// The builder shows this back to the user as confirmation. Somebody who has just filled in
// a form does not necessarily know whether they have built what they meant to, and a
// sentence they can check against their intention is the cheapest way to find out.
type channelPlain struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	DataType     string   `json:"dataType"`
	Receives     string   `json:"receives"`
	Acknowledges string   `json:"acknowledges"`
	Filter       string   `json:"filter,omitempty"`
	Steps        []string `json:"steps,omitempty"`
	Sends        []string `json:"sends"`
	Warnings     []string `json:"warnings,omitempty"`
}

// handleValidateChannel checks YAML and reports what it would build.
func (s *Server) handleValidateChannel(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req validatePayload
	if !s.decode(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.YAML) == "" {
		// Empty is reported as not-yet-valid rather than as an error, because the builder
		// asks about an empty form before the user has typed anything and a red banner at
		// that moment trains people to ignore red banners.
		s.ok(w, validateResponse{
			OK:       false,
			Problems: []validateProblem{{Message: "nothing to validate yet"}},
		})
		return
	}

	// The path is only used in error messages. Naming it something recognisable is
	// better than an empty string appearing in the middle of a sentence.
	c, err := config.Load(bytes.NewReader([]byte(req.YAML)), "(unsaved)")
	if err != nil {
		s.ok(w, validateResponse{
			OK:       false,
			Problems: problemsFromError(err),
		})
		return
	}

	s.ok(w, validateResponse{OK: true, Problems: []validateProblem{}, Summary: describeChannel(c)})
}

// problemsFromError turns a load error into per-line problems where it can.
//
// A YAML error carries its line inside the message text, which is useless to an editor
// that wants to put a marker in the gutter. Pulling the number out is the difference
// between a red box under the form and a mark on the line that is wrong.
func problemsFromError(err error) []validateProblem {
	text := err.Error()

	// A multi-problem validation error arrives as several lines. Each becomes its own
	// problem so the builder can list them rather than showing one wall of text.
	var out []validateProblem
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, validateProblem{Line: yamlLine(line), Message: line})
	}
	if len(out) == 0 {
		out = append(out, validateProblem{Message: text})
	}
	return out
}

// yamlLine extracts a line number from a yaml error message, or returns 0.
func yamlLine(msg string) int {
	// yaml.v3 writes "yaml: line 7: ..." and "yaml: unmarshal errors:\n  line 7: ...".
	i := strings.Index(msg, "line ")
	if i < 0 {
		return 0
	}
	rest := msg[i+len("line "):]

	n := 0
	digits := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		digits++
	}
	if digits == 0 {
		return 0
	}
	return n
}

// describeChannel renders a validated channel as sentences.
func describeChannel(c *config.Channel) *channelPlain {
	out := &channelPlain{
		Name:     c.Name,
		Enabled:  c.IsEnabled(),
		DataType: string(c.Type()),
		Filter:   c.Filter,
	}

	out.Receives = describeSource(c)
	out.Acknowledges = describeAck(c)

	for _, st := range c.Transformations {
		out.Steps = append(out.Steps, st.Describe())
	}

	// A v3 channel's steps live under hl7v3 and would otherwise be missing from this summary entirely - so the
	// interface would show a channel that changes patient data as one that passes it through, and the warning below
	// about "no filter, transformations or scripts" would fire on a channel with three transformations.
	if c.Type() == config.DataHL7v3 && c.HL7v3 != nil {
		for _, st := range c.HL7v3.Transformations {
			out.Steps = append(out.Steps, spec.DescribeV3Step(st))
		}
	}

	for _, d := range c.Destinations {
		out.Sends = append(out.Sends, describeDestination(d))
	}
	// No warning for an empty destination list: Validate refuses that outright, so a
	// channel reaching this point always has one. A warning for a state that cannot occur
	// is dead code that reads like a supported case.

	// Counted across both vocabularies. Checking only the v2 fields would have told somebody their v3 channel passed
	// messages through unchanged while it was masking a birth date, which is the kind of wrong that gets believed.
	v3Steps, v3Filter := 0, ""
	if c.Type() == config.DataHL7v3 && c.HL7v3 != nil {
		v3Steps = len(c.HL7v3.Transformations)
		v3Filter = c.HL7v3.FilterExpression()
	}
	if c.Filter == "" && v3Filter == "" && len(c.Transformations) == 0 && v3Steps == 0 && c.Scripts == nil {
		out.Warnings = append(out.Warnings, "no filter, transformations or scripts: messages are passed through unchanged")
	}
	if c.Scripts != nil {
		out.Warnings = append(out.Warnings, "this channel runs JavaScript, so its behaviour cannot be established by reading the configuration alone")
	}

	return out
}

// describeSource says where messages arrive from, in a sentence.
func describeSource(c *config.Channel) string {
	src := c.Source

	switch src.Type {
	case config.SourceMLLP:
		out := "MLLP connections on " + src.Listen
		if src.TLS != nil {
			out += " over TLS"
			if src.TLS.RequireClientCert {
				// Worth saying, because requiring a client certificate is the single
				// most common cause of a feed that will not connect.
				out += ", requiring a client certificate"
			}
		}
		return out

	case config.SourceKafka:
		if src.Kafka == nil {
			return "a Kafka topic"
		}
		out := "kafka " + strings.Join(src.Kafka.Topics, ", ") + " at " + strings.Join(src.Kafka.Brokers, ", ")
		if src.Kafka.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceBroker:
		if src.Broker == nil {
			return "a message broker"
		}
		out := "broker " + src.Broker.Destination + " at " + src.Broker.Addr
		if src.Broker.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceDICOMQuery:
		if src.DICOMQuery == nil {
			return "DICOM C-FIND"
		}
		out := "DICOM C-FIND against " + src.DICOMQuery.Address + " every " + src.DICOMQuery.Interval.String()
		if src.DICOMQuery.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceDICOM:
		if src.DICOM == nil {
			return "DICOM C-STORE"
		}
		out := "DICOM objects stored to " + src.DICOM.Listen
		if src.DICOM.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceSOAP:
		if src.SOAP == nil {
			return "SOAP requests"
		}
		out := "SOAP envelopes posted to " + src.SOAP.Listen + src.SOAP.SOAPPath()
		if src.SOAP.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceHTTP:
		if src.HTTP == nil {
			return "HTTP requests"
		}
		out := "HTTP POSTs to " + src.HTTP.Listen + src.HTTP.Path
		if src.TLS != nil {
			out += " over TLS"
		}
		return out

	case config.SourceDatabase:
		if src.Database == nil {
			return "rows from a database"
		}
		// The DSN is never shown. It carries a password, and a description that leaks
		// one into a screenshot is worse than a vague description.
		return fmt.Sprintf("rows polled from a %s database every %s",
			src.Database.Driver, src.Database.PollInterval)

	case config.SourceSFTP:
		if src.SFTP == nil {
			return "files from an SFTP server"
		}
		return fmt.Sprintf("files collected from %s:%s every %s",
			src.SFTP.Host, src.SFTP.Dir, src.SFTP.PollInterval)
	}

	return string(src.Type)
}

// describeAck says what the sender is promised, which is the most consequential
// setting in a channel.
func describeAck(c *config.Channel) string {
	if c.Type() == config.DataX12 {
		// Said plainly because it is the thing an HL7 engineer will assume wrongly.
		// X12 has no synchronous acknowledgement; a 997 or 999 is a separate transaction.
		if level := c.X12.Acknowledgement(); level != "" {
			// The sender identifier is named because it is the thing a trading partner has to recognise, and a
			// summary that omitted it would leave the most common misconfiguration invisible. It is an
			// identifier rather than a credential, so naming it discloses nothing.
			return "with a " + level + " in the response, as " + c.X12.AckSenderID
		}

		return "nothing synchronously: X12 acknowledges with a 997 or 999 sent as its own interchange"
	}

	if c.Type() == config.DataHL7v3 {
		if !c.HL7v3.ShouldAcknowledge() {
			// Worth being explicit about, because a v3 sender is usually waiting and will retry.
			return "nothing: this v3 channel does not acknowledge, so a sender expecting a reply will retry"
		}

		// The device is named for the same reason the X12 sender identifier is: it is what the receiver has to
		// recognise, and a summary omitting it would leave the commonest misconfiguration invisible. An identifier,
		// not a credential.
		if device := c.HL7v3.SenderDevice; device != "" {
			return "an MCCI_IN000002UV01 in the response, as " + device
		}

		return "an MCCI_IN000002UV01 in the response"
	}

	switch c.Source.Ack.When {
	case config.AckOnReceipt:
		return "as soon as the message is accepted, before any destination has been written, so an acknowledged message can still be lost if this process dies with work queued"
	default:
		return "once every destination has been written, so an acknowledgement means the message arrived"
	}
}

// describeDestination says where one destination sends, without credentials.
func describeDestination(d config.Destination) string {
	var out string

	switch d.Type {
	case config.DestinationMLLP:
		out = "MLLP to " + d.Address
		if d.TLS != nil {
			out += " over TLS"
		}

	case config.DestinationFile:
		out = "files written to " + d.Dir

	case config.DestinationHTTP:
		if d.HTTP != nil {
			method := d.HTTP.Method
			if method == "" {
				method = "POST"
			}
			out = method + " to " + d.HTTP.URL
		} else {
			out = "an HTTP endpoint"
		}

	case config.DestinationFHIR:
		if d.FHIR != nil {
			out = "converted to FHIR and posted to " + d.FHIR.URL
		} else {
			out = "converted to FHIR"
		}

	case config.DestinationCDA:
		out = "converted to a CDA document"
		if d.CDA != nil && d.CDA.Dir != "" {
			out += " in " + d.CDA.Dir
		}

	case config.DestinationDatabase:
		if d.Database != nil {
			out = "rows inserted into a " + d.Database.Driver + " database"
		} else {
			out = "a database"
		}

	case config.DestinationS3:
		if d.S3 != nil {
			out = fmt.Sprintf("stored as an object in the %s bucket", d.S3.Bucket)
		} else {
			out = "stored as an object in S3"
		}

	case config.DestinationBroker:
		if d.Broker != nil {
			out = "broker " + d.Broker.Destination + " at " + d.Broker.Addr
		} else {
			out = "a message broker"
		}

	case config.DestinationKafka:
		if d.Kafka != nil {
			out = "kafka " + d.Kafka.Topic + " at " + strings.Join(d.Kafka.Brokers, ", ")
			if d.Kafka.Key != "" {
				out += " keyed on " + d.Kafka.Key
			}
		} else {
			out = "a Kafka topic"
		}

	case config.DestinationJavaScript:
		out = "handled by a script"

	case config.DestinationDICOM:
		if d.DICOM != nil {
			out = "DICOM C-STORE to " + d.DICOM.Address
		} else {
			out = "a DICOM archive"
		}

	case config.DestinationSOAP:
		if d.SOAP != nil {
			out = fmt.Sprintf("posted as a SOAP request to %s", d.SOAP.URL)
		} else {
			out = "a SOAP endpoint"
		}

	case config.DestinationDocument:
		if d.Document != nil {
			out = fmt.Sprintf("rendered as a document into %s", d.Document.Dir)
		} else {
			out = "a rendered document"
		}

	case config.DestinationFTP:
		if d.FTP != nil {
			out = fmt.Sprintf("uploaded to %s:%s over FTP", d.FTP.Host, d.FTP.Dir)
		} else {
			out = "an FTP server"
		}

	case config.DestinationSFTP:
		if d.SFTP != nil {
			out = fmt.Sprintf("uploaded to %s:%s", d.SFTP.Host, d.SFTP.Dir)
		} else {
			out = "an SFTP server"
		}

	case config.DestinationSMTP:
		if d.SMTP != nil {
			// Recipients are named, because who receives a notification is the part
			// somebody reviewing this needs to check. BCC recipients are counted rather
			// than listed: that list is often who is being told about a patient.
			out = "emailed to " + strings.Join(d.SMTP.To, ", ")
			if n := len(d.SMTP.CC) + len(d.SMTP.BCC); n > 0 {
				out += fmt.Sprintf(" and %d other recipient(s)", n)
			}
			if d.SMTP.Attach {
				out += ", with the message attached"
			} else {
				out += ", as a summary only"
			}
		} else {
			out = "email"
		}

	case config.DestinationChannel:
		if d.Channel != nil {
			out = "the " + d.Channel.Name + " channel in this server"
		} else {
			out = "another channel in this server"
		}

	default:
		out = string(d.Type)
	}

	out = d.Name + ": " + out

	if !d.IsEnabled() {
		out += " (disabled)"
	}
	if d.Filter != "" {
		out += ", only when " + d.Filter
	}
	if d.Queue.IsEnabled() {
		// The durability promise is worth stating: it changes what a failure means.
		out += ", queued to disk and retried until it succeeds"
	} else if d.Retry.Attempts > 0 {
		out += fmt.Sprintf(", retried %d times before giving up", d.Retry.Attempts)
	}

	return out
}
