// Package mqtt is an MQTT 3.1.1 client: connect (with TLS, a username and password, or a client certificate), publish at
// QoS 0 or 1, subscribe, and acknowledge each received QoS 1 message only once the caller says it was handled.
//
// Written against the OASIS MQTT 3.1.1 standard rather than imported, as Perfuse's other wire protocols are. 3.1.1 is
// the version every broker speaks: Mosquitto, EMQX, HiveMQ, VerneMQ, RabbitMQ's MQTT plugin, IBM MQ's telemetry
// channel, AWS IoT Core and Azure Event Grid.
//
// Delivery: a message received at QoS 1 is acknowledged (PUBACK) only when Ack is called. A caller that cannot handle a
// message closes the connection instead; with a persistent session (CleanSession false) the broker sends it again, marked
// as a duplicate, when the client reconnects. MQTT 3.1.1 has no negative acknowledgement, so this is the only way to ask
// for a message again.
package mqtt

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Packet types (the high nibble of the first byte).
const (
	typeConnect     = 1
	typeConnack     = 2
	typePublish     = 3
	typePuback      = 4
	typeSubscribe   = 8
	typeSuback      = 9
	typePingreq     = 12
	typePingresp    = 13
	typeDisconnect  = 14
	maxRemainingLen = 268435455 // the largest length the four-byte encoding can carry
)

// Config says where and how to connect.
type Config struct {
	// Addr is host:port; 1883 plain, 8883 TLS.
	Addr string
	// TLS, when set, encrypts the connection (and may carry a client certificate, as AWS IoT Core requires).
	TLS *tls.Config
	// ClientID names the session. A source keeps the same one so the broker holds its messages while it is away.
	ClientID string
	// Username and Password authenticate; both may be empty.
	Username, Password string
	// CleanSession false asks the broker to keep the session, and the QoS 1 messages not yet acknowledged, across
	// reconnects.
	CleanSession bool
	// KeepAlive is how often a ping is sent when nothing else is. Defaults to 30s.
	KeepAlive time.Duration
	// Timeout bounds connecting and each wait for the broker. Defaults to 30s.
	Timeout time.Duration
	// MaxMessage refuses a received message larger than this. Defaults to 16 MiB.
	MaxMessage int
}

// Message is one message received.
type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	// Dup is set when the broker says it may have sent this message before.
	Dup bool

	id   uint16
	conn *Conn
}

// Ack acknowledges a QoS 1 message. A QoS 0 message needs none, and Ack does nothing for it.
func (m *Message) Ack() error {
	if m.QoS == 0 {
		return nil
	}
	return m.conn.write([]byte{typePuback << 4, 2, byte(m.id >> 8), byte(m.id)})
}

// Conn is one connection to a broker.
type Conn struct {
	cfg  Config
	nc   net.Conn
	wmu  sync.Mutex
	mu   sync.Mutex
	next uint16
	wait map[uint16]chan []byte // packet id -> the PUBACK or SUBACK body

	messages chan *Message
	done     chan struct{}
	err      error
	once     sync.Once
}

// ConnackError is the broker's refusal of the connection.
type ConnackError byte

func (e ConnackError) Error() string {
	reasons := map[ConnackError]string{1: "unacceptable protocol version", 2: "client identifier rejected",
		3: "server unavailable", 4: "bad user name or password", 5: "not authorised"}
	if r, ok := reasons[e]; ok {
		return "mqtt: the broker refused the connection: " + r
	}
	return fmt.Sprintf("mqtt: the broker refused the connection (code %d)", byte(e))
}

// Dial connects and completes the MQTT handshake.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxMessage == 0 {
		cfg.MaxMessage = 16 << 20
	}
	if cfg.ClientID == "" && !cfg.CleanSession {
		return nil, errors.New("mqtt: a persistent session (clean session off) needs a client id")
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	var nc net.Conn
	var err error
	if cfg.TLS != nil {
		td := tls.Dialer{NetDialer: &d, Config: cfg.TLS}
		nc, err = td.DialContext(ctx, "tcp", cfg.Addr)
	} else {
		nc, err = d.DialContext(ctx, "tcp", cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("mqtt: connecting to %s: %w", cfg.Addr, err)
	}
	c := &Conn{cfg: cfg, nc: nc, wait: map[uint16]chan []byte{}, messages: make(chan *Message, 16), done: make(chan struct{})}

	_ = nc.SetDeadline(time.Now().Add(cfg.Timeout))
	if err := c.write(connectPacket(cfg)); err != nil {
		_ = nc.Close()
		return nil, err
	}
	br := bufio.NewReader(nc)
	first, body, err := readPacket(br, 4)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("mqtt: reading the broker's answer to connect: %w", err)
	}
	if first>>4 != typeConnack || len(body) != 2 {
		_ = nc.Close()
		return nil, fmt.Errorf("mqtt: expected connack, got packet type %d", first>>4)
	}
	if body[1] != 0 {
		_ = nc.Close()
		return nil, ConnackError(body[1])
	}
	_ = nc.SetDeadline(time.Time{})

	go c.readLoop(br)
	go c.pingLoop()
	return c, nil
}

