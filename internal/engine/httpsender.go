package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/trace"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// HTTPSender posts messages to an HTTP endpoint.
//
// Most of the care here is about what counts as delivered. An HTTP transport that
// treats any response as success is worse than no transport at all: the message is
// gone, the channel reports a delivery, and nobody finds out until somebody asks
// where a week of results went.
type HTTPSender struct {
	cfg    config.HTTPDestination
	name   string
	log    *slog.Logger
	client *http.Client

	sent      atomic.Int64
	rejected  atomic.Int64
	bodyFails atomic.Int64
}

// NewHTTPSender builds a sender for an http destination.
func NewHTTPSender(d config.Destination, log *slog.Logger) (*HTTPSender, error) {
	if d.HTTP == nil {
		return nil, fmt.Errorf("destination %q has type http but no http block", d.Name)
	}
	if log == nil {
		log = slog.Default()
	}

	transport := &http.Transport{}
	if d.HTTP.TLS.IsEnabled() {
		cfg, err := tlsconf.ForSender(d.HTTP.TLS)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
		transport.TLSClientConfig = cfg
	}

	client := &http.Client{
		Timeout:   d.Timeout,
		Transport: transport,
	}
	if !d.HTTP.FollowRedirects {
		// A redirect on a write would repost clinical data somewhere the
		// configuration never named, and the log would show the original URL.
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf(
				"refusing to follow a redirect to %s; set follow_redirects if that is intended",
				req.URL.Host)
		}
	}

	for _, w := range d.HTTP.TLS.Warnings(false) {
		log.Warn("tls", "destination", d.Name, "detail", w)
	}

	return &HTTPSender{
		cfg:    *d.HTTP,
		name:   d.Name,
		log:    log.With("destination", d.Name),
		client: client,
	}, nil
}

// HTTPStats reports what this sender has done.
type HTTPStats struct {
	Sent      int64 `json:"sent"`
	Rejected  int64 `json:"rejected"`
	BodyFails int64 `json:"bodyFailures"`
}

// Stats returns a snapshot.
func (s *HTTPSender) Stats() HTTPStats {
	return HTTPStats{
		Sent:      s.sent.Load(),
		Rejected:  s.rejected.Load(),
		BodyFails: s.bodyFails.Load(),
	}
}

// Describe identifies the destination in logs.
func (s *HTTPSender) Describe() string {
	return fmt.Sprintf("http %s %s", s.cfg.HTTPMethod(), s.cfg.URL)
}

// Send posts one message.
func (s *HTTPSender) Send(ctx context.Context, raw []byte) error {
	req, err := http.NewRequestWithContext(ctx,
		s.cfg.HTTPMethod(), s.cfg.URL, bytes.NewReader(raw))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", s.cfg.Type())

	// The traceparent is what makes a trace span two systems. Without it, the
	// receiving application starts a fresh trace and "we sent it and nothing
	// happened" stays unanswerable - which is how almost every interface problem is
	// first reported.
	//
	// Set before the caller's own headers, so an explicitly configured traceparent
	// wins. Somebody who set one meant it.
	if tp := trace.Traceparent(trace.FromContext(ctx).Context()); tp != "" {
		req.Header.Set(trace.TraceparentHeader, tp)
	}
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	switch {
	case s.cfg.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+s.cfg.BearerToken)
	case s.cfg.Username != "":
		req.SetBasicAuth(s.cfg.Username, s.cfg.Password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", s.cfg.URL, err)
	}
	defer resp.Body.Close()

	// Bounded read. An endpoint answering with a megabyte of HTML instead of an
	// acknowledgement should not be able to exhaust memory, and the first few
	// kilobytes carry whatever explanation exists.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	text := strings.TrimSpace(string(body))

	if !s.cfg.Accepts(resp.StatusCode) {
		s.rejected.Add(1)
		return fmt.Errorf("%s answered %s: %s", s.cfg.URL, resp.Status, summariseBody(text))
	}

	// A successful status with a failure in the body. Common enough that ignoring it
	// would make this transport quietly lossy: the endpoint said 200 and meant no.
	for _, marker := range s.cfg.FailOnBody {
		if marker == "" {
			continue
		}
		if strings.Contains(text, marker) {
			s.bodyFails.Add(1)
			return fmt.Errorf(
				"%s answered %s but its response contains %q, which this destination "+
					"treats as a failure: %s",
				s.cfg.URL, resp.Status, marker, summariseBody(text))
		}
	}

	s.sent.Add(1)
	s.log.Debug("posted", "status", resp.StatusCode, "bytes", len(raw))
	return nil
}

// Close releases idle connections.
func (s *HTTPSender) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// summariseBody shortens a response for an error message.
//
// One line, because a delivery error goes into a log line and a message store
// field, and a multi-line HTML error page renders as noise in both.
func summariseBody(text string) string {
	if text == "" {
		return "(empty response)"
	}
	text = strings.Join(strings.Fields(text), " ")
	const limit = 300
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// tlsClientConfigFor is kept separate so a test can assert what was built without
// making a request.
func tlsClientConfigFor(s *HTTPSender) *tls.Config {
	t, ok := s.client.Transport.(*http.Transport)
	if !ok {
		return nil
	}
	return t.TLSClientConfig
}
