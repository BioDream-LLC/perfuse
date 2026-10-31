package mirth

import (
	"strings"
	"testing"
)

// TestADeeplyNestedImportIsRefused guards the import against a document built to exhaust the stack.
func TestADeeplyNestedImportIsRefused(t *testing.T) {
	deep := strings.Repeat("<a>", 200000) + strings.Repeat("</a>", 200000)
	if _, err := parseTree(strings.NewReader(deep)); err == nil {
		t.Fatal("a document nested 200,000 levels deep was parsed")
	}
	ok := strings.Repeat("<a>", 50) + "x" + strings.Repeat("</a>", 50)
	if _, err := parseTree(strings.NewReader(ok)); err != nil {
		t.Fatalf("an ordinary nesting was refused: %v", err)
	}
}
