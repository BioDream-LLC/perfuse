# Queue

What is left to do in Perfuse, and the decisions that are already settled.

Rewritten from scratch on 2026-08-22. The previous version ran to 1223 lines and a third of its checkboxes were wrong — it
recorded intentions from two days earlier that the work had outrun. The decisions it recorded are summarised here; the
superseded wording itself is not kept, because a queue that carries its own drafts stops being read.

**The rule that produced this rewrite: check the code before ranking work from this file.** In one day, sixteen items listed as
outstanding turned out to be built — including a whole SBOM command I reimplemented before a compile error told me one already
existed. Intent gets written down before the work; nothing revises it afterwards. A grep for the type or command name costs
seconds. Do that first, every time.

---

## Where things stand

**The Mirth-replacement checklist is finished.** All three tiers of migration blockers, the importer, the parallel run, the
equivalence diff, DICOM, HL7 v3, X12, SMART on FHIR, tenancy, the WASM playground, the fleet view, packaging, passkeys, SCIM.
Nothing on the old running order is decisive any more.

What is missing is no longer a feature. **It is the first person other than the author who has ever run this.**

Since then, five features aimed at the reasons clinics find an integration engine painful, and a 208-page reference
manual whose configuration chapters are generated from the source with a test that fails when a key is undocumented.
The manual is served from the running binary as well as built to `docs/manual/`.

**Format parity is the current work, and the shape of it changed once it started.** The goal was that every `dataType`
could filter, transform and script rather than just HL7 v2. What that exposed is that the *engines* were being copied per
format: two filter grammars that were character-for-character identical apart from one keyword, and a transformation engine
about to become three. Both are now single implementations generic over the message type — `internal/expr` and
`internal/steps` — and each retirement found a real defect that the duplicate had been hiding. Declarative steps now exist for
five of the eight formats — v2, v3, X12, NCPDP and delimited — each in its own block, because a step for a pipe-delimited
message and a step for a fixed-width X12 element are not the same type. SCRIPT and raw have none; DICOM has *named* steps
rather than a path writer, deliberately.

**Scripts now run on every format, and the seven empty cells in `scriptSlotsRun` are decisions rather than gaps.** DICOM takes
no preprocessor because an object is binary with pixel data in it and editing it as text corrupts an image; raw takes no
filter or transformer because there is nothing addressable to bind to. 41 of 48 cells filled, and the table is checked against
what the loader actually accepts rather than trusted.

None of that moves the item above. The parity checker in section 4 is the closest thing to it: it does not make Perfuse
trustworthy, it gives a site a way to establish that for itself without taking anybody's word. That is the most a person
who wrote both the code and its tests can honestly build.

---

## 1. The only item that outranks all the code

- [x] **Interop checks against other people's software (2026-09-17).** `scripts/interop-up.sh` starts Keycloak, HAPI FHIR, ActiveMQ
      and an nginx demanding client certificates. Every test skips rather than fails without them, so `make check` still passes on a
      machine with no Docker.

      **Two of the four found defects, and two found nothing.** That ratio is the argument for all four: a check that passes has told
      you something you could not otherwise know, and there was no way to say in advance which two it would be.

      SAML's canonicaliser could not accept any real assertion. The FHIR converter gave an unmapped assigning authority an identifier
      system announcing an OID and carrying a word - malformed per RFC 3061, and HAPI accepted and stored it, so it only surfaced on
      reading the resource back.

      PostgreSQL prompted the third finding: a statement written in the wrong dialect's placeholders used to become a syntax error
      from the server when a message arrived, naming a statement somebody did write. It is refused when saved now.

      Correct with nothing to fix: mutual TLS against OpenSSL, STOMP framing against ActiveMQ including the null-byte case that
      desynchronises a stream, DICOM C-STORE into Orthanc with every identifying tag surviving, and SFTP against OpenSSH rather than
      against the same Go library on both ends.

      The pattern common to both defects: **acceptance is not verification.** A receiver tolerating something is evidence that this
      receiver is lenient, not that the thing is right. Both were found by reading back what the other side stored.

      What each check asserts is worth stating too. The broker test asks ActiveMQ's own management interface how deep the queue is
      before and after, rather than trusting a Send that returned nil - because a TEFCA exchange in this codebase validated its
      inputs, made no network call, and returned success with a tracking identifier built from the clock, with four tests asserting it.

- [x] **The friction report is built, and it is the answer to the item this replaced (2026-09-18).** Activity carries an
      admin-only panel showing what Perfuse has refused to do, grouped by message and counted, plus how far the installation has got.

      The item it replaces asked for one real operator to be handed the binary and watched. That stood at the top of the queue for
      three days and was never going to happen - a fourth way a queue rots, alongside the three now guarded: an item whose unblocking
      condition does not exist.

      What it was for survives. Every claim here about ease of use rests on the judgement of whoever wrote the software, which is the
      circularity that let a thousand self-agreeing SAML tests pass while no real identity provider could sign anybody in. A refusal
      is the one signal from outside that loop, and it needs no observer.

      Hooked into `fail()`, the single function every refusal in the API passes through, so the coverage is the whole product rather
      than the handlers that came to mind - a refusal recorded only where somebody remembered would measure this author's attention.

      Nothing typed is recorded: the route with identifiers replaced, the status, and the server's own sentence. Asserted by a test
      that puts a patient name in a query string and requires it not to appear. Two deliberate exclusions, both tested: a 5xx is a
      fault in this software and logged as one, and an expired session produces 401s that would drown every real finding.

      The funnel names the step that has not happened rather than leaving four timestamps to compare, and every value is read from
      records kept for another purpose - the audit trail, the channel files, the message store. Nothing writes a milestone about
      itself, because a milestone this code records to describe its own ease of use is worth nothing. The state it exists to catch is
      "messages arrive and none has been delivered": a channel can take traffic for weeks and deliver none of it, and from every other
      screen that looks like working.

      Break-verified twice: removing the hook, and removing route normalisation - without which every refusal on a different channel
      is its own finding, nothing reaches a count above one, and the report is accurate and useless.

      Three browser tests drive it. The first version of one used `.first()` on a Refresh button and refreshed the audit trail
      instead, then reported that the refusal never appeared - a locator matching the wrong control reports the absence of whatever it
      was looking for.

      What this still is not: one installation is not a study, and somebody who has seen the code is not a first-time operator. The
      only route to strangers is publishing this where people would find it.

- [x] **A guard for a whole type missing from the builder (2026-09-17).** `internal/api/buildtypes_test.go`, comparing the Go
      transport constants against the browser's type unions in both directions. Break-verified each way. It found two gaps on its
      first run, both real, and a third thing while they were being fixed.

      **A raw socket destination could not be chosen.** `tcp` - Mirth's TCP Sender - was in the server's model with a validator, a
      framing block and its own tests, and the builder never offered it, so the only way to send to a laboratory instrument was to
      write YAML by hand. Now offered, with the framing controls the transport needs and no default framing, because writing with
      the wrong framing does not fail: the far end reads messages split in the wrong places.

      **A DICOMweb destination was offered and could never work.** `dicomweb` was in the browser's union with a label, a hint and a
      three-field form, and no such destination type exists in the server at all - `STOWRequest` and `STOWResult` are declared and
      used by nothing, so the comment saying it stores objects describes a type that stores nothing. Choosing it produced a channel
      the server refused **wholesale**, which reads as the builder being broken rather than as one option that should not have been
      on the list. Removed, along with a set of dead draft fields for a DICOMweb source that no source type could reach either.

      **And the message telling somebody which transports exist named nine of seventeen.** Somebody who mistyped `s3` or `soap` was
      told their transport was not supported, which sends them looking for a missing feature instead of at their own spelling. It is
      generated from the constants now, with its own guard both ways, because a list of names beside the names it describes will
      drift.

      The field guard could not have found any of this and never could: it walks the Go model's paths asking whether the browser
      mentions each, so a missing transport looks like one missing field and an invented transport has no path to walk at all.

- [x] **A Mirth-authored channel export is now a fixture, and Perfuse reads it (2026-09-18).** No Administrator GUI needed in the end,
      though the Xvfb route would also have worked and was the idea that unstuck this.

      `internal/mirth/testdata/real_channel_from_mirth.xml` is built by Mirth's own model classes - Channel, Connector,
      TcpReceiverProperties, HttpDispatcherProperties - and written by its own ObjectXMLSerializer, the class its exports go through.
      Mirth accepts it: posted to a running 4.5.2 it is stored with the description intact and both transport names present.
      `scripts/mirth-author-channel.sh` regenerates it and fails if the server rejects it.

      **Perfuse's importer reads it correctly** - name, source transport and destinations all survive. Three tests, and the
      destinations one is break-verified by making the parser drop them.

      Why hand-writing it could never have worked: Mirth's serialiser is configured to ignore elements it does not recognise, so a
      wrong name produces no error at all. There was nothing to read and nothing to correct against, which is why four attempts failed.
      The four faults, found by comparing the two documents rather than by guessing: HttpDispatcherProperties takes a `host` and not a
      `url`; `resourceIds` holds `entry/string` pairs and not bare strings; `Channel` has no `enabled` element and no setter for one;
      and every connector carries a `destinationConnectorProperties` block of queueing and retry settings that the hand-written file
      omitted entirely.

      Two traps recorded in the script because both cost time. Mirth's connectors live in `extensions/`, not `server-lib` - without
      `extensions/tcp/tcp-shared.jar` the properties classes cannot be instantiated and the connectors are dropped in silence, the
      same failure as the hand-written document reached from the other direction. And Mirth ships a patched Rhino shaded into its own
      jars with a stock `rhino-1.7.13.jar` beside it; `NativeDate` exists in five jars, and if the stock one wins the classpath the run
      dies with `IllegalAccessError` from inside XStream.

      The old fixture is kept, because it is small and carries deliberately unknown elements, but it now says in its own comment that
      Mirth will not load it and names what it gets wrong. A test asserts that warning stays there.

- [x] **Export back to Mirth XML — done, and verified by importing into a real Mirth (2026-09-19).**

  Reachable from the browser: Channels, then **To Mirth**. A dialogue lists what the conversion loses before the file is offered, the
  same list goes into the channel's description for whoever imports it, and a channel Mirth cannot express is refused by name rather than
  approximated.

  **The deferral was wrong in three ways.** It said the work needed a real Mirth to import into, that this was somebody else's task, and
  that nothing had been built. There is a Mirth 4.5.2 container here, nobody else was going to do it, and an exporter had existed since August with
  nine passing tests — every one of which compared its output against this repository's own parser. Mirth threw away everything it
  produced. The doc comment on Export had said "Mirth may or may not accept depending on the plugin" the whole time.

  **Why it stayed hidden.** Mirth does not refuse a channel it cannot assemble. It stores one with the description replaced by "This
  channel is invalid. Verify all required extensions are loaded correctly" and the destinations gone, and returns success. Any check on
  the status code passes against a discarded document.

  **What made it work.** The connector definitions are not written here at all.
  `scripts/mirth-dump-connector-templates.sh` has Mirth's own `ObjectXMLSerializer` emit the defaults for thirteen connector classes plus
  `ChannelProperties`, `Transformer` and `Filter`, and those documents are committed under `internal/tomirth/templates`.
  `mirth.SetRawProperty` then refuses any path the template does not already contain, so an invented element is an error naming the path
  at export time rather than a channel that vanishes on import. The synthesised channel properties were missing
  `attachmentProperties`, `metaDataColumns`, `initialState`, `encryptAttachments` and two `removeOnCompletion` flags — enough on their own
  for Mirth to discard the channel.

  **Faults found and fixed, each break-verified against the server:** the channel carried an `enabled` element Mirth's model has no
  setter for, documented in a fixture comment since August and never propagated to the code; destinations were written as a bare
  `<connector>` with the version only on the source, and an existing test asserted that bare form, holding the defect in place; nested
  property elements lost their attributes because the importer flattens to dotted paths that cannot carry `class="linked-hash-map"`.

  **A gap found by breaking it:** Mirth accepts an unknown `transportName` without complaint. A source set to "TCP Transmitter", which
  Mirth has never heard of, still stored as valid — XStream resolves the properties class and takes the transport name on trust, so the
  mistake would surface only on deploy. The transport names are now asserted explicitly.

  Ten transport pairs are verified by importing into the running server: MLLP, TCP, HTTP, file, database and DICOM in both directions,
  plus SMTP, SOAP and JavaScript destinations. Four API tests, three browser tests, and a control in each live suite that requires a
  chosen string back, because the other assertions test for the absence of a sentence and an absence proves nothing unless the same test
  proves the document arrived.

  **What is deliberately not done.** Filters, transformations, scripts, contracts, shadow comparisons, attachment extraction and mapping
  tables do not convert, and each is reported rather than approximated. A mechanical translation of a Perfuse expression into Mirth
  JavaScript would be a guess at what the expression meant.

---

## 3. Genuinely unbuilt

Verified against the code on 2026-08-22, not read off a checkbox.

### SAML 2.0 — supported, apart from verification against a second identity provider

**2026-09-17.** Somebody can sign in with SAML and configure it entirely from the browser. What remains is
verification rather than code.

Done: the two endpoints (`/auth/saml/start`, `/auth/saml/acs`), request binding through `InResponseTo`, a file
format with validation, `PUT`/`POST /api/signon/saml` and its test, the `-saml` flag, a third tab on the
sign-on screen, a sign-in button that coexists with the OIDC one, and the security chapter. Twenty-three Go
tests and two browser tests.

Two security properties were missing rather than merely untested, and both are now closed with break-verified
guards: a certificate carried inside a response could have been trusted by a later edit, and a response was
tied to nothing, so a valid one was a bearer token for whoever held it.

Six tests were added for the differences between providers, and they are explicitly **not** the verification below:
every response in them is one this codebase built, so they prove the attribute handling copes with documented
vendor shapes, not that any vendor emits what its documentation says. Claim URIs match by final segment, several
values resolve upward to the most privileged, capitalisation is ignored, a near-miss attribute name is not
matched, a missing email attribute falls back to the NameID with a domain prefix stripped, and a comma-joined
group string is recorded as deliberately not split.

One of those six failed on first run and the code was right - three subtests shared an assertion id, so the
second and third were correctly refused as replays. The failure message I had written blamed group matching,
confidently and wrongly, which is worth remembering: an assertion naming one cause when several are possible
sends the next person to the wrong place.

What is left is verification, and the plan below always said that was most of the work. It is now two countable
items rather than a paragraph:

