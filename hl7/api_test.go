package hl7

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests guard the promises this package makes now that it is importable. A published API is exactly the place
// where a drift guard earns its keep: the cost of breaking one of these is somebody else's build, and nobody here
// finds out.

func packageFiles(t *testing.T, dir string, includeTests bool) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		out[name] = f
	}
	if len(out) == 0 {
		t.Fatalf("no Go files found in %s", dir)
	}
	return out
}

// selfImportPath is this package's own import path.
//
// Written out rather than derived, so that moving the package makes this fail loudly rather than silently allowing a
// path that is no longer ours.
const selfImportPath = "github.com/biodream-llc/perfuse/hl7"

func TestThePackageHasNoDependencies(t *testing.T) {
	// "No dependencies" is a headline claim for this package and the main reason somebody would choose it over a
	// larger library. It should be impossible to break by accident, including from a test file - a test-only
	// dependency still shows up in a consumer's module graph.
	for name, f := range packageFiles(t, ".", true) {
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			// Standard library paths have no dot in their first segment.
			first := path
			if i := strings.Index(path, "/"); i >= 0 {
				first = path[:i]
			}
			// This package's own path is allowed. A runnable example lives in package hl7_test and has to import
			// hl7 by its full path to be an example of using it - which is the point of examples, and is not a
			// dependency: it adds nothing to a consumer's module graph.
			if path == selfImportPath {
				continue
			}
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %q; this package promises the standard library only", name, path)
			}
		}
	}
}

func TestThePackageDoesNotReachIntoInternal(t *testing.T) {
	// A public package importing internal/ drags private types into a public promise, and those types then cannot
	// change without breaking outside callers who never knew they existed.
	for name, f := range packageFiles(t, ".", true) {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "/internal/") {
				t.Errorf("%s imports %q; a published package must not depend on internal packages", name, path)
			}
		}
	}
}

func TestNoExportedFunctionReturnsAnUnexportedType(t *testing.T) {
	// An exported function returning an unexported type gives callers a value they cannot name, declare or store in
	// a struct field. It compiles, so nothing catches it, and it is discovered by a stranger rather than by us.
	for name, f := range packageFiles(t, ".", false) {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Results == nil {
				continue
			}
			// Methods on unexported types are not part of the public surface.
			if fn.Recv != nil && !receiverIsExported(fn.Recv) {
				continue
			}
			for _, result := range fn.Type.Results.List {
				if bad := unexportedNamed(result.Type); bad != "" {
					t.Errorf("%s: exported %s returns unexported type %q", name, fn.Name.Name, bad)
				}
			}
		}
	}
}

func receiverIsExported(recv *ast.FieldList) bool {
	if len(recv.List) == 0 {
		return false
	}
	return exportedIdent(recv.List[0].Type)
}

func exportedIdent(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr:
		return exportedIdent(t.X)
	case *ast.Ident:
		return ast.IsExported(t.Name)
	}
	return false
}

// unexportedNamed reports the name of an unexported package-level type used in a result, or "" if there is none.
// Builtins are lower-case too, so they are excluded by name.
func unexportedNamed(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return unexportedNamed(t.X)
	case *ast.ArrayType:
		return unexportedNamed(t.Elt)
	case *ast.Ident:
		if ast.IsExported(t.Name) || isBuiltin(t.Name) {
			return ""
		}
		return t.Name
	}
	return ""
}

func isBuiltin(name string) bool {
	switch name {
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any":
		return true
	}
	return false
}

func TestTheSentinelErrorsStayComparable(t *testing.T) {
	// Callers match on these with errors.Is. Turning one into a wrapped or formatted error later would break that
	// silently - their code still compiles, it just stops recognising the error.
	for _, err := range []error{ErrNotHL7, ErrShortHeader} {
		if err == nil {
			t.Fatal("a sentinel error is nil")
		}
		if !strings.HasPrefix(err.Error(), "hl7: ") {
			t.Errorf("sentinel %q should be prefixed with the package name", err.Error())
		}
	}

	if _, err := Parse([]byte("not a message")); err != ErrNotHL7 {
		t.Errorf("parsing rubbish returned %v, want ErrNotHL7 by identity", err)
	}
}
