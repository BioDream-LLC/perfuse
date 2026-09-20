package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/script"
)

// scripts.include reaching the runtime that will use it.
//
// The unit tests in internal/script construct an engine directly, which skips the part that was actually wrong here: the config
// layer built the engine without telling it which language the library belonged to, so every library was compiled as JavaScript.
// A Lua library therefore recorded a JavaScript syntax error, which only the JavaScript path read.
//
// So this loads a real channel file with a real library beside it and runs the filter, because that is the path a site takes.

// loadWithLibrary writes a channel and its library to a temp directory and loads it.
func loadWithLibrary(t *testing.T, language, library, filter string) *Channel {
	t.Helper()

	dir := t.TempDir()

	ext := "js"
	if language == "lua" {
		ext = "lua"
	}
	libName := "shared." + ext

	if err := os.WriteFile(filepath.Join(dir, libName), []byte(library), 0o600); err != nil {
		t.Fatal(err)
	}

	body := "name: lib-channel\ndataType: hl7\nscripts:\n  language: " + language + "\n" +
		"  include:\n    - " + libName + "\n" +
		"  filter: |\n    " + filter + "\n" +
		"source:\n  type: http\n  http:\n    listen: \"127.0.0.1:0\"\n    path: /in\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp/libchannel\n"

	// Loaded with a path inside the directory, because include paths are resolved relative to the channel file so that a
	// library sitting beside the channels travels with them.
	cfg, err := Load(strings.NewReader(body), filepath.Join(dir, "channel.yaml"))
	if err != nil {
		t.Fatalf("loading a channel with a %s library: %v", language, err)
	}

	return cfg
}

// TestAnIncludedLuaLibraryIsReachableFromAChannelsFilter is the end-to-end version of the defect.
func TestAnIncludedLuaLibraryIsReachableFromAChannelsFilter(t *testing.T) {
	cfg := loadWithLibrary(t, "lua",
		"function keepIt() return true end\n",
		"return keepIt()")

	engine := cfg.ScriptEngine()
	if engine == nil {
		t.Fatal("the channel has scripts but no engine")
	}

	filter := cfg.FilterScript()
	if filter == nil {
		t.Fatal("the channel has a filter script but none was compiled")
	}

	res, err := engine.Run(filter, &script.Context{Raw: "MSH|^~\\&|"})
	if err != nil {
		t.Fatalf("running the filter: %v", err)
	}
	if !res.Accept {
		t.Error("the filter did not accept, so scripts.include did not reach the Lua runtime")
	}
}

// TestAnIncludedJavaScriptLibraryIsStillReachable is the regression half.
//
// JavaScript already had this. A change that gave Lua includes by breaking the language that worked would be a poor trade, and
// nothing else in this package would notice.
func TestAnIncludedJavaScriptLibraryIsStillReachable(t *testing.T) {
	cfg := loadWithLibrary(t, "javascript",
		"function keepIt() { return true; }\n",
		"keepIt()")

	engine := cfg.ScriptEngine()
	if engine == nil {
		t.Fatal("the channel has scripts but no engine")
	}

	filter := cfg.FilterScript()
	if filter == nil {
		t.Fatal("the channel has a filter script but none was compiled")
	}

	res, err := engine.Run(filter, &script.Context{Raw: "MSH|^~\\&|"})
	if err != nil {
		t.Fatalf("running the filter: %v", err)
	}
	if !res.Accept {
		t.Error("the filter did not accept, so scripts.include no longer reaches the JavaScript runtime")
	}
}

// TestALuaChannelWithABrokenLibraryIsRefusedAtLoad keeps the diagnosis at load.
//
// This is the property readIncludes already argued for when it refused a missing file: an error naming the library beats an
// undefined global on the first message. A library that parses as neither language would previously load without complaint on a Lua
// channel, because the error was recorded where the Lua path did not look.
func TestALuaChannelWithABrokenLibraryIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "shared.lua"), []byte("function broken( return end\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := `name: lib-broken
dataType: hl7
scripts:
  language: lua
  include:
    - shared.lua
  filter: |
    return true
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/libbroken
`

	_, err := Load(strings.NewReader(body), filepath.Join(dir, "channel.yaml"))
	if err == nil {
		t.Fatal("a channel whose shared library does not parse was accepted; every message would fail instead")
	}
	if !strings.Contains(err.Error(), "shared.lua") {
		t.Errorf("the refusal should name the library so it is clear which file to fix, got: %v", err)
	}
}
