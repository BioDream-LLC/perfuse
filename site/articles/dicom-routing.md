# DICOM routing and de-identification: moving medical images safely

![Concentric rings like a scan, with DICOM service labels](docs/assets/articles/hero-dicom.svg)

## In one minute

- Every medical scan, a CT, an MRI, an X-ray, an ultrasound, travels between machines as a **DICOM** file. DICOM is the worldwide standard for medical images.
- Hospitals move images constantly: from the scanner to storage, to the doctor who reads them, to outside services, to research teams.
- **Routing** means deciding where each study goes, by rules you set.
- **De-identification** means taking the patient's name and details out of an image before it is shared for research or teaching.
- Perfuse, a free program, can route images and remove the patient as part of the same automatic step, and it has been checked against a real image-storage system.

## What this guide is about

This guide is for someone who manages an imaging centre, a radiology department or a laboratory and does not write computer software. You will not need any technical background. By the end you will understand what DICOM is, the handful of ways imaging systems talk to each other, what routing and de-identification mean, why they matter, and what a tool like Perfuse does for you.

We will keep the words plain, explain every short-form name the first time it appears, and use a made-up example to keep things concrete.

> In plain words: this guide is about moving medical pictures from one system to another, safely, and about taking the patient's name off a picture before it leaves for research.

## What DICOM is

**DICOM** stands for **Digital Imaging and Communications in Medicine**. It is the agreed worldwide format for medical images and the information that travels with them. When a CT scanner (a "CT" is a computed tomography scan, the doughnut-shaped machine) finishes a scan, it saves the pictures as DICOM files. When those pictures are sent to a doctor's screen, they travel as DICOM.

A DICOM file is not just a picture. It also carries a lot of written information attached to the image: the patient's name, their identifier, their date of birth, the type of scan, the body part, the date, the machine, and much more. Each piece of information sits in a labelled slot called a **tag**. That mixture, a picture plus patient details in tags, is what makes handling DICOM carefully so important.

A **study** is the usual word for one complete imaging examination, for example "Mr Example's chest CT from Tuesday". A study is made of **series** (groups of related images), and each series is made of individual images. You do not need to track those layers; just know that "study" means one examination.

## Who this affects

- **Imaging centres and radiology departments**, which produce and move studies all day.
- **Hospitals**, which need scans to reach the right reading doctors and the patient's main record.
- **Research teams and AI services**, which need images with the patient removed.
- **Cloud archives**, where older studies are stored cheaply for the long term.

If your organisation makes, reads or stores medical images, this applies to you.

## The pieces that move images

Imaging systems talk to each other with a small set of named services. Each one is a message from one system to another. The systems identify themselves to each other with a short label called an **AE title** (AE stands for **Application Entity**, which is just a fancy name for "a system that speaks DICOM"). Think of an AE title as the name tag a system wears so others know who it is.

There are five services, and they are easier than their names suggest:

- **C-ECHO** — "Are you there?" It is the imaging version of a test phone call, used to check that the other system is reachable. Nothing but a hello is exchanged.
- **C-STORE** — "Here is an image." One system hands a study to another, like handing over an envelope.
- **C-FIND** — "Do you have anything matching this?" A question, for example asking whether a system holds any studies for a given patient.
- **C-MOVE** — "Please send those studies over to that other system." One system asks a second to deliver studies to a third place.
- **C-GET** — "Send them to me, right here." Like C-MOVE, but the images come back over the same connection the request was made on.

![The five DICOM services in plain words](docs/assets/articles/dicom-routing-services.svg)

> In plain words: C-ECHO is hello, C-STORE is "take this picture", C-FIND is "do you have this?", and C-MOVE and C-GET are two ways of saying "send those pictures over".

## What routing means

An imaging **router** is the system in the middle that receives studies and decides where each one should go. The decisions are based on rules you set up once. Common rules include:

- by the **type of scan**: send every CT to the CT reading group, every mammogram to the breast-imaging team;
- by **who sent it** or **who it is addressed to** (the AE titles mentioned above);
- by the **study description, the body part, or the referring doctor**;
- sending a **copy** to an outside service, such as an AI tool or a cloud archive, at the same time as the main copy goes to storage.

Routing also acts as a bridge between imaging and the rest of the medical record. When a scan is finished, the hospital's main record usually needs to know, so that the result appears in the patient's chart. The router can turn "the study is complete" into a message the main record understands.

### A worked example

Imagine an imaging centre scans a patient, Jordan Example, with a chest CT.

1. The CT scanner sends the finished study to Perfuse using C-STORE ("here is an image").
2. Perfuse reads the written tags and sees it is a CT of the chest.
3. A rule says chest CTs go to the chest reading group's storage, so Perfuse forwards it there.
4. Another rule says a copy of every CT also goes to the long-term cloud archive, so a second copy is sent there.
5. Perfuse also sends a short message to the hospital's main record saying the chest CT is done, so it shows in Jordan's chart.

One study arrived; it reached the right readers, the archive and the chart, all from rules set up once.

## What de-identification means

Research projects, teaching files, AI training and outside second opinions often need images **without** the patient attached. Taking the patient out is called **de-identification** (sometimes "anonymisation"). Because a DICOM file carries patient details in its tags, de-identifying it properly means more than just hiding a name on the screen:

