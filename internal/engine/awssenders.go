package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/awsmsg"
	"github.com/biodream-llc/perfuse/internal/awsv4"
	"github.com/biodream-llc/perfuse/internal/config"
)

// SQS and SNS destinations.
//
// Both hand the message over as it is: the body of an SQS message, the Message of an SNS publish. On a FIFO queue or topic the group is
// the patient by default, because SQS orders within a group and nowhere else - an A03 overtaking its A01 is the failure that matters - and
// the deduplication id is the message's own hash, so an engine retry inside SQS's five-minute window is not delivered twice.

// SQSSender sends each message to a queue.
type SQSSender struct {
	cfg   *config.SQSDestination
	name  string
	queue *awsmsg.SQS
}

// NewSQSSender builds the sender.
func NewSQSSender(d config.Destination) (*SQSSender, error) {
	if d.SQS == nil {
		return nil, fmt.Errorf("destination %q is an sqs destination with no sqs block", d.Name)
	}
	q, err := awsmsg.NewSQS(awsmsg.Config{Region: d.SQS.Region, Credentials: d.SQS.Credentials(), Endpoint: d.SQS.Endpoint},
		d.SQS.QueueURL)
	if err != nil {
		return nil, err
	}
	return &SQSSender{cfg: d.SQS, name: d.Name, queue: q}, nil
}

// Send puts one message on the queue.
func (s *SQSSender) Send(ctx context.Context, msg []byte) error {
	if len(msg) > 256*1024 {
		// Said plainly, because SQS's own refusal is a generic validation error.
		return fmt.Errorf("the message is %d bytes and SQS accepts at most 262144; archive it to S3 and send the key instead", len(msg))
	}
	_, err := s.queue.Send(ctx, string(msg), groupFor(msg, s.cfg.GroupBy, s.name), awsv4.SHA256Hex(msg))
	return err
}

// Describe names the queue, never the credentials.
func (s *SQSSender) Describe() string {
	d := "sqs " + s.cfg.QueueURL
	if s.queue.FIFO() {
		d += " (FIFO, grouped by " + s.cfg.GroupBy + ")"
	}
	return d
}

// Close releases nothing.
func (s *SQSSender) Close() error { return nil }

// SNSSender publishes each message to a topic.
type SNSSender struct {
	cfg   *config.SNSDestination
	name  string
	topic *awsmsg.SNS
}

// NewSNSSender builds the sender.
func NewSNSSender(d config.Destination) (*SNSSender, error) {
	if d.SNS == nil {
		return nil, fmt.Errorf("destination %q is an sns destination with no sns block", d.Name)
	}
	t, err := awsmsg.NewSNS(awsmsg.Config{Region: d.SNS.Region, Credentials: d.SNS.Credentials(), Endpoint: d.SNS.Endpoint},
		d.SNS.TopicARN)
	if err != nil {
		return nil, err
	}
	return &SNSSender{cfg: d.SNS, name: d.Name, topic: t}, nil
}

// Send publishes one message.
func (s *SNSSender) Send(ctx context.Context, msg []byte) error {
	if len(msg) > 256*1024 {
		return fmt.Errorf("the message is %d bytes and SNS accepts at most 262144; archive it to S3 and publish the key instead", len(msg))
	}
	subject := s.cfg.Subject
	if subject != "" {
		if m, err := hl7.Parse(msg); err == nil {
			subject = strings.ReplaceAll(subject, "${message_type}", strings.ReplaceAll(m.MustGet("MSH-9"), "^", "_"))
			subject = strings.ReplaceAll(subject, "${control_id}", m.ControlID())
		}
	}
	_, err := s.topic.Publish(ctx, string(msg), subject, groupFor(msg, s.cfg.GroupBy, s.name), awsv4.SHA256Hex(msg))
	return err
}

// Describe names the topic.
func (s *SNSSender) Describe() string { return "sns " + s.cfg.TopicARN }

// Close releases nothing.
func (s *SNSSender) Close() error { return nil }

// groupFor is the FIFO group: the patient, the channel, or a message path.
//
// A message with no patient identifier is grouped under "no-patient" rather than given a unique group, so such messages still keep their
// order among themselves instead of silently losing it.
func groupFor(msg []byte, by, channel string) string {
	switch by {
	case "", "patient":
		if m, err := hl7.Parse(msg); err == nil {
			if id := strings.TrimSpace(m.MustGet("PID-3.1")); id != "" {
				return fifoSafe(id)
			}
		}
		return "no-patient"
	case "channel":
		return fifoSafe(channel)
	default:
		if m, err := hl7.Parse(msg); err == nil {
			if v := strings.TrimSpace(m.MustGet(by)); v != "" {
				return fifoSafe(v)
			}
		}
		return "none"
	}
}

// fifoSafe keeps a group id within what SQS accepts: up to 128 printable ASCII characters.
func fifoSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 33 && r <= 126 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 128 {
			break
		}
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}
