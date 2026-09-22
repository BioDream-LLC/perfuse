# Testing

Standard advice for testing an interface is to build sample messages that reflect your own environment — your laboratory's codes, your case mix, your sender's habits — and to strip the patient data out by hand. That is slow, and doing it incompletely is the normal outcome.

Perfuse offers three things instead: generated traffic that matches a feed's shape without its content, a channel proposed from a sample somebody sent you, and a comparison against the engine you are replacing.

## Generated test traffic

If a channel has handled messages, Perfuse can produce more messages like them.

What is faithful:

- Trigger events in the proportions actually observed, with at least one of each so a rare event still appears.
- Fields populated at the rates they really are — so the case where a field is *absent* gets tested, which is the case that breaks things.
- Repetitions where repetitions occur, composite structure where it occurs.
- Codes drawn from the code tables actually seen.

What is not faithful, and this is stated rather than implied: **clinical coherence**. A generated patient has an unrelated record number, a name-shaped string, and a diagnosis with no relationship to the observation. These messages exercise an interface. They are not a clinical test set and cannot be used to validate clinical logic.

### Why it contains no real data

The argument is structural rather than a claim about effort. The generator's only input is the channel's traffic **profile**, and a profile holds fill rates, shapes, lengths, repetition counts and segment frequencies. It holds no values.

The single exception is code-table fields, and a code identifies nobody.

This is a stronger guarantee than de-identifying real messages, which is a process that can be done incompletely and usually is. Here there is no path by which a name could reach the output, because the name was never in the input.

Every generated message is parsed before it is returned — an unparseable corpus would send somebody hunting for a bug in their interface that is really a bug in the generator — and every one is marked `T` in MSH-11, with a deliberately synthetic sending application and facility.

> A test message that is not marked as one will eventually be found in a production system by somebody who has no way of knowing it was not real. The `T` and the synthetic sender are not cosmetic.

## Building a channel from a sample

Paste the message a laboratory, hospital or vendor emailed you and Perfuse proposes a channel for it. Nothing needs to be running first.

It handles what people actually paste: several messages in one block, any line ending, MLLP framing that survived the copy, and the covering note above the message. Refusing a sample because it arrived with Windows line endings would be an obstruction rather than a check.

### Three lists

The proposal comes with what the sample **shows**, what has been **guessed**, and what the sample **cannot tell you** — three separate lists, weighted equally in the interface.

They are separate because a reader who cannot tell which is which has to verify everything or trust everything, and both are worse than knowing where to look.

From a single message the limits are named specifically rather than generally: which fields are optional, which segments repeat, which codes exist beyond the ones here, whether the sender ever sends a different trigger event. A general caution is one people skim.

Confidence is a word — low, moderate, reasonable — never a percentage. A percentage implies a calculation, and this is a judgement about how many samples there are.

Z-segments are called out by name, because a local segment is the thing no specification mentions and the commonest reason a channel built from a standard template does not work.

### The separator warning

If the sample's field separator is not the usual `|`, that appears **first**, and confidence drops to low.

The reason is a genuine silent failure. HL7 takes its field separator from the message itself, so a sample whose pipes have been replaced by typographic look-alikes — which is what a word processor does — parses perfectly into fields that are all wrong. Nothing errors anywhere.

Refusing such a sample would be wrong, because a sender may legitimately use another separator. So it is flagged, the character is named, and asking for a plain text attachment is suggested.

## Comparing against the old engine

