# Open-source healthcare integration engines compared: Mirth 4.5.2, OIE, BridgeLink and Perfuse

![Four engines as columns, Perfuse highlighted](docs/assets/articles/hero-engines.svg)

## In one minute

- Since Mirth Connect started charging from version 4.6, hospitals have been asking which free integration engine to run instead.
- There are four serious options: the last free Mirth (4.5.2), two free community continuations of it (OIE and BridgeLink), and a newer, separate engine (Perfuse).
- The first three are close relatives on the same software platform (Java) and share the same channel format, so moving between them is easy.
- Perfuse is a fresh start on a different platform, is a single file to install, and can read and write the others' channels — so it works alongside any of them.
- This guide lays out what each one is, in plain words, so you can choose with the facts in front of you. We are fair to all four, and we state Perfuse's real weakness: it is new, version 0.1.9, with no long production track record yet.

## First, what an integration engine is

An **integration engine** (also called an **interface engine**) is the software that moves and translates messages between the many systems inside a hospital. The lab, the imaging department, the pharmacy, the billing office and the patient record each speak their own dialect, and the engine sits in the middle translating and delivering so none of it gets lost.

> In plain words: the engine is the hospital's switchboard and translator. When a result or an admission needs to travel from one system to another, the engine carries it and makes both sides understand.

Each connection the engine runs is called a **channel**: it says where a message comes in, what to do with it, and where it goes out. A medium-sized hospital often runs dozens of channels.

## Why there are suddenly four choices

For years, Mirth Connect was the popular free choice. In March 2025, NextGen Healthcare (its owner) made it commercial from version 4.6, so a paid licence is now required. The last free, open version is **4.5.2**. That one change is why hospitals are weighing up alternatives, and why the community produced free continuations of the old code.

## The four options

**Mirth Connect 4.5.2.** The last free, open release of Mirth Connect. It runs on **Java** (a common software platform), it is widely known, and a great deal of existing hospital integration work is built on it.

**Open Integration Engine (OIE).** A community continuation of Mirth 4.5.2, carried on in the open under the same licence, with its own releases from 4.6.0 onward. The project is moving to the Eclipse Foundation, a well-known non-profit that stewards open-source projects.

**BridgeLink.** Another continuation of the same Mirth 4.5.2 code, from Innovar Healthcare, with its own line of releases.

**Perfuse.** A new engine, written from scratch on a different platform (a language called Go) and released free under the **Apache 2.0** licence. It reads and writes Mirth's channel format, so it can work alongside all three of the above, and it adds a modern set of standards and safety features on top.

## At a glance

![Four engines compared across runs-on, install, licence, channel format and built-in standards, with Perfuse highlighted](docs/assets/articles/engines-compared-side-by-side.svg)

| | Mirth 4.5.2 | OIE | BridgeLink | Perfuse |
|---|---|---|---|---|
| Origin | NextGen Healthcare | community fork of 4.5.2 | fork of 4.5.2 | a new engine |
| Runs on | Java | Java | Java | Go |
| What you install | application + Java platform | application + Java platform | application + Java platform | one file |
| Licence | MPL 2.0 | MPL 2.0 | from Mirth 4.5.2 | Apache 2.0 |
| Channels | Mirth XML | Mirth XML | Mirth XML | plain files; reads and writes Mirth XML |

> In plain words: the first three are the same kind of software with the same way of working; the fourth is a different design that happens to speak the same channel language.

## What the Mirth family shares

Mirth 4.5.2, OIE and BridgeLink all come from one piece of code, so they have a great deal in common: the same idea of a channel (where messages come in, how they are transformed, and where they go out), the same scripting style, the same management program, and the same channel file format. Moving between the three is straightforward, and the skills your team already has carry straight over.

![A family tree: Mirth 4.5.2 at the root, OIE and BridgeLink as community forks, and Perfuse to the side as a separate new engine that reads and writes Mirth channels](docs/assets/articles/engines-compared-family-tree.svg)

That family relationship is worth picturing. OIE and BridgeLink **descend** from Mirth 4.5.2 — they are the same code, continued. Perfuse is drawn off to the side: it shares no code with the family, but it **speaks their channel language** in both directions. So it is not a cousin; it is a different engine that can read and write the family's files.

## What Perfuse adds

Perfuse keeps the parts of Mirth that teams rely on, and builds a new foundation under them.

- **Your Mirth work comes with you.** Your Mirth scripts run unchanged. One command converts a channel, a group, a shared library or a whole server backup. This is tested against real copies of Mirth 4.5.2, OIE 4.5.2 and 4.6.0, and BridgeLink 26.9.0.
- **And it goes back.** Channels built in Perfuse export as Mirth channel files that Mirth, OIE and BridgeLink all accept, so Perfuse can sit side by side with any of them while you decide.
- **One file, far lighter.** 31 MB of memory at rest against Mirth 4.5.2's 383 MB, measured on the same machine — about twelve times less. There is no Java platform to install, patch or tune.
- **Modern standards built in.** FHIR (the modern web-friendly health data standard) with its own server, the health-insurer connections required by the CMS-0057 rule, national record exchange (TEFCA), public-health reporting, and more — included, not bought as add-ons.
- **Every format.** HL7 v2 (hospital messaging), FHIR, DICOM (imaging), X12 (insurance and billing), and clinical documents, all in one engine.
- **Safer change.** Shadow mode tests a change on real traffic while delivering nothing; feed contracts warn you when a sender quietly changes something; and a compare tool proves two engines agree.
- **Operations you can see.** A live dashboard, a map of all your feeds, alerts, and the ability to find a patient's messages across every format at once.
- **Everything in the browser**, and every channel is also a plain file you can keep in version control.

