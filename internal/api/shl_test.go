package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/shl"
	"github.com/biodream-llc/perfuse/internal/store"
)

const visitBundle = `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Patient","name":[{"family":"Doe",` +
	`"given":["Jane"]}],"birthDate":"1980-01-01"}},{"resource":{"resourceType":"Encounter","status":"finished"}}]}`

// A visit record shared back to the patient as a link this server hosts, then fetched the way the patient's app would: over HTTP,
// through the public manifest endpoint, with the passcode.
func TestASharedVisitRecordIsFetchedThroughItsLinkAndNothingElse(t *testing.T) {
	h := newHarness(t)
	public := httptest.NewServer(h.handler)
	defer public.Close()
	h.server.PublicURL = public.URL

	rec := h.do("editor", http.MethodPost, "/api/shl", shlCreateRequest{Label: "Visit 3 Oct", Content: json.RawMessage(visitBundle),
		Passcode: "2468"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID, Link, QRSvg, Warning string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Link, "shlink:/") || !strings.HasPrefix(created.QRSvg, "<svg") || created.Warning == "" {
		t.Fatalf("%+v", created)
	}
	link, err := shl.Parse(created.Link)
	if err != nil || !link.Has('P') || link.Label != "Visit 3 Oct" {
		t.Fatalf("%+v %v", link, err)
	}

	// The server holds no key: nothing in its listing or its table opens the file.
	list := h.do("editor", http.MethodGet, "/api/shl", nil).Body.String()
	if strings.Contains(list, link.Key) || !strings.Contains(list, created.ID) {
		t.Errorf("listing: %s", list)
	}

	r := &shl.Resolver{Client: public.Client()}
	var pe *shl.PasscodeError
	if _, err := r.Resolve(context.Background(), link, "Jane's phone", "0000"); !errors.As(err, &pe) || pe.Remaining != store.SHLAttempts-1 {
		t.Fatalf("wrong passcode: %v", err)
	}
	files, err := r.Resolve(context.Background(), link, "Jane's phone", "2468")
	if err != nil || len(files) != 1 || string(files[0].Content) != visitBundle {
		t.Fatalf("%+v %v", files, err)
	}

	// A receiver asking for files by location gets a one-time URL.
	body := strings.NewReader(`{"recipient":"x","passcode":"2468","embeddedLengthMax":10}`)
	res, err := public.Client().Post(link.URL, "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Files []map[string]string }
	_ = json.NewDecoder(res.Body).Decode(&m)
	_ = res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") != "*" || len(m.Files) != 1 || m.Files[0]["location"] == "" {
		t.Fatalf("by location: %+v", m)
	}
	for i, want := range []int{http.StatusOK, http.StatusNotFound} {
		res, _ := public.Client().Get(m.Files[0]["location"])
		_ = res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("location fetch %d: %d", i+1, res.StatusCode)
		}
	}

	// Revoked, and it is gone.
	if rec := h.do("editor", http.MethodDelete, "/api/shl/"+created.ID, nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, err := r.Resolve(context.Background(), link, "x", "2468"); !errors.Is(err, shl.ErrNoLongerValid) {
		t.Errorf("after revoking: %v", err)
	}
}

func TestReceivingALinkReadsItsBundleAndRefusesPlainHTTPByDefault(t *testing.T) {
	h := newHarness(t)
	public := httptest.NewServer(h.handler)
	defer public.Close()
	h.server.PublicURL = public.URL

	rec := h.do("editor", http.MethodPost, "/api/shl", shlCreateRequest{Content: json.RawMessage(visitBundle)})
	var created struct{ Link string }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	if rec := h.do("editor", http.MethodPost, "/api/shl/resolve", shlResolveRequest{Link: created.Link}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "plain HTTP") {
		t.Errorf("plain HTTP was fetched: %d %s", rec.Code, rec.Body.String())
	}
	h.server.SHLAllowHTTP = true
	rec = h.do("editor", http.MethodPost, "/api/shl/resolve", shlResolveRequest{Link: created.Link})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"patient":"Jane Doe"`) || !strings.Contains(body, `"Encounter":1`) {
		t.Errorf("%d %s", rec.Code, body)
	}
	if rec := h.do("viewer", http.MethodPost, "/api/shl/resolve", shlResolveRequest{Link: created.Link}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer resolved a link: %d", rec.Code)
	}
}

func TestCreatingALinkRefusesWhatIsNotFHIR(t *testing.T) {
	h := newHarness(t)
	h.server.PublicURL = "https://perfuse.example"
	for _, c := range []string{`[1,2]`, `{"no":"type"}`} {
		if rec := h.do("editor", http.MethodPost, "/api/shl", shlCreateRequest{Content: json.RawMessage(c)}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", c, rec.Code)
		}
	}
	if rec := h.do("editor", http.MethodPost, "/api/shl", shlCreateRequest{Content: json.RawMessage(visitBundle), Passcode: "12"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a two-character passcode: %d", rec.Code)
	}
}
