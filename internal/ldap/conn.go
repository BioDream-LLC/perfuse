package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Options describe how to reach a directory.
type Options struct {
	// Addr is the directory's host and port. 389 for plain or StartTLS, 636 for LDAPS.
	Addr string

	// TLS is used for LDAPS, where the connection is encrypted from the first byte.
	TLS *tls.Config

	// StartTLS upgrades a plain connection before anything else is sent.
	//
	// Preferred over LDAPS on 389 by most directories, and either is acceptable. What is not acceptable is neither,
	// which is why Dial refuses that without an explicit override.
	StartTLS bool

	// Insecure allows an unencrypted connection.
	//
	// Named for what it is. A simple bind puts the password on the wire in cleartext, so without TLS anybody who can
	// see the traffic between here and the directory learns the password of every person who signs in - including
	// whichever service account this engine uses, which usually has read access to the whole directory.
	Insecure bool

	// Timeout bounds connecting and each exchange.
	Timeout time.Duration
}

// DefaultTimeout is used when Options.Timeout is zero.
const DefaultTimeout = 10 * time.Second

// maxMessageSize bounds one LDAP message.
//
// A directory entry is a few kilobytes; a megabyte is already generous for one carrying certificates or a photograph.
// The bound exists because this reads from a socket before anybody has authenticated, so a server that is not the
// directory - or a directory that has been replaced - could otherwise announce a four-gigabyte message and have it
// allocated.
const maxMessageSize = 1 << 20

// Conn is a connection to a directory.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	// nextID hands out message IDs. Atomic because Search and Bind may be called from different goroutines on a
	// pooled connection, and a repeated ID makes a response match the wrong request.
	nextID atomic.Int64

	timeout time.Duration

	// encrypted records whether this connection is protected, for the audit line.
	encrypted bool

	mu     sync.Mutex
	closed bool
}

// Dial opens a connection to a directory.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	if opts.Addr == "" {
		return nil, errors.New("an LDAP connection needs an address")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	// Refused before the socket is opened. An unencrypted simple bind is not a weaker configuration, it is a
	// configuration that gives away every password that passes through it, and it must be chosen deliberately.
	if opts.TLS == nil && !opts.StartTLS && !opts.Insecure {
		return nil, errors.New("this LDAP connection would send passwords in cleartext; " +
			"use TLS or StartTLS, or set insecure explicitly to accept that")
	}

	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("the directory at %s could not be reached: %w", opts.Addr, err)
	}

	c := &Conn{conn: raw, timeout: timeout}

	// LDAPS: wrapped immediately, before a single LDAP byte is written.
	if opts.TLS != nil && !opts.StartTLS {
		tlsConn := tls.Client(raw, opts.TLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("the TLS handshake with %s failed: %w", opts.Addr, err)
		}
		c.conn = tlsConn
		c.encrypted = true
	}

	c.br = bufio.NewReader(c.conn)
	c.nextID.Store(1)

	if opts.StartTLS {
		if err := c.startTLS(opts.TLS, opts.Addr); err != nil {
			_ = c.conn.Close()
			return nil, err
		}
	}

	return c, nil
}

// Encrypted reports whether the connection is protected.
func (c *Conn) Encrypted() bool { return c.encrypted }

// startTLS upgrades the connection in place.
func (c *Conn) startTLS(cfg *tls.Config, addr string) error {
	id := c.nextID.Add(1)

	if err := c.write(buildStartTLSRequest(id)); err != nil {
		return fmt.Errorf("the StartTLS request could not be sent: %w", err)
	}

	msg, err := c.read()
	if err != nil {
		return fmt.Errorf("the directory did not answer the StartTLS request: %w", err)
	}

	body, err := msg.child(1)
	if err != nil {
		return err
	}
	if err := parseResult(body); err != nil {
		// Named plainly, because the fallback somebody will reach for is turning encryption off - and this is the
		// moment to say that the alternative is sending passwords in the clear.
		return fmt.Errorf("the directory refused to start TLS, and continuing would send passwords in cleartext: %w", err)
	}

	if cfg == nil {
		// A hostname is required. Without it the certificate is accepted whatever name it carries, which means a
		// server that answered on this address rather than the directory passes verification.
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			host = addr
		}
		cfg = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}

	tlsConn := tls.Client(c.conn, cfg)
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("the TLS handshake after StartTLS failed: %w", err)
	}

	c.conn = tlsConn
	c.br = bufio.NewReader(tlsConn)
	c.encrypted = true

	return nil
}

// Bind authenticates as dn with a password.
//
// An empty password is refused here, before anything is sent, and this is the single most important line in the package.
//
// RFC 4513 is explicit: a simple bind with an empty password is an anonymous bind, and a directory answers it with
// success. So a sign-in form that passed an empty password straight through would authenticate as any account whose name
// somebody could type, without knowing any password at all. The directory is behaving correctly and the log shows a
// successful bind. Nothing looks wrong anywhere.
func (c *Conn) Bind(dn, password string) error {
	if dn == "" {
		return errors.New("an LDAP bind needs a distinguished name")
	}
	if password == "" {
		return errors.New("an LDAP bind with an empty password is an anonymous bind that the directory " +
			"answers with success, so it is refused here rather than mistaken for a valid sign-in")
	}

	id := c.nextID.Add(1)

	if err := c.write(buildBindRequest(id, dn, password)); err != nil {
		return err
	}

	msg, err := c.read()
	if err != nil {
		return err
	}

	body, err := msg.child(1)
	if err != nil {
		return err
	}
	if body.number() != appBindResponse {
		return fmt.Errorf("the directory answered a bind with message type %d", body.number())
	}

	return parseResult(body)
}

