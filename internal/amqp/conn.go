// Package amqp is an AMQP 1.0 client: one connection, one session, sending and receiving links, settled after the receiver says so.
//
// AMQP 1.0 is what Azure Service Bus and Event Hubs speak, what RabbitMQ 4 speaks natively, and what ActiveMQ and Artemis speak beside
// STOMP. Written here for the reason the STOMP client and the AWS signer are: a full client library would be most of the binary's
// dependency list for four performatives that matter.
//
// What it does: SASL PLAIN or ANONYMOUS (Service Bus takes a shared access key name and key as PLAIN), TLS, open/begin/attach, credit-based
// flow, a transfer split into frames when it exceeds the peer's maximum frame size, and dispositions - accepted, released, rejected. The
// sender waits for the broker's disposition before reporting success, so "sent" means the broker took custody. Empty frames keep the
// connection alive inside the peer's idle timeout, which Service Bus enforces.
//
// What it does not: transactions, multiple sessions, link recovery and message annotations beyond reading them.
package amqp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Performative and section codes.
const (
	codeOpen        = 0x10
	codeBegin       = 0x11
	codeAttach      = 0x12
	codeFlow        = 0x13
	codeTransfer    = 0x14
	codeDisposition = 0x15
	codeDetach      = 0x16
	codeEnd         = 0x17
	codeClose       = 0x18
	codeError       = 0x1d

	codeSASLMechanisms = 0x40
	codeSASLInit       = 0x41
	codeSASLOutcome    = 0x44

	codeAccepted = 0x24
	codeRejected = 0x25
	codeReleased = 0x26
	codeModified = 0x27

	codeSource = 0x28
	codeTarget = 0x29

	codeHeader     = 0x70
	codeProperties = 0x73
	codeAppProps   = 0x74
	codeData       = 0x75
	codeValue      = 0x77
)

// Config says where and as whom to connect.
type Config struct {
	// Addr is host:port. 5672 plain, 5671 TLS by convention.
	Addr string
	// TLS, when set, wraps the connection. Service Bus and Event Hubs require it.
	TLS *tls.Config
	// Username and Password authenticate with SASL PLAIN. Both empty means SASL ANONYMOUS. For Service Bus they are the shared access
	// policy name and key.
	Username, Password string
	// Hostname is sent in open and sasl-init; Service Bus routes on it. Defaults to the host of Addr.
	Hostname string
	// ContainerID names this client to the broker. Defaults to a random id.
	ContainerID string
	// Timeout bounds connecting and each operation that waits for the broker. Defaults to 30s.
	Timeout time.Duration
}

// Conn is a connection with one session.
type Conn struct {
	nc       net.Conn
	r        *bufio.Reader
	wmu      sync.Mutex
	maxFrame uint32
	timeout  time.Duration

	mu    sync.Mutex
	links map[uint32]*link
	// remote maps the broker's handle for a link to ours. Each side numbers its own handles, so the two can differ - ActiveMQ's do
	// and RabbitMQ's happen not to - and every frame the broker sends after attach names the link by its handle, not ours.
	remote     map[uint32]*link
	byName     map[string]*link
	nextHandle uint32
	nextOut    uint32 // the session's next outgoing transfer id
	closed     error
	done       chan struct{}
}

// Error is an AMQP error condition from the broker.
type Error struct {
	Condition   string
	Description string
}

func (e *Error) Error() string {
	if e.Description != "" {
		return "amqp: " + e.Condition + ": " + e.Description
	}
	return "amqp: " + e.Condition
}

func errorFrom(v any) error {
	code, f := descriptorCode(v)
	if code != codeError {
		return nil
	}
	cond, _ := field(f, 0).(Symbol)
	desc, _ := field(f, 1).(string)
	return &Error{Condition: string(cond), Description: desc}
}

