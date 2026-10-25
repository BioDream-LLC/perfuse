# How to migrate Mirth Connect channels, step by step

![Six numbered steps rising towards a finished migration](docs/assets/articles/hero-migrate-mirth.svg)

## In one minute

- Moving off Mirth Connect is usually imagined as a full rewrite with a scary cut-over weekend. It does not have to be that.
- With Perfuse, a free integration engine, your channels convert automatically, your scripts run unchanged, and you prove the result on your own message traffic before you switch anything.
- The job has six plain steps: export from Mirth, see what you have, translate, check and test, prove it on real traffic, and switch over one channel at a time.
- If you ever change your mind, Perfuse can export a channel back to a Mirth file, so you are never locked in.
- The same steps work for Mirth Connect, the Open Integration Engine (OIE) and BridgeLink. Honest caveat: Perfuse is new (still at version 0.1) with no long production track record, which is exactly why the "prove it" step matters.

## Who this is for, and what you need to know first

This guide is written for the person who looks after a hospital's message connections — often called an **integration analyst** or **integration engineer** — and who may not be a programmer. You do not need to write code to follow it.

A few words that come up throughout:

> In plain words: a **channel** is one message connection. It says where messages come in, what to do with them along the way, and where they go out. A hospital typically has dozens of channels — one for lab results, one for admissions, one for billing files, and so on.

Mirth Connect, OIE and BridgeLink are **integration engines**: software that passes and translates messages between hospital systems. Mirth stopped being free at version 4.6 in March 2025; OIE and BridgeLink are free community continuations of the last free Mirth, and **Perfuse** is a newer, separate free engine that can read and run Mirth's work. Everything below works the same for all three Mirth-family engines.

## A worked example to keep in mind

Throughout, picture a **300-bed hospital running 40 Mirth channels**. The analyst has a renewal notice on her desk and has been asked to see whether the hospital can move to a free engine without a disruptive project. We will follow her through the six steps.

## Step 1: Export from Mirth

![Six migration steps from Mirth to Perfuse: export, explain, translate, check and test, compare, switch](docs/assets/articles/mirth-migration-steps.svg)

Inside the Mirth Administrator (the program you already use to manage Mirth), you export what you want to move. Any of these works:

- a single channel (a `.xml` file)
- a channel group (several channels bundled together)
- a code template library (shared bits of script your channels use)
- a **whole server backup**, which carries every channel, group and shared library at once

For a 40-channel hospital, the whole server backup is usually the simplest: one file holds everything. Put the exports in one folder, for example `mirth-export/`.

## Step 2: See what you have

```sh
perfuse explain mirth-export/
```

`explain` reads every channel and tells you, **by name**, what converts directly, what converts with a note, and what needs a decision from you. You get the whole picture before you change a single thing.

The result is written as a fraction with its total — for example, "37 of 40 ready" — because "37 ready" on its own tells you nothing about how much is left. There is an important distinction in the report:

- A channel that **could not be read** usually means the export file itself was incomplete or truncated. That is a file problem, not a content problem.
- A channel that **could not be translated** means its content uses something without a direct equivalent — and that is listed by name so you know exactly what to look at.

For a closer look, the **Mirth migration** screen in Perfuse's web console shows the same report, plus a **lock-in audit** that lists any pieces of your channels that can only run on the vendor's own software.

## Step 3: Translate

```sh
perfuse translate -o channels/ mirth-export/
```

Each Mirth channel becomes a Perfuse channel. Here is what happens to the different parts.

![What converts directly, what is carried across as a script, and what is reported for a decision — nothing is dropped silently](docs/assets/articles/migrate-mirth-what-converts.svg)

- **The connections convert directly.** Where messages come in and go out — network connections (MLLP, TCP, HTTP), files, secure file transfer, databases, imaging links, email — are mapped to Perfuse's equivalents.
- **Your scripts run unchanged.** The little programs inside your channels keep working as they are. You do not rewrite them.
- **Shared libraries come across** as shared script files, included by exactly the channels that used them in Mirth — so a shared function such as "format this medical record number" still resolves.
- **A few things are reported for a decision rather than guessed.** If a channel uses something with no true equivalent, Perfuse lists it by name rather than approximating it. A translation that looked close but was subtly wrong would be worse than one flagged for a human, because it would run.

> In plain words: nothing is dropped silently. Every part of every channel is either converted, carried across as a script, or listed for you to decide on.

If you want the conversion to stop and flag anything that needs attention (useful in an automated setup), add `-strict`. For a machine-readable report, add `-json`.

## Step 4: Check and test

```sh
perfuse check channels/
perfuse test channels/
```

`check` validates every channel and prints, in plain words, what it does. `test` runs the tests you write against a channel: you give it a message going in and the message you expect to come out, and it confirms they match. If you do not yet have sample messages, Perfuse can make realistic synthetic ones, or turn real messages into a safe, de-identified test set (with the patient details removed).

## Step 5: Prove it on real traffic

This is the step that makes the switch safe, and it is worth doing carefully.

![A sender sends a copy to both the live Mirth, which keeps delivering, and Perfuse, whose only destination is a folder so it reaches nobody; the two outputs are then compared](docs/assets/articles/migrate-mirth-parallel-run.svg)

The idea is simple. You run both engines on the same real messages, but only Mirth actually delivers anything. Perfuse's **only** destination is a folder on disk, so it physically cannot reach a downstream system even if it were misconfigured. Each engine writes its output to a folder, and then you compare the two:

```sh
perfuse compare -left-name Mirth -right-name Perfuse mirth-out/ perfuse-out/
```

