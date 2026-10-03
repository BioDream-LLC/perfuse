package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/amqp"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// AMQP 1.0, for Azure Service Bus and the brokers that speak it.
//
// The sender keeps one connection and one link, opened on first use and replaced when it fails. Success is the broker's accepted
// disposition, not the write: a message the broker released or rejected is a failed delivery, and the destination's queue retries it.

func amqpConfig(c *config.AMQPConnection) (amqp.Config, error) {
	var t *tls.Config
	if c.TLS != nil && c.TLS.Enabled {
		cfg, err := tlsconf.ForSender(c.TLS)
		if err != nil {
			return amqp.Config{}, fmt.Errorf("the amqp tls settings could not be used: %w", err)
		}
		t = cfg
	}
	return amqp.Config{Addr: c.Addr, TLS: t, Username: c.Username, Password: c.ResolvedPassword(), Hostname: c.Hostname,
		Timeout: c.Timeout}, nil
}

// AMQPSender sends each message to a queue or topic.
type AMQPSender struct {
	cfg *config.AMQPDestination

	mu     sync.Mutex
	conn   *amqp.Conn
	sender *amqp.Sender
}

// NewAMQPSender builds the sender; it connects on the first message.
func NewAMQPSender(d config.Destination) (*AMQPSender, error) {
	if d.AMQP == nil {
		return nil, fmt.Errorf("destination %q is an amqp destination with no amqp block", d.Name)
	}
	return &AMQPSender{cfg: d.AMQP}, nil
}

func (s *AMQPSender) connect(ctx context.Context) (*amqp.Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sender != nil && s.conn.Err() == nil {
		return s.sender, nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	cfg, err := amqpConfig(&s.cfg.AMQPConnection)
	if err != nil {
		return nil, err
	}
	conn, err := amqp.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	snd, err := conn.NewSender(ctx, s.cfg.Address)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	s.conn, s.sender = conn, snd
	return snd, nil
}

// Send transfers one message and waits for the broker to accept it.
func (s *AMQPSender) Send(ctx context.Context, msg []byte) error {
	snd, err := s.connect(ctx)
	if err != nil {
		return err
	}
	m := &amqp.Message{Body: msg, ContentType: s.cfg.ContentType}
	if parsed, err := hl7.Parse(msg); err == nil {
		m.MessageID = parsed.ControlID()
		m.Subject = parsed.MustGet("MSH-9")
		if m.ContentType == "" {
			m.ContentType = "application/hl7-v2"
		}
	}
	if err := snd.Send(ctx, m); err != nil {
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.conn, s.sender = nil, nil
		s.mu.Unlock()
		return err
	}
	return nil
}

// Describe names the broker and address, never the credentials.
func (s *AMQPSender) Describe() string { return "amqp " + s.cfg.Addr + " " + s.cfg.Address }

// Close ends the connection.
func (s *AMQPSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// startAMQPSource begins receiving.
func (c *Channel) startAMQPSource() error {
	cfg := c.cfg.Source.AMQP
	if cfg == nil {
		return fmt.Errorf("channel %q has an amqp source with no configuration", c.cfg.Name)
	}
	if cfg.TLS == nil || !cfg.TLS.Enabled {
		c.log.Warn("amqp source", "detail", "this connection to the broker is not encrypted, and the messages on it are patient data")
	}
	c.log.Info("amqp source starting", "addr", cfg.Addr, "address", cfg.Address, "prefetch", cfg.Prefetch)
	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		c.readAMQPForever(p, cfg)
	}()
	return nil
}

func (c *Channel) readAMQPForever(p *kafkaPoller, cfg *config.AMQPSource) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-p.stop; cancel() }()
	for {
		if err := c.readAMQPSession(ctx, cfg); err != nil && ctx.Err() == nil {
			c.log.Error("the amqp connection failed and will be retried", "addr", cfg.Addr, "err", err, "in", cfg.Reconnect)
		}
		select {
		case <-p.stop:
			return
		case <-time.After(cfg.Reconnect):
		}
	}
}

func (c *Channel) readAMQPSession(ctx context.Context, cfg *config.AMQPSource) error {
	acfg, err := amqpConfig(&cfg.AMQPConnection)
	if err != nil {
		return err
	}
	conn, err := amqp.Dial(ctx, acfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	r, err := conn.NewReceiver(ctx, cfg.Address, uint32(cfg.Prefetch))
	if err != nil {
		return err
	}
	c.log.Info("receiving over amqp", "addr", cfg.Addr, "address", cfg.Address)
	for {
		m, err := r.Receive(ctx)
		if err != nil {
			return err
		}
		_, herr := c.handle(ctx, m.Body)
		if herr == nil {
			if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
				herr = fmt.Errorf("the message was %s", outcome)
			}
		}
		if herr != nil {
			// Released as a failed delivery, so the broker counts the attempt and - past the queue's limit - dead-letters it.
			c.log.Error("a message from amqp could not be handled; the broker will redeliver it", "address", cfg.Address,
				"message_id", m.MessageID, "err", herr)
			if err := m.Release(); err != nil {
				return err
			}
			continue
		}
		if err := m.Accept(); err != nil {
			return err
		}
	}
}