// Dial connects, authenticates and begins a session.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("amqp: address %q is not host:port", cfg.Addr)
	}
	if cfg.Hostname == "" {
		cfg.Hostname = host
	}
	if cfg.ContainerID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		cfg.ContainerID = "perfuse-" + hex.EncodeToString(b)
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var nc net.Conn
	if cfg.TLS != nil {
		t := cfg.TLS.Clone()
		if t.ServerName == "" {
			t.ServerName = host
		}
		nc, err = (&tls.Dialer{Config: t}).DialContext(dctx, "tcp", cfg.Addr)
	} else {
		nc, err = (&net.Dialer{}).DialContext(dctx, "tcp", cfg.Addr)
	}
	if err != nil {
		return nil, err
	}
	c := &Conn{nc: nc, r: bufio.NewReader(nc), maxFrame: 512, timeout: cfg.Timeout, links: map[uint32]*link{},
		remote: map[uint32]*link{}, byName: map[string]*link{},
		done: make(chan struct{})}
	_ = nc.SetDeadline(time.Now().Add(cfg.Timeout))
	if err := c.handshake(cfg); err != nil {
		_ = nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	go c.readLoop()
	return c, nil
}

func (c *Conn) handshake(cfg Config) error {
	// SASL first, always: the brokers that matter require it, and ANONYMOUS covers the ones that do not.
	if err := c.protocolHeader(3); err != nil {
		return err
	}
	_, body, err := c.readFrame()
	if err != nil {
		return err
	}
	perf, _, _ := Decode(body)
	code, f := descriptorCode(perf)
	if code != codeSASLMechanisms {
		return fmt.Errorf("amqp: expected sasl-mechanisms, got 0x%02x", code)
	}
	offered := map[Symbol]bool{}
	switch m := field(f, 0).(type) {
	case Symbol:
		offered[m] = true
	case []any:
		for _, x := range m {
			if s, ok := x.(Symbol); ok {
				offered[s] = true
			}
		}
	}
	mech, resp := Symbol("ANONYMOUS"), []byte(nil)
	if cfg.Username != "" || cfg.Password != "" {
		mech, resp = "PLAIN", []byte("\x00"+cfg.Username+"\x00"+cfg.Password)
	}
	if !offered[mech] {
		return fmt.Errorf("amqp: the broker does not offer SASL %s", mech)
	}
	if err := c.writeFrame(1, 0, described(codeSASLInit, mech, resp, cfg.Hostname), nil); err != nil {
		return err
	}
	_, body, err = c.readFrame()
	if err != nil {
		return err
	}
	perf, _, _ = Decode(body)
	code, f = descriptorCode(perf)
	if code != codeSASLOutcome {
		return fmt.Errorf("amqp: expected sasl-outcome, got 0x%02x", code)
	}
	if asUint(field(f, 0)) != 0 {
		return errors.New("amqp: authentication was refused")
	}

	if err := c.protocolHeader(0); err != nil {
		return err
	}
	if err := c.writeFrame(0, 0, described(codeOpen, cfg.ContainerID, cfg.Hostname, UInt(1<<20), UShort(0), UInt(60000)), nil); err != nil {
		return err
	}
	if err := c.writeFrame(0, 0, described(codeBegin, nil, UInt(0), UInt(5000), UInt(5000)), nil); err != nil {
		return err
	}
	var gotOpen, gotBegin bool
	for !gotOpen || !gotBegin {
		_, body, err := c.readFrame()
		if err != nil {
			return err
		}
		if len(body) == 0 {
			continue
		}
		perf, _, err := Decode(body)
		if err != nil {
			return err
		}
		code, f := descriptorCode(perf)
		switch code {
		case codeOpen:
			gotOpen = true
			if mf := asUint(field(f, 2)); mf >= 512 {
				c.maxFrame = uint32(min(mf, 1<<20))
			} else {
				c.maxFrame = 1 << 20
			}
			if idle := asUint(field(f, 4)); idle > 0 {
				go c.keepAlive(time.Duration(idle) * time.Millisecond / 2)
			}
		case codeBegin:
			gotBegin = true
		case codeClose:
			if e := errorFrom(field(f, 0)); e != nil {
				return e
			}
			return errors.New("amqp: the broker closed the connection")
		}
	}
	return nil
}

