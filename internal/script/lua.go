package script

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// luaRuntime compiles and runs Lua scripts.
type luaRuntime struct{}

func (luaRuntime) language() Language { return Lua }

// compile parses the source and reports a syntax error with its line.
//
// The source is not wrapped. JavaScript is wrapped in an immediately-invoked function so that a bare return statement is legal,
// because Mirth's filters are written as a bare return and the language does not allow one at the top level. Lua allows return
// at the end of a chunk, so wrapping would add a stack frame to every trace for no benefit.
func (luaRuntime) compile(name, source string, kind Kind) (compiled, error) {
	fn, err := lua.NewState().LoadString(source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, cleanupLuaError(err))
	}
	_ = fn

	return &luaScript{name: name, source: source, kind: kind}, nil
}

// luaScript is one compiled Lua chunk.
//
// # Why the source is kept rather than the compiled function
//
// gopher-lua's compiled function is bound to the state that compiled it, so it cannot be shared across the pooled states the
// engine hands out. Recompiling per run would be wasteful, so what is kept is the source and each state compiles it once on
// first use and caches it - the same shape as goja's Program being compiled once and run on many runtimes, arrived at
// differently because the library allows less sharing.
//
// Stated because a reader comparing the two runtimes will notice the asymmetry and it is a property of the library rather than
// an oversight.
type luaScript struct {
	name   string
	source string
	kind   Kind
}

func (s *luaScript) run(e *Engine, ctx *Context, kind Kind) (Result, error) {
	return s.runWith(e, ctx, kind, nil)
}

// runWith is run with an extra chance to install globals.
//
// The hook exists so that a format addressed by path can replace msg with its own binding, without a second copy of the state
// setup - the sandbox, the timeout and the verdict rules have to be identical for every Lua script or they are not shared
// semantics, they are two implementations that happen to agree today.
func (s *luaScript) runWith(e *Engine, ctx *Context, kind Kind, extra func(*lua.LState)) (Result, error) {
	L := lua.NewState(lua.Options{
		// The registry and call stack are bounded so a runaway script fails rather than exhausting the process. A script is
		// somebody's configuration, not trusted code, and the failure has to be contained to the one message.
		RegistrySize:        1024 * 20,
		CallStackSize:       256,
		IncludeGoStackTrace: false,
		SkipOpenLibs:        true,
	})
	defer L.Close()

	openSafeLibraries(L)
	bindLuaGlobals(L, e, ctx)

	// After the standard globals, so a format binding can replace msg rather than having to be merged with it.
	if extra != nil {
		extra(L)
	}

	// The timeout is enforced through the state's context, which is the mechanism gopher-lua provides: the VM checks for
	// cancellation between instructions, so an infinite loop is interrupted rather than running until the process dies.
	//
	// My first version started a goroutine that called L.Close on expiry. That is wrong twice over - it races with the
	// deferred Close, and closing a state while it is executing is not safe. Recorded because it compiled, and it would have
	// looked correct right up until a script actually timed out under load.
	if e.opts.Timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
		defer cancel()
		L.SetContext(timeoutCtx)
	}

	// The shared library, after every global is installed and before the script runs, so a library sees exactly the environment
	// the script will see and can capture it.
	//
	// This is scripts.include, which existed for JavaScript only. A Lua channel accepted the setting, read the files, refused a
	// missing one - and then the runtime never installed any of it, so every call into the library failed on the first message
	// with a nil global. readIncludes has a comment explaining that it refuses a missing library precisely to avoid that error,
	// which for Lua it produced in every case.
	//
	// It mattered more than it looked when it was found, because the path binding was Lua-only then: X12, NCPDP and delimited
	// could only be scripted in Lua, so the three formats that most need a shared library were the three that could not have one.
	// Both languages reach both bindings now, which removes the compounding but not the reason to have fixed this.
	if e.luaLibrary != nil {
		L.Push(L.NewFunctionFromProto(e.luaLibrary))
		if err := L.PCall(0, 0, nil); err != nil {
			return Result{}, fmt.Errorf("%s: the shared script library failed: %w", s.name, cleanupLuaError(err))
		}
	}

	if err := L.DoString(s.source); err != nil {
		// The message is checked as well as the wrapped error, because gopher-lua reports a cancellation as its own
		// ApiError carrying a stack traceback rather than wrapping the context error, so errors.Is alone does not see it.
		if e.opts.Timeout > 0 && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
			return Result{}, fmt.Errorf("%s: the script did not finish within %s", s.name, e.opts.Timeout)
		}

		return Result{}, fmt.Errorf("%s: %w", s.name, cleanupLuaError(err))
	}

	return luaResult(L, kind, ctx)
}

