# CMS-0057-F explained: the Interoperability and Prior Authorization rule, in plain terms

![CMS-0057-F, the Interoperability and Prior Authorization rule](docs/assets/articles/hero-cms-0057.svg)

## In one minute

- CMS-0057-F is a US government rule that tells many health insurance plans to make **prior authorization** (asking the plan to say yes before a test or treatment) faster, and to connect their computers to patients, doctors and each other.
- It affects certain insurance plans directly: Medicare Advantage, most Medicaid and CHIP plans, and the plans sold on HealthCare.gov. Doctors and patients feel the effects.
- From the start of **2026**: urgent decisions in **72 hours**, standard decisions in **7 calendar days**, a clear reason for every "no", and a yearly report card posted on the plan's website.
- From the start of **2027**: four computer connections (called **APIs**) that let members, doctors and other plans get the data they are entitled to.
- Perfuse is free software that already does all of this, so a plan does not have to build it from scratch.

> In plain words: a government rulebook that forces insurance plans to answer approval requests quickly, explain their decisions, and let the right people see the right records through a computer connection.

## What this rule is

CMS-0057-F is a **final rule** from the Centers for Medicare and Medicaid Services. CMS (the Centers for Medicare and Medicaid Services) is the part of the US government that runs Medicare and helps run Medicaid. A "final rule" is a regulation that has been decided and now has the force of law. The letters and number, CMS-0057-F, are just the file name CMS gave it; the "F" means final.

Its full name is the **Interoperability and Prior Authorization Final Rule**. Two long words sit at the heart of it:

- **Interoperability** means different computer systems being able to talk to each other and share information in a form they both understand. Think of two people who both agree to speak the same language instead of shouting past each other.
- **Prior authorization** means asking the health plan for permission before a patient gets a particular test, scan, treatment or piece of equipment. The plan checks whether it will pay, and only then does the care go ahead. It is meant to control cost and avoid unnecessary care, but when it is slow it keeps patients waiting.

So the rule is about two things at once: making prior authorization quicker and clearer, and making health plans' computers share data properly.

## Why the rule exists

For years, prior authorization has been one of the most complained-about parts of American healthcare. A doctor orders something, then the plan has to agree, and the agreement often arrives by fax, phone call or a website that each plan builds differently. Decisions can take days. Patients wait. Doctors' offices spend hours chasing answers. Sometimes a request is denied and nobody can tell the patient exactly why.

At the same time, patients could not easily get their own records out of their insurance plan, doctors could not easily pull a new patient's history from the plan, and when someone switched plans their information did not travel with them.

CMS wrote CMS-0057-F to fix both problems with firm deadlines and a shared technical language, so that the exchange works the same way across the many plans a person may deal with over a lifetime.

## Who it affects

![Who the CMS-0057-F rule covers](docs/assets/articles/cms-0057-who.svg)

The rule names the plans it covers. CMS calls them **impacted payers**. A payer is simply an organization that pays for healthcare, in other words an insurance plan.

