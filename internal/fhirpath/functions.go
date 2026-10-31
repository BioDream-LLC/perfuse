package fhirpath

import (
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type bigInt = big.Int

var (
	bigOne = big.NewInt(1)
	bigTwo = big.NewInt(2)
)

func formatInt(i int64) string { return strconv.FormatInt(i, 10) }

// lazy functions take their arguments unevaluated, evaluated per item with $this.
var lazy = map[string]bool{"where": true, "select": true, "all": true, "exists": true, "repeat": true, "iif": true,
	"ofType": true, "is": true, "as": true, "aggregate": true, "getReferenceKey": true, "sort": true}

func (c *Context) call(name string, focus Collection, args []node) (Collection, error) {
	if f, ok := c.env.Functions[name]; ok {
		vals := make([]Collection, len(args))
		raw := make([]string, len(args))
		for i, a := range args {
			if id, ok := a.(nIdent); ok {
				raw[i] = id.name
			}
			if lazy[name] {
				continue
			}
			v, err := c.eval(a, c.this)
			if err != nil {
				return nil, err
			}
			vals[i] = v
		}
		return f(c, focus, vals, raw)
	}
	argc := func(min, max int) error {
		if len(args) < min || len(args) > max {
			if min == max {
				return evalErr("%s() takes %d argument(s), not %d", name, min, len(args))
			}
			return evalErr("%s() takes %d to %d arguments, not %d", name, min, max, len(args))
		}
		return nil
	}
	// evalArg evaluates an argument against the expression's focus ($this outside, as FHIRPath says for non-iterating
	// functions).
	evalArg := func(i int) (Collection, error) {
		if i >= len(args) {
			return nil, evalErr("%s() needs at least %d argument(s)", name, i+1)
		}
		return c.eval(args[i], c.this)
	}
	single := func() (Item, bool, error) {
		if len(focus) == 0 {
			return Item{}, false, nil
		}
		if len(focus) > 1 {
			return Item{}, false, evalErr("%s() needs one value, not %d", name, len(focus))
		}
		return focus[0], true, nil
	}
	str := func() (string, bool, error) {
		it, ok, err := single()
		if !ok || err != nil {
			return "", ok, err
		}
		s, ok := stringOf(it)
		if _, isStr := normalize(it).V.(string); !isStr {
			return "", false, evalErr("%s() needs a string", name)
		}
		return s, ok, nil
	}
	strArg := func(i int) (string, bool, error) {
		v, err := evalArg(i)
		if err != nil || len(v) == 0 {
			return "", false, err
		}
		s, ok := v[0].V.(string)
		if !ok {
			return "", false, evalErr("%s() needs a string argument", name)
		}
		return s, true, nil
	}

	switch name {
	// Existence.
	case "empty":
		if err := argc(0, 0); err != nil {
			return nil, err
		}
		return boolC(len(focus) == 0), nil
	case "exists":
		if err := argc(0, 1); err != nil {
			return nil, err
		}
		if len(args) == 1 {
			w, err := c.where(focus, args[0])
			if err != nil {
				return nil, err
			}
			return boolC(len(w) > 0), nil
		}
		return boolC(len(focus) > 0), nil
	case "all":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		for i, it := range focus {
			v, err := c.withThis(it, i, focus).eval(args[0], Collection{it})
			if err != nil {
				return nil, err
			}
			b, err := boolOf(v)
			if err != nil {
				return nil, err
			}
			if b == nil || !*b {
				return boolC(false), nil
			}
		}
		return boolC(true), nil
	case "allTrue", "anyTrue", "allFalse", "anyFalse":
		want := strings.Contains(name, "True")
		any := strings.HasPrefix(name, "any")
		for _, it := range focus {
			b, ok := it.V.(bool)
			if !ok {
				return nil, evalErr("%s() needs Booleans", name)
			}
			if any && b == want {
				return boolC(true), nil
			}
			if !any && b != want {
				return boolC(false), nil
			}
		}
		return boolC(!any), nil
	case "subsetOf", "supersetOf":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		other, err := evalArg(0)
		if err != nil {
			return nil, err
		}
		a, b := focus, other
		if name == "supersetOf" {
			a, b = other, focus
		}
		for _, x := range a {
			if !containsItem(b, x) {
				return boolC(false), nil
			}
		}
		return boolC(true), nil
	case "count":
		return intC(int64(len(focus))), nil
	case "distinct":
		return union(focus, nil), nil
	case "isDistinct":
		return boolC(len(union(focus, nil)) == len(focus)), nil

	// Filtering and projection.
	case "where":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		return c.where(focus, args[0])
	case "select":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		out := Collection{}
		for i, it := range focus {
			v, err := c.withThis(it, i, focus).eval(args[0], Collection{it})
			if err != nil {
				return nil, err
			}
			out = append(out, v...)
		}
		return out, nil
	case "repeat":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		out := Collection{}
		queue := focus
		for len(queue) > 0 && len(out) < 100000 {
			var next Collection
			for i, it := range queue {
				v, err := c.withThis(it, i, queue).eval(args[0], Collection{it})
				if err != nil {
					return nil, err
				}
				for _, x := range v {
					if !containsItem(out, x) {
						out = append(out, x)
						next = append(next, x)
					}
				}
			}
			queue = next
		}
		return out, nil
	case "ofType", "is", "as":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		tname, err := typeName(args[0])
		if err != nil {
			return nil, err
		}
		if name != "ofType" {
			return c.typeOp(name, focus, tname)
		}
		out := Collection{}
		for _, it := range focus {
			if isType(it, tname) {
				out = append(out, it)
			}
		}
		return out, nil

	// Subsetting.
	case "single":
		if len(focus) > 1 {
			return nil, evalErr("single() found %d values", len(focus))
		}
		return focus, nil
	case "first":
		if len(focus) == 0 {
			return Collection{}, nil
		}
		return focus[:1], nil
	case "last":
		if len(focus) == 0 {
			return Collection{}, nil
		}
		return focus[len(focus)-1:], nil
	case "tail":
		if len(focus) <= 1 {
			return Collection{}, nil
		}
		return focus[1:], nil
	case "skip", "take":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		v, err := evalArg(0)
		if err != nil {
			return nil, err
		}
		if len(v) != 1 {
			return nil, evalErr("%s() needs an integer", name)
		}
		n, ok := asInt(v[0])
		if !ok {
			return nil, evalErr("%s() needs an integer", name)
		}
		if name == "skip" {
			if n <= 0 {
				return focus, nil
			}
			if int(n) >= len(focus) {
				return Collection{}, nil
			}
			return focus[n:], nil
		}
		if n <= 0 {
			return Collection{}, nil
		}
		if int(n) >= len(focus) {
			return focus, nil
		}
		return focus[:n], nil
	case "intersect", "exclude", "union", "combine":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		other, err := evalArg(0)
		if err != nil {
			return nil, err
		}
		switch name {
		case "union":
			return union(focus, other), nil
		case "combine":
			return append(append(Collection{}, focus...), other...), nil
		case "intersect":
			out := Collection{}
			for _, it := range focus {
				if containsItem(other, it) && !containsItem(out, it) {
					out = append(out, it)
				}
			}
			return out, nil
		}
		out := Collection{}
		for _, it := range focus {
			if !containsItem(other, it) {
				out = append(out, it)
			}
		}
		return out, nil

	case "iif":
		if err := argc(2, 3); err != nil {
			return nil, err
		}
		cond, err := c.eval(args[0], focus)
		if err != nil {
			return nil, err
		}
		b, err := boolOf(cond)
		if err != nil {
			return nil, err
		}
		if b != nil && *b {
			return c.eval(args[1], focus)
		}
		if len(args) == 3 {
			return c.eval(args[2], focus)
		}
		return Collection{}, nil
	case "not":
		b, err := boolOf(focus)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return Collection{}, nil
		}
		return boolC(!*b), nil

	// Conversion.
	case "toString":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		s, ok := stringOf(it)
		if !ok {
			return Collection{}, nil
		}
		return Collection{{V: s, T: "System.String"}}, nil
	case "convertsToString":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		_, ok = stringOf(it)
		return boolC(ok), nil
	case "toInteger", "convertsToInteger":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		var v *int64
		switch x := it.V.(type) {
		case int64:
			v = &x
		case bool:
			i := int64(0)
			if x {
				i = 1
			}
			v = &i
		case string:
			if i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
				v = &i
			}
		}
		if name == "convertsToInteger" {
			return boolC(v != nil), nil
		}
		if v == nil {
			return Collection{}, nil
		}
		return intC(*v), nil
	case "toDecimal", "convertsToDecimal":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		var v *Decimal
		switch x := it.V.(type) {
		case int64:
			d := DecimalFromInt(x)
			v = &d
		case Decimal:
			v = &x
		case bool:
			d := DecimalFromInt(map[bool]int64{true: 1, false: 0}[x])
			v = &d
		case string:
			if d, ok := ParseDecimal(x); ok && !strings.ContainsAny(x, "eE") {
				v = &d
			}
		}
		if name == "convertsToDecimal" {
			return boolC(v != nil), nil
		}
		if v == nil {
			return Collection{}, nil
		}
		return Collection{{V: *v, T: "System.Decimal"}}, nil
	case "toBoolean", "convertsToBoolean":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		var v *bool
		t, f := true, false
		switch x := it.V.(type) {
		case bool:
			v = &x
		case int64:
			if x == 1 {
				v = &t
			} else if x == 0 {
				v = &f
			}
		case Decimal:
			if x.Cmp(DecimalFromInt(1)) == 0 {
				v = &t
			} else if x.Cmp(DecimalFromInt(0)) == 0 {
				v = &f
			}
		case string:
			switch strings.ToLower(x) {
			case "true", "t", "yes", "y", "1", "1.0":
				v = &t
			case "false", "f", "no", "n", "0", "0.0":
				v = &f
			}
		}
		if name == "convertsToBoolean" {
			return boolC(v != nil), nil
		}
		return triple(v), nil
	case "toDate", "toDateTime", "toTime", "convertsToDate", "convertsToDateTime", "convertsToTime":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		kind := strings.TrimPrefix(strings.TrimPrefix(name, "convertsTo"), "to")
		var out *DateTime
		switch x := normalize(it).V.(type) {
		case DateTime:
			if x.Kind == kind || (kind == "DateTime" && x.Kind == "Date") {
				y := x
				y.Kind = kind
				out = &y
			} else if kind == "Date" && x.Kind == "DateTime" {
				y := x.truncate(min(x.Precision, PDay))
				y.Kind, y.HasZone = "Date", false
				out = &y
			}
		case string:
			var d DateTime
			var ok bool
			if kind == "Time" {
				d, ok = ParseTime(x)
			} else {
				d, ok = ParseDateTime(x, kind)
				if ok && kind == "Date" && d.Precision > PDay {
					ok = false
				}
			}
			if ok {
				out = &d
			}
		}
		if strings.HasPrefix(name, "convertsTo") {
			return boolC(out != nil), nil
		}
		if out == nil {
			return Collection{}, nil
		}
		return Collection{{V: *out, T: "System." + kind}}, nil
	case "toQuantity", "convertsToQuantity":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		var q *Quantity
		switch x := it.V.(type) {
		case int64, Decimal:
			d, _ := asDecimal(it)
			q = &Quantity{Value: d, Unit: "1"}
		case Quantity:
			q = &x
		case map[string]any:
			if v, ok := x["value"]; ok {
				d, _ := asDecimal(FromJSON(v, ""))
				u, _ := x["code"].(string)
				if u == "" {
					u, _ = x["unit"].(string)
				}
				q = &Quantity{Value: d, Unit: u}
			}
		}
		if strings.HasPrefix(name, "convertsTo") {
			return boolC(q != nil), nil
		}
		if q == nil {
			return Collection{}, nil
		}
		return Collection{{V: *q, T: "System.Quantity"}}, nil

	// Strings.
	case "indexOf", "startsWith", "endsWith", "contains", "matches", "matchesFull", "replace", "replaceMatches", "split":
		want := 1
		if name == "replace" || name == "replaceMatches" {
			want = 2
		}
		if err := argc(want, want); err != nil {
			return nil, err
		}
		s, ok, err := str()
		if !ok || err != nil {
			return Collection{}, err
		}
		a, okA, err := strArg(0)
		if err != nil || !okA {
			return Collection{}, err
		}
		switch name {
		case "indexOf":
			i := strings.Index(s, a)
			if i >= 0 {
				i = utf8.RuneCountInString(s[:i])
			}
			return intC(int64(i)), nil
		case "startsWith":
			return boolC(strings.HasPrefix(s, a)), nil
		case "endsWith":
			return boolC(strings.HasSuffix(s, a)), nil
		case "contains":
			return boolC(strings.Contains(s, a)), nil
		case "matches", "matchesFull":
			pat := a
			if name == "matchesFull" {
				pat = "^(?:" + a + ")$"
			}
			re, err := regexp.Compile("(?s)" + pat)
			if err != nil {
				return nil, evalErr("%q is not a regular expression", a)
			}
			return boolC(re.MatchString(s)), nil
		case "split":
			out := Collection{}
			for _, p := range strings.Split(s, a) {
				out = append(out, Item{V: p, T: "System.String"})
			}
			return out, nil
		}
		b, okB, err := strArg(1)
		if err != nil || !okB {
			return Collection{}, err
		}
		if name == "replace" {
			return Collection{{V: strings.ReplaceAll(s, a, b), T: "System.String"}}, nil
		}
		re, err := regexp.Compile(a)
		if err != nil {
			return nil, evalErr("%q is not a regular expression", a)
		}
		// The substitution refers to groups as $1 or ${name}, which is the FHIRPath meaning and Go's; the self-replace of "$"
		// that wrapped it here did nothing.
		return Collection{{V: re.ReplaceAllString(s, b), T: "System.String"}}, nil
	case "substring":
		if err := argc(1, 2); err != nil {
			return nil, err
		}
		s, ok, err := str()
		if !ok || err != nil {
			return Collection{}, err
		}
		v, err := evalArg(0)
		if err != nil || len(v) == 0 {
			return Collection{}, err
		}
		start, _ := asInt(v[0])
		r := []rune(s)
		if start < 0 || int(start) >= len(r) {
			return Collection{}, nil
		}
		end := int64(len(r))
		if len(args) == 2 {
			l, err := evalArg(1)
			if err != nil {
				return nil, err
			}
			if len(l) == 1 {
				n, _ := asInt(l[0])
				end = min(start+max(n, 0), int64(len(r)))
			}
		}
		return Collection{{V: string(r[start:end]), T: "System.String"}}, nil
	case "upper", "lower", "trim", "length", "toChars":
		s, ok, err := str()
		if !ok || err != nil {
			return Collection{}, err
		}
		switch name {
		case "upper":
			return Collection{{V: strings.ToUpper(s), T: "System.String"}}, nil
		case "lower":
			return Collection{{V: strings.ToLower(s), T: "System.String"}}, nil
		case "trim":
			return Collection{{V: strings.TrimSpace(s), T: "System.String"}}, nil
		case "length":
			return intC(int64(utf8.RuneCountInString(s))), nil
		}
		out := Collection{}
		for _, r := range s {
			out = append(out, Item{V: string(r), T: "System.String"})
		}
		return out, nil
	case "join":
		if err := argc(0, 1); err != nil {
			return nil, err
		}
		sep := ""
		if len(args) == 1 {
			s, ok, err := strArg(0)
			if err != nil {
				return nil, err
			}
			if ok {
				sep = s
			}
		}
		parts := make([]string, 0, len(focus))
		for _, it := range focus {
			s, ok := stringOf(it)
			if !ok {
				return nil, evalErr("join() needs strings")
			}
			parts = append(parts, s)
		}
		if len(focus) == 0 {
			return Collection{}, nil
		}
		return Collection{{V: strings.Join(parts, sep), T: "System.String"}}, nil

	// Math.
	case "abs", "ceiling", "floor", "round", "truncate", "sqrt", "exp", "ln", "log", "power":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		if q, isQ := it.V.(Quantity); isQ && name == "abs" {
			v := q.Value
			if v.u.Sign() < 0 {
				v = v.Neg()
			}
			return Collection{{V: Quantity{Value: v, Unit: q.Unit}, T: it.T}}, nil
		}
		d, okD := asDecimal(it)
		if !okD {
			return nil, evalErr("%s() needs a number", name)
		}
		_, isInt := it.V.(int64)
		switch name {
		case "abs":
			if d.u.Sign() < 0 {
				d = d.Neg()
			}
			if isInt {
				i, _ := d.IsInt()
				return intC(i), nil
			}
			return Collection{{V: d, T: "System.Decimal"}}, nil
		case "ceiling", "floor", "truncate":
			f := d.Float()
			v := map[string]func(float64) float64{"ceiling": math.Ceil, "floor": math.Floor, "truncate": math.Trunc}[name](f)
			return intC(int64(v)), nil
		case "round":
			p := int64(0)
			if len(args) == 1 {
				v, err := evalArg(0)
				if err != nil {
					return nil, err
				}
				if len(v) == 1 {
					p, _ = asInt(v[0])
				}
			}
			return Collection{{V: roundTo(d, int(p)), T: "System.Decimal"}}, nil
		}
		f := d.Float()
		var r float64
		switch name {
		case "sqrt":
			if f < 0 {
				return Collection{}, nil
			}
			r = math.Sqrt(f)
		case "exp":
			r = math.Exp(f)
		case "ln":
			r = math.Log(f)
		case "log", "power":
			v, err := evalArg(0)
			if err != nil || len(v) == 0 {
				return Collection{}, err
			}
			b, _ := asDecimal(v[0])
			if name == "log" {
				r = math.Log(f) / math.Log(b.Float())
			} else {
				r = math.Pow(f, b.Float())
				if _, bi := v[0].V.(int64); isInt && bi && !math.IsNaN(r) {
					return intC(int64(r)), nil
				}
			}
		}
		if math.IsNaN(r) || math.IsInf(r, 0) {
			return Collection{}, nil
		}
		out, _ := ParseDecimal(strconv.FormatFloat(r, 'f', -1, 64))
		return Collection{{V: out, T: "System.Decimal"}}, nil

	// Tree navigation.
	case "children":
		out := Collection{}
		for _, it := range focus {
			if m, ok := it.V.(map[string]any); ok {
				keys := make([]string, 0, len(m))
				for k := range m {
					if k != "resourceType" && !strings.HasPrefix(k, "_") {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)
				for _, k := range keys {
					out = appendJSON(out, m[k], "")
				}
			}
		}
		return out, nil
	case "descendants":
		out := Collection{}
		queue := focus
		for len(queue) > 0 {
			ch, _ := c.call("children", queue, nil)
			out = append(out, ch...)
			queue = ch
		}
		return out, nil

	// FHIR.
	case "extension":
		if err := argc(1, 1); err != nil {
			return nil, err
		}
		url, ok, err := strArg(0)
		if err != nil || !ok {
			return Collection{}, err
		}
		out := Collection{}
		for _, ext := range c.member(focus, "extension", false) {
			if m, ok := ext.V.(map[string]any); ok && m["url"] == url {
				out = append(out, Item{V: m, T: "Extension"})
			}
		}
		return out, nil
	case "hasValue":
		if len(focus) != 1 {
			return boolC(false), nil
		}
		_, isObj := focus[0].V.(map[string]any)
		return boolC(!isObj), nil
	case "getValue":
		if len(focus) != 1 {
			return Collection{}, nil
		}
		if _, isObj := focus[0].V.(map[string]any); isObj {
			return Collection{}, nil
		}
		return focus, nil
	case "lowBoundary", "highBoundary":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		high := name == "highBoundary"
		var prec *int
		if len(args) == 1 {
			v, err := evalArg(0)
			if err != nil {
				return nil, err
			}
			if len(v) == 1 {
				p, _ := asInt(v[0])
				pi := int(p)
				prec = &pi
			}
		}
		it = normalize(it)
		if str, isStr := it.V.(string); isStr {
			// No model says birthDate is a date, so a string that reads as one is taken as one.
			if d, ok := ParseDateTime(str, ""); ok {
				it = Item{V: d, T: it.T}
			} else if d, ok := ParseTime(str); ok && strings.Contains(str, ":") {
				it = Item{V: d, T: it.T}
			}
		}
		return boundary(it, high, prec)
	case "precision":
		it, ok, err := single()
		if !ok || err != nil {
			return Collection{}, err
		}
		switch x := normalize(it).V.(type) {
		case Decimal:
			return intC(int64(x.scale)), nil
		case int64:
			return intC(0), nil
		case DateTime:
			return intC(int64(dtDigits(x))), nil
		}
		return Collection{}, nil

	// Utility.
	case "now":
		return Collection{{V: fromTime(time.Now(), "DateTime"), T: "System.DateTime"}}, nil
	case "today":
		d := fromTime(time.Now(), "Date")
		return Collection{{V: d.truncate(PDay), T: "System.Date"}}, nil
	case "timeOfDay":
		return Collection{{V: fromTime(time.Now(), "Time"), T: "System.Time"}}, nil
	case "trace":
		return focus, nil
	case "type":
		out := Collection{}
		for _, it := range focus {
			t := typeOf(it)
			ns, n, ok := strings.Cut(t, ".")
			if !ok {
				ns, n = "FHIR", t
			}
			out = append(out, Item{V: map[string]any{"namespace": ns, "name": n}, T: "TypeInfo"})
		}
		return out, nil
	}
	return nil, evalErr("%s() is not a FHIRPath function this engine knows", name)
}

