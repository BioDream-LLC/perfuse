// Package sof runs SQL on FHIR v2 ViewDefinitions: a view turns each FHIR resource of one type into rows of named columns,
// by FHIRPath, so FHIR data can be read as tables by SQL engines, spreadsheets and quality measures.
//
// It follows the ViewDefinition's processing algorithm (column, select, forEach, forEachOrNull, repeat, unionAll, where,
// constants, %rowIndex, getResourceKey and getReferenceKey) and is checked against the official sql-on-fhir test fixtures.
package sof

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhirpath"
)

// ErrInvalid is a ViewDefinition that cannot be run: a structural error found before any resource is read.
var ErrInvalid = errors.New("the ViewDefinition is not valid")

// ErrRun is an error while running a view over a resource (multiple values in a non-collection column, a where clause that
// is not Boolean).
var ErrRun = errors.New("the view could not be run")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Column is one output column.
type Column struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Collection  bool   `json:"collection,omitempty"`
	Description string `json:"description,omitempty"`
	path        *fhirpath.Expression
	rowIndex    bool
}

type selection struct {
	column        []*Column
	sel           []*selection
	unionAll      []*selection
	forEach       *fhirpath.Expression
	forEachOrNull *fhirpath.Expression
	repeat        []*fhirpath.Expression
}

// View is a compiled ViewDefinition.
type View struct {
	Name     string
	Title    string
	URL      string
	Resource string
	// Columns are the output columns in order.
	Columns []*Column
	where   []*fhirpath.Expression
	root    *selection
	env     *fhirpath.Env
}

var columnName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Compile reads and validates a ViewDefinition (JSON).
func Compile(raw []byte) (*View, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return nil, invalid("it is not a JSON object: %v", err)
	}
	return CompileMap(m)
}

// CompileMap is Compile for a decoded ViewDefinition (decoded with UseNumber).
func CompileMap(m map[string]any) (*View, error) {
	if rt, ok := m["resourceType"]; ok && rt != "ViewDefinition" {
		return nil, invalid("resourceType is %v, not ViewDefinition", rt)
	}
	res, ok := m["resource"].(string)
	if !ok || res == "" {
		return nil, invalid("resource is required: the FHIR resource type the view reads, such as Patient")
	}
	v := &View{Resource: res, env: &fhirpath.Env{Vars: map[string]fhirpath.Collection{}}}
	v.Name, _ = m["name"].(string)
	v.Title, _ = m["title"].(string)
	v.URL, _ = m["url"].(string)
	if cs, ok := m["constant"]; ok {
		list, ok := cs.([]any)
		if !ok {
			return nil, invalid("constant must be a list")
		}
		for _, c := range list {
			cm, ok := c.(map[string]any)
			if !ok {
				return nil, invalid("each constant is an object with a name and one value[x]")
			}
			name, _ := cm["name"].(string)
			if !columnName.MatchString(name) {
				return nil, invalid("constant name %q is not a name", name)
			}
			var val *fhirpath.Item
			for k, x := range cm {
				if !strings.HasPrefix(k, "value") || len(k) == 5 {
					continue
				}
				if val != nil {
					return nil, invalid("constant %s has more than one value", name)
				}
				t := strings.ToLower(k[5:6]) + k[6:]
				it := fhirpath.FromJSON(x, t)
				val = &it
			}
			if val == nil {
				return nil, invalid("constant %s has no value", name)
			}
			v.env.Vars[name] = fhirpath.Collection{*val}
		}
	}
	if w, ok := m["where"]; ok {
		list, ok := w.([]any)
		if !ok {
			return nil, invalid("where must be a list")
		}
		for _, x := range list {
			wm, ok := x.(map[string]any)
			if !ok {
				return nil, invalid("each where is an object with a path")
			}
			e, err := expr(wm["path"], "where.path")
			if err != nil {
				return nil, err
			}
			v.where = append(v.where, e)
		}
	}
	sels, err := selections(m["select"], "select")
	if err != nil {
		return nil, err
	}
	if len(sels) == 0 {
		return nil, invalid("select is required, with at least one column")
	}
	v.root = &selection{sel: sels}
	cols, err := validateColumns(v.root, nil)
	if err != nil {
		return nil, err
	}
	v.Columns = cols
	v.env.Functions = map[string]fhirpath.Function{"getResourceKey": getResourceKey, "getReferenceKey": getReferenceKey}
	return v, nil
}

