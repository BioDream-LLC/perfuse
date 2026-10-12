# Claims attachments and CMS-0053: signed C-CDAs in the X12 275

![A document with a signature seal, labelled C-CDA and X12 275](docs/assets/articles/hero-attachments.svg)

## In one minute

- When a clinic sends a bill to an insurer, the insurer often needs supporting paperwork: an operative note, a discharge summary, a test result.
- For years that paperwork travelled by fax and mail. A US rule called **CMS-0053** moves it onto computers.
- The paperwork is sent as a standard electronic document (a **C-CDA**) placed inside a standard electronic envelope (an **X12 275**).
- The document is **signed electronically**, like a wax seal, so the insurer can trust who sent it and that it was not changed.
- Perfuse, a free program, can build these signed attachments, read them, and check the signatures, and its signatures have been verified by independent tools.

## What this guide is about

This guide is for someone who runs a clinic, a laboratory or a billing office and does not write computer software. No technical background is needed. By the end you will understand what a claim attachment is, why it is moving onto computers, what an electronic signature really does, and what a tool like Perfuse does for you.

We will keep the language plain, spell out every short-form name the first time it appears, and use a made-up example to keep things grounded.

> In plain words: this guide is about sending the supporting paperwork for a medical bill by computer, with an electronic signature so the insurer can trust it.

## Why attachments exist

When a healthcare provider treats a patient, it sends a bill to the patient's insurer (the "payer"). This bill is called a **claim**. Often the payer cannot decide whether to pay the claim on the bill alone. It needs to see the clinical story behind it: the operative note from the surgery, the discharge summary from the hospital stay, the laboratory results that justify a test.

That supporting paperwork is a **claim attachment**. The same kind of paperwork is also needed for **prior authorization**, which is when a payer must approve a treatment before it happens.

For a long time attachments travelled by fax and post. That is slow, it is easy to lose, and the payer has no reliable way to be sure the document really came from the clinic and was not altered along the way. Moving attachments onto computers, with signatures, fixes all of that.

## What CMS-0053 is

**CMS** stands for the **Centers for Medicare & Medicaid Services**, the large US government agency that runs Medicare and Medicaid and sets many of the rules that insurers follow. "CMS-0053" is simply the reference number of a particular piece of rule-making from that agency, the one that moves health care attachments onto electronic standards and requires electronic signatures. When people say "CMS-0053" they mean that rule.

## Who this affects

- **Clinics, hospitals and laboratories**, which send the supporting paperwork for their claims.
- **Billing offices and clearinghouses**, which handle claims on providers' behalf.
- **Payers (insurers)**, which receive the attachments and must be able to trust them.

If your organisation sends bills to insurers, or decides whether to pay them, this affects you.

## The three standards involved

Three agreed formats make an electronic attachment work. You do not need to know their inner workings, only what each one is for.

### The envelope: X12 275

**X12** is a long-established family of standards for electronic business documents, widely used in healthcare for bills, payments and the like. The particular X12 document that carries an attachment is numbered **275**, and its official name is "Additional Information to Support a Health Care Claim or Encounter". Think of the X12 275 as the **envelope** that carries the paperwork to the payer. CMS-0053 adopts a specific edition of it, labelled **006020**.

### The document: C-CDA

Inside the envelope goes the clinical document itself. The usual format is a **C-CDA**, which stands for **Consolidated Clinical Document Architecture**. That is a mouthful; in plain terms it is a standard way of writing up a clinical document, such as a discharge summary or an operative note, so that any system can read it. The C-CDA is the **letter** inside the envelope.

### The seal: an electronic signature

Finally, the document is **signed electronically**. The rules for how to sign come from a guide published by HL7 (Health Level Seven, the body behind many healthcare standards) called **DSDR**, short for **Digital Signatures and Delegation of Rights**. The signature lets the payer check who signed the document, in what role, and for what purpose. It is the **wax seal** that proves the letter is genuine and unaltered.

> In plain words: the C-CDA is the letter, the X12 275 is the envelope, and the electronic signature is the wax seal on the letter.

![How a signed claim attachment reaches the payer](docs/assets/articles/claims-attachments-journey.svg)

## What a strong signature looks like

An ordinary scribble on paper is only as good as the moment it is made. An electronic signature can be much stronger, and it needs to be, because a payer may check it years after it was signed, long after the people and the digital certificates involved have moved on.

The strength of an electronic signature is described in **levels**, built up like layers. The levels are named after a signing standard called **XAdES** (you can say it "ex-ah-dess"; it stands for XML Advanced Electronic Signatures). You do not need the detail, only the idea that each layer adds more lasting proof:

- The **first layer** is the signature itself, plus a note of the rules it was signed under.
- The **second layer** adds a trusted **time-stamp**, which is independent proof of exactly when the document was signed. This uses a standard called RFC 3161, which is just the agreed recipe for trustworthy time-stamps.
- The **third layer** adds the signing certificates and a record of whether they were valid at the time, using a check called **OCSP** (Online Certificate Status Protocol, the standard way to ask "is this certificate still good?"). With this layer, the signature can still be verified long after the certificates themselves expire.

The strongest of these levels is written **XAdES-X-L**. That is the level a long-lasting, verifiable signature reaches, and it is the level Perfuse produces.

![How a long-lasting electronic signature is built up](docs/assets/articles/claims-attachments-signature-levels.svg)

