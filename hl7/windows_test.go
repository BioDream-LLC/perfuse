package hl7

import "testing"

// TestCRLFFromAWindowsEditor covers a file that has been through a Windows text editor.
//
// HL7 separates segments with a carriage return alone. Every Windows editor writes a carriage return and a line feed, so
// a message that somebody has opened and saved - to look at it, or to fix one field by hand - arrives with an extra byte
// after every segment. That is not a rare accident: it is what happens the first time anyone inspects a file.
//
// Handled deliberately in message.go, which treats a differing second byte as part of one terminator rather than as an
// empty segment. Guarded here because hospitals run Windows, and the failure would be a field reading "M\n" - unequal
// to every expected value downstream and identical to "M" in a log.
func TestCRLFFromAWindowsEditor(t *testing.T) {
	// What a Windows text editor produces when somebody opens an HL7 file and saves it.
	msg := []byte("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|W001|P|2.5.1\r\nPID|1||MRN7|| WINDOWS^TEST||19800101|M\r\n")
	m, err := Parse(msg)
	if err != nil {
		t.Fatalf("Parse of a CRLF message failed: %v", err)
	}
	t.Logf("control id %q", m.ControlID())
	t.Logf("PID segments %d", len(m.Segments("PID")))
	// The last field of a segment is where a retained newline would hide. A sex field reading "M\n" instead of "M"
	// compares unequal to every expected value downstream and prints identically in a log.
	if got := m.MustGet("PID-8"); got != "M" {
		t.Errorf("PID-8 read %q, want M - a retained newline", got)
	}
}
