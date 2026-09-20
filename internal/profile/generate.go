package profile

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
)

// Generation: test messages that look like a real feed without containing any of it.
//
// # The problem this solves
//
// Every guide to interface testing says the same thing and none of them solve it: you need samples that reflect your own
// environment - your message flow, your trigger events, your code values, your case mix - and a handful of examples off
// the internet does not. The standard advice is to pull messages out of production and strip the identifiers by hand,
// which is slow, and which fails quietly the one time somebody misses a field.
//
// A clinic has it worse than a hospital. There is no test feed, no second instance of the sending system, and nobody
// whose job is to build a corpus.
//
// # Why generating from a profile is safe
//
// The profile is the only input, and a profile does not contain the messages it was built from. It holds fill rates,
// value shapes, length bounds, repetition counts and segment frequencies - no values. The single exception is code-table
// fields, where the observed codes are recorded because which codes a sender actually uses is the most useful fact in a
// profile, and a code table value identifies nobody.
//
// So this cannot leak a patient, because the data it works from never held one. That is a stronger guarantee than
// de-identifying messages, which is a process that can be done incompletely. There is nothing here to miss.
//
// A test asserts it directly: a profile built from messages containing distinctive names and record numbers generates
// output containing none of them.
//
// # What the output is and is not
//
// It is structurally faithful: the right segments at the right rates, the right fields populated as often as they really
// are, values the right shape and length, codes in the observed proportions, repeats where repeats occur.
//
// It is not clinically coherent. A generated patient has a plausible-looking record number and a name-shaped string, and
// no relationship between them. Nobody should validate clinical logic against these. They are for exercising an
// interface - parsing, mapping, filtering, throughput - which is what they are needed for.

// GenerateOptions controls generation.
type GenerateOptions struct {
	// Count is how many messages to produce. Defaults to 10.
	Count int

	// Seed makes the output reproducible.
	//
	// Worth having: a test corpus that changes on every run cannot be committed, cannot be compared against a previous
	// result, and turns a failing test into a mystery. Zero picks a fixed default rather than the clock, because an
	// accidentally random corpus is the thing to avoid.
	Seed int64

	// SendingApplication and friends override the MSH values. Empty uses obviously synthetic placeholders.
	//
	// Deliberately obvious rather than realistic. A generated message that says it came from the real sending system is
	// one somebody can mistake for real traffic, and these get emailed to vendors.
	SendingApplication string
	SendingFacility    string
}

