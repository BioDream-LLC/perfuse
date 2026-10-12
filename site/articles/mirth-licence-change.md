# Mirth Connect 4.6 licence change: your options, and the fastest way forward

![Four paths after the Mirth Connect 4.6 licence change, with moving forward highlighted](docs/assets/articles/hero-mirth-licence.svg)

## In one minute

- In March 2025 the company that owns Mirth Connect, an integration engine many hospitals use to move patient data between systems, started charging for it from version 4.6 onward. The last free version is 4.5.2.
- You have four realistic choices: pay for 4.6, freeze on 4.5.2, move to a free community copy of the old code (OIE or BridgeLink), or move to a newer free engine that can read your existing Mirth work.
- This guide explains each choice in plain words, with a worked example, and then describes Perfuse, a free engine that reads your Mirth channels and runs your Mirth scripts unchanged.
- You do not have to rewrite anything, and you do not have to switch everything at once. You can move one connection at a time and prove each one on your own data first.
- Honest caveat up front: Perfuse is new (version 0.1.9) and has no long production track record yet. The other options are older and more battle-tested. We say where that matters below.

## What actually happened, and why it matters

An **integration engine** (sometimes called an **interface engine**) is a piece of software that sits in the middle of a hospital and passes messages between systems that were never designed to talk to each other. The lab system speaks one language, the imaging department another, the billing system a third, and the patient record wants a fourth. The engine translates between them and makes sure nothing gets lost on the way. Mirth Connect has been one of the most popular of these for years, largely because it was free and open.

> In plain words: an integration engine is the hospital's translator and postal service rolled into one. When a lab result needs to reach the patient's chart, the engine carries it and translates it so both sides understand.

In March 2025, NextGen Healthcare (the company that owns Mirth Connect) changed the rules. From version 4.6 onward, you need a paid licence to use it. The last version released under the old free, open-source terms is **4.5.2**.

Why does this matter so much? Because a very large share of the message connections (called **channels**) running in hospitals today were built on Mirth. Those channels work. The people who built and maintain them did not ask for this change, and rewriting all of that work is not a weekend job. Paying a yearly fee to keep getting fixes for software you already installed is a different deal from the one you originally signed up for.

So the question lands on the desk of whoever runs integration: **what now?**

## Your four options, laid out

![Four options after the Mirth 4.6 licence change, from buying the licence to moving to a modern engine that reads Mirth channels](docs/assets/articles/mirth-licence-decision-tree.svg)

### Option 1: Buy the Mirth 4.6 licence

You keep everything exactly as it is and pay the fee. Nothing in your setup changes. The trade-off is a recurring cost that was not there before, and a dependence on one vendor's pricing for software that runs core parts of your hospital.

### Option 2: Stay on Mirth 4.5.2

Version 4.5.2 does not stop working the day 4.6 arrives. You can keep running it. The catch is that it is the **last** free version, so no more improvements are coming from the original makers — and, importantly, **no more security fixes**. Over time, running software that will never be patched again becomes a risk your security team will raise.

### Option 3: Move to a free community copy (OIE or BridgeLink)

When Mirth went commercial, the community kept the free line alive. Two projects took the 4.5.2 code and continued it in the open:

- **Open Integration Engine (OIE)** — a community continuation, moving to the Eclipse Foundation (a well-known non-profit that looks after open-source projects).
- **BridgeLink** — another continuation of the same code, from Innovar Healthcare.

Both run on **Java** (a widely used programming platform), use the same channel model Mirth used, and keep the familiar tools. If your team is comfortable with Mirth and wants to stay close to it, these keep you there without the fee.

### Option 4: Move to a modern engine that already reads Mirth

This is the option this guide focuses on, because it is the one that moves you forward rather than sideways. **Perfuse** is a free engine that reads your Mirth channels, runs your Mirth scripts unchanged, and adds a modern set of features on top. Crucially, you can try it one channel at a time and prove each one works before you rely on it — and you can export a channel back to a Mirth file if you ever change your mind.

Options 1 to 3 keep you roughly where you are: same place, different bill, or a frozen version. Option 4 is the one that gets you somewhere new, and with Perfuse the move is gradual and reversible.

## Meet Perfuse

Perfuse is a free, open integration engine. It moves clinical data between systems in all the formats a health system meets day to day: **HL7 v2** (the long-standing hospital messaging format), **FHIR** (the modern web-friendly health data standard), **DICOM** (medical imaging), **X12** (insurance and billing), and clinical documents (**C-CDA** and **CDA**). It connects over the usual channels a hospital uses — network connections, files, secure file transfer, databases, imaging links and cloud storage.

