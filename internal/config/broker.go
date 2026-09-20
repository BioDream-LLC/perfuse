package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// BrokerSource reads messages from a message broker.
//
// Mirth's JMS Reader, reached over STOMP because JMS is a Java API rather than a protocol. See internal/stomp for why STOMP and not
// OpenWire.
type BrokerSource struct {
	// Addr is the broker's host and port. Required. 61613 is the usual STOMP port.
	Addr string `yaml:"addr"`

	// Destination is the queue or topic to read from. Required.
	//
	// Brokers spell these differently - /queue/name on ActiveMQ, a bare name on RabbitMQ - so it is passed through unchanged
	// rather than being assembled here. Guessing would mean working against one broker and silently reading nothing on another.
	Destination string `yaml:"destination"`

	// Login and Passcode authenticate. Most brokers refuse an anonymous connection.
	Login    string `yaml:"login,omitempty"`
	Passcode string `yaml:"passcode,omitempty"`

	// Host is the virtual host. Defaults to the address's host.
	Host string `yaml:"host,omitempty"`

	// Selector filters messages at the broker.
	//
	// A JMS selector, passed through rather than interpreted. Filtering at the broker rather than here means the messages this
	// channel does not want never cross the network - which on a shared queue is the difference between reading a hundred
	// messages a day and a hundred thousand.
	Selector string `yaml:"selector,omitempty"`

	// SubscriptionID names the subscription. Defaults to the channel name.
	//
	// Named rather than generated, because a durable subscription is identified by it: a generated one would create a new
	// subscription on every restart and leave the old ones accumulating messages nobody reads.
	SubscriptionID string `yaml:"subscription_id,omitempty"`

	// Heartbeat keeps the connection alive. Defaults to thirty seconds.
	//
	// On by default here, unlike the client's own default, because a source holds one connection open for weeks. Without
	// heartbeats a connection through a firewall that has silently dropped the route looks perfectly healthy from this end and
	// messages simply stop arriving, with nothing logged.
	Heartbeat time.Duration `yaml:"heartbeat,omitempty"`

	// Reconnect is how long to wait before reconnecting after a failure. Defaults to five seconds.
	Reconnect time.Duration `yaml:"reconnect,omitempty"`

	// Timeout bounds connecting. Defaults to thirty seconds.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// TLS wraps the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// Broker defaults.
const (
	DefaultBrokerHeartbeat = 30 * time.Second
	DefaultBrokerReconnect = 5 * time.Second
	DefaultBrokerTimeout   = 30 * time.Second
)

// ResolvedHeartbeat is the heartbeat with its default applied.
func (b *BrokerSource) ResolvedHeartbeat() time.Duration {
	if b == nil || b.Heartbeat < 0 {
		return 0
	}
	if b.Heartbeat == 0 {
		return DefaultBrokerHeartbeat
	}
	return b.Heartbeat
}

// ResolvedReconnect is the reconnect delay with its default applied.
func (b *BrokerSource) ResolvedReconnect() time.Duration {
	if b == nil || b.Reconnect <= 0 {
		return DefaultBrokerReconnect
	}
	return b.Reconnect
}

// ResolvedTimeout is the connect timeout with its default applied.
func (b *BrokerSource) ResolvedTimeout() time.Duration {
	if b == nil || b.Timeout <= 0 {
		return DefaultBrokerTimeout
	}
	return b.Timeout
}

func (b *BrokerSource) validate() []error {
	if b == nil {
		return []error{fmt.Errorf("a broker source needs a broker block")}
	}

	var errs []error

	if strings.TrimSpace(b.Addr) == "" {
		errs = append(errs, fmt.Errorf("broker.addr is required, the host and port of the message broker"))
	}
	if strings.TrimSpace(b.Destination) == "" {
		errs = append(errs, fmt.Errorf("broker.destination is required, the queue or topic to read from"))
	}
	if b.Reconnect < 0 {
		errs = append(errs, fmt.Errorf("broker.reconnect cannot be negative"))
	}
	if b.Reconnect > 0 && b.Reconnect < time.Second {
		// Refused. A reconnect loop faster than once a second against a broker that is down is indistinguishable from an
		// attack, and it fills the log so quickly that the original failure scrolls away.
		errs = append(errs, fmt.Errorf("broker.reconnect is %s; anything under a second will hammer a broker that is down "+
			"and bury the reason in the log", b.Reconnect))
	}
	if b.Login == "" && b.Passcode != "" {
		errs = append(errs, fmt.Errorf("broker.passcode is set without broker.login"))
	}
	if b.TLS != nil {
		for _, err := range b.TLS.Validate(false) {
			errs = append(errs, fmt.Errorf("broker tls: %w", err))
		}
	}

	return errs
}

// BrokerDestination publishes messages to a message broker.
//
// Mirth's JMS Writer.
type BrokerDestination struct {
	// Addr is the broker's host and port. Required.
	Addr string `yaml:"addr"`

	// Destination is the queue or topic to publish to. Required.
	Destination string `yaml:"destination"`

	// Login and Passcode authenticate. Most brokers refuse an anonymous connection.
	Login    string `yaml:"login,omitempty"`
	Passcode string `yaml:"passcode,omitempty"`

	// Host is the virtual host. Defaults to the address's host.
	//
	// Worth setting explicitly on a broker that hosts several virtual hosts, where the default lands the publish on
	// whichever one answers to the server's own name - a queue that exists, accepts the message, and is read by nobody.
	Host string `yaml:"host,omitempty"`

	// ContentType is sent with each message. Defaults to text/plain for HL7.
	ContentType string `yaml:"content_type,omitempty"`

	// Headers are sent with every message.
	//
	// Configuration only, never message content. A header taken from a field would put a patient identifier into broker
	// metadata that is logged, indexed and visible to every other application on the queue.
	Headers map[string]string `yaml:"headers,omitempty"`

	// Persistent asks the broker to survive a restart. Defaults to true.
	//
	// True because a non-persistent message is lost when the broker restarts, and that is not a reasonable default for clinical
	// data. False exists for a genuinely transient feed - a monitoring heartbeat - and has to be chosen.
	Persistent *bool `yaml:"persistent,omitempty"`

	// Timeout bounds one publish, including waiting for the broker to confirm it.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// TLS wraps the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// IsPersistent reports whether messages should survive a broker restart.
func (b *BrokerDestination) IsPersistent() bool {
	if b == nil || b.Persistent == nil {
		return true
	}
	return *b.Persistent
}

// ResolvedTimeout is the publish timeout with its default applied.
func (b *BrokerDestination) ResolvedTimeout() time.Duration {
	if b == nil || b.Timeout <= 0 {
		return DefaultBrokerTimeout
	}
	return b.Timeout
}

func validateBrokerDest(d *Destination) []error {
	cfg := d.Broker
	if cfg == nil {
		return []error{fmt.Errorf("destination %q is a broker destination with no broker block", d.Name)}
	}

	var errs []error

	if strings.TrimSpace(cfg.Addr) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs broker.addr", d.Name))
	}
	if strings.TrimSpace(cfg.Destination) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs broker.destination, the queue or topic to publish to", d.Name))
	}
	if cfg.Login == "" && cfg.Passcode != "" {
		errs = append(errs, fmt.Errorf("destination %q sets broker.passcode without broker.login", d.Name))
	}

	for name := range cfg.Headers {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, fmt.Errorf("destination %q has a broker header with an empty name", d.Name))
			continue
		}
		// The protocol's own headers are refused rather than overwritten. Setting destination or content-length here would
		// either be ignored or corrupt the frame, and neither failure names this setting as the cause.
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "destination", "content-length", "receipt", "persistent", "ack", "message-id", "subscription":
			errs = append(errs, fmt.Errorf("destination %q sets the broker header %q, which the protocol uses itself; "+
				"setting it here would either be ignored or corrupt the message", d.Name, name))
		}
	}

	if cfg.TLS != nil {
		for _, err := range cfg.TLS.Validate(false) {
			errs = append(errs, fmt.Errorf("destination %q broker tls: %w", d.Name, err))
		}
	}

	return errs
}
