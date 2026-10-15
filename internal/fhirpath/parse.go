package fhirpath

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The grammar is FHIRPath's (fhirpath.g4), by precedence from loosest:
//
//	implies, or xor, and, in contains, = ~ != !~, < > <= >=, |, is as, + - &, * / div mod, unary + -, . [] (invocation, indexer)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tDelimIdent // `name`
	tString
	tNumber
	tDate // @...
	tExtConst
	tOp
)

type token struct {
	kind tokKind
	s    string
	pos  int
}

type lexer struct {
	src  string
	pos  int
	toks []token
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src}
	for {
		l.skipSpace()
		if l.pos >= len(l.src) {
			l.toks = append(l.toks, token{kind: tEOF, pos: l.pos})
			return l.toks, nil
		}
		start := l.pos
		c := l.src[l.pos]
		switch {
		case c == '\'':
			s, err := l.quoted('\'')
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tString, s: s, pos: start})
		case c == '`':
			s, err := l.quoted('`')
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tDelimIdent, s: s, pos: start})
		case c == '@':
			l.pos++
			for l.pos < len(l.src) && strings.IndexByte("0123456789-:T.+Z", l.src[l.pos]) >= 0 {
				l.pos++
			}
			// A trailing dot is the invocation that follows (@2014.highBoundary()), not a fraction.
			for l.pos > start+1 && (l.src[l.pos-1] == '.' || (l.src[l.pos-1] == '-' && strings.Count(l.src[start+1:l.pos], "-") > 2)) {
				l.pos--
			}
			body := l.src[start+1 : l.pos]
			if body == "" {
				return nil, evalErr("a date literal at %d has nothing after @", start)
			}
			l.toks = append(l.toks, token{kind: tDate, s: body, pos: start})
		case c == '%':
			l.pos++
			if l.pos < len(l.src) && (l.src[l.pos] == '`' || l.src[l.pos] == '\'') {
				s, err := l.quoted(l.src[l.pos])
				if err != nil {
					return nil, err
				}
				l.toks = append(l.toks, token{kind: tExtConst, s: s, pos: start})
				continue
			}
			id := l.ident()
			if id == "" {
				return nil, evalErr("%% at %d is not followed by a name", start)
			}
			l.toks = append(l.toks, token{kind: tExtConst, s: id, pos: start})
		case c >= '0' && c <= '9':
			for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
				l.pos++
			}
			if l.pos+1 < len(l.src) && l.src[l.pos] == '.' && l.src[l.pos+1] >= '0' && l.src[l.pos+1] <= '9' {
				l.pos++
				for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
					l.pos++
				}
			}
			l.toks = append(l.toks, token{kind: tNumber, s: l.src[start:l.pos], pos: start})
		case c == '$' || c == '_' || isLetter(l.src[l.pos:]):
			if c == '$' {
				l.pos++
			}
			id := l.ident()
			if c == '$' {
				id = "$" + id
			}
			l.toks = append(l.toks, token{kind: tIdent, s: id, pos: start})
		default:
			for _, op := range []string{"<=", ">=", "!=", "!~", "=", "~", "<", ">", "+", "-", "*", "/", "|", "&", ".", ",", "(", ")", "[", "]", "{", "}"} {
				if strings.HasPrefix(l.src[l.pos:], op) {
					l.pos += len(op)
					l.toks = append(l.toks, token{kind: tOp, s: op, pos: start})
					goto next
				}
			}
			return nil, evalErr("unexpected %q at %d", string(c), start)
		next:
		}
	}
}

func isLetter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r)
}

func (l *lexer) ident() string {
	start := l.pos
	for l.pos < len(l.src) {
		r, n := utf8.DecodeRuneInString(l.src[l.pos:])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			l.pos += n
			continue
		}
		break
	}
	return l.src[start:l.pos]
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch {
		case l.src[l.pos] == ' ' || l.src[l.pos] == '\t' || l.src[l.pos] == '\n' || l.src[l.pos] == '\r':
			l.pos++
		case strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case strings.HasPrefix(l.src[l.pos:], "/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				l.pos = len(l.src)
			} else {
				l.pos += end + 4
			}
		default:
			return
		}
	}
}

