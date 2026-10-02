package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bundle that cannot be checked and a bundle that is wrong are different answers.
//
// Validating a bundle used to print "Bundle validation from a file is not supported
// yet" and carry on, which was false in both directions. Bundles whose entries happened
// to be types the Bundle struct could hold were validated and reported valid, so the
// feature plainly was supported. And a bundle that was genuinely invalid produced the
// same line, told the operator the tool could not check their file, counted nothing, and
// exited zero - including under -strict, which exists so this can gate a build.
//
// Measured against the official FHIR R4 example corpus, 42 of the specification's own
// bundles were being skipped this way.

func writeBundleFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

const bundleWithBadDate = `{
  "resourceType": "Bundle",
  "id": "broken",
  "type": "collection",
  "entry": [
    {"resource": {"resourceType": "Patient", "id": "p1", "birthDate": "NOT-A-DATE"}}
  ]
}`

const bundleAllGood = `{
  "resourceType": "Bundle",
  "id": "fine",
  "type": "collection",
  "entry": [
    {"resource": {"resourceType": "Patient", "id": "p1", "birthDate": "1970-01-01",
      "identifier": [{"system": "urn:oid:1.2.3", "value": "1"}],
      "name": [{"family": "Testpatient", "given": ["Alpha"]}]}}
  ]
}`

const bundleUnimplementedEntry = `{
  "resourceType": "Bundle",
  "id": "skipme",
  "type": "collection",
  "entry": [
    {"resource": {"resourceType": "ValueSet", "id": "vs1", "status": "active"}}
  ]
}`

const bundleRequestOnly = `{
  "resourceType": "Bundle",
  "id": "req",
  "type": "batch",
  "entry": [
    {"request": {"method": "GET", "url": "Patient/1"}}
  ]
}`

func TestABrokenBundleIsReportedAsBrokenNotUnsupported(t *testing.T) {
	path := writeBundleFile(t, "broken.json", bundleWithBadDate)

	var out, errOut bytes.Buffer
	err := cmdFHIRValidate([]string{path}, &out, &errOut)

	if err == nil {
		t.Error("a bundle containing an invalid birthDate exited zero. With -strict this " +
			"command is meant to gate a build, and a broken bundle passed the gate")
	}
	got := out.String() + errOut.String()
	if strings.Contains(got, "not supported") {
		t.Errorf("the bundle was reported as an unsupported feature rather than as invalid, "+
			"which tells the operator their file is fine and the tool is lacking:\n%s", got)
	}
	if !strings.Contains(got, "birthDate") {
		t.Errorf("the actual problem is not named, so nobody can fix it:\n%s", got)
	}
	if !strings.Contains(got, "INVALID") {
		t.Errorf("the bundle is not marked invalid:\n%s", got)
	}
}

func TestAGoodBundleStillPasses(t *testing.T) {
	// Without this, reporting every bundle as invalid would satisfy the test above.
	path := writeBundleFile(t, "fine.json", bundleAllGood)

	var out, errOut bytes.Buffer
	if err := cmdFHIRValidate([]string{path}, &out, &errOut); err != nil {
		t.Errorf("a valid bundle was rejected: %v\n%s%s", err, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "Bundle") {
		t.Errorf("the bundle was not reported at all:\n%s", out.String())
	}
	if strings.Contains(out.String(), "0 entr") {
		t.Errorf("the bundle reported no entries checked, so it was counted without being "+
			"looked at:\n%s", out.String())
	}
}

func TestAnUnimplementedEntryTypeIsSkippedNotFailed(t *testing.T) {
	// A resource type this build cannot read is a limitation, not a defect in the
	// operator's data, and must not fail their build.
	path := writeBundleFile(t, "skip.json", bundleUnimplementedEntry)

	var out, errOut bytes.Buffer
	if err := cmdFHIRValidate([]string{path}, &out, &errOut); err != nil {
		t.Errorf("an entry of an unimplemented type failed the run. That is this build's "+
			"limitation and not a problem with the file: %v", err)
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("the skipped entry is not reported, so a bundle that was almost entirely "+
			"unchecked looks fully checked:\n%s", out.String())
	}
}

func TestARequestOnlyBundleIsAccepted(t *testing.T) {
	// A batch entry carrying only a request has no resource to validate. Such a bundle
	// parses as a whole, so it takes the ordinary path rather than the entry-by-entry
	// one, and it must not be reported as a problem for having nothing to check.
	path := writeBundleFile(t, "req.json", bundleRequestOnly)

	var out, errOut bytes.Buffer
	if err := cmdFHIRValidate([]string{path}, &out, &errOut); err != nil {
		t.Errorf("a request-only batch bundle was treated as a problem: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Bundle") {
		t.Errorf("the bundle was not reported at all:\n%s", out.String())
	}
}

func TestAnEntryIsValidatedEvenWhenTheBundleParses(t *testing.T) {
	// The case that would slip through a fix applied only to the failure path: an entry
	// whose JSON is fine and whose content is not. Bundles reached the entry-by-entry
	// code only when the whole document failed to unmarshal, so a validation-only fault
	// had to be checked separately rather than assumed.
	body := `{"resourceType":"Bundle","id":"s","type":"collection","entry":[
	  {"fullUrl":"urn:uuid:1","resource":{"resourceType":"Patient","id":"p1",
	    "gender":"not-a-real-gender",
	    "identifier":[{"system":"urn:oid:1.2.3","value":"1"}],
	    "name":[{"family":"Testpatient","given":["Alpha"]}]}}]}`
	path := writeBundleFile(t, "sneaky.json", body)

	var out, errOut bytes.Buffer
	err := cmdFHIRValidate([]string{path}, &out, &errOut)
	if err == nil {
		t.Error("an entry with a code outside its value set passed. A bundle is the usual " +
			"way a batch of records arrives, so this is the shape real invalid data takes")
	}
	if !strings.Contains(out.String(), "gender") {
		t.Errorf("the offending field is not named:\n%s", out.String())
	}
}

func TestBundleValidationIsNoLongerClaimedUnsupported(t *testing.T) {
	// Looks for the call rather than the phrase. The first version of this guard
	// searched for the sentence and found it in the comment that records why the
	// sentence was wrong, so it failed against the fix.
	src, err := os.ReadFile("fhir.go")
	if err != nil {
		t.Fatalf("reading fhir.go: %v", err)
	}
	if strings.Contains(string(src), `Fprintf(stdout, "%s: Bundle validation`) {
		t.Error("bundle validation is being declined again. Bundles are validated entry by " +
			"entry; if that is removed, the replacement must still distinguish a bundle " +
			"that cannot be read from one that is wrong, and must not exit zero on the " +
			"second")
	}
}