func expr(x any, where string) (*fhirpath.Expression, error) {
	s, ok := x.(string)
	if !ok {
		return nil, invalid("%s must be a FHIRPath expression (a string)", where)
	}
	e, err := fhirpath.Parse(s)
	if err != nil {
		return nil, invalid("%s %q: %v", where, s, err)
	}
	return e, nil
}

func selections(x any, where string) ([]*selection, error) {
	if x == nil {
		return nil, nil
	}
	list, ok := x.([]any)
	if !ok {
		return nil, invalid("%s must be a list", where)
	}
	var out []*selection
	for i, s := range list {
		sm, ok := s.(map[string]any)
		if !ok {
			return nil, invalid("%s[%d] must be an object", where, i)
		}
		sel, err := compileSelection(sm, fmt.Sprintf("%s[%d]", where, i))
		if err != nil {
			return nil, err
		}
		out = append(out, sel)
	}
	return out, nil
}

func compileSelection(m map[string]any, where string) (*selection, error) {
	s := &selection{}
	for k := range m {
		switch k {
		case "column", "select", "unionAll", "forEach", "forEachOrNull", "repeat":
		default:
			return nil, invalid("%s has %s, which a select does not take", where, k)
		}
	}
	n := 0
	var err error
	if x, ok := m["forEach"]; ok {
		n++
		if s.forEach, err = expr(x, where+".forEach"); err != nil {
			return nil, err
		}
	}
	if x, ok := m["forEachOrNull"]; ok {
		n++
		if s.forEachOrNull, err = expr(x, where+".forEachOrNull"); err != nil {
			return nil, err
		}
	}
	if x, ok := m["repeat"]; ok {
		n++
		list, ok := x.([]any)
		if !ok || len(list) == 0 {
			return nil, invalid("%s.repeat must be a list of expressions", where)
		}
		for _, p := range list {
			e, err := expr(p, where+".repeat")
			if err != nil {
				return nil, err
			}
			s.repeat = append(s.repeat, e)
		}
	}
	if n > 1 {
		return nil, invalid("%s has more than one of forEach, forEachOrNull and repeat", where)
	}
	if x, ok := m["column"]; ok {
		list, ok := x.([]any)
		if !ok {
			return nil, invalid("%s.column must be a list", where)
		}
		for i, c := range list {
			cm, ok := c.(map[string]any)
			if !ok {
				return nil, invalid("%s.column[%d] must be an object", where, i)
			}
			col := &Column{}
			col.Name, _ = cm["name"].(string)
			if !columnName.MatchString(col.Name) {
				return nil, invalid("%s.column[%d] name %q must start with a letter and hold only letters, digits and _", where, i, col.Name)
			}
			if col.path, err = expr(cm["path"], fmt.Sprintf("%s.column[%d].path", where, i)); err != nil {
				return nil, err
			}
			col.rowIndex = strings.TrimSpace(col.path.String()) == "%rowIndex"
			col.Type, _ = cm["type"].(string)
			col.Description, _ = cm["description"].(string)
			if cv, ok := cm["collection"]; ok {
				b, ok := cv.(bool)
				if !ok {
					return nil, invalid("%s.column[%d].collection must be true or false", where, i)
				}
				col.Collection = b
			}
			s.column = append(s.column, col)
		}
	}
	if s.sel, err = selections(m["select"], where+".select"); err != nil {
		return nil, err
	}
	if s.unionAll, err = selections(m["unionAll"], where+".unionAll"); err != nil {
		return nil, err
	}
	return s, nil
}

