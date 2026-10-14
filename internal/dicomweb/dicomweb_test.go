package dicomweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDCMTKsThreeEncodingsReadTheSame(t *testing.T) {
	var want string
	for _, name := range []string{"native-explicit.dcm", "native-implicit.dcm", "jpeg-lossless.dcm"} {
		obj, err := Parse(fixture(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		j := ToJSON(obj.Attrs, obj.Order(), func(uint32) string { return "bulk" }, func(tag uint32) bool {
			// The pixel module differs by encoding; the identifying attributes and the sequence must not.
			return tag != 0x7FE00010
		})
		b, _ := json.Marshal(j)
		got := string(b)
		for _, frag := range []string{`"0020000D":{"Value":["1.2.826.0.1.3680043.10.1337.9.1"],"vr":"UI"}`,
			`"00100010":{"Value":[{"Alphabetic":"Doe^Jane"}],"vr":"PN"}`,
			`"00280030":{"Value":[0.5,0.5],"vr":"DS"}`, `"00280010":{"Value":[4],"vr":"US"}`,
			`"00081110":{"Value":[{"00081150":{"Value":["1.2.840.10008.3.1.2.3.1"],"vr":"UI"}`} {
			if !strings.Contains(got, frag) {
				t.Errorf("%s lacks %s:\n%s", name, frag, got)
			}
		}
		if want == "" {
			want = got
		} else if name != "jpeg-lossless.dcm" && got != want {
			t.Errorf("%s reads differently from the explicit VR file", name)
		}
	}
}

func TestNativeFramesAreCutByFrameSize(t *testing.T) {
	obj, err := Parse(fixture(t, "native-explicit.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := obj.Frames([]int{2, 1})
	if err != nil {
		t.Fatal(err)
	}
	if frames[0][0] != 0x10 || frames[1][0] != 0x00 || len(frames[0]) != 16 {
		t.Errorf("frames %x", frames)
	}
	if _, err := obj.Frames([]int{3}); err == nil {
		t.Error("a third frame of a two-frame object was returned")
	}
}

func TestEncapsulatedFramesAreTheirFragments(t *testing.T) {
	obj, err := Parse(fixture(t, "jpeg-lossless.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := obj.Frames([]int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		if !bytes.HasPrefix(f, []byte{0xFF, 0xD8}) || !bytes.HasSuffix(bytes.TrimRight(f, "\x00"), []byte{0xFF, 0xD9}) {
			t.Errorf("frame %d is not a whole JPEG: % x … % x", i+1, f[:4], f[len(f)-4:])
		}
	}
	if FrameMediaType(obj.TransferSyntax) != "image/jpeg" {
		t.Errorf("media type %s", FrameMediaType(obj.TransferSyntax))
	}
}

func TestATruncatedOrForeignFileIsRefused(t *testing.T) {
	raw := fixture(t, "native-explicit.dcm")
	for _, bad := range [][]byte{raw[:len(raw)-10], []byte("not dicom at all, not even close to it"), nil} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("parsed %d bytes of nonsense", len(bad))
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"native-explicit.dcm", "native-implicit.dcm", "jpeg-lossless.dcm"} {
		raw, _ := os.ReadFile("testdata/" + name)
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		obj, err := Parse(raw)
		if err != nil {
			return
		}
		_ = ToJSON(obj.Attrs, obj.Order(), func(uint32) string { return "" }, nil)
		_, _ = obj.Frames([]int{1})
	})
}

func newArchive(t *testing.T) (*Archive, *httptest.Server) {
	t.Helper()
	a, err := OpenArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	h := &Handler{Archive: a, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(http.StripPrefix("/dicomweb", h))
	h.Base = srv.URL + "/dicomweb"
	t.Cleanup(srv.Close)
	return a, srv
}

func get(t *testing.T, url, accept string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), b
}

const (
	studyUID  = "1.2.826.0.1.3680043.10.1337.9.1"
	seriesUID = "1.2.826.0.1.3680043.10.1337.9.1.1"
	sopUID    = "1.2.826.0.1.3680043.10.1337.9.1.1.1"
)

func TestStoreSearchRetrieveRoundTrip(t *testing.T) {
	_, srv := newArchive(t)
	base := srv.URL + "/dicomweb"
	c := &Client{URL: base}
	raw := fixture(t, "jpeg-lossless.dcm")

	out, err := c.Store(context.Background(), raw)
	if err != nil || len(out.Stored) != 1 || out.Stored[0] != sopUID {
		t.Fatalf("STOW: %+v %v", out, err)
	}

	// QIDO at each level, with the matching OHIF uses.
	for url, want := range map[string]string{
		base + "/studies?PatientName=doe&fuzzymatching=true":                  studyUID,
		base + "/studies?PatientID=DWMRN1":                                    `"00201208":{"Value":[1]`,
		base + "/studies?StudyDate=20261001-20261031":                         "DICOMWEB TEST STUDY",
		base + "/studies/" + studyUID + "/series":                             `"00080060":{"Value":["OT"]`,
		base + "/studies/" + studyUID + "/series/" + seriesUID + "/instances": `"00280008":{"Value":[2]`,
		base + "/studies?AccessionNumber=ACC*":                                "ACCDW1",
	} {
		code, ct, body := get(t, url, "application/dicom+json")
		if code != 200 || ct != "application/dicom+json" || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %s %s", url, code, ct, body)
		}
	}
	if code, _, _ := get(t, base+"/studies?PatientID=SOMEONE-ELSE", ""); code != http.StatusNoContent {
		t.Errorf("no match answered %d, want 204", code)
	}
	// An attribute this archive does not match on is refused, never ignored.
	if code, _, body := get(t, base+"/studies?InstitutionName=X", ""); code != 400 || !strings.Contains(string(body), "InstitutionName") {
		t.Errorf("unsupported match: %d %s", code, body)
	}

	// WADO-RS: the object back, byte for byte.
	code, ct, body := get(t, base+"/studies/"+studyUID, `multipart/related; type="application/dicom"`)
	if code != 200 || !strings.HasPrefix(ct, "multipart/related") {
		t.Fatalf("retrieve: %d %s", code, ct)
	}
	_, parts, err := ReadMultipart(ct, bytes.NewReader(body), 1<<20, 1<<20)
	if err != nil || len(parts) != 1 || !bytes.Equal(parts[0].Body, raw) {
		t.Fatalf("retrieve returned %d parts, err %v", len(parts), err)
	}

	// Metadata names the pixel data by URI and carries the sequence.
	code, _, body = get(t, base+"/studies/"+studyUID+"/series/"+seriesUID+"/metadata", "")
	if code != 200 || !strings.Contains(string(body), `"7FE00010":{"BulkDataURI":"`+base) ||
		!strings.Contains(string(body), "00081110") {
		t.Errorf("metadata: %d %s", code, body)
	}

	// Frames, as stored; and refused when only native pixels are acceptable.
	frameURL := base + "/studies/" + studyUID + "/series/" + seriesUID + "/instances/" + sopUID + "/frames/2"
	code, ct, body = get(t, frameURL, `multipart/related; type="image/jpeg"; transfer-syntax=*`)
	if code != 200 {
		t.Fatalf("frames: %d %s", code, body)
	}
	_, parts, _ = ReadMultipart(ct, bytes.NewReader(body), 1<<20, 1<<20)
	if len(parts) != 1 || !bytes.HasPrefix(parts[0].Body, []byte{0xFF, 0xD8}) {
		t.Errorf("frame 2 is not a JPEG")
	}
	if code, _, _ := get(t, frameURL, `multipart/related; type="application/octet-stream"`); code != http.StatusNotAcceptable {
		t.Errorf("a compressed frame was offered as native: %d", code)
	}

	// WADO-URI: the whole file.
	code, ct, body = get(t, base+"/wado?requestType=WADO&studyUID="+studyUID+"&seriesUID="+seriesUID+
		"&objectUID="+sopUID+"&contentType=application/dicom", "")
	if code != 200 || ct != "application/dicom" || !bytes.Equal(body, raw) {
		t.Errorf("WADO-URI: %d %s", code, ct)
	}
}

func TestStowRefusesWhatItCannotKeepAndSaysWhy(t *testing.T) {
	a, srv := newArchive(t)
	base := srv.URL + "/dicomweb"
	c := &Client{URL: base}

	// An object sent to the wrong study is not stored.
	var body bytes.Buffer
	ct, _ := WriteMultipart(&body, "application/dicom", []Part{{Body: fixture(t, "native-explicit.dcm")}})
	resp, err := http.Post(base+"/studies/1.2.3.4", ct, &body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), `"00081197":{"Value":[43264]`) {
		t.Errorf("wrong study: %d %s", resp.StatusCode, b)
	}

	// Nonsense among good objects: 202, the good one kept, the bad one listed.
	out, err := c.Store(context.Background(), fixture(t, "native-explicit.dcm"), []byte("garbage, not DICOM"))
	if err == nil || out.Status != http.StatusAccepted || len(out.Stored) != 1 {
		t.Errorf("mixed: %+v %v", out, err)
	}

	// The same instance under another study is refused rather than moved between patients.
	moved := bytes.Replace(fixture(t, "native-explicit.dcm"), []byte(studyUID+"\x00"), []byte("1.2.826.0.1.3680043.10.1337.8.1"), 1)
	if bytes.Equal(moved, fixture(t, "native-explicit.dcm")) {
		t.Skip("could not build the moved fixture")
	}
	if _, err := a.Store(context.Background(), moved, ""); err == nil {
		t.Error("an instance was moved into another study")
	}
}

