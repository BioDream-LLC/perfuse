// Package xtree is a mutable XML tree.
//
// Three very different features need to modify XML: HL7 transformations, which
// Mirth models as XML because that is what its scripts operate on; clinical
// documents, which are XML natively; and the declarative transformation steps.
// They share this one tree rather than each growing their own, because the
// alternative is three models that disagree about what an empty element means.
//
// The design goal is faithfulness to E4X semantics, since Mirth scripts are E4X
// and the whole point of running them unchanged is that they behave the same.
// That drives two decisions that would otherwise look strange: a node keeps a
// pointer to its parent, because E4X exposes parent(), and children are ordered
// with duplicates allowed, because an HL7 field repetition is literally a
// repeated element and scripts index into them by position.
package xtree

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Attr is an attribute. Namespace is the URI when one applies, not the prefix,
// so that a document which declares xmlns:h on one element and a different
// prefix for the same namespace on another still compares equal.
type Attr struct {
	Name      string
	Namespace string
	Value     string
}

// Node is an element, or a text-bearing leaf.
//
// Text and Children are deliberately both present rather than modelling text as
// a child node. HL7-as-XML is overwhelmingly leaves with text, and a design that
// allocated a child node for every component would triple the tree for no gain.
// Mixed content, which CDA narrative blocks do use, is handled by Fragments.
type Node struct {
	Name      string
	Namespace string
	Attrs     []Attr

	// Text is the character data when this node has no element children.
	Text string

	Children []*Node

	// Fragments preserves mixed content in document order for the cases that
	// need it, chiefly CDA narrative text with inline markup. It is nil for the
	// ordinary case of an element that is either a leaf or has only elements.
	Fragments []Fragment

	parent *Node
}

// Fragment is one piece of mixed content: either text, or a child element.
type Fragment struct {
	Text string
	Node *Node
}

// New makes a detached element.
func New(name string) *Node { return &Node{Name: name} }

// Leaf makes a detached element holding text.
func Leaf(name, text string) *Node { return &Node{Name: name, Text: text} }

// Parent returns the containing element, or nil at the root.
func (n *Node) Parent() *Node {
	if n == nil {
		return nil
	}
	return n.parent
}

// Root walks up to the outermost element.
func (n *Node) Root() *Node {
	for n != nil && n.parent != nil {
		n = n.parent
	}
	return n
}

// Append adds a child, taking ownership of it.
//
// A node that already has a parent is copied rather than moved. E4X does the
// same, and the reason is worth stating: a script that appends a segment it read
// from the inbound message to the outbound one almost never means "remove it
// from the input", and silently moving it produces a bug that only shows up in
// the message after the one being debugged.
func (n *Node) Append(child *Node) *Node {
	if child == nil {
		return nil
	}
	if child.parent != nil {
		child = child.Clone()
	}
	child.parent = n
	n.Children = append(n.Children, child)
	n.Text = ""
	return child
}

// Insert places a child at the given index, clamping out-of-range positions
// rather than failing, because scripts compute these indexes.
func (n *Node) Insert(index int, child *Node) *Node {
	if child == nil {
		return nil
	}
	if child.parent != nil {
		child = child.Clone()
	}
	if index < 0 {
		index = 0
	}
	if index > len(n.Children) {
		index = len(n.Children)
	}
	child.parent = n
	n.Children = append(n.Children, nil)
	copy(n.Children[index+1:], n.Children[index:])
	n.Children[index] = child
	n.Text = ""
	return child
}

// Remove detaches a child. It reports whether anything was removed.
func (n *Node) Remove(child *Node) bool {
	for i, c := range n.Children {
		if c == child {
			n.Children = append(n.Children[:i], n.Children[i+1:]...)
			c.parent = nil
			return true
		}
	}
	return false
}

