package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/kafka"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// Reading a clinical feed off a Kafka topic.
//
// Shaped like the broker source next to it - a goroutine that reconnects for ever and hands each
// message to c.handle - but the acknowledgement model is different and that difference is the
// interesting part.
//
// STOMP acknowledges one message at a time and the broker tracks what is outstanding. Kafka has
// no per-message acknowledgement: a consumer group records a position per partition, and
// committing that position means "everything up to here is done". So a batch is handled in order
// and the commit happens after the whole batch, which means a failure part-way through
// redelivers the messages that already succeeded.
//
// That redelivery is chosen rather than tolerated. The alternative is committing before
// delivering, and a crash in that window loses the message with nothing anywhere recording that
// it existed. A duplicate A08 is a nuisance a receiver is built to absorb; a lab result that
// silently never arrived is a patient safety event.

// kafkaPoller reads from Kafka until stopped.
type kafkaPoller struct {
	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

func (p *kafkaPoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// startKafkaSource begins consuming.
func (c *Channel) startKafkaSource() error {
	cfg := c.cfg.Source.Kafka
	if cfg == nil {
		return fmt.Errorf("channel %q has a kafka source with no configuration", c.cfg.Name)
	}

	if cfg.TLS == nil {
		c.log.Warn("kafka source",
			"detail", "this connection to the cluster is not encrypted, and the messages on it are patient data")
	}

	// Said at startup, because the two settings that decide whether messages can be lost are
	// both invisible once running, and the person who needs to know is the one reading the log
	// after a restart produced duplicates.
	c.log.Info("kafka source starting",
		"brokers", cfg.Brokers,
		"topics", cfg.Topics,
		"group", cfg.Group,
		"from_beginning", cfg.FromBeginning,
		"commit_after_delivery", cfg.ResolvedCommitAfterDelivery(),
	)

	poller := &kafkaPoller{stop: make(chan struct{})}
	c.kafka = poller

	poller.wg.Add(1)
	go func() {
		defer poller.wg.Done()
		c.readKafkaForever(poller, cfg)
	}()

	return nil
}

// stopKafkaSource ends consuming and waits for the current batch.
func (c *Channel) stopKafkaSource() error {
	if c.kafka == nil {
		return nil
	}
	c.kafka.close()
	c.kafka = nil
	return nil
}

// readKafkaForever reconnects until stopped.
func (c *Channel) readKafkaForever(p *kafkaPoller, cfg *config.KafkaSource) {
	for {
		select {
		case <-p.stop:
			return
		default:
		}

		if err := c.readKafkaSession(p, cfg); err != nil {
			select {
			case <-p.stop:
				return
			default:
			}
			// Five seconds, matching the broker source. A cluster that is rolling its
			// brokers is unreachable for seconds at a time and reconnecting harder does
			// not help.
			c.log.Error("the kafka connection failed and will be retried",
				"brokers", cfg.Brokers, "err", err, "in", 5*time.Second)

			select {
			case <-p.stop:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// readKafkaSession consumes until the connection fails or the poller stops.
func (c *Channel) readKafkaSession(p *kafkaPoller, cfg *config.KafkaSource) error {
	var tlsCfg *tls.Config
	if cfg.TLS != nil {
		t, err := tlsconf.ForSender(cfg.TLS)
		if err != nil {
			return fmt.Errorf("the kafka tls settings could not be used: %w", err)
		}
		tlsCfg = t
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancelled from the stop channel, so a shutdown interrupts a poll that is waiting on a
	// quiet topic instead of waiting for it to time out.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-p.stop:
			cancel()
		case <-done:
		}
	}()

	consumer, err := kafka.NewConsumer(ctx, cfg, tlsCfg)
	if err != nil {
		return err
	}
	defer consumer.Close()

	c.log.Info("consuming from kafka",
		"brokers", cfg.Brokers, "topics", cfg.Topics, "group", cfg.Group)

	for {
		select {
		case <-p.stop:
			return nil
		default:
		}

		records, err := consumer.Poll(ctx)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			continue
		}

		if !c.handleKafkaBatch(ctx, consumer, records) {
			// A batch that could not be handled is not committed, so it is redelivered.
			// Returning ends the session and the reconnect loop waits before trying again,
			// which stops a message that always fails from being retried in a tight loop
			// against a destination that is down.
			return nil
		}
	}
}

// handleKafkaBatch routes a batch in order and commits if the whole batch succeeded.
//
// Reports whether to carry on. Order within the batch is preserved deliberately: the records of
// one partition are a patient's events in sequence, and handling them concurrently would deliver
// a discharge before the admission that preceded it.
func (c *Channel) handleKafkaBatch(ctx context.Context, consumer *kafka.Consumer, records []kafka.Record) bool {
	for _, rec := range records {
		if _, err := c.handle(ctx, rec.Value); err != nil {
			// Logged with the coordinates, because "a message failed" is not actionable and
			// a topic, partition and offset can be fetched and looked at.
			c.log.Error("a message from kafka could not be handled; it will be offered again",
				"topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
			return false
		}
	}

	if consumer.CommitAfterDelivery() {
		if err := consumer.Commit(ctx); err != nil {
			// Handled but not committed, so these records arrive again. Said plainly,
			// because the consequence is duplicates and the person seeing two of
			// everything needs to find this line.
			c.log.Error("messages were handled but the offset could not be committed, "+
				"so kafka will deliver them again", "err", err)
			return false
		}
	}
	return true
}

// KafkaSender publishes messages to a Kafka topic.
//
// The connection is opened on demand and kept. A producer that reconnected per message would
// spend more time in metadata requests and handshakes than in producing, and a cluster sees a
// connection per message as an attack.
type KafkaSender struct {
	cfg *config.KafkaDestination

	mu       sync.Mutex
	producer *kafka.Producer
}

// NewKafkaSender builds a sender from a destination.
//
// Refused here rather than defaulted when the section is missing, matching the other senders: a
// destination that cannot work is refused at load rather than at the first patient message.
func NewKafkaSender(d config.Destination) (*KafkaSender, error) {
	if d.Kafka == nil {
		return nil, fmt.Errorf("destination %q is a kafka destination with no kafka section", d.Name)
	}
	if err := d.Kafka.Validate(); err != nil {
		return nil, err
	}
	return &KafkaSender{cfg: d.Kafka}, nil
}

// Send publishes one message.
func (s *KafkaSender) Send(ctx context.Context, msg []byte) error {
	producer, err := s.connect(ctx)
	if err != nil {
		return err
	}

	key, err := s.keyFor(msg)
	if err != nil {
		return err
	}

	headers, err := s.headersFor(msg)
	if err != nil {
		return err
	}

	if err := producer.Publish(ctx, key, msg, headers); err != nil {
		// Dropped so the next send reconnects. A producer whose cluster has moved keeps
		// failing otherwise, and the failure looks like the topic rather than the
		// connection.
		s.reset()
		return err
	}
	return nil
}

// Describe names this destination in logs and errors.
func (s *KafkaSender) Describe() string {
	return fmt.Sprintf("kafka %s topic %s", strings.Join(s.cfg.Brokers, ","), s.cfg.Topic)
}

// connect returns the producer, opening it if needed.
func (s *KafkaSender) connect(ctx context.Context) (*kafka.Producer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.producer != nil {
		return s.producer, nil
	}

	var tlsCfg *tls.Config
	if s.cfg.TLS != nil {
		t, err := tlsconf.ForSender(s.cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("the kafka tls settings could not be used: %w", err)
		}
		tlsCfg = t
	}

	producer, err := kafka.NewProducer(ctx, s.cfg, tlsCfg)
	if err != nil {
		return nil, err
	}
	s.producer = producer
	return producer, nil
}

// reset closes the producer so the next send reconnects.
func (s *KafkaSender) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.producer != nil {
		s.producer.Close()
		s.producer = nil
	}
}

// Close releases the connection.
func (s *KafkaSender) Close() error {
	s.reset()
	return nil
}

// keyFor builds the record key, which is what decides the partition.
//
// This is the field that makes Kafka safe for a clinical feed. Kafka guarantees order within a
// partition and nowhere else, and records with the same key land in the same partition. So
// keying on the patient identifier keeps one patient's events in sequence while letting
// different patients proceed in parallel. With no key there is no guarantee at all: measured
// against a real broker, unkeyed records stayed in one partition and then moved between batches,
// so the ordering holds by accident and fails at a boundary nobody can see.
//
// Accepts either a bare path - PID-3.1 - or the brace form used by the other senders, so
// somebody who has written {PID-3.1} elsewhere is not caught out here.
func (s *KafkaSender) keyFor(raw []byte) ([]byte, error) {
	tmpl := strings.TrimSpace(s.cfg.Key)
	if tmpl == "" {
		return nil, nil
	}

	msg, err := hl7.Parse(raw)
	if err != nil {
		// Not an error. A key is an optimisation for ordering, and refusing to deliver a
		// message because its key could not be computed would turn a partitioning hint into
		// a delivery failure. An unkeyed record is what an absent key would have produced
		// anyway, so the message still goes.
		return nil, nil
	}

	if !strings.Contains(tmpl, "{") {
		v, err := msg.Get(tmpl)
		if err != nil {
			return nil, nil
		}
		return []byte(strings.TrimSpace(v)), nil
	}
	return []byte(expandKafkaTemplate(tmpl, msg)), nil
}

// headersFor builds the record headers.
func (s *KafkaSender) headersFor(raw []byte) (map[string]string, error) {
	if len(s.cfg.Headers) == 0 {
		return nil, nil
	}

	msg, parseErr := hl7.Parse(raw)

	out := make(map[string]string, len(s.cfg.Headers))
	for k, tmpl := range s.cfg.Headers {
		if parseErr != nil || !strings.Contains(tmpl, "{") {
			// A literal value survives an unparseable message, which is the point of
			// allowing literals: a header naming the source system should still be set on
			// a message this channel could not parse.
			out[k] = tmpl
			continue
		}
		out[k] = expandKafkaTemplate(tmpl, msg)
	}
	return out, nil
}

// expandKafkaTemplate substitutes {PATH} references, matching the other senders.
//
// An unclosed brace is written through rather than guessed at, because guessing where it ended
// would silently change the value.
func expandKafkaTemplate(tmpl string, msg *hl7.Message) string {
	var out strings.Builder
	rest := tmpl

	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			out.WriteString(rest)
			break
		}
		shut := strings.Index(rest[open:], "}")
		if shut < 0 {
			out.WriteString(rest)
			break
		}
		shut += open

		out.WriteString(rest[:open])
		path := strings.TrimSpace(rest[open+1 : shut])
		if v, err := msg.Get(path); err == nil {
			out.WriteString(strings.TrimSpace(v))
		}
		rest = rest[shut+1:]
	}
	return out.String()
}
