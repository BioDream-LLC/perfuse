package engine

// The MQTT, IBM MQ, Google Cloud Pub/Sub and Google Cloud Storage connectors. Each source hands a message on only once and
// confirms it to the far side (an MQTT PUBACK, removing the MQ message, a Pub/Sub ack, moving the object) only once the
// channel has handled it, so a crash or a failed message is read again rather than lost.

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/gcp"
	"github.com/biodream-llc/perfuse/internal/ibmmq"
	"github.com/biodream-llc/perfuse/internal/mqtt"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"github.com/biodream-llc/perfuse/mllp"
)

func senderTLS(s *tlsconf.Settings, what string) (*tls.Config, error) {
	if s == nil || !s.Enabled {
		return nil, nil
	}
	t, err := tlsconf.ForSender(s)
	if err != nil {
		return nil, fmt.Errorf("the %s tls settings could not be used: %w", what, err)
	}
	return t, nil
}

// handledOK runs a message through the channel and says whether it may be confirmed to the far side.
func (c *Channel) handledOK(ctx context.Context, body []byte) error {
	if _, err := c.handle(ctx, body); err != nil {
		return err
	}
	if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
		return fmt.Errorf("the message was %s", outcome)
	}
	return nil
}

// runUntilStopped starts a poller goroutine running loop until the channel stops.
func (c *Channel) runUntilStopped(loop func(ctx context.Context, p *kafkaPoller)) {
	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-p.stop; cancel() }()
		loop(ctx, p)
	}()
}

func sleepOrStop(p *kafkaPoller, d time.Duration) bool {
	select {
	case <-p.stop:
		return false
	case <-time.After(d):
		return true
	}
}

// ---- MQTT

func mqttConfig(m *config.MQTTConnection, clientID string, clean bool) (mqtt.Config, error) {
	t, err := senderTLS(m.TLS, "mqtt")
	if err != nil {
		return mqtt.Config{}, err
	}
	if m.ClientID != "" {
		clientID = m.ClientID
	}
	return mqtt.Config{Addr: m.Addr, TLS: t, ClientID: clientID, Username: m.Username, Password: m.ResolvedPassword(),
		CleanSession: clean, Timeout: m.Timeout}, nil
}

// MQTTSender publishes to an MQTT topic, keeping one connection open.
type MQTTSender struct {
	cfg  *config.MQTTDestination
	name string
	mu   sync.Mutex
	conn *mqtt.Conn
}

// NewMQTTSender makes the sender; it connects on the first message.
func NewMQTTSender(d config.Destination) (*MQTTSender, error) {
	if d.MQTT == nil {
		return nil, fmt.Errorf("destination %q is an mqtt destination with no mqtt block", d.Name)
	}
	return &MQTTSender{cfg: d.MQTT, name: d.Name}, nil
}

// Send publishes; at QoS 1 it returns once the broker has the message.
func (s *MQTTSender) Send(ctx context.Context, msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.conn.Err() != nil {
		host, _ := os.Hostname()
		cfg, err := mqttConfig(&s.cfg.MQTTConnection, "perfuse-"+s.name+"-"+host, true)
		if err != nil {
			return err
		}
		if s.conn, err = mqtt.Dial(ctx, cfg); err != nil {
			return err
		}
	}
	if err := s.conn.Publish(ctx, s.cfg.Topic, msg, s.cfg.ResolvedQoS(), s.cfg.Retain); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// Describe names the topic.
func (s *MQTTSender) Describe() string { return "mqtt " + s.cfg.Addr + " " + s.cfg.Topic }

// Close disconnects.
func (s *MQTTSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (c *Channel) startMQTTSource() error {
	cfg := c.cfg.Source.MQTT
	if cfg == nil {
		return fmt.Errorf("channel %q has an mqtt source with no configuration", c.cfg.Name)
	}
	if cfg.TLS == nil || !cfg.TLS.Enabled {
		c.log.Warn("mqtt source", "detail", "this connection to the broker is not encrypted, and the messages on it are patient data")
	}
	if cfg.ResolvedQoS() == 0 {
		c.log.Warn("mqtt source", "detail", "QoS 0 is at most once: a message the broker sends while Perfuse is down is lost")
	}
	c.log.Info("mqtt source starting", "addr", cfg.Addr, "topic", cfg.Topic)
	c.runUntilStopped(func(ctx context.Context, p *kafkaPoller) {
		for {
			if err := c.readMQTTSession(ctx, cfg); err != nil && ctx.Err() == nil {
				c.log.Error("the mqtt connection ended and will be made again", "addr", cfg.Addr, "err", err, "in", cfg.Reconnect)
			}
			if !sleepOrStop(p, cfg.Reconnect) {
				return
			}
		}
	})
	return nil
}

func (c *Channel) readMQTTSession(ctx context.Context, cfg *config.MQTTSource) error {
	// A persistent session under a fixed client id, so the broker keeps what arrives while Perfuse is away and sends again
	// what was not acknowledged.
	mc, err := mqttConfig(&cfg.MQTTConnection, "perfuse-"+c.cfg.Name, cfg.ResolvedQoS() == 0)
	if err != nil {
		return err
	}
	conn, err := mqtt.Dial(ctx, mc)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Subscribe(ctx, cfg.Topic, cfg.ResolvedQoS()); err != nil {
		return err
	}
	c.log.Info("subscribed over mqtt", "addr", cfg.Addr, "topic", cfg.Topic)
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-conn.Messages():
			if !ok {
				return conn.Err()
			}
			if err := c.handledOK(ctx, m.Payload); err != nil {
				// MQTT has no way to refuse one message. Dropping the connection unacknowledged is how to have it
				// sent again; the broker keeps it, first in line, for the next session.
				return fmt.Errorf("a message on %s could not be handled, so it was not acknowledged and will be sent again: %w",
					m.Topic, err)
			}
			if err := m.Ack(); err != nil {
				return err
			}
		}
	}
}

