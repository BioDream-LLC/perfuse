package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/framing"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// tcpListener accepts connections and reads messages with whatever framing was configured.
//
// # Why an oversize message closes the connection
//
// Every other error here is per-message: a message fails, it is recorded, and the next one is read. An oversize message
// is different, because the reader is somewhere in the middle of it with no way to find the next boundary. Continuing
// would deliver the tail of one message as though it were a whole one, which is exactly the plausible-looking corruption
// this connector exists to avoid. So the connection is closed and the peer reconnects, which resynchronises both ends.
type tcpListener struct {
	ch   *Channel
	cfg  *config.TCPSource
	fr   framing.Settings
	ln   net.Listener
	stop chan struct{}

	closed sync.Once
	wg     sync.WaitGroup

	// live counts open connections, for max_connections and for the stats.
	live atomic.Int64

	mu    sync.Mutex
	stats TCPSourceStats
}

// TCPSourceStats reports what the listener has done.
type TCPSourceStats struct {
	Accepted      int64  `json:"accepted"`
	Refused       int64  `json:"refused"`
	Open          int64  `json:"open"`
	Messages      int64  `json:"messages"`
	FramingErrors int64  `json:"framingErrors"`
	Oversize      int64  `json:"oversize"`
	LastError     string `json:"lastError,omitempty"`
	Framing       string `json:"framing,omitempty"`
}

// startTCPSource begins listening.
func (c *Channel) startTCPSource() error {
	cfg := c.cfg.Source.TCP
	if cfg == nil {
		return fmt.Errorf("channel %q has a tcp source but no tcp block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("tcp source", "channel", c.cfg.Name, "warning", w)
	}

	fr, err := cfg.Settings(cfg.MaxMessageSize)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}

	var ln net.Listener
	if cfg.TLS.IsEnabled() {
		tlsCfg, tlsErr := tlsconf.ForListener(cfg.TLS)
		if tlsErr != nil {
			return fmt.Errorf("channel %q tcp source tls: %w", c.cfg.Name, tlsErr)
		}
		ln, err = tls.Listen("tcp", cfg.Listen, tlsCfg)
	} else {
		ln, err = net.Listen("tcp", cfg.Listen)
	}
	if err != nil {
		return fmt.Errorf("channel %q cannot listen on %s: %w", c.cfg.Name, cfg.Listen, err)
	}

	l := &tcpListener{ch: c, cfg: cfg, fr: fr, ln: ln, stop: make(chan struct{})}
	l.stats.Framing = fr.Describe()
	c.tcpSrc = l

	c.log.Info("listening on a raw socket",
		"channel", c.cfg.Name, "address", ln.Addr().String(),
		"framing", fr.Describe(), "reply", cfg.Reply, "tls", cfg.TLS.IsEnabled())

	l.wg.Add(1)
	go l.accept()
	return nil
}

// stopTCPSource stops listening and waits for connections to finish.
func (c *Channel) stopTCPSource() error {
	if c.tcpSrc == nil {
		return nil
	}
	err := c.tcpSrc.close()
	c.tcpSrc = nil
	return err
}

// Addr reports where the listener actually bound, which matters when the port was zero.
func (l *tcpListener) Addr() string {
	if l == nil || l.ln == nil {
		return ""
	}
	return l.ln.Addr().String()
}

// Stats returns a copy.
func (l *tcpListener) Stats() TCPSourceStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stats
	s.Open = l.live.Load()
	return s
}

func (l *tcpListener) close() error {
	var err error
	l.closed.Do(func() {
		close(l.stop)
		err = l.ln.Close()
	})
	l.wg.Wait()
	return err
}