func connectPacket(cfg Config) []byte {
	var flags byte
	if cfg.CleanSession {
		flags |= 0x02
	}
	var payload []byte
	payload = appendString(payload, cfg.ClientID)
	if cfg.Username != "" {
		flags |= 0x80
		payload = appendString(payload, cfg.Username)
	}
	if cfg.Password != "" {
		flags |= 0x40
		payload = appendString(payload, cfg.Password)
	}
	keep := uint16(cfg.KeepAlive / time.Second)
	vh := appendString(nil, "MQTT")
	vh = append(vh, 4, flags, byte(keep>>8), byte(keep))
	return packet(typeConnect<<4, append(vh, payload...))
}

// Publish sends a message. At QoS 1 it returns once the broker has acknowledged it; at QoS 0 once it is written.
func (c *Conn) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	if qos > 1 {
		return errors.New("mqtt: QoS 2 is not supported; use 1, which with an idempotent receiver is as safe")
	}
	if err := validTopic(topic, false); err != nil {
		return err
	}
	first := byte(typePublish<<4) | qos<<1
	if retain {
		first |= 1
	}
	body := appendString(nil, topic)
	var id uint16
	var ch chan []byte
	if qos == 1 {
		id, ch = c.reserve()
		defer c.release(id)
		body = append(body, byte(id>>8), byte(id))
	}
	body = append(body, payload...)
	if err := c.write(packet(first, body)); err != nil {
		return err
	}
	if qos == 0 {
		return nil
	}
	return c.await(ctx, ch, "the broker did not acknowledge the message")
}

// Subscribe asks for the messages on a topic filter, at most at qos. Received messages arrive on Messages.
func (c *Conn) Subscribe(ctx context.Context, filter string, qos byte) error {
	if qos > 1 {
		return errors.New("mqtt: QoS 2 is not supported")
	}
	if err := validTopic(filter, true); err != nil {
		return err
	}
	id, ch := c.reserve()
	defer c.release(id)
	body := []byte{byte(id >> 8), byte(id)}
	body = appendString(body, filter)
	body = append(body, qos)
	if err := c.write(packet(typeSubscribe<<4|0x02, body)); err != nil {
		return err
	}
	var granted []byte
	err := c.awaitBody(ctx, ch, "the broker did not answer the subscription", &granted)
	if err != nil {
		return err
	}
	if len(granted) != 1 || granted[0] == 0x80 {
		return fmt.Errorf("mqtt: the broker refused the subscription to %q", filter)
	}
	return nil
}

// Messages delivers what the subscriptions receive. It is closed when the connection ends.
func (c *Conn) Messages() <-chan *Message { return c.messages }

// Err says why the connection ended, or nil while it is up.
func (c *Conn) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// Close disconnects cleanly.
func (c *Conn) Close() error {
	_ = c.write([]byte{typeDisconnect << 4, 0})
	c.fail(errors.New("mqtt: the connection was closed"))
	return nil
}

func (c *Conn) reserve() (uint16, chan []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		c.next++
		if c.next == 0 {
			c.next = 1
		}
		if _, used := c.wait[c.next]; !used {
			ch := make(chan []byte, 1)
			c.wait[c.next] = ch
			return c.next, ch
		}
	}
}

func (c *Conn) release(id uint16) {
	c.mu.Lock()
	delete(c.wait, id)
	c.mu.Unlock()
}

func (c *Conn) await(ctx context.Context, ch chan []byte, what string) error {
	var ignored []byte
	return c.awaitBody(ctx, ch, what, &ignored)
}

