# The Web Interface

Perfuse is built on the assumption that everything is doable from the browser, and a feature that can only be reached by editing a file is treated as a defect. This chapter is a tour of what is behind each part of the navigation, organised the way the interface is: by the question you are asking.

The bar carries **Dashboard** and **Channels** directly, because those are where most sessions start and end. Everything else is grouped, and the groups are named after questions rather than after subsystems — you do not go to "the audit subsystem", you go and find out who changed something.

## Reading the header

Three things in the header are easy to miss.

**Search**, or `⌘K`. It searches across channels, settings, messages and the manual, which means it is usually faster than navigating — particularly for a setting whose name you half remember.

**Theme.** Three of them: Midnight, Dark, and Light. Midnight is near-black and is what a wall display in a server room usually wants; Dark is a softer neutral grey; Light is for a laptop next to a window. The choice is stored in the browser rather than against the account, because a theme is a property of where somebody is sitting, not of who they are — the same operator wants Midnight on the wall and Light on a train. A machine's own preference is consulted once, to pick a sensible default the first time.

**Manual.** The document you are reading, served by the running server. It is the same file the binary can write to disk, so it always describes the version in front of you rather than the latest release.

## Monitor — whether it is working, and what happened

### Dashboard

The state of every channel, throughput, and anything currently wrong. This is the screen to leave open.

### Messages

Every message that arrived, what it was transformed into, per destination, and what happened to it. Searchable, including by a filter expression, so "show me the ones from this sender that were rejected" is a query rather than an afternoon.

