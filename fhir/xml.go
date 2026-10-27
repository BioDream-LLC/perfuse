package fhir

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// FHIR XML, written from FHIR JSON.
//
// The two formats hold the same content with different rules. In XML a primitive's value is an attribute
// (<gender value="male"/>), an element's id and an extension's url are attributes, a resource is an element named
// after its type, the narrative is XHTML in place, and - the one rule that needs the specification - every element
// must come in the order its definition lists, where JSON may hold members in any order. That order comes from the
// core package's StructureDefinitions (xmlordergen), kept compressed beside this file.

//go:generate sh -c "go run ./xmlordergen $PKG_R4 | gzip -9 -n > xmlorder_r4.txt.gz"
//go:generate sh -c "go run ./xmlordergen $PKG_R5 | gzip -9 -n > xmlorder_r5.txt.gz"

//go:embed xmlorder_r4.txt.gz
var xmlOrderR4 []byte

//go:embed xmlorder_r5.txt.gz
var xmlOrderR5 []byte

// xmlChild is one child element as its definition lists it.
type xmlChild struct {
	name   string   // "gender", or "deceased" for deceased[x]
	choice bool     // a [x] element: the JSON name carries the type
	path   string   // where its own children are listed, when that does not depend on its type
	types  []string // its types, for a choice
}

type xmlOrder map[string][]xmlChild

var (
	xmlOrderOnce [2]sync.Once
	xmlOrders    [2]xmlOrder
)

func orderFor(version Version) xmlOrder {
	i, data := 0, xmlOrderR4
	if version == R5 {
		i, data = 1, xmlOrderR5
	}
	xmlOrderOnce[i].Do(func() { xmlOrders[i] = parseXMLOrder(data) })
	return xmlOrders[i]
}

func parseXMLOrder(data []byte) xmlOrder {
	order := xmlOrder{}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return order
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		path, rest, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		for _, field := range strings.Fields(rest) {
			name, how, _ := strings.Cut(field, ":")
			c := xmlChild{name: name}
			if strings.HasSuffix(name, "[x]") {
				c.name, c.choice = strings.TrimSuffix(name, "[x]"), true
			}
			switch {
			case strings.HasPrefix(how, "@"), strings.HasPrefix(how, "#"):
				c.path = how[1:]
			default:
				c.types = strings.Split(how, ",")
				if len(c.types) == 1 {
					c.path = datatypePath(c.types[0])
				}
			}
			order[path] = append(order[path], c)
		}
	}
	return order
}

// datatypePath is where a type's children are listed: the profiles of Quantity are listed as Quantity.
func datatypePath(t string) string {
	switch t {
	case "Age", "Count", "Distance", "Duration", "MoneyQuantity", "SimpleQuantity":
		return "Quantity"
	}
	return t
}

// jsonValue keeps a JSON object's members in the order they came, which a map would lose.
type jsonValue struct {
	object []jsonMember
	array  []*jsonValue
	scalar string // a string's text, or a number's or boolean's literal
	kind   byte   // 'o', 'a', 's' (string), 'n' (number or boolean), 0 (null)
}

type jsonMember struct {
	key   string
	value *jsonValue
}

func (v *jsonValue) get(key string) *jsonValue {
	if v == nil {
		return nil
	}
	for _, m := range v.object {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

func decodeOrdered(dec *json.Decoder) (*jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := &jsonValue{kind: 'o'}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := k.(string)
				val, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				v.object = append(v.object, jsonMember{key, val})
			}
			_, err := dec.Token()
			return v, err
		case '[':
			v := &jsonValue{kind: 'a'}
			for dec.More() {
				val, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				v.array = append(v.array, val)
			}
			_, err := dec.Token()
			return v, err
		}
	case string:
		return &jsonValue{kind: 's', scalar: t}, nil
	case json.Number:
		return &jsonValue{kind: 'n', scalar: t.String()}, nil
	case bool:
		return &jsonValue{kind: 'n', scalar: fmt.Sprint(t)}, nil
	case nil:
		return &jsonValue{}, nil
	}
	return nil, errors.New("unexpected JSON")
}

// JSONToXML writes a FHIR resource given as JSON in FHIR XML, in the element order of the release given.
func JSONToXML(data []byte, version Version) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	root, err := decodeOrdered(dec)
	if err != nil {
		return nil, fmt.Errorf("fhir: reading JSON for XML: %w", err)
	}
	if root.kind != 'o' || root.get("resourceType") == nil {
		return nil, errors.New("fhir: not a FHIR resource")
	}
	w := &xmlWriter{order: orderFor(version)}
	w.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w.resource(root)
	w.b.WriteByte('\n')
	return w.b.Bytes(), nil
}

type xmlWriter struct {
	b     bytes.Buffer
	order xmlOrder
}

func (w *xmlWriter) resource(v *jsonValue) {
	rt := v.get("resourceType").scalar
	w.b.WriteString("<" + rt + ` xmlns="http://hl7.org/fhir">`)
	w.children(rt, v, true)
	w.b.WriteString("</" + rt + ">")
}

