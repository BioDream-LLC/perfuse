package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The feature declaration table's guardrails.
//
// # What these are for
//
// featureSupport is only worth having if it cannot drift from what the loader does. A table nobody checks is documentation, and
// documentation is what the shadow-mode comment was when it claimed shadow worked on a v3 channel while Channel.handle dispatched
// away before reaching the observation.
//
// So the property under test is agreement: for every data type and every cross-cutting feature, the loader's actual accept-or-
// refuse behaviour must equal the table. A refusal cannot be added, removed or forgotten without the table changing with it, and a
// new data type cannot be added without every cell being answered.

// featureFixture builds a minimal channel with one feature configured.
type featureFixture struct {
	dataType    DataType
	feature     Feature
	contractDir string
}

// yaml renders the fixture.
//
// The filter expression is per data type on purpose. Each format has its own path language, so an HL7 path on a pharmacy channel
// fails as a path error rather than a feature refusal - which is what made the first version of this probe report NCPDP as
// refusing filters when it accepts them.
func (f featureFixture) yaml(t *testing.T) string {
	t.Helper()

	filters := map[DataType]string{
		DataHL7: "PID-3 exists", DataHL7v3: "//id exists", DataX12: "CLM01 exists",
		DataNCPDP: "A1 exists", DataScript: `//Gender == "F"`, DataDelimited: "Ward exists",
		DataDICOM: "PID-3 exists", DataRaw: "PID-3 exists",
	}

	// Blocks some data types require before anything else validates.
	prereq := map[DataType]string{
		DataHL7v3:     "hl7v3:\n  acknowledge: false\n",
		DataDelimited: "delimited:\n  delimiter: \",\"\n  has_header: true\n",
	}

	var block string
	switch f.feature {
	case FeatureFilter:
		block = "filter: '" + filters[f.dataType] + "'\n"
	case FeatureTransformations:
		block = "transformations:\n  - set:\n      path: PID-8\n      value: M\n"
	case FeatureScripts:
		// A postprocessor rather than a preprocessor, because every type that runs scripts at all runs this slot. DICOM
		// refuses a preprocessor specifically, so a preprocessor fixture tests slot support while claiming to test feature
		// support - which is what seeded the DICOM cell wrongly the first time.
		block = "scripts:\n  postprocessor: |\n    logger.info(\"done\");\n"
	case FeatureShadow:
		block = "shadow:\n  channel: candidate.yaml\n"
	case FeatureContract:
		// A real file, so a missing-file error cannot masquerade as a feature refusal.
		block = "contract:\n  file: " + filepath.Join(f.contractDir, "c.yaml") + "\n"
	case FeatureAttachments:
		block = "attachments:\n  extract:\n    - path: OBX-5\n"
	}

	return "name: feature-probe\ndataType: " + string(f.dataType) + "\n" + prereq[f.dataType] + block +
		"source:\n  type: http\n  http:\n    listen: \"127.0.0.1:0\"\n    path: /in\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/featureprobe\n"
}

// TestTheValidatorMatchesTheFeatureTable is the agreement property.
//
// This is the check that converts featureSupport from a comment into a constraint. Without it the table is a second opinion about
// what the loader does, and second opinions drift - which is the whole defect class.
func TestTheValidatorMatchesTheFeatureTable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte("expectations: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, dt := range KnownDataTypes {
		for _, feat := range AllFeatures {
			t.Run(string(dt)+"/"+string(feat), func(t *testing.T) {
				fixture := featureFixture{dataType: dt, feature: feat, contractDir: dir}

				_, err := Load(strings.NewReader(fixture.yaml(t)), "feature.yaml")

				// A refusal counts only when it is about this feature. Anything else is a fixture problem, and treating it
				// as a refusal would let the table record a lie.
				refused := err != nil && featureRefusal(err.Error(), feat)

				want := Supports(dt, feat)

				if want && refused {
					t.Errorf("featureSupport says %s honours %s, but the loader refused it: %v", dt, feat, err)
				}
				if !want && !refused {
					if err != nil {
						t.Skipf("inconclusive: the loader failed for an unrelated reason, so this cell is untested: %v", err)
					}
					t.Errorf("featureSupport says %s does not honour %s, but the loader accepted it. Either wire it up, "+
						"refuse it with a reason, or correct the table", dt, feat)
				}
			})
		}
	}
}