// luaResult reads what the script produced.
//
// The verdict rules are the shared ones, not Lua's. A filter must produce a boolean, and Lua's truthiness - where 0 and the
// empty string are both true - is deliberately not used, because a filter written in one language must mean the same thing as
// the same filter written in the other. A site with filters in both languages cannot have "return 0" accept in Lua and reject
// in JavaScript.
func luaResult(L *lua.LState, kind Kind, ctx *Context) (Result, error) {
	top := L.GetTop()

	// The *first* returned value, not the last.
	//
	// Lua functions return multiple values and many standard ones do: string.gsub returns the new string and the number of
	// substitutions. A preprocessor written as "return string.gsub(message, a, b)" therefore leaves two values on the stack,
	// and reading the top of it gets the count. The first version of this did exactly that and a test caught it - the
	// preprocessor reported that it had returned a number.
	first := func() lua.LValue {
		if top == 0 {
			return lua.LNil
		}

		return L.Get(-top)
	}

	switch kind {
	case Filter:
		if top == 0 {
			// Same as JavaScript: a filter that returns nothing is a mistake worth naming rather than a silent accept or a
			// silent reject. Either default is wrong half the time and neither is discoverable.
			return Result{}, fmt.Errorf("a filter must return true or false, and this one returned nothing")
		}
		v := first()
		b, ok := v.(lua.LBool)
		if !ok {
			return Result{}, fmt.Errorf("a filter must return true or false, and this one returned %s: Lua treats 0 and \"\" as true, so Perfuse requires an actual boolean rather than guessing which was meant", v.Type())
		}

		return Result{Accept: bool(b)}, nil

	case Preprocessor:
		if top == 0 {
			return Result{}, nil
		}
		str, ok := first().(lua.LString)
		if !ok {
			return Result{}, fmt.Errorf("a preprocessor must return the text to parse, or nothing; this one returned %s", first().Type())
		}

		return Result{Text: string(str), Replaced: true}, nil

	case Reader:
		if top == 0 {
			return Result{}, nil
		}
		tbl, ok := first().(*lua.LTable)
		if !ok {
			// A single string is accepted as well as a table, because returning one message is the common case and
			// wrapping it in braces is the kind of ceremony that produces a confusing runtime error the first time.
			if str, isStr := first().(lua.LString); isStr {
				return Result{Messages: []string{string(str)}}, nil
			}

			return Result{}, fmt.Errorf("a reader must return a table of messages or a single string; this one returned %s", first().Type())
		}

		var out []string
		tbl.ForEach(func(_, v lua.LValue) {
			out = append(out, v.String())
		})

		return Result{Messages: out}, nil
	}

	return Result{}, nil
}

// openSafeLibraries loads the standard libraries a script may use.
//
// # What is left out, and why each one
//
// Enumerated rather than described, because "a safe subset" is not a specification and the next person needs to know exactly
// what was decided:
//
//   - os is omitted entirely. os.execute runs a shell command, os.exit stops the process, os.getenv reads credentials out of
//     the environment, os.remove deletes files. A transformation step has no business with any of it.
//   - io is omitted. A script that can open a file can read the database, the TLS keys, and every other channel's
//     configuration.
//   - package and require are omitted, because loading a module reintroduces everything else on disk.
//   - debug is omitted. debug.getupvalue reaches into closures and can retrieve values the sandbox was holding privately,
//     which makes every other restriction negotiable.
//   - coroutine is omitted. Not dangerous, but a yielding script interacts badly with the timeout - it can suspend, appear
//     finished, and resume outside the deadline.
//
// What is included is string, table, math and the base functions minus the loading ones. That covers what a transformation
// actually does: read a field, reformat it, compare it, build a value.
func openSafeLibraries(L *lua.LState) {
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.StringLibName, lua.OpenString},
		{lua.TabLibName, lua.OpenTable},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}

	// The base library brings loaders with it, so they are removed after opening rather than avoided by not opening base -
	// which would also remove pairs, ipairs, tostring and type.
	//
	// dofile and loadfile read the filesystem. load and loadstring compile a string, which is how a script that is reviewed
	// once runs something different later. collectgarbage is not a security question but lets a script stall the process.
	// require is listed separately from package because removing the package table does not remove it: OpenBase installs
	// require itself, so omitting the package library leaves the loader reachable. A test caught that.
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "collectgarbage", "print", "require", "module", "newproxy"} {
		L.SetGlobal(name, lua.LNil)
	}
}