func (c *Conn) protocolHeader(id byte) error {
	want := []byte{'A', 'M', 'Q', 'P', id, 1, 0, 0}
	if _, err := c.nc.Write(want); err != nil {
		return err
	}
	got := make([]byte, 8)
	if _, err := io.ReadFull(c.r, got); err != nil {
		return fmt.Errorf("amqp: no protocol header from the broker: %w", err)
	}
	if string(got[:4]) != "AMQP" || got[5] != 1 {
		return fmt.Errorf("amqp: the broker does not speak AMQP 1.0 (it answered %q)", got)
	}
	if got[4] != id {
		return fmt.Errorf("amqp: the broker wants protocol %d where %d was offered", got[4], id)
	}
	return nil
}

// debugFrames, when set by a test, sees every performative read and written.
var debugFrames func(dir string, v any)

func (c *Conn) readFrame() (byte, []byte, error) {
	head := make([]byte, 8)
	if _, err := io.ReadFull(c.r, head); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(head[0:4])
	doff := int(head[4]) * 4
	if size < 8 || doff < 8 || int(size) < doff || size > 16<<20 {
		return 0, nil, fmt.Errorf("amqp: a frame of %d bytes is malformed", size)
	}
	rest := make([]byte, size-8)
	if _, err := io.ReadFull(c.r, rest); err != nil {
		return 0, nil, err
	}
	if debugFrames != nil && len(rest[doff-8:]) > 0 {
		v, _, _ := Decode(rest[doff-8:])
		debugFrames("<-", v)
	}
	return head[5], rest[doff-8:], nil
}

func (c *Conn) writeFrame(typ byte, channel uint16, perf any, payload []byte) error {
	var body []byte
	if perf != nil {
		body = Encode(body, perf)
		if debugFrames != nil {
			debugFrames("->", perf)
		}
	}
	body = append(body, payload...)
	frame := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(frame[0:4], uint32(8+len(body)))
	frame[4], frame[5] = 2, typ
	binary.BigEndian.PutUint16(frame[6:8], channel)
	frame = append(frame, body...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.nc.Write(frame)
	return err
}

func (c *Conn) keepAlive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.writeFrame(0, 0, nil, nil); err != nil {
				return
			}
		}
	}
}

// Close ends the connection.
func (c *Conn) Close() error {
	_ = c.writeFrame(0, 0, described(codeClose), nil)
	err := c.nc.Close()
	c.fail(errors.New("amqp: the connection is closed"))
	return err
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed != nil {
		return
	}
	c.closed = err
	close(c.done)
	for _, l := range c.links {
		l.fail(err)
	}
}

// Err reports why the connection ended, or nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// readLoop dispatches frames to links until the connection ends.
func (c *Conn) readLoop() {
	var partial *delivery
	for {
		_, body, err := c.readFrame()
		if err != nil {
			c.fail(fmt.Errorf("amqp: the connection was lost: %w", err))
			return
		}
		if len(body) == 0 {
			continue // keepalive
		}
		perf, n, err := Decode(body)
		if err != nil {
			c.fail(err)
			return
		}
		code, f := descriptorCode(perf)
		switch code {
		case codeAttach, codeDetach, codeFlow:
			// An attach is matched by link name, and records the broker's handle for it; detach and flow then name that handle.
			var l *link
			c.mu.Lock()
			switch code {
			case codeAttach:
				name, _ := field(f, 0).(string)
				if l = c.byName[name]; l != nil {
					c.remote[uint32(asUint(field(f, 1)))] = l
				}
			case codeDetach:
				l = c.remote[uint32(asUint(field(f, 0)))]
			case codeFlow:
				if field(f, 4) != nil { // a session-level flow carries no link credit
					l = c.remote[uint32(asUint(field(f, 4)))]
				}
			}
			c.mu.Unlock()
			if l != nil {
				l.frames <- f
				if code == codeDetach {
					l.fail(orDefault(errorFrom(field(f, 2)), errors.New("amqp: the broker detached the link")))
				}
			}
		case codeTransfer:
			handle := uint32(asUint(field(f, 0)))
			if partial == nil {
				partial = &delivery{handle: handle, id: uint32(asUint(field(f, 1)))}
			}
			partial.payload = append(partial.payload, body[n:]...)
			if more, _ := field(f, 5).(bool); more {
				continue
			}
			d := partial
			partial = nil
			c.mu.Lock()
			l := c.remote[d.handle]
			c.mu.Unlock()
			if l != nil {
				l.deliveries <- d
			}
		case codeDisposition:
			first := uint32(asUint(field(f, 1)))
			last := first
			if v := field(f, 2); v != nil {
				last = uint32(asUint(v))
			}
			c.mu.Lock()
			for _, l := range c.links {
				l.settle(first, last, field(f, 4))
			}
			c.mu.Unlock()
		case codeEnd, codeClose:
			c.fail(orDefault(errorFrom(field(f, 0)), errors.New("amqp: the broker closed the connection")))
			_ = c.nc.Close()
			return
		}
	}
}

func orDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

type delivery struct {
	handle  uint32
	id      uint32
	payload []byte
}

type link struct {
	conn       *Conn
	handle     uint32
	frames     chan []any
	deliveries chan *delivery

	mu      sync.Mutex
	credit  uint32
	count   uint32 // delivery-count
	waiting map[uint32]chan any
	err     error
	wake    chan struct{}
}

func (l *link) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		l.err = err
		close(l.wake)
	}
}

func (l *link) settle(first, last uint32, state any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := first; ; id++ {
		if ch, ok := l.waiting[id]; ok {
			ch <- state
			delete(l.waiting, id)
		}
		if id == last {
			break
		}
	}
}

func (c *Conn) attach(ctx context.Context, address string, receiver bool) (*link, error) {
	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return nil, c.closed
	}
	h := c.nextHandle
	c.nextHandle++
	l := &link{conn: c, handle: h, frames: make(chan []any, 16), deliveries: make(chan *delivery, 64), waiting: map[uint32]chan any{},
		wake: make(chan struct{})}
	c.links[h] = l
	c.mu.Unlock()

	name := fmt.Sprintf("perfuse-%s-%d-%d", address, h, time.Now().UnixNano())
	c.mu.Lock()
	c.byName[name] = l
	c.mu.Unlock()
	source := described(codeSource, address)
	target := described(codeTarget, address)
	if receiver {
		target = described(codeTarget)
	} else {
		source = described(codeSource)
	}
	// Unsettled on both sides: the sender wants the broker's disposition, and the receiver settles only after the channel has handled
	// the message.
	err := c.writeFrame(0, 0, described(codeAttach, name, UInt(h), receiver, UByte(0), UByte(0), source, target, nil, nil,
		map[bool]any{true: nil, false: UInt(0)}[receiver]), nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case f := <-l.frames:
		// A broker refusing an address attaches with a null source or target and detaches immediately after.
		if (receiver && field(f, 5) == nil) || (!receiver && field(f, 6) == nil) {
			select {
			case <-l.wake:
				return nil, l.err
			case <-time.After(2 * time.Second):
				return nil, fmt.Errorf("amqp: the broker refused the address %q", address)
			}
		}
		if receiver {
			// The sender's initial delivery count, from which credit is reckoned.
			l.count = uint32(asUint(field(f, 9)))
		}
		return l, nil
	case <-l.wake:
		return nil, l.err
	case <-ctx.Done():
		return nil, fmt.Errorf("amqp: no answer to attaching %q: %w", address, ctx.Err())
	}
}

// Sender sends to one address.
type Sender struct{ l *link }

// NewSender attaches a sending link.
func (c *Conn) NewSender(ctx context.Context, address string) (*Sender, error) {
	l, err := c.attach(ctx, address, false)
	if err != nil {
		return nil, err
	}
	go l.watchFlow()
	return &Sender{l: l}, nil
}

// watchFlow takes credit from the broker's flow frames.
func (l *link) watchFlow() {
	for {
		select {
		case f := <-l.frames:
			if len(f) > 6 {
				// credit available = delivery-count(peer) + link-credit - our delivery-count
				peerCount, credit := uint32(asUint(field(f, 5))), uint32(asUint(field(f, 6)))
				l.mu.Lock()
				l.credit = peerCount + credit - l.count
				l.mu.Unlock()
				select {
				case l.wakeCredit() <- struct{}{}:
				default:
				}
			}
		case <-l.wake:
			return
		}
	}
}

