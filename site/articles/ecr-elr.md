# eCR and ELR explained: automatic public health reporting from clinical data

![A public health cross with eICR, RR and ELR labels](docs/assets/articles/hero-ecr.svg)

## In one minute

- When certain diseases are found, the law says a clinic or laboratory must tell the public health department. For decades that meant faxes and phone calls.
- **eCR** (electronic case reporting) sends a report automatically when a patient's record shows a sign of one of those diseases.
- **ELR** (electronic laboratory reporting) sends reportable test results straight from the laboratory to public health.
- Both are now done by computer, using agreed message formats, so a report goes out because the right code appeared, not because a busy person remembered.
- Perfuse, a free program, can do both for you, and it checks its own work against the official rule-checkers.

## What this guide is about

This guide is written for someone who runs a laboratory, an imaging centre or a hospital department and has no background in computer programming. You do not need to know how any of this is built. By the end you will understand what public health reporting is, why it has moved onto computers, the two main ways it happens, and what a tool like Perfuse does for you.

We will keep the language plain, explain every short-form name the first time it appears, and use a running made-up example so the ideas stay concrete.

> In plain words: public health reporting means telling the health department when you find a disease they are keeping track of. This guide explains how that happens by computer now.

## Why reporting exists at all

Public health departments watch for outbreaks. If ten people in one town catch measles, somebody needs to notice the pattern quickly, find out where it started, and stop it spreading. They can only do that if they are told, promptly, each time a case turns up.

So every US state has a list of **reportable conditions**: diseases and findings that a clinic or laboratory is legally required to report. Measles is on it. So are tuberculosis, hepatitis, many sexually transmitted infections, certain food-borne illnesses and dozens more. The exact list is set by each state.

For a long time the reporting itself was slow and manual. A nurse or a laboratory clerk would fill in a form, print it, and fax it to the health department, or read it down the phone. That is error-prone, it is slow, and it depends on a person remembering to do it in the middle of a busy day. Electronic reporting was created to fix all three problems.

## Who this affects

- **Hospitals and clinics**, which send case reports when a patient is found to have a reportable condition.
- **Laboratories**, which send reportable test results.
- **Public health departments**, which receive the reports and act on them.
- Indirectly, **patients and the public**, because faster reporting means faster response to outbreaks.

If your organisation sees patients or runs tests, some form of reporting almost certainly applies to you.

## The first way: electronic case reporting (eCR)

**eCR** stands for electronic case reporting. The idea is simple: instead of a person deciding to send a report, the computer watches the patient record and sends one automatically the moment a reportable sign appears.

To understand it, you need three plain-English pieces.

### Trigger codes

Everything in a modern medical record is tagged with codes: a code for the diagnosis, a code for the lab test, a code for the result. There is a published national list of codes that may mean a reportable condition. It is called the **RCTC**, which stands for **Reportable Conditions Trigger Codes**. Think of it as a watch-list of codes.

When a code from that watch-list shows up in a patient's visit, a report is due. Nobody has to judge it in the moment; the matching code is the trigger.

### The eICR

The report itself is called the **eICR**, short for **electronic Initial Case Report**. It is a tidy electronic document that gathers up the parts of the visit that matter to public health: who the patient is, when and where they were seen, the problem that triggered the report, the relevant test results, and the medicines involved.

### The Reportability Response (RR)

After the report is sent, public health does not just stay silent. Their systems look at the report and decide whether this case really is reportable, and to which local jurisdiction. They send back a short answer called the **Reportability Response**, usually shortened to **RR**. The clinic stores that answer alongside the patient's record, so there is proof of what was reported and what public health said about it.

> In plain words: the clinic sends a case report (the eICR), and public health sends back a receipt-and-decision (the RR). The clinic keeps both.

### A worked example

Let us make it concrete. Imagine a clinic sees a child, Robin Example, who turns out to have measles.

1. The doctor records the diagnosis. The diagnosis carries a code that is on the national watch-list (the RCTC).
2. The computer notices the match. A case report (the eICR) is built automatically: Robin's details, the measles diagnosis, the relevant results.
3. The report is sent to public health.
4. Public health's system decides measles is reportable in Robin's county, and that it needs attention quickly. It sends back a Reportability Response saying so.
5. The clinic stores that response with Robin's record.

No one had to remember to fax anything. The report went because the measles code matched.

![The electronic case reporting journey, step by step](docs/assets/articles/ecr-elr-journey.svg)

## The second way: electronic laboratory reporting (ELR)

**ELR** stands for electronic laboratory reporting. This is the laboratory's side of the same job. When a laboratory produces a result for a reportable condition, it sends that result straight to public health.

The message format is an older, very widely used standard called **HL7 version 2.5.1**. HL7 stands for **Health Level Seven**, the organisation that publishes many of healthcare's messaging standards; "version 2.5.1" is just a particular edition of one of them. The message carries the patient, the ordering doctor and facility, the specimen (the sample that was tested), and the result, each tagged with standard codes.

### The hard part of ELR

Sending a message is not the difficult bit. The difficult bit is **shaping** it.

A laboratory's everyday result message usually carries every test that was ordered together. If a doctor ordered a panel of ten tests, the laboratory's message has all ten. But public health only wants the one result that is reportable, laid out in the exact structure they expect, with nothing required left out.

So something in the middle has to take the full laboratory message, pick out only the reportable results, and rebuild a clean message that contains just those. And if the laboratory's message is missing something public health requires, that should be pointed out rather than quietly ignored.

