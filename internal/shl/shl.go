// Package shl implements SMART Health Links and SMART Health Cards: the QR codes CMS's "Kill the Clipboard" asks providers to accept at
// check-in, and to hand back with the visit record.
//
// A SMART Health Link (SMART Health Links STU 1) is shlink:/ followed by base64url JSON: a manifest URL, a 256-bit key, and optionally an
// expiry, a label and flags (P passcode, L long-term, U a single file by GET). The receiver posts to the manifest URL and gets back files,
// each a JWE encrypted with that key - alg dir, enc A256GCM, optionally DEFLATE-compressed - holding FHIR JSON, SMART Health Cards, or a
// SMART access token. The server that hosts the files never sees the key: it travels only in the link, which is the patient's.
//
// A SMART Health Card (SMART Health Cards Framework 1.4) is a signed FHIR bundle: a compact JWS, ES256, with a DEFLATE-compressed
// payload naming its issuer, whose public key is at <iss>/.well-known/jwks.json. It travels as a numeric shc:/ QR code or a
// .smart-health-card file.
//
// Only the standard library: crypto/aes, crypto/cipher, crypto/ecdsa and compress/flate cover everything, and a JOSE library would
// bring a much larger surface for two algorithms.
package shl

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Link is a SMART Health Link payload.
type Link struct {
	URL   string `json:"url"`
	Key   string `json:"key"`
	Exp   int64  `json:"exp,omitempty"`
	Flag  string `json:"flag,omitempty"`
	Label string `json:"label,omitempty"`
	V     int    `json:"v,omitempty"`
}

// Has reports whether the link carries a flag.
func (l *Link) Has(flag byte) bool { return strings.IndexByte(l.Flag, flag) >= 0 }

// Expired reports whether the link's exp is in the past. exp is a hint: the server decides, but a receiver should not try a stale link.
func (l *Link) Expired(now time.Time) bool { return l.Exp > 0 && now.Unix() > l.Exp }

// Encode writes the link as shlink:/..., the form that goes in a QR code.
func (l *Link) Encode() string {
	b, _ := json.Marshal(l)
	return "shlink:/" + base64.RawURLEncoding.EncodeToString(b)
}

// ErrNotALink means the text holds no shlink:/ payload.
var ErrNotALink = errors.New("not a SMART Health Link: there is no shlink:/ in it")

// Parse reads a link from what a QR code or a pasted URL gives: shlink:/..., or a viewer URL ending in #shlink:/....
func Parse(text string) (*Link, error) {
	text = strings.TrimSpace(text)
	i := strings.Index(text, "shlink:/")
	if i < 0 {
		return nil, ErrNotALink
	}
	payload := text[i+len("shlink:/"):]
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload, "="))
	if err != nil {
		return nil, fmt.Errorf("the SMART Health Link payload is not base64url: %w", err)
	}
	var l Link
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("the SMART Health Link payload is not JSON: %w", err)
	}
	if l.V > 1 {
		// The spec: a receiver should not proceed with a version it does not know, because a v bump means something that cannot be
		// safely ignored changed.
		return nil, fmt.Errorf("the link is SMART Health Links version %d and this reader knows version 1", l.V)
	}
	if _, err := l.keyBytes(); err != nil {
		return nil, err
	}
	u, err := url.Parse(l.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("the link's url %q is not an http(s) URL", l.URL)
	}
	if l.Has('U') && l.Has('P') {
		return nil, errors.New("the link sets both U and P, which the specification forbids")
	}
	return &l, nil
}

func (l *Link) keyBytes() ([]byte, error) {
	k, err := base64.RawURLEncoding.DecodeString(l.Key)
	if err != nil || len(k) != 32 {
		return nil, errors.New("the link's key is not 32 bytes of base64url, so nothing behind it could be decrypted")
	}
	return k, nil
}

// NewKey makes a random 256-bit key, base64url.
func NewKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewID makes a random 256-bit manifest path segment, which is what the specification asks the manifest URL to carry.
func NewID() string { return NewKey() }

// Encrypt makes a compact JWE: alg dir, enc A256GCM, zip DEF, cty contentType.
func Encrypt(plain []byte, key string, contentType string) (string, error) {
	k, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(k) != 32 {
		return "", errors.New("the key is not 32 bytes of base64url")
	}
	header, _ := json.Marshal(map[string]string{"alg": "dir", "enc": "A256GCM", "cty": contentType, "zip": "DEF"})
	h := base64.RawURLEncoding.EncodeToString(header)

	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestCompression)
	_, _ = w.Write(plain)
	_ = w.Close()

	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, z.Bytes(), []byte(h))
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	enc := base64.RawURLEncoding.EncodeToString
	return h + ".." + enc(iv) + "." + enc(ct) + "." + enc(tag), nil
}

// maxInflated bounds a decompressed file. A JWE is small; a DEFLATE stream can expand a thousand-fold, and a link from a stranger is
// exactly where a decompression bomb would come from.
const maxInflated = 64 << 20

