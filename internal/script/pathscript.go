package script

import (
	"fmt"
	"github.com/dop251/goja"
	lua "github.com/yuin/gopher-lua"

	"github.com/biodream-llc/perfuse/internal/steps"
)

// PathMessage is a message a script addresses by path rather than as a tree.
//
// # Why these formats get paths and HL7 gets a tree
//
// HL7 v2 and v3 scripts see an XML tree because Mirth's scripts do. That is a compatibility obligation - a site migrating has
// thousands of lines written against E4X - not a judgement that a tree is the right shape.
//
// X12, NCPDP and delimited have no such obligation, and for them a tree would be a worse fit invented for consistency's sake:
//
//   - The path is already the format's own vocabulary. CLM01 is how every X12 implementation guide addresses that element, D7
//     is what the NCPDP standard calls the product code, and a column name is what a CSV header says. A script written against
//     msg.child("CLM").child("01") would be a translation of the real name into a shape borrowed from XML.
//   - The paths are the same ones the declarative filter and the transformation steps already use, so a rule can be moved
//     between a step and a script without being rewritten, and a path in an error message means something to whoever reads it.
//   - Writes go through the format's own accessor, which knows the invariants. x12.Set re-pads a fixed-width ISA element and
//     refuses one that is too long. A tree walker applying changes afterwards would have to learn all of that again, or lose it.
//
// So the vocabulary differs by format because the formats differ. What does not differ is everything else: the verdict rules, the
// timeout, the logging, what a filter returning nothing means, what a neutralised write reports.
//
// Both languages reach this binding. That is newer than the binding itself: JavaScript was refused here at first, on the grounds
// that a goja version would be a second implementation which would drift on exactly those shared questions. The objection was
// sound and the conclusion did not follow - the answers were living inside the Lua closures, which is what would have forced a
// second copy. They are methods on PathMessage now, so the bindings are translation only and there is one answer to each question
// by construction rather than by discipline.
type PathMessage[M any] struct {
	// Value is the message. Replaced rather than mutated when a script writes, because the format accessors are functional -
	// x12.Set returns a new message rather than editing one, which is what makes a failed step leave the original intact.
	Value M

	// Accessor resolves paths for this format.
	Accessor steps.Accessor[M]

	// Changed records the paths a script wrote, in order.
	//
	// Kept so the engine can report what a script did in the same terms a declarative step reports, and so a script that
	// wrote nothing can be told from one that wrote the same value back. The second matters for an audit trail: "the script
	// ran and changed nothing" and "the script ran and set three fields" are different facts.
	Changed []string
}

// Get reads a path. The second return is false when the field is absent, which every format here distinguishes from blank.
//
// # Why these three methods exist rather than living in the binding
//
// They were closures inside the Lua binding, which was fine while Lua was the only language. It is not fine with two: the
// interesting behaviour is not the reading, it is what happens on an ambiguous path and on a write the format neutralises, and two
// bindings each with their own copy would answer those differently within a release or two.
//
// So the language bindings do translation only - an error becomes a thrown exception or a raised Lua error, an absent field becomes
// null or nil - and everything that decides *what happened* is here, once.
func (pm *PathMessage[M]) Get(raw string) (string, bool, error) {
	p, err := pm.Accessor.Path(raw)
	if err != nil {
		return "", false, err
	}

	v, present := p.Read(pm.Value)

	return v, present, nil
}

// Has reports whether a path is present.
func (pm *PathMessage[M]) Has(raw string) (bool, error) {
	p, err := pm.Accessor.Path(raw)
	if err != nil {
		return false, err
	}

	_, present := p.Read(pm.Value)

	return present, nil
}

// Set writes a path, recording the change unless the format neutralised it.
func (pm *PathMessage[M]) Set(raw, value string) error {
	p, err := pm.Accessor.Path(raw)
	if err != nil {
		return err
	}

	next, err := p.Write(pm.Value, value)
	if err != nil {
		// The format refused the write - an over-long ISA element, an ambiguous NCPDP path, a column that is not in the header.
		return err
	}

	// Re-read after writing, because a write can be neutralised: setting a fixed-width X12 element to a shorter string re-pads
	// it, so the value that lands is not the value asked for. Reporting a change that did not happen makes an audit trail
	// overstate, which is the same failure the declarative engine's re-read guards.
	if after, present := p.Read(next); !present || after != value {
		if before, had := p.Read(pm.Value); had && before == after {
			pm.Value = next

			return nil
		}
	}

	pm.Value = next
	pm.Changed = append(pm.Changed, p.Canonical)

	return nil
}

// bindPathMessage exposes get, set and has on a message addressed by path.
//
// # Why not a metatable making msg.CLM01 work
//
// It reads better and it cannot be made honest. A path is not an identifier: CLM01 is, but 07-D7 and #3 are not, and neither is
// a column named "Patient ID". Half the paths would work as fields and half would need a function, which is worse than all of
// them needing one.
func bindPathMessage[M any](L *lua.LState, pm *PathMessage[M]) *lua.LTable {
	t := L.NewTable()

	L.SetField(t, "get", L.NewFunction(func(inner *lua.LState) int {
		v, present, err := pm.Get(inner.CheckString(1))
		if err != nil {
			// Raised rather than returned as nil, because a mistyped path is a bug in the script and a script that carries
			// on with nil produces a message missing a field, discovered at the receiver. Failing here attributes it.
			inner.RaiseError("%s", err.Error())

			return 0
		}

		if !present {
			// Nil rather than an empty string. Every format here treats absent and blank as different: a blank field was
			// sent deliberately and an absent one was never sent, which for a claim is the difference between "no
			// diagnosis" and "the diagnosis segment is missing".
			inner.Push(lua.LNil)

			return 1
		}
		inner.Push(lua.LString(v))

		return 1
	}))

	L.SetField(t, "has", L.NewFunction(func(inner *lua.LState) int {
		present, err := pm.Has(inner.CheckString(1))
		if err != nil {
			inner.RaiseError("%s", err.Error())

			return 0
		}
		inner.Push(lua.LBool(present))

		return 1
	}))

	L.SetField(t, "set", L.NewFunction(func(inner *lua.LState) int {
		if err := pm.Set(inner.CheckString(1), inner.CheckString(2)); err != nil {
			// Raised so the script stops rather than continuing with a message it thinks it changed.
			inner.RaiseError("%s", err.Error())

			return 0
		}

		return 0
	}))

	return t
}

