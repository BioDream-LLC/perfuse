package deident

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// Turning real traffic into a corpus that can be shared.
//
// Everybody needs realistic test data and nobody can legally use production. So test corpora get
// hand-written, which means they contain exactly the cases somebody thought of - and the cases
// nobody thought of are the ones that break interfaces. This takes real messages and produces
// ones that keep everything an integrator needs and none of what identifies a patient.
//
// Two properties make the output useful rather than merely safe.
//
// Referential integrity is preserved. The same patient gets the same fake record number in every
// message, so a corpus still exercises merges, updates and readmissions. A scrubber that
// randomised each occurrence would produce data that looks right and cannot test anything: every
// message would be a different patient.
//
// Shape is preserved. A seven-digit record number becomes a different seven-digit record number,
// a date stays a valid date, a coded field keeps its code. Anything reading the corpus behaves as
// it would on the real feed, which is the whole point.
//
// # Deny by default
//
// The field list says what to keep, not what to remove. Anything not named is replaced.
//
// That direction is the entire safety argument. A scrubber configured with a list of fields to
// remove is safe only for the messages its author examined: the first Z-segment carrying a name,
// the first site that puts an address in an unexpected field, and it passes them straight
// through while reporting success. Denying by default means an unrecognised field is scrubbed,
// and the cost of that mistake is a less useful corpus rather than a disclosure.

// Mode decides how a value is replaced.
type Mode string

const (
	// Keep passes the value through unchanged. Only for values that cannot identify anybody:
	// message structure, code table values, timestamps of the message itself.
	Keep Mode = "keep"

	// Pseudonym replaces the value with a stable fake of the same shape. The same input always
	// gives the same output for a given salt, which is what preserves referential integrity.
	Pseudonym Mode = "pseudonym"

	// Redact replaces the value with a fixed marker. For anything whose shape does not matter
	// and whose presence does.
	Redact Mode = "redact"

	// Remove empties the field entirely.
	Remove Mode = "remove"

	// ShiftDate moves a date by a stable per-patient offset, keeping intervals intact.
	//
	// This is the one that needs explaining. Replacing a date of birth with a random date
	// destroys age, which is clinically significant and is often what a mapping is tested on.
	// Shifting every date for one patient by the same number of days keeps every interval -
	// age at admission, length of stay, time between results - while making the absolute dates
	// wrong. It is the standard approach and it is what makes a de-identified corpus still
	// useful for testing anything time-based.
	ShiftDate Mode = "shift-date"
)

// Rule is what to do with one path.
type Rule struct {
	// Path is in the usual notation: PID-5, PID-5.1.
	Path string
	Mode Mode
	// Why records the reasoning, because a rule list is read by whoever has to defend it.
	Why string
}

// Options configure a scrub.
type Options struct {
	// Salt keys the pseudonyms. The same salt gives the same corpus, which is what makes a
	// scrub reproducible; a different salt gives an unrelated one.
	//
	// It must be kept secret. Pseudonyms are derived from real values, so anybody holding the
	// salt and a guess at a record number can confirm the guess. That is true of every
	// keyed-hash scheme and is the reason the salt is a secret rather than a setting.
	Salt []byte

	// Rules override or extend the defaults, matched most specific first.
	Rules []Rule

	// KeepUnknownSegments passes segments the dictionary does not define straight through.
	//
	// Off by default, and the default is the important one. A Z-segment is where a site puts
	// the data that did not fit anywhere else, which in practice means names, notes and
	// identifiers. Passing them through because their contents are unknown is the exact
	// mistake this design exists to prevent.
	KeepUnknownSegments bool
}

// scrubber holds the state for one run.
type scrubber struct {
	opts Options
	// rules is the resolved rule set, keyed by upper-case path.
	rules map[string]Rule
	// shifts caches the per-patient day offset so every date for one patient moves together.
	shifts map[string]int
	// stats records what was done, so a run can be reported rather than trusted.
	stats Stats
}

// Stats reports what a scrub did.
type Stats struct {
	Messages int `json:"messages"`
	// Unreadable messages are not emitted at all. A message that cannot be parsed cannot be
	// scrubbed, and passing it through unchanged would be the one leak in an otherwise clean
	// corpus.
	Unreadable int `json:"unreadable"`

	Kept       int `json:"kept"`
	Pseudonyms int `json:"pseudonyms"`
	Redacted   int `json:"redacted"`
	Removed    int `json:"removed"`
	Shifted    int `json:"shifted"`

	// UnknownFields counts values scrubbed because no rule named them. A large number here
	// means the rule set does not describe this feed well, and the corpus will be less useful
	// than it could be - which is the right way round for the mistake to fall.
	UnknownFields int `json:"unknownFields"`

	// DroppedSegments counts segments removed for being undefined.
	DroppedSegments int `json:"droppedSegments"`
}

