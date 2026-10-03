package s3put

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// Against LocalStack's S3, the read side the S3 source needs and the storage class an archive uses. Skipped when it is not running.
func localstackClient(t *testing.T, storageClass string) *Client {
	t.Helper()
	base := os.Getenv("PERFUSE_LOCALSTACK")
	if base == "" {
		base = "http://127.0.0.1:4566"
	}
	if res, err := http.Get(base + "/_localstack/health"); err != nil {
		t.Skipf("no LocalStack on %s (%v). Start it with ./scripts/interop-up.sh", base, err)
	} else {
		_ = res.Body.Close()
	}
	bucket := fmt.Sprintf("perfuse-test-%d", time.Now().UnixNano())
	c, err := New(Config{Bucket: bucket, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test", Endpoint: base,
		PathStyle: true, StorageClass: storageClass})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.do(context.Background(), http.MethodPut, "", nil, nil, nil); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}
	return c
}

func TestListGetCopyDeleteAgainstLocalStack(t *testing.T) {
	c := localstackClient(t, "")
	ctx := context.Background()
	for _, k := range []string{"in/b.hl7", "in/a b.hl7", "other/c.hl7"} {
		if err := c.Put(ctx, k, []byte("body of "+k), "application/hl7-v2"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.List(ctx, "in/", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "in/a b.hl7" || got[1].Key != "in/b.hl7" || got[0].Size == 0 {
		t.Fatalf("listing: %+v", got)
	}
	after, _ := c.List(ctx, "in/", "in/a b.hl7", 100)
	if len(after) != 1 || after[0].Key != "in/b.hl7" {
		t.Errorf("start-after: %+v", after)
	}
	body, err := c.Get(ctx, "in/a b.hl7", 1<<20)
	if err != nil || string(body) != "body of in/a b.hl7" {
		t.Fatalf("get: %q %v", body, err)
	}
	if _, err := c.Get(ctx, "in/a b.hl7", 4); err == nil {
		t.Error("an object over the limit was read")
	}
	if err := c.Copy(ctx, "in/a b.hl7", "done/a b.hl7"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "in/a b.hl7"); err != nil {
		t.Fatal(err)
	}
	if left, _ := c.List(ctx, "in/", "", 100); len(left) != 1 {
		t.Errorf("after the move: %+v", left)
	}
	if moved, _ := c.Get(ctx, "done/a b.hl7", 1<<20); string(moved) != "body of in/a b.hl7" {
		t.Errorf("the copy holds %q", moved)
	}
}

func TestTheStorageClassReachesTheObjectAgainstLocalStack(t *testing.T) {
	c := localstackClient(t, "GLACIER_IR")
	ctx := context.Background()
	if err := c.Put(ctx, "archive/x.hl7", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	endpoint, host, _ := c.objectURL("archive/x.hl7")
	req, _ := http.NewRequest(http.MethodHead, endpoint, nil)
	req.Host = host
	_ = c.sign(req, nil, time.Now())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got := res.Header.Get("X-Amz-Storage-Class"); got != "GLACIER_IR" {
		t.Errorf("storage class on the stored object = %q", got)
	}
}
