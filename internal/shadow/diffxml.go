package shadow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Comparing two XML documents field by field.
//
// The v2 diff walks segment paths, which a v3 document does not have. Rather than compare the serialised text - which would
// report every difference in whitespace, attribute order and self-closing form as though it mattered - this walks both trees
// and compares what is addressable.
//
// The paths it reports are in the v3 path notation the filter and the field picker already use, so a difference can be pasted
// straight into either. A diff reporting paths in a notation nothing else accepts would make somebody translate by hand, which
// is where the mistakes are.

// DiffXML reports where two XML documents disagree.
//
// Attributes are compared as well as element text, and that is the whole point: in v3 a value is almost always an attribute, so
// a diff comparing only text would report two documents as identical when the birth date, the gender and every identifier had
// changed.
func DiffXML(live, candidate []byte, opts DiffOptions) []FieldDifference {
	liveTree, err := xtree.Parse(live)
	if err != nil {
		return nil
	}
	candidateTree, err := xtree.Parse(candidate)
	if err != nil {
		return nil
	}

	liveValues := map[string]string{}
	candidateValues := map[string]string{}
	collectXML(liveTree, "", liveValues)
	collectXML(candidateTree, "", candidateValues)

	paths := opts.Compare
	if len(paths) == 0 {
		seen := map[string]bool{}
		for p := range liveValues {
			seen[p] = true
		}
		for p := range candidateValues {
			seen[p] = true
		}
		paths = make([]string, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
	}

	// Sorted, because both maps range in an order nobody chose and a report that reorders itself between runs cannot be
	// compared with the previous one - which is the main thing somebody does with it.
	sort.Strings(paths)

	var out []FieldDifference

	for _, p := range paths {
		if opts.Ignore[strings.ToUpper(p)] {
			continue
		}

		lv, inLive := liveValues[p]
		cv, inCandidate := candidateValues[p]

		// Present-and-empty is distinguished from absent, and the distinction is reported in words rather than as
		// two empty strings. An element that was removed and an element that was emptied are different statements
		// in v3, and a diff showing both as "" would hide the one that matters.
		switch {
		case inLive && !inCandidate:
			out = append(out, FieldDifference{Path: p, Live: clip(lv), Candidate: "(removed)"})
		case !inLive && inCandidate:
			out = append(out, FieldDifference{Path: p, Live: "(absent)", Candidate: clip(cv)})
		case lv != cv:
			out = append(out, FieldDifference{Path: p, Live: clip(lv), Candidate: clip(cv)})
		default:
			continue
		}

		if len(out) >= maxDifferenceFields {
			out = append(out, FieldDifference{
				Path: "(truncated)",
				Live: fmt.Sprintf("more than %d fields differ", maxDifferenceFields),
			})

			break
		}
	}

	return out
}

// collectXML records every addressable value in a tree against its path.
//
// A thin entry point over collectXMLWithOccurrence so there is one implementation of the walk. The first version had two, and
// the second grew a half-written loop that deleted keys it had just added - the occurrence has to go on the child's own name
// segment, and applying it from the parent's side produces paths like /patient(1)/id instead of /patient/id(1). Those address
// different things, so it was not a cosmetic error.
func collectXML(node *xtree.Node, parentPath string, out map[string]string) {
	collectXMLWithOccurrence(node, parentPath, 0, out)
}

// collectXMLWithOccurrence recurses with the occurrence the parent worked out.
//
// A wrapper rather than a parameter on collectXML, so the exported walk keeps a two-argument shape and nothing else has to know
// occurrences exist. xtree.Node carries no occurrence of its own, which is why it is passed down rather than read.
func collectXMLWithOccurrence(node *xtree.Node, parentPath string, occurrence int, out map[string]string) {
	if node == nil {
		return
	}

	name := node.Name
	if occurrence > 0 {
		name = fmt.Sprintf("%s(%d)", node.Name, occurrence)
	}

	path := parentPath + "/" + name

	// Every element is recorded against its own path, carrying its text or empty.
	//
	// Recorded even when it has no text, and that is what makes a removal legible. Without it, an element removed
	// entirely and an element emptied both showed up only as their attribute disappearing - so the difference between
	// "this patient has no recorded birth date" and "this element is no longer sent" was two entries somebody had to
	// compare, rather than one that said so. Those are different clinical statements and the diff must not blur them.
	//
	// It adds no noise: a path with the same value on both sides produces no difference.
	out[path] = strings.TrimSpace(node.Text)

	// Attributes, which is where a v3 value nearly always lives. A diff comparing only element text would report two
	// documents as identical when the birth date, the gender and every identifier had changed.
	for _, a := range node.Attrs {
		out[path+"@"+a.Name] = a.Value
	}

	counts := map[string]int{}
	for _, c := range node.Children {
		counts[c.Name]++
	}

	seen := map[string]int{}
	for _, c := range node.Children {
		next := 0
		if counts[c.Name] > 1 {
			seen[c.Name]++
			next = seen[c.Name]
		}
		collectXMLWithOccurrence(c, path, next, out)
	}
}