Before moving a live feed, the question is whether Perfuse produces the same output as the thing that has been running for years. No amount of design quality answers that. See [migration](#migration-from-mirth).

## Verifying against the real thing, in containers

Three checks run against real implementations rather than against Perfuse's own idea of a protocol. They live outside `make check` because they need a container runtime, and a check that needs Docker is a check people stop running.

`scripts/interop-up.sh` starts all of them and prints what each one unlocks.

| Container | Checks | What it found |
| --- | --- | --- |
| Keycloak 26 | `web/e2e-saml/`, and `internal/saml` fixtures | **A defect.** The canonical form a signature covers was computed with namespace prefixes dropped, so no real identity provider could ever have signed anybody in |
| Microsoft Entra | `internal/saml` live tests and fixtures | **Two defects, both in what surrounds the protocol.** An account was created under Entra's opaque NameID, because the readable-name logic wanted an email claim and Entra sends none. And a SAML account was recorded as OIDC, because the store derived the protocol from an issuer that looks identical for both |
| HAPI FHIR | `go test ./internal/engine/ -run HAPI` | **A defect.** An unmapped assigning authority produced an identifier system announcing an OID and carrying a word, which HAPI accepted without complaint |
| ActiveMQ | `go test ./internal/engine/ -run RealBroker` | Correct, including the null-byte framing case. Asserted through the broker's own management interface rather than through a function returning nil |
| nginx, client certificates | `go test ./internal/engine/ -run ClientCertificate` | Correct |
| Orthanc (a real PACS) | `go test ./internal/engine/ -run RealPACS` | Correct. A C-STORE arrives and the patient name, identifier, study date, modality and SOP class all survive |
| PostgreSQL | `go test ./internal/engine/ -run RealPostgres` | Correct, and it prompted a better error: a statement in the wrong dialect's placeholders is now refused when saved rather than becoming a syntax error at the server when a message arrives |
| OpenSSH sftp-server | `go test ./internal/engine/ -run OpenSSHServer` | Correct. The existing SFTP tests use the same Go library on both ends; OpenSSH is a different implementation and the one on the far end of nearly every real feed |
| Mirth 4.5.2 | `go test ./internal/mirth/ -run RealMirth` | Correct now. The only fixture used to be hand-written and Mirth would not load it; `scripts/mirth-author-channel.sh` has Mirth author one instead, and Perfuse reads it |

Every one skips rather than fails when its container is absent, so `make check` passes on a machine with no Docker.

**Why this is separate from the rest of the suite.** Every other test of these features had both halves written here: a document this codebase signed, read back by the code that signed it. That is agreement with oneself, and it is worth less than it appears.

The SAML canonicaliser is the case that proves the point. It carried a thousand lines of tests, five of them specifically about canonicalisation, and all of them passed while **no real identity provider could ever have signed anybody in** — the canonical form that a signature covers was being computed with namespace prefixes dropped, and the only way to find that was to hand it a document Perfuse had not written.

Two of the eight checks found defects outright, and a third prompted a real improvement. Three found nothing wrong. That ratio is the argument for keeping all of them: a check that passes has told you something you could not otherwise have known, and there was no way to tell in advance which ones they would be.

One pattern worth naming, because it appeared in both defects: **acceptance is not verification.** HAPI accepted the malformed identifier system and stored it; the defect only appeared on reading the resource back. A receiver that tolerates something is not evidence that it is right, only that this receiver was lenient.

Two captured assertions are committed, at `internal/saml/testdata/keycloak-assertion.xml` and `entra-response.xml`, with tests over both — so that particular regression is caught without a container.

Both are needed rather than one being spare. Keycloak prefixes its assertion and signature, Entra does not, and those are the two opposite cases in canonicalisation — which is precisely where the defect was. A test asserts each document still covers its own case, because a replacement capture that happened to be prefixed would quietly halve the coverage without failing anything. The general lesson does not generalise so cheaply: for anything else, the container is the check.

## Diagnosing a test that fails once and never again

A test that fails in a full run and passes alone sixteen times is not a flaky test. It is a test that shared something with the run, and the way to find out what is to measure the shared thing rather than the test.

Two instruments exist for this, and both are meant to be run alongside a full suite:

```sh
./scripts/e2e-watchdog.sh /tmp/watchdog.log     # the HTTP server and the shared session
./scripts/e2e-mllp-probe.py /tmp/mllp.log       # the fixture channel's listener
```

Each discovers the run's ports from the setup's own state file and samples every 500 milliseconds until the server exits. They log every sample rather than only the failures, which is what makes them useful: a blip shows up in a run where nothing failed, so a fault that appears once in three runs becomes measurable on every one.

Set `PERFUSE_E2E_KEEP=1` to keep the temporary directory, which holds the database as it was left and the server log for the whole run. **The server log is usually the answer.** In the case this text was written for, it recorded a channel stopping at 05:03:08.735 and its listener rebinding at 05:03:09.156, and the probe's one refused connection was at 05:03:09.008 — inside that 421-millisecond window.

Read the trace rather than `error-context.md`, which has been empty on more than one occasion where the trace held the real message.

**Copy the artefacts out before re-running anything.** A passing run clears `test-results`, so the trace of the failure you are investigating disappears the moment you try to reproduce it. That happened on 19 September and left a question that can no longer be answered.

**Stop the interop containers first.** `colima stop` before a full browser run, and `./scripts/interop-up.sh` afterwards if you still need them.

Eight containers in a virtual machine hold around 9.8GB, and on a 16GB machine that leaves too little for Chromium's processes, node and the Go toolchain together. What it produces is not an out-of-memory error but a renderer that stops being scheduled: a trace from 19 September shows a **60.2 second gap with no frames at all**, during which Playwright waited for a button to become "visible, enabled and stable" and could not get two consecutive animation frames to compare. The server was healthy throughout, the slowest request in the whole run being 154 milliseconds.

That failure reads as a defect in whatever test happened to be running. It is worth knowing the shape of it: every request fast, the page visually frozen, the retry loop never reaching a second iteration, and the whole thing passing in isolation.

**Only one run at a time.** The suite shares one server, one signed-in session in `web/e2e/.auth/admin.json`, and one state file naming the server's process. None of that is per-run, so a second run starting while the first is going overwrites the state file, and whichever teardown finishes first kills the other run's server and deletes its session.

What that looks like is nothing like the cause. It produced a run of 279 failures whose first message was `ENOENT: no such file or directory, open './e2e/.auth/admin.json'`, a run that stopped early reporting 217 passed, and two tests failing with `bind: address already in use` — none of which suggest two runs fighting. The suite now takes a lock and refuses to start, naming the process that holds it. A lock left by an interrupted run is taken over rather than blocking for ever, because a safety measure that wedges the suite is one somebody deletes along with the safety.

A related trap, now fixed: two specs named their listening ports outright, so a server left behind by an interrupted run made them fail on something they had nothing to do with. Ports are asked of the operating system now.

Do not answer any of this with retries. A test that passes on the second attempt found a real race and hid it, and `playwright.config.ts` sets `retries: 0` for that reason.

One caution learned the hard way: sampling every 100 milliseconds spawned twenty processes a second, stretched a nineteen-minute run to twenty-eight, and failed two tests on timeouts that had nothing wrong with them. An instrument that changes what it measures produces findings about itself.

## What still needs real infrastructure

Several parts of Perfuse are implemented and have never been exercised against the real thing. They are listed here rather than left to be discovered:

- SMB against a real Windows share — the protocol code is complete and tested for path safety and argument handling, but no test opens a socket to a domain-joined server.
- A real serial port.
- The Mirth importer against XML a real Mirth produced. Tested against Mirth 4.5.2 in a container, and the finding was about this repository: the only migration fixture here was written by hand and **Mirth will not load it** — it stores the channel as invalid and discards every connector. What was settled is that the `version` attributes real Mirth writes on every element parse to the same channel, so they are not a hazard; what is not settled is everything else a real export contains.
- SAML against a hosted commercial identity provider. Keycloak is verified; one provider proves an implementation matches one vendor.
- An NCPDP claim through a real pharmacy switch.
- A SCRIPT message through Surescripts, where certification is a commercial process rather than a technical one.

And the honest general gap: Perfuse has no production hours. Its throughput ceiling is unmeasured, its long-uptime behaviour is unobserved, and the same person wrote both the code and the tests that check it. The features in this chapter and the next exist so that you can close those gaps with your own evidence rather than taking anybody's word.

## Measured against public conformance corpora

A test suite whose fixtures were written here proves agreement with oneself. These are
other people's data.

### FHIR R4, the specification's own examples

All 2,912 example files published with the FHIR R4 specification — every example the
authors of the standard wrote.

| Result | Count |
|---|---|
| Resources validated with no findings | 13,723 |
| Resources reported invalid | 0 |
| Files refused as an unimplemented resource type | 753 |
| Bundle entries skipped as an unimplemented type | 1,348 |

Not one resource was validated incorrectly. Every failure is an explicit refusal naming
the type it cannot read — 672 `ValueSet`, 80 `ConceptMap` and one `Parameters`. A
validator that quietly passes what it does not understand is worse than one that says so.

The corpus found a defect, which is the reason for running it. Validating a bundle printed
"Bundle validation from a file is not supported yet" and carried on. That was false in
both directions: bundles whose entries happened to be types the parser could hold were
already being validated, and a bundle that was genuinely invalid produced the same line,
counted nothing, and exited zero — including under `-strict`, which exists so this can
gate a build. Forty-two of the specification's own bundles were being skipped.

Bundles are now validated entry by entry, with three outcomes kept apart: an entry that is
wrong fails and names the field, an entry of a type this build cannot read is counted as
skipped and reported, and a bundle carrying no resource bodies is neither. The same corpus
went from 2,100 resources checked to 13,723.

### HL7 v2, the HAPI test corpus

The message fixtures from HAPI, the reference Java HL7 v2 implementation — deliberately
awkward material including uuencoded payloads, escaped delimiters and repeating groups.

59 messages, 59 parsed, nothing refused. Seven message types in a corpus any single system
would describe as one thing, which is the point `perfuse profile` exists to make.

### What has not been run

DICOM against a public conformance set, and X12 against a published corpus. Neither has
been done, and neither should be inferred from the two above.