// children writes an object's members as elements, in the order the definition at path lists them, then any it
// does not list (a newer element, or one this release lacks) in the order they came.
func (w *xmlWriter) children(path string, v *jsonValue, isResource bool) {
	done := map[string]bool{}
	if isResource {
		done["resourceType"] = true
	} else {
		done["id"] = true // an attribute
		if path == "Extension" {
			done["url"] = true
		}
	}
	for _, c := range w.order[path] {
		if c.choice {
			for _, m := range v.object {
				key := strings.TrimPrefix(m.key, "_")
				if done[key] || !strings.HasPrefix(key, c.name) || len(key) == len(c.name) {
					continue
				}
				suffix := key[len(c.name):]
				if suffix[0] < 'A' || suffix[0] > 'Z' {
					continue
				}
				t := choiceType(suffix, c.types)
				w.member(key, v, datatypePath(t))
				done[key] = true
			}
			continue
		}
		if done[c.name] {
			continue
		}
		if v.get(c.name) != nil || v.get("_"+c.name) != nil {
			w.member(c.name, v, c.path)
		}
		done[c.name] = true
	}
	for _, m := range v.object {
		key := strings.TrimPrefix(m.key, "_")
		if done[key] {
			continue
		}
		done[key] = true
		w.member(key, v, "")
	}
}

// choiceType names the type of value[x] written as, say, valueQuantity or valueString.
func choiceType(suffix string, types []string) string {
	for _, t := range types {
		if strings.EqualFold(t, suffix) {
			return t
		}
	}
	return suffix
}

// member writes the element key of v, a single value or a list, with the id and extensions JSON keeps in _key.
func (w *xmlWriter) member(key string, parent *jsonValue, path string) {
	val, under := parent.get(key), parent.get("_"+key)
	if (val != nil && val.kind == 'a') || (under != nil && under.kind == 'a') {
		n := 0
		if val != nil {
			n = len(val.array)
		}
		if under != nil && len(under.array) > n {
			n = len(under.array)
		}
		for i := 0; i < n; i++ {
			var vi, ui *jsonValue
			if val != nil && i < len(val.array) {
				vi = val.array[i]
			}
			if under != nil && i < len(under.array) {
				ui = under.array[i]
			}
			w.element(key, vi, ui, path)
		}
		return
	}
	w.element(key, val, under, path)
}

func (w *xmlWriter) element(name string, v, under *jsonValue, path string) {
	if v != nil && v.kind == 0 {
		v = nil
	}
	if under != nil && under.kind == 0 {
		under = nil
	}
	if v == nil && under == nil {
		return
	}
	switch {
	case v != nil && v.kind == 'o' && v.get("resourceType") != nil && (path == "" || path == "Resource" || w.order[path] == nil):
		w.b.WriteString("<" + name + ">")
		w.resource(v)
		w.b.WriteString("</" + name + ">")
	case name == "div" && v != nil && v.kind == 's':
		w.b.WriteString(xhtml(v.scalar))
	case v == nil || v.kind == 's' || v.kind == 'n':
		w.b.WriteString("<" + name)
		if id := under.get("id"); id != nil {
			w.attr("id", id.scalar)
		}
		if v != nil {
			w.attr("value", v.scalar)
		}
		if under != nil && (under.get("extension") != nil) {
			w.b.WriteString(">")
			w.member("extension", under, "Extension")
			w.b.WriteString("</" + name + ">")
		} else {
			w.b.WriteString("/>")
		}
	case v.kind == 'o':
		w.b.WriteString("<" + name)
		if id := v.get("id"); id != nil {
			w.attr("id", id.scalar)
		}
		if path == "Extension" {
			if url := v.get("url"); url != nil {
				w.attr("url", url.scalar)
			}
		}
		w.b.WriteString(">")
		w.children(path, v, false)
		w.b.WriteString("</" + name + ">")
	}
}

func (w *xmlWriter) attr(name, value string) {
	w.b.WriteString(" " + name + `="`)
	escapeXML(&w.b, value)
	w.b.WriteByte('"')
}

func escapeXML(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '\t':
			b.WriteString("&#9;")
		default:
			b.WriteRune(r)
		}
	}
}

// xhtml is the narrative as it goes into XML: in place, in the XHTML namespace, which FHIR JSON is required to carry
// but not every sender does.
func xhtml(div string) string {
	div = strings.TrimSpace(div)
	if !strings.HasPrefix(div, "<div") {
		var b bytes.Buffer
		escapeXML(&b, div)
		return `<div xmlns="http://www.w3.org/1999/xhtml">` + b.String() + `</div>`
	}
	head, _, _ := strings.Cut(div, ">")
	if !strings.Contains(head, "xmlns") {
		div = `<div xmlns="http://www.w3.org/1999/xhtml"` + div[len("<div"):]
	}
	return div
}