## A worked example

Picture a **300-bed hospital running 40 Mirth channels**, deciding what to do about the licence. Two reasonable paths:

- If the team loves the Mirth way and wants the least change, they move to **OIE** or **BridgeLink**. The screens, the scripts and the habits all carry over, and it stays free and open.
- If the team wants a modern engine and is willing to try something new carefully, they trial **Perfuse**. They convert the 40 channels, run Perfuse beside Mirth on real traffic (delivering nothing), compare the output, and switch over one channel at a time — keeping the ability to export back to Mirth throughout.

Neither path is wrong. The right one depends on whether the goal is "keep what we have, free" or "move to newer ground without leaving our Mirth work behind".

## Choosing, in plain terms

- **Want to keep the Mirth model you know, free and open?** OIE and BridgeLink continue it. They are the closest to what you already run and the most proven in production of the free options.
- **Want a modern engine without throwing away your Mirth work?** Perfuse runs your channels, adds the newer standards that regulators now expect, and lets you move one channel at a time with proof at every step — and go back if you want to.

## Being fair, including Perfuse's real weakness

Each option deserves a fair hearing:

- **Mirth 4.5.2** is the exact software most teams know, but it is frozen: no more fixes, including security fixes.
- **OIE and BridgeLink** keep the free, open line alive and are the natural home for Mirth veterans. They are the most battle-tested of the free choices.
- **Perfuse** brings a lighter footprint and a lot of modern capability, but — stated plainly — **it is new, version 0.1.9, and has no long production track record yet.** The engine, queue, connections and web screen are complete and tested, and the parts that talk to other software are checked against the real thing (real Mirth, OIE and BridgeLink; a real FHIR server; a real imaging server; real databases; and the regulator's own test kits). That is strong evidence, but it is not years of running a busy hospital. Perfuse's shadow mode exists so you can rely on evidence from your own traffic rather than any promise.

## Common questions

**Are OIE and BridgeLink really the same as Mirth?** They are continuations of the same code, so they are very close. Small differences appear over time as each project adds its own fixes.

**Can Perfuse run at the same time as my current engine?** Yes. Because it reads and writes Mirth channels, it can run alongside Mirth, OIE or BridgeLink while you compare and move gradually.

**Do I have to pick one forever?** No. Perfuse exports back to Mirth format, and the three Java engines interchange channels among themselves, so you keep your options open.

**Which is the safest choice for a cautious hospital?** The most proven free options are OIE and BridgeLink. If you want Perfuse's modern features, the cautious route is to trial it in shadow mode first.

## Glossary

- **Integration engine / interface engine** — software that moves and translates messages between systems.
- **Channel** — one message connection: where messages come in, how they change, where they go out.
- **Fork** — a copy of an existing project's code, taken forward separately.
- **Java / Go** — software platforms (programming languages) that an engine is built on.
- **MPL 2.0 / Apache 2.0** — open-source licences that allow free use, including in business.
- **Mirth XML** — the file format Mirth and its forks use to store channels.
- **HL7 v2** — the long-standing hospital messaging format.
- **FHIR** — a modern, web-friendly health data standard.
- **DICOM** — the standard for medical images and the systems that store them.
- **X12** — the message format used for US insurance and billing.
- **CMS-0057** — a US rule requiring health insurers to offer certain data connections from 1 January 2027.
- **TEFCA** — a US framework for exchanging records between organisations nationally.
- **Shadow mode** — running a new version beside the live one, on real traffic, delivering nothing, to compare.
- **Feed contract** — a declared description of what a feed should contain, used to catch quiet changes.
- **Version control** — a system that records every change to a file over time.

## How Perfuse does it

Perfuse is a different design that speaks the Mirth family's language. It reads and writes their channel files, runs Mirth scripts unchanged, installs as one file with no separate platform underneath, and adds FHIR, DICOM, X12, document handling, the CMS-0057 connections, shadow mode and feed contracts in the box. That lets a team move off the Mirth family at its own pace, prove each step, and keep the exit open.

## Try Perfuse in two minutes

1. Download it from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse explain` on one of your Mirth channel exports.
3. Run `perfuse serve` and open http://127.0.0.1:8080.

Related: [the Mirth 4.6 licence change](https://perfuse.health/mirth-licence-change/) and [how to migrate Mirth Connect channels](https://perfuse.health/migrate-mirth-channels/).

Mirth and Mirth Connect are trademarks of their respective owners, used here only to describe compatibility. Open Integration Engine and BridgeLink are the names of their respective projects.

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
