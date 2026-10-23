# HL7 Europe Laboratory Report 2.1.0: 23 messages

| Resource | Profile | Resources | Pass | Fail |
|---|---|---|---|---|
| DiagnosticReport | `DiagnosticReport-eu-lab` | 20 | 0 | 20 |
| Observation | `Observation-resultslab-eu-lab` | 302 | 15 | 287 |
| Organization | `organization-eu-core` | 5 | 5 | 0 |
| Patient | `patient-eu-core` | 23 | 18 | 5 |
| Practitioner | `practitioner-eu-core` | 22 | 20 | 2 |
| Specimen | `Specimen-eu-lab` | 19 | 19 | 0 |

No profile in the guide for: DocumentReference (25), Encounter (18), Location (18)

## DiagnosticReport

- 20 x DiagnosticReport.extension: minimum required = 1, but only found 0 (from http://hl7.eu/fhir/laboratory/StructureDefinition/DiagnosticReport-eu-lab|2.1.0)
- 20 x Slice 'DiagnosticReport.extension:DiagnosticReportCompositionR5' for extension 'http://hl7.org/fhir/5.0/StructureDefinition/extension-DiagnosticReport.composition': a matching slice is required, but not found (from http://hl7.eu/fhir/laboratory/StructureDefinition/DiagnosticReport-eu-lab|2.1.0). Note that other slices are allowed in addition to this required slice

## Observation

- 287 x Observation.performer: minimum required = 1, but only found 0 (from http://hl7.eu/fhir/laboratory/StructureDefinition/Observation-resultslab-eu-lab|2.1.0)
- 260 x Observation.effective[x]: minimum required = 1, but only found 0 (from http://hl7.eu/fhir/laboratory/StructureDefinition/Observation-resultslab-eu-lab|2.1.0)
- 25 x The type 'DocumentReference' is not a valid Target for this element (must be one of [CanonicalType[http://hl7.eu/fhir/base/StructureDefinition/medicalTestResult-eu-core], CanonicalType[http://hl7.org/fhir/StructureDefinition/ImagingStudy]])
- 16 x Constraint failed: eu-lab-1: 'If observation status is other then "registered" or "cancelled", at least one of these Observation elements shall be provided with an actual value:  "value", the R5 "value[x]" extension, "dataAbsentReason", "hasMember" or "component"' (defined in http://hl7.eu/fhir/laboratory/StructureDefinition/Observation-resultslab-eu-lab)

## Patient

- 4 x Patient.birthDate: minimum required = 1, but only found 0 (from http://hl7.eu/fhir/base/StructureDefinition/patient-eu-core|2.0.1)
- 1 x The extension http://hl7.org/fhir/us/core/StructureDefinition/us-core-race could not be found so is not allowed here

## Practitioner

- 2 x Practitioner.name: minimum required = 1, but only found 0 (from http://hl7.eu/fhir/base/StructureDefinition/practitioner-eu-core|2.0.1)
