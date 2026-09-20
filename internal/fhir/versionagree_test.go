package fhir

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The release a server declares and the shape of the resources it serves are two separate facts, and they drifted.
//
// The server defaulted to R5 because R5 is the latest published release, which is a good reason. The US Core resource types are
// R4 shapes because US Core is R4-based, which is also a good reason. Nothing checked that the two agreed, so the capability
// statement advertised 5.0.0 over resources a client parsing R5 cannot read.
//
// The failure is silent. A client asks for a medication list, parses MedicationRequest.medication as a CodeableReference,
// finds nothing, and renders an empty list - which in a clinical application means "this patient takes no medications". That is
// the worst category of defect in this repository: a wrong clinical answer delivered with a 200.

// TestTheStructsAreShapedForTheVersionTheyClaim pins the divergent fields.
//
// Written as an explicit list of field names rather than as a comparison against a specification, because there is no machine
// -readable R4 or R5 schema in this repository to compare against. The list is the record: if somebody reshapes these structs
// for R5, this test tells them exactly which constant to move and why.
func TestTheStructsAreShapedForTheVersionTheyClaim(t *testing.T) {
	if ResourceShapeVersion != R4 {
		t.Fatalf("ResourceShapeVersion is %s, but the checks below describe R4 shapes. "+
			"If the structs were reshaped, update these expectations too rather than only the constant.",
			ResourceShapeVersion)
	}

	// Each case names a field that moved between R4 and R5, the R4 spelling that must be present, and the R5 spelling that
	// must be absent. Both directions matter: a struct carrying both shapes would satisfy a one-sided check while emitting
	// a document that is valid under neither release.
	cases := []struct {
		what     string
		resource Resource
		wantR4   []string
		notR5    []string
	}{
		{
			what: "MedicationRequest.medication[x]",
			resource: &MedicationRequest{
				MedicationCodeableConcept: &CodeableConcept{Text: "amoxicillin"},
			},
			wantR4: []string{"medicationCodeableConcept"},
			// R5 collapses the choice into one CodeableReference named plainly "medication".
			notR5: []string{`"medication":`},
		},
		{
			what: "Procedure.performed[x]",
			resource: &Procedure{
				PerformedDateTime: "2026-08-22T14:32:00Z",
			},
			wantR4: []string{"performedDateTime"},
			// R5 renamed it occurrence[x].
			notR5: []string{"occurrenceDateTime", "occurrencePeriod"},
		},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			raw, err := json.Marshal(c.resource)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)

			for _, want := range c.wantR4 {
				if !strings.Contains(body, want) {
					t.Errorf("%s: the R4 field %q is missing, so this is not an R4 shape: %s",
						c.what, want, body)
				}
			}
			for _, absent := range c.notR5 {
				if strings.Contains(body, absent) {
					t.Errorf("%s: the R5 field %q is present. If the structs were reshaped for R5, "+
						"ResourceShapeVersion must move too or the capability statement will lie again: %s",
						c.what, absent, body)
				}
			}
		})
	}
}

// TestReasonCodeIsTheR4Spelling covers the third divergence.
//
// Separate from the table above because Procedure carries both performed[x] and reasonCode, and a single case asserting both
// would report one failure for two independent problems.
func TestReasonCodeIsTheR4Spelling(t *testing.T) {
	raw, err := json.Marshal(&Procedure{
		ReasonCode: []CodeableConcept{{Text: "suspected fracture"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := string(raw)
	if !strings.Contains(body, "reasonCode") {
		t.Errorf("Procedure.reasonCode is missing, which is the R4 spelling: %s", body)
	}
	// R5 renamed it to reason, as a CodeableReference. Matching on the quoted key avoids matching reasonCode itself.
	if strings.Contains(body, `"reason":`) {
		t.Errorf("Procedure carries the R5 spelling \"reason\": %s", body)
	}
}

// TestTheLatestVersionIsStillReachable makes sure this did not quietly become an R4-only server.
//
// The point of the change was to stop declaring a version the resources do not match, not to drop support for the newest
// release. A deployment that wants R5 can still ask for it.
func TestTheLatestVersionIsStillReachable(t *testing.T) {
	// DefaultVersion is now R4 to match the resource shape, but R5 must remain reachable.
	if DefaultVersion != R4 {
		t.Errorf("DefaultVersion is %s; expected R4 to match ResourceShapeVersion", DefaultVersion)
	}

	got, err := ParseVersion("R5")
	if err != nil {
		t.Fatalf("R5 is no longer parseable: %v", err)
	}
	if got != R5 {
		t.Errorf("ParseVersion(R5) = %s", got)
	}
}

// TestDefaultVersionIsNotUsedAsAnOutputDefault covers the gap that let a real bug through.
//
// The guard in cmd checks that no *flag* default is a release literal. The FHIR destination default was not a flag - it was a
// channel configuration default in engine.newFHIRSender - so it sat outside what any test looked at, and it sent resources
// declaring 5.0.0 while carrying R4 field spellings.
//
// DefaultVersion means "the newest release this build can emit if someone asks for it". Anything choosing a release *on behalf of*
// a caller who did not ask must use ResourceShapeVersion instead, because that is the release the structs actually populate. This
// test enforces that distinction by reading the source, which is ugly, and is still better than finding out from a hospital that
// its medication list is empty.
func TestDefaultVersionIsNotUsedAsAnOutputDefault(t *testing.T) {
	root := ".."

	// Files that legitimately reference DefaultVersion: this package defines it, and the flag plumbing offers it as a choice.
	allowed := map[string]bool{
		"fhir/version.go": true,
	}

	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if allowed[rel] {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for i, line := range strings.Split(string(body), "\n") {
			code := line
			// Ignore comments, which is how this very rule is documented at the sites that used to break it.
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			// Qualified references anywhere, and bare ones only inside this package.
			//
			// The bare form has to be scoped, because internal/generate has its own DefaultVersion holding an HL7 v2
			// version like 2.5.1 - an entirely unrelated constant that happens to share a name. My first version of this
			// check flagged it, and treating that as a finding would have "fixed" the synthetic message generator into
			// emitting a FHIR release as an HL7 version.
			hit := strings.Contains(code, "fhir.DefaultVersion")
			if !hit && strings.HasPrefix(rel, "fhir/") {
				hit = strings.Contains(code, "DefaultVersion") && !strings.Contains(code, "fhir.")
			}
			if hit {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree failed: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("DefaultVersion is used to choose a release for a caller who did not ask, in %d place(s):\n  %s\n\n"+
			"Use ResourceShapeVersion for that. DefaultVersion is the newest release this build can emit on request, "+
			"which is not the same as the release these structs populate - and emitting the difference produces a "+
			"resource that declares one release and carries another.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
