# ISiK Basismodul 4.0.3: 7 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| Condition | `ISiKDiagnose` | 2 | 0 | 2 |
| Encounter | `ISiKKontaktGesundheitseinrichtung` | 7 | 0 | 7 |
| Location | `ISiKStandort` | 7 | 7 | 0 |
| Organization | `ISiKOrganisation` | 7 | 0 | 7 |
| Patient | `ISiKPatient` | 7 | 0 | 7 |
| Practitioner | `ISiKPersonImGesundheitsberuf` | 7 | 5 | 2 |

No profile in the guide for: DiagnosticReport (2), Observation (6), Specimen (2)

## Condition

- 2 x Condition.recordedDate: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKDiagnose|4.0.3)

## Encounter

- 7 x Slice 'Encounter.type:Kontaktebene': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3). Note that other slices are allowed in addition to this required slice
- 2 x Encounter.type: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3)

## Organization

- 7 x Organization.identifier: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKOrganisation|4.0.3)

## Patient

- 7 x Slice 'Patient.identifier:Patientennummer': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKPatient|4.0.3). Note that other slices are allowed in addition to this required slice

## Practitioner

- 2 x Slice 'Practitioner.name:Name': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKPersonImGesundheitsberuf|4.0.3). Note that other slices are allowed in addition to this required slice
