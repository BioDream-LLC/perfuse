package xmldsig

import (
	"strings"
	"testing"
)

// Canonicalisation is judged by one question: do two byte sequences that mean the same thing produce identical
// output?
//
// That is the property signatures depend on, and it is testable without a reference implementation. Every case here
// is a pair of documents that any XML parser agrees are equivalent, asserting they canonicalise identically - or a
// pair that are genuinely different, asserting they do not.
//
// The failure mode being guarded against is quiet and expensive: a canonicaliser that is self-consistent but wrong
// produces signatures that verify against itself and fail against every other implementation, and the error message
// at the far end is "signature invalid" with nothing to say why.

func canon(t *testing.T, doc string, exclusive bool) string {
	t.Helper()
	out, err := Canonicalise([]byte(doc), exclusive, nil)
	if err != nil {
		t.Fatalf("canonicalising %q: %v", doc, err)
	}
	return string(out)
}

// same asserts two equivalent documents produce identical bytes.
func same(t *testing.T, why, a, b string) {
	t.Helper()
	for _, exclusive := range []bool{true, false} {
		ga, gb := canon(t, a, exclusive), canon(t, b, exclusive)
		if ga != gb {
			mode := "inclusive"
			if exclusive {
				mode = "exclusive"
			}
			t.Errorf("%s (%s):\n  %q\n  %q\ngave different bytes:\n  %q\n  %q", why, mode, a, b, ga, gb)
		}
	}
}

func TestEmptyElementsAreExpanded(t *testing.T) {
	// <a/> and <a></a> are the same element. One spelling has to win or nothing agrees.
	same(t, "an empty element written two ways", `<a/>`, `<a></a>`)

	if got := canon(t, `<a/>`, true); got != `<a></a>` {
		t.Errorf("canonical form of <a/> is %q, want <a></a>", got)
	}
}

func TestAttributeOrderDoesNotMatter(t *testing.T) {
	same(t, "attributes in a different order",
		`<a x="1" y="2" z="3"/>`,
		`<a z="3" x="1" y="2"/>`)

	// And the canonical order is by name, so both ends produce the same thing.
	if got := canon(t, `<a z="3" x="1" y="2"/>`, true); got != `<a x="1" y="2" z="3"></a>` {
		t.Errorf("attributes were not sorted: %q", got)
	}
}

func TestAttributeQuotingDoesNotMatter(t *testing.T) {
	same(t, "single and double quoted attributes", `<a x="1"/>`, `<a x='1'/>`)
}

func TestWhitespaceInsideATagDoesNotMatter(t *testing.T) {
	same(t, "extra whitespace inside a tag", `<a x="1"/>`, "<a\n   x=\"1\"\t/>")
}

func TestTheXMLDeclarationIsRemoved(t *testing.T) {
	same(t, "with and without an XML declaration",
		`<?xml version="1.0" encoding="UTF-8"?><a/>`,
		`<a/>`)
}

func TestADoctypeIsRemoved(t *testing.T) {
	// A signature must not depend on a DTD, because a DTD can change how a document parses.
	same(t, "with and without a doctype",
		`<!DOCTYPE a><a/>`,
		`<a/>`)
}

func TestCommentsAreRemoved(t *testing.T) {
	// Both forms used here are the without-comments variants: a signature covering comments breaks when anything in
	// the pipeline reformats the document, which intermediaries do constantly.
	same(t, "with and without a comment",
		`<a><!-- a note --><b/></a>`,
		`<a><b/></a>`)
}

func TestNamespacePrefixesAreSignificant(t *testing.T) {
	// Two documents differing only in prefix are different bytes and must have different signatures. A
	// canonicaliser that renamed prefixes would let somebody rewrite a document without breaking its signature.
	a := canon(t, `<p:a xmlns:p="urn:x"><p:b/></p:a>`, true)
	b := canon(t, `<q:a xmlns:q="urn:x"><q:b/></q:a>`, true)
	if a == b {
		t.Errorf("two different prefixes canonicalised identically to %q; a signature could not tell them apart", a)
	}
}

func TestCharacterDataIsEscapedTheSameWay(t *testing.T) {
	same(t, "a character reference and the character it denotes",
		`<a>&lt;</a>`,
		`<a>&#60;</a>`)

	// Greater-than is escaped even though XML does not require it, because the specification says so and both ends
	// have to do the same thing.
	if got := canon(t, `<a>1 > 0</a>`, true); !strings.Contains(got, "&gt;") {
		t.Errorf("greater-than was not escaped: %q", got)
	}
}

// A carriage return must become a character reference, not stay a literal byte.
//
// XML parsing normalises literal carriage returns away. Leaving one as a byte means the verifier's parser deletes it
// before hashing and computes a different digest from the signer - a failure that depends on which line endings the
// document happened to be saved with.
func TestCarriageReturnsBecomeCharacterReferences(t *testing.T) {
	got := canon(t, "<a>line&#xD;\nbreak</a>", true)
	if !strings.Contains(got, "&#xD;") {
		t.Errorf("a carriage return was not escaped: %q", got)
	}
}