// Decrypt opens a compact JWE made with the link's key, returning its content type and plaintext.
func Decrypt(jwe string, key string) (string, []byte, error) {
	k, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(k) != 32 {
		return "", nil, errors.New("the key is not 32 bytes of base64url")
	}
	parts := strings.Split(strings.TrimSpace(jwe), ".")
	if len(parts) != 5 {
		return "", nil, fmt.Errorf("not a compact JWE: %d parts, want 5", len(parts))
	}
	dec := base64.RawURLEncoding.DecodeString
	headerJSON, err := dec(parts[0])
	if err != nil {
		return "", nil, errors.New("the JWE header is not base64url")
	}
	var header struct {
		Alg, Enc, Cty, Zip string
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "", nil, errors.New("the JWE header is not JSON")
	}
	if header.Alg != "dir" || header.Enc != "A256GCM" {
		return "", nil, fmt.Errorf("the file is encrypted with %s/%s; SMART Health Links use dir/A256GCM", header.Alg, header.Enc)
	}
	if parts[1] != "" {
		return "", nil, errors.New("a dir JWE carries no encrypted key, and this one does")
	}
	iv, err1 := dec(parts[2])
	ct, err2 := dec(parts[3])
	tag, err3 := dec(parts[4])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", nil, errors.New("a part of the JWE is not base64url")
	}
	block, _ := aes.NewCipher(k)
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil || len(iv) == 0 {
		return "", nil, errors.New("the JWE initialisation vector is not usable")
	}
	plain, err := gcm.Open(nil, iv, append(ct, tag...), []byte(parts[0]))
	if err != nil {
		// Said plainly: this is what a wrong key, a tampered file or a file meant for a different link all look like.
		return "", nil, errors.New("the file could not be decrypted with the link's key: it was altered, or belongs to another link")
	}
	if header.Zip == "DEF" {
		r := flate.NewReader(bytes.NewReader(plain))
		defer func() { _ = r.Close() }()
		out, err := io.ReadAll(io.LimitReader(r, maxInflated+1))
		if err != nil {
			return "", nil, fmt.Errorf("the file's compressed content is damaged: %w", err)
		}
		if len(out) > maxInflated {
			return "", nil, errors.New("the file expands past 64 MiB and was refused")
		}
		plain = out
	}
	return header.Cty, plain, nil
}

// File is one decrypted file from a link.
type File struct {
	ContentType string `json:"contentType"`
	Content     []byte `json:"-"`
}

// PasscodeError is a manifest server refusing the passcode.
type PasscodeError struct{ Remaining int }

func (e *PasscodeError) Error() string {
	if e.Remaining >= 0 {
		return fmt.Sprintf("the passcode was refused; %d attempt(s) remain before the link stops working", e.Remaining)
	}
	return "the passcode was refused"
}

// ErrNoLongerValid is a manifest server saying the link has been revoked, has expired, or has used up its attempts.
var ErrNoLongerValid = errors.New("the link is no longer valid: it has expired, been revoked, or run out of passcode attempts")

// Resolver fetches what a link points at.
type Resolver struct {
	// Client makes the requests. It is where a caller puts an egress policy, because the URL comes from a stranger's QR code.
	Client *http.Client
	// Now is for tests.
	Now func() time.Time
}

// maxFetch bounds every response.
const maxFetch = 16 << 20

// Resolve fetches and decrypts every file behind the link. recipient says who is asking, which the specification requires and the
// sharer may see; passcode is needed when the link has the P flag.
func (r *Resolver) Resolve(ctx context.Context, l *Link, recipient, passcode string) ([]File, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if l.Expired(now()) {
		return nil, fmt.Errorf("the link expired at %s", time.Unix(l.Exp, 0).UTC().Format(time.RFC3339))
	}
	if strings.TrimSpace(recipient) == "" {
		return nil, errors.New("a recipient is required: the specification asks every request to say who is receiving the data")
	}
	if l.Has('P') && passcode == "" {
		return nil, errors.New("this link is protected by a passcode, which the patient has")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	if l.Has('U') {
		u, _ := url.Parse(l.URL)
		q := u.Query()
		q.Set("recipient", recipient)
		u.RawQuery = q.Encode()
		jwe, err := get(ctx, client, u.String())
		if err != nil {
			return nil, err
		}
		cty, plain, err := Decrypt(string(jwe), l.Key)
		if err != nil {
			return nil, err
		}
		return []File{{ContentType: cty, Content: plain}}, nil
	}

	body := map[string]any{"recipient": recipient}
	if passcode != "" {
		body["passcode"] = passcode
	}
	reqBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the link's server could not be reached: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxFetch))
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		var e struct {
			RemainingAttempts *int `json:"remainingAttempts"`
		}
		_ = json.Unmarshal(data, &e)
		remaining := -1
		if e.RemainingAttempts != nil {
			remaining = *e.RemainingAttempts
		}
		return nil, &PasscodeError{Remaining: remaining}
	case http.StatusNotFound:
		return nil, ErrNoLongerValid
	default:
		// The body is not echoed: the URL came from a QR code, and repeating what an arbitrary server said is how a fetcher becomes a
		// way to read internal pages.
		return nil, fmt.Errorf("the link's server answered %s", res.Status)
	}

	var manifest struct {
		Files []struct {
			ContentType string `json:"contentType"`
			Embedded    string `json:"embedded"`
			Location    string `json:"location"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("the link's server did not return a SMART Health Links manifest")
	}
	var out []File
	for i, f := range manifest.Files {
		jwe := f.Embedded
		if jwe == "" && f.Location != "" {
			raw, err := get(ctx, client, f.Location)
			if err != nil {
				return nil, fmt.Errorf("file %d: %w", i+1, err)
			}
			jwe = string(raw)
		}
		if jwe == "" {
			return nil, fmt.Errorf("file %d has neither embedded content nor a location", i+1)
		}
		cty, plain, err := Decrypt(jwe, l.Key)
		if err != nil {
			return nil, fmt.Errorf("file %d: %w", i+1, err)
		}
		if cty == "" {
			cty = f.ContentType
		}
		out = append(out, File{ContentType: cty, Content: plain})
	}
	return out, nil
}

func get(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the file could not be fetched: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNoLongerValid
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the file answered %s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxFetch))
}
