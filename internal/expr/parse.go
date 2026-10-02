// Package expr evaluates filter expressions against HL7 messages.
//
// Filters decide whether a message is processed at all, so the language is
// deliberately small: comparisons on message paths, combined with and, or and
// not. There are no function calls, no assignment and no loops. A filter cannot
// modify a message or fail in a way that depends on order, which means reading
// one tells you exactly what it does.
//
// This is the deliberate contrast with a scripting engine. Mirth filters are
// JavaScript, which is why a channel can reach into the JVM, open a database
// connection or depend on another channel's state, and why none of that is
// visible without reading the code.
//
//	MSH-9.2 != "A28"
//	MSH-9.1 == "ADT" and PID-3.1 exists
//	MSH-4 in ["SITEA", "SITEB"] and not PV1-2 == "P"
//	OBX-3.1 matches "^GLU"
package expr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
)

// Expr is a parsed filter expression over one message format.
type Expr[M any] interface {
	// Eval reports whether the expression holds for a message.
	Eval(m M) (bool, error)
	// String renders the expression in canonical form.
	String() string
	// Paths lists every message path the expression reads, which lets a channel
	// report its own data dependencies.
	Paths() []string
}

// HL7Expr is a filter over an HL7 v2 message.
//
// An alias rather than its own type, so an HL7Expr is an Expr[*hl7.Message] and the two are interchangeable. Named
// because it is what almost every caller in this codebase wants, and expr.Expr[*hl7.Message] reads worse at a field
// declaration than expr.HL7Expr does.
type HL7Expr = Expr[*hl7.Message]

// Parse compiles a filter expression over HL7 v2.
//
// Kept as the plain name because v2 is what the overwhelming majority of channels filter. Other formats use ParseFor.
func Parse(src string) (HL7Expr, error) {
	return ParseFor(src, HL7Resolver{})
}

// ParseFor compiles a filter expression over any format with a resolver.
//
// The grammar is identical whatever the format. Somebody who can filter an HL7 feed can filter an X12 one, and the only
// thing to learn is what a path looks like - which they already know, because it is the same notation the transformation
// steps for that format use.
func ParseFor[M any](src string, r Resolver[M]) (Expr[M], error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser[M]{toks: toks, src: src, resolver: r}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.atEnd() {
		return nil, p.errorf("unexpected %s", p.peek().describe())
	}
	return e, nil
}

// MustParse is Parse for expressions known good at compile time.
func MustParse(src string) HL7Expr {
	e, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return e
}

// ---------- lexer ----------

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokPath
	tokString
	tokNumber
	tokOp
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokComma
	tokKeyword // exists, empty, matches, in
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

func (t token) describe() string {
	if t.kind == tokEOF {
		return "end of expression"
	}
	return fmt.Sprintf("%q", t.text)
}

var keywords = map[string]tokenKind{
	"and":     tokAnd,
	"or":      tokOr,
	"not":     tokNot,
	"exists":  tokKeyword,
	"empty":   tokKeyword,
	"matches": tokKeyword,
	"in":      tokKeyword,
}

// operators are ordered longest first so that >= is not read as > followed by =.
var operators = []string{"==", "!=", "=~", ">=", "<=", ">", "<"}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0

	for i < len(src) {
		c := src[i]

		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue

		case c == '(':
			toks = append(toks, token{tokLParen, "(", i})
			i++
			continue

		case c == ')':
			toks = append(toks, token{tokRParen, ")", i})
			i++
			continue

		case c == '[':
			toks = append(toks, token{tokLBracket, "[", i})
			i++
			continue

		case c == ']':
			toks = append(toks, token{tokRBracket, "]", i})
			i++
			continue

		case c == ',':
			toks = append(toks, token{tokComma, ",", i})
			i++
			continue

		case c == '"' || c == '\'':
			s, next, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokString, s, i})
			i = next
			continue
		}

		if op, ok := matchOperator(src, i); ok {
			toks = append(toks, token{tokOp, op, i})
			i += len(op)
			continue
		}

		// A bare word is a path, a keyword, or a number.
		word, next := lexWord(src, i)
		if word == "" {
			return nil, fmt.Errorf("expr: unexpected character %q at position %d", string(c), i)
		}
		start := i
		i = next

		if kind, ok := keywords[strings.ToLower(word)]; ok {
			toks = append(toks, token{kind, strings.ToLower(word), start})
			continue
		}
		if _, err := strconv.ParseFloat(word, 64); err == nil {
			toks = append(toks, token{tokNumber, word, start})
			continue
		}
		toks = append(toks, token{tokPath, word, start})
	}

	toks = append(toks, token{tokEOF, "", len(src)})
	return toks, nil
}

