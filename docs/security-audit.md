# Security audit

Started 2026-08-21, prompted by finding that `perfuse fhir serve` had no authentication at all.

Every finding below was found by **running the software**, not by reading it. That is the single most useful thing in this
document: reading the code found nothing. Four of the first five needed a request sent to a live process, and the fifth
needed a channel started and a message pushed through it.

## Method

For each network listener and each role, the question was the same: *what stops somebody who should not be able to do
this?* Then the attempt was actually made against a running server, and the answer recorded.

Where a defence was added, the defence was then **removed again** to confirm the new test fails. This caught one test of
mine that proved nothing (see finding 1) and would have shipped otherwise.

## Findings

### 1. The FHIR server had no authentication — fixed

`POST /fhir/Patient` with no credentials returned **201**, and a plain `GET` read the record straight back. Any host that
could reach the port could read and change every stored patient record.

Not a check that was wrong — a check that was **absent**. There was no configuration to get wrong and no warning to
ignore, which is why it survived.

Fixed: `NewServer` leaves the authenticator nil, nil refuses every request, and `perfuse fhir serve` will not start until
one of `-auth-db`, `-token` or `-no-auth` is given by name. Running open is still possible for a loopback conversion
pipeline, but it is now something somebody typed, and it warns in capitals.

**My first test for this proved nothing.** It called the middleware directly, so it passed with the hole restored — the
middleware was correct and unreachable. `TestHandlerItselfEnforcesAuth` goes through the real entry point across all eight
routes and does fail.

### 2. The console password could be guessed at 20 attempts a second — fixed

Measured: 60 wrong passwords for the administrator account in 3 seconds, every one answered 401, no delay, no lockout.
Failures were *logged*, which is what made it easy to miss — something was clearly happening on every attempt.

Fixed with `internal/authlimit`, shared with the FHIR server. Five failures, then a doubling delay to five minutes, keyed
on **source address** rather than username — keying on username lets anybody lock out a named account deliberately.

The delay overflow would have been silent: two guards cover it, and with both removed the delay reaches minus 350,000
hours at the 39th attempt, which reads as "not blocked" precisely when it is working hardest.

### 3. A script with file access could read Perfuse's own credential database — fixed

`scripts.allow: [file]` meant the **entire filesystem**. A channel created through the console read one file, wrote it to
another, and read the Perfuse database — password hashes, the LDAP service account password, the OIDC client secret.

Severity precisely: creating a channel needs **editor**, starting one needs **admin**. So an editor cannot finish it
alone — they plant it, and an administrator performing the routine action of starting a channel executes somebody else's
arbitrary file access. A confused deputy, and nothing about starting a channel suggests you are granting anything.

Fixed: `scripts.file_roots` is required whenever `file` is granted, refused at load if absent. Paths are cleaned before
comparison, symlinks resolved on the deepest existing ancestor (so a not-yet-created file under a symlinked parent is
caught), roots resolved too, and the comparison requires a separator so `/var/perfuse` does not admit `/var/perfusehack`.

### 4. Any host could push images into a clinical DICOM channel — fixed

The listener checked the **called** AE title, which is the name a caller dials. It proves nothing about who is calling.
Who actually called was logged and otherwise ignored.

This project's own code already knew better: the comment on the DICOM *destination* says sites configure a PACS to accept
one specific calling title and reject everything else. Written while building the sender, never applied to the receiver.

Fixed with `allowed_calling_ae`, verified against DCMTK's `storescu` — a listed title stores, an unlisted one is rejected
with reason 3 ("calling AE title not recognised") and stores nothing.

Empty still accepts anybody and warns at every start. Refusing by default would break every first installation, and a
check that blocks everything gets switched off wholesale rather than filled in.

### 5. The read-only role could read every downstream password — fixed

A **viewer** account requested a channel definition and got the source token and SFTP password in full. Every credential
in every channel.

Fixed: redacted for viewers, unchanged for editor and above (an editor can already repoint a destination, so withholding
the password buys little). Works on the parsed YAML tree, not on text. Unparseable YAML is refused rather than returned,
which is exactly the case where a fallback leaks everything.

**A drift guard found two credentials my own hand-written list had missed** — `key_passphrase` and `secret_access_key`,
both real, both being served to viewers. The guard reads every struct tag in `internal/config` and fails on anything that
looks like a credential and is not accounted for.

### 6. A destination could read the internal network — controlled

A SOAP destination with a response transformer returns the receiver's reply to a script. Demonstrated: a SOAP destination
aimed at a local HTTP server put that server's response body into a channel log line.

