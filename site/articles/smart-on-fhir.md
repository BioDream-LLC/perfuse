# SMART on FHIR explained: how apps get secure access to health data

![A key labelled SMART](docs/assets/articles/hero-smart.svg)

## In one minute

- **SMART on FHIR** is the standard way an app gets permission to read, and sometimes write, your health data — safely, and only the parts it is allowed.
- It is the same idea as the "Sign in with Google" button you have used on other websites, adapted for medical records.
- It covers three situations: a patient opening an app at home, a clinician opening an app inside the hospital's system, and a computer program running on its own with no person present.
- The key safety idea is the **scope**: a precise statement of exactly what an app may touch, checked on every single request — like a key card that opens only certain doors.
- US rules require it: ONC for hospital data, CMS for insurers. **Perfuse** includes a complete, tested SMART server and gets a perfect score on the official test suite.

## What it is

Imagine you install a new health app on your phone. It wants to show you your lab results, pulled from your hospital. For that to happen, the app needs to reach into the hospital's records — but obviously it must not be able to grab everyone's records, or more of yours than you agreed to, and the hospital must be sure the app is really acting for you.

**SMART on FHIR** is the agreed set of rules that makes this safe. The name breaks down like this:

- **SMART** stands for **Substitutable Medical Applications, Reusable Technologies**. "Substitutable" captures the goal: any approved app should be able to plug into any hospital that follows the standard, the way any lamp fits any wall socket.
- **FHIR** (pronounced "fire") is the modern format health data is shared in — **Fast Healthcare Interoperability Resources**. "Interoperability" just means different systems working together.

So "SMART on FHIR" means: *the standard way an app asks for, and is granted, controlled access to health data in the FHIR format.*

You have met the pattern before. When a website offers "Sign in with Google" and then says "This app would like to see your name and email address — Allow?", you are using the very same machinery underneath. SMART on FHIR is that machinery, tuned for the sensitivity of medical records.

## Why it exists

Before standards like this, every app that wanted hospital data had to be wired in by hand, one hospital at a time, with custom arrangements each time. That was slow, expensive, and inconsistent — and inconsistency around medical data is dangerous.

SMART on FHIR fixes that by making the handshake the same everywhere. An app written once can work with any hospital or insurer that follows the standard. And because the rules are public and well tested, the security is predictable rather than improvised.

It is also now the law of the land in US healthcare. ONC — the **Office of the National Coordinator for Health Information Technology**, the federal office that sets health-IT rules — requires certified systems to support it so patients and researchers can get data through apps. CMS — the **Centers for Medicare & Medicaid Services**, which runs the big government health programmes — requires insurers to expose data through FHIR APIs that use it.

> In plain words: SMART on FHIR is the universal, standardised "Allow this app to access my health data?" screen for the whole industry, backed by rules strong enough for medical records.

## Who it affects

- **Patients**, who can choose apps to view and manage their own records.
- **Clinicians**, who get specialist tools that open right inside the electronic health record, already showing the patient in front of them.
- **App developers**, who can build once and connect to many systems.
- **Hospitals and insurers**, who must provide this access to meet the rules — and must do it securely.

## The building blocks: OAuth and OpenID Connect

SMART does not invent its security from scratch. It builds on two well-established internet standards, which is a strength: they have been attacked and hardened for years across the whole web.

- **OAuth 2.0** is the standard behind every "Allow this app to access your account?" screen. It is about **permission** — granting an app limited access without handing over your password.
- **OpenID Connect** sits on top of OAuth and handles **identity** — confirming *who you are* when you sign in.

A FHIR server publishes where its sign-in machinery lives at a fixed web address ending in `.well-known/smart-configuration`. "Well-known" is literal: it is a standard spot every app knows to look, like knowing a shop's opening hours are always posted on the front door. An app reads that address to learn where to send the user to sign in.

## Three ways an app starts

SMART supports three "launch" situations, because the three are genuinely different.

![The three ways a SMART app starts: standalone launch by a patient, EHR launch by a clinician, and backend services with no person](docs/assets/articles/smart-on-fhir-launches.svg)

