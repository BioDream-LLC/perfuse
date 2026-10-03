// Package azblob reads and writes Azure Blob Storage with the standard library: Shared Key or SAS authorisation over plain HTTP calls.
//
// The same reasoning as s3put: the Azure SDK is large and the operations an integration engine needs are five - put, get, list, delete,
// and setting the access tier. Shared Key signing is specified exactly ("Authorize with Shared Key", Azure Storage REST), and Azurite,
// Microsoft's emulator, checks it the way the service does.
package azblob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the x-ms-version every request is made at.
const APIVersion = "2021-12-02"

// Tiers are the access tiers a block blob can be written in. Archive is offline: reading one back means rehydrating it, which takes hours.
var Tiers = []string{"Hot", "Cool", "Cold", "Archive"}

// Config says where and how to authorise.
type Config struct {
	Account   string
	Container string
	// Key is the storage account key, base64. Either it or SAS is required.
	Key string
	// SAS is a shared access signature query string, used instead of the key. It should grant only what is needed - read, write,
	// list, delete on this container - which is the reason to prefer it.
	SAS string
	// Endpoint overrides https://<account>.blob.core.windows.net, for Azurite or a private endpoint. For Azurite it includes the account:
	// http://127.0.0.1:10000/devstoreaccount1.
	Endpoint string
	// Tier, when set, is sent with each put.
	Tier string
	// HTTPClient defaults to one with a 60s timeout.
	HTTPClient *http.Client
}

// Client works with one container.
type Client struct {
	cfg  Config
	base *url.URL
	key  []byte
	http *http.Client
}

// New checks the configuration.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Account) == "" || strings.TrimSpace(cfg.Container) == "" {
		return nil, errors.New("azure blob needs an account and a container")
	}
	c := &Client{cfg: cfg, http: cfg.HTTPClient}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	base := cfg.Endpoint
	if base == "" {
		base = "https://" + cfg.Account + ".blob.core.windows.net"
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("the blob endpoint %q is not a URL", base)
	}
	c.base = u
	switch {
	case cfg.SAS != "":
	case cfg.Key != "":
		k, err := base64.StdEncoding.DecodeString(cfg.Key)
		if err != nil {
			return nil, errors.New("the storage account key is not base64; copy it again from the portal")
		}
		c.key = k
	default:
		return nil, errors.New("azure blob needs the account key or a SAS token")
	}
	return c, nil
}

func (c *Client) url(blob string, q url.Values) *url.URL {
	u := *c.base
	p := strings.TrimRight(u.Path, "/") + "/" + c.cfg.Container
	if blob != "" {
		p += "/" + blob
	}
	u.Path = p
	u.RawPath = ""
	query := url.Values{}
	for k, v := range q {
		query[k] = v
	}
	if c.cfg.SAS != "" {
		sas, _ := url.ParseQuery(strings.TrimPrefix(c.cfg.SAS, "?"))
		for k, v := range sas {
			query[k] = v
		}
	}
	u.RawQuery = query.Encode()
	return &u
}

// sign adds the Shared Key Authorization header.
func (c *Client) sign(req *http.Request, length int) {
	if c.key == nil {
		return
	}
	h := req.Header
	contentLength := ""
	if length > 0 {
		contentLength = strconv.Itoa(length)
	}
	var names []string
	for k := range h {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-ms-") {
			names = append(names, lk)
		}
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(h.Get(n)) + "\n")
	}
	canonResource := "/" + c.cfg.Account + req.URL.EscapedPath()
	q := req.URL.Query()
	var keys []string
	for k := range q {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		canonResource += "\n" + k + ":" + strings.Join(vals, ",")
	}
	toSign := strings.Join([]string{req.Method, h.Get("Content-Encoding"), h.Get("Content-Language"), contentLength,
		h.Get("Content-MD5"), h.Get("Content-Type"), "", h.Get("If-Modified-Since"), h.Get("If-Match"), h.Get("If-None-Match"),
		h.Get("If-Unmodified-Since"), h.Get("Range")}, "\n") + "\n" + canonHeaders.String() + canonResource
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(toSign))
	h.Set("Authorization", "SharedKey "+c.cfg.Account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func (c *Client) do(ctx context.Context, method, blob string, q url.Values, body []byte, headers map[string]string,
	limit int64) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(blob, q).String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-version", APIVersion)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.sign(req, len(body))
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(data, &e)
		msg := strings.SplitN(e.Message, "\n", 2)[0]
		return res, nil, fmt.Errorf("Azure Blob Storage refused the %s with %s: %s %s", strings.ToLower(method), res.Status, e.Code, msg)
	}
	if int64(len(data)) > limit {
		return res, nil, fmt.Errorf("the blob %s is larger than the %d bytes allowed", blob, limit)
	}
	return res, data, nil
}

