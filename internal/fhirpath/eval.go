package fhirpath

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode"
)

// Env carries the values an expression may name with %: %resource, %rootResource and %context are set by Evaluate;
// anything else (a ViewDefinition's constants, %rowIndex) is the caller's. Functions are extra functions the caller defines
// (getResourceKey, say), consulted before the built-ins.
type Env struct {
	Vars      map[string]Collection
	Functions map[string]Function
}

// Function is an extension function: the focus, its arguments (each evaluated against the focus), and the context.
type Function func(c *Context, focus Collection, args []Collection, raw []string) (Collection, error)

// Context is what an expression is evaluated in.
type Context struct {
	env      *Env
	resource Collection
	this     Collection
	index    int
	total    Collection
}

// Resource is the resource the expression started from.
func (c *Context) Resource() Collection { return c.resource }

// Evaluate runs an expression against input (the focus, usually one resource).
func (e *Expression) Evaluate(input Collection, env *Env) (Collection, error) {
	if env == nil {
		env = &Env{}
	}
	c := &Context{env: env, resource: input, this: input}
	return c.eval(e.root, input)
}

// Eval parses and evaluates in one call.
func Eval(src string, input Collection, env *Env) (Collection, error) {
	e, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return e.Evaluate(input, env)
}

func (c *Context) eval(n node, focus Collection) (Collection, error) {
	switch x := n.(type) {
	case nLiteral:
		return x.v, nil
	case nThis:
		switch x.name {
		case "$this":
			return c.this, nil
		case "$index":
			return Collection{{V: int64(c.index), T: "System.Integer"}}, nil
		case "$total":
			return c.total, nil
		}
	case nConst:
		switch x.name {
		case "resource", "rootResource", "context":
			if v, ok := c.env.Vars[x.name]; ok {
				return v, nil
			}
			return c.resource, nil
		case "ucum":
			return Collection{{V: "http://unitsofmeasure.org", T: "System.String"}}, nil
		case "sct":
			return Collection{{V: "http://snomed.info/sct", T: "System.String"}}, nil
		case "loinc":
			return Collection{{V: "http://loinc.org", T: "System.String"}}, nil
		}
		if v, ok := c.env.Vars[x.name]; ok {
			return v, nil
		}
		return nil, evalErr("%%%s is not defined", x.name)
	case nIdent:
		return c.member(focus, x.name, true), nil
	case nInvoke:
		target := focus
		if x.target != nil {
			t, err := c.eval(x.target, focus)
			if err != nil {
				return nil, err
			}
			target = t
		}
		if !x.fn {
			return c.member(target, x.name, x.target == nil), nil
		}
		return c.call(x.name, target, x.args)
	case nIndex:
		t, err := c.eval(x.target, focus)
		if err != nil {
			return nil, err
		}
		idx, err := c.eval(x.index, c.this)
		if err != nil {
			return nil, err
		}
		if len(idx) == 0 {
			return Collection{}, nil
		}
		i, ok := asInt(idx[0])
		if !ok || len(idx) != 1 {
			return nil, evalErr("an index must be one integer")
		}
		if i < 0 || int(i) >= len(t) {
			return Collection{}, nil
		}
		return Collection{t[i]}, nil
	case nUnary:
		v, err := c.eval(x.x, focus)
		if err != nil {
			return nil, err
		}
		if x.op == "+" || len(v) == 0 {
			return v, nil
		}
		if len(v) != 1 {
			return nil, evalErr("unary minus needs one value")
		}
		switch n := v[0].V.(type) {
		case int64:
			return Collection{{V: -n, T: v[0].T}}, nil
		case Decimal:
			return Collection{{V: n.Neg(), T: v[0].T}}, nil
		case Quantity:
			return Collection{{V: Quantity{Value: n.Value.Neg(), Unit: n.Unit}, T: v[0].T}}, nil
		}
		return nil, evalErr("unary minus needs a number")
	case nType:
		v, err := c.eval(x.x, focus)
		if err != nil {
			return nil, err
		}
		return c.typeOp(x.op, v, x.name)
	case nBinary:
		return c.binary(x, focus)
	}
	return nil, evalErr("cannot evaluate %T", n)
}

