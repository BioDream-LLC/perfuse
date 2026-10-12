# HL7 v2 to FHIR: a practical guide to converting real feeds

![HL7 v2 segments flowing into FHIR resources](docs/assets/articles/hero-v2-fhir.svg)

## In one minute

- Hospitals send most of their data in an old-but-reliable format called **HL7 v2**. Newer apps and the government's rules expect a modern format called **FHIR**.
- Converting one into the other is one of the most common jobs in hospital computing — and one of the easiest to get quietly wrong.
- The danger is not loud errors. It is small, silent mistakes (a time with no time zone, a result marked "final" when it was not) that make the data on a screen wrong while everything still looks fine.
- This guide explains both formats from zero, walks through a real example line by line, and lists the traps.
- At the end it shows how **Perfuse**, a free program, does the conversion and tells you about every gap instead of hiding it.

## What this is

Think of a hospital as a city full of buildings that were each built in a different decade. The lab has its own computer system. The pharmacy has another. The emergency department, the billing office, the imaging department — each has software bought at a different time from a different company. They all need to share information about the same patients, but none of them speaks the others' language.

For nearly forty years, the shared language has been **HL7 v2**. HL7 stands for **Health Level Seven**, the name of the organisation that publishes the standard; "Level Seven" is a nod to the top layer of a technical networking model, the layer closest to people. The "v2" simply means version 2. It is a compact, text-based way of writing down events: a patient was admitted, a lab result is ready, an appointment was booked.

HL7 v2 is everywhere, it works, and it is not going away soon. But it was designed in the 1980s, and it shows. It is terse and hard for modern web and phone apps to use.

So the industry built a newer language: **FHIR**, pronounced "fire". FHIR stands for **Fast Healthcare Interoperability Resources**. "Interoperability" is just a long word for *different systems working together*. FHIR describes the same medical facts as HL7 v2, but in a tidy, modern form that websites, phone apps and cloud services can read easily. When the US government writes rules requiring hospitals and insurers to share data through apps, those rules are written around FHIR.

That leaves a gap. The data is produced in the old language, HL7 v2, but it has to be delivered in the new one, FHIR. **Converting between them is what this guide is about.**

## Why it exists — and why it is harder than it sounds

A fair question: if both formats describe the same facts, why is converting between them a job at all? Can't you just translate word for word?

Not quite. The two formats organise the world differently, label things differently, and disagree about what counts as "known". A good conversion is less like swapping one word for another and more like a careful human translator who understands both cultures. Translate too literally and you produce sentences that are technically words but mean something false.

And here is the part that makes it genuinely risky: **a bad conversion usually produces no error at all.** The output still looks like perfectly good FHIR. No alarm sounds. The mistake only shows up when a doctor, a nurse or a billing clerk reads something that is subtly wrong — a lab value that lost its "less than" sign, a test marked final before the lab had confirmed it. By then, nobody connects it back to the conversion.

## Who it affects

- **Hospital integration teams**, the people who wire the hospital's systems together, do this work daily.
- **App makers** who want to build something useful on top of a hospital's existing data.
- **Labs, imaging centres and insurers** who send or receive clinical data and are increasingly asked to speak FHIR.
- **Patients**, in the end: when a conversion is right, the diabetes app on your phone shows the same blood sugar reading the lab actually measured.

## A worked example: a lab result at 2 a.m.

Picture a real moment. It is 2 a.m. A patient's blood sample has just been analysed by a machine in the laboratory. The machine's computer writes down the result as an HL7 v2 message and sends it off so the rest of the hospital can see it.

