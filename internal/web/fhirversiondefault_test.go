package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No front-end panel may choose a FHIR release for itself.
//
// This defect has now appeared at eight sites: four flag defaults, a channel destination, the conversion endpoint, the endpoint that
// tells the interface what to preselect, and three panels. Every one was a reasonable local choice, because R5 genuinely is the
// newest published release. The trouble is that newest is not the same as the shape the structs populate, and a resource declaring
// one while carrying the other makes a receiver render an empty medication list - which looks exactly like a patient taking nothing.
//
// The rule: the server decides, /api/fhir/versions reports it, and the interface asks. Written in Go rather than as a vitest case
// because the front-end TypeScript configuration has no node types, so a test there cannot read files.
func TestNoPanelPreselectsAFhirRelease(t *testing.T) {
	src := filepath.Join("..", "..", "web", "src")

	if _, err := os.Stat(src); err != nil {
		t.Skipf("front-end sources are not present: %v", err)
	}

	// A state initialiser holding a release name. Option elements are allowed - a dropdown has to name its choices - and so are
	// comments, which is where this rule is explained at the sites that used to break it.
	seeded := regexp.MustCompile(`useState[<(][^)\n]*['"]R(?:4B|4|5)['"]`)

	var offenders []string

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("could not read %s: %v", src, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.Contains(name, ".test.") {
			continue
		}
		if !strings.HasSuffix(name, ".tsx") && !strings.HasSuffix(name, ".ts") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("could not read %s: %v", name, err)
		}

		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if strings.Contains(line, "<option") {
				continue
			}
			if seeded.MatchString(line) {
				offenders = append(offenders, name+":"+itoa(i+1)+": "+trimmed)
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("these panels preselect a FHIR release instead of asking the server:\n  %s\n\n"+
			"Initialise to '' and set it from api.fhirVersions().default. The server derives that from the release its "+
			"structs populate; a release name written in a panel is a second opinion that will eventually disagree.",
			strings.Join(offenders, "\n  "))
	}
}

// TestOnlyOnePanelHardcodesTheReleaseList keeps the list from being copied again.
//
// The document lab held its own copy of the whole list and offered R5 first while the server served R4. One list, fetched.
func TestOnlyOnePanelHardcodesTheReleaseList(t *testing.T) {
	src := filepath.Join("..", "..", "web", "src")

	if _, err := os.Stat(src); err != nil {
		t.Skipf("front-end sources are not present: %v", err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("could not read %s: %v", src, err)
	}

	var withLists []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".tsx") || strings.Contains(name, ".test.") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("could not read %s: %v", name, err)
		}

		text := string(body)
		if strings.Contains(text, `<option value="R5">`) && strings.Contains(text, `<option value="R4">`) {
			withLists = append(withLists, name)
		}
	}

	// ChannelBuilder writes its own, because a channel file has to be editable offline and its choices are the file's rather than
	// the server's. Named explicitly so that a new one is a failure rather than a habit.
	allowed := map[string]bool{"ChannelBuilder.tsx": true}

	for _, name := range withLists {
		if !allowed[name] {
			t.Errorf("%s hardcodes the FHIR release list. Fetch it from api.fhirVersions() instead - "+
				"the copies drift, and the one that drifts silently is the default", name)
		}
	}
}

// itoa avoids importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}

	return string(digits)
}
