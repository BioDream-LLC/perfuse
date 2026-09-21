// Kafka, as a client rather than as something to replace.
//
// A health system that has an event backbone has a Kafka one, and the systems built in the last
// decade publish to it rather than to a JMS broker. So this connector exists to make Perfuse the
// healthcare-aware edge of somebody else's event platform: it reads clinical messages off topics
// and publishes them onto topics, and it deliberately does not try to be a streaming platform.
// Windowing, joins, stream processing and schema registries are Kafka's ecosystem and stay there.
//
// # Why a dependency, when STOMP and MLLP were written by hand
//
// The stated rule in this repository is that a dependency has to buy capability rather than
// convenience, and must be pure Go so one static binary still cross-compiles to every target.
// franz-go is pure Go and carries no cgo, which was checked against all six targets.
//
// Kafka's protocol is not STOMP's. STOMP is a text protocol that fits in a few hundred lines.
// Kafka is a versioned binary protocol across roughly seventy request types, and the part that
// matters most here - consumer groups - is a distributed coordination protocol with join, sync,
// heartbeat and offset-commit phases and a rebalance dance between them. Getting that subtly
// wrong does not produce an obvious failure: it produces a partition nobody is reading, or two
// consumers reading the same one. In a clinical feed those are a missing lab result and a
// duplicated order. That is capability, and it is not worth hand-rolling to save a dependency.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// Record is one Kafka record, in the terms this codebase uses.
type Record struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   map[string]string
	Timestamp time.Time
}

// Consumer reads records from one or more topics as part of a consumer group.
type Consumer struct {
	client *kgo.Client

	// commitAfter holds whether offsets are committed only once a record has been handled.
	commitAfter bool
}

// NewConsumer joins a consumer group and begins fetching.
//
// Auto-commit is switched off unconditionally and the commit is issued by Commit below. The
// reason is the difference between losing a message and seeing it twice: an automatic commit on
// a timer can acknowledge a record that has not been delivered anywhere, and a crash in that
// window loses it with nothing recording that it existed. Committing after delivery redelivers
// on a crash instead, which is visible and which HL7 receivers tolerate.
func NewConsumer(ctx context.Context, cfg *config.KafkaSource, tlsConf *tls.Config) (*Consumer, error) {
	if cfg == nil {
		return nil, errors.New("kafka: no source configuration")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.SessionTimeout(cfg.ResolvedSessionTimeout()),

		// Disabled always, not only when commit-after-delivery is set. A background commit
		// would race the delivery path even in the other mode, and the commit points here
		// are explicit for exactly that reason.
		kgo.DisableAutoCommit(),
	}

	// Where to start when the group has no committed offset. End by default, so a channel
	// pointed at a topic with two years of history does not replay two years of patient
	// events into a live system on the day somebody switches it on.
	if cfg.FromBeginning {
		opts = append(opts, kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	} else {
		opts = append(opts, kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()))
	}

	if cfg.MaxMessageSize > 0 {
		opts = append(opts, kgo.FetchMaxBytes(int32(cfg.MaxMessageSize)))
	}
	if tlsConf != nil {
		opts = append(opts, kgo.DialTLSConfig(tlsConf))
	}
	if mech, err := saslMechanism(cfg.SASL); err != nil {
		return nil, err
	} else if mech != nil {
		opts = append(opts, kgo.SASL(mech))
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}

	// Reachability is proved here rather than left to the first fetch. A consumer that cannot
	// reach its cluster otherwise starts cleanly, logs nothing useful, and looks like a topic
	// with no traffic - which is the failure that gets diagnosed as "the feed is quiet".
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ResolvedTimeout())
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		client.Close()
		return nil, fmt.Errorf("kafka: cannot reach any of the brokers %s: %w",
			strings.Join(cfg.Brokers, ", "), err)
	}

	return &Consumer{client: client, commitAfter: cfg.ResolvedCommitAfterDelivery()}, nil
}

// Poll fetches the next batch of records.
//
// Returns no error when the context is cancelled and nothing was fetched, because that is a
// clean shutdown rather than a fault and logging it as one trains people to ignore the log.
func (c *Consumer) Poll(ctx context.Context) ([]Record, error) {
	fetches := c.client.PollFetches(ctx)
	if errs := fetches.Errors(); len(errs) > 0 {
		// Context cancellation arrives as a fetch error. Separated here so a shutdown is
		// silent and a real broker problem is not.
		for _, e := range errs {
			if errors.Is(e.Err, context.Canceled) || errors.Is(e.Err, context.DeadlineExceeded) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("kafka: fetching from %s: %w", errs[0].Topic, errs[0].Err)
	}

	var out []Record
	fetches.EachRecord(func(r *kgo.Record) {
		rec := Record{
			Topic:     r.Topic,
			Partition: r.Partition,
			Offset:    r.Offset,
			Key:       r.Key,
			Value:     r.Value,
			Timestamp: r.Timestamp,
		}
		if len(r.Headers) > 0 {
			rec.Headers = make(map[string]string, len(r.Headers))
			for _, h := range r.Headers {
				rec.Headers[h.Key] = string(h.Value)
			}
		}
		out = append(out, rec)
	})
	return out, nil
}

// CommitAfterDelivery reports whether the caller must call Commit once records are handled.
func (c *Consumer) CommitAfterDelivery() bool { return c.commitAfter }

// Commit records how far this group has read.
//
// Called by the source after a batch has been handled. Uncommitted offsets are redelivered
// after a restart, which is the deliberate trade: a duplicate is visible and a loss is not.
func (c *Consumer) Commit(ctx context.Context) error {
	if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
		return fmt.Errorf("kafka: committing offsets: %w", err)
	}
	return nil
}

