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

## The Mirth family: Mirth 4.5.2, OIE 4.5.2 and 4.6.0, BridgeLink 26.9.0

**What was found: four defects in the importer, and three whole document kinds it could not read at all.**

Mirth Connect went commercial-only at 4.6 (March 2025). A site leaving it starts from Mirth 4.5.2, the Open Integration Engine (the Eclipse-hosted fork) or BridgeLink (Innovar's fork), so the importer and exporter were run against all of them: Mirth 4.5.2 (`nextgenhealthcare/connect`), OIE 4.5.2 (`openintegrationengine/engine`), OIE 4.6.0 (built from the project's signed release tarball, checksum verified, since it publishes no 4.6 image) and BridgeLink 26.9.0 (`innovarhealthcare/bridgelink`).

The corpus is written by the engines, not here. `scripts/mirth-engine-corpus.sh` compiles `BuildCorpus.java` against each engine's own jars and has its own `ObjectXMLSerializer` write three channels (MLLP with a mapper, a script calling a code template library, a rule-builder filter and two destinations; HTTP into a database; a file poll out over MLLP), a code template library and a channel group. It loads them into the running engine, checks none was stored as invalid, and downloads the engine's own server backup. The four sets are committed under `internal/mirth/testdata/engines/` and checked on every build without any engine running.

Found:

- **Server backups, channel group exports and code template exports could not be read** — the importer accepted only `<channel>` and a list of them. A channel calling a site library imported cleanly and would have failed on its first message with an undefined function. All three are now read, and each library becomes `lib/<name>.js`, included by exactly the channels Mirth had it enabled for.
- **HTTP Listener and File Reader sources were refused** as "only MLLP listeners are supported", long after Perfuse had both. JavaScript, DICOM, Web Service and raw TCP sources, and SMTP, JavaScript, DICOM, Web Service, raw TCP and Channel Writer destinations, translate now too. `perfuse explain` had its own stale table calling the Database and DICOM connectors unsupported; a test now holds it to agree with `translate` on every engine's channels.
- **An HTTP Sender's URL was read from `url`**, a field none of the four engines writes; they all use `host`.
- **Mirth's socket `bufferSize` (65536) was translated as `max_message_size`**, so every message over 64 KB would have been refused.
- **A Mapper step's variable became a JavaScript global.** Mirth puts it into `channelMap`, where `$('mrn')` reads it; the translated script ran the library on an empty string. Found only by running the translated channel: `TestEveryEnginesADTChannelRunsWithItsLibrary` sends an ADT and an ORU to each engine's translated channel and checks the record number was padded by the library and the ORU filtered.

The other direction: every live test of Perfuse's Mirth export now runs once per engine (`internal/mirth/mirthlive`). Channels Perfuse exports, ten transport pairs among them, load in all four without being marked invalid. BridgeLink stamps its own version (`26.9.0`) on everything it writes; nothing in Perfuse depended on the version being 4.x.

What this does not show: channels authored by hand in each Administrator, which may use settings the corpus does not; and plugins beyond those the images ship.

## AMQP 1.0, against RabbitMQ 4 and ActiveMQ; Azure Blob, against Azurite

**What was found: two protocol defects that the codec's own round-trip tests could not have shown.**

The AMQP 1.0 client (`internal/amqp`) sends, receives and settles against RabbitMQ 4 and ActiveMQ Classic 6.2: three messages, the
third 250 KB and so split across frames; the first released and redelivered; the rest accepted once. A channel reading one queue and
writing another, on each broker, delivered the message with its control id and MSH-9 as the AMQP message-id and subject, and left the
source queue empty.

- **Links were matched by our handle number rather than the broker's.** Each side numbers its own handles, and RabbitMQ's happened to
  agree with ours, so everything passed there; ActiveMQ numbered differently and the second link never saw its attach.
- **A split transfer put the `more` flag in the wrong field**, so RabbitMQ refused the 250 KB message as malformed.

Azure Blob (`internal/azblob`) against Azurite, which checks Shared Key signatures as the service does - a wrong key is refused with
403: put, list, get and delete, a name with a space (first stored double-escaped, `a%20b`, until fixed), and an access tier read back
from the blob. A channel took blobs from one container to another, moving the good one to `processed/` and the unparseable one to
`error/`.

What this does not show: Azure Service Bus itself, whose emulator needs SQL Server beside it; Event Hubs; SAS tokens against Azure.

## SMART Health Links and Cards, against kill-the-clipboard and the specification's examples

**What was checked.**

`scripts/shl-interop.sh` runs a Linux build of Perfuse beside vintasoftware's kill-the-clipboard TypeScript library, an independent
implementation of SMART Health Links STU 1 and SMART Health Cards 1.4. The library's `SHLViewer` resolved a passcode-protected link
Perfuse hosted - embedded, and by one-time location - and got the bundle back byte for byte; a wrong passcode raised the library's own
`SHLInvalidPasscodeError`. The other way, the library built a link with `SHLManifestBuilder`, including a SMART Health Card it issued and
signed, and Perfuse's `/api/shl/resolve` decrypted both files and decoded the card (not verified, correctly: its issuer was plain HTTP).

The SMART Health Cards specification's own example card verifies against the example issuer's published key, from its JWS, its
numeric QR form and its file form; altering one character of the payload stops it verifying (`internal/shl/testdata`, offline).

The QR encoder is written here, so `scripts/qr-check.sh` decodes its output with zbar, an independent reader: 26 codes from 1 to 1,800
bytes at both error correction levels, all read back exactly. The code on the share screen was photographed by the browser test and
read by zbar too.

What this does not show: a patient's phone app reading a Perfuse link over the internet, and verification against issuers in the VCI
directory.

## Eligibility (270/271), claim status (276) and enrolment (834)

**What was checked, and what could not be.**

Every built 270 and 276 is parsed back by Perfuse's X12 reader and passes its envelope check - SE01 segment counts, ST/SE and GS/GE
control numbers, ISA/IEA. The synthetic 271 used in the tests and the browser was itself caught by that check on its first run:
it declared 21 segments and held 23.

The independent check available was pyx12 4.0.0, the open-source HIPAA validator. It accepts the 834 test file as valid against
005010X220A1. It ships no maps for 005010X279A1 or 005010X212, so it could not check the 270, 271 or 276; those were built to the 5010
implementation guide structure and have not been checked against a TR3-based validator or accepted by a payer.

The CORE check is this program's reading of the CAQH CORE Eligibility & Benefits data content rule. It is not CORE certification.

## S3, SQS and SNS, against LocalStack and AWS's signature example

**What was checked: the five AWS connectors end to end, with the message looked for where it should have arrived.**

The signer (`internal/awsv4`, shared by every AWS connector) reproduces the signature in AWS's own worked example from the Signature
Version 4 documentation, and the signing-key derivation vector. LocalStack 4 does not verify signatures by default, so those two
tests are what hold the signature; LocalStack holds everything else.

