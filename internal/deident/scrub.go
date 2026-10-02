package deident

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/hl7dict"
)

// Walking a message and replacing what has to go.
//
// The walk is over the segments and fields that are actually present, not over a list of paths to
// look for. That direction is deliberate and it is what makes deny-by-default real: a field
// nobody predicted is still visited, and still scrubbed.

// ErrNoSalt is returned when a scrub is attempted without a key.
//
// Refused rather than defaulted. A scrub with an empty or predictable salt produces pseudonyms
// anybody can reproduce, which means the output is reversible by guessing - and it would look
// exactly like a correct scrub.
var ErrNoSalt = errors.New(
	"de-identification needs a salt of at least 16 bytes: without one the pseudonyms can be " +
		"reproduced by anybody who guesses a record number, so the output would be reversible " +
		"while appearing scrubbed")

// New builds a scrubber.
func New(opts Options) (*Scrubber, error) {
	if len(opts.Salt) < 16 {
		return nil, ErrNoSalt
	}
	return &Scrubber{s: &scrubber{
		opts:   opts,
		rules:  resolve(opts.Rules),
		shifts: map[string]int{},
	}}, nil
}

// Scrubber de-identifies messages.
//
// Stateful on purpose: the pseudonym mapping and the per-patient date offsets have to be shared
// across a whole corpus, or the same patient becomes a different person in each message and the
// corpus cannot test anything that spans messages.
type Scrubber struct {
	s *scrubber
}

// Stats reports what has been done so far.
func (sc *Scrubber) Stats() Stats { return sc.s.stats }

// Rules renders the effective rule set, for a report somebody has to defend.
func (sc *Scrubber) Rules() []string { return describeRules(sc.s.rules) }

// Message scrubs one message.
func (sc *Scrubber) Message(raw []byte) ([]byte, error) {
	s := sc.s

	msg, err := hl7.Parse(raw)
	if err != nil {
		s.stats.Unreadable++
		// Refused rather than passed through. A message that cannot be parsed cannot be
		// scrubbed, and emitting it unchanged would be the single leak in an otherwise clean
		// corpus - the worst possible failure, because the corpus would be certified safe.
		return nil, fmt.Errorf("cannot scrub a message that does not parse: %w", err)
	}

	// The patient key anchors every pseudonym and date shift for this message. Derived from the
	// record number so that all of one patient's messages move together; falling back to the
	// account number, then the control ID.
	key := s.patientKey(msg)

	sep := msg.Separators()
	var out strings.Builder

	for i := 0; i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if !ok {
			continue
		}
		id := seg.Name()
		if id == "" {
			continue
		}

		if !s.segmentAllowed(id) {
			s.stats.DroppedSegments++
			continue
		}

		line, err := s.scrubSegment(msg, id, i, key, sep)
		if err != nil {
			return nil, err
		}

		out.WriteString(line)
		out.WriteByte('\r')
	}

	s.stats.Messages++
	return []byte(out.String()), nil
}

// segmentAllowed decides whether a segment survives at all.
func (s *scrubber) segmentAllowed(id string) bool {
	if s.opts.KeepUnknownSegments {
		return true
	}

	_, described := hl7dict.LookupSegment(id)
	if !described {
		return false
	}
	// The dictionary answers yes for Z-segments, describing them generically. That is not the
	// same as knowing what is in one, and a Z-segment is exactly where a site puts the data
	// that did not fit anywhere else - which in practice means names, notes and identifiers.
	return !strings.HasPrefix(strings.ToUpper(id), "Z")
}

// patientKey chooses a stable anchor for one patient.
func (s *scrubber) patientKey(msg *hl7.Message) string {
	for _, path := range []string{"PID-3.1", "PID-2", "PID-18.1", "PV1-19.1"} {
		if v := valueAt(msg, path); v != "" {
			return v
		}
	}
	// No identifier at all. Falling back to the control ID keeps the message internally
	// consistent, which is the best available: without a patient identifier there is nothing
	// to join this message to another by anyway.
	return valueAt(msg, "MSH-10")
}

