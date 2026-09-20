package profile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/hl7dict"
)

// Building the profile.
//
// One pass per message, accumulating counts. Nothing is retained that could not be printed: the
// only values held are the ones a code table already made public, and a set of value hashes for
// counting distinctness. Holding hashes rather than values is what lets this run over a hundred
// thousand real messages without becoming a copy of them.

// fieldAcc accumulates one path.
type fieldAcc struct {
	path string

	present    int
	minLen     int
	maxLen     int
	maxRepeats int
	components int

	// seen counts distinct values by hash, so the values themselves are not retained. A
	// profile is not a place to keep patient data, and a map of medical record numbers is
	// exactly that.
	seen       map[uint64]bool
	seenCapped bool

	// codes is populated only for a field the dictionary says a table constrains, where the
	// values are not identifying and are the most useful thing in the report.
	codes map[string]int

	numeric bool
	alpha   bool
	other   bool
	dateish bool
	anyVal  bool
}

type segmentAcc struct {
	id       string
	messages int
	maxPer   int
	order    int

	fields map[string]*fieldAcc
	// fieldOrder keeps paths in the order first seen so the report reads like a message.
	fieldOrder []string
}

// Build profiles a corpus.
func Build(messages [][]byte) *Report {
	rep := &Report{}

	segs := map[string]*segmentAcc{}
	var segOrder []string
	types := map[string]int{}

	for _, raw := range messages {
		msg, err := hl7.Parse(raw)
		if err != nil {
			rep.Unreadable++
			continue
		}
		rep.Messages++

		// Type and trigger event together, because "ADT" alone hides the thing worth
		// knowing: a feed described as ADT routinely carries six trigger events, two of
		// which nobody was told about.
		if msgType, event, _ := msg.Type(); msgType != "" {
			label := msgType
			if event != "" {
				label += "^" + event
			}
			types[label]++
		}

		// Per message, so a segment appearing five times counts once towards its rate and
		// five towards its maximum. Conflating the two is how a report claims a segment
		// appears in 300% of messages.
		perMessage := map[string]int{}

		for i := 0; i < msg.SegmentCount(); i++ {
			seg, ok := msg.SegmentAt(i)
			if !ok {
				continue
			}
			id := seg.Name()
			if id == "" {
				continue
			}
			perMessage[id]++

			acc, seen := segs[id]
			if !seen {
				acc = &segmentAcc{id: id, fields: map[string]*fieldAcc{}, order: len(segOrder)}
				segs[id] = acc
				segOrder = append(segOrder, id)
			}

			observeSegment(acc, msg, id, perMessage[id])
		}

		for id, n := range perMessage {
			acc := segs[id]
			acc.messages++
			if n > acc.maxPer {
				acc.maxPer = n
			}
		}
	}

	rep.Types = summariseTypes(types, rep.Messages)
	rep.Segments = summariseSegments(segs, segOrder, rep.Messages)
	rep.Notes = notes(rep)

	return rep
}

// observeSegment records every populated field of one segment occurrence.
func observeSegment(acc *segmentAcc, msg *hl7.Message, id string, occurrence int) {
	// MSH-1 and MSH-2 are the delimiters themselves. Profiling them says nothing about the
	// feed and reports a field whose "values" are punctuation.
	start := 1
	if id == "MSH" {
		start = 3
	}

	for field := start; field <= maxFieldsToScan; field++ {
		path := fmt.Sprintf("%s-%d", id, field)
		if occurrence > 1 {
			path = fmt.Sprintf("%s(%d)-%d", id, occurrence, field)
		}

		p, err := hl7.ParsePath(path)
		if err != nil {
			continue
		}
		v := msg.ValueAt(p)
		if !v.Exists() || v.IsEmpty() {
			continue
		}

		// Recorded under the un-occurrenced path, so PID-5 in the second PID is the same
		// field as PID-5 in the first. An integrator asking about PID-5 means the field, not
		// one instance of it.
		key := fmt.Sprintf("%s-%d", id, field)

		fa, ok := acc.fields[key]
		if !ok {
			fa = newFieldAcc(key, id, field)
			acc.fields[key] = fa
			acc.fieldOrder = append(acc.fieldOrder, key)
		}

		observeValue(fa, v)
	}
}