func containsItem(c Collection, it Item) bool {
	for _, x := range c {
		if eq, _ := equalItems(x, it); eq {
			return true
		}
	}
	return false
}

// typeName reads a type specifier given as a function argument: ofType(Quantity), ofType(FHIR.Quantity).
func typeName(n node) (string, error) {
	switch x := n.(type) {
	case nIdent:
		return x.name, nil
	case nInvoke:
		if !x.fn {
			if id, ok := x.target.(nIdent); ok {
				return id.name + "." + x.name, nil
			}
		}
	}
	return "", evalErr("a type name is expected")
}

func (c *Context) withThis(it Item, i int, total Collection) *Context {
	return &Context{env: c.env, resource: c.resource, this: Collection{it}, index: i, total: total}
}

func (c *Context) where(focus Collection, crit node) (Collection, error) {
	out := Collection{}
	for i, it := range focus {
		v, err := c.withThis(it, i, focus).eval(crit, Collection{it})
		if err != nil {
			return nil, err
		}
		b, err := boolOf(v)
		if err != nil {
			return nil, err
		}
		if b != nil && *b {
			out = append(out, it)
		}
	}
	return out, nil
}

func fromTime(t time.Time, kind string) DateTime {
	_, off := t.Zone()
	return DateTime{Kind: kind, Year: t.Year(), Month: int(t.Month()), Day: t.Day(), Hour: t.Hour(), Minute: t.Minute(),
		Second: t.Second(), Milli: t.Nanosecond() / 1e6, Precision: PMillisecond, HasZone: kind != "Time", ZoneMinutes: off / 60}
}

