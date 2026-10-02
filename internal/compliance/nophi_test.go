package compliance

import (
	"github.com/biodream-llc/perfuse/internal/generate"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No real patient data enters this repository.
//
// Why this is a test rather than a habit. This project cannot avoid containing HL7 messages: it is an HL7 engine, and a parser cannot be tested
// without messages to parse or documented without examples to read. There are a few hundred message fragments across the tree and there have to be.
//
// The danger is that the easiest way to reproduce a bug is to paste the message that caused it, and in this domain that message is somebody's
// medical record. It takes one hurried commit, and once it is pushed to a public repository it is in every clone and every fork permanently. No
// amount of care afterwards retrieves it.
//
// So the fixtures are held to an allowlist. Every patient surname in the tree has to be named here, which makes adding one a deliberate act that
// shows up in review rather than something arriving attached to a bug report. The failure message says what to do about it.
//
// This checks shape, not truth. It cannot tell a real person called Smith from an invented one, and it is not trying to: what it catches is an
// unfamiliar name arriving with a real record around it, which is what the accident actually looks like.

// syntheticSurnames are the invented patients used throughout the fixtures.
//
// Recognisable placeholders, historical figures, and names chosen to exercise character handling. Anything absent from this list fails.
var syntheticSurnames = map[string]bool{
	// Placeholders, in the several conventions the fixtures grew up with.
	"SURNAME": true, "NAME": true, "FAMILY": true, "LAST": true,
	"TEST": true, "TESTPATIENT": true, "PATIENT": true, "SAMPLE": true, "EXAMPLE": true,
	"DOE": true, "ROE": true, "SMITH": true, "JONES": true, "BLOGGS": true,

	// Invented patients that recur across the suite, so a reader can follow one person through a scenario.
	"FROST": true, "REED": true, "OKONKWO": true, "WILSON": true, "TAYLOR": true,
	"HOPPER": true, "LOVELACE": true, "TURING": true, "NIGHTINGALE": true,

	// Chosen to exercise character handling: accents, non-Latin scripts, apostrophes, hyphens.
	"ÉVRARD": true, "中村": true, "O'BRIEN": true, "SMITH-JONES": true, "MÜLLER": true,

	// One invented patient per HL7 version, so the version-coverage corpus reads as a set of distinct people.
	"BROWN": true, "CHEN": true, "LEE": true, "HAYES": true, "NAKAMURA": true,

	// Boundary cases for field length and character handling, named after what they test.
	"SHORT": true, "AVERYMUCHLONGERFAMILYNAMEINDEED": true, "N": true, "EVRARD": true,

	// The rest come from generate.SyntheticSurnames, merged in by init below rather than
	// copied here, so this check and the generator cannot disagree about what is synthetic.

	// Structural values that appear where a name would, in templates and format strings.
	"WRAPPED": true, "MULTI": true, "FIRST": true, "SECOND": true, "PREBUILT": true, "WINDOWS": true,
	"A": true, "B": true, "X": true, "Y": true, "O": true,
}

// allowedSSNs are the canonical invalid examples.
//
// None of these can be issued: 123-45-6789 is the universally used example and the others are sequences that are never assigned. Anything else of
// this shape is treated as real until somebody says otherwise in writing.
var allowedSSNs = map[string]bool{
	"123-45-6789": true,
	"111-22-3333": true,
	"987-65-4321": true,
	"000-00-0000": true,
}

// repoRoot walks up to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root")
		}
		dir = parent
	}
}

// eachSourceFile calls fn for every file worth scanning.
func eachSourceFile(t *testing.T, fn func(path string, content string)) {
	t.Helper()

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "dist": true, "bin": true, "vendor": true,
	}

	err := filepath.Walk(repoRoot(t), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}

			return nil
		}

		// Text only. A binary that happens to contain matching bytes is not a fixture anybody wrote.
		switch filepath.Ext(path) {
		case ".go", ".md", ".ts", ".tsx", ".js", ".yaml", ".yml", ".json", ".hl7", ".txt", ".xml", ".sh", ".py":
		default:
			return nil
		}

		// This file names the allowed values, so it would match itself.
		if strings.HasSuffix(path, "nophi_test.go") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 4<<20 {
			return nil
		}

		fn(path, string(raw))

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoUnexpectedPatientNamesInFixtures(t *testing.T) {
	// PID-5 is the patient name. Matched from the segment start so a PID mentioned in prose does not count.
	pid := regexp.MustCompile(`PID\|[^|\n"']*\|[^|\n"']*\|[^|\n"']*\|[^|\n"']*\|([^|\n"'\\]+)`)

	unexpected := map[string]string{}

	eachSourceFile(t, func(path, content string) {
		for _, m := range pid.FindAllStringSubmatch(content, -1) {
			// Repetitions are separated by ~, components by ^. The surname is the first component of each repetition.
			for _, repeat := range strings.Split(m[1], "~") {
				surname := strings.ToUpper(strings.TrimSpace(strings.Split(repeat, "^")[0]))
				if surname == "" {
					continue
				}

				// Template placeholders and format verbs are not names.
				if strings.ContainsAny(surname, "${%<>") {
					continue
				}

				if syntheticSurnames[surname] {
					continue
				}

				if _, seen := unexpected[surname]; !seen {
					unexpected[surname] = path
				}
			}
		}
	})

	for surname, path := range unexpected {
		t.Errorf("the patient surname %q is not a known synthetic name (in %s).\n"+
			"  If it is invented test data, add it to syntheticSurnames in this file.\n"+
			"  If it came out of a real message it must not be committed: this repository is public, and a message pushed "+
			"once remains in every clone and fork.", surname, path)
	}
}

func TestNoPlausibleSocialSecurityNumbers(t *testing.T) {
	ssn := regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)

	eachSourceFile(t, func(path, content string) {
		for _, found := range ssn.FindAllString(content, -1) {
			if allowedSSNs[found] {
				continue
			}

			t.Errorf("%s contains %s, which has the shape of a real social security number.\n"+
				"  Use one of the canonical invalid examples instead, or add it to allowedSSNs with a reason.", path, found)
		}
	})
}

func TestNoRealTelephoneNumbersInFixtures(t *testing.T) {
	// A ten-digit North American number with a plausible exchange. The 555 range is reserved for fiction and is what fixtures should use.
	phone := regexp.MustCompile(`\b\(?([2-9][0-9]{2})\)?[-. ]?([2-9][0-9]{2})[-. ]([0-9]{4})\b`)

	eachSourceFile(t, func(path, content string) {
		// Only inside files that carry HL7 fixtures, or every version string and port number in the tree matches.
		if !strings.Contains(content, "PID|") && !strings.Contains(content, "NK1|") {
			return
		}

		for _, m := range phone.FindAllStringSubmatch(content, -1) {
			if m[2] == "555" {
				continue
			}

			t.Errorf("%s contains what looks like a real telephone number (%s) in a file holding HL7 fixtures.\n"+
				"  Numbers with a 555 exchange are reserved for fiction; use one of those.", path, m[0])
		}
	})
}

// Every name the generator invents counts as synthetic here, read from the generator itself.
//
// Without this the two lists drift, and the drift presents as this check rejecting output
// produced by this repository's own tooling. Seven of the eight names generate produces were
// missing from the map above, so building a fixture out of perfuse generate output - the
// obvious way to get realistic test data - failed the build claiming it might be real patient
// data.
func init() {
	for _, name := range generate.SyntheticSurnames() {
		syntheticSurnames[strings.ToUpper(name)] = true
	}
}
