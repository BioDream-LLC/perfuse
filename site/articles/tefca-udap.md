# TEFCA and UDAP explained: national health data exchange, in plain terms

![A network of organisations connected through a QHIN hub](docs/assets/articles/hero-tefca.svg)

## In one minute

- When you are treated at a hospital that has never seen you, your records are often somewhere else entirely. Getting them used to mean setting up a one-off connection with each place that holds them.
- **TEFCA** is a national arrangement that replaces all those one-off connections with a single idea: **join once, and reach everyone.**
- The organisations plug into shared national networks called **QHINs**, which are connected to each other, so a request can travel across the country.
- **UDAP** is the technical trick that lets two organisations who have never met trust each other safely, using digital certificates vouched for by a shared authority.
- **Perfuse** is a free program that can take part: it handles the trust checks, the record exchange, and the detailed logging the rules demand.

## What it is

Here is a situation almost everyone has lived through. You are on holiday, far from home, and you end up in an emergency room. The doctors there know nothing about you — your allergies, your medications, the surgery you had two years ago. All of that is sitting in computer systems back home, and the emergency room has no easy way to reach them.

For decades, the only way to connect two healthcare organisations was to build a bridge between them by hand: a technical connection, a legal contract, and a trust arrangement, negotiated separately for every single pair. With tens of thousands of hospitals, clinics, labs and insurers in the country, that is an impossible number of bridges. Most were never built, so your records stayed stranded.

**TEFCA** is the answer to that problem. It stands for the **Trusted Exchange Framework and Common Agreement**. Break the name apart and it explains itself:

- A **Framework** — an agreed set of rules for how everyone exchanges data.
- A **Common Agreement** — a single contract that everyone signs, instead of a separate one with each partner.

The whole point is captured in one phrase: **join once, connect to all.** Sign the common agreement, plug in once, and you can exchange records with any other member across the country.

![Before TEFCA, a tangle of separate connections between organisations; after TEFCA, each connects once to a shared hub](docs/assets/articles/tefca-udap-before-after.svg)

## Why it exists

TEFCA was called for by a US law, the **21st Century Cures Act**, which pushed the country toward making health records flow freely and safely. Congress recognised that the hand-built-bridge approach would never scale, and that a national framework was needed.

It is overseen by **ASTP/ONC** — the Assistant Secretary for Technology Policy / Office of the National Coordinator for Health Information Technology, the federal office responsible for health-IT policy. The day-to-day coordination is run by a non-profit called **The Sequoia Project**, which holds the official title of **Recognized Coordinating Entity**, or RCE — essentially the referee that keeps the whole arrangement running fairly.

> In plain words: TEFCA turns "a thousand separate handshakes" into "one membership card". You agree to the shared rules once, and that membership lets you exchange records with everyone else who agreed to them.

## Who it affects

- **Patients**, who benefit when the hospital treating them can actually see their history — and who can request their own records through it.
- **Hospitals and clinics**, which can retrieve a visiting patient's records instead of starting blind.
- **Insurers (payers)**, which can get the information they need for coverage and payment.
- **Public health agencies**, which can receive the data they need to track disease.

## How it is organised: QHINs, Participants, Subparticipants

TEFCA has three layers. The analogy of a telephone network helps: there are the big national carriers, the businesses that buy service from a carrier, and the people in those businesses.

- **QHINs** — **Qualified Health Information Networks** — are the big national networks. They connect *to each other*, forming the backbone. There are a handful of them. Think of them as the national carriers.
- **Participants** are organisations that join one QHIN: a health system, a regional health information exchange, a payer, a technology vendor, a public health agency.
- **Subparticipants** join *through* a Participant — for example, a small clinic that connects via the larger health system it belongs to.

Because every QHIN connects to every other, and everyone signs the same Common Agreement, a request made at one corner of the country can reach a record held in another. The shared hub does the reaching for you.

## Why organisations are allowed to ask for records

Opening the floodgates would be dangerous, so TEFCA is strict about *why* a record may be requested. These approved reasons are called **exchange purposes**, and a request must state one:

