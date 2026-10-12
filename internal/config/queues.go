package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// MQTT, IBM MQ, Google Cloud Pub/Sub and Google Cloud Storage connectors.

// MQTTConnection is how to reach an MQTT 3.1.1 broker: Mosquitto, EMQX, HiveMQ, AWS IoT Core, Azure Event Grid.
type MQTTConnection struct {
	// Addr is host:port; 1883 plain, 8883 TLS.
	Addr string `yaml:"addr"`
	// ClientID names the session. A source keeps its messages across reconnects only under a fixed client id; it
	// defaults to perfuse-<channel>.
	ClientID string `yaml:"client_id,omitempty"`
	// Username and Password authenticate; either may be empty. The password may be ${ENV}.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// TLS encrypts the connection and may present a client certificate (AWS IoT Core requires one).
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
	// QoS is 0 (at most once) or 1 (at least once, the default).
	QoS *int `yaml:"qos,omitempty"`
	// Timeout bounds connecting and each wait for the broker. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// ResolvedPassword expands an environment reference.
func (m *MQTTConnection) ResolvedPassword() string { return resolveSecret(m.Password) }

// ResolvedQoS is the QoS, 1 when unset.
func (m *MQTTConnection) ResolvedQoS() byte {
	if m.QoS == nil {
		return 1
	}
	return byte(*m.QoS)
}

func (m *MQTTConnection) validate(what string) []error {
	var errs []error
	if _, _, err := net.SplitHostPort(m.Addr); err != nil {
		errs = append(errs, fmt.Errorf("%s needs mqtt.addr as host:port, e.g. broker.example.org:8883", what))
	}
	if m.QoS != nil && *m.QoS != 0 && *m.QoS != 1 {
		errs = append(errs, fmt.Errorf("%s: mqtt.qos is 0 or 1", what))
	}
	if m.Timeout == 0 {
		m.Timeout = 30 * time.Second
	}
	return errs
}

// MQTTSource subscribes to a topic filter.
type MQTTSource struct {
	MQTTConnection `yaml:",inline"`
	// Topic is the filter subscribed to; + and # are wildcards (hl7/+/adt, lab/#).
	Topic string `yaml:"topic"`
	// Reconnect is the wait after a lost connection, or after a message that could not be handled. Defaults to 5s.
	Reconnect time.Duration `yaml:"reconnect,omitempty"`
}

// Validate checks the settings.
func (s *MQTTSource) Validate() []error {
	errs := s.validate("the mqtt source")
	if strings.TrimSpace(s.Topic) == "" {
		errs = append(errs, errors.New("the mqtt source needs mqtt.topic, the topic filter to subscribe to"))
	}
	if s.Reconnect == 0 {
		s.Reconnect = 5 * time.Second
	}
	return errs
}

// MQTTDestination publishes to a topic.
type MQTTDestination struct {
	MQTTConnection `yaml:",inline"`
	// Topic is published to. It may not hold wildcards.
	Topic string `yaml:"topic"`
	// Retain asks the broker to keep the last message for subscribers that arrive later.
	Retain bool `yaml:"retain,omitempty"`
}

// Validate checks the settings.
func (d *MQTTDestination) Validate() []error {
	errs := d.validate("the mqtt destination")
	if strings.TrimSpace(d.Topic) == "" {
		errs = append(errs, errors.New("the mqtt destination needs mqtt.topic"))
	} else if strings.ContainsAny(d.Topic, "+#") {
		errs = append(errs, errors.New("mqtt.topic of a destination cannot hold + or #; they are wildcards for subscribing"))
	}
	return errs
}

// IBMMQConnection is a queue reached through the queue manager's messaging REST API.
type IBMMQConnection struct {
	// URL is the mqweb server, e.g. https://mq.example.org:9443.
	URL string `yaml:"url"`
	// QueueManager and Queue name the queue.
	QueueManager string `yaml:"queue_manager"`
	Queue        string `yaml:"queue"`
	// Username and Password are an mqweb user with the MQWebUser role; the password may be ${ENV}. Leave both empty when a
	// client certificate (tls.cert_file) authenticates.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// TLS names a CA for the server's certificate and, optionally, a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
	// APIVersion is v3 (MQ 9.3 and later, the default), v2 (9.1.5 to 9.2) or v1.
	APIVersion string `yaml:"api_version,omitempty"`
	// Timeout bounds each request. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// ResolvedPassword expands an environment reference.
func (m *IBMMQConnection) ResolvedPassword() string { return resolveSecret(m.Password) }

func (m *IBMMQConnection) validate(what string) []error {
	var errs []error
	if u, err := url.Parse(m.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("%s needs ibm_mq.url, the mqweb server, e.g. https://mq.example.org:9443", what))
	}
	if strings.TrimSpace(m.QueueManager) == "" || strings.TrimSpace(m.Queue) == "" {
		errs = append(errs, fmt.Errorf("%s needs ibm_mq.queue_manager and ibm_mq.queue", what))
	}
	switch m.APIVersion {
	case "", "v1", "v2", "v3":
	default:
		errs = append(errs, fmt.Errorf("%s: ibm_mq.api_version %q is not v1, v2 or v3", what, m.APIVersion))
	}
	if m.Timeout == 0 {
		m.Timeout = 30 * time.Second
	}
	return errs
}

// IBMMQSource reads a queue.
type IBMMQSource struct {
	IBMMQConnection `yaml:",inline"`
	// PollInterval is the wait when the queue is empty, or after a message that could not be handled. Defaults to 2s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
}

// Validate checks the settings.
func (s *IBMMQSource) Validate() []error {
	errs := s.validate("the ibm_mq source")
	if s.PollInterval == 0 {
		s.PollInterval = 2 * time.Second
	}
	if s.PollInterval < 100*time.Millisecond {
		errs = append(errs, errors.New("ibm_mq.poll_interval must be at least 100ms"))
	}
	return errs
}

// IBMMQDestination puts each message on a queue.
type IBMMQDestination struct {
	IBMMQConnection `yaml:",inline"`
	// NonPersistent sends messages that do not survive a queue manager restart. Off by default.
	NonPersistent bool `yaml:"non_persistent,omitempty"`
}

// Validate checks the settings.
func (d *IBMMQDestination) Validate() []error { return d.validate("the ibm_mq destination") }

// GCPAccess is how to authenticate to Google Cloud.
type GCPAccess struct {
	// Project is the Google Cloud project id.
	Project string `yaml:"project,omitempty"`
	// CredentialsFile is a service account's JSON key. Empty uses the service account attached to the VM, GKE pod or
	// Cloud Run service Perfuse runs on (the metadata server), unless Endpoint names an emulator.
	CredentialsFile string `yaml:"credentials_file,omitempty"`
	// Endpoint overrides Google's: a regional endpoint, a Private Service Connect address, or an emulator's
	// http://host:port (which takes no credentials).
	Endpoint string `yaml:"endpoint,omitempty"`
}

// Emulator reports whether the endpoint is plain http, as an emulator's is; those take no credentials.
func (g *GCPAccess) Emulator() bool { return strings.HasPrefix(g.Endpoint, "http://") }

func (g *GCPAccess) validate(what, block string) []error {
	var errs []error
	if g.Endpoint != "" {
		if u, err := url.Parse(g.Endpoint); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: %s.endpoint is not a URL", what, block))
		}
	}
	return errs
}

