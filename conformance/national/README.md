# National FHIR profiles: UK Core, FR Core and ISiK

`run.py` converts real-shaped HL7 v2 messages with `perfuse fhir convert` and checks every resource with the official HL7
validator against the national profile for its type. It is a check of Perfuse's v2-to-FHIR conversion against what each
country's guide requires, not a certification, and not a test of Perfuse's FHIR server.

| Set | Messages | Guide | Resources checked | Pass |
|---|---|---|---|---|
| `uk` | NHS Wales' published v2 examples ([GIG-Cymru-NHS-Wales/hl7-v2-examples](https://github.com/GIG-Cymru-NHS-Wales/hl7-v2-examples) at 45926af), 22 | UK Core STU2 2.0.2 (`fhir.r4.ukcore.stu2`) | 179 | 177 |
| `fr` | The French agency's PAM-FR and document examples ([ansforge/hl7V2-exemples](https://github.com/ansforge/hl7V2-exemples) at 6033987), 59 | FR Core 2.2.0 (`hl7.fhir.fr.core`) | 142 | 124 |
| `de` | Invented German messages in `messages/de` (no real person), 7 | ISiK Basismodul 4.0.3, Stufe 4 (`de.gematik.isik-basismodul`) | 37 | 12 |

Per resource type and every error message: [results/uk.md](results/uk.md), [results/fr.md](results/fr.md),
[results/de.md](results/de.md). Run 2026-10-22, validator_cli 6.10.4, FHIR R4, terminology from tx.fhir.org.

## What is left, and why

- **UK Core, 2 of 179.** One Welsh sample sends a US race code (CDC 2028-9), which Perfuse carries in US Core's race
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
- **Practitioners' names and national numbers.** XCN-10 `L` is now `use: official` (it was dropped). XCN-13 is now the
  identifier type, including France's `IDNPS` (RPPS/ADELI) in FR Core's code system.

## Running it

```
go build -o /tmp/perfuse ./cmd/perfuse
python3 conformance/national/run.py -perfuse /tmp/perfuse            # all three; -sets uk,fr,de
```

Needs Java 17 and `validator_cli.jar` (`-validator`, default `~/.cache/fhir-validator/validator_cli.jar`), and network
access for the packages and tx.fhir.org. The two public sample sets are fetched at the commits above into
`~/.cache/perfuse-national`. `-keep DIR` keeps the bundles, resources and validator output.
