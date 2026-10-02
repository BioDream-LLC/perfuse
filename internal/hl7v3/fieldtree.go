package hl7v3

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// FieldNode is one addressable place in a message, as an interface should show it.
//
// # Why this is derived from a real message rather than from a schema
//
// The v3 schemas are enormous and describe everything an interaction could contain. A picker built from one would show a person
// several thousand fields, nearly all absent from the message in front of them, and they would have to know which parts their
// sender actually populates - which is the knowledge they came here lacking.
//
// So this walks a message the operator supplies. What they see is what their own sender really sends, in the order it arrives,
// with the values that arrived in it. The trade is that a field their sender omits from this particular message cannot be clicked,
// which is honest: they would have been guessing about it anyway.
type FieldNode struct {
	// Name is the element's local name.
	Name string `json:"name"`

	// Path is the notation that addresses this node, ready to paste into a filter.
	//
	// Generated rather than typed, because getting occurrence numbering right by hand is exactly the fiddly part - and
	// the commonest mistake, counting from zero, produces a path that reads correctly and selects the wrong thing.
	Path string `json:"path"`

	// Value is the text of this element, when it has any.
	Value string `json:"value,omitempty"`

	// Attributes are the attributes on this element, each with its own path.
	Attributes []FieldAttribute `json:"attributes,omitempty"`

	// NullFlavor is the reason a value is absent, when the sender gave one.
	//
	// Surfaced at the top level rather than left among the attributes, because in v3 it is not one attribute among
	// several - it is the difference between "nobody recorded this" and "the patient declined to say", which are
	// different clinical facts and the reason this package exists.
	NullFlavor string `json:"nullFlavor,omitempty"`

	// Occurrence is which of its similarly named siblings this is, 1-based. Zero when it is the only one.
	//
	// Sent so an interface can show "name 2 of 2" rather than two identical rows, which is what makes a repeated element
	// comprehensible at a glance.
	Occurrence int `json:"occurrence,omitempty"`

	// SiblingCount is how many similarly named siblings there are in total.
	SiblingCount int `json:"siblingCount,omitempty"`

	// Children are the nested elements.
	Children []FieldNode `json:"children,omitempty"`

	// Interesting marks a node worth showing before the rest.
	//
	// A v3 message is mostly envelope, and somebody opening a picker is nearly always after a demographic or an
	// identifier. Marking rather than filtering, because the envelope is occasionally exactly what somebody needs -
	// routing on interactionId is a real thing to want.
	Interesting bool `json:"interesting,omitempty"`
}

// FieldAttribute is one attribute, with the path that reads it.
type FieldAttribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Path  string `json:"path"`
}

// interestingElements are the elements a person opening a picker is usually looking for.
//
// A deliberate list rather than a heuristic. A heuristic that is wrong about a clinical field is worse than a list that is
// incomplete, because an incomplete list still shows everything - it just does not promote it.
var interestingElements = map[string]bool{
	"id":                       true,
	"birthTime":                true,
	"administrativeGenderCode": true,
	"given":                    true,
	"family":                   true,
	"prefix":                   true,
	"suffix":                   true,
	"telecom":                  true,
	"streetAddressLine":        true,
	"city":                     true,
	"state":                    true,
	"postalCode":               true,
	"country":                  true,
	"deceasedInd":              true,
	"deceasedTime":             true,
	"multipleBirthInd":         true,
	"maritalStatusCode":        true,
	"religiousAffiliationCode": true,
	"raceCode":                 true,
	"ethnicGroupCode":          true,
	"interactionId":            true,
	"statusCode":               true,
	"providerOrganization":     true,
	"name":                     true,
	"addr":                     true,
	"patientPerson":            true,
	"patient":                  true,
}

// FieldTreeOptions controls how a tree is built.
type FieldTreeOptions struct {
	// MaxDepth stops the walk. Zero means a sensible default.
	//
	// Bounded because this output goes to a browser and a v3 message can nest deeply. An unbounded walk of a large
	// message produces a response big enough to make the picker slower than reading the XML by hand.
	MaxDepth int

	// MaxNodes stops the walk by count.
	MaxNodes int
}

