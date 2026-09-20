package peers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SelfPath is the endpoint every instance serves so that another can poll it.
//
// One endpoint rather than aggregating four means one round trip per peer, one contract to keep stable across
// versions, and one place that decides what a peer is willing to disclose. Stitching /api/channels, /api/queue and
// /api/alerts together from outside would put that decision in the caller, which is the wrong side.
const SelfPath = "/api/fleet/self"

// SelfReport is what an instance says about itself. Counts and rates only - no messages, no message content, no
// patient data of any kind. That is what makes centralised monitoring here not a centralisation of the data.
type SelfReport struct {
	Label   string `json:"label"`
	Version string `json:"version"`

	// Now is the peer's clock, so the aggregator can measure skew rather than assume none. An aggregated time
	// series built across servers with disagreeing clocks is wrong in a way nobody notices.
	Now time.Time `json:"now"`

	Health Health `json:"health"`
}

// Client polls peers.
type Client struct {
	http     *http.Client
	insecure *http.Client
}

// NewClient builds a client with the given per-request timeout.
func NewClient(timeout time.Duration) *Client {
	return &Client{
		http: &http.Client{Timeout: timeout},
		insecure: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				// #nosec G402 - opt-in per peer, named insecure_skip_verify, and documented as such. Refusing
				// outright would push people onto plain http, which is worse.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// Poll asks one peer about itself and returns its status. It never returns an error: a failure to reach a peer is
// the status, not an exception, and a caller that had to handle both would end up conflating them.
func (c *Client) Poll(ctx context.Context, p Peer, previous Status) Status {
	started := time.Now()

	out := Status{
		Name:          p.Name,
		URL:           p.URL,
		AllowControl:  p.AllowControl,
		CheckedAt:     started,
		LastReachable: previous.LastReachable,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+SelfPath, nil)
	if err != nil {
		out.Reachability = Unreachable
		out.Error = err.Error()
		return out
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Accept", "application/json")

	client := c.http
	if p.InsecureSkipVerify {
		client = c.insecure
	}

	resp, err := client.Do(req)
	out.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		out.Reachability = Unreachable
		out.Error = describeDialError(err)
		return out
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		out.Reachability = Unauthorised
		out.Error = fmt.Sprintf("the peer answered %d; the token is missing, wrong, or lacks the viewer role",
			resp.StatusCode)
		return out
	case resp.StatusCode == http.StatusNotFound:
		// Almost always an older build without the endpoint, which is expected mid-upgrade and not an outage.
		out.Reachability = Incompatible
		out.Error = "the peer has no " + SelfPath + " endpoint, which usually means it is running an older build"
		return out
	case resp.StatusCode != http.StatusOK:
		out.Reachability = Incompatible
		out.Error = fmt.Sprintf("the peer answered %d", resp.StatusCode)
		return out
	}

	// Bounded: a peer answering with something enormous should not be able to exhaust the aggregator's memory, and
	// a legitimate report is under a kilobyte.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out.Reachability = Unreachable
		out.Error = "the peer stopped responding part way through its reply: " + err.Error()
		return out
	}

	var report SelfReport
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		// Retried leniently, because a newer peer adding a field must not read as a broken peer. If it parses
		// leniently the data is good; only if it fails both ways is the peer genuinely unintelligible.
		if lenientErr := json.Unmarshal(body, &report); lenientErr != nil {
			out.Reachability = Incompatible
			out.Error = "the peer's reply could not be read: " + lenientErr.Error()
			return out
		}
	}

	out.Reachability = Reachable
	out.Version = report.Version
	out.Health = &report.Health
	if !report.Now.IsZero() {
		out.SkewSeconds = report.Now.Sub(started).Seconds()
	}
	now := time.Now()
	out.LastReachable = &now

	return out
}

// describeDialError turns a transport failure into something an operator can act on. The distinctions matter: a
// refused connection means the host is up and nothing is listening, a timeout usually means a firewall, and a
// certificate failure means the peer is fine and the trust is not. Reporting all three as "unreachable" sends
// people to the wrong place.
func describeDialError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out; the host may be unreachable or a firewall may be dropping the connection silently"
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused; the host answered, so it is up and nothing is listening on that port"
	case strings.Contains(msg, "no such host"):
		return "the hostname does not resolve"
	case strings.Contains(msg, "certificate"):
		return "the TLS certificate was rejected: " + msg + "; the peer is probably running and the trust is not " +
			"established - set insecure_skip_verify only if this is a private certificate authority you trust"
	}
	return msg
}
