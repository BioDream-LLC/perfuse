# Releasing Perfuse

What to do to publish a release, and what the previous one could not verify.

This exists because the download links on the front page are the one part of this project that cannot be
tested from inside it. Everything else has something that fails when it is wrong. A release has a
checklist, and a checklist that lives in somebody's head is the same as no checklist.

## Before anything is published

```sh
make check        # formatting, vet, race detector, cross-compilation, type checking, unit suites
make e2e          # the browser suite — stop colima first, see below
make docs         # the manual, HTML and PDF
```

**Stop the container VM before running the browser suite.** `colima stop`. On a 16 GB machine the
interoperability containers hold around 10 GB, and the browser is then starved badly enough that
Playwright's stability check never sees two consecutive animation frames. That produced a family of
failures that looked like race conditions in the application and were not. With the VM stopped the suite
has passed 336 of 336 repeatedly.

## Cutting the release

```sh
# Set the version in the Makefile first. It is written there rather than derived from version control, so that
# a build from a source archive reports the same version as a build from a checkout.
make release                      # six targets, archives, checksums, SBOM
```

`make release` produces, in `dist/`:

- six binaries — linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64
- an archive per target, each containing the binary, `LICENSE`, `NOTICE`, `README.md` and the PDF manual
- `SHA256SUMS`
- `perfuse.cdx.json`, a CycloneDX software bill of materials

Then verify the artefacts rather than trusting them:

```sh
./dist/perfuse-darwin-arm64 version                     # or whichever is native here

# Linux, in a container, with no glibc present. Proves the static linking claim.
docker run --rm -v "$PWD/dist:/d:ro" alpine:3 /d/perfuse-linux-amd64 version

# And that it actually serves the embedded interface, which `version` does not show.
docker run --rm -d -v "$PWD/dist:/d:ro" -p 18080:8080 --name rel alpine:3 \
  sh -c "mkdir -p /tmp/ch && /d/perfuse-linux-amd64 serve -addr 0.0.0.0:8080 -db /tmp/p.db -channels /tmp/ch -insecure"
curl -s http://127.0.0.1:18080/ | grep -o "<title>[^<]*</title>"    # expect <title>Perfuse</title>
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18080/api/nosuchthing   # expect 404, not 200
docker rm -f rel
```

The last check is there for a specific reason. An unmatched `/api/` path used to fall through to the
handler serving the web application and answer 200 with HTML, which made a missing endpoint look like a
successful request. It is worth confirming in the artefact that ships, not only in the test suite.

`-insecure` is only for that throwaway container. Perfuse refuses to serve patient data over plain HTTP on
a non-loopback address unless told to, which is the correct default and should never be used in anger.

## Publishing

1. Create the GitHub release against the tag.
2. Upload everything in `dist/` — the archives, `SHA256SUMS` and `perfuse.cdx.json`.
3. Check the download table in `README.md` points at the new tag. The links are literal URLs containing
   the version, so they do not follow a tag automatically.
4. No container image is published. The `Dockerfile` is in the repository and `README.md` tells
   people to build it themselves, which is honest as long as nothing is pushed to a registry.

## What the v0.1.0 release could not verify

Recorded rather than left implicit, because an unverified claim that nobody wrote down becomes a claim
everybody assumes was checked.

- **The Windows binaries have not been run.** They cross-compile, `go vet` passes for `GOOS=windows`, and
  `file` reports a valid PE32+ executable — but no Windows machine was available. `perfuse service` exists
  for running as a Windows service and is likewise untested on Windows.
- **The ARM64 Linux binary has not been run**, only built. The amd64 one was verified in a container.
- **No container image has been published.** The `Dockerfile` builds and `README.md` names an image
  location; nothing is there yet.
- **No production traffic anywhere.** The engine, transports and interface are tested, and the parts that
  talk to other people's software are verified against that software rather than against mocks — a real
  Mirth Connect, a real Keycloak, a real Microsoft Entra tenant, HAPI FHIR, Orthanc, PostgreSQL, SFTP and
  ActiveMQ. But no site has run clinical traffic through it. The README says so, and shadow mode exists so
  that nobody has to take its word.

## Known flake: the Alerts widget sweep

`every control on Alerts responds` in `web/e2e/widgets.spec.ts` fails roughly one run in four, with:

```
checkbox 0: would not click
checkbox 0: clicked and stayed true
```

Diagnosed rather than retried. `Alerts.tsx` reloads on a fifteen-second interval and that test takes
fourteen to thirty seconds, so a reload lands mid-test, React replaces the row, and the checkbox
Playwright is holding is detached — the click then times out at five seconds and the state has not
changed, which is exactly the pair of messages above.

It is a harness race rather than a broken control: the same test passes on the next run, and the
control works by hand. But it is not purely a test artefact either. A user clicking that checkbox at
the moment the poll lands can have the click go nowhere, which suggests the list is keyed by
something that changes on reload. Worth fixing at the source rather than by adding a retry.

v0.1.1 was released with this failing, deliberately: 335 of 336 passed, the failure is on a view the
release did not touch, and the release fixes a first-run defect that every Windows and macOS user
hits. Written down so that decision is visible rather than assumed.

## Two conventions worth keeping

**Documentation ships in the commit that changes the behaviour.** Twice in one day this project found
knowledge recorded in prose next to the wrong file and never propagated to the code it described — a Mirth
fault documented in a fixture comment since August, and a preview mechanism explained in one spec while
another raced it.

**A guard is verified by breaking the thing it guards.** A test that has never failed is a hypothesis. When
breaking it, confirm the break compiles: a change that does not compile looks exactly like a passing test,
and that has caught this project more than once.
