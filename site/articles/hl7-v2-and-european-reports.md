# HL7 v2 and the European lab and discharge reports: what a feed can give, and what it cannot

![HL7 v2 messages become FHIR resources; a signed-off document is a separate step](docs/assets/articles/hero-eu-reports.svg)

## In one minute

- HL7 Europe has published FHIR guides for a **Laboratory Report** (2.1.0) and a **Hospital Discharge Report** (1.0.0), the kind of documents the European Health Data Space expects patients and clinicians to exchange across borders.
- Most hospitals and labs in Europe still produce their data as **HL7 v2** messages. The obvious plan is to convert those messages into the new reports.
- Conversion gets you most of the way for the **resources** inside a report: the patient, the stay, the diagnoses, the results. It cannot make the **report itself**, because a report is a document a clinician has validated, and no transformation can sign anything off.
- And a v2 message often lacks things the guides require: who performed a result, when, a contact level, a coded diagnosis type. A converter must not invent them.
- Perfuse converts v2 to FHIR and was checked against both guides and against UK Core, FR Core, ISiK and Nictiz, with every failure explained. Where a hospital knows something the message does not say, it adds it with a short script of its own.

## Who this is for

This guide is for integration teams at European hospitals, labs and regional health networks, and for the people planning their path to the European Health Data Space. No FHIR expertise needed.

> In plain words: **HL7 v2** is the long-established message format most hospital systems send. **FHIR** is the modern format the new European guides use. A **resource** is one piece of a FHIR record, such as one lab result or one diagnosis. A **document** is a set of resources bound together, with a title page and a human-readable narrative, that someone vouches for.

## Two different things: resources and the document

It helps to separate two jobs that are often blurred:

1. **Turning messages into resources.** A lab's result message becomes a set of Observations and a DiagnosticReport; an admission and discharge become an Encounter with its diagnoses and procedures. This is a translation, and software can do it well.
2. **Producing the report.** The European Laboratory Report and Hospital Discharge Report are documents: a Composition that ties the resources together, with a readable narrative, and an author who stands behind it. Clinicians are right to insist that this document is validated by a person. Generating it automatically from a feed would produce something that looks like a signed report and is not one.

So the realistic architecture is: convert the feed to checked resources, and let the laboratory or hospital system that owns the clinical sign-off produce the document. Where a lab already produces its own report as a PDF inside the message, or as a separate document message, that report can travel beside the converted results, unchanged.

## What a v2 feed often does not say

Running real and sample v2 messages through the European guides shows the same pattern in every country: the remaining errors are mostly things the message does not carry.

- **Who performed a result.** Many lab messages do not name a performer for each result, or name only the ordering doctor.
- **When.** A result without its own observation time has to take the order's time, if the order has one.
- **National specifics.** Germany's ISiK wants the hospital's IK number and a contact level for every stay; the Netherlands' nl-core wants to know whether each given name is a first name or an initial. No v2 field says either.

The right response is never to fill these in with a plausible guess. A guessed performer or time is a clinical record that is false. The data has to come from somewhere that knows it, or stay absent and visible.

> In plain words: a converter should translate what was said, and admit what was not.

## What the hospital knows and the message does not

Much of the missing information is not unknown, just not in the message. The hospital knows its own IK number. It knows that a visit in its patient administration system is a department-level contact, that its local patient numbers are medical record numbers, that its system sends first names in one field and initials in another. That knowledge belongs in the hospital's configuration, written once, not inferred per message.

## How Perfuse does it

Perfuse converts HL7 v2 (ADT, ORU, SIU, MDM, VXU and the segments in them) to FHIR following HL7's own V2-to-FHIR mappings, and a FHIR destination can run a short **bundle script** (JavaScript) over each converted bundle to add what the site's own systems know. Two example scripts are published, one for ISiK and one for Nictiz.

The checks, all with the official HL7 FHIR validator, every failure listed with its reason:

| Guide | Resources valid |
|---|---|
| HL7 Europe Hospital Discharge Report 1.0.0 | 68 of 69 |
| HL7 Europe Laboratory Report 2.1.0 | 106 of 426, mostly results that name no performer or time, and reports that are not yet a document |
| UK Core 2.0.2 (NHS Wales' published samples) | 185 of 187 |
| FR Core 2.2.0 (the French agency's samples) | 138 of 156 |
| ISiK 4.0.3 (invented German messages) | 18 of 47; 42 of 47 with the hospital's script |
| Nictiz nl-core (invented Dutch messages) | 12 of 24; 18 of 24 with the hospital's script |

The runs found real faults in Perfuse and fixed them: procedures in discharge messages were not converted at all; the ordering doctor was recorded as the person who produced a result; Dutch surnames sent in parts came out garbled; German procedure codes lacked the category ISiK requires.

What it does not do: it does not write the Composition or its narrative, and it is not certified by any national body. All the messages above were samples or invented; none came from a live hospital system.

## Get started

1. See the [Europe page](https://perfuse.health/europe/) and the full [results and how to repeat them](https://github.com/biodream-llc/perfuse/tree/main/conformance/national).
2. Convert one of your own messages with `perfuse fhir convert`, with `-bundle-script` pointing at your site's script.
3. Read [HL7 v2 to FHIR, a practical guide](https://perfuse.health/hl7-v2-to-fhir/) and [who ordered, performed and signed off a lab result](https://perfuse.health/lab-result-people/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [The example bundle scripts](https://github.com/biodream-llc/perfuse/tree/main/examples)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
