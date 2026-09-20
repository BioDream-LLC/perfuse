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

> Perfuse is new. It has no production hours behind it, and that is a real objection rather than a marketing problem. The chapter on [testing](#testing) and the one on [migration](#migration-from-mirth) exist because the answer to "why should I trust this" has to be evidence you gather yourself, not a claim made here.

## What it replaces

Perfuse covers the ground held by Mirth Connect, and can import Mirth channel exports directly. The chapter on [migration](#migration-from-mirth) describes what translates cleanly, what needs a decision, and how to prove the result matches before cutting over.

The differences that matter in daily use are debugging and silence. In Mirth, seeing what a transformer did to a message generally means redeploying the channel with logging added. In Perfuse, any recorded message can be replayed through the current configuration and inspected step by step, without touching the running channel. And where a Mirth alert on "no traffic" is a fixed threshold that is useless on any feed that is quiet at night, Perfuse learns what each feed's normal week looks like and alerts against that.

## How to read this manual

The first chapters are conceptual and are worth reading in order: [channels](#channels), [message flow](#message-flow), then [transformation](#transformation). Everything after that can be read as needed.

The reference chapters near the end list every configuration key, its type, whether it is required and its default. Those chapters are generated from the source, so they cannot describe an option that does not exist or omit one that does. If a key appears in a configuration file and not in the [index](#index-of-configuration-keys), it is not a Perfuse key.

## Conventions

Configuration is YAML. Most keys are `snake_case` on the wire — `max_message_size`, `idle_timeout` — and this manual always gives the wire form, because that is what you type. There is one long-standing exception, `dataType` on a channel, which is camelCase; it is documented that way in the [channel reference](#channel-reference) and accepted only in that spelling.

Field references in HL7 use the usual notation: `PID-5` is the fifth field of the PID segment, `PID-5.1` its first component, and `PID-3[2]` the second repetition of the third field. Where a path is written without a repetition it addresses the first.

Durations are written as a number and a unit — `30s`, `5m`, `2h`. A bare number is not a duration and will be refused rather than guessed at.