// ---- IBM MQ

func ibmmqClient(m *config.IBMMQConnection, nonPersistent bool) (*ibmmq.Client, error) {
	t, err := senderTLS(m.TLS, "ibm_mq")
	if err != nil {
		return nil, err
	}
	return ibmmq.New(ibmmq.Config{BaseURL: m.URL, QueueManager: m.QueueManager, Queue: m.Queue, Username: m.Username,
		Password: m.ResolvedPassword(), TLS: t, APIVersion: m.APIVersion, NonPersistent: nonPersistent, Timeout: m.Timeout})
}

// IBMMQSender puts each message on a queue.
type IBMMQSender struct {
	cfg    *config.IBMMQDestination
	client *ibmmq.Client
}

// NewIBMMQSender makes the sender.
func NewIBMMQSender(d config.Destination) (*IBMMQSender, error) {
	if d.IBMMQ == nil {
		return nil, fmt.Errorf("destination %q is an ibm_mq destination with no ibm_mq block", d.Name)
	}
	cl, err := ibmmqClient(&d.IBMMQ.IBMMQConnection, d.IBMMQ.NonPersistent)
	if err != nil {
		return nil, err
	}
	return &IBMMQSender{cfg: d.IBMMQ, client: cl}, nil
}

// Send puts the message, with its HL7 control id as the correlation id when it has one.
func (s *IBMMQSender) Send(ctx context.Context, msg []byte) error {
	corr := ""
	if m, err := hl7.Parse(msg); err == nil {
		corr = m.ControlID()
	}
	return s.client.Put(ctx, msg, corr)
}

// Describe names the queue.
func (s *IBMMQSender) Describe() string { return "ibm mq " + s.cfg.QueueManager + "/" + s.cfg.Queue }

// Close does nothing; each put is its own request.
func (s *IBMMQSender) Close() error { return nil }

func (c *Channel) startIBMMQSource() error {
	cfg := c.cfg.Source.IBMMQ
	if cfg == nil {
		return fmt.Errorf("channel %q has an ibm_mq source with no configuration", c.cfg.Name)
	}
	client, err := ibmmqClient(&cfg.IBMMQConnection, false)
	if err != nil {
		return err
	}
	c.log.Info("ibm mq source starting", "queue_manager", cfg.QueueManager, "queue", cfg.Queue)
	c.runUntilStopped(func(ctx context.Context, p *kafkaPoller) {
		for {
			if !c.readIBMMQOnce(ctx, client, cfg) && !sleepOrStop(p, cfg.PollInterval) {
				return
			}
			if ctx.Err() != nil {
				return
			}
		}
	})
	return nil
}

// readIBMMQOnce handles the first message on the queue, if there is one, and reports whether it did.
func (c *Channel) readIBMMQOnce(ctx context.Context, client *ibmmq.Client, cfg *config.IBMMQSource) bool {
	m, err := client.Browse(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("reading the ibm mq queue failed and will be retried", "queue", cfg.Queue, "err", err)
		}
		return false
	}
	if m == nil {
		return false
	}
	if err := c.handledOK(ctx, m.Body); err != nil {
		c.log.Error("a message from ibm mq could not be handled; it stays on the queue and will be read again",
			"queue", cfg.Queue, "message_id", m.ID, "err", err)
		return false
	}
	gone, err := client.Remove(ctx, m.ID)
	if err != nil {
		c.log.Error("a message was handled but could not be removed from the queue, so it will be read again",
			"queue", cfg.Queue, "message_id", m.ID, "err", err)
		return false
	}
	if !gone {
		c.log.Warn("a handled message had already left the queue; another reader may be taking from it",
			"queue", cfg.Queue, "message_id", m.ID)
	}
	return true
}

