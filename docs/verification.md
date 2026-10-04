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

What this does not show: CRD against a real EHR's CDS Hooks client, and DTR against a real SMART on FHIR documentation app.

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
- **No third-party FHIR subscriber has received a notification from here.** Delivery was tested against receivers written for the tests.
- **No site has run production clinical traffic through any of this.** The engine, transports, queue and
  web interface are tested, and the parts that talk to other software are verified as described above.
  That is not the same as having survived a real hospital's Monday morning.

Shadow mode exists precisely so that nobody has to take this document's word for any of it: run Perfuse
beside whatever is in place today, on real messages, delivering nothing, and read the differences.
