package script

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// WebAssembly scripts.
//
// # Why the fixtures are compiled here rather than committed
//
// A Go program compiled to wasip1 is about two megabytes, because the runtime comes with it. Committing several of those would put
// eight megabytes of opaque binary in the repository, and a reviewer could not tell what any of it did - which for a sandboxing
// feature is precisely the wrong trade.
//
// So each fixture is a few lines of Go compiled by the test. That makes the tests slower and dependent on a toolchain, which is why
// they skip rather than fail when it is missing: somebody running the suite on a machine without a Go cross-compiler should see the
// rest of the suite pass, not a wall of red about an environment problem.
//
// It also means the fixtures are readable, which matters more than it sounds. The interesting cases here are a module that loops
// forever and a module that exits non-zero, and a reader has to be able to see that the first really does loop.

// buildWASM compiles a Go program to a wasip1 module and returns its bytes.
func buildWASM(t *testing.T, source string) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain, so a WebAssembly fixture cannot be built")
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module wasmfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "fixture.wasm")

	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=")

	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a wasip1 fixture, so this machine cannot run these tests: %v\n%s", err, combined)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

// TestAWASMTransformerReplacesTheMessage is the ordinary case.
//
// stdin to stdout, which is the whole interface. Asserted on the returned text rather than on the module running, because a runtime
// that instantiated the module and discarded its output would pass any test that only checked for an absence of errors.
func TestAWASMTransformerReplacesTheMessage(t *testing.T) {
	module := buildWASM(t, `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	os.Stdout.WriteString(strings.ReplaceAll(string(in), "OKONKWO", "REDACTED"))
}
`)

	e := New(Options{Timeout: 30 * time.Second})

	s, err := e.CompileIn("redact.wasm", module, Transformer, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	res, err := e.Run(s, &Context{Raw: "PID|1||MRN123||OKONKWO^ADAEZE"})
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if !res.Replaced {
		t.Fatal("the transformer's output was not taken as the new message")
	}
	if strings.Contains(res.Text, "OKONKWO") {
		t.Errorf("the name survived: %q", res.Text)
	}
	if !strings.Contains(res.Text, "REDACTED") {
		t.Errorf("the replacement is missing: %q", res.Text)
	}
}

// TestAWASMFilterMustWriteABoolean fixes the filter contract.
//
// Both directions, and the refusal, because the interesting failure is a module that writes something else and is treated as
// meaning one of them. A filter silently reading as false drops traffic.
func TestAWASMFilterMustWriteABoolean(t *testing.T) {
	program := func(body string) string {
		return `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	_ = strings.TrimSpace(string(in))
	` + body + `
}
`
	}

	e := New(Options{Timeout: 30 * time.Second})

	for _, tc := range []struct {
		name       string
		body       string
		wantAccept bool
		wantErr    bool
	}{
		{"true accepts", `os.Stdout.WriteString("true")`, true, false},
		{"false rejects", `os.Stdout.WriteString("false")`, false, false},
		{"trailing newline is trimmed", `os.Stdout.WriteString("true\n")`, true, false},
		{"anything else is refused", `os.Stdout.WriteString("yes")`, false, true},
		{"silence is refused", ``, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := e.CompileIn("filter.wasm", buildWASM(t, program(tc.body)), Filter, WASM)
			if err != nil {
				t.Fatalf("compiling: %v", err)
			}

			res, err := e.Run(s, &Context{Raw: "PID|1"})

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a refusal, got accept=%v", res.Accept)
				}

				return
			}
			if err != nil {
				t.Fatalf("running: %v", err)
			}
			if res.Accept != tc.wantAccept {
				t.Errorf("accept was %v, want %v", res.Accept, tc.wantAccept)
			}
		})
	}
}

// TestAWASMModuleThatLoopsIsStopped is the reason this runtime is worth having.
//
// goja is interrupted between statements and gopher-lua between instructions, both relying on the interpreter to check. A module is
// abandoned when its context expires, which does not depend on the guest cooperating - so this is the test that proves the claim
// rather than repeating it.
func TestAWASMModuleThatLoopsIsStopped(t *testing.T) {
	module := buildWASM(t, `package main

func main() {
	// No syscall, no allocation, nothing the host could use as a checkpoint.
	for {
	}
}
`)

	e := New(Options{Timeout: 2 * time.Second})

	s, err := e.CompileIn("spin.wasm", module, Transformer, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	started := time.Now()
	_, err = e.Run(s, &Context{Raw: "PID|1"})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a module that never returns was allowed to finish")
	}
	if elapsed > 30*time.Second {
		t.Errorf("the module ran for %s against a 2s timeout, so it was not stopped by the deadline", elapsed)
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("the error should say the module ran out of time, got: %v", err)
	}
}

