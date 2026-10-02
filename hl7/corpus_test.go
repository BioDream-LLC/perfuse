package hl7

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Corpus testing runs the parser over real message traffic, which is the only
// way to find the things synthetic fixtures never contain: eight-component
// fields, empty segments, trailing delimiters, Z-segments, encoding characters
// nobody expects, and messages that are simply wrong but which a hospital sends
// anyway.
//
// Real HL7 is protected health information, so no corpus is committed here and
// none ever should be. Point PERFUSE_CORPUS at a local file or directory to run
// these; without it they skip, which keeps CI green and keeps patient data off
// build machines.
//
//	PERFUSE_CORPUS=/path/to/messages.txt go test ./internal/hl7/ -run Corpus -v
//
// Assertions are structural and aggregate. Nothing derived from message content
// is ever logged, because test output ends up in terminals, CI logs and pasted
// bug reports.

func corpusPaths(t *testing.T) []string {
	t.Helper()

	root := os.Getenv("PERFUSE_CORPUS")
	if root == "" {
		t.Skip("PERFUSE_CORPUS is not set; skipping real-message corpus tests")
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("PERFUSE_CORPUS: %v", err)
	}
	if !info.IsDir() {
		return []string{root}
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking PERFUSE_CORPUS: %v", err)
	}
	sort.Strings(paths)
	return paths
}

// splitCorpus finds the messages in a capture file.
//
// Captures arrive in every shape: MLLP-framed, one message per line, or several
// run together. A message begins at an MSH that starts a line.
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

	// Normalise line endings to CR, which is what HL7 specifies, then split on
	// segment boundaries that begin a new header.
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

type corpusStats struct {
	files     int
	messages  int
	parsed    int
	failed    int
	segments  int
	fields    int
	types     map[string]int
	segNames  map[string]int
	maxFields int
	maxSegs   int
	maxRepeat int
	maxComps  int
}

func newCorpusStats() *corpusStats {
	return &corpusStats{types: map[string]int{}, segNames: map[string]int{}}
}

func TestCorpusParses(t *testing.T) {
	paths := corpusPaths(t)
	st := newCorpusStats()

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		st.files++

		msgs := splitCorpus(raw)
		if len(msgs) == 0 {
			t.Errorf("%s: no messages found", filepath.Base(path))
			continue
		}

		for i, raw := range msgs {
			st.messages++

			m, err := Parse(raw)
			if err != nil {
				st.failed++
				// Report the position and the error, never the message.
				t.Errorf("%s message %d: %v", filepath.Base(path), i+1, err)
				continue
			}
			st.parsed++

			checkMessageInvariants(t, m, filepath.Base(path), i+1, st)
		}
	}

	types := make([]string, 0, len(st.types))
	for k := range st.types {
		types = append(types, k)
	}
	sort.Strings(types)

	segs := make([]string, 0, len(st.segNames))
	for k := range st.segNames {
		segs = append(segs, k)
	}
	sort.Strings(segs)

	t.Logf("corpus: %d file(s), %d message(s), %d parsed, %d failed",
		st.files, st.messages, st.parsed, st.failed)
	t.Logf("segments: %d total across %d distinct names: %s",
		st.segments, len(segs), strings.Join(segs, " "))
	t.Logf("fields indexed: %d", st.fields)
	t.Logf("message types: %s", strings.Join(types, " "))
	t.Logf("widest: %d segments, %d fields in one segment, %d repetitions, %d components",
		st.maxSegs, st.maxFields, st.maxRepeat, st.maxComps)

	if st.parsed == 0 {
		t.Fatal("no messages parsed")
	}
}

