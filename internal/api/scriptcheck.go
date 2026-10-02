package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/e4x"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Checking a script without saving or running it.
//
// This is what the editor pane talks to. Mirth makes you deploy a channel to find out
// whether a script compiles, which means the feedback loop for a syntax error runs
// through a running interface. Compiling on request instead means the answer arrives
// while the author is still looking at the line.
//
// It compiles and does not execute. goja's Compile parses and produces bytecode; nothing
// here calls Run, so a script cannot reach the network, the filesystem or the message
// store from this endpoint. That distinction is the whole reason this is safe to expose
// to an editor role.

// maxScriptCheckBytes bounds the source a client may submit.
//
// Generous for a script - the largest Mirth transformer I have seen in the wild is a few
// tens of kilobytes - and a limit that exists so a parser cannot be handed a hundred
// megabytes of nested brackets to think about.
const maxScriptCheckBytes = 256 * 1024

type scriptCheckRequest struct {
	// Source is the script as the author wrote it, E4X and all.
	Source string `json:"source"`

	// Kind is "filter" or "transformer".
	//
	// Both are wrapped in a function, so a bare return is legal in either and neither
	// requires one - they compile identically, and this endpoint will not tell the two
	// apart. It is carried anyway because the kind decides what the wrapper does with
	// the result at run time, and because a check that reported a different kind from
	// the one the channel uses would be answering a question nobody asked.
	Kind string `json:"kind"`

	// Language is "javascript" or "lua". Absent means javascript, which is what an unmarked script is everywhere else.
	//
	// WebAssembly is deliberately not accepted. A module is compiled output rather than source, so there is nothing to paste
	// into an editor, and a channel naming a module already has it compiled and refused at load - so the gap this endpoint
	// exists to close, learning whether a script survives before deploying it, is already closed for modules.
	Language string `json:"language"`
}