// PubSubSource pulls from a Pub/Sub subscription.
type PubSubSource struct {
	GCPAccess `yaml:",inline"`
	// Subscription is the pull subscription's id.
	Subscription string `yaml:"subscription"`
	// MaxMessages is how many are pulled at once. They are still handled one at a time, in order. Defaults to 10.
	MaxMessages int `yaml:"max_messages,omitempty"`
	// Reconnect is the wait after an error. Defaults to 5s.
	Reconnect time.Duration `yaml:"reconnect,omitempty"`
}

// Validate checks the settings.
func (s *PubSubSource) Validate() []error {
	errs := s.validate("the pubsub source", "pubsub")
	if s.Project == "" || s.Subscription == "" {
		errs = append(errs, errors.New("the pubsub source needs pubsub.project and pubsub.subscription"))
	}
	if s.MaxMessages == 0 {
		s.MaxMessages = 10
	}
	if s.MaxMessages < 1 || s.MaxMessages > 1000 {
		errs = append(errs, errors.New("pubsub.max_messages must be 1 to 1000"))
	}
	if s.Reconnect == 0 {
		s.Reconnect = 5 * time.Second
	}
	return errs
}

// PubSubDestination publishes to a Pub/Sub topic.
type PubSubDestination struct {
	GCPAccess `yaml:",inline"`
	// Topic is the topic's id.
	Topic string `yaml:"topic"`
	// OrderBy, when set, is an ordering key from the message (patient for PID-3.1, channel, or a message path such as MSH-4), so one
	// patient's messages arrive in order at a subscription with ordering enabled.
	OrderBy string `yaml:"order_by,omitempty"`
}

