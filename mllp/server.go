package mllp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Handler processes one received message and returns the acknowledgement to
// send back.
//
// Returning an error means no acknowledgement could be produced at all, which
// is different from producing a negative one. A handler that wants to reject a
// message should return an AR acknowledgement and a nil error; returning an
// error leaves the sender with nothing, and it will retry.
type Handler interface {
	HandleMessage(ctx context.Context, msg []byte) (ack []byte, err error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, msg []byte) ([]byte, error)

// HandleMessage calls f.
func (f HandlerFunc) HandleMessage(ctx context.Context, msg []byte) ([]byte, error) {
	return f(ctx, msg)
}

// Server accepts MLLP connections and dispatches messages to a Handler.
//
// One goroutine per connection. In Go that is the cheap option rather than the
// expensive one: a goroutine blocked on a socket read costs a few kilobytes of
// stack and no CPU, and the runtime multiplexes them onto the platform's poller.
// Hand-rolling a poll loop here would be more code and slower.
type Server struct {
	// Addr is the listen address, for example ":6661".
	Addr string

	// Handler processes received messages. Required.
	Handler Handler

	// TLSConfig turns the listener into a TLS listener. Nil serves plain MLLP,
	// which remains the default because the overwhelming majority of hospital
	// interfaces are plain MLLP on a segregated network and refusing them would
	// make this unusable.
	TLSConfig *tls.Config

	// MaxMessageSize limits a single inbound message. Zero applies
	// DefaultMaxMessageSize.
	MaxMessageSize int

	// IdleTimeout closes a connection that sends nothing for this long. Zero
	// means never, which is usually what a hospital feed wants: the connection
	// is expected to stay open for months and go quiet overnight.
	IdleTimeout time.Duration

	// WriteTimeout bounds how long sending an acknowledgement may take. Zero
	// applies a default, because a peer that stops reading must not pin a
	// goroutine and its buffers for ever.
	WriteTimeout time.Duration

	// MaxConnections limits concurrent connections. Zero means unlimited.
	MaxConnections int

	// Logger receives connection and message events. Defaults to slog.Default.
	Logger *slog.Logger

	mu        sync.Mutex
	listener  net.Listener
	conns     map[*connState]struct{}
	closing   bool
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// connState tracks one connection and whether it is mid-message.
//
// Shutdown needs this distinction. An idle connection is blocked in a read and
// has to be closed to unblock it, whereas a connection handling a message must
// be left alone until it has sent its acknowledgement: cutting it loses the
// acknowledgement and makes the sender resend something already processed.
type connState struct {
	conn net.Conn
	busy atomic.Bool
}

// ErrNoHandler means the server was started without a Handler.
var ErrNoHandler = errors.New("mllp: server has no Handler")

const defaultWriteTimeout = 30 * time.Second

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// ListenAndServe listens on Addr and serves until Shutdown or Close is called.
func (s *Server) ListenAndServe() error {
	if s.Handler == nil {
		return ErrNoHandler
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	if s.TLSConfig != nil {
		// Wrapped here rather than in Serve, so a caller passing its own listener
		// keeps full control of it. The handshake happens per connection on first
		// read, which means a client presenting a bad certificate is reported as a
		// connection error rather than preventing the listener from starting.
		ln = tls.NewListener(ln, s.TLSConfig)
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln. It returns nil after a clean shutdown.
func (s *Server) Serve(ln net.Listener) error {
	if s.Handler == nil {
		return ErrNoHandler
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.listener = ln
	if s.conns == nil {
		s.conns = make(map[*connState]struct{})
	}
	if s.done == nil {
		s.done = make(chan struct{})
	}
	s.mu.Unlock()

	s.logger().Info("mllp listening", "addr", ln.Addr().String())

	var sem chan struct{}
	if s.MaxConnections > 0 {
		sem = make(chan struct{}, s.MaxConnections)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return nil
			}
			// A transient accept failure should not take the listener down.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}

		if sem != nil {
			select {
			case sem <- struct{}{}:
			default:
				// At the connection limit. Refusing is honest; accepting and
				// then stalling looks like a network fault to the peer.
				s.logger().Warn("mllp connection refused, at limit",
					"remote", conn.RemoteAddr().String(), "limit", s.MaxConnections)
				_ = conn.Close()
				continue
			}
		}

		cs := s.track(conn)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(cs)
			if sem != nil {
				defer func() { <-sem }()
			}
			s.serveConn(cs)
		}()
	}
}

func (s *Server) track(c net.Conn) *connState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = make(map[*connState]struct{})
	}
	cs := &connState{conn: c}
	s.conns[cs] = struct{}{}
	return cs
}

func (s *Server) untrack(cs *connState) {
	s.mu.Lock()
	delete(s.conns, cs)
	s.mu.Unlock()
	_ = cs.conn.Close()
}

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *Server) serveConn(cs *connState) {
	conn := cs.conn
	remote := conn.RemoteAddr().String()
	log := s.logger().With("remote", remote)
	log.Info("mllp connection opened")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel in-flight handling when the server shuts down.
	go func() {
		select {
		case <-s.doneChan():
			cancel()
		case <-ctx.Done():
		}
	}()

	r := NewReader(conn, s.MaxMessageSize)
	w := NewWriter(conn)

	writeTimeout := s.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}

	messages := 0
	for {
		// Stop accepting new work once shutdown has begun. An idle connection
		// is closed from beneath us instead, which unblocks the read below.
		if s.isClosing() {
			log.Info("mllp connection closing for shutdown", "messages", messages)
			return
		}

		if s.IdleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.IdleTimeout))
		}

		msg, err := r.ReadMessage()
		switch {
		case err == nil:
			// Fall through to handling.
		case errors.Is(err, io.EOF):
			log.Info("mllp connection closed by peer", "messages", messages,
				"discarded_bytes", r.Discarded())
			return
		case errors.Is(err, ErrTooLarge), errors.Is(err, ErrEmptyMessage):
			// The reader has resynchronised, so the connection is still good.
			log.Warn("mllp frame rejected", "err", err)
			continue
		default:
			log.Warn("mllp read failed", "err", err, "messages", messages)
			return
		}

		cs.busy.Store(true)
		ack, err := s.Handler.HandleMessage(ctx, msg)
		if err != nil {
			// No acknowledgement could be produced. Say so rather than sending
			// something invented, and let the sender retry.
			log.Error("mllp handler failed, no acknowledgement sent",
				"err", err, "bytes", len(msg))
			cs.busy.Store(false)
			continue
		}
		if len(ack) == 0 {
			log.Debug("mllp handler returned no acknowledgement", "bytes", len(msg))
			messages++
			cs.busy.Store(false)
			continue
		}

		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		err = w.WriteMessage(ack)
		cs.busy.Store(false)
		if err != nil {
			log.Error("mllp acknowledgement write failed", "err", err)
			return
		}
		messages++
	}
}

