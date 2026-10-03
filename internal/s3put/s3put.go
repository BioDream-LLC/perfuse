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
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
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

	// StorageClass sets x-amz-storage-class: STANDARD_IA, ONEZONE_IA, INTELLIGENT_TIERING, GLACIER_IR, GLACIER, DEEP_ARCHIVE. Empty
	// is STANDARD. This is how an archive goes to Glacier: there is no Glacier API to call, only a storage class on the object.
	StorageClass string

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
	if c.cfg.StorageClass != "" {
		req.Header.Set("X-Amz-Storage-Class", c.cfg.StorageClass)
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

// sign adds the Signature Version 4 authorization header, through the signer every AWS connector shares.
func (c *Client) sign(req *http.Request, body []byte, now time.Time) error {
	awsv4.Sign(req, body, awsv4.Credentials{
		AccessKeyID: c.cfg.AccessKeyID, SecretAccessKey: c.cfg.SecretAccessKey, SessionToken: c.cfg.SessionToken,
	}, c.cfg.Region, "s3", now)
	return nil
}

func escapePath(key string) string { return awsv4.EscapePath(key) }

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

// StorageClasses are the values S3 accepts for x-amz-storage-class on a PUT.
var StorageClasses = []string{"STANDARD", "STANDARD_IA", "ONEZONE_IA", "INTELLIGENT_TIERING", "GLACIER_IR", "GLACIER",
	"DEEP_ARCHIVE"}

// Object is one entry in a listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// List returns up to max objects under prefix, in key order, starting after the given key. S3 returns keys in UTF-8 binary order,
// which is what makes "start after the last one read" a correct cursor.
func (c *Client) List(ctx context.Context, prefix, startAfter string, max int) ([]Object, error) {
	q := url.Values{"list-type": {"2"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if startAfter != "" {
		q.Set("start-after", startAfter)
	}
	if max > 0 {
		q.Set("max-keys", fmt.Sprint(max))
	}
	body, err := c.do(ctx, http.MethodGet, "", q, nil, nil)
	if err != nil {
		return nil, err
	}

	var res struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("the S3 listing could not be read: %w", err)
	}
	out := make([]Object, 0, len(res.Contents))
	for _, o := range res.Contents {
		t, _ := time.Parse(time.RFC3339, o.LastModified)
		out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: t, ETag: strings.Trim(o.ETag, `"`)})
	}
	return out, nil
}

// Get reads one object, refusing anything over limit bytes.
func (c *Client) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	body, err := c.doLimit(ctx, http.MethodGet, key, nil, nil, nil, limit)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// Delete removes one object.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodDelete, key, nil, nil, nil)
	return err
}

// Copy copies an object within the bucket, which is how S3 moves one: copy, then delete.
func (c *Client) Copy(ctx context.Context, from, to string) error {
	_, err := c.do(ctx, http.MethodPut, to, nil, nil, map[string]string{
		"X-Amz-Copy-Source": "/" + c.cfg.Bucket + "/" + awsv4.EscapePath(strings.TrimPrefix(from, "/")),
	})
	return err
}

func (c *Client) do(ctx context.Context, method, key string, q url.Values, body []byte, headers map[string]string) ([]byte, error) {
	return c.doLimit(ctx, method, key, q, body, headers, 16<<20)
}

func (c *Client) doLimit(ctx context.Context, method, key string, q url.Values, body []byte, headers map[string]string,
	limit int64) ([]byte, error) {
	endpoint, host, err := c.objectURL(strings.TrimPrefix(key, "/"))
	if err != nil {
		return nil, err
	}
	if key == "" {
		// The bucket itself: objectURL ends in "/" with an empty key, which is the listing's path.
		endpoint = strings.TrimSuffix(endpoint, "/") + "/"
	}
	if len(q) > 0 {
		endpoint += "?" + awsv4.CanonicalQuery(q)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Host = host
	req.ContentLength = int64(len(body))
	if err := c.sign(req, body, time.Now().UTC()); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("S3 refused the %s with %s: %s", strings.ToLower(method), resp.Status,
			strings.TrimSpace(collapse(string(data[:min(len(data), 2048)]))))
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the object %s is larger than the %d bytes allowed", key, limit)
	}
	return data, nil
}