- [x] **Verified against Keycloak (2026-09-17), and it did not work.** Docker installed via colima, Keycloak 26
      configured by `scripts/keycloak-saml-setup.sh`, Perfuse started against it by `scripts/saml-verify-serve.sh`,
      and a real browser sign-in driven by `web/e2e-saml/keycloak.spec.ts`.

      **The first real assertion failed outright**, with `digest mismatch (document was modified or
      canonicalization differs)`. The canonicaliser dropped namespace prefixes: Keycloak writes the assertion as
      `saml:Assertion` and declares `xmlns:saml` on the root, and the canonical form came out as
      `<Assertion xmlns="...">` - a different document, so no signature could ever verify. **No real identity
      provider could have signed anybody in.**

      Two causes, both in one line. The prefix was reconstructed by looking the namespace URI up among the
      declarations in scope, and that URI was bound to two prefixes at once - `saml` inherited from the root and
      the default declared on the element itself - so it found the wrong one. And the lookup ranged over a Go map,
      making it **non-deterministic**: the same assertion could have verified on one attempt and failed on the
      next, which is a worse failure than being consistently wrong. The prefix is now recorded as written, read
      from the source using the decoder's input offset, because Go's XML decoder resolves prefixes and discards
      them.

      A second place dropped it too: `removeSignature` clones a node by listing fields, and the field it did not
      copy was the one just added - the tree was right and the clone the digest was taken over was not.

      Five canonicalisation tests passed throughout all of this, because each signed and verified with the same
      code. That is the lesson worth keeping: **self-agreement is not evidence**, and no amount of it would have
      found this.

      The captured assertion is committed as `internal/saml/testdata/keycloak-assertion.xml` with four tests over
      it, so the regression is caught without a container runtime - a check that needs Docker is a check that
      stops being run. Break-verified: restoring the reverse lookup fails three of them.

- [x] **SAML verified against Microsoft Entra, including a real sign-in (2026-09-19).**

      A real tenant, a real user, a real assertion, and a session created in a running Perfuse:

      ```
      msg="SAML login" role=admin created=true idp=https://login.microsoftonline.com/00000000-.../saml2
      ```

      The security-critical path was right first time. Signature verification, the tie between response and request, the conditions and
      the audience all worked on the first real assertion Entra ever sent this code. What went wrong was everything around it, and none of
      it would have been found any other way.

      **The fixture is the lasting part.** `internal/saml/testdata/entra-response.xml` is a document Microsoft signed, and it covers what
      Keycloak's cannot: Entra writes `<Assertion>` and `<Signature>` with no namespace prefix, Keycloak writes `<saml:Assertion>` and
      `<dsig:Signature>`. Those are the two opposite cases in canonicalisation, and the defect found in August was in prefix handling - so
      one fixture alone covered one side of it. A test asserts both documents keep covering their own case, because a future capture that
      happened to be prefixed would silently reduce the coverage.

      Unlike the Keycloak test, which checks how far an expired document gets, this one pins the clock to the instant of capture and
      requires the whole path to succeed. Five tests: full verification, the request tie, expiry, the wrong audience, and Keycloak's
      certificate against Entra's document.

      **What real Entra does that a reading of the specification does not predict:**

      - It sends **no email claim at all**. The assertion carries displayname, identityprovider, objectidentifier, tenantid and name, and
        nothing else. A configuration expecting an email address gets nothing.
      - Its **NameID is an opaque persistent identifier**, not an address.
      - It sends **no groups claim** unless the application is explicitly configured to emit one, so a role mapping written against groups
        refuses every sign-in. The first attempt here did exactly that.
      - When groups *are* emitted they are **object GUIDs**, not names, unless the tenant syncs from on-premises Active Directory. Mapping
        a role to the string "perfuse-admins" would not match either way.
      - Its claim URIs use **`schemas.microsoft.com/identity`** where the common examples use `schemas.xmlsoap.org`. This one works only
        because `samlFirstAttr` compares by URI suffix; a stricter comparison would look more correct and lose the display name for every
        Microsoft tenant. There is now a test saying so.

      **Two defects found by looking at the users screen after signing in, not by reading code:**

      - The account was created as `99d-dljf9omwoabcgx3kpkzm22vvzapha5ktwctmnc4`. `samlUsername` derives a readable name from an email
        claim and Entra sends none, so it fell through to the NameID - which is what would then appear beside every channel that account
        changed. It now reads the user principal name from the name claim. Break-verified: removing the lookup reproduces the hash exactly.
      - The account's `auth_source` was **`oidc`**. There was no `AuthSAML` constant, and the store derives the source from the issuer,
        which cannot work: a SAML issuer and an OIDC issuer are both https URLs. The comment on `sourceForIssuer` said hardcoding OIDC
        "was wrong the moment a second kind of external identity existed" - and it was wrong again for the third. A site running both could
        not tell its accounts apart, which is the question that matters when somebody leaves.

      **Automation, in `internal/saml`, skipped without credentials.** A Graph client provisions a non-gallery application, sets SAML mode,
      the identifier and reply URL, and adds a signing certificate, then deletes everything. Three faults of my own on the way, all the
      same family: a template id written from memory rather than looked up; a wait that polled a read when only a write would do, because
      an object in Entra is **readable before it is writable** (measured: GET 200 and PATCH 404 on the same id in the same second); and a
      cleanup that treated 404 as success and therefore silently did nothing, leaving an application in the tenant on every run while every
      test passed.

      `scripts/entra-signin-setup.sh` and `scripts/saml-capture.py` do the interactive half, which cannot be automated because Entra will
      not issue an assertion without a human.

The honest description is now "verified against Keycloak and Microsoft Entra, including a real interactive sign-in
against each". That sentence used to read "implemented against the specification and its own tests, not yet proven
against a product", and the distinction turned out to matter more than it sounded: the first provider found a
canonicalisation defect that a thousand of this package's own tests could not, and the second found five behaviours no
reading of the specification predicts. Every difficult part of SAML is a place where a vendor differs from the standard.

Okta or ADFS would be a third data point rather than a gap. Two independent vendors, one prefixing its XML and one not,
is enough to say the implementation is not shaped around a single product.

### The plan this was built from, kept for the verification half

**Corrected 2026-09-17.** This section read as though SAML had not been started. `internal/saml` is 1,264 lines with 1,068 lines of
tests, and it covers the attacks listed below: signature wrapping three ways, entity expansion, XML depth, replay, expired and
not-yet-valid conditions, wrong audience, destination mismatch, empty and whitespace NameID, and five canonicalisation cases. They
pass.

**It has no callers.** OIDC has eight importers, LDAP six, SAML none — not in `cmd/`, not in the API server, not in the store. Nobody
can log in with SAML. Every other stale entry in this queue overstated the work remaining; this one hid finished work behind a plan,
which is the more expensive direction to be wrong in, because it invites somebody to write it twice.

What actually remains: wiring (an assertion consumer endpoint, session creation, role mapping, and configuration reachable from the
browser rather than only from a file), then the two-identity-provider verification described below.

One security invariant was unguarded and now is not. A certificate carried inside a response must never be trusted, or an attacker
signs a response with their own key, embeds the matching certificate, and becomes any user they name. The code got this right and said
so in three comments; there was no test. `internal/saml/embeddedcert_test.go` now proves it, break-verified by making the verifier
prefer `KeyInfo` — which authenticated `attacker@example.com` as an admin. A comment is not a guard: reading a certificate that is
sitting right there is the obvious thing for a later edit to do, and every existing test would have kept passing.

Queued deliberately on 2026-08-22, explicitly rather than skipped. It is wanted. The reason for the delay is written
down so nobody later mistakes it for lack of interest.

**Why it is harder than OIDC.** With OIDC an entire class of attack was removed by refusing to support HMAC signatures at all —
algorithm confusion needs a symmetric algorithm to confuse the asymmetric one with, so if the code cannot be asked for HMAC
there is nothing to confuse. **SAML offers no equivalent move.** The dangerous surface *is* the document parser, and it cannot
be declined.

Each of the following has broken real products:

- **XML canonicalisation.** A signature covers a canonical form, so the verifier must reproduce byte-for-byte what the signer
  produced: namespace inheritance, attribute ordering, whitespace, comments. Exclusive C14N with and without comments are
  different transforms.
- **Signature wrapping (XSW).** Keep the genuine signed assertion where the verifier looks for the signature, add a forged one
  where the application reads the identity. Every mitigation depends on verifying and reading **the same node** — resolve what
  was signed by reference and process that, never re-query the document.
- **Entity expansion and external entities.** Already closed for CDA and verified by sending payloads, so the groundwork
  exists, but the SAML path must use that same hardened reader rather than a fresh one.
- **Certificate handling.** Which certificate signed this, is it the one configured for this IdP, and is a certificate embedded
  in the document ever trusted on its own? It must not be.
- **Replay and conditions.** `NotBefore`, `NotOnOrAfter`, `AudienceRestriction`, `InResponseTo`, one-time use of the assertion
  id. Skipping any one turns a captured response into a reusable credential.

**Verification plan, not optional: at least two real identity providers.** One lets an implementation match a single vendor's
quirks and call it a specification. Keycloak runs locally; the second should be hosted and commercial (Okta or an Entra
developer tenant) because that is what customers have. Plus deliberately malformed responses — unsigned, signed by the wrong
key, wrapped, expired, wrong audience, replayed.

**Sequencing: its own session, nothing else in flight.** Budget it as larger than OIDC and LDAP together, most of it in
verification rather than in writing the code.

### FHIR breadth, and a version question underneath it

Bigger than the items below, smaller than SAML. Added 2026-08-22 from an honest look at where Perfuse fails rather than from a
feature list.

- [x] **FIXED (2026-08-30, and more thoroughly than the recommendation below): the server declared R5 and served R4 shapes.**

      Both halves of the fork were taken, not just the cheap one. `fhir.DefaultVersion` is now `R4` with
      `TestDefaultVersionIsNotUsedAsAnOutputDefault` and a test asserting the constant matches the shape the structs actually
      have — so the honest short answer is in place. **And option 3 was built anyway:** `MarshalVersioned` upgrades R4 field
      names to their R5 equivalents on the way out, covering the three confirmed cases below, so a client that asks for 5.0.0
      gets R5-shaped JSON from R4-shaped structs.

      Verified rather than assumed, because an implementation with no caller is indistinguishable from a feature that does not
      exist (section 7). `internal/fhirserver/server.go` has exactly two response-marshalling paths and **both** are
      version-aware — `MarshalVersionedIndent` for a single resource at line 917 and `MarshalBundleVersionedIndent` for a
      bundle at 930. That was the failure mode worth checking: a version-aware read path beside a plain search path would
      return R5 for a read and R4 for a search, which is harder to diagnose than being wrong consistently. `handleCapability`
      resolves the version per request, so the statement describes what that client will actually receive.

      The original three-way analysis is kept below, because the reasoning for *why* R4 is the honest default has not changed
      and a future session may be tempted to flip it back to "latest wins".

      `fhir.DefaultVersion` is R5, deliberately and with a good comment — R5 is the latest published release, R4 is selectable,
      and R6 is refused by name because it is in ballot. That part is right and better than I assumed when I started writing
      this bullet.

      But the six US Core resource types I added earlier tonight in a later change are **R4 shapes**, because US Core is R4-based,
      and nothing reconciles the two. Confirmed cases:

      - `MedicationRequest.medication[x]` — R4 splits `medicationCodeableConcept` and `medicationReference`; R5 collapses both
        into a single `medication` as a `CodeableReference`.
      - `Procedure.performed[x]` — R5 renamed it `occurrence[x]`. `Procedure.reasonCode` likewise became `reason`.
      - Probably also `AllergyIntolerance.type` (code in R4, `CodeableConcept` in R5) and `DocumentReference.context`.

      **Why this is the dangerous kind.** A client reads `fhirVersion: 5.0.0` from the capability statement, parses
      `medication` as a `CodeableReference`, finds nothing, and renders **an empty medication list**. Not an error — an empty
      list, which in a clinical app is indistinguishable from a patient who takes no medications. Same shape as the two
      defects in section 7, and it violates the standing rule that a capability statement which overstates is worse than none.

      **The fix is a real fork and needs a decision:**
      1. Default to R4. One line, matches US Core and what every EHR actually consumes, contradicts "FHIR must be latest".
      2. Make the structs R5-shaped. Correct for the declared version, abandons US Core conformance, and US Core is the thing
         American clinical apps and CMS rules require.
      3. Version-aware marshalling — serve either shape from one struct, driven by the negotiated version. Correct, and much
         the largest of the three.

      My view: **2 is wrong, and 1 is the honest short answer** — default R4, say so in the capability statement, and keep R5
      selectable for a client that asks. 3 is the right end state and should not be attempted the same night as the decision.

- [x] **Not fifteen. 126 stored resource types, and they now have a guardrail (2026-08-30).**

      The entry below said fifteen of roughly a hundred and fifty. `resourceConstructors` has **126**, and it is the single
      source the router and the capability statement both derive from — itself a fix for a switch and a slice having drifted so
      that six types were advertised and then 404ed.

      **What was actually missing was not resources, it was a check.** Nothing walked the registry. Adding a line to that table
      makes a type advertised in a document clients cache and diff, at the same instant, and nothing verified as a set that any
      of the 126 could be read back. A bad json tag, or a name not matching its `ResourceTypeName`, would be promised in the
      metadata and fail on first use. `internal/fhir/registry_test.go` now asserts every registered type round-trips through
      this package's own marshaller, that the advertised list matches the registry and stays sorted, and that the clinically
      essential types are present by name — because 126 of 145 would still be useless if the missing nineteen were Patient,
      Observation and MedicationRequest.

      **Writing it found the interesting thing, which turned out not to be a defect.** `ValueSet` and `ConceptMap` are absent
      from the registry, which looked like the terminology work being marked done while the resources it operates on could not
      be read. They are *projected* from the channel mapping tables instead: operation routes, no storage, and a capability
      statement that says so with an empty interaction array and a comment that claiming read would be "a promise answered by a
      404". The test was wrong and the server was right. `TestTheProjectedTerminologyTypesAreNotStored` now guards it, because
      the obvious reaction to noticing they are absent is to add them — which would create two sources for one resource, a
      `$translate` answered from the tables while a read returns a POSTed copy, and both answers look reasonable alone.

      The fifteen are the right fifteen — US Core's core, which is
      what a clinical app reads first. But an app that needs `Encounter`, `CarePlan`, `Coverage` or `ServiceRequest` hits a
      404, and the capability statement correctly tells it so. **Which ones come next is a question about who the client is**,
      and worth answering deliberately rather than by adding whatever is easiest.

      **UPDATE 2026-08-24:** Expanded to 127 resource types — near-complete FHIR R4 coverage (146 defined in spec,
      127 implemented with typed structs, interface methods, search parameters, and indexing). All modules covered:
      foundation/infrastructure, security, clinical, financial, workflow, research, and specialized.
- [x] **The nineteen FHIR resources that are deliberately absent (decided; restated as an item 2026-09-17).** 127 of the 146
      types in R4 are implemented. The remainder is the pharma-manufacturing and research-definition tail — `SubstanceProtein`,
      `SubstancePolymer`, `ResearchElementDefinition`, `CatalogEntry`, `DocumentManifest` and their neighbours. No hospital
      integration engine needs them, and adding them would move the count without moving the capability.

      Written as a closed decision rather than left as a sentence in the middle of another entry, which is where it was. Prose
      under a heading is the third kind of decay this file has shown: a stale checkbox overstates the work, an item recorded only
      in a commit message cannot be found, and remaining work written as prose is not counted by anything. A reader scanning for
      open boxes saw neither this nor the SAML verification above it.

- [x] **Terminology operations built (2026-08-23).** `$translate` over ConceptMap (from mapping tables), `$expand` and
      `$validate-code` over ValueSet (from both built-in converter mappings and channel tables). Also exposed the converter's
      eight built-in mappings as FHIR terminology resources.
