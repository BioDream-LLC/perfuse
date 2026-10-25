# CMS-0057-F before January 2027: a payer's readiness checklist

![The four CMS-0057-F APIs as a checklist](docs/assets/articles/hero-cms-checklist.svg)

## In one minute

- CMS-0057-F, the Interoperability and Prior Authorization rule, applies to Medicare Advantage organisations, state Medicaid and CHIP programmes and their managed care plans, and qualified health plans on the federal exchanges.
- Its operational parts started in 2026: decision timeframes for prior authorization, a specific reason for every denial, and public metrics.
- Its four FHIR APIs are due from **1 January 2027**: Patient Access, Provider Access, Payer-to-Payer and Prior Authorization.
- The work is less about writing APIs than about plumbing: getting claims, decisions and members out of the systems that hold them, matching members between plans, and connecting the new prior authorization route to the review system nurses already use.
- This checklist is the order we would work in. Perfuse, a free engine, covers each item, and its readiness screen says which ones a server is ready for.

## Who this is for

For people at health plans, and the consultants helping them, who own some part of CMS-0057-F and want a plain list of what to have working, and what to test first.

> In plain words: an **API** is a door one system offers another, here built on the FHIR standard. **Prior authorization** is a plan saying yes before a service is given.

## 1. Decide what systems hold the data

Before any API, list where each kind of data lives: paid claims and remittances (often X12 837 and 835 files), prior authorization decisions (the utilization management system), member enrolment (834 files or a membership system), the provider directory, and member consent. Every API below is a view onto these. The gaps found here are the real schedule risk.

## 2. Patient Access: claims and decisions in the member's app

Members must be able to read their claims and, from 2027, their prior authorizations, in an app of their choosing.

- Paid claims converted to **CARIN Blue Button** ExplanationOfBenefit resources.
- Prior authorization decisions as **Da Vinci PDex** resources, including the reason for a denial.
- **SMART on FHIR** sign-in, so the member chooses the app and the plan does not.

Test first: one member, one claim of each type (professional, institutional, pharmacy), one approval and one denial, all through a third-party app.

## 3. Provider Access: the plan's data about attributed patients

In-network providers can request data about the patients attributed to them, in bulk.

- An attribution list per provider (Da Vinci ATR), kept current from the plan's own attribution.
- **Bulk Data** export, limited to that provider's own patients.
- A member's right to opt out, respected.

Test first: that a provider's token can export only its own list, and nobody else's.

## 4. Payer-to-Payer: a member's history follows them

When a member moves plans, and consents, the new plan can request their data from the old one.

- **`$member-match`** to find the member at the other plan from their old coverage details.
- An export of five years of claims and decisions (PDex), as the rule sets out.
- Consent recorded and checked before anything leaves.

Test first: a match that should succeed, one that should fail, and one where consent is absent.

## 5. Prior Authorization: from the order to the decision

This is the largest piece, and it has three parts that work in sequence:

- **CRD** at the order: tell the clinician, inside their EHR, whether approval is needed.
- **DTR** for the paperwork: a questionnaire the EHR can fill from the record.
- **PAS** for the request and the decision, including an inquiry to check status.

Two decisions to make early:

- **Which releases you answer.** EHRs will arrive speaking Da Vinci 2.1.0 or 2.2.x for years. Answering only one turns some providers away. See [Da Vinci 2.1.0 or 2.2](https://perfuse.health/davinci-2-1-and-2-2/).
- **How it reaches your reviewers.** Most plans already run a utilization management system where nurses and physicians work cases. The new route must feed that system (typically as an X12 278), not ask reviewers to move. And a provider's status inquiry should find an authorization however it was requested: by the API, by fax, through the portal or by phone.

Test first: Inferno's official CRD, DTR and PAS test kits, then one real request end to end into your UM test environment.

## 6. The timeframes, the denial reasons and the metrics

These started in 2026 and must keep working through the API change:

- Standard decisions within **7 calendar days**, expedited within **72 hours**, for the payers the timeframes apply to.
- A specific reason for every denial, sent back to the provider.
- Yearly public metrics: approvals, denials, approvals after appeal, extensions, and the time from request to decision.

Test first: that the metrics count requests from every route, electronic and not, once each.

## 7. The provider directory

A public **Plan-Net** directory of in-network providers, kept current from the plan's credentialing data.

## How Perfuse covers the list

Perfuse is one free program (Apache 2.0) that converts claims to CARIN Blue Button and decisions to PDex, serves the FHIR API with a built-in SMART authorization server, Bulk Data export, `$member-match` and Plan-Net, and runs CRD, DTR and PAS in both their 2.2.x and 2.1.0 releases. Requests go to your UM system as X12 278s over the routes payers use (HTTPS, CAQH CORE SOAP and MIME, SFTP batch), a reviewer queue is built in for plans without one, and the yearly metrics page is produced in CMS's layout. Decisions come from your rules, your reviewers, or a decision engine you plug in (BioDream's [Vaglio](https://github.com/BioDream-LLC/vaglio) is one; Perfuse never requires it). The console's **CMS-0057** screen lists each API with what is switched on and what is still missing.

How it was checked: Inferno's hosted kits against the public test server (SMART 80 of 80, Bulk Data 48 of 48, PDex payer-to-payer 19 of 19, CRD 92 pass with none failed, PAS 82 of 84, DTR 43 pass and 2 fail, Plan-Net 199 pass and 10 fail), every failure explained. What it has not done yet: run at a production health plan, or against a real UM system rather than a stand-in written from the published rules.

## Get started

1. Try the [free payer sandbox](https://perfuse.health/sandbox/), or sign in to the [public test server](https://test.perfuse.health/) as `demo` with password `try-perfuse-demo` and open the **CMS-0057** screen.
2. Read [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) and [prior authorization metrics](https://perfuse.health/prior-auth-metrics/).
3. See every [standard and version](https://perfuse.health/standards/) and the [test results](https://perfuse.health/proof/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