// ---- Google Cloud

func gcpCredentials(g *config.GCPAccess, scope string) (*gcp.Credentials, error) {
	if g.Emulator() {
		return nil, nil
	}
	if g.CredentialsFile == "" {
		return gcp.FromMetadata(), nil
	}
	data, err := os.ReadFile(g.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("reading the google cloud credentials file: %w", err)
	}
	return gcp.FromKeyJSON(data, scope)
}

// PubSubSender publishes to a topic.
type PubSubSender struct {
	cfg    *config.PubSubDestination
	name   string
	client *gcp.PubSub
}

// NewPubSubSender makes the sender.
func NewPubSubSender(d config.Destination) (*PubSubSender, error) {
	if d.PubSub == nil {
		return nil, fmt.Errorf("destination %q is a pubsub destination with no pubsub block", d.Name)
	}
	creds, err := gcpCredentials(&d.PubSub.GCPAccess, gcp.ScopePubSub)
	if err != nil {
		return nil, err
	}
	cl, err := gcp.NewPubSub(d.PubSub.Endpoint, d.PubSub.Project, creds)
	if err != nil {
		return nil, err
	}
	return &PubSubSender{cfg: d.PubSub, name: d.Name, client: cl}, nil
}

// Send publishes, with the message type and control id as attributes when it is HL7 v2.
func (s *PubSubSender) Send(ctx context.Context, msg []byte) error {
	m := gcp.Message{Data: msg}
	if parsed, err := hl7.Parse(msg); err == nil {
		m.Attributes = map[string]string{"hl7_message_type": parsed.MustGet("MSH-9"), "hl7_control_id": parsed.ControlID()}
	}
	if s.cfg.OrderBy != "" {
		m.OrderingKey = groupFor(msg, s.cfg.OrderBy, s.name)
	}
	_, err := s.client.Publish(ctx, s.cfg.Topic, m)
	return err
}

// Describe names the topic.
func (s *PubSubSender) Describe() string { return "pubsub " + s.cfg.Project + "/" + s.cfg.Topic }

// Close does nothing; each publish is its own request.
func (s *PubSubSender) Close() error { return nil }

func (c *Channel) startPubSubSource() error {
	cfg := c.cfg.Source.PubSub
	if cfg == nil {
		return fmt.Errorf("channel %q has a pubsub source with no configuration", c.cfg.Name)
	}
	creds, err := gcpCredentials(&cfg.GCPAccess, gcp.ScopePubSub)
	if err != nil {
		return err
	}
	client, err := gcp.NewPubSub(cfg.Endpoint, cfg.Project, creds)
	if err != nil {
		return err
	}
	c.log.Info("pubsub source starting", "project", cfg.Project, "subscription", cfg.Subscription)
	c.runUntilStopped(func(ctx context.Context, p *kafkaPoller) {
		for ctx.Err() == nil {
			msgs, err := client.Pull(ctx, cfg.Subscription, cfg.MaxMessages, 30*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					c.log.Error("pulling from pub/sub failed and will be retried", "subscription", cfg.Subscription, "err", err)
				}
				if !sleepOrStop(p, cfg.Reconnect) {
					return
				}
				continue
			}
			for i, m := range msgs {
				if err := c.handledOK(ctx, m.Data); err != nil {
					c.log.Error("a message from pub/sub could not be handled; it was handed back to be delivered again",
						"subscription", cfg.Subscription, "message_id", m.ID, "err", err)
					// This one and the rest of the batch, so none waits out its acknowledgement deadline.
					var rest []string
					for _, r := range msgs[i:] {
						rest = append(rest, r.AckID)
					}
					_ = client.Nack(ctx, cfg.Subscription, rest...)
					if !sleepOrStop(p, cfg.Reconnect) {
						return
					}
					break
				}
				if err := client.Ack(ctx, cfg.Subscription, m.AckID); err != nil {
					c.log.Error("a handled message could not be acknowledged, so pub/sub will deliver it again",
						"subscription", cfg.Subscription, "message_id", m.ID, "err", err)
				}
			}
		}
	})
	return nil
}

// GCSSender writes each message as an object.
type GCSSender struct {
	cfg    *config.GCSDestination
	name   string
	client *gcp.Storage
}