- **Treatment** — caring for a patient.
- **Payment** — handling the bill or the claim.
- **Health care operations** — the running of a healthcare organisation, such as quality review.
- **Public health** — tracking and responding to disease.
- **Government benefits determination** — deciding eligibility for a government programme.
- **Individual access services** — a patient asking for their *own* records.

Stating a purpose is not a formality. It governs what may be requested and shared, and it is recorded, so misuse can be detected after the fact.

## Where FHIR fits: Facilitated FHIR

Originally, TEFCA exchange was built around sending whole **documents** — think of a packaged summary of a visit, sent as a file. That works, but modern apps prefer to ask precise questions ("what are this patient's current medications?") and get precise answers. That style is **FHIR**, the modern health-data format (**Fast Healthcare Interoperability Resources**, pronounced "fire").

The Common Agreement brought FHIR into TEFCA under the name **Facilitated FHIR**. The word "facilitated" is the key: the QHINs act as matchmakers. They help two participants *find* each other and *establish trust*. Once that introduction is made, the actual FHIR questions and answers flow **directly** between the two participants, rather than through the hub. The hub introduces; the participants then talk.

That leaves one hard problem. Normally, a FHIR system will only answer an app it already knows and has registered in advance. But across a national network, you cannot register every possible app at every possible server ahead of time — there are far too many, and you have never met most of them. How does a server safely answer a request from an app it has never seen? That is exactly what UDAP solves.

## UDAP: trusting a stranger

**UDAP** stands for **Unified Data Access Profiles**. For health data it is spelled out in an HL7 security guide known as **FAST Security** (FAST is the FHIR At Scale Taskforce — a group working on making FHIR work across large networks). You do not need the acronyms; you need the idea.

![An app arrives with a certificate from a trusted community; the server checks it chains back to the shared authority, and publishes its own signed details in return](docs/assets/articles/tefca-udap-trust.svg)

Think about how you trust a stranger's passport at a border. You have never met this person, and you cannot phone their government. Yet you accept their passport because it was issued by an authority you both recognise, and it has features that are hard to forge. You trust the *issuer*, so you can trust the *document*.

UDAP works the same way, using **digital certificates**. A certificate is an electronic document, very hard to forge, that says "this app is who it claims to be" and is signed by a recognised authority. The pieces:

- **A trust community.** This is the shared "passport office" that both sides recognise. Everyone in the community agrees to trust certificates it vouches for.
- **Dynamic client registration.** Instead of being set up by hand in advance, an app can **register itself** on the spot, presenting its certificate as proof of who it is. "Dynamic" simply means "on the fly, without a human arranging it beforehand".
- **Signed metadata.** Each server publishes a signed description of itself — its own tamper-proof details — so an app can confirm it is really talking to the genuine server and not an impostor. The trust runs in both directions.

Put together: an app from a clinic in one state arrives at a hospital's server in another. The app shows a certificate. The server checks that the certificate traces back to the trust community they both belong to — the shared passport office. If it does, the server trusts the app, and the app can check the server's signed details in return. **Neither side had to arrange anything in advance.** The shared authority vouched for both.

![Your organisation and another exchanging records across two QHINs, with UDAP and auditing](docs/assets/articles/tefca-udap.svg)

> In plain words: UDAP lets two organisations that have never met trust each other on the spot, because they both trust the same certificate authority — exactly as a border guard trusts a passport without knowing the traveller.

## A worked example

Imagine a patient, Jordan, collapses while visiting another state and is taken to a hospital that has never treated them. A clinician needs Jordan's history fast.

1. The hospital's system, through its QHIN, asks the national network: *who holds records for this patient?*
2. A clinic back in Jordan's home state is found. The two organisations have never had any direct relationship.
3. The hospital's app presents its certificate. The clinic's server checks that the certificate chains back to the trust community they both belong to — it does.
4. Trust established, the hospital's app sends a FHIR request stating the **exchange purpose: treatment**, and asks for Jordan's medications and allergies.
5. The clinic's server answers directly with the data. **Every step is written to an audit log** at both ends — who asked, for what purpose, and when.

