# What has been verified, and what that found

Most integration software is tested against its own idea of what the other side does. That is how a
product acquires a thousand passing tests and a feature that has never once worked against the real
thing — and it is not a hypothetical failure, it happened in this repository and is recorded below.

So the parts of Perfuse that talk to other people's software are checked against that software: a real
Mirth Connect, a real Keycloak, a real Microsoft Entra tenant, a real HAPI FHIR server, a real PACS, a
real PostgreSQL, a real OpenSSH server, real OpenSSL. This document says what was verified, what version
it was verified against, and what the verification found — because a claim of verification with no record
behind it is exactly the kind of assertion this project exists to distrust.

Every finding here was produced by running the software against another implementation. None of them were
found by reading code, and several had a substantial body of passing tests sitting on top of them.

---

## SAML, against Keycloak 26

**What was found: no identity provider could ever have signed anybody in, and about a thousand lines of
tests said otherwise.**

A real browser sign-in was driven end to end against Keycloak 26. The first genuine assertion failed with
a digest mismatch, and the cause was in the canonicalisation: the canonical form that a signature covers
was being computed with namespace prefixes dropped. That produced a different document from the one
Keycloak had signed, so the signature could never have matched. Verification could not have succeeded for
any provider, ever.

Keycloak writes the assertion as `saml:Assertion` and declares `xmlns:saml` on the document root. The
canonical form came out unprefixed, and every test in the suite agreed with it, because every test had
been written against the same incorrect canonicaliser. The tests were self-consistent and collectively
worthless — they proved the code agreed with itself.

The fixture in `internal/saml/testdata` is a document a real Keycloak published. It is kept because a
document written by hand could never have caught this.

## SAML, against Microsoft Entra ID

**What was found: five behaviours no reading of the specification predicts, and two defects in the code
surrounding the protocol.**

A real interactive sign-in was completed against a live Entra tenant, with a real Microsoft account. The
security-critical path — signature, request binding, conditions, audience — was correct the first time it
saw a real Entra document. Everything that was wrong was around it.

Five behaviours that documentation and examples do not lead you to expect:

1. **No email claim at all.** Entra sends display name, identity provider, object identifier, tenant ID
   and name. No email address.
2. **The NameID is an opaque persistent identifier**, not an address and not readable.
3. **No groups claim is sent** unless the application is explicitly configured to emit one, so a role
   mapping written against groups refuses every sign-in. This happened on the first attempt.
4. **When groups are emitted they are object GUIDs**, not names, unless the tenant synchronises from
   on-premises Active Directory.
5. **Claim URIs use `schemas.microsoft.com/identity`** where published examples show
   `schemas.xmlsoap.org`. Perfuse works here only because attribute lookup compares by URI suffix. A
   stricter comparison would look more correct and would lose the display name for every Microsoft
   tenant. That now has a test.

Two defects, both found by looking at the users screen after signing in rather than by reading code:

- **The account was created under Entra's opaque NameID.** The readable-name logic derived a username
  from an email claim, and Entra sends none, so it fell through to an unreadable identifier — which would
  then have appeared beside every channel that account changed. It now reads the user principal name from
  the `name` claim. Break-verified: removing the lookup reproduces the unreadable name exactly.
- **A SAML account was recorded as OIDC.** No `AuthSAML` constant existed, and the store derived the
  authentication source from the issuer, which cannot work when a SAML issuer and an OIDC issuer are both
  HTTPS URLs. A site running both could not have told its accounts apart, which is the question that
  matters when somebody leaves.

**The lasting artefact is the fixture pair.** Entra writes `<Assertion>` and `<Signature>` with no
namespace prefix; Keycloak writes `<saml:Assertion>` and `<dsig:Signature>`. Those are the two opposite
cases in canonicalisation, and the defect described in the previous section was in prefix handling — so
one fixture covered only one side of it. A test asserts that each document continues to cover its own
case, because a replacement capture that happened to be prefixed would quietly halve the coverage without
failing anything.

## Mirth Connect 4.5.2 — reading its channels

**What was found: the repository's only Mirth fixture was written by hand, and a real Mirth refused to
load it.**

The fixture declared what it was in its own header — written by hand, not derived from any deployment.
Tested against Mirth 4.5.2 in a container, Mirth accepted the upload and stored the channel as invalid.

Mirth now writes the fixtures. A script has Mirth's own serialiser emit its defaults, and those documents
are committed. A document this project wrote could never have been evidence about what Mirth accepts.

## Mirth Connect 4.5.2 — writing its channels

**What was found: the exporter had nine passing tests, and Mirth discarded everything it produced.**

Every one of those nine tests was a round trip through the same package: write XML, read it back with our
own parser, compare. The exporter's own documentation admitted the doubt in writing — that Mirth "may or
may not accept" the result — and nothing had ever checked.

The reason it stayed hidden is worth stating on its own: **Mirth does not refuse a channel it cannot
assemble.** It stores one, replaces the description with a sentence saying the channel is invalid, drops
the destinations, and returns success. Acceptance is not verification.

