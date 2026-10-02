package equiv

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// maxExamples caps the control ids kept per finding.
//
// Three is enough to go and look, and a list of 3,000 ids is not an example - it is the raw data the
// grouping existed to summarise.
const maxExamples = 3

// Options controls a comparison.
type Options struct {
	// LeftName and RightName are what to call the two sides. Default to "the left" and "the right",
	// which is honest but not useful; callers should say "Mirth" and "Perfuse".
	LeftName  string
	RightName string

	// IncludeContent keeps sample values in the report. Off by default because real traffic is patient
	// data and a report is a thing people paste into tickets.
	IncludeContent bool

	// IgnorePaths are paths whose differences are expected and should not be reported. Two engines
	// legitimately disagree about some fields - a timestamp of when the message was processed, a
	// control id the new engine generates itself - and without a way to say so the report is dominated
	// by differences nobody intends to fix.
	IgnorePaths []string
}

func (o Options) leftName() string {
	if o.LeftName == "" {
		return "the left"
	}
	return o.LeftName
}

func (o Options) rightName() string {
	if o.RightName == "" {
		return "the right"
	}
	return o.RightName
}

// Compare compares two sets of outputs keyed by message identity.
//
// The key is the caller's business, but in practice it is MSH-10, the message control id: every HL7
// message has one, both engines preserve it, and it is the only thing that reliably says "this output
// and that output came from the same input".
func Compare(left, right map[string][]byte, opts Options) *Report {
	report := &Report{
		IncludesContent: opts.IncludeContent,
		LeftName:        opts.leftName(),
		RightName:       opts.rightName(),
	}

	ignore := make(map[string]bool, len(opts.IgnorePaths))
	for _, p := range opts.IgnorePaths {
		ignore[strings.ToUpper(strings.TrimSpace(p))] = true
	}

	// Grouping happens here: every difference is filed under its cause, so the number of findings is
	// bounded by the number of distinct problems rather than the number of messages.
	groups := map[string]*group{}

	for _, key := range sortedKeys(left) {
		rightBody, ok := right[key]
		if !ok {
			report.Summary.OnlyLeft = append(report.Summary.OnlyLeft, key)
			continue
		}

		report.Summary.Compared++
		diffs := compareOne(key, left[key], rightBody, ignore)
		if len(diffs) == 0 {
			report.Summary.Identical++
			continue
		}

		report.Summary.Differing++
		for _, d := range diffs {
			file(groups, d, key)
		}
	}

	for _, key := range sortedKeys(right) {
		if _, ok := left[key]; !ok {
			report.Summary.OnlyRight = append(report.Summary.OnlyRight, key)
		}
	}

	report.Findings = finish(groups, opts.IncludeContent)
	sortFindings(report.Findings)
	return report
}

// diff is one difference in one message, before grouping.
type diff struct {
	path  string
	kind  DiffKind
	left  string
	right string
}

// group accumulates one cause across many messages.
type group struct {
	path     string
	kind     DiffKind
	count    int
	examples []string

	// pairs counts distinct left/right combinations. One combination means a single consistent
	// substitution - a code set or a constant - which is usually a one-line fix, and saying so is more
	// useful than the count of affected messages.
	pairs map[string]bool

	// first sample, kept whether or not it will be reported, because deciding later is cheaper than
	// walking the traffic again.
	firstLeft  string
	firstRight string
}

func file(groups map[string]*group, d diff, key string) {
	id := d.path + "\x00" + string(d.kind)

	g, ok := groups[id]
	if !ok {
		g = &group{
			path:       d.path,
			kind:       d.kind,
			pairs:      map[string]bool{},
			firstLeft:  d.left,
			firstRight: d.right,
		}
		groups[id] = g
	}

	g.count++
	g.pairs[d.left+"\x00"+d.right] = true
	if len(g.examples) < maxExamples {
		g.examples = append(g.examples, key)
	}
}

func finish(groups map[string]*group, includeContent bool) []Finding {
	findings := make([]Finding, 0, len(groups))

	for _, g := range groups {
		f := Finding{
			Path:          g.path,
			Kind:          g.kind,
			Count:         g.count,
			Examples:      g.examples,
			DistinctPairs: len(g.pairs),
		}
		if includeContent {
			f.Left = g.firstLeft
			f.Right = g.firstRight
		}
		findings = append(findings, f)
	}

	return findings
}

// compareOne compares a single pair of outputs.
func compareOne(key string, left, right []byte, ignore map[string]bool) []diff {
	// Byte equality first. It is the overwhelmingly common case in a successful parallel run, and
	// parsing both sides to discover they are identical would make the whole exercise slow enough that
	// nobody runs it on a real corpus.
	if string(left) == string(right) {
		return nil
	}

	leftTree, leftErr := hl7xml.FromRaw(left)
	rightTree, rightErr := hl7xml.FromRaw(right)

	if leftErr != nil || rightErr != nil {
		// The most serious finding available, and not a mapping problem. The path names which side so
		// the report does not make somebody open both files to find out.
		side := "both sides"
		switch {
		case leftErr != nil && rightErr == nil:
			side = "the left"
		case rightErr != nil && leftErr == nil:
			side = "the right"
		}
		return []diff{{path: side, kind: Unparseable}}
	}

	var diffs []diff
	compareSegments(leftTree, rightTree, ignore, &diffs)
	return diffs
}