// BuildFieldTree walks a message and returns every addressable place in it.
//
// Paths are generated as descendant paths from the shallowest unambiguous point where that is safe, and as fully anchored paths
// otherwise. See fieldPath for why that distinction is worth the trouble.
func BuildFieldTree(root *xtree.Node, opts FieldTreeOptions) (FieldNode, error) {
	if root == nil {
		return FieldNode{}, fmt.Errorf("there is no message to look at")
	}

	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 24
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 4000
	}

	// Counted across the whole walk rather than per level, since a message that is wide is as expensive to render as one
	// that is deep.
	budget := opts.MaxNodes

	// Names that appear exactly once in the entire document can be addressed with // safely, which produces the short
	// readable paths somebody would have written themselves. Anything appearing more than once has to be anchored or
	// numbered, or the path would silently match the wrong one.
	counts := map[string]int{}
	countNames(root, counts)

	node := buildNode(root, nil, counts, 0, opts.MaxDepth, &budget)

	return node, nil
}

// countNames tallies how often each local element name appears in the document.
func countNames(n *xtree.Node, into map[string]int) {
	if n == nil {
		return
	}

	into[localName(n.Name)]++
	for _, child := range n.Children {
		countNames(child, into)
	}
}

// buildNode converts one node and its children.
//
// ancestry is the chain of steps from the root to this node's parent, used to build an anchored path when a descendant path would
// be ambiguous.
func buildNode(
	n *xtree.Node,
	ancestry []string,
	counts map[string]int,
	depth, maxDepth int,
	budget *int,
) FieldNode {
	name := localName(n.Name)

	// Occurrence among siblings of the same name, which is what a path needs and what an interface should display.
	occurrence, siblings := siblingPosition(n)

	step := name
	if siblings > 1 {
		step = fmt.Sprintf("%s(%d)", name, occurrence)
	}

	here := append(append([]string{}, ancestry...), step)

	out := FieldNode{
		Name:         name,
		Path:         fieldPath(here, name, counts, siblings),
		Value:        strings.TrimSpace(n.Text),
		Occurrence:   occurrenceForDisplay(occurrence, siblings),
		SiblingCount: siblingCountForDisplay(siblings),
		Interesting:  interestingElements[name],
	}

	// nullFlavor is lifted out of the attribute list. Leaving it in among root and codeSystem would bury the one
	// attribute whose presence changes what the absence of a value means.
	for _, attr := range n.Attrs {
		if attr.Name == "nullFlavor" {
			out.NullFlavor = attr.Value

			continue
		}
		out.Attributes = append(out.Attributes, FieldAttribute{
			Name:  attr.Name,
			Value: attr.Value,
			Path:  out.Path + "@" + attr.Name,
		})
	}

	// Sorted by name so the same message always produces the same order. Go's decoder preserves document order for
	// attributes, but an interface showing them in a different order between two loads of the same message reads as
	// though something changed.
	sort.SliceStable(out.Attributes, func(i, j int) bool {
		return out.Attributes[i].Name < out.Attributes[j].Name
	})

	if depth >= maxDepth {
		return out
	}

	for _, child := range n.Children {
		if *budget <= 0 {
			break
		}
		*budget--
		out.Children = append(out.Children, buildNode(child, here, counts, depth+1, maxDepth, budget))
	}

	return out
}

