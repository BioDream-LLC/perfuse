package vfs

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// WebDAV is a collection on a WebDAV server.
//
// # Why implemented rather than imported
//
// The subset needed to poll a directory is four requests: PROPFIND to list, GET to read, DELETE to remove, MOVE to
// archive. All four are ordinary HTTP with an XML body on one of them. A library would bring transitive dependencies for
// locking, property patching and versioning, none of which a polling connector touches.
//
// # Where WebDAV actually turns up
//
// Document management systems, SharePoint, Nextcloud, and a surprising number of vendor portals that expose a drop folder
// over HTTPS because it is the only port their customers' firewalls allow outbound. It is often the least awkward way to
// collect files from a hosted system.
type WebDAV struct {
	client *http.Client
	base   *url.URL

	user     string
	password string
}

// WebDAVSettings is what a WebDAV filesystem needs.
type WebDAVSettings struct {
	// URL is the collection every path is relative to, including scheme and any path prefix.
	URL string

	User     string
	Password string

	// InsecureSkipVerify accepts any certificate.
	//
	// Named rather than implied. Plenty of internal document servers use a self-signed certificate, so this has to be
	// possible, but it must be a decision somebody made rather than a default.
	InsecureSkipVerify bool

	Timeout time.Duration
}

// NewWebDAV prepares a client. It makes no request.
func NewWebDAV(s WebDAVSettings) (*WebDAV, error) {
	if strings.TrimSpace(s.URL) == "" {
		return nil, fmt.Errorf("a webdav source needs a url")
	}

	base, err := url.Parse(s.URL)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable URL: %w", s.URL, err)
	}
	switch base.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("the url scheme is %q; webdav needs http or https", base.Scheme)
	}

	// A trailing slash matters to WebDAV: a collection without one is a resource, and some servers answer PROPFIND on it
	// with the parent rather than its contents.
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	transport := &http.Transport{
		// Kept low deliberately. Each poll is a handful of sequential requests, so a large pool would hold idle
		// connections open against a server for no benefit.
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
	}
	if s.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // deliberate, named setting
	} else {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return &WebDAV{
		client:   &http.Client{Timeout: timeout, Transport: transport},
		base:     base,
		user:     s.User,
		password: s.Password,
	}, nil
}

// urlFor turns a relative path into an absolute URL inside the base collection.
//
// Containment is this function's job. The server has no notion of the configured root, so a path of "../.." would resolve
// against the host and reach a different collection entirely.
func (w *WebDAV) urlFor(p string, collection bool) (*url.URL, error) {
	// Refused before cleaning, not after.
	//
	// My first attempt compared the cleaned path against the base and could never fail: path.Clean("/"+"../x") is "/x",
	// because the .. is absorbed by the leading slash. Nothing escaped the collection - but "../secrets" silently became
	// "secrets" and "../../etc/passwd" became "etc/passwd", so the client would read and delete a *different file* from
	// the one it was asked about, with no error. A test that asserted no request was sent at all caught it; one that only
	// checked for an error would have passed.
	for _, part := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		if part == ".." {
			return nil, fmt.Errorf("%s contains \"..\", which a source will not follow. Every path is relative to "+
				"%s and climbing out of it is refused rather than quietly resolved to something else",
				p, w.base.Path)
		}
	}

	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	joined := path.Join(w.base.Path, cleaned)

	u := *w.base
	u.Path = joined
	if collection && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return &u, nil
}

// do sends one request with authentication applied.
func (w *WebDAV) do(ctx context.Context, method string, u *url.URL, body io.Reader,
	headers map[string]string) (*http.Response, error) {

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if w.user != "" {
		req.SetBasicAuth(w.user, w.password)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		// Named specifically because it is the commonest WebDAV failure and the generic message is unhelpful. Several
		// servers, SharePoint among them, reject basic authentication outright rather than for a wrong password.
		return nil, fmt.Errorf("%s refused the credentials for %s. Some servers reject basic authentication "+
			"entirely rather than rejecting the password, so check that it is permitted as well as correct",
			method, u.Host)
	}

	return res, nil
}

// multistatus is the PROPFIND response body.
type multistatus struct {
	Responses []davResponse `xml:"DAV: response"`
}

type davResponse struct {
	Href     string     `xml:"DAV: href"`
	Propstat []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string  `xml:"DAV: status"`
	Prop   davProp `xml:"DAV: prop"`
}

type davProp struct {
	DisplayName  string `xml:"DAV: displayname"`
	LastModified string `xml:"DAV: getlastmodified"`
	Length       string `xml:"DAV: getcontentlength"`
	ResourceType struct {
		Collection *struct{} `xml:"DAV: collection"`
	} `xml:"DAV: resourcetype"`
}

