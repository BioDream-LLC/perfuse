// Package x12xml converts X12 (and other delimited EDI) to and from the XML that Mirth Connect and Open Integration
// Engine write for it, and back.
//
// # Why that XML and not one of our own
//
// The point of an XML form of X12 is the systems that already use one. A site moving from Mirth has receivers built
// against what Mirth's EDI/X12 data type emits, scripts that walk msg['CLM']['CLM.05']['CLM.05.1'], and upstream engines
// that send X12 as that XML. A tidier shape of our own would serve none of them. So this writes exactly what Open
// Integration Engine 4.6 writes, and reads exactly what it reads, and the tests compare the two byte for byte against
// OIE's own serializer.
//
// An earlier version of this package invented its own shape (an X12Interchange root, elements named CLM05) and said it
// matched Mirth's. It did not; it had never been compared with Mirth, and nothing in Perfuse used it.
//
// # The format
//
// The root is X12Transaction when the message starts with an ISA segment and EDIMessage otherwise, carrying the three
// delimiters as attributes (segmentDelimiter, elementDelimiter, subelementDelimiter). Each segment is an element named by
// its identifier. Each data element is SEG.NN, numbered from 01 (ISA.01, CLM.05), and holds its components as
// SEG.NN.1, SEG.NN.2 and so on: always at least SEG.NN.1 for an element with a value, none for an empty one.
//
// The delimiters are read from the ISA when there is one: the element separator from position 3, the component
// separator from position 104 and the segment terminator from position 105, with a line feed after it counted as part
// of it. Otherwise they are ~ * and :. The repetition separator (ISA11) is not interpreted, which is also OIE's
// behaviour: a repeated element keeps its ^ inside one value.
//
// This is a reimplementation of the behaviour, written from observing OIE's output and from its documented format; no
// code of Mirth's is in it.
package x12xml

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Root names, as OIE writes them.
const (
	RootX12 = "X12Transaction"
	RootEDI = "EDIMessage"
)

// Delimiters are the three characters (or, for a segment terminator followed by a line feed, two) that structure EDI.
type Delimiters struct {
	Segment, Element, Component string
}

// Default is what is used when the message does not say: no ISA, or one too short to read.
var Default = Delimiters{Segment: "~", Element: "*", Component: ":"}

// delimitersOf reads the delimiters from an ISA, as OIE does.
func delimitersOf(raw string) Delimiters {
	d := Default
	if strings.HasPrefix(raw, "ISA") && len(raw) > 105 {
		d.Element, d.Component, d.Segment = raw[3:4], raw[104:105], raw[105:106]
		if len(raw) > 106 && raw[106] == '\n' {
			d.Segment += "\n"
		}
	}
	return d
}

// segmentID is what an XML element named after a segment may be.
var segmentID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// ToXML renders an X12 or EDI message as OIE's XML.
func ToXML(raw []byte) ([]byte, error) {
	text := string(raw)
	d := delimitersOf(text)

	// Lines are re-joined with a line feed whatever ended them, and the result trimmed, as OIE reads its input; a
	// carriage return is therefore never part of a value.
	var norm strings.Builder
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), len(text)+1)
	sc.Split(scanLines)
	for sc.Scan() {
		norm.WriteString(sc.Text())
		norm.WriteByte('\n')
	}
	message := strings.TrimFunc(norm.String(), func(r rune) bool { return r <= ' ' })
	if len(message) < 3 {
		return nil, errors.New("x12xml: the message is empty or too short to be EDI")
	}

	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	first := true
	root := RootEDI
	for _, segment := range splitOn(message, d.Segment) {
		tokens := tokensOf(segment, d.Element)
		id := strings.TrimFunc(tokens[0], func(r rune) bool { return r <= ' ' })
		if !segmentID.MatchString(id) {
			return nil, fmt.Errorf("x12xml: %q does not begin with a segment identifier", truncate(segment))
		}
		if first {
			if id == "ISA" {
				root = RootX12
			}
			fmt.Fprintf(&out, `<%s segmentDelimiter="%s" elementDelimiter="%s" subelementDelimiter="%s">`,
				root, escapeAttr(d.Segment), escapeAttr(d.Element), escapeAttr(d.Component))
			first = false
		}
		out.WriteString("<" + id + ">")
		writeElements(&out, id, tokens[1:], d)
		out.WriteString("</" + id + ">")
	}
	out.WriteString("</" + root + ">")
	return out.Bytes(), nil
}

// writeElements writes a segment's data elements: each delimiter starts the next element, and an element that is only
// followed by another delimiter, or ends the segment, is written empty.
func writeElements(out *bytes.Buffer, id string, tokens []string, d Delimiters) {
	number := 0
	pendingEmpty := false
	name := func() string { return id + "." + twoDigits(number) }
	for _, tok := range tokens {
		if tok == d.Element {
			if pendingEmpty {
				out.WriteString("<" + name() + "></" + name() + ">")
			}
			number++
			pendingEmpty = true
			continue
		}
		pendingEmpty = false
		el := name()
		out.WriteString("<" + el + ">")
		if strings.Contains(tok, d.Component) {
			component := 1
			pendingComponent := true
			for _, part := range tokensOf(tok, d.Component) {
				if part == d.Component {
					if pendingComponent {
						writeComponent(out, el, component, "")
					}
					component++
					pendingComponent = true
					continue
				}
				pendingComponent = false
				writeComponent(out, el, component, part)
			}
			if pendingComponent {
				writeComponent(out, el, component, "")
			}
		} else {
			writeComponent(out, el, 1, tok)
		}
		out.WriteString("</" + el + ">")
	}
	if pendingEmpty {
		out.WriteString("<" + name() + "></" + name() + ">")
	}
}

