package e4x

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
	"github.com/dop251/goja"
)

// XMLObject is the E4X XML and XMLList type, presented to JavaScript.
//
// E4X blurs a single element and a list of them on purpose, and Mirth scripts
// depend on that blurring constantly. All of these appear in real channels:
//
//	msg['PID']['PID.5']['PID.5.1'].toString()   // reads through three levels
//	msg['PID']['PID.3'][1]                      // indexes a repetition
//	msg['OBX'].length()                          // counts them
//	msg['PID']['PID.5']['PID.5.1'] = 'Smith'     // assigns
//
// The second and third only work because msg['OBX'] is a list, while the first
// only works because msg['PID'] behaves like a single element. So one type does
// both: a list of nodes, where reading a property maps over every member and
// concatenating a value of a one-member list gives that member's text.
//
// Anything that would be ambiguous on a list of more than one - assignment, for
// instance - operates on the first member, which is what Rhino does.
type XMLObject struct {
	nodes []*xtree.Node

	// name is the element name this list was produced by, needed so that
	// assigning into an absent element can create it with the right name.
	name string
	// origin is the node the list was selected from, so an assignment to a
	// property that matched nothing can still create it.
	origin *xtree.Node
	// wantIndex is the repetition a script asked for that did not exist yet. It
	// has to be remembered, because msg['PID']['PID.3'][2]['PID.3.1'] = 'x'
	// reads the missing repetition before assigning into it, and creating it at
	// index 0 would silently overwrite the first identifier.
	wantIndex int

	rt *Runtime
}

// Runtime holds the goja VM and the helpers the rewritten source calls.
type Runtime struct {
	vm *goja.Runtime
}

// NewRuntime wires the E4X helpers into a goja runtime.
func NewRuntime(vm *goja.Runtime) (*Runtime, error) {
	r := &Runtime{vm: vm}

	// __e4xEach normalises anything for-each was pointed at into an array.
	if err := vm.Set("__e4xEach", func(call goja.FunctionCall) goja.Value {
		arg := call.Argument(0)
		if x, ok := asXML(arg); ok {
			out := make([]any, 0, len(x.nodes))
			for _, n := range x.nodes {
				out = append(out, r.wrapOne(n))
			}
			return vm.ToValue(out)
		}
		if goja.IsUndefined(arg) || goja.IsNull(arg) {
			return vm.ToValue([]any{})
		}
		// An ordinary array or string iterates as itself.
		if obj, ok := arg.(*goja.Object); ok {
			if obj.ClassName() == "Array" {
				return arg
			}
			// A Java-style map or list from the Mirth helpers.
			if fn, ok := goja.AssertFunction(obj.Get("toArray")); ok {
				if v, err := fn(obj); err == nil {
					return v
				}
			}
		}
		return vm.ToValue([]any{arg})
	}); err != nil {
		return nil, err
	}

	// __e4xParse builds a tree from an XML literal.
	if err := vm.Set("__e4xParse", func(call goja.FunctionCall) goja.Value {
		text := strings.TrimSpace(call.Argument(0).String())
		node, err := xtree.Parse([]byte(text))
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("XML literal is not well formed: %w", err)))
		}
		return r.wrapOne(node)
	}); err != nil {
		return nil, err
	}

	// new XML('...') and new XMLList('...'), which scripts use to build nodes.
	xmlCtor := func(call goja.ConstructorCall) *goja.Object {
		arg := call.Argument(0)
		if goja.IsUndefined(arg) || arg.String() == "" {
			return r.wrapOne(xtree.New("")).ToObject(vm)
		}
		if x, ok := asXML(arg); ok {
			return r.wrap(cloneAll(x.nodes)).ToObject(vm)
		}
		node, err := xtree.Parse([]byte(strings.TrimSpace(arg.String())))
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("XML(): %w", err)))
		}
		return r.wrapOne(node).ToObject(vm)
	}
	if err := vm.Set("XML", xmlCtor); err != nil {
		return nil, err
	}
	if err := vm.Set("XMLList", xmlCtor); err != nil {
		return nil, err
	}

	return r, nil
}

// VM exposes the underlying runtime.
func (r *Runtime) VM() *goja.Runtime { return r.vm }

// Wrap presents a tree to JavaScript as an E4X object.
func (r *Runtime) Wrap(node *xtree.Node) goja.Value { return r.wrapOne(node) }

func (r *Runtime) wrapOne(node *xtree.Node) goja.Value {
	if node == nil {
		return r.wrap(nil)
	}
	return r.wrap([]*xtree.Node{node})
}

func (r *Runtime) wrap(nodes []*xtree.Node) goja.Value {
	x := &XMLObject{nodes: nodes, wantIndex: -1, rt: r}
	return r.vm.NewDynamicObject(x)
}