// fieldPath chooses between a short descendant path and a fully anchored one.
//
// # Why not always anchor
//
// An anchored path is always correct and nearly always unreadable: the route to a birth date in a PDQ response is seven steps of
// envelope before anything clinical. Worse, it is brittle in the specific way v3 punishes - the same field sits at a different
// depth in a different interaction, so an anchored path copied from a query response stops working on a record-added message, and
// stops working silently.
//
// # Why not always use //
//
// Because // takes the first match anywhere, and plenty of names appear more than once. //id in a PDQ response matches the message
// identifier, the patient's identifiers and the query identifier, and the first of those is not what anybody wanted.
//
// # So
//
// Short form when a descendant search cannot reach the wrong element, anchored otherwise. Two cases qualify:
//
//   - The name appears exactly once in the document. //family.
//   - Every occurrence of the name in the document is a sibling of this one. //given(2).
//
// The second case is worth the extra thought because it is common and it saves a great deal of noise. Two given names are the only
// two given elements in the document, so a breadth-first descendant search finds exactly those two in document order and (2) picks
// the right one - which turns a hundred characters of envelope into //given(2).
//
// It does not apply to the patient's identifiers in a PDQ response, and the difference is instructive: id appears three times, of
// which two are siblings, because the message itself has one. //id(1) would be the message identifier rather than the patient's, so
// those stay anchored.
//
// The consequence worth knowing: the same field can produce a short path from one sample and a long one from another, if the second
// sample happens to contain another element of the same name elsewhere. That is not ideal, and it is better than either
// alternative - a path nobody can read, or one that quietly matches the wrong element.
func fieldPath(ancestry []string, name string, counts map[string]int, siblings int) string {
	switch {
	case counts[name] == 1 && siblings <= 1:
		return "//" + name

	case siblings > 1 && counts[name] == siblings:
		// Every element with this name is one of these siblings, so a descendant search reaches exactly this set
		// in document order and the occurrence selects within it.
		return fmt.Sprintf("//%s(%d)", name, occurrenceFromStep(ancestry))
	}

	return strings.Join(ancestry, "/")
}

// occurrenceFromStep reads the occurrence number back out of the last step of an ancestry.
//
// The step was formatted as name(n) a moment earlier, so this parses what this file just wrote. Slightly awkward, and the
// alternative was threading the number through two more parameters to reach one branch - which would put it in every caller's
// signature to serve one case.
func occurrenceFromStep(ancestry []string) int {
	if len(ancestry) == 0 {
		return 1
	}

	step := ancestry[len(ancestry)-1]
	open := strings.IndexByte(step, '(')
	if open < 0 || !strings.HasSuffix(step, ")") {
		return 1
	}

	n, err := strconv.Atoi(step[open+1 : len(step)-1])
	if err != nil || n < 1 {
		return 1
	}

	return n
}

// siblingPosition returns which of its same-named siblings a node is, and how many there are.
func siblingPosition(n *xtree.Node) (position, total int) {
	parent := n.Parent()
	if parent == nil {
		return 1, 1
	}

	name := localName(n.Name)
	for _, child := range parent.Children {
		if localName(child.Name) != name {
			continue
		}
		total++
		if child == n {
			position = total
		}
	}

	if position == 0 {
		// Not found among its parent's children, which should not happen for a parsed tree. Reported as the only one
		// rather than as position zero, because a zero would render as "0 of 1" and send somebody looking for a bug
		// in their message.
		return 1, 1
	}

	return position, total
}

// occurrenceForDisplay reports an occurrence only when there is something to distinguish.
func occurrenceForDisplay(occurrence, siblings int) int {
	if siblings <= 1 {
		return 0
	}

	return occurrence
}

// siblingCountForDisplay reports a sibling count only when there is more than one.
func siblingCountForDisplay(siblings int) int {
	if siblings <= 1 {
		return 0
	}

	return siblings
}

// FlattenFieldTree returns every node with a value or an attribute, as a flat list.
//
// For a search box, and for the case an interface has to handle first: somebody who knows the word "birth" and not where it lives.
// A tree is the right way to understand a message and the wrong way to find one field in it.
func FlattenFieldTree(root FieldNode) []FieldNode {
	// Empty rather than nil, so this serialises as [] and not null. The append below is conditional -
	// pure-structure nodes are skipped - so a document of nothing but empty nested elements, which is
	// what a skeleton or template message looks like, produced nil. The interface declares this a list
	// and filters it, so that pasted template would have crashed the field picker.
	out := []FieldNode{}

	var walk func(FieldNode)
	walk = func(n FieldNode) {
		// A node with neither a value nor an attribute nor a null flavour is pure structure - controlActProcess,
		// subject1 - and there is nothing to read from it. Included in the tree, because it is how somebody
		// understands the shape; excluded here, because a search result you cannot use is noise.
		if n.Value != "" || len(n.Attributes) > 0 || n.NullFlavor != "" {
			flat := n
			flat.Children = nil
			out = append(out, flat)
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)

	return out
}