var creditSignals sync.Map

func (l *link) wakeCredit() chan struct{} {
	ch, _ := creditSignals.LoadOrStore(l, make(chan struct{}, 1))
	return ch.(chan struct{})
}

// Message is what is sent and received: the body, and the properties that matter to an integration engine.
type Message struct {
	Body        []byte
	ContentType string
	MessageID   string
	Subject     string
	// Properties are the application properties.
	Properties map[string]any

	link *link
	id   uint32
}

func encodeMessage(m *Message) []byte {
	var out []byte
	out = Encode(out, described(codeHeader, true)) // durable
	props := []any{nil, nil, nil, nil, nil, nil, nil}
	if m.MessageID != "" {
		props[0] = m.MessageID
	}
	if m.Subject != "" {
		props[3] = m.Subject
	}
	if m.ContentType != "" {
		props[6] = Symbol(m.ContentType)
	}
	out = Encode(out, described(codeProperties, props...))
	if len(m.Properties) > 0 {
		ap := map[any]any{}
		for k, v := range m.Properties {
			ap[k] = v
		}
		out = Encode(out, Described{Descriptor: ULong(codeAppProps), Value: ap})
	}
	return Encode(out, Described{Descriptor: ULong(codeData), Value: m.Body})
}

func decodeMessage(payload []byte) (*Message, error) {
	m := &Message{Properties: map[string]any{}}
	for len(payload) > 0 {
		v, n, err := Decode(payload)
		if err != nil {
			return nil, fmt.Errorf("amqp: the message could not be read: %w", err)
		}
		payload = payload[n:]
		d, ok := v.(Described)
		if !ok {
			continue
		}
		switch asUint(d.Descriptor) {
		case codeProperties:
			f, _ := d.Value.([]any)
			switch id := field(f, 0).(type) {
			case string:
				m.MessageID = id
			case ULong:
				m.MessageID = fmt.Sprint(uint64(id))
			}
			m.Subject, _ = field(f, 3).(string)
			if ct, ok := field(f, 6).(Symbol); ok {
				m.ContentType = string(ct)
			}
		case codeAppProps:
			if mp, ok := d.Value.(map[any]any); ok {
				for k, val := range mp {
					if ks, ok := k.(string); ok {
						m.Properties[ks] = val
					}
				}
			}
		case codeData:
			b, _ := d.Value.([]byte)
			m.Body = append(m.Body, b...)
		case codeValue:
			// An amqp-value body, which is how many clients send a string.
			switch x := d.Value.(type) {
			case string:
				m.Body = []byte(x)
			case []byte:
				m.Body = x
			default:
				m.Body = []byte(fmt.Sprint(x))
			}
		}
	}
	return m, nil
}

