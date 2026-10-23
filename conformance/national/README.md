# National and European FHIR profiles: UK Core, FR Core, ISiK and HL7 Europe Laboratory Report

`run.py` converts real-shaped HL7 v2 messages with `perfuse fhir convert` and checks every resource with the official HL7
validator against the national profile for its type. It is a check of Perfuse's v2-to-FHIR conversion against what each
country's guide requires, not a certification, and not a test of Perfuse's FHIR server.

| Set | Messages | Guide | Resources checked | Pass |
|---|---|---|---|---|
| `uk` | NHS Wales' published v2 examples ([GIG-Cymru-NHS-Wales/hl7-v2-examples](https://github.com/GIG-Cymru-NHS-Wales/hl7-v2-examples) at 45926af), 22 | UK Core STU2 2.0.2 (`fhir.r4.ukcore.stu2`) | 183 | 181 |
| `fr` | The French agency's PAM-FR and document examples ([ansforge/hl7V2-exemples](https://github.com/ansforge/hl7V2-exemples) at 6033987), 59 | FR Core 2.2.0 (`hl7.fhir.fr.core`) | 142 | 124 |
| `de` | Invented German messages in `testdata/de` (no real person), 7 | ISiK Basismodul 4.0.3, Stufe 4 (`de.gematik.isik-basismodul`) | 37 | 12 |
| `eu` | The lab results (ORU^R01) among all three sets, 23 | HL7 Europe Laboratory Report 2.1.0 (`hl7.fhir.eu.laboratory`), with HL7 Europe Base 2.0.1 for patients, practitioners and organisations | 391 | 77 |

Per resource type and every error message: [results/uk.md](results/uk.md), [results/fr.md](results/fr.md),
[results/de.md](results/de.md), [results/eu.md](results/eu.md). Run 2026-10-22 (`eu` 2026-10-23), validator_cli 6.10.4, FHIR R4, terminology from tx.fhir.org.

## What is left, and why

- **UK Core, 2 of 183.** One Welsh sample sends a US race code (CDC 2028-9), which Perfuse carries in US Core's race
  extension; the UK run does not load US Core, so the validator refuses an extension it cannot find. One Specimen error is
  the terminology server failing to parse a SNOMED CT expression in the profile's own binding (tx.fhir.org), not the data.
- **FR Core, 18 Encounters.** FR Core requires a period and an identifier. 14 of the French document messages carry a PV1
  with no admission date (PV1-44), and 4 no visit number (PV1-19). Perfuse does not invent either.
- **ISiK, 25 of 37.** What ISiK requires that a v2 ADT does not say:
  - every Encounter needs a *Kontaktebene* (facility, department or care-unit contact), which no v2 field carries;
  - every Organization needs an identifier (IKNR, BSNR or a department id); the v2 messages name the facility only;
  - the patient number must be typed `MR`; the German messages here send `PI`, as the French national profile does;
  - a diagnosis needs `recordedDate`, from DG1-19 (attestation), which these messages leave empty;
  - two ORU ordering providers have no name type (XCN-10), so their name cannot be called official.

  A site meets these with a transformer step that adds what its own systems know (its IKNR, the contact level). Perfuse does
  not guess them.

- **HL7 Europe Laboratory Report, 77 of 391.** Patients 18 of 23, practitioners 20 of 22, organisations 5 of 5, specimens
  19 of 19 and 15 results pass. Every DiagnosticReport and most results fail, for reasons that are the report's form or the data:
  - the guide's report is a *document*: each DiagnosticReport must point at its Composition (the R5 `composition`
    extension), which exists only once a sender assembles the EU lab document. A v2 result converts to the report's parts,
    not to the document around them;
  - every result must name a performer. 287 of the sample results name none (no OBX-15, OBX-16, OBX-23 or OBX-25), and
    OBR-32, the result interpreter, is the report's (`resultsInterpreter`), not each result's;
  - every result must be timed. 260 have neither OBX-14 nor an OBR-7 to take it from;
  - 25 of the French results are a whole CDA lab report embedded as ED. Perfuse files the document as a DocumentReference
    and keeps an Observation pointing at it; the guide allows neither a valueless result nor that reference.
  The patients lack a date of birth (4) or carry a US race code (1); two practitioners have no name.

## What the runs fixed in Perfuse

Before these runs: FR Core Patient 33 of 40 and Practitioner 14 of 19; ISiK Practitioner 0 of 7, and German diagnoses and
insurance numbers written in forms no German receiver reads.

- **German code systems.** Table 0396 names ICD-10-GM and OPS with their year (`I10G2006`, `ICD10GM2016`, `O3012006`,
  `OPS2016`, `ALPHAID2016`); senders keep the pattern (`I10G2026`). They went to a placeholder system; now
  `http://fhir.de/CodeSystem/bfarm/icd-10-gm` (and `ops`, `alpha-id`) with the year as `Coding.version`, which ISiK requires.
  CWE-7/CWE-8, the coding system version, is now read for every coded field. `I10C` and `I9C` are read as ICD-10-CM and
  ICD-9-CM.
- **National patient numbers in the FHIR output.** A KVNR under gematik's OID (1.2.276.0.76.4.8) or type `NNDEU`, an NHS
  number under its OID: written under the system the country's guides use (`http://fhir.de/sid/gkv/kvid-10`,
  `https://fhir.nhs.uk/Id/nhs-number`, ...), without spacing, with fhir.de's `KVZ10` type for the KVNR. A number failing its
  check digit is kept as sent, untyped, with a warning.
- **The patient's own number** sent as `PI` (French, German) is now `use: usual`, as `MR` already was. FR Core requires it.
- **A result's time and SNOMED CT versions** (from the `eu` run). A result without OBX-14 now takes its report's OBR-7, as v2
  defines OBX-14 (sent only when a result's time differs from its order's). A SNOMED CT version given as a bare date in CWE-7
  (`20140131`) names no edition and is refused by a terminology server; it is left out instead (it was written since the
  CWE-7 change above). Specimens went from 18 to 19 of 19.
- **The laboratory that produced a result** (from the `eu` run). OBX-23 (with OBX-24, its address), or else OBX-15, the
  producer's id, and OBX-25, its medical director, were not read at all. They are now the result's performers, as HL7's
  v2-to-FHIR OBX map has them: 15 results passed that had failed, and the UK run checks 4 more resources, all passing.
- **Practitioners' names and national numbers.** XCN-10 `L` is now `use: official` (it was dropped). XCN-13 is now the
  identifier type, including France's `IDNPS` (RPPS/ADELI) in FR Core's code system.

## Running it

```
go build -o /tmp/perfuse ./cmd/perfuse
python3 conformance/national/run.py -perfuse /tmp/perfuse            # uk, fr and de; -sets uk,fr,de,eu
```

Needs Java 17 and `validator_cli.jar` (`-validator`, default `~/.cache/fhir-validator/validator_cli.jar`), and network
access for the packages and tx.fhir.org. The two public sample sets are fetched at the commits above into
`~/.cache/perfuse-national`. `-keep DIR` keeps the bundles, resources and validator output.
