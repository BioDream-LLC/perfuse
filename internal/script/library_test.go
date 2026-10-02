package script

import (
	"strings"
	"testing"
)

// The shared script library, for both languages.
//
// # What was wrong
//
// scripts.include was JavaScript-only, and silently. A Lua channel accepted the setting, the config layer read the files and
// refused a missing one, the concatenated source was handed to the engine as Options.Library - and the Lua runtime never looked at
// it. Every call into the library failed on the first message with a nil global.
//
// readIncludes carries a comment saying it refuses a missing file at load precisely so that nobody has to diagnose that error. For
// Lua it produced that error in every case, including when the file was present.
//
// Two things made it invisible. The runtime simply had no library support, so there was nothing to go wrong in an obvious way. And
// the engine did not know which language it was building for, so it compiled every library as JavaScript: a Lua library recorded a
// JavaScript syntax error in libraryErr, which only the JavaScript path ever read. A wrong answer filed where nobody would look.
//
// So the tests below assert on a script *calling* into the library rather than on the library loading, because loading was never
// the part that failed.

// TestALuaScriptCanCallItsSharedLibrary is the one that would have caught it.
func TestALuaScriptCanCallItsSharedLibrary(t *testing.T) {
	e := New(Options{
		Language:    Lua,
		Library:     "function doubled(n) return n * 2 end\n",
		LibraryName: "shared.lua",
	})

	s, err := e.CompileIn("filter", "return doubled(21) == 42", Filter, Lua)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	res, err := e.Run(s, &Context{Raw: "irrelevant"})
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if !res.Accept {
		t.Error("the filter did not accept, so the library function was not reachable from the script")
	}
}

// TestALuaLibraryWithASyntaxErrorIsReportedAtCompileTime keeps the diagnosis where it is useful.
//
// The whole argument for refusing a missing include at load is that an error naming the library beats an undefined global on
// message one. A library that parses but is not installed until first use would give up that argument.
func TestALuaLibraryWithASyntaxErrorIsReportedAtCompileTime(t *testing.T) {
	e := New(Options{
		Language:    Lua,
		Library:     "function broken( return end\n",
		LibraryName: "shared.lua",
	})

	_, err := e.CompileIn("filter", "return true", Filter, Lua)
	if err == nil {
		t.Fatal("a library that does not parse was accepted; the failure would surface on the first message instead")
	}
	if !strings.Contains(err.Error(), "shared.lua") {
		t.Errorf("the error should name the library, got: %v", err)
	}
}

// TestALuaLibraryThatThrowsOnLoadFailsTheMessageRatherThanTheChannel separates the two failure times.
//
// A library can parse and still fail when it runs - calling something absent at the top level, for instance. That cannot be caught
// at compile time, so it has to fail the message with an error naming the library rather than being swallowed into a confusing
// error about the script.
func TestALuaLibraryThatThrowsOnLoadFailsTheMessageRatherThanTheChannel(t *testing.T) {
	e := New(Options{
		Language:    Lua,
		Library:     "nosuchfunction()\n",
		LibraryName: "shared.lua",
	})

	s, err := e.CompileIn("filter", "return true", Filter, Lua)
	if err != nil {
		t.Fatalf("a library that parses should compile; the failure belongs at run time: %v", err)
	}

	_, err = e.Run(s, &Context{Raw: "irrelevant"})
	if err == nil {
		t.Fatal("a library that failed on load did not fail the message")
	}
	if !strings.Contains(err.Error(), "library") {
		t.Errorf("the error should say the library was what failed, got: %v", err)
	}
}

// TestTheLibraryIsCompiledForTheLanguageItBelongsTo is the underlying fix.
//
// Before Options.Language existed, every library went through the JavaScript compiler. A Lua library is not valid JavaScript, so
// this pairing is what used to record an error nothing read.
func TestTheLibraryIsCompiledForTheLanguageItBelongsTo(t *testing.T) {
	// Valid Lua, invalid JavaScript.
	const luaOnly = "function helper() return 1 end\n"

	lua := New(Options{Language: Lua, Library: luaOnly, LibraryName: "shared.lua"})
	if _, err := lua.CompileIn("filter", "return helper() == 1", Filter, Lua); err != nil {
		t.Errorf("a Lua library was rejected by a Lua engine: %v", err)
	}

	// Valid JavaScript, invalid Lua. The default language is JavaScript, so this also covers the zero value meaning what every
	// existing caller meant.
	const jsOnly = "function helper() { return 1; }\n"

	js := New(Options{Library: jsOnly, LibraryName: "shared.js"})
	if _, err := js.CompileIn("filter", "helper() === 1", Filter, JavaScript); err != nil {
		t.Errorf("a JavaScript library was rejected by a JavaScript engine: %v", err)
	}
}

// TestAJavaScriptLibraryStillWorks guards against fixing Lua by breaking the language that already had this.
func TestAJavaScriptLibraryStillWorks(t *testing.T) {
	e := New(Options{
		Library:     "function doubled(n) { return n * 2; }\n",
		LibraryName: "shared.js",
	})

	s, err := e.CompileIn("filter", "doubled(21) === 42", Filter, JavaScript)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	res, err := e.Run(s, &Context{Raw: "irrelevant"})
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if !res.Accept {
		t.Error("the JavaScript filter did not accept, so its library was not reachable")
	}
}
