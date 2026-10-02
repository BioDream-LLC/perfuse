package spec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// AUDIT ITEM 6: Rendered output must be DETERMINISTIC.
// Maps range randomly in Go, so anything derived from a map must be sorted.

func TestMarkdownRenderingIsDeterministic(t *testing.T) {
	// A channel with multiple fields and a mapping table - the places where map iteration order matters.
	yaml := `
name: determinism-test
source:
  type: mllp
  listen: ":6661"
  ack:
    when: on_delivery
filter: MSH-9.1 == "ADT" and PID-3.1 != "" and PV1-2 in ["I","O","E"]
transformations:
  - map:
      path: PID-8
      table: {"M": "MALE", "F": "FEMALE", "O": "OTHER", "U": "UNKNOWN", "A": "AMBIGUOUS"}
  - copy:
      from: PID-18
      to: PV1-19
  - set:
      path: MSH-3
      value: PERFUSE
destinations:
  - name: archive
    type: file
    dir: /tmp/out
    queue:
      enabled: true
  - name: downstream
    type: mllp
    address: host.example.invalid:6661
    filter: MSH-9.2 in ["A01", "A04", "A08"]
`
	first := buildMarkdown(t, yaml)
	if first == "" {
		t.Fatal("Markdown() returned empty")
	}

	for i := 0; i < 20; i++ {
		next := buildMarkdown(t, yaml)
		if first != next {
			t.Fatalf("iteration %d: Markdown rendering is not deterministic; two runs differ.\nFirst 200 chars of diff area:\n%s\nvs\n%s",
				i, first[:min(200, len(first))], next[:min(200, len(next))])
		}
	}
}

func TestJSONRenderingIsDeterministic(t *testing.T) {
	yaml := `
name: determinism-test
source:
  type: mllp
  listen: ":6661"
filter: MSH-9.1 == "ADT" and PID-3.1 != ""
transformations:
  - map:
      path: PID-8
      table: {"M": "MALE", "F": "FEMALE", "O": "OTHER", "U": "UNKNOWN"}
destinations:
  - name: archive
    type: file
    dir: /tmp/out
`
	doc := buildDoc(t, yaml)
	first, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		doc2 := buildDoc(t, yaml)
		next, err := json.Marshal(doc2)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(next) {
			t.Fatalf("iteration %d: JSON rendering is not deterministic", i)
		}
	}
}

// AUDIT ITEM 8: Escaping in Markdown output.
// A field description containing a pipe character must not break the table.

func TestMarkdownEscapesPipeInFieldDescription(t *testing.T) {
	// MSH-1 is "The character separating fields, usually |" per the HL7 dictionary.
	// If the filter references MSH-1, its description will be placed in a Markdown table cell.
	yaml := `
name: escape-test
source:
  type: mllp
  listen: ":6661"
filter: MSH-1 == "|"
destinations:
  - name: out
    type: file
    dir: /tmp/out
`
	md := buildMarkdown(t, yaml)

	// A raw pipe in a table cell splits the row. The escaped form is \|.
	// Count pipe-delimited cells in the field table rows.
	lines := strings.Split(md, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "| `MSH-1`") {
			continue
		}
		// A correctly escaped row has exactly 5 pipe delimiters (4 cells + leading + trailing).
		// An unescaped pipe in the description would produce more.
		// Count unescaped pipes: pipes not preceded by backslash.
		unescaped := 0
		for i, r := range line {
			if r == '|' && (i == 0 || line[i-1] != '\\') {
				unescaped++
			}
		}
		if unescaped != 5 {
			t.Errorf("MSH-1 row has %d unescaped pipes (want 5); the description contains a raw pipe that breaks the table:\n%s",
				unescaped, line)
		}
		return
	}
	// If MSH-1 is not in Reads, the test is vacuous - verify it is there.
	t.Log("MSH-1 was not found in the rendered field table; checking if it appears in the doc at all")
	doc := buildDoc(t, yaml)
	found := false
	for _, r := range doc.Reads {
		if r.Path == "MSH-1" {
			found = true
			break
		}
	}
	if !found {
		t.Skip("MSH-1 not referenced by this filter expression; cannot test pipe escaping this way")
	} else {
		t.Error("MSH-1 is in Reads but not rendered in Markdown")
	}
}

func TestMarkdownEscapesPipeInMappingValues(t *testing.T) {
	// User-provided mapping values could contain pipes.
	yaml := `
name: pipe-in-mapping
source:
  type: mllp
  listen: ":6661"
transformations:
  - map:
      path: PID-8
      table: {"A|B": "C|D"}
destinations:
  - name: out
    type: file
    dir: /tmp/out
`
	md := buildMarkdown(t, yaml)

	// The mapping table row should have escaped pipes in the values.
	if strings.Contains(md, "| `A|B`") || strings.Contains(md, "| `C|D`") {
		t.Errorf("mapping values contain raw pipes inside table cells, which breaks rendering in some Markdown parsers:\n%s",
			md)
	}
	// Verify the escaped form is present.
	if !strings.Contains(md, `A\|B`) {
		t.Errorf("expected escaped pipe in mapping From value, got:\n%s", md)
	}
	if !strings.Contains(md, `C\|D`) {
		t.Errorf("expected escaped pipe in mapping To value, got:\n%s", md)
	}
}

// Helpers

func buildDoc(t *testing.T, yaml string) *Document {
	t.Helper()
	c, err := config.Load(strings.NewReader(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return Build(c)
}

func buildMarkdown(t *testing.T, yaml string) string {
	t.Helper()
	doc := buildDoc(t, yaml)
	return doc.Markdown()
}
