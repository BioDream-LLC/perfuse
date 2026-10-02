// Package e4x makes Mirth Connect's JavaScript run unchanged.
//
// Mirth scripts are written for Rhino, which implements E4X: XML is a native
// type with its own syntax. The engine here is goja, which does not, and the
// gap is not only a runtime one. Three pieces of E4X are *syntax*, so goja's
// parser rejects the file outright before any object model could help:
//
//	for each (var seg in msg..OBX) { }     // for-each, and the descendant operator
//	var z = <ZZZ><ZZZ.1>value</ZZZ.1></ZZZ>;  // an XML literal
//	var id = node.@root;                   // an attribute reference
//
// This file rewrites those into ordinary ECMAScript that goja parses, leaving
// the semantics to the runtime object in xml.go. Everything else about E4X -
// property access by element name, indexing repetitions, assignment, toString -
// is already valid JavaScript syntax and needs no rewriting.
//
// The rewriting is done with a real lexer rather than regular expressions,
// because the alternative corrupts working scripts. A pattern for the descendant
// operator finds one inside "http://example.org", a pattern for XML literals
// finds one in "a < b && c > d", and a script that silently changes meaning is
// far worse than one that refuses to load.
package e4x

import (
	"fmt"
	"strings"
)

// Note records a rewrite, so the interface can tell somebody what was translated
// rather than presenting their script as though nothing happened to it.
type Note struct {
	Line    int
	Kind    string // "for-each", "descendant", "attribute", "xml-literal"
	Detail  string
	Snippet string
}

// Preprocess converts E4X syntax to plain ECMAScript.
//
// It returns the rewritten source, notes describing what changed, and an error
// only when the input cannot be understood at all. An unrecognised construct is
// left alone: goja will then produce its own syntax error pointing at the real
// line, which is more useful than a guess made here.
func Preprocess(src string) (string, []Note, error) {
	p := &preprocessor{src: src, line: 1}
	if err := p.run(); err != nil {
		return "", p.notes, err
	}
	return p.out.String(), p.notes, nil
}

// Rewritten reports whether the source contains any E4X syntax at all. It is a
// cheap check used to skip the work for the many scripts that have none.
func Rewritten(src string) bool {
	return strings.Contains(src, "for each") ||
		strings.Contains(src, "..") ||
		strings.Contains(src, ".@") ||
		strings.Contains(src, "<")
}

type preprocessor struct {
	src   string
	pos   int
	line  int
	out   strings.Builder
	notes []Note

	// lastTok is the last syntactically significant character emitted. It
	// decides whether a '<' begins an XML literal or is a comparison, and
	// whether a '/' begins a regular expression or is division. Getting this
	// wrong is how naive rewriters corrupt scripts.
	lastTok byte
	// lastWord is the last identifier or keyword emitted, for the same purpose.
	lastWord string
}

func (p *preprocessor) run() error {
	for p.pos < len(p.src) {
		c := p.src[p.pos]

		switch {
		case c == '\n':
			p.line++
			p.emit(1)

		case c == '/' && p.peekAt(1) == '/':
			p.copyLineComment()

		case c == '/' && p.peekAt(1) == '*':
			if err := p.copyBlockComment(); err != nil {
				return err
			}

		case c == '/' && p.regexAllowed():
			if err := p.copyRegex(); err != nil {
				return err
			}

		case c == '"' || c == '\'':
			if err := p.copyString(c); err != nil {
				return err
			}

		case c == '`':
			if err := p.copyTemplate(); err != nil {
				return err
			}

		case isIdentStart(c):
			p.copyWord()

		case c == '.' && p.peekAt(1) == '.' && p.peekAt(2) != '.' && p.descendantAllowed():
			if !p.rewriteDescendant() {
				p.emit(1)
			}

		case c == '.' && p.peekAt(1) == '@':
			if !p.rewriteAttribute() {
				p.emit(1)
			}

		case c == '<' && p.xmlLiteralAllowed():
			consumed, err := p.rewriteXMLLiteral()
			if err != nil {
				return err
			}
			if !consumed {
				p.emitTok(1)
			}

		default:
			p.emitTok(1)
		}
	}
	return nil
}

// emit copies n bytes through without treating them as significant.
func (p *preprocessor) emit(n int) {
	end := min(p.pos+n, len(p.src))
	p.out.WriteString(p.src[p.pos:end])
	p.pos = end
}

