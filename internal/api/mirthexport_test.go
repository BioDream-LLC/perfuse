package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMirthTestChannel puts a channel file in the harness's directory, which is where the repository reads them from.
func writeMirthTestChannel(t *testing.T, h *harness, file, yaml string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(h.dir, file+".yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Mirth export endpoints.
//
// The download itself is verified against a running Mirth in internal/tomirth; what matters here is that the browser can reach it, that a
// channel Mirth cannot express is refused rather than approximated, and that the losses are reported before a file is offered rather than
// only inside it.

func TestExportMirthOffersADownload(t *testing.T) {
	h := newHarness(t)

	writeMirthTestChannel(t, h, "adt", `name: adt
source:
  type: mllp
  listen: ":6661"
destinations:
  - name: To the registry
    type: mllp
    address: registry.example.invalid:6662
`)

	res := h.do("viewer", http.MethodGet, "/api/channels/adt/mirth", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("expected the export to be offered, got %d: %s", res.Code, res.Body.String())
	}

	// A download rather than something rendered in the page, because the file has to arrive as a file somebody can import.
	if cd := res.Result().Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "adt.mirth.xml") {
		t.Errorf("Content-Disposition is %q, expected an attachment named adt.mirth.xml", cd)
	}

	body := res.Body.String()
	for _, want := range []string{"<channel", "TCP Listener", "TCP Sender", "6661", "registry.example.invalid"} {
		if !strings.Contains(body, want) {
			t.Errorf("the exported document does not contain %q:\n%s", want, body)
		}
	}

	// Mirth's own model has no enabled element on a channel, and writing one makes Mirth discard the whole thing. Asserted here as well
	// as in the mirth package, because this is the path a person actually uses.
	if strings.Contains(body, "<enabled>true</enabled>\n  <sourceConnector") {
		t.Error("the channel carries an enabled element, which makes Mirth store it as invalid")
	}
}

func TestExportMirthSaysWhatItLoses(t *testing.T) {
	h := newHarness(t)

	writeMirthTestChannel(t, h, "rich", `name: rich
group: Admissions
filter: "PID-3 != ''"
source:
  type: mllp
  listen: ":6663"
destinations:
  - name: To the registry
    type: mllp
    address: registry.example.invalid:6664
`)

	res := h.do("viewer", http.MethodGet, "/api/channels/rich/mirth/preview", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("expected a preview, got %d: %s", res.Code, res.Body.String())
	}

	var out mirthExportPreview
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the preview: %v", err)
	}

	if !out.Convertible {
		t.Fatalf("expected the channel to be convertible, refused with: %s", out.Refusal)
	}
	if len(out.Notes) == 0 {
		t.Fatal("a channel with a filter and a group reported no losses, so somebody would carry it to Mirth believing it complete")
	}

	joined := strings.Join(out.Notes, "\n")
	for _, want := range []string{"filter", "group"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no note mentions the %s:\n%s", want, joined)
		}
	}

	// And on the download itself, for a scripted caller that never sees the dialogue.
	dl := h.do("viewer", http.MethodGet, "/api/channels/rich/mirth", nil)
	if dl.Result().Header.Get("X-Perfuse-Export-Notes") == "" {
		t.Error("the download carries no notes header, so a script has no way to learn what was dropped")
	}
}

func TestExportMirthRefusesWhatItCannotExpress(t *testing.T) {
	// The credentials are referenced rather than written, and the variables have to exist for the channel to load at all. Placeholder
	// values: this test never reaches S3, it only needs a channel that is valid and has a destination Mirth cannot express.
	t.Setenv("AWS_ACCESS_KEY_ID", "placeholder-not-a-real-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "placeholder-not-a-real-secret")

	h := newHarness(t)

	writeMirthTestChannel(t, h, "bucket", `name: bucket
source:
  type: mllp
  listen: ":6665"
destinations:
  - name: To the bucket
    type: s3
    s3:
      bucket: results
      region: us-east-1
      access_key_id: ${AWS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SECRET_ACCESS_KEY}
`)

	res := h.do("viewer", http.MethodGet, "/api/channels/bucket/mirth", nil)
	if res.Code == http.StatusOK {
		t.Fatalf("an S3 destination was exported to Mirth, which has no S3 connector. The channel would import cleanly and deliver "+
			"nowhere near the bucket.\n%s", res.Body.String())
	}

	if !strings.Contains(res.Body.String(), "To the bucket") {
		t.Errorf("the refusal does not name the destination at fault: %s", res.Body.String())
	}

	// The preview must agree with the download, or the dialogue offers a button that then fails.
	pv := h.do("viewer", http.MethodGet, "/api/channels/bucket/mirth/preview", nil)
	var out mirthExportPreview
	if err := json.Unmarshal(pv.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the preview: %v", err)
	}

	if out.Convertible {
		t.Error("the preview says this channel is convertible while the download refuses it")
	}
	if out.Notes == nil {
		t.Error("notes came back null rather than empty, which the browser reads as a missing field")
	}
}

func TestExportMirthNeedsASession(t *testing.T) {
	h := newHarness(t)

	writeMirthTestChannel(t, h, "adt", `name: adt
source:
  type: mllp
  listen: ":6666"
destinations:
  - name: d
    type: mllp
    address: host.invalid:6667
`)

	// A channel file names hosts and ports on an internal network, which is worth no less protection than the YAML behind the same list.
	res := h.do("", http.MethodGet, "/api/channels/adt/mirth", nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("the export was served without a session (%d), exposing internal addresses", res.Code)
	}
}
