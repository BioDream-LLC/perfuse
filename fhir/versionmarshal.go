package fhir

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

// MarshalVersioned renders a resource as JSON for the given FHIR version, applying
// version-specific transformations.
//
// The resource structs are R4-shaped (see ResourceShapeVersion). When version is R5,
// the R4 field names are upgraded to their R5 equivalents:
//
//   - MedicationRequest: medicationCodeableConcept/medicationReference → medication (CodeableReference)
//   - Procedure: performed[x] → occurrence[x], reasonCode → reason
//   - AllergyIntolerance: type (code) → type (CodeableConcept)
//
// When version is R4 (or R4B), the existing downgrade path handles it.
//
// This is distinct from Marshal, which is used for storage. Marshal produces a faithful
// round trip (the struct serialises and deserialises without loss). MarshalVersioned
// produces wire-format output shaped for a particular client.
func MarshalVersioned(resource Resource, version Version) ([]byte, error) {
	raw, err := Marshal(resource, version)
	if err != nil {
		return nil, err
	}
	if version == R5 {
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, err
		}
		upgradeToR5(tree, resource.ResourceTypeName())
		return json.Marshal(tree)
	}
	return raw, nil
}

// MarshalVersionedIndent is MarshalVersioned with indentation.
func MarshalVersionedIndent(resource Resource, version Version) ([]byte, error) {
	compact, err := MarshalVersioned(resource, version)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// MarshalBundleVersioned renders a bundle with version-aware transformations.
func MarshalBundleVersioned(b *Bundle, version Version) ([]byte, error) {
	raw, err := MarshalBundle(b, version)
	if err != nil {
		return nil, err
	}
	if version == R5 {
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, err
		}
		// Upgrade each entry's resource.
		if entries, ok := tree["entry"].([]any); ok {
			for _, e := range entries {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if res, ok := entry["resource"].(map[string]any); ok {
					if rt, ok := res["resourceType"].(string); ok {
						upgradeToR5(res, rt)
					}
				}
			}
		}
		return json.Marshal(tree)
	}
	return raw, nil
}

// MarshalBundleVersionedIndent is MarshalBundleVersioned with indentation.
func MarshalBundleVersionedIndent(b *Bundle, version Version) ([]byte, error) {
	compact, err := MarshalBundleVersioned(b, version)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// NegotiateVersion extracts the FHIR version a client is requesting from the Accept header
// or the _format query parameter.
//
// The FHIR specification allows clients to indicate a preferred version via:
//
//	Accept: application/fhir+json; fhirVersion=5.0.0
//
// or via the _format query parameter with the same media type parameters.
//
// If no version is specified, fallback is returned unchanged.
//
// If the client explicitly requests a version that is not supported, a non-Valid version is
// returned so the caller can produce a 406 Not Acceptable rather than silently serving a
// different version — which would be a confident wrong answer of the same class as serving
// R5 field names to an R4 client.
func NegotiateVersion(r *http.Request, fallback Version) Version {
	// Check Accept header first.
	if accept := r.Header.Get("Accept"); accept != "" {
		v, explicit := versionFromMediaType(accept)
		if v.Valid() {
			return v
		}
		if explicit {
			// Client named a fhirVersion we don't support. Return it as-is so the
			// caller sees a non-Valid version and can respond with 406.
			return v
		}
	}

	// Fall back to _format parameter.
	if format := r.URL.Query().Get("_format"); format != "" {
		v, explicit := versionFromMediaType(format)
		if v.Valid() {
			return v
		}
		if explicit {
			return v
		}
	}

	return fallback
}

// versionFromMediaType extracts fhirVersion from a media type string like
// "application/fhir+json; fhirVersion=5.0.0".
//
// Returns the parsed version and whether a fhirVersion parameter was explicitly present.
// When explicit is true but the version is not Valid(), the caller knows the client asked
// for something unsupported rather than not specifying a preference.
func versionFromMediaType(mediaType string) (Version, bool) {
	// The Accept header may contain multiple types separated by commas.
	for _, part := range strings.Split(mediaType, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, params, err := mime.ParseMediaType(part)
		if err != nil {
			continue
		}
		if fv, ok := params["fhirversion"]; ok {
			if v, err := ParseVersion(fv); err == nil {
				return v, true
			}
			// fhirVersion was specified but could not be parsed to a supported version.
			return Version(fv), true
		}
	}
	return "", false
}

// upgradeToR5 rewrites an R4-shaped resource tree into R5 form.
//
// This is the inverse of downgradeToR4: it renames the fields that moved between
// R4 and R5 so that a client expecting R5 shapes parses them correctly.
func upgradeToR5(tree map[string]any, resourceType string) {
	switch resourceType {
	case "MedicationRequest":
		// R4: medicationCodeableConcept or medicationReference (choice type)
		// R5: medication (CodeableReference with concept and/or reference)
		upgradeMedicationToR5(tree)

	case "Procedure":
		// R4: performed[x] → R5: occurrence[x]
		rename(tree, "performedDateTime", "occurrenceDateTime")
		rename(tree, "performedPeriod", "occurrencePeriod")
		rename(tree, "performedString", "occurrenceString")

		// R4: reasonCode → R5: reason (as CodeableReference list)
		if reasons, ok := tree["reasonCode"]; ok {
			// R5's reason is a list of CodeableReference; each R4 CodeableConcept becomes a CodeableReference
			// with only the concept field populated.
			if ccs, ok := reasons.([]any); ok {
				refs := make([]any, 0, len(ccs))
				for _, cc := range ccs {
					refs = append(refs, map[string]any{"concept": cc})
				}
				tree["reason"] = refs
			}
			delete(tree, "reasonCode")
		}

	case "AllergyIntolerance":
		// R4: type is a code (string like "allergy" or "intolerance")
		// R5: type is a CodeableConcept
		if typeCode, ok := tree["type"].(string); ok && typeCode != "" {
			tree["type"] = map[string]any{
				"coding": []any{
					map[string]any{
						"system": "http://hl7.org/fhir/allergy-intolerance-type",
						"code":   typeCode,
					},
				},
			}
		}
	}
}

// upgradeMedicationToR5 converts MedicationRequest.medication[x] from R4 choice
// to R5 CodeableReference.
func upgradeMedicationToR5(tree map[string]any) {
	var concept any
	var ref any

	if cc, ok := tree["medicationCodeableConcept"]; ok {
		concept = cc
		delete(tree, "medicationCodeableConcept")
	}
	if r, ok := tree["medicationReference"]; ok {
		ref = r
		delete(tree, "medicationReference")
	}

	if concept != nil || ref != nil {
		cr := map[string]any{}
		if concept != nil {
			cr["concept"] = concept
		}
		if ref != nil {
			cr["reference"] = ref
		}
		tree["medication"] = cr
	}
}
