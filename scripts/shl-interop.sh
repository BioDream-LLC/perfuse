#!/usr/bin/env bash
# SMART Health Links, Perfuse against an independent implementation: vintasoftware's kill-the-clipboard TypeScript library.
#
# Both directions, in one container so no network plumbing is needed:
#   1. Perfuse hosts a passcode-protected link; the library's SHLViewer resolves it - embedded, and by location - and a wrong passcode
#      is refused the way the library expects.
#   2. The library builds a link with SHLManifestBuilder, including a SMART Health Card it issued, and serves the manifest; Perfuse's
#      /api/shl/resolve reads it.
#
# Usage: ./scripts/shl-interop.sh        Needs Docker. Uses ~/.cache/perfuse-ktc, which Colima shares with containers.
set -euo pipefail
cd "$(dirname "$0")/.."

work="$HOME/.cache/perfuse-ktc"
mkdir -p "$work"
arch="$(docker version --format '{{.Server.Arch}}')"
GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$work/perfuse" ./cmd/perfuse

cat >"$work/interop.mjs" <<'JS'
import http from 'node:http'
import assert from 'node:assert/strict'
import { SHL, SHLManifestBuilder, SHLViewer, SHCIssuer, SHLInvalidPasscodeError } from 'kill-the-clipboard'
import { generateKeyPairSync } from 'node:crypto'

const perfuse = 'http://127.0.0.1:18080'
const token = process.env.PERFUSE_TOKEN
const bundle = { resourceType: 'Bundle', type: 'collection', entry: [
  { fullUrl: 'urn:uuid:p1', resource: { resourceType: 'Patient', name: [{ family: 'Doe', given: ['Jane'] }], birthDate: '1980-01-01' } },
  { fullUrl: 'urn:uuid:e1', resource: { resourceType: 'Encounter', status: 'finished' } },
] }
const api = (method, path, body) => fetch(perfuse + path, { method, headers: { Authorization: 'Bearer ' + token,
  'Content-Type': 'application/json' }, body: body && JSON.stringify(body) })

// 1. Perfuse shares; the library receives.
const created = await (await api('POST', '/api/shl', { label: 'Visit record', content: bundle, passcode: '2468' })).json()
assert.ok(created.link?.startsWith('shlink:/'), JSON.stringify(created))
const viewer = new SHLViewer({ shlinkURI: created.link })
assert.equal(viewer.shl.label, 'Visit record')
assert.equal(viewer.shl.requiresPasscode, true)
await assert.rejects(viewer.resolveSHL({ recipient: 'KTC interop', passcode: '0000' }), SHLInvalidPasscodeError)
const got = await viewer.resolveSHL({ recipient: 'KTC interop', passcode: '2468' })
assert.deepEqual(got.fhirResources[0], bundle)
const byLocation = await new SHLViewer({ shlinkURI: created.link }).resolveSHL({ recipient: 'KTC', passcode: '2468', embeddedLengthMax: 10 })
assert.deepEqual(byLocation.fhirResources[0], bundle)
console.log('1. the library resolved a Perfuse link: embedded, by location, and refused a wrong passcode')

// 2. The library shares, with a card it issued; Perfuse receives.
const files = new Map()
const shl = SHL.generate({ baseManifestURL: 'http://127.0.0.1:18081/', manifestPath: 'manifest.json', flag: 'P', label: 'From KTC' })
const builder = new SHLManifestBuilder({ shl,
  uploadFile: async (content) => { const p = 'f' + files.size; files.set(p, content); return p },
  getFileURL: async (p) => 'http://127.0.0.1:18081/files/' + p,
  loadFile: async (p) => files.get(p) })
await builder.addFHIRResource({ content: bundle, enableCompression: true })
const keys = generateKeyPairSync('ec', { namedCurve: 'P-256' })
const issuer = new SHCIssuer({ issuer: 'http://127.0.0.1:18081',
  privateKey: keys.privateKey.export({ type: 'pkcs8', format: 'pem' }), publicKey: keys.publicKey.export({ type: 'spki', format: 'pem' }) })
await builder.addHealthCard({ shc: await issuer.issue(bundle), enableCompression: true })
const server = http.createServer(async (req, res) => {
  if (req.method === 'POST' && req.url.endsWith('/manifest.json')) {
    let body = ''
    for await (const c of req) body += c
    const r = JSON.parse(body)
    if (r.passcode !== '1357') { res.writeHead(401, { 'Content-Type': 'application/json' }); return res.end('{"remainingAttempts":4}') }
    res.writeHead(200, { 'Content-Type': 'application/json' })
    return res.end(JSON.stringify(await builder.buildManifest({ embeddedLengthMax: r.embeddedLengthMax })))
  }
  const m = req.url.match(/^\/files\/(.+)$/)
  if (m && files.has(m[1])) { res.writeHead(200, { 'Content-Type': 'application/jose' }); return res.end(files.get(m[1])) }
  res.writeHead(404); res.end()
}).listen(18081, '127.0.0.1')

const resolved = await (await api('POST', '/api/shl/resolve', { link: shl.toURI(), passcode: '1357', recipient: 'Perfuse interop' })).json()
server.close()
assert.equal(resolved.label, 'From KTC', JSON.stringify(resolved))
const fhir = resolved.files.find((f) => f.contentType === 'application/fhir+json')
assert.equal(fhir.summary.patient, 'Jane Doe')
assert.deepEqual(JSON.parse(fhir.content), bundle)
const card = resolved.files.find((f) => f.contentType === 'application/smart-health-card')
assert.equal(card.cards.length, 1)
assert.equal(card.cards[0].issuer, 'http://127.0.0.1:18081')
// Decoded, and not verified: Perfuse fetches an issuer's keys only over https.
assert.equal(card.cards[0].verified, false)
assert.match(card.cards[0].problem, /https/)
console.log('2. Perfuse resolved a library link: the FHIR bundle and the SMART Health Card it issued')
JS

docker run --rm -v "$work:/w" -w /w node:22-slim sh -c '
  set -e
  [ -d node_modules/kill-the-clipboard ] || { npm init -y >/dev/null; npm install --silent kill-the-clipboard >/dev/null; }
  mkdir -p /tmp/ch
  ./perfuse serve -channels /tmp/ch -db /tmp/p.db -addr 127.0.0.1:18080 -insecure -public-url http://127.0.0.1:18080 \
    -shl-allow-http >/tmp/serve.log 2>&1 &
  for i in $(seq 1 50); do node -e "fetch(\"http://127.0.0.1:18080/api/health\").then(()=>process.exit(0),()=>process.exit(1))" && break; sleep 0.2; done
  # After the server has created the database, which the token command needs.
  TOKEN=$(./perfuse token create -db /tmp/p.db -label ktc -role editor | grep -E "^[A-Za-z0-9_-]{30,}$" | head -1)
  PERFUSE_TOKEN="$TOKEN" node interop.mjs || { tail -20 /tmp/serve.log; exit 1; }
'
