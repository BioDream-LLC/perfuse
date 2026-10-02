package mllp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quietLogger keeps test output readable without disabling the logging paths.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServer runs a server on an ephemeral port and returns its address.
func startServer(t *testing.T, s *Server) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.Logger == nil {
		s.Logger = quietLogger()
	}

	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	return ln.Addr().String()
}

func echoAck(ack string) Handler {
	return HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
		return []byte(ack), nil
	})
}

func TestServerClientRoundTrip(t *testing.T) {
	const ack = "MSH|^~\\&|B|D|A|C|20260818||ACK|2|P|2.5.1\rMSA|AA|1\r"

	var got [][]byte
	var mu sync.Mutex
	srv := &Server{Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
		mu.Lock()
		got = append(got, bytes.Clone(msg))
		mu.Unlock()
		return []byte(ack), nil
	})}
	addr := startServer(t, srv)

	c := &Client{Addr: addr, Timeout: 2 * time.Second}
	defer c.Close()

	for i := 0; i < 3; i++ {
		reply, err := c.Send(context.Background(), []byte(sampleMsg))
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if string(reply) != ack {
			t.Errorf("ack %d = %q, want %q", i, reply, ack)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("handler saw %d messages, want 3", len(got))
	}
	for i, m := range got {
		if string(m) != sampleMsg {
			t.Errorf("message %d = %q, want %q", i, m, sampleMsg)
		}
	}
}

func TestServerReusesOneConnection(t *testing.T) {
	var conns int64
	srv := &Server{Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
		return []byte("MSH|1\rMSA|AA|1\r"), nil
	})}
	// Count connections by wrapping the listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Logger = quietLogger()
	counted := &countingListener{Listener: ln, count: &conns}

	go func() { _ = srv.Serve(counted) }()
	t.Cleanup(func() { _ = srv.Close() })

	c := &Client{Addr: ln.Addr().String(), Timeout: 2 * time.Second}
	defer c.Close()

	for i := 0; i < 5; i++ {
		if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// A hospital feed holds one connection open for months. Reconnecting per
	// message is a common and expensive mistake.
	if n := atomic.LoadInt64(&conns); n != 1 {
		t.Errorf("server accepted %d connections for 5 messages, want 1", n)
	}
}

type countingListener struct {
	net.Listener
	count *int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		atomic.AddInt64(l.count, 1)
	}
	return c, err
}

func TestHandlerErrorSendsNoAck(t *testing.T) {
	// A handler that fails outright must not cause an invented acknowledgement.
	// The sender is entitled to retry, and a fabricated AA would tell it the
	// message was safely processed when it was not.
	srv := &Server{Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
		return nil, errors.New("database unavailable")
	})}
	addr := startServer(t, srv)

	c := &Client{Addr: addr, Timeout: 500 * time.Millisecond}
	defer c.Close()

	_, err := c.Send(context.Background(), []byte(sampleMsg))
	if err == nil {
		t.Fatal("Send succeeded, want a timeout waiting for an acknowledgement")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestOversizedMessageDoesNotDropConnection(t *testing.T) {
	var handled int64
	srv := &Server{
		MaxMessageSize: 512,
		Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
			atomic.AddInt64(&handled, 1)
			return []byte("MSH|1\rMSA|AA|1\r"), nil
		}),
	}
	addr := startServer(t, srv)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	w := NewWriter(conn)
	// Oversized first, then a normal message on the same connection.
	if err := w.WriteMessage([]byte(strings.Repeat("X", 2000))); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := NewReader(conn, 0)
	if _, err := r.ReadMessage(); err != nil {
		t.Fatalf("no acknowledgement for the message after the oversized one: %v", err)
	}
	if n := atomic.LoadInt64(&handled); n != 1 {
		t.Errorf("handler ran %d times, want 1: the oversized message must not reach it", n)
	}
}

func TestServerRejectsJunkThenServes(t *testing.T) {
	srv := &Server{Handler: echoAck("MSH|1\rMSA|AA|1\r")}
	addr := startServer(t, srv)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Stray bytes before the first frame, as a misconfigured peer sends.
	if _, err := conn.Write([]byte("\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := NewWriter(conn).WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := NewReader(conn, 0).ReadMessage(); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
}

func TestIdleTimeoutClosesConnection(t *testing.T) {
	srv := &Server{
		Handler:     echoAck("MSH|1\rMSA|AA|1\r"),
		IdleTimeout: 150 * time.Millisecond,
	}
	addr := startServer(t, srv)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send nothing and wait for the server to give up.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("read err = %v, want io.EOF after the idle timeout", err)
	}
}

func TestMaxConnections(t *testing.T) {
	release := make(chan struct{})
	srv := &Server{
		MaxConnections: 1,
		Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
			<-release
			return []byte("MSH|1\rMSA|AA|1\r"), nil
		}),
	}
	addr := startServer(t, srv)

	first, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := NewWriter(first).WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}
	// Give the server time to accept and occupy its single slot.
	time.Sleep(100 * time.Millisecond)

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	// Refusing is honest. Accepting and then stalling looks to the peer like a
	// network fault.
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := second.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("second connection err = %v, want io.EOF", err)
	}

	close(release)
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := NewReader(first, 0).ReadMessage(); err != nil {
		t.Errorf("first connection did not get its acknowledgement: %v", err)
	}
}