> In plain words: the more layers a signature has, the longer it can be trusted. The top layer means the payer can still check it years later.

## A worked example

Imagine a surgeon at a clinic performs an operation on a patient, Casey Example, and the clinic sends a claim to Casey's insurer. The insurer asks to see the operative note before it pays.

1. The clinic's system turns the operative note into a standard clinical document (a C-CDA).
2. The document is signed electronically. The signature records that the surgeon is signing, in the role of the clinician who authenticated the note, and it is given a trusted time-stamp and the certificate details, reaching the strongest level, XAdES-X-L.
3. The signed document is placed inside the electronic envelope (an X12 275, in the 006020 edition the rule adopts).
4. The envelope is sent to the insurer.
5. The insurer opens the envelope and checks the signature against its list of trusted signers. The signature holds, so the insurer can trust that the note truly came from the clinic and was not changed, and it processes the claim.

No fax, no lost paperwork, and no doubt about where the document came from.

## Common questions

**Is an electronic signature just a picture of a handwritten signature?**
No. It is a mathematical seal over the whole document. If even one character of the document changes, the seal no longer matches, and the payer can tell.

**Why does the time-stamp matter?**
Because it is independent proof of when the document was signed, from a trusted source rather than from the clinic's own clock. That matters when a signature is examined long after the fact.

**What does "role" and "purpose" mean on a signature?**
The signature can record who the signer is in relation to the document, for example whether they are the person legally responsible for it or simply the person who authenticated it, and why they are signing. This follows the DSDR guide's agreed lists.

**Is this the same as the claim itself?**
No. The claim is the bill. The attachment is the supporting paperwork. They are separate documents that travel together in the overall exchange.

**How do I know the signatures Perfuse makes are really valid?**
Because they have been checked by independent tools that did not create them, including widely used open-source signature-checking software. A signature that only the program that made it can verify would prove nothing.

## A glossary of every short-form name used here

- **CMS** — Centers for Medicare & Medicaid Services, the US agency that sets many payer rules.
- **CMS-0053** — the CMS rule that moves claim attachments onto electronic standards with electronic signatures.
- **CMS-0057** — a related, separate CMS rule about payer data-sharing; mentioned only to note it is not the same as CMS-0053.
- **Claim** — the bill a provider sends to a payer.
- **Payer** — the insurer that receives and decides on the claim.
- **Attachment** — the supporting clinical paperwork sent with or for a claim.
- **Prior authorization** — a payer's approval of a treatment before it happens, which also needs supporting paperwork.
- **X12** — a family of standards for electronic business documents, widely used in healthcare.
- **X12 275** — the specific X12 document that carries an attachment; the "envelope".
- **006020** — the edition of the X12 275 that CMS-0053 adopts.
- **C-CDA** — Consolidated Clinical Document Architecture; a standard way of writing a clinical document; the "letter".
- **HL7** — Health Level Seven, the body behind many healthcare standards.
- **DSDR** — Digital Signatures and Delegation of Rights; HL7's guide for how to sign these documents.
- **XAdES** — a standard for advanced electronic signatures; its "levels" describe signature strength.
- **XAdES-X-L** — the strongest signature level described here; the one Perfuse produces.
- **Time-stamp (RFC 3161)** — trusted, independent proof of when a document was signed.
- **OCSP** — Online Certificate Status Protocol; the standard check for whether a certificate was valid.
- **MDM** — a kind of HL7 v2 message that can carry a document; mentioned because C-CDAs sometimes arrive inside one.

## How Perfuse does it

![A C-CDA signed with XAdES-X-L, placed in an X12 275 and checked by the payer](docs/assets/articles/attachments-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, open-source program for moving medical data between systems. "Open-source" means the program is published for anyone to read and use, at no cost. Building and reading signed claim attachments is built in.

In plain terms, Perfuse can:

- Build and read the electronic envelope (an X12 275 in the 006020 edition that CMS-0053 adopts).
- Sign the clinical document (the C-CDA) to the HL7 DSDR guide, all the way up to the strongest level, XAdES-X-L, with the trusted time-stamp and the certificate-status check included.
- Record who signed, in what role and for what purpose, following the agreed lists, including whether they sign as the person legally responsible for the document or as the one who authenticated it.
- Check every signature on documents it receives against the trusted signers you give it, and report which level each signature reached and anything that is missing.
- Do all of its signing in a way that **independent tools have verified**: widely used open-source signature-checking software, which did not create the signatures, confirms they are valid, and refuses them when a document has been altered.
- Handle clinical documents more broadly too: reading, checking and converting them, including documents that arrive tucked inside an HL7 v2 message, and even checking that a document's readable text agrees with its coded data.

> In plain words: Perfuse builds the signed, sealed attachment for you, checks the ones that come in, and its seals have been proven genuine by outside tools.

These attachments are one part of a larger set of billing and payer tools in the same program, so the system that already handles your claims and remittances can also handle the signed paperwork. There is one file to copy and nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest). It is a single file.
2. Start it with your signing key and a trusted time-stamp source, and it is ready to sign attachments.
3. Read the [manual](https://perfuse.health/manual/) for the attachment options, in the same plain style as this guide.

Related reading: [X12 278 and FHIR PAS](https://perfuse.health/x12-278-vs-pas/) · [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