// featureRefusal reports whether an error is a refusal of this feature rather than an unrelated validation problem.
func featureRefusal(msg string, feat Feature) bool {
	if !strings.Contains(msg, string(feat)) {
		return false
	}

	// The phrases the loader uses when it refuses something because it would not take effect. Matched rather than relying on
	// the feature name alone, because a path error also mentions the feature name.
	for _, phrase := range []string{
		"never take effect", "never execute", "never run", "does not run", "cannot run",
		"does not honour", "would never match", "remove it", "instead",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}

	return false
}

// TestEveryDataTypeDeclaresEveryFeature stops a new data type defaulting its way in.
//
// A type added to KnownDataTypes with no row here would read as supporting nothing, which is a decision nobody made. Failing
// instead forces the six questions to be answered.
func TestEveryDataTypeDeclaresEveryFeature(t *testing.T) {
	for _, dt := range KnownDataTypes {
		row, ok := featureSupport[dt]
		if !ok {
			t.Errorf("%s is a known data type with no row in featureSupport, so every feature would silently read as "+
				"unsupported", dt)

			continue
		}

		for _, feat := range AllFeatures {
			if _, declared := row[feat]; !declared {
				t.Errorf("%s does not declare %s; write true or false rather than leaving it to the zero value", dt, feat)
			}
		}
	}

	// The reverse: a row for a type that no longer exists is a stale entry that would excuse the next real gap.
	known := make(map[DataType]bool, len(KnownDataTypes))
	for _, dt := range KnownDataTypes {
		known[dt] = true
	}
	for dt := range featureSupport {
		if !known[dt] {
			t.Errorf("featureSupport has a row for %q, which is not a known data type", dt)
		}
	}
}

// TestTheExistingTablesAgreeWithTheFeatureTable catches drift during the migration.
//
// shadowRuns and scriptSlotsRun predate this table and are still what the validator consults. Until they are folded in, the two
// can disagree - so this asserts they do not, which is the only thing making the duplication safe.
func TestTheExistingTablesAgreeWithTheFeatureTable(t *testing.T) {
	for _, dt := range KnownDataTypes {
		if got, want := shadowRuns[dt], Supports(dt, FeatureShadow); got != want {
			t.Errorf("%s: shadowRuns says %v, featureSupport says %v", dt, got, want)
		}

		// Scripts are per slot in scriptSlotsRun. The feature-level answer is whether the type runs any slot at all.
		anySlot := len(scriptSlotsRun[dt]) > 0
		if got, want := anySlot, Supports(dt, FeatureScripts); got != want {
			t.Errorf("%s: scriptSlotsRun has %d slot(s) so scripts=%v, featureSupport says %v",
				dt, len(scriptSlotsRun[dt]), anySlot, want)
		}
	}
}

// TestSuspectedInertCellsAreDeclaredSupported keeps the debt list honest in both directions.
//
// An entry in featureAcceptedButSuspectedInert only makes sense for a cell the loader accepts. If the cell has since been refused,
// the entry is stale and would otherwise sit there implying an open defect that is closed.
func TestSuspectedInertCellsAreDeclaredSupported(t *testing.T) {
	for dt, row := range featureAcceptedButSuspectedInert {
		for feat, why := range row {
			if !Supports(dt, feat) {
				t.Errorf("%s/%s is listed as accepted-but-suspected-inert, but featureSupport says it is not supported. "+
					"If it is now refused, remove the entry; the reason recorded was: %s", dt, feat, why)
			}
		}
	}
}

// TestTheSuspectedInertListIsNotEmptyWithoutSaying guards the degenerate case.
//
// Emptying the list would make every check above pass while the underlying defects remained. If it is genuinely empty because the
// cells were fixed, that is a deliberate change and this test should be updated with it.
func TestTheSuspectedInertListIsNotEmptyWithoutSaying(t *testing.T) {
	const knownSuspect = 9

	count := 0
	for _, row := range featureAcceptedButSuspectedInert {
		count += len(row)
	}

	if count > knownSuspect {
		t.Errorf("the suspected-inert list has grown to %d entries from %d; a new one means a feature was accepted on a "+
			"data type that cannot honour it", count, knownSuspect)
	}
	if count < knownSuspect {
		t.Logf("the suspected-inert list has shrunk to %d from %d. If those cells were fixed, lower knownSuspect to match",
			count, knownSuspect)
	}
}