// Close leaves the group and releases the connections.
//
// Leaving the group explicitly matters: without it the coordinator waits out the session
// timeout before reassigning the partitions, so a restart stalls the feed for that long rather
// than resuming immediately.
func (c *Consumer) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// Producer publishes records to a topic.
type Producer struct {
	client *kgo.Client
	topic  string
}

// NewProducer connects for publishing.
func NewProducer(ctx context.Context, cfg *config.KafkaDestination, tlsConf *tls.Config) (*Producer, error) {
	if cfg == nil {
		return nil, errors.New("kafka: no destination configuration")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.DefaultProduceTopic(cfg.Topic),
		kgo.ProduceRequestTimeout(cfg.ResolvedTimeout()),
	}

	switch cfg.ResolvedAcks() {
	case "all":
		opts = append(opts, kgo.RequiredAcks(kgo.AllISRAcks()))
	case "leader":
		opts = append(opts, kgo.RequiredAcks(kgo.LeaderAck()),
			// Idempotency requires all-ISR acks, so it has to be disabled here or the
			// client refuses to start. Named rather than implied: weaker acks cost the
			// duplicate-suppression guarantee as well as the durability one.
			kgo.DisableIdempotentWrite())
	case "none":
		opts = append(opts, kgo.RequiredAcks(kgo.NoAck()), kgo.DisableIdempotentWrite())
	}

	if codec, ok := compressionFor(cfg.ResolvedCompression()); ok {
		opts = append(opts, kgo.ProducerBatchCompression(codec))
	}
	if tlsConf != nil {
		opts = append(opts, kgo.DialTLSConfig(tlsConf))
	}
	if mech, err := saslMechanism(cfg.SASL); err != nil {
		return nil, err
	} else if mech != nil {
		opts = append(opts, kgo.SASL(mech))
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}

	// Proved reachable at load, like every other destination here, so a cluster that cannot be
	// reached is refused when the channel starts rather than discovered by the first patient
	// message failing to deliver.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ResolvedTimeout())
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		client.Close()
		return nil, fmt.Errorf("kafka: cannot reach any of the brokers %s: %w",
			strings.Join(cfg.Brokers, ", "), err)
	}

	return &Producer{client: client, topic: cfg.Topic}, nil
}

// Publish sends one record and waits for the acknowledgement.
//
// Synchronous deliberately. The engine's queue is what makes delivery durable, and it can only
// do that if it is told whether the write succeeded. An asynchronous produce would report
// success to the channel while the record was still in a buffer, and a crash would lose it with
// the message marked delivered.
func (p *Producer) Publish(ctx context.Context, key []byte, value []byte, headers map[string]string) error {
	rec := &kgo.Record{Topic: p.topic, Key: key, Value: value}
	for k, v := range headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}

	res := p.client.ProduceSync(ctx, rec)
	if err := res.FirstErr(); err != nil {
		return fmt.Errorf("kafka: publishing to %s: %w", p.topic, err)
	}
	return nil
}

// Close flushes and releases the connections.
func (p *Producer) Close() {
	if p.client != nil {
		p.client.Close()
	}
}

// compressionFor maps a codec name to the client's value.
func compressionFor(name string) (kgo.CompressionCodec, bool) {
	switch name {
	case "none":
		return kgo.NoCompression(), true
	case "gzip":
		return kgo.GzipCompression(), true
	case "snappy":
		return kgo.SnappyCompression(), true
	case "lz4":
		return kgo.Lz4Compression(), true
	case "zstd":
		return kgo.ZstdCompression(), true
	}
	return kgo.CompressionCodec{}, false
}

// saslMechanism builds the authentication mechanism, or nil when none is configured.
func saslMechanism(cfg *config.KafkaSASL) (sasl.Mechanism, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mechanism)) {
	case config.SASLPlain:
		return plain.Auth{User: cfg.Username, Pass: cfg.Password}.AsMechanism(), nil
	case config.SASLScramSHA256:
		return scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha256Mechanism(), nil
	case config.SASLScramSHA512:
		return scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha512Mechanism(), nil
	}
	return nil, fmt.Errorf("kafka: unknown sasl mechanism %q", cfg.Mechanism)
}
