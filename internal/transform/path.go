package transform

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Path addresses a place in a message using the same notation as the filter
// language, so that a channel does not need two ways of naming a field.
//
//	PID-5.1          the first component of PID-5
//	PID-3(2).1       the first component of the second repetition of PID-3
//	OBX(3)-5         the fifth field of the third OBX segment
//	MSH-9.2          the trigger event
//
// The one departure from the filter language is that a path used as a
// transformation target may need to create what it names, which reading never
// does. Creation is why the parts are kept separately rather than resolved to a
// node in one step.
type Path struct {
	Segment      string
	Occurrence   int // 1-based; 0 means every occurrence
	Field        int
	Repetition   int // 1-based; 0 means every repetition
	Component    int // 0 means the field itself
	Subcomponent int // 0 means the component itself

	raw string
}

// String returns the path as written.
func (p Path) String() string { return p.raw }

// ParsePath reads the notation above.
func ParsePath(s string) (Path, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Path{}, fmt.Errorf("path is empty")
	}

	p := Path{raw: raw}
	rest := raw

	// Segment name, with an optional occurrence.
	dash := strings.IndexByte(rest, '-')
	segmentPart := rest
	if dash >= 0 {
		segmentPart = rest[:dash]
		rest = rest[dash+1:]
	} else {
		rest = ""
	}

	if open := strings.IndexByte(segmentPart, '('); open >= 0 {
		if !strings.HasSuffix(segmentPart, ")") {
			return Path{}, fmt.Errorf("path %q: segment occurrence is missing its closing bracket", raw)
		}
		n, err := strconv.Atoi(segmentPart[open+1 : len(segmentPart)-1])
		if err != nil || n < 1 {
			return Path{}, fmt.Errorf("path %q: segment occurrence must be a positive number", raw)
		}
		p.Occurrence = n
		segmentPart = segmentPart[:open]
	}

	p.Segment = strings.ToUpper(strings.TrimSpace(segmentPart))
	if len(p.Segment) != 3 {
		return Path{}, fmt.Errorf("path %q: %q is not a three-character segment name", raw, p.Segment)
	}

	if rest == "" {
		// A bare segment is a valid target for remove.
		return p, nil
	}

	// Field, with an optional repetition, then component and subcomponent.
	parts := strings.Split(rest, ".")

	fieldPart := parts[0]
	if open := strings.IndexByte(fieldPart, '('); open >= 0 {
		if !strings.HasSuffix(fieldPart, ")") {
			return Path{}, fmt.Errorf("path %q: field repetition is missing its closing bracket", raw)
		}
		n, err := strconv.Atoi(fieldPart[open+1 : len(fieldPart)-1])
		if err != nil || n < 1 {
			return Path{}, fmt.Errorf("path %q: field repetition must be a positive number", raw)
		}
		p.Repetition = n
		fieldPart = fieldPart[:open]
	}

	field, err := strconv.Atoi(fieldPart)
	if err != nil || field < 1 {
		return Path{}, fmt.Errorf("path %q: %q is not a field number", raw, fieldPart)
	}
	p.Field = field

	if len(parts) > 1 {
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 {
			return Path{}, fmt.Errorf("path %q: %q is not a component number", raw, parts[1])
		}
		p.Component = n
	}
	if len(parts) > 2 {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			return Path{}, fmt.Errorf("path %q: %q is not a subcomponent number", raw, parts[2])
		}
		p.Subcomponent = n
	}
	if len(parts) > 3 {
		return Path{}, fmt.Errorf("path %q: HL7 has no level below a subcomponent", raw)
	}

	return p, nil
}

