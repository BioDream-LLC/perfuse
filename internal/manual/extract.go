// Package manual builds the Perfuse reference manual.
//
// # Why this is generated rather than written
//
// A reference manual describing 231 configuration keys, written by hand, is wrong within a week. Somebody adds a
// setting, the manual does not mention it, and the manual's silence now means two different things - this option does
// not exist, and nobody updated the document. A reader cannot tell which, so the whole thing stops being trustworthy
// at once rather than gradually.
//
// So the reference chapters are extracted from the source. The key names come from the yaml tags, the types from the
// Go types, and the prose from the doc comments - which in this codebase already have the shape a reference entry
// needs: a first sentence that defines the option, then why it is the way it is.
//
// The conceptual chapters are written by hand, because no amount of parsing produces an explanation of what a channel
// is for.
//
// # Why not asciidoctor
//
// The obvious choice, and what Hibernate itself uses. Rejected because it needs Ruby gems installed on whatever
// machine builds the documentation, and asciidoctor-pdf brings a substantial dependency tree with it. The standing
// rule for this project is that it needs little to no configuration on the box it runs on, and that applies to
// building the manual as much as to running the server.
//
// The HTML is rendered here with the standard library. The PDF is printed from that HTML by the browser Playwright
// already installs for the end-to-end tests, so it needs no LaTeX and looks identical to the HTML by construction.
package manual

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one documented thing: a configuration key, a setting, an enumerated value.
type Entry struct {
	// Name is what somebody writes in the file. The yaml key, not the Go field.
	//
	// The yaml key because that is what a reader types. Documenting Go field names would describe the program rather
	// than the configuration, and the two differ in every case where a name is snake_case on the wire.
	Name string

	// Type is the value's shape, in terms a reader can act on.
	Type string

	// Optional reports whether the key can be left out.
	//
	// Taken from omitempty, which is the same signal the encoder uses. Deriving it rather than restating it means the
	// manual cannot disagree with the parser about whether something is required.
	Optional bool

	// Summary is the first sentence of the doc comment.
	Summary string

	// Detail is the rest: why the option exists, what goes wrong without it.
	//
	// Kept apart from Summary so the manual can show a compact table of every key and the full explanation underneath,
	// without the prose being written twice.
	Detail string

	// Default is the documented default, where the comment states one.
	Default string
}

// Component is a documented type: a source, a destination, a transform step, a settings group.
type Component struct {
	// Name is the Go type name, which is what the codebase calls it.
	Name string

	// Package is where it lives, so a reader can go and look.
	Package string

	// Summary and Detail come from the type's doc comment.
	Summary string
	Detail  string

	// Entries are its configuration keys, in declaration order.
	//
	// Declaration order rather than alphabetical. The order a struct is written in is usually meaningful - address
	// before credentials before tuning - and sorting destroys that for no gain, since the manual has an index.
	Entries []Entry
}

// Extract reads the components and keys out of a package's source.
//
// Parsed from source rather than reflected from the built binary. Reflection sees types and tags but not comments, and
// the comments are the entire value here: a manual listing 231 key names and their types, with no prose, would be a
// worse version of the struct definitions it was generated from.
func Extract(dir string) ([]Component, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		// Test files excluded. They declare helper structs that are not configuration and would appear in the manual
		// as options somebody could set.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var out []Component
	for pkgName, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}

					// The doc comment sits on the GenDecl for a lone type and on the TypeSpec inside a group.
					doc := ts.Doc
					if doc == nil {
						doc = gd.Doc
					}

					c := Component{
						Name:    ts.Name.Name,
						Package: pkgName,
						Entries: fieldEntries(st),
					}
					c.Summary, c.Detail = splitDoc(doc)

					// A struct with no yaml keys and no explanation is an internal type that happens to be exported.
					// Including it would pad the manual with things nobody can configure.
					if len(c.Entries) == 0 && c.Summary == "" {
						continue
					}
					out = append(out, c)
				}
			}
		}
	}

	// Alphabetical by name, because map iteration over packages and files is random and a manual whose section order
	// changes between builds produces a meaningless diff.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// fieldEntries turns struct fields into documented keys.
func fieldEntries(st *ast.StructType) []Entry {
	var out []Entry

	// A comment covering several fields is carried forward. See inheritedDoc for why this is done here rather than by
	// adding a comment to each field.
	var lastSummary, lastDetail string
	var lastNames []string

	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		key, optional, ok := yamlKey(f.Tag.Value)
		if !ok {
			continue
		}

		summary, detail := splitDoc(f.Doc)

		names := fieldNames(f)
		if summary == "" {
			if inh, det, ok := inheritedDoc(lastSummary, lastDetail, lastNames, names); ok {
				summary, detail = inh, det
			}
		} else {
			lastSummary, lastDetail, lastNames = summary, detail, names
		}

		keys := []string{key}
		if len(names) > 1 {
			keys = yamlKeysFor(f.Tag.Value, names)
		}

		for _, k := range keys {
			out = append(out, Entry{
				Name:     k,
				Type:     describeType(f.Type),
				Optional: optional,
				Summary:  summary,
				Detail:   detail,
				Default:  defaultFrom(summary + " " + detail),
			})
		}
	}

	return out
}