func valueAt(msg *hl7.Message, path string) string {
	p, err := hl7.ParsePath(path)
	if err != nil {
		return ""
	}
	v := msg.ValueAt(p)
	if !v.Exists() {
		return ""
	}
	return v.String()
}

// scrubSegment rebuilds one segment, field by field.
func (s *scrubber) scrubSegment(
	msg *hl7.Message, id string, index int, key string, sep hl7.Separators,
) (string, error) {
	// MSH is special: MSH-1 and MSH-2 are the separators themselves, and rebuilding them from
	// parsed values would change the encoding of the message.
	fields := []string{id}

	last := s.lastPopulatedField(msg, id, index)

	start := 1
	if id == "MSH" {
		// MSH-1 is the field separator, which is what joins the fields, so it is never written
		// as a value. MSH-2 is written verbatim.
		fields = append(fields, string(sep.Component)+string(sep.Repeat)+
			string(sep.Escape)+string(sep.Subcomponent))
		start = 3
	}

	for field := start; field <= last; field++ {
		raw := s.rawField(msg, id, index, field)
		if raw == "" {
			fields = append(fields, "")
			continue
		}

		scrubbed, err := s.scrubValue(id, field, raw, key, sep)
		if err != nil {
			return "", err
		}
		fields = append(fields, scrubbed)
	}

	return strings.Join(fields, string(sep.Field)), nil
}

// lastPopulatedField finds how far a segment goes, so trailing empties are not written.
func (s *scrubber) lastPopulatedField(msg *hl7.Message, id string, index int) int {
	last := 0
	for field := 1; field <= maxFields; field++ {
		if s.rawField(msg, id, index, field) != "" {
			last = field
		}
	}
	return last
}

// maxFields bounds the scan. PID reaches 39 in later versions and OBX 25; fifty covers everything
// in use without scanning a thousand empty positions per segment.
const maxFields = 50

// rawField reads one field of one segment occurrence as text.
func (s *scrubber) rawField(msg *hl7.Message, id string, index, field int) string {
	occurrence := s.occurrenceOf(msg, id, index)

	path := fmt.Sprintf("%s(%d)-%d", id, occurrence, field)
	p, err := hl7.ParsePath(path)
	if err != nil {
		return ""
	}
	v := msg.ValueAt(p)
	if !v.Exists() {
		return ""
	}
	return v.String()
}

