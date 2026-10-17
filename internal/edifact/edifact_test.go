package edifact

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An ORDERS D.96A interchange in the shape of the UN/EDIFACT examples, with a released apostrophe and plus in free text.
const orders = "UNA:+.? '" +
	"UNB+UNOC:3+5412345000013:14+4012345000009:14+261017:1200+REF0001++ORDERS'\n" +
	"UNH+1+ORDERS:D:96A:UN:EAN008'\n" +
	"BGM+220+PO12345+9'\n" +
	"DTM+137:20261017:102'\n" +
	"NAD+BY+5412345000013::9'\n" +
	"NAD+SU+4012345000009::9'\n" +
	"FTX+AAI+++Deliver to the dock?'s rear door?+gate 2'\n" +
	"LIN+1++4000862141404:SRS'\n" +
	"QTY+21:48'\n" +
	"LIN+2++4000862141411:SRS'\n" +
	"QTY+21:12'\n" +
	"UNS+S'\n" +
	"CNT+2:2'\n" +
	"UNT+13+1'\n" +
	"UNH+2+ORDERS:D:96A:UN:EAN008'\n" +
	"BGM+220+PO12346+9'\n" +
	"NAD+BY+5412345000013::9'\n" +
	"UNT+4+2'\n" +
	"UNZ+2+REF0001'\n"

func TestParseAndCheck(t *testing.T) {
	ic, err := Parse([]byte(orders))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ic.AllMessages()); got != 2 {
		t.Fatalf("%d messages", got)
	}
	if p := Check(ic); len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
	m := ic.AllMessages()[0]
	if m.Ref() != "1" || m.Type() != "ORDERS:D:96A:UN" {
		t.Errorf("ref %q type %q", m.Ref(), m.Type())
	}
	if s, q := ic.Sender(); s != "5412345000013" || q != "14" {
		t.Errorf("sender %s %s", s, q)
	}
	read := func(path string) []string {
		p, err := ParsePath(path)
		if err != nil {
			t.Fatal(err)
		}
		return Read(ic, p)
	}
	if v := read("BGM-2"); len(v) != 2 || v[0] != "PO12345" || v[1] != "PO12346" {
		t.Errorf("BGM-2 %v", v)
	}
	if v := read("NAD(2)-2.1"); len(v) != 1 || v[0] != "4012345000009" {
		t.Errorf("NAD(2)-2.1 %v", v)
	}
	if v := read("FTX-4.1"); len(v) != 1 || v[0] != "Deliver to the dock's rear door+gate 2" {
		t.Errorf("release characters: %v", v)
	}
	if v := read("UNB-2.1"); len(v) != 1 || v[0] != "5412345000013" {
		t.Errorf("UNB-2.1 %v", v)
	}
	if v := read("QTY-1"); len(v) != 2 || v[0] != "21:48" {
		t.Errorf("QTY-1 %v", v)
	}
}

func TestRoundTripAndSplit(t *testing.T) {
	ic, err := Parse([]byte(orders))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(ic.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Bytes(), ic.Bytes()) || !strings.Contains(string(ic.Bytes()), "dock?'s rear door?+gate") {
		t.Errorf("not stable:\n%s", ic.Bytes())
	}
	parts := Split(ic)
	if len(parts) != 2 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		if probs := Check(p); len(probs) != 0 {
			t.Errorf("part %d: %v", i, probs)
		}
		if p.Ref() != "REF0001" || len(p.AllMessages()) != 1 {
			t.Errorf("part %d: ref %s, %d messages", i, p.Ref(), len(p.AllMessages()))
		}
	}
}

