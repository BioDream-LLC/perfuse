<div align="center">

<img src="docs/assets/perfuse-logo.svg" alt="Perfuse" width="88" height="88">

# Perfuse

### Open-source healthcare integration engine — HL7 v2, FHIR, DICOM, X12 and CDA in one static binary

**A modern, Apache-2.0 licensed alternative to Mirth Connect.** No JVM. No installer. No paid licence.
Your existing Mirth JavaScript runs unchanged.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![Release](https://img.shields.io/badge/release-v0.1.0-success.svg)](#download)
[![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey.svg)](#download)
[![Tests](https://img.shields.io/badge/tests-verified%20against%20real%20Mirth%2C%20Keycloak%20%26%20Entra-brightgreen.svg)](#verified-against-real-software-not-mocks)

### Download v0.1.0

[![Download for Linux](https://img.shields.io/badge/Download-Linux-E95420?logo=linux&style=for-the-badge&logoColor=white)](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-linux-amd64.tar.gz)
[![Download for macOS](https://img.shields.io/badge/Download-macOS-000000?logo=apple&style=for-the-badge&logoColor=white)](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-darwin-arm64.tar.gz)
[![Download for Windows](https://img.shields.io/badge/Download-Windows-0078D6?logo=windows&style=for-the-badge&logoColor=white)](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-windows-amd64.zip)

<sub>Linux x86-64 · macOS Apple Silicon · Windows x64 — one file, nothing to install.
[Other architectures and checksums](#download)</sub>

[**Quick start**](#quick-start) · [**Why Perfuse**](#why-perfuse-exists) ·
[**Mirth migration**](#migrating-from-mirth-connect) · [**Features**](#features) ·
[**Manual**](docs/manual/perfuse-manual.html) · [**Reference**](docs/reference.md) · [**FAQ**](#faq)

</div>

---

## What is Perfuse?

**Perfuse is an open-source healthcare integration engine** — also called an interface engine — for moving
clinical data between systems that were never designed to talk to each other. It parses and routes
**HL7 v2**, **FHIR R4/R4B/R5**, **DICOM**, **X12 837/835/270/271** and **C-CDA / CDA** documents, and it
ships as a **single static binary** with the web console built in.

It is written in Go, licensed under **Apache 2.0**, and runs on **Linux, macOS and Windows** with nothing
installed beside it — no Java runtime, no application server, no database to stand up first.

```
   ┌──────────┐      ┌─────────────────────────────────┐      ┌──────────────┐
   │   LIS    │─MLLP─▶│                                 │─FHIR─▶│   EHR / API  │
   ├──────────┤      │            P E R F U S E         │      ├──────────────┤
   │ Radiology│─DICOM▶│  parse → filter → transform →   │─MLLP─▶│   Downstream │
   ├──────────┤      │        queue → deliver          │      ├──────────────┤
   │  Payer   │──X12─▶│                                 │─SFTP─▶│   Archive    │
   └──────────┘      └─────────────────────────────────┘      └──────────────┘
                          durable queue · shadow mode
                          alerts · audit · metrics
```

### Who it is for

- Integration teams running **HL7 v2 interfaces** who want the engine's source and no licence renewal
- Sites on **Mirth Connect 4.5.2 or earlier**, now unable to take upstream fixes
- Anyone building **FHIR APIs** on top of existing v2 feeds
- Labs, imaging centres, payers and health systems who need **X12**, **DICOM** or **CDA** alongside v2

---

## Download

**Version 0.1.0** — single binary, nothing to install. Each archive contains the executable, the licence,
the notice and the full PDF manual.

| Platform | Architecture | Download |
|---|---|---|
| **Windows** | x64 (Intel/AMD) | [`perfuse-windows-amd64.zip`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-windows-amd64.zip) |
| **Windows** | ARM64 | [`perfuse-windows-arm64.zip`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-windows-arm64.zip) |
| **macOS** | Apple Silicon (M1–M4) | [`perfuse-darwin-arm64.tar.gz`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-darwin-arm64.tar.gz) |
| **macOS** | Intel | [`perfuse-darwin-amd64.tar.gz`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-darwin-amd64.tar.gz) |
| **Linux** | x86-64 | [`perfuse-linux-amd64.tar.gz`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-linux-amd64.tar.gz) |
| **Linux** | ARM64 / aarch64 | [`perfuse-linux-arm64.tar.gz`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/perfuse-linux-arm64.tar.gz) |

**Verify what you downloaded** against [`SHA256SUMS`](https://github.com/biodream-llc/perfuse/releases/download/v0.1.0/SHA256SUMS):

```sh
shasum -a 256 -c SHA256SUMS --ignore-missing     # macOS / Linux
```

A CycloneDX software bill of materials ships with every release as `perfuse.cdx.json`, and any build can
produce its own with `perfuse sbom -json`.

<details>
<summary><b>Other ways to install</b></summary>

```sh
# Go 1.23 or newer
go install github.com/biodream-llc/perfuse/cmd/perfuse@latest

# Container — scratch-based, nothing inside but the binary
docker run -p 8080:8080 ghcr.io/biodream-llc/perfuse:v0.1.0

# From source
git clone https://github.com/biodream-llc/perfuse && cd perfuse && make build
```

The Linux binaries are statically linked and verified to run on Alpine, so they need no glibc and work in
`scratch` and distroless images.

</details>

---

## Quick start

```sh
# 1. Start the console. The web interface is inside the binary.
perfuse serve

# 2. Open http://127.0.0.1:8080 and build a channel in the browser.
```

That is the whole installation. There is no separate web application to deploy and no assets directory to
point at.

`perfuse init` will lay out the directories, write a working example channel and generate a service
definition for your operating system — a systemd unit, a launchd plist or a Windows service.

To run a channel from the command line instead:

```sh
# Listen for HL7 v2 over MLLP and write each message to a file
cat > labs.yaml <<'YAML'
name: labs
description: Accepts HL7 v2 over MLLP and files each message

source:
  type: mllp
  # Localhost only. MLLP has no authentication or encryption of its own.
  listen: 127.0.0.1:2575

destinations:
  - name: archive
    type: file
    # One file per day per message type, appended. So five ADT messages of two
    # kinds produce two files, not five.
    dir: ./received
YAML

# Check it before running it
perfuse check labs.yaml

# Run it
perfuse run labs.yaml

# In another terminal, send it synthetic traffic
perfuse generate -n 5 -send 127.0.0.1:2575
```

**Everything in the configuration files can also be done from the web interface.** That is a rule this
project holds itself to, not an aspiration — a feature reachable only from YAML is treated as a bug.

---

## Why Perfuse exists

**In March 2025, NextGen Healthcare closed Mirth Connect's source.** From version 4.6 a paid licence is
required. Version 4.5.2 remains available under the Mozilla Public License, which means every site still
running it is on a release that will never receive another upstream fix — including security fixes.

That leaves a real problem. Mirth Connect runs a very large share of the HL7 interfaces in production
today, those interfaces work, and the people who maintain them did not choose this. Rewriting them is not
a weekend's work, and paying to keep receiving patches for software you already deployed is a different
proposition from the one you originally accepted.

Perfuse is a way out that does not require a rewrite:

- **Your Mirth JavaScript runs unchanged**, E4X and all — `msg['PID']['PID.5']['PID.5.1']`, `for each`,
  XML literals, `channelMap`, `$()`, `DateUtil`, `SerializerFactory`
- **`perfuse translate` converts Mirth channel exports into Perfuse channels**, not just a report saying
  what it found
- **`perfuse explain`** reads a Mirth export and says plainly what would block a migration, before you
  start
- **Shadow mode** runs the converted channel beside the original on real traffic and shows exactly where
  the two disagree — while only the original delivers anything
- **You can export back to Mirth.** A channel built in Perfuse can be written as a Mirth channel file, and
  a real Mirth server accepts it. Leaving is as supported as arriving.

The last point is deliberate. An engine you cannot leave is the problem, not the solution.

---

## Verified against real software, not mocks

Most integration tooling is tested against its own idea of what the other side does. That is how a product
ends up with a hundred passing tests and a feature that has never once worked against the real thing.

Perfuse is verified against actual running software. What was checked, against which versions, and what
it found is recorded in [docs/verification.md](docs/verification.md):

| Verified against | What it proved |
|---|---|
| **Mirth Connect 4.5.2** (real server) | Channels Perfuse exports are accepted and deploy. Found four faults the exporter's own nine passing tests could not see — Mirth stores an invalid channel and *returns success* |
| **Keycloak 26** | SAML sign-in end to end. Found a signature-canonicalisation defect a thousand self-written tests had missed |
| **Microsoft Entra ID** (real tenant) | SAML sign-in with a real Microsoft account, including an interactive browser sign-in. Found five behaviours no reading of the specification predicts |
| **HAPI FHIR** | FHIR resources Perfuse produces validate in an independent server |
| **Orthanc** | DICOM C-STORE and C-FIND against a real PACS |
| **PostgreSQL, SFTP, ActiveMQ** | Database, file transfer and broker destinations against real services |

Interoperability tests run against containers, not stubs. Where a real product's behaviour contradicted
the specification, the real behaviour won and a fixture recording it was committed.

---

## Features

### HL7 v2

A parser that **indexes rather than decodes**, so a message is not rebuilt to read one field. Segments,
repetitions, components and subcomponents, escape sequences, custom Z-segments and non-standard
separators. MLLP in both directions with TLS. A field dictionary means the interface can tell you that
`PID-5.1` is the patient's family name instead of showing you a path.

### FHIR — a first-class feature, not an export format

**R4, R4B and R5.** An HL7 v2 to FHIR mapper, a FHIR REST server you can point a client at, `fhir` as a
channel destination type, validation, and a browser-based FHIR lab. Search parameters, bundles,
transactions and references.

### DICOM

C-STORE, C-FIND and C-ECHO, in both directions. Metadata extraction, de-identification steps, and
conversion of studies into observations that can travel as v2 or FHIR.

### X12

**837 professional and institutional, 835 remittance, 270/271 eligibility.** Loops, segments and
qualifiers parsed properly rather than treated as delimited text.

### Clinical documents (C-CDA / CDA)

Read, validated and converted — **including documents that arrive base64-encoded inside an `MDM^T02`**,
which is how they usually turn up. The narrative text and the coded entries are compared against each
other, so a document whose human-readable section disagrees with its structured data is flagged. Nothing
else does this.

### Delivery that does not lose messages

A **durable on-disk queue** keeps messages in order when a receiver goes down and delivers when it comes
back. Retries with backoff, dead-lettering, and a queue you can inspect, retry and drain from the
interface.

### Connectors hospitals actually run

**MLLP · HTTP/HTTPS · files · SFTP · databases (PostgreSQL, MySQL, SQL Server, Oracle) · DICOM · SMTP ·
SOAP · message brokers (STOMP/AMQP) · JavaScript Reader · S3** — in both directions.

### Transformation and filtering

Ten declarative transformation steps that need no code, a filter expression language, shared mapping
tables with a record of who decided each mapping and why, and full JavaScript when you need it —
including the Mirth dialect.

### Shadow mode

Run a **candidate version of a channel beside the live one on real traffic**. Perfuse shows precisely
where the two differ, field by field, and the candidate delivers nothing. This is how you change a
working interface without guessing.

### Feed contracts

Declare what a feed is *supposed* to contain — which fields, which code sets, which cardinalities — and
Perfuse tells you when reality drifts. Most interface outages are a sender changing something quietly.

### Operations

A live **dashboard**, **metrics** with percentiles and Prometheus exposition, **alerts** that tell you
rather than waiting to be found, a **message store** with retention and search, a **flow map**, and an
**audit log** of who changed what.

### Security

**SAML 2.0** (verified against Keycloak and Microsoft Entra, configurable by reading the provider's
metadata — no certificate transcription), **OpenID Connect**, **LDAP/Active Directory**, **passkeys /
WebAuthn**, TLS with mutual authentication, role-based access, and **UDAP / TEFCA** support. Refuses to
serve patient data over plain HTTP on a public interface unless you explicitly override it.

### Fleet

Run more than one instance and see them together — channel counts, health and queue depth across the
estate.

---

## Migrating from Mirth Connect

```sh
# 1. What would stop you, before you commit to anything
perfuse explain mirth-channel-export.xml

# 2. Convert it
perfuse translate mirth-channel-export.xml -o channels/

# 3. Test it against sample messages
perfuse test channels/labs.yaml

# 4. Run it beside the original on real traffic, delivering nothing
perfuse serve   # then switch the channel to shadow mode in the interface
```

`perfuse explain` is the step worth running first. It reads the export and tells you what converts
cleanly, what converts with caveats, and what does not convert at all — by name, not as a count. A
migration that surprises you in week three is worse than one that refuses on day one.

**What does not convert is reported, never silently approximated.** Perfuse will refuse a destination type
it cannot honour rather than write something close and let you discover the difference in production.

Full detail: [Migrating from Mirth](docs/reference.md#migrating-from-mirth) ·
[Mirth scripts](docs/reference.md#mirth-scripts)

---

## Perfuse vs Mirth Connect

| | Perfuse | Mirth Connect 4.6+ |
|---|---|---|
| **Licence** | Apache 2.0, open source | Commercial, paid |
| **Source available** | Yes | No (closed since 4.6) |
| **Runtime** | Single static binary | JVM + application server |
| **Install** | Copy one file | Installer, Java, database setup |
| **Memory at rest** | Tens of MB | Hundreds of MB to GB |
| **Existing Mirth JavaScript** | Runs unchanged, E4X included | Native |
| **FHIR** | R4/R4B/R5, mapper + REST server built in | Add-on / manual |
| **DICOM** | Built in, both directions | Limited |
| **X12** | 837, 835, 270/271 parsed structurally | Text handling |
| **CDA narrative vs coded comparison** | Yes | No |
| **Shadow mode on live traffic** | Yes | No |
| **Feed contracts / drift detection** | Yes | No |
| **Export back to the other engine** | Yes — writes Mirth channel files a real Mirth accepts | N/A |
| **Configuration from the web UI** | Everything | Most things |

Perfuse is younger and has a smaller connector catalogue. Where a capability is missing it says so rather
than approximating it.

---

## Documentation

| | |
|---|---|
| [**Reference**](docs/reference.md) | Every command, connector, transformation step and configuration key |
| [**Manual (HTML)**](docs/manual/perfuse-manual.html) | The full book, with worked examples |
| [**Manual (PDF)**](docs/manual/perfuse-manual.pdf) | The same, for printing and for aeroplanes |
| [**Verification record**](docs/verification.md) | What was tested against which real software, what it found, and what remains unverified |
| [**Security**](docs/reference.md#tls) | TLS, authentication, audit and what is deliberately refused |
| [**HL7 Go API**](docs/hl7-api.md) | Using the parser as a library in your own Go programs |

The manual is also served by the running instance, so an operator never has to find this page.

---

## Using Perfuse as a Go library

The HL7 v2 parser is a standalone package with no dependency on the engine:

```go
import "github.com/biodream-llc/perfuse/hl7"

msg, err := hl7.Parse(raw)
if err != nil {
    return err
}

// Path lookup returns an error for a path that is not valid, rather than an
// empty string that could equally mean "absent" or "you typed it wrong".
family, err := msg.Get("PID-5.1")     // patient's family name
if err != nil {
    return err
}

for _, obx := range msg.Segments("OBX") {
    fmt.Println(obx.Field(3).Component(2).String(), obx.Field(5).String())
}
```

It indexes rather than decoding, so reading one field does not rebuild the message.
[API documentation](docs/hl7-api.md).

---

## FAQ

<details>
<summary><b>Is Perfuse really free? What is the licence?</b></summary>

Yes. **Apache License 2.0** — commercial use, modification, distribution and private use are all
permitted, with no fee and no per-channel or per-interface pricing. See [LICENSE](LICENSE) and
[NOTICE](NOTICE).

</details>

<details>
<summary><b>Will my existing Mirth channels work?</b></summary>

Mirth JavaScript transformers and filters run unchanged, E4X included. Channel exports are converted with
`perfuse translate`. Run `perfuse explain` on your export first — it tells you by name what will and will
not convert. Anything that cannot convert is reported rather than approximated.

</details>

<details>
<summary><b>Can I go back to Mirth if I change my mind?</b></summary>

Yes. A channel built in Perfuse can be exported as a Mirth channel file, and this is verified against a
real Mirth server rather than against our own parser. An engine you cannot leave is the problem, not the
solution.

</details>

<details>
<summary><b>Do I need Java, a database or an application server?</b></summary>

No. One static binary. It uses an embedded database by default and PostgreSQL if you point it at one. The
web console is compiled into the executable.

</details>

<details>
<summary><b>Is it HIPAA compliant?</b></summary>

Compliance is a property of a deployment, not of software, so no product can honestly claim it on your
behalf. What Perfuse provides: TLS with mutual authentication, an audit log of who changed what,
role-based access control, message-store retention limits, de-identification steps, and a refusal to serve
patient data over plain HTTP on a public interface unless you explicitly override it. The
[security chapter](docs/reference.md#tls) is written for the person who has to sign off on it.

</details>

<details>
<summary><b>Is this production ready?</b></summary>

It is version 0.1.0 — the first public release. The engine, queue, transports and web interface are
complete and tested, and the parts that talk to other people's software are verified against that software
rather than against mocks. It has not yet run production traffic at a site other than the author's own
testing. **Use shadow mode.** Run it beside what you have, on real messages, delivering nothing, and read
the differences for yourself. That feature exists precisely because you should not take this paragraph's
word for it.

</details>

<details>
<summary><b>What is missing?</b></summary>

A smaller connector catalogue than Mirth's, no clustering yet, and no plugin ecosystem. Filters,
transformations, scripts, contracts, shadow comparisons and mapping tables do not survive a round trip out
to Mirth and back — each is reported when it cannot be converted. Missing capabilities are refused by name
rather than approximated.

</details>

<details>
<summary><b>How do I get help or report a bug?</b></summary>

Open a [GitHub issue](https://github.com/biodream-llc/perfuse/issues). Include the output of
`perfuse version` and, if a message is involved, a de-identified sample. There is also a friction report
built into the interface that records where this installation refused you and how often — that is often the
fastest way to describe a problem.

</details>

---

## Contributing

Pull requests are welcome. Two conventions this repository holds to:

1. **Documentation changes ship in the same commit as the code they describe.** Prose that sits next to the
   wrong file does not reach anybody.
2. **A test that cannot fail is not evidence.** New guards are verified by breaking the thing they guard
   and confirming the test notices.

`make check` runs formatting, vet, the race detector, cross-compilation, type checking and the unit
suites. `make e2e` runs the browser tests. [docs/releasing.md](docs/releasing.md) covers cutting a release
and lists what the current one could not verify.

---

## Licence

[Apache License 2.0](LICENSE). Copyright 2026 BioDream LLC and the Perfuse contributors. See
[NOTICE](NOTICE).

Perfuse is not affiliated with, endorsed by, or derived from Mirth Connect or NextGen Healthcare. HL7 and
FHIR are registered trademarks of Health Level Seven International; DICOM is a registered trademark of
NEMA. Those marks are used only to describe what this software reads and writes.

<div align="center">

**Keywords:** healthcare integration engine · interface engine · Mirth Connect alternative · open source
HL7 engine · HL7 v2 parser · HL7 to FHIR converter · FHIR server · FHIR R4 R4B R5 · DICOM router · X12 837
835 270 271 · C-CDA CDA validator · MLLP · healthcare interoperability · HIE · health data integration ·
open source interface engine · Mirth Connect replacement · Go healthcare

</div>
