# Da Vinci CRD, DTR and PAS explained: electronic prior authorization from order to decision

![CRD, DTR and PAS as three linked steps from order to decision](docs/assets/articles/hero-da-vinci.svg)

By **BioDream Developer**, BioDream LLC

Prior authorization has long meant phone calls, faxes and portals. The HL7 **Da Vinci Project** replaces that with three FHIR implementation guides that work together inside the clinician's normal workflow:

- **CRD**, Coverage Requirements Discovery: does this order need prior authorization, and what is needed?
- **DTR**, Documentation Templates and Rules: collect exactly that documentation, pre-filled from the chart.
- **PAS**, Prior Authorization Support: submit the request and receive the decision.

Together they make up the Prior Authorization API that CMS-0057-F requires payers to offer from 1 January 2027.

## CRD: the answer at the moment of ordering

CRD runs on **CDS Hooks**. When a clinician selects or signs an order, books an appointment, or starts or discharges an encounter, the EHR calls the payer's CRD service with the order and a prefetch of relevant data such as the patient and coverage.

The payer answers within seconds, while the clinician is still looking at the order:

- whether the service is covered
- whether prior authorization is needed, or already satisfied
- which documentation is required, and which DTR questionnaire collects it
- links to the coverage policy

The answer travels with the order as a **`coverage-information` extension**, including a `coverage-assertion-id` that later steps can refer back to.

## DTR: documentation, pre-filled

When documentation is needed, the EHR calls DTR's **`$questionnaire-package`** operation on the payer's FHIR server. The payer returns the Questionnaire together with the **CQL** logic that pre-fills it from the patient's record, and the value sets it uses.

The clinician reviews the pre-filled answers, completes the rest, and the result is a **QuestionnaireResponse**. **Adaptive questionnaires** go further: the payer sends one question at a time through `$next-question`, so each answer decides what is asked next.

## PAS: the request and the decision

PAS submits the request as a FHIR **Bundle** to **`Claim/$submit`**. The bundle carries a Claim with use `preauthorization`, the patient, coverage, provider and the requested services, plus supporting documentation such as the DTR QuestionnaireResponse.

The payer answers with a **ClaimResponse**: approved, denied, modified or pended. A pended request can be followed with **`Claim/$inquire`**, and payers can push the final decision through a FHIR **subscription**. Under HIPAA the prior authorization transaction is the **X12 278**, so PAS defines the mapping between the two.

## How the three fit together

![CRD, DTR and PAS between the EHR and the payer: coverage, documentation, decision](docs/assets/articles/da-vinci-flow.svg)

1. The clinician signs an order. **CRD** says prior authorization is needed and names the questionnaire.
2. **DTR** fetches the questionnaire, pre-fills it, and the clinician completes it.
3. **PAS** submits the request with the completed documentation.
4. The payer decides. Many requests can be approved in real time, because the documentation the payer needs is already there.

## How Perfuse does it

![The four CMS-0057 APIs a payer serves with Perfuse](docs/assets/articles/cms-0057-apis.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine that implements all three guides, for payers and for the systems that talk to them.

**CRD 2.2.1, from a rules file**

- A coverage requirements service over CDS Hooks at `/cds-services`, started with `perfuse serve -crd-rules rules.yaml`.
- The payer's rules are a plain YAML file: which codes, covered or not, whether prior authorization is needed, which documentation and questionnaire, the reason, policy links, billing codes, contacts and coverage details.
- Every rules file is checked when it loads, so a mistake is reported at once rather than discovered by a clinician.
- Inferno's **CRD Server 2.2.1 suite passes**.

**DTR 2.2.0 questionnaire packages**

- `$questionnaire-package` on the built-in FHIR endpoint, chosen by the order's coverage information, the questionnaire's URL, or CRD's assertion id.
- Packages include the CQL libraries the questionnaire pre-fills itself with, **and every library those depend on**, so the receiving engine has everything it needs.
- **Adaptive questionnaires** with `$next-question`.
- Inferno's **DTR Payer Server 2.2.0 suite: 43 of 45**.

**PAS 2.2.1 on either side**

- **Be the payer**: a PAS server with `Claim/$submit`, `$inquire` and a reviewer's `$decide`, answering from the same rules file as CRD. Pended decisions are delivered by subscription. Inferno's **PAS Server suite: 82 of 84**.
- **Be the provider**: submit prior authorization requests to payer FHIR APIs from any channel.
- **Bridge to X12**: PAS is mapped to and from the **X12 278**, so a FHIR request reaches a payer that only speaks X12, and an X12 decision comes back as a PAS ClaimResponse that passes the official HL7 validator.
- PAS decisions are turned into **Da Vinci PDex** prior authorizations for the Patient Access, Provider Access and Payer-to-Payer APIs.

**Security included**

- A built-in **SMART on FHIR authorization server** that passes Inferno's SMART App Launch STU2.2 suite, 80 of 80.

All of this ships inside the same single binary as the rest of the engine: HL7 v2, FHIR, X12, DICOM and CDA, twenty connector types, a durable queue and a full web console.

## Try it

```sh
perfuse serve -pas -crd-rules examples/crd/rules.yaml
```

Then point a CDS Hooks client at `/cds-services`, or send a PAS bundle to `/fhir/Claim/$submit`. The [manual](https://perfuse.health/manual/) walks through each step.

Related: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/).
