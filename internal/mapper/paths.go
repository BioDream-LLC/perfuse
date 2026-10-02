package mapper

import (
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/hl7dict"
)

// pathMeaning returns what an HL7 v2 path names, or an empty string if it names nothing knowable.
//
// The mapper's whole purpose is mapping between one system's field names and another's, and in practice one side is an HL7 path. A
// path is a poor thing to compare a name against: PID-7 and DateOfBirth are the same field and share no characters. So the path is
// translated into what the standard calls it, and the comparison happens between two descriptions.
//
// Empty for anything not knowable, which includes locally defined Z segments. A Z segment means whatever one site decided it means,
// so a guess about it would be a fabrication, and returning nothing leaves the literal spelling to be compared instead.
func pathMeaning(path string) string {
	path = strings.TrimSpace(path)

	segment, field, component, ok := splitPath(path)
	if !ok {
		return ""
	}

	f, ok := hl7dict.LookupField(segment, field)
	if !ok {
		return ""
	}

	// A component names something narrower than its field: PID-5.1 is the family name, not the patient name. Preferring the narrower
	// description is what makes a mapping to a component distinguishable from a mapping to the whole field.
	if component > 0 && component <= len(f.Components) {
		return f.Components[component-1]
	}

	return f.Name
}

// splitPath breaks "PID-5.1" into its segment, field and component.
//
// The component is zero when the path does not name one. Deliberately strict: anything that is not this shape is not a path, and
// guessing at a near miss would put a fabricated meaning behind a confidence score.
func splitPath(path string) (segment string, field, component int, ok bool) {
	dash := strings.IndexByte(path, '-')
	if dash != 3 {
		return "", 0, 0, false
	}

	segment = strings.ToUpper(path[:dash])
	for i := 0; i < len(segment); i++ {
		if segment[i] < 'A' || segment[i] > 'Z' {
			return "", 0, 0, false
		}
	}

	rest := path[dash+1:]
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		c, err := strconv.Atoi(rest[dot+1:])
		if err != nil || c < 1 {
			return "", 0, 0, false
		}

		component = c
		rest = rest[:dot]
	}

	f, err := strconv.Atoi(rest)
	if err != nil || f < 1 {
		return "", 0, 0, false
	}

	return segment, f, component, true
}