// Validate checks the settings.
func (d *PubSubDestination) Validate() []error {
	errs := d.validate("the pubsub destination", "pubsub")
	if d.Project == "" || d.Topic == "" {
		errs = append(errs, errors.New("the pubsub destination needs pubsub.project and pubsub.topic"))
	}
	return errs
}

// GCSAccess is a Cloud Storage bucket.
type GCSAccess struct {
	GCPAccess `yaml:",inline"`
	// Bucket is the bucket's name.
	Bucket string `yaml:"bucket"`
}

func (g *GCSAccess) validateBucket(what string) []error {
	errs := g.validate(what, "gcs")
	if strings.TrimSpace(g.Bucket) == "" {
		errs = append(errs, fmt.Errorf("%s needs gcs.bucket", what))
	}
	return errs
}

// GCSDestination writes each message as an object.
type GCSDestination struct {
	GCSAccess `yaml:",inline"`
	// Object is the object name, with the same placeholders as an s3 key. Defaults to ${date}/${channel}/${control_id}.hl7.
	Object string `yaml:"object,omitempty"`
	// StorageClass is STANDARD, NEARLINE, COLDLINE or ARCHIVE; empty is the bucket's default.
	StorageClass string `yaml:"storage_class,omitempty"`
	// ContentType defaults to application/hl7-v2.
	ContentType string `yaml:"content_type,omitempty"`
}

// Validate checks the settings.
func (d *GCSDestination) Validate() []error {
	errs := d.validateBucket("the gcs destination")
	if d.StorageClass != "" {
		up := strings.ToUpper(d.StorageClass)
		switch up {
		case "STANDARD", "NEARLINE", "COLDLINE", "ARCHIVE":
			d.StorageClass = up
		default:
			errs = append(errs, fmt.Errorf("gcs.storage_class %q is not STANDARD, NEARLINE, COLDLINE or ARCHIVE", d.StorageClass))
		}
	}
	if d.Object == "" {
		d.Object = defaultS3Key
	}
	return errs
}

// GCSSource picks objects up from a bucket prefix, as the s3 and azure_blob sources do.
type GCSSource struct {
	GCSAccess `yaml:",inline"`
	// Prefix is where objects are picked up from, ending in /, e.g. inbound/.
	Prefix string `yaml:"prefix"`
	// Suffix, when set, takes only names ending in it, e.g. .hl7.
	Suffix string `yaml:"suffix,omitempty"`
	// AfterRead is move (to move_to, the default) or delete.
	AfterRead string `yaml:"after_read,omitempty"`
	// MoveTo is where a handled object goes when after_read is move. Defaults to processed/.
	MoveTo string `yaml:"move_to,omitempty"`
	// ErrorPrefix is where an object that could not be handled goes. Defaults to error/.
	ErrorPrefix string `yaml:"error_prefix,omitempty"`
	// PollInterval is how often the prefix is listed. Defaults to 30s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
	// MaxObjectSize leaves larger objects where they are. Defaults to 16 MiB.
	MaxObjectSize int64 `yaml:"max_object_size,omitempty"`
	// Framed reads several MLLP-framed messages from one object.
	Framed bool `yaml:"framed,omitempty"`
}

// Validate checks the settings.
func (s *GCSSource) Validate() []error {
	errs := s.validateBucket("the gcs source")
	if !strings.HasSuffix(s.Prefix, "/") {
		errs = append(errs, errors.New("gcs.prefix is required and ends in /, e.g. inbound/"))
	}
	if s.AfterRead == "" {
		s.AfterRead = S3AfterReadMove
	}
	if s.AfterRead != S3AfterReadMove && s.AfterRead != S3AfterReadDelete {
		errs = append(errs, fmt.Errorf("gcs.after_read %q is not valid; use move or delete", s.AfterRead))
	}
	if s.MoveTo == "" {
		s.MoveTo = "processed/"
	}
	if s.ErrorPrefix == "" {
		s.ErrorPrefix = "error/"
	}
	if s.Prefix != "" && (strings.HasPrefix(s.MoveTo, s.Prefix) || strings.HasPrefix(s.ErrorPrefix, s.Prefix)) {
		errs = append(errs, errors.New("gcs.move_to and error_prefix must be outside prefix, or objects would be read again"))
	}
	if s.PollInterval == 0 {
		s.PollInterval = 30 * time.Second
	}
	if s.PollInterval < time.Second {
		errs = append(errs, errors.New("gcs.poll_interval must be at least 1s"))
	}
	if s.MaxObjectSize == 0 {
		s.MaxObjectSize = 16 << 20
	}
	return errs
}
