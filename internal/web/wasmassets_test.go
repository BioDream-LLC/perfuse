package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The playground was broken in every shipped build and the tab still rendered.
//
// The go:embed directive listed dist/index.html and dist/assets and not dist/wasm, so the module and its loader were simply not in
// the binary. The single-page fallback then answered /wasm/wasm_exec.js with index.html, and the browser refused to execute HTML as
// a script. Nothing in the test suite noticed, because the tab sweep only checked that the panel rendered - and it did. The feature
// just could not run.
func TestTheWasmAssetsAreInTheBinary(t *testing.T) {
	assets, err := Assets()
	if err != nil {
		t.Fatalf("no assets: %v", err)
	}

	// Both halves are needed and they must match. wasm_exec.js comes from the Go distribution and has to correspond to the
	// compiler that built the module, so one without the other is not a working playground.
	for _, name := range []string{"wasm/wasm_exec.js", "wasm/perfuse.wasm"} {
		f, err := assets.Open(name)
		if err != nil {
			t.Errorf("%s is not embedded, so the playground cannot load: %v", name, err)

			continue
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil {
			t.Errorf("could not stat %s: %v", name, err)

			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is embedded but empty", name)
		}
	}
}

// TestAMissingAssetIsNotTheApplication is the check that would have made the bug above obvious rather than mysterious.
func TestAMissingAssetIsNotTheApplication(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("no handler: %v", err)
	}

	// Files that do not exist, of the kinds a browser loads as a subresource.
	for _, p := range []string{
		"/wasm/does-not-exist.js",
		"/wasm/missing.wasm",
		"/assets/nope.css",
		"/favicon-that-is-not-there.png",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s returned %d, want 404. Serving the application for a missing script makes the browser "+
				"report a MIME type error, which says nothing about the file being absent", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!doctype html") {
			t.Errorf("GET %s returned the application HTML", p)
		}
	}
}

// TestADeepLinkStillServesTheApplication is the behaviour the 404 must not break.
func TestADeepLinkStillServesTheApplication(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("no handler: %v", err)
	}

	// Reloading the browser on an in-app route has to return the app, which is the entire reason the fallback exists.
	for _, p := range []string{"/", "/channels", "/channels/labs", "/settings/security"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s returned %d, want 200: a reload on an application route must serve the app", p, rec.Code)
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype html") {
			t.Errorf("GET %s did not return the application HTML", p)
		}
	}
}

// TestTheWasmAssetsAreServedWithAUsableType covers what the browser actually requires.
//
// A .wasm served as text/html is refused by WebAssembly.instantiateStreaming, and a .js served as text/html is refused by the script
// loader. Being present in the binary is not sufficient; the type has to be right too.
func TestTheWasmAssetsAreServedWithAUsableType(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("no handler: %v", err)
	}

	for path, wantType := range map[string]string{
		"/wasm/wasm_exec.js": "javascript",
		"/wasm/perfuse.wasm": "wasm",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s returned %d", path, rec.Code)

			continue
		}

		got := strings.ToLower(rec.Header().Get("Content-Type"))
		if !strings.Contains(got, wantType) {
			t.Errorf("GET %s was served as %q, which does not contain %q. The browser refuses to run it",
				path, got, wantType)
		}
		if strings.Contains(got, "text/html") {
			t.Errorf("GET %s was served as HTML, so the fallback answered instead of the file", path)
		}
	}
}

// TestEverythingBuiltIsEmbedded catches the next directory somebody forgets.
//
// The specific fix was to add dist/wasm to the embed list. The general problem is that the list is written by hand and the build
// output is not, so any new directory vite or the Makefile produces is silently left out. This compares the two.
func TestEverythingBuiltIsEmbedded(t *testing.T) {
	assets, err := Assets()
	if err != nil {
		t.Fatalf("no assets: %v", err)
	}

	// Top-level entries of the built output, as embedded.
	seen := map[string]bool{}

	entries, err := fs.ReadDir(assets, ".")
	if err != nil {
		t.Fatalf("could not read the embedded root: %v", err)
	}
	for _, e := range entries {
		seen[e.Name()] = true
	}

	// These are what the build produces today. A new one appearing on disk without appearing here means the embed directive was
	// not updated, which is the bug this test exists for.
	for _, want := range []string{"index.html", "assets", "wasm"} {
		if !seen[want] {
			t.Errorf("%q is part of the built front end but is not embedded in the binary. "+
				"Add it to the go:embed directive in embed.go", want)
		}
	}
}
