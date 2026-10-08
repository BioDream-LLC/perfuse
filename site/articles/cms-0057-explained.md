# CMS-0057-F explained: the Interoperability and Prior Authorization rule, in plain terms

![CMS-0057-F, the Interoperability and Prior Authorization rule](docs/assets/articles/hero-cms-0057.svg)

CMS-0057-F, the **Interoperability and Prior Authorization Final Rule**, is the biggest change to how US payers exchange data since the original Patient Access rule. It sets firm deadlines for faster prior authorization decisions and for four FHIR APIs that payers must run.

This guide explains who it applies to, what it requires, when, and how to meet it without building everything from scratch.

## Who it applies to

The rule covers **"impacted payers"**:

- Medicare Advantage organizations
- State Medicaid and CHIP fee-for-service programs
- Medicaid managed care plans and CHIP managed care entities
- Qualified Health Plan issuers on the Federally-Facilitated Exchanges

Providers are affected too. From the 2027 performance period, the **"Electronic Prior Authorization"** measure is part of the Merit-based Incentive Payment System (MIPS) Promoting Interoperability category and the Medicare Promoting Interoperability Program for hospitals.

## What it requires, and when

![CMS-0057-F timeline: decision timeframes and metrics from 2026, four FHIR APIs from 2027](docs/assets/articles/cms-0057-timeline.svg)

### From 1 January 2026: faster, clearer decisions

- **Decision timeframes**: 72 hours for expedited (urgent) requests and 7 calendar days for standard requests.
- **A specific reason for every denial**, whatever channel the request arrived on.
- **Public prior authorization metrics**, posted every year on the payer's website: approvals, denials, approvals after appeal, extensions, and average and median decision times.

### From 1 January 2027: four FHIR APIs

![The four CMS-0057 APIs connecting a payer to members, providers, other payers and clinicians](docs/assets/articles/cms-0057-apis.svg)

1. **Patient Access API**: the existing API, extended so members can also see their prior authorization requests and decisions.
2. **Provider Access API**: in-network providers can retrieve their patients' claims, encounter data, clinical data and prior authorizations, with a member opt-out.
3. **Payer-to-Payer API**: when a member changes plans, the new payer can request up to five years of data from the old one, with the member's permission.
4. **Prior Authorization API**: providers can find out whether an item or service needs prior authorization, what documentation is required, and submit the request and get the decision, all over FHIR.

## The standards behind it

CMS names a set of HL7 FHIR implementation guides for these APIs:

- **SMART App Launch** and **Bulk Data Access** for security and bulk export
- **CARIN Blue Button** for claims as ExplanationOfBenefit resources
- **Da Vinci PDex** for clinical data and prior authorizations exchanged between payers and with providers
- **Da Vinci CRD, DTR and PAS** for the Prior Authorization API: coverage requirements, documentation templates and the request itself
- **Da Vinci HRex** for shared building blocks such as `$member-match`

CMS has also said it will use enforcement discretion for payers who handle prior authorization over FHIR with Da Vinci PAS rather than the X12 278 transaction alone.

## What a payer actually has to build

Meeting the rule means running, securing and connecting a lot of moving parts:

- A FHIR server holding members' claims, clinical data and prior authorizations
- An authorization server for SMART on FHIR, with patient and backend-services flows
- Conversion of claims (837 and 835) into CARIN Blue Button resources
- Member matching across payers, with consent
- Bulk export for providers, honoring member opt-outs
- A CRD service over CDS Hooks, DTR questionnaire packages, and a PAS endpoint
- A bridge between PAS and the X12 278 the existing utilization management system speaks
- The yearly metrics page

## How Perfuse does it

![CRD, DTR and PAS between the EHR and the payer](docs/assets/articles/da-vinci-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with the whole CMS-0057 stack built in. It is one file to download, with nothing else to install.

- **All four APIs** from the built-in FHIR endpoint, with a screen that shows which ones are ready on your instance and what each still needs.
- **CARIN Blue Button 2.2.0**: `perfuse cms0057` turns an 837 and its 835 into professional, inpatient or outpatient ExplanationOfBenefits, with their Patient, Coverage, Organizations and Practitioners. The output passes the official HL7 validator with no errors.
- **Da Vinci PDex 2.2.0** prior authorizations, built from PAS decisions.
- **HRex `$member-match`** for Payer-to-Payer, unique matches only and consent required.
- **`$davinci-data-export`** for Provider Access, honoring member opt-outs.
- **The right data to the right party**: exports leave out cost-sharing and provider remittances for providers and other payers, and denied prior authorizations for other payers.
- **The yearly metrics page** in the layout of CMS's own template, published as a self-contained page, CSV or JSON.
- **A built-in SMART on FHIR authorization server**: standalone and EHR launch with PKCE, Backend Services, granular scopes, introspection and revocation. It passes Inferno's SMART App Launch STU2.2 suite, 80 of 80.
- **The full Prior Authorization API**: Da Vinci CRD 2.2.1, DTR 2.2.0 and PAS 2.2.1, tested with Inferno (CRD Server suite passes, PAS Server 82 of 84, DTR Payer 43 of 45).
- **PAS to and from the X12 278**, so your existing utilization management system stays in place.

Perfuse also does everything an integration engine does: HL7 v2, FHIR, X12, DICOM and CDA over twenty connector types, with a durable queue, a web console and full monitoring. So the same engine that feeds your FHIR server from existing systems also serves the APIs.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse serve` and open http://127.0.0.1:8080.
3. Read the [manual](https://perfuse.health/manual/) for the CMS-0057 setup.

Related: [Da Vinci CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/).