Against LocalStack: SQS send, long-poll receive and delete; a FIFO queue keeping order within a patient's group and dropping a resend
with the same deduplication id; an SNS publish arriving in an SQS queue subscribed to the topic; S3 listing with `start-after`, a key
containing a space, get with a size limit, copy and delete; and an object written with `GLACIER_IR` reporting that storage class back.
Then two running channels: SQS in, to an SQS queue and an Athena-shaped archive in S3, checked in the destination queue, in the source
queue (empty, so the message was deleted after handling) and in the bucket (one JSON line under `dt=<today>/` with the fields parsed);
and S3 in, where a good object moved to `processed/`, an unparseable one to `error/`, and a `.txt` the suffix excludes stayed put.

The first run found that an unparseable S3 object was moved to `processed/`: the channel reports a message it cannot parse without
returning an error, so the source now checks the outcome as the file source does.

What this does not show: behaviour against AWS itself, IAM policy failures, KMS-encrypted buckets, and the `perfuse athena` table
run in a real Athena.

## FHIR, against HAPI FHIR

**What was found: the converter was fabricating an identifier system, and HAPI did not object to it.**

A converted Patient is accepted by a real HAPI server, and the round trip preserves what matters: the
name, the birth date converted from HL7's `19061209` to `1906-12-09`, and the gender mapped from `F` to
`female`.

The third test is the one that makes the other two mean anything. It posts deliberately invalid FHIR and
requires HAPI to refuse it, because a server with validation switched off would make the first two tests
pass while proving only that a server exists.

## Da Vinci PAS 2.2.1, against the official HL7 validator

**What was found: the 278-to-PAS conversion had never been checked against PAS, and it failed.**

`pas278` converts an X12 `278` decision into a PAS `ClaimResponse`. Until this check, its only evidence was its own tests, which asserted what the code did rather than what the guide requires.

The official HL7 validator (`validator_cli.jar`, loaded with `hl7.fhir.us.davinci-pas#2.2.1` and its dependencies) rejected every output, with six errors for a certification and eight for a pend:

- `patient`, `created`, `insurer` and `request` were missing, and all four are required.
- The adjudication category was not the pattern PAS fixes, `submitted`, and the code used instead, `PASTempCodes#reviewAction`, does not exist.
- A pend answered `outcome: queued`, which is not in PAS's `ClaimResponseOutcome` value set.

Reading the profile and the guide's own published example (`ReferralAuthorizationResponseExample`) found two more things the validator could not see: the reason code named the wrong X12 code list, and `processNote.type` was shaped for R5 rather than R4.

After the fix, a certification, a denial and a pend each validate with **0 errors**. The warnings that remain are the validator reporting that it cannot load X12's code systems, which X12 licenses.

The validator needs Java and a large download, so it is not run by `make check`. `TestClaimResponseMatchesPASPublishedShape` holds each rule it enforced instead, and fails if one is reverted.

What this does not show: that any payer's PAS endpoint accepts these responses. Conformance to the profile is a necessary condition, not a sufficient one.

## CARIN Blue Button 2.2.0 and PDex 2.2.0, against the official HL7 validator

**What was found: five conformance defects in the new converters, and a store that had been dropping most of every ExplanationOfBenefit.**

`cms0057` converts paid claims (an 837 with its 835) into CARIN Blue Button bundles, and Da Vinci PAS decisions into PDex prior authorisations. Both were run through the official HL7 validator (`validator_cli.jar`, FHIR 4.0.1) loaded with the published packages `hl7.fhir.us.carin-bb#2.2.0` and `hl7.fhir.us.davinci-pdex#2.2.0`. The CARIN inputs were synthetic professional, inpatient and outpatient claims; the PDex inputs were the PAS 2.2.1 guide's own example Claim and response bundles, not shapes written here to agree with the converter.

The first run rejected all three CARIN claim types:

- The institutional profiles fix the claim-type coding *with its code system version* (`1.0.1`), and the converter wrote no version.
- CARIN's EOB invariants require `meta.profile` to carry the profile version (`|2.2.0`).
- The CARIN Patient requires the member identifier to have a system. The converter left it out when none was configured; it now refuses to convert without one rather than inventing a namespace.
- The outpatient profile allows a service date, not a period, on a line.
- Two test NPIs failed their check digit. The fixtures were wrong, and the converter now reports a bad NPI in words before US Core rejects it.

After the fixes the validator reports no errors on any of the three CARIN claim types or on either PDex decision. The remaining warnings are a missing narrative (a best-practice recommendation) and NUBC and X12 code systems the validator has no copy of, so cannot check codes in. The only PDex errors on the first run were `example.org` URLs carried in from the HL7 example inputs, which the validator refuses in non-example content; with those replaced it reports none.

The larger finding came from asking whether a converted claim survived the FHIR server. It did not: the store parsed every resource into an internal model and serialised the model, and the model declared a dozen of ExplanationOfBenefit's fields. Items, diagnoses, supporting information, adjudication and payment were dropped on the way in - everything a CARIN claim is for - while the package's own comments said unknown members survived a round trip. The model now keeps whatever a resource carries that it does not declare, without bringing back a declared field the program cleared on purpose, and a test loads a CARIN bundle through the transaction endpoint and reads it back whole.

Asking who could call a Group export found the next gap: any valid token could export any Group, so a provider given a token for its own attribution list could read every other provider's. API tokens can now be limited to Groups (`perfuse token create -fhir-groups`). `TestGroupLimitedTokenReachesOnlyItsGroup` holds that such a token exports and reads its own Group, gets not-found for another provider's, and is refused ordinary reads, searches and `$member-match`; `TestAPITokenGroupLimit` and `TestCreateTokenWithFHIRGroups` cover storing the limit and issuing it through the console API.

Not verified: the member match and Group exports against another payer's implementation, and the metrics against a payer's published figures.

## HL7 v2 to US Core 9.0.0, against the official HL7 validator

Five synthetic v2.5.1 messages (`internal/v2fhir/testdata/uscdi`: ADT^A01 with a diagnosis and an allergy, ORU^R01 with a
specimen, VXU^V04, MDM^T02, SIU^S12) were converted with `-us-core`. Each resulting resource was then validated against US Core
9.0.0 with the official HL7 validator, using the live terminology server. US Core 9.0.0 is the version ONC's 2026 SVAP approves for
USCDI v6.

The first run found that `-us-core` had been claiming almost nothing. Perfuse's own checker knew only US Core Patient, so every
other resource went out without a claim. Validating those resources against the profiles they should have met found errors in
the converter itself:

- A Location's `partOf` named the facility Organization, which is invalid FHIR (`partOf` is another Location). It is now
  `managingOrganization`.
- The facility Organization had no `active`, which US Core requires.
- A Specimen's type was read only from `OBR-15`, which v2.5 withdrew. A 2.5.1 result's `SPM` segment was ignored.
- The sender's code text was written as the coding's display, so LOINC and CVX codes carried displays their code systems
  contradict. It is now the concept's text.
