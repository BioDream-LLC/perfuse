package hl7

import (
	"fmt"
	"strings"
	"testing"
)

// A 40-segment ORU, the shape that makes eager parsing expensive: one header,
// one patient, one order, and a long run of observations.
func buildORU(observations int) string {
	var sb strings.Builder
	sb.WriteString("MSH|^~\\&|LAB|SENDFAC|EHR|RECVFAC|20260818120000||ORU^R01^ORU_R01|CTRL0001|P|2.5.1\r")
	sb.WriteString("PID|1||MRN123456^^^SENDFAC^MR||Doe^Jane^Q||19800101|F\r")
	sb.WriteString("OBR|1|ORD123|FIL456|CBC^Complete Blood Count^LN|||20260818110000\r")
	for i := 1; i <= observations; i++ {
		fmt.Fprintf(&sb,
			"OBX|%d|NM|TEST%d^Analyte %d^LN||%d.%d|mmol/L|3.5-5.1|N|||F|||20260818113000\r",
			i, i, i, 100+i, i%10)
	}
	return sb.String()
}

// TestParseAllocations pins the cost of indexing a message. The parser builds
// two slices and touches nothing else, so this number should stay small and
// independent of how many fields a caller later reads.
func TestParseAllocations(t *testing.T) {
	msg := []byte(buildORU(37))

	allocs := testing.AllocsPerRun(200, func() {
		m, err := Parse(msg)
		if err != nil {
			t.Fatal(err)
		}
		_ = m
	})

	// Five: the Message, the segment index, the field index, the defensive copy of the input, and the options slice.
	//
	// It was three before copying became the default, and the bound was not moved with it - which went unnoticed
	// because the test suite was not running. The copy is the point rather than an accident: it costs one
	// allocation and about three percent, and it stops a caller reusing its read buffer from silently turning one
	// patient's message into another's.
	//
	// No headroom above five. Both index slices are sized from a counting pass, so a sixth allocation means
	// something new is being built per message and that is worth finding out about.
	const limit = 5
	if allocs > limit {
		t.Errorf("Parse allocated %.0f times, want at most %d", allocs, limit)
	}
	t.Logf("Parse of a %d-segment message: %.0f allocations", 40, allocs)
}

// TestRoutingDecisionAllocations covers the case an interface engine actually
// spends its life on: parse, read a couple of fields to decide where the
// message goes, forward it unchanged. The body of the message should never be
// decoded.
func TestRoutingDecisionAllocations(t *testing.T) {
	msg := []byte(buildORU(37))

	allocs := testing.AllocsPerRun(200, func() {
		m, err := Parse(msg)
		if err != nil {
			t.Fatal(err)
		}
		// Routing on message type and sending facility.
		seg, _ := m.Segment("MSH", 1)
		if got := seg.Field(9).Component(1).Bytes(); len(got) == 0 {
			t.Fatal("empty message type")
		}
		if got := seg.Field(4).Bytes(); len(got) == 0 {
			t.Fatal("empty sending facility")
		}
	})

	// Bytes returns a view into the message, so reading fields to make a routing
	// decision must add nothing on top of the index.
	// Five, the same as a bare parse: reading two fields to decide where a message goes must allocate nothing of its
	// own. That is the property under test, and it still holds - the number moved only because the defensive copy
	// was added to Parse.
	const limit = 5
	if allocs > limit {
		t.Errorf("parse plus routing decision allocated %.0f times, want at most %d", allocs, limit)
	}
	t.Logf("parse plus two field reads: %.0f allocations", allocs)
}

func BenchmarkParse(b *testing.B) {
	for _, n := range []int{1, 10, 37, 200} {
		msg := []byte(buildORU(n))
		b.Run(fmt.Sprintf("segments=%d", n+3), func(b *testing.B) {
			b.SetBytes(int64(len(msg)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Parse(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRouteDecision(b *testing.B) {
	msg := []byte(buildORU(37))
	b.SetBytes(int64(len(msg)))
	b.ReportAllocs()
	for b.Loop() {
		m, err := Parse(msg)
		if err != nil {
			b.Fatal(err)
		}
		seg, _ := m.Segment("MSH", 1)
		_ = seg.Field(9).Component(2).Bytes()
	}
}

func BenchmarkGetPath(b *testing.B) {
	m, err := ParseString(buildORU(37))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Get("OBX(20)-3.2"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnescapeNoEscapes(b *testing.B) {
	sep := DefaultSeparators()
	in := []byte("Complete Blood Count With Differential")
	b.ReportAllocs()
	for b.Loop() {
		_ = Unescape(in, sep)
	}
}
