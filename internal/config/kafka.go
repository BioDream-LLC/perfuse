package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// Kafka, as a source and as a destination.
//
// Separate from BrokerSource rather than another mode of it. The STOMP broker connector talks to
// ActiveMQ and RabbitMQ, where a subscription is a string and the broker tracks what has been
// delivered. Kafka's model is different in the ways that matter to a clinical feed: a topic is
// partitioned, ordering is guaranteed only within a partition, and the consumer tracks its own
// position. Folding both into one connector would mean fields that apply to one and are ignored
// by the other, which is the shape of a configuration that validates and then does nothing.
//
// # Why Kafka at all, given a broker connector already exists
//
// STOMP is the Mirth-era answer and it reaches the brokers a hospital already ran. Kafka is what
// the systems built in the last decade publish to, and a health system that has an event
// backbone has a Kafka one. This connector is deliberately a client: Perfuse is the
// healthcare-aware edge of somebody else's event platform, not a replacement for it.

// KafkaSource reads messages from a Kafka topic.
type KafkaSource struct {
	// Brokers is the bootstrap server list. Required.
	//
	// A list rather than one address, because a single bootstrap broker is a single point of
	// failure for starting up: the cluster survives losing it and this channel would not.
	Brokers []string `yaml:"brokers"`

	// Topics are the topics to read. At least one is required.
	Topics []string `yaml:"topics"`

	// Group is the consumer group. Required.
	//
	// Named rather than generated, and required rather than defaulted, because it is what
	// remembers how far this channel has read. A generated group would start from the
	// beginning - or from the end, losing everything - on every restart, and it would leave
	// the old group behind holding committed offsets nobody reads. Requiring it makes the
	// person name the thing that has to stay the same across restarts.
	Group string `yaml:"group"`

	// FromBeginning reads the topic from its start when the group has no committed offset.
	//
	// Off by default, so a new channel pointed at a topic with two years of history does not
	// replay two years of patient events into a live system on the day it is switched on. The
	// first person to hit that would be doing it in production, because that is where the
	// topic with the history is.
	FromBeginning bool `yaml:"from_beginning,omitempty"`

	// CommitAfterDelivery commits the offset only once the message has been handled.
	//
	// On by default, and the reason is the whole difference between losing a message and
	// seeing it twice. Committing on read means a crash between the commit and the delivery
	// loses the message silently, and nothing anywhere records that it existed. Committing
	// after means a crash in the same window redelivers it, which is visible and which HL7
	// receivers are built to tolerate - a duplicate A08 is a nuisance and a missing lab result
	// is a patient safety event.
	CommitAfterDelivery *bool `yaml:"commit_after_delivery,omitempty"`

	// MaxMessageSize bounds one record. Defaults to the channel's limit.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// SessionTimeout is how long the group coordinator waits before assuming this consumer is
	// gone and reassigning its partitions. Defaults to forty-five seconds.
	SessionTimeout time.Duration `yaml:"session_timeout,omitempty"`

	// Timeout bounds connecting and metadata requests. Defaults to thirty seconds.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// SASL authenticates to the cluster.
	SASL *KafkaSASL `yaml:"sasl,omitempty"`

	// TLS wraps the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// KafkaDestination publishes messages to a Kafka topic.
type KafkaDestination struct {
	// Brokers is the bootstrap server list. Required.
	Brokers []string `yaml:"brokers"`

	// Topic is where to publish. Required.
	Topic string `yaml:"topic"`

	// Key is a template for the record key, which decides the partition.
	//
	// This is the field that makes Kafka safe for a clinical feed, and it is worth being
	// precise about why. Kafka guarantees order within a partition and nowhere else. Records
	// with the same key always land in the same partition, so keying on the patient identifier
	// keeps that patient's events in order while letting different patients go in parallel.
	//
	// Without a key there is no such guarantee. Measured against a real broker, thirty unkeyed
	// records went to one partition rather than spreading: the client keeps a batch together
	// and picks a new partition between batches. So the ordering holds by accident and breaks
	// at a boundary nobody can see, which is worse than breaking consistently - an A03
	// discharge read before its A01 admission, intermittently, under load.
	//
	// An HL7 path, evaluated against the outgoing message: PID-3.1 keys by patient.
	Key string `yaml:"key,omitempty"`

	// Acks is how many brokers must confirm a write: "all", "leader" or "none".
	//
	// Defaults to all, which is the only setting that survives a broker failing between the
	// write and the replication. "none" is fire-and-forget and will lose messages; it exists
	// because somebody moving non-clinical telemetry may legitimately want it, and refusing
	// it outright would mean they wrote their own producer instead.
	Acks string `yaml:"acks,omitempty"`

	// Compression is "none", "gzip", "snappy", "lz4" or "zstd". Defaults to snappy.
	//
	// On by default because HL7 is highly compressible text and the wire is usually the
	// constraint. Snappy rather than zstd as the default: it is the cheapest in CPU, and a
	// connector on the delivery path should not be the thing that saturates a core.
	Compression string `yaml:"compression,omitempty"`

	// Headers are record headers to set. Values are templates evaluated per message.
	Headers map[string]string `yaml:"headers,omitempty"`

	// Timeout bounds one publish. Defaults to thirty seconds.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// SASL authenticates to the cluster.
	SASL *KafkaSASL `yaml:"sasl,omitempty"`

	// TLS wraps the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// KafkaSASL authenticates to a Kafka cluster.
type KafkaSASL struct {
	// Mechanism is "plain", "scram-sha-256" or "scram-sha-512".
	//
	// PLAIN sends the password readable on the wire, so it belongs with TLS. SCRAM does not,
	// which is why it is worth naming the difference here rather than treating the three as
	// interchangeable.
	Mechanism string `yaml:"mechanism"`

	// Username is the SASL user.
	Username string `yaml:"username"`

	// Password is the SASL password. Supports ${ENV} references like every other secret here,
	// so a cluster credential need not be written into the channel file.
	Password string `yaml:"password"`
}

// Supported SASL mechanisms, named so validation and the implementation cannot disagree.
const (
	SASLPlain        = "plain"
	SASLScramSHA256  = "scram-sha-256"
	SASLScramSHA512  = "scram-sha-512"
	kafkaDefaultAcks = "all"
)

// KafkaSASLMechanisms lists what is accepted, in the order the interface should offer them.
func KafkaSASLMechanisms() []string {
	return []string{SASLPlain, SASLScramSHA256, SASLScramSHA512}
}

// KafkaCompressions lists the accepted compression codecs.
func KafkaCompressions() []string {
	return []string{"none", "gzip", "snappy", "lz4", "zstd"}
}

// KafkaAcks lists the accepted acknowledgement settings.
func KafkaAcks() []string {
	return []string{"all", "leader", "none"}
}

// ResolvedCommitAfterDelivery reports whether to commit only after handling a message.
//
// A pointer in the struct and a resolver here, because the safe value is true and a plain bool
// would make the zero value false. A channel written without the field would then commit on
// read and lose messages on a crash, which is the failure this setting exists to prevent.
func (k *KafkaSource) ResolvedCommitAfterDelivery() bool {
	if k.CommitAfterDelivery == nil {
		return true
	}
	return *k.CommitAfterDelivery
}

// ResolvedSessionTimeout is how long before the coordinator reassigns this consumer's work.
func (k *KafkaSource) ResolvedSessionTimeout() time.Duration {
	if k.SessionTimeout <= 0 {
		return 45 * time.Second
	}
	return k.SessionTimeout
}

// ResolvedTimeout bounds connecting.
func (k *KafkaSource) ResolvedTimeout() time.Duration {
	if k.Timeout <= 0 {
		return 30 * time.Second
	}
	return k.Timeout
}

// ResolvedAcks is how many brokers must confirm a write.
func (k *KafkaDestination) ResolvedAcks() string {
	if k.Acks == "" {
		return kafkaDefaultAcks
	}
	return strings.ToLower(k.Acks)
}

// ResolvedCompression is the codec to use.
func (k *KafkaDestination) ResolvedCompression() string {
	if k.Compression == "" {
		return "snappy"
	}
	return strings.ToLower(k.Compression)
}

// ResolvedTimeout bounds one publish.
func (k *KafkaDestination) ResolvedTimeout() time.Duration {
	if k.Timeout <= 0 {
		return 30 * time.Second
	}
	return k.Timeout
}

// Validate checks a Kafka source at load.
func (k *KafkaSource) Validate() error {
	if len(k.Brokers) == 0 {
		return fmt.Errorf("kafka source: at least one broker is required, for example localhost:9092")
	}
	for _, b := range k.Brokers {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("kafka source: a broker address is empty")
		}
	}
	if len(k.Topics) == 0 {
		return fmt.Errorf("kafka source: at least one topic is required")
	}
	for _, t := range k.Topics {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("kafka source: a topic name is empty")
		}
	}
	// Refused rather than defaulted. The group is what remembers how far this channel has
	// read; inventing one would work for exactly as long as the process lives.
	if strings.TrimSpace(k.Group) == "" {
		return fmt.Errorf("kafka source: a consumer group is required, because it is what " +
			"remembers how far this channel has read; a generated one would restart from " +
			"scratch on every restart")
	}
	if k.SASL != nil {
		if err := k.SASL.Validate(); err != nil {
			return fmt.Errorf("kafka source: %w", err)
		}
	}
	return nil
}

