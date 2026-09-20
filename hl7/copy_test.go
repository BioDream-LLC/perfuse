package hl7

import (
	"bytes"
	"testing"
)

// The message a caller reads first, and the one that overwrites it in a reused buffer. Same length, so the second
// read fills the buffer exactly and nothing is left over to make the corruption obvious.
var (
	firstPatient  = []byte("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|0001|P|2.5.1\rPID|1||MRN0001^^^A^MR||FROST^IVY||19910228|F\r")
	secondPatient = []byte("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|0002|P|2.5.1\rPID|1||MRN0002^^^A^MR||HAYES^ROY||19640715|M\r")
)

func TestTheDefaultSurvivesTheCallerReusingItsBuffer(t *testing.T) {
	// This is the whole reason the default copies. Reading into a reused buffer is the normal, efficient way to
	// read from a network in Go, so this pattern is what careful code looks like - and without the copy the
	// message quietly starts reporting the next patient's details under the first patient's name.
	buf := make([]byte, len(firstPatient))
	copy(buf, firstPatient)

	msg, err := Parse(buf)
	if err != nil {
		t.Fatal(err)
	}

	// The caller reuses its buffer for the next message, as it is entitled to.
	copy(buf, secondPatient)

	if got := msg.MustGet("PID-5.1"); got != "FROST" {
		t.Errorf("after the caller reused its buffer the name read %q, want FROST", got)
	}
	if got := msg.MustGet("PID-3.1"); got != "MRN0001" {
		t.Errorf("the MRN read %q, want MRN0001 - this is one patient's data under another's name", got)
	}
	if got := msg.ControlID(); got != "0001" {
		t.Errorf("the control ID read %q, want 0001", got)
	}
}

func TestZeroCopyAliasesTheCallerBuffer(t *testing.T) {
	// Documenting the hazard in a test rather than only in a comment, so that if someone ever makes zero-copy the
	// default again, this test tells them exactly what they have signed up for.
	buf := make([]byte, len(firstPatient))
	copy(buf, firstPatient)

	msg, err := Parse(buf, WithZeroCopy())
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.MustGet("PID-5.1"); got != "FROST" {
		t.Fatalf("before any reuse the name read %q, want FROST", got)
	}

	copy(buf, secondPatient)

	// Not asserting a specific wrong value - the point is only that zero-copy aliases the caller's memory, so the
	// Message is no longer trustworthy once that memory changes.
	if got := msg.MustGet("PID-5.1"); got == "FROST" {
		t.Error("zero-copy appears to have copied after all; the option or the default has changed")
	}
}

func TestZeroCopyReallyAvoidsTheCopy(t *testing.T) {
	// Cheap structural check that the option does what it says: the Message's raw bytes should be the same backing
	// array as the input, not an equal-looking copy.
	buf := make([]byte, len(firstPatient))
	copy(buf, firstPatient)

	msg, err := Parse(buf, WithZeroCopy())
	if err != nil {
		t.Fatal(err)
	}
	raw := msg.Raw()
	if len(raw) == 0 || &raw[0] != &buf[0] {
		t.Error("WithZeroCopy still copied the input")
	}
}

func TestTheDefaultReallyCopies(t *testing.T) {
	buf := make([]byte, len(firstPatient))
	copy(buf, firstPatient)

	msg, err := Parse(buf)
	if err != nil {
		t.Fatal(err)
	}
	raw := msg.Raw()
	if len(raw) == 0 {
		t.Fatal("no raw bytes")
	}
	if &raw[0] == &buf[0] {
		t.Error("Parse aliased the caller's buffer; the default is supposed to copy")
	}
	// Compared against the input with its trailing segment terminator removed, which is what Parse retains: the
	// final separator terminates the last segment rather than being part of it. Asserting equality with the raw
	// input was wrong and said so only once the tests were running again.
	want := bytes.TrimRight(firstPatient, "\r")
	if !bytes.Equal(raw, want) {
		t.Errorf("the copy is %q, want %q", raw, want)
	}
}

func TestParseStringNeverNeedsToCopy(t *testing.T) {
	// A string is immutable, so the conversion to a slice has already made the only copy required and a second one
	// would be pure waste on the commonest path in tests and scripts.
	msg, err := ParseString(string(firstPatient))
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.MustGet("PID-5.1"); got != "FROST" {
		t.Errorf("name read %q, want FROST", got)
	}
}

func TestOptionsAreAdditiveAndNilSafe(t *testing.T) {
	// Parse(raw) must keep working unchanged - that is the entire reason the signature is variadic - and a nil
	// option in a slice built by a caller's own logic must not panic.
	if _, err := Parse(firstPatient); err != nil {
		t.Errorf("Parse with no options failed: %v", err)
	}
	if _, err := Parse(firstPatient, nil); err != nil {
		t.Errorf("Parse with a nil option failed: %v", err)
	}
	if _, err := Parse(firstPatient, WithZeroCopy(), WithZeroCopy()); err != nil {
		t.Errorf("Parse with a repeated option failed: %v", err)
	}
}
