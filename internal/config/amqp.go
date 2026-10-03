package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// AMQP 1.0: Azure Service Bus and Event Hubs, RabbitMQ 4, ActiveMQ and Artemis.
//
// Separate from the broker connector, which speaks STOMP, because the two disagree where it matters: AMQP has links and credit, and a
// message is settled by an explicit disposition rather than an ack frame. For Service Bus, addr is <namespace>.servicebus.windows.net:5671
// with tls, username the shared access policy name, password its key, and address the queue name (or <topic>/subscriptions/<name> to
// receive from a subscription).

// AMQPConnection is where and as whom to connect.
type AMQPConnection struct {
	// Addr is host:port: 5672 plain, 5671 TLS.
	Addr string `yaml:"addr"`
	// Address is the queue or topic: a Service Bus queue name, a RabbitMQ /queues/<name>, an ActiveMQ queue name.
	Address string `yaml:"address"`
	// Username and Password authenticate with SASL PLAIN; both empty means ANONYMOUS. For Service Bus, the policy name and key.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// Hostname is sent to the broker, which Service Bus routes on. Defaults to the host of addr.
	Hostname string `yaml:"hostname,omitempty"`
	// TLS encrypts the connection. Required for Service Bus and Event Hubs.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
	// Timeout bounds connecting and each wait for the broker. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

func (a *AMQPConnection) validate(what string) []error {
	var errs []error
	if _, _, err := net.SplitHostPort(a.Addr); err != nil {
		errs = append(errs, fmt.Errorf("%s needs amqp.addr as host:port, e.g. contoso.servicebus.windows.net:5671", what))
	}
	if strings.TrimSpace(a.Address) == "" {
		errs = append(errs, fmt.Errorf("%s needs amqp.address, the queue or topic", what))
	}
	if strings.HasSuffix(strings.Split(a.Addr, ":")[0], ".servicebus.windows.net") && (a.TLS == nil || !a.TLS.Enabled) {
		errs = append(errs, fmt.Errorf("%s connects to Azure Service Bus, which requires tls", what))
	}
	if a.Timeout == 0 {
		a.Timeout = 30 * time.Second
	}
	return errs
}

// ResolvedPassword expands an environment reference.
func (a *AMQPConnection) ResolvedPassword() string { return resolveSecret(a.Password) }

// AMQPSource receives from a queue or subscription.
//
// A message is accepted only after the channel has handled it. One that could not be handled is released as a failed delivery, so the
// broker redelivers it and - on Service Bus, past the queue's max delivery count - dead-letters it.
type AMQPSource struct {
	AMQPConnection `yaml:",inline"`
	// Prefetch is how many messages the broker may send ahead. Defaults to 10. They are still handled one at a time, in order.
	Prefetch int `yaml:"prefetch,omitempty"`
	// Reconnect is the wait after a lost connection. Defaults to 5s.
	Reconnect time.Duration `yaml:"reconnect,omitempty"`
}

// Validate checks the settings.
func (s *AMQPSource) Validate() []error {
	errs := s.validate("the amqp source")
	if s.Prefetch == 0 {
		s.Prefetch = 10
	}
	if s.Prefetch < 1 || s.Prefetch > 1000 {
		errs = append(errs, errors.New("amqp.prefetch must be 1 to 1000"))
	}
	if s.Reconnect == 0 {
		s.Reconnect = 5 * time.Second
	}
	return errs
}

// AMQPDestination sends to a queue or topic, and reports success only once the broker has accepted the message.
type AMQPDestination struct {
	AMQPConnection `yaml:",inline"`
	// ContentType is sent with each message. Defaults to the channel's data type.
	ContentType string `yaml:"content_type,omitempty"`
}

// Validate checks the settings.
func (d *AMQPDestination) Validate() []error { return d.validate("the amqp destination") }
