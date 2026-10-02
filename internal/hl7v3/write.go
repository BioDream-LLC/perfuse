package hl7v3

import (
	"fmt"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Writing into an HL7 v3 document is harder than reading from one, and the difference is not incidental.
//
// A read asks a question and gets an answer or nothing. A write has to decide what to create when the thing it names does
// not exist, and in v3 that is a real question rather than a detail: setting a patient's family name on a message that
// carries no name element means creating three nested elements, and the value nearly always belongs in an attribute
// rather than in element text.
//
// So this file is deliberately small and explicit about what it will and will not do.

// nullFlavorAttr is the attribute carrying a stated reason a value is absent.
//
// Named once rather than spelled in each place it is used, because it is written in four and a typo in one of them would
// produce an element that looks null-flavoured to a reader and is not.
const nullFlavorAttr = "nullFlavor"

// ErrCannotCreate is returned when a path could match anywhere and therefore cannot be created.
//
// A path beginning // means "search at any depth", which is exactly right for reading and meaningless for creating: there
// is no answer to where a missing element should go. Rather than guess a location - which would put clinical data
// somewhere plausible and wrong - a descending path may update what it finds and may not bring anything into existence.
var ErrCannotCreate = fmt.Errorf("this path searches at any depth, so there is nowhere definite to create the value")

// Resolve finds the single node a path addresses for writing, creating elements along the way when the path is anchored.
//
// Returns the node whose text or attribute should be changed. The caller decides what to do with it, because the three
// ways to remove a value in v3 are genuinely different statements and no single function should choose between them.
//
// Occurrence zero means the first, matching the read side. An explicit occurrence creates the earlier repetitions when
// they are missing, because assigning to the third identifier when one exists means the third: compacting would put a
// national identifier where a medical record number belongs, and nobody would notice until a human read it.
func (p Path) ResolveForWrite(root *xtree.Node) (*xtree.Node, error) {
	if root == nil {
		return nil, fmt.Errorf("there is no document to write to")
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("the path %q names no element", p.raw)
	}

	// A descending path may only address what already exists. Finding it is the read path's job, so ask that and
	// refuse rather than inventing a location.
	if p.Descend {
		nodes := p.Resolve(root)
		switch len(nodes) {
		case 0:
			return nil, fmt.Errorf("%q matched nothing in this message: %w", p.raw, ErrCannotCreate)
		case 1:
			return nodes[0], nil
		default:
			// More than one match is refused rather than resolved to the first. A path that reads as
			// "the family name" and writes to whichever one happened to be found first is how a
			// transformation corrupts a message it appeared to work on.
			return nil, fmt.Errorf("%q matches %d elements in this message, so it does not say which to "+
				"change - anchor the path or add an occurrence like (1)", p.raw, len(nodes))
		}
	}

	// An anchored path walks from the root, creating as it goes.
	//
	// The first step is checked against the root's own name rather than searched for among its children, because an
	// anchored path is written from the document element down.
	node := root
	steps := p.Steps
	if steps[0].Name == root.Name {
		steps = steps[1:]
	}

	for _, step := range steps {
		index := 0
		if step.Occurrence > 0 {
			index = step.Occurrence - 1
		}
		node = ensureSibling(node, step.Name, index)
	}

	return node, nil
}

// knownOrder is the child sequence for the elements a transformation realistically creates.
//
// Element order is significant in a schema-valid v3 document, so a created element in the wrong position produces a document
// that parses here and is rejected at the receiver - which is the worst place for it to fail, because the person who can see
// the error is not the person who made the change.
//
// This was an appended-at-the-end limitation, stated honestly and still a limitation. A full answer needs the schema; this is
// the part of the schema that matters, written out for the interactions this package targets. The lists are short because the
// set of elements somebody adds by transformation is short: a name part, an identifier, a birth date, a gender, an address, a
// telephone number, a death date.
//
// Deliberately not generated from the schema files. Generating it would mean shipping and parsing them, and the parts that
// would then be covered are the parts nobody creates by hand - so the cost is real and the extra correctness is theoretical.
var knownOrder = map[string][]string{
	// The order within a person, from the RIM. deceasedInd before deceasedTime is the one people get wrong, and it
	// matters because a receiver reading them in document order can see the flag before the date.
	"patientPerson": {
		"id", "name", "telecom", "administrativeGenderCode", "birthTime",
		"deceasedInd", "deceasedTime", "multipleBirthInd", "multipleBirthOrderNumber",
		"addr", "maritalStatusCode", "religiousAffiliationCode", "raceCode", "ethnicGroupCode",
		"asOtherIDs", "birthPlace", "languageCommunication",
	},

	// A patient wraps the person. statusCode before patientPerson is required and is easy to get backwards, because
	// reading the document the person looks like the more important thing.
	"patient": {
		"id", "statusCode", "effectiveTime", "confidentialityCode", "veryImportantPersonCode",
		"patientPerson", "providerOrganization", "subjectOf1",
	},

	// A name. Prefixes before given names before family before suffixes, which is the sequence in the datatype and
	// not alphabetical - a point worth stating because sorting them would look tidy and be wrong.
	"name": {"delimiter", "prefix", "given", "family", "suffix", "validTime"},

	// An address.
	"addr": {
		"delimiter", "country", "state", "county", "city", "postalCode",
		"streetAddressLine", "houseNumber", "direction", "streetName", "streetNameBase",
		"streetNameType", "additionalLocator", "unitID", "unitType", "careOf",
		"censusTract", "deliveryAddressLine", "useablePeriod",
	},
}

// insertPosition returns where a new element of this name belongs among a parent's children.
//
// Returns the index to insert at, and whether the position is known. Not known means append, which is what happened before
// and remains the honest fallback for an element this table has not been taught.
func insertPosition(parent *xtree.Node, name string) (int, bool) {
	order, ok := knownOrder[parent.Name]
	if !ok {
		return 0, false
	}

	rank := -1
	for i, n := range order {
		if n == name {
			rank = i

			break
		}
	}
	if rank < 0 {
		// The parent is known and this child is not. Appending, rather than guessing a position among elements
		// whose order is known - putting an unknown element in the middle of a known sequence is worse than putting
		// it at the end, because it breaks a sequence that was correct.
		return 0, false
	}

	// The first child whose rank is higher; the new element goes immediately before it.
	//
	// A child whose name is not in the table gets rank -1 and therefore never satisfies the comparison, since rank is
	// non-negative by the check above. That is why there is no separate guard for it: the first version had one, with
	// a comment about extension elements pushing everything to the end, and a plant proved the two branches
	// behaviourally identical. A guard that cannot change an outcome reads as protection and provides none.
	for i, c := range parent.Children {
		childRank := -1
		for j, n := range order {
			if n == c.Name {
				childRank = j

				break
			}
		}
		if childRank > rank {
			return i, true
		}
	}

	return len(parent.Children), true
}

// ensureSibling returns the index'th child by name, creating it in the right place.
//
// Three cases, in order of confidence. A repeat goes immediately after the last element of the same name, which is where the
// schema expects it. A new element whose position is known from knownOrder goes there. Anything else is appended, which is a
// stated limitation rather than a solved problem.
func ensureSibling(parent *xtree.Node, name string, index int) *xtree.Node {
	if existing := parent.Count(name); existing > index {
		return parent.Child(name, index)
	}

	// Find where the last element of this name sits, so repeats stay together.
	after := -1
	for i, c := range parent.Children {
		if c.Name == name {
			after = i
		}
	}

	for parent.Count(name) <= index {
		child := xtree.New(name)

		switch {
		case after >= 0:
			// A repeat. Immediately after the last of its namesakes, so repeats stay together.
			after++
			parent.Insert(after, child)

		default:
			if at, ok := insertPosition(parent, name); ok {
				parent.Insert(at, child)
				// Subsequent repetitions follow this one rather than being placed independently.
				after = at

				continue
			}
			parent.Append(child)
			after = len(parent.Children) - 1
		}
	}

	return parent.Child(name, index)
}

// SetValue writes a value where this path points, creating what it must.
//
// Where the value goes is the decision this makes, and it is the one a v2-shaped implementation gets wrong. An explicit
// @attribute is honoured literally. Otherwise the value goes to the value attribute rather than to element text, because
// in v3 that is where a value almost always lives - a birth date is birthTime@value, a gender is
// administrativeGenderCode@code, and an implementation that wrote element text would produce a document that looks
// populated and reads as empty everywhere.
//
// Writing a value also removes any null flavour on that element. The two are mutually exclusive by definition: an element
// cannot both carry a value and record that no value is available, and leaving the flavour behind produces a document
// that contradicts itself.
func (p Path) SetValue(root *xtree.Node, value string) error {
	node, err := p.ResolveForWrite(root)
	if err != nil {
		return err
	}

	switch {
	case p.Attribute != "":
		node.SetAttr(p.Attribute, value)

	case len(node.Children) > 0:
		// An element with children is a structure, not a value. Writing into it would discard the children,
		// which is a much larger change than the author asked for.
		return fmt.Errorf("%q addresses an element with %d child element(s), so it holds a structure rather "+
			"than a value - name the attribute or a leaf element", p.raw, len(node.Children))

	// An element that already carries its value in an attribute keeps carrying it there. This is the case that
	// matters most, and getting it wrong is silent: writing text onto <birthTime value="19551014"/> produces an
	// element holding both a value and some text, which parses, validates in most tooling, and means two
	// different things to two different readers.
	case hasAttrValue(node, "value"):
		node.SetAttr("value", value)
	case hasAttrValue(node, "code"):
		node.SetAttr("code", value)

	// A new coded element by name, since v3 names coded elements consistently and a code does not belong in text.
	case isCodedName(node.Name):
		node.SetAttr("code", value)

	// An element carrying a null flavour and nothing else has had its value deliberately removed, which also
	// removed the only evidence of where that value lived. Refused rather than guessed.
	//
	// This is the one genuinely ambiguous case and it needs the author, not a heuristic. Guessing text produces
	// <birthTime>19600101</birthTime>, which is not valid v3 and which the receiver rejects; guessing an attribute
	// produces <family value="Dubois"/>, which every reader treats as having no name. Both are silent here and loud
	// at somebody else's site.
	//
	// It is also the case where a heuristic would make the same step behave differently depending on what ran
	// before it, which is worse than either wrong answer.
	case hasAttrValue(node, nullFlavorAttr):
		return fmt.Errorf("%q addresses an element that carries a null flavour and no value, so there is no "+
			"way to tell whether its value belongs in the element text or in an attribute - write the "+
			"attribute explicitly, as in %s@value", p.raw, p.raw)

	// Otherwise element text, which is what a bare path means on the read side.
	//
	// The symmetry is the point and it was wrong in the first version of this file: a bare path read element text
	// and wrote a value attribute, so a step that set a family name wrote somewhere the picker would never look
	// and the field appeared unchanged. Reading and writing have to mean the same thing by the same notation.
	//
	// For a brand-new element whose name gives no clue, this is a guess in favour of text. An author who wants an
	// attribute writes one, and every path the field picker generates already says which it is.
	default:
		node.SetText(value)
	}

	if p.Attribute != nullFlavorAttr {
		node.RemoveAttr(nullFlavorAttr)
	}

	return nil
}

// hasAttrValue reports whether an attribute is present and not empty.
//
// Present-and-empty counts as absent here on purpose. An element carrying value="" is not telling us where its value
// lives, so it should not steer a write away from the element text.
func hasAttrValue(n *xtree.Node, name string) bool {
	v, ok := n.Attr(name)

	return ok && v != ""
}

// ReadValue returns the value this path addresses, for a transformation.
//
// Deliberately more forgiving than Path.Values, and deliberately not the same function. Path.Values means exactly element
// text for a bare path, because the field picker lists one row per readable thing and needs text and attributes to stay
// distinguishable. A transformation is asking a different question - "what is this element's value" - and the answer for
// an element carrying value or code is that attribute.
//
// This is the same rule the filter uses, which matters: a step conditioned on a field and a step changing that field must
// agree about what the field's value is, or a channel skips exactly the messages it was written to change.
func (p Path) ReadValue(root *xtree.Node) (string, bool) {
	nodes := p.Resolve(root)
	if len(nodes) == 0 {
		return "", false
	}

	if p.Attribute != "" {
		v, ok := nodes[0].Attr(p.Attribute)

		return v, ok
	}

	return elementValue(nodes[0])
}

// ClearValue empties the element this path addresses without removing it or saying why it is empty.
//
// One of three different statements, and the weakest: the element is present and carries nothing. Use SetNullFlavor to
// say why nothing is there, and RemoveElement to say the element does not apply at all. Collapsing the three would
// discard information a receiving system acts on - "asked and not known" sends a human to ask again, and an absent
// element does not.
func (p Path) ClearValue(root *xtree.Node) error {
	node, err := p.ResolveForWrite(root)
	if err != nil {
		return err
	}

	if p.Attribute != "" {
		node.RemoveAttr(p.Attribute)
		return nil
	}

	node.SetText("")
	node.RemoveAttr("value")
	node.RemoveAttr("code")

	return nil
}

// SetNullFlavor records that no value is available, and why.
//
// This is the construct v2 has no equivalent for and the reason a v3 transformation cannot be the v2 one with a different
// path parser. ASKU means somebody asked and the patient did not know; NAV means try later; MSK means it is known and
// withheld; an absent element means nobody recorded anything at all. A receiving system does different things with each,
// and a demographic match that should stay unresolved gets resolved by a human guessing when they are flattened.
//
// Setting a flavour removes any value, for the same reason writing a value removes the flavour.
func (p Path) SetNullFlavor(root *xtree.Node, flavor string) error {
	if !KnownNullFlavor(flavor) {
		return fmt.Errorf("%q is not a null flavour this understands; the ones that carry meaning are %s",
			flavor, knownNullFlavorList())
	}

	node, err := p.ResolveForWrite(root)
	if err != nil {
		return err
	}

	node.SetText("")
	node.RemoveAttr("value")
	node.RemoveAttr("code")
	node.SetAttr(nullFlavorAttr, flavor)

	return nil
}

// RemoveElement deletes the element this path addresses.
//
// The strongest of the three removals: not "no value available" but "this does not apply". Refuses to remove the document
// element, because a transformation that empties the message is not a transformation anybody wrote on purpose.
func (p Path) RemoveElement(root *xtree.Node) error {
	node, err := p.ResolveForWrite(root)
	if err != nil {
		return err
	}
	if node == root {
		return fmt.Errorf("%q addresses the whole document, and removing it would leave nothing to send", p.raw)
	}

	parent := node.Parent()
	if parent == nil {
		return fmt.Errorf("%q addresses an element with no parent, so there is nothing to remove it from", p.raw)
	}
	parent.Remove(node)

	return nil
}

// isCodedName reports whether an element of this name carries its value in a code attribute.
//
// v3 puts a coded value in code and a plain value in value, and the two are not interchangeable: writing value on
// administrativeGenderCode produces an element a receiver reads as having no code at all. The test is the name suffix,
// which is the convention the standard follows throughout - administrativeGenderCode, statusCode, classCode,
// religiousAffiliationCode.
//
// A suffix test rather than a list of names, because the list would be wrong the first time somebody used an element this
// file's author had not seen, and being wrong that way is silent.
func isCodedName(name string) bool {
	const suffix = "Code"

	return len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix
}
