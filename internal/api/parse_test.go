package api

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The safety of opening an existing channel in the form rests entirely on the round-trip check, so
// these tests are led by the ways it must refuse rather than by the happy path.

func TestFormOpensAChannelItCanRepresent(t *testing.T) {
	res := parseChannelForForm(`name: simple
source:
  type: mllp
  listen: 127.0.0.1:2575
transformations:
  - description: normalise sex
    map:
      path: PID-8
      table:
        "1": M
        "2": F
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)

	if !res.Editable {
		t.Fatalf("refused a channel the form built its own model from: %s", res.Why)
	}
	if res.Model == nil {
		t.Fatal("editable but no model was returned")
	}
	if res.Model.Name != "simple" {
		t.Errorf("name = %q", res.Model.Name)
	}
	if len(res.Model.Steps) != 1 {
		t.Fatalf("read %d transformations, want 1", len(res.Model.Steps))
	}
	if len(res.Model.Dests) != 1 || res.Model.Dests[0].Name != "out" {
		t.Errorf("destinations = %+v", res.Model.Dests)
	}
}

func TestFormRefusesASettingItDoesNotKnow(t *testing.T) {
	// The failure this whole feature exists to prevent. A key the form cannot show would be dropped
	// on save, and an interface that has run for years would quietly stop doing something.
	//
	// Uses a key the loader accepts but the form model has no field for, so the file is valid and
	// the only thing standing between it and silent data loss is this check.
	res := parseChannelForForm(`name: shadowed
source:
  type: mllp
  listen: 127.0.0.1:2575
shadow:
  channel: other
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)

	if res.Editable {
		t.Fatal("the form offered to edit a channel containing a setting it cannot show")
	}
	if res.Model != nil {
		// A half-populated form is worse than none, because it looks usable.
		t.Error("a refusal still returned a model")
	}
	if !strings.Contains(res.Why, "shadow") {
		t.Errorf("the refusal does not name the setting: %q", res.Why)
	}
	if !strings.Contains(strings.ToLower(res.Why), "text editor") {
		t.Errorf("the refusal does not say what to do instead: %q", res.Why)
	}
}

func TestARefusalNamesTheKeyAndNotAGoType(t *testing.T) {
	// yaml.v3 reports an unknown key as "field foo not found in type api.buildSource", which names a
	// Go type the reader has never heard of and cannot look up. The key and the line are the useful
	// parts.
	//
	// Tested directly rather than through a file, because config.Load refuses unknown keys before
	// this code is reached. The only keys that get here are ones the loader accepts and the form does
	// not - so this function's input comes from the form model's decoder, not the loader's.
	one := tidyYAMLError(errors.New("yaml: unmarshal errors:\n  line 6: field shadow not found in type api.buildModel"))
	if strings.Contains(one, "api.build") {
		t.Errorf("a Go type leaked at the reader: %q", one)
	}
	if !strings.Contains(one, "shadow") {
		t.Errorf("the key was not named: %q", one)
	}
	if !strings.Contains(strings.ToLower(one), "text editor") {
		t.Errorf("no guidance on what to do instead: %q", one)
	}

	// More than one unknown key reads as a list rather than as a sentence about a single setting.
	many := tidyYAMLError(errors.New("yaml: unmarshal errors:\n" +
		"  line 6: field shadow not found in type api.buildModel\n" +
		"  line 9: field mirth not found in type api.buildModel"))
	if !strings.Contains(many, "shadow") || !strings.Contains(many, "mirth") {
		t.Errorf("not every unknown key was named: %q", many)
	}
	if strings.Contains(many, "api.build") {
		t.Errorf("a Go type leaked at the reader: %q", many)
	}

	// Anything that is not an unknown-field complaint is passed through rather than mangled into a
	// confident sentence about a key that does not exist.
	plain := tidyYAMLError(errors.New("line 3: did not find expected key"))
	if !strings.Contains(plain, "did not find expected key") {
		t.Errorf("an unrelated error was rewritten: %q", plain)
	}
}

func TestAFileThatDoesNotLoadIsSentToTheTextEditor(t *testing.T) {
	// No matter what the form could do with it, a broken file is a text editor job, and the loader's
	// own message is more useful than anything invented here.
	res := parseChannelForForm(`name: broken
source:
  type: mllp
destinations: []
`)

	if res.Editable {
		t.Fatal("a channel that does not load was offered to the form")
	}
	if !strings.Contains(strings.ToLower(res.Why), "text editor") {
		t.Errorf("no guidance in the refusal: %q", res.Why)
	}
}

func TestCommentsAreAWarningRatherThanARefusal(t *testing.T) {
	// Refusing commented files would make the feature useless, since commenting a channel file is
	// good practice. Losing a comment is recoverable and visible in the history; losing a setting is
	// neither.
	res := parseChannelForForm(`# This feed comes from the hospital's ADT system.
name: commented
source:
  type: mllp
  listen: 127.0.0.1:2575
destinations:
  # the lab wants everything
  - name: out
    type: file
    dir: /tmp/out
`)

	if !res.Editable {
		t.Fatalf("a commented channel was refused: %s", res.Why)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("no warning that the comments will be lost")
	}
	if !strings.Contains(strings.ToLower(res.Warnings[0]), "comment") {
		t.Errorf("the warning does not mention comments: %q", res.Warnings[0])
	}
}

