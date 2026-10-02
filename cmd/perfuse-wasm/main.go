//go:build js && wasm

// Perfuse in a browser tab.
//
// This exists because of how software gets evaluated. Nobody installs an integration engine to find out
// whether they like it - they read a page, decide it is probably fine, and never try it. The gap between
// "interested" and "has run a message through it" is where almost everyone is lost, and every step in that
// gap costs most of the remaining audience: download, unpack, run, find a port, write a channel.
//
// So the whole engine's core runs here instead. Paste a real HL7 message, paste the Mirth JavaScript you
// already have, and watch it run. Nothing is installed, nothing is uploaded, and there is no server: the
// parser, the filter language, the transformation steps, the E4X translator and the whole script engine are
// compiled to WebAssembly and execute in the tab.
//
// # The claim it is really there to prove
//
// Perfuse's entire migration argument is "your existing Mirth scripts run unchanged". That is a big claim
// and it is the one nobody will believe from a README. This is the cheapest possible demonstration: their
// script, their message, in front of them, in ten seconds.
//
// # Why nothing leaves the tab
//
// An integration analyst's realistic test data is real patient data. If this posted anything to a server,
// the correct answer for them would be to not use it - and they would be right. Everything is local, which
// is a property of how it is built rather than a promise in a privacy policy, and it is worth saying on the
// page.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/e4x"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// scriptTimeout bounds a script in the playground.
//
// Shorter than the engine's default, because a runaway script in a browser tab freezes the tab. There is no
// separate thread to interrupt from in WebAssembly, so the only defence is a tight bound.
const scriptTimeout = 2 * time.Second

func main() {
	js.Global().Set("perfuseParse", js.FuncOf(wrap(parseMessage)))
	js.Global().Set("perfuseFilter", js.FuncOf(wrap(evalFilter)))
	js.Global().Set("perfuseTransform", js.FuncOf(wrap(runTransform)))
	js.Global().Set("perfuseScript", js.FuncOf(wrap(runScript)))
	js.Global().Set("perfuseTranslate", js.FuncOf(wrap(translateE4X)))
	js.Global().Set("perfuseVersion", js.FuncOf(wrap(reportVersion)))

	// A courtesy for a page that would rather listen than poll. Guarded because there is no DOM in Node
	// or in a worker, and an unguarded call panics there - which killed the module before it had installed
	// anything, in a host where it otherwise works perfectly. Being runnable outside a browser is worth
	// keeping: it is how this gets tested.
	if ev := js.Global().Get("Event"); ev.Type() == js.TypeFunction {
		if dispatch := js.Global().Get("dispatchEvent"); dispatch.Type() == js.TypeFunction {
			js.Global().Call("dispatchEvent", ev.New("perfuse-ready"))
		}
	}

	// WebAssembly exits when main returns, taking the exported functions with it.
	<-make(chan struct{})
}

// wrap turns a Go function into something JavaScript can call, and turns a panic into a returned error.
//
// A panic in WebAssembly kills the whole module, and the page would then appear to work while every button
// did nothing. Recovering means a bug shows up as an error message next to the input that caused it, which
// is also a bug report somebody can send.
func wrap(fn func(args []js.Value) any) func(js.Value, []js.Value) any {
	return func(_ js.Value, args []js.Value) (result any) {
		defer func() {
			if r := recover(); r != nil {
				result = toJS(map[string]any{
					"error": fmt.Sprintf("Perfuse hit an internal error: %v. This is a bug worth "+
						"reporting, with the input that caused it.", r),
				})
			}
		}()
		return fn(args)
	}
}

// parseMessage reports the structure of a message.
func parseMessage(args []js.Value) any {
	if len(args) < 1 {
		return toJS(map[string]any{"error": "no message was given"})
	}

	raw := normalise(args[0].String())
	if strings.TrimSpace(raw) == "" {
		return toJS(map[string]any{"error": "the message is empty"})
	}

	msg, err := hl7.Parse([]byte(raw))
	if err != nil {
		return toJS(map[string]any{"error": err.Error()})
	}

	kind, event, structure := msg.Type()

	tree, err := hl7xml.FromRaw([]byte(raw))
	if err != nil {
		return toJS(map[string]any{"error": err.Error()})
	}

	segments := []any{}
	for _, seg := range tree.Children {
		fields := []any{}
		for _, f := range seg.Children {
			value := f.Value()
			fields = append(fields, map[string]any{
				"path":  seg.Name + "-" + fieldNumber(f.Name),
				"name":  f.Name,
				"value": value,
				// Present-but-empty is reported as a distinct state rather than as an absence, because
				// filters turn on exactly that difference and the playground is where somebody learns it.
				"empty": value == "",
			})
		}
		segments = append(segments, map[string]any{
			"name":   seg.Name,
			"fields": fields,
		})
	}

	return toJS(map[string]any{
		"messageType": kind,
		"event":       event,
		"structure":   structure,
		"controlId":   msg.MustGet("MSH-10"),
		"version":     msg.MustGet("MSH-12"),
		"segments":    segments,
	})
}