- [x] **US Core profile validation on write (2026-08-23).** A resource claiming US Core Patient conformance is checked against
      the published mandatory elements. An unknown profile is reported as unchecked at warning and the write succeeds; it is
      never silently passed as conformant.

### The small ones

- [x] **JavaScript Reader source (2026-08-24; reachable from the builder 2026-08-30).** Runs a user-supplied script on a timer
      and feeds returned strings into the channel. Same goja sandbox as transformers, configurable poll interval and timeout.
      Equivalent of Mirth's JavaScript Reader.

      This entry existed twice for six days — once open with its text struck through, once closed — which is the stale-entry
      shape that let section 5 tell a reader to delete a package that had been deliberately rebuilt. Struck-through text is not
      a closed item; it just looks like one.
- [x] **FHIR search modifiers (earlier session).** `:exact`, `:contains`, `:missing` implemented.
- [x] **`_include:iterate` (2026-08-23).** Implemented with version-agreement guard.
- [x] **`$everything` (2026-08-23).** Patient/$everything with compartment derivation from SearchParams.

### Format parity: what is done and what is left

The engines are shared now, so each remaining format is a binding rather than an implementation. Roughly fifteen lines for a
filter, ninety for the steps.

- [x] **One filter grammar (`internal/expr`), generic over message type.** Retired the duplicate v3 evaluator, 712 lines to
      250. Faster than before the work started: 168ns against 182ns, and fewer allocations, because the presence path
      allocates no slice. Three genuine format differences are collected in one `Semantics` struct rather than scattered.
- [x] **One transformation engine (`internal/steps`), generic over message type.** X12's binding is 95 lines where the engine
      was 468.
- [x] **X12: filter, destination filter, `when`, transformations.** All proven end to end with real 837s.
- [x] **NCPDP: filter, destination filter, `when`, transformations.** Addressed by the standard's own two-character field
      identifiers. Proven end to end with real D.0 transmissions.
- [x] **The builder drift guardrail now compares qualified paths, and the gap it measured is closed (2026-08-29).** The old
      test compared yaml key *names*, so a nested key counted as covered whenever the same name appeared anywhere else in the
      model — `x12.transformations` was excused by the top-level `transformations` for as long as X12 has had steps. Comparing
      paths found the real gaps and they are now all closed: an HTTP source's TLS, acknowledgement and `max_message_size`;
      client TLS on the HTTP and SOAP destinations; the map step's shape, which would have emitted a channel the loader
      *refuses*; and the whole JavaScript Reader source, which this file had listed as built since 24 August while the form
      could not configure it. `knownBuilderGaps` is kept at zero so the next setting added without a form field fails the build.
      **The first version of that list said 97 and 63 of those were a bug in the test itself** — it skipped embedded structs
      whose type name is unexported, and the builder embeds several of those deliberately to share fields. Recorded in section
      7, because the shape is worth remembering: one verified sample is not a verified list.
- [x] **The React form renders every field the model has (2026-09-17).** `knownFormGaps` is down to two entries and both are
      documented as deliberate rather than debt: `source.http.tls.serverName`, which is meaningless on a listener, and
      `contract.checkEvery`, which is set where contracts are authored rather than in the channel builder.

      Fifty-four fields in total, counting the seven the narrowed search revealed. Four whole features and one whole destination
      type turned out to be unreachable or broken rather than merely missing a control - see the commits of 2026-09-16 and
      2026-09-17.

      Superseded detail below.

- [x] ~~**The React form still has to render the fields the model gained.**~~ (done; see above) The drift test reads the Go build model, so it can
      say the channel file is expressible and cannot say a human can reach it. That is the remaining half, and it is genuinely
      unmeasured — there is no guardrail tying form controls to model fields.
- [x] **Delimited. The design decision was already made, in the code, with a stated rule (confirmed 2026-09-16).** The blocker
      recorded here was that one document becomes many messages, so what a step or a filter sees had to be settled first. It is
      settled, in `internal/engine/delimitedchannel.go`, and the rule is stated in a comment beside the implementation:

      **With split on, everything runs per row** - declarative steps, filter, transformer script - which is what makes a path mean
      this row's column rather than the first row's. **With split off, the message is the whole document**, and `delimited.Set`
      refuses a write across rows, so a script gets the same refusal a step would rather than silently writing to row one.

      Asserted in `internal/engine/pathscriptjs_test.go` around line 206, where split is on and a filter is proven to run per row.

      The eight format options were the actual gap and are now built (see the delimited commit of 2026-09-16). Written up in the
      formats chapter, and the per-row rule is now written there too - it had existed only as a comment next to the code, which is
      the wrong place for a rule somebody needs before writing a filter.
- [x] **SCRIPT declarative steps (2026-08-31).** A `script:` block carrying `hl7v3.Step`, applied before the scripts on the tree
      the script stage already parses.

      The estimate that used to be here was wrong twice over, and both are worth keeping. It said a new xtree resolver of about the
      size of the v3 binding: `internal/eprescribe` already borrowed `hl7v3.Path` for its channel filter, with a comment saying v3
      and CDA use the same grammar, so a third step type would have been a third set of answers to what `//a/b[2]@c` means. And it
      assumed the work was the resolver, when the work was the wiring — the step engine, the block refusal, the re-serialise, the
      form.

      Two things it found. A channel with steps and no transformer script delivered the bytes that arrived: `runTreeScriptStage`
      only re-serialises when a transformer ran, having no way to know the tree changed underneath it, so the steps applied to a
      tree nobody serialised. And a `script:` block on an HL7 channel was accepted, because the per-format block refusals are
      written one arm at a time in a switch on `dataType` and a new block has to be added to every arm. The check for this one sits
      outside the switch, which is the form to copy.

      `nullflavor` is refused on SCRIPT and withheld from the form rather than offered and rejected. It states why a *v3* value is
      absent and a prescription has no equivalent.

- [x] **The per-format block refusals are a table. Already done, and the entry was stale (confirmed 2026-09-16).**
      `internal/config/formatblocks.go` holds it: one row per block with its owning type, and every other type refused by
      construction. `TestEveryFormatBlockIsRefusedOnEveryOtherType` asserts the exhaustiveness rather than trusting it, and passes
      per pair.

      Worth recording what the table found when it was written, because it measured the cost of the old shape rather than assuming
      it. A probe loading every block against every data type found **eight wrong acceptances**: an `ncpdp` block accepted on hl7,
      dicom, raw, delimited and x12 channels; a `delimited` block on dicom and x12; an `hl7v3` block on x12. Two arms had no block
      refusals at all — hl7v3 returned early and x12 fell through to the envelope checks. Every one of those loaded, validated, and
      would have done nothing.

      The bespoke messages survived the move, deliberately. A generic "wrong data type" would be correct and would throw away the
      explanations, and the explanation is the useful part: telling somebody a v3 block on a SCRIPT channel is wrong because both
      are XML addressing entirely different element trees is what stops them trying the next XML-shaped thing.

- [x] **WebAssembly scripts via wazero (2026-08-31).** A third runtime, and the only one bounded by construction rather than by
      cooperation: goja is interrupted between statements and gopher-lua between instructions, both relying on the interpreter to
      check, while a module is abandoned when its context expires whether it cooperates or not. A test proves that with a module
      whose whole body is an empty loop — no syscall, no allocation, nothing the host could use as a checkpoint.

      The argument for it is not speed or sandboxing. It is that some transformations already exist as code somebody trusts — a
      Rust crate that parses a supplier's dialect, a Go package the integration team maintains — and rewriting those in Lua is how
      a migration stalls.

      **The interface is stdin and stdout.** The alternative was an exported function over linear memory, which is faster and
      requires every module author to agree with us about allocation, string encoding and who frees what. For one transformation
      per message that is not worth an ABI each toolchain implements slightly differently. What the output means follows the
      existing verdict rules: a filter writes `true` or `false`, and empty output from a transformer means unchanged — not an exit
      code, because a module exiting non-zero has failed and a filter rejecting a message has not.

      **The field holds a path, not source.** A module is compiled output, two megabytes from anything built in Go, so it names a
      file resolved relative to the channel like `scripts.include` already is. That overloads `scripts.filter` — source in Lua, a
      path in wasm — which is the shape this project treats as a defect elsewhere. It is acceptable only because
      `scripts.language` is mandatory to reach it and sits beside it, and because the loader refuses anything that is not a
      readable module. A Lua script left behind after changing the language is refused by name.

      **Refused on the path-addressed formats**, and this was nearly shipped as a defect. The dispatch in `RunPathScript` had a
      Lua arm and a default arm running goja, so a module on an X12 channel would have been handed to a JavaScript engine and its
      bytes evaluated as source. The arms are named now and the refusal is tested. The reason is real: a path binding is host
      functions, and stdin is what lets a module need no agreement about calling conventions.

      **The module is compiled when the channel loads, not on its first message** — corrected the same evening, because the first
      version did the latter and the note recording it was an instruction to work around a bug. Compiling is hundreds of
      milliseconds for a Go-built module and seconds under a race detector, and the script timeout covered it, so a channel with a
      good module and a sensible budget failed its first message and worked forever after. That is a fault nobody diagnoses
      correctly, because by the time anyone looks the channel is healthy.

      The problem underneath was quieter and mattered more: a module whose preamble was right and whose body was not passed load
      and failed on the first message, so `perfuse check` said the channel was fine, the deploy succeeded, and the first patient's
      message was the test. In the one feature whose entire argument is bounded, predictable execution, that was indefensible.

      Compiling at load also made a leak certain, so `Scripts.Release` now runs from `Channel.Stop` after the undeploy script. A
      wazero module is mapped executable memory Go's collector does not account for, so a reloaded config would have grown by one
      runtime per module — unbounded, in a process meant to run for months, and invisible in the memory figures anybody would think
      to check.

- [x] **DICOM named steps (2026-08-31).** Deidentify, strip private tags, set AE titles, set institution — applied before
      delivery and re-encoded in the syntax the object arrived in.

      The steps already existed. `internal/dicom/steps.go` and `transform.go` had carried the four actions, `dicom.Apply` and
      their tests for a while, and **nothing referenced any of it** — five hundred lines with no caller, which section 7 of this
      file names: an implementation with no caller is indistinguishable from a feature that does not exist. So this item was
      wiring, not writing, and that is the third time in one session an estimate was wrong the same way.

      Named actions rather than a path writer, deliberately and permanently. An object is binary with the pixel data inside it, so
      a general tag writer produces an image that opens and is wrong rather than a message that is rejected — and whoever reads
      the study cannot detect that. Each action knows which tags it touches and none can reach the pixels.

      Two things found while wiring it. A `dicom:` block on another data type was accepted, same as the `script:` one was, because
      the per-format refusals are written one arm at a time. And `wireToDraft` did not read the steps back, so opening an existing
      imaging channel in the form and saving it dropped its transformations silently — which for a de-identify step means the next
      object leaves carrying the patient, with a successful save and a valid file to show for it.

- [x] **What `Received` counts on a delimited channel is settled and written down (2026-08-31).** Rows, not documents: a
      four-hundred-row file counts four hundred. The header is not counted, being structure rather than data.

      This was a consequence rather than a choice. Rows were chosen so a filter could drop one bad row instead of a whole file,
      and the counting followed. Nothing documented it and no test asserted it, so the number was correct, invisible and free to
      change without anything noticing — which for a metric a site may be alerting on is the worst of the three.

      Now in three places that each serve a different reader: the `Stats.Received` doc comment for anyone in the code, the
      `perfuse_messages_received_total` help text for whoever is looking at Grafana, and a test that fails if the unit changes so
      that it has to be a decision. `Delivered` is asserted to agree, because a delivered-over-received ratio is the obvious way
      to spot a channel dropping traffic and it means nothing if the two count different things.

- [x] **X12 shadow mode. Already built, and the entry was stale (confirmed 2026-09-16).** `internal/shadow/diffx12.go` has the
      X12 walker, `DataX12` is in `shadowRuns`, `TestAShadowOnAnX12ChannelIsAccepted` passes, and thirty-three shadow and X12 tests
      pass together.

      Note for anyone checking this the way I first did: `go test -run TestEveryShadowingDataTypeActuallyObserves` printed `ok` and
      `PASS` while running nothing, because that test name does not exist. `-run` with a filter that matches nothing reports
      success. The real guards are in `internal/config/shadowruns_test.go`.

- [x] **Shadow could not be switched on from the interface at all (2026-09-16).** Found while checking the item above. The builder's
      drift guard excused the whole `shadow` block with the reason "configured from the shadow tab, where the candidate can be
      picked from a list of channels that exist" — and the shadow tab was read-only. No editor, no endpoint. The only way to start a
      comparison was to hand-edit the YAML that a shadow exists to avoid doing blind.

      **An excuse resting on a capability that does not exist is worse than no excuse**, because it removes the check that would
      have found the gap and reads as a decision somebody made. Second one today; the first was a package comment claiming four
      TEFCA exchange patterns that made no network call.

      Now `PUT`/`DELETE /api/channels/{name}/shadow` with an editor on the shadow tab. The candidate is chosen from real channels,
      because a name that does not resolve makes the *live* channel invalid rather than warning. The share is entered as a
      percentage and stored as a fraction, because somebody meaning half who types 0.5 into a percentage field gets a comparison
      that observes almost nothing and no complaint. Configured is reported separately from running, because a comparison starts
      when the channel next loads and claiming otherwise is a small lie that costs trust in the whole report.

      Seven API tests, two end-to-end specs, break-verified on the percentage conversion.

### Recorded 2026-09-16, from the end-to-end flakiness investigation

- [x] **The remaining flakes are a starved browser, and the cause is this machine, not the suite (2026-09-19).** Measured from a trace
      that was preserved before re-running anything, which is the only reason there is an answer.

      `livetraffic.spec.ts:15` failed while waiting on a predicate. The trace gives the mechanism exactly:

      ```
      +0.0s   waiting for element to be visible, enabled and stable
      +60.2s  element is visible, enabled and stable
      ```

      Playwright resolved the Refresh button immediately and then could not call it actionable for sixty seconds. The screencast has a
      **60.2 second gap with no frames at all**, beginning at the same instant. Playwright's stability check needs two consecutive
      animation frames with the same bounding box, and a renderer producing no frames can never satisfy it.

      Everything else rules out the obvious suspects. Every API request in that run completed fast - the slowest was 154ms and
      `/api/messages` answered 200 in 30ms - so the server was healthy. Only two calls to `/api/messages` were made, so the retry loop
      never got a second iteration: the page could not run the JavaScript to issue one. The message itself was in the store and in the
      table by the end, correct and delivered. The list held seventeen rows, so this is not list size.

      **The cause is memory.** This machine has 16GB, and the colima VM started on 17 September for the interop containers was holding
      **61 per cent of it** - about 9.8GB, eight containers including Keycloak and HAPI. During a suite run, Chromium's processes, node
      and the Go toolchain compete for what is left. A sixty second renderer stall is what that looks like.

      With colima stopped: **327 of 327**, twice. The containers are restarted with `./scripts/interop-up.sh` and stopped with
      `colima stop`.

      This also reframes the two earlier members of the family. flowmap and synthesis both failed on 17 September, the day colima was
      started, and their fixed-port and readiness problems were real but may not have been the whole story. The dicom-steps preview race
      was real too and is fixed. What is now clear is that a suite of 327 browser tests and a 9.8GB virtual machine do not fit in 16GB
      together.

      **The practical rule, now in the manual:** stop the interop containers before a full browser run, or expect stalls that look like
      defects. And copy failing artefacts out before re-running: a passing run clears `test-results`, which destroyed the evidence for
      dicom-steps and left a question that can no longer be answered.

