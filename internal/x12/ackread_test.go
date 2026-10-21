package x12

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 999 CMS publishes in its esMD 278 companion guide, rejecting a 278 for a bad N4 in the subscriber's loop, reads as that.
func TestThePublished999RejectionIsReadAsOne(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "esmd", "ack-999-rejected.x12"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsAcknowledgement() {
		t.Fatal("not recognised as an acknowledgement")
	}
	a, err := m.ReadAcknowledgement()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Rejected() || a.Kind != "999" || len(a.Groups) != 1 {
		t.Fatalf("%+v", a)
	}
	g := a.Groups[0]
	if g.Functional != "HI" || g.ControlNumber != "121822" || g.Status != "R" || len(g.Sets) != 1 {
		t.Fatalf("group %+v", g)
	}
	s := g.Sets[0]
	if s.ID != "278" || s.ControlNumber != "121822" || s.Status != "R" || len(s.Errors) != 3 {
		t.Fatalf("set %+v", s)
	}
	if e := s.Errors[1]; e.Segment != "N4" || e.Position != "26" || e.Element != "3" || e.Code != "7" || e.Value != "21244" {
		t.Errorf("element error %+v", e)
	}
	if got := a.Summary(); !strings.Contains(got, "rejected") || !strings.Contains(got, "N403 at segment 26 code 7") {
		t.Errorf("summary %q", got)
	}
}

// A TA1 alone is an acknowledgement; its note code is said in words.
func TestATA1IsReadWithItsNote(t *testing.T) {
	raw := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*CLINIC         *261021*1200*^*00501*000000777*0*T*:~" +
		"TA1*000000123*261021*1200*R*025~IEA*0*000000777~"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.ReadAcknowledgement()
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsAcknowledgement() || !a.Rejected() || a.Interchange.ControlNumber != "000000123" {
		t.Fatalf("%+v", a)
	}
	if got := a.Summary(); got != "TA1: interchange 123 rejected (TA1 note 025: duplicate interchange control number)" {
		t.Errorf("summary %q", got)
	}
	// A 278 is not one.
	r, _ := os.ReadFile(filepath.Join("testdata", "esmd", "response-certified.x12"))
	m, _ = Parse(r)
	if m.IsAcknowledgement() {
		t.Error("a 278 response read as an acknowledgement")
	}
}

// The TA1 this package writes for mismatched ISA/IEA control numbers says 001, the code for exactly that.
func TestATA1ForMismatchedControlNumbersSays001(t *testing.T) {
	if got := ta1NoteCode([]Problem{{Segment: "IEA", Code: CodeControlNumberMismatch}}); got != "001" {
		t.Fatalf("%s", got)
	}
}
