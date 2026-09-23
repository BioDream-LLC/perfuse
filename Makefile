GO      ?= go
BIN     := bin/perfuse
PKG     := ./cmd/perfuse
# The released version.
#
# Written here rather than derived from version control, so that a build from a source archive - which has no history to
# describe - reports the same version as a build from a checkout. When this was derived from git describe, unpacking the
# source and building it produced a binary that called itself "dev", which is the kind of difference nobody notices until
# somebody reports a bug against a version that does not exist.
#
# Override it for a local build: make build VERSION=mine
VERSION ?= v0.1.3
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web test vet fmt check bench clean cross release package sbom docker wasm e2e docs site screens

all: check build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

# web rebuilds the front end into internal/web/dist, which is embedded in the
# binary. The output is committed so `go build` works without Node installed.
#
# Depends on wasm because Vite copies web/public into the output, and web/public/wasm
# is not committed. Without that dependency, `make web` on a fresh clone produced a
# dist with no wasm directory, and the next `go build` failed on the embed pattern -
# so a clean checkout could be broken by running a build step in it.
web: wasm
	cd web && pnpm install --frozen-lockfile && pnpm build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal

# check is what CI runs: formatting must already be clean, not fixed in place.
# The race detector is not optional here, because the server is concurrent and a
# data race in message handling is a patient-safety defect, not a flaky test.
# check runs everything and prints a marker only on success.
#
# The marker exists because of a real mistake: for twenty-one commits this was verified by grepping the output for lines
# beginning with FAIL, and a vet failure produces no such line. So a broken test file went unnoticed while every commit
# reported green. Make exited non-zero the whole time and nobody was reading it.
#
# Grepping for a success marker cannot fail the same way: if anything above it fails, make stops and the marker is never
# printed. An absent marker is a failure whatever the rest of the output looks like.
check: check-go check-cross check-web check-web-current
	@echo "CHECK PASSED"

# GOTESTTIMEOUT is the per-package budget for the Go suite, and it is defined once.
#
# It used to be 300s here and 120s in the CI workflow, which meant the local gate and the CI gate had different
# definitions of passing. The suite went green locally and timed out in CI, and the failure said only "panic: test
# timed out" next to whichever test happened to be running - which is not where the time went.
#
# It is per package, not total. internal/engine is the slowest at about 130 seconds with the race detector on this
# hardware, and a shared CI runner is several times slower, so the budget has to have real headroom or it becomes a
# flake that depends on how busy somebody else's machine is.
GOTESTTIMEOUT ?= 600s

check-go:
	@test -z "$$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
	$(GO) vet ./...
	$(GO) test -race -timeout $(GOTESTTIMEOUT) ./...

# The front end has a small amount of logic worth testing - the palette's ranking
# above all, because a matcher that quietly stops matching looks like an empty list
# rather than like a bug. Skipped rather than failed when the dependencies are not
# installed, so a Go-only checkout still passes.
# check-cross compiles for Windows.
#
# Hospitals run Windows, and this is the cheapest possible guard against breaking it: the compiler catches a Unix-only
# import, a syscall that does not exist there, and a build tag that excludes something still referenced. It does not
# prove anything runs - only that it builds - and that limit is worth stating rather than letting a green check imply
# more than it earned.
#
# Tests are vetted rather than run, because they cannot execute here. Vet still type-checks them, which is what caught
# the message store's stale calls.
check-cross:
	@GOOS=windows GOARCH=amd64 $(GO) build ./... || { echo "the Windows build is broken"; exit 1; }
	@GOOS=windows GOARCH=amd64 $(GO) vet ./... || { echo "the Windows build is broken in a test file"; exit 1; }
	@echo "windows: builds"

check-web:
	@if [ -d web/node_modules ]; then 		cd web && npx tsc --noEmit && npx vitest run --reporter dot; 	else 		echo "web: node_modules missing, skipping (run: cd web && pnpm install)"; 	fi