// validateColumns is the specification's ValidateColumns: names unique, union branches alike.
func validateColumns(s *selection, prior []*Column) ([]*Column, error) {
	ret := append([]*Column{}, prior...)
	has := func(name string) bool {
		for _, c := range ret {
			if c.Name == name {
				return true
			}
		}
		return false
	}
	for _, c := range s.column {
		if has(c.Name) {
			return nil, invalid("column %s is defined twice", c.Name)
		}
		ret = append(ret, c)
	}
	for _, sel := range s.sel {
		cols, err := validateColumns(sel, ret)
		if err != nil {
			return nil, err
		}
		for _, c := range cols[len(ret):] {
			if has(c.Name) {
				return nil, invalid("column %s is defined twice", c.Name)
			}
			ret = append(ret, c)
		}
	}
	if len(s.unionAll) > 0 {
		u0, err := validateColumns(s.unionAll[0], ret)
		if err != nil {
			return nil, err
		}
		names := func(cs []*Column) string {
			var n []string
			for _, c := range cs {
				n = append(n, c.Name)
			}
			return strings.Join(n, ",")
		}
		for i, sel := range s.unionAll[1:] {
			u, err := validateColumns(sel, ret)
			if err != nil {
				return nil, err
			}
			if names(u[len(ret):]) != names(u0[len(ret):]) {
				return nil, invalid("unionAll branch %d has columns (%s), unlike branch 0 (%s)", i+1, names(u[len(ret):]), names(u0[len(ret):]))
			}
		}
		ret = append(ret, u0[len(ret):]...)
	}
	return ret, nil
}

// columnsOf lists a selection's columns (for forEachOrNull's null row).
func columnsOf(s *selection) []*Column {
	cols, _ := validateColumns(s, nil)
	return cols
}

// Row is one output row: a value per column, in View.Columns order (nil for null).
type Row []any

type partial map[string]any

// Run runs the view over one resource (decoded with UseNumber).
func (v *View) Run(resource map[string]any) ([]Row, error) {
	if rt, _ := resource["resourceType"].(string); rt != v.Resource {
		return nil, nil
	}
	in := fhirpath.Collection{fhirpath.FromJSON(resource, "")}
	for _, w := range v.where {
		res, err := w.Evaluate(in, v.envWith(0))
		if err != nil {
			return nil, fmt.Errorf("%w: where %q: %v", ErrRun, w, err)
		}
		if len(res) == 0 {
			return nil, nil
		}
		if len(res) > 1 {
			return nil, fmt.Errorf("%w: where %q gave %d values, not one Boolean", ErrRun, w, len(res))
		}
		b, ok := res[0].V.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: where %q is not a Boolean", ErrRun, w)
		}
		if !b {
			return nil, nil
		}
	}
	parts, err := v.process(v.root, in[0], in, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(parts))
	for _, p := range parts {
		r := make(Row, len(v.Columns))
		for i, c := range v.Columns {
			r[i] = p[c.Name]
		}
		out = append(out, r)
	}
	return out, nil
}

func (v *View) envWith(rowIndex int) *fhirpath.Env {
	vars := make(map[string]fhirpath.Collection, len(v.env.Vars)+1)
	for k, x := range v.env.Vars {
		vars[k] = x
	}
	vars["rowIndex"] = fhirpath.Collection{{V: int64(rowIndex), T: "integer"}}
	return &fhirpath.Env{Vars: vars, Functions: v.env.Functions}
}