func TestAnAlreadyTidyFileWarnsAboutNothing(t *testing.T) {
	// A file the form itself produced should reopen with no fuss at all. A warning on every open
	// teaches people to ignore warnings.
	built, err := marshalChannel(buildModel{
		Name:   "tidy",
		Source: buildSource{Type: "mllp", Listen: "127.0.0.1:2575"},
		Dests:  []buildDest{{Name: "out", Type: "file", Dir: "/tmp/out"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := parseChannelForForm(built)
	if !res.Editable {
		t.Fatalf("the form refused its own output: %s", res.Why)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warned about a file it generated itself: %v", res.Warnings)
	}
}

func TestTheRoundTripPreservesWhatTheChannelDoes(t *testing.T) {
	// The proof the whole feature rests on. Read a channel into the form model, write it back, and
	// require the loaded result to be identical - not similar.
	original := `name: careful
description: an interface with a bit of everything
source:
  type: mllp
  listen: 127.0.0.1:2575
filter: MSH-9.1 == 'ADT'
transformations:
  - description: strip the padding
    replace:
      path: PID-3.1
      pattern: "^0+"
      with: ""
  - trim:
      path: PID-5.1
destinations:
  - name: lab
    type: mllp
    address: 10.0.0.5:2575
  - name: archive
    type: file
    dir: /var/spool/archive
`

	res := parseChannelForForm(original)
	if !res.Editable {
		t.Fatalf("refused: %s", res.Why)
	}

	regenerated, err := marshalChannel(*res.Model)
	if err != nil {
		t.Fatal(err)
	}

	// Compared through the loader, which is what actually decides behaviour.
	a := mustCanonical(t, original)
	b := mustCanonical(t, regenerated)
	if a != b {
		t.Errorf("the round trip changed the channel:\n%s", firstDifference(a, b))
	}
}

func TestARefusalNamesTheFirstDifference(t *testing.T) {
	// "The form would change something" leaves the reader with no idea what, so they either abandon
	// the form or assume the check is broken.
	if got := firstDifference("a: 1\nb: 2\n", "a: 1\n"); !strings.Contains(got, "b: 2") {
		t.Errorf("a dropped line was not named: %q", got)
	}
	if got := firstDifference("a: 1\n", "a: 1\nc: 3\n"); !strings.Contains(got, "c: 3") {
		t.Errorf("an added line was not named: %q", got)
	}
	if got := firstDifference("a: 1\n", "a: 2\n"); !strings.Contains(got, "a: 1") ||
		!strings.Contains(got, "a: 2") {
		t.Errorf("a changed line was not named on both sides: %q", got)
	}
	if got := firstDifference("a: 1\n", "a: 1\n"); got != "" {
		t.Errorf("identical input reported a difference: %q", got)
	}
}

func TestCommentDetectionIgnoresAHashInsideAValue(t *testing.T) {
	// A hash in a password or a URL fragment is not a comment. Being wrong here only costs a less
	// specific warning, but a wrong warning is worse than a vague one because it teaches distrust.
	if hasComments("name: x\npassword: \"abc#123\"\n") {
		t.Error("a hash inside a quoted value was read as a comment")
	}
	if !hasComments("# a real comment\nname: x\n") {
		t.Error("a leading comment was missed")
	}
	if !hasComments("name: x # trailing\n") {
		t.Error("a trailing comment was missed")
	}
}

func TestParseEndpointRefusesAnEmptyBody(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", "POST", "/api/channels/parse", parseRequest{YAML: "   "})
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestParseEndpointNeedsEditor(t *testing.T) {
	// It compiles the channel to prove the round trip, exactly as validate does.
	h := newHarness(t)

	rec := h.do("viewer", "POST", "/api/channels/parse", parseRequest{YAML: "name: x\n"})
	if rec.Code != 403 {
		t.Errorf("a viewer got %d, want 403", rec.Code)
	}
}

func TestParseEndpointAnswersARefusalWithTwoHundred(t *testing.T) {
	// The caller asked whether the form can edit this file. "No, because of this" is a successful
	// answer to that question, not a server error, and a client that treats non-200 as a failure
	// would show a red box instead of the explanation.
	h := newHarness(t)

	rec := h.do("editor", "POST", "/api/channels/parse", parseRequest{YAML: `name: shadowed
source:
  type: mllp
  listen: 127.0.0.1:2575
shadow:
  channel: other
destinations:
  - name: out
    type: file
    dir: /tmp/out
`})
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "shadow") {
		t.Errorf("body does not name the setting: %s", rec.Body.String())
	}
}

func mustCanonical(t *testing.T, text string) string {
	t.Helper()
	c, err := loadForTest(text)
	if err != nil {
		t.Fatal(err)
	}
	s, err := canonical(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// loadForTest loads a channel from text the way a file on disk is loaded.
func loadForTest(text string) (*config.Channel, error) {
	return config.Load(bytes.NewReader([]byte(text)), "(test)")
}
