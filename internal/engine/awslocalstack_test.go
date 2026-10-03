package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsmsg"
	"github.com/biodream-llc/perfuse/internal/awsv4"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/s3put"
)

// Channels reading from and writing to SQS, SNS and S3, run against LocalStack and checked at the far end: the message is looked for
// in the queue or bucket it should have reached, not inferred from the channel's counters. Skipped without LocalStack.

func localstackBase(t *testing.T) string {
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
	return base
}

func lsConfig(base string) awsmsg.Config {
	return awsmsg.Config{Region: "us-east-1", Endpoint: base, Credentials: awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}}
}

// lsQueue creates a queue by posting the JSON protocol directly, the way the client does.
func lsQueue(t *testing.T, base, name string) *awsmsg.SQS {
	t.Helper()
	body := fmt.Sprintf(`{"QueueName":%q}`, name)
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	req.Host = strings.TrimPrefix(base, "http://")
	awsv4.Sign(req, []byte(body), lsConfig(base).Credentials, "us-east-1", "sqs", time.Now())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	q, err := awsmsg.NewSQS(lsConfig(base), out.QueueURL)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func lsBucket(t *testing.T, base, name string) *s3put.Client {
	t.Helper()
	c, err := s3put.New(s3put.Config{Bucket: name, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test", Endpoint: base,
		PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/"+name, nil)
	req.Host = strings.TrimPrefix(base, "http://")
	awsv4.Sign(req, nil, awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, "us-east-1", "s3", time.Now())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return c
}

func runChannel(t *testing.T, yaml string) *Channel {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(yaml), "aws.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ch.Stop(ctx)
	})
	return ch
}

const lsADT = "MSH|^~\\&|WARD|H|REG|H|20260101120000||ADT^A01|C1|P|2.5\rPID|1||555^^^H^MR||DOE^JANE\r"

func TestAnSQSChannelDeliversToSQSAndAnAthenaArchiveAgainstLocalStack(t *testing.T) {
	base := localstackBase(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	in := lsQueue(t, base, fmt.Sprintf("perfuse-in-%d", stamp))
	out := lsQueue(t, base, fmt.Sprintf("perfuse-out-%d", stamp))
	archive := lsBucket(t, base, fmt.Sprintf("perfuse-archive-%d", stamp))

	inURL := strings.TrimSuffix(base, "/") + fmt.Sprintf("/000000000000/perfuse-in-%d", stamp)
	outURL := strings.TrimSuffix(base, "/") + fmt.Sprintf("/000000000000/perfuse-out-%d", stamp)
	creds := fmt.Sprintf("region: us-east-1\n    endpoint: %s\n    access_key_id: test\n    secret_access_key: test", base)
	runChannel(t, fmt.Sprintf(`
name: from-sqs
source:
  type: sqs
  sqs:
    queue_url: %s
    wait_seconds: 1
    %s
destinations:
  - name: to-sqs
    type: sqs
    sqs:
      queue_url: %s
      %s
  - name: archive
    type: s3
    s3:
      bucket: perfuse-archive-%d
      region: us-east-1
      endpoint: %s
      path_style: true
      access_key_id: test
      secret_access_key: test
      storage_class: glacier_ir
      format: ndjson
`, inURL, creds, outURL, strings.ReplaceAll(creds, "\n    ", "\n      "), stamp, base))

	if _, err := in.Send(ctx, lsADT, "", ""); err != nil {
		t.Fatal(err)
	}

	var got []awsmsg.Message
	for i := 0; i < 10 && len(got) == 0; i++ {
		got, _ = out.Receive(ctx, 1, 2, 0)
	}
	if len(got) != 1 || got[0].Body != lsADT {
		t.Fatalf("the destination queue holds %+v", got)
	}

	// Deleted from the source queue once handled.
	if left, _ := in.Receive(ctx, 10, 1, 0); len(left) != 0 {
		t.Errorf("the source message was not deleted after handling: %+v", left)
	}

	objs, err := archive.List(ctx, "archive/dt="+time.Now().UTC().Format("2006-01-02")+"/", "", 10)
	if err != nil || len(objs) != 1 {
		all, _ := archive.List(ctx, "", "", 10)
		t.Fatalf("the Athena archive holds %+v (all: %+v) %v", objs, all, err)
	}
	body, _ := archive.Get(ctx, objs[0].Key, 1<<20)
	var rec AthenaRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("not one JSON line: %q", body)
	}
	if rec.MessageType != "ADT" || rec.TriggerEvent != "A01" || rec.PatientID != "555" || rec.ControlID != "C1" || rec.Message != lsADT {
		t.Errorf("the record: %+v", rec)
	}
}

func TestAnS3ChannelMovesWhatItHandledAndQuarantinesWhatItCouldNotAgainstLocalStack(t *testing.T) {
	base := localstackBase(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	bucket := lsBucket(t, base, fmt.Sprintf("perfuse-drop-%d", stamp))
	out := lsQueue(t, base, fmt.Sprintf("perfuse-s3out-%d", stamp))
	outURL := strings.TrimSuffix(base, "/") + fmt.Sprintf("/000000000000/perfuse-s3out-%d", stamp)

	if err := bucket.Put(ctx, "inbound/one.hl7", []byte(lsADT), ""); err != nil {
		t.Fatal(err)
	}
	if err := bucket.Put(ctx, "inbound/notes.txt", []byte("not for us"), ""); err != nil {
		t.Fatal(err)
	}
	if err := bucket.Put(ctx, "inbound/broken.hl7", []byte("this is not hl7"), ""); err != nil {
		t.Fatal(err)
	}

	runChannel(t, fmt.Sprintf(`
name: from-s3
source:
  type: s3
  s3:
    bucket: perfuse-drop-%d
    prefix: inbound/
    suffix: .hl7
    poll_interval: 1s
    path_style: true
    region: us-east-1
    endpoint: %s
    access_key_id: test
    secret_access_key: test
destinations:
  - name: to-sqs
    type: sqs
    sqs:
      queue_url: %s
      region: us-east-1
      endpoint: %s
      access_key_id: test
      secret_access_key: test
`, stamp, base, outURL, base))

	var got []awsmsg.Message
	for i := 0; i < 10 && len(got) == 0; i++ {
		got, _ = out.Receive(ctx, 10, 2, 0)
	}
	if len(got) != 1 || got[0].Body != lsADT {
		t.Fatalf("delivered: %+v", got)
	}

	time.Sleep(time.Second)
	keys := func(prefix string) []string {
		objs, _ := bucket.List(ctx, prefix, "", 100)
		var k []string
		for _, o := range objs {
			k = append(k, o.Key)
		}
		return k
	}
	if k := keys("inbound/"); len(k) != 1 || k[0] != "inbound/notes.txt" {
		t.Errorf("left under inbound/: %v (only the .txt the suffix excludes should remain)", k)
	}
	if k := keys("processed/"); len(k) != 1 || k[0] != "processed/one.hl7" {
		t.Errorf("processed/: %v", k)
	}
	if k := keys("error/"); len(k) != 1 || k[0] != "error/broken.hl7" {
		t.Errorf("error/: %v", k)
	}
}