// resolve finds the node a path names, optionally creating what is missing.
func (p Path) resolve(root *xtree.Node, create bool) *xtree.Node {
	occurrence := p.Occurrence
	if occurrence < 1 {
		occurrence = 1
	}

	segment := root.Child(p.Segment, occurrence-1)
	if segment == nil {
		if !create {
			return nil
		}
		segment = root.Ensure(p.Segment, occurrence-1)
	}
	if p.Field == 0 {
		return segment
	}

	fieldName := fmt.Sprintf("%s.%d", p.Segment, p.Field)
	repetition := p.Repetition
	if repetition < 1 {
		repetition = 1
	}

	field := segment.Child(fieldName, repetition-1)
	if field == nil {
		if !create {
			return nil
		}
		field = segment.Ensure(fieldName, repetition-1)
	}

	// A field with no component named still resolves to its first component,
	// because that is where the value lives in the Mirth-shaped tree and a
	// caller writing to "PID-5" means the value, not the wrapper.
	component := p.Component
	if component < 1 {
		component = 1
	}
	componentName := fmt.Sprintf("%s.%d", fieldName, component)
	comp := field.First(componentName)
	if comp == nil {
		if !create {
			// Reading an absent component when the field itself holds text
			// directly, which happens for MSH-1 and MSH-2.
			if p.Component == 0 && field.Simple() {
				return field
			}
			return nil
		}
		comp = field.Ensure(componentName, 0)
	}

	if p.Subcomponent == 0 {
		return comp
	}

	subName := fmt.Sprintf("%s.%d", componentName, p.Subcomponent)
	sub := comp.First(subName)
	if sub == nil {
		if !create {
			return nil
		}
		sub = comp.Ensure(subName, 0)
	}
	return sub
}

// resolveAll finds every node a path names.
//
// A path that does not say which occurrence or repetition it means addresses all
// of them, which matches the filter language: there, a path naming no repetition
// tests every repetition. Keeping the two consistent matters because the same
// path string is frequently used in a filter and then in a transformation, and
// having it mean different things in the two places would be a trap.
func (p Path) resolveAll(root *xtree.Node) []*xtree.Node {
	var segments []*xtree.Node
	if p.Occurrence > 0 {
		if seg := root.Child(p.Segment, p.Occurrence-1); seg != nil {
			segments = []*xtree.Node{seg}
		}
	} else {
		segments = root.All(p.Segment)
	}
	if len(segments) == 0 {
		return nil
	}
	if p.Field == 0 {
		return segments
	}

	fieldName := fmt.Sprintf("%s.%d", p.Segment, p.Field)
	var fields []*xtree.Node
	for _, seg := range segments {
		if p.Repetition > 0 {
			if f := seg.Child(fieldName, p.Repetition-1); f != nil {
				fields = append(fields, f)
			}
			continue
		}
		fields = append(fields, seg.All(fieldName)...)
	}
	if len(fields) == 0 {
		return nil
	}

	component := p.Component
	if component < 1 {
		component = 1
	}
	componentName := fmt.Sprintf("%s.%d", fieldName, component)

	var comps []*xtree.Node
	for _, f := range fields {
		if c := f.First(componentName); c != nil {
			comps = append(comps, c)
			continue
		}
		if p.Component == 0 && f.Simple() {
			comps = append(comps, f)
		}
	}
	if p.Subcomponent == 0 {
		return comps
	}

	subName := fmt.Sprintf("%s.%d", componentName, p.Subcomponent)
	var subs []*xtree.Node
	for _, c := range comps {
		if s := c.First(subName); s != nil {
			subs = append(subs, s)
		}
	}
	return subs
}

// condition is the small comparison language used by a step's "when".
//
// It is deliberately not the full filter expression language: a transformation
// condition guards one step, and importing the filter parser here would tie the
// declarative layer to it. The syntax is the same subset people actually write:
//
//	PID-8 == "F"
//	MSH-9.2 != "A28"
//	PID-3.1 exists
//	OBX-5 empty
type condition struct {
	path  Path
	op    string
	value string
}