func (l *lexer) quoted(q byte) (string, error) {
	start := l.pos
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == q:
			l.pos++
			return b.String(), nil
		case c == '\\' && l.pos+1 < len(l.src):
			l.pos++
			e := l.src[l.pos]
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'f':
				b.WriteByte('\f')
			case 'u':
				if l.pos+4 < len(l.src) {
					var r rune
					for _, h := range l.src[l.pos+1 : l.pos+5] {
						r <<= 4
						switch {
						case h >= '0' && h <= '9':
							r |= h - '0'
						case h >= 'a' && h <= 'f':
							r |= h - 'a' + 10
						case h >= 'A' && h <= 'F':
							r |= h - 'A' + 10
						}
					}
					b.WriteRune(r)
					l.pos += 4
				}
			default:
				b.WriteByte(e)
			}
			l.pos++
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return "", evalErr("an unterminated %c string at %d", q, start)
}

// Node is a parsed expression.
type node interface{}

type (
	nLiteral struct{ v Collection }
	nIdent   struct{ name string } // a member name or a type name at the start of a path
	nThis    struct{ name string } // $this, $index, $total
	nConst   struct{ name string } // %name
	nInvoke  struct {
		target node // nil: on the focus
		name   string
		args   []node
		fn     bool
	}
	nIndex struct{ target, index node }
	nUnary struct {
		op string
		x  node
	}
	nBinary struct {
		op   string
		l, r node
	}
	nType struct {
		op   string // is, as
		x    node
		name string
	}
)

type parser struct {
	toks []token
	i    int
}

// Expression is a parsed FHIRPath expression.
type Expression struct {
	src  string
	root node
}

// String is the source text.
func (e *Expression) String() string { return e.src }

// Parse parses an expression.
func Parse(src string) (*Expression, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, evalErr("unexpected %q at %d in %q", p.peek().s, p.peek().pos, src)
	}
	return &Expression{src: src, root: n}, nil
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.s == s
}

func (p *parser) isWord(s string) bool {
	t := p.peek()
	return t.kind == tIdent && t.s == s
}

func (p *parser) expect(s string) error {
	if !p.isOp(s) {
		return evalErr("expected %q at %d, found %q", s, p.peek().pos, p.peek().s)
	}
	p.i++
	return nil
}

// Precedence levels, loosest first.
var levels = [][]string{
	{"implies"},
	{"or", "xor"},
	{"and"},
	{"in", "contains"},
	{"=", "~", "!=", "!~"},
	{"<", ">", "<=", ">="},
	{"|"},
	{"is", "as"},
	{"+", "-", "&"},
	{"*", "/", "div", "mod"},
}

func (p *parser) matchLevel(level int) (string, bool) {
	t := p.peek()
	for _, op := range levels[level] {
		if (t.kind == tOp && t.s == op) || (t.kind == tIdent && t.s == op && isWordOp(op)) {
			return op, true
		}
	}
	return "", false
}

func isWordOp(op string) bool {
	return op[0] >= 'a' && op[0] <= 'z'
}

func (p *parser) expr(level int) (node, error) {
	if level == len(levels) {
		return p.unary()
	}
	l, err := p.expr(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.matchLevel(level)
		if !ok {
			return l, nil
		}
		p.i++
		if op == "is" || op == "as" {
			name, err := p.typeSpec()
			if err != nil {
				return nil, err
			}
			l = nType{op: op, x: l, name: name}
			continue
		}
		r, err := p.expr(level + 1)
		if err != nil {
			return nil, err
		}
		l = nBinary{op: op, l: l, r: r}
	}
}

func (p *parser) typeSpec() (string, error) {
	t := p.next()
	if t.kind != tIdent && t.kind != tDelimIdent {
		return "", evalErr("a type name is expected at %d", t.pos)
	}
	name := t.s
	if p.isOp(".") && (p.toks[p.i+1].kind == tIdent || p.toks[p.i+1].kind == tDelimIdent) {
		p.i++
		name += "." + p.next().s
	}
	return name, nil
}

func (p *parser) unary() (node, error) {
	if p.isOp("+") || p.isOp("-") {
		op := p.next().s
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return nUnary{op: op, x: x}, nil
	}
	return p.postfix()
}