// lexWord reads a bare word: a keyword, a number, or an HL7 path.
//
// Paths contain punctuation that is structural elsewhere in the grammar —
// MSH-9.2 and OBX(3)-5(2).1 — so an index group is only absorbed into the word
// when it is a parenthesised or bracketed number. That way the parenthesis in
// "(A or B)" and the bracket in "in [X, Y]" still read as structure.
func lexWord(src string, start int) (string, int) {
	i := start
	for i < len(src) {
		c := src[i]
		// Slash and at-sign are path characters for the XML formats: an HL7 v3 or CDA path is written //birthTime and
		// //patient/id@extension. Hash is one for delimited files, where #3 is the third column - required rather than
		// inferred from a bare number, because a file exported with years as headers has a column legitimately named 2024.
		// None of the three is an operator or structure anywhere in the grammar, so accepting them costs nothing for the
		// formats that do not use them.
		if isAlnum(c) || c == '-' || c == '.' || c == '_' || c == '/' || c == '@' || c == '#' {
			i++
			continue
		}
		if c == '(' || c == '[' {
			closing := byte(')')
			if c == '[' {
				closing = ']'
			}
			j := i + 1
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			if j > i+1 && j < len(src) && src[j] == closing {
				i = j + 1
				continue
			}
		}
		break
	}
	return src[start:i], i
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lexString(src string, start int) (string, int, error) {
	quote := src[start]
	var sb strings.Builder
	i := start + 1

	for i < len(src) {
		switch src[i] {
		case '\\':
			if i+1 >= len(src) {
				return "", 0, fmt.Errorf("expr: string ends with a trailing backslash at position %d", i)
			}
			// Only the quote and the backslash itself are escapable. Keeping the
			// set tiny avoids surprises in regular expressions, which contain
			// backslashes of their own.
			switch src[i+1] {
			case quote, '\\':
				sb.WriteByte(src[i+1])
			default:
				sb.WriteByte('\\')
				sb.WriteByte(src[i+1])
			}
			i += 2
		case quote:
			return sb.String(), i + 1, nil
		default:
			sb.WriteByte(src[i])
			i++
		}
	}
	return "", 0, fmt.Errorf("expr: unterminated string starting at position %d", start)
}

func matchOperator(src string, i int) (string, bool) {
	for _, op := range operators {
		if strings.HasPrefix(src[i:], op) {
			return op, true
		}
	}
	return "", false
}

// ---------- parser ----------

type parser[M any] struct {
	toks []token
	pos  int
	src  string

	// resolver compiles each path as it is parsed, so a path the format cannot address fails here rather than on the
	// first message.
	resolver Resolver[M]
}

func (p *parser[M]) peek() token { return p.toks[p.pos] }
func (p *parser[M]) atEnd() bool { return p.peek().kind == tokEOF }
func (p *parser[M]) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser[M]) back()       { p.pos-- }
func (p *parser[M]) errorf(format string, args ...any) error {
	return fmt.Errorf("expr: %s at position %d in %q",
		fmt.Sprintf(format, args...), p.peek().pos, p.src)
}

// parseExpr handles or, which binds least tightly.
func (p *parser[M]) parseExpr() (Expr[M], error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &orExpr[M]{left: left, right: right}
	}
	return left, nil
}

func (p *parser[M]) parseAnd() (Expr[M], error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &andExpr[M]{left: left, right: right}
	}
	return left, nil
}