// BindAnonymous binds without credentials, for a directory that allows anonymous search.
//
// Separate from Bind so that an anonymous bind is always something the caller asked for by name. Reaching it by passing
// an empty password to Bind is exactly the confusion that makes the empty-password case dangerous.
func (c *Conn) BindAnonymous() error {
	id := c.nextID.Add(1)

	if err := c.write(buildBindRequest(id, "", "")); err != nil {
		return err
	}

	msg, err := c.read()
	if err != nil {
		return err
	}

	body, err := msg.child(1)
	if err != nil {
		return err
	}

	return parseResult(body)
}

// Search runs a search and returns every entry.
func (c *Conn) Search(req SearchRequest) ([]*Entry, error) {
	filter, err := parseFilter(req.Filter)
	if err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)

	if err := c.write(buildSearchRequest(id, req, filter)); err != nil {
		return nil, err
	}

	var out []*Entry

	for {
		msg, err := c.read()
		if err != nil {
			return nil, err
		}

		msgIDEl, err := msg.child(0)
		if err != nil {
			return nil, err
		}
		msgID, err := msgIDEl.integer()
		if err != nil {
			return nil, err
		}
		if msgID != id {
			// Refused rather than skipped. A response carrying a different message ID means this connection's
			// requests and responses have come apart, and continuing would attach one person's group list to
			// another person's sign-in.
			return nil, fmt.Errorf("the directory answered message %d while %d was outstanding", msgID, id)
		}

		body, err := msg.child(1)
		if err != nil {
			return nil, err
		}

		switch body.number() {
		case appSearchResEntry:
			entry, err := parseSearchEntry(body)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)

		case appSearchResDone:
			if err := parseResult(body); err != nil {
				// A size limit that this package set deliberately is not a failure - it is the answer.
				//
				// findPerson asks for two entries when it expects one, so that an ambiguous username is
				// visible rather than resolved by taking the first. A directory holding three matches
				// answers with two entries and sizeLimitExceeded, and treating that as an error threw the
				// entries away and reported "size limit exceeded" for what is really "this username
				// identifies several people". Found by adding a third person to the test directory.
				var ldapErr *Error
				if req.SizeLimit > 0 && errors.As(err, &ldapErr) && ldapErr.Code == resultSizeLimitExceeded {
					return out, nil
				}
				return nil, err
			}
			return out, nil

		default:
			// A search continuation reference, most likely. Ignored rather than followed: chasing a referral
			// means connecting somewhere the directory named, and that is a decision for configuration rather
			// than something to do automatically while checking a password.
			continue
		}
	}
}

// Close ends the connection, sending an unbind first.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// Best effort. The connection is going away regardless, and a directory that has already dropped it is not a
	// failure worth reporting to whoever was signing in.
	_ = c.write(buildUnbindRequest(c.nextID.Add(1)))

	return c.conn.Close()
}

// write sends one message.
func (c *Conn) write(b []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	if _, err := c.conn.Write(b); err != nil {
		return fmt.Errorf("the directory connection could not be written to: %w", err)
	}
	return nil
}

// read reads one LDAP message.
//
// The header is read a byte at a time to learn the length, then exactly that many bytes are read. Reading into a fixed
// buffer and hoping a message arrived whole is the bug that makes a decoder work in testing and fail under load, because
// TCP is free to split a message anywhere.
func (c *Conn) read() (*element, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, err
	}

	header := make([]byte, 0, 8)

	tag, err := c.br.ReadByte()
	if err != nil {
		return nil, readError(err)
	}
	header = append(header, tag)

	lenByte, err := c.br.ReadByte()
	if err != nil {
		return nil, readError(err)
	}
	header = append(header, lenByte)

	length := int(lenByte)
	if lenByte&0x80 != 0 {
		count := int(lenByte & 0x7f)
		if count == 0 || count > 4 {
			return nil, fmt.Errorf("the directory sent a message with an unusable length of %d bytes", count)
		}
		sizeBytes := make([]byte, count)
		if _, err := io.ReadFull(c.br, sizeBytes); err != nil {
			return nil, readError(err)
		}
		header = append(header, sizeBytes...)

		length = 0
		for _, b := range sizeBytes {
			length = length<<8 | int(b)
		}
	}

	if length > maxMessageSize {
		return nil, fmt.Errorf("the directory sent a %d byte message, which is larger than the %d byte limit",
			length, maxMessageSize)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(c.br, body); err != nil {
		return nil, readError(err)
	}

	full := append(header, body...)

	msg, _, err := parseElement(full, 0)
	if err != nil {
		return nil, err
	}

	return msg, nil
}

// readError makes a read failure readable.
func readError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("the directory closed the connection part way through a message: %w", err)
	}
	return fmt.Errorf("the directory connection could not be read: %w", err)
}
