# ISiK Basismodul 4.0.3: 8 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| AllergyIntolerance | `ISiKAllergieUnvertraeglichkeit` | 1 | 1 | 0 |
| Condition | `ISiKDiagnose` | 4 | 2 | 2 |
| Encounter | `ISiKKontaktGesundheitseinrichtung` | 8 | 0 | 8 |
| Location | `ISiKStandort` | 8 | 8 | 0 |
| Organization | `ISiKOrganisation` | 8 | 0 | 8 |
| Patient | `ISiKPatient` | 8 | 0 | 8 |
| Practitioner | `ISiKPersonImGesundheitsberuf` | 9 | 7 | 2 |
| Procedure | `ISiKProzedur` | 1 | 0 | 1 |

No profile in the guide for: DiagnosticReport (2), Observation (6), ServiceRequest (2), Specimen (2)

## Condition

- 2 x Condition.recordedDate: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKDiagnose|4.0.3)

## Encounter

- 8 x Slice 'Encounter.type:Kontaktebene': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3). Note that other slices are allowed in addition to this required slice
- 3 x Slice 'Encounter.diagnosis.use.coding:Diagnosetyp': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3). Note that other slices are allowed in addition to this required slice
- 2 x Encounter.type: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3)
- 1 x Encounter.diagnosis.use: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKKontaktGesundheitseinrichtung|4.0.3)

## Organization

- 8 x Organization.identifier: minimum required = 1, but only found 0 (from https://gematik.de/fhir/isik/StructureDefinition/ISiKOrganisation|4.0.3)

## Patient

- 8 x Slice 'Patient.identifier:Patientennummer': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKPatient|4.0.3). Note that other slices are allowed in addition to this required slice

## Practitioner

- 2 x Slice 'Practitioner.name:Name': a matching slice is required, but not found (from https://gematik.de/fhir/isik/StructureDefinition/ISiKPersonImGesundheitsberuf|4.0.3). Note that other slices are allowed in addition to this required slice

## Procedure

- 1 x The code system 'http://fhir.de/CodeSystem/bfarm/ops' version '*' in the ValueSet include is different to the one in the value ('2026')