func (r *Runtime) wrapSelection(nodes []*xtree.Node, name string, origin *xtree.Node) goja.Value {
	x := &XMLObject{nodes: nodes, name: name, origin: origin, wantIndex: -1, rt: r}
	return r.vm.NewDynamicObject(x)
}

// wrapMissing represents a repetition a script asked for that is not there yet.
func (r *Runtime) wrapMissing(name string, origin *xtree.Node, index int) goja.Value {
	x := &XMLObject{name: name, origin: origin, wantIndex: index, rt: r}
	return r.vm.NewDynamicObject(x)
}

// Nodes returns the underlying tree nodes, for Go code that needs the result of
// a script.
func (x *XMLObject) Nodes() []*xtree.Node { return x.nodes }

// asXML recovers an XMLObject from a JavaScript value.
func asXML(v goja.Value) (*XMLObject, bool) {
	obj, ok := v.(*goja.Object)
	if !ok {
		return nil, false
	}
	dyn, ok := obj.Export().(goja.DynamicObject)
	if !ok {
		return nil, false
	}
	x, ok := dyn.(*XMLObject)
	return x, ok
}

func cloneAll(nodes []*xtree.Node) []*xtree.Node {
	out := make([]*xtree.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Clone())
	}
	return out
}

// Get implements property access.
//
// The order of these cases is the semantics. A numeric key indexes the list; a
// method name returns a callable; anything else selects child elements by name,
// which is what makes msg['PID']['PID.5'] work.
func (x *XMLObject) Get(key string) goja.Value {
	vm := x.rt.vm

	if index, err := strconv.Atoi(key); err == nil {
		if index >= 0 && index < len(x.nodes) {
			return x.rt.wrapOne(x.nodes[index])
		}
		// Indexing past the end yields an empty list, not undefined, so that
		// chained access on it does not throw. E4X behaves this way and scripts
		// rely on it to test for optional repetitions. The index is carried so
		// that assigning through it creates the right repetition.
		return x.rt.wrapMissing(x.name, x.origin, index)
	}

	if fn := x.method(key); fn != nil {
		return vm.ToValue(fn)
	}

	// An attribute written in bracket form.
	if strings.HasPrefix(key, "@") {
		return x.attribute(key[1:])
	}

	// Select children by element name across every member of the list.
	var selected []*xtree.Node
	for _, n := range x.nodes {
		if key == "*" {
			selected = append(selected, n.Children...)
			continue
		}
		selected = append(selected, n.All(key)...)
	}
	var origin *xtree.Node
	if len(x.nodes) > 0 {
		origin = x.nodes[0]
	}
	return x.rt.wrapSelection(selected, key, origin)
}

// Set implements assignment.
//
// Assigning to a name that does not exist creates it, because that is how a
// Mirth transformer adds a field. Assigning a string to an element that has
// children replaces them, matching E4X.
func (x *XMLObject) Set(key string, val goja.Value) bool {
	if index, err := strconv.Atoi(key); err == nil {
		if index >= 0 && index < len(x.nodes) {
			assign(x.nodes[index], val)
			return true
		}
		// Assigning past the end appends, creating the intermediate gaps.
		if x.origin != nil && x.name != "" {
			node := x.origin.Ensure(x.name, index)
			assign(node, val)
			x.nodes = x.origin.All(x.name)
			return true
		}
		return false
	}

	if strings.HasPrefix(key, "@") {
		for _, n := range x.nodes {
			n.SetAttr(key[1:], val.String())
		}
		return true
	}

	// Assigning to a child name: find it, or create it.
	target := x.nodes
	if len(target) == 0 {
		// The list itself is empty, which happens when a script reaches through
		// an element that was not in the message. Create it on the origin.
		if x.origin == nil || x.name == "" {
			return false
		}
		at := x.wantIndex
		if at < 0 {
			at = 0
		}
		created := x.origin.Ensure(x.name, at)
		x.nodes = []*xtree.Node{created}
		target = x.nodes
	}

	for _, n := range target {
		child := n.First(key)
		if child == nil {
			child = n.Append(xtree.New(key))
		}
		assign(child, val)
	}
	return true
}

// assign writes a JavaScript value into a node.
func assign(node *xtree.Node, val goja.Value) {
	if x, ok := asXML(val); ok {
		node.SetText("")
		node.Children = nil
		for _, src := range x.nodes {
			node.Append(src.Clone())
		}
		if len(x.nodes) == 1 && x.nodes[0].Simple() {
			node.SetText(x.nodes[0].Text)
		}
		return
	}
	if goja.IsUndefined(val) || goja.IsNull(val) {
		node.SetText("")
		return
	}
	node.SetText(val.String())
}