// checkMessageInvariants asserts the properties that must hold for every
// message, whatever is in it.
func checkMessageInvariants(t *testing.T, m *Message, file string, n int, st *corpusStats) {
	t.Helper()
	where := func(what string) string {
		return file + " message " + itoa(n) + ": " + what
	}

	// The parser must not alter the message. An interface engine has to be able
	// to forward exactly what it received, byte for byte.
	if m.String() != string(m.Raw()) {
		t.Error(where("String() does not match Raw()"))
	}

	// Every message needs a type and a control ID, or it cannot be routed or
	// acknowledged. A missing one is worth reporting without quoting anything.
	typ, event, _ := m.Type()
	if typ == "" {
		t.Error(where("MSH-9 has no message type"))
	}
	label := typ
	if event != "" {
		label += "^" + event
	}
	st.types[label]++

	if m.ControlID() == "" {
		t.Error(where("MSH-10 has no control ID"))
	}

	if got := m.SegmentCount(); got > st.maxSegs {
		st.maxSegs = got
	}
	st.segments += m.SegmentCount()

	// Walk every addressable position. The point is that no combination of
	// indices panics, and that every returned view stays inside the message.
	raw := m.Raw()
	for i := 0; i < m.SegmentCount(); i++ {
		seg, ok := m.SegmentAt(i)
		if !ok {
			t.Error(where("SegmentAt returned nothing within range"))
			continue
		}

		name := seg.Name()
		if len(name) == 0 {
			t.Error(where("segment has an empty name"))
		}
		st.segNames[name]++

		if !bytesWithin(seg.Raw(), raw) {
			t.Error(where("segment bytes are outside the message"))
		}

		fieldCount := seg.FieldCount()
		if fieldCount > st.maxFields {
			st.maxFields = fieldCount
		}
		st.fields += fieldCount

		for f := 1; f <= fieldCount; f++ {
			v := seg.Field(f)
			if !v.Exists() {
				continue
			}
			if !bytesWithin(v.Bytes(), raw) {
				t.Error(where("field bytes are outside the message"))
			}
			// String resolves escapes; it must never panic on real data.
			_ = v.String()

			if r := v.RepeatCount(); r > st.maxRepeat {
				st.maxRepeat = r
			}
			for rep := 1; rep <= v.RepeatCount(); rep++ {
				rv := v.Repeat(rep)
				if !rv.Exists() {
					t.Error(where("a counted repetition does not exist"))
					continue
				}
				if c := rv.ComponentCount(); c > st.maxComps {
					st.maxComps = c
				}
				for comp := 1; comp <= rv.ComponentCount(); comp++ {
					cv := rv.Component(comp)
					if !cv.Exists() {
						continue
					}
					if !bytesWithin(cv.Bytes(), raw) {
						t.Error(where("component bytes are outside the message"))
					}
					for sub := 1; sub <= cv.SubcomponentCount(); sub++ {
						sv := cv.Subcomponent(sub)
						if sv.Exists() && !bytesWithin(sv.Bytes(), raw) {
							t.Error(where("subcomponent bytes are outside the message"))
						}
					}
				}
			}

			// Reading past the end must be absent rather than an error.
			if seg.Field(fieldCount + 50).Exists() {
				t.Error(where("a field well past the end of the segment reports that it exists"))
			}
		}
	}
}

