// Package awsmsg speaks to Amazon SQS and SNS with the standard library and the shared Signature Version 4 signer.
//
// The same argument as s3put: the AWS SDK would double the size of the bill of materials for four API calls. SQS uses its JSON protocol
// (AWS JSON 1.0, X-Amz-Target AmazonSQS.<Action>), which AWS documents as the current one; SNS uses its query protocol, which is the only
// one it has. Both work against LocalStack and VPC endpoints through an endpoint override.
package awsmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
)

// Config locates a service and signs for it.
type Config struct {
	Region      string
	Credentials awsv4.Credentials
	// Endpoint overrides AWS's host: LocalStack, ElasticMQ, a VPC endpoint. Empty means AWS for the region.
	Endpoint string
	// HTTPClient defaults to one with a timeout longer than the longest SQS long poll (20s).
	HTTPClient *http.Client
}

func (c Config) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 40 * time.Second}
}

func (c Config) check(what string) error {
	if strings.TrimSpace(c.Region) == "" {
		return fmt.Errorf("%s needs a region, because it forms part of the request signature", what)
	}
	if !c.Credentials.Valid() {
		return fmt.Errorf("%s needs an access key id and a secret access key", what)
	}
	return nil
}

// SQS sends to and receives from one queue.
type SQS struct {
	cfg      Config
	queueURL string
	endpoint *url.URL
}

// NewSQS builds a client for a queue, named by its URL.
func NewSQS(cfg Config, queueURL string) (*SQS, error) {
	if err := cfg.check("an SQS queue"); err != nil {
		return nil, err
	}
	if _, err := url.Parse(queueURL); err != nil || !strings.Contains(queueURL, "://") {
		return nil, fmt.Errorf("the SQS queue URL %q is not a URL; it looks like https://sqs.eu-west-2.amazonaws.com/123456789012/adt",
			queueURL)
	}
	ep, err := awsv4.Endpoint(cfg.Endpoint, "sqs", cfg.Region)
	if err != nil {
		return nil, err
	}
	return &SQS{cfg: cfg, queueURL: queueURL, endpoint: ep}, nil
}

// FIFO reports whether the queue is a FIFO queue, which needs a group id on every send.
func (q *SQS) FIFO() bool { return strings.HasSuffix(q.queueURL, ".fifo") }

func (q *SQS) call(ctx context.Context, action string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+action)
	req.Host = q.endpoint.Host
	awsv4.Sign(req, body, q.cfg.Credentials, q.cfg.Region, "sqs", time.Now())

	resp, err := q.cfg.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data[:min(len(data), 300)]))
		}
		return fmt.Errorf("SQS refused %s with %s: %s %s", action, resp.Status, e.Type, e.Message)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Send puts one message on the queue. group and dedup are for FIFO queues and ignored otherwise.
func (q *SQS) Send(ctx context.Context, body, group, dedup string) (string, error) {
	in := map[string]any{"QueueUrl": q.queueURL, "MessageBody": body}
	if q.FIFO() {
		in["MessageGroupId"] = group
		if dedup != "" {
			in["MessageDeduplicationId"] = dedup
		}
	}
	var out struct {
		MessageID string `json:"MessageId"`
	}
	if err := q.call(ctx, "SendMessage", in, &out); err != nil {
		return "", err
	}
	return out.MessageID, nil
}

// Message is one received message. It stays on the queue, invisible, until it is deleted or its visibility timeout runs out.
type Message struct {
	ID            string
	Body          string
	ReceiptHandle string
	// ReceiveCount is how many times SQS has delivered it, which says when a message keeps failing.
	ReceiveCount int
}

// Receive long-polls for up to max messages (1 to 10), waiting up to wait seconds (0 to 20).
func (q *SQS) Receive(ctx context.Context, max, wait, visibility int) ([]Message, error) {
	in := map[string]any{
		"QueueUrl":                    q.queueURL,
		"MaxNumberOfMessages":         max,
		"WaitTimeSeconds":             wait,
		"MessageSystemAttributeNames": []string{"ApproximateReceiveCount"},
	}
	if visibility > 0 {
		in["VisibilityTimeout"] = visibility
	}
	var out struct {
		Messages []struct {
			MessageID     string            `json:"MessageId"`
			Body          string            `json:"Body"`
			ReceiptHandle string            `json:"ReceiptHandle"`
			Attributes    map[string]string `json:"Attributes"`
		} `json:"Messages"`
	}
	if err := q.call(ctx, "ReceiveMessage", in, &out); err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		n := 0
		_, _ = fmt.Sscan(m.Attributes["ApproximateReceiveCount"], &n)
		msgs = append(msgs, Message{ID: m.MessageID, Body: m.Body, ReceiptHandle: m.ReceiptHandle, ReceiveCount: n})
	}
	return msgs, nil
}

// Delete removes a message that has been handled.
func (q *SQS) Delete(ctx context.Context, receiptHandle string) error {
	return q.call(ctx, "DeleteMessage", map[string]any{"QueueUrl": q.queueURL, "ReceiptHandle": receiptHandle}, nil)
}

// SNS publishes to one topic.
type SNS struct {
	cfg      Config
	topicARN string
	endpoint *url.URL
}

// NewSNS builds a client for a topic, named by its ARN.
func NewSNS(cfg Config, topicARN string) (*SNS, error) {
	if err := cfg.check("an SNS topic"); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(topicARN, "arn:") {
		return nil, fmt.Errorf("the SNS topic %q is not an ARN; it looks like arn:aws:sns:eu-west-2:123456789012:adt", topicARN)
	}
	ep, err := awsv4.Endpoint(cfg.Endpoint, "sns", cfg.Region)
	if err != nil {
		return nil, err
	}
	return &SNS{cfg: cfg, topicARN: topicARN, endpoint: ep}, nil
}

// FIFO reports whether the topic is a FIFO topic.
func (t *SNS) FIFO() bool { return strings.HasSuffix(t.topicARN, ".fifo") }

// Publish sends one message. subject is optional; group and dedup are for FIFO topics.
func (t *SNS) Publish(ctx context.Context, message, subject, group, dedup string) (string, error) {
	form := url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {t.topicARN}, "Message": {message}}
	if subject != "" {
		form.Set("Subject", subject)
	}
	if t.FIFO() {
		form.Set("MessageGroupId", group)
		if dedup != "" {
			form.Set("MessageDeduplicationId", dedup)
		}
	}
	body := []byte(awsv4.CanonicalQuery(form))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Host = t.endpoint.Host
	awsv4.Sign(req, body, t.cfg.Credentials, t.cfg.Region, "sns", time.Now())

	resp, err := t.cfg.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    string `xml:"Error>Code"`
			Message string `xml:"Error>Message"`
		}
		_ = xml.Unmarshal(data, &e)
		return "", fmt.Errorf("SNS refused the publish with %s: %s %s", resp.Status, e.Code, e.Message)
	}
	var out struct {
		MessageID string `xml:"PublishResult>MessageId"`
	}
	_ = xml.Unmarshal(data, &out)
	return out.MessageID, nil
}
