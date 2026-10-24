# FR Core 2.2.0: 59 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| Encounter | `fr-core-encounter` | 40 | 22 | 18 |
| Location | `fr-core-location` | 36 | 36 | 0 |
| Organization | `fr-core-organization` | 7 | 7 | 0 |
| Patient | `fr-core-patient` | 40 | 40 | 0 |
| Practitioner | `fr-core-practitioner` | 33 | 33 | 0 |

No profile in the guide for: DiagnosticReport (14), DocumentReference (44), Observation (178), ServiceRequest (14), Specimen (14)

## Encounter

- 14 x Encounter.period: minimum required = 1, but only found 0 (from https://hl7.fr/ig/fhir/core/StructureDefinition/fr-core-encounter|2.2.0)
- 4 x Encounter.identifier: minimum required = 1, but only found 0 (from https://hl7.fr/ig/fhir/core/StructureDefinition/fr-core-encounter|2.2.0)
