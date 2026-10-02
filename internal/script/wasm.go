package script

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// WebAssembly scripts, run through wazero.
//
// # Why a third runtime at all
//
// Lua and JavaScript both mean writing the transformation in a language chosen by this project. WebAssembly means writing it in
// whatever the site already uses - a Rust crate that already parses their supplier's dialect, a Go package the integration team
// maintains, a C library from the vendor. That is the actual argument: not speed, and not sandboxing, but that some
// transformations already exist as code somebody trusts and rewriting them in Lua is how a migration stalls.
//
// It is also the only runtime here that is bounded by construction rather than by cooperation. goja is interrupted between
// statements and gopher-lua between instructions, both of which rely on the interpreter checking. A wazero module runs with a
// context and is closed when it expires, and it cannot reach anything that was not handed to it - no filesystem, no clock, no
// network, because none is provided.
//
// # The interface, and why it is stdin and stdout
//
// A module reads the message on stdin and writes its result to stdout. That is the whole contract.
//
// The alternative was an exported function over linear memory - transform(ptr, len) returning a packed pointer and length - which
// is faster and requires every module author to agree with us about allocation, string encoding and who frees what. For a
// transformation that runs once per message on a hospital feed, the difference is not worth an ABI that every toolchain implements
// slightly differently. Bytes in, bytes out is what every language already does, and it is what a person testing their module on
// the command line already has.
//
// What the output means is decided by the script's kind, using the same rules the other two runtimes use rather than new ones:
//
//   - Filter: stdout must be "true" or "false", trimmed. Not an exit code, because a non-zero exit is how a module reports that
//     it failed, and a filter that rejects a message has not failed. Conflating those two would make a crashing module look like
//     one that decided to drop traffic.
//   - Transformer, preprocessor and writer: stdout is the new message, and empty output means the message is unchanged. That is
//     the same rule JavaScript has, where falling off the end of a preprocessor leaves the message alone, and it is deliberate:
//     treating no output as "the message is now empty" would discard traffic on a technicality.
//
// Anything the module writes to stderr becomes log lines, so a module can explain itself the way a script calling logger can.
//
// # Why the module is compiled at load rather than on the first message
//
// Compiling a module is not free: wazero turns WebAssembly into machine code, and for a module built from Go - which brings its
// runtime with it, two megabytes or so - that is hundreds of milliseconds, and seconds under a race detector.
//
// It used to happen on the first message, guarded so it happened once. That was wrong twice over. The visible problem was that the
// script timeout covered it, so a channel with a perfectly good module and a sensible five second budget failed its first message
// and worked forever after - which is the shape of a fault nobody diagnoses correctly, because by the time anyone looks the channel
// is healthy and the evidence is one stale error. The answer was documented as "raise scripts.timeout", which is a footgun with a
// note attached rather than a fix.
//
// The worse problem was quieter. A module whose preamble is right and whose body is not - truncated, a bad section, a feature this
// runtime does not implement - passed load and failed on the first message. So `perfuse check` said the channel was fine, the deploy
// succeeded, and the first patient's message was the test. That is the defect shape this project keeps finding, and leaving it in the
// one feature whose whole argument is bounded, predictable execution would have been indefensible.
//
// So the work happens in compile, where a failure is a load error next to the field that caused it, and the timeout covers only what
// an operator set it to cover: running the module against one message.
//
// context.Background is right here rather than a deadline. Compiling is a bounded CPU operation on a fixed input, it happens while a
// human is waiting for a load to finish rather than while a message is queued, and a deadline would reintroduce the thing being
// fixed - a machine slow enough to miss it would refuse a valid channel at startup.

// wasmRuntime compiles and runs WebAssembly modules.
type wasmRuntime struct{}

func (wasmRuntime) language() Language { return WASM }

// compile validates the module and prepares it for reuse.
//
// # Why compilation is deferred to first run
//
// wazero compiles against a runtime, and a runtime holds resources that have to be closed. Compiling here would mean either
// keeping a runtime alive for the life of the engine - which is right, and is what happens - or creating one per compile and
// throwing away the work. The first needs a context, and compile has none to give.
//
// So this checks the source is a WebAssembly module and defers the real compilation, which happens once, guarded, on the first
// message. The check is worth having on its own: a channel whose module is a text file, or a Lua script somebody put in the wrong
// field, is refused at load rather than on the first patient.
func (wasmRuntime) compile(name, source string, kind Kind) (compiled, error) {
	raw := []byte(source)

	// The four byte preamble every module starts with: a NUL and "asm". Checked before handing it to wazero because the common
	// mistake is not a corrupt module, it is the wrong kind of file entirely - a Lua script, a path, a base64 blob - and "this is
	// not a WebAssembly module" is a better error than whatever a decoder says about an unexpected section.
	if len(raw) < 8 || !bytes.Equal(raw[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		return nil, fmt.Errorf("%s: this is not a WebAssembly module - it does not start with the WASM preamble. A module "+
			"is compiled output, so this field wants the bytes of a .wasm file rather than source in any language", name)
	}

	// Every slot that is handed a message, and no others.
	//
	// Deploy and undeploy are refused, and the reason is the sandbox rather than effort. A lifecycle hook exists to touch the
	// world - check that a partner's endpoint answers, write a marker file, tell an operator the channel is up - and a module
	// has no network, no filesystem and no clock, because none is granted. So a module here could only compute and then
	// succeed or fail on nothing, which is not a hook, and offering it would invite somebody to write a readiness check that
	// cannot check anything and always passes.
	//
	// Stated rather than left as a gap in a list: the first version of this switch simply omitted the lifecycle kind, which
	// reads as an oversight and was one, and produced an error that named the slot without saying why.
	switch kind {
	case Filter, Transformer, Preprocessor, Postprocessor, Writer, Reader:
	case Lifecycle:
		return nil, fmt.Errorf("%s: a deploy or undeploy script cannot be a WebAssembly module. These hooks exist to check "+
			"or change something outside the channel, and a module is given no network, no filesystem and no clock - so "+
			"it could only succeed or fail on nothing. Write this one in lua or javascript", name)
	default:
		return nil, fmt.Errorf("%s: WebAssembly is not offered for this script slot", name)
	}

	ctx := context.Background()

	// WithCloseOnContextDone is what stops a runaway module: the runtime is told the context matters and abandons the module when
	// it expires. Set on the runtime, which outlives any one message, while the deadline that actually fires is per message.
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)

		return nil, fmt.Errorf("%s: preparing the WebAssembly host: %w", name, err)
	}

	prepared, err := runtime.CompileModule(ctx, raw)
	if err != nil {
		_ = runtime.Close(ctx)

		// Named as a compile failure rather than passed through, because the wazero message describes a section offset and
		// the person reading it needs to know which channel and which slot to look at.
		return nil, fmt.Errorf("%s: this module does not compile, so the channel is refused now rather than on its first "+
			"message: %w", name, err)
	}

	return &wasmScript{name: name, kind: kind, runtime: runtime, prepared: prepared}, nil
}