Not a hole by itself — delivering to configured endpoints is the whole purpose, and an editor is meant to configure them.
What makes it worth controlling is one address: **169.254.169.254** serves the instance's own role credentials to anything
that can make an HTTP request from the machine.

`internal/egress` now refuses metadata and link-local addresses (AWS/Azure/Google, Alibaba, Oracle, IPv4 and IPv6).
Ordinary private ranges are **not** blocked — hospital systems live on them, and a control that refuses the normal case
gets switched off entirely.

The exemption is `-allow-metadata-egress`, an **operator flag**, deliberately not a channel setting: a channel author is
exactly the person the control restrains.

**A bug found alongside it, wrong in both directions:** validation said response transformers apply to `mllp, http, fhir`;
the senders that actually implement `Responder` are **MLLP and SOAP**. So `http` and `fhir` passed validation and failed at
startup, and `soap` — which works — was refused at load and unusable. Both lists are now derived from one source with a
drift guard, plus a guard on the guard that walks every destination type.

**A tradeoff I got wrong first:** the initial version resolved hostnames during validation, taking the config tests from
under a second to eleven — and making a channel's validity depend on whether DNS answered. Now literals only during
validation, with resolution at channel start.

### 7. One tenant's administrator could delete another's — fixed

**The worst finding.** Demonstrated end to end: alpha's administrator demoted beta's administrator, disabled them, then
**deleted the account**. One customer on a shared server could lock another out of their own integration engine.

User identifiers are global integers and the by-identifier handlers used `WHERE id = ?` with no tenant filter. Counting
upwards was the whole attack.

Fixed by scoping the lookup — which closes read, modify and delete together, since both handlers look a user up first —
then scoped versions of every by-identifier mutator as well, each confirming ownership independently. Belt as well as
braces, because relying on the earlier lookup is the invisible coupling that caused this.

**Two more in the same pass:** the audit log was unscoped, so a tenant admin saw every tenant's usernames and changes.
And the settings page added the same night was readable by any admin, when those files are process-wide.

**A quieter one:** because the unscoped user list defaults to the *default* tenant rather than all tenants, a tenant admin
was shown — and could manage — the default tenant's accounts while their own were invisible.

**My first test for this passed for the wrong reason and I nearly accepted it.** With the filter removed it still passed,
because the refusal was 409 "only enabled administrator" rather than a not-found — it asserted an unrelated safety check.
Both tenants now get a spare admin so that check cannot fire, and the test insists on 404 specifically.

**The guard was also wrong first:** its list said `GetUser` and the method is `GetUserByID`, so exact comparison matched
nothing and the guard passed while the leak was live. It matches prefixes now, which found four more unscoped mutators.

## Cleared

Checked and found sound. Recorded so nobody re-checks them:

- **Console API route table** — everything behind roles; only sign-in, health and the two probes are open, and none
  disclose anything a port scan does not.
- **Path traversal on channel names** — strict allowlist; every traversal, encoding and NUL byte tried was neutralised.
- **Request body limits** — every network body bounded.
- **Decompression** — the only decompression in the tree is a local CLI on the operator's own file, streamed with a line
  cap. Nothing decompresses anything from the network.
- **Script timeouts** — an infinite loop is interrupted and the runtime survives it. Already tested.
- **TLS verification** — every `InsecureSkipVerify` is an opt-in named setting, including the one hardcoded in the peers
  client, which is only selected for a peer that asked for it.
- **HTTP source bearer token** — constant-time comparison.
- **`route` script permission** — sends to another channel by name with no allowlist. Considered and deliberately left:
  it stays inside this engine, and an editor who can author a channel can already add a destination.
- **Session invalidation** — logout, disabling an account, and changing a role all invalidate existing sessions
  immediately. Verified by capturing a token and replaying it after each.
- **CSRF** — mutating requests require `X-Perfuse-Request`, which a cross-site form cannot set, plus `SameSite=Lax` on
  the cookie. A form-encoded POST with a valid session cookie gets 403.
- **Audit log** — append-only; no delete or prune path exists anywhere. Survives user and tenant deletion. Readable by
  viewers, which is appropriate: usernames, actions and IPs, no credentials.
- **HTTP destination responses** — not stored, not logged, and not available to a script. Only MLLP and SOAP return a
  reply, which is why the egress control targets those.
