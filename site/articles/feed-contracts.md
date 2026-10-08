# Feed contracts: catch upstream changes before they break an interface

![A contract with field checks, one flagged as a violation](docs/assets/articles/hero-contracts.svg)

By **BioDream Developer**, BioDream LLC

The expensive part of an interface is rarely building it. It is the day the sending system changes. A vendor upgrade drops a field. A new code appears in a coded field. A segment starts repeating. None of those is an error: the messages are still valid HL7, every engine accepts them, and nothing notices until a receiving system falls over weeks later and the investigation starts from the wrong end.

**Feed contracts** close that gap. A contract says what a feed is supposed to contain, and the engine tells you the day reality drifts.

## Start from what you actually receive

Interface specifications are usually years old. The real feed has the three Z-segments the site added later, the field that is always empty, and the code nobody documented. So the starting point is the traffic itself.

## Expectations as rates

Real feeds contain a small share of genuinely odd messages: a vendor test message, a patient with no recorded sex, a manual entry. A contract that fires on each one trains people to ignore it. So a good expectation is a **proportion**: the medical record number is populated in at least 99% of messages. One odd message in ten thousand never fires. Five percent of messages losing their medical record number does.

## How Perfuse does it

![Profile, promote, check on a schedule, and alert on a contract violation](docs/assets/articles/feed-contracts.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with feed contracts built into every channel.

**1. Profile the feed**

```sh
perfuse profile messages/
```

Reports which fields are always populated, which never are, the real value sets of coded fields, repetition counts, and which segments are outside the standard. Patient data stays private: values are reported only for fields whose values form a small code set.

To see what changed since last month:

```sh
perfuse profile -save last-month.json messages/
perfuse profile -against last-month.json today/
```

**2. Promote it into a contract**

```sh
perfuse contract promote -o adt.contract.yaml messages/
```

Every line of the generated contract says whether it was **measured** or **decided**, so pruning it to what matters is quick:

```yaml
expectations:
  - path: PID-3
    rule: populated
    min_rate: 0.99
    why: the medical record number; everything downstream keys on it
```

**3. Attach it to the channel**

```yaml
name: adt-in
contract:
  file: adt.contract.yaml
  check_every: 15m
  over: 500          # profile the last 500 messages
```

Perfuse re-profiles recent traffic on that schedule and raises a **contract violation** alert when the feed stops matching. A default alert rule is included, so attaching a contract is enough.

**Clear, honest results**

- **Too few messages reads as "not judged"**, never as passing, so silence is never mistaken for health.
- **The alert says the sender changed**, not that the engine is failing, so you look in the right place first.
- **`perfuse contract check -strict`** in a cron job or a CI pipeline exits non-zero on any violation.

**Alongside it**

- **Eleven alert types**, including **below rhythm**, which notices a feed running quieter than its own history.
- **Shadow mode** for testing your own changes on real traffic.
- **Shared mapping tables** that record who decided each mapping and why.

All in one engine: HL7 v2, FHIR, X12, DICOM and CDA over twenty connector types, with a durable queue and a full web console, in one file with nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse profile` on a folder of messages from one feed.
3. Read the [manual](https://perfuse.health/manual/) for every contract rule.

Related: [Shadow mode](https://perfuse.health/shadow-mode/) · [HL7 v2 to FHIR](https://perfuse.health/hl7-v2-to-fhir/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
