package stomp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Conn is a STOMP session with a broker.
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader

	// writeMu serialises writes. Frames must not interleave, and a send from a destination can coincide with an
	// acknowledgement from a source on the same connection.
	writeMu sync.Mutex

	// Version is the protocol version the broker agreed to.
	Version string

	// Server is what the broker calls itself, for the log.
	Server string

	heartbeatSend time.Duration
	closeOnce     sync.Once
	closed        chan struct{}

	// pending holds messages that arrived while waiting for a receipt.
	//
	// Necessary because a subscription is confirmed by a receipt, and the broker may deliver a message between registering the
	// subscription and that receipt being read. Discarding it would lose a message; erroring would make subscribing fail on a
	// busy queue.
	pending []*Frame
}

// Options describe how to connect.
type Options struct {
	// Addr is the broker's host and port. 61613 is the usual STOMP port.
	Addr string

	// Host is the virtual host. Defaults to the address's host, which is what brokers expect when there is only one.
	Host string

	// Login and Passcode authenticate. Both empty connects anonymously, which most brokers refuse by default.
	Login    string
	Passcode string

	// Timeout bounds connecting and each read. Defaults to thirty seconds.
	Timeout time.Duration

	// Heartbeat asks the broker to expect and send activity at this interval. Zero disables it.
	//
	// Worth enabling on a long-lived subscription. Without it a connection through a firewall that has silently dropped the
	// route looks perfectly healthy from this end, and messages stop arriving with nothing logged.
	Heartbeat time.Duration

	// TLS wraps the connection.
	TLS *tls.Config
}

// Dial opens a session.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("stomp: could not reach the broker at %s: %w", opts.Addr, err)
	}

	if opts.TLS != nil {
		tlsConn := tls.Client(raw, opts.TLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("stomp: the TLS handshake with %s failed: %w", opts.Addr, err)
		}
		raw = tlsConn
	}

	c := &Conn{conn: raw, reader: bufio.NewReaderSize(raw, 64<<10), closed: make(chan struct{})}

	host := opts.Host
	if host == "" {
		if h, _, splitErr := net.SplitHostPort(opts.Addr); splitErr == nil {
			host = h
		} else {
			host = opts.Addr
		}
	}

	headers := map[string]string{
		// Both offered, so a broker that only speaks 1.1 still works. 1.2 is preferred because it is the version that
		// specifies header escaping, and without that a value containing a colon is ambiguous.
		"accept-version": "1.1,1.2",
		"host":           host,
	}
	if opts.Login != "" {
		headers["login"] = opts.Login
		headers["passcode"] = opts.Passcode
	}
	if opts.Heartbeat > 0 {
		ms := strconv.FormatInt(opts.Heartbeat.Milliseconds(), 10)
		headers["heart-beat"] = ms + "," + ms
	} else {
		headers["heart-beat"] = "0,0"
	}

	// STOMP rather than CONNECT. The two are the same frame, but a broker answering STOMP is required to reply with an ERROR
	// frame rather than closing the socket when it refuses - so a wrong password produces a message instead of a bare
	// connection reset.
	if err := c.write(Frame{Command: CmdStomp, Headers: headers}); err != nil {
		_ = raw.Close()
		return nil, err
	}

	_ = raw.SetReadDeadline(time.Now().Add(timeout))
	reply, err := ReadFrame(c.reader)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("stomp: the broker did not answer the connection attempt: %w", err)
	}
	_ = raw.SetReadDeadline(time.Time{})

	if reply.Command == CmdError {
		_ = raw.Close()
		return nil, fmt.Errorf("%w: %s", ErrBrokerError, ErrorMessage(reply))
	}
	if reply.Command != CmdConnected {
		_ = raw.Close()
		return nil, fmt.Errorf("stomp: expected CONNECTED and the broker sent %s", reply.Command)
	}

	c.Version = reply.Header("version")
	c.Server = reply.Header("server")
	c.heartbeatSend = negotiatedHeartbeat(reply.Header("heart-beat"), opts.Heartbeat)

	if c.heartbeatSend > 0 {
		go c.sendHeartbeats()
	}

	return c, nil
}

// negotiatedHeartbeat works out how often we must send.
//
// The broker's reply is "what I will send, what I expect", so our sending interval comes from the second number - and the
// specification says to use the larger of the two sides' figures, because a broker asking for more often than we offered would
// otherwise disconnect us for being late.
func negotiatedHeartbeat(header string, offered time.Duration) time.Duration {
	if header == "" || offered <= 0 {
		return 0
	}

	parts := strings.Split(header, ",")
	if len(parts) != 2 {
		return 0
	}

	wanted, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || wanted <= 0 {
		return 0
	}

	interval := time.Duration(wanted) * time.Millisecond
	if offered > interval {
		interval = offered
	}

	// Sent at two thirds of the agreed interval. Sending exactly on it means any scheduling delay is a missed heartbeat, and a
	// missed heartbeat is a disconnection.
	return interval * 2 / 3
}

// sendHeartbeats keeps the connection alive.
func (c *Conn) sendHeartbeats() {
	ticker := time.NewTicker(c.heartbeatSend)
	defer ticker.Stop()

	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			c.writeMu.Lock()
			// A bare newline is the heartbeat. Written directly rather than as a frame, because it is not one.
			_, err := c.conn.Write([]byte{'\n'})
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// write sends one frame, serialised against other writers.
func (c *Conn) write(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(c.conn, f)
}

// Send publishes a message to a destination.
//
// A receipt is requested and waited for, so that returning nil means the broker has the message rather than that the bytes left
// this machine. Without it a broker that is refusing messages - a full queue, a missing address, no permission - looks like a
// working destination, and the queue behind it drains into nothing.
func (c *Conn) Send(destination string, body []byte, headers map[string]string, timeout time.Duration) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("stomp: no destination was given")
	}

	receipt := "r-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	out := map[string]string{
		"destination": destination,
		"receipt":     receipt,
		// Persistent by default. A non-persistent message is lost when the broker restarts, and that is not a reasonable
		// default for clinical data - the caller can turn it off deliberately.
		"persistent": "true",
	}
	for name, value := range headers {
		out[name] = value
	}

	if err := c.write(Frame{Command: CmdSend, Headers: out, Body: body}); err != nil {
		return err
	}

	return c.awaitReceipt(receipt, timeout)
}