- **XML parsing (XXE and billion laughs)** — verified by sending the payloads, not by reading the decoder settings. An
  external entity cannot read a file off disk or reach the network, nested entities do not expand, and a 50,000-deep
  document returns rather than overflowing the stack — which matters because a Go stack overflow is unrecoverable and
  would take down the server rather than fail one message. Weakening the entity map makes two of these fail.
- **Fleet / peers** — polling sends a bearer token and warns when one is absent; the token is documented as
  viewer-scoped. The self report is counts and health only: no credentials, no message content. TLS verification is
  per-peer opt-out for private certificate authorities.
- **Login timing** — a missing username and a wrong password differ by 1.13x, because the missing path hashes a dummy.
  Removing that makes it 27.6x. Guarded by a test.
- **Password hashing** — PBKDF2-HMAC-SHA256 at 600,000 iterations, the OWASP figure, from the standard library.

## Fixed in passing

- A channel named `con`, `prn`, `aux` or `nul` produced a filename that is a **device** on Windows, where the write
  succeeds and the bytes go nowhere. Hospitals run Windows and `aux` reads as an ordinary abbreviation.
- `scripts.allow: [database]` validated and granted nothing — `DatabaseConnectionFactory` is refused either way. Now
  refused at load, naming the alternatives.

### Cross-tenant, second pass — all sound

After finding the worst hole in the surface checked last, the remaining ones that take an identifier or a name were
attempted too. All refused correctly:

- **Reprocessing another tenant's message** — 404. Verified against a baseline: the owner's own attempt returns 409
  ("channel is not running"), so the 404 is the tenant filter refusing and not the endpoint being broken for everybody.
  That baseline exists because the user-deletion test passed for the wrong reason without one.
- **The queue view** — no other tenant's channels or destinations.
- **Metrics, metric names and the dashboard** — no other tenant's channel names.

Already covered before tonight by `messageisolation_test.go`: listing messages, fetching one by identifier, naming another
tenant's channel in a filter, and the stats endpoint.

### 8. The scrape endpoint served anybody who could reach the port — FIXED

Asked and answered: it should require a credential.

`/metrics` took no credentials at all. That is the convention and it was defensible when Perfuse ran one operator's own
channels — the numbers describe traffic they already know about. It stopped being defensible once the same process served
several customers, because a metric label names a channel and a channel name usually names the system at the other end. So
an open endpoint told one customer about another's interfaces, and told anybody who could reach the port about all of them.

Three ways in, because they serve genuinely different callers:

- **`-metrics-token`** is a dedicated scrape credential, and the only thing Prometheus can actually send — a scraper cannot
  follow a login redirect or hold a cookie. Compared in constant time, because a scrape endpoint is polled continuously,
  which is the ideal condition for measuring a comparison that stops at the first wrong byte.
- **A session or API token** works too, so an operator already signed in does not need a second credential to see numbers
  the dashboard shows them anyway. Without this, an installation setting neither flag would have an endpoint nobody could
  read, and the obvious fix for that would be to open it.
- **`-metrics-open`** restores the old behaviour, because plenty of installations really do bind that port to a private
  interface and breaking them pushes people towards not upgrading. Named rather than implied so it appears in the command
  line of any server running this way: "we thought it was firewalled" is how this becomes a finding again.

**Closing it to anonymous callers was not sufficient on its own.** The endpoint is reachable by a tenant's own operator, so
the exposition also had to stop naming other customers' channels. `WritePrometheusForTenant` filters before writing, so
another tenant's series is never written down at all — including its name, which is the disclosing part.

The scrape token and `-metrics-open` both see the whole installation. That is what an operator monitoring their own server
needs, and is why the flag help says not to hand that token to a customer: an operator who had to configure Prometheus per
tenant would notice a sick tenant last.

`WritePrometheusForTenant` refuses an empty tenant rather than treating it as "everything", because an empty tenant reaching
that function means a caller failed to resolve one, and the permissive reading of that mistake is the disclosure it exists
to prevent.

Both halves were verified by planting the hole back: restoring the unauthenticated behaviour fires three tests, and removing
the tenant filter fires the isolation test with clinic-a's channel name in the output. Also verified against a running
server for all four paths — no credential 401, right token 200 with 39 metric lines, wrong token 401, `-metrics-open` 200.

One ordering detail worth keeping: the authentication check runs **before** the "metrics are not being collected" response,
so an anonymous caller cannot even learn whether this server collects metrics.

### Superseded: the decision that was open

`/metrics` takes no credentials, by design, because a Prometheus scraper has none to give. That is defensible for a single
installation — counts of one operator's own traffic — and is a different proposition on a shared server, where a label
naming a channel discloses one customer's interfaces to anybody who can reach the port.

