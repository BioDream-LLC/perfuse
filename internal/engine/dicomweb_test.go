package engine

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicomweb"
)

// A study posted by STOW-RS to a channel, delivered by STOW-RS to an archive, and found there by QIDO-RS.
func TestAStudyTravelsFromSTOWThroughAChannelIntoADICOMwebArchive(t *testing.T) {
	archive, err := dicomweb.OpenArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	h := &dicomweb.Handler{Archive: archive, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pacs := httptest.NewServer(http.StripPrefix("/dicom-web", h))
	defer pacs.Close()
	h.Base = pacs.URL + "/dicom-web"

	addr := freeAddr(t)
	cfg, err := config.Load(strings.NewReader(`
name: imaging-relay
dataType: dicom
source:
  type: dicomweb
  dicomweb:
    listen: "`+addr+`"
    token: relay-token
destinations:
  - name: archive
    type: dicomweb
    dicomweb:
      url: "`+pacs.URL+`/dicom-web"
`), "relay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New([]*config.Channel{cfg}, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(context.Background()) }()

	raw, err := os.ReadFile("../dicomweb/testdata/jpeg-lossless.dcm")
	if err != nil {
		t.Fatal(err)
	}
	post := func(token string, body []byte) (int, string) {
		var buf bytes.Buffer
		ct, _ := dicomweb.WriteMultipart(&buf, "application/dicom", []dicomweb.Part{{Body: body}})
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/dicom-web/studies", &buf)
		req.Header.Set("Content-Type", ct)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := post("wrong", raw); code != http.StatusUnauthorized {
		t.Errorf("a wrong token answered %d", code)
	}
	code, body := post("relay-token", raw)
	if code != http.StatusOK || !strings.Contains(body, "1.2.826.0.1.3680043.10.1337.9.1.1.1") {
		t.Fatalf("STOW to the channel: %d %s", code, body)
	}
	counts, _ := archive.Count(context.Background())
	if counts.Instances != 1 {
		t.Fatalf("the archive holds %d instances, want 1", counts.Instances)
	}
	held, _ := archive.Instances(context.Background(), "1.2.826.0.1.3680043.10.1337.9.1", "", "")
	got, _ := archive.Read(held[0])
	if !bytes.Equal(got, raw) {
		t.Error("the archive's copy differs from what was sent")
	}

	// Not an object: refused by the channel with the STOW-RS failure, and the archive is unchanged.
	if code, body := post("relay-token", []byte("not DICOM")); code != http.StatusConflict || !strings.Contains(body, "00081198") {
		t.Errorf("garbage answered %d %s", code, body)
	}
}

func TestADICOMwebDestinationNeedsAnImagingChannel(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: wrong
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: archive
    type: dicomweb
    dicomweb:
      url: "https://pacs.example/dicom-web/studies"
`), "wrong.yaml")
	if err == nil || !strings.Contains(err.Error(), "dataType: dicom") || !strings.Contains(err.Error(), "ending in /studies") {
		t.Fatalf("got %v", err)
	}
}