func (s *Server) doneChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

// Addrs returns the address the server is listening on, or an empty string if it
// is not listening. Useful in tests that bind port zero.
func (s *Server) Addrs() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Shutdown stops accepting connections and waits for in-flight messages to
// finish, or for ctx to be cancelled.
//
// Existing connections are left open until their current message completes,
// because cutting a connection mid-message loses the acknowledgement and makes
// the sender resend something that was already processed.
func (s *Server) Shutdown(ctx context.Context) error {
	s.beginClose()

	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		// Out of patience: drop the remaining connections.
		s.closeConns()
		s.wg.Wait()
		return ctx.Err()
	}
}

// Close stops the server immediately, dropping open connections.
func (s *Server) Close() error {
	s.beginClose()
	s.closeConns()
	s.wg.Wait()
	return nil
}

func (s *Server) beginClose() {
	s.mu.Lock()
	s.closing = true
	ln := s.listener
	if s.done == nil {
		s.done = make(chan struct{})
	}
	done := s.done
	s.mu.Unlock()

	s.closeOnce.Do(func() { close(done) })
	if ln != nil {
		_ = ln.Close()
	}
	// Idle connections are blocked in a read and will not notice the shutdown
	// on their own, so close them. Busy ones are left to finish.
	s.closeIdleConns()
}

func (s *Server) closeIdleConns() {
	s.mu.Lock()
	idle := make([]net.Conn, 0, len(s.conns))
	for cs := range s.conns {
		if !cs.busy.Load() {
			idle = append(idle, cs.conn)
		}
	}
	s.mu.Unlock()

	for _, c := range idle {
		_ = c.Close()
	}
}

func (s *Server) closeConns() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for cs := range s.conns {
		conns = append(conns, cs.conn)
	}
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
}

// Client sends messages over a single MLLP connection and reads the
// acknowledgement for each.
//
// MLLP has no correlation identifier in the framing, so a connection carries one
// message at a time: send, wait for the acknowledgement, send the next. Client
// is safe for concurrent use and serialises callers to preserve that.
type Client struct {
	// Addr is the remote address, for example "hospital.example:6661".
	Addr string

	// DialTimeout bounds connection establishment. Zero applies a default.
	DialTimeout time.Duration

	// Timeout bounds a send and its acknowledgement. Zero applies a default.
	Timeout time.Duration

	// MaxMessageSize limits an inbound acknowledgement.
	MaxMessageSize int

	// Dialer is used to establish connections. Zero value uses a net.Dialer,
	// which is the hook for TLS.
	Dial func(ctx context.Context, addr string) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
	r    *Reader
	w    *Writer
}

const (
	defaultDialTimeout = 10 * time.Second
	defaultTimeout     = 30 * time.Second
)

// Send transmits one message and returns the acknowledgement.
//
// The connection is established on first use and reused afterwards. If the
// connection has failed, Send reconnects once and retries, because the common
// cause is an idle connection reaped by something in between.
func (c *Client) Send(ctx context.Context, msg []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ack, err := c.send(ctx, msg)
	if err == nil {
		return ack, nil
	}
	if !isConnectionError(err) {
		return nil, err
	}

	c.reset()
	return c.send(ctx, msg)
}

func (c *Client) send(ctx context.Context, msg []byte) ([]byte, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}

	if err := c.w.WriteMessage(msg); err != nil {
		return nil, fmt.Errorf("mllp: sending to %s: %w", c.Addr, err)
	}

	ack, err := c.r.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("mllp: awaiting acknowledgement from %s: %w", c.Addr, err)
	}
	return ack, nil
}

func (c *Client) connect(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}

	dial := c.Dial
	if dial == nil {
		timeout := c.DialTimeout
		if timeout <= 0 {
			timeout = defaultDialTimeout
		}
		d := &net.Dialer{Timeout: timeout}
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr)
		}
	}

	conn, err := dial(ctx, c.Addr)
	if err != nil {
		return fmt.Errorf("mllp: connecting to %s: %w", c.Addr, err)
	}
	c.conn = conn
	c.r = NewReader(conn, c.MaxMessageSize)
	c.w = NewWriter(conn)
	return nil
}

func (c *Client) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = nil
	c.r = nil
	c.w = nil
}

// Close releases the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.r = nil
	c.w = nil
	return err
}

// isConnectionError reports whether an error is worth one reconnect attempt. A
// rejected or oversized message is not: resending it would fail the same way.
func isConnectionError(err error) bool {
	switch {
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrEmptyMessage):
		return false
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, net.ErrClosed), errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.ECONNRESET):
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return !ne.Timeout()
	}
	return false
}