Four faults, each break-verified against the running server:

1. **The channel carried an `enabled` element.** Mirth's model has no setter for it, so its deserialiser
   discarded the entire channel. This had been documented in a fixture comment since August and never
   reached the code it described.
2. **Destinations were written as a bare connector element**, with the version only on the source — and
   an existing test asserted that bare form, holding the defect in place.
3. **Nested property elements lost their attributes**, because the importer flattened them to dotted
   paths which cannot carry a class attribute or per-element versions. Fixed by retaining the parsed
   subtree and writing it back verbatim.
4. **Resource identifiers were written as a single string** where Mirth writes a key/value pair, and MLLP
   framing properties were missing entirely.

A further gap was found by deliberately breaking the exporter: **Mirth accepts an unknown transport name
without complaint.** A source set to a transport that does not exist still stored as valid, because the
deserialiser resolves the properties class and takes the transport name on trust. The mistake would
surface only on deploy. Transport names are now asserted explicitly.

Ten transport pairs are verified by importing into a running Mirth server: MLLP, TCP, HTTP, file,
database, DICOM in both directions, SMTP, SOAP and JavaScript destinations.

What does not convert is reported by name and never approximated. Filters, transformations, scripts,
contracts, shadow comparisons, attachment extraction and mapping tables have nowhere to live in Mirth's
format, and each is listed before the file is offered. A destination type that cannot be honoured is
refused by name rather than replaced with something close.

## FHIR, against HAPI FHIR

**What was found: the converter was fabricating an identifier system, and HAPI did not object to it.**

A converted Patient is accepted by a real HAPI server, and the round trip preserves what matters: the
name, the birth date converted from HL7's `19061209` to `1906-12-09`, and the gender mapped from `F` to
`female`.

The third test is the one that makes the other two mean anything. It posts deliberately invalid FHIR and
requires HAPI to refuse it, because a server with validation switched off would make the first two tests
pass while proving only that a server exists.

## DICOM, against Orthanc

**What was found: a second implementation agreeing, where previously there was one.**

An instance Perfuse sends arrives at a real PACS, and the patient name, identifier, study date, modality
and SOP class all survive the association. The manual already claimed the DICOM work was verified against
DCMTK; this is a different codebase agreeing, which is worth more than either claim alone.

The assertion is Orthanc's own catalogue and then the tags it stored — not the absence of an error. An
association that negotiates successfully and stores nothing looks identical to success from the sending
end.

## Databases, against PostgreSQL 16

**What was found: nothing broken, and two controls that make the result mean something.**

The destination's statement is accepted, the row is in the table, and it is read back over a second
connection — because an INSERT that reports success and stores nothing, or that binds its parameters in
the wrong order, is indistinguishable from success from the sending side.

Two controls on the dialect itself: a question-mark placeholder is refused by this server and a dollar
placeholder is accepted. That makes the tests about the statement rather than about a server that would
accept anything. A statement written in the wrong dialect is now refused when the channel is saved rather
than at three in the morning.

## SFTP, against OpenSSH

Verified against OpenSSH, which is a different implementation from the one used on both ends of the
existing tests. A client and server from the same library agreeing tells you they agree with each other.

## Mutual TLS, against OpenSSL

Verified against OpenSSL rather than against Perfuse's own client. Certificate requirements, rejection of
an untrusted client certificate, and rejection of a certificate for the wrong name are all checked by a
tool that shares no code with the thing being tested.

## UDAP and TEFCA, against a real authorization server

A real authorization server reads what this client signs. It refuses on trust-community membership rather
than on the form of the request, which is the distinction that matters: a server that rejected malformed
requests would say nothing about whether the signed metadata was correct.

Discovery and signed-metadata verification are checked against a real server and a real trust community.

---

## The general lessons, which cost more than the specific fixes

**Self-agreement is not evidence.** The SAML canonicaliser, the Mirth exporter's nine tests, and the
hand-written Mirth fixture were all instances of code agreeing with tests written against the same
assumption. In each case the body of passing tests was what made the fault invisible.

**Acceptance is not verification.** Mirth stores an unusable channel and returns success. An INSERT can
report success and store nothing. A DICOM association can negotiate and discard. In every case the
sending side sees exactly what success looks like.

**A test can hold a defect in place.** One test asserted the malformed destination form that caused Mirth
to empty the destinations. It was not a missing test; it was a test that had to be deleted.

**Controls decide whether a result means anything.** The HAPI suite would have passed against a server
with validation disabled without the test that requires a refusal. The PostgreSQL suite would have passed
against a server that accepted any dialect.

**Knowledge recorded next to the wrong file does not propagate.** The Mirth `enabled` fault was written
down in a fixture comment in August and the code it described was never changed. Prose is not a guard.

**A guard is verified by breaking the thing it guards.** A test that has never failed is a hypothesis.
When breaking one, confirm the break compiles — a change that does not compile looks exactly like a
passing test, and that has caught this project more than once.

