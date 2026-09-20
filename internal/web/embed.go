// Package web serves the built front end from inside the binary.
//
// The assets are embedded rather than shipped alongside, so deployment stays one
// file. That is the same reason the engine avoids cgo: an operator should be able
// to copy a binary onto a server and run it, with no JVM, no installer and no
// asset directory to get out of step with the executable.
//
// The built output is committed so that `go build` works on a checkout without
// Node installed. Rebuild it with `make web`.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Deliberately naming what is embedded rather than taking all of dist.
//
// The WebAssembly playground is also built into dist, and it is 20 MB. Embedding it would double the
// download for every operator in order to serve an audience - people evaluating Perfuse before installing
// it - who by definition have not installed it. So dist/wasm is left out, and the playground is served from
// a website built by CI instead.
//
// A pattern that matches nothing is a compile error, which is the behaviour worth having here: if the front
// end has not been built, that should stop the build rather than produce a server that returns 404 for its
// own interface.
//
// dist/wasm is listed explicitly because it is not under dist/assets.
//
// It was omitted, so the playground was broken in every shipped build: the browser asked for /wasm/wasm_exec.js, the file was not
// in the binary, the SPA fallback answered with index.html, and Chrome refused to execute HTML as a script. The tab rendered, which
// is why the tab sweep passed - the feature simply could not run.
//
//go:embed dist/index.html dist/assets dist/wasm
var embedded embed.FS

// Assets returns the built front end as a filesystem.
func Assets() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Available reports whether a real front end was built into this binary, as
// opposed to the placeholder that keeps the package compiling.
func Available() bool {
	assets, err := Assets()
	if err != nil {
		return false
	}
	entries, err := fs.ReadDir(assets, "assets")
	return err == nil && len(entries) > 0
}

// Handler serves the front end.
//
// Anything that is not a real file falls back to index.html, because the
// interface is a single page application: a browser reloaded on a deep link must
// get the app rather than a 404.
func Handler() (http.Handler, error) {
	assets, err := Assets()
	if err != nil {
		return nil, err
	}

	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			serveIndex(w, r, assets)
			return
		}

		f, err := assets.Open(clean)
		if err != nil {
			// A missing file that is clearly an asset gets a 404, not the app.
			//
			// The fallback exists so a reload on /channels/labs serves the application. It should not answer for a missing
			// script or stylesheet, because the browser then tries to execute index.html as JavaScript and reports a MIME
			// type error - which says nothing about the actual problem, that the file is not in the binary. That is exactly
			// how the playground stayed broken: /wasm/wasm_exec.js was not embedded, and the fallback hid it behind a
			// confusing error instead of an obvious one.
			//
			// Matched on extension rather than on a path prefix, so it keeps working for asset directories nobody has
			// thought of yet.
			if isAssetPath(clean) {
				http.NotFound(w, r)

				return
			}
			serveIndex(w, r, assets)

			return
		}
		defer f.Close()

		if info, err := f.Stat(); err == nil && info.IsDir() {
			serveIndex(w, r, assets)
			return
		}

		// Hashed asset filenames change whenever their contents change, so they
		// can be cached hard. index.html must not be, or a browser keeps loading
		// an old app against a new API.
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "the web interface was not built into this binary", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// assetExtensions are the file types a browser fetches as a subresource rather than as a page.
//
// A request for one of these that does not exist is a broken build, and saying so plainly beats serving the application and letting
// the browser produce a MIME type error about it.
var assetExtensions = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".wasm": true, ".map": true,
	".json": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".ico": true, ".webp": true, ".woff": true, ".woff2": true,
	".ttf": true, ".otf": true, ".eot": true, ".webmanifest": true,
}

// isAssetPath reports whether a path names a subresource rather than an application route.
//
// Application routes are things like /channels/labs, which have no extension. A dotted segment in the middle of a route would
// confuse this, but the front end has no such routes and a test pins that.
func isAssetPath(clean string) bool {
	return assetExtensions[strings.ToLower(path.Ext(clean))]
}