// pseudonym derives a stable replacement of the same shape.
//
// HMAC rather than a plain hash, because the salt has to be a key and not merely a prefix: with
// a plain hash, anybody who knows the construction can test candidate record numbers offline
// without the salt at all.
func (s *scrubber) pseudonym(value string) string {
	if value == "" {
		return ""
	}

	mac := hmac.New(sha256.New, s.opts.Salt)
	mac.Write([]byte(value))
	sum := mac.Sum(nil)

	// The shape of the input decides the shape of the output. A receiver that parses a record
	// number as an integer, or a fixed-width column that truncates, must behave the same on the
	// corpus as on the feed, or the corpus is not testing the same thing.
	switch classify(value) {
	case shapeDigits:
		return digitsLike(value, sum)
	case shapeUpper:
		return lettersLike(value, sum, true)
	case shapeLower:
		return lettersLike(value, sum, false)
	case shapeTitle:
		return titleLike(value, sum)
	default:
		return mixedLike(value, sum)
	}
}

type shape int

const (
	shapeDigits shape = iota
	shapeUpper
	shapeLower
	shapeTitle
	shapeMixed
)

func classify(v string) shape {
	digits, upper, lower := 0, 0, 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= 'a' && r <= 'z':
			lower++
		}
	}
	n := len([]rune(v))
	switch {
	case digits == n:
		return shapeDigits
	case upper == n:
		return shapeUpper
	case lower == n:
		return shapeLower
	case upper == 1 && lower == n-1:
		return shapeTitle
	default:
		return shapeMixed
	}
}

// digitsLike produces digits of the same length, preserving leading zeroes.
//
// Length is preserved because a record number's length is frequently load-bearing: a fixed-width
// export, a check digit, a downstream column. Leading zeroes are preserved for the same reason,
// and because a scrubber that turned 0001234 into 8471923 would quietly fix a bug the corpus
// exists to reproduce.
func digitsLike(value string, sum []byte) string {
	out := make([]byte, 0, len(value))
	for i := range value {
		b := sum[i%len(sum)]
		if value[i] == '0' && i < len(value)-1 && allLeadingZero(value, i) {
			out = append(out, '0')
			continue
		}
		out = append(out, '0'+b%10)
	}
	return string(out)
}

func allLeadingZero(value string, upto int) bool {
	for i := 0; i <= upto; i++ {
		if value[i] != '0' {
			return false
		}
	}
	return true
}

func lettersLike(value string, sum []byte, upper bool) string {
	base := byte('a')
	if upper {
		base = 'A'
	}
	out := make([]byte, 0, len(value))
	for i := range value {
		out = append(out, base+sum[i%len(sum)]%26)
	}
	return string(out)
}

func titleLike(value string, sum []byte) string {
	if value == "" {
		return ""
	}
	out := []byte(lettersLike(value, sum, false))
	out[0] = 'A' + sum[0]%26
	return string(out)
}

// mixedLike keeps punctuation and structure while replacing letters and digits.
//
// Structure matters more than it looks: a phone number, a postcode and a compound identifier all
// carry meaning in their separators, and a parser tested against a corpus where those changed is
// not tested against the feed.
func mixedLike(value string, sum []byte) string {
	out := make([]rune, 0, len(value))
	i := 0
	for _, r := range value {
		b := sum[i%len(sum)]
		switch {
		case r >= '0' && r <= '9':
			out = append(out, rune('0'+b%10))
		case r >= 'A' && r <= 'Z':
			out = append(out, rune('A'+b%26))
		case r >= 'a' && r <= 'z':
			out = append(out, rune('a'+b%26))
		default:
			// Separators, spaces and anything non-alphanumeric survive.
			out = append(out, r)
		}
		i++
	}
	return string(out)
}

// shiftFor returns the stable day offset for one patient key.
//
// Derived from the key rather than random, so a second run over the same data produces the same
// corpus. Bounded to a year either way: enough that an absolute date is wrong, little enough that
// a date stays plausible and does not fall outside a range a receiver validates.
func (s *scrubber) shiftFor(key string) int {
	if shift, ok := s.shifts[key]; ok {
		return shift
	}

	mac := hmac.New(sha256.New, s.opts.Salt)
	mac.Write([]byte("date-shift:" + key))
	sum := mac.Sum(nil)

	n := int(binary.BigEndian.Uint32(sum[:4]) % 730)
	shift := n - 365
	if shift == 0 {
		// A shift of zero would leave the date real, which is the one outcome that must not
		// happen silently.
		shift = 1
	}

	s.shifts[key] = shift
	return shift
}

// describeRules renders the rule set, for a report somebody has to defend.
func describeRules(rules map[string]Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		line := fmt.Sprintf("%s: %s", r.Path, r.Mode)
		if r.Why != "" {
			line += " — " + r.Why
		}
		out = append(out, line)
	}
	return out
}

func upperPath(p string) string { return strings.ToUpper(strings.TrimSpace(p)) }