// RunPathScript runs a compiled script against a message addressed by path.
//
// Returns the message as the script left it, the paths it wrote, and the result. The message is returned rather than mutated in
// place because the format accessors are functional, and because a caller that gets its original back on error can retry from
// what arrived.
func RunPathScript[M any](e *Engine, s *Script, ctx *Context, msg M, accessor steps.Accessor[M]) (M, []string, Result, error) {
	if s == nil {
		return msg, nil, Result{Accept: true}, nil
	}

	pm := &PathMessage[M]{Value: msg, Accessor: accessor}

	// msg is the same global name the tree formats use, in both languages, so a script author moving between channels does not
	// learn a second word for the message. What it offers differs by format; what it is called does not.
	var (
		res Result
		err error
	)

	switch s.Language() {
	case Lua:
		res, err = runLuaWith(e, s, ctx, func(L *lua.LState) {
			L.SetGlobal("msg", bindPathMessage(L, pm))
		})

	case JavaScript:
		res, err = e.runJavaScriptWith(s, ctx, func(vm *goja.Runtime) error {
			bound, berr := bindPathMessageJS(vm, pm)
			if berr != nil {
				return berr
			}

			return vm.Set("msg", bound)
		})

	default:
		// WebAssembly cannot have this binding, and the reason is the interface rather than effort.
		//
		// A path binding is host functions: the script calls get and set and the host resolves the path. A module here is
		// given a message on stdin and writes bytes back, which is what makes it language-agnostic - it needs no agreement
		// about calling conventions or string encoding. Handing it get and set would mean inventing that agreement, and then
		// the answers to a neutralised write would live in a third place.
		//
		// Named explicitly rather than left to fall through. This arm used to be the JavaScript one, so a wasm script on an
		// X12 channel would have been handed to goja and run as JavaScript - a module's bytes evaluated as source, failing
		// with a syntax error that named neither the language nor the real problem.
		var zero M

		return zero, nil, Result{}, fmt.Errorf("a %s script cannot address a %T message: this format is addressed by path, "+
			"which means the script calls get and set on the host, and a module is given the message on stdin instead. "+
			"Write it in lua or javascript, or use a format the whole message is handed to", s.Language(), msg)
	}

	if err != nil {
		return msg, nil, res, err
	}

	return pm.Value, pm.Changed, res, nil
}

// bindPathMessageJS exposes the same three methods to JavaScript.
//
// # Why this is safe to have now when it was refused before
//
// The refusal said a goja binding would be a second implementation of the same path vocabulary and the two would drift, the
// interesting questions being what happens on an ambiguous path and on a write the format neutralises. That was correct about the
// risk and wrong about it being unavoidable: the reason there would have been two answers is that the answers lived inside the Lua
// closures. They are on PathMessage now, so both bindings ask the same code and neither can answer differently.
//
// What is left here is translation, and the two languages differ in exactly two ways. An error becomes a thrown exception rather
// than a raised Lua error, and an absent field becomes null rather than nil. Both are the local spelling of the same decision.
//
// Deliberately no property access. msg.CLM01 is temptingly readable and cannot be made honest: a path is not an identifier, so
// CLM01 would work while 07-D7, #3 and a column named "Patient ID" would not, and half the paths working as properties is worse
// than none of them doing so. Same reason there is no metatable on the Lua side.
func bindPathMessageJS[M any](vm *goja.Runtime, pm *PathMessage[M]) (*goja.Object, error) {
	obj := vm.NewObject()

	if err := obj.Set("get", func(call goja.FunctionCall) goja.Value {
		v, present, err := pm.Get(call.Argument(0).String())
		if err != nil {
			// Thrown rather than returned as undefined, because a mistyped path is a bug in the script and a script that
			// carries on regardless produces a message missing a field, discovered at the receiver. Failing attributes it.
			panic(vm.NewGoError(err))
		}

		if !present {
			// Null rather than undefined or an empty string. Every format here treats absent and blank as different: a
			// blank field was sent deliberately and an absent one was never sent, which for a claim is the difference
			// between "no diagnosis" and "the diagnosis segment is missing".
			//
			// Null rather than undefined specifically because undefined is what a typo in a property name produces, and
			// these two facts should not look identical.
			return goja.Null()
		}

		return vm.ToValue(v)
	}); err != nil {
		return nil, err
	}

	if err := obj.Set("has", func(call goja.FunctionCall) goja.Value {
		present, err := pm.Has(call.Argument(0).String())
		if err != nil {
			panic(vm.NewGoError(err))
		}

		return vm.ToValue(present)
	}); err != nil {
		return nil, err
	}

	if err := obj.Set("set", func(call goja.FunctionCall) goja.Value {
		if err := pm.Set(call.Argument(0).String(), call.Argument(1).String()); err != nil {
			// Thrown so the script stops rather than continuing with a message it thinks it changed.
			panic(vm.NewGoError(err))
		}

		return goja.Undefined()
	}); err != nil {
		return nil, err
	}

	return obj, nil
}
