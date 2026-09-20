package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/script"
)

// A channel whose scripts are WebAssembly modules.
//
// # What is being tested
//
// The runtime is covered in internal/script. What only this level can show is that a channel file naming a module reaches it: the
// field holds a path rather than source, because a module is compiled output and two megabytes of it cannot be a string in YAML.
//
// That overloading is the thing worth guarding. scripts.filter means source on a Lua channel and a path on a wasm one, which is
// exactly the shape this project treats as a defect elsewhere - a field meaning different things depending on another field. It is
// acceptable here only because scripts.language is mandatory to reach it and sits beside it, and because the loader refuses anything
// that is not a readable module. The tests below are that refusal.

// buildModule compiles a wasip1 module into dir and returns its filename.
func buildModule(t *testing.T, dir, source string) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain, so a WebAssembly module cannot be built")
	}

	src := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module wasmfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "filter.wasm")

	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=")

	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a wasip1 module on this machine: %v\n%s", err, combined)
	}

	return "filter.wasm"
}

// TestAChannelCanNameAWebAssemblyModule is the reachability test.
func TestAChannelCanNameAWebAssemblyModule(t *testing.T) {
	dir := t.TempDir()

	name := buildModule(t, dir, `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	if strings.Contains(string(in), "OKONKWO") {
		os.Stdout.WriteString("true")

		return
	}
	os.Stdout.WriteString("false")
}
`)

	body := `name: wasm-channel
dataType: hl7
scripts:
  language: wasm
  # Left at the default deliberately. The module is compiled when the channel loads, not on its first message,
  # so the five seconds covers only running it - and if compilation ever moves back into the message path this
  # test fails under -race, which is where that regression should surface.
  filter: ` + name + `
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/wasmchannel
`

	cfg, err := Load(strings.NewReader(body), filepath.Join(dir, "channel.yaml"))
	if err != nil {
		t.Fatalf("loading a channel with a wasm filter: %v", err)
	}

	engine := cfg.ScriptEngine()
	filter := cfg.FilterScript()
	if engine == nil || filter == nil {
		t.Fatal("the channel has a wasm filter but nothing was compiled")
	}

	// Run it, because a compiled module that is never executed is the failure this project keeps finding.
	res, err := engine.Run(filter, &script.Context{Raw: "PID|1||MRN1||OKONKWO^ADAEZE"})
	if err != nil {
		t.Fatalf("running the module: %v", err)
	}
	if !res.Accept {
		t.Error("the module said to keep this message and the engine did not")
	}

	rejected, err := engine.Run(filter, &script.Context{Raw: "PID|1||MRN2||NAKAMURA^KENJI"})
	if err != nil {
		t.Fatalf("running the module on a second message: %v", err)
	}
	if rejected.Accept {
		t.Error("the module said to drop this message and the engine kept it; a filter that always accepts is " +
			"indistinguishable from one that never ran")
	}
}

// TestAMissingModuleIsRefusedAtLoad keeps the diagnosis where the operator is watching.
func TestAMissingModuleIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()

	body := `name: wasm-missing
dataType: hl7
scripts:
  language: wasm
  filter: nosuch.wasm
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/wasmchannel
`

	_, err := Load(strings.NewReader(body), filepath.Join(dir, "channel.yaml"))
	if err == nil {
		t.Fatal("a channel naming a module that does not exist was accepted; every message would fail instead")
	}
	if !strings.Contains(err.Error(), "nosuch.wasm") {
		t.Errorf("the refusal should name the file so it is clear what to fix, got: %v", err)
	}
}

// TestAScriptLeftInAWasmChannelIsRefused is what makes the overloaded field honest.
//
// The likely mistake is changing language to wasm and leaving the Lua behind. Without this the loader would try to read a file named
// "return true" and report a confusing path error, or worse accept it.
func TestAScriptLeftInAWasmChannelIsRefused(t *testing.T) {
	dir := t.TempDir()

	// A file that exists and is not a module, so the failure cannot be a missing path.
	if err := os.WriteFile(filepath.Join(dir, "filter.wasm"), []byte("return true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := `name: wasm-not-a-module
dataType: hl7
scripts:
  language: wasm
  filter: filter.wasm
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: file
    dir: /tmp/wasmchannel
`

	_, err := Load(strings.NewReader(body), filepath.Join(dir, "channel.yaml"))
	if err == nil {
		t.Fatal("a file that is not a WebAssembly module was accepted as one")
	}
	if !strings.Contains(err.Error(), "WebAssembly module") {
		t.Errorf("the refusal should say what is wrong with the file, got: %v", err)
	}
}
