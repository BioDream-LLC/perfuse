package v2fhir

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoPlaceholderClaimsToBeAnOID(t *testing.T) {
	// urn:oid: means an OID. RFC 3061 requires a dotted numeric one after it, so "urn:oid:local:hosp" is malformed - it announces a
	// registered object identifier and carries a word.
	//
	// Found by sending a converted Patient to a real HAPI FHIR server and reading back what it stored. HAPI accepted it without
	// complaint, which is the reason this needed a guard rather than a fix: the value was wrong and nothing in the pipeline objected,
	// so the next stricter receiver would have been the one to notice, in production, on somebody's patient identifier.
	//
	// Scans the source rather than calling the function, because the point is that no code path anywhere reintroduces the form.
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	// A positive control: a glob that matched nothing would report agreement about nothing.
	if len(paths) < 3 {
		t.Fatalf("found %d Go files in this package, so the glob is not reading it", len(paths))
	}

	fabricated := regexp.MustCompile(`"urn:oid:[^"0-9]`)

	checked := 0

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		checked++

		for _, m := range fabricated.FindAll(data, -1) {
			t.Errorf("%s builds %s…, which claims to be an OID and is not: use placeholderSystem", path, m)
		}
	}

	if checked == 0 {
		t.Fatal("no non-test files were checked")
	}
}

func TestThePlaceholderSystemIsAUsableURI(t *testing.T) {
	// An identifier without a system is ambiguous, so a placeholder is unavoidable. What it has to be is honest: a valid URI, one that
	// can never resolve to somebody's real service, and obviously a placeholder to anybody reading a message.
	got := placeholderSystem("HOSP")

	if !strings.HasPrefix(got, "http://") && !strings.HasPrefix(got, "https://") && !strings.HasPrefix(got, "urn:") {
		t.Errorf("placeholderSystem returned %q, which is not a URI", got)
	}

	// .invalid is reserved by RFC 2606 for exactly this. It guarantees the namespace cannot collide with a real one, which matters
	// because an identifier system is how a receiver decides whether two records are the same person.
	if !strings.Contains(got, ".invalid") {
		t.Errorf("placeholderSystem returned %q, which could collide with a real namespace", got)
	}

	if !strings.Contains(got, "hosp") {
		t.Errorf("placeholderSystem returned %q, which does not name the authority it stands for", got)
	}

	// Lower-cased, so the same authority written two ways in two messages does not become two systems - which would make one patient
	// look like two.
	if placeholderSystem("HOSP") != placeholderSystem("hosp") {
		t.Error("the same authority in different case produces two different systems")
	}
}