---

## The footprint figures on the front page

Measured, not estimated, on one machine with both products idle.

Perfuse: **31 MB** resident at rest, **32 MB** after thirty seconds idle, **46 MB** after five thousand messages had
been parsed, stored and delivered. CPU at idle was 0.0%. Four channels configured, message storage on.

Mirth Connect 4.5.2, in a container on the same machine: **383 MB**, of which the Java process itself accounted for
358 MB. CPU at idle 0.13% to 0.27%. Its application directory is 254 MB and it requires OpenJDK 17 underneath.

What that comparison is and is not. It is an honest measure of what each costs you to have running, taken the same
way on the same hardware. It is not a throughput benchmark, and neither product was tuned: Mirth's memory is
largely JVM heap and can be configured down, and Perfuse's will rise with traffic and retention. The durable claim
is not the ratio, it is that one of them needs a Java runtime installed and the other is a file.

For completeness, the throughput observed while measuring was 245 messages per second over a single MLLP
connection with an acknowledgement awaited for each one. That figure is bounded by the sequential sender, not by
the engine, and should not be quoted as a capacity.

---

## Patient data in this repository

There is none. Every HL7 message in the tree is invented, and there are a few hundred of them because an HL7 engine cannot be
tested without messages or documented without examples.

The patient names are recognisable placeholders (`DOE`, `SMITH`, literal `SURNAME^GIVEN`), invented recurring patients so a
reader can follow one person through a scenario, historical figures, and names chosen to exercise character handling —
accents, non-Latin scripts, apostrophes, and a surname long enough to test field limits. Identifiers are sequential
(`MRN1`, `0001234`). The only social security numbers present are the canonical examples that cannot be issued. Telephone
numbers use the 555 exchange reserved for fiction.

That is checked by a test rather than by care. `internal/compliance/nophi_test.go` holds every patient surname in the tree to
an allowlist, so introducing a new one is a deliberate act visible in review rather than something that arrives attached to a
bug report — which is the realistic accident, because the easiest way to reproduce a defect is to paste the message that
caused it, and in this domain that message is somebody's medical record. It also rejects social security numbers outside the
canonical set and telephone numbers outside the 555 range.

Break-verified with a fixture containing a plausible name, address and telephone number: both checks failed, naming the file.

---

## What continuous integration found the first time it ran

The workflow file had existed for some time and had never executed, because the repository had no remote. Its first run
failed, and what it found was not in the code.

**The local gate and the CI gate disagreed about what passing meant.** The Makefile allowed each package 300 seconds;
the workflow duplicated the test command with its own 120-second budget. So the suite was green locally and timed out
in CI, and the failure read `panic: test timed out` beside whichever test happened to be in flight - which was not
where the time had gone. The timeout is now defined once, in the Makefile, and the workflow calls that target.

**Then the real finding.** `internal/engine` took 215 seconds, and six of its tests took exactly 15.0 seconds each. An
exact round number is a clue: the default retry policy is five attempts with a doubling one-second backoff, which is
1+2+4+8 seconds, and each of those tests was sitting through the full production retry schedule in order to observe a
failed delivery it already had. Setting one attempt in the two test helpers concerned took the package from 215 seconds
to 93, and to 128 with the race detector on.

What remains slow is legitimate: file and SFTP polling tests that have to watch a file stop changing, which means real
time has to pass. Those were left alone.

**And a genuinely flaky test, found only because the suite ran on a busy machine.** `TestWorkerRetriesThenSucceeds`
waited for a successful send and then cancelled the worker, while asserting on state the worker writes to the store
*after* sending. Cancel in that gap and the item is still recorded as sending with three attempts. It failed about one
run in six, and only in a full run. It now waits for the state it asserts on.

The first diagnosis of that one was wrong. The fake sender counts successful sends only, so waiting for a count of one
was correct; changing it to four made the test fail every time, which is how the mistake surfaced immediately rather
than becoming a second flake.

---

## What has not been verified

Stated plainly, because an unverified claim that nobody writes down becomes a claim everybody assumes was
checked.

- **The Windows binaries have never been run.** They cross-compile, `go vet` passes for `GOOS=windows`,
  and they are valid PE32+ executables — but no Windows machine was available. `perfuse service`, which
  runs Perfuse as a Windows service, is likewise untested on Windows.
- **The Linux arm64 binary has been built and not run.** The amd64 binary was verified inside Alpine,
  which has no glibc, confirming the static-linking claim; it serves the embedded web interface and
  answers a JSON 404 for unknown API paths.
- **No container image has been published**, though the build file and the README both name one.
- **No site has run production clinical traffic through any of this.** The engine, transports, queue and
  web interface are tested, and the parts that talk to other software are verified as described above.
  That is not the same as having survived a real hospital's Monday morning.

Shadow mode exists precisely so that nobody has to take this document's word for any of it: run Perfuse
beside whatever is in place today, on real messages, delivering nothing, and read the differences.