// fieldNames lists a field's declared names.
func fieldNames(f *ast.Field) []string {
	out := make([]string, 0, len(f.Names))
	for _, n := range f.Names {
		out = append(out, n.Name)
	}
	return out
}

// inheritedDoc carries a shared comment forward to the fields it also describes.
//
// # Why the generator does this instead of the source carrying a comment per field
//
// This codebase documents credential pairs together, and correctly: "Username and Password use HTTP basic
// authentication" reads properly as one sentence about two fields, and so does "To, CC and BCC are recipients". Go's
// AST attaches that comment only to the first field of the run, so a naive extraction leaves Password, CC and BCC
// blank - which was 24 of 396 keys, and they were disproportionately the credential fields a reader most needs help
// with.
//
// The fix could have been 24 new comments in the source. That would have been worse code: it would duplicate prose
// that reads correctly as a pair, and the duplicate would drift from the original.
//
// So the comment is inherited, but only when it actually names the field. That condition is what makes this safe
// rather than a guess - it is the same signal a human reader uses when they see Password directly under a sentence
// mentioning Password, and it will not silently attach an unrelated comment to a field that genuinely has none.
func inheritedDoc(summary, detail string, from, to []string) (string, string, bool) {
	if summary == "" || len(to) == 0 {
		return "", "", false
	}
	prose := summary + " " + detail
	for _, n := range to {
		if !strings.Contains(prose, n) {
			return "", "", false
		}
	}
	// The comment must also name the field it was attached to, or it is a general remark rather than a description of
	// a group.
	named := false
	for _, n := range from {
		if strings.Contains(prose, n) {
			named = true
		}
	}
	if !named {
		return "", "", false
	}
	return summary, detail, true
}

// yamlKey pulls the key name and whether it is optional out of a struct tag.
func yamlKey(tag string) (name string, optional bool, ok bool) {
	i := strings.Index(tag, `yaml:"`)
	if i < 0 {
		return "", false, false
	}
	rest := tag[i+len(`yaml:"`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return "", false, false
	}
	parts := strings.Split(rest[:j], ",")
	name = parts[0]
	if name == "" || name == "-" {
		return "", false, false
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, optional, true
}

// yamlKeysFor handles several fields sharing one tag position.
//
// Go allows Login and Passcode on one line with one tag, but yaml needs a key each and the parser derives the second
// from the field name. Reproducing that here rather than guessing keeps the manual and the parser agreeing.
func yamlKeysFor(tag string, names []string) []string {
	first, _, ok := yamlKey(tag)
	if !ok || len(names) == 0 {
		return nil
	}
	out := []string{first}
	for _, n := range names[1:] {
		out = append(out, strings.ToLower(n))
	}
	return out
}

// describeType renders a Go type as something a reader can act on.
//
// Durations and booleans are named in the terms a yaml file uses rather than their Go spelling, because a reader is
// writing yaml. time.Duration would send somebody looking for a Go value where what they need to write is 30s.
func describeType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "text"
		case "bool":
			return "true or false"
		case "int", "int64", "int32", "uint", "uint16":
			return "whole number"
		case "float64", "float32":
			return "number"
		default:
			return t.Name
		}
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if id.Name == "time" && t.Sel.Name == "Duration" {
				return "duration, such as 30s or 5m"
			}
			return id.Name + "." + t.Sel.Name
		}
		return t.Sel.Name
	case *ast.StarExpr:
		return describeType(t.X) + " (a block, omit to leave off)"
	case *ast.ArrayType:
		return "list of " + describeType(t.Elt)
	case *ast.MapType:
		return "mapping of " + describeType(t.Key) + " to " + describeType(t.Value)
	default:
		return "value"
	}
}

