package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/stomp"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// brokerPoller reads from a message broker until stopped.
type brokerPoller struct {
	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

func (p *brokerPoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// startBrokerSource begins reading from a broker.
func (c *Channel) startBrokerSource() error {
	cfg := c.cfg.Source.Broker
	if cfg == nil {
		return fmt.Errorf("channel %q has a broker source with no configuration", c.cfg.Name)
	}

	if cfg.TLS == nil {
		c.log.Warn("broker source",
			"detail", "this connection to the broker is not encrypted, and the messages on it are patient data")
	}

	c.log.Info("broker source starting",
		"broker", cfg.Addr,
		"destination", cfg.Destination,
		"heartbeat", cfg.ResolvedHeartbeat(),
	)

	poller := &brokerPoller{stop: make(chan struct{})}
	c.broker = poller

	poller.wg.Add(1)
	go func() {
		defer poller.wg.Done()
		c.readBrokerForever(poller, cfg)
	}()

	return nil
}

// stopBrokerSource ends reading and waits for the current message.
func (c *Channel) stopBrokerSource() error {
	if c.broker == nil {
		return nil
	}
	c.broker.close()
	c.broker = nil
	return nil
}

// readBrokerForever connects, reads, and reconnects.
//
// Reconnecting rather than failing is the whole point of a long-lived source. A broker restart, a network blip or a failover are all
// ordinary events over weeks of running, and a channel that stopped on the first one would need somebody to notice and restart it.
func (c *Channel) readBrokerForever(p *brokerPoller, cfg *config.BrokerSource) {
	for {
		select {
		case <-p.stop:
			return
		default:
		}

		if err := c.readBrokerSession(p, cfg); err != nil {
			select {
			case <-p.stop:
				return
			default:
			}

			// Logged at error each time. A quieter second attempt would hide a broker that is refusing every connection, and
			// the reconnect delay bounds how often this can happen.
			c.log.Error("the broker connection failed, reconnecting",
				"broker", cfg.Addr, "err", err, "in", cfg.ResolvedReconnect())

			select {
			case <-p.stop:
				return
			case <-time.After(cfg.ResolvedReconnect()):
			}
		}
	}
}

// readBrokerSession holds one connection and reads from it.
func (c *Channel) readBrokerSession(p *brokerPoller, cfg *config.BrokerSource) error {
	opts := stomp.Options{
		Addr:      cfg.Addr,
		Host:      cfg.Host,
		Login:     cfg.Login,
		Passcode:  cfg.Passcode,
		Timeout:   cfg.ResolvedTimeout(),
		Heartbeat: cfg.ResolvedHeartbeat(),
	}
	if cfg.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(cfg.TLS)
		if err != nil {
			return fmt.Errorf("the broker tls settings could not be used: %w", err)
		}
		opts.TLS = tlsCfg
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ResolvedTimeout())
	conn, err := stomp.Dial(ctx, opts)
	cancel()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	subscription := cfg.SubscriptionID
	if subscription == "" {
		// The channel name, so a durable subscription is stable across restarts. A generated identifier would create a new
		// subscription every time and leave the previous ones accumulating messages nobody reads.
		subscription = c.cfg.Name
	}

	if err := conn.Subscribe(subscription, cfg.Destination, cfg.Selector); err != nil {
		return err
	}

	c.log.Info("subscribed to the broker",
		"broker", cfg.Addr, "destination", cfg.Destination, "server", conn.Server, "version", conn.Version)

	for {
		select {
		case <-p.stop:
			return nil
		default:
		}

		// A short read timeout so shutdown is prompt on a quiet queue. Receive returns nil without error when nothing arrived,
		// which is the ordinary case rather than a problem.
		frame, err := conn.Receive(2 * time.Second)
		if err != nil {
			return err
		}
		if frame == nil {
			continue
		}

		c.handleBrokerMessage(conn, frame)
	}
}

// handleBrokerMessage routes one message and tells the broker what happened.
func (c *Channel) handleBrokerMessage(conn *stomp.Conn, frame *stomp.Frame) {
	if _, err := c.handle(context.Background(), frame.Body); err != nil {
		// Refused rather than acknowledged. Acknowledging a message this channel could not handle would discard it, and the
		// broker's own redelivery and dead-letter handling is already configured by whoever runs it.
		if nackErr := conn.Nack(frame); nackErr != nil {
			c.log.Error("a message could not be handled and could not be refused either",
				"err", err, "nack_err", nackErr)
			return
		}
		c.log.Warn("a message was refused back to the broker", "err", err)
		return
	}

	if err := conn.Ack(frame); err != nil {
		// Handled but not acknowledged, so the broker will offer it again. Said plainly, because the consequence is a duplicate
		// rather than a loss and somebody seeing two of everything needs to find this line.
		c.log.Error("a message was handled but could not be acknowledged, so the broker may deliver it again", "err", err)
	}
}

// BrokerSender publishes messages to a message broker.
//
// Mirth's JMS Writer. The connection is opened on demand and kept, because a destination that reconnected per message would spend
// more time in handshakes than in sending - and a broker sees a connection per message as an attack.
type BrokerSender struct {
	cfg *config.BrokerDestination

	mu   sync.Mutex
	conn *stomp.Conn
}

// NewBrokerSender builds a broker destination.
func NewBrokerSender(d config.Destination) (*BrokerSender, error) {
	if d.Broker == nil {
		return nil, fmt.Errorf("destination %q is a broker destination with no configuration", d.Name)
	}
	return &BrokerSender{cfg: d.Broker}, nil
}

// Send publishes one message.
func (s *BrokerSender) Send(ctx context.Context, msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}

	headers := map[string]string{
		"persistent": boolText(s.cfg.IsPersistent()),
	}
	if s.cfg.ContentType != "" {
		headers["content-type"] = s.cfg.ContentType
	} else {
		// Declared rather than left out. A broker that has to guess may treat the body as binary and some consumers then
		// receive base64 where they expected text.
		headers["content-type"] = "text/plain"
	}
	for name, value := range s.cfg.Headers {
		headers[name] = value
	}

	if err := conn.Send(s.cfg.Destination, msg, headers, s.cfg.ResolvedTimeout()); err != nil {
		// The connection is dropped on any failure, so the next attempt reconnects. A half-broken connection that is kept
		// fails every subsequent message with the same error and looks like the broker being permanently down.
		_ = conn.Close()
		s.conn = nil
		return err
	}

	return nil
}

// connection returns the open connection, dialling if necessary.
func (s *BrokerSender) connection(ctx context.Context) (*stomp.Conn, error) {
	if s.conn != nil {
		return s.conn, nil
	}

	opts := stomp.Options{
		Addr:     s.cfg.Addr,
		Host:     s.cfg.Host,
		Login:    s.cfg.Login,
		Passcode: s.cfg.Passcode,
		Timeout:  s.cfg.ResolvedTimeout(),
	}
	if s.cfg.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(s.cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("the broker tls settings could not be used: %w", err)
		}
		opts.TLS = tlsCfg
	}

	conn, err := stomp.Dial(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.conn = conn
	return conn, nil
}

// Describe names the destination.
//
// Never the login or the passcode.
func (s *BrokerSender) Describe() string {
	out := fmt.Sprintf("published to %s on the broker at %s", s.cfg.Destination, s.cfg.Addr)
	if !s.cfg.IsPersistent() {
		out += " (not persistent)"
	}
	if s.cfg.TLS != nil {
		out += " over TLS"
	}
	return out
}

// Close releases the connection.
func (s *BrokerSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return nil
	}

	err := s.conn.Close()
	s.conn = nil

	if err != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
