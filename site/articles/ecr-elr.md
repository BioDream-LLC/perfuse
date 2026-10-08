# eCR and ELR explained: automatic public health reporting from clinical data

![A public health cross with eICR, RR and ELR labels](docs/assets/articles/hero-ecr.svg)

By **BioDream Developer**, BioDream LLC

Every US state requires providers and labs to report certain conditions to public health: measles, tuberculosis, hepatitis, COVID-19 and dozens more. For decades that meant faxes and phone calls. Today it is done electronically, through two standards: **electronic case reporting (eCR)** and **electronic laboratory reporting (ELR)**.

This guide explains both, how they work, and how to automate them.

## Electronic case reporting (eCR)

eCR sends a case report automatically when a patient's record contains a sign of a reportable condition.

- **Trigger codes.** The **Reportable Conditions Trigger Codes (RCTC)** are a published list of diagnosis, lab and result codes that may indicate a reportable condition. When a code from the list appears in an encounter, a report is due.
- **The eICR.** The **electronic Initial Case Report** carries the patient, the encounter, the problems, results and medications that triggered it. It is defined by HL7 in CDA and in FHIR (the **eCR FHIR implementation guide**).
- **The Reportability Response (RR).** Public health decision support decides whether the case is reportable, and to which jurisdiction, and sends back a **Reportability Response** that the provider stores with the record.

eCR also supports the **Public Health and Clinical Data Exchange** objective in CMS's Promoting Interoperability programs.

## Electronic laboratory reporting (ELR)

ELR sends reportable lab results from a laboratory to public health. The standard is the **HL7 version 2.5.1 ELR implementation guide**: an **ORU^R01** message carrying the patient, the ordering provider and facility, the specimen and the results, with LOINC and SNOMED CT codes.

The hard part is usually not sending the message but shaping it: a lab's everyday ORU carries every test ordered, and public health wants only the reportable ones, in the agreed structure, with nothing required missing.

## How Perfuse does it

![Clinical data flowing through Perfuse into eICR case reports and ELR lab reports](docs/assets/articles/ecr-elr-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with both kinds of public health reporting built in.

**eCR**

- **A FHIR destination that sends an HL7 eCR 2.1.2 eICR** for each message carrying a reportable-condition trigger code, and nothing for the rest.
- **Triggers from the RCTC you load**, or a built-in sample for testing. A report goes because a code matched, not because somebody remembered.
- **Reportability Responses are received** at `$process-message` and stored where the original report said to look.
- **A built-in test agency** answers eICRs, so you can try the whole round trip before connecting to a real jurisdiction: `perfuse serve -ecr-agency agency.yaml` on one side, `-ecr-responses` on the other.
- **`perfuse fhir eicr`** builds case reports from HL7 v2 messages at the command line.
- Validated against the eCR guide with the official HL7 validator. The Reportability Response validates with 0 errors.

**ELR**

- **`perfuse elr`** reshapes a lab's ORU^R01 into an **HL7 2.5.1 ELR** message carrying only the reportable orders, and reports what the lab left out.
- Trigger codes come from a FHIR ValueSet or Bundle, such as the eRSD, or the built-in sample.
- Checked with **NIST's HL7 v2 validator**.

```sh
perfuse elr -config examples/elr/elr.yaml -out elr/ lab-results/
```

And because Perfuse is a complete integration engine, the same server that receives your ADT and ORU feeds also does the reporting: HL7 v2, FHIR, X12, DICOM and CDA over twenty connector types, with a durable queue, monitoring and alerts, in one file with nothing else to install.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Try the ELR example in `examples/elr/`, or start a test agency and send it an eICR.
3. Read the [manual](https://perfuse.health/manual/) for every option.

Related: [HL7 v2 to FHIR: a practical guide](https://perfuse.health/hl7-v2-to-fhir/).