type scriptDiagnostic struct {
	// Line is 1-based, and 0 when the message carries no position.
	Line    int    `json:"line"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

type scriptNoteOut struct {
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
	Snippet string `json:"snippet,omitempty"`
}

type scriptCheckResponse struct {
	OK   bool   `json:"ok"`
	Kind string `json:"kind"`

	// Language echoes which compiler answered, so a client that sent one and got the other's diagnostics can tell.
	Language string `json:"language"`

	// Error is the compile failure, if any, with its position separated out so the
	// editor can put a marker on the right line rather than printing a string.
	Error *scriptDiagnostic `json:"error,omitempty"`

	// Rewritten reports whether any E4X was translated.
	Rewritten bool `json:"rewritten"`

	// Translated is what actually runs. Shown because a script that was silently
	// rewritten and then behaved differently is the hardest kind of migration problem
	// to diagnose, and showing the rewrite turns it into a five-second comparison.
	Translated string `json:"translated,omitempty"`

	// Notes describe each construct that was translated.
	Notes []scriptNoteOut `json:"notes"`
}

// handleCheckScript compiles a script and reports what happened to it.
func (s *Server) handleCheckScript(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req scriptCheckRequest
	if !s.decode(w, r, &req) {
		return
	}

	if len(req.Source) > maxScriptCheckBytes {
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			"the script is too large to check",
			"the limit is "+strconv.Itoa(maxScriptCheckBytes/1024)+" KiB and this is "+strconv.Itoa(len(req.Source)/1024)+" KiB")
		return
	}

	kind, kindName, ok := parseScriptKind(req.Kind)
	if !ok {
		s.fail(w, r, http.StatusBadRequest,
			"the script kind is not recognised",
			"use one of "+strings.Join(scriptCheckKinds, ", "))
		return
	}

	language, languageName, ok := parseScriptLanguage(req.Language)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "unsupported language",
			"the script language is not recognised, or cannot be checked here")

		return
	}

	out := scriptCheckResponse{Kind: kindName, Language: languageName, Notes: []scriptNoteOut{}}

	// An empty script is valid and means "no script". Reporting a syntax error for it
	// would make the pane look broken while somebody is still typing the first line.
	if strings.TrimSpace(req.Source) == "" {
		out.OK = true
		s.ok(w, out)
		return
	}

	// The translation is reported even when compilation later fails, because the E4X
	// rewrite is often the thing that caused the failure and hiding it at that moment
	// removes the only clue.
	// Only for JavaScript. E4X is a JavaScript extension, and running the translator over Lua would report rewrites of a
	// language it does not parse - which would be worse than saying nothing, because the pane's whole job is to show what
	// happened to the script.
	if language == script.JavaScript {
		if translated, notes, err := e4x.Preprocess(req.Source); err == nil {
			out.Translated = translated
			out.Rewritten = e4x.Rewritten(req.Source)
			for _, n := range notes {
				out.Notes = append(out.Notes, scriptNoteOut{
					Line: n.Line, Kind: n.Kind, Detail: n.Detail, Snippet: n.Snippet,
				})
			}
		}
	}

	// A fresh engine per request, with no permissions granted. Nothing is executed, so
	// the permissions do not gate anything here - but constructing it this way means a
	// future change that did execute would fail closed rather than inherit whatever the
	// last channel was allowed to do.
	engine := script.New(script.Options{})
	if _, err := engine.CompileIn("editor", req.Source, kind, language); err != nil {
		out.OK = false
		out.Error = diagnoseScriptError(err.Error())
		s.ok(w, out)
		return
	}

	out.OK = true
	s.ok(w, out)
}

// parseScriptLanguage reads the language, defaulting the way the rest of the project does.
//
// WebAssembly is refused rather than defaulted, and refused explicitly rather than falling into the unrecognised arm, because a
// person who sends it deserves to know the reason is the paste box rather than a typo.
func parseScriptLanguage(in string) (script.Language, string, bool) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "", "javascript", "js":
		return script.JavaScript, "javascript", true
	case "lua":
		return script.Lua, "lua", true
	default:
		return "", "", false
	}
}

// parseScriptKind reads the slot a script is destined for.
//
// All seven, where this used to take two. The workbench refused the other five, which made it the wrong shape of tool: a person
// migrating from Mirth has preprocessors and postprocessors and deploy hooks to move as well as transformers, and the one place
// they could find out whether a script compiles would only answer for two of them. Worse, the kind is not decoration - it decides
// the wrapper, so a Writer compiled as a Transformer has its return value discarded and a Lifecycle script is told there is no
// message. Checking a preprocessor as a transformer would have reported on a script nobody was going to run.
//
// The engine has always handled all seven in both languages. Only this parser was narrow.
func parseScriptKind(in string) (script.Kind, string, bool) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "filter":
		return script.Filter, "filter", true
	case "transformer", "":
		// Defaulted rather than refused, because the transformer is what a Mirth
		// migration is mostly made of. The two compile identically, so the default
		// cannot make a script pass that would otherwise fail.
		return script.Transformer, "transformer", true
	case "preprocessor":
		return script.Preprocessor, "preprocessor", true
	case "postprocessor":
		return script.Postprocessor, "postprocessor", true
	case "writer":
		return script.Writer, "writer", true
	case "lifecycle":
		return script.Lifecycle, "lifecycle", true
	case "reader":
		return script.Reader, "reader", true
	}

	return 0, "", false
}

// scriptCheckKinds is what the endpoint accepts, in the order a person meets them in a message's life.
//
// Written down so a drift test can compare it against the form's selector. The form offering fewer than this is the defect being
// fixed here, and it went unnoticed for months because nothing compared the two.
var scriptCheckKinds = []string{"preprocessor", "filter", "transformer", "writer", "postprocessor", "lifecycle", "reader"}

// scriptPosition matches the position goja puts in a compile error.
//
// goja reports "name.js: Line 3:11 Unexpected token" and similar. Parsing it is worth
// doing because a marker on the right line is the difference between an editor and a
// textarea with a red box under it.
var scriptPosition = regexp.MustCompile(`(?i)line\s+(\d+)\s*:\s*(\d+)`)

// diagnoseScriptError separates the position from the message.
func diagnoseScriptError(msg string) *scriptDiagnostic {
	d := &scriptDiagnostic{Message: strings.TrimSpace(msg)}

	if m := scriptPosition.FindStringSubmatch(msg); m != nil {
		if line, err := strconv.Atoi(m[1]); err == nil {
			d.Line = line
		}
		if col, err := strconv.Atoi(m[2]); err == nil {
			d.Column = col
		}
	}
	return d
}