A message can be replayed from here, and reprocessed through the *current* configuration rather than the one that was live at the time — which is how you confirm a fix works against the traffic that broke it. See [the message store](#the-message-store) for what is kept and for how long.

### Queue

What is waiting to be delivered, and why. A queue that is not draining is the single most useful early warning an integration engine produces, and it is worth pairing with an alert rule so nobody has to be watching. See [delivery](#delivery-queueing-and-retry).

### Alerts

What has fired and what the rules are. Rules are edited here rather than in a file. See [alerting](#alerting).

### Metrics

Throughput, latency and error rates over time. Useful for capacity questions and, more often, for board packs — which is why the site's own logo appears on it if one has been uploaded.

### Flow map

A picture of how messages actually move: which sources feed which channels, and which destinations they reach.

Its value is not decoration. Every estate accumulates a channel nobody remembers commissioning and a destination nobody realised was still receiving, and both are visible here in a way they are not in a list of channel files.

### Sharing what a feed actually contains

**Profile this feed** reports what is really arriving: which trigger events, which fields are populated and how often, what shapes the values take, and which codes a sender actually uses. That last one is usually the most useful single fact in it.

A profile can be downloaded and sent to somebody else. It needs a name and — required — which system produced the feed, because the question a stranger asks of a shared profile is whether it describes their system, and an optional field would make "Unknown" the commonest answer in any shared collection.

**It carries no patient data**, and that is checked rather than assumed. A profile is statistical: counts, rates, lengths, shapes. Values are kept only for fields the standard defines as code tables, because a code table value is not identifying. Identifier values are never listed.

Two things follow from that being a promise rather than a hope. The server rebuilds the profile from the stored messages when you export it, rather than sending the one on your screen — so what leaves the building was assembled by the profiler, which has a test proving this property, not by a browser. And the export refuses outright if a profile ever does list values for a field that is not a code table, naming the field but not the values.

A profile that arrives from elsewhere is checked the same way on the way in, for a different reason: it was produced by somebody whose care is unknown, and showing their patients' identifiers would be this server's disclosure. Importing one changes nothing — a profile describes a feed, and what to do about it is a separate decision.

## Build — change what a channel does, and try it before it is live

### Channels

The channel builder. Sources, destinations, filters, transformations, scripts and tests, with the generated YAML shown beside the form as you work — so the file is never a mystery, and anybody who prefers the file can read along.

See [channels](#channels) for what the parts are, and [transformation](#transformation) for what the steps do.

### Scripts

A workbench for scripts, separate from the builder because writing a script and wiring a script are different activities.

All seven slots are offered — filter, transformer, preprocessor, postprocessor, writer, lifecycle and reader — and choosing the right one matters, because the slot decides what the script receives and what it is expected to return. Checking a script as the wrong kind is not a workaround; it compiles a different thing.

JavaScript, Lua and WebAssembly are all available. See [transformation](#transformation).

### Contracts

Expectations about what a feed must look like, created and checked here as well as from `perfuse contract`. See [the command line](#the-command-line).

### Tables

Lookup tables and code sets: the mappings that turn one system's codes into another's. Editable here so that adding a code is not a deployment.

### Mapper

Proposes a channel from a single sample message, and states explicitly what the sample cannot tell it. That second half is the part that makes it trustworthy — a proposal that looked complete would invite you to believe things the message never said.

### Playground

A scratchpad for messages and transformations. Paste a message, apply steps, see the result.

Nothing is sent anywhere once the page has loaded, which is the reason it exists in this form: it is safe to paste something real into, on the understanding that it stays in the browser.

### Shadow

Runs traffic through a candidate configuration alongside the live one and reports the differences, so a change can be evidenced before it is trusted. The same idea as `perfuse compare`, applied to a change rather than to a migration. See [testing](#testing).

## Exchange — other formats and other organisations

### FHIR lab

Convert HL7 v2 to FHIR, validate a resource, and inspect the result. The validation includes US Core, which is the profile American regulation is written against, so this answers "is what they sent us actually conformant" without adopting anything.

### Documents

Clinical documents — CDA and the printable form of a document. Useful when the thing being exchanged is a discharge summary rather than a message.

### TEFCA

The audit trail for exchange through a Qualified Health Information Network, and the purpose-of-use rules that govern it.

Two transports, at genuinely different stages, and the screen says which is which. One flag covering both would be wrong in whichever direction it was rounded.

**Facilitated FHIR is implemented, and has never spoken to a real QHIN.** It follows the Sequoia Project Standard Operating Procedure effective 8 March 2026: discover the partner's UDAP metadata, verify it, register dynamically, obtain an access token carrying the purpose of use, and query. UDAP is a public key infrastructure over OAuth 2.0 — a trust community issues X.509 certificates to its members, and a member authenticates by signing JWTs with the private key and attaching the certificate chain.

The security layer is verified against somebody else's implementation, which is the only verification worth anything here. A registration signed by this build was sent to a live third-party reference server and refused with `unapproved_software_statement — Untrusted: Certificate is not a member of community`. To produce that answer the server decoded the request, parsed the software statement as a JWT, checked its signature, walked the certificate chain, and reached a decision about membership. It is the difference between being turned away at the door and being handed back an illegible letter.

What remains cannot be done from here. The certificate that proves community membership comes out of QHIN onboarding and is not something software produces. Until one exists, no exchange with a real partner completes.

**The older QHIN-to-QHIN exchange built on the IHE profiles is not implemented.** An attempt is refused with an error saying so, and the refusal is audited as a failure. That is deliberate rather than pending a tidy-up: a stubbed exchange reporting success would put entries in the audit trail for exchanges that never happened, and a trail that disagrees with what happened is worse than none, because the trail is what gets believed.

Throughout, the endpoints used for exchange come from the **signed** part of a partner's metadata, never the document body. Verifying a signature and then reading the endpoints from the unsigned copy would be an elaborate way of trusting the network: an attacker able to rewrite the response leaves the signed element alone, points the token endpoint at their own server, and collects client assertions from everybody who checked the signature and then ignored what it covered.

A trust anchor bundle is required and its absence is refused at startup. Checking a partner's signed metadata against the certificate that arrived inside it proves only that one party made both — a mistake this codebase has made before, in SAML, where the verifier preferred the embedded certificate and authenticated an attacker as an administrator while every test passed. Include the intermediates in the bundle: a UDAP server need only send its leaf certificate, and the public reference server does exactly that.

## Administer — who can do what, and how this server is set up

### Users

Accounts, roles, and what each role is allowed to do. See [security](#security).

**Machine credentials** are also here. A token is for a machine rather than a person: it does not expire and is not affected by anybody signing out, which is what a server polling its neighbour needs. Only a hash is stored, so a token is shown once when it is created and cannot be recovered — a stolen database yields no usable token. Give one the lowest role that does the job.

### Certificates

The TLS certificates in use and when they expire. The expiry dates are the point: a certificate that lapsed on a Sunday is a class of outage that is entirely preventable and routinely is not prevented.

### Activity

Who changed what, and when. Every configuration change is recorded, which is what makes "it started failing on Tuesday" answerable.

### Fleet

Every Perfuse instance on one page.

Mirth sells this as a separate product; here it is a configuration file and a read-only token. The reason it is worth having is narrow and important: **a dead server does not look like a quiet one**. A single instance's dashboard cannot tell you that a second site stopped reporting, and a fleet view can.

### Migrate

Drop in a Mirth or OIE channel export and see how many of your channels would run here unchanged. The same analysis as `perfuse explain` and `perfuse translate`, without needing a terminal. See [migration](#migration-from-mirth).

### Settings

Every setting the server has, with a control for each — including the ones that would traditionally live only in a file. Each says whether it takes effect at once or needs a restart, and whether it has been set explicitly or is sitting at its default.

**Branding** is here too: a site can set the product name, a tagline, an accent colour, and upload a logo. The logo then appears in the header, on the sign-in page, on the dashboard and on the reporting screens. If none is uploaded, nothing is substituted — an unbranded installation stays unbranded rather than acquiring decoration.

## What the colours mean

Four registers, and each says something different. The distinction is worth stating because it is the difference between a screen you can read at a glance and one where everything competes for attention.

**Plain text** is a fact about how Perfuse works. Most of the interface. Nothing is coloured to tell you it is important.

**Green** means something succeeded just now, because of something you did. It appears after an action and does not persist.

**Amber** is a caution: worth knowing before you act, and not a fault. A feature that is not switched on, a setting whose changes are audited, a conversion that approximates rather than reproduces, a transport Perfuse handles but cannot fully verify. Amber is the colour of "read this before you continue".

**Red** means something is wrong at this moment. A value that was rejected, a request that failed, a certificate that has expired, a critical alert that is firing. Nothing else uses it.

That last rule is why a screen does not open red. Red is the only colour that carries an instruction — stop, and it may be something you did — and a screen that spends it on decoration has nothing left to say when the input really is wrong. This was not always true: the users screen used to open with "the server sent a response that could not be read" on every installation that had not configured passkeys, and the settings screen marked audited values in the same colour as a rejected one. A test in `web/src/RedMeansWrong.test.ts` now fails if a phrase describing something absent or approximate is given a red style.

Inside a code box the vocabulary is different. There, red is a token type — an HL7 segment name, an XML element — and carries no judgement at all.