No bridge was built in advance. The whole thing worked because both organisations had already joined the same framework.

## Common questions

**Is my data just flowing around freely now?** No. A request must state an approved purpose, both sides must prove who they are, and every exchange is logged. The framework is built around limits, not the absence of them.

**Who can see my records?** Only organisations with a legitimate approved purpose, who have established trust through the framework. And you can request your own records through the "individual access" purpose.

**Does TEFCA replace FHIR or SMART on FHIR?** No — it uses them. FHIR is the data format; UDAP handles trust between organisations; SMART on FHIR handles an individual app getting a person's permission. They are layers that fit together.

**How do I know an organisation really is who it claims?** That is precisely what UDAP's certificates and signed metadata are for: a shared, hard-to-forge proof of identity, vouched for by a trusted authority.

## Glossary

- **TEFCA** — Trusted Exchange Framework and Common Agreement: the national "join once, connect to all" arrangement.
- **Common Agreement** — the single contract everyone signs instead of pairwise contracts.
- **QHIN** — Qualified Health Information Network: a national network that connects to the other QHINs.
- **Participant** — an organisation that joins a QHIN.
- **Subparticipant** — an organisation that joins through a Participant.
- **ASTP/ONC** — the US federal office responsible for health-IT policy.
- **The Sequoia Project / RCE** — the non-profit that coordinates TEFCA; RCE means Recognized Coordinating Entity.
- **21st Century Cures Act** — the US law that called for TEFCA.
- **Exchange purpose** — the approved reason a record may be requested (treatment, payment, and so on).
- **FHIR** — Fast Healthcare Interoperability Resources, the modern health-data format. Pronounced "fire".
- **Facilitated FHIR** — TEFCA's FHIR-based exchange, where QHINs help participants find and trust each other.
- **UDAP** — Unified Data Access Profiles: how two organisations trust each other using certificates.
- **FAST Security** — the HL7 security guide that profiles UDAP for FHIR; FAST is the FHIR At Scale Taskforce.
- **Certificate** — a hard-to-forge electronic document proving identity, signed by a trusted authority.
- **Trust community** — the shared authority both sides recognise; the "passport office".
- **Dynamic client registration** — an app registering itself on the spot, without a human setting it up beforehand.
- **Signed metadata** — a server's own tamper-proof description, so apps can confirm it is genuine.
- **Audit** — the record of who exchanged what, when and why.

## How Perfuse does it

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine that can take part in TEFCA exchange, handling the trust checks and the logging the rules require.

- **UDAP**: dynamic client registration, discovery, and **signed-metadata verification** — the stranger-trusting machinery above — checked against a real authorization server and a real trust community, not just against Perfuse's own assumptions.
- **Facilitated FHIR exchange** between TEFCA participants, following the official procedure.
- **Partner discovery** with full certificate-chain verification, so trust is traced all the way back to the shared authority.
- **Every exchange audited**, with the audit written to disk rather than kept in fleeting memory — because TEFCA does not treat logging as optional, and neither does Perfuse.
- **The data to exchange**: Perfuse converts HL7 v2 feeds into FHIR and US Core (the US national rulebook for FHIR), serves it from its built-in FHIR endpoint, and checks it with the official HL7 validator.
- **Security around it all**: staff sign-on through SAML, OpenID Connect, LDAP, passkeys, SCIM and mutual TLS, plus a built-in SMART on FHIR server for app-level access.

Notably, during real testing a genuine authorization server refused Perfuse's client **on trust-community membership rather than on the shape of the request** — which is the right kind of refusal. A server that only rejected badly formed requests would prove nothing about whether the certificate was genuinely trusted; refusing on membership is proof the trust check is really being done.

All of this lives in the same single file as the rest of the engine, with the web console built in — so the system that gathers your records is also the one that exchanges them nationally.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows.
2. Run `perfuse serve` and open http://127.0.0.1:8080 in your browser.
3. Read the [manual](https://perfuse.health/manual/) for TEFCA and UDAP setup, explained step by step.

Related: [SMART on FHIR explained](https://perfuse.health/smart-on-fhir/) · [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
