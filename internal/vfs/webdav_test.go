package vfs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// WebDAV, tested against a handler that answers the way real servers do.
//
// The parsing is the risk here, not the HTTP. A listing misread reports a file as zero bytes, which the poller then waits
// on forever, or reports the wrong size, which makes the settle rule compare the wrong numbers.

// davServer answers PROPFIND, GET, DELETE, MOVE and MKCOL over a small in-memory tree.
type davServer struct {
	t     *testing.T
	files map[string]string
	calls []string
}

func newDavServer(t *testing.T, files map[string]string) (*davServer, *WebDAV) {
	t.Helper()

	d := &davServer{t: t, files: files}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)

	fs, err := NewWebDAV(WebDAVSettings{URL: srv.URL + "/dav/inbox", User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	return d, fs
}

func (d *davServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.calls = append(d.calls, r.Method+" "+r.URL.Path)

	if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case "PROPFIND":
		d.propfind(w, r)

	case http.MethodGet:
		body, ok := d.files[strings.TrimPrefix(r.URL.Path, "/dav/inbox/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)

	case http.MethodDelete:
		delete(d.files, strings.TrimPrefix(r.URL.Path, "/dav/inbox/"))
		w.WriteHeader(http.StatusNoContent)

	case "MOVE":
		from := strings.TrimPrefix(r.URL.Path, "/dav/inbox/")
		body, ok := d.files[from]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		dest := r.Header.Get("Destination")
		idx := strings.Index(dest, "/dav/inbox/")
		if idx < 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		to := dest[idx+len("/dav/inbox/"):]

		if _, exists := d.files[to]; exists && r.Header.Get("Overwrite") == "F" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		d.files[to] = body
		delete(d.files, from)
		w.WriteHeader(http.StatusCreated)

	case "MKCOL":
		w.WriteHeader(http.StatusCreated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// propfind answers with a multistatus body shaped the way real servers shape it, including the awkward parts:
// namespace-prefixed elements, the collection listing itself, and a 404 propstat block alongside a 200 one.
func (d *davServer) propfind(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	// A prefix rather than a default namespace, because plenty of servers do that and code matching on element name
	// without the namespace breaks on it.
	b.WriteString(`<D:multistatus xmlns:D="DAV:">`)

	// The collection itself. Always present, and must be skipped by the client rather than counted as a file.
	b.WriteString(`<D:response><D:href>/dav/inbox/</D:href><D:propstat>` +
		`<D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>inbox</D:displayname>` +
		`<D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`)

	for name, body := range d.files {
		b.WriteString(`<D:response><D:href>/dav/inbox/` + name + `</D:href>`)
		b.WriteString(`<D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop>`)
		b.WriteString(`<D:displayname>` + name + `</D:displayname>`)
		b.WriteString(`<D:getcontentlength>` + itoa(len(body)) + `</D:getcontentlength>`)
		b.WriteString(`<D:getlastmodified>Thu, 27 Aug 2026 12:00:00 GMT</D:getlastmodified>`)
		b.WriteString(`<D:resourcetype/>`)
		b.WriteString(`</D:prop></D:propstat>`)

		// A second block for properties the server does not hold. Real servers emit this, and a client that reads values
		// out of it reports every file as zero bytes - which the poller waits on forever.
		b.WriteString(`<D:propstat><D:status>HTTP/1.1 404 Not Found</D:status><D:prop>` +
			`<D:getcontentlength/><D:getlastmodified/></D:prop></D:propstat>`)
		b.WriteString(`</D:response>`)
	}

	// A subdirectory, which must be reported as one so the poller skips it.
	b.WriteString(`<D:response><D:href>/dav/inbox/processed/</D:href><D:propstat>` +
		`<D:status>HTTP/1.1 200 OK</D:status><D:prop><D:displayname>processed</D:displayname>` +
		`<D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`)

	b.WriteString(`</D:multistatus>`)

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// A listing must report real sizes and must not include the collection itself.
//
// The collection appears in every PROPFIND response. Counted as a file it is a zero-byte entry the poller waits on
// forever, and a permanently non-zero waiting count is how a real backlog becomes invisible.
func TestAWebDAVListingReportsSizesAndSkipsTheCollectionItself(t *testing.T) {
	body := "MSH|^~\\&|LAB|A|P|B|20260827120000||ORU^R01|1|P|2.5.1\r"
	_, fs := newDavServer(t, map[string]string{"one.hl7": body})

	entries, err := fs.List(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}

	var files, dirs int
	for _, e := range entries {
		if e.IsDir {
			dirs++
			continue
		}
		files++
		if e.Name != "one.hl7" {
			t.Errorf("unexpected file %q", e.Name)
		}
		// The assertion that matters. A 404 propstat block sits next to the 200 one, and reading values out of it
		// reports zero.
		if e.Size != int64(len(body)) {
			t.Errorf("%s reports %d bytes, want %d. The 404 propstat block was probably read as well as the 200 one",
				e.Name, e.Size, len(body))
		}
		if e.ModTime.IsZero() {
			t.Errorf("%s has no modification time, so the settle rule has nothing to compare", e.Name)
		}
	}

	if files != 1 {
		t.Errorf("%d files listed, want 1", files)
	}
	// One subdirectory, and the collection itself must not be among them.
	if dirs != 1 {
		t.Errorf("%d directories listed, want 1 - the collection itself is probably being counted", dirs)
	}
}

// Reading, deleting and moving must work.
func TestWebDAVCanReadDeleteAndMove(t *testing.T) {
	ctx := context.Background()
	srv, fs := newDavServer(t, map[string]string{"one.hl7": "MSH|one", "two.hl7": "MSH|two"})

	rc, err := fs.Open(ctx, "one.hl7")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "MSH|one" {
		t.Errorf("read %q", got)
	}

	if err := fs.Rename(ctx, "one.hl7", "processed/one.hl7"); err != nil {
		t.Fatalf("moving: %v", err)
	}
	if _, still := srv.files["one.hl7"]; still {
		t.Error("the file is still in the source collection after a move")
	}
	if _, moved := srv.files["processed/one.hl7"]; !moved {
		t.Error("the file did not arrive in the archive collection")
	}

	if err := fs.Remove(ctx, "two.hl7"); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, still := srv.files["two.hl7"]; still {
		t.Error("the file was not deleted")
	}
}

// A move must not overwrite an existing file.
//
// The file already there is somebody's evidence. The poller responds to this refusal by adding a suffix and retrying,
// which only works if the server is asked not to overwrite in the first place.
func TestAWebDAVMoveDoesNotOverwriteAnExistingFile(t *testing.T) {
	srv, fs := newDavServer(t, map[string]string{
		"one.hl7":           "the new one",
		"processed/one.hl7": "yesterday's, which matters",
	})

	err := fs.Rename(context.Background(), "one.hl7", "processed/one.hl7")
	if err == nil {
		t.Fatal("the move replaced an existing file")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the error does not say why the move was refused: %v", err)
	}
	if srv.files["processed/one.hl7"] != "yesterday's, which matters" {
		t.Error("the existing file was destroyed despite the refusal")
	}
}

// A path escaping the collection must be refused.
//
// The server has no notion of the configured root, so a path of ../.. resolves against the host and reaches a different
// collection entirely. Containment is the client's responsibility.
func TestAWebDAVPathEscapingTheCollectionIsRefused(t *testing.T) {
	srv, fs := newDavServer(t, map[string]string{"one.hl7": "MSH|one"})
	ctx := context.Background()

	before := len(srv.calls)

	for _, attempt := range []string{"../secrets.hl7", "../../etc/passwd", "sub/../../outside.hl7"} {
		if _, err := fs.Open(ctx, attempt); err == nil {
			t.Errorf("%q was read from outside the collection", attempt)
		}
		if err := fs.Remove(ctx, attempt); err == nil {
			t.Errorf("%q was deleted from outside the collection", attempt)
		}
	}

	// And no request was made at all, which is the stronger assertion: refusing after asking the server would already
	// have disclosed the attempt and, for DELETE, might have succeeded.
	if len(srv.calls) != before {
		t.Errorf("the client sent %d request(s) for paths it should have refused locally: %v",
			len(srv.calls)-before, srv.calls[before:])
	}
}

// Wrong credentials must produce an error naming the likely cause.
//
// The commonest WebDAV failure. Several servers reject basic authentication outright rather than rejecting the password,
// and "401 Unauthorized" sends somebody to check a password that was correct all along.
func TestWebDAVCredentialFailureNamesTheLikelyCause(t *testing.T) {
	d := &davServer{t: t, files: map[string]string{}}
	srv := httptest.NewServer(d)
	defer srv.Close()

	fs, err := NewWebDAV(WebDAVSettings{URL: srv.URL + "/dav/inbox", User: "u", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fs.List(context.Background(), ".")
	if err == nil {
		t.Fatal("a wrong password was accepted")
	}
	if !strings.Contains(err.Error(), "basic authentication") {
		t.Errorf("the error does not mention that basic auth may be refused outright: %v", err)
	}
}

// A URL without a trailing slash must still list the collection.
//
// WebDAV treats a collection without one as a resource, and some servers answer PROPFIND on it with the parent instead of
// the contents. Somebody typing a URL into a form will not add the slash.
func TestAWebDAVURLWithoutATrailingSlashStillWorks(t *testing.T) {
	d := &davServer{t: t, files: map[string]string{"one.hl7": "MSH|one"}}
	srv := httptest.NewServer(d)
	defer srv.Close()

	fs, err := NewWebDAV(WebDAVSettings{URL: srv.URL + "/dav/inbox", User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := fs.List(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("nothing was listed for a URL given without a trailing slash")
	}
}

// A non-HTTP scheme must be refused at construction.
func TestAWebDAVURLWithTheWrongSchemeIsRefused(t *testing.T) {
	if _, err := NewWebDAV(WebDAVSettings{URL: "ftp://example.org/files"}); err == nil {
		t.Error("an ftp URL was accepted as webdav")
	}
}