// NewGCSSender makes the sender.
func NewGCSSender(d config.Destination) (*GCSSender, error) {
	if d.GCS == nil {
		return nil, fmt.Errorf("destination %q is a gcs destination with no gcs block", d.Name)
	}
	creds, err := gcpCredentials(&d.GCS.GCPAccess, gcp.ScopeStorage)
	if err != nil {
		return nil, err
	}
	cl, err := gcp.NewStorage(d.GCS.Endpoint, d.GCS.Bucket, d.GCS.StorageClass, creds)
	if err != nil {
		return nil, err
	}
	return &GCSSender{cfg: d.GCS, name: d.Name, client: cl}, nil
}

// Send writes the object.
func (s *GCSSender) Send(ctx context.Context, msg []byte) error {
	name, err := renderObjectKey(s.cfg.Object, s.name, msg)
	if err != nil {
		return err
	}
	ct := s.cfg.ContentType
	if ct == "" {
		ct = "application/hl7-v2"
	}
	return s.client.Put(ctx, name, msg, ct)
}

// Describe names the bucket.
func (s *GCSSender) Describe() string { return "gcs gs://" + s.cfg.Bucket + "/" + s.cfg.Object }

// Close does nothing.
func (s *GCSSender) Close() error { return nil }

func (c *Channel) startGCSSource() error {
	cfg := c.cfg.Source.GCS
	if cfg == nil {
		return fmt.Errorf("channel %q has a gcs source with no configuration", c.cfg.Name)
	}
	creds, err := gcpCredentials(&cfg.GCPAccess, gcp.ScopeStorage)
	if err != nil {
		return err
	}
	client, err := gcp.NewStorage(cfg.Endpoint, cfg.Bucket, "", creds)
	if err != nil {
		return err
	}
	c.log.Info("gcs source starting", "bucket", cfg.Bucket, "prefix", cfg.Prefix)
	c.runUntilStopped(func(ctx context.Context, p *kafkaPoller) {
		for {
			c.pollGCSOnce(ctx, client, cfg)
			if !sleepOrStop(p, cfg.PollInterval) {
				return
			}
		}
	})
	return nil
}

func (c *Channel) pollGCSOnce(ctx context.Context, client *gcp.Storage, cfg *config.GCSSource) {
	token := ""
	for {
		objs, next, err := client.List(ctx, cfg.Prefix, token, 1000)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Error("listing the bucket prefix failed and will be retried", "bucket", cfg.Bucket, "err", err)
			}
			return
		}
		for _, o := range objs {
			if ctx.Err() != nil {
				return
			}
			if strings.HasSuffix(o.Name, "/") || (cfg.Suffix != "" && !strings.HasSuffix(o.Name, cfg.Suffix)) {
				continue
			}
			c.handleGCSObject(ctx, client, cfg, o)
		}
		if next == "" {
			return
		}
		token = next
	}
}

func (c *Channel) handleGCSObject(ctx context.Context, client *gcp.Storage, cfg *config.GCSSource, o gcp.Object) {
	rel := strings.TrimPrefix(o.Name, cfg.Prefix)
	var body []byte
	move := func(to string) bool {
		if err := client.Put(ctx, to, body, ""); err != nil {
			c.log.Error("an object could not be moved, so it will be read again", "object", o.Name, "to", to, "err", err)
			return false
		}
		return true
	}
	fail := func(why error) {
		c.log.Error("an object could not be handled; it was moved to the error prefix", "object", o.Name, "err", why)
		if body != nil && move(cfg.ErrorPrefix+rel) {
			_ = client.Delete(ctx, o.Name)
		}
	}
	if o.Size > cfg.MaxObjectSize {
		c.log.Error("an object is larger than gcs.max_object_size and was left where it is", "object", o.Name, "size", o.Size)
		return
	}
	var err error
	body, err = client.Get(ctx, o.Name, cfg.MaxObjectSize)
	if err != nil {
		c.log.Error("reading an object failed and will be retried at the next poll", "object", o.Name, "err", err)
		return
	}
	messages := [][]byte{body}
	if cfg.Framed {
		messages = nil
		r := mllp.NewReader(bytes.NewReader(body), int(cfg.MaxObjectSize))
		for {
			m, err := r.ReadMessage()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(fmt.Errorf("the object is set as framed but the framing is not valid: %w", err))
				return
			}
			messages = append(messages, m)
		}
	}
	for i, m := range messages {
		if err := c.handledOK(ctx, m); err != nil {
			fail(fmt.Errorf("message %d of %d in the object: %w", i+1, len(messages), err))
			return
		}
	}
	if cfg.AfterRead == config.S3AfterReadMove && !move(cfg.MoveTo+rel) {
		return
	}
	if err := client.Delete(ctx, o.Name); err != nil {
		c.log.Error("an object was handled but could not be removed, so it will be read again", "object", o.Name, "err", err)
	}
}