// emitTok copies n bytes and records the last one as significant.
func (p *preprocessor) emitTok(n int) {
	end := min(p.pos+n, len(p.src))
	text := p.src[p.pos:end]
	p.out.WriteString(text)
	for i := len(text) - 1; i >= 0; i-- {
		if !isSpace(text[i]) {
			p.lastTok = text[i]
			p.lastWord = ""
			break
		}
	}
	p.pos = end
}

func (p *preprocessor) peekAt(n int) byte {
	if p.pos+n >= len(p.src) {
		return 0
	}
	return p.src[p.pos+n]
}

func (p *preprocessor) copyLineComment() {
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '\n' {
		p.pos++
	}
	p.out.WriteString(p.src[start:p.pos])
}

func (p *preprocessor) copyBlockComment() error {
	start := p.pos
	p.pos += 2
	for p.pos < len(p.src) {
		if p.src[p.pos] == '\n' {
			p.line++
		}
		if p.src[p.pos] == '*' && p.peekAt(1) == '/' {
			p.pos += 2
			p.out.WriteString(p.src[start:p.pos])
			return nil
		}
		p.pos++
	}
	return fmt.Errorf("line %d: block comment is never closed", p.line)
}

func (p *preprocessor) copyString(quote byte) error {
	start := p.pos
	startLine := p.line
	p.pos++
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case quote:
			p.pos++
			p.out.WriteString(p.src[start:p.pos])
			p.lastTok = quote
			p.lastWord = ""
			return nil
		case '\n':
			return fmt.Errorf("line %d: string is never closed", startLine)
		}
		p.pos++
	}
	return fmt.Errorf("line %d: string is never closed", startLine)
}

func (p *preprocessor) copyTemplate() error {
	start := p.pos
	startLine := p.line
	p.pos++
	depth := 0
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case '\n':
			p.line++
		case '$':
			if p.peekAt(1) == '{' {
				depth++
				p.pos += 2
				continue
			}
		case '}':
			if depth > 0 {
				depth--
			}
		case '`':
			if depth == 0 {
				p.pos++
				p.out.WriteString(p.src[start:p.pos])
				p.lastTok = '`'
				p.lastWord = ""
				return nil
			}
		}
		p.pos++
	}
	return fmt.Errorf("line %d: template literal is never closed", startLine)
}

// copyWord copies an identifier or keyword, and rewrites "for each".
func (p *preprocessor) copyWord() {
	start := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	word := p.src[start:p.pos]

	if word == "for" && p.forEachFollows() {
		if p.rewriteForEach(start) {
			return
		}
	}

	p.out.WriteString(word)
	p.lastWord = word
	p.lastTok = word[len(word)-1]
}

// forEachFollows reports whether the "for" just read is followed by "each".
func (p *preprocessor) forEachFollows() bool {
	i := p.pos
	for i < len(p.src) && isSpace(p.src[i]) {
		i++
	}
	return strings.HasPrefix(p.src[i:], "each") &&
		(i+4 >= len(p.src) || !isIdentPart(p.src[i+4]))
}

// rewriteForEach turns
//
//	for each (var x in expr) body
//
// into
//
//	for (var x of __e4xEach(expr)) body
//
// E4X's for-each iterates values while plain for-in iterates keys, so for-of is
// the correct target. __e4xEach normalises whatever the expression produced -
// a single node, a list of them, or an ordinary array - into something iterable,
// which matters because in E4X a single-element list and a bare element are
// indistinguishable and a script relies on iterating either.
func (p *preprocessor) rewriteForEach(forStart int) bool {
	save := p.pos
	i := p.pos

	for i < len(p.src) && isSpace(p.src[i]) {
		i++
	}
	i += 4 // "each"
	for i < len(p.src) && isSpace(p.src[i]) {
		i++
	}
	if i >= len(p.src) || p.src[i] != '(' {
		p.pos = save
		return false
	}

	open := i
	closeParen, ok := matchParen(p.src, open)
	if !ok {
		p.pos = save
		return false
	}
	header := p.src[open+1 : closeParen]

	// Split on the "in" that separates the binding from the subject, at paren
	// depth zero and outside any string.
	binding, subject, found := splitTopLevelIn(header)
	if !found {
		p.pos = save
		return false
	}

	binding = strings.TrimSpace(binding)
	subject = strings.TrimSpace(subject)
	if binding == "" || subject == "" {
		p.pos = save
		return false
	}
	// A binding without a declaration keyword is legal E4X and assigns to an
	// existing variable. for-of accepts that too.
	p.out.WriteString("for (")
	p.out.WriteString(binding)
	p.out.WriteString(" of __e4xEach(")
	p.out.WriteString(subject)
	p.out.WriteString("))")

	p.note(Note{
		Line:    p.line,
		Kind:    "for-each",
		Detail:  "E4X for-each rewritten as for-of over an iterator",
		Snippet: strings.TrimSpace(p.src[forStart : closeParen+1]),
	})

	// The subject may itself contain E4X, so run it back through.
	p.pos = closeParen + 1
	p.lastTok = ')'
	p.lastWord = ""

	inner, notes, err := Preprocess(subject)
	if err == nil && inner != subject {
		// Rebuild with the rewritten subject.
		text := p.out.String()
		marker := "__e4xEach(" + subject + "))"
		if idx := strings.LastIndex(text, marker); idx >= 0 {
			p.out.Reset()
			p.out.WriteString(text[:idx])
			p.out.WriteString("__e4xEach(" + inner + "))")
		}
		for _, n := range notes {
			n.Line += p.line - 1
			p.note(n)
		}
	}
	return true
}

