// Package ibmmq sends to and reads from IBM MQ queues through the queue manager's messaging REST API (mqweb), which IBM
// MQ 9.1.5 and later ship on every platform, z/OS included. It needs no MQ client library: the native MQI protocol is
// IBM's and undocumented, and the REST API is the supported way to reach MQ without one. (IBM MQ also speaks AMQP 1.0
// on an AMQP channel; Perfuse's amqp connector covers that route.)
//
// Reading is at least once: the first message is browsed, handed to the caller, and removed by its message id only once
// the caller has handled it. A message that could not be handled stays on the queue, first in line.
package ibmmq

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config says which queue on which queue manager.
type Config struct {
	// BaseURL is the mqweb server, e.g. https://mq.example.org:9443.
	BaseURL string
	// QueueManager and Queue name the queue.
	QueueManager, Queue string
	// Username and Password authenticate with HTTP basic authentication. Both empty: a client certificate in TLS does.
	Username, Password string
	// TLS carries a CA for a private certificate and, optionally, a client certificate.
	TLS *tls.Config
	// APIVersion is v3 (MQ 9.3 and later, the default), v2 (9.1.5 to 9.2) or v1.
	APIVersion string
	// NonPersistent sends messages non-persistent. By default they are persistent and survive a queue manager restart.
	NonPersistent bool
	// Timeout bounds each request. Defaults to 30s.
	Timeout time.Duration
	// MaxMessage refuses a message larger than this. Defaults to 16 MiB.
	MaxMessage int64
}

// Client is one queue.
type Client struct {
	cfg Config
	hc  *http.Client
	url string
}

// New makes a client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" || cfg.QueueManager == "" || cfg.Queue == "" {
		return nil, errors.New("ibmmq: needs the mqweb url, the queue manager and the queue")
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = "v3"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxMessage == 0 {
		cfg.MaxMessage = 16 << 20
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg.TLS
	u := strings.TrimRight(cfg.BaseURL, "/") + "/ibmmq/rest/" + cfg.APIVersion + "/messaging/qmgr/" +
		url.PathEscape(cfg.QueueManager) + "/queue/" + url.PathEscape(cfg.Queue) + "/message"
	return &Client{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout + 5*time.Second, Transport: tr}, url: u}, nil
}

// Message is one message read.
type Message struct {
	Body []byte
	// ID is MQ's message id, as the REST API writes it.
	ID string
	// CorrelationID is the correlation id, when the sender set one.
	CorrelationID string
}

// Error is MQ's error answer.
type Error struct {
	Status int
	Text   string
}

func (e *Error) Error() string { return fmt.Sprintf("ibmmq: %d %s", e.Status, e.Text) }

// Put sends a message.
func (c *Client) Put(ctx context.Context, body []byte, correlationID string) error {
	h := http.Header{}
	h.Set("Content-Type", "text/plain;charset=utf-8")
	if c.cfg.NonPersistent {
		h.Set("ibm-mq-md-persistence", "nonPersistent")
	} else {
		h.Set("ibm-mq-md-persistence", "persistent")
	}
	if correlationID != "" {
		h.Set("ibm-mq-md-correlationId", correlationID)
	}
	resp, _, err := c.do(ctx, http.MethodPost, c.url, h, body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ibmmq: putting the message: unexpected %d", resp.StatusCode)
	}
	return nil
}

// Browse returns the first message on the queue without removing it, or nil when the queue is empty.
func (c *Client) Browse(ctx context.Context) (*Message, error) {
	resp, body, err := c.do(ctx, http.MethodGet, c.url, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	id := resp.Header.Get("ibm-mq-md-messageId")
	if id == "" {
		return nil, errors.New("ibmmq: the browsed message came without its message id, so it could not be removed safely")
	}
	return &Message{Body: body, ID: id, CorrelationID: resp.Header.Get("ibm-mq-md-correlationId")}, nil
}

// Remove takes the message with this id off the queue. A message already gone (another reader took it) reports false.
func (c *Client) Remove(ctx context.Context, id string) (bool, error) {
	resp, _, err := c.do(ctx, http.MethodDelete, c.url+"?messageId="+url.QueryEscape(id), nil, nil)
	if err != nil {
		return false, err
	}
	return resp.StatusCode == http.StatusOK, nil
}

// Get removes and returns the first message, waiting up to wait for one; nil when none came. Used by tests to empty a
// queue: reading for a channel is Browse, then Remove, so a message is not lost if handling fails.
func (c *Client) Get(ctx context.Context, wait time.Duration) (*Message, error) {
	resp, body, err := c.do(ctx, http.MethodDelete, c.url+fmt.Sprintf("?wait=%d", wait.Milliseconds()), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	return &Message{Body: body, ID: resp.Header.Get("ibm-mq-md-messageId")}, nil
}

func (c *Client) do(ctx context.Context, method, u string, h http.Header, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	// mqweb refuses a state-changing request without this header, as its defence against cross-site request forgery.
	req.Header.Set("ibm-mq-rest-csrf-token", "perfuse")
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("ibmmq: %s %s: %w", method, c.cfg.Queue, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxMessage+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > c.cfg.MaxMessage {
		return nil, nil, fmt.Errorf("ibmmq: a message on %s is larger than %d bytes", c.cfg.Queue, c.cfg.MaxMessage)
	}
	if resp.StatusCode >= 400 {
		return nil, nil, &Error{Status: resp.StatusCode, Text: errorText(data)}
	}
	return resp, data, nil
}

// errorText pulls MQ's explanation out of its JSON error answer without decoding the whole shape.
func errorText(b []byte) string {
	s := string(b)
	for _, key := range []string{`"message":`, `"explanation":`} {
		if i := strings.Index(s, key); i >= 0 {
			rest := strings.TrimLeft(s[i+len(key):], " \"")
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				return rest[:j]
			}
		}
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}
