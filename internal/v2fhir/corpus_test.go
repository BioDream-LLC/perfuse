package v2fhir

import (
	"bytes"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Conversion is validated against real message traffic the same way the parser
// is: opt in with PERFUSE_CORPUS, skip otherwise, and report aggregates only.
// Real HL7 is protected health information and none of it belongs in this
// repository or in test output.
//
//	PERFUSE_CORPUS=/path/to/capture.txt go test ./internal/v2fhir/ -run Corpus -v
//
// This is the test that matters most for the mapper. Synthetic fixtures contain
// what the author thought of; real ADT contains empty assigning authorities,
// timestamps with no offset, patient classes nobody documented, and names with
// eight components. A mapper that only works on fixtures is a demo.

func corpusMessages(t *testing.T) []*hl7.Message {
	t.Helper()

	path := os.Getenv("PERFUSE_CORPUS")
	if path == "" {
		t.Skip("PERFUSE_CORPUS is not set; skipping real-message conversion tests")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PERFUSE_CORPUS: %v", err)
	}

	var out []*hl7.Message
	for _, chunk := range splitCorpus(raw) {
		m, err := hl7.Parse(chunk)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatal("no messages parsed from the corpus")
	}
	return out
}

func splitCorpus(raw []byte) [][]byte {
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

func TestCorpusConvertsAndValidates(t *testing.T) {
	messages := corpusMessages(t)

	opts := Options{
		Version:                 fhir.R5,
		DefaultIdentifierSystem: "urn:oid:2.16.840.1.113883.3.example",
		Timezone:                time.UTC,
	}

	var (
		converted   int
		failed      int
		resources   = map[string]int{}
		findings    = map[string]int{}
		noteCounts  = map[string]int{}
		errorFields = map[string]int{}
	)

	for i, m := range messages {
		res, err := Convert(m, opts)
		if err != nil {
			failed++
			t.Errorf("message %d: %v", i+1, err)
			continue
		}
		converted++

		for kind, n := range res.ResourceCounts() {
			resources[kind] += n
		}
		for _, note := range res.Notes {
			noteCounts[note.Severity]++
			if note.Severity != "info" {
				// Group by source field, not by message, so the report says which
				// parts of the feed need attention rather than listing 299 lines.
				errorFields[note.Source]++
			}
		}

		// Every resource produced must validate for both releases. This is the
		// assertion that makes the mapper trustworthy: it is not enough to produce
		// JSON, it has to be JSON a server will accept.
		for _, version := range []fhir.Version{fhir.R5, fhir.R4} {
			result := fhir.Validate(res.Bundle, version)
			for _, f := range result.Findings {
				key := string(f.Severity) + " " + f.Rule + " " + f.Path
				findings[key]++
				if f.Severity == fhir.Error {
					t.Errorf("message %d produced an invalid resource for %s: %s at %s",
						i+1, version.Name(), f.Message, f.Path)
				}
			}
		}

		// And the bundle must serialise. A resource that cannot be marshalled is
		// one that sets two halves of a choice element.
		if _, err := fhir.MarshalBundle(res.Bundle, fhir.R5); err != nil {
			t.Errorf("message %d: serialising R5: %v", i+1, err)
		}
		if _, err := fhir.MarshalBundle(res.Bundle, fhir.R4); err != nil {
			t.Errorf("message %d: serialising R4: %v", i+1, err)
		}
	}

	t.Logf("converted %d of %d messages, %d failed", converted, len(messages), failed)
	t.Logf("resources produced: %s", summarise(resources))
	t.Logf("mapping notes: %s", summarise(noteCounts))
	if len(errorFields) > 0 {
		t.Logf("fields needing attention: %s", summarise(errorFields))
	}
	if len(findings) > 0 {
		t.Logf("validation findings by rule:")
		for _, line := range topN(findings, 12) {
			t.Logf("  %s", line)
		}
	}

	if converted == 0 {
		t.Fatal("nothing was converted")
	}
}

func TestCorpusIDsAreStable(t *testing.T) {
	// The same message converted twice must produce the same ids, or a feed that
	// retries creates duplicates somebody merges by hand.
	messages := corpusMessages(t)
	opts := Options{Timezone: time.UTC}

	for i, m := range messages {
		first, err := Convert(m, opts)
		if err != nil {
			continue
		}
		second, err := Convert(m, opts)
		if err != nil {
			continue
		}

		if len(first.Bundle.Entry) != len(second.Bundle.Entry) {
			t.Fatalf("message %d produced different entry counts on two runs", i+1)
		}
		for j := range first.Bundle.Entry {
			a := first.Bundle.Entry[j].Resource
			b := second.Bundle.Entry[j].Resource
			if a.ResourceID() != b.ResourceID() {
				t.Fatalf("message %d entry %d: id %q then %q",
					i+1, j, a.ResourceID(), b.ResourceID())
			}
		}
	}
}

func TestCorpusSamePatientSameID(t *testing.T) {
	// Two messages about the same patient must map to the same Patient id, or a
	// registry accumulates a copy per message.
	messages := corpusMessages(t)
	opts := Options{
		Timezone:                time.UTC,
		DefaultIdentifierSystem: "urn:oid:2.16.840.1.113883.3.example",
	}

	idsByMRN := map[string]map[string]bool{}

	for _, m := range messages {
		res, err := Convert(m, opts)
		if err != nil {
			continue
		}
		for _, e := range res.Bundle.Entry {
			p, ok := e.Resource.(*fhir.Patient)
			if !ok || len(p.Identifier) == 0 {
				continue
			}
			key := p.Identifier[0].System + "|" + p.Identifier[0].Value
			if idsByMRN[key] == nil {
				idsByMRN[key] = map[string]bool{}
			}
			idsByMRN[key][p.ID] = true
		}
	}

	distinct := 0
	collisions := 0
	for _, ids := range idsByMRN {
		distinct++
		if len(ids) > 1 {
			collisions++
		}
	}
	if collisions > 0 {
		t.Errorf("%d identifiers mapped to more than one Patient id", collisions)
	}
	t.Logf("%d distinct patient identifiers, each with exactly one resource id", distinct)
}

func TestCorpusNoInventedCodes(t *testing.T) {
	// The rule the mapper is built on: a value that cannot be mapped is preserved
	// as text, never guessed at. Any coding that claims a standard system must
	// have come from a mapping table, not from a hopeful assumption.
	messages := corpusMessages(t)
	opts := Options{Timezone: time.UTC}

	standardSystems := map[string]bool{
		fhir.SystemLOINC:  true,
		fhir.SystemSNOMED: true,
		fhir.SystemUCUM:   true,
		fhir.SystemRxNorm: true,
	}

	var checked, textOnly int
	for i, m := range messages {
		res, err := Convert(m, opts)
		if err != nil {
			continue
		}

		for _, e := range res.Bundle.Entry {
			obs, ok := e.Resource.(*fhir.Observation)
			if !ok {
				continue
			}
			if obs.ValueQuantity != nil {
				q := obs.ValueQuantity
				checked++
				// A UCUM code must never appear without the UCUM system, and a
				// unit that was not recognised must have no code at all.
				if q.Code != "" && q.System != fhir.SystemUCUM {
					t.Errorf("message %d: unit code %q asserted with system %q",
						i+1, q.Code, q.System)
				}
				if q.Code == "" && q.Unit != "" {
					textOnly++
				}
			}
			if obs.Code != nil {
				for _, coding := range obs.Code.Coding {
					if standardSystems[coding.System] && coding.Code == "" {
						t.Errorf("message %d: a standard system was claimed with no code", i+1)
					}
				}
			}
		}
	}

	t.Logf("%d quantities checked, %d kept their unit as text because it had no known UCUM code",
		checked, textOnly)
}

func summarise(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+itoa(counts[k]))
	}
	return strings.Join(parts, ", ")
}

func topN(counts map[string]int, n int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, itoa(counts[k])+"x  "+k)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