// bindLuaGlobals gives a Lua script the same view a JavaScript script has.
//
// # What is deliberately absent
//
// The Mirth compatibility surface is not here: no Packages, no java, no javax, no SerializerFactory. Those exist in the
// JavaScript engine to recognise Mirth scripts and refuse them with an explanation, which is a migration aid. A Lua script
// cannot be a migrated Mirth script, so offering shims for a Java API would be inventing a compatibility problem rather than
// solving one.
func bindLuaGlobals(L *lua.LState, e *Engine, ctx *Context) {
	// message is the raw text, msg is the parsed tree. Same names as JavaScript, because somebody reading both should not have
	// to learn two vocabularies for one thing.
	// Nil rather than an empty string when there is no message text, so indexing it fails loudly instead of quietly
	// producing nothing. Set either way, because the state is reused between messages.
	if ctx.RawUnavailable {
		L.SetGlobal("message", lua.LNil)
	} else {
		L.SetGlobal("message", lua.LString(ctx.Raw))
	}

	if ctx.Message != nil {
		L.SetGlobal("msg", luaNode(L, ctx.Message))
	}
	if ctx.Output != nil {
		L.SetGlobal("tmp", luaNode(L, ctx.Output))
	}

	L.SetGlobal("channelName", lua.LString(ctx.ChannelName))

	L.SetGlobal("logger", luaLogger(L, ctx))

	for name, m := range map[string]*SharedMap{
		"channelMap":   ctx.ChannelMap,
		"connectorMap": ctx.ConnectorMap,
		"responseMap":  ctx.ResponseMap,
		"sourceMap":    ctx.SourceMap,
	} {
		if m != nil {
			L.SetGlobal(name, luaSharedMap(L, m))
		}
	}
}

// luaLogger exposes the four levels as functions.
func luaLogger(L *lua.LState, ctx *Context) *lua.LTable {
	t := L.NewTable()
	for _, level := range []string{"debug", "info", "warn", "error"} {
		lvl := level
		L.SetField(t, lvl, L.NewFunction(func(inner *lua.LState) int {
			ctx.log(lvl, inner.CheckString(1))

			return 0
		}))
	}

	return t
}

// luaSharedMap wraps a SharedMap as a table with get, put and remove.
//
// Methods rather than metatable indexing, because a metatable that intercepted reads would make map.foo look like a field and
// then behave differently from one - assignment would not persist, and the failure would be invisible.
func luaSharedMap(L *lua.LState, m *SharedMap) *lua.LTable {
	t := L.NewTable()

	L.SetField(t, "get", L.NewFunction(func(inner *lua.LState) int {
		v, ok := m.Get(inner.CheckString(1))
		if !ok {
			inner.Push(lua.LNil)

			return 1
		}
		inner.Push(lua.LString(fmt.Sprint(v)))

		return 1
	}))

	L.SetField(t, "put", L.NewFunction(func(inner *lua.LState) int {
		m.Put(inner.CheckString(1), inner.CheckAny(2).String())

		return 0
	}))

	L.SetField(t, "remove", L.NewFunction(func(inner *lua.LState) int {
		inner.Push(lua.LBool(m.Remove(inner.CheckString(1))))

		return 1
	}))

	return t
}