// primitiveTypes are FHIR's primitive types, named as a choice suffix would be (valueDateTime means dateTime).
var primitiveTypes = map[string]bool{
	"boolean": true, "integer": true, "integer64": true, "string": true, "decimal": true, "uri": true, "url": true,
	"canonical": true, "base64Binary": true, "instant": true, "date": true, "dateTime": true, "time": true, "code": true,
	"oid": true, "id": true, "markdown": true, "unsignedInt": true, "positiveInt": true, "uuid": true, "xhtml": true,
}

func choiceType(suffix string) string {
	lower := strings.ToLower(suffix[:1]) + suffix[1:]
	if primitiveTypes[lower] {
		return lower
	}
	return suffix
}

// member navigates to a child element. atRoot lets a path begin with the focus's own type name (Patient.name).
func (c *Context) member(focus Collection, name string, atRoot bool) Collection {
	out := Collection{}
	for _, it := range focus {
		m, ok := it.V.(map[string]any)
		if !ok {
			continue
		}
		if v, ok := m[name]; ok {
			out = appendJSON(out, v, "")
			continue
		}
		if atRoot && it.T == name {
			out = append(out, it)
			continue
		}
		// A choice element: value means valueQuantity, valueString ...
		for k, v := range m {
			if len(k) > len(name) && strings.HasPrefix(k, name) && unicode.IsUpper(rune(k[len(name)])) {
				out = appendJSON(out, v, choiceType(k[len(name):]))
			}
		}
	}
	return out
}

func appendJSON(out Collection, v any, t string) Collection {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if e != nil {
				out = append(out, FromJSON(e, t))
			}
		}
	case nil:
	default:
		out = append(out, FromJSON(x, t))
	}
	return out
}

// fhirParent is the FHIR primitive type hierarchy: code is a string, url a uri ...
var fhirParent = map[string]string{
	"code": "string", "id": "string", "markdown": "string", "url": "uri", "canonical": "uri", "oid": "uri", "uuid": "uri",
	"positiveInt": "integer", "unsignedInt": "integer",
}

// systemOf is the System type a FHIR primitive is, for System.X type tests.
var systemOf = map[string]string{
	"string": "String", "uri": "String", "base64Binary": "String", "xhtml": "String", "integer": "Integer", "integer64": "Long",
	"decimal": "Decimal", "boolean": "Boolean", "date": "Date", "dateTime": "DateTime", "instant": "DateTime", "time": "Time",
	"Quantity": "Quantity",
}

func rootType(t string) string {
	for fhirParent[t] != "" {
		t = fhirParent[t]
	}
	return t
}

// typeOf is an item's type: its FHIR type when known, else inferred from its value (System types as System.X).
func typeOf(it Item) string {
	if it.T != "" {
		return it.T
	}
	switch it.V.(type) {
	case string:
		return "System.String"
	case bool:
		return "System.Boolean"
	case int64:
		return "System.Integer"
	case Decimal:
		return "System.Decimal"
	case Quantity:
		return "System.Quantity"
	case DateTime:
		return "System." + it.V.(DateTime).Kind
	}
	return ""
}

