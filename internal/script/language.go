package script

import (
	"fmt"
	"strings"
)

// Language is which scripting language a script is written in.
//
// # Why a second language at all
//
// JavaScript is here because Mirth's scripts are JavaScript and a site migrating has thousands of lines of it. That is a
// compatibility obligation rather than a preference, and it comes with E4X, a dead ECMAScript extension that has to be
// translated before goja will parse it.
//
// Lua is here because it is a better language for the job when nobody is migrating: a smaller runtime, no prototype chain to
// misuse, integer-indexed tables that match segment and field addressing naturally, and a sandbox that is easier to make
// airtight because the standard library is small enough to enumerate. A site starting fresh should be able to choose it.
//
// # Why this is a language and not an engine
//
// The temptation is to call this an engine and give each one its own Compile, Run, Context and Result. That would produce two
// sets of semantics for the same feature: two answers to what a filter returning nothing means, two definitions of which
// globals exist, two places for the timeout to be enforced or forgotten.
//
// So Context and Result are shared and only the execution is per language. A script's observable behaviour - what it can see,
// what it may do, how long it may take, what a filter verdict means - is defined once.
type Language string

const (
	// JavaScript is the default, for Mirth compatibility.
	//
	// Default because an unmarked script in an existing channel file is JavaScript: making Lua the default would silently
	// reinterpret every script written before this existed.
	JavaScript Language = "javascript"

	// Lua is the alternative.
	Lua Language = "lua"

	// WASM is a compiled module rather than a language.
	//
	// The value the field takes is still a language name because that is what the setting is called and what a person writing
	// the file expects to put there. What it selects is a runtime that does not care which language produced the module, which
	// is the point of offering it: a site with a transformation already written in Rust or Go can bring it rather than rewrite
	// it in one of ours.
	WASM Language = "wasm"
)

// KnownLanguages is every language a script may declare.
var KnownLanguages = []Language{JavaScript, Lua, WASM}

// DefaultLanguage is what an unmarked script is.
const DefaultLanguage = JavaScript

// ParseLanguage reads a language name.
//
// Case-insensitive, and js is accepted for javascript because that is what people type. Lua has no common abbreviation, so
// there is nothing to accept for it.
func ParseLanguage(s string) (Language, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return DefaultLanguage, nil
	case "javascript", "js":
		return JavaScript, nil
	case "wasm", "webassembly":
		return WASM, nil
	case "lua":
		return Lua, nil
	}

	names := make([]string, 0, len(KnownLanguages))
	for _, l := range KnownLanguages {
		names = append(names, string(l))
	}

	return "", fmt.Errorf("%q is not a scripting language Perfuse runs; use one of %s", s, strings.Join(names, ", "))
}

// runtime is what a language must provide.
//
// Deliberately narrow. Everything a script can observe - the globals, the maps, the log, the timeout - is arranged by the
// engine and handed over, so a language implementation cannot accidentally offer more or less than the other one.
type runtime interface {
	// compile turns source into something runnable, reporting a syntax error with a line number.
	compile(name, source string, kind Kind) (compiled, error)

	// language names it, for error messages.
	language() Language
}

// compiled is one prepared script.
type compiled interface {
	// run executes against a context and reports what the script did.
	//
	// The Result it returns carries the verdict and any text; the engine owns the logs and the duration, so a language cannot
	// report a duration that excludes its own setup.
	run(e *Engine, ctx *Context, kind Kind) (Result, error)
}