func dtDigits(d DateTime) int {
	switch d.Precision {
	case PYear:
		if d.Kind == "Time" {
			return 2
		}
		return 4
	case PMonth:
		return 6
	case PDay:
		return 8
	case PHour:
		if d.Kind == "Time" {
			return 2
		}
		return 10
	case PMinute:
		if d.Kind == "Time" {
			return 4
		}
		return 12
	case PSecond:
		if d.Kind == "Time" {
			return 6
		}
		return 14
	}
	if d.Kind == "Time" {
		return 9
	}
	return 17
}

// boundary is lowBoundary or highBoundary: the least or greatest value the written precision allows.
func boundary(it Item, high bool, prec *int) (Collection, error) {
	switch x := it.V.(type) {
	case int64:
		d := DecimalFromInt(x)
		return decimalBoundary(d, high, prec), nil
	case Decimal:
		return decimalBoundary(x, high, prec), nil
	case Quantity:
		b := decimalBoundary(x.Value, high, prec)
		if len(b) == 0 {
			return b, nil
		}
		return Collection{{V: Quantity{Value: b[0].V.(Decimal), Unit: x.Unit}, T: it.T}}, nil
	case DateTime:
		target := PMillisecond
		if x.Kind == "Date" {
			target = PDay
		}
		if prec != nil {
			switch *prec {
			case 4:
				target = PYear
			case 6:
				target = PMonth
			case 8:
				target = PDay
			case 10:
				target = PHour
			case 12:
				target = PMinute
			case 14:
				target = PSecond
			case 17:
				target = PMillisecond
			default:
				return Collection{}, nil
			}
		}
		d := x
		fill := func(p Precision, lo, hi int, f *int) {
			if d.Precision < p && p <= target {
				if high {
					*f = hi
				} else {
					*f = lo
				}
			}
		}
		fill(PMonth, 1, 12, &d.Month)
		if d.Precision < PDay && PDay <= target {
			if high {
				d.Day = daysIn(d.Year, d.Month)
			} else {
				d.Day = 1
			}
		}
		fill(PHour, 0, 23, &d.Hour)
		fill(PMinute, 0, 59, &d.Minute)
		fill(PSecond, 0, 59, &d.Second)
		fill(PMillisecond, 0, 999, &d.Milli)
		if x.Kind == "Time" && x.Precision < PMillisecond && target == PMillisecond {
			fill(PMillisecond, 0, 999, &d.Milli)
		}
		if target > d.Precision {
			d.Precision = target
		} else {
			d = d.truncate(target)
		}
		if x.Kind == "DateTime" && !x.HasZone && target > PDay {
			// No zone: the boundary spans every zone, +14:00 to -12:00.
			d.HasZone = true
			d.ZoneMinutes = 14 * 60
			if high {
				d.ZoneMinutes = -12 * 60
			}
		}
		return Collection{{V: d, T: it.T}}, nil
	}
	return Collection{}, nil
}

