// The HL7 v2 binding.
//
// This is the logic that used to live inline in eval.go. It is unchanged in behaviour - deliberately, because the v2
// filter is what every HL7 channel runs and the point of the generic grammar was to add formats, not to alter this one.
package expr

import "github.com/biodream-llc/perfuse/hl7"

// HL7Resolver addresses HL7 v2 messages.
type HL7Resolver struct{}

// Prepare parses an HL7 path and returns closures that read it.
func (HL7Resolver) Prepare(path string) (Prepared[*hl7.Message], error) {
	p, err := hl7.ParsePath(path)
	if err != nil {
		return Prepared[*hl7.Message]{}, err
	}

	return Prepared[*hl7.Message]{
		Candidates: func(m *hl7.Message) []Value { return hl7Candidates(m, p) },
		Presence:   func(m *hl7.Message) (int, bool) { return hl7Presence(m, p) },
		Single:     func(m *hl7.Message) string { return m.ValueAt(p).String() },
	}, nil
}

// hl7Presence counts the addressed values and reports whether all of them are empty.
//
// Walks the same values as hl7Candidates but never calls String, because exists and empty do not need the text and
// materialising it for them slowed every filtered message.
func hl7Presence(m *hl7.Message, p hl7.Path) (int, bool) {
	if p.Segment == "MSH" && p.Field > 0 && p.Field <= 2 {
		if v := m.ValueAt(p); v.Exists() {
			return 1, v.IsEmpty()
		}
		return 0, true
	}

	if p.FieldRepeat > 0 || p.Field == 0 {
		if v := m.ValueAt(p); v.Exists() {
			return 1, v.IsEmpty()
		}
		return 0, true
	}

	base := p
	base.Component = 0
	base.Subcomponent = 0

	field := m.ValueAt(base)
	if !field.Exists() {
		return 0, true
	}

	count, allEmpty := 0, true
	for _, r := range field.Repeats() {
		v := r
		if p.Component > 0 {
			v = v.Component(p.Component)
		}
		if p.Subcomponent > 0 {
			v = v.Subcomponent(p.Subcomponent)
		}
		if !v.Exists() {
			continue
		}
		count++
		if !v.IsEmpty() {
			allEmpty = false
		}
	}
	return count, allEmpty
}

// hl7Candidates returns every value the path addresses.
//
// A path that names no repetition addresses all of them. This matters because PID-3.4 asks about the assigning authority
// of the patient identifier list, and that list repeats: a message carrying an MRN and a social security number has two.
// Reading only the first would make a filter miss the identifier it was written to find.
//
// When the path names a repetition explicitly there is exactly one candidate.
func hl7Candidates(m *hl7.Message, p hl7.Path) []Value {
	// MSH-1 and MSH-2 are the delimiters themselves, and MSH-2 contains the repetition separator, so splitting them on
	// it is meaningless.
	if p.Segment == "MSH" && p.Field > 0 && p.Field <= 2 {
		if v := m.ValueAt(p); v.Exists() {
			return []Value{{Text: v.String(), Empty: v.IsEmpty()}}
		}
		return nil
	}

	if p.FieldRepeat > 0 || p.Field == 0 {
		if v := m.ValueAt(p); v.Exists() {
			return []Value{{Text: v.String(), Empty: v.IsEmpty()}}
		}
		return nil
	}

	base := p
	base.Component = 0
	base.Subcomponent = 0

	field := m.ValueAt(base)
	if !field.Exists() {
		return nil
	}

	reps := field.Repeats()
	out := make([]Value, 0, len(reps))
	for _, r := range reps {
		v := r
		if p.Component > 0 {
			v = v.Component(p.Component)
		}
		if p.Subcomponent > 0 {
			v = v.Subcomponent(p.Subcomponent)
		}
		if v.Exists() {
			out = append(out, Value{Text: v.String(), Empty: v.IsEmpty()})
		}
	}
	return out
}
