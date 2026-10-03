package awsv4

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS's own worked example from the Signature Version 4 documentation ("Create a signed AWS API request"), so this is checked
// against AWS's arithmetic rather than against itself.
func TestTheDocumentedIAMExampleSignsToAWSsSignature(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Host = "iam.amazonaws.com"
	now, _ := time.Parse("20060102T150405Z", "20150830T123600Z")

	Sign(req, nil, Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		"us-east-1", "iam", now)

	want := "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, "Signature="+want) {
		t.Errorf("signature differs from AWS's documented example:\n%s", got)
	}
}

func TestTheCanonicalQueryEncodesASpaceAsPercent20(t *testing.T) {
	q := url.Values{"prefix": {"lab results/"}, "list-type": {"2"}}
	if got := CanonicalQuery(q); got != "list-type=2&prefix=lab%20results%2F" {
		t.Errorf("got %q", got)
	}
}

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
	got := EscapePath("2026/08/20/adt-C1.hl7")
	if got != "2026/08/20/adt-C1.hl7" {
		t.Errorf("escaped = %q, want the slashes preserved", got)
	}
}

func TestAwkwardCharactersInAKeyAreEscaped(t *testing.T) {
	got := EscapePath("a b+c.hl7")
	if got != "a%20b%2Bc.hl7" {
		t.Errorf("escaped = %q, want spaces and plus encoded", got)
	}
}