- [x] **The flaky-spec family is diagnosed and fixed (2026-09-18).** Two causes, both found by measuring the thing the specs share
      rather than by chasing the specs.

      The roll was `alert-rules.spec.ts:251`, `gui-qa.spec.ts:17`, `use-everything.spec.ts:216`, `flowmap.spec.ts:43` and
      `synthesis.spec.ts:34`. The recorded reasoning had one thing wrong and one thing right.

      **Wrong: the failures were not unusually fast.** "233ms and 1.8s against a twenty-minute suite" used the wrong denominator. Those
      tests normally take 913ms and 3.2s, so the right statement is that each failed partway through its own short runtime.

      **Right: something was not ready.** Two of the five - flowmap and synthesis - send HL7 over MLLP, and those two are exactly the
      pair that failed together on 17 September. They are also the only two specs in the suite that kept their own copy of the send
      helper instead of using `mllp.ts`. Neither copy waited for the channel they had just started to be listening: flowmap sent
      immediately after the start request returned, and synthesis slept a fixed 1500ms first. 233ms and 1.8s are what those two paths
      add up to.

      Measured rather than inferred. A probe connecting to the fixture channel every 500ms for a whole run caught the port refusing
      connections, and the kept server log put a stop at 05:03:08.735 and the listener rebinding at 05:03:09.156 - 421ms with nothing
      accepting. The probe's refusal was at 05:03:09.008, inside that window. A third run reproduced synthesis failing with
      `read ECONNRESET`, which is what a listener closing a connection it has accepted produces.

      Fixed with `waitForListener`, which connects until something accepts, and a retry in `sendMLLP` limited to a connection refused
      **before anything was written** - a retry after a write could deliver the same admission twice, which would be worse than the
      flake. Both specs now use the shared helper. `web/e2e/mllpwait.spec.ts` guards all three behaviours and the retry is
      break-verified: disabling it fails that test in 7ms. Synthesis also got faster, 3.2s to 1.5s, because a readiness check returns
      when the listener is up rather than when a guessed interval expires.

      The second cause, in the navigation helper all 61 specs share: `openTab` counted matches without waiting. `count()` answers
      immediately, and the `expect()` beneath it was given a number rather than a locator, so neither retried - a menu still rendering
      read as a menu without the item in it, and the helper reported that a view was in no group. Reproduced under CPU throttling,
      where the pre-fix helper failed four times out of four; the fixed one passes six out of six. `tabLabels` had the same race
      silently, returning a short list with no error, which let the test asserting every view is reachable check fewer views and pass.
      `gui-qa.spec.ts` also skipped itself when a racy count read zero, blaming the viewport - a skipped test is a green run with less
      in it.

      What was eliminated, so the answer was not reached by assuming: an HTTP watchdog polled the server and the shared session for a
      whole run and found no fault in 11,000 samples, with the API answering in 1.5ms and the page in 0.3ms throughout. Accumulated
      server state, machine contention and server-side faults had been ruled out earlier.

      The instruments are kept: `scripts/e2e-watchdog.sh` and `scripts/e2e-mllp-probe.py`. The watchdog polls every 500ms rather than
      every 100ms, because at 100ms it spawned twenty processes a second, stretched a nineteen-minute run to twenty-eight and failed two
      tests on timeouts that had nothing wrong with them - the instrument changed the thing it measured.

- [x] **The TEFCA screen's honesty is asserted, and writing the assertion found a second defect (2026-09-16).** The notice saying
      exchange is not implemented had no test, because the branch it lives in is unreachable end to end: participation is built from
      settings when the server starts, and the end-to-end harness shares one server it cannot restart.

      Covered in two halves that have to agree. `internal/api/tefca_test.go` asserts a configured participant reports
      `exchangeImplemented: false` with an explanation naming both the gap and what still works;
      `web/src/TEFCAViewConfigured.test.tsx` asserts the screen turns that into something an operator reads, and asserts the other
      direction too, so the pair cannot outlive the gap it describes — a notice left hard-coded after a transport arrives is a lie
      that is harder to notice, because nobody investigates a warning claiming a working feature does not work.

      **The second defect:** `TEFCAView` returned a spinner whenever the status was null, with no error check before it. A failed
      status request set the error in state and left the status null, so the one screen whose subject is national exchange answered
      a broken server with an animation that never ended, and the message saying what was wrong was already in state and
      unreachable. Found only because the component test rendered an empty tree and I asked why. Now shown, with an end-to-end test
      that fails the interception if the spinner comes back.

### Queued 2026-09-16, in this order

Ordered by how answerable each is, not by size. The first is measurement, the rest are dated obligations.

- [x] **A guardrail tying builder form controls to model fields (built 2026-09-16).** `internal/api/buildformfields_test.go`. It
      reported **47 fields** the interface never mentioned; narrowing the search from all of `web/src` to the eight files the builder
      is made of revealed **7 more** that a substring match had hidden. Sixteen paid off so far; the rest are the two-way ratchet in
      `knownFormGaps`, which fails on a new gap and also on a stale excuse. The prediction in this entry was right: the zero was
      never checked.

      Superseded detail below.

- [x] ~~**A guardrail tying builder form controls to model fields.**~~ (built; see above) `builddriftpath_test.go`
      walks the Go build model and proves a setting is expressible in a channel file, and `enumcontrols_test.go` reads the React form
      but only for enumerated *values*. So a field can exist in the model, be perfectly expressible in YAML, have no control anywhere
      in the builder, and every guard passes. Write the guard, report the list before fixing anything, and expect
      `knownBuilderGaps` to stop being zero - a zero that was never checked is worth less than a number that was.

      Match on control shape rather than on the field name appearing in the file. The first version of the enum guard searched whole
      files and could not fail, because `wasm` still appeared in a type union after the option was deleted. Same trap, one level up.

- [x] **X12 275 and 277 — claims attachments (2026-09-16).** CMS-0053-F adopts X12N 275 version 006020 as a HIPAA standard for
      claims attachments, compliance required by 26 May 2028. Both transactions are now parsed into named fields, and the 277's
      request for documentation is recognised by status code rather than left to a channel to interpret.

      **The interesting part was not the transactions.** A 275 carries its document in a BIN segment as raw bytes, and raw bytes
      contain delimiters. The existing splitter found segments by scanning for the terminator, so a PDF containing a tilde was cut
      into fragments - each of which is a structurally valid segment, so nothing reported an error, the document was truncated, and
      every segment after it was misaligned. That was true of any interchange with an attachment in it, and had been all along.

      Fixed in two halves, both break verified: the splitter honours the declared byte count, and the element splitter keeps the
      payload whole rather than cutting it at every asterisk it happens to contain. A declared length that disagrees with the data is
      refused naming both numbers, because the useful question is whether the sender miscounted or the file was truncated in transit.

      BDS is deliberately refused with an explanation rather than guessed at. It also carries binary data and this package has not
      verified which element holds its count; a wrong guess produces a plausible-looking wrong length, which is worse than a refusal.

      Three of my own mistakes worth keeping. I justified a new `Element.Bytes` by claiming `String` would corrupt non-UTF-8 data,
      which is false - Go strings hold arbitrary bytes - and a plausible-sounding wrong reason in a doc comment is how the stale WASM
      note happened. I built an STC test fixture by counting asterisks and put the message in element 11 twice, which read back empty
      and looked like a parser fault; the fixture is built from named positions now. And I documented a filter expression
      `x12.requests_documentation` that does not exist in the grammar, then a mixed-notation one that does not compile - there is a
      test that compiles the expression printed in the manual, because a documented filter that is refused at load teaches somebody
      the feature is broken.

- [x] **X12 278 — prior authorisation request and response (2026-09-16).** Both directions parsed, with the authorisation number
      surfaced as the field a claim actually needs: without it a certified service is not billable, and the resulting denial reads
      downstream as a clinical one.

      **The distinction worth the work** is that a 278 says no in two incompatible ways. An HCR with action A3 is a decision and the
      answer is an appeal; an AAA is a refusal to consider - patient not found, requester unknown - and the answer is a corrected
      resubmission. Both read as "not approved" to anybody scanning, and the actions are opposite. Reported as separate outcomes,
      break verified by making a refusal report as a denial and watching the test name the consequence.

      Partial certification is its own outcome rather than folded into approval, and an unrecognised action code is reported as
      unknown rather than pending, because pending reads as "wait" and waiting is wrong for most of what it could be.

      **My asterisk-counting mistake happened a third time**, in the same session I wrote it down after the second. UM06 is the
      level-of-service code and the fixture put the value in element 9, so the field read as absent and looked like the parser
      ignoring it - the dangerous direction, because it invites "fixing" correct code. Both UM and STC fixtures are now built from
      named element positions. The rule that follows: a test fixture for a positional format is written by naming positions, never by
      counting separators.

- [x] **Bridge Da Vinci PAS to the 278 (2026-09-16).** `internal/pas278`. Converts a parsed 278 into a PAS ClaimResponse and
      reports every judgement it needed.

      **It has its own decision vocabulary rather than reusing priorauth's**, and that was the whole design question.
      `PriorAuthResponse.Decision` has three states and the 278 distinguishes seven; three of the differences change what somebody
      does next. Partial certification collapsed into approved hides a quantity reduction until the fourth claim. A refusal to
      consider collapsed into denied sends a practice to appeal something nobody ruled on. Narrowing into the three states is
      available through `Narrow`, which returns what it discarded, so the loss is a choice somebody made rather than something that
      happens quietly inside a converter.

      Two findings from writing the tests. A fixture put the event's HCR after the service-line loop, so it attached to the line and
      every event read as undecided - the parser was right and the fixture wrong again, this time structurally rather than
      positionally. That in turn exposed a real gap: a payer answering per service leaves the event without a decision, and reading
      only the event level reported those as pending. A practice then waits for an answer it already has while the approval expires.
      The event now derives from its lines, taking the least favourable, because an event with a refused line is not an approval.

- [x] **TEFCA: found something worse than the missing feature (2026-09-16).** The instruction was to check what the package claims
      before writing anything, and that was the right instruction.

      **No TEFCA function made a network call.** There is no `net/http` import in the package. All four exchange patterns validated
      their inputs and returned success: `Query` an empty response, `Retrieve` a nil document with a content type of
      `application/fhir+json`, `Notify` a bare nil, and `Deliver` an `Accepted` response with a tracking identifier built from the
      clock. The auditing layer above then recorded each as a completed exchange, in a persistent log whose only purpose is to prove
      what was exchanged when somebody asks months later.

      The audit file's own comment says a trail that disagrees with what happened is "worse than no trail, because the trail is what
      gets believed". That was exactly right, and the layer underneath made it false.

      **Four tests asserted the defect and passed**: TestQuery_Success, TestDeliver_Success, TestRetrieve_Success,
      TestNotify_Success. Third time in this codebase a test has defended a bug rather than caught one, and the first time in a
      feature whose claim is made to hospitals about national data exchange.

      Fixed by refusing: `ErrExchangeNotImplemented`, naming what is missing and what still works, so a refusal is auditable as a
      failure - which TEFCA requires anyway - and cannot be mistaken for an exchange. Three guards now cover it: a source-reading
      test that every exchange function either performs a network operation or refuses (with a positive control, so a rename cannot
      make it vacuous), a behavioural test that no attempt is ever audited as successful, and a test that the refusal names the gap.
      Break verified by restoring the delivery stub, which fails all three.

      Purpose-of-use checking, configuration validation and the audit trail are real and were left alone. The package doc, the manual
      and the TEFCA screen now all say plainly that exchange is not implemented - the screen especially, because a configured
      participant with no transport looked identical to a working one, and an operator who believes exchange is running does not go
      looking for why no records arrive.

- [x] **Mutual TLS verified against OpenSSL (2026-09-17), which is the transport half of TEFCA.** An nginx demanding a client
      certificate, started by `scripts/mtls-server.sh`, with four tests in `internal/engine/mutualtls_test.go`. It works: the
      certificate is presented and accepted, a request without one is refused with 400, a server certificate signed by an unknown
      authority is rejected, and the raw handshake negotiates at least TLS 1.2.

      Worth doing separately because every other client-certificate test in this codebase is Go talking to Go - crypto/tls agreeing
      with crypto/tls, which says nothing about what OpenSSL makes of what we send. That distinction stopped being academic tonight,
      when the SAML canonicaliser turned out to be unable to accept any real assertion while passing a thousand lines of self-agreeing
      tests.

      The tests skip when the container is absent, with a message naming the script. A check that needs Docker is a check people stop
      running, so `make check` has to pass without it.

- [x] **Facilitated FHIR is built, and its security is verified against somebody else's server (2026-09-19).** What remains is QHIN
      membership, which no amount of code produces.

      The item used to say the exchange "cannot be written speculatively: it is tested against a real QHIN or it is not tested". That was
      half right, and the wrong half was the expensive one. Joining a QHIN needs onboarding and issued certificates. The security profile
      underneath it - HL7 Security for Scalable Registration, Authentication and Authorization, which is UDAP - is a published
      implementation guide with public reference servers, and one of them answers on the open internet.

      `internal/udap` implements discovery, signed metadata verification, dynamic client registration and the token request with the
      hl7-b2b authorization extension. `internal/tefca/facilitatedfhir.go` is the exchange on top of it, per the Sequoia Project SOP
      effective 8 March 2026.

      **The measurement.** A registration signed here was sent to the live reference server at securedcontrols.net and refused with
      `unapproved_software_statement - Untrusted: Certificate is not a member of community`. To answer that, the server decoded the
      request, parsed the software statement as a JWT, checked its signature, walked the certificate chain and reached a decision about
      membership. The token endpoint answered `invalid_client` for a client_id it has never issued, meaning the form was decoded and the
      assertion read before the lookup failed. Being turned away at the door proves the letter was legible.

      A real server's signed metadata also verifies against the real trust community: the fixture in `internal/udap/testdata` came from
      fhirlabs.net, and the EMR Direct Test PKI publishes the authorities that issued it, so the chain is anchored rather than merely
      self-consistent. It verified on the first attempt, which is not what happened with SAML.

      Three defences, each break-verified. A trust anchor is required and its absence is an error - checking a document against the
      certificate inside it proves one party made both, which is the SAML defect that authenticated an attacker as an administrator. The
      endpoints used must be the ones that were signed, or the signature is decorative. The algorithm comes from an allowlist rather than
      from the document.

      One real interop finding: the reference server puts **only its leaf** certificate in the x5c header, so a client holding just the
      community root cannot build a path and the failure reads as "certificate signed by unknown authority", which sends somebody looking
      for the wrong problem. The trust anchor bundle is therefore used as both roots and intermediates.

      Reported honestly in two places that a test holds to account: the API reports Facilitated FHIR and the IHE transport separately, and
      the explanation must mention the reference server, QHINs and onboarding or the test fails for overstating what was proved. Removing
      the bounding clause was verified to fail it.

      **Still not done, and not doable here:** an exchange with a real QHIN. That needs a certificate issued through onboarding. Also not
      built: the older QHIN-to-QHIN transport on the IHE profiles, which remains refused rather than stubbed.