- A LOINC document type in `TXA-2` was labelled as HL7 table 0270.
- `DG1` diagnoses and `AL1` allergies were not converted at all. Race, ethnicity and preferred language were dropped. A phone
  number in v2.5's split components was dropped too.

After the fixes, Perfuse checks every profile the converter can claim. 23 of the 24 resources claim US Core, and the validator
reports **no errors on any resource**. The 24th is an Encounter with no type, so it carries no claim and the conversion notes say
why. The remaining warnings include a missing narrative, observations with no performer, local codes outside extensible value
sets, and an inactive RxNorm code in the test data.

`TestEveryConformingResourceClaimsItsUSCoreProfile` and `TestTheUSCoreValidatorFindingsStayFixed` hold the result without Java.

What this does not show: conversions of messages from a real EHR feed, or an ONC certification test (Inferno) run.

## Da Vinci CRD 2.2.1 and DTR 2.2.0, against the official HL7 validator

The order CRD updates (a DeviceRequest carrying the coverage-information extension), the whole CDS Hooks response validated against
CRD's `CRDHooksResponse` logical model, the example questionnaire against DTR's `dtr-std-questionnaire`, and the server's own
`$questionnaire-package` output against `dtr-qpackage-output-parameters`: no errors. The remaining warnings are a missing narrative on the
order and the HCPCS code system, which the validator has no copy of.

The first run found that cards named their topic in CRD's temporary code system, which the response model's value set does not
include; they now use the CDS Hooks card-type system. Building the package found that the FHIR store's Questionnaire model declares no
`item`, and the package was written through a path that did not restore undeclared members - so every questionnaire went out with no
questions. It now uses the serialiser that does.

**DTR value sets and adaptive questionnaires, October 2026.** Every ValueSet in DTR 2.2.0 and US Core 6.1.0 (44) was loaded and
expanded. The 16 that list their codes expand, and their expansions validate with no errors; the 28 that need VSAC or a whole code
system are refused by name with 422, none expanded partly. Building it found that table-view expansions carried no `timestamp`, which
FHIR requires. `$next-question` was run on DTR's own example input and on a three-step branch, and its output validates against
`dtr-next-question-output-parameters`, the package against `dtr-qpackage-output-parameters`. That run found a package defect older
than this work: a bundle entry's `fullUrl` was the questionnaire's canonical url even when the url did not end in `Questionnaire/{id}`,
which FHIR forbids; it is now the resource's address on this server. Not verified: a real DTR app (such as a SMART DTR client) driving
the adaptive flow.

**ELR, October 2026.** The old ELR builder wrote a message from a handful of fields, with no SFT, ORC or SPM, and nothing used
it; it was removed. `perfuse elr` was written instead, and its output checked with NIST's HL7 v2 validator (`hl7-v2-validation`
1.7.2, the engine behind the ONC ELR tool) against the APHL ELR 2.5.1 Foundation profile that NIST publishes with its ELR validation
plugins. Two sources: a lab message with a CBC and a SARS-CoV-2 result, and a SARS-CoV-2 result with no specimen. The run is
`ELR_OUT=dir go test ./internal/publichealth -run NIST`, then the validator over `dir`.

The first run found 32 errors. Fixed: placer and filler numbers without an assigning authority, an NPI without its OID and identifier
type, race and ethnicity in CDC codes where ELR uses HL7's tables, the phone number in a component ELR does not allow, and no
performing lab on the results. What is left are the lab's own gaps (no specimen type, no time received, both named in the notes) and
findings the profile raises against itself:

- MSH-15 is declared with a cardinality of exactly two, the field's length put in the wrong place ("no valid length specification").
- PID-10 and PID-22 must be coded `HL70005` and `HL70189`, and the coding system table they are checked against lists only the
  placeholder `HL7nnnn`.
- OBX-5's coding system is bound to HL7's table 0396 without `SCT`, while OBX-3 is bound to the ELR table, which has it; ELR
  requires SNOMED CT for coded results.
- SPM-4 is bound to HL7 table 0487 alone, and the specimen was coded in SNOMED CT, which ELR prefers.
- ELR-037 (ORC-12 must equal OBR-16) fails with the two fields identical, as it does on the profile's own sample message.
- Two ARLN program checks that do not apply to this program.

**The ONC ELR tool, October 2026.** The ONC certification tool itself (Electronic Laboratory Reporting HL7 V2.5.1 Validation Tool
1.9.3, formerly NIST's, now at tools.valitheus.com/mu-elr), context-free validation, driven through a browser with the same two
messages. It validates against ELR Release 1 with its errata, not the APHL profile. It found one error the profile does not check:

- **MSH-2 was four characters.** The October 2011 errata to ELR Release 1 made MSH-2 five, `^~\&#` (the separators and the
  truncation character), and the certification tool requires it (ELR-013). Perfuse now writes the five. A state whose own guide still
  shows four can have them with `encoding_characters: "^~\\&"` in the ELR file. The APHL profile accepts either.

After the fix, the message with its specimen is **Valid, 0 errors**. The one without a specimen has two: SPM-4 and SPM-18 missing,
the lab's gaps Perfuse names in its notes and does not fill. None of the APHL profile's own defects above appear in the ONC tool.

**SMART App Launch, October 2026.** Inferno's SMART App Launch STU2.2 suite (smart_app_launch_test_kit 1.0.3) against
`serve -fhir -smart-clients -smart-users`, Perfuse issuing the tokens itself. A script played the browser: it followed Inferno's
authorization links, signed in through Perfuse's forms, chose the patient, approved the scopes and returned to Inferno, and for the
EHR launch opened Inferno's launch URL from Perfuse's `/auth/launch`. With a public client: standalone launch, EHR launch, Backend
Services and token introspection, 80 of 80. With a confidential-symmetric client: 67 pass and 3 omitted (CORS on the token exchange, which
SMART requires only for public clients).

The suite found:

- No CORS headers. A SMART app in a browser reads discovery, the capability statement and resources from another origin. The FHIR
  endpoint and the token, key and discovery endpoints now answer with `Access-Control-Allow-Origin: *`, which is safe because none
  of them reads a cookie; every request carries its own token.
- An app launched from a chart could not read its own user. A token limited to a patient refused the Practitioner named in its
  `fhirUser`. That one resource is now readable; anything else outside the patient is still not.
- Introspection refused Inferno, which sends no credentials unless told to. It now also accepts an active access token from this
  server as the caller's proof, besides a confidential client's credentials; a public client's id alone is still refused.
- A member's `user/` scopes would have reached what a user may see. They are issued as `patient/` scopes instead.

**The Inferno Da Vinci PAS test kit, October 2026.** The official PAS Server v2.2.1 suite (davinci-pas-test-kit v0.15.2, run
locally in Docker) against `serve -pas -fhir-subscriptions`, with Keycloak issuing the SMART Backend Services token: 82 pass, 2 fail,
of 84. Approval, denial, a pended request finalised by a reviewer and delivered on the PAS subscription, the four-step claim update
sequence, inquiries, error handling, and the must-support groups for every profile but ClaimResponse pass. The two failures are the
ClaimResponse must-support checks for `$submit` and `$inquire`. The elements they want that Perfuse does not send are not allowed
there by their own extensions' context in PAS 2.2.1: `adjudication.extension:reviewAction` (its `reviewActionCode` may only appear on
item and addItem adjudications), `extension:authorizedProvider` and `item.extension:communicatedDiagnosis`. Sending them made the
validator reject every response, so they are left out. The rest are `request.extension:DataAbsentReason` (Perfuse always knows the
request) and `addItem.extension:productOrServiceCodeEnd`.