# check-web-current fails when the committed bundle was not built from the committed
# source.
#
# internal/web/dist is committed so that go build works without Node, which means it
# can disagree with web/src and nothing notices. Compared against a rebuild rather
# than against git, so a correct bundle that has not been committed yet passes: the
# question is whether the bundle matches the source, not whether it is staged. Two ways that hurts. A binary built
# from source serves an interface that is not the one in the tree, so every claim
# about the interface may be false. And the browser tests serve the bundle, so editing
# a component and running them without rebuilding tests the previous bundle - which
# reports a pass, and is indistinguishable from the change working.
#
# The second is not hypothetical: it produced two false passes while this guard was
# being written, on a break that was meant to fail. The e2e target below rebuilds both
# and always did - the mistake was running playwright directly instead of through it,
# which is worth knowing because that shortcut fails silently rather than loudly.
#
# Skipped rather than failed without node_modules, matching check-web, so a Go-only
# checkout still passes.
check-web-current:
	@if [ -d web/node_modules ]; then rm -rf /tmp/perfuse-dist-before; cp -R internal/web/dist /tmp/perfuse-dist-before; $(MAKE) --no-print-directory web >/dev/null 2>&1; if ! diff -r /tmp/perfuse-dist-before internal/web/dist >/dev/null 2>&1; then echo "the front end bundle on disk is not what web/src builds:"; diff -rq /tmp/perfuse-dist-before internal/web/dist 2>&1 | head -10; echo "it has been rebuilt now - review the change and commit internal/web/dist"; rm -rf /tmp/perfuse-dist-before; exit 1; fi; rm -rf /tmp/perfuse-dist-before; else echo "web: node_modules missing, skipping bundle freshness check"; fi

bench:
	$(GO) test -run XXX -bench . -benchmem ./...

# cross builds the release matrix. No cgo anywhere, so every target is a single
# static binary with no runtime to install.
cross:
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "$(LDFLAGS)" -o dist/perfuse-$$os-$$arch$$ext $(PKG) || exit 1; \
	done

# wasm compiles the engine core for the in-browser playground.
#
# Placed in web/public so Vite serves it as a static asset rather than trying to bundle 20 MB through
# rollup. It is fetched on demand by the playground page, so nobody downloading the interface pays for it.
#
# wasm_exec.js comes from the Go distribution and has to match the compiler that built the module - a
# mismatched pair fails at instantiation with an error that says nothing useful, so it is copied here every
# time rather than committed and forgotten.
# -buildvcs=false because this artefact is committed.
#
# Go stamps the commit hash and a dirty flag into a binary it builds inside a repository. For a committed artefact that is
# circular: committing the module changes the commit, which changes the next build of the module, which makes the committed
# copy stale again. The bundle freshness check then fails on every commit, for a reason the message cannot explain.
#
# Turning the stamp off makes the build reproducible from the same source, which is what a committed artefact has to be.
wasm:
	@mkdir -p web/public/wasm
	GOOS=js GOARCH=wasm $(GO) build -trimpath -buildvcs=false 		-ldflags "-X main.version=$(VERSION)" 		-o web/public/wasm/perfuse.wasm ./cmd/perfuse-wasm
	@cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" web/public/wasm/wasm_exec.js
	@ls -lh web/public/wasm/perfuse.wasm | awk '{print "  web/public/wasm/perfuse.wasm", $$5}'

# release builds every target, then writes the checksums and the bill of materials beside them.
#
# Checksums because a hospital downloading a binary has no other way to know it is the one that was
# published, and the bill of materials because the person who reviews it will ask for one and getting it
# after the fact means rebuilding to be sure it matches.
release: cross sbom package
	@cd dist && shasum -a 256 perfuse-* > SHA256SUMS && 		echo "  dist/SHA256SUMS"

# package wraps each binary with the things somebody needs beside it.
#
# A bare binary is not a release. Whoever downloads it needs the licence it is offered under, the notice that names what
# is in it, and the manual - and needs them without going back to a web page they may not have open. Archives also
# survive being copied around in a way a bare executable does not: a .tar.gz keeps the executable bit, and a .zip is
# what Windows can open with nothing installed.
#
# Each archive unpacks into its own directory rather than scattering files into the current one, because somebody
# extracting three of these to compare them should not have them overwrite each other.
#
# The version is in the archive name and in the directory it unpacks to, so a file sitting in a downloads folder
# says what it is without being run, and two releases extracted side by side do not collide. That second part was
# already the stated intent of using a directory at all, and unversioned names quietly defeated it: extracting
# v0.1.1 and v0.1.2 of the same platform overwrote one with the other.
#
# The executable inside keeps its unversioned name on purpose. A systemd unit, a launchd plist or a Windows
# service definition names a path, and renaming the binary every release would break all three on upgrade.
# Which version it is is a question `perfuse version` answers.
package:
	@mkdir -p dist
	@for f in dist/perfuse-*; do \
		case "$$f" in *.tar.gz|*.zip|*.json|*SHA256SUMS) continue;; esac; \
		base=$$(basename $$f); \
		name=$${base%.exe}; \
		stage="dist/stage/perfuse-$(VERSION)-$${name#perfuse-}"; \
		rm -rf $$stage; mkdir -p $$stage; \
		cp $$f $$stage/$$(basename $$f); \
		cp LICENSE NOTICE README.md $$stage/; \
		if [ -f docs/manual/perfuse-manual.pdf ]; then cp docs/manual/perfuse-manual.pdf $$stage/; fi; \
		vname="perfuse-$(VERSION)-$${name#perfuse-}"; \
		case "$$base" in \
			*.exe) (cd dist/stage && zip -qr ../$$vname.zip $$vname) && echo "  dist/$$vname.zip";; \
			*) tar -czf dist/$$vname.tar.gz -C dist/stage $$vname && echo "  dist/$$vname.tar.gz";; \
		esac; \
	done
	@rm -rf dist/stage

