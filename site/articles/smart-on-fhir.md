# SMART on FHIR explained: how apps get secure access to health data

![A key labelled SMART](docs/assets/articles/hero-smart.svg)

By **BioDream Developer**, BioDream LLC

**SMART on FHIR** is the standard way for an app to get permission to read, and sometimes write, health data through a FHIR API. Patient apps, clinician apps that launch inside the EHR, and back-end systems all use it. It is required by ONC's certification criteria for patient and population access, and by CMS's interoperability rules for payers.

This guide explains how SMART works and what a good implementation looks like.

## Built on OAuth 2.0 and OpenID Connect

SMART App Launch builds on **OAuth 2.0**, the same standard behind "Sign in with..." buttons across the web, and **OpenID Connect** for identity. A FHIR server advertises its authorization endpoints at `.well-known/smart-configuration`, and apps discover them there.

## Three ways to launch

- **Standalone launch**: a patient opens an app on their phone, picks their provider or plan, signs in and grants access.
- **EHR launch**: a clinician opens an app from inside the EHR, and the app receives the current patient and encounter as context.
- **Backend Services**: a system with no user, such as a payer's bulk export job, authenticates with a signed JSON Web Token instead of a sign-in.

## The flow

1. The app sends the user to the **authorization server**, with **PKCE** so an intercepted code is useless to anyone else.
2. The user signs in and **consents** to the **scopes** the app asked for.
3. The app exchanges the code for an **access token**, and usually a **refresh token** and an **ID token**.
4. The app calls the FHIR API with the access token. The server checks the scopes on **every request**.

## Scopes

Scopes say exactly what an app may do. `patient/Observation.rs` lets an app read and search one patient's observations; `user/*.rs` covers what the signed-in user can see. SMART v2 adds **granular scopes**, such as only laboratory observations: `patient/Observation.rs?category=laboratory`.

## How Perfuse does it

![An app, the authorization server and the FHIR API, with PKCE, consent, tokens and scopes](docs/assets/articles/smart-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with a **built-in SMART on FHIR authorization server** in front of its FHIR endpoint.

- **Standalone and EHR launch** with PKCE, consent, refresh tokens and ID tokens.
- **Backend Services** with signed client assertions, for bulk export and system-to-system access.
- **Granular scopes enforced** on every request.
- **Token introspection and revocation.**
- **Passes Inferno's SMART App Launch STU2.2 suite: 80 of 80.**
- Clients and users are plain YAML files, with secrets stored as hashes: `perfuse smart hash` creates them. Examples are in `examples/smart/`.
- Already have an identity provider? Perfuse can instead accept tokens from your existing authorization server, by issuer and key set.

Around it, Perfuse brings the rest of what a secure FHIR service needs: **SAML, OpenID Connect, LDAP, passkeys, SCIM and mutual TLS** for staff sign-on, role-based access, an audit log of who changed what, and PHI access monitoring that spots unusual volume, after-hours and bulk access.

And behind the API sits a complete integration engine, converting HL7 v2, X12 and CDA into FHIR and US Core, so the data an app reads is current.

## Get started

```sh
perfuse serve -smart-clients examples/smart/clients.yaml -smart-users examples/smart/users.yaml
```

Then point a SMART app, or Inferno's SMART test kit, at the FHIR endpoint. The [manual](https://perfuse.health/manual/) covers every option.

Related: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [TEFCA and UDAP explained](https://perfuse.health/tefca-udap/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
