// Package fhir implements FHIR resources, serialisation and validation.
//
// Version handling is the first design decision, because "FHIR" on its own does
// not identify a wire format. R5 is the latest published release and is the
// default here. R4 is what the installed base and US regulation actually consume,
// so it is fully supported rather than treated as legacy. R6 is in ballot at the
// time of writing and is deliberately not implemented: shipping a guess at an
// unpublished specification would be worse than not supporting it.
//
// The resource model is R5-shaped. Where R4 differs, the difference is applied
// when serialising rather than by keeping two parallel sets of structs, which is
// how a codebase ends up with two subtly divergent definitions of Patient.
package fhir

import (
	"fmt"
	"strings"
)

// Version identifies a FHIR release.
type Version string

// Supported releases.
const (
	// R4 is 4.0.1, the version US Core and the CMS interoperability rules are
	// built on. Most production systems speak this.
	R4 Version = "4.0.1"

	// R4B is 4.3.0, a maintenance release between R4 and R5.
	R4B Version = "4.3.0"

	// R5 is 5.0.0, published March 2023 and the latest normative release.
	R5 Version = "5.0.0"
)

// DefaultVersion is what is used when nothing is specified.
//
// R4 is the release that US Core, the CMS interoperability rules, and the
// overwhelming majority of production EHRs speak. The resource structs in this
// package are R4-shaped (see ResourceShapeVersion), so declaring R4 is honest:
// a client reading the capability statement will parse the wire format it
// actually receives. R5 remains selectable for clients that explicitly request it.
const DefaultVersion = R4

// ResourceShapeVersion is the release the resource structs in this package are shaped to.
//
// R4, and it is not the same question as "which release is newest". The US Core profiles are R4-based, US Core is what American
// clinical apps and the CMS interoperability rules require, and the structs here follow US Core. Several fields moved between
// R4 and R5 in ways that are not additive:
//
//   - MedicationRequest.medication[x] is medicationCodeableConcept and medicationReference in R4, and a single medication of
//     type CodeableReference in R5.
//   - Procedure.performed[x] became occurrence[x], and Procedure.reasonCode became reason.
//   - AllergyIntolerance.type is a code in R4 and a CodeableConcept in R5.
//
// So a server that declares R5 while serving these structs tells a client to parse a shape that is not there. The client does
// not get an error - it gets an empty medication list, which in a clinical application is indistinguishable from a patient who
// takes no medications. A test asserts that anything declaring a served version agrees with this constant.
//
// Changing this to R5 means reshaping the structs, not editing the constant.
const ResourceShapeVersion = R4

// CanonicalVersion is the form the structs marshal to with no transformation applied.
//
// R5, because downgradeToR4 is the only transformation and it runs when an R4-family release is requested. Marshalling with this
// value is therefore a faithful round trip: unmarshal, marshal, unmarshal returns what you started with.
//
// Not the same as ResourceShapeVersion, and the difference is uncomfortable but real. The structs are a mixture: Encounter carries
// R5 shapes (actualPeriod, class as a list) while MedicationRequest and Procedure carry R4 field names, because the latter follow
// US Core. So "which release do these structs represent" has no single answer, and the two constants answer two different
// questions - what to tell a client, and what not to transform.
//
// Anything persisting a resource must use this. Storing the served release's form means an R4-configured server writes JSON its own
// structs cannot read back.
const CanonicalVersion = R5

// AllVersions lists the releases this package can produce, newest first.
var AllVersions = []Version{R5, R4B, R4}

// ParseVersion accepts a release name or number: "R4", "4.0.1", "r5", "5.0.0".
func ParseVersion(s string) (Version, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "":
		return DefaultVersion, nil
	case "R4", "4.0", "4.0.1":
		return R4, nil
	case "R4B", "4.3", "4.3.0":
		return R4B, nil
	case "R5", "5.0", "5.0.0":
		return R5, nil
	case "R6", "6.0", "6.0.0":
		return "", fmt.Errorf(
			"fhir: R6 is in ballot and not published; use R5 (the latest release) or R4 (what most systems consume)")
	default:
		return "", fmt.Errorf("fhir: unknown version %q; supported: R4, R4B, R5", s)
	}
}

