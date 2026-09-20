package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/transform"
)

// Shared script libraries. The reason to have them is arithmetic: a site with forty channels usually
// has one library every channel calls, and copying it into forty files is forty places to fix.

func channelWithScripts(t *testing.T, dir string, scripts *Scripts) *Channel {
	t.Helper()

	c := &Channel{
		Name:    "using-a-library",
		Source:  Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Scripts: scripts,
		Destinations: []Destination{
			{Name: "out", Type: DestinationFile, Dir: t.TempDir()},
		},
		Transformations: []transform.Step{{Trim: &transform.TrimStep{Path: "PID-5.1"}}},
	}
	c.path = filepath.Join(dir, "channel.yaml")
	return c
}

func writeLibrary(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAScriptCanCallIntoASharedLibrary(t *testing.T) {
	dir := t.TempDir()
	writeLibrary(t, dir, "lib.js", `function padMRN(value) { return '00' + value; }`)

	c := channelWithScripts(t, dir, &Scripts{
		Include:     []string{"lib.js"},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = padMRN('123');`,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("a channel using a shared library did not load: %v", err)
	}
	if c.Scripts.TransformerScript() == nil {
		t.Fatal("the transformer was not compiled")
	}
	// The engine is what carries the library, and creating a runtime is what proves it runs.
	if c.Scripts.Engine() == nil {
		t.Fatal("no script engine was created")
	}
}

func TestAMissingLibraryIsRefusedAtLoad(t *testing.T) {
	// Skipping it would mean every script calling into the library fails with "undefined is not a
	// function" pointing at the wrong file, which is far harder to diagnose than a named missing file.
	dir := t.TempDir()

	c := channelWithScripts(t, dir, &Scripts{
		Include:     []string{"not-here.js"},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = 'x';`,
	})

	err := c.Validate()
	if err == nil {
		t.Fatal("a channel naming a library that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "not-here.js") {
		t.Errorf("the error does not name the missing file: %v", err)
	}
}

func TestALibraryThatDoesNotCompileIsReported(t *testing.T) {
	dir := t.TempDir()
	writeLibrary(t, dir, "broken.js", `function oops( {`)

	c := channelWithScripts(t, dir, &Scripts{
		Include:     []string{"broken.js"},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = 'x';`,
	})

	// Compilation of the library is deferred to the first runtime, so validation may pass and the
	// failure appears when a runtime is created. Either way it must be reported, and it must name the
	// library rather than the channel's own script.
	err := c.Validate()
	if err == nil {
		engine := c.Scripts.Engine()
		if engine == nil {
			t.Fatal("no engine, so the library error can never surface")
		}
		_, err = engine.Run(c.Scripts.TransformerScript(), nil)
	}
	if err == nil {
		t.Fatal("a library that does not compile was accepted silently")
	}
	if !strings.Contains(err.Error(), "broken.js") {
		t.Errorf("the error does not name the library: %v", err)
	}
}

func TestSeveralLibrariesLoadInTheOrderGiven(t *testing.T) {
	// A library may depend on one listed before it, and reordering somebody's list to suit an
	// implementation detail would be surprising.
	dir := t.TempDir()
	writeLibrary(t, dir, "a.js", `function base() { return 'a'; }`)
	writeLibrary(t, dir, "b.js", `function derived() { return base() + 'b'; }`)

	c := channelWithScripts(t, dir, &Scripts{
		Include:     []string{"a.js", "b.js"},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = derived();`,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("two libraries where the second calls the first did not load: %v", err)
	}
}

func TestALibraryWithoutATrailingNewlineDoesNotEatTheNextFile(t *testing.T) {
	dir := t.TempDir()
	// No trailing newline, which is easy to produce and would otherwise comment out or merge into the
	// first line of whatever follows.
	writeLibrary(t, dir, "first.js", `function one() { return 1; }`)
	writeLibrary(t, dir, "second.js", `function two() { return one() + 1; }`)

	c := channelWithScripts(t, dir, &Scripts{
		Include:     []string{"first.js", "second.js"},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = String(two());`,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("a library with no trailing newline broke the next one: %v", err)
	}
}

func TestAChannelWithOnlyALibraryAndNoScriptsIsNotConsideredScripted(t *testing.T) {
	// An include on its own does nothing: there is no script to call into it. Treating that as scripted
	// would attach a script engine to a channel that never runs one, and would put a "this channel has
	// scripts" caveat on its specification for no reason.
	dir := t.TempDir()
	writeLibrary(t, dir, "lib.js", `function unused() { return 1; }`)

	scripts := &Scripts{Include: []string{"lib.js"}}
	if !scripts.Empty() {
		t.Error("a channel with an include but no scripts reports as scripted")
	}
}

func TestAnAbsoluteIncludePathIsUsedAsGiven(t *testing.T) {
	// A site keeping its libraries in one place should not have to write a relative path from every
	// channel directory.
	libDir := t.TempDir()
	writeLibrary(t, libDir, "shared.js", `function shared() { return 'yes'; }`)

	c := channelWithScripts(t, t.TempDir(), &Scripts{
		Include:     []string{filepath.Join(libDir, "shared.js")},
		Transformer: `msg['PID']['PID.3']['PID.3.1'] = shared();`,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("an absolute include path was not honoured: %v", err)
	}
}