// TestAWASMModuleCannotReachTheFilesystem is the sandbox claim, tested rather than asserted.
//
// Nothing is granted, so an open must fail inside the guest. Worth a test because the failure mode of getting this wrong is silent:
// a module reading /etc/passwd would work and nothing would report it.
func TestAWASMModuleCannotReachTheFilesystem(t *testing.T) {
	module := buildWASM(t, `package main

import (
	"os"
)

func main() {
	if _, err := os.ReadFile("/etc/hosts"); err != nil {
		os.Stdout.WriteString("refused")

		return
	}
	os.Stdout.WriteString("READ THE HOST FILESYSTEM")
}
`)

	e := New(Options{Timeout: 30 * time.Second})

	s, err := e.CompileIn("nosy.wasm", module, Transformer, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	res, err := e.Run(s, &Context{Raw: "PID|1"})
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if strings.Contains(res.Text, "READ THE HOST") {
		t.Error("a module read a file from the host filesystem; nothing should be reachable because nothing is granted")
	}
	if !strings.Contains(res.Text, "refused") {
		t.Errorf("expected the module to report a refused open, got %q", res.Text)
	}
}

// TestSomethingThatIsNotAModuleIsRefusedAtCompileTime keeps the diagnosis early.
//
// The likely mistake is not a corrupt module, it is the wrong sort of file in the field - a Lua script, a path, a base64 blob. Named
// at load, that is a typo. On the first message it is an outage.
func TestSomethingThatIsNotAModuleIsRefusedAtCompileTime(t *testing.T) {
	e := New(Options{})

	for _, tc := range []struct{ name, source string }{
		{"a lua script", "return true"},
		{"a file path", "/opt/perfuse/redact.wasm"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.CompileIn("wrong.wasm", tc.source, Filter, WASM); err == nil {
				t.Fatal("accepted something that is not a WebAssembly module")
			}
		})
	}
}

// TestAWASMModuleWritingToStderrProducesLogs gives a module a way to explain itself.
func TestAWASMModuleWritingToStderrProducesLogs(t *testing.T) {
	module := buildWASM(t, `package main

import "os"

func main() {
	os.Stderr.WriteString("looked at a message\n")
	os.Stdout.WriteString("true")
}
`)

	e := New(Options{Timeout: 30 * time.Second})

	s, err := e.CompileIn("chatty.wasm", module, Filter, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	res, err := e.Run(s, &Context{Raw: "PID|1"})
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if len(res.Logs) != 1 {
		t.Fatalf("got %d log line(s), want 1: %+v", len(res.Logs), res.Logs)
	}
	if !strings.Contains(res.Logs[0].Message, "looked at a message") {
		t.Errorf("the log line does not carry what the module wrote: %q", res.Logs[0].Message)
	}
}

// TestAModuleThatDoesNotCompileIsRefusedAtLoad is the quieter half of moving compilation out of the message path.
//
// # Why this matters more than the timeout it fixed
//
// The visible problem with compiling on the first message was that the script timeout covered it, so a channel with a good module and
// a sensible budget failed once and worked forever after - a fault nobody diagnoses correctly, because by the time anyone looks the
// channel is healthy.
//
// This is the problem underneath it. A module whose preamble is right and whose body is not passed load and failed on the first
// message, so perfuse check said the channel was fine, the deploy succeeded, and the first patient's message was the test. Leaving
// that in the one feature whose entire argument is bounded, predictable execution would have been indefensible.
//
// The fixture is a valid header followed by a section id that does not exist. Note that the bare eight byte header is a legitimate
// empty module and compiles, which is why the invalid case needs something after it - a test using the header alone would assert that
// valid input is rejected and pass for the wrong reason.
func TestAModuleThatDoesNotCompileIsRefusedAtLoad(t *testing.T) {
	header := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

	e := New(Options{Timeout: 30 * time.Second})

	// The control. An empty module is valid, so if this were refused the test below would prove nothing about bad bodies.
	if _, err := e.CompileIn("empty.wasm", string(header), Filter, WASM); err != nil {
		t.Fatalf("the bare header is a valid empty module and should compile: %v", err)
	}

	// Section id 0x7f does not exist, so a decoder must reject it.
	broken := append(append([]byte{}, header...), 0x7f, 0x05, 0x01, 0x02, 0x03)

	_, err := e.CompileIn("broken.wasm", string(broken), Filter, WASM)
	if err == nil {
		t.Fatal("a module that cannot be decoded was accepted at load; the failure would arrive on the first message " +
			"instead, after perfuse check and the deploy had both reported success")
	}

	// The error has to say the refusal is deliberate and early, or it reads as a runtime fault in the wrong place.
	if !strings.Contains(err.Error(), "broken.wasm") {
		t.Errorf("the refusal should name the script so it is clear which slot to fix, got: %v", err)
	}
}

// TestReleasingAScriptIsSafeAndRepeatable covers the shutdown path.
//
// A wazero module is mapped executable memory that Go's collector does not account for, so a reloaded config would accumulate one
// runtime per module and the growth would not appear in the memory statistics anybody thinks to check. Channel.Stop reaches this
// through Scripts.Release.
//
// Asserted as safety rather than as freed bytes, because there is no portable way to observe the unmapping - what can be checked is
// that every shutdown path is safe to take, including the ones that run twice or run against a script that holds nothing.
func TestReleasingAScriptIsSafeAndRepeatable(t *testing.T) {
	e := New(Options{Timeout: 30 * time.Second})

	module := string([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})

	s, err := e.CompileIn("release.wasm", module, Filter, WASM)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}

	if err := s.Release(); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	// Twice, because Stop can be reached more than once and a shutdown that panics the second time is worse than a leak.
	if err := s.Release(); err != nil {
		t.Errorf("releasing a second time should be harmless, got: %v", err)
	}

	// A script with nothing to release, which is every Lua and JavaScript script, must also be safe.
	lua, err := e.CompileIn("plain.lua", "return true", Filter, Lua)
	if err != nil {
		t.Fatalf("compiling lua: %v", err)
	}
	if err := lua.Release(); err != nil {
		t.Errorf("releasing a script that holds nothing should be harmless, got: %v", err)
	}

	// And a nil script, because Stop runs on channels that never started.
	var none *Script
	if err := none.Release(); err != nil {
		t.Errorf("releasing a nil script should be harmless, got: %v", err)
	}
}
