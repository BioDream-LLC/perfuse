package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A PDQ query response, the shape a picker will actually be pointed at.
const v3Sample = `<?xml version="1.0"?>
<PRPA_IN201306UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.72" extension="MSG00001"/>
  <interactionId extension="PRPA_IN201306UV02"/>
  <controlActProcess classCode="CACT">
    <subject><registrationEvent><subject1>
      <patient classCode="PAT">
        <id root="2.16.840.1.113883.3.72.5.9.1" extension="PIX1234"/>
        <id root="2.16.840.1.113883.4.1" extension="999887777"/>
        <patientPerson>
          <name use="L"><given>Rosalind</given><family>Okonkwo-Hale</family></name>
          <birthTime value="19551014"/>
          <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
        </patientPerson>
      </patient>
    </subject1></registrationEvent></subject>
  </controlActProcess>
</PRPA_IN201306UV02>`

func TestTheFieldTreeEndpointDescribesAPastedMessage(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/fields", map[string]any{"message": v3Sample})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var got fieldTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	// The interaction, because somebody who pasted the wrong message type should learn that here rather than from a
	// channel that never matches.
	if got.Interaction != "PRPA_IN201306UV02" {
		t.Errorf("interaction = %q", got.Interaction)
	}
	if len(got.Fields) < 8 {
		t.Errorf("only %d readable fields came back", len(got.Fields))
	}
	if got.Root.Name != "PRPA_IN201306UV02" {
		t.Errorf("root = %q", got.Root.Name)
	}

	// Every field has to carry a path, or the picker has nothing to give somebody when they click.
	for _, f := range got.Fields {
		if f.Path == "" {
			t.Errorf("%s came back with no path", f.Name)
		}
		for _, attr := range f.Attributes {
			if attr.Path == "" {
				t.Errorf("%s@%s came back with no path", f.Name, attr.Name)
			}
		}
	}
}

// TestEveryPathTheEndpointReturnsWorksThroughTheEndpoint closes the loop at the API level.
//
// The package test already proves a generated path resolves in Go. This proves it survives being serialised, sent, and fed back in
// through the checking endpoint - which is the actual journey a path takes when somebody clicks a field and then presses test.
func TestEveryPathTheEndpointReturnsWorksThroughTheEndpoint(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/fields", map[string]any{"message": v3Sample})
	if rec.Code != http.StatusOK {
		t.Fatalf("building the tree failed: %d", rec.Code)
	}

	var tree fieldTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}

	var checked int
	for _, field := range tree.Fields {
		type candidate struct {
			path string
			want string
		}
		var candidates []candidate

		if field.Value != "" {
			candidates = append(candidates, candidate{field.Path, field.Value})
		}
		for _, attr := range field.Attributes {
			candidates = append(candidates, candidate{attr.Path, attr.Value})
		}

		for _, c := range candidates {
			checked++

			rec := h.do("editor", http.MethodPost, "/api/hl7v3/path", map[string]any{
				"path":    c.path,
				"message": v3Sample,
			})
			if rec.Code != http.StatusOK {
				t.Errorf("checking %q returned %d", c.path, rec.Code)

				continue
			}

			var check v3PathCheckResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil {
				t.Fatal(err)
			}
			if !check.Valid {
				t.Errorf("the picker produced %q, which the checker rejects: %s", c.path, check.Error)

				continue
			}
			if len(check.Values) == 0 {
				t.Errorf("the picker produced %q for %q, which resolves to nothing", c.path, c.want)

				continue
			}
			if check.Values[0] != c.want {
				t.Errorf("%q was shown as %q but resolves to %q", c.path, c.want, check.Values[0])
			}
		}
	}

	if checked < 10 {
		t.Errorf("only %d paths were checked", checked)
	}
}

// TestAHalfTypedPathIsNotAnError covers the live feedback loop.
//
// This endpoint is called while somebody types, and half-finished input is the normal case. Answering 400 would make an interface
// show an error banner on every other keystroke, and people would turn the feature off.
func TestAHalfTypedPathIsNotAnError(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/path", map[string]any{
		"path":    "//patient/id(",
		"message": v3Sample,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("a half-typed path returned %d, want 200 with valid:false", rec.Code)
	}

	var got v3PathCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Error("an unclosed bracket was reported as a valid path")
	}
	if got.Error == "" {
		t.Error("no explanation was given, so the interface has nothing to show")
	}
	// Never null, or an interface counting the values throws while somebody is mid-word.
	if got.Values == nil {
		t.Error("values came back null rather than empty")
	}
}

// TestAValidPathWithNoSampleIsStillReportedValid covers checking syntax before pasting anything.
func TestAValidPathWithNoSampleIsStillReportedValid(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/path", map[string]any{
		"path": "//birthTime@value",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}

	var got v3PathCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid {
		t.Errorf("a correct path with no sample was reported invalid: %s", got.Error)
	}
	if len(got.Values) != 0 {
		t.Errorf("values came back with no message to read: %v", got.Values)
	}
}