Not changed, because the right answer is a design decision rather than a bug: a scrape endpoint per tenant, labels that
omit channel names, or a token on that path. There is a test that logs the question so it resurfaces rather than being
lost in this document.

## Added this morning, and what each closes

**Machine credentials in the interface (tokens).** Not a hole, but it made one likely: the FHIR authentication added the night
before uses API tokens, and tokens were CLI-only. So securing FHIR had quietly made shell access a prerequisite for connecting
a lab system, and the workaround people reach for is giving a script a person's password.

**SCIM deprovisioning.** The security value is entirely in the second half. When somebody leaves, the identity provider is the
system that knows first, and if its call to disable the account fails - or succeeds without removing access - a former employee
keeps working access and nobody finds out, because nothing watches for a thing that did not happen.

So: an operation the server does not understand is a 400 rather than a silent skip, because a provider that receives 200 records
the deprovisioning as complete and never tries again. Disabling ends sessions immediately rather than at expiry. A failure is
reported as a failure even when the account was created successfully.

**Passkeys.** Unphishable sign-in, which matters for an engine holding a hospital's interfaces. The security-relevant decisions
are in the commit message and in the package documentation; the ones worth repeating here:

- The signing algorithm is fixed at registration and stored with the key. Nothing at sign-in reads an algorithm from the
  client, and there is deliberately no function that could.
- The origin is compared exactly against a list. A suffix match accepts `evil-perfuse.example.org`; a prefix match accepts
  `perfuse.example.org.attacker.net`.
- A registration challenge records the account that requested it, and the response is checked against both the challenge and
  the session. Every broken passkey implementation was broken by a credential registered in one session being attached to a
  different account.
- A failed sign-in gives one message for every cause, byte-identical, because distinguishing them turns the endpoint into an
  oracle.
- Attestation is parsed and deliberately not verified, and the code says so rather than leaving somebody to infer a guarantee
  that is not there.

**A CSRF exemption that needed a test before it was safe.** Bearer-authenticated requests no longer need the
`X-Perfuse-Request` header, because no identity provider can be configured to send one. The reasoning is sound - cross-site
forgery is an attack on ambient credentials, and a hostile page cannot attach a header it does not know - but it only holds if
cookie requests are still refused. There was no test asserting that, so the exemption could have disabled the protection
entirely and nothing would have said so. There is one now.

## Not a hole, but the most consequential finding

**Tests enforced no database constraints while production enforced them.** SQLite disables foreign keys per connection and
`Open` set the pragma only for a file database, so every `ON DELETE CASCADE` was inert under test and live in production.

Recorded here rather than only in the commit because of what it means for everything above: for however long that was true,
every test in this document that touched referential integrity was weaker than it appeared. Enabling it exposed real violations
that had been silent - the API harness had been creating users in tenants that did not exist, which production would refuse.

## Still to check

- Message content in error messages and metric labels — the invariant exists, but it has not been probed.
- Whether an editor can escalate to admin through any channel-authored path other than `file`.
- The `route` permission across tenants: can a channel destination in one tenant deliver into another's channel?
- The DICOM and MLLP listeners under malformed input, rather than only against well-formed senders.
- The scrape endpoint decision above.
- `perfuse token create` has no tenant flag, so a token made on the command line lands in the default tenant. Not a leak —
  the token carries its tenant and acts inside it, verified — but on a multi-tenant server there is no way to issue a
  token for a particular tenant from the CLI. The interface can now do it; the CLI should be able to as well.

## The pattern worth remembering

Five of the seven were **absences** rather than mistakes: no authenticator, no throttle, no confinement, no allowlist, no tenant filter.
Absences do not appear in code review, because there is nothing to look at. They appear when you try the thing that should
not work.

The other two were **disagreements between two places** that each looked correct alone: a list of destinations that
differed from the senders it described, and a set of secret field names that differed from the config it was meant to
cover. Both are now derived rather than written twice, and both have a guard — because that is the only kind of fix that
survives the next feature.


## The habit that found all of them

Every finding came from attempting the thing that should not work, against a running process. Reading the code found none.

And three times a test written immediately after a fix passed for the wrong reason — twice asserting the fix rather than
the property, once because a guard's list had a name spelled slightly differently from the method it was meant to catch.
The only reliable way to tell was to take the fix out again and watch. That step is not optional; it is the step that
tells you whether the previous hour was worth anything.
