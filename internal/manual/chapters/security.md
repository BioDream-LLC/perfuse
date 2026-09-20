# Security

Perfuse handles clinical data. This chapter is what it does about that and what it leaves to you.

## Transport security

TLS on the web interface is configured with `-tls-cert` and `-tls-key`. Without them the interface is plain HTTP, which is acceptable on a loopback address for a first look and nowhere else — it carries credentials and message content, both readable on the wire.

TLS on channel transports is per source and per destination, under a `tls` block. The available settings are in the [source](#source-reference) and [destination](#destination-reference) references.

`insecure_skip_verify` accepts any certificate. It exists because hospital systems routinely present certificates that will not verify, and refusing to connect would mean refusing to integrate. It is named to be conspicuous, and using it means the connection is encrypted but not authenticated — you have protection against passive interception and none against an interposed server.

> An unverified TLS connection is not the same as a verified one, and the distinction is worth recording somewhere your auditors will find it. The better fix is almost always to obtain the far end's certificate and trust it explicitly rather than trusting everything.

## Authentication and roles

Users are held in the database, with three roles:

| Role | Can |
|---|---|
| Viewer | See channels, messages, statistics and traces. |
| Editor | Also create and change channels, and run comparisons. |
| Admin | Also manage users, settings and tenants. |

The split that matters is Viewer and Editor. A great deal of useful work — investigating why a message failed, tracing it, searching the store — needs no ability to change anything, and giving somebody Editor so they can look at a message is how configuration gets changed by accident.

Note that Viewer can read message content, which is clinical data. It is not a low-privilege role in any sense that matters for privacy; it is a low-privilege role for *configuration*.

## Audit

Configuration changes, logins, and access to message content are recorded with who, what and when.

The audit log is in the same database as the messages. That is convenient and it is a limitation: somebody who can modify the database can modify the audit log. If your requirements include tamper-evident audit, the log needs shipping somewhere append-only, and Perfuse's own log output is the way to do that.

## Secrets in configuration

Passwords, passcodes and keys appear in channel configuration, which means they are in the YAML files.

Two consequences. The files need filesystem permissions that reflect what they contain, and if they are in version control — which is otherwise recommended — the repository holds credentials and must be treated accordingly.

Where a value is a secret, the interface renders it as a secret and the API does not return it once saved. That protects it from being read over somebody's shoulder or leaked in a screenshot. It does not protect the file.

## What is in the database

The message store holds messages as received, which for a clinical feed means patient-identifiable data. It should be on encrypted storage, backed up as clinical data with the retention that implies, and access-controlled at the filesystem level as well as through Perfuse's own roles.

`cleanup.periodDays` limits how long it is kept. See [the message store](#the-message-store) for the tension between that and having enough history for rhythm-aware alerting.

## Tenants

A tenant is an isolation boundary: channels, messages, users and settings belong to one, and nothing crosses.

This exists for a service provider running feeds for several practices. Do not use it to separate departments within one organisation that need to see each other's traffic — the isolation is real and there is no cross-tenant view.

## Test data

The generated messages described in [testing](#testing) exist partly as a privacy control. Reproducing a problem elsewhere, sending an example to a vendor, or populating a test environment are all things that otherwise get done with real messages.

The generator's output contains no patient data by construction rather than by redaction. Prefer it to hand-stripped real messages, which is a process that can be done incompletely and usually is.

## Single sign-on

Three mechanisms, and local accounts alongside all of them.

**OpenID Connect** (`-oidc`), **a directory over LDAP** (`-ldap`), and **SAML 2.0** (`-saml`). Each is configured from **Administer → Sign-on** and written to the file the flag names, so the screen and the file are two views of one thing rather than two places to keep in step.

Local accounts are always available and cannot be turned off. That is deliberate: an on-premises integration engine whose only way in is an identity provider becomes unreachable exactly when the identity provider is unreachable, and a clinical interface is not a good place to learn that. The sign-on screen tells you how many local administrators exist before it encourages you to depend on anything else.

### What a group mapping decides

All three map groups from the directory to Perfuse roles, and somebody in groups matching more than one gets the most privileged. That is the only safe direction to resolve it — the alternative is a person losing access they are entitled to because they are also in a lesser group.

A mapping that grants nobody anything is refused rather than saved. A configuration that authenticates people and turns all of them away looks identical to a broken product from the outside: the round trip completes and everybody is denied with nothing explaining why.

Creating an account on first sign-in is off by default. With it off, somebody has to exist in Perfuse before they can sign in, so the directory decides who they are and Perfuse decides who is allowed in.

### SAML, specifically

The parts worth knowing before you configure it:

- **Only the certificate you configure is trusted.** A certificate carried inside a response is never used. An attacker who can send a document can also put their own certificate in it, and a verifier that reads it is one anybody can authenticate to as anybody.
- **A response has to answer a sign-in this server started.** Perfuse remembers the request it sent and refuses a response naming anything else, or naming a request it has already answered. Without that check a valid response is a bearer token for whoever holds it: somebody signs in as themselves, keeps the response, and posts it into your browser, and you are then inside their account with the audit log recording their name.
- **Sign-ins started at the identity provider are therefore off by default.** Turn on *Accept sign-ins started at the identity provider* only if you need a portal tile, and knowing that it is what makes the above possible.
- **The group attribute has no default and is required.** Entra sends `groups`, Okta sends whatever the application was configured with, ADFS sends a claim URI. A guess would produce a sign-in that works and grants nobody anything. If a sign-in is refused for having no role, the server log names the attributes that did arrive — which is usually the whole answer.
- **The reply URL must match exactly** what is registered at the provider. A response says where it was destined and one addressed elsewhere is refused, because accepting it would mean accepting a response meant for a different service.

### What each provider sends

The group attribute is where sign-ins go wrong, and the three common providers each name it differently.

| Provider | Usually sends | Notes |
| --- | --- | --- |
| Keycloak | `groups` | Needs a group membership mapper added to the client. |
| Entra ID | a claim URI ending `/claims/groups` | Sends group object ids unless the application is configured to send names. Map the ids, or change the provider. |
| Okta | whatever the application was configured with | Commonly `groups`, with a filter deciding which are sent. |
| ADFS | a claim URI | Signs the Response rather than the Assertion, which Perfuse accepts. |

A claim URI may be entered by its final segment: typing `groups` finds `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups`. A name that merely ends with the word does not match — `excluded_groups` is not `groups`.

Group membership arriving as one comma-separated value is treated as a single group name and matches nothing. That is the correct reading of the standard, and splitting on commas would mean a group legitimately containing one silently became two. If that is what your provider sends, change it there.

**Test** on the sign-on screen checks everything that can be checked without a person: the settings, the certificate, and whether a sign-in request can be built. It says plainly that it cannot prove somebody can sign in, because that needs a real assertion about a real person and the only way to get one is for them to try.

## What Perfuse does not do

It does not encrypt the database at rest. Use filesystem or volume encryption.

It does not manage its own certificates or renew them. Point it at files and manage those files with whatever you already use.


SAML has been verified against two independent providers, each with a real browser sign-in, a real assertion and a real session: Keycloak 26 and Microsoft Entra. Two matters more than twice as much as one, because the two disagree in a way that turned out to be load-bearing — Keycloak writes its assertion with namespace prefixes and Entra writes the same elements without them, and the one defect this area has had was in prefix handling.

**Start by reading your provider's metadata.** Settings, then Sign-on, then the SAML tab: give the metadata URL or paste the document, and the sign-on address and signing certificate are filled in for you. Every SAML provider publishes one of these — Okta, Entra, AWS IAM Identity Center, Keycloak, ADFS — and it is the difference between configuring a provider and transcribing one. Doing it by hand means finding the certificate inside the XML, stripping the line breaks out of the base64 and wrapping it in PEM headers, where a single stray space produces a signature error that says nothing about formatting.

What comes back is described rather than dumped: each certificate's subject, expiry and SHA-256 fingerprint, so you can check it against what your provider's own console shows. An expired certificate is called expired rather than left as a date to compare by eye. Nothing is saved by reading — applying it to the form is a separate click, because a document somebody pasted is not yet a decision about what to trust.

A metadata URL is fetched over https only, and the address is checked against the same policy that governs channel destinations, so this cannot be used to make the server read cloud instance credentials. Redirects are not followed, because a permitted address redirecting to a blocked one would walk straight past that check.

**If you are configuring Entra, five things will not match what the examples show you.** It sends no email claim at all. Its NameID is an opaque identifier rather than an address. It sends no groups claim unless the application is explicitly configured to emit one, so a role mapping written against groups refuses every sign-in. When groups are emitted they are object GUIDs rather than names, unless your tenant synchronises from on-premises Active Directory. And its claim names live under `schemas.microsoft.com/identity` where most documentation shows `schemas.xmlsoap.org`. Perfuse handles all five; they are listed because the first four will otherwise look like faults in Perfuse when they are Entra being Entra.

That distinction is not theoretical here. The first genuine Keycloak assertion failed outright, because the canonical form the signature covers was being computed with namespace prefixes dropped. No real identity provider could have signed anybody in, and the package's own five canonicalisation tests passed throughout — each of them signed and verified with the same code, so they proved only that it agreed with itself. If you are the first to point a different provider at this, expect to find something, and the server log will name the stage that failed.

It has never been penetration tested by anybody other than its author, and it has no production hours. The [testing](#testing) chapter is explicit about what that means; this is the security-shaped version of the same admission.
