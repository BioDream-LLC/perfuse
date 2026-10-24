// Package davinci says which release of the Da Vinci prior-authorization guides (CRD, DTR, PAS) a client speaks.
//
// Perfuse answers in two: 2.2 (CRD 2.2.1, DTR 2.2.0, PAS 2.2.1), the newest, and 2.1 (CRD, DTR and PAS 2.1.0), the release
// CMS-0057-F's recommended list points at once the 2.0.1 releases' US Core 3.1.1 basis expired. The two differ in shapes a
// client reads by name - DTR's $questionnaire-package parameters (PackageBundle in 2.1, packagebundle in 2.2), PAS's $inquire
// answer (one Bundle in 2.1, a Parameters of them in 2.2), extensions that exist in only one of them - so one answer cannot
// serve both, and the server has to know which it is talking to.
//
// It is told, most specific first: by the request itself (CRD's davinci-crd.requestedVersion; the version on a profile the
// input claims, profile|2.1.0), then by the client's registration (davinci_version on a SMART or CDS client), then by the
// server's own setting (serve -davinci-version), which defaults to the newest.
package davinci

import "strings"

const (
	// V21 is CRD, DTR and PAS 2.1.0.
	V21 = "2.1"
	// V22 is CRD 2.2.1, DTR 2.2.0 and PAS 2.2.1.
	V22 = "2.2"
	// Latest is what a client that says nothing gets, unless the server is set otherwise.
	Latest = V22
)

// Versions are the releases answered, oldest first.
var Versions = []string{V21, V22}

// Normalize reads a version as a client or a configuration writes it - "2.1", "2.1.0", "v2.2", "2.2.1" - as V21 or V22, and
// anything else as "".
func Normalize(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(v)), "v")
	switch {
	case v == "2.1" || strings.HasPrefix(v, "2.1."):
		return V21
	case v == "2.2" || strings.HasPrefix(v, "2.2."):
		return V22
	}
	return ""
}

// FromProfiles is the version named on the Da Vinci profiles an instance claims (http://hl7.org/fhir/us/davinci-pas/...|2.1.0),
// or "" when none names one.
func FromProfiles(profiles ...string) string {
	for _, p := range profiles {
		if !strings.Contains(p, "/fhir/us/davinci-") {
			continue
		}
		if _, ver, ok := strings.Cut(p, "|"); ok {
			if v := Normalize(ver); v != "" {
				return v
			}
		}
	}
	return ""
}

// Pick is the first version given that is one Perfuse answers, else Latest.
func Pick(candidates ...string) string {
	for _, c := range candidates {
		if v := Normalize(c); v != "" {
			return v
		}
	}
	return Latest
}

// Release is the full release of a guide in a version: Release("dtr", V22) is "2.2.0".
func Release(guide, version string) string {
	if version == V21 {
		return "2.1.0"
	}
	if guide == "dtr" {
		return "2.2.0"
	}
	return "2.2.1"
}

// ProfilesIn collects every meta.profile in a decoded JSON resource and the resources inside it (a Bundle's entries, a
// Parameters' resources, contained resources).
func ProfilesIn(node any) []string {
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if meta, ok := v["meta"].(map[string]any); ok {
				if list, ok := meta["profile"].([]any); ok {
					for _, p := range list {
						if s, ok := p.(string); ok {
							out = append(out, s)
						}
					}
				}
			}
			for k, c := range v {
				if k != "meta" {
					walk(c)
				}
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(node)
	return out
}
