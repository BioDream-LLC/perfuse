# Who ordered, who performed, who signed it off: the people on a lab result, from HL7 v2 to FHIR

![The three roles on a lab result and the v2 fields that carry them](docs/assets/articles/hero-lab-people.svg)

## In one minute

- A lab result involves several people and organisations: the clinician who **ordered** the test, the laboratory and staff who **performed** it, and the pathologist or scientist who **interpreted and signed off** the report.
- HL7 v2 carries each in a different field. FHIR has a different place for each too. Put one in the wrong place and the record says something false, for example that the doctor who asked for a test also produced its result.
- This matters for safety (who to call about a result), for audit, and for the European and national guides, which check these roles.
- We found exactly this mistake in Perfuse, prompted by another implementer's published conversion notes, and fixed it.

## Who this is for

This is for integration analysts mapping lab feeds, and for anyone checking whether their converted results say the right thing about who did what.

> In plain words: an **order** is the request for a test. A **result** is one measured value. A **report** groups the results of one order and is the thing a pathologist signs off.

## The roles, in v2 and in FHIR

| Role | HL7 v2 | FHIR |
|---|---|---|
| Ordered the test | `ORC-12`, or `OBR-16` (ordering provider) | the requester of the order (a ServiceRequest), which the report is based on |
| Produced the result | `OBX-23` (the performing organisation, with its address in `OBX-24`), `OBX-15` (producer's id), `OBX-25` (its medical director), `OBX-16` (responsible observer) | the result's (Observation's) performer |
| Interpreted and signed off the report | `OBR-32` (principal result interpreter) | the report's results interpreter |
| Technicians, transcriptionists | `OBR-34`, `OBR-35` | the report's performers, each with its function |
| When it was observed | `OBX-14`, or the order's `OBR-7` | the result's effective time |

These placements follow HL7's own V2-to-FHIR mappings, which is the reference to hold any converter to.

## The mistake that is easy to make

The ordering provider is the person most messages name most reliably, so it is tempting to put them somewhere visible on the report, such as its performer. But "performer" means who produced the result. A report that names the ordering GP as its performer tells every reader that the GP ran the assay. It also hides the people who did: the laboratory and the pathologist who signed it off, and they are the ones to contact about an unexpected value.

A second trap is `OBR-32`. In the v2 standard it is a composite field (an NDL) whose first part holds the person, with the name split into sub-parts. Many systems instead write it in the simpler shape used for other people fields (an XCN: `id^family^given^...`). A converter that reads only one form reads the other as garbage, with a surname where a date should be. Both have to be read.

And a third: a result without its own time. In v2, `OBX-14` is sent only when a result's time differs from its order's, so an empty `OBX-14` means "the order's time", `OBR-7`. Leaving it empty in FHIR loses information the message did carry.

## How Perfuse does it

Each OBR of a result message becomes both a DiagnosticReport and the ServiceRequest it answers. The report is based on the order, the order's requester is `ORC-12` or `OBR-16`, and its status comes from `ORC-5` or is left `unknown` (a result's `ORC-1` of `RE` says nothing about the order). `OBR-32` becomes the results interpreter, read in either form. `OBR-34` and `OBR-35` become performers with their function recorded. Each result takes `OBX-15`, `OBX-23` to `OBX-25` and `OBX-16` as its performers, and `OBR-7` when it has no `OBX-14`.

How this came about, honestly: until recently Perfuse wrote `OBR-16` as the report's performer and ignored `OBR-32`. Reading another implementer's published notes on converting lab messages for an NHS region, while answering their question on HL7's chat server, made us check, and the mistake was there. The fix was re-checked with the HL7 validator against UK Core, FR Core and HL7 Europe's Laboratory Report; the new orders and practitioners pass, except where a message names an interpreter by number only, which the European base profile refuses.

What it does not do: it does not invent a performer or a time a message does not carry. A result with neither stays without them, and the validator's complaint stays visible.

## Get started

1. Paste one of your lab messages into the FHIR lab in the console, or run `perfuse fhir convert`, and look at who ends up where.
2. Read the [practical guide to HL7 v2 to FHIR](https://perfuse.health/hl7-v2-to-fhir/) and [HL7 v2 and the European reports](https://perfuse.health/hl7-v2-and-european-reports/).

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