// Tab, newline and carriage return in an attribute must be escaped for the same reason.
//
// Attribute-value normalisation turns them into spaces before the digest is computed, so leaving them literal makes
// two documents that differ invisibly produce the same digest - or the same document produce two.
func TestWhitespaceInAttributesIsEscaped(t *testing.T) {
	got := canon(t, "<a x=\"one&#x9;two&#xA;three\"/>", true)
	for _, want := range []string{"&#x9;", "&#xA;"} {
		if !strings.Contains(got, want) {
			t.Errorf("attribute whitespace was not escaped as %s: %q", want, got)
		}
	}
}

// Exclusive canonicalisation must not pull in a namespace the element never uses.
//
// This is the entire reason it exists. A CDA gets embedded in a SOAP envelope or an IHE metadata wrapper, which
// declares namespaces on an ancestor. Under inclusive rules those declarations enter the canonical form and the
// signature breaks - not because anything about the document changed, but because of what it was wrapped in.
func TestExclusiveIgnoresUnusedAncestorNamespaces(t *testing.T) {
	bare := `<doc xmlns="urn:hl7-org:v3"><title>T</title></doc>`
	wrapped := `<env:Envelope xmlns:env="urn:soap" xmlns:meta="urn:ihe:metadata">` +
		`<doc xmlns="urn:hl7-org:v3"><title>T</title></doc></env:Envelope>`

	plain, err := Canonicalise([]byte(bare), true, nil)
	if err != nil {
		t.Fatal(err)
	}

	inside, err := Canonicalise([]byte(wrapped), true, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The wrapped form contains the envelope too, so the document's own canonical bytes must appear inside it
	// unchanged. If the soap and metadata prefixes leaked onto the document element they would not.
	if !strings.Contains(string(inside), string(plain)) {
		t.Errorf("the document's canonical form changed when it was wrapped.\n  alone:   %s\n  wrapped: %s",
			plain, inside)
	}
}

// Inclusive canonicalisation of a subset must pull in ancestor declarations. Exclusive must not.
//
// This is where the two forms actually differ, and finding that out corrected a mistake in these tests. My first
// version compared the two forms over a whole document and expected inclusive to repeat the envelope's declaration
// onto the inner element. It does not, and should not: the declaration is written once on the envelope and is in
// scope for everything below it, so repeating it would be wrong in both forms.
//
// The difference appears only when canonicalising a subset, because then the ancestor's declaration is outside what
// is being canonicalised and has to be either carried in or left out. That is precisely what signing an element
// inside a SOAP envelope does, so the subset case is the one that matters and the whole-document comparison was
// testing nothing.
func TestInclusiveCarriesAncestorNamespacesIntoASubsetAndExclusiveDoesNot(t *testing.T) {
	wrapped := `<env:Envelope xmlns:env="urn:soap" xmlns:meta="urn:ihe">` +
		`<doc xmlns="urn:hl7-org:v3" ID="d1"><title>T</title></doc></env:Envelope>`

	exc, err := CanonicaliseElement([]byte(wrapped), "d1", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	inc, err := CanonicaliseElement([]byte(wrapped), "d1", false, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Exclusive: only what the element uses. The envelope's prefixes are not among them.
	if strings.Contains(string(exc), "urn:soap") || strings.Contains(string(exc), "urn:ihe") {
		t.Errorf("exclusive canonicalisation pulled in namespaces the element never uses: %s", exc)
	}

	// Inclusive: everything in scope, which is why a signature made this way breaks when the wrapper changes.
	if !strings.Contains(string(inc), "urn:soap") {
		t.Errorf("inclusive canonicalisation did not carry in the ancestor declaration: %s", inc)
	}
}

// Signing an element must produce the same bytes however it is wrapped.
//
// This is the property the whole exclusive form exists to provide, and the one that makes a signature survive being
// put inside a SOAP envelope, an IHE metadata wrapper, or a v2 message.
func TestAnElementCanonicalisesIdenticallyHoweverItIsWrapped(t *testing.T) {
	alone := `<doc xmlns="urn:hl7-org:v3" ID="d1"><title>T</title></doc>`
	wrapped := `<env:Envelope xmlns:env="urn:soap">` + alone + `</env:Envelope>`
	deeper := `<a xmlns:q="urn:other"><b><c>` + alone + `</c></b></a>`

	first, err := CanonicaliseElement([]byte(alone), "d1", true, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range []string{wrapped, deeper} {
		got, err := CanonicaliseElement([]byte(doc), "d1", true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(first) {
			t.Errorf("wrapping changed the signed bytes:\n  alone:   %s\n  wrapped: %s", first, got)
		}
	}
}

// A reference to an element that is not there must be refused.
//
// Silently canonicalising the whole document instead would produce a signature over something other than what was
// asked for, and it would verify - against the wrong content.
func TestAMissingElementIsRefused(t *testing.T) {
	if _, err := CanonicaliseElement([]byte(`<a ID="x"/>`), "nope", true, nil); err == nil {
		t.Error("a reference to an element that does not exist was accepted")
	}
}

// Two elements with the same ID must be refused.
//
// This is a signature-wrapping attack: an attacker adds a second element with the ID the signature references, and a
// verifier that takes the first match validates the original while the application reads the second.
func TestADuplicateIDIsRefused(t *testing.T) {
	doc := `<r><a ID="d1"><t>original</t></a><a ID="d1"><t>substituted</t></a></r>`
	if _, err := CanonicaliseElement([]byte(doc), "d1", true, nil); err == nil {
		t.Error("two elements sharing an ID were accepted, which is how signature wrapping works")
	}
}

// A redundant redeclaration must not be written twice.
//
// A document that declares the same namespace on parent and child is equivalent to one that declares it only on the
// parent, and both must canonicalise the same way.
func TestARedundantRedeclarationIsDropped(t *testing.T) {
	same(t, "a namespace redeclared identically on a child",
		`<a xmlns="urn:x"><b xmlns="urn:x"/></a>`,
		`<a xmlns="urn:x"><b/></a>`)
}

// An element in no namespace inside one that has a default namespace must undeclare it.
//
// Without the undeclaration the canonical form says the child is in its parent's namespace, which is a different
// document - and a verifier reading the canonical bytes would be checking a signature over something the signer did
// not sign.
func TestADefaultNamespaceIsUndeclaredWhenAChildLeavesIt(t *testing.T) {
	out := canon(t, `<a xmlns="urn:x"><b xmlns=""/></a>`, true)
	if !strings.Contains(out, `<b xmlns="">`) {
		t.Errorf("the default namespace was not undeclared on the child: %q", out)
	}
}

// Content whitespace is significant and must survive exactly.
//
// Two documents differing only in whitespace between elements are different documents to a signature, and a
// canonicaliser that trimmed it would let somebody reformat a signed document without invalidating it.
func TestContentWhitespaceIsPreserved(t *testing.T) {
	withSpace := canon(t, "<a> <b/> </a>", true)
	without := canon(t, "<a><b/></a>", true)
	if withSpace == without {
		t.Error("whitespace between elements was discarded, so a signed document could be reformatted freely")
	}
	if !strings.Contains(withSpace, "> <b") {
		t.Errorf("the whitespace was not preserved exactly: %q", withSpace)
	}
}

// A prefix used and never declared is a broken document and must be refused, not guessed at.
func TestAnUndeclaredPrefixIsRefused(t *testing.T) {
	if _, err := Canonicalise([]byte(`<p:a><b/></p:a>`), true, nil); err == nil {
		t.Error("a document using an undeclared prefix was canonicalised anyway")
	}
}

// Truncated XML must be refused rather than producing a partial canonical form.
//
// A partial form would hash successfully and produce a signature over half a document.
func TestUnclosedElementsAreRefused(t *testing.T) {
	if _, err := Canonicalise([]byte(`<a><b></a>`), true, nil); err == nil {
		t.Error("mismatched tags were accepted")
	}
	if _, err := Canonicalise([]byte(`<a><b>`), true, nil); err == nil {
		t.Error("an unclosed element was accepted, which would sign half a document")
	}
}

// Canonicalising twice must produce the same bytes, or nothing downstream can be trusted.
func TestCanonicalisationIsStable(t *testing.T) {
	doc := `<?xml version="1.0"?><a xmlns="urn:x" z="3" x="1"><!-- c --><b y='2'/><c>text &amp; more</c></a>`

	once := canon(t, doc, true)
	twice := canon(t, once, true)
	if once != twice {
		t.Errorf("canonicalising a canonical document changed it:\n  %q\n  %q", once, twice)
	}
}

// The xml prefix is bound by definition and must never be declared.
func TestTheXmlPrefixIsNeverDeclared(t *testing.T) {
	out := canon(t, `<a xml:lang="en"><b/></a>`, true)
	if strings.Contains(out, "xmlns:xml") {
		t.Errorf("the reserved xml prefix was declared: %q", out)
	}
	if !strings.Contains(out, `xml:lang="en"`) {
		t.Errorf("the xml:lang attribute was lost: %q", out)
	}
}

// An attribute in a namespace sorts by that namespace's URI, not by its prefix.
//
// Prefixes are arbitrary. Sorting by prefix means two documents that differ only in their choice of prefix produce
// attributes in different orders, and their canonical forms differ for no meaningful reason.
func TestNamespacedAttributesSortByURINotPrefix(t *testing.T) {
	// Same document, prefixes swapped so that alphabetical prefix order and alphabetical URI order disagree.
	a := canon(t, `<r xmlns:aa="urn:z" xmlns:zz="urn:a" aa:x="1" zz:y="2"/>`, true)
	b := canon(t, `<r xmlns:zz="urn:a" xmlns:aa="urn:z" zz:y="2" aa:x="1"/>`, true)
	if a != b {
		t.Errorf("attribute order depended on how they were written:\n  %q\n  %q", a, b)
	}

	// urn:a sorts before urn:z, so the zz-prefixed attribute must come first.
	if strings.Index(a, `zz:y`) > strings.Index(a, `aa:x`) {
		t.Errorf("attributes were sorted by prefix rather than by namespace URI: %q", a)
	}
}