// List returns the entries in a collection.
func (w *WebDAV) List(ctx context.Context, dir string) ([]Entry, error) {
	u, err := w.urlFor(dir, true)
	if err != nil {
		return nil, err
	}

	// Only the four properties actually used. Asking for allprop makes some servers return every custom property they
	// hold, which on a document management system can be a very large response for a directory listing.
	body := strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<propfind xmlns="DAV:"><prop>
<displayname/><getcontentlength/><getlastmodified/><resourcetype/>
</prop></propfind>`)

	res, err := w.do(ctx, "PROPFIND", u, body, map[string]string{
		// Depth 1 is the collection and its immediate children. Depth infinity would descend the whole tree, which on a
		// document store is both enormous and forbidden by most servers.
		"Depth":        "1",
		"Content-Type": `application/xml; charset="utf-8"`,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusMultiStatus && res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing %s returned %s. A 405 here usually means the server serves files over HTTP "+
			"but does not implement WebDAV", u.Path, res.Status)
	}

	var ms multistatus
	dec := xml.NewDecoder(io.LimitReader(res.Body, 32<<20))
	// Entity expansion off. A listing is untrusted input from a remote server and an XML parser that expands entities is
	// a denial of service waiting to happen.
	dec.Strict = false
	dec.Entity = map[string]string{}
	if err := dec.Decode(&ms); err != nil {
		return nil, fmt.Errorf("the server's listing of %s could not be read: %w", u.Path, err)
	}

	selfPath := strings.TrimSuffix(u.Path, "/")
	out := make([]Entry, 0, len(ms.Responses))

	for _, r := range ms.Responses {
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			href = r.Href
		}
		// The collection itself is always in the response. Skipped by comparing paths rather than by position, because
		// servers do not agree on whether it comes first.
		if strings.TrimSuffix(href, "/") == selfPath {
			continue
		}

		e := Entry{Name: path.Base(strings.TrimSuffix(href, "/"))}

		for _, ps := range r.Propstat {
			// Only the properties the server said it found. A 404 propstat block carries empty values, and reading them
			// would report every file as zero bytes - which a poller would then wait on forever.
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			if ps.Prop.DisplayName != "" {
				e.Name = ps.Prop.DisplayName
			}
			if ps.Prop.ResourceType.Collection != nil {
				e.IsDir = true
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(ps.Prop.Length), 10, 64); err == nil {
				e.Size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(ps.Prop.LastModified)); err == nil {
				e.ModTime = t.UTC()
			}
		}

		if e.Name == "" {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Open reads a file.
func (w *WebDAV) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	u, err := w.urlFor(p, false)
	if err != nil {
		return nil, err
	}

	res, err := w.do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("reading %s returned %s", u.Path, res.Status)
	}
	// Streamed rather than buffered. Unlike FTP there is no shared control connection to free, so the caller can read at
	// its own pace and the poller's size limit still applies through its LimitReader.
	return res.Body, nil
}

// Remove deletes a file.
func (w *WebDAV) Remove(ctx context.Context, p string) error {
	u, err := w.urlFor(p, false)
	if err != nil {
		return err
	}

	res, err := w.do(ctx, http.MethodDelete, u, nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return fmt.Errorf("deleting %s returned %s", u.Path, res.Status)
	}
}

// Rename moves a file.
func (w *WebDAV) Rename(ctx context.Context, from, to string) error {
	src, err := w.urlFor(from, false)
	if err != nil {
		return err
	}
	dst, err := w.urlFor(to, false)
	if err != nil {
		return err
	}

	res, err := w.do(ctx, "MOVE", src, nil, map[string]string{
		"Destination": dst.String(),
		// F for false: do not overwrite. The poller handles a name collision by adding a suffix and retrying, because the
		// file already there is somebody's evidence. Overwrite: T would destroy it silently.
		"Overwrite": "F",
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		// What Overwrite: F produces when the destination exists. Reported plainly so the poller's suffix-and-retry path
		// reads as the deliberate response it is.
		return fmt.Errorf("%s already exists and has not been replaced", dst.Path)
	default:
		return fmt.Errorf("moving %s to %s returned %s", src.Path, dst.Path, res.Status)
	}
}

// MkdirAll creates a collection and its parents.
func (w *WebDAV) MkdirAll(ctx context.Context, dir string) error {
	cleaned := strings.Trim(path.Clean("/"+dir), "/")
	if cleaned == "" || cleaned == "." {
		return nil
	}

	// One level at a time. MKCOL creates a single collection and fails if the parent is missing, with no recursive form.
	var built string
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" {
			continue
		}
		built = path.Join(built, part)

		u, err := w.urlFor(built, true)
		if err != nil {
			return err
		}

		res, err := w.do(ctx, "MKCOL", u, nil, nil)
		if err != nil {
			return err
		}
		res.Body.Close()

		// 405 is the collection already existing, which is the ordinary case on every poll after the first. Anything else
		// is left to the operation that needed the directory, which will fail next and name the actual file.
		if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusMethodNotAllowed {
			continue
		}
	}
	return nil
}

// Join joins path elements with a forward slash, which is what a URL path uses.
func (w *WebDAV) Join(elem ...string) string { return path.Join(elem...) }

// Describe names the collection without the password.
func (w *WebDAV) Describe() string {
	if w.user != "" {
		return fmt.Sprintf("webdav %s@%s%s", w.user, w.base.Host, w.base.Path)
	}
	return fmt.Sprintf("webdav %s%s", w.base.Host, w.base.Path)
}

// Close releases idle connections.
func (w *WebDAV) Close() error {
	w.client.CloseIdleConnections()
	return nil
}

var _ FS = (*WebDAV)(nil)
