# Perfuse

Healthcare integration tooling in Go. A single static binary, no JVM, no installer.

**Status: it runs.** One binary with the web interface inside it. No JVM, no
installer, no separate asset directory.

- **Existing Mirth JavaScript runs unchanged.** Including E4X — `msg['PID']['PID.5']['PID.5.1']`,
  `for each`, XML literals, `channelMap`, `$()`, `DateUtil`, `SerializerFactory`.
  See [Mirth scripts](#mirth-scripts).
- **`perfuse serve`** is the console: a live dashboard, a metrics section, the
  channel builder, a message browser, a FHIR lab, a clinical document lab, users
  and an activity log — with the engine running in the same process, so channels
  can be started, stopped and watched.
- **FHIR is a first-class feature**, not an export format. R4, R4B and R5, an
  HL7 v2 to FHIR mapper, a FHIR REST server, and `fhir` as a channel destination
  type.
- **Clinical documents are read, checked and converted**, including the ones that
  arrive base64-encoded inside an `MDM^T02`. The narrative and the coded entries
  are compared against each other, which nothing else does. See
  [Clinical documents](#clinical-documents).
- **Messages are not lost when a receiver goes down.** A durable queue keeps them
  on disk, in order, and delivers when it comes back. See [The queue](#the-queue).
- **Connectors that match what hospitals actually run:** MLLP, files, HTTP,
  databases, SFTP, JavaScript Reader, Kafka and message brokers, in both
  directions. See [Databases](#databases) and [SFTP](#sftp).
- **Alerts tell you, rather than waiting to be found.** See [Alerts](#alerts).
- **Shadow mode** runs a candidate version of a channel beside the live one on real
  traffic and shows exactly where they differ, without delivering anything. See
  [Shadow mode](#shadow-mode).
- **`perfuse translate`** converts Mirth, Open Integration Engine and BridgeLink
  exports into Perfuse channels, not just a report on them: a channel, a channel
  group, a code template export or a whole server backup. **`perfuse test`** runs tests against a channel. **`perfuse
  generate`** makes synthetic traffic to point one at.
- **`perfuse run`** runs channels headless. **`perfuse check`** validates them.
  **`perfuse explain`** reads Mirth channel exports and says what blocks a
  migration. **`perfuse fhir`** converts, validates and serves from the command
  line. **`perfuse listen` / `send`** are the manual tools.

Underneath: an HL7 v2 parser that indexes rather than decodes, MLLP and HTTP
transport with TLS, a filter expression language, ten declarative transformation
steps, a metrics collector with percentiles and Prometheus exposition, a durable
queue, alerting, a message store with retention, and a field dictionary that lets
the interface say `PID-5.1` is the patient's family name.

## Why this exists now

In March 2025 NextGen Healthcare changed Mirth Connect's license. From version
4.6 the source is closed and a paid license is required. Version 4.5.2 is the
last open-source release.

That leaves a large installed base with three options: pay, stay on a frozen
version, or migrate to one of the community forks. All three start with the same
problem — somebody has to open a few hundred channels written years ago by
someone who left, and work out what they actually do.

That is the problem `perfuse explain` solves — and `perfuse translate`, which converts the
channels rather than only describing them.

Being honest about the forks: [Open Integration Engine](https://www.openintegrationengine.org/)
and BridgeLink are both alive, and OIE is a drop-in continuation of 4.5.2 under the
same licence. If all you want is what you have now, kept working, they are a cheaper
answer than this and you should use one.

Perfuse is for the case where you want something different: channels in files rather
than a database, so they go through code review; a single static binary; a browser
interface instead of a Java desktop client; message tracing that answers "why did this
message do that" in a tab; and feed contracts that tell you when a sending system
changes, rather than finding out weeks later from a receiver that fell over.

The forks cannot follow on any of those without ceasing to be forks — each one means
leaving the JVM, abandoning the storage model, or rewriting the client.

### Proving it before you commit to it

The objection that actually matters is "we cannot be sure it does the same thing to
our traffic". So:

```sh
perfuse translate -o channels/ mirth-export.xml   # bring the channels across
# run both engines on the same messages, with only the incumbent delivering
perfuse compare mirth-out/ perfuse-out/           # find every difference, grouped by cause
```

The comparison groups differences by cause, so three thousand messages differing for one
reason are one finding rather than three thousand lines.

## Install

```sh
git clone https://github.com/biodream-llc/perfuse
cd perfuse
make build          # -> bin/perfuse
```

Requires Go 1.26 or later. No cgo, so `make cross` produces static binaries for
Linux, macOS and Windows on amd64 and arm64. Each one is about 25 MB and has
nothing to install alongside it — no JVM, no database server, no web server, no
runtime of any kind.

Where those 25 MB go, since it is a fair question for a single file:

| | |
| --- | --- |
| ~12 MB | the JavaScript engine, so Mirth's transformer scripts run unmodified |
| ~7 MB | SQLite compiled to pure Go, so there is no database server and no cgo |
| ~3 MB | the SQL Server driver, plus Postgres and MySQL |
| ~1 MB | the web interface, compiled in so it cannot drift from its API |
| the rest | HL7, FHIR, PDF, SOAP, MLLP, SFTP, FTP, S3 and the engine itself |

The first two are the interesting ones. Running Mirth's JavaScript is what makes a
migration possible at all rather than a rewrite, and shipping SQLite inside the
binary is what makes `perfuse init && perfuse serve` work on a machine with nothing
installed. Both are deliberate purchases. For comparison, the JVM that Mirth needs
before you install Mirth is several times this whole file.

To set up a directory and a service definition for your operating system:

```sh
perfuse init                    # layout, an example channel, a systemd unit or launchd plist
perfuse sbom                    # everything linked into this binary, on one screen
```

## Quick start

Two terminals. Receive HL7 over MLLP and acknowledge it:

```sh
perfuse listen -addr :6661 -write-dir ./received
```

Send a message to it:

```sh
perfuse send -addr localhost:6661 -show-ack message.hl7
```

```
  1  ADT^A01 CTRL1             AA       accepted
     MSH|^~\&|PERFUSE|RECVFAC|SENDAPP|SENDFAC|20260818152120||ACK|202608181521201|P|2.5.1 | MSA|AA|CTRL1

1 sent: 1 accepted, 0 rejected, 0 failed
```

Messages written by `-write-dir` are MLLP-framed, so a stored file can be replayed
straight back:

```sh
perfuse send -addr localhost:6661 ./received/20260818-ADT_A01.hl7
```

`send` exits non-zero if anything was rejected or failed, so it works in a script.
It accepts a single message, several concatenated, or an MLLP-framed capture, and
normalises Windows line endings.

**`listen` is unauthenticated and unencrypted.** MLLP has no notion of either.
Restrict access at the network layer, and do not expose it to an untrusted
network. `-write-dir` stores messages that may contain PHI; both facts are logged
as warnings at startup rather than buried here.

## Usage

Every command has `-h`. The full list:

| Command | What it does |
| --- | --- |
| `serve` | run the web interface and API |
| `run` | run channels defined in YAML |
| `check` | validate channel definitions and print what they do |
| `test` | run tests against channel definitions |
| `init` | set up a directory and a service definition |
| `explain` | describe Mirth channels and what blocks a migration |
| `translate` | convert Mirth channels into Perfuse channels |
| `compare` | prove two engines agree over the same traffic |
| `profile` | report what is actually in a feed, and what changed |
| `contract` | say what a feed must look like, then check it |
| `fhir` | convert HL7 v2 to FHIR, validate it, or serve it |
| `generate` | make synthetic HL7 v2 messages for testing |
| `deident` | turn real messages into a corpus that can be shared |
| `listen` / `send` | accept or send HL7 v2 over MLLP |
| `sbom` | list everything linked into this binary |

### explain

```sh
perfuse explain path/to/channel.xml       # one channel
perfuse explain path/to/channels/         # a directory, walked for *.xml
perfuse explain -json channels/           # machine-readable
perfuse explain -strict -quiet channels/  # summary only, exit 1 if anything is blocked
perfuse explain server-backup.xml         # a whole server backup, a channel group or a channel
```

Every export kind Mirth, the Open Integration Engine and BridgeLink write is read: `<channel>`, a `<list>` of
channels, `<channelGroup>`, a code template library export, and `<serverConfiguration>` (Backup Config).

### translate

```sh
perfuse translate -o channels/ server-backup.xml
```

Writes one YAML file per channel, plus `lib/<library>.js` for each code template library. Each channel the library
was enabled for in Mirth gets `scripts.include: [lib/<library>.js]`; drag-and-drop snippets are not written. Channels
keep their group, and a Channel Writer becomes a `channel` destination naming its target when the target is in the
same export. In the browser, **Administer → Migrate** does the same, and `POST /api/mirth/libraries`
(`{"file": "lib/x.js", "source": "..."}`, editor) saves a library beside the channels; only `lib/<name>.js` or `.lua`
is accepted.

Real output, from the synthetic channel in `internal/mirth/testdata`:

```
CHANNEL  ADT Inbound - Synthetic Site
         enabled  ·  Mirth 4.4.0  ·  revision 12

         Receives ADT over MLLP, drops A28, forwards to the registry.

FLOW
  in   TCP Listener (listening on 0.0.0.0:6661)
       HL7V2
       filter     2 rules: drop A28, require MRN
       transform  4 steps: 1 javascript, 1 mapper, 1 unknown, 1 xslt
  out  1  Registry API       HTTP Sender (https://registry.example.invalid/hl7)
          HL7V2 → JSON
  out  2  Archive            File Writer (writing /var/spool/archive/${message.messageId}.hl7)  [disabled]
          HL7V2

STORAGE
  mode DEVELOPMENT  ·  prune metadata 30d, content 14d  ·  attachments stored

FINDINGS  2 blocker(s), 1 warning(s), 4 note(s)

  BLOCKER  source, step 2 (site transform)
           XSLT transformation step
           Perfuse has no XSLT engine. The stylesheet has to be rewritten, or
           XSLT support added.

  BLOCKER  source, step 3 (third party widget)
           Unknown step plugin com.example.someplugin.WidgetStep
           This is a third-party or newer Mirth plugin. Its behaviour is not
           described anywhere in the export, so it has to be reimplemented by
           hand.

  WARNING  channel
           Message storage mode is DEVELOPMENT
           Every message and every intermediate transformation is retained.
           This is the heaviest setting and is not meant for production.
```

`-strict` makes this usable in CI: fail a build if a channel picks up a
dependency that cannot be migrated.

## FHIR

FHIR is not an add-on here. A channel can receive HL7 v2 and post FHIR, the binary
serves a FHIR endpoint, and the interface will show you what a message becomes
before you commit to it.

### Which version

"FHIR" on its own does not identify a wire format, so the version is explicit.

| Release | Status here |
| --- | --- |
| **R5** (5.0.0) | The latest published release, and the default |
| **R4B** (4.3.0) | Maintenance release between R4 and R5 |
| **R4** (4.0.1) | What most production systems and US regulation consume; fully supported |
| R6 | In ballot, deliberately **not** implemented |

R6 is refused with an explanation rather than guessed at. Shipping an
interpretation of an unpublished specification would be worse than not supporting
it.

The resource model is R5-shaped and R4 differences are applied when serialising,
in one place. The alternative — two parallel definitions of `Patient` — drifts
until one is R4 and the other R5 and nobody notices until a hospital rejects a
message. The differences are real: R5 renamed `Encounter.period` to `actualPeriod`
and `hospitalization` to `admission`, changed `class` from a `Coding` to a list of
`CodeableConcept`, and replaced several status codes, so an R5 status in an R4
resource fails a required binding.

```sh
perfuse fhir convert -version R4 -authority "SITEA=http://sitea.example.org/mrn" message.hl7
perfuse fhir validate -strict patient.json
perfuse fhir serve -addr 127.0.0.1:8080 -db ./fhir.db
perfuse fhir versions
```

### How the mapper behaves

One rule underpins it: **a value that cannot be mapped is preserved as text, never
guessed at.** A local code turned into a plausible standard code is worse than an
uncoded value, because the receiver believes it. Every judgement is reported as a
note naming the source field, so the ones that matter get checked instead of
someone reading all the output.

What follows from that:

- **Units** keep their original text and gain a UCUM code only when the mapping is
  certain. Guessing a unit code can turn a normal result into an alarming one.
  Cell counts are mapped explicitly, because laboratories write the same unit six
  different ways.
- **Timestamps are converted, not copied.** HL7 v2 permits a local time with no
  offset and FHIR does not, so when one has to be supplied the conversion says so.
  Copying a v2 timestamp straight through is the most common reason a converted
  resource is rejected. The offset supplied is `-tz` (or `timezone` on a `fhir`
  destination) when set, otherwise the one the sender put on `MSH-7`, since v2
  presumes a bare time is the sender's local time, otherwise UTC.
- **Encounter status comes from the trigger event**, since v2 has no status field.
  An A13 cancels a discharge, so the visit is in progress again — treating it as
  finished would leave a discharged patient still in a bed.
- **Resource ids are deterministic** and bundle entries are conditional upserts, so
  a feed that retries updates rather than accumulating duplicates somebody later
  merges by hand.

Measured against 299 real ADT messages: all 299 converted with zero validation
errors in either R4 or R5, producing 1,257 resources, with 96 distinct patient
identifiers each mapping to exactly one resource id.

That count used Perfuse's own validator. The official HL7 validator, run later on
whole bundles, found that every `fullUrl` was malformed (a resource id, not a
UUID, after `urn:uuid:`), which Perfuse's checker did not look at. On the 70
messages of the public nw-gmsa test set it then passed 43; after the fixes it
passes 64, and the other 6 fail only on codes the senders sent that are not in
the code system. The details are in `docs/verification.md`.

### The FHIR server

A capability statement, read, search, create, update, delete, transaction and
`$validate`. A transaction or batch is `POST` to the base (`/fhir` or `/fhir/`). Every stored resource carries
`meta.versionId` and `meta.lastUpdated`; the sender's `meta.security`, `meta.tag` and `meta.source` are kept. One hundred and twenty-seven resource types with search parameters, covering clinical
(Patient, Encounter, Observation, DiagnosticReport, Condition, Procedure),
medications (Medication, MedicationRequest, MedicationStatement, MedicationDispense,
MedicationAdministration), care coordination (CarePlan, CareTeam, Goal, Task),
financial (Coverage, Claim, ExplanationOfBenefit), and infrastructure (Device,
Provenance, Consent, Composition). Resources are stored as JSON with an extracted
search index, which is how real FHIR servers work — searching by scanning JSON stops
working at the first thousand patients.

Decisions worth knowing:

- **An unsupported search parameter is refused, not ignored.** A server that
  ignores one returns the wrong resources and the client cannot tell, which in
  clinical data means acting on somebody else's results.
- **The capability statement states what is missing** — no history, no chained
  search, no `_include`. One that overstates the server is worse than none, because
  a client trusts it. A test asserts the advertised parameter list matches the
  implemented one.
- **A deleted resource reads as 410, not 404**, because a client resending it needs
  the difference.
- **Search follows FHIR's AND and OR.** Commas OR (`status=final,amended`) and a repeated
  parameter ANDs (`date=ge2020&date=le2021`). `POST [type]/_search` takes a form body.
  The US Core 6.1 server search parameters, `_revinclude=Provenance:target` and
  `_include=MedicationRequest:medication` are supported. The Inferno US Core 6.1.0 FHIR
  API tests pass except where the published examples fail current terminology.
- **Dates search as ranges.** `2026` is the whole year and a Period runs from its start to its end, open at a missing end, so
  `Encounter?date=ge2026-08-01` finds a stay that began in July. Offsets are honoured, and `eq ge le gt lt sa eb ap ne` follow
  R4's definitions. An unreadable date is a 400 naming the parameter.
- **Escapes in search values are honoured.** `identifier=MR1\|936` is one value with a pipe in it, not a system and a code, and
  `\,` is a comma rather than an OR.
- **Contained ids must be unique.** Two contained resources with one id are refused with a 400, since `#id` names no type.
- **The capability statement is public**: clients read it before they hold a token, and it
  lists US Core's `supportedProfile`s. Everything else needs a token.
- **Validation on write is on by default.** A store that accepts anything is
  convenient until somebody queries it and finds half the data unusable, by which
  point it is thousands of records old.

### As a channel destination

```yaml
destinations:
  - name: fhir-store
    type: fhir
    fhir:
      url: https://fhir.internal/fhir
      version: R4
      identifier_systems:
        SITEA: http://sitea.example.org/mrn
```

It posts a transaction bundle, so a patient and their encounter land together. It
refuses to post a bundle that fails validation, because a transport error would
hide the real fixable problem. Authentication failures are not retried — burning
the retry budget only delays the alert that would help. Redirects are refused
outright, since following one would repost patient data somewhere the
configuration did not name.

Configuration validation refuses a `fhir.url` that uses plain HTTP to a remote
host, and warns when no identifier system is configured, which is the single most
common cause of unmatchable patients downstream.

### Public health case reporting (eCR)

An `ecr` block makes a `fhir` destination report to public health. Each message is
converted and checked for reportable-condition trigger codes in its diagnoses (`DG1`),
results (`OBX` code and coded value) and orders. A message with one is sent as an
HL7 eCR 2.1.2 eICR, wrapped in an eCR message, to the URL's `$process-message`. A
message with none is not sent, and that is not a failure.

```yaml
destinations:
  - name: public-health
    type: fhir
    fhir:
      url: https://ecr.agency.example/fhir
      identifier_systems:
        SPRINGFIELD: http://springfield-general.example/mrn
      ecr:
        rctc: /etc/perfuse/rctc.json      # the RCTC, as a FHIR ValueSet or the eRSD Bundle
        source: https://fhir.springfield-general.example/fhir   # where the Reportability Response goes
        facility: {name: Springfield General Hospital, npi: "1234567893", phone: +1-217-555-0100,
                   line: 100 Main St, city: Springfield, state: IL, postal_code: "62701"}
```

The trigger codes should be the RCTC (Reportable Conditions Trigger Codes), which is
published through the eRSD and needs a UMLS licence, so it cannot ship with Perfuse.
Without `rctc`, a short built-in sample is used. It is for testing, and every report
built from it says so.

The facility is configured because eCR requires its phone and address, and a v2
message carries neither. What the message lacks is not invented:

- a missing race or ethnicity is sent as the text "Unknown";
- a missing language is sent as BCP 47 `und`;
- with no visit number, the encounter is identified by its id in the report;
- with no `PID-30`, the patient is reported as living.

Each of these is logged as a note. Practitioners are named through
PractitionerRoles, as eCR requires. An NPI in `XCN.9` (by OID or as `NPI`) gets the
`us-npi` system.

#### The Reportability Response

The agency answers an eICR with a Reportability Response (RR): whether the eICR was
processed and, per condition, whether it is reportable, to which agency, and how soon.
It comes back as an eCR message to `source`'s `$process-message`.

`serve -ecr-responses` takes them there. Each RR is stored, exactly as received, as a
DocumentReference (LOINC 88085-6 Reportability response) whose attachment is the RR
document, at an id derived from the identifier of the eICR it answers. That address is
logged when the report is sent (`case report sent ... eicr=urn:uuid:... response_at=
.../DocumentReference/<id>`), so a report and its response are linked by the eICR
identifier alone. Receipt is logged with the processing status and each condition's
determination; the description says the same in one line.

`serve -ecr-agency agency.yaml` plays the agency, to test the exchange without one. It
answers each eICR posted to `$process-message` with an eCR 2.1.2 RR, keeps the eICR it
received, and with `reply: true` also posts the RR to the eICR's source endpoint:

```yaml
name: Illinois Department of Public Health
phone: +1-217-555-0199
line: 535 W Jefferson St
city: Springfield
state: IL
postalCode: "62761"
endpoint: https://ph.test.example/fhir   # named as the RR's source
rctc: rctc.json                          # optional; the built-in sample otherwise
reply: true
reply_bearer_token: ${HOSPITAL_TOKEN}    # read from the environment
```

Its decisions are a stand-in's, not a jurisdiction's: every condition its trigger codes
find is reportable to it, within 24 hours for one the built-in list marks immediate and
72 hours otherwise. A real agency decides with the RCKMS rules. Both flags need a
writable FHIR endpoint; a hospital refuses case reports and the agency refuses RRs, with
422. `source` may be plain `http` only to this machine.

`perfuse fhir eicr [-facility f.json] [-rctc rctc.json] [-destination URL -source URL] <messages>`
builds the same reports from files. The FHIR lab's **Public health case report** panel
builds one from a pasted message.

`perfuse elr -config elr.yaml [-rctc rctc.json] [-out dir] <messages>` turns lab results (ORU^R01) into HL7 2.5.1 ELR
messages (ELR Release 1, `PHLabReport-NoAck`), one per message with a reportable result, keeping only the orders that carry one.
The config names the sending and receiving application and facility (by OID, or CLIA), and the ordering facility, performing lab
and order-number assigning authorities used when the lab's message leaves them out. See `examples/elr/elr.yaml`. What it changes:
the source's separators become `^~\&`; ORC is built from OBR when missing; SPM from OBR-15, OBR-7 and OBR-14 when missing; CE
results become CWE; race and ethnicity coded CDCREC are recoded to HL7 tables 0005 and 0189, keeping the sender's coding as the
alternate; an NPI named only `NPI` gets its OID and identifier type. What the lab did not send (a specimen type, the time the
specimen was received) stays empty and is printed as a note.

A destination's `elr` block does the same on the way out of a channel: `elr: {config: elr.yaml, rctc: rctc.json}` (paths relative
to the channel file; `rctc` optional). Each ORU^R01 is delivered as its ELR message; one with nothing reportable is not sent and
counts as delivered. HL7 v2 channels only, on destinations that deliver the message itself (mllp, http, tcp, file, sftp, ftp, s3,
azure_blob, soap, broker, kafka, sqs, sns, amqp, channel). The config is read and checked when the channel loads.

## CMS-0057 for payers

The four APIs the CMS Interoperability and Prior Authorization rule requires of payers from 1 January 2027, and the data behind them. The full account is the [CMS-0057 chapter of the manual](https://perfuse.health/manual/#s16-cms-0057-for-payers); the commands and flags:

| | |
|---|---|
| `perfuse cms0057 carinbb -system <uri> [-network innetwork] <837> <835>` | Paid claims to CARIN Blue Button 2.2.0 transactions, for the Patient Access API |
| `perfuse cms0057 priorauth <pas-response.json> [pas-claim.json]` | A PAS decision to a PDex 2.2.0 prior authorisation ExplanationOfBenefit |
| `perfuse cms0057 metrics -year <year> [-services <csv>] <decisions.csv>` | The yearly public prior authorisation metrics page, in CMS's template layout (`-format html`, `csv` or `json`) |
| `serve -fhir-payer-apis` | `POST /fhir/Patient/$member-match` (HRex), and with `-fhir-bulk-export`, `Group/{id}/$davinci-data-export` and `Group/{id}/$export` |
| `serve -fhir-member-match-without-consent` | Lets `$member-match` answer without an active Consent; for testing only |
| `serve -crd-rules <file>` | Da Vinci CRD 2.2.1 over CDS Hooks at `/cds-services` (order-sign, order-select, order-dispatch, appointment-book). See `examples/crd/rules.yaml` |
| CRD rules: `members: fhir` | Resolve the coverage against this server's FHIR store: `no-member-found`, `coverage-not-found`, `no-active-coverage`, and `satisfied` with the number of an approved ClaimResponse |
| CRD rule fields | `billing_codes`, `details` (crd-coverage-detail codes), `contact`, `expiry_days`, `depends_on`, `doc_purpose` (not `withpa`) |
| `serve -cds-clients <file>` | EHRs trusted to call it with signed JWTs: `clients: [{issuer, jwks_url}]`. A Perfuse API token is also accepted |
| `POST /fhir/Questionnaire/$questionnaire-package` | Da Vinci DTR 2.2.0: the questionnaires named, named on the order's coverage-information, or asked for by CRD's coverage assertion id (`context`), each as a package bundle with a QuestionnaireResponse and the Libraries it needs |
| `POST /fhir/Questionnaire/$log-questionnaire-errors` | DTR 2.2.0: problems a DTR app met with a questionnaire, written to the server log |
| `POST /fhir/Questionnaire/$next-question` | DTR 2.2.0 adaptive questionnaires: a stored Questionnaire with SDC's `questionnaireAdaptive` extension is packaged as an empty shell, and each call appends the next top-level item whose `enableWhen` the answers satisfy, or marks the response `completed`. A required question already asked must be answered first |
| `serve -ecr-responses` | Accept eCR Reportability Responses at `POST /fhir/$process-message`; each is stored as `DocumentReference/<id>` derived from the eICR it answers (see eCR above) |
| `serve -ecr-agency <file>` | Play a public health agency for testing: answer eICRs at `$process-message` with a Reportability Response, and with `reply: true` post it to the eICR's source |
| `serve -pas` | Da Vinci PAS 2.2.1 on the FHIR endpoint (needs `-fhir`, writable): `POST /fhir/Claim/$submit`, `$inquire` (a read) and `$decide` (a reviewer's decision on a pended request). Decisions come from `-crd-rules`; with none, every service is pended |
| PAS rule fields | `pa_decision` (approve, deny, pend), `pa_alternative` (a coding approved instead), `pa_attachments` and `pa_attachment_modifiers` (LOINC), `details` `allowed-quantity`, and `questionnaire`, asked for when pended |
| `$decide` parameters | `claimResponse` (id), `decision` (approve, deny, modify), `item` (sequences, repeatable), `reason`, `reviewer` (NPI); with modify, `quantity` (valueDecimal, the units certified) and/or `alternative` (valueCoding, the service approved instead). 409 when nothing is pended |
| `serve -pas-278-url` | Forward each PAS request to the payer's utilization management system as one X12 278 request (005010X217, each item a patient event, POSTed as `application/edi-x12`) and answer with its 278 response: the UM system's action codes and authorization numbers. Unreachable or not a 278 response: every item is pended. Also `-pas-278-receiver` (required), `-pas-278-sender`, `-pas-278-payer-id`, `-pas-278-token-env`, `-pas-278-production` |
| `POST /fhir/$submit-attachment` (also `Claim/$submit-attachment`) | Da Vinci CDex 2.1 `$submit-attachment` for the documents a pended PAS request asks for: `TrackingId` is the CommunicationRequest's identifier (the attachment control number) or the Task's, `MemberId` must be the request's member, `AttachTo` preauthorization; each `Attachment` `Content` (DocumentReference or QuestionnaireResponse) is kept with the request for its reviewer. Nothing is decided by a document arriving; 422 for an unknown TrackingId or another member's |
| `GET /api/pas/cases[?all=1]`, `GET /api/pas/cases/<id>`, `POST /api/pas/cases/<id>/decide` | The reviewer queue behind Exchange → CMS-0057 → Reviewer queue: pended requests soonest due (72 hours expedited, 7 days standard), one read whole with its documents (audited), and a decision (`decision`, `items`, `reason`, `reviewerNpi`, `quantity`, `alternative`; editor role, audited) |
| `POST /fhir/Claim/$decide-278` | A UM system's later 278 response (X12 body): each event is matched by its trace number to the request item it was sent for and decided as `$decide` would, so the PAS subscription delivers it. Answers a Parameters with each event's result: decided, pending, already-decided or unknown |
| PAS subscription topic | `http://hl7.org/fhir/us/davinci-pas/SubscriptionTopic/PASSubscriptionTopic` with `-pas -fhir-subscriptions`; filter `org-identifier`; the notification is the PAS Response Bundle |
| Reading a PAS answer | `GET /fhir/Bundle/<id>` (the latest PAS Response Bundle, the focus an id-only notification names), `GET /fhir/Bundle/<id>/_history/<n>` (as that notification sent it), `GET /fhir/ClaimResponse/<id>`. A patient-launch token is told they do not exist |
| SMART scopes | v1 (`.read`, `.write`) and v2 (`.cruds`) forms. Granular v2 scopes (`patient/Observation.rs?category=laboratory`) limit searches, reads and `_include` results to what that query finds; several are either-or, and an unfiltered scope for the type lifts them. A read checks token parameters only, so a filter on a date or reference refuses the read. Included resources are held to the same scopes and launch context as reads. A patient-launch token also reads its own Coverage (by `beneficiary`), and by id (never by search) the Organization, Practitioner, PractitionerRole, Location, HealthcareService, Endpoint and Medication its scopes cover; `fhirUser` reads the signed-in user's own resource. Checked with Inferno's US Core 7 granular-scope groups |
| `serve -smart-clients <file>` | Run Perfuse's own SMART authorization server at `/auth` for the apps in this YAML file (see `examples/smart/clients.yaml`), and accept its tokens on the FHIR endpoint; cannot be combined with `-smart-issuer`. It serves SMART Backend Services: `POST /auth/token` with `client_credentials` and a signed JWT assertion (RS384, ES384, RS256 or ES256; `sub` and `iss` the client id, `aud` the token URL, at most five minutes, each `jti` once), granting the requested system scopes the client is registered for, for five minutes. `GET /auth/jwks` is its key set. Discovery advertises only what is offered |
| `serve -smart-users <file>` | With `-smart-clients`, the people who may sign in at `/auth/authorize` to authorize apps (see `examples/smart/users.yaml`): `username`, `password_hash` (from `perfuse smart hash`), `fhir_user` (`Patient/…`, `Practitioner/…`, `PractitionerRole/…`, `RelatedPerson/…`). Enables the authorization code flow: PKCE S256 required, `aud` must be this FHIR base, redirect URIs exactly as registered; sign-in (five failures end the attempt), a patient picker for clinicians when the app asks for `launch/patient`, and a consent page where scopes can be unticked. A Patient user only ever gets their own record; `patient/` scopes with no patient chosen are dropped. Codes last a minute and work once. Access tokens last an hour and carry `patient` and `fhirUser`; `openid` adds an RS256 ID token (`/auth/.well-known/openid-configuration`); `offline_access` (90 days) or `online_access` (12 hours) adds a refresh token, which can narrow scopes and never widen them. Codes, refresh tokens, launch ids, used client assertion ids and revocations are kept in the console database (`smart_grants`, keyed by a SHA-256 of each, so a copy of the database holds nothing that can be presented), so a restart signs nobody out and forgets no revocation |
| `serve -smart-oidc <file>` | With `-smart-users`, a "Sign in with …" button on the SMART sign-in page for the organisation's OpenID Connect provider (YAML: `issuer`, `client_id`, `client_secret_file` or `client_secret_env`, `label`, `scopes`; see `examples/smart/oidc.yaml`). Register `<issuer>/oidc/callback` at the provider. PKCE and a nonce on every attempt; the ID token's `sub` must be some user's `oidc_subject` in the users file, which still says which FHIR user they are. An unlinked account is refused, never created |
| Users file `oidc_subject` | Links a person to their provider account. A person needs a `password_hash`, an `oidc_subject` or both; one subject cannot be two people |
| `GET /api/smart`, `PUT`/`DELETE /api/smart/clients/<id>`, `PUT`/`DELETE /api/smart/users/<username>` | Users → SMART apps: the registered apps and the people who sign in, edited in the console (admin; a platform account when multi-tenant). Saves are checked as at start-up and written to the `-smart-clients` and `-smart-users` files (comments are not kept), and take effect at once. A confidential-symmetric app saved without a secret is given one, returned once; passwords are set, never read back. Audited as `smart.client.save`, `smart.client.delete`, `smart.user.save`, `smart.user.delete` |
| `/auth/launch` | EHR launch, standing in for the EHR: a user signs in, chooses an app registered with a `launch_url` and a patient (a member gets their own), optionally an encounter, and the app opens at its launch URL with `iss` and a launch id that works once within five minutes. The token from that authorization carries the patient and encounter |
| `/auth/introspect`, `/auth/revoke` | Token introspection (RFC 7662) for authenticated confidential and backend clients, or a caller presenting an active access token from this server as `Authorization: Bearer`: `active`, scope, client, patient, encounter, `fhirUser`. Revocation (RFC 7009) by the client the token was issued to: a refresh token stops working, and an access token is refused by the FHIR endpoint until it would have expired. Both advertised in discovery |
| `perfuse smart hash < secret` | Prints the hash for a users file `password_hash` or a clients file `secret_hash`, reading the secret from standard input |
| CORS | The FHIR endpoint and `/auth/token`, `/jwks`, `/introspect`, `/revoke` and OpenID discovery answer `Access-Control-Allow-Origin: *` and preflights, so SMART apps in a browser can use them; none reads cookies. The sign-in pages are not cross-origin |
| `serve -smart-key <file>` | The authorization server's RSA signing key, PEM, created (2048-bit, mode 0600) when missing; refused if other users can read it. Default `smart-signing.key` |
| SMART client kinds | `backend` (needs `jwks` or an https `jwks_uri`), `public`, `confidential-symmetric` (`secret_hash`), `confidential-asymmetric`; each with `scopes` (exact, or ending in `*` for a prefix) and, except backend, `redirect_uris` |
| `serve -smart-issuer <url>`, `-smart-audience <url>` | Accept SMART access tokens from this authorization server, issued for this audience (default: the FHIR base, from `-public-url` when given) |
| `serve -smart-jwks <url>` | Where to fetch the issuer's signing keys when its own address does not resolve from the server (split DNS, a private address) |
| `serve -smart-authorize`, `-smart-token`, `-smart-jwks-uri` | The endpoints `/.well-known/smart-configuration` advertises. `sso-openid-connect` is claimed only with `-smart-jwks-uri` |
| `serve -smart-backend-services` | Advertise SMART Backend Services (`client_credentials`, `private_key_jwt`, RS384/ES384), for DTR, PAS and bulk clients. A read scope may call the operations that change nothing: `$questionnaire-package`, `$next-question`, `$log-questionnaire-errors`, `$expand`, `$validate-code`, `$translate` |
| `PUT /fhir/ValueSet/{id}`, `GET /fhir/ValueSet?url=` | Stored value sets, such as a payer's DTR answer lists; they travel in the questionnaire package. A url under `urn:perfuse:codeset:` is refused, since those are the mapping tables' views |
| `GET /fhir/ValueSet/$expand?url=`, `$validate-code` | For a stored value set: listed concepts, included value sets and enumerated excludes, with `filter` and `count`. A rule over a whole code system (a filter, or a system with no concepts) is refused with 422 rather than expanded partly |
| `POST /api/crd/ask` | From the console: an order sent to this server's CRD rules or to a payer's CDS service URL |
| `serve -dashboards <file>` | Which team dashboard each directory group (`groups:`) and role (`roles:`) opens on |
| `GET /api/dashboards`, `PUT /api/dashboards/view`, `GET /api/dashboards/{id}/grafana` | The team dashboards, a person's saved view, and a dashboard as Grafana JSON |
| `GET /api/dashboards/figures?tiles=<id>,…` | The figures behind the department tiles, computed on the server as rows of a label and a value: `pas-timeliness`, `pas-decisions`, `dicom-modality`, `dicom-routing`, `dicom-queries`, `reports-undelivered`, `lab-delivery-time`, `lab-critical`, `x12-999`, `x12-277ca`, `x12-835-match`, `x12-rejection-reasons`, `privacy-patient-access`, `token-use`. Aggregates only; a figure that cannot be computed says why in `unavailable` |
| Metrics `perfuse_dicom_query_polls_total`, `perfuse_dicom_query_failures_total` | C-FIND queries each `dicom_query` channel ran, and those that failed |
| Audit `message.read` | Opening a stored message in the console or by `GET /api/messages/{id}` |
| `serve -vpn <file>` | Partner VPN tunnels for the Connections dashboard (see `examples/vpn.yaml`): each `aws` (Site-to-Site VPN by EC2 `DescribeVpnConnections`, SigV4 with `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`), `azure` (a VPN Gateway connection from Azure Resource Manager with an Entra ID app's client credentials, `client_secret_env`) or `strongswan` (`swanctl --list-sas --ike <connection>` on this host, run without a shell), plus `ours` and `theirs` settings: peer address, networks, IKE versions, phase 1 and 2 encryption, integrity, DH groups and lifetimes. Read at most once a minute; nothing is changed and no pre-shared key is read or shown |
| `GET /api/vpn`, `GET /api/vpn/<name>/sheet` | Every tunnel's state (up, partial when a redundant tunnel or the child SA is down, down, unknown with the reason) and where the two sides' settings cannot agree, compared across vendors' spellings (AES256 and aes-256, SHA2-256 and sha256, 14 and MODP_2048); and a partner's connection sheet as Markdown, to send them. A platform account when multi-tenant |
| `GET /api/connections` | Every networked destination checked by DNS, TCP, TLS and its last delivery, with the reason in words; nothing is sent |
| `POST /api/dtr/package` | From the console: the DTR questionnaire package for an order CRD answered, from this server or a payer's FHIR base URL |
| `perfuse token create -label <name> -fhir-groups <id>[,<id>]` | A provider's Provider Access token: on the FHIR endpoint it can read those Groups, export them and fetch the export, and nothing else. Also under Users → Machine credentials, or `fhirGroups` on `POST /api/tokens` |

Provider Access and Payer-to-Payer exports leave out cost-sharing and provider remittances, Payer-to-Payer leaves out denied prior authorisations, and members with an active Provider Access opt-out are left out of a provider export. Drugs are out of scope throughout.

## Clinical documents

A C-CDA is the densest clinical payload in an HL7 feed, and it almost never
arrives on its own. It arrives base64-encoded in `OBX-5` of an `MDM^T02`, with the
metadata in `TXA`. Perfuse owns both ends of that.

```yaml
destinations:
  - name: documents
    type: cda
    cda:
      dir: ./documents          # or url: https://fhir.internal/fhir
      write: both               # fhir | document | both
      version: R5
      identifier_systems:
        2.16.840.1.113883.19.5.99999.2: http://stjoes.example.org/mrn
```

The Documents tab in the console does the same thing interactively: paste a
document or the message carrying one, and see it read.

### The agreement check

Every section of a C-CDA carries its content **twice** — a narrative a clinician
reads, and coded entries a receiving system imports. They are supposed to say the
same thing.

Nothing verifies that they do. Not the schema, not the Schematron, not the
certification tests, which check that both are *present*. So a penicillin allergy
that appears in the text and not in the codes never reaches the importing
system's allergy list, and nobody finds out until somebody is prescribed
penicillin.

Perfuse compares them and reports six kinds of disagreement: entries with no
narrative, narrative with no entries, a term in one and not the other in either
direction, a negation mismatch, and an entry nothing in the narrative references.
The console shows both readings side by side under the headings *what a clinician
reads* and *what a system imports*.

The check is deliberately conservative and its stemming is deliberately crude,
because a false match means a real disagreement goes unreported, and a check that
cries wolf gets switched off. A test asserts it is nearly silent on a consistent
document.

### Reading

Sections and document types are named in English rather than by template OID:
`2.16.840.1.113883.10.20.22.2.6.1` reads as *Allergies*. An OID that is not
recognised says so, rather than rendering as nothing — a viewer showing a blank
leaves the reader unable to tell missing from unfamiliar.

Extraction tolerates what senders actually do: base64 declared as ASCII still
decodes, with the disagreement recorded, because losing clinical data over a
metadata mistake is the wrong trade. HL7 hex escapes such as `\X0D\` are resolved
by scanning rather than string replacement, since consecutive sequences share no
delimiter. A PDF attachment is reported as an attachment we do not handle, not as
an error. A `TXA-13` parent document is recognised as a replacement and that
survives into FHIR, because a receiver holding both versions with nothing to say
which is current is worse than holding neither.

The patient is read from `recordTarget` only. Searching the whole document for a
`patientRole` finds the one in a family history section and files the record
against a relative.

### Converting

Patient, AllergyIntolerance, Condition, MedicationStatement, Observation,
Procedure and a DocumentReference holding the original bytes, as a conditional-
upsert transaction bundle so re-sending a document updates rather than
duplicates. The original bytes matter: they are the legal record a hospital has
to retain.

**A negated entry is never emitted positively.** FHIR cannot express "not
allergic to penicillin" on an AllergyIntolerance, so the conversion refuses with
an explanation rather than emitting the resource that says the opposite.

## Feed contracts

The expensive problem in an interface is not building it. It is the day the sending
system changes: a vendor upgrade drops a field, a new code appears, a segment starts
repeating. None of those is an error — the messages are still valid HL7 and every
engine in the world accepts them — so nothing notices until a receiver falls over
weeks later and the investigation starts from the wrong end.

A contract says what a feed must look like, and Perfuse checks it continuously.

### Finding out what you actually receive

Start from the traffic rather than from the specification, which is usually years old
and omits the three Z-segments the site added in 2019:

```sh
perfuse profile messages/
```

That reports which fields are always populated, which never are, the real value sets
of coded fields, repetition counts, and which segments are not in the standard. Values
are never reported except for fields whose values form a small enough set to be a code
set — a profile of real traffic must not become a way to read patient data.

To find out what changed since last month:

```sh
perfuse profile -save last-month.json messages/
perfuse profile -against last-month.json today/     # only what changed
perfuse profile -against last-month.json -strict today/   # exit 1 on any change
```

### Turning that into an expectation

```sh
perfuse contract promote -o adt.contract.yaml messages/
```

**Read it and delete most of it.** The generated file is a starting point, and every
line records whether it was measured or decided so you can prune it sensibly. An
expectation nobody has pruned is one nobody has read, and a contract that fires on
things nobody cares about gets switched off.

Then point the channel at it:

```yaml
name: adt-in
contract:
  file: adt.contract.yaml
  check_every: 15m
  over: 500          # profile the last 500 messages
```

The engine re-profiles recent traffic on that schedule and raises an alert when the
feed stops matching. There is a default alert rule for it, so attaching a contract is
enough — a check that runs, finds a problem and tells nobody would be worse than none.

### Why expectations are rates

An expectation is a proportion, not a per-message rule:

```yaml
expectations:
  - path: PID-3
    rule: populated
    min_rate: 0.99
    why: the medical record number; everything downstream keys on it
```

Real feeds contain a proportion of genuinely odd messages — a vendor test message, a
patient with no recorded sex, a manual entry. Alerting on each one trains people to
ignore the alerts, at which point the feature is worse than nothing because it has
consumed the attention it needed. One odd message in ten thousand never fires. Five
percent of messages losing their medical record number does.

**A contract describes what arrives, not what leaves.** Perfuse stores messages as
they arrived, which is deliberate — the stored message is evidence of what a sender
sent — so a contract sees the incoming feed rather than the output of your
transformations. An expectation about a mapped value will fail confusingly, because
the mapped codes never appear in the traffic being profiled. Put that in a channel
test instead, which runs the real transformations.

Two more consequences worth knowing:

- Too few messages reports as **not judged** rather than as passing. "We have no
  evidence" must not read as "all well", which is the failure mode of every monitoring
  system that reports green when its input has stopped.
- When a contract fires, **nothing is failing**. The messages parsed, the deliveries
  succeeded, the error rate is zero. Something at the sending end is different. The
  alert says so, because otherwise you go looking at the engine and find it healthy.

### Shared mapping tables

The other half of the same problem: a site's sex codes and patient classes get
translated identically in every channel that touches them, and a table inside a
channel is a table only that channel can use.

```yaml
# codes.codeset.yaml
tables:
  - name: sex-to-lab
    describes: the hospital's sex codes into the numeric codes the lab expects
    decided_by: Dave, during the 2019 migration
    decided_on: 2019-04-11
    source: the lab's interface specification, version 3
    entries:
      - from: M
        to: "1"
      - from: U
        to: "9"
        why: the lab has no 'unknown', and 9 is its 'not stated'
```

```yaml
# in the channel
tables:
  - codes.codeset.yaml
transformations:
  - map:
      path: PID-8
      use: sex-to-lab
```

The provenance is the point. Most of what makes an interface hard to maintain is that
nobody knows *why* a mapping is the way it is, and the person who knew has left. A
mapping with a reason can be argued with; one without becomes something nobody dares
change and nobody dares delete.

## Metrics

Series, not counters. "What changed twenty minutes ago" cannot be answered by a
current total.

```
GET /api/metrics?window=1h     # what the console draws
GET /metrics                    # Prometheus exposition
```

The Metrics tab is laid out around the questions somebody arrives with, in the
order they arrive: is anything wrong now, is throughput normal, is anything slow,
and what is the process doing. A grid of every available metric would be more
complete and much less useful, so the full list is collapsed at the bottom.

**Latency is a histogram, never a mean.** Ninety-five fast messages and five slow
ones give a healthy p50 and an alarming p99, and only the second number tells you
a destination is in trouble. The console draws p50, p95 and p99 as bands rather
than three lines, because the question is how wide the spread is and that is
easier to see as an area than as lines to be mentally subtracted.

Failures get their own chart rather than being stacked under successes, where a
handful among thousands is invisible. The destination table reports **average
attempts**, because a destination that only succeeds on the fourth try looks
perfectly healthy in a success count.

Two things constrain what is collected. **Label cardinality is capped**, and the
cap reports itself — unbounded labels are the standard way monitoring takes down
the thing it monitors. And **every label comes from configuration, never from
message content**, which is what makes leaving `/metrics` outside the session
defensible: a scraper cannot hold a cookie, and what it can read is counts and
timings labelled with channel and destination names somebody wrote in a YAML
file.

## Running a channel

```yaml
name: adt-inbound

source:
  type: mllp
  listen: ":6661"
  ack:
    when: on_delivery      # an AA means every destination has the message
    application: PERFUSE

filter: MSH-9.1 == "ADT" and MSH-9.2 != "A28"

destinations:
  - name: registry
    type: mllp
    address: registry.internal:6661
    timeout: 10s
    retry:
      attempts: 5
      backoff: 1s
      max_backoff: 1m

  - name: archive
    type: file
    dir: ./archive
    filter: MSH-9.2 in ["A01", "A03", "A04", "A08"]
```

```sh
perfuse check examples/adt-inbound.yaml   # validate and describe
perfuse run   examples/adt-inbound.yaml   # or a directory of them
```

`check` prints what the channel will actually do, including the message paths it
reads:

```
adt-inbound              enabled   :6661
  filter        MSH-9.1 == "ADT" and MSH-9.2 != "A28"
  → registry     mllp   registry.internal:6661 (timeout 10s, 5 attempt(s))
  → archive      file   ./archive (timeout 30s, 5 attempt(s))
      filter    MSH-9.2 in ["A01", "A03", "A04", "A08"]
  reads         MSH-9.1 MSH-9.2
  acknowledges  on_delivery
```

Validation is strict and happens at load. Filters are compiled, addresses are
parsed, and an unknown field is an error rather than something silently ignored —
a misspelled key that gets shrugged off produces a channel that looks configured
and is not, which for a filter or a retry policy is the worst possible outcome.
Every problem in a file is reported at once.

On shutdown, `run` reports what happened:

```
demo
  received 3  delivered 2  filtered 1  partial 0  failed 0  unparseable 0
    admits           delivered 1  failed 0  filtered 1
    archive          delivered 2  failed 0  filtered 0
```

### What an acknowledgement means

This is the setting worth understanding, because it decides what the sender is
being promised.

`when: on_delivery` (the default) sends an AA only after every destination that
wanted the message has accepted it. Slower, and an AA means the data actually
arrived somewhere.

`when: on_receipt` sends an AA as soon as the message is accepted, then delivers
in the background. Faster, and an acknowledged message can still be lost if the
process dies with work queued.

Outcomes are kept distinct rather than collapsed into "worked" and "did not":

| Outcome | Reply | Meaning |
| --- | --- | --- |
| delivered | AA | Every destination that wanted it accepted it |
| filtered | AA | The filter rejected it. The sender did nothing wrong |
| partial | AE | Some destinations have it and some do not |
| failed | AE | Nothing got it, and the reason names the destination |
| unparseable | AR | Not an HL7 message. Still answered, or the sender retries for ever |

A partial delivery is deliberately not an AA. Telling a sender everything is fine
when half its destinations are missing the message loses exactly the information
someone needs at 2am.

An MLLP destination that returns AE or AR counts as a delivery failure and is
retried. Forwarding a message, being told it was rejected, and then reporting
success upstream would lose the data with every party believing someone else
has it.

## The queue

Without one, a destination that is down for two minutes loses everything sent
during those two minutes: retries happen in memory, and when they run out the
message is recorded as failed and gone.

```yaml
destinations:
  - name: registry
    type: mllp
    address: registry.internal:6661
    queue:
      enabled: true
      backoff: 5s
      max_backoff: 1m
      max_depth: 10000
```

Two things about it are worth understanding before turning it on.

**Order is preserved per destination, which means a queued destination stops
taking the fast path.** HL7 is a stream of events about the same patients: an A01
admits, an A03 discharges. If a failed A01 sits in the queue while the next A03 is
delivered directly, the receiving system is told about a discharge for a patient it
never admitted. So once anything is waiting for a destination, everything for that
destination waits behind it — even after the receiver recovers and the direct path
would work.

That costs throughput while a backlog drains, and it is not configurable. The
faster alternative is silently wrong, and the person who would switch it on to
clear a backlog is exactly the person who cannot afford the consequence. It is also
the specific thing that goes wrong in other engines when somebody enables
concurrent queue threads.

**A queued message is an accepted message.** The sender is told AA, because the
bytes are committed and fsynced before the acknowledgement is written. Reporting an
error would make a working store-and-forward queue look like a fault and invite the
sender to resend what we already hold.

### Getting a queue moving again

The alternative other engines force is stopping and restarting the channel to shift
one stuck message, which interrupts everything that was working. Every operation
here is scoped to the message or destination with the problem: **retry now**, **skip**,
**drain** and **remove**.

Retry resets the attempt counter, because somebody retrying by hand has usually just
fixed something and counting the old failures would abandon the message
immediately. A pending message cannot be deleted — it has to be skipped first, so
there is a record that it was abandoned deliberately — and *skipped* is a separate
state from *failed*, because one is the system giving up and the other is a person
deciding.

Retrying needs editor rights; skip, drain and remove need admin and are audited by
name. Needing to find an administrator at three in the morning to press retry would
be its own outage, but abandoning messages a sender was told we had accepted should
have a name against it.

### What to watch

Three gauges rather than one, because depth alone cannot distinguish a queue of
four hundred that is draining from a queue of two that has not moved since Tuesday:

```
perfuse_queue_depth{channel,destination}
perfuse_queue_oldest_seconds{channel,destination}   # the one to alert on
perfuse_queue_failed{channel,destination}
```

## Alerts

Nobody should have to be looking at the dashboard. "Failed transactions remain
queued without timely investigation" is a documented operational failure in this
class of software, and the cause is always the same: the information was on a
screen nobody was in front of at four in the morning.

```sh
perfuse serve -alert-webhook https://chat.internal/hooks/interfaces
perfuse serve -alerts ./alerts.yaml -alert-severity critical
```

```yaml
rules:
  - kind: queue-age
    threshold: 900        # seconds
    for: 1m
    severity: critical
  - kind: error-rate
    threshold: 0.05
    for: 2m
  - kind: queue-stuck
    threshold: 10         # attempts on the same message
    for: 2m
    severity: critical
```

Kinds: `error-rate`, `queue-depth`, `queue-age`, `queue-stuck`, `no-traffic`,
`channel-down`, `script-errors`, `slow-delivery`.

Three decisions shape it:

- **Every alert clears itself**, and the resolution is sent too. A notification that
  fires and never resolves teaches people to ignore notifications.
- **Nothing fires until its condition has held for a duration**, not a count. "Error
  rate above 5% right now" fires on one unlucky message during a quiet hour. The
  grace period restarts after a recovery, so a channel that flaps every minute never
  accumulates its way into firing as though it had been broken continuously.
- **The payload carries no patient data.** Channel names, destination names, numbers
  and thresholds, all of which come from configuration or from counting. A test
  asserts the payload field by field, so adding anything that could carry message
  content has to be deliberate: a webhook goes to a chat room, a chat room has a
  scrollback, and a scrollback is not somewhere a patient identifier belongs.

Each alert says what it means or what to do, not just what happened. A queue retried
forty times says retrying will not fix it and that a reachable receiver refusing is a
different problem from one that is down. A burst of unparseable messages says the
sending system has probably changed what it emits, making the fix a phone call
rather than a restart.

Webhook only, and that is a considered limit rather than a gap: email needs an SMTP
relay and a deliverability story, SMS needs a provider account, and a webhook
already covers Slack, Teams, Mattermost, PagerDuty and Alertmanager while adding
nothing to a binary that has no notification dependencies at all.

Notification needs a webhook; **evaluation does not**. A deployment with no chat
integration still gets the alerts page and the log lines.

## Filters

A small expression language, not a scripting engine:

```
MSH-9.2 != "A28"
MSH-9.1 == "ADT" and PID-3.1 exists
MSH-4 in ["SITEA", "SITEB"] and not PV1-2 empty
OBX-3.1 matches "^GLU"
OBX-5 > 90
```

Comparisons on message paths combined with `and`, `or` and `not`, plus `exists`,
`empty`, `matches` and `in`. No function calls, no assignment, no loops. A filter
cannot modify a message, reach outside it, or fail in an order-dependent way, so
reading one tells you exactly what it does. That is the contrast with Mirth, where
filters are JavaScript and can therefore reach into the JVM, open a database
connection or depend on another channel's state — none of it visible without
reading the code.

Two behaviours worth knowing. A path that names no repetition addresses all of
them, so `PID-3.4 == "SSA"` is true when any identifier in the list has that
assigning authority, and `!=` correspondingly means no repetition matches.
Evaluation never fails on missing or odd data: an absent path is empty and a
non-numeric value compared with `>` is false, because a filter that errors
instead of deciding leaves a message stuck. A malformed expression, on the other
hand, fails at load.

## Transformations

Two layers, deliberately separated. Declarative steps for the common work, and
scripting as a marked exception rather than the default tool.

```yaml
transformations:
  - description: pad the MRN to ten characters
    pad: {path: PID-3(1).1, width: 10}
  - description: normalise the sex code
    case: {path: PID-8.1, to: upper}
  - description: reformat the date of birth
    date: {path: PID-7.1, from: yyyyMMdd, to: "yyyy-MM-dd"}
  - description: map the sending facility to ours
    map:
      path: MSH-4
      values: {SITEA: RFAC1, SITEB: RFAC2}
  - description: drop the account number for this receiver
    clear: {path: PID-18}
    when: {path: MSH-5, equals: REGISTRY}
```

Ten step types: `set copy clear remove map replace pad date trim case`. Exactly
one action per step — two is refused at load, because a step doing two things has
an order and the order is not written down. Conditions use `== != contains matches
exists empty`.

Path notation is the same as the filter language, including the rule that a path
naming no repetition addresses all of them. That consistency is on purpose: the
same string has to mean the same thing in a filter and in a transformation, or
every channel becomes a puzzle.

**Three defaults chosen against convenience:**

- An unmapped code is **kept**, not blanked. A preserved local code can be mapped
  later; an emptied field cannot be recovered.
- A date that does not match its stated format **stops the message** by default.
  `on_error: fail|keep|clear` can change it, but the default has to be the safe
  one, because a silently mangled timestamp is accepted downstream and then
  misread.
- Padding an empty field **does nothing** rather than inventing an identifier of
  zeroes.

Steps are compiled during validation, so a broken one stops the channel from
starting rather than failing on the first message.

## Lua scripts

Scripts are JavaScript unless the channel says otherwise. `scripts.language: lua`
runs every script on the channel in Lua 5.1 instead: filter, transformer,
preprocessor, postprocessor, deploy and undeploy, and the libraries named in
`include` (`lib/<name>.lua`). One language per channel, because its scripts share
the channel map and are reviewed together.

```yaml
scripts:
  language: lua
  filter: |
    return msg.child("MSH").child("MSH.9").child("MSH.9.1").text() == "ADT"
  transformer: |
    local name = msg.child("PID").child("PID.5").child("PID.5.1")
    name.setText(string.upper(name.text()))
    channelMap.put("mrn", msg.child("PID").child("PID.3").child("PID.3.1").text())
    logger.info("normalised " .. channelMap.get("mrn"))
```

What a Lua script sees:

| Global | What it is |
|---|---|
| `msg` | The message. HL7 v2 and v3 as the same tree JavaScript sees (`PID` > `PID.5` > `PID.5.1`): `name()`, `text()`, `setText()`, `attr()`, `setAttr()`, `removeAttr()`, `child(name [, n])` (n is one-based), `children()`, `ensure(name)`, `append()`. X12, NCPDP and delimited by path instead: `get("CLM01")`, `has()`, `set()` |
| `message` | The raw text; `nil` when there is none |
| `tmp` | The outbound tree, when the channel has one |
| `channelName` | The channel's name |
| `logger` | `debug`, `info`, `warn`, `error` |
| `channelMap`, `connectorMap`, `responseMap`, `sourceMap` | `get`, `put`, `remove` |

Accessors are functions, not fields, so `node.txet` is an error rather than a
quiet `nil`, and a value read after a write is the value written.

The limits, all deliberate:

- **Libraries:** `string`, `table`, `math` and the base functions only. `os`, `io`,
  `package`/`require`, `debug` and `coroutine` are absent, and so are `load`,
  `loadstring`, `dofile`, `loadfile`, `print` and `collectgarbage`. A Lua script
  cannot read a file, run a command, read the environment or load code.
- **No Mirth helpers.** `DateUtil`, `FileUtil`, `router`, database connections,
  serializers, `UUIDGenerator`, E4X and Java are JavaScript's, for migrated Mirth
  channels. Lua is for scripts written here.
- **A filter must return `true` or `false`.** Lua counts `0` and `""` as true; a
  filter written in either language has to mean the same thing, so anything else
  is an error, as is returning nothing.
- **A deadline** (`scripts.timeout`, five seconds by default), which cannot be
  switched off; an endless loop is stopped, not waited for.
- **A call depth of 256** and a registry of 20,480 slots, so runaway recursion
  fails the message instead of the process.

## Mirth scripts

Existing Mirth transformers and filters run as written. This is the whole
migration argument: a hospital invested in Mirth finds that its scripts work
here as-is.

```javascript
// A real Mirth transformer. Unmodified.
var svc = msg['PV1']['PV1.10']['PV1.10.1'].toString();
var lookup = {'MED': 'Medicine', 'SUR': 'Surgery'};
if (lookup[svc]) { msg['PV1']['PV1.10']['PV1.10.1'] = lookup[svc]; }

var abnormal = 0;
for each (var obx in msg..OBX) {
  if (obx['OBX.8']['OBX.8.1'].toString() == 'H') { abnormal++; }
}
if (abnormal > 0) {
  msg.appendChild(<ZAB><ZAB.1><ZAB.1.1>{abnormal}</ZAB.1.1></ZAB.1></ZAB>);
}
channelMap.put('abnormal', abnormal);
logger.info('abnormal results: ' + abnormal);
```

### How, and why it was hard

Mirth's scripts are not JavaScript. They are **E4X**, an abandoned ECMAScript
extension in which XML is a native type with its own syntax. Rhino implements it;
no modern JavaScript engine does.

Three parts of E4X are *syntax*, not library behaviour, so a parser rejects the
file before any clever runtime could help: `for each (…)`, the descendant
operator `..`, the attribute operator `.@`, and XML literals. Perfuse rewrites
those into ordinary JavaScript before compiling, using a lexer rather than
pattern matching.

That distinction is the difference between working and appearing to work. A naive
rule for `..` finds one inside `http://example.org`, inside the regex `/a..b/`,
and inside the string `"a..b"`. A test asserts fifteen ordinary constructs come
through byte for byte, including `a<b && c>d`, `f(...args)`, `1..toString()` and
`x <<= 2`. Line numbers are preserved so error positions still point at the right
line.

### What is provided

`msg`, `tmp`, and the six maps with their Java `Map` methods — `channelMap`,
`connectorMap`, `responseMap`, `sourceMap`, `globalMap`, `globalChannelMap` —
plus `$()` with narrowest-scope-wins resolution and the `$c $co $r $s $g $gc`
shorthands. `logger`, `validate()`, `SerializerFactory`, `UUIDGenerator`,
`DateUtil` with Java `SimpleDateFormat` patterns, `FileUtil`, `ChannelUtil`, and
`router.routeMessage`.

One faithful behaviour is surprising enough to call out: **a field with two
repetitions concatenates when read without an index**, and assigning to it writes
both. Rhino does this, so Mirth does, so Perfuse does. Reproducing it was a
choice — a ported channel that behaves differently from the original is the one
outcome that makes a migration untrustworthy.

### Three deliberate departures

```yaml
scripts:
  transformer: |
    ...
  timeout: 5s
  allow: [file]        # file, database and route are denied unless granted
```

1. **Scripts run with a deadline.** Mirth will let a transformer loop forever and
   take the channel with it. There is no way to switch this off, because an
   interface that stops accepting admissions over a typo is not a feature.
2. **File, database and routing access are denied unless the channel grants
   them** by name. Mirth hands every script all three by default.
3. **Java interop fails loudly, naming the class it reached for.** A script
   calling into `java.sql` cannot be made to work here, and the useful outcome is
   knowing that at the line where it happens rather than getting `undefined`.
   `DatabaseConnectionFactory` is refused with an explanation, and a note that it
   is also the most common cause of a Mirth channel stalling under load.

Runtimes are pooled, and `msg`, `tmp` and every map are scrubbed between
messages. A test asserts no data from one patient can reach the next.

### Order

Fixed, and not configurable: **filter → declarative steps → script.** The script
therefore sees an already-normalised message, and anything it does stands out as
the exception it is meant to be.

## Migrating from Mirth

```sh
perfuse explain   channels/            # what would block a migration
perfuse translate -o ./channels old/   # do most of the migration
perfuse translate -strict channels/    # exit 1 if anything is blocked
```

Three rules shape the translation:

**Nothing is silently dropped.** Every part of a channel is translated, carried
across as a script that runs unchanged, or reported as needing a person. A
translator that quietly omitted a step would produce a channel that starts, runs,
and loses data.

**Declarative where it is certain, script where it is not.** A Mapper that copies a
field becomes a declarative step. A Mapper holding an expression becomes a script,
because Perfuse runs Mirth's JavaScript unchanged and rewriting it is exactly where
a translator introduces a difference nobody notices.

**The output is written to be read**, with a header naming the original channel and
`REVIEW` comments where a decision was made. A migration gets signed off by
somebody who has to review it.

Behaviour differences are reported rather than reproduced. A destination
transformer has no equivalent, since Perfuse transforms once before fan-out, so it
says to split the channel. A preprocessor ran before the filter in Mirth and cannot
here. A postprocessor ran after delivery, so it is carried across commented out
rather than run at the wrong time. `respondAfterProcessing: false` becomes
`ack.when: on_receipt` with a note that the original told senders AA even when
delivery failed.

Concurrent queue threads get the most emphatic note: Perfuse always drains in
order, that is slower during a backlog, and it is correct.

Passwords are deliberately **not** carried across. Mirth stores them in the export,
and writing one into a file destined for git would turn a migration into a
credential leak.

## Testing a channel

A channel is a program that rewrites clinical messages, and it gets edited under
pressure by people who cannot try it against production. That is most of why
interface work is frightening.

```yaml
channel: ./adt.yaml
tests:
  - name: an admission has its MRN padded to ten characters
    message: |
      MSH|^~\&|SEND|SITEA|RECV|RFAC|20260819080000-0500||ADT^A01^ADT_A01|T1|P|2.5.1
      PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|f
    expect:
      outcome: delivered
      ack: AA
      fields:
        PID-3.1: "000000MRN7"
        PID-8.1: "F"
      not_contains: ["ACC900"]     # the account number must not reach the registry

  - name: an A28 is filtered out and nothing is sent
    message: |
      MSH|^~\&|SEND|SITEA|RECV|RFAC|20260819080100-0500||ADT^A28^ADT_A05|T2|P|2.5.1
      PID|1||MRN8^^^SITEA^MR||Frost^Ivy||19910228|F
    expect:
      outcome: filtered
      ack: AA
      destinations:
        registry: {received: false}
```

```sh
perfuse test -v channels/          # *_test.yaml and *.test.yaml
perfuse test -json channels/       # for CI
```

A case runs through the **real** path: the same filter, the same declarative steps,
the same script engine, in the same order, with only the transport replaced. A
runner that reimplemented any of that would produce tests that pass while the
channel fails.

Two things are refused at load rather than at run. A case that asserts nothing
passes unconditionally and looks like coverage. And an unknown key is an error, so a
misspelled assertion cannot quietly become a test that tests nothing.

Every failing assertion is reported with the actual value quoted, not just the
first. A destination can be told to `fail`, which is how failure handling gets
tested without a real receiver that has to break on cue. `logged` asserts on script
output, which is the only way to check a code path that produces nothing else.

Retries are collapsed to a single attempt during a test, because the transport has
already been replaced and retrying a fake sender measures the retry loop rather than
the channel. It also turned one failing case into fifteen seconds of waiting, which
is how a suite stops being run.

## Generating test traffic

```sh
perfuse generate -n 20                        # ADT to stdout
perfuse generate -kind mixed -n 500 -dir ./fixtures
perfuse generate -n 100 -send 127.0.0.1:6661  # straight at a channel
perfuse generate -kind mdm -n 5               # documents inside MDM^T02
```

Everything is invented, and the obviously fictional names are a design constraint
rather than a disclaimer: a generator producing plausible real names would
eventually put one into a test fixture, then a bug report, then a repository.

Realism is spent only where it changes what gets tested. Timestamps carry UTC
offsets, because HL7 permits a bare local time and a generator that always omitted
the offset would never exercise the code that has to guess one. A feed revisits the
same handful of patients. ADT is weighted towards A08, since real traffic is mostly
updates. An A28 carries no PV1, because it is a registration with no visit. Results
come in panels with about a fifth out of range, because abnormal flags are the
reason anybody reads OBX-8.

About one message in twelve is given a legal but awkward shape: a missing optional
field, trailing separators, a second repetition where code often reads only the
first, an escaped ampersand, a Z segment. `-perfect` turns that off.

## Databases

How a great many hospital interfaces actually start. A department system has no HL7
capability, so somebody writes rows into a staging table and the engine polls it.

```yaml
source:
  type: database
  database:
    driver: postgres            # postgres, mysql, sqlserver, sqlite
    dsn: "postgres://interface:${DB_PASSWORD}@db.internal:5432/labstaging"
    query: |
      SELECT row_id, mrn, surname, forename, result
      FROM lab_outbound WHERE processed IS NULL ORDER BY row_id
    after_query: UPDATE lab_outbound SET processed = now() WHERE row_id = $1
    key_column: row_id
    poll_interval: 15s
    max_attempts: 3
    template: |
      MSH|^~\&|LAB|SITEA|PERFUSE|RFAC|${sent_at}||ORU^R01^ORU_R01|${row_id}|P|2.5.1
      PID|1||${mrn}^^^SITEA^MR||${surname}^${forename}
      OBX|1|ST|RESULT^Result||${result}
```

### The stall, and why this connector is built around it

A database reader that meets a row it cannot process and retries it forever is the
single commonest way a Mirth channel stalls. Nothing looks broken. The channel is
started, the queue is empty, the log repeats. But the feed has stopped, every row
behind the bad one is waiting for a row that will never succeed, and the first
anybody knows is a ward asking why results stopped arriving overnight.

So a row here gets `max_attempts` and is then **quarantined**, loudly, and the poll
moves on. It is deliberately *not* marked as processed, so it can be fixed and
picked up on a restart. Abandoning one row and telling somebody is worse than
processing it and better than every other option available — that is the whole
calculation, and `perfuse_database_rows_quarantined_total` is the visible cost of
it. Alert on any increase:

```yaml
rules:
  - kind: rows-quarantined
    threshold: 1
    severity: critical
```

### The other decisions

**A row is marked only after the message is durably held**, so a crash between the
two resends rather than loses. At-least-once is right here: a duplicate ADT can be
reconciled on MSH-10, and a lost admission cannot be reconciled at all. When the
marking statement itself fails the message has already been accepted, which is the
genuinely dangerous case — it says so plainly and states the honest limit, that the
duplicate is prevented while the process lives and not across a restart.

**Every column value is escaped before it goes into a template.** Same class of
problem as SQL injection and easier to hit: a free-text comment containing a pipe
would otherwise create fields nobody intended, shifting every value after it into
the wrong place. The result parses cleanly and is wrong. Newlines are encoded rather
than becoming segment breaks that invent segments.

**A query must be a SELECT.** Use `after_query` for the statement that changes
something, so the change happens once per row and is recorded against that row. A
poll running an UPDATE on a timer with no record of what it touched is not something
to discover afterwards.

### Writing to a database

```yaml
destinations:
  - name: registry
    type: database
    database:
      driver: sqlserver
      dsn: "sqlserver://writer:${REG_PASSWORD}@registry.internal:1433?database=reg"
      statement: INSERT INTO results (mrn, surname, received) VALUES (@p1, @p2, getdate())
      params: [PID-3.1, PID-5.1]
```

Values are always bound, never built into the SQL. That is an injection defence, but
the everyday reason hits sooner: O'Brien is a common name and a concatenated query
breaks on the apostrophe.

Two things that would otherwise fail quietly are errors. A statement that succeeds
and **affects no rows** is a failure, because an UPDATE whose WHERE clause matched
nothing reports success and writes nothing. And a **placeholder count that does not
match the params** is refused at load, because a mismatch binds values to the wrong
columns and writes data that looks entirely valid.

An absent field binds **NULL, not an empty string**. In a clinical table those are
different facts — "the message did not say" and "the message said it was blank" —
and collapsing them is permanent.

Placeholders are the driver's own: `$1`, `?`, `@p1`. Perfuse does not rewrite them,
because getting a rewrite wrong would bind a patient's name to the wrong column.

Oracle and DB2 are refused with a clear message rather than guessed at.

## SFTP

Extremely common and rarely spoken about. A lab or radiology system writes a file
every few minutes, an SFTP server holds it, and something has to come and get it.

```yaml
source:
  type: sftp
  sftp:
    host: sftp.lab.internal
    user: interface
    key_file: /etc/perfuse/id_ed25519
    known_hosts_file: /etc/perfuse/known_hosts
    dir: /outbound
    pattern: "*.hl7"
    poll_interval: 30s
    stable_for: 5s
    after_read: move
    move_to: /outbound/done
    error_dir: /outbound/bad
```

### Half a message

A file being written to and a file finished being written to are indistinguishable
over SFTP. There is no lock, no flag and no notification: there is a size and a
modification time, and both are true of a half-written file.

Reading too early collects half a message, and HL7 has no terminator, so half a
message is very often still parseable. The MSH is intact, the segments that arrived
are well formed, the ones that did not are simply absent. It is accepted,
acknowledged, stored, delivered, and the missing half is never mentioned again.

So a file is read only once its size and modification time have been unchanged
across two observations at least `stable_for` apart — and **never on the first
sighting**, whatever `stable_for` says, because one observation cannot establish
that anything has stopped changing. Names ending `.part`, `.tmp`, `.temp`,
`.filepart`, `.writing`, and dotfiles, are skipped: the sending side adopted that
convention for exactly this reason.

`perfuse_sftp_files_waiting` is worth watching. A number that climbs and never falls
means files are arriving faster than they settle, or something is writing a file it
never finishes.

### The host key

`known_hosts_file` is required. This is the whole security of SFTP, and getting it
wrong is invisible: everything works, the transfer is encrypted, and nothing in any
log says the server was never checked.

```sh
ssh-keyscan -H sftp.lab.internal >> /etc/perfuse/known_hosts
```

Check that against what the other side says it should be before trusting it.

`insecure_skip_host_key_check` exists, because refusing to offer it sends people to
a shell script with `StrictHostKeyChecking=no`, which is worse in every way
including auditability. It is never the default and it warns at every start that the
connection is encrypted and the server unverified — a less obvious problem than no
encryption at all.

A mismatched key refuses, says the server was either rebuilt or is being
impersonated, and explicitly warns against the obvious wrong fix. Deleting the entry
and carrying on is precisely what an impersonation needs.

### Files, once read

A file is disposed of only when **every message in it has been accepted**. Moving a
file whose second message failed would lose that message with no record anywhere,
and the file is the only copy.

`after_read` is `move` (the default), `delete` or `leave`. A file that cannot be
processed goes to `error_dir`, so one bad file does not become a warning that
repeats on every poll forever. That is logged loudly, because its messages were
never delivered and nothing else will mention them again.

### Writing files

```yaml
destinations:
  - name: send-to-registry
    type: sftp
    sftp:
      host: sftp.registry.internal
      user: interface
      key_file: /etc/perfuse/id_ed25519
      known_hosts_file: /etc/perfuse/known_hosts
      dir: /incoming
      file_name: "${date}-${control_id}.hl7"
      temp_suffix: .part
```

Every file is written under `temp_suffix` and renamed once closed, because whoever
collects these files has the identical problem and a rename within a directory is
atomic on every server worth using. A failed write removes the partial rather than
leaving it where a poller will find it.

The name is built from message fields and therefore sanitised. A control ID is data
from the sending system and it ends up in a path; without that, a message carrying
`../` in MSH-10 writes outside the configured directory.

`append: true` puts every message in one file per day and forces `framed: true`,
because MSH can appear inside a free-text field and an unframed multi-message file
cannot be split again reliably.

## Kafka

A health system with an event backbone has a Kafka one, and the services built in
the last decade publish to it rather than to a JMS broker. Perfuse reads clinical
messages off topics and publishes them onto topics, and deliberately does nothing
else: windowing, joins, stream processing, schema registries and consumer-group
tooling all stay in Kafka's ecosystem. The intent is to be the healthcare-aware
edge of somebody else's event platform, not to compete with one.

The existing STOMP broker connector is a different thing and both are kept. STOMP reaches ActiveMQ, Artemis and RabbitMQ — the brokers a hospital
already ran, and what Mirth's JMS Reader talked to. Kafka is not a broker in that
sense, and the differences are the ones that decide whether a clinical feed is safe.

### As a source

```yaml
name: adt-from-kafka
source:
  type: kafka
  kafka:
    brokers: [kafka-1.hospital.local:9092, kafka-2.hospital.local:9092]
    topics: [adt.events]
    group: perfuse-adt
destinations:
  - name: to-the-registry
    type: mllp
    address: registry.hospital.local:2575
```

`brokers` is a list because one bootstrap address is a single point of failure for
starting up: the cluster survives losing it and a channel pointed only at it would
not.

`group` is **required and never generated**. It is what remembers how far this
channel has read. A generated name would start from scratch on every restart and
leave the previous group behind holding committed offsets nobody reads. Requiring it
makes somebody name the thing that has to stay the same. Two channels sharing one
group split the traffic between them, which presents as messages going missing.

`from_beginning` is off by default. A channel pointed at a topic carrying two years
of history would otherwise replay two years of patient events into a live system on
the day it was switched on — and the first person to discover that would be doing it
in production, because production is where the topic with the history lives.

### Ordering, which is the part that matters

Kafka guarantees order **within a partition and nowhere else.** Records that share a
key always land in the same partition. So keying on the patient identifier keeps one
patient's events in sequence while letting different patients proceed in parallel:

```yaml
destinations:
  - name: to-the-bus
    type: kafka
    kafka:
      brokers: [kafka-1.hospital.local:9092]
      topic: adt.events
      key: PID-3.1
```

Without a key there is no guarantee at all. It is worth being precise about the
failure, because the obvious description is wrong: measured against a real broker,
thirty unkeyed records went to **one** partition rather than spreading, because the
client keeps a batch together and chooses a new partition between batches. So an
unkeyed feed appears ordered, stays ordered through testing, and then reorders at a
batch boundary nobody can see. An A03 discharge read before the A01 admission that
preceded it, intermittently, under load. That is worse than failing consistently,
and it is why `perfuse check` names the key or says plainly that ordering is not
guaranteed.

The key accepts a bare HL7 path (`PID-3.1`) or the brace form the other senders use
(`{PID-3.1}-{MSH-4}`). A message that cannot be parsed is published unkeyed rather
than refused: a partitioning hint should not become a delivery failure.

### Losing a message versus seeing it twice

Kafka has no per-message acknowledgement. A consumer group records a position per
partition, and committing that position means "everything up to here is done". So
Perfuse handles a batch in order and commits after the whole batch.

`commit_after_delivery` defaults to on and should stay on. With it on, a crash
mid-batch redelivers the messages that had already been handled — duplicates, which
HL7 receivers are built to absorb. With it off, a crash between the commit and the
delivery loses the message with nothing anywhere recording that it existed. A
duplicate A08 is a nuisance; a lab result that silently never arrived is a patient
safety event.

The setting is a pointer internally with a resolver, so its **absence** means the
safe value. A plain boolean would have made the zero value the dangerous one, and a
channel file written without the line would have committed on read.

### Acknowledgement and compression

`acks` defaults to `all`, which waits for every in-sync replica and is the only
setting that survives a broker failing between the write and the replication.
`leader` waits for one. `none` does not wait and will lose messages; it exists
because somebody moving non-clinical telemetry may legitimately want it, and
refusing it outright would mean they wrote their own producer instead. Choosing
anything other than `all` also disables idempotent writes, so duplicate suppression
goes with it — named in the configuration rather than discovered.

`compression` defaults to `snappy`. HL7 is highly compressible text and the wire is
usually the constraint; snappy is the cheapest in CPU, which matters on the delivery
path. `gzip`, `lz4`, `zstd` and `none` are accepted.

### Authentication

`sasl` supports `plain`, `scram-sha-256` and `scram-sha-512`. PLAIN sends the
password readable on the wire and belongs with TLS; SCRAM does not. The password
accepts `${ENV}` references like every other secret here, so a cluster credential
need not be written into a channel file.

A cluster that cannot be reached is refused when the channel loads, not at the first
patient message. Without that, a consumer starts cleanly, logs nothing useful, and
looks exactly like a topic with no traffic — which gets diagnosed as a quiet feed.

### Why this is a dependency

STOMP, MLLP, DICOM and X12 are implemented here by hand. Kafka is not, and the
reason is worth stating because the rule in this project is that a dependency has to
buy capability rather than convenience.

STOMP is a text protocol that fits in a few hundred lines. Kafka is a versioned
binary protocol across roughly seventy request types, and the part that matters most
— consumer groups — is a distributed coordination protocol with join, sync, heartbeat
and offset-commit phases and a rebalance between them. Getting that subtly wrong does
not produce an obvious failure. It produces a partition nobody is reading, or two
consumers reading the same one. In a clinical feed those are a missing lab result and
a duplicated order.

`franz-go` was chosen because it is pure Go with no cgo, so the binary stays a single
static file that cross-compiles to every target — checked against all six.

### Verified against a real broker

The Kafka tests run against an actual cluster rather than a fake, because the parts
that can be got wrong are the parts a fake does not have:

```sh
KAFKA_ADDR=localhost:9092 go test ./internal/kafka/
```

They skip loudly without that variable, so the suite still passes on a machine with
no Docker. What they assert: a message published comes back byte for byte, records
sharing a key land in one partition in the order they were produced, uncommitted
records are redelivered after rejoining, committed records are not, and an
unreachable cluster is refused at connect.

The keyed-ordering test creates its topic with a known partition count and fails if
the fixture has fewer than two. That is not defensive padding. The first version of
that test ran against a single-partition topic, where every record shares a partition
whatever its key — so it passed while proving nothing.

## Eligibility, claim status and enrolment (X12 5010)

Inspectors, like the attachment and prior authorisation ones: they build or read what is sent and store nothing. Viewer role.

| Endpoint | |
|---|---|
| `POST /api/x12/eligibility/build` | A 270 (005010X279A1) from `{senderId, receiverId, payer, provider, subscriber, dependent?, serviceTypes?, serviceDate?}`; each party is `{lastName, firstName?, id, dob?, gender?}`. The provider's NPI check digit is verified |
| `POST /api/x12/eligibility/read` | `{x12}` holding a 271: the assembled answer, every benefit, rejections in words, and `core`, the CAQH CORE data content findings |
| `POST /api/x12/claimstatus/build` | A 276 (005010X212) from `{..., patientAccount, payerClaimNumber?, chargeAmount?, serviceFrom, serviceTo?}` |
| `POST /api/x12/enrollment/read` | `{x12}` holding an 834: sponsor, payer, and each member's action, relationship, identifiers and coverages |

Built interchanges are read back and envelope-checked before they are returned, with any problem listed. The 277 that answers a 276 is
read by the existing claim status reader.

## SMART Health Links and Cards

| Endpoint | |
|---|---|
| `POST /api/shl` | Host a file behind a new link: `{label?, content, contentType?, passcode?, expiresInDays?}`. `content` is a FHIR resource or a `.smart-health-card` file. Returns `{id, link, qrSvg, expiresAt}` - the link, which holds the key, only this once. Editor |
| `GET /api/shl`, `DELETE /api/shl/{id}` | List hosted links (no keys - the server has none), and revoke one, which deletes its file. Editor |
| `POST /shl/{id}` | The public manifest endpoint, unauthenticated as the specification requires: `{recipient, passcode?, embeddedLengthMax?}`. 401 with `remainingAttempts` for a wrong passcode; 404 once revoked, expired or out of attempts. CORS open |
| `GET /shl/file/{ticket}` | A file by location, one use, valid a minute |
| `POST /api/shl/resolve` | Read a link: `{link, passcode?, recipient?, channel?}`. Returns each file decrypted, a FHIR summary, verified cards, and - with `channel` - the delivery result. Editor |
| `POST /api/shc/verify` | `{text}`: a numeric `shc:/` QR, a `.smart-health-card` file or a JWS. Viewer |

`serve -public-url https://records.example.org` sets the address links point at (at most 80 characters, so the manifest URL fits 128).
`-shl-allow-http` lets a received link use plain HTTP, for tests. Files are JWE `dir`/`A256GCM`, DEFLATE-compressed; cards ES256 with
the issuer key from `<iss>/.well-known/jwks.json`, fetched only over https.

## Amazon Web Services: S3, SQS and SNS

Five connectors, none of them using the AWS SDK: each is signed HTTP through one Signature Version 4 implementation
(`internal/awsv4`), checked against AWS's own published example. An endpoint override points any of them at LocalStack,
ElasticMQ, MinIO or a VPC interface endpoint.

Every one takes the same access settings, inline in its block:

| Key | |
|---|---|
| `region` | Required. Part of the signature, so a wrong one fails exactly like wrong credentials |
| `access_key_id`, `secret_access_key` | Literal, or `${AWS_ACCESS_KEY_ID}` and `${AWS_SECRET_ACCESS_KEY}` to read the environment at startup |
| `session_token` | For temporary credentials, which expire - and the channel stops when they do |
| `endpoint` | Overrides AWS's host |

There is no instance-metadata credential chain, deliberately: metadata addresses are refused as destinations by default, and a
credential cache with refresh is where a small implementation stops being small.

### SQS source

```yaml
source:
  type: sqs
  sqs:
    queue_url: https://sqs.eu-west-2.amazonaws.com/123456789012/adt
    region: eu-west-2
    access_key_id: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
    wait_seconds: 20          # long poll, 1-20
    max_messages: 10          # per receive, 1-10
    visibility_timeout: 90s   # optional; the queue's own setting otherwise
```

A message is deleted only after the channel has handled it. One the channel could not parse or deliver is left, so SQS offers
it again when its visibility timeout ends - and a redrive policy on the queue moves it to a dead-letter queue after however many
attempts the queue allows. Nothing is lost; a message can arrive twice, which SQS allows anyway.

### S3 source

```yaml
source:
  type: s3
  s3:
    bucket: hospital-inbound
    prefix: inbound/          # required, ends in /
    suffix: .hl7              # optional
    after_read: move          # move (default) or delete
    move_to: processed/       # default
    error_prefix: error/      # default
    poll_interval: 30s
    region: eu-west-2
    access_key_id: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
```

Objects are read in key order. A handled object is moved (copied, then deleted) or deleted; one that cannot be handled moves to
`error_prefix`, so it does not block the rest. There is no `leave`: a bucket gives a poller nothing to remember across a restart,
so leaving objects would read them all again. `move_to` and `error_prefix` may not be inside `prefix`. `framed: true` reads several
MLLP-framed messages from one object, every one of which must be accepted before the object is moved. `max_object_size` defaults to
16 MiB.

### SQS and SNS destinations

```yaml
destinations:
  - name: events
    type: sqs
    sqs:
      queue_url: https://sqs.eu-west-2.amazonaws.com/123456789012/adt.fifo
      group_by: patient       # patient (default), channel, or a path such as MSH-4
      region: eu-west-2
      access_key_id: ${AWS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SECRET_ACCESS_KEY}
  - name: fanout
    type: sns
    sns:
      topic_arn: arn:aws:sns:eu-west-2:123456789012:adt
      subject: ${message_type} ${control_id}   # for email subscribers
      region: eu-west-2
      access_key_id: ${AWS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SECRET_ACCESS_KEY}
```

On a FIFO queue or topic (a name ending `.fifo`) the message group is the patient by default: SQS orders within a group and nowhere
else, so one patient's events stay in sequence while different patients proceed in parallel. The deduplication id is the
message's SHA-256, so an engine retry inside SQS's five-minute window is not delivered twice. Both refuse a message over 256 KB with
that reason, rather than SQS's generic validation error.

### S3 destination: storage class and Athena

```yaml
  - name: archive
    type: s3
    s3:
      bucket: hospital-archive
      storage_class: GLACIER_IR     # STANDARD_IA, INTELLIGENT_TIERING, GLACIER, DEEP_ARCHIVE...
      format: ndjson                # one JSON line per message, for Athena
      region: eu-west-2
      access_key_id: ${AWS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SECRET_ACCESS_KEY}
```

`storage_class` is how an archive goes to Glacier: S3 has no separate Glacier API to call. `format: ndjson` writes each message as
one JSON object with `received_at`, `channel`, `message_type`, `trigger_event`, `control_id`, `sending_application`,
`sending_facility`, `receiving_facility`, `patient_id`, `message_time` and the whole `message`, under
`<destination>/dt=YYYY-MM-DD/`. `perfuse athena -bucket hospital-archive -destination archive` prints the `CREATE EXTERNAL TABLE`
for it, with partition projection so nothing has to run as days are added:

```sql
SELECT control_id, received_at FROM perfuse.hl7_archive
WHERE dt >= '2026-10-01' AND message_type = 'ADT' AND patient_id = '555';
```

The archive holds patient data. Encrypt the bucket (`server_side_encryption` or a bucket default), and give Athena's query results
location the same protection - results are written there in the clear unless it is encrypted too.

## Claims attachment signatures (HL7 DSDR)

`POST /api/x12/attachment/build` with `sign: {role, roleDisplay, purpose, as, signerName, npi}` signs a C-CDA before it is put in
the 275 (admin only). `role` is a NUCC taxonomy code (required). `purpose` is an Appendix E code from `8.2.1.1` to `8.2.1.18`. `as` is
`legalAuthenticator` or `authenticator`. The response's `signature` gives the participant, the XAdES `level` reached, whether it
`conforms` (X-L) and what is `missing`. `POST /api/x12/attachment/read` checks every DSDR signature in the documents, against
`trustPem` when given.

| Flag | |
|---|---|
| `serve -signing-cert`, `-signing-key` | The signing key. CA certificates after the signer's in the same PEM travel with the signature |
| `serve -tsa-url <url>` | RFC 3161 time-stamping authority, for XAdES-T and XAdES-X. Without one the signature stops at EPES |

## Hosted FHIR services

`fhir.auth` on a fhir destination: `type: aws` (HealthLake; `region`, `access_key_id`, `secret_access_key`, `session_token`),
`type: azure` (Health Data Services; `tenant_id`, `client_id`, `client_secret`, `scope` defaulting to `<url>/.default`), or
`type: client_credentials` (`token_url`, `client_id`, `client_secret`, `scope`). Secrets may be `${ENV}` references. Cannot be combined
with `bearer_token`.

## AMQP 1.0 and Azure Blob Storage

```yaml
source:
  type: amqp
  amqp:
    addr: contoso.servicebus.windows.net:5671
    address: adt-inbound              # or <topic>/subscriptions/<name>; RabbitMQ 4: /queues/<name>
    username: RootManageSharedAccessKey
    password: ${SERVICEBUS_KEY}
    tls: {enabled: true}
    prefetch: 10                      # sent ahead; still handled one at a time, in order
destinations:
  - name: archive
    type: azure_blob
    azure_blob:
      account: hospitalarchive
      container: hl7
      sas: ${AZURE_STORAGE_SAS}       # or key: ${AZURE_STORAGE_KEY}
      blob: ${date}/${channel}/${control_id}.hl7
      tier: Cool                      # Hot, Cool, Cold, Archive
```

AMQP: SASL PLAIN (or ANONYMOUS with no username), TLS, credit-based flow, transfers split at the peer's maximum frame size, and an
explicit disposition for every message. Messages carry the HL7 control id as message-id and MSH-9 as subject. An `azure_blob` source
takes `prefix` (required), `suffix`, `after_read` (move or delete), `move_to`, `error_prefix`, `poll_interval`, `max_object_size` and
`framed`, as the S3 source does. `endpoint` points either at an emulator or a private endpoint.

## TLS

```yaml
source:
  type: mllp
  listen: 0.0.0.0:6661
  tls:
    enabled: true
    cert_file: /etc/perfuse/node.crt
    key_file: /etc/perfuse/node.key
    ca_file: /etc/perfuse/ca.crt
    require_client_cert: true     # mutual TLS

destinations:
  - name: registry
    type: mllp
    address: registry.internal:6661
    tls:
      enabled: true
      ca_file: /etc/perfuse/ca.crt
```

The encryption is a few lines. The value is in what is refused at load:

- **`require_client_cert` without `ca_file`** — requesting a certificate and
  accepting whatever arrives is indistinguishable from mutual TLS in every log while
  providing nothing.
- **`insecure_skip_verify` on a listener** — it means nothing there, and its
  presence suggests a misunderstanding worth correcting before it gets copied onto a
  sender where it means everything.
- **`insecure_skip_verify` together with `ca_file`** — the authority would never be
  consulted, so one of the two is a mistake.
- **Certificate files named without `enabled: true`** — almost always somebody who
  meant to turn it on.

The minimum version defaults to **1.2, not 1.3**, deliberately. A hospital
interface engine has to talk to systems that will never support 1.3, and refusing
them means the feed runs unencrypted instead. TLS 1.0 and 1.1 are refused with
somewhere to put the compatibility rather than a bare no.

`insecure_skip_verify` exists because refusing to offer it sends people to stunnel
or back to plain TCP. It warns that the connection is encrypted against
eavesdropping but not impersonation, and points at `server_name`, which is the
honest fix for the hostname mismatch that makes people reach for it.

### The certificates page

An expired certificate on an interface feed is a real outage, and it happens
because nobody was told. The Certificates tab reports **days remaining** rather
than a date, promotes expiry from a certificate detail to a warning on the whole
connection, reads every certificate in a chain because an intermediate expiring
breaks things just as thoroughly as the leaf, and lists the endpoints running
without TLS — a page showing only the encrypted ones would let somebody conclude
everything was.

It also notes what will work here and fail elsewhere: a certificate with no subject
alternative names cannot verify against any hostname however sensible its common
name looks, and an RSA key under 2048 bits may be refused by the other end while
this end is happy.

## The HL7 v2 parser

`internal/hl7` parses by recording byte offsets, not by building objects.
Components, repetitions and subcomponents are split only when something asks for
them, and escape sequences are resolved only when a value becomes a string.

```go
m, err := hl7.ParseString(raw)

m.Type()                    // "ADT", "A01", "ADT_A01"
m.Get("PID-5.1")            // family name
m.Get("PID-3(2).1")         // second patient identifier
m.Get("OBX(3)-5")           // observation value in the third OBX
m.Raw()                     // the original bytes, unmodified
```

Paths use the notation people already write in interface specifications:
`MSH-9.2`, `PID-5.1.2`, `OBX(3)-5(2).1`. A dot works in place of the dash.

Measured on an Apple M-series laptop, `go test -bench`:

| message | time | throughput | allocations |
| --- | --- | --- | --- |
| 4 segments | 0.8 µs | 330 MB/s | 3 |
| 13 segments | 2.4 µs | 383 MB/s | 3 |
| 40-segment ORU | 7.9 µs | 380 MB/s | 3 |
| 203-segment ORU | 39 µs | 401 MB/s | 3 |
| parse + route decision | 7.5 µs | 397 MB/s | 3 |

Three allocations regardless of message size: the message, the segment index and
the field index, all sized from a counting pass. Reading fields to make a routing
decision adds nothing, because accessors return views into the original bytes.
Tests assert the allocation count so a regression fails the build.

Details that are easy to get wrong and are covered by tests: MSH field numbering
is shifted by one because MSH-1 is the field separator itself; delimiters are
read from MSH-2 rather than assumed; CR, LF and CRLF are all accepted as segment
terminators; MLLP framing is stripped; HL7's explicit null (`""`) is
distinguishable from an empty field, and an empty field from one past the end of
the segment.

## MLLP transport

`internal/mllp` carries HL7 v2 over TCP. Tolerant on read, strict on write.

```go
srv := &mllp.Server{
    Addr:        ":6661",
    IdleTimeout: 0, // a hospital feed stays open for months and goes quiet at night
    Handler: mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
        m, err := hl7.Parse(raw)
        if err != nil {
            // A sender that transmits garbage still needs an answer, or it
            // retries for ever.
            return hl7.AckFor(err, hl7.AckOptions{SendingApplication: "PERFUSE"}), nil
        }
        return m.Ack(hl7.AckOptions{Code: hl7.AckAccept}), nil
    }),
}
srv.ListenAndServe()
```

One goroutine per connection. In Go that is the cheap option: a goroutine blocked
on a read costs a few kilobytes of stack and no CPU, and the runtime multiplexes
them onto the platform's poller. Writing a poll loop by hand here would be more
code and slower.

Behaviour that is tested because it is what goes wrong in production:

- **The final segment terminator is preserved.** HL7 terminates segments with a
  carriage return and MLLP's trailer is an end block *followed by* a carriage
  return. Stripping a trailing CR without checking for the end block silently
  deletes the last segment's terminator.
- **Resynchronisation.** Stray bytes between frames are skipped and counted, not
  treated as fatal. A misconfigured peer sending an extra newline should not take
  down a feed.
- **A size limit that does not desynchronise.** An oversized message is discarded
  and the stream resumes, so one bad sender does not take out every message
  behind it.
- **A truncated frame is not a message.** If the stream ends mid-frame the read
  fails rather than returning half an HL7 message to a clinical system.
- **A handler error sends nothing.** Failing to process a message must not
  produce an invented AA telling the sender it was safely stored.
- **Graceful shutdown finishes the current message.** Idle connections are
  closed; a connection mid-message keeps going until its acknowledgement is sent,
  because cutting it loses the acknowledgement and the sender resends work
  already done.

Acknowledgements mirror addressing rather than copying it, echo the original
control ID into MSA-2, and answer in the sender's HL7 version and with the
sender's delimiters. Answering a 2.3 sender in 2.5.1, or with different
delimiters, produces an ACK it cannot read.

## What it finds

Each finding carries a stable code, so you can count or suppress them without
matching on prose. The ones that matter most:

| Code | Severity | What it catches |
| --- | --- | --- |
| `JAVA_INTEROP` | blocker | A script calling into the JVM — `java.util.Date`, `importPackage`, `Packages.*`. Invisible until you try to run it anywhere else. |
| `MIRTH_INTERNALS` | blocker | Scripts reaching into `com.mirth.connect.*` classes directly. |
| `UNKNOWN_STEP_PLUGIN` | blocker | Third-party or newer plugins, named precisely rather than skipped. |
| `XSLT_STEP` | blocker | XSLT transformation steps. |
| `EXTERNAL_SCRIPT` | blocker | Steps running a script from the server filesystem, which is not in the export. |
| `TRANSPORT_UNSUPPORTED` | blocker | Connectors Perfuse cannot yet carry, named with their endpoint. |
| `SCRIPT_DATABASE` | blocker | Database connections opened inside a script, where nobody reading the config will find them. |
| `GLOBAL_MAP` | warning | Shared mutable state across every channel on the server. |
| `CHANNEL_CHAINING` | warning | `router.routeMessage` — a dependency on another channel that does not appear in the configuration. |
| `CONFIGURATION_MAP` | warning | Values that live in server configuration rather than the export. |
| `CUSTOM_RESOURCE` | warning | Libraries loaded from the Mirth server's filesystem. |
| `NO_PRUNING` | warning | No pruning configured, so the message tables grow without limit. This is the usual cause of an interface engine filling a disk. |
| `STORAGE_DEVELOPMENT` | warning | A channel left in development storage mode in production. |

Commented-out code is not reported, and a script using the JVM five times
produces one finding, not five.

## Finding a message

Three ways, for three different questions.

**By metadata** — channel, message type, trigger event, control ID, sender, outcome,
a date range. Indexed, and what the message browser filters on.

**By content** — a filter expression in the same language channel filters use, so
what somebody works out during an incident pastes straight into a channel. This is a
bounded scan rather than an index lookup, and it says how many messages it examined
so a search that found nothing after looking at a thousand of forty thousand cannot
be mistaken for a search that found nothing.

**By who the message is about** — one box that takes an MRN, a patient name, a date
of birth, an accession number or a claim number, and finds every message about it
**whatever format it arrived in.**

### Why the third one exists

Identity lives somewhere different in every format. `PID-3` in an HL7 v2 admission,
`Patient.identifier` in a FHIR resource, `(0010,0020)` in a DICOM instance, `NM109`
of an `NM1*IL` loop in an 837 claim. Four vocabularies on one server, and during an
incident the message being hunted might be in any of them. Both of the searches above
require knowing which — and the expression search parses HL7 v2 only, so everything
else counted as unreadable.

So Perfuse maps a few concepts onto where each format keeps them: who the message is
about, which visit, which study, which claim. The concepts are deliberately few,
because they are what somebody has in their hand when they need to find a message,
which is a number off a phone call or a name off a complaint.

Values are matched as people type them, not as messages write them. A message holding
`SAMPLESON^BRAVO` is found by typing `Sampleson, Bravo`; one holding `MRN-0012345` is
found by typing `mrn0012345`. Leading zeros are kept deliberately, because two MRNs
differing only by a leading zero are two patients and quietly merging them is worse
than a search that needs the zero typed.

X12 identity is read only from `NM1` loops whose qualifier means a person — `IL`,
`QC` or `74`. Reading `NM109` from whichever `NM1` comes first would index a
submitter ID and a billing provider's NPI as patient identifiers, which is both a
wrong answer to a patient search and a provider's tax ID written into a patient
index.

### It is a privacy decision, and it is optional

Identifiers are extracted when a message is recorded and stored in an indexed table,
which is what makes the search an index lookup rather than a scan of every payload.

Those identifiers were already in the database, inside the message contents. Indexing
them adds no data — and it does make them **enumerable**, and a store that could only
be grepped for a number somebody already had can now be asked to list patients. That
is a change in exposure and it is a decision for the site, not for the software.

`-index-identity` switches it, and so does **Index patient identifiers** under
Settings → Data, without a restart. It is refused outright when message contents are
not stored, because turning that off is how a site says it cannot hold clinical
content at rest, and writing patient names into an index at the same time would
retain exactly what was refused.

Every search is written to the audit log **with the term**. A log entry recording
that somebody ran a patient search cannot answer the question an audit asks, which is
who looked up whom. So the audit trail holds patient identifiers deliberately, by the
same reasoning that requires the trail at all. Searches that find nothing are
recorded too: working down a list of names to see which ones a server has heard of is
exactly what an audit exists to reveal, and recording only the matches would hide it.

### An empty result says which kind of empty it is

"No message mentions this patient" is a statement about the traffic. "Nothing has
been indexed here" is a statement about a setting. Showing the first when the second
is true tells somebody a patient was never seen, which is a clinical conclusion drawn
from a checkbox — and it is the failure that will actually happen, because indexing
only covers messages recorded after it was switched on. Every response carries
whether the index exists, and the console says something different for each.

## Shadow mode

The question that makes interface work frightening is how you know a change is safe.
Not by reading it. A transformation is a program operating on messages whose variety
nobody has catalogued, and the case that breaks is always the one nobody thought of:
the patient with two identifiers, the result with an empty units field, the A08 that
arrives before its A01.

`perfuse test` proves the cases somebody thought of. Shadow mode proves the cases
that actually arrive.

```yaml
# in the live channel
shadow:
  channel: ./adt-candidate.yaml
  ignore: [MSH-7, MSH-10]       # things that differ on every message
  sample: 1
  max_differences: 100
```

Every message the live channel handles is also run through the candidate, and the
two outputs are compared. The Shadow tab shows the difference rate and, for each
differing message, which fields disagree and what each version produced.

### Why it is safe to point at live traffic

Two properties, both structural rather than configurable:

**The shadow cannot deliver.** Its channel is built with no destinations at all —
there is no sender object in existence for it. Not a factory that refuses, not a stub
that discards: nothing to call. A test asserts the candidate's own output directory
is never even created.

**The shadow cannot affect the live channel.** It runs after the live message has
been delivered and acknowledged, on a context detached from the message's own, with
its own shorter timeout, and its panics are recovered. A candidate whose script
throws is recorded as a candidate failure and the live message is still delivered and
still acknowledged AA. Without that, shadow mode would be a way to break production
while trying to avoid breaking production.

### What it reports

**A filter disagreement is counted separately from a transformation difference.** It
is the most consequential disagreement available: one version keeps a message the
other drops, which decides whether the receiving system hears about that patient at
all. A transformation difference changes a value.

**Comparison is field by field at component level.** Comparing bytes would report
"they differ" and leave you to find it, or produce a character-level diff of a
pipe-delimited string that nobody can read. Component level because that is the
granularity a mapping operates on — a change to `PID-5.1` reported as a change to
`PID-5` hides which part moved.

**`ignore` is usually necessary.** A channel that stamps a timestamp or a sequence
number differs on every single message, and without excluding those the report says
100% while telling you nothing.

**The verdict never says "safe to promote".** It says what was observed. When
everything matched it says that is evidence the candidate changes nothing on the
traffic seen so far, not proof that it changes nothing. Nothing here can tell an
intended change from a mistake; that judgement is the point of showing you the
differences rather than a score.

`sample` exists for a busy feed and is worth understanding before using: it reduces
cost and confidence in exactly the same proportion, and the message that would have
shown the difference is usually the unusual one.

## Performance and scaling

Measured rather than asserted, on a development machine, with a regression test so
these numbers cannot quietly get worse.

**The message store absorbs about 9,000 messages a second** on disk with WAL
enabled, and does not degrade with more concurrent writers. A large hospital ADT
feed peaks two orders of magnitude below that, so throughput is not the constraint.

**The constraint was an interaction, not a rate.** Everything shared one database
connection, so a message browser search scanning the payload column held the
connection every arriving message needed. A write went from 104µs to 2.6ms while a
search scanned four thousand messages: twenty-five times slower, scaling with the
table. At a few hundred thousand stored messages a report becomes seconds of
delivery latency — and a write sits on the acknowledgement path, so the sender times
out, resends, and somebody spends a morning looking for a network fault. It is
indistinguishable from what the Mirth forums call "processing stalls each morning",
which is of course exactly when somebody opens the dashboard to look at the
overnight batch.

SQLite in WAL mode already allows readers alongside a writer; the limit was Go's
pool sitting above it. There are now two pools against the same file: one connection
for writes, because SQLite permits one writer regardless, and several for reports. In
the same scenario a write takes 207µs instead of 3.2ms.

The read pool is opened read-only, so a query that quietly became a write cannot run
on a pool sized for concurrency. It is bounded at eight connections rather than
scaled freely, because each carries its own page cache and a burst of expensive
reports would then compete for memory instead of for the disk.

## Design

**Read what people actually have.** You cannot replace an interface engine
without first reading its configuration. That is why the importer came before
the engine.

**Never fail on the unknown.** Channel exports are XStream serializations of
Java objects whose class names move between Mirth versions, and any plugin can
appear. Perfuse walks the XML into a generic tree, recognises steps by class-name
suffix so a package rename does not break it, and reports what it did not
understand instead of rejecting the file. "Here are the four things I could not
translate" is useful. "Parse error at line 812" is not.

**Findings must be specific enough to act on.** "This channel uses JavaScript"
is not a finding. "Destination 1, step 3 calls `java.util.Date`" is.

**Configuration belongs in files, not a database.** Mirth stores channels in its
own database, which is why version control is a separate product — a third-party
tool exists purely to extract channels out of that database and into git — and why
promoting a channel between environments is awkward. Perfuse channels are files.
Git, diffs, review and CI work with no extra machinery, and the audit trail is
`git log`. The database holds only users, sessions, the audit log, messages and
FHIR resources; never a channel.

**One static binary, and dependencies chosen deliberately.** No JVM, no ODBC layer,
no native client library to install on a hospital server. Every dependency is pure
Go, which is what makes that possible: 15 direct, three of which are the database
drivers. That number went up from four when the database connectors landed and again
when Kafka did, and both were the right trade — an interface engine that cannot reach
the hospital's SQL Server, or the event backbone the rest of the estate publishes to,
is not a replacement for one that can. Where a dependency would buy convenience
rather than capability it is refused: alerting is webhook-only for that reason.
`lib/pq` was chosen over `pgx` because only `database/sql` is ever used and pgx's
connection pool would be dead weight. `franz-go` was chosen over a librdkafka
wrapper because it is pure Go, and over hand-writing the protocol — which is what
STOMP, MLLP, DICOM and X12 got here — because Kafka's consumer-group protocol is
distributed coordination, and getting it subtly wrong yields a partition nobody reads
or two consumers reading one rather than an obvious failure.

This paragraph said "eight direct" for several months while `go.mod` had thirteen,
in the passage arguing that dependencies are counted carefully. A test now compares
the number against `go.mod`, because a claim about discipline that is itself
out of date argues against itself.

**Two readings of the same thing must be checked against each other.** A C-CDA
carries its content as narrative and as codes. An acknowledgement says one thing
and the message store another. A dashboard count and a message browser can
disagree. Wherever the same fact is recorded twice, the interesting failure is
that the two copies differ, so the metrics come from the same record the store
gets, and the document reader compares the narrative against the entries.

**Memory should be boring.** Mirth's most-reported operational problem is heap
exhaustion. The target here is a flat resident set under load rather than a
small idle number: lazy message parsing to byte offsets, pooled buffers, no
reflection in the hot path, and a hard memory ceiling.

## Verifying it against real software

`make check` and `make e2e` need nothing but Go and Node. Three further checks need a container runtime, and they exist because the
rest of the suite cannot do what they do:

```sh
scripts/interop-up.sh             # Keycloak, HAPI FHIR, ActiveMQ, Orthanc, nginx with client certificates
```

Two of the five found defects that nothing else would have. SAML's canonicaliser could not accept any real
assertion. The FHIR converter was giving unmapped assigning authorities an identifier system that announced an
OID and carried a word — which HAPI accepted and stored, so it only showed up on reading the resource back.
Mutual TLS, STOMP framing and DICOM C-STORE against a real PACS turned out to be correct.

Every other test of these features had both halves written here — a document Perfuse signed, read back by the code that signed it.
That is agreement with oneself. The SAML canonicaliser carried a thousand lines of such tests and **no real identity provider could
have signed anybody in**: the canonical form a signature covers was computed with namespace prefixes dropped, and only a document
Perfuse had not written could show it. Mutual TLS, checked the same way, turned out to be correct. The method mattered more than the
outcome in both cases.

## What happens when it is killed

The question anyone who has run an interface engine asks first, and the one nobody
answers, because the honest answer is usually embarrassing.

It was answered by killing the process with `SIGKILL` part way through a batch, which
is what a power failure, an out-of-memory kill and a hypervisor reset all look like
from inside, and then counting what survived against what had been promised.

### The property being tested

Not "nothing is lost". That is not achievable and claiming it would be a lie. The
narrower promise is the one a sender actually relies on:

> a message that was positively acknowledged is a message that arrived.

With `ack.when: on_delivery`, which is the default, every destination is written
before the acknowledgement goes out, so an `AA` is a statement about the destination
rather than about a queue. A message killed before its acknowledgement may well be
lost, and that is correct: the sender was never promised anything and will send it
again.

### Killed mid-batch, acknowledging on delivery

Five runs, killed after 5, 25, 40, 75 and 110 messages of 120.

| Acknowledged | Present downstream afterwards | Acknowledged but missing |
|---|---|---|
| 255 | 255 | **0** |

The promise held at every kill point. Nothing was duplicated either, though a
duplicate would have been acceptable and a loss would not: a resent A08 is a nuisance
receivers absorb, and a lab result that silently never arrived is a patient safety
event.

### Killed mid-batch, acknowledging on receipt

`on_receipt` answers as soon as the message is queued, before any destination has been
written. The documentation has always said an acknowledged message can still be lost
this way. Here is the size of it:

| Killed after | Acknowledged | Present downstream | Lost |
|---|---|---|---|
| 5 | 5 | 1 | 4 |
| 25 | 25 | 1 | 24 |
| 75 | 75 | 2 | 73 |
| 110 | 110 | 2 | 108 |

Essentially everything in flight. The setting is not a defect and there are feeds
where it is the right choice, but it should be chosen in full knowledge that a crash
discards what has been acknowledged and not yet written, and that "what has been
acknowledged and not yet written" is almost all of a burst.

Nothing recovered on restart, because `perfuse run` has no durable store. A
destination that must not lose messages during an outage needs `queue.enabled`, which
needs the database that `perfuse serve` provides.

### A sender that dies half way through a message

The most dangerous of these faults, because a truncated HL7 message usually still
parses. The segments before the cut are complete and well formed, so a receiver has no
way to know that `PID` and `PV1` arrived and the `OBX` segments carrying the results
did not.

A frame was opened, six tenths of a message sent, and the connection reset rather than
closed. The partial message was not delivered, the listener survived, and a
well-formed message sent immediately afterwards was accepted normally — so the
fragment was not carried forward into the next message either.

What this did find was a reporting gap. The number of bytes abandoned was reported
when the peer closed tidily and omitted for every other read failure — which is to say
omitted for a sender that crashed, a process that was killed and a cable that was
pulled. Those connections logged `messages=0` and nothing else, so an operator could
not tell an empty health check from a lab result thrown away nine tenths of the way
through. It is now reported for every failure mode.

### A full disk

The destination volume was filled to zero bytes available and messages kept coming.
Space exhaustion is detected and logged rather than swallowed.

Two things are worth knowing. The sender does not get a prompt rejection: the delivery
retries on the usual schedule — one second, two, four, eight — and the negative
acknowledgement arrives about fifteen seconds later. A sender whose own timeout is
shorter than that sees a stalled connection rather than an `AE`, and will conclude the
network is at fault rather than the disk.

And a volume reporting zero bytes free can still accept small appends for a while,
because adding a couple of hundred bytes to a file whose last block has room needs no
new allocation. Do not treat the first write failure as the moment the disk filled; it
is later than that.

### What was not tested

A destination directory losing write permission. It could not be injected with
`chmod`: the engine holds the output file open, and changing a file's mode does not
affect a descriptor already opened against it. Testing it properly needs the volume
remounted read-only or removed underneath the process, which has not been done. It is
listed here rather than omitted, because a gap nobody mentions reads as a gap nobody
looked for.

## Measured against public conformance corpora

Tested against other people's data rather than only its own, because a test suite whose
fixtures were written here proves agreement with oneself.

### FHIR R4, the specification's own examples

All 2,912 example files published with the FHIR R4 specification, which is to say every
example the authors of the standard wrote.

| Result | Count |
|---|---|
| Resources validated with no findings | **13,723** |
| Resources reported invalid | 0 |
| Files refused as an unimplemented resource type | 753 |
| Bundle entries skipped as an unimplemented type | 1,348 |

Not a single resource was validated incorrectly. Every failure is an explicit refusal
naming the type it cannot read — 672 `ValueSet`, 80 `ConceptMap` and one `Parameters`.
That is the intended behaviour: a validator that quietly passes what it does not
understand is worse than one that says so.
ValueSet has since become a stored type (October 2026, for DTR answer lists), so the
672 value sets in that corpus are now read rather than refused.

The corpus found a real defect, which is the reason for running it. Validating a bundle
printed "Bundle validation from a file is not supported yet" and carried on. That was
false in both directions. Bundles whose entries happened to be types the Bundle struct
could hold were already being validated and reported valid. And a bundle that was
genuinely invalid — an impossible `birthDate`, a code outside its value set — produced
the same line, told the operator the tool could not check their file, counted nothing,
and **exited zero**. With `-strict`, which exists so this can gate a build, a broken
bundle passed the gate. Forty-two of the specification's own bundles were being skipped
this way.

Bundles are now validated entry by entry. The same corpus went from 2,100 resources
checked to 13,723.

### HL7 v2, the HAPI test corpus

The message fixtures from HAPI, the reference Java HL7 v2 implementation — deliberately
awkward material, including uuencoded payloads, escaped delimiters and repeating groups.

59 messages, 59 parsed, nothing refused. Seven message types in a corpus that any one
system would describe as one thing, which is the point `perfuse profile` exists to make.

### What has not been run

DICOM against a public conformance set, and X12 against a published corpus. Neither has
been done, and neither should be assumed from the two above.

## More than one server

A site with two instances has a question no single console can answer: is everything
running? Mirth charges for the answer. This is the fleet view.

One instance is nominated as the place you look. It polls the others and shows every
channel on every server in one table, with a rollup across all of them.

### Adding a peer

Settings → Fleet, or `PUT /api/fleet/peers`. A peer needs a name, a URL, and a token
issued by the peer itself:

```
name:  bravo
url:   https://perfuse-02.hospital.internal:8443
token: <a viewer-scoped token created on perfuse-02>
```

The name is required and is not the URL, because a URL is not something anybody
recognises at three in the morning.

The token must be created **on the peer**, under Users, and should be viewer-scoped.
Adding a peer without one is refused, with those instructions, rather than accepted and
then failing quietly on every poll. Reading another instance's health is the smallest
privilege there is, and it is the only one the fleet view needs.

### Controlling a peer, which is off

`allow_control` permits starting and stopping that peer's channels from here. It is off
by default and every use is audited by name.

The two privileges are deliberately separate. Reading another server's health is minor;
stopping its channels during a transfusion is not, and the second should never arrive
silently attached to the first.

### What the report says

Each server reports the total number of channels, how many are running, stopped or
errored, the queue depth and the age of the oldest waiting message, how many alerts are
firing, and whether it is draining for shutdown.

The rollup counts servers three ways: **reachable**, **unreachable**, and
**undetermined**. The third covers peers whose health nobody knows — not yet polled,
refusing the token, or running a version this one cannot read. It is kept apart from
unreachable because "we cannot tell" is not "it is down", and folding the two together
is how a fleet page starts lying.

For the same reason the rollup carries **knownFrom**: how many instances the channel and
queue figures were actually read from. Twelve channels running across a fleet means
something different when two of five servers did not answer, and the total alone cannot
say so.

An unreachable peer says why in the terms an operator can act on — a refused connection
is reported as the host being up with nothing listening on that port, which is a
different problem from a host that does not answer at all.

### Clock skew

The rollup reports the largest difference between the clocks of the servers in it.
Worth a number of its own: correlating an incident across two instances whose clocks
disagree by four minutes produces a sequence of events that did not happen.

### What a peer learns about you

Counts and rates. The report one instance gives another carries no message identifiers,
no channel-level detail and no patient data of any kind.

That is what stops a fleet view being a centralisation of clinical data: the aggregating
instance learns how many channels are running and nothing whatsoever about what flowed
through them. If you want the messages, you open that server's own console, where the
audit log records that you did.

### A peer pointed at itself

Refused. An instance polling its own address through its own HTTP stack appears twice in
its own fleet view and double-counts every channel it has.

### TLS on internal networks

`insecure_skip_verify` accepts a peer's certificate without verifying it. It exists
because hospital infrastructure runs on private certificate authorities, and refusing to
work at all would push people onto plain HTTP, which is worse. It is named so it cannot
be mistaken for a good idea.

## Roadmap

Not built yet. Listed so the direction is clear, not to suggest it works.

1. Local AI assistance for explanation and mapping proposals, with the ability to
   abstain rather than guess.

## Not goals

- Being a drop-in Mirth replacement. Scripts run unchanged, which covers most of
  what a channel does, but a channel reaching into the JVM — `java.sql`,
  `Packages.*`, a custom JAR — cannot be translated mechanically, and pretending
  otherwise would be dishonest. Those fail with the class name so you know
  exactly what needs rewriting.
- Generating C-CDA. Reading, checking and converting documents is most of the
  value at a fraction of the risk.
- Cloud anything. This runs where the data is.

## A note on data

Everything in `testdata` is synthetic and written by hand. **No real HL7 is in
this repository and none ever should be** — real messages are protected health
information. `.gitignore` blocks `*.hl7`, `*.er7` and any `corpus/` directory
outside `testdata` as a backstop, but that is a safety net, not a policy.

### Testing against real traffic

The parser is validated against real message captures through an opt-in corpus
test that skips when no corpus is present, so no patient data ever reaches a
build machine:

```sh
PERFUSE_CORPUS=/path/to/capture.txt go test ./internal/hl7/ -run Corpus -v
```

It accepts MLLP-framed captures, one message per line, or messages run together.
Every assertion is structural and every log line is an aggregate — nothing
derived from message content is printed, because test output ends up in
terminals, CI logs and pasted bug reports.

What it checks for each message: that it parses, that MSH-9 and MSH-10 are
present, that the parser returned the bytes unmodified, that every addressable
segment, field, repetition, component and subcomponent can be read without
panicking and returns bytes that actually occur in the message, that reading past
the end of a segment reports absence rather than failing, that an escape round
trip is stable, and that a generated acknowledgement is itself parseable and
correctly addressed.

Against a sample of 299 real ADT messages spanning ten trigger events (A01, A02,
A03, A04, A06, A07, A08, A11, A13, A28): all 299 parsed, 28,396 fields indexed,
1,196 generated acknowledgements validated, 13,345 values escape-round-tripped,
at 425 MB/s and 3 allocations per message. The widest message had 13 segments, a
45-field segment, a field with 7 repetitions and one with 13 components.

## License

Apache License 2.0. See [LICENSE](../LICENSE).
