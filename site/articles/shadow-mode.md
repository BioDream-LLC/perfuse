# Shadow mode: how to change a live interface without guessing

![A live path that delivers and a dashed shadow path that only compares](docs/assets/articles/hero-shadow.svg)

## In one minute

- Changing a working hospital interface is nerve-wracking: the one message that breaks is always the odd one nobody thought to test.
- Shadow mode runs the new version of a connection beside the live one, on the real message traffic, delivering nothing, and shows you exactly where the two would differ.
- It is safe to point at a live, busy feed because the shadow has no way to deliver and cannot affect the live channel.
- You read the result field by field — a short list of real changes, each one click from the message that caused it — rather than a single pass-or-fail.
- Perfuse has shadow mode built into every channel. Honest caveat: Perfuse is new (version 0.1.9), and shadow mode is itself part of how you check it against what you already run.

## Who this is for

This guide is for the person who looks after a hospital's message connections and has to change one from time to time — a new field here, a code translation there, a tweak to which messages get through. You do not need to be a programmer to follow it.

A couple of words first:

> In plain words: an **interface** (or **channel**) is one message connection — where messages come in, what happens to them, and where they go out. A **field** is one small piece inside a message, such as the patient's family name or a result's unit of measure.

## The problem shadow mode solves

Every integration team knows the feeling. A transformation needs to change. The change looks right. The tests pass. And then, days later, something breaks — because the feed carries messages nobody ever catalogued: the patient with two identifiers, the result with an empty units field, the discharge message that somehow arrives before its matching admission.

The case that breaks is almost always the one nobody thought of. That is not a failure of care; it is the nature of real traffic. You cannot write a test for a message shape you have never seen.

![Tests cover a few known shapes; the real feed carries many more, including odd ones nobody catalogued, which shadow mode checks against](docs/assets/articles/shadow-mode-why-tests-fall-short.svg)

> In plain words: tests prove the cases somebody imagined. The real feed is full of cases nobody imagined. Shadow mode checks your change against the real feed, not just the handful you dreamed up.

## The idea

Run the new version of a connection **beside the live one, on the real traffic**, with **nothing delivered**. Compare the two outputs for every message. Read exactly where they differ, field by field, before the new version ever touches a receiving system.

That is the whole idea, and it answers the question tests cannot: what will this change actually do to the messages that actually arrive?

## How Perfuse does it

![A message reaches the live channel, which delivers, and a candidate channel with no destinations, compared field by field](docs/assets/articles/shadow-mode.svg)

Perfuse is a free integration engine with shadow mode built into every channel. You take a copy of the live channel, make your change in the copy (the "candidate"), and tell the live channel to shadow it. In Perfuse's settings that looks like this — but note that in the web console you simply pick the candidate from a list and press start, so you never type any of this by hand:

```yaml
# in the live channel
shadow:
  channel: ./adt-candidate.yaml
  ignore: [MSH-7, MSH-10]       # things that differ on every message
  sample: 1
  max_differences: 100
```

### It is safe to point at production, by design

- **The shadow cannot deliver.** The candidate channel is built with no destinations at all — there is literally nothing for it to send with. Even if someone misconfigured it, it has nowhere to go.
- **The shadow cannot affect the live channel.** It runs *after* the live message has already been delivered and acknowledged, with its own time limit. If the candidate hits an error, that error is recorded against the candidate while the live message carries on untouched.

> In plain words: the shadow is a passenger watching the road, not a second driver. It sees everything the live channel sees and touches none of the controls.

### It reports what matters

![A shadow report: a difference rate, a field-level difference counted as one mapping decision, a filter disagreement counted on its own, and expected differences ignored](docs/assets/articles/shadow-mode-reading-the-report.svg)

- **Field by field, at the smallest level.** If a change affects the first part of the patient-name field, the report names exactly that piece, and shows what each version produced.
- **"Dropped vs kept" counted separately.** When one version lets a message through and the other filters it out, that decides whether a receiving system hears about a patient at all — so it is shown on its own, not buried among value changes.
- **A difference rate**, on the channel's Shadow tab, with each differing message one click away.
- **Expected differences ignored.** Things like timestamps and message control numbers change on every single message. Telling the shadow to ignore them (the common pair is `MSH-7` and `MSH-10`) means the report shows only real changes, not noise. Without this, a channel that stamps a timestamp would report a 100% difference rate and tell you nothing.
- **Honest verdicts.** When everything matches, Perfuse says plainly that the candidate changed nothing on the traffic seen so far. That is evidence, stated as evidence.

> In plain words: you do not get a green tick or a red cross. You get a short, specific list — "this field changed on 12 messages, this message was dropped that used to get through" — and each line is something you can decide about.

## A worked example

Picture that **300-bed hospital running 40 Mirth channels**, now moved to Perfuse. An analyst needs to change the admissions channel: a downstream system has asked for the patient's family name in a slightly different form, and a particular message type should no longer be forwarded.