func (c *Conn) awaitBody(ctx context.Context, ch chan []byte, what string, out *[]byte) error {
	t := time.NewTimer(c.cfg.Timeout)
	defer t.Stop()
	select {
	case b := <-ch:
		*out = b
		return nil
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		err := fmt.Errorf("mqtt: %s within %s", what, c.cfg.Timeout)
		c.fail(err)
		return err
	}
}

func (c *Conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.cfg.Timeout))
	_, err := c.nc.Write(b)
	if err != nil {
		err = fmt.Errorf("mqtt: writing to the broker: %w", err)
		c.fail(err)
	}
	return err
}

func (c *Conn) fail(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
		_ = c.nc.Close()
	})
}

func (c *Conn) pingLoop() {
	t := time.NewTicker(c.cfg.KeepAlive / 2)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			_ = c.write([]byte{typePingreq << 4, 0})
		}
	}
}

func (c *Conn) readLoop(br *bufio.Reader) {
	defer close(c.messages)
	for {
		// The broker must send something within one and a half keep-alive periods (it answers our pings).
		_ = c.nc.SetReadDeadline(time.Now().Add(c.cfg.KeepAlive * 3 / 2))
		first, body, err := readPacket(br, c.cfg.MaxMessage+1024)
		if err != nil {
			c.fail(fmt.Errorf("mqtt: reading from the broker: %w", err))
			return
		}
		switch first >> 4 {
		case typePublish:
			m, err := parsePublish(first, body)
			if err != nil {
				c.fail(err)
				return
			}
			m.conn = c
			select {
			case c.messages <- m:
			case <-c.done:
				return
			}
		case typePuback, typeSuback:
			if len(body) < 2 {
				c.fail(errors.New("mqtt: a short acknowledgement from the broker"))
				return
			}
			id := binary.BigEndian.Uint16(body)
			c.mu.Lock()
			ch := c.wait[id]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- body[2:]:
				default:
				}
			}
		case typePingresp:
		default:
			c.fail(fmt.Errorf("mqtt: unexpected packet type %d from the broker", first>>4))
			return
		}
	}
}

func parsePublish(first byte, body []byte) (*Message, error) {
	qos := (first >> 1) & 3
	if qos > 1 {
		return nil, errors.New("mqtt: the broker sent a QoS 2 message, which was not asked for")
	}
	topic, rest, err := readString(body)
	if err != nil {
		return nil, err
	}
	m := &Message{Topic: topic, QoS: qos, Dup: first&0x08 != 0}
	if qos > 0 {
		if len(rest) < 2 {
			return nil, errors.New("mqtt: a publish without its packet id")
		}
		m.id = binary.BigEndian.Uint16(rest)
		rest = rest[2:]
	}
	m.Payload = rest
	return m, nil
}

// packet frames a body with its first byte and remaining length.
func packet(first byte, body []byte) []byte {
	out := []byte{first}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func readPacket(r *bufio.Reader, limit int) (byte, []byte, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		if i == 4 {
			return 0, nil, errors.New("mqtt: a remaining length longer than four bytes")
		}
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(b&0x7f) * mult
		mult *= 128
		if b&0x80 == 0 {
			break
		}
	}
	if n > limit || n > maxRemainingLen {
		return 0, nil, fmt.Errorf("mqtt: a packet of %d bytes is larger than allowed", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return first, body, nil
}

func appendString(b []byte, s string) []byte {
	return append(append(b, byte(len(s)>>8), byte(len(s))), s...)
}

func readString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, errors.New("mqtt: a short string")
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return "", nil, errors.New("mqtt: a string longer than its packet")
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}

// validTopic checks a topic name, or with filter a topic filter (which may hold + and #).
func validTopic(t string, filter bool) error {
	if t == "" || len(t) > 65535 {
		return errors.New("mqtt: a topic is 1 to 65535 bytes")
	}
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case 0:
			return errors.New("mqtt: a topic cannot hold a NUL")
		case '+', '#':
			if !filter {
				return fmt.Errorf("mqtt: %q: + and # are wildcards, allowed in a subscription and not in a topic published to", t)
			}
			if (i > 0 && t[i-1] != '/') || (i+1 < len(t) && t[i+1] != '/') || (t[i] == '#' && i != len(t)-1) {
				return fmt.Errorf("mqtt: %q: a wildcard takes a whole level, and # only the last", t)
			}
		}
	}
	return nil
}
