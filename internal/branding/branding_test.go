package branding

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A logo is served to every browser that loads the console, including before anyone has signed in.
// That makes it the widest-reaching attacker-supplied content in the product, so the sanitiser is
// tested by planting each attack rather than by asserting it returns something.

// pngBytes is a real 1x1 PNG, so content sniffing has something valid to identify.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// ── The sanitiser, attacked ───────────────────────────────────────────────────

func TestSanitiseSVGRemovesEveryKnownVector(t *testing.T) {
	// Each of these is a way an SVG runs code or reaches the network. The forbidden string is what
	// must not survive; if it does, the browser would act on it.
	cases := []struct {
		name      string
		svg       string
		forbidden []string
	}{
		{
			name:      "script element",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><circle r="5"/></svg>`,
			forbidden: []string{"script", "alert"},
		},
		{
			name:      "script element with attributes",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><script type="text/javascript">fetch('/api/users')</script></svg>`,
			forbidden: []string{"script", "fetch"},
		},
		{
			name:      "unclosed script",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)`,
			forbidden: []string{"<script"},
		},
		{
			name:      "onload handler",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><circle r="5"/></svg>`,
			forbidden: []string{"onload", "alert"},
		},
		{
			name:      "onclick on a child",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><circle r="5" onclick="alert(1)"/></svg>`,
			forbidden: []string{"onclick", "alert"},
		},
		{
			name:      "handler name split by whitespace",
			svg:       "<svg xmlns=\"http://www.w3.org/2000/svg\"><circle r=\"5\"\nonmouseover =\"alert(1)\"/></svg>",
			forbidden: []string{"onmouseover", "alert"},
		},
		{
			name:      "foreignObject carrying html",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body xmlns="http://www.w3.org/1999/xhtml"><script>alert(1)</script></body></foreignObject></svg>`,
			forbidden: []string{"foreignObject", "script"},
		},
		{
			name:      "javascript URL in a link",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><circle r="5"/></a></svg>`,
			forbidden: []string{"javascript:"},
		},
		{
			name:      "javascript URL via xlink",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><a xlink:href="javascript:alert(1)"><circle r="5"/></a></svg>`,
			forbidden: []string{"javascript:"},
		},
		{
			name:      "external use reference",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><use href="https://evil.example.com/x.svg#a"/><circle r="5"/></svg>`,
			forbidden: []string{"evil.example.com"},
		},
		{
			name:      "iframe",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><iframe src="https://evil.example.com"></iframe><circle r="5"/></svg>`,
			forbidden: []string{"iframe", "evil.example.com"},
		},
		{
			name:      "animate driving an attribute to a script URL",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><a><animate attributeName="href" to="javascript:alert(1)"/><circle r="5"/></a></svg>`,
			forbidden: []string{"javascript:"},
		},
		{
			name:      "style element with an import",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><style>@import url("https://evil.example.com/x.css");</style><circle r="5"/></svg>`,
			forbidden: []string{"evil.example.com", "@import"},
		},
		{
			name:      "entity declaration for expansion",
			svg:       `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaaaaaaaaa">]><svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`,
			forbidden: []string{"ENTITY", "DOCTYPE"},
		},
		{
			name:      "external entity reading a file",
			svg:       `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg"><text>&xxe;</text></svg>`,
			forbidden: []string{"ENTITY", "file:///etc/passwd"},
		},
		{
			name:      "data URL carrying html",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="><circle r="5"/></a></svg>`,
			forbidden: []string{"data:text/html"},
		},
		{
			name:      "nested script inside a removed element",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><script>alert(1)</script></foreignObject><circle r="5"/></svg>`,
			forbidden: []string{"script", "foreignObject"},
		},
		{
			name:      "mixed case evasion",
			svg:       `<svg xmlns="http://www.w3.org/2000/svg"><ScRiPt>alert(1)</ScRiPt><circle r="5"/></svg>`,
			forbidden: []string{"alert"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clean, err := SanitiseSVG([]byte(c.svg))
			if err != nil {
				// Refusing outright is an acceptable outcome; serving the payload is not.
				return
			}
			got := strings.ToLower(string(clean))
			for _, bad := range c.forbidden {
				if strings.Contains(got, strings.ToLower(bad)) {
					t.Errorf("%q survived sanitisation:\n%s", bad, clean)
				}
			}
		})
	}
}

// Sanitising twice must change nothing. A sanitiser whose output is not already clean can be
// defeated by nesting, because removing the outer construct reveals an inner one.
func TestSanitiseSVGReachesAFixedPoint(t *testing.T) {
	nasty := `<svg xmlns="http://www.w3.org/2000/svg" onload="a()">` +
		`<foreignObject><script>b()</script></foreignObject>` +
		`<a href="javascript:c()"><circle r="5" onclick="d()"/></a></svg>`

	once, err := SanitiseSVG([]byte(nasty))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := SanitiseSVG(once)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("a second pass changed the output, so the first did not finish:\nonce:  %s\ntwice: %s", once, twice)
	}
}

// A legitimate logo must survive intact, or the sanitiser is useless in practice.
func TestSanitiseSVGKeepsARealLogo(t *testing.T) {
	logo := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48">` +
		`<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
		`<stop offset="0" stop-color="#0ea5e9"/><stop offset="1" stop-color="#8b5cf6"/>` +
		`</linearGradient></defs>` +
		`<rect width="48" height="48" rx="10" fill="url(#g)"/>` +
		`<path d="M12 24h8l4-8 4 16 4-8h4" stroke="#fff" stroke-width="2" fill="none"/>` +
		`<title>Acme Health</title></svg>`

	clean, err := SanitiseSVG([]byte(logo))
	if err != nil {
		t.Fatalf("a legitimate logo was refused: %v", err)
	}
	for _, keep := range []string{"linearGradient", "url(#g)", "stop-color", "#0ea5e9", "<path", "Acme Health", "viewBox"} {
		if !strings.Contains(string(clean), keep) {
			t.Errorf("sanitising removed %q, which a real logo needs:\n%s", keep, clean)
		}
	}
}

// ── Validation ────────────────────────────────────────────────────────────────

func TestValidateSniffsRatherThanTrusting(t *testing.T) {
	// An HTML document named as an image is HTML.
	if _, _, err := Validate([]byte(`<html><body><script>alert(1)</script></body></html>`)); err == nil {
		t.Error("an HTML document was accepted as a logo")
	}
	// A shell script is not an image.
	if _, _, err := Validate([]byte("#!/bin/sh\nrm -rf /\n")); err == nil {
		t.Error("a shell script was accepted as a logo")
	}
	// A real PNG is.
	_, ct, err := Validate(pngBytes)
	if err != nil {
		t.Fatalf("a valid PNG was refused: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
}

func TestValidateRefusesOversizeAndEmpty(t *testing.T) {
	if _, _, err := Validate(nil); err == nil {
		t.Error("an empty upload was accepted")
	}
	big := make([]byte, MaxLogoBytes+1)
	copy(big, pngBytes)
	if _, _, err := Validate(big); err == nil {
		t.Error("an oversize upload was accepted")
	}
}

// A PNG whose pixel data happens to contain the bytes "<svg" must not be treated as SVG, or a raster
// image would be run through a text sanitiser and corrupted.
func TestValidateDoesNotMistakeRasterForSVG(t *testing.T) {
	data := append([]byte{}, pngBytes...)
	data = append(data, []byte("<svg onload=\"alert(1)\">")...)
	_, ct, err := Validate(data)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("content type = %q, want image/png; a PNG containing the text <svg was misidentified", ct)
	}
}

// ── The store ─────────────────────────────────────────────────────────────────

func TestStoreRoundTripsAndReplaces(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Has() {
		t.Fatal("a fresh store reports a logo")
	}
	if _, err := s.Logo(); err == nil {
		t.Error("Logo returned no error when nothing is stored")
	}

	if err := s.SetLogo(pngBytes); err != nil {
		t.Fatal(err)
	}
	got, err := s.Logo()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes, pngBytes) {
		t.Error("the stored logo differs from what was uploaded")
	}
	firstETag := got.ETag

	// Replacing with a different format must remove the old file, or two logos exist and which one
	// loads on restart depends on map iteration order.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`)
	if err := s.SetLogo(svg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "perfuse-logo.png")); !os.IsNotExist(err) {
		t.Error("the previous PNG was left behind after uploading an SVG")
	}
	after, _ := s.Logo()
	if after.ContentType != "image/svg+xml" {
		t.Errorf("content type = %q after replacing with SVG", after.ContentType)
	}
	if after.ETag == firstETag {
		t.Error("the tag did not change, so a browser would keep showing the old logo")
	}
}