![The journey of one lab result from the laboratory machine to the doctor's screen](docs/assets/articles/hl7-v2-to-fhir-journey.svg)

Here is a shortened version of what the lab's message looks like. Do not worry about reading it fluently — we will go through it line by line.

```
MSH|^~\&|LAB|HOSPITAL|EHR|HOSPITAL|20260112020500-0500||ORU^R01|MSG001|P|2.5.1
PID|1||MRN12345^^^HOSPITAL||SAMPLESON^BRAVO||19800101|M
OBR|1||ACC9988|CBC^Complete Blood Count
OBX|1|NM|WBC^White Blood Cell Count||3.2|10*9/L|4.0-11.0|L|||F
```

Reading it in plain words:

- **`MSH`** is the **Message Header** — the envelope. It says who sent the message (the `LAB` at the `HOSPITAL`), who it is for (the `EHR`, the electronic health record), when it was sent (`20260112020500-0500`, which is 2:05 a.m. on 12 January 2026, five hours behind London time), and what kind of message it is (`ORU^R01`, the code for a result). The `MSH` line also sets the punctuation marks the rest of the message uses: the `|` separates fields, the `^` separates smaller pieces inside a field.
- **`PID`** is the **Patient Identification** segment. It carries the patient's medical record number (`MRN12345`), their name (`SAMPLESON^BRAVO`, i.e. family name Sampleson, given name Bravo), date of birth and sex.
- **`OBR`** is the **Observation Request** — which test this is. Here, a Complete Blood Count, with an accession number `ACC9988` the lab uses to track the sample.
- **`OBX`** is the **Observation** — one actual result. The white blood cell count is `3.2`, measured in a unit written `10*9/L`, the normal range is `4.0–11.0`, the flag `L` means the value is **low**, and `F` means the result is **final**.

> In plain words: a "segment" is just one line of the message, and its three-letter code tells you what the line is about. PID is the patient, OBX is a single result. Once you know the codes, the message reads like a form with the labels abbreviated.

Now the goal: deliver this to a modern app in FHIR. In FHIR, the same result is split into tidy objects called **resources**. Each resource is one kind of thing:

- a **Patient** resource for the person,
- a **DiagnosticReport** resource for the overall blood count,
- an **Observation** resource for the single white-blood-cell result.

A good converter reads the four lines above and builds those three resources, carrying every fact across without changing its meaning. That sounds simple. The next section is about everything that can go wrong in the gap.

## What maps to what

Before the traps, the big picture. HL7 v2 sends different kinds of message for different events, and each becomes a predictable set of FHIR resources. The official **HL7 v2-to-FHIR implementation guide** — an implementation guide is just an agreed rulebook for how to do the conversion — lays out these mappings so everyone does it the same way.

![HL7 v2 segments PID, PV1, OBR, OBX, AL1 and DG1 mapped to the FHIR resources Patient, Encounter, DiagnosticReport, Observation, AllergyIntolerance and Condition](docs/assets/articles/v2-fhir-mapping.svg)

| HL7 v2 message | The event it describes | FHIR resources it becomes |
|---|---|---|
| ADT | a patient is admitted, moved, discharged or updated | Patient, Encounter, plus allergies, diagnoses and coverage |
| ORU^R01 | lab and test results | DiagnosticReport, Observation, Specimen |
| ORM / OML | an order for a test or service | ServiceRequest |
| SIU | an appointment is scheduled | Appointment |
| MDM | a document or clinical note | DocumentReference |
| VXU | an immunisation (a vaccination) | Immunization |

At the single-line level, the pattern is steady: a **PID** line becomes a **Patient**, a **PV1** line (patient visit) becomes an **Encounter**, an **OBR** becomes a **DiagnosticReport**, an **OBX** becomes an **Observation**, an **AL1** (allergy) becomes an **AllergyIntolerance**, and a **DG1** (diagnosis) becomes a **Condition**.

## The details that make or break a conversion

These are the quiet traps. None of them causes an error message. Each one can put wrong information in front of a clinician.

![Five quiet mistakes in a conversion, each shown as a wrong example beside the right one](docs/assets/articles/hl7-v2-to-fhir-pitfalls.svg)

**Times and time zones.** HL7 v2 often records a time with no time zone — just "2:05 a.m." with no clue whether that is New York, London or Tokyo. FHIR, sensibly, insists that if you give a time, you say which zone. So the converter has to supply the missing zone, and the honest source is the sender's own zone, which is written in the message header (the `MSH` line). Get this wrong and a result timed at 2 a.m. local can shift by hours — enough to change which day it appears to belong to. If the original only gave a date with no time, it should stay a date, not gain a made-up midnight.

**Identifiers and who issued them.** A medical record number like `MRN12345` means nothing on its own. Two different hospitals can both have an `MRN12345`, belonging to two different people. So every identifier needs to carry **the name of the system that issued it** — like saying "passport number 12345, *issued by France*" rather than just "number 12345". In FHIR this is called the identifier's `system`. Get it right and the same patient arriving from two feeds is recognised as one person, and a corrected message updates the existing record instead of creating a confusing duplicate.

**Codes and units.** HL7 v2 uses lots of short codes — `M` for male, `F` for a final result, `L` for low. FHIR has its own agreed codes for the same ideas, and the converter must translate each one, not copy it blindly. Units need the same care. There is an international standard for units of measure called **UCUM** (the Unified Code for Units of Measure). A converter should label a unit as UCUM only when it genuinely is one, rather than claiming a messy local spelling is a standard code.

**Comparison values.** A lab result is sometimes not a plain number. "Less than 5" is a real, meaningful result. If a converter throws away the "less than" and stores just `5`, it has changed the medicine. FHIR has a proper place to keep the "less than" sign beside the number, and a good conversion uses it.

**Missing information.** When an HL7 v2 field is empty but FHIR wants a value, the tempting shortcut is to invent a default. That is how a patient with no recorded sex quietly becomes "male" everywhere downstream. The honest answer is to mark the value as **"unknown"** or **"data absent"** — to say "we don't know" out loud rather than guess.

**Status.** An empty result status is **not** the same as "final". A final result has been checked; a blank one has not. And a result that was deleted is "entered in error", not "cancelled" — the two mean different things to the person reading the chart.

**Profiles.** In the United States, receivers usually expect the data to follow **US Core**, a national rulebook that says, for example, exactly how a Patient resource must be filled in. A conversion should claim it meets US Core only when it actually does. Claiming conformance you haven't earned is how a receiving system accepts data it will later choke on.

> In plain words: the whole art of this conversion is refusing to guess. Where the source is clear, carry it across faithfully. Where the source is silent, say so — never fill the silence with an invention.

## Common questions

**Is HL7 v2 being replaced by FHIR?** Not any time soon. There is far too much working HL7 v2 in hospitals to switch it off. For years to come, the realistic picture is both at once, with conversion in the middle.

**Why not just make the old systems speak FHIR directly?** Because replacing hospital software is enormously expensive and risky, and the old systems work. Converting their output is far cheaper and safer than rebuilding them.

**If the conversion is wrong, won't someone notice straight away?** That is the trap — usually not. The output looks valid, so computers accept it. Only a human reading something odd eventually catches it, often weeks later. That is exactly why a converter should report every gap it hit, so problems surface immediately instead of silently.

**Who checks the result is correct?** A trustworthy converter does two things: it validates its own output against the official rules, and it tells you where the source message left a gap. Being honest about gaps matters more than looking tidy.

## Glossary

- **HL7** — Health Level Seven, the organisation and family of standards for sharing health data.
- **HL7 v2** — the older, compact, text-based messaging standard most hospitals still use.
- **FHIR** — Fast Healthcare Interoperability Resources, the modern standard that apps and government rules expect. Pronounced "fire".
- **Interoperability** — different computer systems being able to work together.
- **Segment** — one line of an HL7 v2 message, named by a three-letter code (PID, OBX, and so on).
- **Resource** — one tidy object in FHIR, such as a Patient or an Observation.
- **ADT, ORU, SIU, MDM, VXU** — HL7 v2 message types for admissions, results, scheduling, documents and immunisations.
- **MSH, PID, PV1, OBR, OBX, AL1, DG1** — common HL7 v2 segments: the header, the patient, the visit, the order, a result, an allergy, a diagnosis.
- **US Core** — the US national rulebook for how FHIR resources must be filled in.
- **UCUM** — Unified Code for Units of Measure, the international standard for writing units like millimetres or litres.
- **Validator** — a tool that checks whether data follows the rules of a standard.
- **MLLP** — Minimal Lower Layer Protocol, the usual way HL7 v2 messages travel over a network connection.

## How Perfuse does it

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with a full HL7 v2 to FHIR converter built in. It was designed around exactly the traps above: do the honest thing, and never hide a gap.

- **The mappings done properly**: an admission (ADT) becomes a Patient and an Encounter; a result (ORU) becomes a DiagnosticReport and Observations; an appointment (SIU) becomes an Appointment; a note (MDM) becomes a DocumentReference with the note attached; an immunisation (VXU) becomes an Immunization — with the date and code conversions handled, not skipped.
- **Modern FHIR versions**: it produces R4, R4B and R5, the three current editions of FHIR.
- **US Core 9.0.0**, the version approved for the latest US data requirements. Perfuse claims a resource meets US Core only after checking it against the rules, and its output passes the official HL7 validator with no errors.
- **Time zones done right**: a v2 time with no zone takes the sender's zone from the message header, or one you set.
- **Identifiers with real systems**: each issuing authority is mapped to its proper system name, so the same patient is recognised across feeds.
- **A mapping report for every message**, saying what was converted, how, and where the source left a gap — so nothing happens silently. This is the honesty the whole job depends on.
- **Verified against a real HAPI FHIR server** (a widely used, independent FHIR system), which once caught Perfuse inventing an identifier system it should not have. Checking against real software, not against its own assumptions, is a core habit of the project.

You can use it whichever way fits:

```sh
# Convert a folder of messages from the command line, with a report
perfuse fhir convert -us-core -notes -out bundles/ messages/

# Check the FHIR it produced against the rules
perfuse fhir validate bundles/*.json
```

- **As a live channel**: an HL7 v2 feed comes in, and a FHIR destination goes out, so an existing interface delivers straight to any FHIR app.
- **As a FHIR server**: Perfuse can also store the result and let apps search it, with transactions, notifications and bulk export.
- **In the browser**: the built-in **FHIR lab** lets you paste an HL7 v2 message and see, side by side, the FHIR it becomes — already validated.
- **For public health**: the same conversion builds electronic case reports from v2 messages, used to notify health agencies about reportable conditions.

And it is one piece of a complete engine — HL7 v2, FHIR, X12, DICOM and CDA, with a durable queue, monitoring and alerts, all in a single file with nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows.
2. Run `perfuse serve` and open http://127.0.0.1:8080 in your browser.
3. Open the **FHIR lab**, paste an HL7 v2 message, and watch it become FHIR, line by line.

Related: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [How to migrate Mirth Connect channels](https://perfuse.health/migrate-mirth-channels/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
