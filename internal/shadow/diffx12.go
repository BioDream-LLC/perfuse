package shadow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// DiffX12 reports where two X12 interchanges disagree.
//
// # Why X12 needs its own walker
//
// Diff walks HL7 v2 by segment and field, splitting on the delimiters an MSH declares. Handed an X12 interchange it finds no
// MSH, treats the whole thing as one unparseable blob, and reports either nothing or everything - and "everything" is the
// dangerous answer, because a shadow report claiming a candidate rewrites the entire message is indistinguishable from a
// candidate that genuinely does, so somebody investigating learns nothing and eventually stops reading the reports.
//
// The paths are the ones the X12 filter and transformation steps already use - CLM01, NM103 - so a difference names a location
// somebody can look up in an implementation guide, and the same string can be pasted into an ignore list.
//
// # Occurrence matters more here than in v2
//
// An 837 carries many CLM segments, one per claim, and they are not distinguished by anything in the segment identifier. So a
// repeated segment is addressed with its occurrence, CLM(2)03, counting from one. Without that, two hundred claims would
// collapse into one path and a change to the fourth would either be invisible or reported against the first.
func DiffX12(live, candidate []byte, opts DiffOptions) []FieldDifference {
	liveFields, liveErr := x12Fields(live)
	candFields, candErr := x12Fields(candidate)

	// A parse failure on either side is reported rather than swallowed. Returning no differences would say "these are
	// identical", which is the wrong answer and the one that gets a bad candidate promoted - the same defect DiffXML had when
	// it returned nothing for raw HL7 and nothing meant identical.
	//
	// Reported as a difference against a whole-interchange path rather than through an error return, because FieldDifference
	// is what reaches the report and a runner that dropped the error would show an empty difference list.
	if liveErr != nil || candErr != nil {
		which, err := "the live output", liveErr
		if liveErr == nil {
			which, err = "the candidate output", candErr
		}

		return []FieldDifference{{
			Path:      "(whole interchange)",
			Live:      fmt.Sprintf("%d bytes", len(live)),
			Candidate: fmt.Sprintf("%s could not be parsed as X12, so the two cannot be compared: %v", which, err),
		}}
	}

	paths := opts.Compare
	if len(paths) == 0 {
		paths = unionKeys(liveFields, candFields)
	}

	var out []FieldDifference
	for _, p := range paths {
		if opts.Ignore[strings.ToUpper(p)] {
			continue
		}

		l := liveFields[p]
		c := candFields[p]
		if l == c {
			continue
		}

		out = append(out, FieldDifference{Path: p, Live: l, Candidate: c})
		if len(out) >= maxDifferenceFields {
			break
		}
	}

	return out
}

// x12Fields flattens an interchange into path to value.
//
// Empty elements are deliberately included. A candidate that clears CLM02 has changed the message, and a walker that only
// collected populated fields would compare "500" against a missing key and report nothing - which is the failure mode where a
// diff is silent about a deletion.
func x12Fields(raw []byte) (map[string]string, error) {
	msg, err := x12.Parse(raw)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string)
	counts := make(map[string]int)

	for i := 0; i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if !ok {
			continue
		}

		counts[seg.ID]++
		occurrence := counts[seg.ID]

		for position := 1; position <= seg.ElementCount(); position++ {
			// Elements are numbered from one, matching every X12 implementation guide and the filter path syntax. The
			// segment identifier is not element zero: CLM01 is the first element after CLM.
			value := seg.Element(position).String()

			// The unqualified path for the first occurrence, so that CLM01 works on a single-claim interchange without
			// anybody having to write CLM(1)01 - which is correct and which nobody writes.
			if occurrence == 1 {
				out[fmt.Sprintf("%s%02d", seg.ID, position)] = value
			}
			out[fmt.Sprintf("%s(%d)%02d", seg.ID, occurrence, position)] = value
		}
	}

	return out, nil
}

// unionKeys returns every path present in either map, sorted.
//
// Sorted because a shadow report is read by a person and diffed against yesterday's. Map order would make an unchanged
// candidate appear to produce a different report on every run.
func unionKeys(a, b map[string]string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}
