package config

import (
	"reflect"
	"sort"
	"strings"
)

// Setting is one entry in the channel schema, with enough detail to look for its readers.
type Setting struct {
	// Path is the dotted yaml path, as a person writes it in a file.
	Path string

	// Field is the Go field name, which is what a reader would reference.
	Field string

	// Owner is the struct that declares it, so a common field name can be told apart from another struct's.
	Owner string
}

// AllSettings enumerates the schema with the detail needed to audit it.
//
// # Why the Go field name matters
//
// A setting that no code outside this package ever reads cannot affect behaviour, whatever the file says and however carefully the
// loader validates it. That is mechanically detectable: find the field, look for readers. It is the one part of the audit a
// machine can do, and it catches the worst version of the defect - not a setting wired to the wrong place, but one wired nowhere.
func AllSettings() []Setting {
	seen := map[string]bool{}
	var out []Setting

	for _, s := range walkSettings(reflect.TypeOf(Channel{}), "Channel", "", 0) {
		key := s.Owner + "." + s.Field
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	return out
}

func walkSettings(rt reflect.Type, owner, prefix string, depth int) []Setting {
	// Bounded because a config type can reference itself - a shadow names another channel - and an unbounded walk would not
	// terminate.
	if depth > 8 {
		return nil
	}

	for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return nil
	}

	if rt.Name() != "" {
		owner = rt.Name()
	}

	var out []Setting
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)

		if f.PkgPath != "" && !f.Anonymous {
			continue
		}

		tag := f.Tag.Get("yaml")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			out = append(out, walkSettings(f.Type, owner, prefix, depth+1)...)

			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		nested := walkSettings(f.Type, owner, path, depth+1)
		if len(nested) > 0 {
			out = append(out, nested...)

			continue
		}

		out = append(out, Setting{Path: path, Field: f.Name, Owner: owner})
	}

	return out
}
