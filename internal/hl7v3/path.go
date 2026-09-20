package hl7v3

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Path addresses a place in an HL7 v3 message.
//
// The notation:
//
//	patient/id@extension                 an attribute of a nested element
//	//patientPerson/name/given           the first given name, found anywhere
//	//name(2)/family                     the family name of the second name
//	//birthTime@value                     a date, which in v3 lives in an attribute
//	//administrativeGenderCode@code       a coded value
//	//birthTime@nullFlavor                why a value is absent
//
// # Why attributes are first class rather than an afterthought
//
// This is the decision that matters. In HL7 v2 a value is text at a position. In v3 a value is almost always an attribute:
// <birthTime value="19551014"/> carries the date in value, and <id root="2.16.840.1.113883.4.1" extension="123456"/> carries the
// identifier in extension. A path language for v3 that could only reach element text would return empty for nearly every field
// anybody actually wants, and would do it without complaining.
//
// So @ is part of the notation from the start, and a path with no @ means the element's own text - which in v3 is the unusual
// case rather than the normal one.
//
// # Why occurrence is written (n) rather than [n]
//
// Because the v2 path language in this product already writes repetition that way, and PID-3(2) and name(2) meaning the same
// thing in two formats is worth more than matching XPath's brackets. Somebody who has learned one has learned both.
//
// # Why // exists
//
// Real v3 messages nest the same content at different depths depending on the interaction. A patient sits somewhere different in
// a record-added message than in a query response, and this is not a quirk of one vendor - the interactions are genuinely
// different documents that happen to share payload types.
//
// A path anchored to the root would therefore have to be rewritten for each interaction, and somebody configuring a channel
// would discover that only when a second message shape arrived. // says "find this anywhere below here", which is what the
// hl7v3 package's own extraction already does internally for the same reason.
type Path struct {
	// Steps are the element names to walk, in order.
	Steps []PathStep

	// Attribute is the attribute to read from the final step. Empty means the element's text.
	Attribute string

	// Descend is true for a path starting //, meaning the first step is searched for at any depth.
	Descend bool

	raw string
}

// PathStep is one element name, optionally with an occurrence.
type PathStep struct {
	// Name is the local element name, without any namespace prefix.
	Name string

	// Occurrence is 1-based. Zero means "the first one" when reading a single value and "all of them" when listing.
	//
	// Zero rather than a separate flag, matching the v2 path type, so the two behave the same where they can.
	Occurrence int
}

// String returns the path as written.
func (p Path) String() string { return p.raw }

// ParsePath reads the notation above.
//
// Strict about what it accepts. A path is written by a person into a channel that will run unattended, and a misparsed path does
// not fail - it silently addresses the wrong thing, or nothing, and the channel filters every message or none.
func ParsePath(s string) (Path, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Path{}, fmt.Errorf("the path is empty")
	}

	p := Path{raw: raw}
	body := raw

	switch {
	case strings.HasPrefix(body, "//"):
		p.Descend = true
		body = body[2:]
	case strings.HasPrefix(body, "/"):
		body = body[1:]
	}

	// The attribute is split off first, because an @ inside a step name is not something this notation allows and
	// finding it early gives a better message than a confusing step error.
	if at := strings.Index(body, "@"); at >= 0 {
		p.Attribute = body[at+1:]
		body = body[:at]

		if p.Attribute == "" {
			return Path{}, fmt.Errorf("there is an @ with no attribute name after it")
		}
		// Whitespace is included in what is refused. An XML attribute name cannot contain a space, so
		// "@ex tension" is a typo - and accepting it would mean looking for an attribute that cannot exist,
		// which reads as a message with a missing field rather than a path with a mistake in it.
		if strings.ContainsAny(p.Attribute, "/@() \t\r\n") {
			return Path{}, fmt.Errorf("%q is not a valid attribute name", p.Attribute)
		}
	}

	body = strings.Trim(body, "/")
	if body == "" {
		// An attribute with no element would be an attribute of the document, which does not exist. Refused rather
		// than silently read from the root, because "@extension" looks like it means something and does not.
		return Path{}, fmt.Errorf("the path names no element")
	}

	for _, part := range strings.Split(body, "/") {
		step, err := parseStep(part)
		if err != nil {
			return Path{}, err
		}
		p.Steps = append(p.Steps, step)
	}

	return p, nil
}

// parseStep reads one element name with an optional occurrence.
func parseStep(part string) (PathStep, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		// Two slashes in the middle of a path. Refused rather than treated as a descendant search, because // means
		// that only at the start and accepting it anywhere would make a/b//c ambiguous with a typo.
		return PathStep{}, fmt.Errorf("there is an empty step in the path")
	}

	open := strings.Index(part, "(")
	if open < 0 {
		if err := checkName(part); err != nil {
			return PathStep{}, err
		}

		return PathStep{Name: part}, nil
	}

	if !strings.HasSuffix(part, ")") {
		return PathStep{}, fmt.Errorf("%q is missing its closing bracket", part)
	}

	name := part[:open]
	if err := checkName(name); err != nil {
		return PathStep{}, err
	}

	inner := part[open+1 : len(part)-1]
	n, err := strconv.Atoi(strings.TrimSpace(inner))
	if err != nil {
		return PathStep{}, fmt.Errorf("%q is not a number, so %s cannot be an occurrence", inner, part)
	}
	if n < 1 {
		// One-based, like the v2 notation and like every specification a person reading this will have open. Zero is
		// refused rather than accepted as "the first", because somebody writing (0) meant something and it is not
		// clear what.
		return PathStep{}, fmt.Errorf("occurrences count from 1, so %s is not valid", part)
	}

	return PathStep{Name: name, Occurrence: n}, nil
}