- **Standalone launch.** A patient opens an app on their own phone. The app asks "which hospital or health plan are you with?", sends them there to sign in, and the patient approves the access. *Who signs in: the patient. Example: a diabetes-tracking app.*
- **EHR launch.** A clinician is looking at a patient inside the EHR — the **electronic health record**, the hospital's main computer system — and clicks a button to open a specialist app. Because it was launched from inside the record, the app already knows which patient is open, so the clinician doesn't have to search again. *Who signs in: the clinician. Example: a risk calculator that opens on the current patient.*
- **Backend Services.** Sometimes there is no person at all — just a program doing a scheduled job, like an insurer's overnight export of claims data. There is no one to type a password, so instead the program proves who it is with a **signed digital token** (think of it as a tamper-proof letter of introduction that only the real program could have written). *Who signs in: nobody. Example: a nightly bulk export.*

## The flow, step by step

Here is what actually happens when a patient connects an app, told as a story. Suppose Alex installs an app to see their lab results.

![An app, the authorization server and the FHIR API, with PKCE, consent, tokens and scopes](docs/assets/articles/smart-flow.svg)

1. **The app sends Alex to sign in.** It does not collect Alex's hospital password itself — that would be dangerous. Instead it hands Alex over to the hospital's own **authorization server**, the trusted gatekeeper that handles sign-ins. On the way, the app uses a safety technique called **PKCE** (pronounced "pixy", short for Proof Key for Code Exchange). PKCE is like putting a unique wax seal on the request so that even if a thief intercepts the reply, it is useless to them because they don't have the matching seal.
2. **Alex signs in and consents.** The hospital's gatekeeper, not the app, checks Alex's password. Then it shows a consent screen: "This app wants to read your lab results. Allow?" The specific things it is asking for are called **scopes** (more on these next). Alex approves.
3. **The app receives tokens.** The gatekeeper hands the app an **access token** — a short-lived digital pass that proves Alex granted this access. Usually it also gets a **refresh token** (used to get a fresh pass when the old one expires, so Alex doesn't have to sign in constantly) and an **ID token** (which confirms who Alex is).
4. **The app reads the data.** It calls the hospital's FHIR API, showing its access token with every request. The server checks the token's scopes on **every single call** — not just at the start. If the app asks for something outside what Alex allowed, it is refused.

> In plain words: the app never sees your password. A separate trusted gatekeeper checks it, asks your permission, and hands the app a limited pass. The server rechecks that pass on every request, so the app can never quietly reach beyond what you agreed to.

## Scopes: the heart of it

A **scope** is a short, precise statement of what an app may do. This is where SMART earns its trust, so it is worth understanding by analogy.

![Scopes work like a hotel key card: a broad key opens many doors, a narrow key opens only one](docs/assets/articles/smart-on-fhir-scopes.svg)

Think of a scope as a hotel key card. A guest's card opens only their own room. A cleaner's card opens the rooms on one floor. A manager's card opens everything. The card is checked at every door, not just at the entrance.

Scopes are written in a short code. You do not need to write them, but reading one is easy once you know the shape:

- `patient/Observation.rs` — read one patient's observations (test results and vital signs). The `patient/` part says *whose* data; `Observation` says *what kind*; `.rs` means *read and search*.
- `user/*.rs` — read everything the signed-in user is allowed to see. The `*` means "all kinds". This is the broad "staff key".
- `patient/Observation.rs?category=laboratory` — read **only** the lab results, not the vital signs. This narrowing is a newer feature called a **granular scope**, and it matters: it lets an app ask for the smallest key that does its job, rather than demanding the whole keyring.

The principle behind all of this is sometimes called **least privilege**: give an app exactly as much access as it needs and not one bit more. Granular scopes make least privilege practical.

## A note on who signs in versus whose data

A small but important distinction. `patient/` scopes are about one patient's data — the patient who signed in or whose chart was open. `user/` scopes are about everything the *signed-in person* is allowed to see, which for a clinician might be many patients. The gatekeeper and the server work together to keep these honest, so a patient's app can never quietly turn into a staff key.

## Common questions

**Does the app ever see my password?** No. You type your password only into the hospital's own trusted gatekeeper, never into the app. The app receives a limited pass instead.

**What stops an app from grabbing more than I allowed?** The scopes, checked on every request. If the pass does not cover what the app asked for, the server refuses — every time, not just at sign-in.

**What if my pass is stolen?** Access tokens are deliberately short-lived, so a stolen one expires quickly. The PKCE "wax seal" also makes an intercepted sign-in reply useless to a thief.

**Is this different from the sign-in I use at work?** Staff sign-on (so employees can log into hospital systems) is a related but separate job, often done with standards like SAML or OpenID Connect. SMART on FHIR is specifically about letting *apps* reach *health data* on a person's behalf.

**How do we know an implementation actually follows the rules?** There is an official, free test suite called **Inferno**, run by ONC, that exercises a server against the standard. A good server publishes its Inferno results.

## Glossary

- **SMART on FHIR** — the standard way an app gets controlled access to health data in the FHIR format.
- **FHIR** — Fast Healthcare Interoperability Resources, the modern health-data format. Pronounced "fire".
- **OAuth 2.0** — the internet standard for granting an app limited access without sharing your password.
- **OpenID Connect** — the companion standard that confirms who you are.
- **Authorization server** — the trusted gatekeeper that checks your sign-in and issues passes.
- **Access token** — the short-lived digital pass that proves access was granted.
- **Refresh token** — a token used to get a fresh pass without signing in again.
- **ID token** — a token confirming the user's identity.
- **Scope** — a precise statement of what an app may do, checked on every request.
- **Granular scope** — a narrower scope, e.g. "lab results only".
- **PKCE** — Proof Key for Code Exchange, a technique that makes an intercepted sign-in reply useless to a thief. Pronounced "pixy".
- **EHR** — electronic health record, the hospital's main patient-record system.
- **Backend Services** — SMART's way for a program with no human to get access, using a signed token.
- **ONC** — the US Office of the National Coordinator for Health Information Technology, which sets health-IT rules.
- **CMS** — the US Centers for Medicare & Medicaid Services, which requires insurers to expose FHIR APIs.
- **Inferno** — ONC's free test suite that checks a server against the SMART standard.
- **Least privilege** — the principle of granting only as much access as is needed.

## How Perfuse does it

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with a complete **SMART on FHIR authorization server** built in, standing guard in front of its FHIR data.

- **Standalone and EHR launch**, with PKCE, consent, refresh tokens and ID tokens — the whole patient and clinician flow described above.
- **Backend Services** with signed tokens, for programs with no human, such as bulk exports.
- **Granular scopes enforced on every request**, so an app that asked for lab results only cannot reach anything else.
- **Token introspection and revocation** — the ability to check whether a pass is still valid, and to cancel one.
- **A perfect score on the official test:** it passes Inferno's SMART App Launch STU2.2 suite, **80 of 80**, which is the independent proof that it follows the standard correctly.
- **Simple setup:** the apps and users it recognises are plain configuration files, with secrets stored only as scrambled hashes rather than in the clear. Ready-made examples are included.
- **Works with what you already have:** if your organisation already runs its own gatekeeper, Perfuse can accept passes issued by it instead of running its own.

Around the FHIR access, Perfuse brings the rest of what a secure health service needs: staff sign-on through SAML, OpenID Connect, LDAP, passkeys, SCIM and mutual TLS; roles that limit who can change what; an audit log recording who did what; and monitoring that flags unusual access, such as someone reading an unusual volume of records or working at odd hours.

And behind the API sits a full integration engine, turning HL7 v2, X12 and CDA into FHIR, so the data an app reads is current and complete.

## Get started

```sh
perfuse serve -smart-clients examples/smart/clients.yaml -smart-users examples/smart/users.yaml
```

Then point a SMART app — or ONC's free Inferno test suite — at the FHIR endpoint and watch the flow run. The [manual](https://perfuse.health/manual/) covers every option in plain terms.

Related: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [TEFCA and UDAP explained](https://perfuse.health/tefca-udap/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
