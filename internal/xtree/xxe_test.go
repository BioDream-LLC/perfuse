package xtree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAnExternalEntityCannotReadAFile is the XXE case.
//
// Clinical documents arrive from outside - a CDA in an HL7 message, a SOAP body - so a document that could make the parser
// read a file would be handed to us by whoever sends the messages.
//
// Verified against a real file rather than by reading the decoder's settings, because "Entity is set to HTMLEntity" is a
// claim about behaviour and this is the behaviour.
func TestAnExternalEntityCannotReadAFile(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("SENSITIVE-FILE-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	doc := `<?xml version="1.0"?>
<!DOCTYPE root [
  <!ENTITY xxe SYSTEM "file://` + secret + `">
]>
<ClinicalDocument><title>&xxe;</title></ClinicalDocument>`

	node, err := Parse([]byte(doc))
	if err != nil {
		// Refusing is a perfectly good outcome.
		return
	}

	rendered := render(node)
	if strings.Contains(rendered, "SENSITIVE-FILE-CONTENT") {
		t.Errorf("an external entity read a file off disk:\n%s", rendered)
	}
}

// TestAnEntityCannotReachTheNetwork covers the other half of XXE.
//
// A document that could make the parser fetch a URL would be a way to scan the internal network from outside, and to reach
// the metadata address that internal/egress exists to block.
func TestAnEntityCannotReachTheNetwork(t *testing.T) {
	doc := `<?xml version="1.0"?>
<!DOCTYPE root [
  <!ENTITY xxe SYSTEM "http://169.254.169.254/latest/meta-data/">
]>
<ClinicalDocument><title>&xxe;</title></ClinicalDocument>`

	started := time.Now()
	node, err := Parse([]byte(doc))
	elapsed := time.Since(started)

	// A network fetch would take far longer than parsing a few hundred bytes, so the time is itself the assertion: a
	// connection attempt to a link-local address either connects or waits.
	if elapsed > 2*time.Second {
		t.Errorf("parsing took %s, which suggests the parser tried to fetch the entity", elapsed)
	}
	if err != nil {
		return
	}
	if strings.Contains(render(node), "ami-") {
		t.Error("the parser appears to have fetched something")
	}
}

// TestBillionLaughsDoesNotExpand covers the denial of service.
//
// Nested entities that each reference the previous one expand exponentially. A document a few hundred bytes long can become
// gigabytes, and on an integration engine that takes down the interface for a whole hospital.
func TestBillionLaughsDoesNotExpand(t *testing.T) {
	doc := `<?xml version="1.0"?>
<!DOCTYPE lolz [
  <!ENTITY lol "lol">
  <!ENTITY lol1 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
  <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
  <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
  <!ENTITY lol4 "&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;">
  <!ENTITY lol5 "&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;&lol4;">
  <!ENTITY lol6 "&lol5;&lol5;&lol5;&lol5;&lol5;&lol5;&lol5;&lol5;&lol5;&lol5;">
  <!ENTITY lol7 "&lol6;&lol6;&lol6;&lol6;&lol6;&lol6;&lol6;&lol6;&lol6;&lol6;">
  <!ENTITY lol8 "&lol7;&lol7;&lol7;&lol7;&lol7;&lol7;&lol7;&lol7;&lol7;&lol7;">
  <!ENTITY lol9 "&lol8;&lol8;&lol8;&lol8;&lol8;&lol8;&lol8;&lol8;&lol8;&lol8;">
]>
<lolz>&lol9;</lolz>`

	started := time.Now()
	node, err := Parse([]byte(doc))
	elapsed := time.Since(started)

	if elapsed > 2*time.Second {
		t.Fatalf("parsing took %s, so entities are being expanded", elapsed)
	}
	if err != nil {
		return
	}

	// A hundred million characters would be the full expansion. Anything remotely near it means expansion happened.
	if size := len(render(node)); size > 100_000 {
		t.Errorf("the document expanded to %d bytes from under a kilobyte", size)
	}
}

// TestADeeplyNestedDocumentDoesNotExhaustTheStack covers nesting rather than entities.
//
// A few thousand open tags is a small document to send and an unbounded recursion to parse.
func TestADeeplyNestedDocumentDoesNotExhaustTheStack(t *testing.T) {
	const depth = 50_000

	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString("<a>")
	}
	b.WriteString("x")
	for i := 0; i < depth; i++ {
		b.WriteString("</a>")
	}

	// The assertion is that this returns at all rather than crashing the process. A stack overflow in Go is not
	// recoverable, so a panic here would take down a server rather than fail one message.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Parse([]byte(b.String()))
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Error("parsing a deeply nested document did not finish")
	}
}

// render walks a tree into text, for asserting what a document produced.
func render(n *Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*Node)
	walk = func(node *Node) {
		if node == nil {
			return
		}
		b.WriteString(node.Text)
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}
