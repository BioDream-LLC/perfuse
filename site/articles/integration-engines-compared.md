# Open-source healthcare integration engines compared: Mirth 4.5.2, OIE, BridgeLink and Perfuse

![Four engines as columns, Perfuse highlighted](docs/assets/articles/hero-engines.svg)

By **BioDream Developer**, BioDream LLC

Since Mirth Connect moved to a commercial licence from version 4.6, many teams have been asking which open-source engine to run. There are now four serious options in the Mirth family and beyond. This guide sets out what each one is, so you can choose with the facts in front of you.

## The four options

**Mirth Connect 4.5.2.** The last open-source release of Mirth Connect, under the Mozilla Public License 2.0. It runs on Java, it is widely known, and a great deal of existing interface work is built on it.

**Open Integration Engine (OIE).** A community fork of Mirth Connect 4.5.2, continuing development in the open under the same licence, with releases from 4.6.0 onwards. The project is preparing to move to the Eclipse Foundation.

**BridgeLink.** Another fork of the Mirth 4.5.2 code base, continuing its line of releases.

**Perfuse.** A new engine, written from the ground up in Go and licensed under **Apache 2.0**. It reads and writes Mirth's channel format, so it works alongside all three of the above, and adds a modern set of standards and safety features on top.

## At a glance

| | Mirth 4.5.2 | OIE | BridgeLink | Perfuse |
|---|---|---|---|---|
| Origin | NextGen Healthcare | community fork of 4.5.2 | fork of 4.5.2 | new engine |
| Language | Java | Java | Java | **Go** |
| What you install | application + Java runtime | application + Java runtime | application + Java runtime | **one file** |
| Licence | MPL 2.0 | MPL 2.0 | from Mirth 4.5.2 | **Apache 2.0** |
| Channels | Mirth XML | Mirth XML | Mirth XML | **YAML files, imports and exports Mirth XML** |

![The Mirth 4.5.2 family compared with Perfuse](docs/assets/articles/engines-compared.svg)

## What the Mirth family shares

Mirth 4.5.2, OIE and BridgeLink come from one code base, so they share a great deal: the channel model of sources, filters, transformers and destinations, JavaScript with E4X for transformations, the Administrator client, and the same channel export format. Moving between them is straightforward, and the skills carry over.

## What Perfuse adds

**Perfuse keeps what teams rely on from Mirth and builds a new foundation under it.**

- **Your Mirth work comes with you.** Mirth JavaScript runs **unchanged**, E4X included. `perfuse translate` converts a channel, a group, a code template library or a whole server backup. Verified against **Mirth 4.5.2, OIE 4.5.2 and 4.6.0, and BridgeLink 26.9.0**.
- **And it goes back.** Channels built in Perfuse export as Mirth channel files that **Mirth, OIE and BridgeLink all accept**, so Perfuse works side by side with any of them.
- **One file, 31 MB of memory at rest**, against Mirth 4.5.2's 383 MB measured on the same machine. No Java to install, patch or tune.
- **Modern standards built in**: FHIR R4, R4B and R5 with a REST server and US Core, Da Vinci **CRD, DTR and PAS**, the four **CMS-0057** payer APIs, **TEFCA and UDAP**, **eCR and ELR**, **SMART on FHIR** with its own authorization server, **SMART Health Links**, and signed **X12 275** attachments.
- **Every format**: HL7 v2, FHIR, DICOM (all five services), X12 (837, 835, 270/271, 276/277, 834, 278), C-CDA and CDA.
- **Twenty source types and twenty-two destinations**, including Kafka, AMQP 1.0, Azure Blob, and AWS S3, SQS and SNS.
- **Safer change**: **shadow mode** tests a change on real traffic with nothing delivered, **feed contracts** catch upstream changes, and **`perfuse compare`** proves two engines agree.
- **Operations**: a live dashboard, a flow map with a time scrubber, Prometheus metrics, OpenTelemetry tracing, eleven alert types, patient search across every format, and a fleet view across instances.
- **Enterprise sign-on**: SAML, OpenID Connect, LDAP, passkeys, SCIM and mutual TLS.
- **Everything in the browser**, and every channel also a plain file you can keep in git.
- **Lua as well as JavaScript**, and eleven declarative steps that need no code.

## Choosing

- **Staying with the Mirth model you know?** OIE and BridgeLink continue it in the open.
- **Want a modern engine without leaving your Mirth work behind?** Perfuse runs your channels, adds the standards CMS and ONC now require, and lets you move one channel at a time, with proof at every step.

## Try Perfuse in two minutes

1. Download it from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse explain` on one of your Mirth channel exports.
3. Run `perfuse serve` and open http://127.0.0.1:8080.

Related: [The Mirth 4.6 licence change](https://perfuse.health/mirth-licence-change/) · [How to migrate Mirth Connect channels](https://perfuse.health/migrate-mirth-channels/).

Mirth and Mirth Connect are trademarks of their respective owners, used here only to describe compatibility. Open Integration Engine and BridgeLink are the names of their respective projects.
