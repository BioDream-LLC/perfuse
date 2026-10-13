package fhir

import (
	"sort"
	"strconv"
	"strings"
)

// Profile validation, for the claims a resource makes about itself.
//
// meta.profile is an assertion of conformance. Something downstream reads it and decides it may rely on the elements that profile
// guarantees - that is the entire purpose of the field. So a claim nobody checked is worse than no claim: without it a consumer
// writes defensive code, and with it a consumer does not.
//
// Perfuse could already stamp US Core profile URLs onto everything the v2 converter produces, behind an opt-in flag whose
// documentation said to enable it "only once the output has actually been checked against US Core". Nothing existed that could do the
// checking. This file is that missing half.

// ProfileRules describes what a profile requires, to the extent this build can check it.
//
// Deliberately not a general StructureDefinition engine. A real one needs FHIRPath, slicing, and the published packages, which is a
// far larger undertaking. What is here is the mandatory cardinalities and the named invariants, transcribed from the published
// profile and cited so the next person can check the transcription rather than trusting it.
type ProfileRules struct {
	// URL is the canonical profile URL, unversioned, as it appears in meta.profile.
	URL string

	// Name is what to call it in a message to a human.
	Name string

	// Source is where the rules were read from, so a disagreement can be settled.
	Source string

	// Check appends problems for this profile. It is only called with a resource of the type the profile constrains.
	Check func(r Resource, res *ValidationResult)

	// AppliesTo is the resource type this profile constrains.
	AppliesTo string
}

// usCorePatientRules is transcribed from US Core 9.0.0.
//
// Mandatory, per the profile's own summary of four mandatory elements (two of them nested):
//
//	identifier         1..*
//	identifier.system  1..1
//	identifier.value   1..1
//	name               1..*
//
// And the invariant us-core-6 on Patient.name: family or given must be present, unless the data absent reason extension is.
//
// gender is 0..1 and is NOT mandatory. Worth stating explicitly because it is easy to assume otherwise - a validator that rejects a
// conformant Patient for want of a gender is worse than no validator, because the feed stops and the message blames the sender.
var usCorePatientRules = ProfileRules{
	URL:       "http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient",
	Name:      "US Core Patient",
	Source:    "US Core 9.0.0, StructureDefinition-us-core-patient",
	AppliesTo: "Patient",
	Check: func(r Resource, res *ValidationResult) {
		p, ok := r.(*Patient)
		if !ok {
			return
		}

		if len(p.Identifier) == 0 {
			res.add(Error, "Patient.identifier", "us-core-patient",
				"US Core requires at least one identifier, and this Patient has none. "+
					"An identifier is how anything downstream matches this person to its own record")
		}

		for i, id := range p.Identifier {
			// system and value are both 1..1 within each identifier. An identifier without a system is a bare string with no
			// namespace, so two hospitals' medical record numbers become indistinguishable.
			if strings.TrimSpace(id.System) == "" {
				res.add(Error, indexed("Patient.identifier", i)+".system", "us-core-patient",
					"US Core requires a system on every identifier, and this one has none, "+
						"so the value has no namespace and cannot be matched safely")
			}
			if strings.TrimSpace(id.Value) == "" {
				res.add(Error, indexed("Patient.identifier", i)+".value", "us-core-patient",
					"US Core requires a value on every identifier, and this one has none")
			}
		}

		if len(p.Name) == 0 {
			res.add(Error, "Patient.name", "us-core-patient",
				"US Core requires at least one name, and this Patient has none")
		}

		for i, n := range p.Name {
			// us-core-6. The data absent reason extension is the sanctioned way to say a name is genuinely unavailable, which does
			// happen - an unidentified patient in an emergency department.
			if strings.TrimSpace(n.Family) != "" || anyNonEmpty(n.Given) {
				continue
			}
			if hasDataAbsentReason(n.Extension) {
				continue
			}

			res.add(Error, indexed("Patient.name", i), "us-core-6",
				"US Core invariant us-core-6: a name must have a family name or a given name, "+
					"or carry the data absent reason extension to say plainly that it has neither")
		}
	},
}

// profileRules is the set of profiles this build can check.
//
// One list. A profile that can be claimed but not checked is reported as exactly that, by profileResult below, rather than passing
// quietly - the same reasoning as the capability statement, which must not overstate what the server does.
var profileRules = map[string]ProfileRules{
	usCorePatientRules.URL: usCorePatientRules,
}