Run again with Perfuse's own authorization server (`-smart-clients`) in place of Keycloak, the kit's backend client registered with
its own public keys and its token from `/auth/token`: the same 82 of 84.

The request bundles were the kit's own, for its client simulation. They are identical for approval, denial and pending, because
Inferno's simulated payer is told what to answer; for a payer that decides from what is asked, the denial asked for dialysis and the
pended request for surgery, and three more requests exercised a quantity limit, an alternative, a request for documents and a line
naming no service.

The suite found:

- The handshake was refused because it arrived about a millisecond before Inferno began listening for it. A refused handshake is
  now retried before the subscription becomes an error.
- PAS's own example Subscription writes its filter as `org-identifier=...` without a resource type, which Perfuse refused.

**The Inferno Da Vinci DTR test kit, October 2026.** The official DTR Payer Server v2.2.0 suite (davinci-dtr-test-kit v0.18.0,
run locally in Docker) against Perfuse with Keycloak as the authorization server for SMART Backend Services: 43 pass, 1 omitted (no
Binary attachments in the data), 2 fail. The two that fail cannot pass for any server: they require
`Questionnaire.extension:assemble-expectation`, which DTR's base questionnaire profile allows at most 0 times, and `item` in the
adaptive search profile, which also has a maximum of 0. The payer data was Inferno's own dinner-order fixtures, an adaptive version of
it written with standard `enableWhen`, and one questionnaire using every element DTR marks must-support.

The suite found these defects, each fixed and covered by a test:

- The FHIR base ignored `-public-url`, so bundle links and the SMART audience named the listen address (`0.0.0.0`).
- A token with read scopes was refused `$questionnaire-package`, `$next-question` and `$expand`, because every POST counted as a write.
- The capability statement did not declare the three DTR operations, though they were served.
- `/.well-known/smart-configuration` lacked `grant_types_supported`, and claimed OpenID Connect sign-in with no `jwks_uri`.
- `$expand` refused a Parameters body, which is how FHIR clients send it.
- Small value sets went into the package unexpanded (DTR oper-15 wants those under 40 codes expanded).
- Library and value set references in the packaged questionnaire were unversioned; they now name the version packaged.
- An unknown CRD context answered 200; DTR requires a 4xx with an OperationOutcome.
- `$next-question` accepted answers its questionnaire does not have, of the wrong type or outside the options; these are now 400.
  It also asked one question per call where the answers already decided more, and its contained questionnaire did not say it was
  derived from the packaged one.

**The Inferno Da Vinci CRD test kit, October 2026.** The official CRD Server v2.2.1 suite (davinci-crd-test-kit, run locally in
Docker) passes as a whole: discovery, the four hooks, the cross-hook and must-support checks, and the technical issues, no member
found, coverage not found and no active coverage responses. It took these fixes:

- **Every signed call was refused.** The CDS Hooks JWT has `iss`, `aud`, `exp`, `iat` and `jti` and no `sub`, and the verifier
  required a subject, as sign-in does. A JWT no longer needs a subject, and a repeated `jti` is now refused.
- **Discovery was wrong.** There was no `davinci-crd.version`, and the configuration options had the request-side name and were empty.
  Discovery now declares `2.2` and a `coverage-info` boolean, and turning it off returns no cards.
- **order-dispatch answered nothing.** Only a prefetch key called `order` was read. Orders are now found from `dispatchedOrders` in
  any prefetch, or read from the EHR.
- **Nothing was ever fetched.** When prefetch lacked the Coverage, the answer was a card. The Coverage is now read from the EHR's
  `fhirServer`. A failure gives `indeterminate` with `technical`, and an inactive or ended coverage gives `not-covered` with
  `no-active-coverage`.
- **Every patient was treated as a member.** With `members: fhir`, the coverage is checked against the payer's records:
  `no-member-found`, `coverage-not-found`, or `satisfied` with the number of an approved authorisation.
- **The updated order changed.** Re-encoding sorted its keys, and Inferno's comparison, which sorts arrays by their JSON text, saw
  an Appointment's participants as modified. The order is now returned byte for byte, plus the extension.
- **A GET of an unknown `/cds-services/...` path answered with the console's HTML.** It now answers with JSON.
- **The example rules used a display that HCPCS rejects.** The HCPCS URI is also inconsistent: HL7 Terminology prefers
  `http://www.cms.gov/...`, while CRD 2.2.1 and CARIN BB 2.2.0 use `https://`. Rules now match either.

The run also turned up two problems in the kit. Its own sample order-select request fails its context check, because CommunicationRequest
is listed in `selections`. Its sample appointment has neither `start` nor `requestedPeriod`, which crd-apt1 requires. Both were
adjusted for the run. encounter-start and encounter-discharge are not served.

**Rechecked in October 2026, after reading implementers' questions in chat.fhir.org's Da Vinci channels.**

- DTR had been built to 2.1.0 while CRD and PAS here are 2.2.1, the releases meant to be used together. Against DTR 2.2.0 the
  package was not conformant: its parameters were `PackageBundle` and `Outcome` where 2.2.0 says `packagebundle` and `outcome`, each
  bundle lacked the QuestionnaireResponse 2.2.0 requires, a Library's own `depends-on` Libraries were left out (the question raised
  about the reference implementation omitting FHIRHelpers), a version-specific canonical returned whichever version was found first,
  `context` was ignored, and `$log-questionnaire-errors` and `$next-question`, which 2.2.0's payer capability statement requires, did
  not exist. All fixed; the output validates against `dtr-qpackage-output-parameters` 2.2.0 with no errors.
- CRD answered an order no rule matched with `info-needed` `OTH` and no reason, which breaks crd-ci-q6: the validator rejected every
  such answer. The earlier check had only validated a matched order. Rules files are now checked against crd-ci-q1, q2, q3, q5, q6
  and q8 when they load.
- CRD 2.2.1's own invariant crd-ci-q4 rejects every coverage-information carrying `doc-purpose` `withpa`, even with `pa-needed`
  `auth-needed`: its left side is a `where()` with no `exists()`, so it is empty, and "empty implies false" is not true. It also tests
  for `noauth` where the code is `no-auth`. Perfuse does not send `doc-purpose` (it is optional), so its output stays valid; the fault
  is reported to the guide's authors.

