package engine

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/trace"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// The HTTP listener.
//
// Something posts a message and gets the acknowledgement back in the response
// body, which is how the modern half of a hospital integrates when MLLP is not
// available to it.
//
// The acknowledgement is an HL7 ACK, not a JSON envelope. A sender that speaks HL7
// over HTTP still speaks HL7, and inventing a different success format would mean
// every client needed code specific to us.

// DefaultHTTPMaxMessageSize bounds an inbound body.
const DefaultHTTPMaxMessageSize = 16 << 20

// startHTTPSource begins listening for posted messages.
func (c *Channel) startHTTPSource() error {
	src := c.cfg.Source.HTTP
	if src == nil {
		return fmt.Errorf("channel %q: source type http but no http block", c.cfg.Name)
	}

	for _, w := range src.Warnings() {
		// Warned at every start rather than once at load. An unauthenticated
		// endpoint accepting clinical messages is the kind of thing that gets set up
		// for a test and then forgotten.
		c.log.Warn("http source", "detail", w)
	}

	tlsCfg, err := tlsconf.ForListener(src.TLS)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	for _, w := range src.TLS.Warnings(true) {
		c.log.Warn("tls", "detail", w)
	}

	maxSize := src.MaxMessageSize
	if maxSize <= 0 {
		maxSize = DefaultHTTPMaxMessageSize
	}

	mux := http.NewServeMux()
	mux.HandleFunc(src.HTTPPath(), c.handlePostedMessage(src, maxSize))

	readTimeout := src.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 30 * time.Second
	}

	srv := &http.Server{
		Addr:              src.Listen,
		Handler:           mux,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		// Deliberately generous. The response is not written until the message has
		// been delivered, so a slow downstream receiver must not look like a
		// timeout to the sender.
		WriteTimeout: 5 * time.Minute,
	}

	ln, err := net.Listen("tcp", src.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}

	c.httpServer = srv
	c.log.Info("http listening",
		"addr", src.Listen, "path", src.HTTPPath(),
		"tls", tlsCfg != nil, "authenticated", src.Token != "")

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("the http listener stopped", "error", err)
		}
	}()

	return nil
}

// handlePostedMessage returns the handler for one channel.
func (c *Channel) handlePostedMessage(src *config.HTTPSource, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			w.Header().Set("Allow", "POST, PUT")
			http.Error(w, "send a message with POST or PUT", http.StatusMethodNotAllowed)
			return
		}

		if src.Token != "" && !validBearer(r, src.Token) {
			// No detail about why. An endpoint that distinguishes "wrong token" from
			// "no token" tells an attacker which half to work on.
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxSize)+1))
		if err != nil {
			http.Error(w, "could not read the request", http.StatusBadRequest)
			return
		}
		if len(body) > maxSize {
			// 413 rather than a truncated message. Accepting a partial HL7 message
			// would parse into something plausible and wrong.
			http.Error(w, fmt.Sprintf(
				"the message is larger than the %d byte limit", maxSize),
				http.StatusRequestEntityTooLarge)
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			http.Error(w, "the request has no message in it", http.StatusBadRequest)
			return
		}

		// MLLP framing is stripped, because a client that already speaks MLLP will
		// send it and there is no reason to make them special-case us.
		message := stripFraming(body)

		// A sending system that traces its own work puts a traceparent on the
		// request. Continuing that trace rather than starting a new one is the
		// whole reason this is here.
		ctx := trace.WithRemote(r.Context(), trace.ParseTraceparent(r.Header.Get(trace.TraceparentHeader)))

		ack, err := c.handle(ctx, message)
		if err != nil {
			c.log.Error("handling a posted message failed", "error", err)
			http.Error(w, "the message could not be processed", http.StatusInternalServerError)
			return
		}

		// The acknowledgement is returned as HL7, and the status code reflects it, so
		// a client can act on either without parsing the other.
		status := http.StatusOK
		switch c.LastOutcomeForTest() {
		case Failed, PartiallyDelivered:
			// 502: we understood the message and something downstream refused it.
			// Distinguishing this from a bad request is what tells a sender whether
			// resending unchanged is worth trying.
			status = http.StatusBadGateway
		case Unparseable:
			status = http.StatusBadRequest
		}

		// X12 acknowledges only when the channel is configured to, and the content type
		// differs, so it is handled separately rather than falling through.
		//
		// The default remains no body. Classical X12 acknowledgement is asynchronous - a
		// clearinghouse takes an 837 by file drop and returns a 999 hours later as its own
		// interchange - and a partner not expecting one may treat it as an unsolicited
		// file. But a real-time transaction holds this connection open precisely so it can
		// be answered here, and CAQH CORE requires exactly that for a 270.
		if c.cfg.Type() == config.DataX12 {
			if len(ack) == 0 {
				// Nothing to return, and labelling an empty body as X12 would be a lie a
				// client could act on. The status still carries the outcome.
				if status == http.StatusOK {
					status = http.StatusNoContent
				}
				w.WriteHeader(status)

				return
			}

			// application/EDI-X12 is the registered type, and the casing is the registry's
			// own. Some trading partner software matches it literally.
			w.Header().Set("Content-Type", "application/EDI-X12")
			w.WriteHeader(status)
			_, _ = w.Write(ack)

			return
		}

		w.Header().Set("Content-Type", "application/hl7-v2+er7")
		w.WriteHeader(status)
		_, _ = w.Write(ack)
	}
}

// validBearer checks the Authorization header in constant time.
func validBearer(r *http.Request, token string) bool {
	header := r.Header.Get("Authorization")
	presented, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	// Constant time, so the comparison does not leak the token a byte at a time.
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), []byte(token)) == 1
}

// stripFraming removes MLLP framing bytes if present.
func stripFraming(body []byte) []byte {
	text := string(body)
	text = strings.TrimPrefix(text, "\x0b")
	text = strings.TrimSuffix(text, "\x1c\r")
	text = strings.TrimSuffix(text, "\x1c")
	return []byte(text)
}

// stopHTTPSource shuts the listener down, waiting for messages in flight.
func (c *Channel) stopHTTPSource(ctx context.Context) error {
	if c.httpServer == nil {
		return nil
	}
	err := c.httpServer.Shutdown(ctx)
	c.httpServer = nil
	return err
}

// logHTTPSource is used by tests to confirm what was configured.
func (c *Channel) httpAddr() string {
	if c.httpServer == nil {
		return ""
	}
	return c.httpServer.Addr
}
