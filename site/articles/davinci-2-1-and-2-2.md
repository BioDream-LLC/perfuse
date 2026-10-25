# Da Vinci 2.1.0 or 2.2: which release a payer must answer in 2027

![An EHR on 2.1.0 and an EHR on 2.2.x, both answered by one payer](docs/assets/articles/hero-davinci-releases.svg)

## In one minute

- The three Da Vinci guides behind electronic prior authorization (CRD, DTR and PAS) are published in more than one release. The two that matter now are **2.1.0** and **2.2.x** (CRD 2.2.1, DTR 2.2.0, PAS 2.2.1).
- EHR vendors and payers are not all building to the same one. Some EHRs will arrive at a payer speaking 2.1.0, others 2.2.x, and that will be true for years.
- The differences are small in number but they break things: renamed parameters, elements one release has and the other does not, and a different shape for some answers.
- A payer that answers in only one release will turn some providers away, or send them answers their software cannot read.
- Perfuse answers in both: it reads which release the caller speaks from the request itself, keeps one record of every decision, and writes each answer in the caller's release.

## Who this is for

This guide is for people at health plans, and the vendors and consultants who work for them, who are getting ready for the January 2027 prior authorization deadline in CMS-0057-F. You do not need to read FHIR to follow it.

> In plain words: **CRD** tells a doctor, while ordering, whether approval is needed. **DTR** gathers the paperwork with a questionnaire. **PAS** sends the request and brings back the decision. A **release** is one published version of a guide, and software is written to a particular release.

## Why there are two releases at once

Standards are published, used, found wanting and fixed. The Da Vinci guides went from 2.0.1 to 2.1.0 and then to 2.2. HL7's own leads have said plainly that 2.2 fixes a wide number of errors present in 2.1, and that no further update of 2.1 is planned.

That would settle it if everybody moved at once. They do not. EHR vendors plan their releases a year or more ahead, certify them, and roll them out to customers who upgrade on their own schedule. A hospital that installed a 2.1.0-based build will keep sending 2.1.0 requests until it upgrades. So a payer in 2027 will hear from both.

> In plain words: the newer release is better, but the payer does not get to choose what the doctor's software sends.

## What actually differs

Most of a request is the same in both releases: the patient, the coverage, the service, the codes. The trouble is in the details, and software is unforgiving about details. A few examples of the kind of difference a payer has to handle:

- **DTR's questionnaire package.** In 2.1.0 the package comes back under a parameter named `PackageBundle`; in 2.2 the name and the contents changed, and 2.2 can include a partly filled response that 2.1.0 does not expect.
- **Adaptive questionnaires.** The next-question operation names its input and output parameters differently, and the rules for what the questionnaire itself may contain changed.
- **PAS answers.** Some extensions that 2.2 adds to a ClaimResponse do not exist in 2.1.0, an error location is written as text in one and as a structured value in the other, and an inquiry is answered with one Bundle in 2.1.0.
- **CRD cards.** 2.2 can say a coverage answer is "indeterminate"; 2.1.0 has no such value, so the same meaning has to be said another way.

None of these is hard alone. Together they mean a server built for one release will, sooner or later, send the other release something it refuses.

![The Da Vinci flow: CRD at the order, DTR for documentation, PAS for the request and decision](docs/assets/articles/da-vinci-flow.svg)

## Three ways to handle it, and their costs

**Answer in one release only.** Simple to build, and wrong for some callers. Either some providers cannot use the electronic route, which is exactly what the rule is meant to end, or they get answers their software misreads.

**Run two servers.** One for each release. It works, but now there are two places decisions are made, two sets of rules to keep in step, two audit trails, and the metrics CMS asks payers to publish have to be added up across both.

**One server that speaks both.** Decide once, record once, and translate only at the edge: read which release the request is in, and write the answer in that release. This is the approach that keeps one source of truth.

## How Perfuse does it

Perfuse takes the third approach. It decides and stores everything in the 2.2 form, and rewrites the answer for a 2.1.0 caller on the way out. It works out which release a caller speaks, in this order:

1. **From the request.** CRD has a field for it (`davinci-crd.requestedVersion`), and DTR and PAS requests name their profiles with a version (`|2.1.0`).
2. **From the client's registration.** A payer can record that a particular EHR client speaks 2.1.0.
3. **From the server's default,** which a payer sets.

It also advertises both: its CDS Hooks discovery lists both CRD releases, and its FHIR capability statement lists both DTR and PAS releases, so a client can see before it calls.

How it was checked, stated plainly: no Inferno test kit exists for 2.1.0, so each 2.1.0 guide's own example requests were sent to Perfuse and every answer was checked by the HL7 FHIR validator against 2.1.0, and again against 2.2. There were no errors in Perfuse's answers. Inferno's official 2.2 suites still give the same results as before (CRD 92 pass with none failed, PAS 82 of 84, DTR 43 pass and 2 fail, the failures explained on the proof page). It has not yet been tried against a real EHR in production.

## What to ask any vendor

Whatever you use, these questions sort the ready from the not-yet:

- Which releases of CRD, DTR and PAS do you answer, and how do you decide which one a caller is using?
- Is a decision made once, or once per release?
- Can I see the answers for both releases checked by the HL7 validator?
- How are the CMS metrics counted when requests arrive in two releases?

## Get started

1. Try the [free payer sandbox](https://perfuse.health/sandbox/): send it a 2.1.0 request and a 2.2 request and compare the answers.
2. Read [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) and [CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/).
3. See every [standard and version](https://perfuse.health/standards/) Perfuse supports, and what checked each.

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [The 2.1.0 checks and their results](https://github.com/biodream-llc/perfuse/tree/main/conformance/davinci21)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