// TestCorpusAcknowledgements checks that an acknowledgement generated for every
// real message is itself valid and correctly addressed. Malformed ACKs are a
// common production fault and only show up against real traffic.
func TestCorpusAcknowledgements(t *testing.T) {
	paths := corpusPaths(t)

	var checked int
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		for i, msgRaw := range splitCorpus(raw) {
			m, err := Parse(msgRaw)
			if err != nil {
				continue
			}

			for _, opts := range []AckOptions{
				{Code: AckAccept},
				{Code: AckError, Text: "could not be stored"},
				{Code: AckReject, Text: "unsupported", ErrorCode: "200"},
				{Code: AckAccept, IncludeTriggerEvent: true},
			} {
				ackRaw := m.Ack(opts)

				ack, err := Parse(ackRaw)
				if err != nil {
					t.Fatalf("%s message %d: generated an unparseable acknowledgement: %v",
						filepath.Base(path), i+1, err)
				}

				// MSA-2 must echo the original control ID; it is the only link
				// back to the message being answered.
				if got := ack.MustGet("MSA-2"); got != m.ControlID() {
					t.Errorf("%s message %d: MSA-2 does not echo the original control ID",
						filepath.Base(path), i+1)
				}
				// The version must match the sender's, or strict senders reject it.
				msh, _ := m.Segment("MSH", 1)
				if want := msh.Field(12).String(); want != "" {
					if got := ack.MustGet("MSH-12"); got != want {
						t.Errorf("%s message %d: acknowledgement version does not match the sender's",
							filepath.Base(path), i+1)
					}
				}
				// Addressing is mirrored, not copied.
				if got, want := ack.MustGet("MSH-5"), msh.Field(3).String(); want != "" && got != want {
					t.Errorf("%s message %d: acknowledgement receiver is not the original sender",
						filepath.Base(path), i+1)
				}
				// And the acknowledgement is a single logical message.
				if bytes.Contains(ackRaw, []byte("\n")) {
					t.Errorf("%s message %d: acknowledgement contains a line feed",
						filepath.Base(path), i+1)
				}
				checked++
			}
		}
	}

	if checked == 0 {
		t.Fatal("no acknowledgements checked")
	}
	t.Logf("generated and validated %d acknowledgements", checked)
}

// TestCorpusEscapeRoundTrip checks that every value in real traffic survives
// being unescaped and escaped again. Real messages contain the delimiters and
// escape sequences that hand-written fixtures do not.
func TestCorpusEscapeRoundTrip(t *testing.T) {
	paths := corpusPaths(t)

	var values, withEscapes int
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		for i, msgRaw := range splitCorpus(raw) {
			m, err := Parse(msgRaw)
			if err != nil {
				continue
			}
			sep := m.Separators()

			for s := 0; s < m.SegmentCount(); s++ {
				seg, _ := m.SegmentAt(s)
				name := seg.Name()
				for f := 1; f <= seg.FieldCount(); f++ {
					// MSH-1 and MSH-2 are the delimiters themselves.
					if name == "MSH" && f <= 2 {
						continue
					}
					v := seg.Field(f)
					if !v.Exists() || v.IsEmpty() {
						continue
					}
					values++

					decoded := v.String()
					if decoded != v.Raw() {
						withEscapes++
					}

					// Re-escaping the decoded value and decoding again must be
					// stable, or a transformation would corrupt data.
					if got := Unescape([]byte(Escape(decoded, sep)), sep); got != decoded {
						t.Errorf("%s message %d %s-%d: escape round trip is not stable",
							filepath.Base(path), i+1, name, f)
					}
				}
			}
		}
	}

	if values == 0 {
		t.Fatal("no values examined")
	}
	t.Logf("round-tripped %d values, %d of which contained escape sequences", values, withEscapes)
}

func BenchmarkCorpusParse(b *testing.B) {
	root := os.Getenv("PERFUSE_CORPUS")
	if root == "" {
		b.Skip("PERFUSE_CORPUS is not set")
	}
	info, err := os.Stat(root)
	if err != nil || info.IsDir() {
		b.Skip("PERFUSE_CORPUS must be a single file for this benchmark")
	}
	raw, err := os.ReadFile(root)
	if err != nil {
		b.Fatal(err)
	}
	msgs := splitCorpus(raw)
	if len(msgs) == 0 {
		b.Skip("no messages in the corpus")
	}

	var total int
	for _, m := range msgs {
		total += len(m)
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()

	for b.Loop() {
		for _, msg := range msgs {
			if _, err := Parse(msg); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// bytesWithin reports whether an accessor's result actually occurs in the
// message. Nothing an accessor returns should be invented or strayed outside the
// bytes that arrived.
func bytesWithin(sub, parent []byte) bool {
	if len(sub) == 0 {
		return true
	}
	return bytes.Contains(parent, sub)
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
