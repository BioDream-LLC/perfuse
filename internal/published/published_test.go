package published

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

// Packages are the published packages these guards cover, by directory from the repository root. A package made
// importable is added here, or it is published without the promises below.
var Packages = []string{"hl7", "fhir", "pas"}

const module = "github.com/biodream-llc/perfuse/"

func files(t *testing.T, dir string, tests bool) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || (!tests && strings.HasSuffix(n, "_test.go")) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", n, err)
		}
		out[filepath.Join(dir, n)] = f
	}
	if len(out) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return out
}

// TestStandardLibraryOnly: a published package may import the standard library and other published Perfuse packages,
// nothing else - test files included, because a test-only dependency still lands in a consumer's module graph.
func TestStandardLibraryOnly(t *testing.T) {
	allowed := map[string]bool{}
	for _, p := range Packages {
		allowed[module+p] = true
	}
	for _, pkg := range Packages {
		for name, f := range files(t, filepath.Join("..", "..", pkg), true) {
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				first, _, _ := strings.Cut(path, "/")
				if strings.Contains(path, "/internal/") {
					t.Errorf("%s imports %q; a published package must not depend on internal packages", name, path)
				} else if strings.Contains(first, ".") && !allowed[path] {
					t.Errorf("%s imports %q; published packages promise the standard library only", name, path)
				}
			}
		}
	}
}

// TestNoExportedFunctionReturnsAnUnexportedType: a value a caller cannot name cannot be stored in a field or declared.
func TestNoExportedFunctionReturnsAnUnexportedType(t *testing.T) {
	for _, pkg := range Packages {
		for name, f := range files(t, filepath.Join("..", "..", pkg), false) {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !fn.Name.IsExported() || fn.Type.Results == nil {
					continue
				}
				if fn.Recv != nil && !exported(fn.Recv.List[0].Type) {
					continue
				}
				for _, r := range fn.Type.Results.List {
					if bad := unexported(r.Type); bad != "" {
						t.Errorf("%s: exported %s returns unexported type %q", name, fn.Name.Name, bad)
					}
				}
			}
		}
	}
}

func exported(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr:
		return exported(t.X)
	case *ast.Ident:
		return ast.IsExported(t.Name)
	case *ast.IndexExpr:
		return exported(t.X)
	}
	return false
}

func unexported(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return unexported(t.X)
	case *ast.ArrayType:
		return unexported(t.Elt)
	case *ast.MapType:
		if s := unexported(t.Key); s != "" {
			return s
		}
		return unexported(t.Value)
	case *ast.Ident:
		switch t.Name {
		case "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32",
			"int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any":
			return ""
		}
		if !ast.IsExported(t.Name) {
			return t.Name
		}
	}
	return ""
}