- **Medicare Advantage organizations.** These are private plans that run Medicare benefits for people on Medicare.
- **State Medicaid and CHIP fee-for-service programs.** Medicaid is the public program for people with low income; CHIP (the Children's Health Insurance Program) covers children. "Fee-for-service" means the state pays doctors directly for each service.
- **Medicaid managed care plans and CHIP managed care entities.** These are the private plans a state hires to run Medicaid or CHIP for it.
- **Qualified Health Plan issuers on the Federally-Facilitated Exchanges.** A Qualified Health Plan, or QHP, is a plan sold on the government's insurance marketplace, HealthCare.gov. The issuer is the company that offers it.

Two groups are affected even though the rule does not aim at them directly:

- **Doctors and hospitals.** From the 2027 reporting period, there is a measure that asks doctors and hospitals to report how much of their prior authorization they do electronically. It is part of two existing programs that reward providers for using health technology well.
- **Patients and families.** They are the point of the whole exercise: faster answers, a reason for every denial, and a way to see their own requests and records.

One thing is left out on purpose. **Prescription drugs are outside the prior authorization parts of the rule.** Drugs already have their own separate rules, so they are not counted in the deadlines or the yearly report here.

## What happens, step by step

![What CMS-0057-F requires, and when](docs/assets/articles/cms-0057-timeline.svg)

Let us follow one made-up patient to see how the rule changes a real day.

Maria is 58 and has a Medicare Advantage plan. Her knee has hurt for months, and her doctor wants to order an MRI (magnetic resonance imaging, a detailed scan). The MRI needs prior authorization: the plan must agree before it is done.

1. **The doctor orders the MRI.** In the doctor's computer system, as the order is placed, the system quietly asks Maria's plan a question: does this scan need approval, and if so, what proof do you want to see?
2. **The plan answers right away.** It says yes, this needs approval, and here is the short form we need filled in, with questions about how long the knee has hurt and what has already been tried.
3. **The form fills itself in.** Most of the answers are already in Maria's chart, so the computer pre-fills them. Her doctor reads them over, adds a note, and signs.
4. **The request is sent to the plan**, with the proof attached in the same message.
5. **The plan decides.** Because this is a standard (not urgent) request, the plan has **7 calendar days** to answer. If Maria's knee were an emergency, it would be an urgent request and the limit would be **72 hours**. In practice, because the right proof arrived with the request, the plan can often answer in minutes.
6. **If the answer is no, it comes with a reason.** The rule requires the plan to give a specific reason for every denial, whatever way the request came in. So Maria and her doctor are not left guessing.
7. **Maria can see the request herself.** Through an app she chooses, she can look at the prior authorization and its decision, because the rule says the plan must make that visible to her.

That is the 2026 half of the rule: faster decisions, a reason for every denial, and the request visible to the patient. The 2027 half adds the computer connections that make records flow.

### The four connections, from 2027

![The four CMS-0057 APIs a payer serves with Perfuse](docs/assets/articles/cms-0057-apis.svg)

From 1 January 2027, each impacted plan must run four computer connections. The technical word for such a connection is an **API** (an application programming interface, a doorway that lets one computer system ask another for information in an agreed way). Here is what each one does, in plain terms.

1. **Patient Access API.** This lets a patient's chosen app pull their own information from the plan: claims, clinical data, and now their prior authorization requests and decisions too. This connection already existed from an earlier rule; CMS-0057-F adds the prior authorizations to it.
2. **Provider Access API.** This lets a doctor who is in the plan's network pull the records of their own patients: claims, visit data, clinical data and prior authorizations. A patient can tell the plan to leave them out if they wish; that is called an opt-out.
3. **Payer-to-Payer API.** When a patient changes from one plan to another, the new plan can ask the old plan for up to five years of the patient's data, with the patient's permission. So the history follows the person.
4. **Prior Authorization API.** This is the one that makes Maria's story work. It lets a doctor's system find out whether something needs approval, learn what proof is required, send the request, and get the decision, all by computer.

## Common questions

**Does this mean prior authorization goes away?**
No. The plan can still require approval for the same things. The rule makes the process faster, clearer and electronic; it does not remove the plan's right to say yes or no.

**Will patients notice anything?**
Over time, yes: quicker answers, a reason attached to every denial, and the ability to see their own requests in an app. Much of the machinery is invisible to the patient, which is the point.

**Is this the same as the "No Surprises" billing rules, or other health laws?**
No. Those are separate. CMS-0057-F is specifically about data sharing and prior authorization speed.

**What if a plan misses a deadline?**
The deadlines are requirements, and the yearly public report shows how often the plan met them. Being slow becomes visible, which is part of the pressure the rule applies.

**Who built the technical standards?**
CMS points to a set of shared recipes written by HL7, a healthcare standards body (see the glossary). Using shared recipes is what lets different plans and systems connect the same way.

## The shared recipes behind it

CMS does not make each plan invent its own method. It names a set of **implementation guides** from HL7. An implementation guide is a detailed recipe that says exactly how a particular exchange should work, so that any two systems following it will fit together. The main ones here:

- **SMART App Launch** and **Bulk Data Access**, for securely letting an app sign in and for sending data in bulk.
- **CARIN Blue Button**, for presenting claims (the bills and payments) in a standard shape.
- **Da Vinci PDex**, for clinical data and prior authorizations shared between plans and with providers.
- **Da Vinci CRD, DTR and PAS**, the three recipes that together make up the Prior Authorization connection. (There is a separate plain-language guide to these three; see the link at the end.)
- **Da Vinci HRex**, for shared pieces such as matching a member across two plans.

CMS has also said it will not punish a plan for handling prior authorization through the new FHIR method rather than only through the older X12 method. (FHIR and X12 are two technical languages; another guide compares them.)

## What a plan actually has to build

Meeting the rule is a large job if a plan starts from nothing. It means running and connecting many moving parts:

- A **FHIR server**, which is a store that holds members' claims, clinical data and prior authorizations in the shared shape.
- A **sign-in service** so apps and other systems can be let in safely.
- A way to turn claims from the old billing format into the shared CARIN shape.
- A way to **match a member** across two plans, with consent, so data goes to the right person.
- **Bulk export** for doctors, while respecting any patient who has opted out.
- The three prior authorization recipes (CRD, DTR, PAS) and a bridge to the older X12 method so the plan's existing systems keep working.
- The **yearly report page**.

## How Perfuse does it

![Da Vinci prior authorization: CRD, DTR and PAS](docs/assets/articles/da-vinci-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is free, open-source software with the whole CMS-0057 toolkit built in. It is one file to download, with nothing else to install, and it is covered by the Apache 2.0 licence, which means no fee and no per-connection charge.

- **All four connections** from the built-in FHIR server, with a screen that shows which ones are ready on your copy and what each still needs.
- **Claims in the CARIN shape.** A single command turns the plan's billing records into the standard claim shape, and the result passes the official HL7 checking tool with no errors.
- **Prior authorizations in the PDex shape**, built from the decisions the plan makes.
- **Member matching** for the Payer-to-Payer connection, which only ever returns a single, certain match, and only with the member's consent.
- **Bulk export** for the Provider Access connection, which leaves out any member who has opted out.
- **The right data to the right party.** Exports leave out cost and payment details that a doctor or another plan should not see, and leave denied prior authorizations out of what goes to another plan.
- **The yearly report page**, in the layout of CMS's own template, produced as a ready-to-publish web page, a spreadsheet or data.
- **A built-in sign-in service** that passes the official SMART test suite, 80 of 80.
- **The full Prior Authorization connection**, using CRD, DTR and PAS, each checked against the official HL7 test kits (CRD passes, PAS 82 of 84, DTR 43 of 45; the handful left are items the recipes themselves forbid, explained in the project's record).
- **A bridge to the older X12 method**, so the plan's existing approval system stays in place.

Perfuse also does everything a general healthcare integration engine does: it moves many kinds of medical message between systems, keeps a safe queue so nothing is lost, and offers a web console for watching it all. So the same software that fills the FHIR server from the plan's existing systems also serves the four connections.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run it and open the console in your web browser.
3. Read the [manual](https://perfuse.health/manual/) for the CMS-0057 setup.

Related reading: [Da Vinci CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/) · [X12 278 and FHIR PAS compared](https://perfuse.health/x12-278-vs-pas/) · [Prior authorization metrics explained](https://perfuse.health/prior-auth-metrics/).

## Glossary

| Term | What it means |
|---|---|
| API | Application programming interface: a doorway that lets one computer system ask another for information in an agreed way. |
| Bulk Data Access | A shared recipe for sending many records at once. |
| CARIN Blue Button | A shared recipe for presenting claims (bills and payments) in a standard shape. |
| CHIP | Children's Health Insurance Program, public coverage for children. |
| CMS | Centers for Medicare and Medicaid Services, the US government body that runs Medicare and helps run Medicaid. |
| CMS-0057-F | The file name of this rule; the "F" means final. |
| CRD, DTR, PAS | The three Da Vinci recipes that together make up the prior authorization connection. |
| Da Vinci | An HL7 project that writes recipes for data exchange between plans and providers. |
| FHIR | Fast Healthcare Interoperability Resources: the modern shared language for health data. |
| HL7 | Health Level Seven International, the body that writes healthcare data standards. |
| Implementation guide | A detailed recipe saying exactly how a particular exchange should work. |
| Interoperability | Different computer systems being able to share information in a form they both understand. |
| Medicaid | Public health coverage for people with low income. |
| Medicare Advantage | Private plans that run Medicare benefits. |
| MRI | Magnetic resonance imaging, a detailed scan. |
| Opt-out | A patient's choice to be left out of a particular data sharing. |
| Payer | An organization that pays for healthcare; an insurance plan. |
| PDex | A Da Vinci recipe for sharing clinical data and prior authorizations. |
| Prior authorization | Asking the health plan for permission before a patient gets a test or treatment. |
| QHP | Qualified Health Plan, a plan sold on the government's HealthCare.gov marketplace. |
| SMART App Launch | A shared recipe for letting an app sign in safely to a health system. |
| X12 | An older technical language used for insurance transactions. |

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
