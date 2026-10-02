package script

import (
	"strings"
	"testing"
	"time"
)

func luaEngine(t *testing.T) *Engine {
	t.Helper()

	return New(Options{Timeout: 2 * time.Second})
}

func runLua(t *testing.T, source string, kind Kind, ctx *Context) (Result, error) {
	t.Helper()

	e := luaEngine(t)
	s, err := e.CompileIn("test.lua", source, kind, Lua)
	if err != nil {
		return Result{}, err
	}
	if ctx == nil {
		ctx = &Context{}
	}

	return e.Run(s, ctx)
}

func TestALuaFilterReturnsItsVerdict(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want bool
	}{
		{`return true`, true},
		{`return false`, false},
		{`return 1 == 1`, true},
		{`return string.find(message, "ADT") ~= nil`, true},
	} {
		res, err := runLua(t, tc.src, Filter, &Context{Raw: "MSH|^~\\&|LAB|H|||20260830||ADT^A08|1|P|2.5"})
		if err != nil {
			t.Errorf("%s: %v", tc.src, err)

			continue
		}
		if res.Accept != tc.want {
			t.Errorf("%s accepted=%v, want %v", tc.src, res.Accept, tc.want)
		}
	}
}

// TestLuaTruthinessIsNotUsedForAFilterVerdict is the parity property, and it is the reason Result is shared.
//
// Lua treats 0 and the empty string as true. JavaScript treats both as false. A site with filters in both languages cannot have
// "return 0" accept in one and reject in the other, so Perfuse requires an actual boolean and says which values are ambiguous.
func TestLuaTruthinessIsNotUsedForAFilterVerdict(t *testing.T) {
	for _, src := range []string{`return 0`, `return ""`, `return {}`, `return "yes"`} {
		_, err := runLua(t, src, Filter, nil)
		if err == nil {
			t.Errorf("%s was accepted as a filter verdict; Lua and JavaScript disagree about its truthiness, so it must be refused rather than guessed", src)

			continue
		}
		if !strings.Contains(err.Error(), "true or false") {
			t.Errorf("%s: the error should say a boolean is required, got: %v", src, err)
		}
	}
}

// TestALuaFilterReturningNothingIsAnError matches the JavaScript rule.
//
// Either default is wrong half the time and neither is discoverable from the outside: a silent accept delivers what should have
// been excluded, a silent reject drops everything.
func TestALuaFilterReturningNothingIsAnError(t *testing.T) {
	_, err := runLua(t, `local x = 1`, Filter, nil)
	if err == nil {
		t.Fatal("a filter that returned nothing was accepted")
	}
}

