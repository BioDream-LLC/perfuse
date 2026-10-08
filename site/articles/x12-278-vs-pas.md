# X12 278 and FHIR PAS: two ways to request prior authorization, and how to bridge them

![X12 278 and FHIR PAS connected in both directions](docs/assets/articles/hero-x12-pas.svg)

By **BioDream Developer**, BioDream LLC

Prior authorization has two electronic standards in the US. The **X12 278** has been the HIPAA standard transaction for years. **Da Vinci PAS** brings the same exchange to **FHIR**, and it is the standard behind the Prior Authorization API that CMS-0057-F requires payers to run from 1 January 2027.

Most organisations will need both for years to come. This guide explains each one and how to connect them.

## The X12 278

The **278 Health Care Services Review** is an X12 EDI transaction. A request (the 278 request) carries the patient, the requesting and servicing providers, the diagnoses and the services asked for; the response carries the payer's decision and, when approved, the certification number.

- It is built from **segments and loops** rather than resources.
- It is the language of payers' **utilization management systems** and of **clearinghouses**.
- Supporting documentation travels separately, for example as an **X12 275** attachment.

## Da Vinci PAS

**Prior Authorization Support (PAS)** expresses the same request in FHIR:

- A **Bundle** sent to **`Claim/$submit`**, carrying a Claim with use `preauthorization`, the patient, coverage, providers and services.
- **Documentation attached** in the same request, such as the QuestionnaireResponse from DTR.
- A **ClaimResponse** back: approved, denied, modified or pended, with the authorization number.
- **`Claim/$inquire`** to check status, and **subscriptions** so a pended decision is pushed back when it is made.

PAS was designed to sit inside the clinician's workflow alongside **CRD** and **DTR**, so a request can often be decided at once because the documentation is already there.

## Why both matter

- Under HIPAA, the 278 is the standard for prior authorization between covered entities. CMS has said it will use **enforcement discretion** for payers that handle prior authorization over FHIR with PAS instead.
- PAS defines its content to line up with the 278, so an **intermediary** can turn a FHIR request into a 278 for a payer system that only speaks X12, and turn the 278 response back into a ClaimResponse.
- Payers keep their existing utilization management systems while offering the FHIR API CMS requires.

## How Perfuse does it

![Perfuse between X12 278 and FHIR PAS, converting in both directions](docs/assets/articles/x12-278-vs-pas.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine that speaks both, and converts between them.

**X12, parsed as a true structure**

- **837, 835, 270/271, 276/277, 834 and 278**, with interchanges, groups, transaction sets, loops, segments and qualifiers understood as a structure.
- An X12-to-XML representation for transformation, and back again.
- **X12 275 claims attachments** in the 006020 form CMS-0053-F adopts, built and read, with C-CDAs signed to the HL7 Digital Signatures guide.

**PAS on either side**

- **Da Vinci PAS mapped to and from the X12 278**: a FHIR request reaches a payer that only speaks X12, and an X12 decision comes back as a **PAS 2.2.1 ClaimResponse that passes the official HL7 validator**.
- **Be the payer**: a PAS 2.2.1 server with `Claim/$submit`, `$inquire` and a reviewer's `$decide`, with pended decisions delivered by subscription. Inferno's **PAS Server suite: 82 of 84**.
- **Be the provider**: submit prior authorization requests to payer FHIR APIs from any channel.
- **CRD 2.2.1 and DTR 2.2.0** alongside, so the order, its documentation and the request all have an answer.
- PAS decisions become **Da Vinci PDex** prior authorizations for the Patient Access, Provider Access and Payer-to-Payer APIs.

**And the connections around it**: SFTP, HTTP, SOAP, MLLP, databases, Kafka, AMQP and AWS, with a durable queue that keeps every transaction in order and never loses one.

## Get started

```sh
perfuse serve -pas -crd-rules examples/crd/rules.yaml
```

Send a PAS bundle to `/fhir/Claim/$submit`, or route X12 278 files through a channel. The [manual](https://perfuse.health/manual/) shows both.

Related: [Da Vinci CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/) · [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/).
