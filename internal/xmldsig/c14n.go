// Package xmldsig implements XML Digital Signatures and the XAdES profile of them.
//
// # Why this is written rather than borrowed
//
// A signed clinical document is a legal artefact. It asserts that a named person, at a named time, took
// responsibility for a specific set of bytes. Getting that wrong in either direction is serious: a signature that
// cannot be verified makes a valid document look forged, and a verifier that is too generous accepts a document
// somebody altered.
//
// # Canonicalisation is the whole problem
//
// XML has many byte sequences that mean the same thing. Attribute order is not significant, namespace prefixes can
// be renamed, empty elements can be written two ways, and whitespace inside a tag is free. A signature is over
// bytes, so before hashing anything both ends must agree on exactly one byte sequence for a given document. That
// agreement is canonicalisation, and it is where implementations go wrong, because every mistake produces a
// signature that verifies against your own output and fails against everybody else's.
//
// So this file is the canonicalisation, on its own, with its own tests. Nothing about signing appears here.
//
// # Exclusive rather than inclusive, by default
//
// Inclusive canonicalisation pulls in every namespace declaration that is in scope, including ones the signed
// element never uses. That makes a signature break when the document is later embedded inside something that
// declares an unrelated namespace on an ancestor, which is precisely what happens when a CDA is put inside a SOAP
// envelope or an IHE metadata wrapper. Exclusive canonicalisation includes only the declarations the element
// actually uses, and is what document exchange profiles specify for that reason.
package xmldsig

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Algorithm identifiers, as they appear in a signature.
const (
	// C14NExclusive is exclusive canonicalisation without comments.
	C14NExclusive = "http://www.w3.org/2001/10/xml-exc-c14n#"

	// C14NInclusive is the original canonical XML.
	C14NInclusive = "http://www.w3.org/TR/2001/REC-xml-c14n-20010315"
)

// Canonicalise writes the canonical form of an XML document or fragment.
//
// inclusiveNamespaces lists prefixes that must be treated as visibly used even when they are not, which exists
// because some profiles require it for content whose namespaces appear only inside attribute values or XPath
// expressions - places a canonicaliser cannot see into.
func Canonicalise(doc []byte, exclusive bool, inclusiveNamespaces []string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))

	// Entity expansion left off deliberately.
	//
	// An external entity in a document being signed or verified is an attempt to make the canonicaliser fetch
	// something, and a signature over content that was pulled from a URL at verification time is a signature over
	// whatever that URL served that day.
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	c := &canonicaliser{
		out:       &bytes.Buffer{},
		exclusive: exclusive,
		forced:    map[string]bool{},
	}
	for _, p := range inclusiveNamespaces {
		c.forced[p] = true
	}

	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("canonicalising: %w", err)
		}
		if err := c.token(tok); err != nil {
			return nil, err
		}
	}

	if len(c.stack) != 0 {
		return nil, fmt.Errorf("canonicalising: %d element(s) were never closed", len(c.stack))
	}
	return c.out.Bytes(), nil
}

// canonicaliser walks tokens keeping track of namespace scope.
//
// RawToken is used rather than Token because Token resolves prefixes into namespace URIs and discards the prefixes
// themselves. Canonical XML has to reproduce prefixes exactly - two documents differing only in prefix are
// different bytes and have different signatures - so the resolution has to be done here, where the prefix is still
// visible.
type canonicaliser struct {
	out       *bytes.Buffer
	exclusive bool
	forced    map[string]bool

	// stack holds one frame per open element.
	stack []frame

	// depth tracks how deep we are so a leading declaration can be handled once.
	rendered []map[string]string
}

type frame struct {
	// raw is the element name exactly as written, prefix included, so the end tag matches the start tag.
	raw string

	// declared maps prefix to URI for declarations written on this element.
	declared map[string]string
}

func (c *canonicaliser) token(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		return c.start(t)

	case xml.EndElement:
		if len(c.stack) == 0 {
			return fmt.Errorf("canonicalising: an end tag appeared with nothing open")
		}
		top := c.stack[len(c.stack)-1]
		c.stack = c.stack[:len(c.stack)-1]
		c.rendered = c.rendered[:len(c.rendered)-1]

		// Always a full end tag. Canonical XML has no self-closing form: <a/> and <a></a> are the same element and
		// must produce the same bytes, so one of the two spellings has to win and the specification picks this one.
		fmt.Fprintf(c.out, "</%s>", top.raw)
		return nil

	case xml.CharData:
		c.out.Write(escapeText(t))
		return nil

	case xml.Comment:
		// Dropped. Both canonicalisation forms used here are the without-comments variants, because a signature
		// that covers comments breaks when anything in the pipeline reformats the document, and reformatting
		// without changing meaning is something intermediaries do constantly.
		return nil

	case xml.ProcInst:
		// The XML declaration is not a processing instruction to canonical XML and is removed; anything else is
		// kept, in canonical form.
		if strings.EqualFold(t.Target, "xml") {
			return nil
		}
		if len(t.Inst) == 0 {
			fmt.Fprintf(c.out, "<?%s?>", t.Target)
		} else {
			fmt.Fprintf(c.out, "<?%s %s?>", t.Target, t.Inst)
		}
		return nil

	case xml.Directive:
		// Doctype and friends are removed by canonicalisation. A signature must not depend on a DTD, since a DTD
		// can change how a document parses.
		return nil
	}
	return nil
}