// RemoveName detaches every child with the given name and returns how many went.
func (n *Node) RemoveName(name string) int {
	kept := n.Children[:0]
	removed := 0
	for _, c := range n.Children {
		if c.Name == name {
			c.parent = nil
			removed++
			continue
		}
		kept = append(kept, c)
	}
	n.Children = kept
	return removed
}

// Child returns the index'th child with the given name, or nil.
func (n *Node) Child(name string, index int) *Node {
	if n == nil {
		return nil
	}
	seen := 0
	for _, c := range n.Children {
		if c.Name != name {
			continue
		}
		if seen == index {
			return c
		}
		seen++
	}
	return nil
}

// First returns the first child with the given name, or nil.
func (n *Node) First(name string) *Node { return n.Child(name, 0) }

// All returns every child with the given name, in document order.
func (n *Node) All(name string) []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// Count returns how many children carry the given name.
func (n *Node) Count(name string) int {
	if n == nil {
		return 0
	}
	total := 0
	for _, c := range n.Children {
		if c.Name == name {
			total++
		}
	}
	return total
}

// Ensure returns the index'th child with the given name, creating it and any
// earlier repetitions if they are missing.
//
// Creating the gap matters: a script that assigns to the third repetition of a
// field when one exists means the third, not the second. Compacting would put
// the value in the wrong place, and in an address or an identifier list that is
// a data corruption nobody notices until a human reads it.
func (n *Node) Ensure(name string, index int) *Node {
	for n.Count(name) <= index {
		n.Append(New(name))
	}
	return n.Child(name, index)
}

// Attr returns an attribute value and whether it was present. Absent and empty
// are different questions and callers need to tell them apart.
func (n *Node) Attr(name string) (string, bool) {
	if n == nil {
		return "", false
	}
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// AttrValue returns an attribute value, empty when absent.
func (n *Node) AttrValue(name string) string {
	v, _ := n.Attr(name)
	return v
}

// SetAttr sets or replaces an attribute.
func (n *Node) SetAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, Attr{Name: name, Value: value})
}

// RemoveAttr deletes an attribute, reporting whether it was there.
func (n *Node) RemoveAttr(name string) bool {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs = append(n.Attrs[:i], n.Attrs[i+1:]...)
			return true
		}
	}
	return false
}

// SetText replaces the content with text, discarding any children.
func (n *Node) SetText(text string) {
	for _, c := range n.Children {
		c.parent = nil
	}
	n.Children = nil
	n.Fragments = nil
	n.Text = text
}

// Simple reports whether the node has no element children, which E4X calls
// simple content.
func (n *Node) Simple() bool { return n != nil && len(n.Children) == 0 }

// Value returns the text of a leaf, or the concatenated text of a subtree.
//
// E4X's toString() returns markup for complex content, but almost every use in
// a real script is reading a value, so the distinction is left to the E4X layer
// and this returns what a human means by the content.
func (n *Node) Value() string {
	if n == nil {
		return ""
	}
	if len(n.Children) == 0 {
		return n.Text
	}
	var b strings.Builder
	n.writeText(&b)
	return b.String()
}

func (n *Node) writeText(b *strings.Builder) {
	if len(n.Fragments) > 0 {
		for _, f := range n.Fragments {
			if f.Node != nil {
				f.Node.writeText(b)
			} else {
				b.WriteString(f.Text)
			}
		}
		return
	}
	b.WriteString(n.Text)
	for _, c := range n.Children {
		c.writeText(b)
	}
}

// Clone makes a deep, detached copy.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	out := &Node{
		Name:      n.Name,
		Namespace: n.Namespace,
		Text:      n.Text,
	}
	if len(n.Attrs) > 0 {
		out.Attrs = make([]Attr, len(n.Attrs))
		copy(out.Attrs, n.Attrs)
	}
	for _, c := range n.Children {
		clone := c.Clone()
		clone.parent = out
		out.Children = append(out.Children, clone)
	}
	for _, f := range n.Fragments {
		if f.Node == nil {
			out.Fragments = append(out.Fragments, Fragment{Text: f.Text})
			continue
		}
		// Point the fragment at the already-cloned child so that the two views
		// of the same content cannot drift apart.
		if i := n.indexOfChild(f.Node); i >= 0 && i < len(out.Children) {
			out.Fragments = append(out.Fragments, Fragment{Node: out.Children[i]})
		}
	}
	return out
}