Messages are paired up by their control number (a unique ID each message carries), and differences are **grouped by cause**. That grouping matters: if three thousand messages differ for the same single reason, you see **one** finding, not three thousand. Differences you expect — like timestamps, which naturally change on every message — can be ignored.

The report deliberately keeps patient content out unless you ask for it, so it is safe to paste into a ticket or a change request. And nobody has to approve a trial that delivers to no one.

> In plain words: you let Perfuse watch the real traffic for a while without touching anything, then you read a short list of exactly where it would have produced something different from Mirth. Each line is a decision, not a surprise.

Inside Perfuse, **shadow mode** does something similar for later, once you have moved: it runs a new version of a channel beside the live Perfuse one, on real traffic, delivering nothing, and shows you field by field where the two disagree.

## Step 6: Switch over, one channel at a time

Start Perfuse with your channels and open the web console:

```sh
perfuse serve -channels channels/
```

Then move feeds **one at a time**: point the first sender at Perfuse, watch it on the dashboard, confirm it is healthy, and move on to the next. There is no need to switch all 40 at once, and good reason not to.

Perfuse keeps every message in a **durable on-disk queue** — a holding line that keeps messages in order and survives a restart — and it only tells a sender "received" after the message has actually been delivered. So nothing is lost along the way.

> In plain words: the queue is like a numbered ticket line that is written down on paper rather than kept in someone's head. If the power blips, the line is still there and still in order when the lights come back.

## And if you ever need to go back

Perfuse exports as well as imports. Any channel built or changed in Perfuse can be written out as a Mirth channel file, and **Mirth, OIE and BridgeLink all accept it**. You are never locked in.

One honest note: Mirth's file format has nowhere to store some of Perfuse's newer features (such as feed contracts or shadow comparisons), so an exported channel does less than the original. The connections and their addresses convert; the newer logic does not. Perfuse lists exactly what will be lost, by name, before it writes the file.

## Why teams make the move

![Perfuse uses 31 MB of memory at rest against Mirth Connect 4.5.2's 383 MB](docs/assets/articles/memory-at-rest.svg)

- **One file, no Java platform.** 31 MB of memory at rest against Mirth's 383 MB on the same machine, and nothing to install underneath it.
- **Everything in the browser**, and every channel is also a plain file you can keep in version control (a system that records every change).
- **More than Mirth had**: shadow mode, feed contracts that catch upstream changes, a dashboard, alerts, and the ability to find a patient's messages across every format at once.
- **Modern standards built in**: FHIR, US Core, Da Vinci, the CMS-0057 payer connections, public-health reporting, and more.
- **Free and open** under Apache 2.0, with no licence keys and no paid tiers.

## Being fair about the trade-offs

Mirth, OIE and BridgeLink are older and more proven in production than Perfuse. Perfuse is new — still at version 0.1 — and has not yet run a busy hospital for years. The engine, queue, connections and web screen are complete and tested, and the parts that talk to other software are verified against the real thing (real Mirth, OIE and BridgeLink; a real FHIR server; a real imaging server; real databases). But that is not the same as a long track record. The "prove it" step in Step 5, and shadow mode later, exist precisely so you can rely on evidence from your own traffic rather than anyone's promises.

## Common questions

**Will my scripts really run without changes?** Yes. Run `perfuse explain` first and it will name anything that needs attention.

**How long does a 40-channel migration take?** It depends on how many hand-written scripts you have and how long you want to watch the "prove it" step. A calm migration over several weeks, one channel at a time, is normal and sensible.

**What if a channel uses something Perfuse cannot convert?** It is reported by name, never silently approximated. You then decide how to handle it.

**Can I run Perfuse and Mirth side by side during the move?** Yes — that is exactly what Step 5 is. Perfuse delivers to nobody while you compare.

**Is my patient data safe during the trial?** The comparison report carries counts, not patient content, unless you explicitly ask for it. And the trial engine physically cannot deliver anywhere except a folder you choose.

## Glossary

- **Channel** — one message connection: where messages come in, how they are transformed, and where they go out.
- **Integration engine / interface engine** — software that moves and translates messages between systems.
- **Mirth Administrator** — the program used to manage a Mirth server.
- **Server backup** — a single export file holding every channel, group and shared library.
- **Code template library** — shared bits of script used by more than one channel.
- **MLLP / TCP / HTTP** — common ways systems send messages over a network.
- **Control number** — a unique ID each message carries, used to pair messages when comparing.
- **Durable on-disk queue** — a holding line for messages that keeps order and survives a restart.
- **Shadow mode** — running a new version beside the live one, on real traffic, delivering nothing, to compare.
- **Lock-in audit** — a report of the pieces of a channel that can only run on one vendor's software.
- **De-identify** — remove patient details so a message can be shared safely for testing.
- **OIE / BridgeLink** — free community continuations of the last open-source Mirth.
- **Apache 2.0** — a permissive open-source licence allowing free use, including in business.

## How Perfuse does it

Perfuse reads every Mirth export, runs your scripts as they are, tells you in plain words what converts and what needs a decision, lets you prove the result against Mirth on your own traffic before you switch, moves you across one channel at a time with a queue that never loses a message, and exports back to Mirth whenever you want. The engine is a single file with the web console inside it.

## Start now

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse explain` on one of your channel exports.
3. Read the [manual](https://perfuse.health/manual/) for every connection type and option.

Related: [the Mirth 4.6 licence change and your options](https://perfuse.health/mirth-licence-change/).

Mirth and Mirth Connect are trademarks of their respective owners, used here only to describe compatibility.

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