sbom: build
	@mkdir -p dist
	@./bin/perfuse sbom -json > dist/perfuse.cdx.json
	@echo "  dist/perfuse.cdx.json"

# The image is scratch-based, so this needs nothing installed inside it.
docker:
	docker build -t perfuse:$(VERSION) -t perfuse:latest .

clean:
	rm -rf bin dist

# End-to-end tests that drive the real GUI in a real browser.
#
# Deliberately not part of `check`. These start a server and drive Chromium, so they take seconds rather than milliseconds and
# they need a browser downloaded (pnpm exec playwright install chromium, about 95MB). `check` has to stay fast enough to run on
# every commit; this is a QA pass run on purpose.
#
# They test the embedded build, so the binary and the web assets are rebuilt first. Testing the vite dev server would pass while
# the shipped binary served a stale internal/web/dist, which is exactly the drift worth catching.
e2e: web build
	cd web && pnpm exec playwright test

# docs builds the reference manual as one HTML file and a PDF.
#
# The PDF is printed from the HTML by the browser Playwright already installs, so this needs no Ruby, no gems and no TeX
# distribution - only what the end-to-end tests already require. The two files cannot disagree about layout because the
# layout is defined once, in the manual's own stylesheet.
# The website, built from the documents this repository already publishes rather than from a
# separate copy of them. SITE_BASE is the canonical origin; every canonical tag, og:url and
# sitemap entry is written from it, so building with the wrong one is visible rather than silent.
SITE_BASE ?= https://perfuse.health

# Web-sized screenshots.
#
# A 3000px PNG screenshot is around 460 KB and is displayed at about 800px, so the landing page was
# carrying two megabytes of images to show four of them. The same images as WebP at the size they are
# actually shown come to 341 KB for all seven. The README keeps the PNGs, because GitHub documents
# PNG, GIF, JPEG and SVG as its image types and does not list WebP.
#
# Needs sips (macOS) and cwebp. Records the source hashes so that a PNG changed without regenerating
# is caught by a test rather than shipping a stale picture of the product.
screens:
	@command -v cwebp >/dev/null || { echo "cwebp not found: brew install webp"; exit 1; }
	@mkdir -p docs/assets/screens/web
	@for f in docs/assets/screens/*.png; do \
		b=$$(basename "$$f" .png); \
		sips -Z 1600 "$$f" --out "/tmp/perfuse-$$b.png" >/dev/null; \
		cwebp -q 82 -quiet "/tmp/perfuse-$$b.png" -o "docs/assets/screens/web/$$b.webp"; \
		rm -f "/tmp/perfuse-$$b.png"; \
		echo "  $$b.webp"; \
	done
	@( head -9 docs/assets/screens/web/sources.sha256 2>/dev/null || true; \
	   cd docs/assets/screens && for f in *.png; do shasum -a 256 "$$f"; done ) > /tmp/perfuse-sums \
	   && mv /tmp/perfuse-sums docs/assets/screens/web/sources.sha256
	@echo "  sources.sha256 updated"

site: docs
	go run ./cmd/perfusesite -out dist-site -base "$(SITE_BASE)"

docs:
	go run ./cmd/perfusedoc -version "version 0.1 · $$(git rev-parse --short HEAD)"
	cd web && node print-manual.mjs ../docs/manual/perfuse-manual.html ../docs/manual/perfuse-manual.pdf