She copies the live admissions channel to a candidate, makes the two changes, and switches shadow mode on. Over the next few days, the candidate quietly watches every real admission message and delivers none of them. On the Shadow tab she sees:

- `PID-5.1` (the family-name piece) differs on 12 messages — live produced "SMITH", candidate produced "Smith". That is exactly the change she intended: one decision, confirmed.
- One **filter disagreement**: the live channel kept a message the candidate dropped. She checks it, and yes — it is the message type the downstream system no longer wants. Intended.
- Everything else matches.

She has now seen her change play out against real traffic, including the message types that only appear occasionally, without any risk to a receiving system. When she is satisfied, she makes the candidate live.

## The rest of the safety net

Shadow mode is one tool among several Perfuse gives you for changing things safely:

- **A test runner** checks the cases you write: a message in, the expected message out.
- **A compare tool** proves two whole engines agree over the same traffic — the way a move from Mirth is checked.
- **Feed contracts** catch the day a sender quietly changes what it sends you.
- **Channel history** shows what changed and when, because every channel is a plain file kept under version control.

Shadow mode is part of a complete engine: it handles HL7 v2, FHIR, X12, DICOM and clinical documents over many connection types, with a durable message queue, monitoring and alerts, all in one file with nothing else to install.

## Three things to know before you trust the numbers

1. **Pick the candidate from the list, do not type its name.** A shadow names a channel file. If you type a name that does not exist, Perfuse treats the *live* channel as broken (it refuses a channel that points at something missing). Choosing from the list avoids this.
2. **If you lower the sample share, know the cost.** On a very busy feed you can watch a sample rather than every message. But sampling reduces the work and the confidence in the same proportion — and the message that would have shown the problem is often the rare one.
3. **You usually need to ignore some fields.** A channel that stamps a timestamp or a sequence number differs on every message, which would read as "100% different" and tell you nothing. Ignoring `MSH-7` and `MSH-10` is the common starting point.

## Being fair: what shadow mode is and is not

Shadow mode is strong evidence, but it is evidence about **the traffic it has seen**. If a rare message type has not arrived during the shadow period — a month-end batch, an unusual trigger event, a sender that behaves differently at midnight — then the shadow has not tested it yet. That is why the honest advice is to run a shadow over a period that includes the quiet and the busy times, including a weekend and a month end, rather than an afternoon.

And the broader caveat applies here too: Perfuse itself is new (version 0.1.9) with no long production track record. Shadow mode is part of how you make that acceptable — you never have to take anyone's word for a change, because you can watch it against your own real traffic first.

## Common questions

**Will the shadow slow down my live channel?** The shadow runs after the live message is delivered and has its own time limit, so a slow or failing candidate does not hold up live delivery.

**Can the shadow accidentally send a message somewhere?** No. The candidate is built with no destinations, so it has nothing to send with.

**What do I do with the differences it finds?** Each one is a decision. A single value pair repeated across many messages (like "SMITH" vs "Smith") is one mapping choice. A message dropped that used to get through is a filter choice. You confirm each is what you intended.

**How long should I run it?** Long enough to include the unusual traffic — ideally across a weekend and a month end, not just one afternoon.

**Does this only work for Perfuse-to-Perfuse changes?** Shadow mode compares a candidate Perfuse channel against a live Perfuse channel. To compare Perfuse against Mirth during a migration, you use the separate compare tool, which is covered in the migration guide.

## Glossary

- **Interface / channel** — one message connection: where messages come in, how they change, and where they go out.
- **Field** — one small piece inside a message, such as a patient's family name.
- **Candidate** — the new version of a channel you are testing in shadow mode.
- **Filter** — the rule that decides whether a message is forwarded or dropped.
- **Difference rate** — the share of messages where the candidate's output differs from the live one's.
- **MSH-7 / MSH-10** — the parts of an HL7 v2 message holding its timestamp and its control number; commonly ignored because they change every time.
- **Durable message queue** — a holding line for messages that keeps order and survives a restart.
- **Feed contract** — a declared description of what a feed should contain, used to catch quiet changes.
- **Version control** — a system that records every change to a file over time.
- **Apache 2.0** — a permissive open-source licence allowing free use, including in business.

## How Perfuse does it

Every Perfuse channel can run a candidate beside it, on real traffic, delivering nothing, with the candidate unable to reach any receiving system and unable to disturb the live channel. The result is a field-by-field list of real differences — value changes counted as mapping decisions, dropped-versus-kept counted on its own — with expected noise ignored and an honest "nothing changed" when that is the truth. It is built into the engine, not a separate product.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Copy a live channel to `candidate.yaml`, make your change, and switch shadow mode on from the live channel's Shadow tab.
3. Watch the Shadow tab over a period that includes busy and quiet times. Read the [manual](https://perfuse.health/manual/) for every option.

Related: [feed contracts](https://perfuse.health/feed-contracts/) and [how to migrate Mirth Connect channels](https://perfuse.health/migrate-mirth-channels/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