// Validate checks a Kafka destination at load.
func (k *KafkaDestination) Validate() error {
	if len(k.Brokers) == 0 {
		return fmt.Errorf("kafka destination: at least one broker is required, for example localhost:9092")
	}
	for _, b := range k.Brokers {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("kafka destination: a broker address is empty")
		}
	}
	if strings.TrimSpace(k.Topic) == "" {
		return fmt.Errorf("kafka destination: a topic is required")
	}

	acks := k.ResolvedAcks()
	if !contains(KafkaAcks(), acks) {
		return fmt.Errorf("kafka destination: acks is %q; it must be one of %s",
			k.Acks, strings.Join(KafkaAcks(), ", "))
	}

	codec := k.ResolvedCompression()
	if !contains(KafkaCompressions(), codec) {
		return fmt.Errorf("kafka destination: compression is %q; it must be one of %s",
			k.Compression, strings.Join(KafkaCompressions(), ", "))
	}

	if k.SASL != nil {
		if err := k.SASL.Validate(); err != nil {
			return fmt.Errorf("kafka destination: %w", err)
		}
	}
	return nil
}

// Validate checks SASL settings.
func (s *KafkaSASL) Validate() error {
	m := strings.ToLower(strings.TrimSpace(s.Mechanism))
	if !contains(KafkaSASLMechanisms(), m) {
		return fmt.Errorf("sasl mechanism is %q; it must be one of %s",
			s.Mechanism, strings.Join(KafkaSASLMechanisms(), ", "))
	}
	if s.Username == "" || s.Password == "" {
		return fmt.Errorf("sasl needs both a username and a password")
	}
	return nil
}

// contains reports whether a string is in a list.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