And it is **one file**. You download it, you run it, and you open your web browser to use it. There is nothing else to install.

> In plain words: where the old engine needs a separate software platform installed underneath it before it will even start, Perfuse is a single program you can copy onto a machine and double-click.

### One file, far lighter

![Perfuse uses 31 MB of memory at rest against Mirth Connect 4.5.2's 383 MB](docs/assets/articles/memory-at-rest.svg)

Here is a simple, measured comparison. "Memory at rest" means how much of the computer's working memory the software holds while sitting idle, doing nothing. Lower is better.

| | Perfuse | Mirth Connect 4.5.2 |
|---|---|---|
| Memory at rest | 31 MB | 383 MB |
| What you install | one file (about 20 MB to download) | a 254 MB application plus a Java platform |
| Platform to install first | none | OpenJDK 17 (a Java runtime) |
| Startup | immediate | the Java platform has to boot and warm up |

Both figures were measured on the same machine, both idle. Perfuse uses about **twelve times less** memory at rest. There is no Java platform to install, patch or tune, no separate server to run, and no database to set up first. It runs on Windows, macOS and Linux. On Windows you simply double-click it.

## Your Mirth channels, running without a rewrite

Perfuse was built for this exact migration from its first day.

- **Your Mirth scripts run unchanged.** The little programs you wrote inside your channels (in a language called JavaScript, in Mirth's particular style) keep working as they are. You do not rewrite them.
- **`perfuse explain`** reads a file exported from Mirth and tells you, channel by channel and by name, exactly what will convert cleanly, what converts with a note, and what needs a decision — *before* you commit to anything.
- **`perfuse translate`** does the conversion: it turns a single channel, a group of channels, a shared library of code, or a **whole server backup** into Perfuse channels in one step.
- **It works for every flavour of Mirth.** Perfuse is tested against real copies of Mirth Connect 4.5.2, Open Integration Engine 4.5.2 and 4.6.0, and BridgeLink 26.9.0.
- **And you can export back.** A channel built in Perfuse can be written out as a Mirth channel file that Mirth, OIE and BridgeLink all accept. You can move in both directions, so trying Perfuse carries no lock-in.

A typical first look takes three commands:

```sh
perfuse explain mirth-backup.xml        # what converts, by name
perfuse translate -o channels/ mirth-backup.xml
perfuse serve -channels channels/       # open http://127.0.0.1:8080
```

## A worked example

Picture a **300-bed hospital running 40 Mirth channels** — lab results coming in, admission and discharge messages flowing to the patient record, orders going out to departments, and a nightly billing file. The integration analyst, who is not a programmer by training, has been told the Mirth licence renewal is due and asked to look at the alternatives.

![An example migration timeline over six weeks for a 300-bed hospital with 40 channels, one step at a time, every step reversible](docs/assets/articles/mirth-licence-migration-timeline.svg)

Here is how that might go over about six weeks, with no "big bang" cut-over weekend:

1. **Week 1 — Look.** She exports a backup from Mirth and runs `perfuse explain` on it. In a few seconds she has a plain-language list: 37 of the 40 channels convert cleanly, 2 convert with a note about a hand-written script, and 1 uses a feature that needs a decision. She now knows the shape of the job before changing anything.
2. **Week 2 — Translate.** `perfuse translate` turns all 40 into Perfuse channels. The scripts come across and run as they are.
3. **Week 3 — Check and test.** She runs the channels through Perfuse's own checks and feeds them sample messages to confirm the output looks right.
4. **Weeks 4–5 — Prove it.** This is the step that makes everyone comfortable. She runs Perfuse *beside* the live Mirth, on the real message traffic, with Perfuse delivering to nobody. Perfuse then compares its own output against Mirth's, message by message, and shows exactly where (if anywhere) the two differ.
5. **Week 6 — Switch over, one at a time.** She points the first sender at Perfuse, watches it on the dashboard, and moves on to the next. Never all at once.

If anything ever looks wrong, she can export the channel back to a Mirth file and return. Nothing about this is irreversible.

## What you gain that Mirth never had

Moving forward is only worth it if the new ground is better. A few of the things Perfuse adds:

- **Shadow mode** — change a working connection and see the result on real traffic before it goes live. (There is a whole separate guide on this.)
- **Feed contracts** — Perfuse learns what a feed should contain and warns you the moment a sender quietly changes something. Silent upstream changes are one of the most common causes of interface trouble.
- **FHIR built in** — Perfuse can turn your old HL7 v2 feeds into modern FHIR data, and it includes a FHIR server, so you are ready for the newer standards without buying a separate product.
- **CMS-0057 and Da Vinci** — the health insurer rules coming into force on 1 January 2027 are built in, which matters if you are a payer or work closely with one.
- **Every format in one place** — imaging (DICOM), insurance and billing (X12), clinical documents (CDA), and public-health reporting, all handled by the same engine.
- **Enterprise sign-on** — it connects to your organisation's existing login system (via standards such as SAML and OpenID Connect), tested against real Keycloak and Microsoft Entra.
- **Everything in the browser** — anything a settings file can do, the web screen can do too. There is no separate program to deploy.

## Proven against real software

A fair question about anything new is: has it really been tested? Perfuse is checked against the actual systems it talks to, not imitations of them — real copies of Mirth, OIE and BridgeLink; a real FHIR server (HAPI); the official HL7 validator; a real imaging server (Orthanc); real databases; and the test kits that the US health-IT regulator uses (Inferno). The honest limit, stated plainly, is the one below.

## Being fair: where each option is strong, and Perfuse's real weakness

Let us not oversell. Each option has a genuine case:

- **Buying 4.6** keeps you on the exact software you know, with vendor support, if the fee is acceptable.
- **Staying on 4.5.2** is fine for the short term, but it will never be patched again, which eventually becomes a security problem.
- **OIE and BridgeLink** are the natural home for teams who love the Mirth way and want to keep it, free and open. They are closest to what you already run.

And Perfuse's real weakness, stated plainly: **it is new, and it has no long production track record yet.** The current version is 0.1.9. The engine, the message queue, the connections and the web screen are complete and tested, and the parts that talk to other software are checked against that software — but that is not the same as years of running a busy hospital's Monday morning. This is exactly why shadow mode exists: so you never have to take anyone's word for it. You run Perfuse beside what you have, on real messages, delivering nothing, and read the differences yourself.

## Common questions

**Do I have to switch everything at once?** No. You move one channel at a time and prove each one before you rely on it.

**Will my Mirth scripts work?** Yes, they run unchanged. Run `perfuse explain` first and it will tell you by name if anything needs attention.

**Can I go back to Mirth if I change my mind?** Yes. A channel built in Perfuse can be exported as a Mirth channel file that a real Mirth server accepts.

**Is it really free?** Yes. Perfuse is under the Apache 2.0 licence: free to use and run in production, commercially or not, with no licence keys and no paid tiers hidden inside the product.

**Do I need Java or a database?** No. It is one file, with the web screen built in. It uses a small built-in database by default, and can use PostgreSQL if you point it at one.

## Glossary

- **Channel** — one message connection: where messages come in, how they are transformed, and where they go out.
- **Integration engine / interface engine** — software that moves and translates messages between hospital systems.
- **HL7 v2** — the long-standing hospital messaging format for things like admissions and lab results.
- **FHIR** — a modern, web-friendly health data standard (said "fire").
- **DICOM** — the standard for medical images and the systems that store them.
- **X12** — the message format used for US insurance and billing (claims, eligibility, remittances).
- **C-CDA / CDA** — formats for clinical documents such as discharge summaries.
- **Java / JVM / OpenJDK** — a software platform some engines must run on; a runtime you install first.
- **OIE (Open Integration Engine)** — a free community continuation of Mirth 4.5.2.
- **BridgeLink** — another free community continuation of Mirth 4.5.2.
- **CMS-0057** — a US rule requiring health insurers to offer certain data connections from 1 January 2027.
- **Da Vinci** — a set of agreed FHIR designs used for insurer-provider data exchange.
- **SAML / OpenID Connect** — standards for signing in using your organisation's existing login system.
- **Apache 2.0** — a permissive open-source licence allowing free use, including in business.
- **Shadow mode** — running a new version beside the live one, on real traffic, delivering nothing, to compare.

## How Perfuse does it

Perfuse treats this migration as the main event, not an afterthought. It reads every kind of Mirth export, runs your scripts as they are, tells you in plain words what will and will not convert, lets you prove the result on your own traffic before switching, moves you across one channel at a time, and lets you export back to Mirth at any point. The engine is one file with the web screen inside it, so there is no platform to stand up first.

## Get started in two minutes

1. Download Perfuse for your platform from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse serve` (on Windows, double-click `perfuse.exe`).
3. Open http://127.0.0.1:8080, sign in, and import your first Mirth channel.

Related: [How to migrate Mirth Connect channels, step by step](https://perfuse.health/migrate-mirth-channels/) and [open-source integration engines compared](https://perfuse.health/integration-engines-compared/).

Mirth and Mirth Connect are trademarks of their respective owners, used here only to describe compatibility.

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