// splitDoc separates the defining sentence from the reasoning.
func splitDoc(doc *ast.CommentGroup) (summary, detail string) {
	if doc == nil {
		return "", ""
	}

	var lines []string
	for _, c := range doc.List {
		s := strings.TrimPrefix(c.Text, "//")
		s = strings.TrimPrefix(s, "/*")
		s = strings.TrimSuffix(s, "*/")
		lines = append(lines, strings.TrimSpace(s))
	}

	// Paragraphs, so the reasoning keeps its structure. A doc comment in this codebase is several paragraphs and
	// joining them into one would produce a wall.
	var paras []string
	var cur []string
	for _, l := range lines {
		if l == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = nil
			}
			continue
		}
		// Headings inside doc comments, which this codebase uses. Kept as their own paragraph.
		if strings.HasPrefix(l, "#") {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = nil
			}
			paras = append(paras, l)
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	if len(paras) == 0 {
		return "", ""
	}

	// The first sentence defines the thing; everything after it explains. Splitting there lets the manual show a
	// scannable table and the full reasoning without the text being written twice.
	first := paras[0]
	if i := sentenceEnd(first); i > 0 && i < len(first)-1 {
		summary = strings.TrimSpace(first[:i+1])
		remainder := strings.TrimSpace(first[i+1:])
		rest := paras[1:]
		if remainder != "" {
			rest = append([]string{remainder}, rest...)
		}
		return summary, strings.Join(rest, "\n\n")
	}
	return first, strings.Join(paras[1:], "\n\n")
}

// sentenceEnd finds the first full stop that ends a sentence.
//
// Not strings.Index on ". ", because these comments contain version numbers, addresses and abbreviations. Requiring a
// following capital or end of string avoids cutting "HL7 v2.5" in half.
func sentenceEnd(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		if i+1 >= len(s) {
			return i
		}
		if s[i+1] != ' ' {
			continue
		}
		// A digit either side is a version or a dotted number.
		if i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
			continue
		}
		if i+2 < len(s) && s[i+2] >= 'A' && s[i+2] <= 'Z' {
			return i
		}
		if i+2 >= len(s) {
			return i
		}
	}
	return -1
}

// defaultFrom lifts a stated default out of the prose.
//
// The comments in this codebase state defaults in a consistent phrasing, so they can be surfaced as their own column
// rather than leaving a reader to find "Defaults to thirty seconds" in the middle of a paragraph. Where the phrasing
// is absent the column is empty, which is honest: no default was documented.
func defaultFrom(prose string) string {
	for _, marker := range []string{"Defaults to ", "Default is ", "Defaults: "} {
		i := strings.Index(prose, marker)
		if i < 0 {
			continue
		}
		rest := prose[i+len(marker):]
		// Cut at the first stop, comma or line break rather than using sentenceEnd. sentenceEnd deliberately skips a
		// full stop not followed by a space, which is right for prose and wrong here: the stop ending "Defaults to
		// thirty seconds." is followed by a paragraph break, so sentenceEnd walked past it and returned the whole
		// remaining comment as the default. Found by dumping the extraction and reading it.
		if j := strings.IndexAny(rest, ".,\n;"); j > 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// PackageDoc is a package's own explanation of itself.
type PackageDoc struct {
	// Path is the import path relative to the module, so a reader can find it.
	Path string
	// Name is the package name.
	Name string
	// Summary and Detail come from the package comment.
	Summary string
	Detail  string
}

// ExtractPackageDoc reads a package's doc comment.
//
// These are the "what this component is and how it works" chapters. In this codebase the package comments carry the
// design reasoning - why STOMP rather than OpenWire, why a median rather than a mean - which is exactly what somebody
// reading a manual to decide whether to trust the thing wants, and is the part that would never survive being
// rewritten by hand into a separate document.
func ExtractPackageDoc(dir, importPath string) (*PackageDoc, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// # Why the file is chosen deliberately rather than taken from the map
	//
	// A package may have a comment in more than one file - hl7 has one in doc.go and another in message.go - and Go's
	// parser returns files in a map, whose iteration order is random. Taking the first one meant the manual described
	// that package differently on different builds, so every regeneration produced a different file and the staleness
	// check could never pass. Found by diffing two builds against each other.
	//
	// doc.go is preferred because that is the conventional home for a package comment, with the alphabetically first
	// filename as the fallback so the choice is always the same one.
	for name, pkg := range pkgs {
		paths := make([]string, 0, len(pkg.Files))
		for path := range pkg.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		sort.SliceStable(paths, func(i, j int) bool {
			return filepath.Base(paths[i]) == "doc.go" && filepath.Base(paths[j]) != "doc.go"
		})

		for _, path := range paths {
			file := pkg.Files[path]
			if file.Doc == nil {
				continue
			}
			summary, detail := splitDoc(file.Doc)
			if summary == "" {
				continue
			}
			return &PackageDoc{
				Path:    importPath,
				Name:    name,
				Summary: summary,
				Detail:  detail,
			}, nil
		}
	}

	return nil, nil
}

// ImportPathFor renders a directory as its import path.
func ImportPathFor(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return "github.com/biodream-llc/perfuse/" + filepath.ToSlash(rel)
}