#### The format and scripting work is finished

Every item that used to be here is done. What is left in this file is section 1, which no amount of code closes, and sections 4
onwards, which are bets and records rather than gaps.

The pattern across the last of them is worth carrying, because it was consistent and it was expensive. Four items estimated as
"build this" turned out to be "wire what already exists": the SCRIPT script stage was already generic under a v3-specific name,
the SCRIPT steps needed no resolver because `eprescribe` already borrowed `hl7v3.Path`, the DICOM steps existed in full with no
caller at all, and JavaScript on the path formats needed the shared answers moved rather than a second binding written.

So the first question for the next feature is not how to build it. It is whether it is already here under a name that hides it,
or behind a refusal whose reason has stopped being true.

---

## 4. Bets, not gaps

Neither is a missing capability. Both are wagers on how the project grows, and both need a judgement call before any code.

- [x] **Shareable profiles: the machinery existed and had no caller (built 2026-09-16).** `internal/profile/share.go` has held
      `ExportProfile`, `MarshalProfile` and `UnmarshalProfile` with a versioned format for some time. **Zero callers.** Recipes had
      endpoints; profiles had the machinery and no way to reach it. This entry described the work as open when what was missing was
      two handlers and a form — the shape already in §7 of this file: *an implementation with no caller is indistinguishable from a
      feature that does not exist.*

      Now `POST /api/profiles/export` and `/import`, with the share controls beside the report in the profiler.

      **The design decision worth keeping:** the export takes a *channel name*, not a profile. The shorter path was to post the
      report already on screen, and it would have put the assembly of the one artefact that must not carry patient data in the
      browser's hands. The server rebuilds it from stored messages, so the guarantee rests on the profiler — which has a guard
      proving identifier values are never listed — rather than on a request body. The boundary is checked anyway, in both directions,
      naming the offending field and never its values.

      Two incidental findings. Strict JSON decoding caught the first version immediately, because the browser's profile type is
      richer than `profile.Report` — which is what prompted the better design. And importing a helper from a `.spec.ts` file
      **re-registers that file's tests in the importer**: the new spec quietly ran three tests for two, and the extra one was
      livetraffic's. Helpers now live in `web/e2e/mllp.ts`.

      **The open question in this entry stands and is not answered by building it:** whether anybody shares anything. What has
      changed is that it now costs a download to find out rather than a project.
- [x] **AI-assisted mapping with abstention. Built, and the entry was stale (confirmed 2026-09-16).** `internal/mapper` has the
      engine, confidence scoring, fuzzy matching and abstention; `MapperPanel.tsx` shows an abstention as visually distinct from a
      low-confidence suggestion, with its own component test; the AI Mapper tab is in the navigation.

      The abstention half — the part this entry called the whole idea — is the part that is best covered.
      `internal/mapper/danger_test.go` is independent verification on the pairs where a confident wrong mapping does clinical harm,
      and the cases are chosen rather than generic: date of birth against date of death, systolic against diastolic, admit against
      discharge, patient ID against account number. All near-miss names, so string similarity rates them highly, and all with a
      shared value shape that would then push them over a threshold. The engine must decline. `audit_test.go` verifies the string
      metrics against known values rather than against itself, and bounds confidence.

- [x] **The mapper misread HL7's own date format as a medical record number (2026-09-17).** Found by checking a claim I had made
      in the entry below without testing it, which is the whole reason to check claims.

      `detectPattern` tried value patterns in table order and kept the first with the best match fraction. MRN is
      `^[A-Za-z0-9]{6,12}$` and is listed above date, so `19800101` - HL7 v2's own date format, and the commonest date in the
      messages this engine exists to map - matched MRN and won the tie. The date pattern includes `\d{8}` precisely for that format
      and could never win with it. Ten-digit phone numbers went the same way.

      **The field name was never consulted**, and it held the answer: `DateOfBirth` matches the date pattern's own name hints. Name
      detection was a fallback reached only when no value pattern scored 0.5, so a wrong value match locked it out.

      The effect was an inverted ranking on the most safety-critical pair there is. A birth date offered against a medical record
      number scored **53 with a pattern match credited**; the same birth date offered against a date field scored **50 with none**.
      The engine ranked putting a date of birth into an identifier field above putting it into a date field. After the fix: 23 and
      80.

      Nothing was confidently wrong, because both abstained at 70 - the abstention design was the only thing containing it. And
      because the panel abstained on nearly everything, the obvious response was to lower the threshold, which would have surfaced
      the dangerous mapping ranked above the correct one. `danger_test.go` did not catch it because it tests name-based confusable
      pairs and this was a value misclassification.

      Ties now break by name first, then toward the narrower pattern; MRN is marked `broad` because "6-12 alphanumeric characters"
      describes most other patterns' values too. Five tests, four break-verified.

- [x] **The mapper could not be confident about the one thing it exists for (2026-09-17).** Mapping a descriptive field name to an
      HL7 path capped around 60 and abstained, because name similarity compared `DateOfBirth` against the literal string `PID-7`.
      `internal/hl7dict` already knew that PID-7 is the Date/Time of Birth field - it was written for the message viewer and the
      mapper never asked. Now scored against the meaning, and the reasoning says so on screen: `PID-7 is Date/Time of Birth; name
      similarity 88%; pattern match (date)`. `DateOfBirth` to `PID-7` went 56 to 74. A `Z` segment returns nothing rather than a
      guess, because it means whatever one site decided.

- [x] **Abstained is a property of the source field, not of a suggestion (2026-09-17).** It is set on all of a field's suggestions
      only when the best is below the threshold, so once the best clears it every weaker one carries `abstained: false`. The panel
      approved on that flag, so a 46 arrived with an approval box beside a 77, looking equally endorsed. Now gated on the
      suggestion's own confidence, with a third state on screen: shown for context, below your threshold, not approvable.

      Invisible until the two fixes above made anything clear the threshold at all. Fixing one thing is what made the next reachable.

- [x] **Nothing checked that the committed front end bundle was built from the committed source (2026-09-17).**
      `internal/web/dist` is committed so `go build` works without Node, and could disagree with `web/src` silently. Two costs: a
      binary built from source serves an interface that is not in the tree, and the browser tests serve the bundle, so editing a
      component and running them without rebuilding tests the previous bundle and **passes**.

      Not hypothetical - it gave me two false passes on a break that was meant to fail, and I only noticed because the Go engine
      disagreed with the browser about the same inputs. `make check` now rebuilds and fails if the result differs. Note the `e2e`
      target already rebuilt both and its comment already named this drift; the mistake was running playwright directly instead of
      through it, and that shortcut fails silently rather than loudly.

- [x] **A mapper suggestion can be acted on (2026-09-17).** Per-mapping approval, then either steps on a channel or a shareable
      recipe. `POST /api/mappings/approve` and `/api/mappings/recipe`, nine API tests, two end-to-end specs, break-verified on the
      abstention refusal.

      The constraint recorded here was honoured and is now enforced on both sides: approval is per mapping, there is no threshold
      parameter and no accept-all, and **an abstention has no approval control at all** - not a disabled one, because a disabled box
      beside a suggestion still reads as something that could be enabled. The server refuses an abstention outright rather than
      skipping it, so a request containing one is a visible disagreement between the two halves rather than a mapping that quietly
      does not appear.

      **Three things were wrong underneath, and none was the missing button.**

      1. **The panel could not reach the engine's confident path.** Confidence is name similarity plus evidence that the values
         belong together, and the panel sent neither example values nor a target kind - so it abstained on nearly everything. An
         exact name match scores 68 and abstains at the default threshold of 70. Both are now enterable, and the defaults carry them
         so the first impression is not "it never suggests anything".

      2. **A target's `pattern` is a kind, not a regular expression** - `mrn`, `npi`, `ssn`, `phone`, `date`, `email`. Passing a
         regex made it an unrecognised kind that scored nothing, silently. I got this wrong first and the reasoning string is what
         showed it: it listed only name similarity.

      3. **A copy step cannot express most of these mappings**, which the channel loader caught before anything was written. A step
         copies from one place in a message to another, so the source has to be a path; the mapper's sources are usually column
         names or a vendor's field names. Refused now with that explanation and a pointer to the recipe, rather than by the loader
         complaining that PATIENTMRN is not a three-character segment name - which is true and tells nobody what to do.

      Original reasoning kept below, because it is the argument and it has not changed.

- [x] ~~**Reframe the Java scanner as a lock-in audit, and lead with it.**~~ (superseded by the entry above) Raised 30 August, and it is a positioning
      change rather than a feature. `Channel.ScanJava` already sorts every Java reference into four verdicts. It was built to
      answer *"what must I rewrite to migrate to Perfuse"*; the same output answers *"how much of my integration layer only
      runs on their software"*, which is a question a hospital wants answered **before** it has any opinion about Perfuse.

      Why it is a stronger opening move than "Mirth replacement": it runs against a plain channel XML export, needs nothing
      installed, implies no commitment, and the `VerdictOutOfScope` count **is** the score. The audience is whoever signs the
      renewal, not the integration engineer.

      **The distinction is the whole product, and it is why this is honest rather than scaremongering.** Most Java in Mirth
      scripts is not lock-in — `SimpleDateFormat`, `HashMap`, Apache Commons, all of it working around a 2009 JavaScript
      engine, all of it rewritable in minutes and portable anywhere. Lock-in is specifically `com.mirth.connect.*` and vendor
      jars. Five of those and a site is stuck; five hundred of the other kind and it is free. A tool that reported "you have
      312 Java references, you are trapped" would be lying. This one can report the number that matters.

      The context that makes it timely, and it should be stated as dates rather than insinuation: Thoma Bravo took NextGen
      private in November 2023 for $1.8B including debt; Madison Dearborn Partners took a significant position announced
      around the turn of 2024–25; Mirth 4.6 removed source availability in March 2025. Before that a site had a theoretical
      exit — MPL source, fork it, hire somebody. **That option closed retroactively on code already written against it.**

      The mechanism to describe is not a price rise. A price rise is visible and has a renewal date to argue on. What happens
      is a version reaching end of life, an upgrade required for support, and the new terms arriving with the upgrade. It is
      accepted because the alternative is re-deriving years of undocumented clinical rules — the vendor need not name a high
      price, only know the alternative is worse.

      **What must be said in the same breath, or the pitch is dishonest.** Perfuse's own lock-in: Apache 2.0 is irrevocable
      for code already released, the scripts are ordinary JavaScript with no proprietary surface to call — `installJavaRefusal`
      makes Mirth's flavour of dependency structurally unavailable here, which is the one place a limitation is genuinely a
      feature — and the config is YAML a site can read without the product. Against that: one maintainer, zero production
      hours, and the same person wrote the code and the tests. **The portability of the artifacts is what survives a change of
      owner. The project is not the part to reassure anybody about.**


---

## 5. Settled. Do not re-litigate, and do not helpfully rebuild.

- **NCPDP — removed 2026-08-22, then re-added 2026-08-28 deliberately. The entry stays because the reason for
  the original removal has not gone away.**
  Built and deleted the same day, 22 August: **Clause 13 of the employment agreement names pharmacy dispensing explicitly** —
  that is not adjacent to the named subject matter, it is the named subject matter. Nothing depended on it, which is why it
  was cheap to undo that day and would not have been in a month.
  On 28 August it was rebuilt , as item 2 of a format list decided directly. Filtering and transformations
  followed on 29 August. **That is a reversal by the only person entitled to make it, and the code is not the problem here.**
  What is worth recording is that **the re-add commit does not mention the removal or the reason for it**, so for a day this
  file said "do not re-add" about a package that had been re-added. The standing note in section 9 — that the IP question is
  live and being handled separately — now covers a larger surface than it did: `internal/ncpdp` is parse, build, response
  reading, a path language, a filter binding, transformation steps and pipeline wiring. **Whether Clause 13 was settled
  before the rebuild is not recorded anywhere in this repository, and this note is not evidence that it was.**
- **Plugins and custom extensions — decided against 2026-08-21.** Not a gap, a choice. Anything a site wants gets built into
  the one executable. Consistent with everything else here: one binary, nothing to install alongside it, every feature
  auditable in the source.
- **Clustering with failover — deliberately not done.** Mirth sells this separately and was right to; it is a distributed
  systems project, not a UI feature. **Perfuse's design makes it harder rather than easier:** the durable queue is
  per-destination on local disk with strict ordering that is deliberately not configurable, so cross-node takeover would have
  to migrate ownership of a partially-drained queue while preserving per-destination order. That touches the queue and the
  message store, not the console.
- **DICOM query and retrieve — absent on purpose, and that is parity.** Mirth has no C-FIND, C-MOVE or C-GET connector either.
  (C-FIND has since been built anyway, as a source.)
- **The WASM playground is not embedded in the server binary**, deliberately. It is compressed and lazy-loaded.
- **Hospitals are not stranded on frozen software.** NextGen closed Mirth in March 2025 at 4.6; 4.5.2 was the last open
  release, and within a week two live forks appeared — **Open Integration Engine** (MPL 2.0, non-profit steering committee,
  4.6.0 GA, 24 CVEs remediated, consultancies selling migrations to it) and **BridgeLink** (Innovar Healthcare). They have a
  cheaper option than Perfuse, and superiority does not beat switching cost. Recorded because the repo's original framing
  rested on the opposite premise and I should not re-derive this.

---

## 6. Known limits, recorded so they are not rediscovered as bugs

- **An existing session keeps its role until it expires** after an OIDC or LDAP demotion. True of both.
- **No SMART EHR launch** (`context-ehr-patient`, `context-ehr-encounter`). Those describe an authorization server taking part
  in a launch sequence; this server verifies a token somebody else issued. Claiming them would make an EHR attempt a launch
  against an endpoint that cannot answer.
- **No FHIR chains through an ambiguous reference** unless written `subject:Patient.family`. Returning several candidate
  subjects for one observation is a wrong clinical answer a client would resolve by taking the first.
- **v3 element creation appends an unknown element name** rather than refusing it.
- **The `segment present` contract rule has no v3 meaning** — the v3 report has one container standing for the document.
- **Bulk export holds payloads in the database** with a hard 256 MB ceiling, in exchange for no export directory and no
  residue. The refusal names `_type` and `_since` as the way to narrow.

---

## 7. Defects worth remembering, because the shape recurs

The first two were found by running the code, not reading it.

**A reference search could not tell a `Patient/123` from a `Group/123`.** The search index held a bare id, so it returned
another subject's records as the requested one's — with a 200, in a bundle that looks entirely normal. Shared ids across types
are the *normal* case when they come from a source system's sequence. Fixed with a `ref_type` column, and then the same check
had to be made twice more, in `_include` and in chained search, because each was a fresh opportunity for it to come back.

