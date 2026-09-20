package mirth

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Mirth channel exports are XStream serializations of Java objects. Element
// names are fully-qualified class names that move between Mirth versions, and
// unknown plugins can appear anywhere. Binding that to fixed Go structs makes a
// parser that fails on the next version.
//
// So we walk the document into a generic tree and map from it deliberately,
// recording anything we did not recognise instead of dropping or rejecting it.
// "I read this channel and here are the four things I could not translate" is
// a far more useful answer than an error.

var errEmptyDocument = errors.New("mirth: document contained no elements")

type node struct {
	Name string
	Attr map[string]string
	Text string
	Kids []*node
}

func parseTree(r io.Reader) (*node, error) {
	dec := xml.NewDecoder(r)

	var root *node
	var stack []*node
	var text []*strings.Builder

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name.Local}
			if len(t.Attr) > 0 {
				n.Attr = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					n.Attr[a.Name.Local] = a.Value
				}
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Kids = append(parent.Kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
			text = append(text, &strings.Builder{})

		case xml.CharData:
			if len(text) > 0 {
				text[len(text)-1].Write(t)
			}

		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			n := stack[len(stack)-1]
			n.Text = strings.TrimSpace(text[len(text)-1].String())
			stack = stack[:len(stack)-1]
			text = text[:len(text)-1]
		}
	}

	if root == nil {
		return nil, errEmptyDocument
	}
	return root, nil
}

// child returns the first direct child with the given name, or nil.
func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for _, k := range n.Kids {
		if k.Name == name {
			return k
		}
	}
	return nil
}

// children returns all direct children with the given name.
func (n *node) children(name string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, k := range n.Kids {
		if k.Name == name {
			out = append(out, k)
		}
	}
	return out
}

// at walks a chain of child names, returning nil if any link is missing.
func (n *node) at(path ...string) *node {
	cur := n
	for _, p := range path {
		cur = cur.child(p)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// str returns the trimmed text at a child path, or "".
func (n *node) str(path ...string) string {
	if t := n.at(path...); t != nil {
		return t.Text
	}
	return ""
}

// boolAt reports whether the text at a child path is "true".
func (n *node) boolAt(path ...string) bool {
	return strings.EqualFold(n.str(path...), "true")
}

// intAt returns the integer at a child path, or 0.
func (n *node) intAt(path ...string) int {
	v, err := strconv.Atoi(n.str(path...))
	if err != nil {
		return 0
	}
	return v
}

// class returns the XStream "class" attribute, which is how Mirth records the
// concrete connector or properties implementation.
func (n *node) class() string {
	if n == nil || n.Attr == nil {
		return ""
	}
	return n.Attr["class"]
}

// leaves flattens the subtree into dotted-path -> text for every element that
// has text and no element children. Connector property bags differ per
// transport, so we keep them generically rather than modelling each one.
func (n *node) leaves() map[string]string {
	out := map[string]string{}
	if n == nil {
		return out
	}
	var walk func(cur *node, prefix string)
	walk = func(cur *node, prefix string) {
		for _, k := range cur.Kids {
			path := k.Name
			if prefix != "" {
				path = prefix + "." + k.Name
			}
			if len(k.Kids) == 0 {
				if k.Text != "" {
					out[path] = k.Text
				}
				continue
			}
			walk(k, path)
		}
	}
	walk(n, "")
	return out
}

// childNames lists the direct child element names, used to report the parts of
// a channel we did not recognise.
func (n *node) childNames() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Kids))
	for _, k := range n.Kids {
		out = append(out, k.Name)
	}
	return out
}