func (l *tcpListener) accept() {
	defer l.wg.Done()

	for {
		conn, err := l.ln.Accept()
		if err != nil {
			select {
			case <-l.stop:
				return
			default:
			}
			// A transient accept failure, usually a file descriptor limit. Logged and retried rather than abandoning
			// the listener: giving up would take the feed down permanently for a condition that clears.
			l.mu.Lock()
			l.stats.LastError = err.Error()
			l.mu.Unlock()
			l.ch.log.Error("could not accept a connection",
				"channel", l.ch.cfg.Name, "err", err,
				"why", "retrying rather than giving up: this is usually a file descriptor limit, which clears, "+
					"and abandoning the listener would take the feed down permanently")
			select {
			case <-l.stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		if max := l.cfg.MaxConnections; max > 0 && l.live.Load() >= int64(max) {
			l.mu.Lock()
			l.stats.Refused++
			l.mu.Unlock()
			// Closed immediately rather than queued. A device that cannot connect retries; one that connects and is
			// never read waits forever holding a socket, and the operator sees a healthy connection delivering
			// nothing.
			l.ch.log.Warn("refused a connection because max_connections is reached",
				"channel", l.ch.cfg.Name, "from", conn.RemoteAddr().String(), "limit", max)
			_ = conn.Close()
			continue
		}

		l.live.Add(1)
		l.mu.Lock()
		l.stats.Accepted++
		l.mu.Unlock()

		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.live.Add(-1)
			l.serve(conn)
		}()
	}
}

// serve reads messages from one connection until it ends.
func (l *tcpListener) serve(conn net.Conn) {
	defer conn.Close()

	peer := conn.RemoteAddr().String()
	log := l.ch.log.With("channel", l.ch.cfg.Name, "peer", peer)

	// Closed when the channel stops, so a quiet connection does not hold up shutdown.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-l.stop:
			_ = conn.Close()
		case <-done:
		}
	}()

	reader := framing.NewReader(conn, l.fr)

	for {
		if l.cfg.IdleTimeout > 0 {
			if err := conn.SetReadDeadline(time.Now().Add(l.cfg.IdleTimeout)); err != nil {
				log.Warn("could not set the idle timeout", "err", err)
			}
		}

		msg, err := reader.ReadMessage()
		if err == io.EOF {
			return
		}
		if err != nil {
			select {
			case <-l.stop:
				return
			default:
			}

			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Info("closing an idle connection", "after", l.cfg.IdleTimeout)
				return
			}

			// An oversize message means the reader is mid-message with no way to find the next boundary. Continuing
			// would deliver the tail of one message as though it were whole, so the connection is closed and the peer
			// reconnects, which resynchronises both ends.
			if errors.Is(err, framing.ErrTooLarge) {
				l.mu.Lock()
				l.stats.Oversize++
				l.stats.LastError = err.Error()
				l.mu.Unlock()
				log.Error("closing the connection after an oversize message, because there is no way to find "+
					"where the next message starts and reading on would deliver part of one message as a "+
					"whole one", "err", err)
				return
			}

			l.mu.Lock()
			l.stats.FramingErrors++
			l.stats.LastError = err.Error()
			l.mu.Unlock()
			log.Error("could not read a message from the stream", "err", err, "framing", l.fr.Describe())
			return
		}

		l.mu.Lock()
		l.stats.Messages++
		l.mu.Unlock()

		ctx := context.Background()
		if _, err := l.ch.handle(ctx, msg); err != nil {
			log.Error("a message from the stream could not be processed", "err", err)
			// Not fatal to the connection. The next message may be fine, and dropping the connection for one bad
			// message makes a device resend everything it has.
		}

		if err := l.reply(conn); err != nil {
			log.Error("could not send the reply", "err", err)
			return
		}
	}
}

// reply sends whatever the configuration says to send back.
//
// A device that expects a reply and does not get one usually retries the same message forever, which looks like a
// duplicate storm rather than a missing reply.
func (l *tcpListener) reply(conn net.Conn) error {
	switch l.cfg.Reply {
	case config.ReplyNone, "":
		return nil

	case config.ReplyACK:
		_, err := conn.Write([]byte{0x06})
		return err

	case config.ReplyText:
		// Resolved per message rather than once, which is wasteful but keeps the validation in one place. The
		// configuration cannot change under a running listener, so this cannot start failing mid-stream.
		body, err := l.cfg.ReplyBytes()
		if err != nil {
			return err
		}
		_, err = conn.Write(body)
		return err

	default:
		return fmt.Errorf("reply %q is not understood", l.cfg.Reply)
	}
}