func (o GenerateOptions) withDefaults() GenerateOptions {
	if o.Count <= 0 {
		o.Count = 10
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	if o.SendingApplication == "" {
		o.SendingApplication = "PERFUSE-TEST"
	}
	if o.SendingFacility == "" {
		o.SendingFacility = "SYNTHETIC"
	}
	return o
}

// Generate produces test messages matching the shape of a profiled feed.
//
// Every message is parsed before being returned. A generator that emits something unparseable is worse than useless: it
// sends somebody hunting a bug in their interface that is really in the corpus.
func Generate(r *Report, opts GenerateOptions) ([][]byte, error) {
	if r == nil || r.Messages == 0 {
		return nil, fmt.Errorf("there is no profile to generate from; profile some traffic first")
	}
	if len(r.Segments) == 0 {
		return nil, fmt.Errorf("the profile has no segments, so there is no shape to reproduce")
	}

	opts = opts.withDefaults()
	rng := rand.New(rand.NewSource(opts.Seed))

	types := typeChooser(r)
	if len(types) == 0 {
		return nil, fmt.Errorf("the profile records no message types, so a generated message would have nothing in MSH-9")
	}

	out := make([][]byte, 0, opts.Count)
	for i := range opts.Count {
		msgType := types[rng.Intn(len(types))]
		raw := generateOne(r, msgType, i+1, rng, opts)

		// Parsed as a gate, not as a formality.
		if _, err := hl7.Parse(raw); err != nil {
			return nil, fmt.Errorf("generated message %d does not parse, which would send somebody hunting a bug in "+
				"their interface that is really in this corpus: %w", i+1, err)
		}
		out = append(out, raw)
	}

	return out, nil
}

// typeChooser expands the observed type distribution into a slice to draw from.
//
// Weighted by what was actually seen, because the proportions are part of the shape. A feed that is 80% A08 and 2% A40
// exercises an interface quite differently from one with an even spread, and an even spread is what a naive generator
// produces.
func typeChooser(r *Report) []string {
	var out []string
	for _, t := range r.Types {
		if t.Type == "" {
			continue
		}
		// At least one of each, so a rare trigger event is still represented - those are usually the ones nobody was
		// told about and the ones that break things.
		n := max(int(t.Rate*100), 1)
		for range n {
			out = append(out, t.Type)
		}
	}
	return out
}

func generateOne(r *Report, msgType string, seq int, rng *rand.Rand, opts GenerateOptions) []byte {
	var b strings.Builder

	control := fmt.Sprintf("TEST%06d", seq)

	for _, seg := range r.Segments {
		if seg.ID == "MSH" {
			// Written explicitly rather than generated from the profile. MSH carries the encoding characters, the
			// message type and the control ID, and a generated MSH-1 or MSH-2 produces a message nothing can read.
			b.WriteString(buildMSH(msgType, control, opts))
			b.WriteString("\r")
			continue
		}

		// Segments appear at the rate they were observed. A segment present in 30% of messages should be absent from
		// most of the corpus, because whether an interface copes with its absence is exactly what wants testing.
		if seg.Rate < 1 && rng.Float64() > seg.Rate {
			continue
		}

		repeats := 1
		if seg.MaxPerMessage > 1 {
			repeats = 1 + rng.Intn(seg.MaxPerMessage)
		}
		for occurrence := range repeats {
			b.WriteString(buildSegment(seg, occurrence+1, rng))
			b.WriteString("\r")
		}
	}

	return []byte(b.String())
}

func buildMSH(msgType, control string, opts GenerateOptions) string {
	return strings.Join([]string{
		"MSH",
		"^~\\&",
		opts.SendingApplication,
		opts.SendingFacility,
		"RECEIVER",
		"RECEIVING-FACILITY",
		"20260101120000",
		"",
		msgType,
		control,
		// P for production would be a lie about a synthetic message, and T is what a receiver checks to refuse one
		// that reached it by mistake.
		"T",
		"2.5",
	}, "|")
}

func buildSegment(seg Segment, occurrence int, rng *rand.Rand) string {
	// Widest populated field decides the length, so trailing empty fields are not written.
	widest := 0
	byField := map[int][]Field{}
	for _, f := range seg.Fields {
		n := fieldNumber(f.Path)
		if n <= 0 {
			continue
		}
		byField[n] = append(byField[n], f)
		if n > widest {
			widest = n
		}
	}

	fields := make([]string, widest+1)
	fields[0] = seg.ID

	for n := 1; n <= widest; n++ {
		fields[n] = buildField(byField[n], occurrence, rng)
	}

	return strings.Join(fields, "|")
}

// buildField renders one field, which may be composite or repeating.
func buildField(candidates []Field, occurrence int, rng *rand.Rand) string {
	if len(candidates) == 0 {
		return ""
	}

	// The whole-field entry when there is one, otherwise the components.
	var whole *Field
	var components []Field
	for i := range candidates {
		f := candidates[i]
		if strings.Contains(f.Path, ".") {
			components = append(components, f)
			continue
		}
		whole = &candidates[i]
	}

	if len(components) > 0 {
		sort.Slice(components, func(i, j int) bool {
			return componentNumber(components[i].Path) < componentNumber(components[j].Path)
		})
		widest := componentNumber(components[len(components)-1].Path)
		parts := make([]string, widest)
		for _, c := range components {
			idx := componentNumber(c.Path) - 1
			if idx < 0 || idx >= len(parts) {
				continue
			}
			if !populated(c, rng) {
				continue
			}
			parts[idx] = valueFor(c, occurrence, rng)
		}
		return strings.Join(parts, "^")
	}

	if whole == nil || !populated(*whole, rng) {
		return ""
	}

	value := valueFor(*whole, occurrence, rng)

	// Composite structure, where the field had it.
	//
	// The profiler records how many components a field carried but not what each one looked like, so the whole-field
	// length is divided across them. Worth doing rather than emitting one long run of characters: a name field written
	// as a single 30-character string exercises no name parsing at all, and name parsing is where interfaces break.
	if whole.Components > 1 && len(whole.Codes) == 0 {
		value = splitIntoComponents(value, whole.Components, occurrence, rng)
	}

	// Repeats where they were observed, because code written against a single value silently takes the first and the
	// only way to find that is to send two.
	if whole.MaxRepeats > 1 && rng.Float64() < 0.3 {
		second := valueFor(*whole, occurrence, rng)
		return value + "~" + second
	}
	return value
}

// populated decides whether to fill a field, at the rate it was really filled.
func populated(f Field, rng *rand.Rand) bool {
	if f.FillRate >= 1 {
		return true
	}
	if f.FillRate <= 0 {
		return false
	}
	return rng.Float64() < f.FillRate
}

// valueFor invents a value of the right shape and length.
//
// Nothing here is drawn from the profiled traffic, because the profile does not contain it. Codes are the exception and
// are the one thing that legitimately carries over: which codes a sender uses is the point of profiling, and a code
// table value identifies nobody.
func valueFor(f Field, occurrence int, rng *rand.Rand) string {
	if len(f.Codes) > 0 {
		return weightedCode(f.Codes, rng)
	}

	length := f.MinLength
	if f.MaxLength > f.MinLength {
		length = f.MinLength + rng.Intn(f.MaxLength-f.MinLength+1)
	}
	if length <= 0 {
		length = 1
	}

	switch f.Shape {
	case ShapeDate:
		// Two lengths only: HL7 dates are 8 or 14 digits and anything else is not a date. A random-length digit string
		// would parse as a date nowhere and fail every downstream conversion.
		if f.MaxLength >= 14 {
			return fmt.Sprintf("2026%02d%02d%02d%02d%02d", 1+rng.Intn(12), 1+rng.Intn(28), rng.Intn(24), rng.Intn(60), rng.Intn(60))
		}
		return fmt.Sprintf("2026%02d%02d", 1+rng.Intn(12), 1+rng.Intn(28))

	case ShapeNumeric:
		return digits(length, occurrence, rng)

	case ShapeAlpha:
		return letters(length, rng)

	case ShapeEmpty:
		return ""

	default:
		// Mixed and alphanumeric both get letters and digits, which is what those shapes mean.
		if length < 2 {
			return letters(length, rng)
		}
		return letters(length-1, rng) + digits(1, occurrence, rng)
	}
}

// weightedCode picks a code in the proportions observed.
func weightedCode(codes []CodeCount, rng *rand.Rand) string {
	total := 0
	for _, c := range codes {
		total += c.Count
	}
	if total <= 0 {
		return codes[0].Code
	}
	n := rng.Intn(total)
	for _, c := range codes {
		n -= c.Count
		if n < 0 {
			return c.Code
		}
	}
	return codes[len(codes)-1].Code
}

// digits produces a numeric string that is unique per message where it is long enough to be.
//
// The occurrence and a random tail, so identifiers differ between messages. A corpus where every patient has the same
// record number tests deduplication in exactly the wrong direction: everything looks like the same patient.
func digits(length, seq int, rng *rand.Rand) string {
	s := fmt.Sprintf("%d%d", seq, rng.Intn(1_000_000))
	for len(s) < length {
		s += fmt.Sprint(rng.Intn(10))
	}
	return s[:length]
}

func letters(length int, rng *rand.Rand) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if length <= 0 {
		return ""
	}
	b := make([]byte, length)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

// splitIntoComponents spreads a value across the number of components observed.
//
// The first component gets the bulk of it, because that is how composite fields behave in practice: a family name is
// longer than an initial, an identifier is longer than its assigning authority. Trailing components are sometimes left
// empty, since a real feed rarely populates every component of every composite.
func splitIntoComponents(value string, components, occurrence int, rng *rand.Rand) string {
	if components < 2 || len(value) < components {
		return value
	}

	parts := make([]string, components)
	remaining := value

	// Roughly half to the first component, then the rest shared out.
	head := max(len(value)/2, 1)
	parts[0] = remaining[:head]
	remaining = remaining[head:]

	per := len(remaining) / (components - 1)
	for i := 1; i < components && len(remaining) > 0; i++ {
		take := min(max(per, 1), len(remaining))
		// Some components genuinely go unpopulated, and an interface that assumes otherwise is one worth catching.
		if i > 1 && rng.Float64() < 0.25 {
			continue
		}
		parts[i] = remaining[:take]
		remaining = remaining[take:]
	}

	return strings.Join(parts, "^")
}

func componentNumber(path string) int {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range path[i+1:] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