func (c *canonicaliser) start(t xml.StartElement) error {
	// RawToken leaves the prefix in Name.Space, which is what is wanted here.
	raw := t.Name.Local
	prefix := t.Name.Space
	if prefix != "" {
		raw = prefix + ":" + t.Name.Local
	}

	declared := map[string]string{}
	var attrs []xml.Attr

	for _, a := range t.Attr {
		switch {
		case a.Name.Space == "" && a.Name.Local == "xmlns":
			declared[""] = a.Value
		case a.Name.Space == "xmlns":
			declared[a.Name.Local] = a.Value
		default:
			attrs = append(attrs, a)
		}
	}

	// The namespaces in scope, and which have already been written out by an ancestor.
	inScope := map[string]string{}
	already := map[string]string{}
	if len(c.rendered) > 0 {
		for k, v := range c.rendered[len(c.rendered)-1] {
			already[k] = v
		}
	}
	for _, f := range c.stack {
		for k, v := range f.declared {
			inScope[k] = v
		}
	}
	for k, v := range declared {
		inScope[k] = v
	}

	// Which declarations to write.
	var emit []string
	if c.exclusive {
		// Exclusive: only the prefixes this element visibly uses, plus any the caller insisted on.
		//
		// "Visibly uses" means the element's own prefix and the prefixes of its attributes. Not its children's -
		// they will declare their own when their turn comes - and not declarations that merely happen to be in
		// scope, which is the whole point: a signature that included an unrelated ancestor declaration would break
		// the moment the document was embedded in a SOAP envelope.
		used := map[string]bool{prefix: true}
		for _, a := range attrs {
			if a.Name.Space != "" {
				used[a.Name.Space] = true
			}
		}
		for p := range c.forced {
			used[p] = true
		}

		for p := range used {
			// The xml prefix is bound by definition and never declared.
			if p == "xml" {
				continue
			}
			uri, ok := inScope[p]
			if !ok {
				if p == "" {
					// No default namespace anywhere. Nothing to write, and nothing to undeclare.
					continue
				}
				return fmt.Errorf("canonicalising: prefix %q is used on <%s> and never declared", p, raw)
			}
			if already[p] == uri {
				continue
			}
			emit = append(emit, p)
		}

		// An element in no namespace inside one that has a default namespace must undeclare it, or the
		// canonical form says the element is in its parent's namespace, which is a different document.
		if prefix == "" {
			if _, declaredHere := declared[""]; !declaredHere {
				if already[""] != "" && inScope[""] == "" {
					emit = append(emit, "")
				}
			}
		}
	} else {
		// Inclusive: every declaration in scope that an ancestor has not already written.
		for p, uri := range inScope {
			if p == "xml" {
				continue
			}
			if already[p] == uri {
				continue
			}
			if uri == "" && already[p] == "" {
				continue
			}
			emit = append(emit, p)
		}
	}

	// Namespace declarations sort by prefix, with the default namespace first. Attributes sort after them by
	// namespace URI then local name. This ordering is not cosmetic: it is the ordering both ends must agree on.
	sort.Slice(emit, func(i, j int) bool { return emit[i] < emit[j] })

	sort.SliceStable(attrs, func(i, j int) bool {
		ui, uj := inScope[attrs[i].Name.Space], inScope[attrs[j].Name.Space]
		if attrs[i].Name.Space == "" {
			ui = ""
		}
		if attrs[j].Name.Space == "" {
			uj = ""
		}
		if ui != uj {
			return ui < uj
		}
		return attrs[i].Name.Local < attrs[j].Name.Local
	})

	c.out.WriteString("<")
	c.out.WriteString(raw)

	nowRendered := map[string]string{}
	for k, v := range already {
		nowRendered[k] = v
	}
	for _, p := range emit {
		uri := inScope[p]
		if p == "" {
			fmt.Fprintf(c.out, ` xmlns="%s"`, escapeAttr(uri))
		} else {
			fmt.Fprintf(c.out, ` xmlns:%s="%s"`, p, escapeAttr(uri))
		}
		nowRendered[p] = uri
	}

	for _, a := range attrs {
		name := a.Name.Local
		if a.Name.Space != "" {
			name = a.Name.Space + ":" + a.Name.Local
		}
		fmt.Fprintf(c.out, ` %s="%s"`, name, escapeAttr(a.Value))
	}

	c.out.WriteString(">")

	c.stack = append(c.stack, frame{raw: raw, declared: declared})
	c.rendered = append(c.rendered, nowRendered)
	return nil
}

// escapeText escapes character data as canonical XML requires.
//
// The rules differ from ordinary XML escaping and the differences matter. A carriage return becomes a character
// reference rather than a literal byte, because XML parsing normalises literal carriage returns away: leaving one
// as a byte means the verifier's parser deletes it and computes a different digest. Greater-than is escaped even
// though it need not be, because the specification says so and both ends must do the same thing.
func escapeText(s []byte) []byte {
	var b bytes.Buffer
	for _, c := range s {
		switch c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteByte(c)
		}
	}
	return b.Bytes()
}

// escapeAttr escapes an attribute value as canonical XML requires.
//
// Tab, newline and carriage return become character references. Without that, attribute-value normalisation in the
// verifier's parser turns them into spaces before the digest is computed, and the two ends disagree about bytes
// that look identical on screen.
func escapeAttr(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '"':
			b.WriteString("&quot;")
		case '\t':
			b.WriteString("&#x9;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