// checkName refuses an element name that could not appear in a document.
func checkName(name string) error {
	if name == "" {
		return fmt.Errorf("there is an empty element name in the path")
	}
	if strings.ContainsAny(name, " \t@()/") {
		return fmt.Errorf("%q is not a valid element name", name)
	}
	// A prefix is refused rather than stripped. Silently ignoring it would mean hl7:name and name behave identically,
	// and somebody who wrote a prefix believing it selected a namespace would never learn otherwise - while a path
	// written against one sender's prefixes would appear to work until a sender using different ones arrived.
	if strings.Contains(name, ":") {
		return fmt.Errorf("write %q without a namespace prefix - prefixes are the sender's choice and vary "+
			"between systems, so this notation ignores them and matches on the local name",
			name)
	}

	return nil
}

// Resolve finds the nodes a path addresses.
//
// Returns every match rather than the first, because a path with no occurrence on its last step legitimately names several things
// - a patient with two given names, an interaction with three identifiers - and the caller decides whether that is one value or a
// list.
func (p Path) Resolve(root *xtree.Node) []*xtree.Node {
	if root == nil || len(p.Steps) == 0 {
		return nil
	}

	// Where the walk begins. For a descendant path the first step is searched for at any depth, which is what makes one
	// path work across interactions that nest the same content differently.
	var current []*xtree.Node
	if p.Descend {
		current = findDescendants(root, p.Steps[0].Name)
		current = pickOccurrence(current, p.Steps[0].Occurrence)
	} else {
		// A path anchored at the root may name the root itself, since that is how somebody would write it after
		// looking at the document.
		if localName(root.Name) == p.Steps[0].Name {
			current = []*xtree.Node{root}
		} else {
			current = pickOccurrence(childrenNamed(root, p.Steps[0].Name), p.Steps[0].Occurrence)
		}
	}

	for _, step := range p.Steps[1:] {
		var next []*xtree.Node
		for _, node := range current {
			next = append(next, pickOccurrence(childrenNamed(node, step.Name), step.Occurrence)...)
		}
		current = next
	}

	return current
}

// Values returns the strings a path addresses.
//
// A node whose value is absent contributes nothing rather than an empty string. That distinction is the whole reason the hl7v3
// package exists: an absent birth date and a birth date the patient declined to give are different clinical facts, and flattening
// both to "" is what makes a downstream system chase a value nobody has.
func (p Path) Values(root *xtree.Node) []string {
	nodes := p.Resolve(root)

	var out []string
	for _, node := range nodes {
		if p.Attribute != "" {
			if v, ok := node.Attr(p.Attribute); ok {
				out = append(out, v)
			}

			continue
		}
		// Trimmed, because XML indentation is whitespace inside an element and a value of "\n      " is not a value
		// somebody meant to send.
		if text := strings.TrimSpace(node.Text); text != "" {
			out = append(out, text)
		}
	}

	return out
}

// Value returns the first value a path addresses, and whether there was one.
//
// Two returns rather than an empty string, so a caller can tell "not present" from "present and empty". A filter comparing an
// absent field to "" should not match.
func (p Path) Value(root *xtree.Node) (string, bool) {
	values := p.Values(root)
	if len(values) == 0 {
		return "", false
	}

	return values[0], true
}

// NullFlavor returns the null flavour of what a path addresses, if it has one.
//
// Its own method because this is the question a v3 channel asks constantly and asking it through a second path with @nullFlavor
// appended is both easy to forget and easy to get wrong - the flavour belongs to the element, not to whichever attribute the
// original path happened to read.
func (p Path) NullFlavor(root *xtree.Node) (string, bool) {
	for _, node := range p.Resolve(root) {
		if v, ok := node.Attr("nullFlavor"); ok && v != "" {
			return v, true
		}
	}

	return "", false
}

// Exists reports whether a path addresses anything at all.
//
// Distinct from having a value. An element present with nullFlavor="ASKU" exists and has no value, and a filter that treats those
// the same cannot express "the sender said they asked and the patient did not know".
func (p Path) Exists(root *xtree.Node) bool {
	return len(p.Resolve(root)) > 0
}

// childrenNamed returns the direct children with a local name, ignoring namespace prefixes.
func childrenNamed(n *xtree.Node, name string) []*xtree.Node {
	if n == nil {
		return nil
	}

	var out []*xtree.Node
	for _, child := range n.Children {
		if localName(child.Name) == name {
			out = append(out, child)
		}
	}

	return out
}

// findDescendants returns every node at any depth with a local name, in document order.
//
// Breadth-first, so //name finds the shallowest occurrences first. That matters because the shallowest is nearly always the one
// somebody meant: a query response contains the patient's name and also the requesting provider's, and the patient is the subject
// of the message.
func findDescendants(root *xtree.Node, name string) []*xtree.Node {
	if root == nil {
		return nil
	}

	var out []*xtree.Node
	queue := []*xtree.Node{root}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		if node != root && localName(node.Name) == name {
			out = append(out, node)
		}
		queue = append(queue, node.Children...)
	}

	return out
}

// pickOccurrence narrows a list to one entry, or leaves it alone.
func pickOccurrence(nodes []*xtree.Node, occurrence int) []*xtree.Node {
	if occurrence == 0 {
		return nodes
	}
	if occurrence > len(nodes) {
		// Out of range is nothing, not the last one. Returning the last would mean a path asking for the third
		// identifier quietly reads the second, and a channel routing on it would send the message to the wrong place.
		return nil
	}

	return nodes[occurrence-1 : occurrence]
}