func (n *Node) indexOfChild(child *Node) int {
	for i, c := range n.Children {
		if c == child {
			return i
		}
	}
	return -1
}

// Find returns the first descendant matching name, breadth-first so that the
// shallowest match wins. CDA nests the same element name at several depths and
// the outer one is nearly always the one meant.
func (n *Node) Find(name string) *Node {
	if n == nil {
		return nil
	}
	queue := []*Node{n}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range cur.Children {
			if c.Name == name {
				return c
			}
		}
		queue = append(queue, cur.Children...)
	}
	return nil
}

// Descendants returns every descendant matching name, in document order. This
// backs E4X's .. operator.
func (n *Node) Descendants(name string) []*Node {
	var out []*Node
	if n == nil {
		return out
	}
	var walk func(*Node)
	walk = func(cur *Node) {
		for _, c := range cur.Children {
			if name == "*" || c.Name == name {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

// Path walks a slash-separated element path, creating nothing.
// An empty step or a missing element yields nil.
func (n *Node) Path(path string) *Node {
	cur := n
	for _, step := range strings.Split(path, "/") {
		if step == "" {
			continue
		}
		name, index := step, 0
		if open := strings.IndexByte(step, '['); open > 0 && strings.HasSuffix(step, "]") {
			name = step[:open]
			if _, err := fmt.Sscanf(step[open+1:len(step)-1], "%d", &index); err != nil {
				return nil
			}
		}
		if cur = cur.Child(name, index); cur == nil {
			return nil
		}
	}
	return cur
}

// Depth reports how many ancestors a node has, used to bound recursion when
// serialising a tree a script may have made deeper than expected.
func (n *Node) Depth() int {
	depth := 0
	for cur := n.parent; cur != nil; cur = cur.parent {
		depth++
	}
	return depth
}

// SetParent re-establishes parent pointers throughout a subtree. It is needed
// after a tree is assembled by literal struct construction, which the HL7
// converter does for speed.
func (n *Node) SetParent(parent *Node) {
	n.parent = parent
	for _, c := range n.Children {
		c.SetParent(n)
	}
}

// SortChildren orders children by name, stable within a name so repetitions keep
// their relative order. Used only where a schema demands element order.
func (n *Node) SortChildren(order map[string]int) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, aok := order[n.Children[i].Name]
		b, bok := order[n.Children[j].Name]
		switch {
		case aok && bok:
			return a < b
		case aok:
			return true
		default:
			return false
		}
	})
}

// WellFormed reports whether data is well-formed XML by the specification, not by what Parse will tolerate.
//
// # Why this exists separately from Parse
//
// Parse sets Strict to false, which is right for its job: documents arrive from other vendors, real ones carry minor
// non-conformances, and refusing them would take an interface down over something a receiver would have accepted. Being
// generous about what you accept is the correct direction.
//
// Emitting is the opposite direction, and reusing Parse to check outbound documents applies the tolerance the wrong way. A
// script that writes an element named with a space in it produces text this package will happily read back and every
// conforming parser will reject - so a check built on Parse passes, the document goes out, and the failure appears at the
// receiver as though it were the receiver's fault.
//
// So this is the check to use after a script has written to a tree: strict, and interested only in whether the bytes are
// legal rather than in what they mean.
func WellFormed(data []byte) error {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = true

	// The same entity restriction Parse applies. Strictness here is about syntax, and it would be an odd trade to gain it
	// while giving up the refusal to expand an external entity.
	dec.Entity = xml.HTMLEntity

	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("xml: %w", err)
		}
	}
}

