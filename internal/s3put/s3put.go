// Package s3put writes objects to S3 using only the standard library.
//
// The obvious way to talk to S3 is the AWS SDK, and the SDK is enormous. This project sells a static
// binary with eight direct dependencies and a bill of materials that fits on one screen - that is a real
// argument with the person who reviews software before a hospital installs it, and trading it away for
// one connector would be a bad bargain.
//
// What S3 actually needs is an HTTP PUT and a signature. Signature Version 4 is fully specified, it is
// deterministic, and AWS publishes test vectors for it. So it is implemented here: about two hundred lines
// against crypto/hmac and net/http, with no new dependency and nothing to patch when the SDK has an
// advisory.
//
// # What this deliberately does not do
//
// No multipart upload, no retries of its own, no credential chain beyond static keys and the standard
// environment variables. An HL7 message is kilobytes, so multipart solves a problem this does not have;
// the engine already retries; and an instance-role credential chain would mean IMDS calls, caching and
// refresh logic, which is where a small implementation stops being small. If somebody needs those, the
// honest answer is a file destination and a sync tool, not a half-built SDK.
//
// It works with anything speaking the S3 API - MinIO, Ceph, Wasabi, Backblaze - because it is just signed
// HTTP. That is a side effect worth having, since a hospital object store is often not AWS.
package s3put

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config describes where and how to write.
type Config struct {
	// Bucket is the bucket name.
	Bucket string

	// Region is the AWS region, for example eu-west-2. Required even for non-AWS endpoints, because it
	// forms part of the signature.
	Region string

	// AccessKeyID and SecretAccessKey are static credentials.
	AccessKeyID     string
	SecretAccessKey string

	// SessionToken is set when using temporary credentials.
	SessionToken string

	// Endpoint overrides the AWS host, for MinIO or another S3-compatible store. Empty means AWS.
	Endpoint string

	// PathStyle addresses the bucket as a path rather than a subdomain. Required by most
	// S3-compatible stores and by AWS for buckets whose names are not valid hostnames.
	PathStyle bool

	// ServerSideEncryption sets the x-amz-server-side-encryption header, for example AES256.
	//
	// Worth a field of its own rather than leaving it to a bucket policy: a bucket policy that rejects
	// unencrypted uploads produces a 403 with no explanation of what was wrong, and the person reading
	// it is looking at an integration engine, not at IAM.
	ServerSideEncryption string

	// HTTPClient is used for the request. Defaults to a client with a sensible timeout.
	HTTPClient *http.Client
}

// Client puts objects into one bucket.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("an S3 destination needs a bucket")
	}
	if strings.TrimSpace(cfg.Region) == "" {
		// Refused rather than defaulted. A wrong region produces a signature mismatch, which reads as
		// "your credentials are wrong" and sends people to rotate keys that were never the problem.
		return nil, fmt.Errorf("an S3 destination needs a region, because it forms part of the signature")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("an S3 destination needs an access key id and secret access key")
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	return &Client{cfg: cfg, http: client}, nil
}

// Put writes one object.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	key = strings.TrimPrefix(key, "/")
	if key == "" {
		return fmt.Errorf("an S3 object needs a key")
	}

	endpoint, host, err := c.objectURL(key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.cfg.ServerSideEncryption != "" {
		req.Header.Set("X-Amz-Server-Side-Encryption", c.cfg.ServerSideEncryption)
	}
	req.Header.Set("Host", host)
	req.Host = host
	req.ContentLength = int64(len(body))

	if err := c.sign(req, body, time.Now().UTC()); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// S3 errors carry the reason in an XML body, and the status alone is nearly useless - a 403 could be
	// a bad key, a missing permission, or a bucket policy refusing unencrypted uploads. Reading a bounded
	// amount of it and putting it in the error is the difference between a fixable problem and an
	// afternoon of guessing.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("S3 refused the object with %s: %s",
		resp.Status, strings.TrimSpace(collapse(string(detail))))
}

// objectURL builds the request URL and the host to sign.
func (c *Client) objectURL(key string) (string, string, error) {
	scheme := "https"
	host := c.cfg.Bucket + ".s3." + c.cfg.Region + ".amazonaws.com"
	prefix := ""

	if c.cfg.Endpoint != "" {
		parsed, err := url.Parse(c.cfg.Endpoint)
		if err != nil {
			return "", "", fmt.Errorf("the S3 endpoint is not a URL: %w", err)
		}
		if parsed.Scheme != "" {
			scheme = parsed.Scheme
		}
		host = parsed.Host
		if host == "" {
			host = parsed.Path
		}
		if c.cfg.PathStyle {
			prefix = "/" + c.cfg.Bucket
		} else {
			host = c.cfg.Bucket + "." + host
		}
	} else if c.cfg.PathStyle {
		host = "s3." + c.cfg.Region + ".amazonaws.com"
		prefix = "/" + c.cfg.Bucket
	}

	return scheme + "://" + host + prefix + "/" + escapePath(key), host, nil
}

// sign adds the Signature Version 4 authorization header.
//
// Implemented from the specification rather than copied from an SDK, and checked against AWS's published
// test vectors, because a signing bug produces a 403 that looks exactly like wrong credentials.
func (c *Client) sign(req *http.Request, body []byte, now time.Time) error {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := sha256Hex(body)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.cfg.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.cfg.SessionToken)
	}

	signedHeaders, canonicalHeaders := canonicalizeHeaders(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, c.cfg.Region, "s3", "aws4_request"}, "/")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	key := signingKey(c.cfg.SecretAccessKey, dateStamp, c.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKeyID, scope, signedHeaders, signature))

	return nil
}

// signingKey derives the request key by the four-step chain the specification describes.
func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// canonicalizeHeaders builds the signed header list and the canonical header block.
//
// Host is included explicitly because Go does not put it in Header, and omitting it from the signature is
// the single commonest way a hand-written SigV4 implementation fails.
func canonicalizeHeaders(req *http.Request) (string, string) {
	headers := map[string]string{"host": req.Host}

	for name, values := range req.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "user-agent", "content-length":
			// Excluded: authorization is what is being produced, and the other two are altered in
			// transit by clients and proxies, which would invalidate the signature.
			continue
		}
		headers[lower] = strings.Join(trimAll(values), ",")
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var block strings.Builder
	for _, name := range names {
		block.WriteString(name)
		block.WriteByte(':')
		block.WriteString(headers[name])
		block.WriteByte('\n')
	}

	return strings.Join(names, ";"), block.String()
}

func canonicalURI(u *url.URL) string {
	if u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

// escapePath encodes an object key for a URL path.
//
// Slashes are left alone because they are the key's own structure, and S3 keys routinely contain them to
// look like directories. Everything else outside the unreserved set is percent-encoded, which is what the
// signature calculation assumes.
func escapePath(key string) string {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		ch := key[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b.WriteByte(ch)
		case ch == '-' || ch == '_' || ch == '.' || ch == '~' || ch == '/':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strings.TrimSpace(v))
	}
	return out
}

// collapse turns an XML error body into one line.
//
// S3's errors are pretty-printed XML, and a multi-line error in a log makes the next line look like a
// separate event.
func collapse(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
