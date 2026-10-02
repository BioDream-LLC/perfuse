package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Notifier sends an alert somewhere a person will see it.
//
// Webhook only, on purpose. Email needs an SMTP relay, credentials and a
// deliverability story; SMS needs an account with a provider. A webhook works
// with Slack, Teams, Mattermost, PagerDuty, Alertmanager and anything somebody
// writes in an afternoon, and it adds no dependency to a binary whose whole
// selling point is that it has almost none.
type Notifier struct {
	// URL receives a POST for every alert that fires and every one that resolves.
	URL string

	// Headers are added to the request, for a signing token or a routing key.
	Headers map[string]string

	// MinSeverity suppresses anything below it. Default warning.
	MinSeverity Severity

	// MinSeverityFn supersedes MinSeverity when set, so the setting can change without a restart.
	//
	// Read when an alert is dispatched rather than copied at startup. Raising the threshold is usually a response
	// to noise at an unwelcome hour, and "after a restart" is not a useful answer then.
	MinSeverityFn func() Severity

	// Client is optional.
	Client *http.Client

	Log *slog.Logger

	once sync.Once
}

// Payload is what a webhook receives.
//
// Deliberately flat and self-describing, so a chat integration can render it
// without a schema. It carries channel names, destination names, numbers and
// thresholds — all of which come from configuration or from counting. No message
// content, no identifiers: a webhook goes to a chat room, a chat room has a
// scrollback, and a scrollback is not somewhere a patient identifier belongs.
type Payload struct {
	// Status is "firing" or "resolved", so a receiver can close its own incident.
	Status string `json:"status"`

	Kind        Kind     `json:"kind"`
	Severity    Severity `json:"severity"`
	Channel     string   `json:"channel,omitempty"`
	Destination string   `json:"destination,omitempty"`

	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`

	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`

	FiringSince time.Time `json:"firingSince"`
	At          time.Time `json:"at"`

	// Text is a rendered one-liner, because most chat webhooks want a string and
	// making every deployment write a template would mean most of them do not.
	Text string `json:"text"`
}

func (n *Notifier) init() {
	n.once.Do(func() {
		if n.Client == nil {
			n.Client = &http.Client{Timeout: 10 * time.Second}
		}
		if n.Log == nil {
			n.Log = slog.Default()
		}
		if n.MinSeverity == "" {
			n.MinSeverity = Warning
		}
	})
}

// minSeverity is the threshold in force right now.
//
// One accessor rather than reads of the field, because the field became live-editable and the retention window taught what
// happens otherwise: one value read in two places, one converted, and the two silently disagreeing. Both spellings
// type-check, so nothing catches it but a test.
//
// An unrecognised value falls back to the default rather than suppressing everything. A threshold nobody recognises must
// not mean silence: an operator who mistypes it should get more alerts than they wanted, not none, because the failure
// mode of the other choice is a queue backing up unannounced.
func (n *Notifier) minSeverity() Severity {
	if n.MinSeverityFn != nil {
		switch got := n.MinSeverityFn(); got {
		case Warning, Critical:
			return got
		case "":
			// Nothing configured, so fall through to the field.
		default:
			return Warning
		}
	}
	if n.MinSeverity != "" {
		return n.MinSeverity
	}

	return Warning
}

// Notify sends one alert. Safe to pass directly to NewEvaluator.
func (n *Notifier) Notify(a Alert) {
	if n == nil || strings.TrimSpace(n.URL) == "" {
		return
	}
	n.init()

	if n.minSeverity() == Critical && a.Severity != Critical {
		return
	}
	// An acknowledged alert stops paging but keeps showing in the interface.
	// Somebody who knows a receiver is down for maintenance should be able to stop
	// the noise without pretending the queue is empty.
	if a.Acknowledged && a.Firing() {
		return
	}

	payload := Payload{
		Status:      statusOf(a),
		Kind:        a.Kind,
		Severity:    a.Severity,
		Channel:     a.Channel,
		Destination: a.Destination,
		Summary:     a.Summary,
		Detail:      a.Detail,
		Value:       a.Value,
		Threshold:   a.Threshold,
		FiringSince: a.FiringSince,
		At:          time.Now().UTC(),
		Text:        renderText(a),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		n.Log.Error("could not encode an alert", "error", err)
		return
	}

	// Sent in the background with its own timeout. An alert is not worth blocking
	// the evaluation loop for, and a webhook endpoint that has itself gone down
	// must not stop us noticing the next problem.
	go n.post(body, a)
}

func (n *Notifier) post(body []byte, a Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		n.Log.Error("could not build the alert request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.Headers {
		req.Header.Set(k, v)
	}

	resp, err := n.Client.Do(req)
	if err != nil {
		// Logged, not retried. A retry queue for alerts is a second thing that can
		// break, and an alert that arrives twenty minutes late is worse than one
		// that is missing and obviously so.
		n.Log.Error("could not deliver an alert",
			"kind", string(a.Kind), "channel", a.Channel, "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		n.Log.Error("the alert endpoint refused an alert",
			"status", resp.Status, "kind", string(a.Kind), "channel", a.Channel)
	}
}

func statusOf(a Alert) string {
	if a.Firing() {
		return "firing"
	}
	return "resolved"
}

// renderText writes the one-liner a chat room shows.
func renderText(a Alert) string {
	var b strings.Builder
	if a.Firing() {
		if a.Severity == Critical {
			b.WriteString("CRITICAL: ")
		} else {
			b.WriteString("Warning: ")
		}
		b.WriteString(a.Summary)
		if a.Detail != "" {
			b.WriteString(" — ")
			b.WriteString(a.Detail)
		}
		return b.String()
	}

	b.WriteString("Resolved: ")
	b.WriteString(a.Summary)
	if !a.FiringSince.IsZero() {
		b.WriteString(fmt.Sprintf(" (lasted %s)",
			formatDuration(a.ResolvedAt.Sub(a.FiringSince).Seconds())))
	}
	return b.String()
}