**A `one of` contract expectation could never fail** when no vocabulary was recorded for the path. It counted as checked, the
contract read green, and the check had never run once. Affects v2 today.

The shape both share: **not a missing feature, but a wrong answer delivered confidently.** Those do not show up as failures.
They show up as a customer asking why the numbers are odd, eight months later.

### The audit of 25 August, and what it says about the shape

Everything written between 23 and 25 August was reviewed line by line, about 21,000 lines across 48 Go files and the front
end. **Forty defects.** Not one was a crash. Every single one was the shape above.

The ones worth carrying forward, because each is a *category* rather than an incident:

**An invented field name serialises to nothing, and nothing is a valid answer.** Three medication resources used
`medicationCodeableReference`, which exists in no FHIR release. A client asking for R4 looks for
`medicationCodeableConcept`, one asking for R5 looks for `medication`; both found neither and rendered an empty medication
list — indistinguishable from a patient who takes nothing. Then a fourth instance turned up in `Medication.ingredient.item`.
The lesson is not "check field names"; it is that **an empty collection is never a safe default for clinical data**, and any
code path that can produce one silently deserves a test that plants content and proves it survives.

**A near-miss name plus a matching value shape beats a similarity threshold.** The mapper offered date of birth → date of
death at confidence 75 against a threshold of 70. The names differ by one word, so string similarity rates them 0.91, and
because both hold dates the pattern detector added its full bonus. Patient id → patient account number scored 72 the same
way. Confidence built by adding independent signals will always do this, because the signals are not independent when the
fields are near-misses. Pairs where a confident wrong answer is dangerous now have the pattern bonus withheld.

**An escape helper that is never called looks identical to one that works.** The public health builders had no HL7 v2
escaping at all: one patient name containing a pipe shifted every later field by one position, and the message stayed well
formed. The fix is easy; noticing is the hard part, and the only reliable check reads the bytes that would go on the wire
rather than the function that is supposed to produce them.

**A guard that compares the wrong thing rejects everyone.** SAML compared the signature reference against the assertion id
unconditionally, so any IdP that signs the Response — which ADFS does by default — could never authenticate anybody. The
test suite passed because every helper signed the assertion. **When a spec permits two shapes, test both, or the untested one
is the one production uses.**

**A rule whose baseline is wrong is a rule that does not exist.** PHI volume-spike detection divided total events by total
events, so the average was always exactly 1.0 and a relative-spike detector had silently become a fixed threshold of three
per minute. Its test passed because the contrived input exceeded the broken threshold too. **A detection rule needs a
negative test; only that distinguishes "fires correctly" from "fires always".**

**A partially-applied rename is worse than the original error.** Fixing `Catalog` to `CatalogEntry` in the type while the
constructor registry and search table still said `Catalog` meant the resource could not be built by name at all — refused as
unimplemented rather than merely misnamed. Any identifier that appears in more than one table needs a test that walks all of
them; there is now one covering all 87 resource types.

**Silently ignoring an input returns more than was asked for.** FHIR search recognised three date parameters out of thirteen
and three reference parameters out of eighteen, so most prefixed date queries and every type-qualified reference query matched
nothing. A token search meaning "any code in this system" returned nothing. The house rule that an unknown parameter is an
error exists for this, and it only helps where the parameter is actually *recognised as* a parameter.

**A test can pass because of something not in the repository.** New front-end tests imported two packages installed locally
and never declared, so the suite was green here and would have failed on any clean checkout. Verify with a frozen lockfile,
not with whatever is in `node_modules`.

Two results from the audit were *clean*, and they are worth recording so they are not re-audited from scratch: the syntax
highlighter renders untrusted message content through JSX children rather than by building HTML, so it is not an injection
vector, confirmed at the DOM level across all four formats; and every route added in that window was correctly behind an
authorisation check at a role consistent with its siblings. There is now a table-driven test over every protected route, so
the next unprotected endpoint fails immediately instead of shipping.

**What the audit says about method.** Reading found some of these. Running found more. But the highest-yield technique by a
wide margin was *planting the violation a guard exists to catch and confirming the guard fires* — that is what exposed the
volume-spike baseline, the SAML signature comparison, and the script isolation leak, all three of which had passing tests
written over broken code. A test that has never been seen to fail has not been shown to test anything.

---

### The success case is the untested case

Found the direct way: an operator converted a message in the FHIR lab, clicked Decisions, and the view
died with `Cannot read properties of null (reading 'length')`.

The mechanism is dull. A nil Go slice marshals to JSON `null`. The TypeScript interface declared
`findings: Finding[]`, non-nullable, so the interface read `.length` off null. The fix is one initialiser.

What is worth remembering is **which input triggered it**. Every deliberately broken message produced
findings, filled the slice, and worked. The only input that crashed was one that converted *perfectly* —
because that is the only input for which there is nothing to report. The demo message. The one a customer
pastes first.

An audit for the same shape found **five more**, each triggered by its own success case:

| Where | Empty when |
|---|---|
| `v2fhir.Result.Notes` | the message mapped cleanly with no judgement calls |
| `engine.ReplayReport.Differences` | the replay found no differences — the hoped-for outcome |
| `cda.AgreementReport.Findings` | narrative and coded entries agree — the normal case |
| `api.Runtime.States()` | returned nil when the channels directory was unreadable, which is when the dashboard most needs to render |
| `settingsValuesResponse.RestartRequired` | a save that needed no restart — reported by the screen confirming success |
| `hl7v3.FlattenFieldTree` | a document of pure structure with no values — a skeleton or template message |

The last one was found only after going back a second time, and it is the clearest case of all: the
append is conditional on a node carrying a value, so pasting a template into the v3 field picker
returned null where the interface filters an array. Proven reachable by a test before being fixed.

So: **test the path where nothing goes wrong.** Error paths get exercised by anyone probing the feature.
The empty, clean, nothing-to-report path only gets exercised by a real user on their first try.

Guards added: a Go test asserting no response body contains a JSON `null` anywhere (Perfuse omits absent
scalars with `omitempty`, so a null *is* a nil slice or map), and an e2e sweep over read **and write**
endpoints — both known instances were in POST/PUT responses, so a read-only sweep would have missed both.

### Two levels of tab, and the suite only knew about one

The e2e suite asserted every top-level tab renders. It never clicked the sub-view buttons *inside* a tab,
which is where this crashed. Those controls are the worst case for coverage: they do not exist until an
async action completes, so a test that merely loads the tab cannot see them.

An audit found **eleven** such switchers, of which only two were touched by any test. Uncovered and
carrying unguarded nested access: the FHIR lab's three views, the document lab's four, message detail's
`Why this happened`, two Playground tabs, both window pickers, and the v3 field picker.

`web/e2e/subviews.spec.ts` now performs the action first, then clicks every resulting sub-view, using
valid input so the empty-result paths are the ones under test.

### The Messages fixture could never have had a message in it

The e2e fixture channel listened on `127.0.0.1:0`, an OS-assigned port, so nothing could send it
anything. Messages, message detail and the trace panel therefore had no data, and every assertion about
them was really an assertion about the empty-state placeholder row.

Worse, the first version of the trace test *passed* against that placeholder: it clicked the row, found
none of the three sub-view controls, and skipped all three with a `continue`. Reported green, exercised
nothing. The fixture now takes a known port and `web/e2e/mllp.ts` sends a real ADT through it; the three
controls are required rather than discovered.

### A slow test was a racing test

`builder.spec.ts` checked that every channel template produces a channel the loader accepts, and
intermittently reported two templates as broken. They were not. The YAML preview is rebuilt by a
debounced request, and the form builds its *default* draft on mount before the template is applied — so
the poll, which waited only for non-empty text, could return the default channel. The default has one
nameless destination, so the loader refused it and the failure read as a broken template.

Now it waits for the specific channel name the template produces, and says so when the preview never
becomes that. It also runs four times faster, because it is no longer polling through the debounce.

---

### Sorting timestamps as text, when the text was not fixed width

Found by running the Go suite for a status report and seeing an audit test fail that had passed in
`make check` minutes earlier. One in five, whole-package only.

`time.RFC3339Nano` **drops trailing zeros from the fraction**. Timestamps are stored as TEXT in SQLite
and every store orders and compares them as strings. So the same clock produced different lengths:

```
20:56:33.12345Z    <- 123450 microseconds, a zero dropped
20:56:33.123456Z   <- 123456 microseconds
```

As text the first is **greater**: after the shared `12345` it has `Z` where the other has `6`, and `Z`
sorts above every digit. The earlier instant sorted later. Roughly one adjacent pair in ten.

What it broke, both visible to a user:

- **The audit log listed same-second entries out of order.** A test writing five entries produced
  `three five four two one`. An audit trail is a compliance artefact and the order is the point.
- **The message browser** orders by `received_at DESC, id DESC`. Under load messages arrive in the same
  second, so the browser could list them wrongly — exactly when somebody is reconstructing an incident.

33 write sites across five packages all went through `Format(time.RFC3339Nano)`. Now `internal/dbtime`,
one fixed-width layout, `.000000000` rather than `.999999999` — zeros are significant in the first and
dropped in the second. `Parse` still accepts the old variable-width form, so existing rows read fine.

A migration pads the rows already on disk. Writing new rows correctly fixes the future; an audit log is
read for the past, so leaving history unfixed would keep the defect in the data it mattered for. Guarded
to be idempotent, since an interrupted upgrade gets retried.

Proved by property rather than by example, over 20,000 random pairs, because **examples cannot find
this**: every fraction anyone would think to write down — `.5`, `.25`, `.123` — sorts correctly under the
old format too. The failures are pairs where one fraction is a prefix of the other, which is not a case
that occurs to somebody choosing test data. The audit ordering test now runs 200 iterations with no
sleep, since writing as fast as possible is the condition that exposes it.

Two dicom tables are deliberately left out of the migration: they are created by a later migration so
they do not exist at that point, and their columns are compared against cutoffs days wide.

- **Verify by running, not by reasoning.** Three separate lists of resource types had already drifted, and the capability
  statement advertised fifteen types while the router answered 404 for six. Reading would never have found it.
- **Prove a guard fires by planting a violation.** Running total across sessions: **~24 tests that passed for the wrong
  reason, and six inert checks removed.** A test that cannot fail is worse than no test, because it is counted.
  Most recent: a null-sweep test whose assertion I had accidentally deleted while editing, leaving it
  unable to fail; and the route-protection test that walked a hand-written list while claiming to cover
  every route.
- **Test the case where nothing goes wrong.** Error paths get probed by anyone building the feature. The
  empty result, the clean input, the zero-findings response — those are reached first by a real user.
- **One list, not three.** Where two lists must stay separate, a drift guard holds them together.
- **A capability statement that overstates is worse than none.** A client trusts it, builds a query from it, and gets an error
  the statement said could not happen.
- **A feature that cannot work yet is refused at load, not skipped at run time.**
- **An expectation with nothing to check against is not a pass.**
- **A property, not examples, when the defect hides in the cases nobody would choose.** The timestamp
  ordering bug is correct for every fraction a person would write down by hand. Only randomised pairs
  find it.
- **An intermittent failure is a defect until proven otherwise.** This one presented as a flaky test and
  was a compliance defect in the audit log.
- **A test that skips is not a test that passes.** A null sweep written against the Go harness skipped
  seven of fourteen endpoints because that harness starts without an engine. Moved to e2e, against a
  real server, where the endpoints actually answer.
- **Never assert on something that was already on screen.** This is the single most productive rule of the
  session. The AI Mapper test matched `PID-3.1`, which is a textarea's default contents, and passed while
  every request behind it was refused with a 403. The channel-saved check matched the name in the
  still-open form's YAML preview. The trace-panel test skipped all three of its controls with a `continue`
  and reported green. Assert on something that can only exist if the work happened.
- **Validating is not running.** Three channel templates generated YAML the loader accepted and then
  refused to start, because their archive needed root to create.
- **Watch the network, not the markup.** A feature refused with a 403 on every request still renders a tab,
  a button and a plausible error message.

---

### The queue itself was wrong about six items, and always in the same direction

Recorded 2026-09-16, after working through items 4 to 12 in one pass. Six of nine were not what this file said they were, and every
error flattered the size of the remaining work rather than the state of the code.

- **Per-format block refusals** — asked for a table. `internal/config/formatblocks.go` already was one, with an exhaustiveness test.
- **X12 shadow mode** — said an X12 channel refuses a shadow. It does not; there is an X12 walker and thirty-three passing tests.
- **The delimited design decision** — said one document becoming many messages had to be settled before any code. It was settled, in
  the engine, with the rule written beside the implementation.
- **Shareable profiles** — described as open work. The versioned format, serialiser and parser existed with zero callers.
- **AI-assisted mapping** — "agreed in principle, never scoped". Built, including the abstention half, with clinically chosen
  dangerous-pair tests.
- **The Java scanner reframing** — genuinely unbuilt, but the scan it needed was complete.

Two lessons, and the second is the useful one.

**A queue entry decays differently from code.** Nobody runs it, so nothing fails when it becomes false. The entries that were wrong
were mostly *written before the work* and never revisited after it — the plan outlived the planning.

**Check the code before believing the plan, and check with something that fails.** Every one of these took under five minutes to
disprove: a grep for the symbol, a test run, a count of callers. What made them survive was that reading the entry felt like knowing
the answer. Note the trap that nearly caught me here: `go test -run TestNameThatDoesNotExist` prints `ok` and `PASS`, so the first
confirmation of a guard was itself false.

### An implementation with no caller is indistinguishable from a feature that does not exist

This has now happened seven times in this codebase, and it is the single most productive thing to go looking for.
The pattern: code that is written, correct, tested, and that nothing outside its own package ever calls. Every test
passes. Every review of the code finds it sound. And nobody can use it.

| Found | Size | How it was found |
|---|---|---|
| **AI Mapper** | a whole section | The tab rendered, the button was there, and pressing it had never worked because nothing had ever pressed it and watched |
| **`POST /api/certificates/inspect`** | one endpoint | The control sweep reported Certificates as having nothing operable |
| **`cda.Validate`** | 343 lines | Checked reachability of every exported function in the package after the certificate finding |
| **`cda.Generate`** | 854 lines | Same check |
| **`cda.ReconcileMedications`** | 416 lines | Same check |
| **`cda.MergeSections`** | in the same file | Same check |
| **The entire `tefca` package** | 346 lines plus 552 of tests | Same check, applied to another package |

The check is one line and worth running against any package that looks self-contained:

```sh
# for each exported function, count callers outside its own package
for f in Validate Generate ReconcileMedications MergeSections; do
  echo "$f: $(grep -rn "cda\.$f(" --include=*.go . | grep -v "^./internal/cda/" | wc -l)"
done
```

**Why tests do not catch it.** A package's own tests call its functions directly, so coverage looks complete and the
code is genuinely correct. What is missing is a caller in the product, and no test inside the package can notice
that. The only things that find it are a reachability check like the one above, or a sweep that operates every
control in the interface and reports a section with nothing in it.

**The correction that follows.** For each of these, an API test now asserts the capability is reachable, so it cannot
silently become unreachable again. That is the difference between fixing an instance and fixing the class.