// maxFieldsToScan bounds how far into a segment to look.
//
// The longest segment in common use is OBX at 25 fields; PID reaches 39 in later versions, and
// Z-segments are arbitrary. Fifty covers everything real without scanning a thousand empty
// positions per segment per message, which at a hundred thousand messages is the difference
// between a second and a minute.
const maxFieldsToScan = 50

func newFieldAcc(path, segment string, field int) *fieldAcc {
	fa := &fieldAcc{
		path:    path,
		seen:    map[uint64]bool{},
		minLen:  -1,
		numeric: true,
		alpha:   true,
		dateish: true,
	}

	// Codes are collected only where the dictionary says a table constrains the field. That
	// is the line between reporting the shape of a feed and reporting its contents.
	if f, ok := hl7dict.LookupField(segment, field); ok && f.Table != "" {
		fa.codes = map[string]int{}
	}
	return fa
}

// observeValue folds one value into the accumulator.
func observeValue(fa *fieldAcc, v hl7.Value) {
	fa.present++

	if n := len(v.Repeats()); n > fa.maxRepeats {
		fa.maxRepeats = n
	}
	if n := v.ComponentCount(); n > fa.components {
		fa.components = n
	}

	// Every repetition is examined, not only the first. A field whose second repetition has a
	// different shape from its first is exactly the surprise this exists to surface.
	for _, rep := range v.Repeats() {
		// A code table constrains the identifier component, not the whole composite. Table
		// 0076 defines "ADT", so comparing "ADT^A01" against it reports every message as
		// carrying an undefined code - which is how a report loses its credibility on the
		// first screen somebody looks at.
		codeValue := rep.String()
		if rep.ComponentCount() > 1 {
			codeValue = rep.Component(1).String()
		}

		observeString(fa, rep.String(), codeValue)
	}
}

// observeString folds one value into the accumulator: length, distinctness, vocabulary and shape.
//
// Extracted from observeValue so the v3 profiler shares it rather than carrying its own copy. Distinctness and shape
// classification are where a profile earns its credibility, and two implementations would drift - one of them would end
// up counting an empty value as a distinct one, or treating an 8-digit number as a date under different conditions, and
// the two profilers would then disagree about the same feed.
//
// codeValue is the part a code table constrains, which for v2 is the first component rather than the whole composite. For
// v3 it is the value itself, because a v3 code lives in its own attribute already.
func observeString(fa *fieldAcc, s, codeValue string) {
	if s == "" {
		return
	}
	fa.anyVal = true

	if fa.minLen < 0 || len(s) < fa.minLen {
		fa.minLen = len(s)
	}
	if len(s) > fa.maxLen {
		fa.maxLen = len(s)
	}

	if !fa.seenCapped {
		if len(fa.seen) >= maxDistinct {
			fa.seenCapped = true
		} else {
			fa.seen[hash(s)] = true
		}
	}

	if fa.codes != nil && len(fa.codes) < maxCodes {
		fa.codes[codeValue]++
	} else if fa.codes != nil {
		// Already at the cap: count an existing code, ignore a new one. A field with more than forty distinct
		// codes is not really a coded field, and the report says so through Distinct.
		if _, ok := fa.codes[codeValue]; ok {
			fa.codes[codeValue]++
		}
	}

	classify(fa, s)
}

// classify folds one value into the shape flags.
func classify(fa *fieldAcc, s string) {
	digits, letters := 0, 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letters++
		}
	}

	if digits != len(s) {
		fa.numeric = false
		fa.dateish = false
	}
	if letters != len(s) {
		fa.alpha = false
	}
	if digits == 0 && letters == 0 {
		fa.other = true
	}
	// An HL7 timestamp is 8 digits for a date and 14 with a time. Anything else that is all
	// digits is a number rather than a date.
	if fa.dateish && len(s) != 8 && len(s) != 14 && len(s) != 12 {
		fa.dateish = false
	}
}

