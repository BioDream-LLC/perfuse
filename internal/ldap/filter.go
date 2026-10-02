package ldap

import (
	"fmt"
	"strings"
)

// Filter choice tags, from RFC 4511 section 4.5.1.
const (
	filterAnd             = 0
	filterOr              = 1
	filterNot             = 2
	filterEqualityMatch   = 3
	filterSubstrings      = 4
	filterGreaterOrEqual  = 5
	filterLessOrEqual     = 6
	filterPresent         = 7
	filterApproxMatch     = 8
	filterExtensibleMatch = 9
)

// Substring choice tags.
const (
	substringInitial = 0
	substringAny     = 1
	substringFinal   = 2
)

// parseFilter compiles a filter string into BER.
//
// Written rather than borrowed because the whole point of this package is that what decides who gets in is readable. A
// filter is also the one place a username reaches the protocol as syntax rather than as data, so the compiler and the
// escaping in EscapeFilter are two halves of the same defence: escaping stops a value becoming structure, and this
// refuses anything malformed rather than guessing at it.
func parseFilter(s string) (*packet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		// Refused rather than defaulted to (objectClass=*). A missing filter that silently matched everything would
		// return the first object in the directory and authenticate against it.
		return nil, fmt.Errorf("an LDAP filter cannot be empty")
	}

	p, rest, err := parseFilterExpr(s, 0)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("an LDAP filter has trailing text after the closing bracket: %q", rest)
	}
	return p, nil
}

// parseFilterExpr parses one bracketed expression and returns what is left.
func parseFilterExpr(s string, depth int) (*packet, string, error) {
	const maxDepth = 16
	if depth > maxDepth {
		return nil, "", fmt.Errorf("an LDAP filter nests more than %d levels deep", maxDepth)
	}

	s = strings.TrimLeft(s, " ")
	if !strings.HasPrefix(s, "(") {
		return nil, "", fmt.Errorf("an LDAP filter must start with a bracket, found %q", first(s, 16))
	}
	s = s[1:]
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return nil, "", fmt.Errorf("an LDAP filter ends after its opening bracket")
	}

	switch s[0] {
	case '&', '|':
		tag := byte(filterAnd)
		if s[0] == '|' {
			tag = filterOr
		}
		group := newSequence(classContext | tag)
		rest := s[1:]

		for {
			rest = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(rest, ")") {
				if len(group.children) == 0 {
					// An empty and-group matches everything, an empty or-group matches nothing. Both are
					// legal BER and both are almost certainly a mistake in a hand-written filter, and the
					// first one is a mistake that authenticates against an arbitrary account.
					return nil, "", fmt.Errorf("an LDAP filter has an empty %s group", groupName(tag))
				}
				return group, rest[1:], nil
			}
			if rest == "" {
				return nil, "", fmt.Errorf("an LDAP filter is missing a closing bracket")
			}

			child, remainder, err := parseFilterExpr(rest, depth+1)
			if err != nil {
				return nil, "", err
			}
			group.add(child)
			rest = remainder
		}

	case '!':
		group := newSequence(classContext | filterNot)
		child, rest, err := parseFilterExpr(s[1:], depth+1)
		if err != nil {
			return nil, "", err
		}
		group.add(child)

		rest = strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(rest, ")") {
			return nil, "", fmt.Errorf("an LDAP not-filter is missing its closing bracket")
		}
		return group, rest[1:], nil

	default:
		end := strings.IndexByte(s, ')')
		if end < 0 {
			return nil, "", fmt.Errorf("an LDAP filter item is missing its closing bracket: %q", first(s, 32))
		}
		item, err := parseFilterItem(s[:end])
		if err != nil {
			return nil, "", err
		}
		return item, s[end+1:], nil
	}
}

func groupName(tag byte) string {
	if tag == filterAnd {
		return "and"
	}
	return "or"
}

// parseFilterItem parses a single comparison.
func parseFilterItem(s string) (*packet, error) {
	// Ordered longest-first. Checking for "=" before ">=" would split "uid>=3" at the equals sign and produce an
	// equality match on an attribute named "uid>", which a directory answers with nothing rather than an error.
	for _, op := range []struct {
		text string
		tag  byte
	}{
		{">=", filterGreaterOrEqual},
		{"<=", filterLessOrEqual},
		{"~=", filterApproxMatch},
		{":=", filterExtensibleMatch},
	} {
		if i := strings.Index(s, op.text); i > 0 {
			if op.tag == filterExtensibleMatch {
				// Not supported. Extensible matching carries a matching rule OID and a DN-attributes flag,
				// and AD uses it for recursive group membership - which is worth having one day and is not
				// worth guessing at now.
				return nil, fmt.Errorf("extensible LDAP matching rules are not supported: %q", s)
			}
			attr := strings.TrimSpace(s[:i])
			value := s[i+len(op.text):]
			return comparison(op.tag, attr, value)
		}
	}

	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return nil, fmt.Errorf("an LDAP filter item has no comparison in it: %q", s)
	}

	attr := strings.TrimSpace(s[:i])
	value := s[i+1:]

	if attr == "" {
		return nil, fmt.Errorf("an LDAP filter item has no attribute name: %q", s)
	}

	// A bare asterisk is a presence test rather than an equality match on the literal character.
	if value == "*" {
		return newPacket(classContext|filterPresent, []byte(attr)), nil
	}

	if strings.Contains(value, "*") {
		return substringFilter(attr, value)
	}

	return comparison(filterEqualityMatch, attr, value)
}

// comparison builds an attribute-and-value assertion.
func comparison(tag byte, attr, value string) (*packet, error) {
	if attr == "" {
		return nil, fmt.Errorf("an LDAP filter comparison has no attribute name")
	}
	p := newSequence(classContext | tag)
	p.addString(attr)
	p.addString(unescapeFilterValue(value))
	return p, nil
}

// substringFilter builds a substring match.
func substringFilter(attr, value string) (*packet, error) {
	parts := strings.Split(value, "*")

	p := newSequence(classContext | filterSubstrings)
	p.addString(attr)
	seq := p.add(newSequence(tagSequence | classUniversal))

	for i, part := range parts {
		if part == "" {
			continue
		}
		text := unescapeFilterValue(part)
		switch {
		case i == 0:
			seq.add(newPacket(classContext|substringInitial, []byte(text)))
		case i == len(parts)-1:
			seq.add(newPacket(classContext|substringFinal, []byte(text)))
		default:
			seq.add(newPacket(classContext|substringAny, []byte(text)))
		}
	}

	if len(seq.children) == 0 {
		return nil, fmt.Errorf("an LDAP substring filter on %q has nothing to match", attr)
	}

	return p, nil
}

// unescapeFilterValue turns RFC 4515 hex escapes back into bytes.
//
// The value goes onto the wire as raw octets, so the escaping that made it safe as filter text has to be undone exactly
// once. Undoing it twice would reintroduce the injection this prevents; not undoing it would look up a user whose name
// literally contains a backslash and two digits, and find nobody.
func unescapeFilterValue(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+2 < len(s) {
			hi, ok1 := hexValue(s[i+1])
			lo, ok2 := hexValue(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
