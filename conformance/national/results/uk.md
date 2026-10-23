# UK Core STU2 2.0.2: 22 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| AllergyIntolerance | `UKCore-AllergyIntolerance` | 1 | 1 | 0 |
| Appointment | `UKCore-Appointment` | 1 | 1 | 0 |
| Condition | `UKCore-Condition` | 2 | 2 | 0 |
| DiagnosticReport | `UKCore-DiagnosticReport` | 4 | 4 | 0 |
| Encounter | `UKCore-Encounter` | 6 | 6 | 0 |
| Immunization | `UKCore-Immunization` | 7 | 7 | 0 |
| Location | `UKCore-Location` | 4 | 4 | 0 |
| Observation | `UKCore-Observation` | 118 | 118 | 0 |
| Organization | `UKCore-Organization` | 4 | 4 | 0 |
| Patient | `UKCore-Patient` | 18 | 17 | 1 |
| Practitioner | `UKCore-Practitioner` | 14 | 14 | 0 |
| Specimen | `UKCore-Specimen` | 3 | 2 | 1 |

## Patient

- 1 x The extension http://hl7.org/fhir/us/core/StructureDefinition/us-core-race could not be found so is not allowed here

## Specimen

- 1 x Error from https://tx.fhir.org/r4: Error: Invalid ECL expression: 'descendantOf 49755003 OR descendantOf 105590001 OR descendantOf 123037004 OR descendantOf 123038009 OR descendantOf 260787004': (Parser error at token 1: Unexpected tokens after end of expression. Current token: SCTID(49755003))
