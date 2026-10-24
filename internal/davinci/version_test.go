package davinci

import "testing"

func TestVersionsAreReadAsTheyAreWritten(t *testing.T) {
	for in, want := range map[string]string{"2.1": V21, "2.1.0": V21, "v2.1.0": V21, "2.2": V22, "2.2.1": V22, "2.0.1": "", "": "", "3.0": ""} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Pick("", "2.0.1", "2.1.0", "2.2"); got != V21 {
		t.Errorf("Pick = %q, want the first answered version", got)
	}
	if got := Pick("", "nonsense"); got != Latest {
		t.Errorf("Pick with nothing usable = %q, want %q", got, Latest)
	}
	bundle := map[string]any{"meta": map[string]any{"profile": []any{"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-request-bundle"}},
		"entry": []any{map[string]any{"resource": map[string]any{"meta": map[string]any{"profile": []any{
			"http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient|7.0.0",
			"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim|2.1.0"}}}}}}
	if got := FromProfiles(ProfilesIn(bundle)...); got != V21 {
		t.Errorf("FromProfiles = %q, want 2.1 from the Claim's profile (US Core's version is not a Da Vinci one)", got)
	}
	if got := Release("dtr", V22); got != "2.2.0" {
		t.Errorf("Release(dtr, 2.2) = %q", got)
	}
}
