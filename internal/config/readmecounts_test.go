package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The counts the README advertises must be the counts the code has.
//
// This exists because those numbers were wrong twice. The README claimed fourteen source types
// and sixteen destination types when the code had fifteen and seventeen, and both errors survived
// a deliberate review of the same table - once by me, reading the list and not the enum. A number
// in prose has no way of being checked by anything, so it drifts silently, and a capability
// nobody knows about is the same as one that does not exist.
//
// Deliberately asserts the count rather than the list. A list in a README is prose: it abbreviates
// ("DICOM query (C-FIND)"), it groups ("message broker (STOMP)"), and demanding an exact match
// would fail on wording rather than on substance. The count cannot be fudged.

// readmePath is relative to this package.
const readmePath = "../../README.md"

func readREADME(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("reading the README: %v", err)
	}
	return string(b)
}

// sourceTypeCount is how many source types the code offers.
//
// Counted from the constants rather than a list, because there is no exported list of source
// types the way there is for destinations - and counting the constants is what a reader of the
// code would do.
func sourceTypeCount(t *testing.T) int {
	t.Helper()

	b, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("reading config.go: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*Source[A-Za-z0-9]+ SourceType = "`)
	n := len(re.FindAllString(string(b), -1))
	if n == 0 {
		t.Fatal("found no source type constants; this test is measuring nothing")
	}
	return n
}

func TestTheREADMECountsMatchTheCode(t *testing.T) {
	readme := readREADME(t)

	sources := sourceTypeCount(t)
	destinations := len(AllDestinationTypes())

	// Sanity, so a broken count function cannot make the test vacuous.
	if destinations == 0 {
		t.Fatal("AllDestinationTypes is empty; this test is measuring nothing")
	}

	want := fmt.Sprintf("%d source types, %d destination types", sources, destinations)
	if !strings.Contains(readme, want) {
		// The claim that is there, so the failure says what to change rather than only that
		// something is wrong.
		found := regexp.MustCompile(`(\d+) source types, (\d+) destination types`).
			FindStringSubmatch(readme)
		got := "no such claim at all"
		if found != nil {
			got = found[0]
		}
		t.Errorf("the README says %q and the code has %q.\n"+
			"  Update README.md. A connector nobody knows about is the same as one that "+
			"does not exist, and this number has been wrong twice.", got, want)
	}
}

// Every connector in the code must be named somewhere in the README.
//
// The count above catches a missed addition arithmetically; this catches the case where the count
// was updated and the list was not, which is how DICOM query stayed unmentioned for months while
// the number beside it was corrected.
//
// Matched loosely, against the README lowercased, because the README writes "message broker
// (STOMP)" for broker and "DICOM query (C-FIND)" for dicom_query. The alias table records those
// so a genuinely absent connector still fails.
func TestEveryConnectorIsNamedInTheREADME(t *testing.T) {
	readme := strings.ToLower(readREADME(t))

	// How the README spells a type when it does not spell it literally.
	aliases := map[string]string{
		"broker":      "message broker",
		"dicom_query": "dicom query",
		"javascript":  "javascript",
		"smtp":        "smtp",
		"s3":          "s3",
		"cda":         "cda",
		"channel":     "another channel",
		"mllp":        "mllp",
	}

	var missing []string
	check := func(name string) {
		needle := strings.ToLower(name)
		if alias, ok := aliases[needle]; ok {
			needle = alias
		}
		if !strings.Contains(readme, needle) {
			missing = append(missing, name)
		}
	}

	for _, d := range AllDestinationTypes() {
		check(string(d))
	}
	for _, s := range []SourceType{
		SourceMLLP, SourceTCP, SourceHTTP, SourceSOAP, SourceFile, SourceFTP, SourceSFTP,
		SourceSMB, SourceWebDAV, SourceDatabase, SourceDICOM, SourceDICOMQuery, SourceKafka,
		SourceBroker, SourceJavaScript, SourceSerial,
	} {
		check(string(s))
	}

	if len(missing) > 0 {
		t.Errorf("these connectors exist and the README never names them: %s\n"+
			"  Add them to the connector rows. If the README spells one differently, add the "+
			"spelling to the alias table in this test rather than loosening the check.",
			strings.Join(missing, ", "))
	}
}

// The source list in this test must cover every source type.
//
// Without this the list above silently stops keeping pace: a new source type added to the code
// and not added here would be checked by nothing, and the guard would report success while
// covering fifteen of sixteen.
func TestTheConnectorGuardCoversEverySourceType(t *testing.T) {
	listed := []SourceType{
		SourceMLLP, SourceTCP, SourceHTTP, SourceSOAP, SourceFile, SourceFTP, SourceSFTP,
		SourceSMB, SourceWebDAV, SourceDatabase, SourceDICOM, SourceDICOMQuery, SourceKafka,
		SourceBroker, SourceJavaScript, SourceSerial,
	}
	if got, want := len(listed), sourceTypeCount(t); got != want {
		t.Errorf("the guard above checks %d source types and the code has %d; a type absent "+
			"from that list is checked by nothing", got, want)
	}
}

// The dependency count in the reference must match go.mod.
//
// Found by reading it: the reference said eight direct dependencies when go.mod had thirteen,
// because the number was written when the database drivers landed and never revisited. It is the
// same failure as the connector counts, in a document that argues explicitly for keeping
// dependencies few - so the wrong number undermines the very claim it is making.
func TestTheDependencyCountInTheReferenceMatchesGoMod(t *testing.T) {
	ref, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatalf("reading the reference: %v", err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	// Direct requirements are the ones not marked indirect.
	direct := 0
	inRequire := false
	for _, line := range strings.Split(string(mod), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "require ("):
			inRequire = true
			continue
		case trimmed == ")":
			inRequire = false
			continue
		}
		if !inRequire || trimmed == "" || strings.Contains(trimmed, "// indirect") {
			continue
		}
		direct++
	}
	if direct == 0 {
		t.Fatal("counted no direct dependencies; this test is measuring nothing")
	}

	re := regexp.MustCompile(`(?i)(\w+) direct(?:,| dependenc)`)
	m := re.FindStringSubmatch(string(ref))
	if m == nil {
		t.Skip("the reference no longer states a dependency count in a form this test reads")
	}

	// Written as a word in the prose, which is why this maps rather than parses.
	words := map[string]int{
		"four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
		"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
		"sixteen": 16, "seventeen": 17, "eighteen": 18,
	}
	stated, ok := words[strings.ToLower(m[1])]
	if !ok {
		if n, err := strconv.Atoi(m[1]); err == nil {
			stated = n
		} else {
			t.Skipf("could not read %q as a number", m[1])
		}
	}

	if stated != direct {
		t.Errorf("the reference says %d direct dependencies and go.mod has %d.\n"+
			"  Update docs/reference.md. The claim appears in the paragraph arguing that "+
			"dependencies are chosen deliberately, so a wrong count undermines it.",
			stated, direct)
	}
}