// compareSegments walks the two messages segment by segment.
//
// Segment-level differences are reported as segments rather than as their fields. One missing PID would
// otherwise appear as twenty missing fields: the same cause, reported twenty times, in a report whose
// entire purpose is to report each cause once.
func compareSegments(left, right *xtree.Node, ignore map[string]bool, out *[]diff) {
	names := segmentNames(left, right)

	for _, name := range names {
		if ignore[name] {
			continue
		}

		leftSegs := left.All(name)
		rightSegs := right.All(name)

		if len(leftSegs) > 0 && len(rightSegs) == 0 {
			*out = append(*out, diff{path: name, kind: SegmentMissingOnRight})
			continue
		}
		if len(rightSegs) > 0 && len(leftSegs) == 0 {
			*out = append(*out, diff{path: name, kind: SegmentMissingOnLeft})
			continue
		}

		if len(leftSegs) != len(rightSegs) {
			*out = append(*out, diff{
				path:  name,
				kind:  RepeatCountDiffers,
				left:  fmt.Sprintf("%d", len(leftSegs)),
				right: fmt.Sprintf("%d", len(rightSegs)),
			})
			// Still compare the segments that exist on both sides. A count difference usually has a
			// cause visible in the fields, and stopping here would hide it.
		}

		n := len(leftSegs)
		if len(rightSegs) < n {
			n = len(rightSegs)
		}

		for i := 0; i < n; i++ {
			// The repetition index is left out of the path when there is only one, because "PID-5"
			// reads better than "PID[0]-5" and the overwhelming majority of segments occur once.
			prefix := name
			if len(leftSegs) > 1 || len(rightSegs) > 1 {
				prefix = fmt.Sprintf("%s[%d]", name, i+1)
			}
			compareFields(prefix, leftSegs[i], rightSegs[i], ignore, out)
		}
	}
}

// compareFields walks one segment's fields.
func compareFields(prefix string, left, right *xtree.Node, ignore map[string]bool, out *[]diff) {
	names := childNames(left, right)

	for _, name := range names {
		leftVals := left.All(name)
		rightVals := right.All(name)

		// Field names in the tree are already qualified - "PID.5" inside PID - so the path is built
		// from the field number rather than repeating the segment name.
		path := prefix + "-" + fieldNumber(name)
		if ignore[strings.ToUpper(path)] {
			continue
		}

		if len(leftVals) != len(rightVals) && len(leftVals) > 0 && len(rightVals) > 0 {
			*out = append(*out, diff{
				path:  path,
				kind:  RepeatCountDiffers,
				left:  fmt.Sprintf("%d", len(leftVals)),
				right: fmt.Sprintf("%d", len(rightVals)),
			})
		}

		leftText, leftPresent := textOf(leftVals)
		rightText, rightPresent := textOf(rightVals)

		switch {
		case !leftPresent && !rightPresent:
			// Neither side has it, which is agreement.

		case leftPresent && !rightPresent:
			*out = append(*out, diff{path: path, kind: MissingOnRight, left: leftText})

		case rightPresent && !leftPresent:
			*out = append(*out, diff{path: path, kind: MissingOnLeft, right: rightText})

		case leftText == rightText:
			// Equal, so nothing to say.

		case leftText != "" && rightText == "":
			// Present but empty on the right. Distinguished from absent because in an update message an
			// empty field means "no change" and an absent one means "never sent".
			*out = append(*out, diff{path: path, kind: EmptyOnRight, left: leftText})

		case rightText != "" && leftText == "":
			*out = append(*out, diff{path: path, kind: EmptyOnLeft, right: rightText})

		default:
			*out = append(*out, diff{
				path:  path,
				kind:  ValueDiffers,
				left:  leftText,
				right: rightText,
			})
		}
	}
}

// textOf returns the joined text of a field's repetitions and whether the field was present at all.
//
// Presence and emptiness are returned separately because they mean different things and a single string
// cannot carry both.
func textOf(nodes []*xtree.Node) (string, bool) {
	if len(nodes) == 0 {
		return "", false
	}

	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, n.Value())
	}
	return strings.Join(parts, "~"), true
}

// segmentNames is every segment name in either message, in the order the left message uses them, with
// anything only the right message has appended.
//
// Left order rather than alphabetical, because a report that walks the message in the order it is
// written reads like the message; one sorted alphabetically reads like a dictionary.
func segmentNames(left, right *xtree.Node) []string {
	return childNames(left, right)
}

func childNames(left, right *xtree.Node) []string {
	seen := map[string]bool{}
	var names []string

	for _, n := range left.Children {
		if !seen[n.Name] {
			seen[n.Name] = true
			names = append(names, n.Name)
		}
	}

	var extra []string
	for _, n := range right.Children {
		if !seen[n.Name] {
			seen[n.Name] = true
			extra = append(extra, n.Name)
		}
	}
	sort.Strings(extra)

	return append(names, extra...)
}

// fieldNumber turns a tree field name into the number a path uses: "PID.5" becomes "5".
func fieldNumber(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// KeyOf returns the identity of a message for pairing the two sides.
//
// MSH-10, the control id, because every HL7 message has one, both engines preserve it, and it is the
// only field that reliably says two outputs came from the same input. A message without one cannot be
// paired, and saying so beats pairing it with the wrong partner - which would manufacture differences
// that do not exist and is the worst thing this package could do.
func KeyOf(raw []byte) (string, error) {
	msg, err := hl7.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cannot read the message to find its control id: %w", err)
	}

	id := strings.TrimSpace(msg.MustGet("MSH-10"))
	if id == "" {
		return "", fmt.Errorf("the message has no MSH-10 control id, so it cannot be paired")
	}
	return id, nil
}