// KnownProfiles returns the profile URLs this build can verify, sorted.
//
// Sorted because Go maps range randomly and this ends up in a capability statement and on screen.
func KnownProfiles() []string {
	out := make([]string, 0, len(profileRules))
	for url := range profileRules {
		out = append(out, url)
	}
	sort.Strings(out)

	return out
}

// ValidateProfiles checks every profile a resource claims in meta.profile.
//
// Three outcomes per claim, and the third is the one that matters:
//
//   - known, and the resource conforms: nothing reported.
//   - known, and it does not: an error naming the element and the rule.
//   - not known to this build: reported as unverified, at warning. Silence here would let a resource assert conformance to anything
//     at all and pass, which is the failure this whole file exists to prevent.
//
// A resource claiming no profile is not a problem. Claiming one is optional; claiming one falsely is not.
func ValidateProfiles(r Resource, res *ValidationResult) {
	claims := profileClaims(r)
	if len(claims) == 0 {
		return
	}

	checked := make([]string, 0, len(claims))

	for _, url := range claims {
		rules, known := profileRules[url]
		if !known {
			res.add(Warning, "meta.profile", "profile-unverified",
				"this resource claims conformance to %s, which this build cannot check, "+
					"so the claim has been passed through unverified rather than confirmed", url)

			continue
		}

		// A profile constrains one resource type. Claiming a Patient profile on an Observation is a straightforward mistake and
		// worth saying so, rather than running the rules and producing confusing output.
		if rules.AppliesTo != r.ResourceTypeName() {
			res.add(Error, "meta.profile", "profile-wrong-type",
				"this %s claims conformance to %s, which constrains %s, not %s",
				r.ResourceTypeName(), rules.Name, rules.AppliesTo, r.ResourceTypeName())

			continue
		}

		rules.Check(r, res)
		checked = append(checked, rules.Name)
	}

	// Recorded so a caller can say what was actually verified rather than implying everything was.
	if len(checked) > 0 {
		res.Profile = strings.Join(checked, ", ")
	}
}

// ConformsToProfile reports whether a resource satisfies a profile this build knows, without needing the caller to assemble a result.
//
// This is what makes the ClaimUSCore option honest: the claim can be checked before it is stamped, so a resource never carries an
// assertion that was not tested. It returns false for a profile this build cannot check, because "I could not check" and "it passes"
// must not be the same answer.
func ConformsToProfile(r Resource, profileURL string) (bool, *ValidationResult) {
	res := &ValidationResult{Findings: []Finding{}}

	rules, known := profileRules[profileURL]
	if !known {
		res.add(Warning, "meta.profile", "profile-unverified",
			"this build cannot check %s, so conformance to it cannot be confirmed", profileURL)

		return false, res
	}
	if rules.AppliesTo != r.ResourceTypeName() {
		res.add(Error, "meta.profile", "profile-wrong-type",
			"%s constrains %s, not %s", rules.Name, rules.AppliesTo, r.ResourceTypeName())

		return false, res
	}

	rules.Check(r, res)
	res.Profile = rules.Name

	errs, _, _ := res.Counts()

	return errs == 0, res
}

// profileClaims returns the profile URLs a resource declares, in order and without duplicates.
func profileClaims(r Resource) []string {
	meta := r.ResourceMeta()
	if meta == nil {
		return nil
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(meta.Profile))

	for _, url := range meta.Profile {
		url = strings.TrimSpace(url)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, url)
	}

	return out
}

// indexed renders a repeating element path the way FHIR does, so a message points at one entry rather than the whole list.
//
// "Patient.identifier[2].system is missing" tells somebody which of five identifiers to look at. "Patient.identifier.system is
// missing" makes them read all of them.
func indexed(path string, i int) string {
	return path + "[" + strconv.Itoa(i) + "]"
}

// hasDataAbsentReason reports whether the sanctioned "this is genuinely missing" extension is present.
func hasDataAbsentReason(exts []Extension) bool {
	const url = "http://hl7.org/fhir/StructureDefinition/data-absent-reason"

	for _, e := range exts {
		if e.URL == url {
			return true
		}
	}

	return false
}

// anyNonEmpty reports whether a repeating string element has any actual content.
//
// A given name of [""] is present in the JSON and absent in every sense that matters.
func anyNonEmpty(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}

	return false
}