// TestAValidPathThatMatchesNothingReturnsAnEmptyListNotNull covers the case the other tests miss.
//
// Both of the paths that return early send an explicit empty slice, so neither exercises the nil check on the real branch: a path
// that parses, against a message that parses, that simply matches nothing. That is also the commonest outcome while somebody
// explores - most paths they try are wrong.
//
// A JSON null here becomes an interface that has to check before it can count, and forgetting the check is how a picker throws
// mid-word. Found because planting the nil check away broke no test.
func TestAValidPathThatMatchesNothingReturnsAnEmptyListNotNull(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/path", map[string]any{
		"path":    "//deceasedTime@value",
		"message": v3Sample,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	// Asserted on the raw body, because unmarshalling turns a JSON null into a nil slice and both would satisfy a check
	// on the decoded struct. The interface sees the bytes.
	if strings.Contains(rec.Body.String(), `"values":null`) {
		t.Errorf("values came back as null: %s", rec.Body.String())
	}

	var got v3PathCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid {
		t.Errorf("a correct path was reported invalid: %s", got.Error)
	}
	if got.Values == nil {
		t.Error("values decoded to nil")
	}
	if len(got.Values) != 0 {
		t.Errorf("a path matching nothing returned %v", got.Values)
	}
	// And it must not claim the element exists.
	if got.Exists {
		t.Error("a path matching nothing reported that the element exists")
	}
}

// TestNullFlavorIsVisibleWhileWritingAFilter covers the distinction that matters most in v3.
//
// An element present with nullFlavor="ASKU" exists and has no value. Somebody writing a filter needs to see that while they write
// it, not discover it when a channel silently stops matching.
func TestNullFlavorIsVisibleWhileWritingAFilter(t *testing.T) {
	h := newHarness(t)

	const flavoured = `<msg xmlns="urn:hl7-org:v3">
	  <patientPerson><birthTime nullFlavor="ASKU"/></patientPerson>
	</msg>`

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/path", map[string]any{
		"path":    "//birthTime@value",
		"message": flavoured,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}

	var got v3PathCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Values) != 0 {
		t.Errorf("a null-flavoured birth date reported a value: %v", got.Values)
	}
	if !got.Exists {
		t.Error("the element reports as absent, so its reason is unreachable from the interface")
	}
	if got.NullFlavor != "ASKU" {
		t.Errorf("null flavour = %q, want ASKU", got.NullFlavor)
	}
}

// TestAnExternalEntityIsRefusedByTheSameParserAChannelUses covers XXE at the endpoint.
//
// This is a server parsing XML a caller supplied, so it is the obvious place to try to read a file off the disk.
func TestAnExternalEntityIsRefusedByTheSameParserAChannelUses(t *testing.T) {
	h := newHarness(t)

	const attack = `<?xml version="1.0"?>
<!DOCTYPE msg [<!ENTITY secret SYSTEM "file:///etc/passwd">]>
<msg xmlns="urn:hl7-org:v3"><patientPerson><family>&secret;</family></patientPerson></msg>`

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/fields", map[string]any{"message": attack})

	// Either refusing the document or parsing it with the entity unresolved is acceptable. Returning the file is not.
	body := rec.Body.String()
	for _, leak := range []string{"root:", "/bin/bash", "/bin/sh", "daemon:"} {
		if strings.Contains(body, leak) {
			t.Fatalf("an external entity was resolved and its content returned: %s", body)
		}
	}
}

// TestNonXmlIsRefusedWithSomethingReadable covers the commonest mistake.
func TestNonXmlIsRefusedWithSomethingReadable(t *testing.T) {
	h := newHarness(t)

	// An HL7 v2 message, which is exactly what somebody will paste by accident.
	rec := h.do("editor", http.MethodPost, "/api/hl7v3/fields", map[string]any{
		"message": "MSH|^~\\&|LAB|HOSP|EMR|HOSP|20260822||ADT^A01|1|P|2.5",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a v2 message returned %d, want 400", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("no explanation was given")
	}
}

// TestAnEmptyMessageSaysWhatToDo covers the first thing somebody sees.
func TestAnEmptyMessageSaysWhatToDo(t *testing.T) {
	h := newHarness(t)

	rec := h.do("editor", http.MethodPost, "/api/hl7v3/fields", map[string]any{"message": "   "})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "paste") {
		t.Errorf("the message does not say what to do: %s", rec.Body.String())
	}
}

// TestAViewerCanUseThePicker pins the role deliberately at viewer.
//
// These endpoints disclose nothing - a message the caller pasted back to them as a tree - and they live on the Playground tab,
// which viewers can reach. An editor floor would have put a tool on a page where using it answers 403.
//
// Pinned rather than left implicit, because "tools are editor" is a plausible-sounding rule that would tighten this back and
// produce exactly that broken page.
func TestAViewerCanUseThePicker(t *testing.T) {
	h := newHarness(t)

	rec := h.do("viewer", http.MethodPost, "/api/hl7v3/fields", map[string]any{"message": v3Sample})
	if rec.Code != http.StatusOK {
		t.Errorf("a viewer could not read a field tree: %d %s", rec.Code, rec.Body.String())
	}

	rec = h.do("viewer", http.MethodPost, "/api/hl7v3/path", map[string]any{
		"path":    "//birthTime@value",
		"message": v3Sample,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("a viewer could not test a path: %d %s", rec.Code, rec.Body.String())
	}
}

// TestThePickerStillNeedsAnAccount covers the floor that does exist.
func TestThePickerStillNeedsAnAccount(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/api/hl7v3/fields", strings.NewReader(`{"message":"<a/>"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an anonymous caller got %d, want 401", rec.Code)
	}
}