// Name returns the short release name, such as "R5".
func (v Version) Name() string {
	switch v {
	case R4:
		return "R4"
	case R4B:
		return "R4B"
	case R5:
		return "R5"
	default:
		return string(v)
	}
}

// Valid reports whether this is a release the package can produce.
func (v Version) Valid() bool {
	switch v {
	case R4, R4B, R5:
		return true
	}
	return false
}

// Before reports whether this release predates another.
func (v Version) Before(other Version) bool { return v.ordinal() < other.ordinal() }

func (v Version) ordinal() int {
	switch v {
	case R4:
		return 4
	case R4B:
		return 43
	case R5:
		return 5000
	default:
		return 0
	}
}

// IsR4Family reports whether this release uses R4-era field names and types.
// R4 and R4B share the differences that matter for the resources here.
func (v Version) IsR4Family() bool { return v == R4 || v == R4B }

// SpecURL returns the base URL of the published specification.
func (v Version) SpecURL() string {
	switch v {
	case R4:
		return "http://hl7.org/fhir/R4"
	case R4B:
		return "http://hl7.org/fhir/R4B"
	case R5:
		return "http://hl7.org/fhir/R5"
	default:
		return "http://hl7.org/fhir"
	}
}

// Well-known system URIs. Hard-coding these is correct: they are stable
// identifiers defined by the specification, not configuration.
const (
	// SystemLOINC identifies LOINC codes. LOINC is free to use.
	SystemLOINC = "http://loinc.org"

	// SystemSNOMED identifies SNOMED CT. Using SNOMED content requires an
	// affiliate licence in most countries; the identifier itself does not.
	SystemSNOMED = "http://snomed.info/sct"

	// SystemUCUM identifies units of measure. Free to use, and the correct
	// system for a lab result's unit.
	SystemUCUM = "http://unitsofmeasure.org"

	// SystemRxNorm identifies medications. Free to use.
	SystemRxNorm = "http://www.nlm.nih.gov/research/umls/rxnorm"

	// SystemV2Table is the base for HL7 v2 code system tables, used when a v2
	// value has no cleaner FHIR equivalent. Preserving the original code with an
	// honest system beats inventing a mapping.
	SystemV2Table = "http://terminology.hl7.org/CodeSystem/v2-"

	// SystemIdentifierType is the identifier type code system, used to say that
	// an identifier is a medical record number rather than guessing.
	SystemIdentifierType = "http://terminology.hl7.org/CodeSystem/v2-0203"

	// SystemObservationCategory categorises observations, for example as
	// laboratory results.
	SystemObservationCategory = "http://terminology.hl7.org/CodeSystem/observation-category"

	// SystemActCode carries HL7 v3 act codes, which is where encounter class
	// values live.
	SystemActCode = "http://terminology.hl7.org/CodeSystem/v3-ActCode"

	// SystemDataAbsentReason explains why a value is missing rather than leaving
	// a field silently empty.
	SystemDataAbsentReason = "http://terminology.hl7.org/CodeSystem/data-absent-reason"
)

// USCoreProfile returns the US Core profile URL for a resource type.
//
// US Core is the profile set US regulation requires, and claiming conformance is
// only meaningful if the profile is actually declared on the resource.
func USCoreProfile(resourceType string) string {
	switch resourceType {
	case "Patient":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"
	case "Encounter":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-encounter"
	case "Observation":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-observation-lab"
	case "DiagnosticReport":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-diagnosticreport-lab"
	case "Practitioner":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-practitioner"
	case "Organization":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-organization"
	case "Location":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-location"
	case "Specimen":
		return "http://hl7.org/fhir/us/core/StructureDefinition/us-core-specimen"
	default:
		return ""
	}
}
