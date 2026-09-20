package xmldsig

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Canonicalising one element out of a document.
//
// # Why this exists separately
//
// A signature references what it covers, usually by an identifier: Reference URI="#d1" means "this signature is over
// the element whose ID is d1". So the thing that gets hashed is a subset of the document, not the whole of it, and
// the subset has to be canonicalised with the namespace context it inherits from its ancestors.
//
// That inheritance is where the two canonicalisation forms differ, and it is the only place they do. Over a whole
// document they produce the same bytes. Over a subset, inclusive canonicalisation carries in every declaration that
// was in scope and exclusive carries in only the ones the subset actually uses - which is what lets a signed document
// be wrapped in a SOAP envelope, an IHE metadata container or anything else without the signature breaking.

// CanonicaliseElement returns the canonical form of the element with the given ID.
//
// An empty id means the whole document. Any attribute whose local name is ID, Id or id is treated as an identifier,
// because XMLDSig implementations in the field disagree about which spelling to use and a verifier that only accepts
// one spelling rejects valid signatures from half the world.
func CanonicaliseElement(doc []byte, id string, exclusive bool, inclusiveNamespaces []string) ([]byte, error) {
	if id == "" {
		return Canonicalise(doc, exclusive, inclusiveNamespaces)
	}

	// The whole document is scanned before anything is canonicalised, to find out how many elements carry this
	// identifier.
	//
	// Stopping at the first match is a documented way to be defeated: an attacker appends a second element with the
	// same ID, the verifier canonicalises and validates the first, and the application that reads the document
	// afterwards takes the second. Both are "the element with ID d1", the signature checks out, and the content
	// acted upon was never signed. So more than one is an error rather than a preference.
	found, err := countIDs(doc, id)
	if err != nil {
		return nil, err
	}
	switch found {
	case 0:
		return nil, fmt.Errorf("canonicalising: no element has the identifier %q, so there is nothing to sign "+
			"or verify; signing the whole document instead would produce a signature over content nobody asked "+
			"about", id)
	case 1:
	default:
		return nil, fmt.Errorf("canonicalising: %d elements carry the identifier %q. A signature naming an "+
			"ambiguous identifier cannot be trusted: a verifier and the application that reads the document "+
			"afterwards may each pick a different one, so the content checked is not the content used", found, id)
	}

	dec := xml.NewDecoder(bytes.NewReader(doc))
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

	// scope holds the namespace declarations seen on ancestors, so the target element can inherit them.
	var scope []map[string]string
	depth := 0
	target := -1

	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("canonicalising: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++

			declared := map[string]string{}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					declared[""] = a.Value
				case a.Name.Space == "xmlns":
					declared[a.Name.Local] = a.Value
				}
			}
			scope = append(scope, declared)

			if target < 0 && elementID(t) == id {
				target = depth

				// The ancestors' declarations are pushed onto the canonicaliser as a synthetic outer scope, so the
				// target element sees exactly what it inherits. Under exclusive rules it will use almost none of
				// them, which is the point; under inclusive rules it writes all of them.
				c.stack = append(c.stack, frame{raw: "", declared: mergeScopes(scope[:len(scope)-1])})
				c.rendered = append(c.rendered, map[string]string{})
			}

			if target > 0 {
				if err := c.start(t); err != nil {
					return nil, err
				}
			}

		case xml.EndElement:
			if target > 0 {
				if err := c.token(tok); err != nil {
					return nil, err
				}
			}
			if depth == target {
				// Done: everything inside the target has been written.
				return c.out.Bytes(), nil
			}
			depth--
			scope = scope[:len(scope)-1]

		default:
			if target > 0 {
				if err := c.token(tok); err != nil {
					return nil, err
				}
			}
		}
	}

	if target < 0 {
		return nil, fmt.Errorf("canonicalising: no element has the identifier %q", id)
	}
	return nil, fmt.Errorf("canonicalising: the element with identifier %q was never closed", id)
}

// countIDs counts elements carrying an identifier, so an ambiguous reference can be refused.
func countIDs(doc []byte, id string) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	n := 0
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("canonicalising: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok && elementID(start) == id {
			n++
		}
	}
}