What this does not show: CRD against a production EHR (Inferno's simulated CRD client is the stand-in), and DTR against a real SMART on FHIR documentation app.

## HL7 DSDR signatures, against OpenSSL and xmlsec1

The signing PKI was all OpenSSL: a CA from `openssl ca`, its OCSP responder from `openssl ocsp`, and a time-stamping authority
from `openssl ts -reply`. Against those, a C-CDA was signed to XAdES-X-L. Independent software then checked the result:

- OpenSSL verified both time-stamps, each over the canonical bytes XAdES says it covers, and the embedded OCSP response.
- xmlsec1 1.3 (libxmlsec) verified the XML signature with the certificate chained to the CA. That covers the XPath Filter 2.0
  exclusion of the signers, exclusive canonicalisation, the signed-properties reference and the RSA signature.
- xmlsec1 refused the document once a dose in it was changed.

The guide was read from HL7's October 2014 publication. The regulation was read from the CMS-0053-F final rule.

Running xmlsec1 against Perfuse's XML signatures for the first time found three faults in the existing signer, which only Perfuse
had ever checked:

- Canonicalisation wrote the line break after the XML declaration. Canonical XML has no text outside the document element, so
  no other verifier reproduced the digest. Now byte-for-byte equal to libxml2's.
- The signed-properties reference named no canonicalisation, so XML-DSig's inclusive default applied while Perfuse digested
  exclusively.
- ECDSA signature values were DER, where XML-DSig carries r and s side by side (RFC 4051). Older Perfuse signatures still verify
  in Perfuse.

xmlsec1 now verifies Perfuse's enveloped and by-ID signatures, RSA and ECDSA (`internal/xmldsig`), and its DSDR signatures
(`internal/dsdr`). Both tests run when Docker is available.

What this does not show: a payer's own DSDR verifier, which none publishes, and a signature from a commercial healthcare CA.

## Hosted FHIR sign-in, against Keycloak

The `client_credentials` preset was run against Keycloak 26: a confidential client created through Keycloak's admin API, the token
Perfuse fetched checked to be Keycloak's and issued to that client, and a wrong secret refused with Keycloak's own error, without the
secret in the message. The `aws` preset signs with the shared Signature Version 4 signer, which matches AWS's published example, for the
service `healthlake` in the URL's region. Not verified: AWS HealthLake and Azure Health Data Services themselves, which need accounts.

## The 006020 275, against the published companion guides

**What was found: one of the two guides contradicts itself about the byte count, and the table is right.**

CMS-0053-F adopts the `006020X314` `275`. Its technical report is licensed by X12 and not held here, so the structure was taken from the two published companion guides for that version: the CMS esMD guide (AR2024.04.0) and UnitedHealthcare's.

- **The binary segment.** Both confirm the `006020` binary segment is `BDS`, with the filter in `BDS01`, the length in `BDS02` and the data in `BDS03`.
- **The contradiction.** The esMD guide's table gives `BDS*B64*53*` for a payload of exactly 53 bytes. Its sample interchange declares `16` for the same payload. Perfuse follows the table, refuses the sample, and a test holds both.
- **The payload.** Both guides show a MIME entity in `BDS03`, and UnitedHealthcare's gives the `ASC` and `B64` layouts. The parser unwraps it and the builder produces it, and a built attachment round-trips byte for byte, including a document containing every X12 delimiter.
- **Where they differ.** The guides disagree on some segments: esMD requires a submitter and `REF*X1`, and UnitedHealthcare lists a service date. The builder satisfies both where it can and sends an optional segment only when given the value. Each rule is recorded in `TestAttachment6020FollowsThePublishedGuides` with the guide it came from.

What this does not show: conformance to the technical report itself, acceptance by any clearinghouse or payer, or an electronic signature. CMS-0053-F also adopts a signature standard for attachments, and none is applied.

## FHIR subscriptions, and the R4 bug they exposed

**What was found: Perfuse's own R4 FHIR server refused Perfuse's own R4 ADT conversion.**

Subscriptions notify when an Encounter changes, so testing them meant writing Encounters to an R4 server, and the first write answered 400. The server converted resources to R4 when serving them and did not convert them back when receiving them. So `Encounter.class`, which R4 writes as a single coding and the internal form holds as a list, could not be read, and it is present in every ADT conversion. The v2-in, FHIR-stored pipeline had never been run end to end against Perfuse's own store.

It is fixed, and two tests keep it fixed:

- one posts an ADT, SIU, MDM and VXU conversion to an R4 server and requires each to be accepted
- one requires everything written as R4 to be read back unchanged

Both fail with the fix removed.

The subscriptions themselves were tested against a real HTTP receiver, both in the Go tests and in a Playwright test that drives the interface. The tests confirm:

- the handshake, and that a client cannot declare its own subscription active
- that notifications are delivered in order and retried
- that one written before a restart is delivered after it
- that a full-resource notification carries the version that changed
- that redirects are not followed
- that the page does not show a subscriber's header value or the token in its endpoint

Removing each protection makes its test fail.

What this does not show: interoperation with any third-party subscriber, such as an EHR or a HIE's notification service.

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

## Hard v2 cases from the FHIR community, against the official HL7 validator

Two deliberately awkward messages posted on chat.fhir.org (`#v2 to FHIR`, September 2026) as tests for v2 converters, and the 70
messages of the public [nw-gmsa/Testing](https://github.com/nw-gmsa/Testing) set (NHS lab, genomics, order and Epic messages),
converted to R4 with the CLI defaults and checked as whole bundles by the HL7 validator against base R4 with the live terminology
server. The first run passed 43 of the 70.

What it found, all fixed:

- **Every `fullUrl` was malformed**: `urn:uuid:` followed by a resource id, not a UUID. Every earlier validator run had checked the
  resources taken out of the bundle, so none of them saw it, and Perfuse's own checker did not look. CDA-to-FHIR bundles had the
  same fault. They now carry a UUID derived from the resource, so they stay deterministic.
- **The same resource added twice** (one practitioner on several results) broke bdl-7 in 13 bundles.
- **Encounters with no class**, which R4 requires; **coding-system placeholder URIs with spaces in them**; and **site identifier
  types labelled as HL7 table 0203**.
- **PDFs read as codes.** Three messages send a Base64 PDF under `OBX-2` `CE`; it went into `Coding.display`, past FHIR's 1 MB string
  limit. A coded value shaped like `ED` is now read as `ED`, and `ED` becomes a DocumentReference the Observation points to.
- **That fix reached 9 of the 28 such messages** (found October 2026, answering a question on the same thread). In 18 the Base64
  holds a space, which MIME Base64 ignores and Go's decoder does not, so a complete PDF was refused as undecodable and then still
  emitted as a Coding with code `Base64` and the PDF as its display. Whitespace is now dropped before decoding: 27 become
  DocumentReferences, and the one whose data is cut short stays text, never a code. The bundles validate with 0 errors.
- **An event that disagrees with `MSH-9`.** A case from the same thread: `MSH-9` A01, `EVN-1` A08, as an engine that remaps A08 to
  A01 leaves it. `MSH-9` decides, and the IG maps no `EVN-1` row, so the disagreement vanished. It is now a warning.

What the two community messages exposed besides, which the validator cannot see because each was dropped or decided without a
note: `PID-4` and `CX.7`/`CX.8` dropped; an ISO OID in the assigning authority ignored; repeats of a text result joined with the raw
`~`; Z segments skipped without a word; `MRG` on an `A08` ignored, and on an `A40` too, so no merge was ever expressed; a time of
birth in `PID-7` dropped; a name type with no FHIR equivalent turned into `old`; an `A08` with a discharge date called in progress;
a bare time read as UTC when `MSH-7` said +10:00; `ug/L` not recognised as UCUM; and `SN` `<>` (not equal) turned into the number.

After the fixes, 64 of the 70 pass. The other 6 fail only on codes the senders sent that the validator cannot find: a UK-edition
SNOMED code checked against the International edition, LOINC answer codes with a typo, and the made-up LOINC codes of a test
message. Those are the sender's codes, and a converter that dropped or changed them would be worse. The two community messages are
in `internal/v2fhir/testdata/hard`, with tests holding each fix.

The five US Core messages above were then validated again as whole bundles, with `-ig` US Core 9.0.0: all five pass. Doing so also
caught a mistake in one of our own fixtures, an appointment reason labelled as HL7 table 0277 (appointment type), now corrected.

## DSDR signatures, against the EU Digital Signature Service

DSDR signatures were checked with the European Commission's DSS demonstration webapp, built from esig/dss-demonstrations and run
in Docker, through its REST validation service. The signature was put back in place of the `sdtc:signatureText` that carries it,
as xmlsec1 is given it. With RSA and ECDSA signers, DSS reports **XAdES-XL**: every layer is recognised and verifies. The
indication is INDETERMINATE / NO_CERTIFICATE_CHAIN_FOUND only because the test CA is on no trusted list. Getting there took
three fixes:

- **Perfuse never checked the time-stamping authority's signature on a time-stamp.** The test TSA did not sign at all, and the
  verifier counted its stamps as XAdES-T evidence, so a forged time would have passed. DSS refused those stamps ("signed by 0
  signers"). The verifier now checks the CMS signature, the messageDigest and the ESS signing certificate, and that the TSA's
  certificate is critically marked for time-stamping and chains to a trusted root. The test TSA now signs as RFC 3161 says.
- **X-L stopped at X.** CertificateValues and RevocationValues carried the signer's path only. They now carry the TSA's certificate
  and its OCSP or CRL as well, and DSS moved from XAdES-X to XAdES-XL.
- **DSS's one remaining warning is a known choice.** The DSDR guide's own `SignaturePurpose` element sits in
  SignedSignatureProperties, where XAdES's schema wildcard is strict. The guide gives the element no namespace, so it is in
  urn:hl7-org:sdtc. The same purpose is also sent as a XAdES CommitmentTypeIndication, which DSS reads.

## The FHIR server, against the Inferno US Core 6.1.0 server suite

The 179 US Core 6.1.0 example resources were loaded into Perfuse's FHIR endpoint. The US Core FHIR API group of ONC's Inferno test kit
(run locally in Docker) was then run against it, with a Perfuse API token. The first run passed 225 tests and failed 121; the last
passed 323. What it found, all fixed:

- **Every multiple-or search matched nothing.** A comma list (`status=final,amended`) was read as one literal value, and a repeated
  parameter was ORed. FHIR defines the reverse, and a test said otherwise, so it was asserting the bug. Commas now OR and repeats AND.
- **POST `_search` answered 415.** FHIR requires search by POST with a form body.
- **The capability statement needed a token.** Clients and certification tests read it before they have one. It is now public; it
  holds no patient data. It also gained `instantiates` us-core-server and each type's US Core `supportedProfile`, and lost an empty
  `interaction` array on ValueSet, which FHIR forbids.
- **`_revinclude=Provenance:target`** was refused on every type, and **`_include=MedicationRequest:medication`** was not offered.
- **Seventeen US Core search parameters answered 400**, among them Condition `asserted-date` and `abatement-date`, Encounter
  `location`, `type` and `discharge-disposition`, CareTeam `role`, DocumentReference `period`, Goal `target-date`, the Location
  address parameters and Patient `death-date`.

The 19 failures left are in the data, not the server. Most are the examples themselves failing current terminology: example.org
URLs, LOINC and CVX displays that have since changed, and a CPT code (99201) that was deleted. The rest are elements no example
carries (PractitionerRole, a data-absent-reason), and an Inferno multiple-or check that looks for values held only by resources
outside the encounter it searched. The SMART launch and granular-scope groups were run later against Perfuse's own authorization
server, below.

## US Core 7 with SMART granular scopes, against the Inferno US Core test kit

**October 2026.** The Inferno US Core test kit (us_core_test_kit 1.1.6, US Core Server v7.0.0, SMART App Launch 2.0.0), run
locally in Docker against `serve -fhir -smart-clients -smart-users`, Perfuse issuing the tokens itself. The data was Inferno's own
reference server patient bundles (740 resources, patients 85 and 355). A script played the browser through Perfuse's sign-in and
consent pages.

- Standalone launch: 20 of 20.
- **Granular scopes 1** (Condition `encounter-diagnosis` and `health-concern`, Observation `laboratory` and `social-history`):
  the launch 20 of 20, and every repeated search returns only what the granted scopes allow, 17 passes and 2 skips.
- **Granular scopes 2** (Condition `problem-list-item`, Observation `vital-signs`, `survey` and `sdoh`): the same, 17 passes and 2 skips.
- The skips are searches the data cannot exercise: no `asserted-date` search, and no `health-concern` or `problem-list-item`
  Condition for the patient.
- US Core FHIR API under a patient token (patient 85): 272 pass, 14 fail.
- The same group as a backend-services client (`system/*.rs`, both patients): 501 pass, 4 fail. All four are one Observation in
  Inferno's data whose `effectivePeriod` has `stop` where FHIR has `end`; Perfuse returns what it was given.

The suite found:

- **POST `_search` needed write access.** A token that could read was refused a search sent by POST. `_search` is now a read.
- **The next page lost the search.** A paged result's `next` link kept `_count` and `_offset` and dropped every search parameter,
  so following it paged through every resource of the type. The link now carries the query.
- **An attachment whose data is absent for a stated reason failed us-core-6.** `_data` holding only a data-absent-reason counts as
  data under FHIRPath, and now does here.
- **The `fhirUser` scope did not let an app read its user.** With only granular Observation scopes, the app could not read the
  Patient it was told had signed in. That one resource is now readable with `fhirUser`.
- **A patient's app could not read what its records point at.** A member's Coverage (whose patient is `beneficiary`) and the
  Organization, Practitioner, Location and Medication referenced by their records answered 404. Now Coverage counts as the
  patient's. The shared directory and drug types can be read by id when the scopes cover the type.
- **`_include=MedicationDispense:medication`** was refused. It is now supported, and stored dispenses are indexed again.
- **Most types were answered without `meta.versionId` or `meta.lastUpdated`.** Only nine types were stamped when stored, so a
  Condition, a MedicationRequest or a Coverage gave a client nothing to put in `If-Match` or a `_lastUpdated` search. Every stored
  type is stamped now, and the sender's security labels, tags and source are kept (they were dropped on those nine).
- **A transaction to the base URL was redirected.** `POST /fhir` answered 307 to `/fhir/`, which most HTTP clients do not follow
  with the body. The base itself is now served.

What is left under the patient token:

- **Searches of the directory itself** (12: Practitioner, PractitionerRole, Organization, Location by name, address or
  specialty), which Perfuse refuses on purpose under a token limited to one patient, so that a patient's app cannot list every
  clinician. The backend-services run shows the same searches pass when the token may see the directory.
- **What patient 85's data does not hold** (2): its one data-absent-reason is on a Condition category no US Core search reaches,
  and its documents name no custodian. Both pass with patient 355 in the backend-services run.

## Public health case reports, against the HL7 validator and HAPI FHIR

The eICR builder had been in the repository for months, reachable from nowhere, and was wrong in every way the validator could
see:

- fullUrls like `urn:uuid:patient-1`, which are not UUIDs;
- references that matched no entry;
- no Bundle identifier or timestamp;
- a Composition with none of the seven sections `eicr-composition` requires;
- a "Reportable Condition" section coded as Social history.

The README listed eCR and ELR as features. Nothing reached either one.

It was rebuilt to work from what a converted v2 message holds. An admission with a COVID-19 diagnosis, a lab result for SARS-CoV-2
RNA, and the same result with no visit number were each checked with validator 6.10.4 against `hl7.fhir.us.ecr#2.1.2`, with
tx.fhir.org for terminology. All three eICRs, and the eCR message wrapping one, have 0 errors. The runs found:

- **Displays that LOINC rejects.** Two section codes used the older "Narrative" wording. The trigger flags copied the sender's text
  as the code's display, the same mistake US Core validation found earlier. A trigger now carries a display only when the value set
  gives one.
- **eCR's absent-reason rule.** eCR fixes the data-absent reason to `masked`, which would say the facility withheld data it never
  had. A missing race is sent as the text "Unknown" instead, and a missing language as BCP 47 `und`.
- **Facility details.** eCR requires the facility's phone and address and the clinician's PractitionerRole, which no v2 message
  carries, so the facility is configuration.
- **An NPI in `XCN.9` was dropped.** That covers both `NPI&2.16.840.1.113883.4.6&ISO` and a plain `NPI`. The practitioner identifier
  ignored the OID handling patient identifiers had, so the US Core and eCR practitioner profiles both failed. It now gets
  `http://hl7.org/fhir/sid/us-npi`. The SSA OID likewise maps to `us-ssn`.
- **`PV2-3`, the admit reason, was never mapped.** The Encounter struct had no reason field it serialised. That field is now there,
  and R5 output carries it as `reason`.

HAPI FHIR (latest) stores each eICR as a document Bundle. HAPI does not implement `$process-message`, so the eCR message itself
was checked by the validator and by a test receiver, not by an agency's endpoint.

### The Reportability Response, October 2026

The other half of the exchange: the agency's answer. Perfuse now reads Reportability Responses (`serve -ecr-responses`) and can
play an agency that writes them (`serve -ecr-agency`). Checked three ways:

- **The IG's own example.** The eCR 2.1.2 RR example (one condition, one agency) reads back as the IG describes it: processed
  with a warning, Zika reportable on the patient's home address within 24 hours, immediate action required.
- **The HL7 validator.** The RR the test agency writes, and the eCR message carrying it, have 0 errors against
  hl7.fhir.us.ecr#2.1.2. The first attempt had four: no Composition.encounter, which the RR composition requires, and then the
  eicr-encounter profile's required location, which the copied encounter must keep along with the resources it points to.
- **Two Perfuse servers.** An ADT with a COVID-19 diagnosis went over MLLP to a hospital server whose `ecr` destination sent the
  eICR to a second server playing the agency. The agency answered and posted the RR back; the hospital stored it at the address
  it had logged when the report went, and the stored RR validated with 0 errors.

The two-server run found a defect unrelated to eCR: **`bearer_token: ${VAR}` on FHIR, CDA and HTTP destinations was sent as the
literal text**, though the configuration's own comment recommends that form; only the S3, AMQP and cloud credentials expanded it.
The agency refused the hospital with 401. Those tokens, and the HTTP destination's password, are now read from the environment.

What this does not show is an agency's endpoint accepting Perfuse's eICR. AIMS onboarding is through APHL and a jurisdiction.

## Vendor-shaped v2 from two national programmes, against the HL7 validator

These are not a live EHR feed; no site has run one through Perfuse. They are the nearest public equivalent: 81 messages from the
French national agency's IHE PAM-FR and document-exchange examples (ansforge/hl7V2-exemples) and the NHS Wales v2 examples
(GIG-Cymru-NHS-Wales/hl7-v2-examples), covering ADT, ORU, MDM, SIU and VXU in v2.3 to v2.5.1. All 81 converted, and the HL7
validator found 34 errors in the bundles. The fixes:

- **Dates that are not dates.** `01/10/1948` and `196203520` became the FHIR dates 0110-19-48 and 1962-03-52. A value that is
  not a v2 DT/DTM, or has no such month or day, is now dropped with a note.
- **Codes labelled with HL7 tables they are not in.** `SPOUSE^^HL70063` was sent where table 0063 says SPO. Codes claiming tables
  0063 and 0131 are now checked against them, as table 0203 already was.
- **Table 0189's letters read as CDC codes.** `N^Not Hispanic or Latino^HL70189` became CDCREC code N, which does not exist. H and
  N now map to 2135-2 and 2186-5.
- **An unsystemed LOINC document type was labelled table 0270.** `TXA|1|18748-4` is now read as LOINC when the code has LOINC's
  form and a valid check digit, and kept with no system otherwise.
- **An appointment with a start and a duration but no end** broke app-4. The end is now the start plus the duration.
- **CVX `3` for `03`** is padded to the form CVX defines.

The one error left was a US Core extension the base-R4 run could not resolve, because that run was made without the US Core
package (see below).

A third set followed: the 139 v2 samples Microsoft ships with its FHIR Converter (microsoft/FHIR-Converter at 70fd328e,
`data/SampleData/Hl7v2`, MIT), written in the shape of US EHR feeds: ADT A01 to A60, BAR, MDM, ORU, OUL, OML, OMG, REF, RRI,
SIU, VXU and LRI lab messages, v2.3 to v2.8. All 139 converted. The validator, this time with US Core 6.1.0 loaded, found 8
errors, and three were Perfuse's:

- **ICD codes without the dot.** `DG1|1|I9|71596^...^I9` is how feeds commonly send ICD-9-CM 715.96; the undotted form is not a
  code in either ICD system. ICD-9-CM and ICD-10-CM codes sent without the dot now get it, with a note.
- **Vital signs labelled laboratory.** Every OBX became category `laboratory`, heart rates included. Results coded with the
  LOINC codes of FHIR's vital-signs profiles are now category `vital-signs`.
- **A repeated numeric value kept with the repeat separator.** `27~25` in a numeric OBX-5 became the text "27~25". One
  Observation holds one value, so the repeats are kept as text, "27; 25", with a warning.

The other five are the samples' own: a practitioner's assigning authority `&2.8&ISO`, which the validator does not accept as an
OID (twice), and a "Bacteria identified" result sent with the LOINC heart-rate code, two numbers and no time, which the R4
heart-rate profile then rejects three ways. Perfuse passes such data through with notes rather than rewriting it.

The bundles also draw one warning per in-bundle reference: entries have `urn:uuid` fullUrls while references are `Patient/id`,
which bundle resolution rules cannot follow inside the file. That is how a transaction is meant to be read (each entry is a PUT
to `Patient/id`, so the reference resolves on the server). The library's `Options.BaseURL` gives absolute fullUrls that also
resolve in the file; `perfuse fhir convert` has no flag for it.

Re-run with US Core loaded, the French and Welsh set is now free of validator errors as well: the US Core race extension the
base-R4 run could not resolve validates.

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

## Every view, before v0.1.4

**What was found: a view could not be linked to, the console was unusable on a phone, and the test suite was overstating what it covered.**

Before v0.1.4, five new probes (`web/e2e/hunt-*.spec.ts`) drove all 24 views. Each probe asks one question:

- **Roles.** Does each view work for a viewer and an editor as well as for admin?
- **Phone width.** Does anything run off a 390-pixel screen?
- **Reload.** Does a view survive a reload?
- **Hostile text.** Does every field survive empty, garbage, wrong-format, Unicode and 400 KB input, and does every primary action then either show a result or say why not?
- **Accessibility.** Do all controls and fields have names, do images have alt text, are ids unique, and does keyboard focus stay visible?

Two more probes read every API route from the Go source. One sends each route malformed JSON, the wrong types, nulls and 3 MB bodies, and requires a 4xx in JSON rather than a 500. The other checks each role guard on the running server: 401 with no session, and 403 for a viewer on every editor, admin and platform route. All 128 guarded routes passed, and no escalation was found.

What they found in the product:

- **No view had an address.** A reload, a bookmark, a shared link and the back button all led to the Dashboard.
- **Every view scrolled sideways on a phone**, for five separate reasons.
- **Revoking an API token that does not exist answered 500**, along with four other unmapped store errors.
- **One route answered in plain text** where every other `/api` answer is JSON.
- **A search in Messages that matched nothing looked like one that never ran.**
- **Repeated SVG ids** could strip the fills from the logo and the flow map.
- **Subscriptions had no way to refresh.**

It also found three defects in the tests themselves. Each one made the suite report something false:

- Six "every view" sweeps had never swept up to four of the views.
- A run refused by the run lock killed the run it was refused for, which then reported 46 defects that did not exist.
- The navigation helper confused a view's own tabs with the console's.

The complete itemised list is in the commit message of `36118b7`.

## Defects found by reading chat.fhir.org

Six months of #implementers, #inferno, #V2, #v2 to FHIR, the Da Vinci streams and others were read for threads where the
behaviour being discussed is something Perfuse does. Each candidate was tested against Perfuse before it was called a defect.
Four were real, and each now has a test that failed before the fix:

- **Date search compared strings, against a Period's start alone** (#implementers, "Period Search"). An Encounter from 29 July to
  2 August was not found by `date=ge1950-08-01`, a time with an offset compared wrongly with one in UTC, and `date=2026` was a
  prefix match. Dates are now indexed as ranges in UTC, Periods whole, and every prefix follows R4. Inferno US Core 6.1.0 FHIR API
  was rerun afterwards: 323 pass, 19 fail and 111 skip, the same as before, with the failures all in the published example data.
- **A pipe escaped in a search value split the value** (#inferno, "Search by identifier containing pipe characters").
  `identifier=MR0909981\|936\|UNIV OF CA` was read as a system and a code and matched nothing.
- **Two contained resources could share an id** (#implementers, "uniqueness of contained resource id"). The resource was stored,
  and `#id` then meant either. Such a resource is now refused, on create, update and in a transaction.
- **A CRD card summary could exceed CDS Hooks' 140-character limit** (#Da Vinci CRD, "CDS / CRD logical model potential gaps").
  The summary includes the payer's rule description. A long one is now cut at a word, and the full text starts the detail.

One more turned up while testing a thread's example (#V2, "Representing exponential numbers in OBX-5"): a unit marked UCUM in
OBX-6.3, such as `{copies}/mL`, was reported as having no UCUM code because it was not in Perfuse's table of common spellings. It is
now kept as a UCUM code, unless it cannot be one (a space outside braces, unpaired brackets).

Checked and found correct: subscription notification Bundles carry `request` and `response` on every entry, history entries'
`request.url` is the interaction rather than the history URL, `$log-questionnaire-errors` pairs questionnaires and outcomes by
position, DTR accepts `referenced`, and bulk export says `requiresAccessToken: true`.

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
- **No payer or clearinghouse has received a 275 built here**, and it was built from published companion guides rather than the X12 technical report. See above.
- **The Inferno US Core SMART groups have not been run as US Core groups.** The SMART App Launch STU2.2 suite they are built on
  has, against Perfuse's own authorization server (below); the US Core kit was not installed, so its granular-scope groups were not.
- **No payer has verified a DSDR signature made here.** The EU DSS validator has, as above, with a test CA.
- **No public health agency, or AIMS, has received a case report from here.** The eICR and the eCR message validate, and the trigger
  codes were the built-in sample, not the RCTC. No state has received an ELR message from here; see the ELR section for what the
  NIST validator does and does not show.
- **No third-party FHIR subscriber has received a notification from here.** Delivery was tested against receivers written for the tests.
- **No site has run production clinical traffic through any of this.** The engine, transports, queue and
  web interface are tested, and the parts that talk to other software are verified as described above.
  That is not the same as having survived a real hospital's Monday morning.

Shadow mode exists precisely so that nobody has to take this document's word for any of it: run Perfuse
beside whatever is in place today, on real messages, delivering nothing, and read the differences.
