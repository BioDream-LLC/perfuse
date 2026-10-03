// Package awsv4 signs requests to AWS with Signature Version 4, using only the standard library.
//
// It began inside s3put. SQS, SNS and reading from S3 need the same signature with a different service name and with a query string,
// and a second copy of the signing code would be a second place for the one bug that matters - a signature mismatch reads exactly like
// wrong credentials. So it lives here, checked against AWS's published test vectors, and every AWS connector uses it.
//
// Credentials are static keys or the standard environment variables. There is deliberately no instance-metadata credential chain: it
// means IMDS calls, caching and refresh, which is where a small implementation stops being small, and Perfuse refuses metadata
// addresses as destinations by default anyway.
package awsv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials are the keys a request is signed with.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is set for temporary credentials.
	SessionToken string
}

// Valid reports whether both halves of the key pair are present.
func (c Credentials) Valid() bool {
	return strings.TrimSpace(c.AccessKeyID) != "" && strings.TrimSpace(c.SecretAccessKey) != ""
}

// UnsignedPayload is the payload hash S3 accepts in place of a body hash.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// Sign adds the Signature Version 4 headers to req.
//
// body is the exact request body, or nil for none. req.Host must already be set to the host being signed, because Go does not put
// Host in the header map and leaving it out of the signature is the commonest way a hand-written implementation fails.
func Sign(req *http.Request, body []byte, creds Credentials, region, service string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := SHA256Hex(body)

	req.Header.Set("X-Amz-Date", amzDate)
	if service == "s3" {
		// S3 requires the payload hash as a header; the other services accept it but do not ask for it.
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	signedHeaders, canonicalHeaders := canonicalizeHeaders(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		CanonicalQuery(req.URL.Query()),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		SHA256Hex([]byte(canonicalRequest)),
	}, "\n")

	key := signingKey(creds.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, scope, signedHeaders, signature))
}

// signingKey derives the request key by the four-step chain the specification describes.
func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func canonicalizeHeaders(req *http.Request) (string, string) {
	headers := map[string]string{"host": req.Host}

	for name, values := range req.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "user-agent", "content-length":
			// Authorization is what is being produced; the other two are altered in transit by clients and proxies.
			continue
		}
		trimmed := make([]string, 0, len(values))
		for _, v := range values {
			trimmed = append(trimmed, strings.Join(strings.Fields(v), " "))
		}
		headers[lower] = strings.Join(trimmed, ",")
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
	if p := u.EscapedPath(); p != "" {
		return p
	}
	return "/"
}

// CanonicalQuery is the query string as the signature sees it: keys sorted, every key and value percent-encoded with the RFC 3986
// unreserved set. url.Values.Encode is close and wrong - it writes a space as "+" - which is enough for S3 to refuse a listing with a
// prefix containing a space.
func CanonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, Escape(k)+"="+Escape(v))
		}
	}
	return strings.Join(parts, "&")
}

// Escape percent-encodes everything outside the RFC 3986 unreserved set.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// EscapePath encodes an S3 object key for a URL path, leaving its slashes alone because they are the key's own structure.
func EscapePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = Escape(p)
	}
	return strings.Join(parts, "/")
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// SHA256Hex is the lowercase hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Endpoint resolves where a service lives: an override such as LocalStack or a VPC endpoint, or AWS's own host for the region.
func Endpoint(override, service, region string) (*url.URL, error) {
	if strings.TrimSpace(override) == "" {
		return &url.URL{Scheme: "https", Host: service + "." + region + ".amazonaws.com", Path: "/"}, nil
	}
	u, err := url.Parse(strings.TrimSpace(override))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("the %s endpoint %q is not a URL with a host", service, override)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}