// descendantAllowed reports whether a '..' here is E4X's descendant operator
// rather than something else. It requires a preceding identifier, closing
// bracket or paren, which excludes both "1..toString()" and a '..' inside what
// the lexer already knows is a string.
func (p *preprocessor) descendantAllowed() bool {
	if p.lastWord != "" {
		return !isReservedBeforeOperand(p.lastWord)
	}
	switch p.lastTok {
	case ')', ']', '}':
		return true
	}
	return false
}

// rewriteDescendant turns expr..NAME into expr.__e4xDescendants('NAME').
func (p *preprocessor) rewriteDescendant() bool {
	i := p.pos + 2

	// X..[expr] is a descendant search with a computed name. It matters more here
	// than in general E4X, because an HL7 element name contains a dot and so
	// cannot be written as an identifier: msg..['OBX.5'] is the only way to say
	// it.
	if i < len(p.src) && p.src[i] == '[' {
		closeIdx, ok := matchDelim(p.src, i, '[', ']')
		if !ok {
			return false
		}
		expr := p.src[i+1 : closeIdx]
		p.out.WriteString(".__e4xDescendants(")
		p.out.WriteString(expr)
		p.out.WriteString(")")
		p.pos = closeIdx + 1
		p.lastTok = ')'
		p.lastWord = ""
		p.note(Note{Line: p.line, Kind: "descendant", Detail: "descendant search with a computed name", Snippet: "..[" + expr + "]"})
		return true
	}

	if i < len(p.src) && p.src[i] == '*' {
		p.out.WriteString(".__e4xDescendants('*')")
		p.pos = i + 1
		p.lastTok = ')'
		p.lastWord = ""
		p.note(Note{Line: p.line, Kind: "descendant", Detail: "descendant wildcard", Snippet: "..*"})
		return true
	}
	if i >= len(p.src) || !isIdentStart(p.src[i]) {
		return false
	}
	start := i
	for i < len(p.src) && (isIdentPart(p.src[i]) || p.src[i] == '.' || p.src[i] == ':') {
		i++
	}
	// A trailing dot belongs to the next expression, not the name.
	name := strings.TrimRight(p.src[start:i], ".")
	i = start + len(name)

	p.out.WriteString(".__e4xDescendants('")
	p.out.WriteString(name)
	p.out.WriteString("')")
	p.pos = i
	p.lastTok = ')'
	p.lastWord = ""

	p.note(Note{
		Line:    p.line,
		Kind:    "descendant",
		Detail:  "E4X descendant operator rewritten as a method call",
		Snippet: ".." + name,
	})
	return true
}

// rewriteAttribute turns expr.@name into expr.__e4xAttribute('name').
func (p *preprocessor) rewriteAttribute() bool {
	i := p.pos + 2
	if i < len(p.src) && p.src[i] == '*' {
		p.out.WriteString(".__e4xAttributes()")
		p.pos = i + 1
		p.lastTok = ')'
		p.lastWord = ""
		p.note(Note{Line: p.line, Kind: "attribute", Detail: "all attributes", Snippet: ".@*"})
		return true
	}
	if i >= len(p.src) || !isIdentStart(p.src[i]) {
		return false
	}
	start := i
	for i < len(p.src) && (isIdentPart(p.src[i]) || p.src[i] == ':' || p.src[i] == '-') {
		i++
	}
	name := p.src[start:i]

	p.out.WriteString(".__e4xAttribute('")
	p.out.WriteString(name)
	p.out.WriteString("')")
	p.pos = i
	p.lastTok = ')'
	p.lastWord = ""

	p.note(Note{
		Line:    p.line,
		Kind:    "attribute",
		Detail:  "E4X attribute reference rewritten as a method call",
		Snippet: ".@" + name,
	})
	return true
}

