package s3put

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A signing bug produces a 403 that looks exactly like wrong credentials, and somebody then rotates keys
// that were never the problem. So signing is tested against AWS's own published derivation vector rather
// than against my own output, which would only prove the code agrees with itself.

func TestTheSigningKeyMatchesTheAWSPublishedVector(t *testing.T) {
	// From the AWS documentation's worked example of deriving a signing key. If this passes, the four-step
	// chain is right; if it fails, everything else about the signature is untrustworthy.
	const (
		secret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		dateStamp = "20150830"
		region    = "us-east-1"
		service   = "iam"
		want      = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	)

	got := hex.EncodeToString(signingKey(secret, dateStamp, region, service))
	if got != want {
		t.Errorf("signing key = %s\nwant           %s", got, want)
	}
}

func TestHostIsAlwaysSigned(t *testing.T) {
	// Go does not put Host in Header, and omitting it from the signature is the single commonest way a
	// hand-written SigV4 implementation fails.
	req, err := http.NewRequest(http.MethodPut, "https://bucket.s3.eu-west-2.amazonaws.com/a/b.hl7", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "bucket.s3.eu-west-2.amazonaws.com"

	signed, canonical := canonicalizeHeaders(req)
	if !strings.Contains(signed, "host") {
		t.Errorf("signed headers do not include host: %q", signed)
	}
	if !strings.Contains(canonical, "host:bucket.s3.eu-west-2.amazonaws.com") {
		t.Errorf("the canonical headers do not carry the host: %q", canonical)
	}
}

func TestVolatileHeadersAreNotSigned(t *testing.T) {
	// Content-Length and User-Agent get rewritten by clients and proxies. Signing them means a request
	// that verified locally fails in production, which is the worst possible place to discover it.
	req, err := http.NewRequest(http.MethodPut, "https://b.s3.eu-west-2.amazonaws.com/k", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "b.s3.eu-west-2.amazonaws.com"
	req.Header.Set("User-Agent", "something")
	req.Header.Set("Content-Length", "12")
	req.Header.Set("Authorization", "should not be signed")

	signed, _ := canonicalizeHeaders(req)
	for _, bad := range []string{"user-agent", "content-length", "authorization"} {
		if strings.Contains(signed, bad) {
			t.Errorf("%s should not be signed: %q", bad, signed)
		}
	}
}

func TestKeySlashesSurviveEscaping(t *testing.T) {
	// Slashes are the key's own structure - S3 keys routinely contain them to look like directories - and
	// escaping them would create an object with a literal %2F in its name that nothing else can find.
	got := escapePath("2026/08/20/adt-C1.hl7")
	if got != "2026/08/20/adt-C1.hl7" {
		t.Errorf("escaped = %q, want the slashes preserved", got)
	}
}

func TestAwkwardCharactersInAKeyAreEscaped(t *testing.T) {
	got := escapePath("a b+c.hl7")
	if got != "a%20b%2Bc.hl7" {
		t.Errorf("escaped = %q, want spaces and plus encoded", got)
	}
}

func TestPutSendsASignedRequest(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotSHA    string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSHA = r.Header.Get("X-Amz-Content-Sha256")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(Config{
		Bucket:          "traffic",
		Region:          "eu-west-2",
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secret",
		Endpoint:        srv.URL,
		PathStyle:       true,
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("MSH|^~\\&|A|B|C|D|20260820||ADT^A01|C1|P|2.5.1\r")
	if err := c.Put(context.Background(), "2026/08/20/C1.hl7", body, "application/hl7-v2"); err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/traffic/2026/08/20/C1.hl7" {
		t.Errorf("path = %q, want the bucket then the key", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotSHA != sha256Hex(body) {
		t.Errorf("content sha = %q, want the body's hash", gotSHA)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %q, want the message unchanged", gotBody)
	}
}

func TestAnS3ErrorCarriesTheReasonNotJustTheStatus(t *testing.T) {
	// A 403 could be a bad key, a missing permission, or a bucket policy refusing unencrypted uploads.
	// The status alone sends somebody to rotate credentials that were never the problem.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error>\n  <Code>AccessDenied</Code>\n" +
			"  <Message>Server side encryption is required</Message>\n</Error>"))
	}))
	defer srv.Close()

	c, err := New(Config{
		Bucket: "b", Region: "eu-west-2",
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, PathStyle: true, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = c.Put(context.Background(), "k.hl7", []byte("x"), "")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "encryption is required") {
		t.Errorf("the error loses the reason: %v", err)
	}
	// And it is one line, because a multi-line error in a log makes the next line look like a separate
	// event.
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the error spans several lines: %q", err)
	}
}

func TestAMissingRegionIsRefusedRatherThanDefaulted(t *testing.T) {
	// A wrong region produces a signature mismatch, which reads as "your credentials are wrong".
	_, err := New(Config{Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"})
	if err == nil {
		t.Fatal("a destination with no region was accepted")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("the error does not explain why the region matters: %v", err)
	}
}

func TestMissingCredentialsAreRefused(t *testing.T) {
	_, err := New(Config{Bucket: "b", Region: "eu-west-2"})
	if err == nil {
		t.Fatal("a destination with no credentials was accepted")
	}
}

func TestVirtualHostStyleIsTheDefaultForAWS(t *testing.T) {
	c, err := New(Config{
		Bucket: "traffic", Region: "eu-west-2",
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint, host, err := c.objectURL("a.hl7")
	if err != nil {
		t.Fatal(err)
	}
	if host != "traffic.s3.eu-west-2.amazonaws.com" {
		t.Errorf("host = %q", host)
	}
	if endpoint != "https://traffic.s3.eu-west-2.amazonaws.com/a.hl7" {
		t.Errorf("endpoint = %q", endpoint)
	}
}

func TestServerSideEncryptionIsSentWhenAsked(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Amz-Server-Side-Encryption")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(Config{
		Bucket: "b", Region: "eu-west-2",
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, PathStyle: true, HTTPClient: srv.Client(),
		ServerSideEncryption: "AES256",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(context.Background(), "k.hl7", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	if got != "AES256" {
		t.Errorf("encryption header = %q, want AES256", got)
	}
}

func TestSigningIsDeterministicForAGivenTime(t *testing.T) {
	// Two identical requests at the same instant must produce the same signature. If they do not,
	// something unordered has crept into the canonical form and failures will be intermittent - the worst
	// kind to diagnose in a hospital.
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	c, err := New(Config{
		Bucket: "b", Region: "eu-west-2",
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	var signatures []string
	for i := 0; i < 6; i++ {
		req, err := http.NewRequest(http.MethodPut, "https://b.s3.eu-west-2.amazonaws.com/k.hl7", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "b.s3.eu-west-2.amazonaws.com"
		req.Header.Set("Content-Type", "application/hl7-v2")
		if err := c.sign(req, []byte("body"), at); err != nil {
			t.Fatal(err)
		}
		signatures = append(signatures, req.Header.Get("Authorization"))
	}

	for i := 1; i < len(signatures); i++ {
		if signatures[i] != signatures[0] {
			t.Fatalf("signature changed between runs:\n%s\n%s", signatures[0], signatures[i])
		}
	}
}