func TestEnvelopeProblems(t *testing.T) {
	for _, c := range []struct{ name, edit, want string }{
		{"segment count", "UNT+13+1", "UNT says 14"},
		{"message ref", "UNT+4+2", "UNT names reference"},
		{"interchange count", "UNZ+2+REF0001", "UNZ says 3"},
		{"interchange ref", "UNZ+2+REF0001", "UNZ names reference"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repl := map[string]string{"segment count": "UNT+14+1", "message ref": "UNT+4+9", "interchange count": "UNZ+3+REF0001",
				"interchange ref": "UNZ+2+REF0002"}[c.name]
			ic, err := Parse([]byte(strings.Replace(orders, c.edit, repl, 1)))
			if err != nil {
				t.Fatal(err)
			}
			probs := Check(ic)
			if len(probs) == 0 || !strings.Contains(probs[0].String(), c.want) {
				t.Errorf("%v", probs)
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(orders, "UNZ+2+REF0001'\n", "", 1), // cut short
		strings.Replace(orders, "UNT+4+2'\n", "", 1),       // message never closed
		"MSH|^~\\&|", "UNA:+.? 'UNB+UNOC:3+A+B+261017:1200+R'UNZ+0+R'", // no message
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("parsed %.40q", bad)
		}
	}
}

func TestGroupsAndSyntax4(t *testing.T) {
	src := "UNB+UNOC:4+SENDER+RECEIVER+20261017:1200+77'" +
		"UNG+INVOIC+SENDER+RECEIVER+20261017:1200+G1+UN+D:01B'" +
		"UNH+1+INVOIC:D:01B:UN'BGM+380+INV1*INV1B+9'UNT+3+1'" +
		"UNE+1+G1'UNZ+1+77'"
	ic, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if p := Check(ic); len(p) != 0 {
		t.Fatal(p)
	}
	p, _ := ParsePath("BGM-2(2)")
	if v := Read(ic, p); len(v) != 1 || v[0] != "INV1B" {
		t.Errorf("repetition: %v", v)
	}
	if parts := Split(ic); len(parts) != 1 || len(parts[0].Groups) != 1 || len(Check(parts[0])) != 0 {
		t.Errorf("split with groups: %+v", parts)
	}
}

func TestCONTRL(t *testing.T) {
	ic, _ := Parse([]byte(orders))
	c := CONTRL(ic, Accepted, "ACK1", time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	out := string(c.Bytes())
	for _, want := range []string{"UNB+UNOC:3+4012345000009:14+5412345000013:14+261017:1200+ACK1'", "UNH+1+CONTRL:D:3:UN'",
		"UCI+REF0001+5412345000013:14+4012345000009:14+7'", "UNT+3+1'", "UNZ+1+ACK1'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if probs := Check(c); len(probs) != 0 {
		t.Error(probs)
	}
}

func TestFilter(t *testing.T) {
	ic, _ := Parse([]byte(orders))
	for src, want := range map[string]bool{
		`BGM-2 == "PO12346"`:            true,
		`UNH-2.1 == "INVOIC"`:           false,
		`NAD(1)-2.1 == "5412345000013"`: true,
		`QTY-1.2 > 40`:                  true,
	} {
		f, err := ParseFilter(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got, err := f.Eval(ic); err != nil || got != want {
			t.Errorf("%s: %v %v", src, got, err)
		}
	}
	if _, err := ParseFilter(`PID-3 == "x"`); err != nil {
		t.Logf("an HL7-looking path parses as a tag (PID is three letters): %v", err)
	}
	if _, err := ParseFilter(`bgm-2 == "x"`); err == nil {
		t.Error("a lower-case tag was accepted")
	}
}

const pyEdifact = `
import sys, json
from pydifact.segmentcollection import Interchange
src = open('/work/in.edi').read()
ic = Interchange.from_str(src)
segs = [[s.tag] + [e if isinstance(e, list) else [e] for e in s.elements] for s in ic.segments]
msgs = [m.type for m in ic.get_messages()]
open('/work/out.edi', 'w').write(ic.serialize())
print(json.dumps({'segments': segs, 'messages': msgs}))
`

// Against pydifact, an independent EDIFACT parser: it reads what Perfuse writes to the same segments and values, and
// Perfuse reads what it writes. Set PERFUSE_TEST_PYDIFACT=pf-pydifact (a Docker image with pydifact 0.1.8 installed).
func TestInteroperatesWithPydifact(t *testing.T) {
	image := os.Getenv("PERFUSE_TEST_PYDIFACT")
	if image == "" {
		t.Skip("set PERFUSE_TEST_PYDIFACT to a Docker image with pydifact")
	}
	home, _ := os.UserHomeDir()
	work, err := os.MkdirTemp(filepath.Join(home, ".pftest"), "edirun")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	ic, err := Parse([]byte(orders))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(work, "in.edi"), ic.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(work, "h.py"), []byte(pyEdifact), 0o600)
	cmd := exec.Command("docker", "run", "--rm", "-v", work+":/work", image, "python", "/work/h.py")
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("pydifact: %v\n%s", err, e.String())
	}
	var got struct {
		Segments [][]any  `json:"segments"`
		Messages []string `json:"messages"`
	}
	if err := json.Unmarshal(o.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, o.String())
	}
	// pydifact lists the segments inside UNB and UNZ, the envelope being the interchange itself.
	ours := ic.Segments()
	ours = ours[1 : len(ours)-1]
	if len(got.Segments) != len(ours) {
		t.Fatalf("pydifact read %d segments, Perfuse %d", len(got.Segments), len(ours))
	}
	for i, s := range ours {
		ps := got.Segments[i]
		if ps[0] != s.Tag {
			t.Fatalf("segment %d: %v vs %s", i, ps[0], s.Tag)
		}
		for j, el := range s.Elements {
			comps, _ := ps[j+1].([]any)
			flat := make([]string, len(comps))
			for k, c := range comps {
				flat[k], _ = c.(string)
			}
			if strings.Join(flat, ":") != strings.Join(el[0], ":") {
				t.Errorf("%s element %d: pydifact %v, Perfuse %v", s.Tag, j+1, flat, el[0])
			}
		}
	}
	if len(got.Messages) != 2 || got.Messages[0] != "ORDERS" {
		t.Errorf("pydifact messages %v", got.Messages)
	}
	back, err := os.ReadFile(filepath.Join(work, "out.edi"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(back)
	if err != nil {
		t.Fatalf("Perfuse cannot read pydifact's interchange: %v\n%s", err, back)
	}
	if probs := Check(again); len(probs) != 0 {
		t.Errorf("pydifact's interchange: %v", probs)
	}
	p, _ := ParsePath("FTX-4.1")
	if v := Read(again, p); len(v) != 1 || v[0] != "Deliver to the dock's rear door+gate 2" {
		t.Errorf("after pydifact: %v", v)
	}
}