// process is the specification's Process(S, N). resource is kept for %resource.
func (v *View) process(s *selection, node fhirpath.Item, resource fhirpath.Collection, rowIndex int) ([]partial, error) {
	foci := []fhirpath.Item{node}
	indexed := false
	switch {
	case len(s.repeat) > 0:
		indexed = true
		foci = nil
		var traverse func(n fhirpath.Item, root bool) error
		traverse = func(n fhirpath.Item, root bool) error {
			if !root {
				foci = append(foci, n)
				if len(foci) > 1_000_000 {
					return fmt.Errorf("%w: repeat found more than a million elements", ErrRun)
				}
			}
			for _, p := range s.repeat {
				kids, err := p.Evaluate(fhirpath.Collection{n}, v.withResource(resource, rowIndex))
				if err != nil {
					return fmt.Errorf("%w: repeat %q: %v", ErrRun, p, err)
				}
				for _, k := range kids {
					if err := traverse(k, false); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := traverse(node, true); err != nil {
			return nil, err
		}
	case s.forEach != nil || s.forEachOrNull != nil:
		indexed = true
		e := s.forEach
		if e == nil {
			e = s.forEachOrNull
		}
		res, err := e.Evaluate(fhirpath.Collection{node}, v.withResource(resource, rowIndex))
		if err != nil {
			return nil, fmt.Errorf("%w: forEach %q: %v", ErrRun, e, err)
		}
		foci = res
	}
	var out []partial
	for i, f := range foci {
		idx := rowIndex
		if indexed {
			idx = i
		}
		env := v.withResource(resource, idx)
		var parts [][]partial
		row := partial{}
		for _, c := range s.column {
			val, err := c.path.Evaluate(fhirpath.Collection{f}, env)
			if err != nil {
				return nil, fmt.Errorf("%w: column %s %q: %v", ErrRun, c.Name, c.path, err)
			}
			switch {
			case c.Collection:
				arr := make([]any, 0, len(val))
				for _, it := range val {
					arr = append(arr, fhirpath.ToJSON(it))
				}
				row[c.Name] = arr
			case len(val) == 0:
				row[c.Name] = nil
			case len(val) == 1:
				row[c.Name] = fhirpath.ToJSON(val[0])
			default:
				return nil, fmt.Errorf("%w: column %s (%q) has %d values; set collection: true to keep them all, or narrow the path",
					ErrRun, c.Name, c.path, len(val))
			}
		}
		parts = append(parts, []partial{row})
		for _, sel := range s.sel {
			rows, err := v.process(sel, f, resource, idx)
			if err != nil {
				return nil, err
			}
			parts = append(parts, rows)
		}
		if len(s.unionAll) > 0 {
			var urows []partial
			for _, u := range s.unionAll {
				rows, err := v.process(u, f, resource, idx)
				if err != nil {
					return nil, err
				}
				urows = append(urows, rows...)
			}
			parts = append(parts, urows)
		}
		out = append(out, product(parts)...)
	}
	if len(foci) == 0 && s.forEachOrNull != nil {
		r := partial{}
		for _, c := range columnsOf(s) {
			if c.rowIndex {
				r[c.Name] = int64(0)
			} else if c.Collection {
				r[c.Name] = []any{}
			} else {
				r[c.Name] = nil
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func (v *View) withResource(resource fhirpath.Collection, rowIndex int) *fhirpath.Env {
	e := v.envWith(rowIndex)
	e.Vars["resource"] = resource
	e.Vars["rootResource"] = resource
	return e
}

func product(parts [][]partial) []partial {
	acc := []partial{{}}
	for _, rows := range parts {
		var next []partial
		for _, a := range acc {
			for _, r := range rows {
				m := make(partial, len(a)+len(r))
				for k, x := range a {
					m[k] = x
				}
				for k, x := range r {
					m[k] = x
				}
				next = append(next, m)
			}
		}
		acc = next
	}
	return acc
}

// getResourceKey is the resource's id: references to it (Patient/123) give the same key through getReferenceKey.
func getResourceKey(c *fhirpath.Context, focus fhirpath.Collection, _ []fhirpath.Collection, _ []string) (fhirpath.Collection, error) {
	out := fhirpath.Collection{}
	for _, it := range focus {
		if m, ok := it.V.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, fhirpath.Item{V: id, T: "string"})
			}
		}
	}
	return out, nil
}

// getReferenceKey reads a relative literal reference (Patient/123, or a full URL ending in it, with or without
// _history), optionally only of one resource type.
func getReferenceKey(c *fhirpath.Context, focus fhirpath.Collection, _ []fhirpath.Collection, raw []string) (fhirpath.Collection, error) {
	want := ""
	if len(raw) > 0 {
		want = raw[0]
	}
	out := fhirpath.Collection{}
	for _, it := range focus {
		m, ok := it.V.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := m["reference"].(string)
		if ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "urn:") {
			continue
		}
		if i := strings.Index(ref, "/_history/"); i >= 0 {
			ref = ref[:i]
		}
		p := strings.Split(strings.TrimSuffix(ref, "/"), "/")
		if len(p) < 2 {
			continue
		}
		typ, id := p[len(p)-2], p[len(p)-1]
		if want != "" && typ != want {
			continue
		}
		out = append(out, fhirpath.Item{V: id, T: "string"})
	}
	return out, nil
}