// shape reduces the flags to one answer.
func (fa *fieldAcc) shape() Shape {
	switch {
	case !fa.anyVal:
		return ShapeEmpty
	case fa.dateish:
		return ShapeDate
	case fa.numeric:
		return ShapeNumeric
	case fa.alpha:
		return ShapeAlpha
	case !fa.other:
		return ShapeAlphanumeric
	default:
		return ShapeMixed
	}
}

func summariseTypes(types map[string]int, total int) []TypeCount {
	out := make([]TypeCount, 0, len(types))
	for t, n := range types {
		rate := 0.0
		if total > 0 {
			rate = float64(n) / float64(total)
		}
		out = append(out, TypeCount{Type: t, Count: n, Rate: rate})
	}
	// Commonest first, then alphabetically. Sorting is not cosmetic here: a Go map ranges
	// randomly, so without it two profiles of the same corpus would differ and anybody
	// diffing them would see changes that are not there.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func summariseSegments(segs map[string]*segmentAcc, order []string, total int) []Segment {
	out := make([]Segment, 0, len(segs))

	for _, id := range order {
		acc := segs[id]

		def, described := hl7dict.LookupSegment(id)

		// The dictionary deliberately answers yes for a Z-segment, describing it as locally
		// defined, so its answer cannot be used as the test for standardness. A locally
		// defined segment is exactly what this report needs to single out.
		known := described && !strings.HasPrefix(strings.ToUpper(id), "Z")

		rate := 0.0
		if total > 0 {
			rate = float64(acc.messages) / float64(total)
		}

		s := Segment{
			ID:            id,
			Standard:      known,
			Messages:      acc.messages,
			Rate:          rate,
			MaxPerMessage: acc.maxPer,
		}
		// The description is worth showing either way: for a Z-segment it says it is
		// locally defined, which is the useful thing to tell somebody.
		if described {
			s.Name = def.Description
		}

		// Sorted by field number rather than first-seen, because a segment reads in field
		// order and a list jumping from 3 to 11 to 5 is hard to scan.
		paths := append([]string{}, acc.fieldOrder...)
		sort.Slice(paths, func(i, j int) bool {
			return fieldNumber(paths[i]) < fieldNumber(paths[j])
		})

		for _, key := range paths {
			s.Fields = append(s.Fields, acc.fields[key].summarise(id, acc.messages))
		}

		out = append(out, s)
	}

	return out
}

func (fa *fieldAcc) summarise(segment string, segmentMessages int) Field {
	f := Field{
		Path:       fa.path,
		Present:    fa.present,
		Distinct:   len(fa.seen),
		Shape:      fa.shape(),
		MaxLength:  fa.maxLen,
		MaxRepeats: fa.maxRepeats,
	}
	if fa.minLen > 0 {
		f.MinLength = fa.minLen
	}
	if fa.components > 1 {
		f.Components = fa.components
	}
	if fa.seenCapped {
		f.DistinctCapped = true
	}

	// Relative to the segment, not the corpus. OBX-5 populated in 30% of messages means
	// something entirely different from OBX-5 populated in 30% of messages that have an OBX.
	if segmentMessages > 0 {
		f.FillRate = float64(fa.present) / float64(segmentMessages)
		if f.FillRate > 1 {
			// A repeated segment can carry more populated instances than there are
			// messages. Reported as fully populated rather than as 340%, which reads like
			// a bug.
			f.FillRate = 1
		}
	}

	if def, ok := hl7dict.LookupField(segment, fieldNumber(fa.path)); ok {
		f.Name = def.Name
		f.Table = def.Table
	}

	if fa.codes != nil {
		f.Codes = summariseCodes(fa.codes, f.Table)
	}

	return f
}

func summariseCodes(counts map[string]int, table string) []CodeCount {
	out := make([]CodeCount, 0, len(counts))
	for code, n := range counts {
		cc := CodeCount{Code: code, Count: n}
		if table != "" {
			if meaning, ok := hl7dict.ExplainCode(table, code); ok {
				cc.Meaning = meaning
				cc.Known = true
			}
		}
		out = append(out, cc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// fieldNumber extracts the field number from a path like "PID-5".
func fieldNumber(path string) int {
	i := strings.LastIndex(path, "-")
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range path[i+1:] {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// hash is FNV-1a, inlined to avoid retaining the value it summarises.
func hash(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}