func TestShutdownWaitsForInFlightMessage(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	var completed atomic.Bool

	srv := &Server{
		Logger: quietLogger(),
		Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
			close(started)
			<-finish
			completed.Store(true)
			return []byte("MSH|1\rMSA|AA|1\r"), nil
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := NewWriter(conn).WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}
	<-started

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	// Shutdown must not return while a message is being processed. Cutting a
	// connection mid-message loses the acknowledgement, and the sender resends
	// something that was already handled.
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned early with %v while a message was in flight", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(finish)
	if err := <-shutdownDone; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if !completed.Load() {
		t.Error("in-flight message did not complete")
	}
}

func TestShutdownTimeoutDropsConnections(t *testing.T) {
	srv := &Server{
		Logger: quietLogger(),
		Handler: HandlerFunc(func(ctx context.Context, msg []byte) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := NewWriter(conn).WriteMessage([]byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// The handler blocks until its context is cancelled, which shutdown does.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestServeWithoutHandler(t *testing.T) {
	srv := &Server{}
	if err := srv.ListenAndServe(); !errors.Is(err, ErrNoHandler) {
		t.Errorf("err = %v, want ErrNoHandler", err)
	}
}

func TestClientReconnectsAfterServerClose(t *testing.T) {
	// A connection reaped while idle is the normal reason a send fails, so one
	// transparent reconnect saves losing a message to a firewall timeout.
	srv := &Server{Handler: echoAck("MSH|1\rMSA|AA|1\r"), Logger: quietLogger()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	addr := ln.Addr().String()

	c := &Client{Addr: addr, Timeout: 2 * time.Second}
	defer c.Close()

	if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// Drop every server-side connection, leaving the client holding a dead one.
	srv.closeConns()
	time.Sleep(50 * time.Millisecond)

	if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
		t.Errorf("send after the connection was dropped: %v", err)
	}

	_ = srv.Close()
}

func TestClientUnreachable(t *testing.T) {
	c := &Client{Addr: "127.0.0.1:1", DialTimeout: 500 * time.Millisecond}
	if _, err := c.Send(context.Background(), []byte(sampleMsg)); err == nil {
		t.Error("Send to an unreachable address succeeded")
	}
}

func TestClientCustomDial(t *testing.T) {
	// The Dial hook is how TLS is added without this package knowing about it.
	srv := &Server{Handler: echoAck("MSH|1\rMSA|AA|1\r")}
	addr := startServer(t, srv)

	var dialed atomic.Int64
	c := &Client{
		Addr:    addr,
		Timeout: 2 * time.Second,
		Dial: func(ctx context.Context, a string) (net.Conn, error) {
			dialed.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, "tcp", a)
		},
	}
	defer c.Close()

	if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
		t.Fatal(err)
	}
	if dialed.Load() != 1 {
		t.Errorf("custom Dial called %d times, want 1", dialed.Load())
	}
}

func TestConcurrentClientSends(t *testing.T) {
	// MLLP carries one message at a time per connection, so the client has to
	// serialise callers rather than interleaving frames.
	srv := &Server{Handler: echoAck("MSH|1\rMSA|AA|1\r")}
	addr := startServer(t, srv)

	c := &Client{Addr: addr, Timeout: 3 * time.Second}
	defer c.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent send: %v", err)
	}
}

func TestManyConnections(t *testing.T) {
	srv := &Server{Handler: echoAck("MSH|1\rMSA|AA|1\r")}
	addr := startServer(t, srv)

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &Client{Addr: addr, Timeout: 3 * time.Second}
			defer c.Close()
			if _, err := c.Send(context.Background(), []byte(sampleMsg)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	count := 0
	for err := range errs {
		count++
		if count < 3 {
			t.Errorf("connection failed: %v", err)
		}
	}
	if count > 0 {
		t.Errorf("%d of %d concurrent connections failed", count, n)
	}
}