// elementID reads whichever spelling of an identifier attribute an element carries.
//
// ID, Id and id are all accepted. The specification says the attribute is of XML type ID, which requires a DTD or
// schema to establish - and signed documents in the field arrive with no schema and every spelling. A verifier that
// insists on one rejects valid signatures from half the implementations in use.
func elementID(e xml.StartElement) string {
	for _, a := range e.Attr {
		// Namespaced identifier attributes such as wsu:Id are used by WS-Security, so the prefix is not required to
		// be empty - but xmlns declarations must not be mistaken for one.
		if a.Name.Space == "xmlns" {
			continue
		}
		switch a.Name.Local {
		case "ID", "Id", "id":
			return a.Value
		}
	}
	return ""
}

// mergeScopes flattens ancestor declarations into one map, nearest ancestor winning.
func mergeScopes(scopes []map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range scopes {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// IDsIn lists the identifiers a document carries, and which are duplicated.
//
// Offered because "signature invalid" is a useless message and this is one of the few cases where the reason can be
// stated precisely: the reference names an identifier that is absent, or present twice.
func IDsIn(doc []byte) (ids []string, duplicated []string, err error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	seen := map[string]int{}
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			if id := elementID(start); id != "" {
				seen[id]++
			}
		}
	}

	// Initialised empty, never nil: a caller asking how many identifiers a document has should get zero rather than
	// an error about a missing field.
	ids, duplicated = []string{}, []string{}
	for id, n := range seen {
		ids = append(ids, id)
		if n > 1 {
			duplicated = append(duplicated, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(duplicated)
	return ids, duplicated, nil
}

// StripSignature removes an element by name from a document, returning the result.
//
// Needed for the enveloped-signature transform: a signature inside the thing it signs cannot cover itself, so the
// digest is computed over the document with the Signature element taken out. Doing it by text manipulation would be
// fragile, so this reserialises through the same token path everything else uses.
func StripSignature(doc []byte, localName, namespace string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	var out bytes.Buffer
	skipDepth := 0
	depth := 0

	// Prefix bindings tracked so the element can be matched on its namespace URI rather than on whatever prefix the
	// sender happened to choose. Matching on the prefix means a document using ds: is handled and one using sig: is
	// not, and both are valid.
	scopes := []map[string]string{}

	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++

			declared := map[string]string{}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					declared[""] = a.Value
				case a.Name.Space == "xmlns":
					declared[a.Name.Local] = a.Value
				}
			}
			scopes = append(scopes, declared)

			if skipDepth == 0 && t.Name.Local == localName {
				uri := mergeScopes(scopes)[t.Name.Space]
				if namespace == "" || uri == namespace {
					skipDepth = depth
					continue
				}
			}
			if skipDepth > 0 {
				continue
			}
			writeRawStart(&out, t)

		case xml.EndElement:
			if skipDepth > 0 {
				if depth == skipDepth {
					skipDepth = 0
				}
				depth--
				scopes = scopes[:len(scopes)-1]
				continue
			}
			name := t.Name.Local
			if t.Name.Space != "" {
				name = t.Name.Space + ":" + t.Name.Local
			}
			fmt.Fprintf(&out, "</%s>", name)
			depth--
			scopes = scopes[:len(scopes)-1]

		case xml.CharData:
			if skipDepth == 0 {
				out.Write(escapeText(t))
			}

		case xml.Comment:
			if skipDepth == 0 {
				fmt.Fprintf(&out, "<!--%s-->", t)
			}

		case xml.ProcInst:
			if skipDepth == 0 && !strings.EqualFold(t.Target, "xml") {
				fmt.Fprintf(&out, "<?%s %s?>", t.Target, t.Inst)
			}
		}
	}

	return out.Bytes(), nil
}

func writeRawStart(out *bytes.Buffer, t xml.StartElement) {
	name := t.Name.Local
	if t.Name.Space != "" {
		name = t.Name.Space + ":" + t.Name.Local
	}
	out.WriteString("<")
	out.WriteString(name)
	for _, a := range t.Attr {
		an := a.Name.Local
		if a.Name.Space != "" {
			an = a.Name.Space + ":" + a.Name.Local
		}
		fmt.Fprintf(out, ` %s="%s"`, an, escapeAttr(a.Value))
	}
	out.WriteString(">")
}