func TestAUIDCannotNameAPathOutsideTheArchive(t *testing.T) {
	_, srv := newArchive(t)
	for _, p := range []string{"/dicomweb/studies/..%2F..%2Fetc", "/dicomweb/studies/1.2.3/series/../../x/metadata"} {
		code, _, _ := get(t, srv.URL+p, "")
		if code != 400 && code != 404 {
			t.Errorf("%s answered %d", p, code)
		}
	}
	if ValidUID("1.2.3/../4") || ValidUID("") || ValidUID(strings.Repeat("1", 65)) {
		t.Error("ValidUID accepted something that is not a UID")
	}
}

// OHIF asks for frames with the type parameter unquoted, which RFC 2045 does not allow and every viewer sends.
func TestFramesAreServedToAViewerThatDoesNotQuoteTheType(t *testing.T) {
	for _, accept := range []string{
		"multipart/related; type=application/octet-stream; transfer-syntax=*",
		`multipart/related; type="application/octet-stream"; transfer-syntax=*`,
		"multipart/related;type=application/octet-stream",
	} {
		r := httptest.NewRequest(http.MethodGet, "/frames/1", nil)
		r.Header.Set("Accept", accept)
		if !acceptsFrames(r, "application/octet-stream", "1.2.840.10008.1.2") {
			t.Errorf("%q was refused", accept)
		}
	}
	if mt, p, err := ParseMediaType("multipart/related; type=application/dicom; boundary=abc"); err != nil || mt != "multipart/related" ||
		p["type"] != "application/dicom" || p["boundary"] != "abc" {
		t.Errorf("got %q %v %v", mt, p, err)
	}
	for _, bad := range []string{"", "a/b/c", "multipart/related; type"} {
		if _, _, err := ParseMediaType(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
