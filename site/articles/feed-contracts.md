# Feed contracts: catch upstream changes before they break an interface

![A contract with field checks, one flagged as a violation](docs/assets/articles/hero-contracts.svg)

## In one minute

- Hospital computer systems constantly send each other streams of messages — a stream is called a **feed**.
- The expensive problem is rarely building a feed. It is the day the sending system quietly **changes** something: a field disappears, a new code shows up, a value starts repeating.
- These changes are not errors. The messages still look valid, so nothing complains — until a receiving system falls over weeks later, and the hunt for the cause starts in the wrong place.
- A **feed contract** is a written promise about what a feed should contain. The engine checks reality against it and raises the alarm the day things drift — early, and pointing at the real cause.
- **Perfuse** builds a contract from your actual traffic, checks it on a schedule, and alerts you when the feed stops matching.

## What this is

Imagine two departments in a hospital that send each other a steady stream of messages all day: the lab sending results to the main patient-record system, say. That stream is called a **feed**. Setting one up is a project — connecting the systems, agreeing what each message means, testing it. But once it works, it tends to just keep working, often for years.

The trouble starts on the day the *sending* side changes. Maybe the lab's software gets an upgrade, and the upgrade happens to stop including a field that used to be there. Maybe a new kind of code appears in a field that only ever held three values before. Maybe something that was always a single value starts arriving several times over.

None of these is an error in any technical sense. The messages are still perfectly valid. Every system accepts them without complaint. And that is exactly the danger: **nobody notices.** The change slips through silently, and only much later does some downstream system — a billing report, a safety alert, a registry — quietly produce the wrong answer because a field it depended on went missing weeks ago.

A **feed contract** closes that gap. It is a written statement of what a feed is *supposed* to contain, kept beside the feed itself. The engine continuously compares what is really arriving against that promise, and tells you the day reality drifts away from it.

> In plain words: a feed contract is a promise about a data stream, plus a watchdog that barks the moment the stream stops keeping the promise.

## Why it exists

Here is the uncomfortable truth about hospital interfaces: most outages are not caused by the interface breaking. They are caused by the *other side* changing something without telling anyone. A vendor pushes an update. A configuration is tweaked. A new feature starts populating a field differently. The team running the interface in the middle finds out last, usually from an angry phone call about a report that is wrong.

![A timeline showing a silent change on day one becoming a downstream outage weeks later, which a contract would have caught at once](docs/assets/articles/feed-contracts-timeline.svg)

Follow that timeline. On **Day 1**, the sending system quietly drops a field. No error. For **weeks**, everything looks fine — the messages are valid, traffic flows, dashboards are green. **Later**, a downstream system that needed that field finally chokes, and the investigation begins. But it begins at the wrong end: people look at the system that fell over, not at the feed that changed weeks earlier. Hours or days are lost tracing a problem back to a cause that is long cold.

A feed contract rewrites that story. The alarm goes off on **Day 1**, the moment the field stops arriving — before any downstream system has a chance to misbehave, and pointing straight at the feed that changed. That is the whole reason feed contracts exist: to move the discovery from "weeks later, wrong place" to "immediately, right place".

## Who it affects

- **Integration teams**, who own the feeds in the middle and get blamed for other people's changes.
- **The sending and receiving systems' owners**, who may not even realise a change rippled outward.
- **Patients and staff downstream**, who are affected when a safety alert, a report or a registry quietly goes wrong.

## Start from what you really receive, not the spec

The natural instinct is to write a contract from the interface specification — the original document that described what the feed should contain. That almost always goes wrong, for a simple reason: specifications are usually years out of date.

The real, live feed has grown barnacles over time. There are the three extra custom fields the site added two years ago. There is the field the spec says is required but that is, in practice, always empty. There is the code nobody ever documented but that shows up every day. A contract written from the old spec would flag all of these normal realities as problems, and a watchdog that barks at normal things gets ignored.

