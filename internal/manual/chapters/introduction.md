# Introduction

Perfuse moves clinical messages between systems that were never designed to talk to each other. A laboratory sends results in HL7 v2; the practice management system expects them with different field positions, a different patient identifier and an acknowledgement within five seconds. Something has to sit in the middle, and that something is an integration engine.

This manual describes what Perfuse does, every option it accepts, and — where a decision is not obvious — why it works the way it does.

## What Perfuse is for

A single interface between two clinical systems is commonly costed in the tens of thousands, with fifteen to twenty per cent of that again every year in maintenance. Much of that cost is not the work; it is that the work requires somebody who has done HL7 before, and most practices do not employ one.

Perfuse is built on the assumption that the person configuring it has a problem to solve and no prior HL7 experience. That assumption shows up in specific places:

- Everything is configurable from the web interface. A feature reachable only from a terminal or a hand-edited file is treated as a defect, not a design choice.
- Messages can be traced one transformation at a time, showing what each step read, what it changed, and what it skipped and why.
- Test traffic can be generated that matches the shape of a real feed without containing any of its data.
- A channel can be proposed from a single sample message, with an explicit statement of what the sample cannot tell you.
- Before replacing an existing engine, its output and Perfuse's can be compared message by message, with the differences grouped by field.

> The chapter on [testing](#testing) and the one on [migration](#migration-from-mirth) exist because the answer to "why should I trust this" has to be evidence you gather yourself, not a claim made here.

## What is in it

Everything below is in the one binary. Each line names the chapter that covers it.

- **Formats.** HL7 v2, FHIR R4, R4B and R5, DICOM, X12 (837, 835, 270/271, 276/277, 834, 278, 275), C-CDA and CDA, HL7 v3,
  NCPDP Telecommunication and SCRIPT, SMART Health Links and Cards, delimited and raw. See [message formats](#message-formats).
- **Connectors.** 28 source types and 31 destination types, among them MLLP, files, SFTP, databases, DICOM, Kafka, AMQP 1.0,
  MQTT, IBM MQ, and AWS, Azure and Google Cloud storage and messaging. See the [source reference](#source-reference) and the
  [destination reference](#destination-reference).
- **Changing messages.** Filters, eleven declarative transformation steps, JavaScript (Mirth's dialect included) and Lua, shared
  mapping tables and code sets, and mapping suggestions. See [filtering](#filtering) and [transformation](#transformation).
- **Delivering them.** A durable, ordered queue per destination, retries, dead letters that keep the message, and acknowledgements
  sent after delivery. See [delivery](#delivery-queueing-and-retry) and [acknowledgements](#acknowledgements).
- **Watching it run.** Dashboards, team dashboards, a flow map, eleven kinds of alert, metrics, tracing, and a message store that
  finds a patient's messages across formats. See [alerting](#alerting), [the message store](#the-message-store) and
  [debugging](#debugging).
- **Changing a live interface safely.** Shadow mode, feed contracts, channel tests and engine comparison. See [testing](#testing).
- **Leaving Mirth.** Mirth, Open Integration Engine and BridgeLink channels explained, translated and exported back. See
  [migration](#migration-from-mirth).
- **Payers and public health.** The four CMS-0057 APIs, Da Vinci CRD, DTR, PAS, PDex and Plan-Net, CARIN Blue Button, an optional
  decision service, eCR and ELR. Da Vinci CRD, DTR and PAS answer both their 2.2.x and their 2.1.0 releases. See
  [CMS-0057](#cms-0057-for-payers).
- **Who may do what.** SAML, OpenID Connect, LDAP, passkeys, SCIM, mutual TLS, six roles, channel groups with a role in each,
  tenants, a SMART authorization server, TEFCA and UDAP, and an audit log. See [security](#security).
- **Running it.** One static binary for Linux, macOS and Windows, as a service, with a fleet view of several servers. See
  [operations](#operations), the [web interface](#the-web-interface), the [command line](#the-command-line) and
  [server options](#server-options).

## Standards and versions

Which release of each standard Perfuse implements, and how that was checked. "Checked" names the outside tool or
reference that judged the result; where nothing outside has judged it, it says so. Results are from test data, not from a
production site.

### Messaging and documents

| Standard | Release | Checked |
|---|---|---|
| HL7 v2 | any version: messages are read by their own separators, not a version's schema; acknowledgements default to 2.5.1 | NIST HL7 v2 validator; real Mirth and OIE traffic |
| HL7 v2 to FHIR | HL7's V2-to-FHIR mapping (ADT, ORU, SIU, MDM, VXU and the segments in them) | the HL7 FHIR validator against US Core, UK Core, FR Core, ISiK, nl-core and HL7 Europe |
| FHIR | R4 (4.0.1), with R4B and R5 read and written | the HL7 FHIR validator; Inferno |
| C-CDA and CDA | C-CDA 2.1 | ONC SITE (C-CDA samples sent and received over XDR); not by a CDA schematron |
| HL7 v3 | PIX and PDQ (IHE ITI-44, 45, 47) | IHE's NE2008 schemas and sample messages |
| DICOM and DICOMweb | DIMSE C-STORE, C-FIND, C-MOVE, C-GET, C-ECHO; QIDO-RS, WADO-RS, WADO-URI, STOW-RS | Orthanc |
| X12 (HIPAA) | 005010: 837, 835, 270/271 (X279A1), 276/277 (X212), 834, 278 request (X217) and inquiry (X215); 275 in 006020 (X314) | payers' published companion guides; CMS esMD samples |
| CAQH CORE connectivity | vC2.2.0 SOAP and MIME, vC4.0.0 with MTOM | a stand-in UM system written from CAQH's rules |
| NCPDP | Telecommunication D.0; SCRIPT 2023011 (and 2017071) with ePA; Real-Time Prescription Benefit v13 | written from the public Surescripts companion guides and their examples; not against NCPDP's licensed schemas |
| UN/EDIFACT | syntax 3 and 4, CONTRL | pydifact 0.1.8 |
| IHE document sharing | XDS.b, XCA, XDR, MHD 4.2.2, PIXm, PDQm, XDM | the NIST XDS Toolkit; ONC SITE |
| Direct secure messaging | Applicability Statement 1.2, XDR and XDM for Direct, DNS and LDAP certificate discovery | ONC SITE; ONC's Certificate Discovery Tool (18 of 18) |
| AS2 | RFC 4130, compression RFC 5402 | pyas2lib |
| Signed attachments | HL7 DSDR, XAdES-X-L | xmlsec1, OpenSSL |

### FHIR implementation guides

| Guide | Release | Checked |
|---|---|---|
| US Core | conversions to 9.0.0 (USCDI v6); FHIR server against 6.1.0 and 7.0.0 | the HL7 validator; Inferno US Core |
| SMART App Launch | 2.2 | Inferno: 80 of 80 (public app) |
| Bulk Data Access | 2.0.0 | Inferno: 48 of 48 |
| Subscriptions R5 Backport | for R4 | a real HTTP receiver; the PAS kit's notification tests |
| Da Vinci CRD | 2.2.1, and 2.1.0 | Inferno 2.2.1: 92 pass, none fail; 2.1.0 by the guide's own examples |
| Da Vinci DTR | 2.2.0, and 2.1.0 | Inferno 2.2.0: 43 pass, 2 fail (a kit defect); 2.1.0 by the guide's own examples |
| Da Vinci PAS | 2.2.1, and 2.1.0 | Inferno 2.2.1: 82 of 84; 2.1.0 by the guide's own examples |
| Da Vinci PDex | 2.2.0 | Inferno PDex 2.0: payer-to-payer 19 of 19 |
| Da Vinci HRex `$member-match` | 1.2.0 | Inferno PDex payer-to-payer |
| Da Vinci ATR | 2.1.0 | not checked by an outside tool |
| Da Vinci CDex `$submit-attachment` | 2.1 | not checked by an outside tool |
| Da Vinci Plan-Net | 1.1.0 | Inferno: 199 pass, 10 fail (a kit defect) |
| Da Vinci US Drug Formulary | 2.1.0 | the HL7 validator |
| CARIN Blue Button | 2.2.0 | the HL7 validator |
| eCR | 2.1.2 | the HL7 validator |
| ELR | HL7 2.5.1 ELR Release 1 with its errata | ONC's ELR tool: 28 of 28 |
| International Patient Summary | 2.0.0 | the HL7 validator |
| DEQM | 5.0.0 | the HL7 validator |
| SQL on FHIR | v2 | the official test suite: 144 of 144 |
| SMART Health Cards and Links | Cards 1.4, Links STU 1 | the specifications' own example card; a published SMART Health Links viewer library |
| UDAP Security | STU 1 | a real authorization server and trust community |
| TEFCA Facilitated FHIR | SOP v2.0 | not checked against a real QHIN |

### National and European profiles

Checked by converting published or invented HL7 v2 messages and validating every resource; the full results and the
reasons for each failure are in the repository's `conformance/national` folder.

| Profile | Release | Resources valid |
|---|---|---|
| UK Core | STU2 2.0.2 | 185 of 187 |
| FR Core | 2.2.0 | 138 of 156 |
| ISiK Basismodul | 4.0.3 (Stufe 4) | 18 of 47; 42 of 47 with the hospital's own bundle script |
| Nictiz nl-core | 0.12.1-beta.1 (zib 2020) | 12 of 24; 18 of 24 with the hospital's own bundle script |
| HL7 Europe Laboratory Report | 2.1.0 | 106 of 426 (the guide expects a document a v2 result is not) |
| HL7 Europe Hospital Discharge Report | 1.0.0 | 68 of 69 |

None of this is a certification. Perfuse has not been certified by ONC, CMS, gematik, Nictiz, NHS England or anyone else.

## What it replaces

Perfuse covers the ground held by Mirth Connect, and can import Mirth channel exports directly. The chapter on [migration](#migration-from-mirth) describes what translates cleanly, what needs a decision, and how to prove the result matches before cutting over.

The differences that matter in daily use are debugging and silence. In Mirth, seeing what a transformer did to a message generally means redeploying the channel with logging added. In Perfuse, any recorded message can be replayed through the current configuration and inspected step by step, without touching the running channel. And where a Mirth alert on "no traffic" is a fixed threshold that is useless on any feed that is quiet at night, Perfuse learns what each feed's normal week looks like and alerts against that.

## How to read this manual

The first chapters are conceptual and are worth reading in order: [channels](#channels), [message flow](#message-flow), then [transformation](#transformation). Everything after that can be read as needed.

The reference chapters near the end list every configuration key, its type, whether it is required and its default. Those chapters are generated from the source, so they cannot describe an option that does not exist or omit one that does. If a key appears in a configuration file and not in the [index](#index-of-configuration-keys), it is not a Perfuse key.

## Conventions

Configuration is YAML. Most keys are `snake_case` on the wire — `max_message_size`, `idle_timeout` — and this manual always gives the wire form, because that is what you type. There is one long-standing exception, `dataType` on a channel, which is camelCase; it is documented that way in the [channel reference](#channel-reference) and accepted only in that spelling.

Field references in HL7 use the usual notation: `PID-5` is the fifth field of the PID segment, `PID-5.1` its first component, and `PID-3(2)` the second repetition of the third field. A path written without a repetition addresses every repetition (see [paths](#paths)).

Durations are written as a number and a unit — `30s`, `5m`, `2h`. A bare number is not a duration and will be refused rather than guessed at.
