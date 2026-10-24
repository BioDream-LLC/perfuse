# Nictiz nl-core 0.12.1-beta.1 (zib 2020): 3 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| AllergyIntolerance | `nl-core-AllergyIntolerance` | 1 | 0 | 1 |
| Condition | `nl-core-Problem` | 3 | 0 | 3 |
| Encounter | `nl-core-Encounter` | 3 | 3 | 0 |
| Location | `nl-core-HealthcareProvider` | 3 | 3 | 0 |
| Observation | `nl-core-LaboratoryTestResult` | 2 | 2 | 0 |
| Organization | `nl-core-HealthcareProvider-Organization` | 4 | 4 | 0 |
| Patient | `nl-core-Patient` | 3 | 0 | 3 |
| Practitioner | `nl-core-HealthProfessional-Practitioner` | 3 | 0 | 3 |
| Procedure | `nl-core-Procedure-event` | 1 | 0 | 1 |
| Specimen | `nl-core-LaboratoryTestResult.Specimen` | 1 | 0 | 1 |

No profile in the guide for: DiagnosticReport (1)

## AllergyIntolerance

- 1 x Error from https://tx.fhir.org/r4: Error: The filter "concept in 98061000146100" is not understood or supported

## Condition

- 3 x Error from https://tx.fhir.org/r4: Error: The filter "concept in 31000147101" is not understood or supported

## Patient

- 6 x HumanName.given.extension: minimum required = 1, but only found 0 (from http://nictiz.nl/fhir/StructureDefinition/nl-core-NameInformation|0.12.1-beta.1)
- 6 x Slice 'HumanName.given.extension:givenOrInitial' for extension 'http://hl7.org/fhir/StructureDefinition/iso21090-EN-qualifier': a matching slice is required, but not found (from http://nictiz.nl/fhir/StructureDefinition/nl-core-NameInformation|0.12.1-beta.1). Note that other slices are allowed in addition to this required slice

## Practitioner

- 3 x HumanName.given.extension: minimum required = 1, but only found 0 (from http://nictiz.nl/fhir/StructureDefinition/nl-core-NameInformation|0.12.1-beta.1)
- 3 x Slice 'HumanName.given.extension:givenOrInitial' for extension 'http://hl7.org/fhir/StructureDefinition/iso21090-EN-qualifier': a matching slice is required, but not found (from http://nictiz.nl/fhir/StructureDefinition/nl-core-NameInformation|0.12.1-beta.1). Note that other slices are allowed in addition to this required slice

## Procedure

- 1 x Error from https://tx.fhir.org/r4: Error: The filter "concept in 41000147108" is not understood or supported

## Specimen

- 1 x Error from https://tx.fhir.org/r4: Error: The filter "concept in 2581000146104" is not understood or supported