// xmlLiteralAllowed decides whether '<' starts an XML literal or is a comparison.
//
// This is the judgement most likely to go wrong, so it is deliberately
// conservative: a literal is only recognised where an operand is expected. That
// means "a < b" is never mistaken for a literal, at the cost of missing a literal
// in a position no real script uses.
func (p *preprocessor) xmlLiteralAllowed() bool {
	next := p.peekAt(1)
	if !isIdentStart(next) && next != '/' && next != '>' && next != '!' {
		return false
	}
	if p.lastWord != "" {
		return isReservedBeforeOperand(p.lastWord)
	}
	switch p.lastTok {
	case 0, '=', '(', ',', '[', ':', '{', ';', '?', '&', '|', '+', '!', '>', '<', '*', '%', '~', '^':
		return true
	}
	return false
}

// rewriteXMLLiteral converts an XML literal into a __e4xParse call on a template
// literal, so that E4X's {expr} interpolation becomes ${expr} and JavaScript
// evaluates it for us.
func (p *preprocessor) rewriteXMLLiteral() (bool, error) {
	end, err := p.scanXMLLiteral(p.pos)
	if err != nil {
		return false, err
	}
	if end < 0 {
		return false, nil
	}

	raw := p.src[p.pos:end]
	p.out.WriteString("__e4xParse(`")
	p.out.WriteString(toTemplate(raw))
	p.out.WriteString("`)")

	p.line += strings.Count(raw, "\n")
	p.pos = end
	p.lastTok = ')'
	p.lastWord = ""

	snippet := raw
	if len(snippet) > 60 {
		snippet = snippet[:57] + "..."
	}
	p.note(Note{
		Line:    p.line,
		Kind:    "xml-literal",
		Detail:  "XML literal rewritten as a parse call",
		Snippet: strings.Join(strings.Fields(snippet), " "),
	})
	return true, nil
}

// scanXMLLiteral finds the end of an XML literal beginning at start, or returns
// -1 when what follows is not one after all.
func (p *preprocessor) scanXMLLiteral(start int) (int, error) {
	i := start
	depth := 0
	startLine := p.line

	for i < len(p.src) {
		switch {
		case strings.HasPrefix(p.src[i:], "<!--"):
			closeIdx := strings.Index(p.src[i:], "-->")
			if closeIdx < 0 {
				return -1, fmt.Errorf("line %d: XML comment in a literal is never closed", startLine)
			}
			i += closeIdx + 3

		case strings.HasPrefix(p.src[i:], "<![CDATA["):
			closeIdx := strings.Index(p.src[i:], "]]>")
			if closeIdx < 0 {
				return -1, fmt.Errorf("line %d: CDATA section in a literal is never closed", startLine)
			}
			i += closeIdx + 3

		case strings.HasPrefix(p.src[i:], "</"):
			closeIdx := strings.IndexByte(p.src[i:], '>')
			if closeIdx < 0 {
				return -1, fmt.Errorf("line %d: closing tag in an XML literal is never finished", startLine)
			}
			i += closeIdx + 1
			depth--
			if depth <= 0 {
				return i, nil
			}

		case p.src[i] == '<':
			tagEnd, selfClosing, ok := scanTag(p.src, i)
			if !ok {
				if depth == 0 {
					return -1, nil // Not an XML literal; leave it to goja.
				}
				return -1, fmt.Errorf("line %d: tag in an XML literal is never finished", startLine)
			}
			i = tagEnd
			if !selfClosing {
				depth++
			} else if depth == 0 {
				return i, nil // <foo/> on its own.
			}

		case p.src[i] == '{':
			// An interpolation may contain braces and strings of its own.
			closeIdx, ok := matchBrace(p.src, i)
			if !ok {
				return -1, fmt.Errorf("line %d: interpolation in an XML literal is never closed", startLine)
			}
			i = closeIdx + 1

		default:
			if depth == 0 {
				// Text before any tag means this was not a literal.
				return -1, nil
			}
			i++
		}
	}
	if depth > 0 {
		return -1, fmt.Errorf("line %d: XML literal is never closed", startLine)
	}
	return -1, nil
}

