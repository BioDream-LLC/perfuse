package hl7

import "strings"

// HL7 escapes delimiters inside data using the escape character declared in
// MSH-2, wrapped as \F\, \S\, \T\, \R\ and \E\. There are also character-set
// and formatting escapes; the formatting ones carry no data and are dropped,
// and \X..\ carries hex bytes.

// Unescape resolves HL7 escape sequences.
//
// The common case is a value with no escape character at all, which returns
// without scanning twice or allocating anything beyond the string itself.
func Unescape(b []byte, sep Separators) string {
	i := indexByte(b, sep.Escape)
	if i < 0 {
		return string(b)
	}

	var sb strings.Builder
	sb.Grow(len(b))
	sb.Write(b[:i])

	for i < len(b) {
		if b[i] != sep.Escape {
			sb.WriteByte(b[i])
			i++
			continue
		}

		// Find the closing escape character.
		end := -1
		for j := i + 1; j < len(b); j++ {
			if b[j] == sep.Escape {
				end = j
				break
			}
			// An unterminated sequence is data, not an escape. Real senders
			// produce lone backslashes in free text.
			if b[j] == sep.Field || b[j] == '\r' || b[j] == '\n' {
				break
			}
		}
		if end < 0 {
			sb.WriteByte(b[i])
			i++
			continue
		}

		seq := b[i+1 : end]
		writeEscape(&sb, seq, sep)
		i = end + 1
	}

	return sb.String()
}

func writeEscape(sb *strings.Builder, seq []byte, sep Separators) {
	if len(seq) == 0 {
		// \\ is an escaped escape character.
		sb.WriteByte(sep.Escape)
		return
	}

	switch seq[0] {
	case 'F':
		if len(seq) == 1 {
			sb.WriteByte(sep.Field)
			return
		}
	case 'S':
		if len(seq) == 1 {
			sb.WriteByte(sep.Component)
			return
		}
	case 'T':
		if len(seq) == 1 {
			sb.WriteByte(sep.Subcomponent)
			return
		}
	case 'R':
		if len(seq) == 1 {
			sb.WriteByte(sep.Repeat)
			return
		}
	case 'E':
		if len(seq) == 1 {
			sb.WriteByte(sep.Escape)
			return
		}
	case 'X':
		// Hexadecimal data, two digits per byte.
		if hex := seq[1:]; len(hex) > 0 && len(hex)%2 == 0 {
			ok := true
			buf := make([]byte, 0, len(hex)/2)
			for i := 0; i < len(hex); i += 2 {
				hi, lo := hexVal(hex[i]), hexVal(hex[i+1])
				if hi < 0 || lo < 0 {
					ok = false
					break
				}
				buf = append(buf, byte(hi<<4|lo))
			}
			if ok {
				sb.Write(buf)
				return
			}
		}
	case 'H', 'N':
		// Highlight on and off. Formatting only, no data.
		if len(seq) == 1 {
			return
		}
	case '.':
		// Formatting commands in FT fields: .br, .sp, .fi and so on. The line
		// break is worth keeping because dropping it silently runs clinical
		// text together.
		if string(seq) == ".br" || strings.HasPrefix(string(seq), ".sp") {
			sb.WriteByte('\n')
			return
		}
		return
	case 'C', 'M', 'Z':
		// Character-set switches and locally defined escapes. There is no
		// portable interpretation, so they are dropped rather than guessed at.
		return
	}

	// Anything unrecognised is preserved verbatim, including its delimiters, so
	// no data is silently lost.
	sb.WriteByte(sep.Escape)
	sb.Write(seq)
	sb.WriteByte(sep.Escape)
}

// Escape encodes a value for placement in a message, escaping any delimiter it
// contains.
func Escape(s string, sep Separators) string {
	if !strings.ContainsAny(s, string([]byte{
		sep.Field, sep.Component, sep.Repeat, sep.Escape, sep.Subcomponent,
	})) {
		return s
	}

	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		// The escape character must be written first; otherwise the escapes
		// emitted for the other delimiters would themselves be escaped.
		case sep.Escape:
			sb.WriteByte(sep.Escape)
			sb.WriteByte('E')
			sb.WriteByte(sep.Escape)
		case sep.Field:
			sb.WriteByte(sep.Escape)
			sb.WriteByte('F')
			sb.WriteByte(sep.Escape)
		case sep.Component:
			sb.WriteByte(sep.Escape)
			sb.WriteByte('S')
			sb.WriteByte(sep.Escape)
		case sep.Repeat:
			sb.WriteByte(sep.Escape)
			sb.WriteByte('R')
			sb.WriteByte(sep.Escape)
		case sep.Subcomponent:
			sb.WriteByte(sep.Escape)
			sb.WriteByte('T')
			sb.WriteByte(sep.Escape)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

func indexByte(b []byte, target byte) int {
	for i := range b {
		if b[i] == target {
			return i
		}
	}
	return -1
}