// evalFilter runs a filter expression against a message.
func evalFilter(args []js.Value) any {
	if len(args) < 2 {
		return toJS(map[string]any{"error": "a message and an expression are both needed"})
	}

	raw := normalise(args[0].String())
	source := args[1].String()

	compiled, err := expr.Parse(source)
	if err != nil {
		// Separated from an evaluation failure, because one is a typo in the expression and the other is
		// a fact about the message, and the fix is different.
		return toJS(map[string]any{"error": err.Error(), "stage": "parse"})
	}

	msg, err := hl7.Parse([]byte(raw))
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "message"})
	}

	passed, err := compiled.Eval(msg)
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "evaluate"})
	}

	return toJS(map[string]any{
		"passed": passed,
		// The canonical form, so somebody can see how their expression was understood - which is how the
		// precedence of and/or gets learned without reading a grammar.
		"canonical": compiled.String(),
		"paths":     toAnySlice(compiled.Paths()),
	})
}

// runTransform applies declarative transformation steps.
func runTransform(args []js.Value) any {
	if len(args) < 2 {
		return toJS(map[string]any{"error": "a message and a list of steps are both needed"})
	}

	raw := normalise(args[0].String())

	var steps []transform.Step
	if err := json.Unmarshal([]byte(args[1].String()), &steps); err != nil {
		return toJS(map[string]any{"error": "the steps are not valid JSON: " + err.Error()})
	}

	pipeline, err := transform.Compile(steps)
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "compile"})
	}

	tree, err := hl7xml.FromRaw([]byte(raw))
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "message"})
	}

	changes, err := pipeline.Apply(tree)
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "apply"})
	}

	output, err := hl7xml.ToER7(tree, hl7xml.Options{})
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "encode"})
	}

	// The list of what changed, not only the result. A step that silently did nothing is the commonest
	// confusion with declarative transformations, and showing the changes makes it visible instead of
	// leaving somebody to diff two messages by eye.
	changed := []any{}
	for _, c := range changes {
		changed = append(changed, map[string]any{
			"step": c.Step,
			"path": c.Path,
			"from": c.From,
			"to":   c.To,
			"note": c.Note,
		})
	}

	return toJS(map[string]any{"output": string(output), "changes": changed})
}

// runScript runs JavaScript against a message, exactly as a channel transformer would.
func runScript(args []js.Value) any {
	if len(args) < 2 {
		return toJS(map[string]any{"error": "a message and a script are both needed"})
	}

	raw := normalise(args[0].String())
	source := args[1].String()

	engine := script.New(script.Options{Timeout: scriptTimeout})

	compiled, err := engine.Compile("playground", source, script.Transformer)
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "compile"})
	}

	tree, err := hl7xml.FromRaw([]byte(raw))
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "message"})
	}

	result, err := engine.Run(compiled, &script.Context{Message: tree, Raw: raw})
	if err != nil {
		// Logs are returned even on failure. A script that logged three lines and then threw has told you
		// where it got to, and discarding that is discarding the most useful part.
		return toJS(map[string]any{
			"error": err.Error(),
			"stage": "run",
			"logs":  logsToJS(result.Logs),
		})
	}

	output, err := hl7xml.ToER7(tree, hl7xml.Options{})
	if err != nil {
		return toJS(map[string]any{"error": err.Error(), "stage": "encode"})
	}

	return toJS(map[string]any{
		"output": string(output),
		"logs":   logsToJS(result.Logs),
		"accept": result.Accept,
	})
}

// translateE4X shows what Mirth's E4X becomes.
//
// The most educational of these, because the answer is usually "almost nothing changed", and seeing that is
// what makes the compatibility claim believable in a way a paragraph cannot.
func translateE4X(args []js.Value) any {
	if len(args) < 1 {
		return toJS(map[string]any{"error": "no script was given"})
	}

	rewritten, notes, err := e4x.Preprocess(args[0].String())
	if err != nil {
		return toJS(map[string]any{"error": err.Error()})
	}

	noteList := []any{}
	for _, n := range notes {
		noteList = append(noteList, map[string]any{
			"line":    n.Line,
			"kind":    n.Kind,
			"message": n.Detail,
		})
	}

	return toJS(map[string]any{
		"output": rewritten,
		"notes":  noteList,
		// So the page can say "nothing needed changing", which is the interesting outcome.
		"unchanged": strings.TrimSpace(rewritten) == strings.TrimSpace(args[0].String()),
	})
}

func reportVersion([]js.Value) any {
	return toJS(map[string]any{
		"version": version,
		"note":    "running entirely in this tab; nothing is sent anywhere",
	})
}

// normalise turns the line endings a browser textarea produces into the ones HL7 requires.
//
// A textarea gives \n, and HL7 segments are separated by \r. Without this every pasted message fails to
// parse past MSH, which would make the playground look broken on the very first thing anybody tries - and
// would be blamed on the parser rather than on the clipboard.
func normalise(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	return s
}

func fieldNumber(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}

func logsToJS(logs []script.LogLine) []any {
	out := []any{}
	for _, l := range logs {
		out = append(out, map[string]any{"level": l.Level, "message": l.Message})
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// toJS converts a Go map to something js.ValueOf accepts.
func toJS(v map[string]any) js.Value {
	return js.ValueOf(v)
}

// version is set at build time.
var version = "dev"
