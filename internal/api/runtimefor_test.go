package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Handlers that may legitimately read s.Runtime directly, with the reason.
//
// An allow list rather than a convention, because "this one is fine" is a judgement that has to be recorded somewhere a
// reviewer will see it. Anything not listed here and not using runtimeFor is a cross-tenant bug waiting for a second
// tenant to exist.
var runtimeDirectAccessAllowed = map[string]string{
	// The self report describes this process for a fleet view: how many channels are running on this instance,
	// across every tenant it serves. Scoping it to the caller's tenant would make a fleet page report a fraction of
	// each server and there would be no way to tell.
	"selfReport": "reports the whole instance for a fleet view, deliberately not one tenant",

	// Readiness and liveness are properties of the process. A probe has no session and no tenant, and answering
	// "ready" per tenant would mean a load balancer could not use it.
	"handleReadyz": "process health, no session exists",
	"handleLivez":  "process health, no session exists",
	"handleHealth": "process health, no session exists",

	// Metrics are collected by one collector with tenant labels already inside. Splitting the endpoint would mean
	// scraping a platform once per tenant, and Prometheus does not work that way.
	"handleMetrics":            "one collector, already labelled by tenant",
	"handleMetricsDefinitions": "one collector, already labelled by tenant",
	"handlePrometheus":         "one collector, already labelled by tenant",

	// The accessor itself, and the helper that wraps it.
	"runtimeFor":        "this is the accessor",
	"optionalRuntime":   "the non-writing accessor, for handlers that work without an engine",
	"requireRuntimeFor": "wraps the accessor",
	"requireRuntime":    "checks existence only, before any tenant is known",
	"requireMessages":   "checks existence only, before any tenant is known",
	"queueStore":        "reached through runtimeFor by its callers",
	"channelCountsFor":  "helper on Runtime rather than a handler",
	"EnableContracts":   "startup, before any request",
	"ContractDrift":     "called from the alert loop, not a request",
	"CheckContractsDue": "called from the alert loop, not a request",
}

// TestHandlersReachTheEngineThroughRuntimeFor fails when a handler with a session reads s.Runtime directly.
//
// This is the same class of mistake as reading s.Channels instead of channelsFor, and it caused the same kind of bug:
// every handler acting on whichever engine happened to be on the server, so one tenant could start and stop another's
// channels. Review cannot catch it across sixty handlers, and the symptom only appears once a second tenant exists.
func TestHandlersReachTheEngineThroughRuntimeFor(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	var problems []string

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, allowed := runtimeDirectAccessAllowed[fn.Name.Name]; allowed {
				continue
			}
			// Only functions that have a session to scope by. A function without one cannot do better than the
			// server's runtime, and demanding it use the accessor would be asking for a lie.
			if !hasSessionParam(fn) {
				continue
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Runtime" {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "s" {
					return true
				}
				problems = append(problems, name+": "+fn.Name.Name+
					" reads s.Runtime while holding a session; use s.runtimeFor(w, r, sess)")
				return false
			})
		}
	}

	if len(problems) > 0 {
		t.Errorf("%d handler(s) reach the engine without scoping it to a tenant:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

// hasSessionParam reports whether a function takes a *store.Session it could scope by.
//
// A parameter named "_" counts as absent, which is deliberate: discarding the session is exactly how the earlier
// channel-isolation bug was written, and a guard that accepted it would miss the thing it exists to find.
func hasSessionParam(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, param := range fn.Type.Params.List {
		star, ok := param.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Session" {
			continue
		}
		for _, name := range param.Names {
			if name.Name != "_" && name.Name != "" {
				return true
			}
		}
	}
	return false
}