func isType(it Item, name string) bool {
	ns, bare, qualified := strings.Cut(name, ".")
	if !qualified {
		bare, ns = name, ""
	}
	t := typeOf(it)
	if strings.HasPrefix(t, "System.") {
		sys := strings.TrimPrefix(t, "System.")
		switch ns {
		case "System":
			return sys == bare
		case "FHIR":
			return systemFHIR(sys, bare, it)
		}
		return sys == bare || systemFHIR(sys, bare, it)
	}
	if ns == "System" {
		s := systemOf[rootType(t)]
		return s == bare
	}
	// FHIR, or unqualified: the type or one it derives from.
	for x := t; x != ""; x = fhirParent[x] {
		if x == bare {
			return true
		}
	}
	if bare == "Resource" || bare == "DomainResource" {
		if m, ok := it.V.(map[string]any); ok {
			_, isRes := m["resourceType"]
			return isRes
		}
	}
	// Unqualified System names (String, Integer ...) match FHIR primitives too.
	if ns == "" {
		if s := systemOf[rootType(t)]; s != "" && s == bare && strings.ToUpper(bare[:1]) == bare[:1] {
			return true
		}
	}
	return false
}

// systemFHIR says whether a value known only by its System type is the FHIR type named (a JSON string is a FHIR string).
func systemFHIR(sys, fhir string, it Item) bool {
	switch sys {
	case "String":
		return fhir == "string"
	case "Boolean":
		return fhir == "boolean"
	case "Integer":
		return fhir == "integer"
	case "Decimal":
		return fhir == "decimal"
	case "Quantity":
		return fhir == "Quantity"
	case "Date":
		return fhir == "date"
	case "DateTime":
		return fhir == "dateTime"
	case "Time":
		return fhir == "time"
	}
	return false
}

func (c *Context) typeOp(op string, v Collection, name string) (Collection, error) {
	if len(v) == 0 {
		return Collection{}, nil
	}
	if len(v) > 1 {
		return nil, evalErr("%s needs one value, not %d", op, len(v))
	}
	ok := isType(v[0], name)
	if op == "is" {
		return boolC(ok), nil
	}
	if ok {
		return v, nil
	}
	return Collection{}, nil
}

func boolC(b bool) Collection { return Collection{{V: b, T: "System.Boolean"}} }

// normalize turns a FHIR date, dateTime, instant or time held as a string into its value.
func normalize(it Item) Item {
	s, ok := it.V.(string)
	if !ok {
		return it
	}
	switch rootType(it.T) {
	case "date":
		if d, ok := ParseDateTime(s, "Date"); ok {
			return Item{V: d, T: it.T}
		}
	case "dateTime", "instant":
		if d, ok := ParseDateTime(s, "DateTime"); ok {
			return Item{V: d, T: it.T}
		}
	case "time":
		if d, ok := ParseTime(s); ok {
			return Item{V: d, T: it.T}
		}
	}
	return it
}

// coercePair lets an untyped string meet a date or time: a JSON birthDate against @1978-03-12.
func coercePair(a, b Item) (Item, Item) {
	a, b = normalize(a), normalize(b)
	if s, ok := a.V.(string); ok {
		if d, ok := b.V.(DateTime); ok {
			if p, ok := parseLike(s, d); ok {
				a = Item{V: p, T: a.T}
			}
		}
	}
	if s, ok := b.V.(string); ok {
		if d, ok := a.V.(DateTime); ok {
			if p, ok := parseLike(s, d); ok {
				b = Item{V: p, T: b.T}
			}
		}
	}
	return a, b
}

func parseLike(s string, like DateTime) (DateTime, bool) {
	if like.Kind == "Time" {
		return ParseTime(s)
	}
	d, ok := ParseDateTime(s, "")
	if ok && like.Kind == "DateTime" {
		d.Kind = "DateTime"
	}
	return d, ok
}

func asInt(it Item) (int64, bool) {
	switch x := it.V.(type) {
	case int64:
		return x, true
	case Decimal:
		return x.IsInt()
	}
	return 0, false
}

func asDecimal(it Item) (Decimal, bool) {
	switch x := it.V.(type) {
	case int64:
		return DecimalFromInt(x), true
	case Decimal:
		return x, true
	}
	return Decimal{}, false
}