// scanTag finds the end of a start tag, reporting whether it was self-closing.
func scanTag(src string, i int) (end int, selfClosing bool, ok bool) {
	j := i + 1
	if j < len(src) && !isIdentStart(src[j]) && src[j] != '>' {
		return 0, false, false
	}
	for j < len(src) {
		switch src[j] {
		case '"', '\'':
			quote := src[j]
			j++
			for j < len(src) && src[j] != quote {
				j++
			}
			if j >= len(src) {
				return 0, false, false
			}
		case '{':
			closeIdx, found := matchBrace(src, j)
			if !found {
				return 0, false, false
			}
			j = closeIdx
		case '/':
			if j+1 < len(src) && src[j+1] == '>' {
				return j + 2, true, true
			}
		case '>':
			return j + 1, false, true
		}
		j++
	}
	return 0, false, false
}

// toTemplate converts E4X literal text into a JavaScript template literal body:
// {expr} becomes ${expr}, and characters that mean something in a template are
// escaped so that XML content cannot inject code.
func toTemplate(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '`':
			b.WriteString("\\`")
		case '\\':
			b.WriteString("\\\\")
		case '$':
			// A literal dollar must not become an interpolation.
			if i+1 < len(raw) && raw[i+1] == '{' {
				b.WriteString("\\$")
			} else {
				b.WriteByte('$')
			}
		case '{':
			if end, ok := matchBrace(raw, i); ok {
				b.WriteString("${")
				b.WriteString(raw[i+1 : end])
				b.WriteByte('}')
				i = end
			} else {
				b.WriteByte('{')
			}
		default:
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

// regexAllowed reports whether a '/' begins a regular expression literal.
func (p *preprocessor) regexAllowed() bool {
	if p.lastWord != "" {
		return isReservedBeforeOperand(p.lastWord)
	}
	switch p.lastTok {
	case 0, '=', '(', ',', '[', ':', '{', ';', '?', '&', '|', '+', '-', '*', '%', '!', '<', '>', '^', '~':
		return true
	}
	return false
}

func (p *preprocessor) copyRegex() error {
	start := p.pos
	startLine := p.line
	p.pos++
	inClass := false
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '\n':
			return fmt.Errorf("line %d: regular expression is never closed", startLine)
		case '/':
			if !inClass {
				p.pos++
				for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
					p.pos++ // flags
				}
				p.out.WriteString(p.src[start:p.pos])
				p.lastTok = '/'
				p.lastWord = ""
				return nil
			}
		}
		p.pos++
	}
	return fmt.Errorf("line %d: regular expression is never closed", startLine)
}

func (p *preprocessor) note(n Note) {
	// Cap the notes so a generated script cannot produce a million of them.
	if len(p.notes) < 200 {
		p.notes = append(p.notes, n)
	}
}

// splitTopLevelIn splits a for-each header on the "in" keyword at depth zero.
func splitTopLevelIn(header string) (binding, subject string, found bool) {
	depth := 0
	for i := 0; i < len(header); i++ {
		switch header[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '"', '\'':
			quote := header[i]
			i++
			for i < len(header) && header[i] != quote {
				if header[i] == '\\' {
					i++
				}
				i++
			}
		case 'i':
			if depth != 0 {
				continue
			}
			if !strings.HasPrefix(header[i:], "in") {
				continue
			}
			if i+2 < len(header) && isIdentPart(header[i+2]) {
				continue
			}
			if i > 0 && isIdentPart(header[i-1]) {
				continue
			}
			return header[:i], header[i+2:], true
		}
	}
	return "", "", false
}

// matchParen finds the ')' matching the '(' at open, skipping strings.
func matchParen(src string, open int) (int, bool) { return matchDelim(src, open, '(', ')') }

// matchBrace finds the '}' matching the '{' at open, skipping strings.
func matchBrace(src string, open int) (int, bool) { return matchDelim(src, open, '{', '}') }

func matchDelim(src string, open int, opener, closer byte) (int, bool) {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '"', '\'', '`':
			quote := src[i]
			i++
			for i < len(src) && src[i] != quote {
				if src[i] == '\\' {
					i++
				}
				i++
			}
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// isReservedBeforeOperand reports whether a keyword is one after which an
// operand is expected, which is what distinguishes "return <foo/>" from
// "x < y" and "return /re/" from "a / b".
func isReservedBeforeOperand(word string) bool {
	switch word {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void",
		"throw", "case", "do", "else", "yield", "await":
		return true
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