So the honest starting point is **the traffic itself**. Look at what is actually flowing today, and build the contract from that. Then you are describing reality, not a memory of what reality was meant to be.

## Expectations as rates, not absolutes

Here is the single most important design idea, and it is pure common sense once you see it.

Real feeds always contain a trickle of genuinely odd messages. A vendor sends a test message. A patient has no recorded sex. Someone types an entry by hand and skips a field. These oddities are normal background noise. A contract that fires an alarm on *every* odd message would go off constantly, and people would learn to ignore it — at which point it is worse than useless, because it is also there when something real happens.

The fix is to state each expectation as a **proportion**, not an absolute. Instead of "the medical record number is *always* present", you say "the medical record number is present in **at least 99%** of messages".

![Why an expectation is a proportion: a rule set at 99 percent ignores one missing field in ten thousand but fires when five hundred go missing](docs/assets/articles/feed-contracts-rate.svg)

Walk through what that buys you. Suppose ten thousand messages come through:

- On a **normal day**, maybe one message out of ten thousand is missing its record number — a stray test message. That is 99.99% present, comfortably above the 99% rule. **No alarm.** The watchdog stays quiet for harmless noise.
- On a **bad day**, something has broken and five hundred messages out of ten thousand are missing the record number. That is 95% present, well below the 99% rule. **Alarm.** This is exactly the kind of real drop you want to hear about immediately.

The same rule stays silent for the trickle of normal weirdness and speaks up for a genuine change. That is what makes it something people actually trust and act on.

> In plain words: don't demand perfection, because real feeds are never perfect. Demand "almost always", and let the watchdog bark only when "almost always" stops being true.

## A worked example

Suppose you run the feed that carries admissions (ADT messages) from the registration desk to the rest of the hospital. You want to know the day something important changes.

![Profile the feed, promote it to a contract, check it on a schedule, and alert on a violation](docs/assets/articles/feed-contracts.svg)

**Step 1 — look at what is really flowing.** You profile a folder of recent messages. "Profiling" just means measuring: which fields are always filled in, which are always empty, what values actually appear in each coded field, how often things repeat, and which parts of the message are outside the standard. Now you know the feed as it truly is today.

**Step 2 — turn that into a contract.** From the profile, you generate a draft contract. It lists expectations like "the medical record number is present in at least 99% of messages". You trim it down to the handful of things that genuinely matter — the record number, the patient class, a key date — and delete the rest, because a contract about everything is a contract about nothing.

**Step 3 — attach it to the feed and let it watch.** You tell the engine to re-check recent traffic every fifteen minutes against the contract. From then on, if the feed stops matching — the record number drops to 95% present — you get an alert that says, in plain terms, *the sender changed something*. Not "the engine is broken". The right place, at once.

Weeks later, the registration vendor pushes an upgrade that drops the record number from a slice of messages. Instead of a billing report mysteriously going wrong next month, you get an alert this afternoon, naming the field and the feed.

## Honest results matter as much as alarms

A watchdog is only trustworthy if it is honest about what it does and does not know. Two principles make the difference:

- **Too little data reads as "not judged", never as "passing".** If only a handful of messages have come through — overnight, say — the contract does not pretend everything is fine. Silence is never mistaken for health. It says plainly that it has not seen enough to judge.
- **The alarm blames the right thing.** When a contract is violated, the message says the *sender* changed, not that your engine failed. That one word sends people to look in the right place first, which is often most of the battle.

## Common questions

**Isn't this just extra alerting?** It is a specific, powerful kind. Ordinary alerts tell you when something *errors*. A feed contract tells you when something *changes while still looking valid* — the failure that ordinary alerts miss entirely.

**Won't it nag us constantly?** Not if the expectations are rates, as above. A well-set contract is quiet for normal noise and speaks only for real change. That is the design goal.

