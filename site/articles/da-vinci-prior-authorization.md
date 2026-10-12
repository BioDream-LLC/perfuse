# Da Vinci CRD, DTR and PAS explained: electronic prior authorization from order to decision

![CRD, DTR and PAS as three linked steps from order to decision](docs/assets/articles/hero-da-vinci.svg)

## In one minute

- Prior authorization means asking a health plan to say yes before a test or treatment. For years it has meant phone calls, faxes and portals.
- **Da Vinci** is a healthcare project that wrote three computer recipes to replace that, and they work together inside the doctor's normal software.
- **CRD** answers one question as the doctor orders: does this need approval, and what proof do you want?
- **DTR** hands the doctor a form that fills itself in from the patient's chart.
- **PAS** sends the request to the plan and brings back the decision, often in seconds.

> In plain words: three short recipes, named CRD, DTR and PAS, that turn a slow fax-and-phone chore into a quick computer conversation between the doctor's system and the health plan.

## What this is

When a government rule (CMS-0057-F) told health plans to make prior authorization electronic, it did not ask each plan to invent its own method. It pointed to recipes written by the **Da Vinci Project**, a group inside the healthcare standards body **HL7** (Health Level Seven International). Da Vinci's job is to write shared recipes so that plans and doctors' systems can exchange data the same way everywhere.

Three of those recipes handle prior authorization, and they are meant to be used together:

- **CRD**, which stands for Coverage Requirements Discovery.
- **DTR**, which stands for Documentation Templates and Rules.
- **PAS**, which stands for Prior Authorization Support.

Together they are the **Prior Authorization connection** (an API, or computer doorway) that CMS-0057-F requires health plans to offer from 1 January 2027.

## Why it exists

Prior authorization is one of the biggest sources of friction in American healthcare. A doctor decides a patient needs something, but the plan has to agree first. The agreement traditionally travels by fax, phone or a website that each plan builds in its own way. The doctor's office often finds out only after the visit that approval is needed, then hunts for the plan's rules, fills in a form by hand, faxes it, and waits.

![Prior authorization before and after Da Vinci](docs/assets/articles/da-vinci-before-after.svg)

That is slow, error-prone and frustrating for everyone. The three Da Vinci recipes move the whole conversation into the doctor's software, so the question is answered the moment the order is placed, the paperwork fills itself in, and the request and its proof travel together.

## Who it affects

- **Health plans** must offer these recipes as part of the Prior Authorization connection from 2027.
- **Doctors and their software vendors** gain the ability to ask, document and submit without leaving the chart.
- **Patients** get decisions far sooner, because the plan usually has what it needs to decide at once.

## What happens, step by step

