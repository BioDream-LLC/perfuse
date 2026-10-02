package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/framing"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// TCPSender writes messages to a socket with configurable framing.
//
// Mirth's TCP Sender. The counterpart to the TCP source, and the two share their framing code so that Perfuse sending to
// itself round-trips - which is not a trick, it is the only cheap way to be sure the two halves agree.
type TCPSender struct {
	cfg config.TCPDest
	fr  framing.Settings

	mu   sync.Mutex
	conn net.Conn
}

// NewTCPSender prepares a sender. It does not connect.
//
// Deliberately not connecting here. A destination that dialled at construction would stop a whole channel from starting
// because one device was switched off overnight, and the queue exists precisely so that does not happen.
func NewTCPSender(cfg config.TCPDest) (*TCPSender, error) {
	cfg.ApplyDefaults()

	fr, err := cfg.Settings(cfg.MaxMessageSize)
	if err != nil {
		return nil, err
	}
	return &TCPSender{cfg: cfg, fr: fr}, nil
}

// Describe names the destination for logs and the interface.
func (s *TCPSender) Describe() string {
	return fmt.Sprintf("tcp %s (%s)", s.cfg.Address, s.fr.Describe())
}

// Send delivers one message.
func (s *TCPSender) Send(ctx context.Context, msg []byte) error {
	body, err := framing.Frame(msg, s.fr)
	if err != nil {
		// A framing failure is permanent for this message: it is too large for its own header, or too long for a fixed
		// record. Retrying would fail identically forever, so it is reported rather than queued.
		return fmt.Errorf("this message cannot be framed for %s, so it has not been sent: %w", s.cfg.Address, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.KeepAlive {
		if err := s.sendKeepAlive(ctx, body); err != nil {
			return err
		}
		return nil
	}

	conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	return s.write(ctx, conn, body)
}

// sendKeepAlive writes on the held connection, redialling once if it has gone.
//
// One retry, not a loop. A held connection that a firewall closed silently fails on first write and succeeds on the
// second, which is worth handling. A connection that fails twice is a real fault, and retrying further here would
// duplicate what the channel's queue already does with backoff.
func (s *TCPSender) sendKeepAlive(ctx context.Context, body []byte) error {
	if s.conn != nil {
		if err := s.write(ctx, s.conn, body); err == nil {
			return nil
		}
		_ = s.conn.Close()
		s.conn = nil
	}

	conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	s.conn = conn

	if err := s.write(ctx, conn, body); err != nil {
		_ = conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

func (s *TCPSender) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: s.cfg.Timeout}

	if s.cfg.TLS.IsEnabled() {
		tlsCfg, err := tlsconf.ForSender(s.cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("tls settings for %s: %w", s.cfg.Address, err)
		}
		conn, err := tls.DialWithDialer(d, "tcp", s.cfg.Address, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", s.cfg.Address, err)
		}
		return conn, nil
	}

	conn, err := d.DialContext(ctx, "tcp", s.cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", s.cfg.Address, err)
	}
	return conn, nil
}

// write sends the framed bytes and optionally waits for a reply.
func (s *TCPSender) write(ctx context.Context, conn net.Conn, body []byte) error {
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	if _, err := conn.Write(body); err != nil {
		return fmt.Errorf("writing to %s: %w", s.cfg.Address, err)
	}

	// With whole-stream framing the close *is* the end of the message, so the peer will not process anything until the
	// write side is shut down. Without this the far end waits for more bytes and the message is never delivered.
	if s.fr.Mode == framing.ModeWhole {
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				return fmt.Errorf("closing the write side of %s, which is what marks the end of the message "+
					"with whole-stream framing: %w", s.cfg.Address, err)
			}
		}
	}

	if !s.cfg.ExpectReply {
		// Worth being clear about what success means here: the bytes reached the operating system's send buffer. A peer
		// that crashed a moment later never read them. expect_reply is how a caller asks for more than that.
		return nil
	}

	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("waiting for a reply from %s: %w", s.cfg.Address, err)
	}
	if n == 0 {
		return fmt.Errorf("%s closed the connection without replying, and expect_reply is set", s.cfg.Address)
	}
	return nil
}

// Close releases a held connection.
func (s *TCPSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}