func (p *parser[M]) parseUnary() (Expr[M], error) {
	if p.peek().kind == tokNot {
		p.next()
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &notExpr[M]{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser[M]) parsePrimary() (Expr[M], error) {
	t := p.next()

	if t.kind == tokLParen {
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, p.errorf("expected a closing parenthesis")
		}
		p.next()
		return &groupExpr[M]{inner: inner}, nil
	}

	if t.kind != tokPath {
		p.back()
		return nil, p.errorf("expected a message path, found %s", t.describe())
	}

	// A format-specific operator, if the resolver contributes any. Checked before the shared keywords are dispatched but
	// after they are lexed, so an extension cannot shadow exists or in - those are keywords to the lexer and never
	// arrive here as a bare word.
	if op, ok := p.extension(p.peek()); ok {
		p.next()
		val := p.peek()
		if val.kind != tokString {
			return nil, p.errorf("%s needs a quoted value, like %s %s \"ASKU\"", op.Keyword, t.text, op.Keyword)
		}
		p.next()

		test, cerr := op.Compile(t.text, val.text)
		if cerr != nil {
			return nil, fmt.Errorf("expr: %w", cerr)
		}
		return &opExpr[M]{test: test, raw: t.text, keyword: op.Keyword, value: val.text}, nil
	}

	// Validate the path now rather than at evaluation time, so a typo is a
	// configuration error and not a filter that silently never matches.
	prepared, err := p.resolver.Prepare(t.text)
	if err != nil {
		return nil, fmt.Errorf("expr: %w", err)
	}

	switch nt := p.peek(); {
	case nt.kind == tokKeyword && nt.text == "exists":
		p.next()
		return &existsExpr[M]{presence: prepared.Presence, raw: t.text}, nil

	case nt.kind == tokKeyword && nt.text == "empty":
		p.next()
		return &emptyExpr[M]{presence: prepared.Presence, raw: t.text}, nil

	case nt.kind == tokKeyword && nt.text == "matches":
		p.next()
		return p.parseMatches(prepared, t.text)

	case nt.kind == tokKeyword && nt.text == "in":
		p.next()
		return p.parseIn(prepared, t.text)

	case nt.kind == tokOp:
		op := p.next().text
		if op == "=~" {
			return p.parseMatches(prepared, t.text)
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &compareExpr[M]{
			cands: prepared.Candidates, single: prepared.Single,
			raw: t.text, op: op, value: val, sem: p.semantics(),
		}, nil

	default:
		return nil, p.errorf("expected a comparison after %q", t.text)
	}
}

// extension reports whether a token names an operator the format added.
//
// A bare word rather than a keyword, because the lexer is shared and has no resolver: anything not in the shared keyword
// table arrives here as a path token, which is exactly where a format's own operator can be recognised.
func (p *parser[M]) extension(t token) (Operator[M], bool) {
	if t.kind != tokPath && t.kind != tokKeyword {
		return Operator[M]{}, false
	}

	ext, ok := p.resolver.(Extender[M])
	if !ok {
		return Operator[M]{}, false
	}

	for _, op := range ext.Operators() {
		if t.text == op.Keyword {
			return op, true
		}
	}
	return Operator[M]{}, false
}

// semantics returns the format's comparison choices, or the defaults.
func (p *parser[M]) semantics() Semantics {
	if o, ok := p.resolver.(Opinionated); ok {
		return o.Semantics()
	}
	return Semantics{}
}

func (p *parser[M]) parseMatches(prepared Prepared[M], raw string) (Expr[M], error) {
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(val)
	if err != nil {
		return nil, fmt.Errorf("expr: invalid regular expression %q: %w", val, err)
	}
	return &matchExpr[M]{cands: prepared.Candidates, raw: raw, re: re, absentFails: p.semantics().AbsentMatchesNothing}, nil
}

func (p *parser[M]) parseIn(prepared Prepared[M], raw string) (Expr[M], error) {
	// Either bracket style. The v2 filter documented in ["A", "B"] and the v3 filter documented in ("A", "B"), and both
	// are in use. Unifying the two grammars must not quietly stop parsing filters written against either of them, so both
	// are accepted and the closing token has to match the opening one.
	var closing tokenKind
	switch p.peek().kind {
	case tokLBracket:
		closing = tokRBracket
	case tokLParen:
		closing = tokRParen
	default:
		return nil, p.errorf("expected a list after in, in brackets or parentheses")
	}
	p.next()

	var values []string
	for {
		if p.peek().kind == closing {
			p.next()
			break
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, val)

		switch p.peek().kind {
		case tokComma:
			p.next()
		case closing:
			p.next()
			if len(values) == 0 {
				return nil, p.errorf("empty list")
			}
			return &inExpr[M]{
				cands: prepared.Candidates, raw: raw, values: values,
				absentFails: p.semantics().AbsentMatchesNothing,
			}, nil
		default:
			return nil, p.errorf("expected a comma or a close of the list")
		}
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("expr: in needs at least one value")
	}
	return &inExpr[M]{
		cands: prepared.Candidates, raw: raw, values: values,
		absentFails: p.semantics().AbsentMatchesNothing,
	}, nil
}

func (p *parser[M]) parseValue() (string, error) {
	t := p.next()
	switch t.kind {
	case tokString, tokNumber:
		return t.text, nil
	case tokPath:
		if p.semantics().RequireQuotedValues {
			p.back()
			return "", p.errorf("expected a quoted value, found %s", t.describe())
		}
		return t.text, nil
	default:
		p.back()
		return "", p.errorf("expected a value, found %s", t.describe())
	}
}
