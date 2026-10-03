package awsmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
)

// Against LocalStack, which implements SQS and SNS closely enough that the AWS SDKs test against it. Start it with
// ./scripts/interop-up.sh; skipped when it is not there. PERFUSE_LOCALSTACK overrides the address.

func localstack(t *testing.T) Config {
	t.Helper()
	base := os.Getenv("PERFUSE_LOCALSTACK")
	if base == "" {
		base = "http://127.0.0.1:4566"
	}
	res, err := http.Get(base + "/_localstack/health")
	if err != nil {
		t.Skipf("no LocalStack on %s (%v). Start it with ./scripts/interop-up.sh", base, err)
	}
	_ = res.Body.Close()
	return Config{Region: "us-east-1", Endpoint: base, Credentials: awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}}
}

// createQueue makes a queue through the same signed JSON protocol, and returns its URL.
func createQueue(t *testing.T, cfg Config, name string, attrs map[string]string) string {
	t.Helper()
	ep, _ := awsv4.Endpoint(cfg.Endpoint, "sqs", cfg.Region)
	q := &SQS{cfg: cfg, endpoint: ep}
	in := map[string]any{"QueueName": name}
	if attrs != nil {
		in["Attributes"] = attrs
	}
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := q.call(context.Background(), "CreateQueue", in, &out); err != nil {
		t.Fatal(err)
	}
	return out.QueueURL
}

func TestSQSSendReceiveDeleteAgainstLocalStack(t *testing.T) {
	cfg := localstack(t)
	qurl := createQueue(t, cfg, fmt.Sprintf("perfuse-test-%d", time.Now().UnixNano()), nil)
	q, err := NewSQS(cfg, qurl)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	msg := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|C1|P|2.5\rPID|1||123^^^H^MR\r"
	if _, err := q.Send(ctx, msg, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := q.Receive(ctx, 10, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != msg || got[0].ReceiveCount != 1 {
		t.Fatalf("received %+v", got)
	}
	if err := q.Delete(ctx, got[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
	again, err := q.Receive(ctx, 10, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("a deleted message came back: %+v", again)
	}
}

func TestSQSFIFOGroupsAndDeduplicatesAgainstLocalStack(t *testing.T) {
	cfg := localstack(t)
	qurl := createQueue(t, cfg, fmt.Sprintf("perfuse-test-%d.fifo", time.Now().UnixNano()), map[string]string{"FifoQueue": "true"})
	q, _ := NewSQS(cfg, qurl)
	ctx := context.Background()
	if !q.FIFO() {
		t.Fatal("a .fifo queue was not recognised as FIFO")
	}
	for _, id := range []string{"C1", "C1", "C2"} {
		if _, err := q.Send(ctx, "message "+id, "patient-123", id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := q.Receive(ctx, 10, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "message C1" || got[1].Body != "message C2" {
		t.Fatalf("FIFO did not deduplicate by control id and keep order: %+v", got)
	}
}

func TestSNSPublishReachesASubscribedQueueAgainstLocalStack(t *testing.T) {
	cfg := localstack(t)
	ctx := context.Background()

	// A topic and a queue subscribed to it, so the publish is checked by where it arrives rather than by a 200.
	call := func(form url.Values) string {
		ep, _ := awsv4.Endpoint(cfg.Endpoint, "sns", cfg.Region)
		form.Set("Version", "2010-03-31")
		body := []byte(awsv4.CanonicalQuery(form))
		req, _ := http.NewRequest(http.MethodPost, ep.String(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		req.Host = ep.Host
		awsv4.Sign(req, body, cfg.Credentials, cfg.Region, "sns", time.Now())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var b bytes.Buffer
		_, _ = b.ReadFrom(res.Body)
		return b.String()
	}
	name := fmt.Sprintf("perfuse-test-%d", time.Now().UnixNano())
	created := call(url.Values{"Action": {"CreateTopic"}, "Name": {name}})
	arn := created[strings.Index(created, "<TopicArn>")+10 : strings.Index(created, "</TopicArn>")]

	qurl := createQueue(t, cfg, name, nil)
	queueARN := "arn:aws:sqs:us-east-1:000000000000:" + name
	call(url.Values{"Action": {"Subscribe"}, "TopicArn": {arn}, "Protocol": {"sqs"}, "Endpoint": {queueARN},
		"Attributes.entry.1.key": {"RawMessageDelivery"}, "Attributes.entry.1.value": {"true"}})

	topic, err := NewSNS(cfg, arn)
	if err != nil {
		t.Fatal(err)
	}
	id, err := topic.Publish(ctx, "MSH|^~\\&|A|B|C|D|20260101||ORU^R01|C9|P|2.5\r", "ORU", "", "")
	if err != nil || id == "" {
		t.Fatalf("publish: %q %v", id, err)
	}

	q, _ := NewSQS(cfg, qurl)
	got, err := q.Receive(ctx, 1, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Body, "ORU^R01|C9") {
		raw, _ := json.Marshal(got)
		t.Fatalf("the published message did not reach the subscribed queue: %s", raw)
	}
}
