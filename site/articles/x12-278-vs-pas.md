# X12 278 and FHIR PAS: two ways to request prior authorization, and how to bridge them

![X12 278 and FHIR PAS connected in both directions](docs/assets/articles/hero-x12-pas.svg)

## In one minute

- Prior authorization means asking a health plan to say yes before a test or treatment. In the US there are two electronic ways to do it.
- **X12 278** is the long-standing way, used by health plans and the middlemen between them for years. It is written as rows of codes.
- **FHIR PAS** is the newer way, built on modern health records, and it is the one the government rule requires from 2027.
- Most organizations will need **both** for years, because not everyone will switch at once.
- Perfuse speaks both and **translates between them in both directions**, so a plan can keep its old system and still offer the new connection.

> In plain words: two different languages for the same request. One is old and widely spoken; one is new and required. Perfuse is the translator in the middle.

## Why there are two

For a long time, when a doctor's office asked a health plan for approval electronically, it used a format called the **X12 278**. X12 is a family of electronic business messages used across US industries, and the "278" is the specific one for a healthcare services review, which is the formal name for a prior authorization request and answer. Plans built their systems around it, and so did **clearinghouses**, the companies that sit between doctors and plans and pass messages along.

Then came a newer, friendlier way of handling health data called **FHIR** (Fast Healthcare Interoperability Resources), the modern shared language for medical records. A set of recipes built on FHIR, called **PAS** (Prior Authorization Support), does the same job as the X12 278 but in that modern language. A US government rule, CMS-0057-F, requires health plans to offer the PAS method from 1 January 2027.

So there are two electronic languages for exactly the same conversation, and for a good while both will be in use.

## The two side by side

![Two languages for the same request](docs/assets/articles/x12-278-vs-pas-compare.svg)

### The X12 278

The 278 is the long-standing standard. A request carries the patient, the doctors involved, the diagnoses and the services asked for; the answer carries the plan's decision and, when approved, a certification number that proves the approval.

A few things to know about it:

- It is written as **rows of codes**, compact and precise but not meant for a human to read easily.
- It is the native tongue of plans' approval systems and of clearinghouses.
- The supporting proof (notes, test results) travels in a **separate message**, not inside the request itself.

> In plain words: the 278 is like a tightly abbreviated telegram that the plan's old systems understand perfectly, with the attachments posted separately.

### Da Vinci PAS

PAS says the same thing in the modern FHIR language:

- The request is a **package of health records** sent to the plan.
- The **proof is attached in the same request**, so everything the plan needs arrives together.
- The answer comes back as a record saying approved, denied, changed or pended, with the approval number.
- The plan can push the final answer back automatically when a pended request is decided.
- PAS was designed to sit inside the doctor's normal software, alongside the CRD and DTR recipes, so a request can often be decided at once. (See the separate Da Vinci guide.)

> In plain words: PAS is like a modern email with the attachments included, written in a language newer systems speak naturally.

## Why both matter

- Under the main US health privacy law (HIPAA), the 278 is the standard for prior authorization between the organizations it covers. CMS has said it will not punish a plan for using the newer PAS method instead.
- PAS was carefully shaped to **line up with** the 278, so a translator can turn a modern request into an old-style one for a plan that still speaks only X12, and turn the old-style answer back into the modern shape.
- This lets plans keep their existing approval systems while offering the new connection the rule requires. Nobody has to replace everything at once.

## What happens, step by step

![A translator between the two languages](docs/assets/articles/x12-278-vs-pas-bridge.svg)

Let us follow a made-up example. A clinic uses modern software that speaks FHIR PAS. The patient, Lena, needs a wheelchair that her plan must approve. Her plan, however, still runs an older approval system that speaks only X12 278.

1. **Lena's clinic sends the request in PAS.** Its modern software packages up the request and the proof and sends it toward the plan.
2. **The translator steps in.** In the middle sits software (such as Perfuse) that turns the PAS request into an X12 278 the plan can understand.
3. **The plan decides as it always has**, using its existing X12 system, and sends back an X12 278 answer.
4. **The translator turns the answer back into PAS**, so Lena's clinic receives a modern record saying approved, with the approval number.
5. **Neither side had to change.** The clinic kept its new software; the plan kept its old system. The translator let them talk.

The same works in reverse: a clinic that still sends X12 can reach a plan that offers only the modern PAS connection, because the translator works both ways.

## An everyday way to picture it

Imagine two offices that have to exchange forms.

The first office has used the same paper form for twenty years. Everyone there knows exactly which box means what, the filing cabinets are built around it, and the couriers who carry forms between offices expect that layout. The form is cramped and full of abbreviations, but it works and nobody wants to throw out twenty years of filing. That is the **X12 278**.

The second office is new. It uses a clean digital form that explains itself, carries its attachments stapled inside, and can be filled in by the computer that already holds the answers. That is **FHIR PAS**.

Both offices are asking the same thing: may we go ahead with this? The problem is only that they write it differently. Rather than force the old office to rebuild its filing cabinets overnight, you put a translator at the door who turns one form into the other, both ways. The old office keeps its cabinets; the new office keeps its clean form; the request still gets through. That translator is the role Perfuse plays.

## What it means for each party

