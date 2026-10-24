# ISiK Basismodul 4.0.3, with the hospital's bundle script: 8 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| AllergyIntolerance | `ISiKAllergieUnvertraeglichkeit` | 1 | 1 | 0 |
| Condition | `ISiKDiagnose` | 4 | 2 | 2 |
| Encounter | `ISiKKontaktGesundheitseinrichtung` | 8 | 8 | 0 |
| Location | `ISiKStandort` | 8 | 8 | 0 |
| Organization | `ISiKOrganisation` | 8 | 8 | 0 |
| Patient | `ISiKPatient` | 8 | 8 | 0 |
| Practitioner | `ISiKPersonImGesundheitsberuf` | 9 | 7 | 2 |
| Procedure | `ISiKProzedur` | 1 | 0 | 1 |

No profile in the guide for: DiagnosticReport (2), Observation (6), Specimen (2)

## Condition

- 2 x Condition.recordedDate: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKDiagnose|4.0.3)

## Practitioner

- 2 x Slice 'Practitioner.name:Name': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKPersonImGesundheitsberuf|4.0.3). Note that other slices are allowed in addition to this required slice

## Procedure

- 1 x The code system 'http://fhir.de/CodeSystem/bfarm/ops' version '*' in the ValueSet include is different to the one in the value ('2026')