// Put writes a block blob.
func (c *Client) Put(ctx context.Context, blob string, body []byte, contentType string) error {
	h := map[string]string{"x-ms-blob-type": "BlockBlob"}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	if c.cfg.Tier != "" {
		h["x-ms-access-tier"] = c.cfg.Tier
	}
	_, _, err := c.do(ctx, http.MethodPut, escapeBlob(blob), nil, body, h, 1<<20)
	return err
}

// Get reads a blob, refusing anything over limit bytes.
func (c *Client) Get(ctx context.Context, blob string, limit int64) ([]byte, error) {
	_, data, err := c.do(ctx, http.MethodGet, escapeBlob(blob), nil, nil, nil, limit)
	return data, err
}

// Delete removes a blob.
func (c *Client) Delete(ctx context.Context, blob string) error {
	_, _, err := c.do(ctx, http.MethodDelete, escapeBlob(blob), nil, nil, nil, 1<<20)
	return err
}

// Properties reads a blob's tier, for checking where an archive went.
func (c *Client) Properties(ctx context.Context, blob string) (http.Header, error) {
	res, _, err := c.do(ctx, http.MethodHead, escapeBlob(blob), nil, nil, nil, 1<<20)
	if err != nil {
		return nil, err
	}
	return res.Header, nil
}

// Blob is one listing entry.
type Blob struct {
	Name string
	Size int64
}

// List returns blobs under prefix in name order, with a continuation marker when there are more.
func (c *Client) List(ctx context.Context, prefix, marker string, max int) ([]Blob, string, error) {
	q := url.Values{"restype": {"container"}, "comp": {"list"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if marker != "" {
		q.Set("marker", marker)
	}
	if max > 0 {
		q.Set("maxresults", strconv.Itoa(max))
	}
	_, data, err := c.do(ctx, http.MethodGet, "", q, nil, nil, 16<<20)
	if err != nil {
		return nil, "", err
	}
	var res struct {
		Blobs []struct {
			Name       string `xml:"Name"`
			Properties struct {
				ContentLength int64 `xml:"Content-Length"`
			} `xml:"Properties"`
		} `xml:"Blobs>Blob"`
		NextMarker string `xml:"NextMarker"`
	}
	if err := xml.Unmarshal(data, &res); err != nil {
		return nil, "", fmt.Errorf("the blob listing could not be read: %w", err)
	}
	out := make([]Blob, 0, len(res.Blobs))
	for _, b := range res.Blobs {
		out = append(out, Blob{Name: b.Name, Size: b.Properties.ContentLength})
	}
	return out, res.NextMarker, nil
}

// CreateContainer makes the container, succeeding if it exists. For tests and first setup.
func (c *Client) CreateContainer(ctx context.Context) error {
	res, _, err := c.do(ctx, http.MethodPut, "", url.Values{"restype": {"container"}}, nil, nil, 1<<20)
	if res != nil && res.StatusCode == http.StatusConflict {
		return nil
	}
	return err
}

// escapeBlob only trims a leading slash. Escaping is the URL's job when the path is set; doing it here as well stored "a b" as "a%20b".
func escapeBlob(name string) string { return strings.TrimPrefix(name, "/") }