// Send transfers a message and waits for the broker to accept it.
func (s *Sender) Send(ctx context.Context, m *Message) error {
	l, c := s.l, s.l.conn
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	for {
		l.mu.Lock()
		if l.err != nil {
			l.mu.Unlock()
			return l.err
		}
		if l.credit > 0 {
			l.credit--
			l.count++
			l.mu.Unlock()
			break
		}
		l.mu.Unlock()
		select {
		case <-l.wakeCredit():
		case <-l.wake:
		case <-ctx.Done():
			return errors.New("amqp: the broker gave no credit to send; the address may be full or the link blocked")
		}
	}

	c.mu.Lock()
	id := c.nextOut
	c.nextOut++
	c.mu.Unlock()
	result := make(chan any, 1)
	l.mu.Lock()
	l.waiting[id] = result
	l.mu.Unlock()

	payload := encodeMessage(m)
	tag := make([]byte, 8)
	binary.BigEndian.PutUint64(tag, uint64(id))
	room := int(c.maxFrame) - 128
	for first := true; first || len(payload) > 0; first = false {
		chunk := payload
		more := false
		if len(chunk) > room {
			chunk, more = payload[:room], true
		}
		payload = payload[len(chunk):]
		var perf any
		if first {
			// handle, delivery-id, delivery-tag, message-format, settled, more
			perf = described(codeTransfer, UInt(l.handle), UInt(id), tag, UInt(0), false, more)
		} else {
			perf = described(codeTransfer, UInt(l.handle), nil, nil, nil, nil, more)
		}
		if err := c.writeFrame(0, 0, perf, chunk); err != nil {
			return err
		}
	}

	select {
	case state := <-result:
		code, f := descriptorCode(state)
		switch code {
		case codeAccepted:
			return nil
		case codeRejected:
			return orDefault(errorFrom(field(f, 0)), errors.New("amqp: the broker rejected the message"))
		case codeReleased, codeModified:
			return errors.New("amqp: the broker released the message without taking it")
		}
		return fmt.Errorf("amqp: the broker settled the message with outcome 0x%02x", code)
	case <-l.wake:
		return l.err
	case <-ctx.Done():
		return errors.New("amqp: the broker did not confirm the message in time")
	}
}

// Close detaches the link.
func (s *Sender) Close() error {
	return s.l.conn.writeFrame(0, 0, described(codeDetach, UInt(s.l.handle), true), nil)
}

// Receiver receives from one address.
type Receiver struct {
	l      *link
	window uint32
	given  uint32
}

// NewReceiver attaches a receiving link and grants credit for window messages at a time.
func (c *Conn) NewReceiver(ctx context.Context, address string, window uint32) (*Receiver, error) {
	if window == 0 {
		window = 10
	}
	l, err := c.attach(ctx, address, true)
	if err != nil {
		return nil, err
	}
	r := &Receiver{l: l, window: window}
	go func() {
		for {
			select {
			case <-l.frames:
			case <-l.wake:
				return
			}
		}
	}()
	return r, r.grant(window)
}

func (r *Receiver) grant(n uint32) error {
	c := r.l.conn
	c.mu.Lock()
	next := c.nextOut
	c.mu.Unlock()
	r.l.mu.Lock()
	count := r.l.count
	r.l.mu.Unlock()
	r.given = n
	return c.writeFrame(0, 0, described(codeFlow, nil, UInt(5000), UInt(next), UInt(5000), UInt(r.l.handle), UInt(count), UInt(n)), nil)
}

// Receive waits for the next message. It must be settled with Accept, Release or Reject.
func (r *Receiver) Receive(ctx context.Context) (*Message, error) {
	select {
	case d := <-r.l.deliveries:
		r.l.mu.Lock()
		r.l.count++
		r.l.mu.Unlock()
		m, err := decodeMessage(d.payload)
		if err != nil {
			_ = r.settle(d.id, described(codeRejected))
			return nil, err
		}
		m.link, m.id = r.l, d.id
		r.given--
		if r.given <= r.window/2 {
			if err := r.grant(r.window); err != nil {
				return nil, err
			}
		}
		return m, nil
	case <-r.l.wake:
		return nil, r.l.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Receiver) settle(id uint32, state any) error {
	return r.l.conn.writeFrame(0, 0, described(codeDisposition, true, UInt(id), nil, true, state), nil)
}

// Accept tells the broker the message was handled; it is removed.
func (m *Message) Accept() error { return (&Receiver{l: m.link}).settle(m.id, described(codeAccepted)) }

// Release gives the message back for redelivery, counting it as a failed attempt.
func (m *Message) Release() error {
	return (&Receiver{l: m.link}).settle(m.id, described(codeModified, true))
}

// Reject refuses a message that can never be handled; the broker dead-letters it.
func (m *Message) Reject(reason string) error {
	return (&Receiver{l: m.link}).settle(m.id, described(codeRejected,
		described(codeError, Symbol("amqp:internal-error"), reason)))
}

// Close detaches the link.
func (r *Receiver) Close() error {
	return r.l.conn.writeFrame(0, 0, described(codeDetach, UInt(r.l.handle), true), nil)
}
