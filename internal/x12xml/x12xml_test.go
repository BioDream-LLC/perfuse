package x12xml

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The golden files are Open Integration Engine 4.6.0's own output for the sample interchanges: *.oie.xml is what its
// EDI/X12 serializer wrote for *.x12, and *.oie.back.x12 is what it wrote back from that XML. They are regenerated with
// the harness in PERFUSE_TEST_OIE_X12 (see below) and checked in so these tests run without Java.

func goldens(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("testdata/*.oie.xml")
	if err != nil || len(files) < 4 {
		t.Fatalf("golden files: %v %v", files, err)
	}
	return files
}

func TestToXMLWritesWhatOIEWrites(t *testing.T) {
	for _, g := range goldens(t) {
		src := strings.TrimSuffix(g, ".oie.xml") + ".x12"
		raw, _ := os.ReadFile(src)
		want, _ := os.ReadFile(g)
		got, err := ToXML(raw)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: differs from OIE at byte %d:\n ours %.120q\n OIE  %.120q", src, firstDiff(got, want),
				got[max(0, firstDiff(got, want)-40):], want[max(0, firstDiff(got, want)-40):])
		}
	}
}

func TestFromXMLWritesWhatOIEWrites(t *testing.T) {
	for _, g := range goldens(t) {
		doc, _ := os.ReadFile(g)
		want, _ := os.ReadFile(strings.TrimSuffix(g, ".oie.xml") + ".oie.back.x12")
		got, err := FromXML(doc)
		if err != nil {
			t.Fatalf("%s: %v", g, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: differs from OIE at byte %d", g, firstDiff(got, want))
		}
	}
}

func TestPrettyPrintedXMLReadsTheSame(t *testing.T) {
	doc := `<X12Transaction segmentDelimiter="~" elementDelimiter="*" subelementDelimiter=":">
  <CLM>
    <CLM.01><CLM.01.1>A1</CLM.01.1></CLM.01>
    <CLM.03><CLM.03.1> spaced </CLM.03.1></CLM.03>
  </CLM>
</X12Transaction>`
	got, err := FromXML([]byte(doc))
	if err != nil || string(got) != "CLM*A1** spaced ~" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestAShortISAElementIsPaddedBack(t *testing.T) {
	doc := `<X12Transaction><ISA><ISA.06><ISA.06.1>SENDER</ISA.06.1></ISA.06><ISA.13><ISA.13.1>42</ISA.13.1></ISA.13></ISA></X12Transaction>`
	got, err := FromXML([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "*SENDER         *") || !strings.Contains(string(got), "*000000042") {
		t.Errorf("%q", got)
	}
}

func TestADoctypeIsRefused(t *testing.T) {
	if _, err := FromXML([]byte(`<!DOCTYPE x [<!ENTITY a "b">]><EDIMessage/>`)); err == nil {
		t.Fatal("a DOCTYPE was accepted")
	}
}

func TestTooShortOrNotEDIIsRefused(t *testing.T) {
	for _, in := range []string{"", "ST", "  \n ", "*ST*1~"} {
		if _, err := ToXML([]byte(in)); err == nil {
			t.Errorf("%q was converted", in)
		}
	}
}

// TestAgainstOIE compares this package with Open Integration Engine's serializer on generated messages, both ways.
//
// PERFUSE_TEST_OIE_X12 is a command that runs the harness: it reads EDI on standard input and prints OIE's XML, or with
// the argument "from" reads that XML and prints OIE's EDI. The harness is twenty lines of Java around
// com.mirth.connect.plugins.datatypes.edi.EDISerializer from OIE's datatype-edi extension.
func TestAgainstOIE(t *testing.T) {
	oracle := os.Getenv("PERFUSE_TEST_OIE_X12")
	if oracle == "" {
		t.Skip("PERFUSE_TEST_OIE_X12 is not set (a command running OIE's EDISerializer)")
	}
	run := func(in []byte, args ...string) []byte {
		fields := strings.Fields(oracle)
		cmd := exec.Command(fields[0], append(fields[1:], args...)...)
		cmd.Stdin = bytes.NewReader(in)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("the oracle failed: %v", err)
		}
		return out
	}
	rng := rand.New(rand.NewSource(20261030))
	inputs := [][]byte{}
	for range 25 {
		inputs = append(inputs, generate(rng))
	}
	for _, g := range goldens(t) {
		raw, _ := os.ReadFile(strings.TrimSuffix(g, ".oie.xml") + ".x12")
		inputs = append(inputs, raw)
	}
	for i, in := range inputs {
		want := run(in)
		got, err := ToXML(in)
		if err != nil {
			t.Fatalf("input %d: %v\n%q", i, err, in)
		}
		if !bytes.Equal(got, want) {
			at := firstDiff(got, want)
			t.Fatalf("input %d to XML differs at %d:\n ours %.160q\n OIE  %.160q\ninput %q", i, at, got[max(0, at-60):],
				want[max(0, at-60):], in)
		}
		back, err := FromXML(want)
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if wantBack := run(want, "from"); !bytes.Equal(back, wantBack) {
			t.Fatalf("input %d from XML differs at %d:\n ours %q\n OIE  %q", i, firstDiff(back, wantBack), back, wantBack)
		}
	}
}

// generate makes an interchange with the awkward parts: empty elements, empty and trailing components, repetitions,
// characters XML escapes, line feeds after terminators, and the delimiters an ISA may choose.
func generate(rng *rand.Rand) []byte {
	sets := []Delimiters{{"~", "*", ":"}, {"~\n", "*", ":"}, {"'", "+", ">"}, {"\n", "|", "^"}}
	d := sets[rng.Intn(len(sets))]
	pick := func(xs ...string) string { return xs[rng.Intn(len(xs))] }
	value := func() string {
		switch rng.Intn(9) {
		case 0:
			return ""
		case 1:
			return pick("A&B", "<x>", `"q"`, "it's", "a b ")
		case 2:
			return "1" + d.Component + "" + d.Component + "3"
		case 3:
			return d.Component + "x"
		case 4:
			return "x" + d.Component
		case 5:
			return "R1^R2"
		default:
			return fmt.Sprint(rng.Intn(100000))
		}
	}
	isa := fmt.Sprintf("ISA%[1]s00%[1]s          %[1]s00%[1]s          %[1]sZZ%[1]sSENDER         %[1]sZZ%[1]sRECEIVER       %[1]s261030%[1]s1200%[1]s^%[1]s00501%[1]s%09[4]d%[1]s0%[1]sT%[1]s%[2]s%[3]s",
		d.Element, d.Component, d.Segment, rng.Intn(1e9))
	var b strings.Builder
	b.WriteString(isa)
	b.WriteString("GS" + d.Element + "HC" + d.Element + "S" + d.Element + "R" + d.Segment)
	for range 3 + rng.Intn(6) {
		b.WriteString(pick("CLM", "NM1", "HI", "REF", "SV1", "DTP"))
		for range rng.Intn(8) {
			b.WriteString(d.Element + value())
		}
		b.WriteString(d.Segment)
	}
	b.WriteString("GE" + d.Element + "1" + d.Element + "1" + d.Segment + "IEA" + d.Element + "1" + d.Element + "000000001" + d.Segment)
	return []byte(b.String())
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