- removing or replacing the **patient's name, identifier and date of birth**, and the other identifying tags the DICOM standard lists;
- stripping out **private tags** — extra slots where equipment makers sometimes tuck away information, which can quietly contain identifying details;
- setting **new labels and identifiers** so the study cannot be traced back to the person.

The safest way to do this is to make it part of the routing step itself, so a study is cleaned automatically on its way out. If de-identification is a separate manual chore, sooner or later a study leaves for research with the patient still inside it. Building it into the pipeline removes that risk.

![Removing the patient before an image leaves for research](docs/assets/articles/dicom-routing-deidentify.svg)

### A worked example

Suppose a researcher asks the imaging centre for a hundred chest CTs to train a tool that spots pneumonia. The pictures are useful; the patients' names are not, and must not leave.

- Each study is routed through the de-identification step.
- The patient name, identifier and date of birth are removed.
- The private tags are stripped out.
- A fresh, meaningless label is set in place of the old one.
- The cleaned studies are sent to the research archive.

The pictures arrive for the research; the people in them do not.

> In plain words: de-identification keeps the medical picture and takes the person off it, so images can be shared for research without exposing anyone.

## Common questions

**Does de-identification change the actual picture?**
No. The medical image stays as it is. What changes is the written patient information attached to it.

**Can I keep a copy with the patient still in it?**
Yes. De-identification applies to the copy you send out. Your own storage keeps the full original.

**What if the equipment maker hid patient details in a private tag?**
That is exactly why stripping private tags is part of proper de-identification. Those hidden slots are cleared.

**Can one study go to several places at once?**
Yes. A common setup sends the main copy to storage, a copy to a reading team, and a copy to a cloud archive, all from one incoming study.

**How do I know an image really arrived where it was sent?**
Confirming arrival matters, because a connection can look successful and still deliver nothing. Perfuse's imaging has been checked against a real storage system by reading that system's own catalogue to confirm the image, and its details, truly landed.

## A glossary of every short-form name used here

- **DICOM** — Digital Imaging and Communications in Medicine. The worldwide standard for medical images and their attached information.
- **Study** — one complete imaging examination, such as a chest CT.
- **Series** — a group of related images within a study.
- **Tag** — a labelled slot in a DICOM file holding one piece of information, such as the patient's name.
- **AE title** — Application Entity title. The short name a DICOM system goes by, so others know who it is.
- **C-ECHO** — the "are you there?" check between two systems.
- **C-STORE** — sending an image to another system.
- **C-FIND** — asking another system whether it has matching studies.
- **C-MOVE** — asking a system to send studies to a third place.
- **C-GET** — fetching studies back over the same connection.
- **PACS** — Picture Archiving and Communication System: the main storage-and-viewing system for medical images in a hospital.
- **Modality** — a type of imaging machine (CT, MRI, ultrasound, X-ray). "Routing by modality" means routing by machine type.
- **CT** — computed tomography, a cross-section X-ray scan.
- **MRI** — magnetic resonance imaging, a scan using magnets rather than X-rays.
- **De-identification** — removing patient details from an image before sharing it.
- **HL7 v2** and **FHIR** — two standards for the non-image parts of the medical record, used when imaging reports a result back to the main chart.
- **S3** and **Azure Blob Storage** — two popular cloud storage services for keeping files for the long term.
- **Orthanc** — a well-known free image-storage system, used to independently check that Perfuse's imaging works.

## How Perfuse does it

![Studies flowing from modalities through Perfuse to a PACS, a de-identified research archive and the EHR](docs/assets/articles/dicom-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, open-source program for moving medical data between systems. "Open-source" means the program is published for anyone to read and use, at no cost. Imaging is built in, in both directions, so Perfuse can both receive and send studies.

In plain terms, Perfuse can:

- Speak all five services, C-STORE, C-FIND, C-ECHO, C-MOVE and C-GET, both sending and receiving.
- Ask for studies on a schedule: it can run a C-FIND query by itself and turn each result into something to route.
- De-identify as a routing step: remove the patient, strip the private tags, and set a new label, automatically, so nothing leaves with the patient still inside.
- Read the written tags out of each study so your routing rules can use the study's own details (the machine type, the body part, who sent it).
- Pick a particular image or frame out of a study when a derived picture is needed.
- Turn "a study is done" into a message the main record understands, as either HL7 v2 or FHIR, so the chart knows the scan is complete.
- Send studies to almost anywhere: another storage system over DICOM, plain files, secure file transfer, the Amazon S3 and Microsoft Azure Blob cloud services (including the cheapest long-term tiers), and more.

Crucially, this has been **checked against a real image-storage system called Orthanc**: an image Perfuse sent truly arrived, and the patient name, identifier, study date, machine type and image type all survived the trip. The check reads Orthanc's own catalogue rather than just trusting that no error appeared, because an imaging connection can look successful and store nothing.

> In plain words: Perfuse can receive scans, decide where each one goes, take the patient out when needed, tell the chart the scan is done, and store copies in the cloud, all from rules you set up once.

And imaging runs in the same program as everything else Perfuse handles, with one file to copy and nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest). It is a single file.
2. Start it, open the web page it provides in your browser, and set up a channel that receives images.
3. Read the [manual](https://perfuse.health/manual/) for every imaging option, in the same plain style as this guide.

Related reading: [HL7 v2 to FHIR](https://perfuse.health/hl7-v2-to-fhir/) · [Open-source integration engines compared](https://perfuse.health/integration-engines-compared/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
