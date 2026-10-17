package edifact

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/expr"
)

// Path addresses data elements, in the notation the HL7 and X12 paths here use:
//
//	BGM            the whole segment, as written
//	BGM-2          element 2 (every component, joined with the component separator)
//	BGM-1.1        element 1, component 1
//	NAD(2)-3.1     the second NAD, element 3, component 1
//	LIN-3(2).1     element 3, second repetition, component 1
//	UNB-2.1        the interchange sender
//
// Element and component numbers count from 1 in the order the segment is written, which is how implementation guides
// number them (BGM's C002 is element 1, 1004 is element 2). A segment without a number means every occurrence.
type Path struct {
	Tag                            string
	Occurrence                     int // 0: every one
	Element, Repetition, Component int // 0: whole / every
}

var pathRe = regexp.MustCompile(`^([A-Z][A-Z0-9]{2})(?:\((\d+)\))?(?:-(\d+)(?:\((\d+)\))?(?:\.(\d+))?)?$`)

// ParsePath reads a path.
func ParsePath(s string) (Path, error) {
	m := pathRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Path{}, fmt.Errorf("edifact: %q is not a path; write a segment tag and an element, such as BGM-2 or NAD(2)-3.1", s)
	}
	n := func(x string) int { v, _ := strconv.Atoi(x); return v }
	p := Path{Tag: m[1], Occurrence: n(m[2]), Element: n(m[3]), Repetition: n(m[4]), Component: n(m[5])}
	if (m[2] != "" && p.Occurrence == 0) || (m[3] != "" && p.Element == 0) || (m[4] != "" && p.Repetition == 0) || (m[5] != "" && p.Component == 0) {
		return Path{}, fmt.Errorf("edifact: %q: numbers in a path count from 1", s)
	}
	return p, nil
}

// Read returns every value the path addresses in the interchange.
func Read(ic *Interchange, p Path) []string {
	var out []string
	occ := 0
	for _, s := range ic.Segments() {
		if s.Tag != p.Tag {
			continue
		}
		occ++
		if p.Occurrence != 0 && occ != p.Occurrence {
			continue
		}
		if p.Element == 0 {
			var b bytes.Buffer
			writeSegment(&b, s, ic.Delims)
			out = append(out, strings.TrimSuffix(b.String(), string(ic.Delims.Segment)))
			continue
		}
		if p.Element > len(s.Elements) {
			continue
		}
		el := s.Elements[p.Element-1]
		for ri, rep := range el {
			if p.Repetition != 0 && ri+1 != p.Repetition {
				continue
			}
			if p.Component == 0 {
				out = append(out, strings.Join(rep, string(ic.Delims.Component)))
			} else if p.Component <= len(rep) {
				out = append(out, rep[p.Component-1])
			}
		}
	}
	return out
}

// Resolver addresses interchanges for the filter grammar.
type Resolver struct{}

// Prepare compiles one path.
func (Resolver) Prepare(raw string) (expr.Prepared[*Interchange], error) {
	p, err := ParsePath(raw)
	if err != nil {
		return expr.Prepared[*Interchange]{}, err
	}
	return expr.Prepared[*Interchange]{
		Candidates: func(ic *Interchange) []expr.Value {
			vals := Read(ic, p)
			out := make([]expr.Value, 0, len(vals))
			for _, v := range vals {
				out = append(out, expr.Value{Text: v, Empty: strings.TrimSpace(v) == ""})
			}
			return out
		},
		Presence: func(ic *Interchange) (int, bool) {
			vals := Read(ic, p)
			allEmpty := true
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					allEmpty = false
					break
				}
			}
			return len(vals), allEmpty
		},
		Single: func(ic *Interchange) string {
			if vals := Read(ic, p); len(vals) > 0 {
				return vals[0]
			}
			return ""
		},
	}, nil
}

// Semantics keeps ordering numeric and quotes optional, as for X12: quantities and amounts are what gets compared, and
// EDIFACT dates (CCYYMMDD, YYMMDD) compare correctly either way.
func (Resolver) Semantics() expr.Semantics { return expr.Semantics{} }

// ParseFilter compiles a filter expression over an interchange.
func ParseFilter(src string) (expr.Expr[*Interchange], error) {
	return expr.ParseFor(src, Resolver{})
}