func TestALuaPreprocessorReplacesTheText(t *testing.T) {
	res, err := runLua(t, `return string.gsub(message, "OLD", "NEW")`, Preprocessor, &Context{Raw: "an OLD message"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced {
		t.Fatal("Replaced is false, so the returned text would be ignored")
	}
	if res.Text != "an NEW message" {
		t.Errorf("Text = %q", res.Text)
	}
}

// TestALuaPreprocessorReturningNothingIsNotAReplacement is the distinction Replaced exists for.
//
// An empty string returned deliberately and a script that returned nothing are different instructions, and one of them means
// discard the message.
func TestALuaPreprocessorReturningNothingIsNotAReplacement(t *testing.T) {
	res, err := runLua(t, `local x = 1`, Preprocessor, &Context{Raw: "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced {
		t.Error("Replaced is true for a script that returned nothing, so returning nothing and returning an empty string are being conflated")
	}
}

func TestALuaReaderReturnsMessages(t *testing.T) {
	res, err := runLua(t, `return {"one", "two", "three"}`, Reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("got %d message(s): %v", len(res.Messages), res.Messages)
	}
}

// TestALuaReaderMayReturnASingleString saves the ceremony of wrapping one message in a table.
func TestALuaReaderMayReturnASingleString(t *testing.T) {
	res, err := runLua(t, `return "only one"`, Reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0] != "only one" {
		t.Errorf("got %v", res.Messages)
	}
}

func TestALuaScriptCanLog(t *testing.T) {
	res, err := runLua(t, `logger.info("hello"); logger.warn("careful"); return true`, Filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logs) != 2 {
		t.Fatalf("got %d log line(s): %+v", len(res.Logs), res.Logs)
	}
	if res.Logs[0].Level != "info" || res.Logs[0].Message != "hello" {
		t.Errorf("first line is %+v", res.Logs[0])
	}
}

func TestALuaScriptCanUseTheChannelMap(t *testing.T) {
	m := NewSharedMap()
	res, err := runLua(t, `channelMap.put("seen", "yes"); return channelMap.get("seen") == "yes"`, Filter, &Context{ChannelMap: m})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accept {
		t.Error("the script could not read back what it wrote to the channel map")
	}

	// The value has to survive into Go, not just within the script, because that is what makes the map useful between the
	// filter and the transformer.
	if v, ok := m.Get("seen"); !ok || v != "yes" {
		t.Errorf("the map holds %v, %v after the script wrote to it", v, ok)
	}
}

// TestTheDangerousLuaLibrariesAreAbsent enumerates the sandbox rather than describing it.
//
// "A safe subset" is not a specification. Each of these is named because the next person needs to know exactly what was
// decided, and because a library reappearing - through a library upgrade changing what OpenBase installs, for instance - would
// otherwise be silent.
func TestTheDangerousLuaLibrariesAreAbsent(t *testing.T) {
	for name, why := range map[string]string{
		"os":             "os.execute runs a shell command and os.getenv reads credentials out of the environment",
		"io":             "a script that can open a file can read the database and the TLS keys",
		"package":        "loading a module reintroduces everything else on disk",
		"require":        "same as package",
		"debug":          "debug.getupvalue reaches into closures and makes every other restriction negotiable",
		"coroutine":      "a yielding script can suspend, appear finished, and resume outside the timeout",
		"dofile":         "reads the filesystem",
		"loadfile":       "reads the filesystem",
		"load":           "compiles a string, so a script reviewed once can run something different later",
		"loadstring":     "same as load",
		"collectgarbage": "lets a script stall the process",
	} {
		res, err := runLua(t, `return `+name+` == nil`, Filter, nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		if !res.Accept {
			t.Errorf("%s is reachable from a Lua script, and it should not be: %s", name, why)
		}
	}
}

// TestTheUsefulLuaLibrariesArePresent is the other half.
//
// A sandbox that removed these would be safe and useless: reading a field, reformatting it and comparing it is the entire job.
func TestTheUsefulLuaLibrariesArePresent(t *testing.T) {
	for _, expr := range []string{
		`string.upper("a") == "A"`,
		`table.concat({"a","b"}, ",") == "a,b"`,
		`math.floor(1.7) == 1`,
		`tostring(1) == "1"`,
		`type({}) == "table"`,
		`tonumber("42") == 42`,
		`select("#", 1, 2) == 2`,
	} {
		res, err := runLua(t, `return `+expr, Filter, nil)
		if err != nil {
			t.Errorf("%s: %v", expr, err)

			continue
		}
		if !res.Accept {
			t.Errorf("%s was false, so something a transformation needs is missing from the sandbox", expr)
		}
	}
}

// TestALuaScriptThatLoopsForeverIsInterrupted is the availability property.
//
// Without it, one bad filter stops the channel, and a channel that stops on one message is an outage. This also covers the bug I
// wrote first: the original implementation called L.Close from a timer goroutine, which races with the deferred close and is not
// safe while the state is executing. It compiled, and it would have looked correct until a script actually timed out.
func TestALuaScriptThatLoopsForeverIsInterrupted(t *testing.T) {
	e := New(Options{Timeout: 150 * time.Millisecond})
	s, err := e.CompileIn("loop.lua", `while true do end`, Filter, Lua)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := e.Run(s, &Context{})
		done <- runErr
	}()

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("an infinite loop completed successfully")
		}
		if !strings.Contains(runErr.Error(), "did not finish") {
			t.Errorf("the error should say the script timed out, got: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the script was not interrupted, so a runaway filter would stop the channel indefinitely")
	}
}

func TestALuaSyntaxErrorIsReportedAtCompileTime(t *testing.T) {
	e := luaEngine(t)
	_, err := e.CompileIn("bad.lua", `if then end`, Filter, Lua)
	if err == nil {
		t.Fatal("invalid Lua compiled successfully")
	}

	// The whole source must not be echoed into the message. gopher-lua names a string chunk after its contents, which for a
	// real script produces an error line hundreds of characters long with the useful part at the end.
	if len(err.Error()) > 200 {
		t.Errorf("the error is %d characters, which suggests the source is being echoed: %v", len(err.Error()), err)
	}
}

func TestParseLanguage(t *testing.T) {
	for in, want := range map[string]Language{
		"":           JavaScript,
		"javascript": JavaScript,
		"JavaScript": JavaScript,
		"js":         JavaScript,
		"lua":        Lua,
		"LUA":        Lua,
		" lua ":      Lua,
	} {
		got, err := ParseLanguage(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)

			continue
		}
		if got != want {
			t.Errorf("%q parsed to %q, want %q", in, got, want)
		}
	}

	if _, err := ParseLanguage("python"); err == nil {
		t.Error("an unsupported language was accepted")
	}
}

// TestAnUnmarkedScriptIsJavaScript is why the default is what it is.
//
// Every script written before this existed is JavaScript and says nothing about its language. Making Lua the default, or
// requiring the field, would reinterpret or refuse all of them.
func TestAnUnmarkedScriptIsJavaScript(t *testing.T) {
	e := luaEngine(t)

	// Syntax that is valid JavaScript and invalid Lua. If the default were Lua this would fail to compile.
	s, err := e.Compile("unmarked.js", `var x = 1; return x === 1;`, Filter)
	if err != nil {
		t.Fatalf("an unmarked script did not compile as JavaScript: %v", err)
	}
	if s.Language() != JavaScript {
		t.Errorf("an unmarked script reports language %q", s.Language())
	}
}

// TestTheMirthShimsAreNotOfferedToLua records a deliberate absence.
//
// Packages, java and javax exist in the JavaScript engine to recognise migrated Mirth scripts and refuse them with an
// explanation. A Lua script cannot be a migrated Mirth script, so offering shims for a Java API would invent a compatibility
// problem rather than solve one.
func TestTheMirthShimsAreNotOfferedToLua(t *testing.T) {
	for _, name := range []string{"Packages", "java", "javax", "SerializerFactory"} {
		res, err := runLua(t, `return `+name+` == nil`, Filter, nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		if !res.Accept {
			t.Errorf("%s is bound for Lua, which invents a Java compatibility problem where there was none", name)
		}
	}
}
