# The Command Line

Everything Perfuse does to a running system is reachable from the web interface, and that is deliberate: a feature that needs a terminal is treated as a defect rather than a design choice. The commands here are for the work that happens *around* a running system — before it exists, while you are deciding whether to trust it, and when you need to hand something to somebody else.

Several of them never touch a server at all. `perfuse explain`, `perfuse profile`, `perfuse deident` and `perfuse translate` read files and write files, so they are useful on a laptop against an estate that still runs entirely on something else.

Run any command with `-h` for its flags. This chapter says what each one is *for*.

> The flags shown here are illustrative, not exhaustive. `-h` is generated from the code and cannot fall out of date; a list copied into prose can.

## Running channels

### perfuse serve

Runs the web interface and the API, and by default runs the channels too. This is the command an installation uses.

It refuses to serve plain HTTP to anything except loopback unless you pass `-insecure`. That is not configurable politeness: clinical traffic and a session cookie over an unencrypted LAN connection is a reportable event, and the flag exists so that saying yes is a decision somebody made rather than a default they inherited. See [security](#security).

### perfuse run

Runs channels from YAML with no web interface and no database. Useful in a container that should do exactly one thing, and useful when you want to see a channel's behaviour without anything else running.

### perfuse check

Validates channel definitions and prints what they do, without running them. The second half is the point: a channel that loads is not necessarily the channel you meant, and reading a plain-English summary of what a file will do catches a mistake that validation cannot see.

Exits non-zero on a fault, so it belongs in whatever checks your configuration before it reaches a server. `-quiet` prints nothing on success, for that use.

`-allow-metadata-egress` permits destinations pointed at cloud instance metadata addresses. Those addresses hold the machine's own credentials, so a channel that can be talked into sending there is a credential leak; the check refuses them unless you say otherwise.

### perfuse test

Runs tests written against channel definitions. A test sends a message through the channel's real filter, real transformations and real scripts, in the real order, with only the network replaced — so what passes here is what the channel does.

Tests live in `*_test.yaml` or `*.test.yaml` beside the channels. See [testing](#testing).

## Migrating from another engine

These three are the ones to run first, before deciding anything. See [migration](#migration-from-mirth).

### perfuse explain

Reads Mirth channel exports and describes what each channel does and what would block a migration. It changes nothing and needs no Perfuse installation, which makes it the cheapest possible first step: point it at an export of your estate and read what comes back.

`-strict` exits non-zero if anything is blocked, so it can gate a pipeline. `-json` emits the same findings for a machine.

### perfuse translate

Converts Mirth channel exports into Perfuse channel files.

Nothing is silently dropped. Every part of a channel is either translated, carried across as a script that runs unchanged, or reported as needing a human. The third category is the honest one — a translator that produced a clean-looking file for every input would be hiding the decisions you most need to make.

### perfuse compare

Runs the same traffic through two engines and reports where they disagree, grouped by field.

This is the command that answers "why should I trust this with patient data", and it answers it with evidence you gathered rather than a claim made here. It is the recommended last step before cutting over, and the recommended first step in any argument about whether Perfuse is ready.

## Understanding a feed

### perfuse profile

Reads messages and reports what is actually in them: which fields are always populated, which never are, the real value sets of coded fields, repetition counts, and which Z-segments appear.

Values are only reported for fields whose values form a small code set, so a profile of real traffic does not become a file full of patient data.

Saving a profile and comparing against it later is how you find out that a sender changed something:

    perfuse profile -save last-month.json corpus/
    perfuse profile -against last-month.json new/

### perfuse contract

Turns the output of that thinking into something enforceable. A contract says what a feed must look like, so that the day it changes you are told, rather than finding out weeks later from a receiver that fell over — the messages are still valid HL7 when a field disappears, so nothing else notices.

    perfuse contract promote -o adt.contract.yaml corpus/
    perfuse contract check adt.contract.yaml today/

`promote` generates a starting point, not an answer. Read it and delete most of it: every line records whether it was measured or decided, which is what tells you which lines to keep.

## Producing messages

### perfuse generate

Produces synthetic HL7 v2 messages. Everything is invented — obviously fictional names, sequential identifiers, made-up addresses — so the output is safe to commit, mail, and attach to a ticket.

It can send straight at a channel rather than writing a file, which is the fastest way to prove a listener works:

    perfuse generate -n 100 -send 127.0.0.1:6661

### perfuse deident

Reads real messages and writes a corpus that keeps their structure, their codes and their intervals, and none of what identifies a patient. This is the difference between being able to send somebody a reproduction and not.

It is not the same tool as `perfuse generate` and the distinction matters. Generated messages are invented and therefore safe but unrealistic; a de-identified corpus is *derived from real traffic*, so it reproduces the oddities that actually break interfaces.

Two properties worth understanding before using it:

- The same salt always produces the same corpus, so a shared corpus can be regenerated rather than archived.
- **Keep the salt secret.** Pseudonyms are derived from real values, so anybody holding the salt can confirm a guess about who a record refers to.

> `-keep-local-segments` passes Z-segments through unchanged and is unsafe. Z-segments are exactly where sites put names, notes and free text, so keeping them defeats the purpose of the tool. It exists because a structural problem is sometimes *in* a Z-segment, and then you need a corpus you must not share.

## Moving messages by hand

### perfuse listen

Accepts HL7 v2 over MLLP and acknowledges it. A receiver that exists for as long as you need it, for proving that a sender can reach this machine at all.

### perfuse send

Sends HL7 v2 messages over MLLP from files.

> MLLP has no authentication and no encryption, in Perfuse or anywhere else. Both of these commands are subject to that, and so is every MLLP source and destination: access is restricted at the network layer or it is not restricted.

## FHIR

### perfuse fhir

Converts HL7 v2 to FHIR, validates FHIR, or serves it, depending on the subcommand.

The validating subcommand is useful on its own: it will tell you whether a resource somebody sent you satisfies US Core, which is the profile American regulation is written against. Being able to answer that without adopting an engine is the point.

## Setting up and operating

### perfuse init

Creates the directory layout, an example channel, and a service definition for the operating system it is run on. Nothing existing is replaced unless you pass `-force`.

    perfuse init -dir /opt/perfuse -service systemd

### perfuse service

Registers Perfuse as a Windows service, removes it, or reports whether it is installed and running. Windows only; on Linux the equivalent is the unit file `perfuse init` can write for you. See [operations](#operations).

### perfuse token

Issues credentials for machines rather than people. A token does not expire and is not affected by anybody signing out, which is what a server polling its neighbour needs and what a browser session deliberately is not.

    perfuse token create -db perfuse.db -label fleet-from-site-a -role viewer
    perfuse token list   -db perfuse.db
    perfuse token revoke -db perfuse.db -label fleet-from-site-a

Only the hash is stored, so a token is displayed once when it is created and cannot be recovered afterwards. A stolen database therefore yields no usable token. Give a token the lowest role that does the job: a fleet view needs `viewer`, and nothing that only reads should hold anything more.

### perfuse sbom

Lists everything linked into the binary, read from the build itself rather than from `go.mod` — so it describes the binary in front of you and not what somebody once asked for.

`-json` emits CycloneDX and `-spdx` emits SPDX tag-value, which is usually what a procurement or security review is actually asking for. The browser code embedded in the binary is included, because a reviewer matching a binary against advisories needs to know it contains a React application.

### perfuse version

Prints the version and the commit it was built from. Worth putting in a ticket: "the current one" is not a version, and two machines that were installed a week apart are frequently not running the same build.