// Has reports whether a property exists, which drives the "in" operator.
func (x *XMLObject) Has(key string) bool {
	if index, err := strconv.Atoi(key); err == nil {
		return index >= 0 && index < len(x.nodes)
	}
	if x.method(key) != nil {
		return true
	}
	for _, n := range x.nodes {
		if n.Count(key) > 0 {
			return true
		}
	}
	return false
}

// Delete removes an element or attribute, backing the delete operator.
func (x *XMLObject) Delete(key string) bool {
	if index, err := strconv.Atoi(key); err == nil {
		if index < 0 || index >= len(x.nodes) {
			return false
		}
		node := x.nodes[index]
		if parent := node.Parent(); parent != nil {
			parent.Remove(node)
		}
		x.nodes = append(x.nodes[:index], x.nodes[index+1:]...)
		return true
	}
	if strings.HasPrefix(key, "@") {
		removed := false
		for _, n := range x.nodes {
			if n.RemoveAttr(key[1:]) {
				removed = true
			}
		}
		return removed
	}
	removed := false
	for _, n := range x.nodes {
		if n.RemoveName(key) > 0 {
			removed = true
		}
	}
	return removed
}

// Keys lists enumerable properties: the indexes, matching E4X's for-in.
func (x *XMLObject) Keys() []string {
	keys := make([]string, 0, len(x.nodes))
	for i := range x.nodes {
		keys = append(keys, strconv.Itoa(i))
	}
	return keys
}

func (x *XMLObject) first() *xtree.Node {
	if len(x.nodes) == 0 {
		return nil
	}
	return x.nodes[0]
}

// text returns what E4X's toString gives: the text of simple content, or the
// markup of complex content.
func (x *XMLObject) text() string {
	if len(x.nodes) == 0 {
		return ""
	}
	if len(x.nodes) == 1 {
		n := x.nodes[0]
		if n.Simple() {
			return n.Text
		}
		return n.String()
	}
	var b strings.Builder
	for _, n := range x.nodes {
		if n.Simple() {
			b.WriteString(n.Text)
		} else {
			b.WriteString(n.String())
		}
	}
	return b.String()
}