// wasmScript is one compiled module.
type wasmScript struct {
	name string
	kind Kind

	// runtime holds the compiled code, which is mapped memory rather than garbage, so it has to be closed.
	runtime  wazero.Runtime
	prepared wazero.CompiledModule
}

// Close releases the compiled code.
//
// Necessary rather than tidy. The compiled form is mapped executable memory that the garbage collector does not account for, so a
// server whose config is reloaded would accumulate one runtime per wasm script per reload - unbounded growth in a process meant to
// run for months, and invisible in Go's own memory statistics. Reached from Channel.Stop through Scripts.Release, after the undeploy
// script has run so that an undeploy module still has a runtime to execute in.
func (s *wasmScript) Close() error {
	if s == nil || s.runtime == nil {
		return nil
	}

	return s.runtime.Close(context.Background())
}

// run executes the module against the context's message.
func (s *wasmScript) run(e *Engine, ctx *Context, kind Kind) (Result, error) {
	timeout := e.opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var out, logs bytes.Buffer

	// The message on stdin. Raw rather than the parsed tree, because a module has no way to receive a tree - and because the
	// formats that would need one already have two runtimes that can address them.
	config := wazero.NewModuleConfig().
		WithStdin(strings.NewReader(ctx.Raw)).
		WithStdout(&out).
		WithStderr(&logs).
		// No filesystem, no environment, no arguments, and no clock beyond what WASI needs. Each of those would be a
		// capability a channel author did not ask for, and the whole reason to prefer this runtime is that it has none
		// unless granted.
		WithName("")

	mod, err := s.runtime.InstantiateModule(runCtx, s.prepared, config)
	if err != nil {
		// A module that exits non-zero has failed, and that is different from a filter deciding to reject. Reported as an
		// error so the message is recorded as failed rather than filtered.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%s: the module did not finish within %s", s.name, timeout)
		}

		s.report(ctx, logs.String())

		return Result{}, fmt.Errorf("%s: %w", s.name, err)
	}
	defer mod.Close(runCtx)

	s.report(ctx, logs.String())

	return s.verdict(out.String(), kind)
}

// verdict turns the module's output into a Result, by the same rules the other runtimes use.
func (s *wasmScript) verdict(stdout string, kind Kind) (Result, error) {
	var res Result

	switch kind {
	case Filter:
		// A boolean, spelled out. Not an exit code and not truthiness: a filter written for one runtime has to mean the same
		// thing written for another, and this project already refuses Lua's rule where 0 and "" are both true.
		switch strings.TrimSpace(stdout) {
		case "true":
			res.Accept = true
		case "false":
			res.Accept = false
		default:
			return res, fmt.Errorf("%s: a filter module must write true or false to stdout, got %q. An exit code is not "+
				"used for this, because a module that exits non-zero has failed and a filter that rejects a message "+
				"has not", s.name, truncateForError(stdout))
		}

		return res, nil

	default:
		res.Accept = true

		// Empty means unchanged, which is what a preprocessor falling off the end means in JavaScript. Treating it as "the
		// message is now empty" would drop traffic because somebody wrote a module that only logs.
		if stdout != "" {
			res.Text = stdout
			res.Replaced = true
		}

		return res, nil
	}
}

// report turns anything the module wrote to stderr into log lines.
//
// Through ctx.Log rather than Result.Logs, because Run replaces the latter with what it collected: the engine owns the log list and
// the duration, and a runtime that kept its own would be free to report a different set from the one the message record shows. That
// is the same reason the Lua logger global goes through ctx.Log rather than accumulating.
//
// Bounded for the same reason the JavaScript log is: a module looping on a write must not exhaust memory before the timeout fires.
func (s *wasmScript) report(ctx *Context, stderr string) {
	if ctx == nil || ctx.Log == nil || strings.TrimSpace(stderr) == "" {
		return
	}

	written := 0

	for _, line := range strings.Split(stderr, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if written >= 500 {
			return
		}
		ctx.Log("info", line)
		written++
	}
}

// truncateForError keeps an error message readable when a module wrote something enormous.
func truncateForError(s string) string {
	const limit = 120

	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "..."
}