---

### What was made reachable, and what it turned out to be for

Wiring these up was not a wiring exercise. Each needed a decision about what it should refuse to do, and those
decisions are the substance.

**Conformance checking** got its own panel rather than being folded into the existing agreement report, because they
answer different questions. Agreement asks whether the narrative and the coded entries say the same thing.
Conformance asks whether a receiver would accept the document at all. A document can pass either and fail the other,
and the failure people meet is the second — the partner refuses it and says only that it was non-conformant.

**Document repair** addresses the failure that made this whole area worth the effort. C-CDA carries every clinical
fact twice, and almost every viewer renders the narrative and ignores the entries. So a section with entries and an
empty narrative passes the schema, passes the receiver's import, is counted as a successful exchange at both ends,
and displays as a blank page. The medication list is in the file and invisible, and no error is raised anywhere.
"No medications" and "we failed to render the medications" look identical on screen.

It rebuilds narrative from the entries and **invents no clinical content**. A missing custodian is reported, not
chosen. Narrative that exists but is incomplete is reported and left alone: the narrative is the attested content of
a clinical document, and a program that silently replaces a human assertion with a derived one leaves no reader able
to tell which is which.

**PDF rendering** renders the narrative and not the entries, for the same reason. A section nobody attested prints as
visibly unattested with a count of what is behind it — not blank, which reads as nothing to report, and not filled
in, because a printout that will be filed is the wrong place to make that decision silently.

**Medication reconciliation** and **combining sources** both answer "is this the same patient?" before anything else,
because merging or comparing two people's lists produces a result that belongs to neither and looks entirely
plausible. Identifiers are compared within a shared assigning authority: an identifier is a root plus an extension,
and MRN 12345 from two hospitals is a coincidence, not a match.

**TEFCA** had a worse problem than its missing audit wrappers: the audit log was in memory only. Recording every
exchange is a condition of participation and the records have to exist months later. A slice in memory satisfies
every test, works in a demonstration, and loses everything at the first restart. Nothing fails and nobody is told,
which is worse than having no audit feature because the presence of one is relied upon.

---

### Signatures: "valid" is five questions

Reducing them to one badge is how a verifier misleads people. The bytes are intact, or not. The signature came from
the key in that certificate, or not. The signing time and claimed capacity are covered by the signature, or editable
by anybody. The certificate was valid when used, or not. And it is trusted here, unknown, or **was never checked**.

A document whose certificate has since expired is not a forgery, and showing it identically to an altered document
sends somebody to look in entirely the wrong place. "Not checked" shown as "failed" is how a verifier reports every
document as untrusted and teaches everybody to disregard the field.

**Canonicalisation is where signing goes wrong**, so it was built and tested on its own before any signing code
existed. Every mistake in it produces signatures that verify against your own output and fail against everybody
else's, and the message at the far end is "signature invalid" with nothing to say why. The tests are pairs of
documents any parser agrees are equivalent, asserting they canonicalise identically.

Two attacks are refused explicitly. **Duplicate identifiers**: an attacker appends a second element carrying the ID
the signature references, the verifier validates the first, and the application reads the second. **Certificate
substitution**: without binding the signature to a specific certificate it verifies against any certificate with the
same public key, so one naming a records clerk can be swapped for one naming a consultant with every cryptographic
check still passing.

---

### A test that would pass against broken code is worse than no test

Forty signature tests passed on the first run, which for something that fiddly is suspicious rather than reassuring.
So the certificate digest comparison was disabled deliberately, the substitution test was confirmed to fail, and the
check was restored. Worth doing whenever a difficult thing works first time.

Three tests in this stretch were passing while proving nothing:

- The end-to-end signature tests **returned early when the sign button was absent**. The fixture server had no
  certificate, so that branch was always taken: they passed against a server that could not sign at all.
- A test read a textarea's contents with `textContent`, which is empty for a value set programmatically, so it signed
  an empty string.
- A canonicalisation test compared inclusive and exclusive forms over a whole document, where they do not differ.
  Finding out why exposed a real gap: the operation signatures actually need — canonicalising a *subset* — did not
  exist.

---

### Check whether the package already exists before writing it

I created `internal/pdf/pdf.go` over an existing file of that name. A deliberately minimal monospaced text renderer
was already there, used by a channel destination, with a doc comment arguing against pulling in a layout engine.
Restored from git within a minute and nothing was lost, but the mistake was not checking first.

Having read it, the right answer was additive: a structured builder beside the text one, sharing its page constants
and its escaping. `Render` keeps its contract. Two documents from one product with different margins is a detail
somebody notices on a printout and cannot explain, and one package with two escaping rules eventually has one that
is wrong.

---

### Operating every control in every section

Asked, with justified irritation, to stop reporting on coverage and actually use all twenty-one sections. The
result was a self-discovering sweep — `web/e2e/use-everything.spec.ts` — that opens each section, finds every
button, input, select, textarea, disclosure and non-native control inside it, and operates each one while
watching what the browser reports.

**Nine of twenty-one passed when it was written. All twenty-two pass now** — twenty-two because a TEFCA section was
added afterwards, and the sweep found it had nothing operable on an instance that does not participate. That was a
product finding, not a test problem: deciding whether to take part means knowing what would be declared, and that
question comes before any configuration exists.

**Why self-discovering rather than a list.** A list is written once against the interface as it was, and the
control added next month is not in it. The AI Mapper proved the cost: the tab rendered, the button was there,
and the feature had never once worked because nothing had ever pressed it and watched what happened.

**Two rules stop it being theatre.** It counts what it operated and fails when the count is zero, so a locator
that stops matching is a failure rather than a silent pass. And it judges by consequences that can only follow
the interaction — an uncaught exception, a refused request, a blank panel — never by text that was already on
screen.

**It distinguishes a handled refusal from a silent one.** Typing nonsense into a field *should* produce a 400.
What matters is whether the person who typed it is told. So a 4xx is a fault only when the section reports
nothing; a 403 is always a fault, because it means the interface sent the request wrongly; a 5xx is always a
fault, because the server failed instead of refusing bad input.

#### What it found in the product

| Finding | Why nothing caught it before |
|---|---|
| **An administrator could demote themselves out of the system in one click.** Every user row has a role dropdown that saves on change; on your own row that removed your own admin rights. The sections needed to undo it are the first thing you lose, so recovery is database surgery | The existing guard only protected the *last* administrator. With a second present the change was allowed — the case that looks safe |
| **A blank username was reported as a server crash.** Validation returned a plain error with no sentinel, so the error mapper fell through to 500 "something went wrong" | Nothing tested the empty-field path. An administrator would check logs, restart, and open a ticket for a blank field |
| **Six copy buttons could not report failure and one lied.** All called `navigator.clipboard.writeText` directly. One had no optional chaining, so it threw where the API is absent — which is any plain-HTTP deployment, a configuration this product supports. Four discarded the rejection. The Playground displayed "Link copied" regardless | A copy that silently fails looks identical to one that worked. Copying an API token this way gets pasted wrongly and debugged as an auth fault |
| **The confirmation dialog was not a dialog.** No role, no focus move, no Escape, no focus trap — in front of stopping a live interface or deleting an account | Visually identical to a real one |
| **Error boxes were coloured and silent.** No live region, so a refused save announced nothing | They appear after an action, which is exactly when a screen reader user has no reason to go looking |
| **Five dashboard buttons announced identically** as "Stop, Stop, Stop, Start, Start" — on the page whose purpose is stopping the thing misbehaving | Found by needing to address one unambiguously from a test |
| **Three sections could only be read about, not used** — Contracts, Tables and Fleet each required a terminal on the server. Fleet printed a CLI command and a YAML file to hand-write | The sweep reported them as having no operable controls, which was the finding |
| **One endpoint had never been called.** `POST /api/certificates/inspect` was written, protected and tested, with no caller | An endpoint with no caller is indistinguishable from a feature that does not exist |

Three latent faults surfaced while fixing those: a fleet built from a zero config **panicked in `NewTicker`** at
startup (harmless only while `Start` returned early with no peers); the fleet label was read without the lock
its own comment demands; and channel YAML validated in isolation cannot resolve relative companion paths, so a
correct contract reference failed with "no such file or directory".

#### What it found in itself, which is worth as much

Four faults in the harness, each of which would have produced a confident wrong answer:

- It **clicked the Dismiss button on the error box** it was about to check for, then reported a silent failure
  that was not silent. A harness that destroys its own evidence costs as much trust as one that misses a defect.
- It **pressed Stop on the channels** every later section depended on, so failures surfaced sections away from
  their cause.
- It **guessed at error wording**. A refusal reading "this does not look like an HL7 v3 message" is perfectly
  good and matches no keyword anybody would list. It now looks for an alert region first — and requiring that is
  what made every error box announce itself.
- It **did not know a `summary` element is a control.** Ten exist here, two of them in Alerts, which is why that
  section reported having nothing to operate: everything was behind a disclosure the sweep could not see.

#### The reusable checks

```sh
# UI gated on a minimum count - a quiet coverage hole, because the section looks fully exercised
grep -rnE "\.length > [1-9]|\.length >= [2-9]" web/src/*.tsx

# controls that are not buttons, inputs, selects or textareas
grep -rnE "<summary|role=\"(switch|tab|button)\"" web/src/*.tsx
```

#### Two ordering hazards, both real bugs in the tests

A test that leaves shared state changed produces a dozen unrelated failures elsewhere. The Live/Paused toggle
persists, so a sweep that pressed it made a later test fail — the test now reads the starting state rather than
assuming it, and puts it back. The same applies to the fixture: adding a second shadowed channel broke a sibling
test that had passed only because a single one made the default selection unambiguous.

**And one honest note on a timeout.** After the suite grew from 127 tests to 159 driving real traffic, two
different tests failed on two consecutive full runs and both passed alone in seconds. That is load, not a
regression, so the tab-render poll went from 10s to 30s. A previous session recorded an "unexplained"
intermittent failure here that turned out to be exactly this, which is why it is written down rather than
quietly adjusted.

Asked whether last night's pass really covered every section. Checking rather than repeating the claim: all
21 sections are opened by at least one spec, and that part held. But *opened* is not *used*, which the same
session had already proved — the AI Mapper rendered perfectly and had never once worked.

Measuring interaction depth instead of visits surfaced the real gap. The Shadow view has a channel selector
that only renders when `summaries.length > 1`, and the fixture had exactly one shadowed channel. No test had
clicked it, and none could. Same shape as the channels filter that only appears past eight channels, which
only the full suite exposed.

**Behind it, two mistakes.** The per-channel fetch swallowed its error, and the report was not cleared when
the selection changed. So choosing a channel whose report failed to load left the *previous* channel's
comparison on screen with the new channel highlighted. Every other view showing stale data during a failed
refresh is an annoyance; here the view's only job is to answer whether a configuration change is safe, and a
confident answer about the wrong channel is how somebody promotes an unverified candidate.

**Honest note on the fix.** Either half satisfies the test on its own — with only the clearing removed it
still passes, because the error panel renders in place of the report. The load-bearing half is surfacing the
error. Both were kept: clearing covers the gap during a slow but successful load, surfacing covers a
permanent failure.

**A knock-on worth recording.** Adding the second shadowed channel broke a sibling test, which had passed
only because a single shadowed channel made the default selection unambiguous. It was asserting on whichever
report happened to load. Both tests now choose their channel explicitly.

**And an accessibility defect found by needing a stable locator.** The selector appends the difference count
straight onto the channel name, so it read as `shadowed1` — which sounds like a channel called shadowed1
rather than a channel with one difference. Now labelled `shadowed, 1 message differed`, with `aria-pressed`
for the selection. The visible text is unchanged.

**The general check, worth repeating before any release.** Grep for UI gated on a minimum count:

```
grep -rnE "\.length > [1-9]|\.length >= [2-9]" web/src/*.tsx
```

Seven sites today. Five render text or a chart. Two were interactive: the channels filter at eight, fixed
last night, and this selector at two. A count gate is a quiet coverage hole because the section looks fully
exercised — the tests visit it, click what is there, and pass.

**What I would still not claim.** Depth across the 21 sections is uneven. The specs that walk sections
mostly assert that something rendered, and last night's most productive rule was that asserting on what is
already on screen finds nothing. Sections with a dedicated `use-*` spec have real coverage; the rest are
visited and inspected.

Asked whether the channel engine could run out of room, and how to make that impossible rather than
unlikely. The engine spawns a goroutine per destination per message. Goroutines are cheap; what they hold
is not.

**The arithmetic that matters.** With default retry — five attempts, thirty second timeout, fifteen seconds
of backoff — one delivery to a receiver that has stopped answering holds a descriptor for about
**165 seconds**. A few hundred senders against one dead partner is therefore a few hundred descriptors held
for minutes. The soft limit on macOS is often 256.

**Why that specific failure is worth engineering against.** Running out of descriptors does not fail where
it ran out. It fails as `too many open files` somewhere unrelated: SQLite cannot open a journal, the web
interface stops accepting, a log cannot be written. Three broken things, one cause, nothing on screen
connecting them to a partner system that went quiet. That is the shape of an expensive support call.

**The fix, in order of what each part buys:**

1. `internal/fdlimit` raises the soft limit to the hard limit at startup. On a stock Linux container that is
   1024 → 1048576, which removes the problem outright rather than policing it.
2. `internal/admit` bounds deliveries in flight, with a **total** and a **per-destination** limit.
3. The limits are **derived** from the descriptor limit, never configured. A limit somebody has to set is a
   limit nobody sets until after the outage.
4. Saturation is published as metrics, and the arithmetic is logged at startup in prose.

**The per-destination limit matters more than the total, and the acquisition order is the crux.** A total
alone does not fix the problem, it moves it: the dead receiver's deliveries consume the whole budget and
every other channel is blocked by a receiver it does not send to. Blast radius grows from one interface to
all of them, which is worse than no limit.

So the per-destination slot is taken **first**, then the total. Taken the other way round, waiters for a hung
destination hold a slice of the process-wide budget *while they wait*. The test for this plants the swapped
order and fails with the reason.

**What happens when a slot cannot be had.** Wait for the destination's own timeout, then treat it as a
delivery failure — which routes into the queue path that already exists for a receiver that will not answer.
A destination saturated with stalled deliveries *is* a receiver that is not answering, so handling it
identically is honest rather than convenient. Unbounded waiting is how a limit becomes a hang.

**One controller per process, enforced structurally.** `admit.Shared()` is a singleton, which a package-level
variable usually should not be. The justification: the resource is the process's descriptor table. Three
places start channels — the engine behind `perfuse run`, the server's runtime, and one runtime per tenant —
and anything building its own controller would believe it had the whole table. Ten of them permit ten times
what the process can afford, which is not a limit but arithmetic that happens to be smaller than infinity.

**An environment too small to work says so at startup**, naming `LimitNOFILE` and `ulimit -n`. Below the
reservation the limits fall back to minimums, which is a guess; being told at startup is the difference
between raising a container setting and debugging SQLite during a busy hour.