func (x *XMLObject) method(name string) func(goja.FunctionCall) goja.Value {
	vm := x.rt.vm
	r := x.rt

	switch name {
	case "toString", "valueOf":
		return func(goja.FunctionCall) goja.Value { return vm.ToValue(x.text()) }

	case "toXMLString":
		return func(goja.FunctionCall) goja.Value {
			var b strings.Builder
			for _, n := range x.nodes {
				b.WriteString(n.String())
			}
			return vm.ToValue(b.String())
		}

	case "length":
		return func(goja.FunctionCall) goja.Value { return vm.ToValue(len(x.nodes)) }

	case "text":
		return func(goja.FunctionCall) goja.Value {
			var b strings.Builder
			for _, n := range x.nodes {
				b.WriteString(n.Value())
			}
			return vm.ToValue(b.String())
		}

	case "name", "localName":
		return func(goja.FunctionCall) goja.Value {
			if n := x.first(); n != nil {
				return vm.ToValue(n.Name)
			}
			return goja.Undefined()
		}

	case "parent":
		return func(goja.FunctionCall) goja.Value {
			if n := x.first(); n != nil && n.Parent() != nil {
				return r.wrapOne(n.Parent())
			}
			return goja.Undefined()
		}

	case "children", "elements":
		return func(call goja.FunctionCall) goja.Value {
			var out []*xtree.Node
			wanted := ""
			if len(call.Arguments) > 0 {
				wanted = call.Argument(0).String()
			}
			for _, n := range x.nodes {
				for _, c := range n.Children {
					if wanted == "" || wanted == "*" || c.Name == wanted {
						out = append(out, c)
					}
				}
			}
			return r.wrapSelection(out, wanted, x.first())
		}

	case "child":
		return func(call goja.FunctionCall) goja.Value {
			arg := call.Argument(0)
			if index, err := strconv.Atoi(arg.String()); err == nil {
				var out []*xtree.Node
				for _, n := range x.nodes {
					if index >= 0 && index < len(n.Children) {
						out = append(out, n.Children[index])
					}
				}
				return r.wrap(out)
			}
			return x.Get(arg.String())
		}

	case "descendants":
		return func(call goja.FunctionCall) goja.Value {
			wanted := "*"
			if len(call.Arguments) > 0 {
				wanted = call.Argument(0).String()
			}
			return x.descendants(wanted)
		}

	case "__e4xDescendants":
		return func(call goja.FunctionCall) goja.Value {
			return x.descendants(call.Argument(0).String())
		}

	case "attribute", "__e4xAttribute":
		return func(call goja.FunctionCall) goja.Value {
			return x.attribute(call.Argument(0).String())
		}

	case "attributes", "__e4xAttributes":
		return func(goja.FunctionCall) goja.Value {
			var out []*xtree.Node
			for _, n := range x.nodes {
				for _, a := range n.Attrs {
					out = append(out, xtree.Leaf("@"+a.Name, a.Value))
				}
			}
			return r.wrap(out)
		}

	case "hasSimpleContent":
		return func(goja.FunctionCall) goja.Value {
			n := x.first()
			return vm.ToValue(n != nil && n.Simple())
		}

	case "hasComplexContent":
		return func(goja.FunctionCall) goja.Value {
			n := x.first()
			return vm.ToValue(n != nil && !n.Simple())
		}

	case "appendChild":
		return func(call goja.FunctionCall) goja.Value {
			target := x.first()
			if target == nil {
				return goja.Undefined()
			}
			for _, n := range nodesFrom(call.Argument(0)) {
				target.Append(n)
			}
			return r.wrapOne(target)
		}

	case "prependChild":
		return func(call goja.FunctionCall) goja.Value {
			target := x.first()
			if target == nil {
				return goja.Undefined()
			}
			incoming := nodesFrom(call.Argument(0))
			for i := len(incoming) - 1; i >= 0; i-- {
				target.Insert(0, incoming[i])
			}
			return r.wrapOne(target)
		}

	case "insertChildAfter", "insertChildBefore":
		before := name == "insertChildBefore"
		return func(call goja.FunctionCall) goja.Value {
			target := x.first()
			if target == nil {
				return goja.Undefined()
			}
			ref, _ := asXML(call.Argument(0))
			incoming := nodesFrom(call.Argument(1))
			at := len(target.Children)
			if ref != nil && ref.first() != nil {
				for i, c := range target.Children {
					if c == ref.first() {
						at = i
						if !before {
							at = i + 1
						}
						break
					}
				}
			}
			for i := len(incoming) - 1; i >= 0; i-- {
				target.Insert(at, incoming[i])
			}
			return r.wrapOne(target)
		}

	case "setChildren":
		return func(call goja.FunctionCall) goja.Value {
			target := x.first()
			if target == nil {
				return goja.Undefined()
			}
			target.SetText("")
			target.Children = nil
			for _, n := range nodesFrom(call.Argument(0)) {
				target.Append(n)
			}
			return r.wrapOne(target)
		}

	case "setName":
		return func(call goja.FunctionCall) goja.Value {
			if n := x.first(); n != nil {
				n.Name = call.Argument(0).String()
			}
			return goja.Undefined()
		}

	case "copy":
		return func(goja.FunctionCall) goja.Value { return r.wrap(cloneAll(x.nodes)) }

	case "contains":
		return func(call goja.FunctionCall) goja.Value {
			other, ok := asXML(call.Argument(0))
			if !ok {
				return vm.ToValue(false)
			}
			for _, want := range other.nodes {
				for _, have := range x.nodes {
					if have == want {
						return vm.ToValue(true)
					}
				}
			}
			return vm.ToValue(false)
		}

	// Mirth scripts call these String methods straight on an XML value, relying
	// on E4X's implicit conversion. goja will not convert for us, so the common
	// ones are forwarded to the text.
	case "indexOf", "lastIndexOf", "substring", "substr", "slice", "charAt",
		"toUpperCase", "toLowerCase", "trim", "split", "replace", "match",
		"concat", "search", "startsWith", "endsWith", "padStart", "padEnd", "includes":
		return func(call goja.FunctionCall) goja.Value {
			str := vm.ToValue(x.text()).ToObject(vm)
			fn, ok := goja.AssertFunction(str.Get(name))
			if !ok {
				return goja.Undefined()
			}
			out, err := fn(str, call.Arguments...)
			if err != nil {
				panic(err)
			}
			return out
		}
	}

	return nil
}

func (x *XMLObject) descendants(name string) goja.Value {
	var out []*xtree.Node
	for _, n := range x.nodes {
		out = append(out, n.Descendants(name)...)
	}
	return x.rt.wrapSelection(out, name, x.first())
}

func (x *XMLObject) attribute(name string) goja.Value {
	var out []*xtree.Node
	for _, n := range x.nodes {
		if v, ok := n.Attr(name); ok {
			out = append(out, xtree.Leaf("@"+name, v))
		}
	}
	return x.rt.wrap(out)
}

// nodesFrom converts an argument into nodes to insert, accepting an XML object,
// a string of markup, or plain text.
func nodesFrom(v goja.Value) []*xtree.Node {
	if x, ok := asXML(v); ok {
		return cloneAll(x.nodes)
	}
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	text := v.String()
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "<") {
		if node, err := xtree.Parse([]byte(trimmed)); err == nil {
			return []*xtree.Node{node}
		}
	}
	return []*xtree.Node{xtree.Leaf("", text)}
}