![From order to decision, inside the clinician's workflow](docs/assets/articles/da-vinci-flow.svg)

Let us follow a made-up patient. Dev is 44 and has back pain that will not settle. His doctor, Dr. Patel, wants to order a course of physical therapy, which Dev's plan requires approval for.

1. **Dr. Patel starts the order.** As she selects the physical therapy in her computer, the system quietly sends the order to Dev's plan and asks the first question. This is **CRD** at work.
2. **The plan answers in seconds.** It says: yes, this needs approval; here is the policy; and here is the short form we need, which asks how long the pain has lasted and what has already been tried.
3. **The form fills itself in.** The system calls on **DTR**, which pulls the questionnaire and fills most of the answers straight from Dev's chart: the date his pain started, the painkillers he has tried, a recent note. Dr. Patel reads it over, adds one sentence, and confirms.
4. **The request goes to the plan.** This is **PAS**. The completed form and the request travel together in one message.
5. **The plan decides.** Because the proof it needs is already attached, the plan can often approve the request immediately. Dr. Patel sees the answer before Dev has left the room.

Compare that with the old way: Dr. Patel signs the order, a staff member later notices it needs approval, looks up the plan's rules, fills in a form, faxes it, and the office waits two or three days for a reply that might be a denial with no clear reason.

## The three recipes, one at a time

![Three steps, three short names](docs/assets/articles/da-vinci-three-steps.svg)

### CRD: the answer at the moment of ordering

CRD, Coverage Requirements Discovery, answers the question "does this need approval?" at the exact moment the doctor is placing the order. It runs on a mechanism called **CDS Hooks**, which is simply a way for a doctor's system to ping the plan at the right moment, for example when an order is signed or an appointment is booked.

The plan replies, while the doctor is still looking at the order, with:

- whether the service is covered,
- whether prior authorization is needed, or already satisfied,
- which proof is required, and which form (questionnaire) collects it,
- links to the plan's written policy.

The answer rides along with the order, so later steps can refer back to it.

> In plain words: CRD is the plan whispering "yes, you'll need our approval for this, and here's the form" the instant the doctor orders.

### DTR: the paperwork, filled in for you

DTR, Documentation Templates and Rules, handles the proof. When CRD says proof is needed, the doctor's system asks the plan for the right form. The plan sends back the questionnaire together with the logic that pre-fills it from the patient's record.

The doctor reviews the pre-filled answers, completes anything left, and the result is a saved, completed form. Some plans use a cleverer version called an **adaptive questionnaire**, where each answer decides the next question, so the doctor is never shown questions that do not apply.

> In plain words: DTR is a form that already knows most of the answers, because it reads them from the chart, leaving the doctor to check and sign rather than retype.

### PAS: the request and the decision

PAS, Prior Authorization Support, sends the request to the plan and brings back the decision. The request carries everything the plan needs: who the patient is, their coverage, the provider, the services asked for, and the completed form from DTR.

The plan answers: approved, denied, changed, or pended (still being decided). A pended request can be checked later, and the plan can push the final decision back automatically when it is made.

Behind the scenes, the long-standing legal standard for prior authorization is a technical format called the **X12 278**, so PAS also defines how the two line up, letting a plan that still uses X12 take part. (There is a separate guide comparing X12 278 and PAS.)

> In plain words: PAS is the envelope that carries the request and its proof to the plan, and carries the answer back.

## How the three fit together

1. The doctor signs an order. **CRD** says prior authorization is needed and names the form.
2. **DTR** fetches the form, fills it from the chart, and the doctor completes it.
3. **PAS** submits the request with the completed proof.
4. The plan decides. Many requests can be approved right away, because the proof the plan needs is already there.

## Common questions

**Does the doctor have to learn three new tools?**
No. All three happen inside the doctor's existing software. The names CRD, DTR and PAS matter to the people building the systems, not to the doctor using them.

**What if the plan has no rule for this order?**
A good system does not assume approval is or is not needed; it says it cannot determine the answer and leaves it to a person, rather than guessing.

**Can every request be answered instantly?**
Not always. Some requests still need a human reviewer at the plan, so they come back as "pended" and a decision follows. But because the proof arrives up front, far more can be settled at once than before.

**Is the patient's data safe in all this?**
The connections use a secure sign-in step so that only authorized systems take part, and the patient's permission governs what is shared.

## How Perfuse does it

![The four CMS-0057 APIs a payer serves with Perfuse](docs/assets/articles/cms-0057-apis.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is free, open-source software that carries out all three recipes, for health plans and for the systems that talk to them. It is one file to download, licensed under Apache 2.0, so there is no fee.

**CRD, from a simple rules file**

- Perfuse answers the "does this need approval?" question from a plain rules file the plan writes: which codes, whether they are covered, whether approval is needed, which proof and which form, the reason, policy links and contacts.
- Every rules file is checked when it loads, so a mistake is caught at once rather than discovered by a doctor.
- It passes the official HL7 CRD test suite.

**DTR, forms that fill themselves**

- Perfuse hands back the forms as complete packages, including the logic they use to pre-fill from the chart and everything that logic depends on.
- It supports adaptive questionnaires, asking one question at a time.
- It passes 43 of 45 checks in the official HL7 DTR test suite; the two left require an option the base recipe does not allow.

**PAS, on either side**

- **Be the plan:** Perfuse can receive requests, let a reviewer decide, and push pended decisions back automatically. It passes 82 of 84 checks in the official HL7 PAS test suite; the two left are items the recipe's own rules forbid there.
- **Be the doctor's side:** Perfuse can submit requests to plans' FHIR connections.
- **Bridge to the old method:** Perfuse maps PAS to and from the X12 278, so a modern request reaches a plan that still speaks only X12, and the answer comes back in the modern shape, passing the official HL7 checker.
- PAS decisions become PDex prior authorizations for the Patient, Provider and Payer-to-Payer connections.

**Security included**

- A built-in sign-in service that passes the official SMART test suite, 80 of 80.

All of this ships inside the same single file as the rest of the engine, which also moves many kinds of medical message between systems and keeps a safe queue so nothing is lost.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run it with the prior authorization options turned on and point a doctor's system or a test tool at it.
3. The [manual](https://perfuse.health/manual/) walks through each step.

Related reading: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [X12 278 and FHIR PAS compared](https://perfuse.health/x12-278-vs-pas/) · [Prior authorization metrics explained](https://perfuse.health/prior-auth-metrics/).

## Glossary

| Term | What it means |
|---|---|
| Adaptive questionnaire | A form where each answer decides the next question. |
| API | Application programming interface: a doorway that lets one computer system ask another for information. |
| CDS Hooks | A way for a doctor's system to ping the plan at the right moment, such as when an order is signed. |
| CMS-0057-F | The US rule that requires these recipes from 2027. |
| CRD | Coverage Requirements Discovery: asks whether an order needs approval and what proof is wanted. |
| Da Vinci | An HL7 project that writes recipes for data exchange between plans and providers. |
| DTR | Documentation Templates and Rules: the self-filling form that gathers the proof. |
| FHIR | Fast Healthcare Interoperability Resources: the modern shared language for health data. |
| HL7 | Health Level Seven International, the body that writes healthcare data standards. |
| PAS | Prior Authorization Support: sends the request and brings back the decision. |
| PDex | A Da Vinci recipe for sharing clinical data and prior authorizations. |
| Pended | A request still being decided, with the answer to follow. |
| Prior authorization | Asking the health plan for permission before a test or treatment. |
| SMART | A shared recipe for letting an app or system sign in safely. |
| X12 278 | The older technical format used for prior authorization. |

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