// luaNode exposes a message tree, readable and writable.
//
// # Why the accessors are functions and not fields
//
// The first version of this set name and text as table values. That is a trap the moment writing exists: after setText the
// stale copy in the table still holds the old value, so a script that writes a field and then reads it back gets what it
// replaced. Nothing errors, and the script looks correct.
//
// So everything is a function. node.text() asks the node, every time.
//
// # Why there is no metatable
//
// A metatable could make node.text look like a field and intercept assignment, which reads more naturally. It also makes
// node.txet silently nil instead of an error, and makes it impossible to tell a typo from an absent element. For a language
// whose whole job here is editing clinical messages, a loud mistake beats a convenient one.
func luaNode(L *lua.LState, n *xtree.Node) *lua.LTable {
	t := L.NewTable()

	L.SetField(t, "name", L.NewFunction(func(inner *lua.LState) int {
		inner.Push(lua.LString(n.Name))

		return 1
	}))

	L.SetField(t, "text", L.NewFunction(func(inner *lua.LState) int {
		inner.Push(lua.LString(n.Text))

		return 1
	}))

	L.SetField(t, "setText", L.NewFunction(func(inner *lua.LState) int {
		n.Text = inner.CheckString(1)

		return 0
	}))

	L.SetField(t, "attr", L.NewFunction(func(inner *lua.LState) int {
		v, ok := n.Attr(inner.CheckString(1))
		if !ok {
			// Nil rather than an empty string, because absent and empty are different facts in XML and a v3 element with
			// no value is not the same as one carrying "". Collapsing them is the mistake the filter grammar had for
			// months.
			inner.Push(lua.LNil)

			return 1
		}
		inner.Push(lua.LString(v))

		return 1
	}))

	L.SetField(t, "setAttr", L.NewFunction(func(inner *lua.LState) int {
		n.SetAttr(inner.CheckString(1), inner.CheckString(2))

		return 0
	}))

	L.SetField(t, "removeAttr", L.NewFunction(func(inner *lua.LState) int {
		inner.Push(lua.LBool(n.RemoveAttr(inner.CheckString(1))))

		return 1
	}))

	L.SetField(t, "child", L.NewFunction(func(inner *lua.LState) int {
		name := inner.CheckString(1)

		// An optional one-based index, because a message repeats elements and "the second id" is a thing scripts need to
		// say. One-based to match the filter paths and every conversation about a message; zero-based would be right for a
		// programmer and wrong for everybody describing the problem.
		index := 1
		if inner.GetTop() >= 2 {
			index = inner.CheckInt(2)
		}
		if index < 1 {
			inner.RaiseError("child index starts at 1, not %d", index)

			return 0
		}

		found := n.Child(name, index-1)
		if found == nil {
			inner.Push(lua.LNil)

			return 1
		}
		inner.Push(luaNode(inner, found))

		return 1
	}))

	L.SetField(t, "children", L.NewFunction(func(inner *lua.LState) int {
		name := ""
		if inner.GetTop() >= 1 {
			name = inner.CheckString(1)
		}

		list := inner.NewTable()
		for _, c := range n.Children {
			if name != "" && c.Name != name {
				continue
			}
			list.Append(luaNode(inner, c))
		}
		inner.Push(list)

		return 1
	}))

	// ensure is the write counterpart of child: it returns the element, creating it if absent.
	//
	// Separate from child rather than child growing a create flag, because the two have opposite failure modes. Reading a
	// missing element should answer nil; writing to one should make it exist. A single function doing both would silently
	// create elements during what the author thought was a read.
	L.SetField(t, "ensure", L.NewFunction(func(inner *lua.LState) int {
		name := inner.CheckString(1)

		index := 1
		if inner.GetTop() >= 2 {
			index = inner.CheckInt(2)
		}
		if index < 1 {
			inner.RaiseError("child index starts at 1, not %d", index)

			return 0
		}

		inner.Push(luaNode(inner, n.Ensure(name, index-1)))

		return 1
	}))

	L.SetField(t, "append", L.NewFunction(func(inner *lua.LState) int {
		child := xtree.New(inner.CheckString(1))
		if inner.GetTop() >= 2 {
			child.Text = inner.CheckString(2)
		}
		n.Append(child)
		inner.Push(luaNode(inner, child))

		return 1
	}))

	L.SetField(t, "remove", L.NewFunction(func(inner *lua.LState) int {
		inner.Push(lua.LNumber(n.RemoveName(inner.CheckString(1))))

		return 1
	}))

	return t
}

// cleanupLuaError makes a Lua error readable.
//
// gopher-lua prefixes its messages with the chunk name it invented, which for a string chunk is the whole source. That produces
// an error line hundreds of characters long with the useful part at the end.
func cleanupLuaError(err error) error {
	msg := err.Error()

	if i := strings.Index(msg, "at line"); i > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(msg[i:]))
	}

	// A leading "<string>:" is noise: the caller already knows the script's name and prints it.
	msg = strings.TrimPrefix(msg, "<string>:")

	return fmt.Errorf("%s", strings.TrimSpace(msg))
}

// runLuaWith runs a Lua script with an extra global installer.
//
// Here rather than on Engine because it is the bridge for the path formats and belongs beside them conceptually, and because
// exporting a method taking a *lua.LState would put the Lua library in this package's public surface.
func runLuaWith(e *Engine, s *Script, ctx *Context, extra func(*lua.LState)) (Result, error) {
	ls, ok := s.compiled.(*luaScript)
	if !ok {
		return Result{}, fmt.Errorf("%s is not a Lua script", s.Name)
	}

	if ctx == nil {
		ctx = &Context{}
	}

	// The engine owns the logs and the duration, exactly as Run does. A second path collecting its own would eventually
	// report a duration excluding its own setup.
	started := time.Now()
	var logs []LogLine
	inner := ctx.Log
	ctx.Log = func(level, message string) {
		logs = append(logs, LogLine{Level: level, Message: message})
		if inner != nil {
			inner(level, message)
		}
	}
	defer func() { ctx.Log = inner }()

	res, err := ls.runWith(e, ctx, s.kind, extra)
	res.Logs = logs
	res.Duration = time.Since(started)

	return res, err
}

// compileLuaLibrary compiles shared library source without running it.
//
// Separate from running it so that a syntax error is reported when the channel loads rather than when the first message arrives.
// That is the same reason readIncludes refuses a missing file at load: an error naming the library is a diagnosis, and an
// undefined global on message one is a puzzle.
//
// A throwaway state is used for the compile because gopher-lua exposes compilation through one. The proto it produces outlives the
// state - protos are immutable and carry no state reference - so it can be installed into every later state, which is what makes
// one compile enough for a runtime that builds a fresh state per message.
func compileLuaLibrary(src string) (*lua.FunctionProto, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()

	fn, err := L.LoadString(src)
	if err != nil {
		return nil, cleanupLuaError(err)
	}

	return fn.Proto, nil
}
