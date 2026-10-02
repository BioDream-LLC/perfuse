package expr

import (
	"bytes"
	"os"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
)

// Filters are validated against real traffic the same way the parser is: opt in
// with PERFUSE_CORPUS, skip otherwise, and log only counts. Real HL7 is
// protected health information and none of it belongs in this repository or in
// test output.
//
//	PERFUSE_CORPUS=/path/to/capture.txt go test ./internal/expr/ -run Corpus -v
func TestCorpusFilters(t *testing.T) {
	path := os.Getenv("PERFUSE_CORPUS")
	if path == "" {
		t.Skip("PERFUSE_CORPUS is not set; skipping real-message filter tests")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PERFUSE_CORPUS: %v", err)
	}

	var messages []*hl7.Message
	for _, chunk := range splitCorpusForTest(raw) {
		m, err := hl7.Parse(chunk)
		if err != nil {
			continue
		}
		messages = append(messages, m)
	}
	if len(messages) == 0 {
		t.Fatal("no messages parsed from the corpus")
	}

	// Filters of the shape people actually write. The assertion is not a
	// specific count, which would depend on the corpus, but that evaluation
	// never errors and that the results are internally consistent.
	filters := []string{
		`MSH-9.1 == "ADT"`,
		`MSH-9.2 != "A28"`,
		`MSH-9.2 in ["A01", "A04", "A08"]`,
		`PID-3 exists`,
		`PID-3.1 exists and not PID-3.1 empty`,
		`PID-5.1 matches "^[A-Za-z]"`,
		`MSH-9.1 == "ADT" and PID-3 exists and not PV1-2 empty`,
		`not (MSH-9.2 == "A28" or MSH-9.2 == "A31")`,
	}

	for _, src := range filters {
		e, err := Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}

		var matched int
		for i, m := range messages {
			got, err := e.Eval(m)
			if err != nil {
				t.Fatalf("%s: message %d: %v", src, i+1, err)
			}
			if got {
				matched++
			}
		}

		// A filter must also be consistent with its own negation on every
		// message, which catches a whole class of logic errors.
		neg, err := Parse("not (" + src + ")")
		if err != nil {
			t.Fatalf("parsing the negation of %q: %v", src, err)
		}
		var negMatched int
		for _, m := range messages {
			got, err := neg.Eval(m)
			if err != nil {
				t.Fatal(err)
			}
			if got {
				negMatched++
			}
		}
		if matched+negMatched != len(messages) {
			t.Errorf("%s matched %d and its negation matched %d, which does not account for all %d messages",
				src, matched, negMatched, len(messages))
		}

		t.Logf("%-60s matched %d/%d", src, matched, len(messages))
	}
}

// splitCorpusForTest mirrors the splitter used in the hl7 package tests.
func splitCorpusForTest(raw []byte) [][]byte {
	if bytes.IndexByte(raw, 0x0B) >= 0 {
		var out [][]byte
		for _, frame := range bytes.Split(raw, []byte{0x0B}) {
			if i := bytes.IndexByte(frame, 0x1C); i >= 0 {
				frame = frame[:i]
			}
			if len(bytes.TrimSpace(frame)) > 0 {
				out = append(out, frame)
			}
		}
		return out
	}

	norm := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\r"))
	norm = bytes.ReplaceAll(norm, []byte("\n"), []byte("\r"))

	var out [][]byte
	for len(norm) > 0 {
		start := bytes.Index(norm, []byte("MSH"))
		if start < 0 {
			break
		}
		norm = norm[start:]
		next := bytes.Index(norm[1:], []byte("\rMSH"))
		if next < 0 {
			if trimmed := bytes.Trim(norm, "\r"); len(trimmed) > 0 {
				out = append(out, trimmed)
			}
			break
		}
		out = append(out, norm[:next+2])
		norm = norm[next+2:]
	}
	return out
}