// A logo must survive a restart, which is what reloading from the directory represents.
func TestStoreReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetLogo(pngBytes); err != nil {
		t.Fatal(err)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Has() {
		t.Fatal("the logo did not survive a reload")
	}
}

// A file replaced on disk between writes is re-sanitised on load. A sanitiser that runs only on
// upload protects only uploads.
func TestStoreSanitisesOnLoadNotOnlyOnUpload(t *testing.T) {
	dir := t.TempDir()
	nasty := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><circle r="5"/></svg>`
	if err := os.WriteFile(filepath.Join(dir, "perfuse-logo.svg"), []byte(nasty), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("a store over a planted file failed to open: %v", err)
	}
	got, err := s.Logo()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(got.Bytes)), "script") {
		t.Errorf("a logo planted on disk was served with its script intact:\n%s", got.Bytes)
	}
}

func TestClearLogo(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := s.SetLogo(pngBytes); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLogo(); err != nil {
		t.Fatal(err)
	}
	if s.Has() {
		t.Error("the logo is still reported after being cleared")
	}
	if err := s.ClearLogo(); err != nil {
		t.Errorf("clearing twice should be harmless, got %v", err)
	}
}

// Without a directory an upload must be refused rather than kept in memory, which would appear to
// work and then vanish on restart.
func TestStoreWithoutDirectoryRefusesUploads(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogo(pngBytes); err == nil {
		t.Error("an upload was accepted with no directory configured")
	}
}

func TestStoreIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := s.SetLogo(pngBytes); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%8 == 0 {
				_ = s.SetLogo(pngBytes)
				return
			}
			_, _ = s.Logo()
			_ = s.Has()
		}(i)
	}
	wg.Wait()
}

// ── Serving ───────────────────────────────────────────────────────────────────

// The policy is the second half of the defence. A handler that serves the bytes without it removes
// that half silently, so the headers are asserted rather than assumed.
func TestServeHeadersPermitNothing(t *testing.T) {
	h := http.Header{}
	ServeHeaders(h, &Logo{Bytes: pngBytes, ContentType: "image/png", ETag: "abc"})

	if got := h.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'none'", "sandbox"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy is missing %q: %q", want, csp)
		}
	}
	if got := h.Get("ETag"); got != `"abc"` {
		t.Errorf("ETag = %q", got)
	}
}