func writeComponent(out *bytes.Buffer, element string, n int, value string) {
	name := element + "." + strconv.Itoa(n)
	out.WriteString("<" + name + ">" + escapeText(value) + "</" + name + ">")
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// splitOn splits on any character of set, dropping empty pieces, which is how OIE separates segments: a terminator
// written as "~\n" splits on either character, and a blank line between segments is no segment.
func splitOn(s, set string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(set, r) })
}

// tokensOf splits on any character of set and keeps each delimiter as a token of its own.
func tokensOf(s, set string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if strings.ContainsRune(set, r) {
			if i > start {
				out = append(out, s[start:i])
			}
			out = append(out, string(r))
			start = i + len(string(r))
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// scanLines splits as Java's readLine does: on a line feed, a carriage return, or the pair.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func escapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;", "\n", "&#10;", "\r", "&#13;")
	return r.Replace(s)
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// isaWidths are the fixed widths of ISA01 to ISA15. A value that lost its padding on the way through XML (spaces
// trimmed by an editor, a short value set by a script) is padded back so the ISA stays valid: the control number with
// leading zeros, everything else with trailing spaces.
var isaWidths = []int{2, 10, 2, 10, 2, 15, 2, 15, 6, 4, 1, 5, 9, 1, 1}

const isaControlNumber = 13

// Whitespace a pretty-printer puts between tags is removed before reading, as OIE does, so indented XML reads the same
// as XML on one line. Whitespace inside a value is kept.
var (
	beforeStartTag = regexp.MustCompile(`\s*<([^/][^>]*)>`)
	afterEndTag    = regexp.MustCompile(`<([^>]*/|/[^>]*)>\s*`)
)

// FromXML writes OIE's XML back as X12 or EDI.
//
// Elements are placed by their number, so a gap (CLM.01 then CLM.03) is written as an empty element between them, and
// components likewise. The delimiters come from the root's attributes, defaulting to ~ * and :.
func FromXML(doc []byte) ([]byte, error) {
	doc = afterEndTag.ReplaceAll(beforeStartTag.ReplaceAll(doc, []byte("<$1>")), []byte("<$1>"))
	dec := xml.NewDecoder(bytes.NewReader(doc))
	// A DOCTYPE is refused, as OIE refuses one: a document type is the door to entity expansion.
	var (
		out           bytes.Buffer
		d             = Default
		sawRoot       bool
		depth         int // 0 document, 1 segment, 2 element, 3 component
		inISA         bool
		lastElement   int
		lastComponent int
		component     bool // a component has already been written in this element
		elementID     int
		elementStart  int
	)
	pad := func(id, start int) {
		if !inISA || id < 1 || id > len(isaWidths) {
			return
		}
		missing := isaWidths[id-1] - (out.Len() - start)
		if missing <= 0 {
			return
		}
		if id == isaControlNumber {
			b := out.Bytes()
			tail := append([]byte(nil), b[start:]...)
			out.Truncate(start)
			out.WriteString(strings.Repeat("0", missing))
			out.Write(tail)
			return
		}
		out.WriteString(strings.Repeat(" ", missing))
	}
	numberAt := func(name string, part int) (int, bool) {
		parts := strings.FieldsFunc(name, func(r rune) bool { return r == '.' })
		if len(parts) != part+1 {
			return 0, false
		}
		n, err := strconv.Atoi(parts[part])
		if err != nil {
			return 0, true
		}
		return n, true
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("x12xml: the XML cannot be read: %w", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			if bytes.HasPrefix(bytes.TrimSpace(t), []byte("DOCTYPE")) {
				return nil, errors.New("x12xml: a DOCTYPE is not accepted")
			}
		case xml.StartElement:
			name := t.Name.Local
			if !sawRoot {
				sawRoot = true
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "segmentDelimiter":
						d.Segment = a.Value
					case "elementDelimiter":
						d.Element = a.Value
					case "subelementDelimiter":
						d.Component = a.Value
					}
				}
				continue
			}
			switch depth {
			case 0:
				out.WriteString(name)
				inISA = name == "ISA"
				lastElement = 0
				depth = 1
			case 1:
				elementID = 0
				if n, ok := numberAt(name, 1); ok {
					for i := 1; i < n-lastElement; i++ {
						out.WriteString(d.Element)
						pad(lastElement+i, out.Len())
					}
					lastElement, elementID = n, n
				}
				out.WriteString(d.Element)
				elementStart = out.Len()
				lastComponent, component = 0, false
				depth = 2
			case 2:
				if n, ok := numberAt(name, 2); ok {
					for i := 1; i < n-lastComponent; i++ {
						out.WriteString(d.Component)
					}
					lastComponent = n
				}
				if component {
					out.WriteString(d.Component)
				}
				component = true
				depth = 3
			}
		case xml.EndElement:
			switch depth {
			case 1:
				out.WriteString(d.Segment)
				depth = 0
			case 2:
				pad(elementID, elementStart)
				depth = 1
			case 3:
				depth = 2
			}
		case xml.CharData:
			if sawRoot {
				out.Write(t)
			}
		}
	}
	if !sawRoot {
		return nil, errors.New("x12xml: the document is empty")
	}
	return out.Bytes(), nil
}