// equalItems is FHIRPath =: true, false, or unknown (ok false) when precisions differ.
func equalItems(a, b Item) (bool, bool) {
	a, b = coercePair(a, b)
	switch x := a.V.(type) {
	case int64, Decimal:
		da, _ := asDecimal(a)
		db, ok := asDecimal(b)
		if !ok {
			return false, true
		}
		return da.Cmp(db) == 0, true
	case string:
		y, ok := b.V.(string)
		return ok && x == y, true
	case bool:
		y, ok := b.V.(bool)
		return ok && x == y, true
	case DateTime:
		y, ok := b.V.(DateTime)
		if !ok {
			return false, true
		}
		cmp, known := compareDT(x, y)
		if !known {
			return false, false
		}
		return cmp == 0, true
	case Quantity:
		y, ok := b.V.(Quantity)
		return ok && x.Unit == y.Unit && x.Value.Cmp(y.Value) == 0, true
	case map[string]any:
		y, ok := b.V.(map[string]any)
		return ok && reflect.DeepEqual(jsonNormal(x), jsonNormal(y)), true
	}
	return reflect.DeepEqual(a.V, b.V), true
}

func jsonNormal(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func equivalentItems(a, b Item) bool {
	a, b = coercePair(a, b)
	switch x := a.V.(type) {
	case string:
		y, ok := b.V.(string)
		return ok && strings.Join(strings.Fields(strings.ToLower(x)), " ") == strings.Join(strings.Fields(strings.ToLower(y)), " ")
	case int64, Decimal:
		da, _ := asDecimal(a)
		db, ok := asDecimal(b)
		if !ok {
			return false
		}
		// Compared at the precision of the less precise.
		s := min(da.scale, db.scale)
		return roundTo(da, s).Cmp(roundTo(db, s)) == 0
	case DateTime:
		y, ok := b.V.(DateTime)
		if !ok {
			return false
		}
		c, known := compareDT(x, y)
		return known && c == 0
	}
	eq, _ := equalItems(a, b)
	return eq
}

func roundTo(d Decimal, scale int) Decimal {
	if d.scale <= scale {
		return d
	}
	div := pow10(d.scale - scale)
	q, r := new(bigInt).QuoRem(d.u, div, new(bigInt))
	if new(bigInt).Abs(new(bigInt).Mul(r, bigTwo)).Cmp(div) >= 0 {
		if d.u.Sign() < 0 {
			q.Sub(q, bigOne)
		} else {
			q.Add(q, bigOne)
		}
	}
	return Decimal{u: q, scale: scale}
}

// compareItems is <, > ...: -1, 0, 1, or ok false when unknown (dates of different precision); an error when the two
// cannot be compared at all.
func compareItems(a, b Item) (int, bool, error) {
	a, b = coercePair(a, b)
	switch x := a.V.(type) {
	case int64, Decimal:
		da, _ := asDecimal(a)
		db, ok := asDecimal(b)
		if !ok {
			return 0, false, evalErr("a number cannot be compared with %s", typeOf(b))
		}
		return da.Cmp(db), true, nil
	case string:
		y, ok := b.V.(string)
		if !ok {
			return 0, false, evalErr("a string cannot be compared with %s", typeOf(b))
		}
		return strings.Compare(x, y), true, nil
	case DateTime:
		y, ok := b.V.(DateTime)
		if !ok {
			return 0, false, evalErr("a date cannot be compared with %s", typeOf(b))
		}
		cmp, known := compareDT(x, y)
		return cmp, known, nil
	case Quantity:
		y, ok := b.V.(Quantity)
		if !ok || x.Unit != y.Unit {
			return 0, false, nil
		}
		return x.Value.Cmp(y.Value), true, nil
	}
	return 0, false, evalErr("%s values cannot be compared", typeOf(a))
}

// boolOf is singleton evaluation as a Boolean: nil for empty, an error for more than one value.
func boolOf(v Collection) (*bool, error) {
	if len(v) == 0 {
		return nil, nil
	}
	if len(v) > 1 {
		return nil, evalErr("a single Boolean is expected, not %d values", len(v))
	}
	b, ok := v[0].V.(bool)
	if !ok {
		t := true // a single non-Boolean value counts as true
		return &t, nil
	}
	return &b, nil
}

func triple(b *bool) Collection {
	if b == nil {
		return Collection{}
	}
	return boolC(*b)
}

func (c *Context) binary(x nBinary, focus Collection) (Collection, error) {
	l, err := c.eval(x.l, focus)
	if err != nil {
		return nil, err
	}
	// The right side of and/or is still evaluated: FHIRPath has no short-circuit requirement, and errors there matter.
	r, err := c.eval(x.r, focus)
	if err != nil {
		return nil, err
	}
	switch x.op {
	case "and", "or", "xor", "implies":
		a, err := boolOf(l)
		if err != nil {
			return nil, err
		}
		b, err := boolOf(r)
		if err != nil {
			return nil, err
		}
		t, f := true, false
		switch x.op {
		case "and":
			switch {
			case (a != nil && !*a) || (b != nil && !*b):
				return triple(&f), nil
			case a != nil && b != nil:
				return triple(&t), nil
			}
			return Collection{}, nil
		case "or":
			switch {
			case (a != nil && *a) || (b != nil && *b):
				return triple(&t), nil
			case a != nil && b != nil:
				return triple(&f), nil
			}
			return Collection{}, nil
		case "xor":
			if a == nil || b == nil {
				return Collection{}, nil
			}
			v := *a != *b
			return triple(&v), nil
		case "implies":
			switch {
			case a != nil && !*a:
				return triple(&t), nil
			case b != nil && *b:
				return triple(&t), nil
			case a != nil && *a && b != nil:
				return triple(b), nil
			}
			return Collection{}, nil
		}
	case "|":
		return union(l, r), nil
	case "=", "!=":
		if len(l) == 0 || len(r) == 0 {
			return Collection{}, nil
		}
		eq := len(l) == len(r)
		for i := 0; eq && i < len(l); i++ {
			e, known := equalItems(l[i], r[i])
			if !known {
				return Collection{}, nil
			}
			eq = e
		}
		if x.op == "!=" {
			eq = !eq
		}
		return boolC(eq), nil
	case "~", "!~":
		eq := len(l) == len(r)
		for i := 0; eq && i < len(l); i++ {
			eq = equivalentItems(l[i], r[i])
		}
		if x.op == "!~" {
			eq = !eq
		}
		return boolC(eq), nil
	case "<", ">", "<=", ">=":
		if len(l) == 0 || len(r) == 0 {
			return Collection{}, nil
		}
		if len(l) > 1 || len(r) > 1 {
			return nil, evalErr("%s needs one value on each side", x.op)
		}
		cmp, known, err := compareItems(l[0], r[0])
		if err != nil {
			return nil, err
		}
		if !known {
			return Collection{}, nil
		}
		var v bool
		switch x.op {
		case "<":
			v = cmp < 0
		case ">":
			v = cmp > 0
		case "<=":
			v = cmp <= 0
		case ">=":
			v = cmp >= 0
		}
		return boolC(v), nil
	case "in", "contains":
		elem, coll := l, r
		if x.op == "contains" {
			elem, coll = r, l
		}
		if len(elem) == 0 {
			return Collection{}, nil
		}
		if len(elem) > 1 {
			return nil, evalErr("%s needs one value to look for", x.op)
		}
		for _, it := range coll {
			if eq, _ := equalItems(elem[0], it); eq {
				return boolC(true), nil
			}
		}
		return boolC(false), nil
	case "&":
		ls, rs := "", ""
		if len(l) == 1 {
			ls, _ = stringOf(l[0])
		}
		if len(r) == 1 {
			rs, _ = stringOf(r[0])
		}
		return Collection{{V: ls + rs, T: "System.String"}}, nil
	case "+", "-", "*", "/", "div", "mod":
		return arith(x.op, l, r)
	}
	return nil, evalErr("unknown operator %s", x.op)
}

func union(a, b Collection) Collection {
	out := Collection{}
	add := func(it Item) {
		for _, o := range out {
			if eq, _ := equalItems(o, it); eq {
				return
			}
		}
		out = append(out, it)
	}
	for _, it := range a {
		add(it)
	}
	for _, it := range b {
		add(it)
	}
	return out
}

func arith(op string, l, r Collection) (Collection, error) {
	if len(l) == 0 || len(r) == 0 {
		return Collection{}, nil
	}
	if len(l) > 1 || len(r) > 1 {
		return nil, evalErr("%s needs one value on each side", op)
	}
	a, b := normalize(l[0]), normalize(r[0])
	if op == "+" {
		if sa, ok := a.V.(string); ok {
			sb, ok := b.V.(string)
			if !ok {
				return nil, evalErr("a string cannot be added to %s", typeOf(b))
			}
			return Collection{{V: sa + sb, T: "System.String"}}, nil
		}
	}
	if d, ok := a.V.(DateTime); ok {
		q, ok := b.V.(Quantity)
		if !ok || (op != "+" && op != "-") {
			return nil, evalErr("a date takes + or - a time quantity")
		}
		if op == "-" {
			q.Value = q.Value.Neg()
		}
		res, ok := addDuration(d, q)
		if !ok {
			return nil, evalErr("%s is not a calendar duration", q.Unit)
		}
		return Collection{{V: res, T: a.T}}, nil
	}
	if qa, ok := a.V.(Quantity); ok {
		qb, ok := b.V.(Quantity)
		if ok && (op == "+" || op == "-") && qa.Unit == qb.Unit {
			v := qa.Value.Add(qb.Value)
			if op == "-" {
				v = qa.Value.Sub(qb.Value)
			}
			return Collection{{V: Quantity{Value: v, Unit: qa.Unit}, T: "System.Quantity"}}, nil
		}
		return Collection{}, nil
	}
	ia, aInt := a.V.(int64)
	ib, bInt := b.V.(int64)
	if aInt && bInt {
		switch op {
		case "+":
			return intC(ia + ib), nil
		case "-":
			return intC(ia - ib), nil
		case "*":
			return intC(ia * ib), nil
		case "div":
			if ib == 0 {
				return Collection{}, nil
			}
			return intC(ia / ib), nil
		case "mod":
			if ib == 0 {
				return Collection{}, nil
			}
			return intC(ia % ib), nil
		}
	}
	da, ok1 := asDecimal(a)
	db, ok2 := asDecimal(b)
	if !ok1 || !ok2 {
		return nil, evalErr("%s needs numbers, not %s and %s", op, typeOf(a), typeOf(b))
	}
	var v Decimal
	switch op {
	case "+":
		v = da.Add(db)
	case "-":
		v = da.Sub(db)
	case "*":
		v = da.Mul(db)
	case "/":
		q, ok := da.Div(db)
		if !ok {
			return Collection{}, nil
		}
		v = q
	case "div":
		q, ok := da.Div(db)
		if !ok {
			return Collection{}, nil
		}
		return intC(truncDecimal(q)), nil
	case "mod":
		q, ok := da.Div(db)
		if !ok {
			return Collection{}, nil
		}
		v = da.Sub(DecimalFromInt(truncDecimal(q)).Mul(db))
	}
	return Collection{{V: v, T: "System.Decimal"}}, nil
}

func truncDecimal(d Decimal) int64 {
	q := new(bigInt).Quo(d.u, pow10(d.scale))
	return q.Int64()
}

func intC(i int64) Collection { return Collection{{V: i, T: "System.Integer"}} }

func stringOf(it Item) (string, bool) {
	switch x := normalize(it).V.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int64:
		return formatInt(x), true
	case Decimal:
		return x.String(), true
	case DateTime:
		return x.String(), true
	case Quantity:
		return x.String(), true
	}
	return "", false
}