**How is this different from testing my own changes?** Feed contracts watch what the *other side* sends you. Testing your own changes before you deploy them is a separate tool — often called **shadow mode**, which runs a proposed change against real traffic without delivering anything, so you can see the effect safely.

**Do I have to write the contract by hand?** No. You generate it from real traffic and then trim it. Each line notes whether it was *measured* from the data or *decided* by a default, so pruning to what matters is quick.

**What if I want this to block a release?** You can run the check in an automated pipeline, where it fails loudly if the feed does not match — useful as a gate before changes go out.

## Glossary

- **Feed** — a continuous stream of messages flowing from one system to another.
- **Interface** — the connection, in the middle, that carries a feed and may transform it along the way.
- **Feed contract** — a written statement of what a feed should contain, checked automatically against reality.
- **Expectation** — one promise inside a contract, such as "the record number is present in at least 99% of messages".
- **Rate / proportion** — stating an expectation as "almost always" rather than "always", to tolerate normal noise.
- **Profiling** — measuring what is actually in a feed: which fields are filled, what values appear, how often things repeat.
- **Drift** — the slow or sudden change in what a feed contains over time.
- **Contract violation** — the alarm raised when a feed stops matching its contract.
- **Shadow mode** — a separate tool for testing *your own* changes against real traffic without delivering anything.
- **ADT** — the HL7 v2 message type for admissions, transfers and discharges (used in the example above).
- **Z-segment** — a custom, non-standard part of a message that a specific site added.

## How Perfuse does it

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with feed contracts built into every feed it runs. The three steps above map straight onto three plain commands.

**1. Profile the feed** — measure what is really arriving:

```sh
perfuse profile messages/
```

This reports which fields are always populated, which never are, the real set of values in each coded field, how often things repeat, and which parts are outside the standard. Patient privacy is respected: actual values are only reported for fields whose contents form a small, non-identifying code set.

To see what changed since last month, save a profile and compare against it:

```sh
perfuse profile -save last-month.json messages/
perfuse profile -against last-month.json today/
```

**2. Promote it into a contract** — turn the measurement into a promise:

```sh
perfuse contract promote -o adt.contract.yaml messages/
```

Every line of the generated contract records whether it was **measured** from the data or **decided** by a default, so trimming it to what matters is fast:

```yaml
expectations:
  - path: PID-3
    rule: populated
    min_rate: 0.99
    why: the medical record number; everything downstream keys on it
```

**3. Attach it to the feed** — and let the watchdog run:

```yaml
name: adt-in
contract:
  file: adt.contract.yaml
  check_every: 15m
  over: 500          # re-measure the last 500 messages each time
```

Perfuse re-profiles recent traffic on that schedule and raises a **contract violation** alert when the feed stops matching. A default alert rule comes with it, so attaching a contract is all you need to start being told.

**Clear, honest results**, exactly as described above:

- **Too few messages reads as "not judged", never as passing** — silence is never mistaken for health.
- **The alert says the sender changed**, not that the engine failed, so you look in the right place first.
- **A strict check** can run in an automated pipeline and fail loudly on any violation, useful as a release gate.

**And it sits among related tools**:

- **Eleven kinds of alert**, including a "below rhythm" alert that notices when a feed is running *quieter* than its own history — catching a half-broken sender that a simple threshold would miss.
- **Shadow mode** for safely testing *your own* changes against real traffic.
- **Shared mapping tables** that record who decided each translation and why, so the reasoning is not lost.

All of this is one engine — HL7 v2, FHIR, X12, DICOM and CDA over many connector types, with a durable message queue and a full web console — in a single file with nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows.
2. Run `perfuse profile` on a folder of messages from one feed and read what it finds.
3. See the [manual](https://perfuse.health/manual/) for every contract rule, in plain terms.

Related: [Shadow mode](https://perfuse.health/shadow-mode/) · [HL7 v2 to FHIR](https://perfuse.health/hl7-v2-to-fhir/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
