package gcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PubSub is a client for one project's topics and subscriptions.
type PubSub struct {
	// Endpoint is https://pubsub.googleapis.com, a regional endpoint, or an emulator's http://host:8085.
	Endpoint string
	Project  string
	Creds    *Credentials
	HTTP     *http.Client
}

// NewPubSub makes a client. An empty endpoint is Google's.
func NewPubSub(endpoint, project string, creds *Credentials) (*PubSub, error) {
	if project == "" {
		return nil, errors.New("gcp: pub/sub needs a project")
	}
	if endpoint == "" {
		endpoint = "https://pubsub.googleapis.com"
	}
	return &PubSub{Endpoint: strings.TrimRight(endpoint, "/"), Project: project, Creds: creds,
		HTTP: &http.Client{Timeout: 90 * time.Second}}, nil
}

// Message is one message to publish or received.
type Message struct {
	Data        []byte
	Attributes  map[string]string
	OrderingKey string
	// ID and AckID are set on received messages.
	ID    string
	AckID string
	// Attempt is the delivery attempt, when the subscription has a dead-letter policy (0 otherwise).
	Attempt int
}

type wireMessage struct {
	Data        string            `json:"data"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	OrderingKey string            `json:"orderingKey,omitempty"`
	MessageID   string            `json:"messageId,omitempty"`
}

func (p *PubSub) url(kind, name, verb string) string {
	return fmt.Sprintf("%s/v1/projects/%s/%s/%s%s", p.Endpoint, url.PathEscape(p.Project), kind, url.PathEscape(name), verb)
}

// Publish sends messages to a topic and returns their ids once Pub/Sub has stored them.
func (p *PubSub) Publish(ctx context.Context, topic string, msgs ...Message) ([]string, error) {
	var in struct {
		Messages []wireMessage `json:"messages"`
	}
	for _, m := range msgs {
		in.Messages = append(in.Messages, wireMessage{Data: base64.StdEncoding.EncodeToString(m.Data), Attributes: m.Attributes,
			OrderingKey: m.OrderingKey})
	}
	var out struct {
		MessageIDs []string `json:"messageIds"`
	}
	if _, err := doJSON(ctx, p, http.MethodPost, p.url("topics", topic, ":publish"), in, &out); err != nil {
		return nil, err
	}
	if len(out.MessageIDs) != len(msgs) {
		return nil, fmt.Errorf("gcp: published %d messages and got %d ids back", len(msgs), len(out.MessageIDs))
	}
	return out.MessageIDs, nil
}

// Pull receives up to max messages, waiting up to wait for some to arrive. None by then is not an error: it returns an
// empty list. Pub/Sub holds a pull open until a message comes, so the wait is what bounds it.
func (p *PubSub) Pull(ctx context.Context, subscription string, max int, wait time.Duration) ([]Message, error) {
	pctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	msgs, err := p.pull(pctx, subscription, max)
	if err != nil && pctx.Err() != nil && ctx.Err() == nil {
		return nil, nil
	}
	return msgs, err
}

func (p *PubSub) pull(ctx context.Context, subscription string, max int) ([]Message, error) {
	var out struct {
		Received []struct {
			AckID           string      `json:"ackId"`
			Message         wireMessage `json:"message"`
			DeliveryAttempt int         `json:"deliveryAttempt"`
		} `json:"receivedMessages"`
	}
	in := map[string]any{"maxMessages": max}
	if _, err := doJSON(ctx, p, http.MethodPost, p.url("subscriptions", subscription, ":pull"), in, &out); err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Received))
	for _, r := range out.Received {
		data, err := base64.StdEncoding.DecodeString(r.Message.Data)
		if err != nil {
			return nil, fmt.Errorf("gcp: message %s's data is not base64", r.Message.MessageID)
		}
		msgs = append(msgs, Message{Data: data, Attributes: r.Message.Attributes, OrderingKey: r.Message.OrderingKey,
			ID: r.Message.MessageID, AckID: r.AckID, Attempt: r.DeliveryAttempt})
	}
	return msgs, nil
}

// Ack tells Pub/Sub the messages were handled.
func (p *PubSub) Ack(ctx context.Context, subscription string, ackIDs ...string) error {
	_, err := doJSON(ctx, p, http.MethodPost, p.url("subscriptions", subscription, ":acknowledge"),
		map[string]any{"ackIds": ackIDs}, nil)
	return err
}

// Nack asks for the messages again now, rather than when their acknowledgement deadline passes.
func (p *PubSub) Nack(ctx context.Context, subscription string, ackIDs ...string) error {
	_, err := doJSON(ctx, p, http.MethodPost, p.url("subscriptions", subscription, ":modifyAckDeadline"),
		map[string]any{"ackIds": ackIDs, "ackDeadlineSeconds": 0}, nil)
	return err
}

// CreateTopic and CreateSubscription exist for tests against the emulator; a site creates its own in Google Cloud.
func (p *PubSub) CreateTopic(ctx context.Context, topic string) error {
	_, err := doJSON(ctx, p, http.MethodPut, p.url("topics", topic, ""), map[string]any{}, nil)
	return err
}

// CreateSubscription makes a pull subscription on a topic.
func (p *PubSub) CreateSubscription(ctx context.Context, subscription, topic string, ordered bool) error {
	_, err := doJSON(ctx, p, http.MethodPut, p.url("subscriptions", subscription, ""), map[string]any{
		"topic": "projects/" + p.Project + "/topics/" + topic, "ackDeadlineSeconds": 60, "enableMessageOrdering": ordered}, nil)
	return err
}

func doJSON(ctx context.Context, p *PubSub, method, u string, in, out any) ([]byte, error) {
	body, err := jsonBody(in)
	if err != nil {
		return nil, err
	}
	return do(ctx, p.HTTP, p.Creds, method, u, "application/json", body, out)
}
