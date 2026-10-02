package hl7v3

import (
	"encoding/json"
	"testing"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// A document with structure and no values still flattens to a list, not to null.
//
// FlattenFieldTree appends only for nodes that carry a value, an attribute or a null flavour, because a
// pure-structure node is noise in a search result. That makes the empty case reachable: a document of
// nothing but nested empty elements - a skeleton, a template, the thing somebody pastes to see what the
// picker does before they have a real message - produced a nil slice.
//
// A nil slice marshals to JSON null. The interface declares this field a non-nullable array and calls
// filter on it, so that paste crashed the field picker. Same defect as the FHIR lab's Decisions view,
// and again triggered by an input with nothing wrong with it.
func TestFlattenFieldTreeReturnsEmptyListForAStructureOnlyDocument(t *testing.T) {
	// Deliberately no attributes, no text and no null flavours anywhere.
	skeleton := `<ClinicalDocument xmlns="urn:hl7-org:v3">
		<component>
			<structuredBody>
				<component>
					<section></section>
				</component>
			</structuredBody>
		</component>
	</ClinicalDocument>`

	root, err := xtree.Parse([]byte(skeleton))
	if err != nil {
		t.Fatalf("parsing the skeleton failed: %v", err)
	}

	tree, err := BuildFieldTree(root, FieldTreeOptions{MaxNodes: 4000})
	if err != nil {
		t.Fatalf("building the tree failed: %v", err)
	}

	fields := FlattenFieldTree(tree)

	// The point of the test: empty, and specifically not nil.
	if fields == nil {
		t.Fatal("FlattenFieldTree returned nil, which marshals to JSON null; the interface declares " +
			"this a list and filters it, so a structure-only document would crash the field picker")
	}
	if len(fields) != 0 {
		t.Fatalf("expected no readable fields in a structure-only document, got %d", len(fields))
	}

	// And prove it on the wire, which is where the defect actually bit.
	encoded, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"fields":[]}` {
		t.Errorf("serialised as %s, want {\"fields\":[]}", got)
	}
}

// The ordinary case still works, so the fix above did not achieve emptiness by breaking the feature.
func TestFlattenFieldTreeStillFindsReadableFields(t *testing.T) {
	doc := `<ClinicalDocument xmlns="urn:hl7-org:v3">
		<patient>
			<name>Doe</name>
			<birthTime value="19800101"/>
		</patient>
	</ClinicalDocument>`

	root, err := xtree.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := BuildFieldTree(root, FieldTreeOptions{MaxNodes: 4000})
	if err != nil {
		t.Fatal(err)
	}

	fields := FlattenFieldTree(tree)
	if len(fields) == 0 {
		t.Fatal("a document with a name and a birth time produced no readable fields")
	}
}
