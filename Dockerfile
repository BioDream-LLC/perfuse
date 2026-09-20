# Perfuse in a container.
#
# Two stages, and the second is scratch. That is possible because the engine is pure Go with CGO disabled -
# the SQLite driver is a Go translation rather than a binding - so there is nothing to link against and
# nothing in the image except the binary, a certificate bundle and a timezone database.
#
# The result is worth stating plainly to whoever reviews it: an image with no shell, no package manager and
# no libc has no package CVEs, because it has no packages. A Java integration engine's base image needs
# patching on somebody else's schedule for as long as it runs.

FROM golang:1.26-alpine AS build

# Node is needed for the web interface, which is compiled into the binary rather than served from a
# directory. One artefact means the UI cannot drift out of step with the API it talks to.
RUN apk add --no-cache nodejs npm git ca-certificates tzdata && npm install -g pnpm@9

WORKDIR /src

# Dependencies first, so a source change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile

COPY . .

RUN cd web && pnpm build

# Trimpath removes local filesystem paths from the binary, and -w -s drop the debug information. Both make
# the build reproducible for somebody checking that the binary matches the source.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-w -s" \
      -o /out/perfuse ./cmd/perfuse

# A smoke test in the build, so a broken binary never becomes an image.
RUN /out/perfuse version && /out/perfuse sbom -direct


FROM scratch

# Root certificates, for FHIR over HTTPS and for S3. Without these every outbound TLS connection fails
# with a certificate error that looks like a server problem.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# The timezone database. HL7 timestamps carry offsets and a channel may be configured in a named zone; on
# scratch there is no /usr/share/zoneinfo, so time.LoadLocation fails and the failure surfaces as a wrong
# timestamp rather than as an error.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

COPY --from=build /out/perfuse /perfuse

# Not root. There is no /etc/passwd here, so the numeric id is used directly; 65532 is the conventional
# non-root uid for distroless images.
USER 65532:65532

# Channels are read from a volume and the database is written to one. Both are mounted rather than baked,
# because a channel file is configuration a site owns and an image is something they replace.
VOLUME ["/channels", "/data"]

EXPOSE 8443

# No shell in the image, so this is the exec form and there is no entrypoint script. A container that
# cannot run a shell also cannot be used to poke around inside a system holding patient data, which is a
# property worth keeping.
ENTRYPOINT ["/perfuse"]
CMD ["serve", "-addr", "0.0.0.0:8443", "-channels", "/channels", "-db", "/data/perfuse.db"]

# There is no HEALTHCHECK because scratch has no curl and no shell to run one. Perfuse serves /livez and
# /readyz, so the orchestrator should check those - Kubernetes and Compose both can, without needing a
# binary inside the image.
#
# Use /readyz for the readiness probe specifically: it reports not-ready for the drain period after
# SIGTERM, which is what stops traffic arriving at a container that is shutting down. /livez stays up until
# the process exits, so using it for readiness would send messages to a server that is draining.