Different people care about different parts of this.

- **A doctor's office** wants the request to go through and the answer to come back, without having to care which language the plan speaks. With a translator in place, the office uses whatever software it has and still reaches every plan.
- **A health plan** wants to meet the 2027 requirement without ripping out an approval system that works and that staff know. The bridge lets the plan offer the modern connection on the outside while the old system keeps running on the inside.
- **A clearinghouse**, the middleman that passes messages between many doctors and many plans, has to handle whatever each side sends. Speaking both languages, and translating, is exactly its business.
- **A patient** does not see any of this, and should not have to. They simply benefit when the request is approved faster because the proof travelled with it.

## Why not just switch everyone to the new way?

It is a fair question. If PAS is required and friendlier, why keep X12 around at all?

The honest answer is scale and risk. The X12 278 is woven into thousands of systems, and those systems handle real patients' care every day. Replacing all of them at the same moment would be enormously expensive and, worse, dangerous: a bug introduced during a mass switch could delay approvals for people who need care now. Healthcare changes carefully, in steps, with the old and new running side by side until the new is proven. A translator is what makes that careful, step-by-step change possible. It lets each organization move when it is ready, instead of forcing everyone to jump together.

There is also a plain legal reason: the X12 278 remains a named standard under the main US health privacy law, so plans have long been required to support it. CMS easing the way for PAS does not erase that overnight.

## Common questions

**Will the X12 278 disappear?**
Not soon. It is deeply embedded in plans and clearinghouses, and it remains a legal standard. Expect both to run side by side for years.

**If PAS is required, why keep X12 at all?**
Because switching every system at once is impractical and risky. The bridge lets each side move at its own pace while still connecting.

**Does translating lose anything?**
A careful translator keeps the meaning intact and checks the result against the official rules. Where something genuinely has no equivalent, a trustworthy translator reports it rather than quietly guessing.

**Is one of them better?**
They are built for different eras. PAS is easier for modern systems and carries its proof together; X12 is compact and universally understood by existing plan systems. The point is not to pick a winner but to connect them.

## How Perfuse does it

![Perfuse bridges FHIR PAS and X12 278 in both directions](docs/assets/articles/x12-278-vs-pas.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is free, open-source software that speaks both languages and translates between them. It is one file to download, licensed under Apache 2.0, so there is no fee.

**It understands X12 properly**

- It reads and writes the main X12 healthcare messages, including the 278 for prior authorization, understanding their structure rather than treating them as plain text.
- It can also build and read the separate attachment message that carries supporting documents.

**It handles PAS on either side**

- **Translate both ways:** Perfuse maps PAS to and from the X12 278, so a modern request reaches a plan that only speaks X12, and the X12 answer comes back as a modern record that passes the official HL7 checking tool.
- **Be the plan:** Perfuse can receive PAS requests, let a reviewer decide, and push pended decisions back automatically. It passes 82 of 84 checks in the official HL7 PAS test suite.
- **Reach the plan's own review system:** Perfuse sends the 278 the way plans' utilization management systems take it: CAQH CORE web services (SOAP or MIME), batch files over SFTP, or plain HTTPS. It reads their receipts (the 999 and TA1) as well as their answers. This was tested against a stand-in built from CAQH's published rules, not yet against a real plan or clearinghouse, and Perfuse is not CAQH CORE certified.
- **Be the doctor's side:** Perfuse can submit requests to plans' modern connections.
- **The companions too:** Perfuse also handles the CRD and DTR recipes, so the order, its paperwork and the request all have an answer.
- PAS decisions become PDex prior authorizations for the Patient, Provider and Payer-to-Payer connections the rule requires.

**And the connections around it**

Perfuse also connects over the many channels healthcare uses, with a safe queue that keeps every message in order and never loses one. So the same software that translates between X12 and PAS also moves the rest of the plan's data.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run it with the prior authorization options turned on, and send it a PAS request or route an X12 file through it.
3. The [manual](https://perfuse.health/manual/) shows both paths.

Related reading: [Da Vinci CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/) · [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [Prior authorization metrics explained](https://perfuse.health/prior-auth-metrics/).

## Glossary

| Term | What it means |
|---|---|
| Certification number | The number a plan gives to prove it approved a request. |
| Clearinghouse | A company that sits between doctors and plans and passes messages along. |
| CMS | Centers for Medicare and Medicaid Services, the US government body behind the rule. |
| CMS-0057-F | The US rule that requires the PAS method from 2027. |
| CRD, DTR | The two Da Vinci recipes that sit alongside PAS (asking whether approval is needed, and gathering the proof). |
| FHIR | Fast Healthcare Interoperability Resources: the modern shared language for health data. |
| HIPAA | The main US health privacy law, which names the X12 278 as a standard. |
| HL7 | Health Level Seven International, the body that writes healthcare data standards. |
| PAS | Prior Authorization Support: the FHIR way to request approval. |
| PDex | A Da Vinci recipe for sharing clinical data and prior authorizations. |
| Pended | A request still being decided, with the answer to follow. |
| Prior authorization | Asking the health plan for permission before a test or treatment. |
| X12 | A family of electronic business messages used across US industries. |
| X12 278 | The specific X12 message for a prior authorization request and answer. |

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