func decimalBoundary(d Decimal, high bool, prec *int) Collection {
	p := 8
	if prec != nil {
		p = *prec
	}
	if p < 0 || p > 28 {
		return Collection{}
	}
	// Half a unit in the last written place, either way.
	half := Decimal{u: big.NewInt(5), scale: d.scale + 1}
	var v Decimal
	if high {
		v = d.Add(half)
	} else {
		v = d.Sub(half)
	}
	if v.scale < p {
		v = Decimal{u: v.rescale(p), scale: p}
	} else if v.scale > p {
		v = truncateTo(v, p, high)
	}
	return Collection{{V: v.trim(), T: "System.Decimal"}}
}

func truncateTo(d Decimal, p int, up bool) Decimal {
	div := pow10(d.scale - p)
	q, r := new(big.Int).QuoRem(d.u, div, new(big.Int))
	if r.Sign() != 0 && up == (d.u.Sign() > 0) {
		if up {
			q.Add(q, bigOne)
		} else {
			q.Sub(q, bigOne)
		}
	}
	return Decimal{u: q, scale: p}
}

// addDuration adds a calendar duration to a date or time.
func addDuration(d DateTime, q Quantity) (DateTime, bool) {
	n, isInt := q.Value.IsInt()
	unit := strings.Trim(q.Unit, "'")
	if mapped, ok := map[string]string{"a": "year", "mo": "month", "wk": "week", "d": "day", "h": "hour", "min": "minute",
		"s": "second", "ms": "millisecond"}[unit]; ok {
		unit = mapped
	}
	unit = strings.TrimSuffix(unit, "s")
	if !isInt && unit != "second" {
		return d, false
	}
	if d.Kind == "Time" {
		t := time.Date(2000, 1, 1, d.Hour, d.Minute, d.Second, d.Milli*1e6, time.UTC)
		switch unit {
		case "hour":
			t = t.Add(time.Duration(n) * time.Hour)
		case "minute":
			t = t.Add(time.Duration(n) * time.Minute)
		case "second":
			t = t.Add(time.Duration(q.Value.Float() * float64(time.Second)))
		case "millisecond":
			t = t.Add(time.Duration(n) * time.Millisecond)
		default:
			return d, false
		}
		out := d
		out.Hour, out.Minute, out.Second, out.Milli = t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1e6
		return out, true
	}
	m, day := max(d.Month, 1), max(d.Day, 1)
	t := time.Date(d.Year, time.Month(m), day, d.Hour, d.Minute, d.Second, d.Milli*1e6, time.UTC)
	switch unit {
	case "year":
		t2 := t.AddDate(int(n), 0, 0)
		if t2.Day() != t.Day() {
			t2 = time.Date(t2.Year(), t2.Month(), 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
		}
		t = t2
	case "month":
		t2 := t.AddDate(0, int(n), 0)
		if t2.Day() != t.Day() {
			t2 = time.Date(t2.Year(), t2.Month(), 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
		}
		t = t2
	case "week":
		t = t.AddDate(0, 0, 7*int(n))
	case "day":
		t = t.AddDate(0, 0, int(n))
	case "hour":
		t = t.Add(time.Duration(n) * time.Hour)
	case "minute":
		t = t.Add(time.Duration(n) * time.Minute)
	case "second":
		t = t.Add(time.Duration(q.Value.Float() * float64(time.Second)))
	case "millisecond":
		t = t.Add(time.Duration(n) * time.Millisecond)
	default:
		return d, false
	}
	out := d
	out.Year = t.Year()
	if d.Precision >= PMonth {
		out.Month = int(t.Month())
	}
	if d.Precision >= PDay {
		out.Day = t.Day()
	}
	out.Hour, out.Minute, out.Second, out.Milli = t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1e6
	return out, true
}
