package config

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/residency"
)

func TestAChannelSendingOutsideTheResidencyIsRefused(t *testing.T) {
	p, err := residency.Parse("eea,uk")
	if err != nil {
		t.Fatal(err)
	}
	prev := residency.SetDefault(p)
	defer residency.SetDefault(prev)
	ch := func(dest string) error {
		_, err := Load(strings.NewReader("name: c\nsource:\n  type: mllp\n  listen: \":2575\"\ndestinations:\n"+dest), "c.yaml")
		return err
	}
	if err := ch("  - name: bucket\n    type: s3\n    s3:\n      bucket: b\n      region: us-east-1\n"); err == nil || !strings.Contains(err.Error(), "us-east-1, outside") {
		t.Fatalf("an S3 bucket in Virginia: %v", err)
	}
	if err := ch("  - name: bucket\n    type: s3\n    s3:\n      bucket: b\n      region: eu-central-1\n      access_key_id: a\n      secret_access_key: s\n"); err != nil {
		t.Fatalf("Frankfurt: %v", err)
	}
	// MinIO on the site's own network: no region to judge.
	if err := ch("  - name: bucket\n    type: s3\n    s3:\n      bucket: b\n      region: us-east-1\n      access_key_id: a\n      secret_access_key: s\n      endpoint: http://minio.internal:9000\n      path_style: true\n"); err != nil {
		t.Fatalf("MinIO: %v", err)
	}
	// An HTTP destination aimed at an AWS host in another region.
	if err := ch("  - name: api\n    type: http\n    http:\n      url: https://abc.execute-api.us-west-2.amazonaws.com/prod\n"); err == nil || !strings.Contains(err.Error(), "us-west-2") {
		t.Fatalf("API Gateway in Oregon: %v", err)
	}
	// A plain host says nothing about where it is.
	if err := ch("  - name: epr\n    type: mllp\n    address: epr.example.org:2575\n"); err != nil {
		t.Fatal(err)
	}
}