// Parse reads XML into a tree.
//
// It is deliberately strict about one thing and tolerant about everything else.
// Strict: entity expansion is not performed beyond the predefined five, so a
// document cannot make the parser read a file or expand exponentially. Clinical
// documents arrive from outside and a billion-laughs attack on an integration
// engine would take down the interface for a whole hospital.
//
// Tolerant about syntax, which is why WellFormed exists: this will read back text that no conforming parser accepts, so it
// cannot be used to check something Perfuse is about to send.
func Parse(data []byte) (*Node, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	// Refuse to resolve anything but the predefined entities.
	dec.Entity = xml.HTMLEntity

	var root *Node
	stack := []*Node{}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > maxDepth {
				return nil, fmt.Errorf("xml: nesting deeper than %d elements, refusing", maxDepth)
			}
			node := &Node{Name: t.Name.Local, Namespace: t.Name.Space}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue // Declarations are structure, not data.
				}
				node.Attrs = append(node.Attrs, Attr{
					Name:      a.Name.Local,
					Namespace: a.Name.Space,
					Value:     a.Value,
				})
			}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				node.parent = parent
				parent.Children = append(parent.Children, node)
				parent.Fragments = append(parent.Fragments, Fragment{Node: node})
			}
			stack = append(stack, node)

		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("xml: closing tag %q with nothing open", t.Name.Local)
			}
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			// Drop the fragment list when it adds nothing, so the common case
			// costs no memory.
			if !node.mixed() {
				node.Fragments = nil
			}

		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			node := stack[len(stack)-1]
			text := string(t)
			if len(node.Children) == 0 {
				node.Text += text
			}
			if strings.TrimSpace(text) != "" || len(node.Children) > 0 {
				node.Fragments = append(node.Fragments, Fragment{Text: text})
			}
		}
	}

	if root == nil {
		return nil, fmt.Errorf("xml: no elements found")
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("xml: %d unclosed element(s), starting with %q", len(stack), stack[0].Name)
	}
	return root, nil
}

const maxDepth = 256

// mixed reports whether the node has both text and elements interleaved.
func (n *Node) mixed() bool {
	if len(n.Children) == 0 {
		return false
	}
	for _, f := range n.Fragments {
		if f.Node == nil && strings.TrimSpace(f.Text) != "" {
			return true
		}
	}
	return false
}

// Marshal serialises the tree. Indent of zero writes it on one line.
func (n *Node) Marshal(indent int) []byte {
	var b strings.Builder
	n.write(&b, indent, 0)
	return []byte(b.String())
}

// String returns the tree as XML on a single line.
func (n *Node) String() string {
	if n == nil {
		return ""
	}
	return string(n.Marshal(0))
}

func (n *Node) write(b *strings.Builder, indent, level int) {
	pad := func(l int) {
		if indent > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*l))
		}
	}

	if level > 0 {
		pad(level)
	}
	b.WriteByte('<')
	b.WriteString(n.Name)
	for _, a := range n.Attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		escapeAttr(b, a.Value)
		b.WriteByte('"')
	}

	// An element with neither text nor children is written self-closing.
	if len(n.Children) == 0 && n.Text == "" {
		b.WriteString("/>")
		return
	}
	b.WriteByte('>')

	if len(n.Fragments) > 0 && n.mixed() {
		for _, f := range n.Fragments {
			if f.Node != nil {
				f.Node.write(b, 0, 0)
			} else {
				escapeText(b, f.Text)
			}
		}
	} else if len(n.Children) == 0 {
		escapeText(b, n.Text)
	} else {
		for _, c := range n.Children {
			c.write(b, indent, level+1)
		}
		pad(level)
	}

	b.WriteString("</")
	b.WriteString(n.Name)
	b.WriteByte('>')
}

func escapeText(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		default:
			b.WriteByte(s[i])
		}
	}
}

func escapeAttr(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			b.WriteString("&lt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '\t':
			b.WriteString("&#9;")
		default:
			b.WriteByte(s[i])
		}
	}
}