// occurrenceOf returns which occurrence of its own segment type this index is, 1-based.
func (s *scrubber) occurrenceOf(msg *hl7.Message, id string, index int) int {
	n := 0
	for i := 0; i <= index && i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if ok && seg.Name() == id {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// scrubValue applies the rule for one field, preserving repetition and component structure.
func (s *scrubber) scrubValue(
	id string, field int, raw, key string, sep hl7.Separators,
) (string, error) {
	path := fmt.Sprintf("%s-%d", id, field)

	rule, named := s.rules[upperPath(path)]
	if !named {
		// The default. Deny by default is the whole safety argument: a field nobody predicted
		// is replaced rather than passed through, and the cost of being wrong is a less useful
		// corpus instead of a disclosure.
		//
		// A value shaped like an HL7 timestamp is shifted rather than randomised, though.
		// Pseudonymising a date produces eight digits that are not a date - a real run turned
		// a date of birth into 76677303 - and a corpus carrying impossible dates breaks
		// anything that parses them, which is most of what a corpus is for. Shifting is still
		// de-identification; it just leaves the result valid.
		mode := Pseudonym
		why := "no rule names this field, so it is replaced"
		if looksLikeTimestamp(raw) {
			mode = ShiftDate
			why = "no rule names this field, but the value is a timestamp, so it is shifted " +
				"rather than randomised into an impossible date"
		}
		rule = Rule{Path: path, Mode: mode, Why: why}
		s.stats.UnknownFields++
	}

	switch rule.Mode {
	case Keep:
		s.stats.Kept++
		return raw, nil

	case Remove:
		s.stats.Removed++
		return "", nil

	case Redact:
		s.stats.Redacted++
		return "REDACTED", nil

	case ShiftDate:
		s.stats.Shifted++
		return s.shiftDates(raw, key, sep), nil

	case Pseudonym:
		s.stats.Pseudonyms++
		return s.pseudonymiseStructured(raw, sep), nil
	}

	return "", fmt.Errorf("unknown de-identification mode %q for %s", rule.Mode, path)
}

// pseudonymiseStructured replaces each component and repetition separately.
//
// Component by component rather than the whole field at once, because the structure carries
// meaning: PID-5 is family^given^middle, and replacing the joined string would produce one long
// token where a parser expects three components. A corpus that broke name parsing would not be
// testing the same thing as the feed.
func (s *scrubber) pseudonymiseStructured(raw string, sep hl7.Separators) string {
	reps := strings.Split(raw, string(sep.Repeat))

	for i, rep := range reps {
		comps := strings.Split(rep, string(sep.Component))
		for j, comp := range comps {
			subs := strings.Split(comp, string(sep.Subcomponent))
			for k, sub := range subs {
				if sub == "" {
					continue
				}
				subs[k] = s.pseudonym(sub)
			}
			comps[j] = strings.Join(subs, string(sep.Subcomponent))
		}
		reps[i] = strings.Join(comps, string(sep.Component))
	}

	return strings.Join(reps, string(sep.Repeat))
}

// shiftDates moves every date-looking component by the patient's offset.
func (s *scrubber) shiftDates(raw, key string, sep hl7.Separators) string {
	shift := s.shiftFor(key)

	comps := strings.Split(raw, string(sep.Component))
	for i, comp := range comps {
		comps[i] = shiftOne(comp, shift)
	}
	return strings.Join(comps, string(sep.Component))
}

// looksLikeTimestamp reports whether a value is an HL7 date or timestamp.
//
// Deliberately strict: exactly 8, 12 or 14 digits, and a date that actually parses. A seven-digit
// record number must not be mistaken for a date, and an eight-digit one that happens to read as a
// valid date is the awkward case - it gets shifted, which still de-identifies it and still leaves
// eight digits, so nothing downstream can tell the difference.
func looksLikeTimestamp(v string) bool {
	if len(v) != 8 && len(v) != 12 && len(v) != 14 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	_, err := time.Parse("20060102", v[:8])
	return err == nil
}

// shiftOne shifts one HL7 timestamp, keeping its precision.
//
// Precision is preserved because it is information: a system that sends a date where another sends
// a date and time behaves differently downstream, and a scrubber that normalised them would hide
// that difference from anybody testing against the corpus.
func shiftOne(value string, shiftDays int) string {
	if value == "" {
		return ""
	}

	// HL7 timestamps are YYYYMMDD optionally followed by HHMM, HHMMSS or fractional seconds.
	digits := value
	suffix := ""
	if i := strings.IndexAny(value, "+-"); i > 8 {
		// A timezone offset. Kept verbatim: shifting it would change the time rather than
		// the date.
		digits, suffix = value[:i], value[i:]
	}

	if len(digits) < 8 {
		// A year or year-month alone. Left as it is: it is not identifying on its own, and
		// shifting it by days is meaningless.
		return value
	}
	for _, r := range digits[:8] {
		if r < '0' || r > '9' {
			return value
		}
	}

	day, err := time.Parse("20060102", digits[:8])
	if err != nil {
		// Not a real date - 20261340, say. Left alone rather than corrected, because a corpus
		// exists partly to reproduce the malformed values a real feed carries.
		return value
	}

	shifted := day.AddDate(0, 0, shiftDays).Format("20060102")
	return shifted + digits[8:] + suffix
}