### A worked example

Suppose the same clinic's laboratory runs a blood panel for another patient, Alex Sample. The panel includes a cholesterol test, a blood count, a vitamin D level, and a measles test. The measles test comes back positive.

- The laboratory's everyday message contains all four results.
- Only the measles result is reportable.
- The reporting step keeps the measles result, drops the other three, and builds a clean ELR message with just the reportable result, the patient, the ordering provider, the specimen and the performing laboratory.
- If the laboratory did not include, say, the specimen type, the step notes that gap rather than hiding it.

Public health receives the measles result, not the cholesterol.

![Reshaping a lab result into a public health report](docs/assets/articles/ecr-elr-reshape.svg)

## How eCR and ELR differ

They are two halves of the same purpose, from two different starting points.

| | eCR (case reporting) | ELR (lab reporting) |
|---|---|---|
| Who sends it | A clinic or hospital | A laboratory |
| What starts it | A reportable code in the patient's visit | A reportable test result |
| What is sent | A case report (the eICR) | A reshaped lab result message |
| Is there an answer back | Yes, the Reportability Response | Usually a simple acknowledgement |
| Main format | A clinical document, modern style | HL7 version 2.5.1, older style |

In practice a hospital may do both: eCR from its clinical records, and ELR from its laboratory.

## Common questions

**Do I have to choose between eCR and ELR?**
No. They do different jobs. A hospital with its own laboratory often needs both. A standalone laboratory may need only ELR.

**Who decides what is reportable?**
Each state sets its own list of reportable conditions. The national trigger-code list (the RCTC) is a shared starting point that the codes in a record are matched against.

**What if a report is sent but the condition turns out not to be reportable here?**
That is exactly what the Reportability Response is for. Public health looks at the report and tells the clinic whether it is reportable and where. The clinic keeps that answer.

**Is this the same as billing or claims?**
No. This is reporting to public health, not to an insurer. It is a separate obligation with separate rules.

**Does automatic reporting mean mistakes get sent faster?**
The point of using codes as the trigger is consistency: the same situation produces the same report every time. A built-in rule-checker (described below) also catches malformed reports before they go out.

## A glossary of every short-form name used here

- **eCR** — electronic case reporting. A clinic reporting a case to public health, automatically.
- **ELR** — electronic laboratory reporting. A laboratory reporting a result to public health.
- **eICR** — electronic Initial Case Report. The actual case-report document that eCR sends.
- **RR** — Reportability Response. Public health's answer to an eICR.
- **RCTC** — Reportable Conditions Trigger Codes. The national watch-list of codes that may mean a reportable condition.
- **HL7** — Health Level Seven, the body that publishes many healthcare messaging standards.
- **HL7 version 2.5.1** — a specific edition of the older HL7 message format, used for ELR.
- **ORU** — the kind of HL7 version 2 message a laboratory uses to send results. (You will see it named below.)
- **LOINC** and **SNOMED CT** — two standard code systems for naming tests and findings, so everyone means the same thing.
- **FHIR** — a newer healthcare data standard (said "fire"); the modern eICR is expressed in it.
- **CDA** — Clinical Document Architecture, an older document standard the eICR can also use.
- **NIST** — the US National Institute of Standards and Technology, which publishes an official message-checker.
- **ONC** — the US office that oversees health-technology certification; it provides an ELR checking tool.

## How Perfuse does it

![Clinical data flowing through Perfuse into eICR case reports and ELR lab reports](docs/assets/articles/ecr-elr-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, open-source program for moving medical data between systems. "Open-source" means the program and its inner workings are published for anyone to read and use, at no cost. Both kinds of public health reporting are built in.

Here is what it does, in plain terms.

**For case reporting (eCR):**

- When a message arrives carrying a code from the watch-list, Perfuse builds and sends a case report (an eICR in the eCR 2.1.2 edition). When no watch-list code is present, it sends nothing. A report goes because a code matched, not because somebody remembered.
- The watch-list of trigger codes is one you load (the RCTC), or a built-in sample you can practise with.
- The answer comes back and is kept: Reportability Responses are received and stored in the place the original report pointed to.
- There is a built-in practice "agency" so you can try the whole round trip, report out and response back, before you ever connect to a real health department.
- Perfuse checks its own case reports against the official rule-checker published by HL7, and the Reportability Response comes through with zero errors.

**For laboratory reporting (ELR):**

- Perfuse takes a laboratory's everyday result message (an HL7 version 2 "ORU" message), keeps only the reportable results, and writes a clean HL7 version 2.5.1 report. It also tells you what the laboratory left out.
- The trigger codes come from a list you provide, or the built-in sample.
- Perfuse checks its reports with NIST's official HL7 message-checker, and ONC's ELR certification tool reports its example messages as valid.

> In plain words: you point Perfuse at your feeds, give it the watch-list of codes, and it writes and sends the right reports, drops the irrelevant results, keeps the answers, and checks its own work.

And because Perfuse is a complete data-handling tool, the same program that already receives your admission and laboratory feeds can also do the reporting. There is one program to run, one file to copy, and nothing else to install underneath it.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest). It is a single file.
2. Try the laboratory-reporting example that ships with it, or start the built-in practice agency and send it a case report to watch the round trip.
3. Read the [manual](https://perfuse.health/manual/) for every option, in the same plain style as this guide.

Related reading: [HL7 v2 to FHIR: a practical guide](https://perfuse.health/hl7-v2-to-fhir/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