func parseCondition(s string) (*condition, error) {
	text := strings.TrimSpace(s)

	for _, op := range []string{"==", "!=", "contains", "matches", "exists", "empty"} {
		idx := indexToken(text, op)
		if idx < 0 {
			continue
		}

		left := strings.TrimSpace(text[:idx])
		right := strings.TrimSpace(text[idx+len(op):])

		path, err := ParsePath(left)
		if err != nil {
			return nil, err
		}

		switch op {
		case "exists", "empty":
			if right != "" {
				return nil, fmt.Errorf("%q takes no value, but %q follows it", op, right)
			}
			return &condition{path: path, op: op}, nil
		}

		value, err := unquote(right)
		if err != nil {
			return nil, err
		}
		return &condition{path: path, op: op, value: value}, nil
	}

	return nil, fmt.Errorf("condition %q: expected one of ==, !=, contains, matches, exists, empty", s)
}

// indexToken finds an operator outside a quoted string, and requires a word
// operator to stand alone so that "containsX" is not read as "contains".
func indexToken(s, op string) int {
	word := op[0] >= 'a' && op[0] <= 'z'
	inQuote := byte(0)

	for i := 0; i+len(op) <= len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = c
			continue
		}
		if s[i:i+len(op)] != op {
			continue
		}
		if word {
			if i > 0 && isWordByte(s[i-1]) {
				continue
			}
			if i+len(op) < len(s) && isWordByte(s[i+len(op)]) {
				continue
			}
		}
		return i
	}
	return -1
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func unquote(s string) (string, error) {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1], nil
		}
	}
	if s == "" {
		return "", fmt.Errorf("a value is required")
	}
	// An unquoted value is accepted, because people write PID-8 == F.
	return s, nil
}

func (c *condition) eval(root *xtree.Node) (bool, error) {
	nodes := c.path.resolveAll(root)

	switch c.op {
	case "exists":
		for _, n := range nodes {
			if n.Value() != "" {
				return true, nil
			}
		}
		return false, nil

	case "empty":
		for _, n := range nodes {
			if n.Value() != "" {
				return false, nil
			}
		}
		return true, nil
	}

	// A comparison holds when any addressed repetition matches, and "!=" holds
	// when none does. This mirrors the filter language, where != means no
	// repetition matches rather than the first one differing.
	matchedAny := false
	for _, n := range nodes {
		value := n.Value()
		var hit bool
		switch c.op {
		case "==":
			hit = value == c.value
		case "!=":
			hit = value == c.value // inverted below
		case "contains":
			hit = strings.Contains(value, c.value)
		case "matches":
			re, err := compileCached(c.value)
			if err != nil {
				return false, err
			}
			hit = re.MatchString(value)
		}
		if hit {
			matchedAny = true
			break
		}
	}

	if c.op == "!=" {
		return !matchedAny, nil
	}
	return matchedAny, nil
}

// compileCached compiles a regular expression, memoised because a "matches"
// condition is evaluated once per message and recompiling is most of its cost.
var regexCache sync.Map

func compileCached(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
		return nil, v.(error)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		regexCache.Store(pattern, err)
		return nil, fmt.Errorf("pattern %q is not valid: %w", pattern, err)
	}
	regexCache.Store(pattern, re)
	return re, nil
}

// ValueAt reads one path out of a raw HL7 message.
//
// It exists for the channel test runner, which asserts on values in a transformed
// message. It goes through the same conversion and the same path resolution the
// transformation layer uses, so a path in an assertion means exactly what it means
// in a transformation. Reimplementing field addressing for tests would let a test
// pass while the transformation it describes was wrong.
func ValueAt(raw []byte, path string) (string, error) {
	p, err := ParsePath(path)
	if err != nil {
		return "", err
	}

	root, err := hl7xml.FromRaw(raw)
	if err != nil {
		return "", err
	}

	// A path naming no repetition addresses all of them, matching the filter
	// language. Values are joined with a comma so a two-repetition field reads as
	// two values rather than silently reporting only the first, which is the bug
	// this rule exists to avoid in the first place.
	nodes := p.resolveAll(root)
	if len(nodes) == 0 {
		return "", nil
	}
	if len(nodes) == 1 {
		return nodes[0].Value(), nil
	}

	var parts []string
	for _, n := range nodes {
		parts = append(parts, n.Value())
	}
	return strings.Join(parts, ","), nil
}
