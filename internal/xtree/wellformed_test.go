package xtree

import "testing"

// TestWellFormedIsStricterThanParse is the point of having both.
//
// Parse sets Strict to false so that a document from another vendor with a minor non-conformance is read rather than refused,
// which is the right trade for inbound traffic. That same tolerance made it useless as a check on outbound documents: the
// transformer guard in the engine was built on Parse, and it passed a document containing an element named with spaces - the exact
// case its own comment said it existed to catch. The receiver would have been the one to reject it.
//
// So the property worth locking is not that WellFormed rejects bad XML, but that the two disagree, because a future change
// collapsing them back into one function would silently restore the defect.
func TestWellFormedIsStricterThanParse(t *testing.T) {
	// What a script produces when it names an element with a space, which append allows because the check belongs at the end of
	// the stage rather than in every writer.
	const malformed = `<Message>
  <not a valid name/>
</Message>`

	if _, err := Parse([]byte(malformed)); err != nil {
		t.Fatalf("Parse is meant to tolerate this so that inbound documents are not refused over syntax: %v", err)
	}

	if err := WellFormed([]byte(malformed)); err == nil {
		t.Error("WellFormed accepted an element name containing spaces; every conforming parser rejects it, so a receiver " +
			"would refuse the message and the fault would look like theirs")
	}
}

// TestWellFormedAcceptsWhatMarshalProduces guards the other direction.
//
// A check that refused ordinary output would be worse than none: it would fail every transformed document and the fix somebody
// reached for would be to remove the check.
func TestWellFormedAcceptsWhatMarshalProduces(t *testing.T) {
	root := New("Message")
	body := New("Body")
	body.Append(New("Given"))
	root.Append(body)

	patient := New("Patient")
	patient.SetAttr("id", "1234")
	patient.Append(New("Name"))
	root.Append(patient)

	// Text needing escaping, since an ampersand written straight through would make the output malformed and this check is what
	// would have to notice.
	root.Ensure("Note", 0).Text = `Smith & Sons <clinic>`

	out := root.Marshal(2)

	if err := WellFormed(out); err != nil {
		t.Fatalf("WellFormed rejected this package's own output, which would fail every transformed document: %v\n%s",
			err, out)
	}
}

// TestWellFormedRefusesTruncatedDocuments covers the ordinary failure.
//
// A transformer that returns early, or a serialiser that stops mid-write, produces something that opens elements it never closes.
//
// Not every violation of the specification is here, because this is a token-level check and some are not visible at that level.
// Character data before the root element is the one worth naming: it is not well-formed, and the decoder tokenises it without
// complaint, so a case asserting it is refused will fail. That is a limit of the check rather than something to fix here - the
// failure mode it exists for is a document a script damaged in the middle, not one with a stray byte at the front.
func TestWellFormedRefusesTruncatedDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"unclosed element":   `<Message><Body>`,
		"mismatched close":   `<Message><Body></Header></Message>`,
		"unquoted attribute": `<Message id=1/>`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := WellFormed([]byte(doc)); err == nil {
				t.Errorf("WellFormed accepted %q", doc)
			}
		})
	}
}