func (p *parser) postfix() (node, error) {
	n, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			p.i++
			t := p.next()
			if t.kind != tIdent && t.kind != tDelimIdent {
				return nil, evalErr("a name is expected after . at %d", t.pos)
			}
			if t.kind == tIdent && p.isOp("(") {
				args, err := p.args()
				if err != nil {
					return nil, err
				}
				n = nInvoke{target: n, name: t.s, args: args, fn: true}
			} else {
				n = nInvoke{target: n, name: t.s}
			}
		case p.isOp("["):
			p.i++
			idx, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			n = nIndex{target: n, index: idx}
		default:
			return n, nil
		}
	}
}

func (p *parser) args() ([]node, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var args []node
	if p.isOp(")") {
		p.i++
		return args, nil
	}
	for {
		a, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.isOp(",") {
			p.i++
			continue
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return args, nil
	}
}

var calendarUnits = map[string]string{
	"year": "year", "years": "year", "month": "month", "months": "month", "week": "week", "weeks": "week",
	"day": "day", "days": "day", "hour": "hour", "hours": "hour", "minute": "minute", "minutes": "minute",
	"second": "second", "seconds": "second", "millisecond": "millisecond", "milliseconds": "millisecond",
}

func (p *parser) term() (node, error) {
	t := p.next()
	switch t.kind {
	case tString:
		return nLiteral{v: Collection{{V: t.s, T: "System.String"}}}, nil
	case tNumber:
		var it Item
		if strings.Contains(t.s, ".") {
			d, _ := ParseDecimal(t.s)
			it = Item{V: d, T: "System.Decimal"}
		} else {
			d, _ := ParseDecimal(t.s)
			if i, ok := d.IsInt(); ok {
				it = Item{V: i, T: "System.Integer"}
			} else {
				it = Item{V: d, T: "System.Decimal"}
			}
		}
		// A quantity: a number followed by a unit string or a calendar word.
		if nt := p.peek(); nt.kind == tString || (nt.kind == tIdent && calendarUnits[nt.s] != "") {
			p.i++
			d, _ := ParseDecimal(t.s)
			unit := nt.s
			if nt.kind == tIdent {
				unit = calendarUnits[nt.s]
			}
			return nLiteral{v: Collection{{V: Quantity{Value: d, Unit: unit}, T: "System.Quantity"}}}, nil
		}
		return nLiteral{v: Collection{it}}, nil
	case tDate:
		body := t.s
		if strings.HasPrefix(body, "T") {
			tm, ok := ParseTime(body[1:])
			if !ok {
				return nil, evalErr("@%s is not a time", body)
			}
			return nLiteral{v: Collection{{V: tm, T: "System.Time"}}}, nil
		}
		kind := "Date"
		if strings.Contains(body, "T") {
			kind = "DateTime"
			body = strings.TrimSuffix(body, "T")
			if !strings.Contains(body, "T") {
				dt, ok := ParseDateTime(body, "DateTime")
				if !ok {
					return nil, evalErr("@%s is not a date-time", t.s)
				}
				return nLiteral{v: Collection{{V: dt, T: "System.DateTime"}}}, nil
			}
		}
		dt, ok := ParseDateTime(body, kind)
		if !ok {
			return nil, evalErr("@%s is not a date", t.s)
		}
		return nLiteral{v: Collection{{V: dt, T: "System." + kind}}}, nil
	case tExtConst:
		return nConst{name: t.s}, nil
	case tDelimIdent:
		return nIdent{name: t.s}, nil
	case tIdent:
		switch t.s {
		case "true", "false":
			return nLiteral{v: Collection{{V: t.s == "true", T: "System.Boolean"}}}, nil
		case "$this", "$index", "$total":
			return nThis{name: t.s}, nil
		}
		if strings.HasPrefix(t.s, "$") {
			return nil, evalErr("%s is not a variable", t.s)
		}
		if p.isOp("(") {
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			return nInvoke{name: t.s, args: args, fn: true}, nil
		}
		return nIdent{name: t.s}, nil
	case tOp:
		switch t.s {
		case "(":
			n, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return n, nil
		case "{":
			if err := p.expect("}"); err != nil {
				return nil, err
			}
			return nLiteral{v: Collection{}}, nil
		}
	}
	if t.kind == tEOF {
		return nil, evalErr("the expression ends where a value is expected")
	}
	return nil, evalErr("unexpected %q at %d", t.s, t.pos)
}