**Honest note on the integration test.** The assertion that catches the defect is the *peak in flight*, not
the "other channel still works" one. At twenty stalled deliveries the other channel gets through regardless,
because twenty is nowhere near exhausting a descriptor table; reproducing the real outage would need
hundreds of connections and minutes. Asserting the invariant fails immediately instead of asserting the
symptom and hoping the scale is enough.

**Not addressed, recorded rather than left implied:** inbound connections are still unbounded per channel by
default (`max_connections: 0`). Each is one descriptor and they are long-lived, so the multiplier is on the
outbound side — but a client that opens a connection per message would still exhaust the process. Bounding
that means changing a default, which deserves its own decision.

Asked to actually use every section rather than confirm it renders. Five defects the existing checks had
no way to see:

| Defect | Why nothing caught it |
|---|---|
| Three channel templates could not **start** — the archive pointed at `/var/lib/perfuse/archive`, which needs root | The existing test checked the loader accepted the YAML |
| **No label in the channel builder named its input** — `Field` took an optional `htmlFor` almost nobody passed | Invisible; the text sits above the box either way |
| Two controls were announced as a **paragraph** — the wrapper enclosed the required marker and the whole hint inside the label | Also invisible, and the visual result is identical |
| **The AI Mapper had never worked** — a raw `fetch` with no `X-Perfuse-Request` header, refused as cross-site every time | The tab rendered, the button was there, and the assertion matched a textarea's default text |
| Choosing DICOM C-FIND **emptied the preview and said nothing** — the build error was discarded | Nobody had ever chosen it in a test; five sources behaved this way |

Plus ten form controls with no accessible name at all, across nine sections.

Labels are now asked as a property over every section, because the defect is invisible and could therefore
be anywhere: a label's `for` must reference an input, select or textarea, and every visible control must
have a name from somewhere. Pointing `for` at a `div` is worse than leaving it off — it looks associated
and is not.

**My own races, all one race.** Every failure while writing these was the debounced YAML preview being read
before it rebuilt, returning the previous draft. It once reported all eleven source types broken when every
one was fine, and earlier made two channel templates look defective. Wait for an outcome that can only
follow the change, never for text that is already present.

---

### Three assertions in one day that could not have failed

Not one bug — the same mistake three times in a single session, which is what makes it a pattern rather than an
incident.

**A leak test aimed at a path that does not exist.** The generated-test-message work needed proof that no real data
survives. I injected a real surname at `PID-5.1` and asserted it was absent from the output. It passed. It would have
passed against a generator that copied messages verbatim, because the profiler records **whole fields** — there is no
`PID-5.1` in a profile, so there was never anything to match. Re-aimed at `PID-5`; re-proven by the injection failing on
both surname and forename.

**A kind-coverage test that iterated the map it was checking.** It walked the dispatch map's own keys and asserted each
was in the dispatch map. Always true. Now it reads the `Kind` constants out of `alerts.go` with a regexp, and removing
one from the dispatch map fails naming the constant.

**A verdict asserted from stale state.** Recorded earlier, same shape.

The common form: **the assertion and the thing it checks come from the same source.** A test that cannot distinguish
correct code from broken code is not weak, it is absent — and worse than absent, because the coverage number counts it.

The check that costs thirty seconds: break the code deliberately and watch the test fail. If it passes, the test is
decoration. Done for every teeth-critical assertion this session, and it caught all three.

### DiffXML on raw HL7 returned nothing, and nothing meant "identical"

The parity checker compares Perfuse's output against the engine being replaced. I reached for `shadow.DiffXML`. It
compiled, ran, and returned no differences — because it parses its input as XML and hands back nil on anything else.

My code read "no field differences" as "differs only cosmetically." So every genuine difference was counted as
harmless, and the report said **perfect parity on messages whose fields had changed.**

That is the worst possible failure for that feature specifically. Its entire purpose is to be believed when somebody is
deciding whether to move a live clinical feed, and it would have told them their migration was clean.

The right function, `shadow.Diff`, was twenty lines further down the same file. Found only because a test asserted four
thousand differences and got zero.

The shape: **a function that returns a zero value on input it does not understand, called from code that treats the
zero value as a meaningful answer.** Nothing errors anywhere. Prefer functions that refuse; where they cannot, check the
precondition at the call site rather than trusting the return.

### An empty-to-empty change was counted, and shown, as a change

A `copy` from an absent field produced a `Change` from `""` to `""`. The stage-level trace reported three changes where
two fields had changed, and the interface displayed the third — telling somebody their step had worked.

Found by a test asserting the per-step and stage-level views agree, which I expected to pass.

The fix broke `TestDateFailureIsLoudByDefault`, because `date` with `on_error: keep` deliberately keeps a value and
explains why. My first discriminator checked whether the step left a `Note` — also wrong, because `copy` always sets one
for provenance. Needed an explicit `Change.Deliberate`, set only in the date `keep` branch.

Worth remembering for the second half: **the obvious discriminator was a side effect that another feature also
produced.** Two things that look alike from outside usually need a field that says which, not an inference from
something incidental.

### Wrote before reading, twice in the same hour

Set out to build `internal/replay` for step-by-step message replay. Got most of the way through before checking
properly. `engine.TraceMessage` already existed — with a panel, an endpoint, per-field reads, skips with reasons,
destination decisions, and staleness detection I had not thought of. Deleted the parallel implementation and folded the
genuinely new part into the existing code.

In the same session, created `internal/api/trace.go` with `fs_write create` and **overwrote 132 lines** including that
staleness detection. Recovered with `git checkout`.

This is section 5's rule and the rewrite rule at the top of this file, violated twice on the same day they were most
relevant. The grep costs seconds. `fs_write create` on a path that might exist costs an hour.

### The manual was different on every build

Package comments were read from Go's file map, and `hl7` has one in `doc.go` and another in `message.go`. Map iteration
order is random, so the manual described that package one of two ways per build. Compounded by sorting results on
package **name**, which is not unique — `hl7`, `winservice` and `compliance` each occur twice in this tree.

The staleness check caught it, but presented as a test that failed immediately after regenerating the file it was
checking. That reads as a broken test, and a broken test gets rerun rather than investigated.

**A nondeterministic build makes every downstream check flaky rather than failing.** Now: `doc.go` preferred with an
alphabetical fallback, sorted by import path, and an explicit test that two builds produce identical bytes so the cause
is named rather than inferred.

### Documented the behaviour backwards, at length

Wrote a chapter section explaining that acknowledgement is sent before delivery, with several paragraphs justifying the
trade. The default is the opposite: `ack.when` is `on_delivery`, and the reasoning is recorded in the constant's own
comment.

Caught by reading the constants instead of trusting what I had just written.

The shape is worse than a wrong line, because **confident prose is read as authority and nothing downstream checks it.**
A wrong config key fails a build. A wrong explanation of a safety-critical default gets followed. Generated reference
chapters cannot drift; hand-written reasoning has no such protection, and every paragraph of it needs the same treatment
as a claim in a commit message — check the code, then write.

### Adding a capability turns a correct guard into a silent skip

Three times in one session, and it is now the shape I expect rather than one I was surprised by.

**A `scripts:` block on a DICOM, X12, pharmacy, delimited or raw channel validated and never ran.** `Channel.handle`
dispatched those six to their own paths *before* the preprocessor, filter and transformer stages, so the scripts were
parsed, validated, compiled and never executed. The guard existed for X12 alone and was not extended when the other five
paths were added. Proven rather than reasoned about: a raw channel whose only script was a bare `throw` accepted a message
over MLLP, delivered it, wrote the output file, and never ran the script — and `perfuse check` called the channel valid.

**Destination filters were skipped on every non-HL7 channel, and I introduced that one myself.** The filter was evaluated
inside `deliver`, which read the HL7 expression off the destination; a channel with no parsed HL7 form passed `nil` and the
whole block was skipped. That was correct *only* because those types refused destination filters at load, and there was a
comment saying so. Making X12 filters compile broke the assumption without touching the comment. Every message would have
gone to a destination the configuration said to exclude. The fix is that the caller supplies the evaluator and a filter that
cannot be evaluated is an **error, not a pass** — `HasFilter` asks the configuration rather than whichever compiled field
happened to be populated, so the guard cannot be fooled.

**Destination filters were compiled as HL7 regardless of channel type.** For X12 this mostly *worked*, which is worse than
failing: `CLM` and `ISA` are plausible HL7 segment names, so the filter compiled into a field the evaluator never reads.

The shape: **a refusal is load-bearing, and the code that depends on it is somewhere else.** Removing a refusal is not a
local change. Every time, the honest fix was to make the dependent code fail loudly rather than assume.

### A test that stopped being able to fail, without anybody editing it

The transformation engine re-reads a path after writing it, because a write can be *neutralised* — setting an X12 ISA
element to a shorter string re-pads it, so the request differs from what was there and the result does not. Comparing the
request against the previous value would report a change that did not happen.

That protection was found by a trim on an ISA element. **That case is now refused at load**, which removed the only
scenario exercising it. Deleting the re-read caused **zero** test failures.

This is not the same as the "assertion that could not fail" entries above, and the difference matters: nobody wrote a bad
test. A correct test was made vacuous by a *later, correct* change somewhere else. Found only because I break the
implementation and count failures as a habit, on a refactor I could easily have called mechanical.

Which is the argument for that habit. **A passing suite says nothing about whether it would notice.**

### `nullflavor` worked and `== "19551014"` did not, in the same filter language

The v3 filter had nine operators. Exactly one of them — `empty` — knew that a v3 value usually lives in an attribute, and
its comment recorded that mistake as *"the mistake v3 invites"*. The fix was never carried across to `==`, `!=`, `matches`,
`in` or the ordering operators, which all read element text.

So `//birthTime empty` correctly reported a value present, while `//birthTime == "19551014"` could not match
`<birthTime value="19551014"/>`. **Every comparison against a v3 attribute value silently answered false** — for a filter,
that means every message excluded or every message let through, with nothing in the log to say why.

Found only by unifying the two grammars, and **I nearly reintroduced it**: my first resolver used `Path.Values`, which is
text-only for a bare path. The comment on the old `empty` node is the only reason I caught it.

The shape: **a fix applied to one operator and not its siblings.** A bug fixed in one branch of a switch is a bug still
present in the other six, and the fixed one is the one with the comment explaining the danger.

### Two greps, two confident wrong answers about what code does

Grepping for `script.` and `Scripts.` in the v3 handler returned zero matches. Both times the conclusion — "v3 does not run
scripts" — was wrong; `handleHL7v3` runs them through `ScriptEngine()` and `runV3ScriptStage`. The existing tests caught it,
four of them, which is the only reason it did not ship.

**A grep answers a question about text, not about behaviour.** For "does this code path do X", read the handler.

### One verified sample is not a verified list

A new guardrail reported that the graphical builder could not express **97** channel settings. 63 of those were not gaps. The
walk skipped any embedded struct whose type name is unexported — right for an unexported *data* field, wrong for an embedded
one, and the builder embeds several unexported structs deliberately so that the TCP source and destination can share framing
settings and the polling sources can share their poll fields.

The part worth remembering is not the bug, it is **how close it came to being believed.** The first implausible-looking entry
was checked: `source.ftp.dir` looked wrong, so I confirmed that the FTP source struct genuinely has no `dir` while the file and
sftp sources do. It does not. The test was sound *for that path*, and I stopped there and wrote 97 into a commit message.

What should have prompted a second check was the number itself. 97 missing settings is not a plausible amount of drift in a
form that is otherwise carefully built, and implausibility is evidence. The real number is 17, now zero.

The shape: **a new measurement that returns a shocking number is more likely to be a broken measurement than a shocking
truth**, and one confirming sample is exactly enough evidence to stop looking at the wrong moment.


## 9. Not code, and not mine to settle

- **Pricing is set:** free source, free to individuals, $2000/month clinic, $5000/month hospital, flat.
- **The IP question is live and is being handled separately.** One thing recorded here because it is a technical decision with a
  deadline attached and nothing else in the repo holds it: **the request for a written scope release should go to PharmaPoint
  before publication, not after.** Before, it is an employee being straightforward; after, it is a demand letter. That timing
  is the whole difference, and it is a decision about the repository as much as about the contract.
  **The surface this covers grew on 28–29 August.** NCPDP was removed on 22 August because Clause 13 names pharmacy
  dispensing, then rebuilt deliberately and given a path language, a filter, transformation steps and pipeline wiring.
  Pharmacy is no longer a hole and is no longer cheap to undo — it is load-bearing for the shared filter and step engines'
  claim to work across formats. That does not make the rebuild wrong; it makes the timing of the release request matter more
  than it did when this note was written.

---

## Standing invariants

Not negotiable while working this queue. Each was decided for a reason recorded in the commit that introduced it.

- Channels live in files, never in the database. Runtime state lives in the database.
- Settings live in a YAML file beside the database; the file wins and flags only seed it.
- A feature that cannot work yet is refused at load, not silently skipped at run time.
- Unknown YAML key, JSON field, alert kind, script permission, settings key or search parameter is an error, never ignored.
- A shadow, replay or trace channel has no senders and cannot deliver — enforced by construction.
- Credentials never appear in a description, summary, specification, log, metric label or audit detail.
- Metric, alert and trace labels come from configuration, never from message content.
- Go maps range randomly, so everything user-visible is sorted. These outputs get diffed.
- Transformation order is filter → declarative steps → scripts. Same for v3.
- ACK: delivered→AA, filtered→AA, queued→AA, partial→AE, failed→AE, unparseable→AR. v3 uses CA/CE/CR.
- Security controls an author could grant themselves are worthless.
- `make check` green at every commit.
- **Documentation is part of the change, not a follow-up.** Anything built, modified or removed updates the documentation in the same
  commit: the reference manual for configuration and behaviour, this queue for what is now done or newly known, and the doc comment on
  whatever was touched. A feature nobody can find in the documentation is one nobody outside this machine can use, which is the same
  defect as a feature that cannot be reached from the interface. `make docs` regenerates the generated chapters and
  `internal/manual/manual_test.go` fails when a configuration key is undocumented, so the enforced half is enforced - the unenforced
  half is prose, and that is the half a reader actually needs.

---

## 10. Regulatory features — what hospitals must have

- [~] **TEFCA gateway participation.** Partly done and stated honestly elsewhere in this file: purpose-of-use validation,
      configuration validation and the audit trail are real; the QHIN transport is not implemented and is refused rather than faked.
      Tracked as the single open item in §3, not here. **Do not treat this line as the status.**
- [x] **Bidirectional public health reporting (eCR/ELR).** `internal/publichealth` exists. Note that CMS *suppressed* the eCR
      measure for CY2026 because the CDC paused onboarding, so the deadline pressure this entry implied is not current.
- [x] **CMS ADT Event Notification compliance workflow.** Real-time ADT routing to established providers with
      delivery confirmation, provider registry, and audit trail for CMS surveyors. Required since May 2021.
- [x] **Prior Authorization FHIR API (CMS-0057).** Submit prior auth requests as FHIR resources, receive
      decisions back. Handles the workflow end-to-end. CMS deadline January 2027.
- [x] **PHI breach detection and compliance audit.** Real-time anomaly detection on message flows (unusual
      volume, after-hours, new destinations), instant compliance reports showing what moved where and when.