// awaitReceipt waits for the broker to confirm a frame.
func (c *Conn) awaitReceipt(receipt string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	deadline := time.Now().Add(timeout)
	_ = c.conn.SetReadDeadline(deadline)
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	for {
		f, err := ReadFrame(c.reader)
		if err != nil {
			return fmt.Errorf("stomp: waiting for confirmation from the broker: %w", err)
		}

		switch f.Command {
		case CmdReceipt:
			if f.Header("receipt-id") == receipt {
				return nil
			}
			// A receipt for something else. Ignored rather than treated as ours, because pairing them wrongly would report a
			// message as delivered on the strength of a different one.
		case CmdError:
			return fmt.Errorf("%w: %s", ErrBrokerError, ErrorMessage(f))
		case CmdMessage:
			// Held rather than dropped or refused. A message can legitimately arrive between a subscription being registered
			// and its receipt being read, and either discarding it or failing would turn a busy queue into a broken one.
			c.pending = append(c.pending, f)
		}

		if time.Now().After(deadline) {
			return errors.New("stomp: the broker did not confirm the message in time")
		}
	}
}

// Subscribe registers interest in a destination and waits for the broker to confirm it.
//
// Waiting matters. Without a receipt this returned as soon as the frame was written, so a caller that subscribed and then expected
// messages published immediately afterwards could miss them - the broker had not registered the subscription yet. Found by three
// tests failing where an identical one passed, the difference being a few hundred milliseconds.
//
// In production that is the shape of "we lose messages when the channel restarts", which is close to unfindable from a log.
//
// Acknowledgement is per message rather than automatic. Automatic means the broker considers a message delivered the moment it is
// written to the socket, so anything that fails afterwards - a parse error, a destination being down, this process stopping - loses
// it silently. That is the wrong trade for clinical data even though it is faster.
func (c *Conn) Subscribe(id, destination, selector string) error {
	receipt := "s-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	headers := map[string]string{
		"id":          id,
		"destination": destination,
		"ack":         "client-individual",
		"receipt":     receipt,
	}
	if strings.TrimSpace(selector) != "" {
		// A JMS selector, which brokers implement to varying degrees. Passed through rather than interpreted, because
		// reimplementing the grammar here would mean supporting less than the broker does while appearing to support it.
		headers["selector"] = selector
	}

	if err := c.write(Frame{Command: CmdSubscribe, Headers: headers}); err != nil {
		return err
	}

	return c.awaitReceipt(receipt, 30*time.Second)
}

// Receive waits for the next message.
//
// Returns nil with no error when the deadline passes with nothing arriving, so a caller can poll and check for shutdown between
// attempts rather than blocking forever on a quiet queue.
func (c *Conn) Receive(timeout time.Duration) (*Frame, error) {
	// Anything held while waiting for a receipt comes out first, in arrival order.
	if len(c.pending) > 0 {
		f := c.pending[0]
		c.pending = c.pending[1:]
		return f, nil
	}

	if timeout > 0 {
		_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
		defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	}

	for {
		f, err := ReadFrame(c.reader)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil, nil
			}
			return nil, err
		}

		switch f.Command {
		case CmdMessage:
			return f, nil
		case CmdError:
			return nil, fmt.Errorf("%w: %s", ErrBrokerError, ErrorMessage(f))
		case CmdReceipt:
			// Left over from a send on this connection. Ignored.
		}
	}
}

// Ack confirms a message was handled.
func (c *Conn) Ack(f *Frame) error {
	id := ackID(f)
	if id == "" {
		return errors.New("stomp: the message carries no acknowledgement identifier")
	}
	return c.write(Frame{Command: CmdAck, Headers: map[string]string{"id": id}})
}

// Nack reports that a message was not handled.
//
// Sent rather than staying silent, because a broker holding an unacknowledged message will redeliver it after a timeout that is
// frequently minutes long - and a message that is going to fail again should fail again now, where the retry and the dead letter
// handling are the broker's own and already configured.
func (c *Conn) Nack(f *Frame) error {
	id := ackID(f)
	if id == "" {
		return errors.New("stomp: the message carries no acknowledgement identifier")
	}
	return c.write(Frame{Command: CmdNack, Headers: map[string]string{"id": id}})
}

// ackID finds the identifier to acknowledge with.
//
// STOMP 1.2 uses the ack header and 1.1 uses message-id, and a client that reads only one of them silently fails to acknowledge
// against half the brokers in use - which presents as every message being redelivered forever.
func ackID(f *Frame) string {
	if f == nil {
		return ""
	}
	if id := f.Header("ack"); id != "" {
		return id
	}
	return f.Header("message-id")
}

// Close ends the session politely.
func (c *Conn) Close() error {
	var err error

	c.closeOnce.Do(func() {
		close(c.closed)

		// DISCONNECT before closing, with a short deadline. A broker that is told goodbye can release the subscription
		// immediately; one that finds the socket gone waits for a timeout and logs a failure against this client.
		_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = c.write(Frame{Command: CmdDisconnect})

		err = c.conn.Close()
	})

	return err
}
