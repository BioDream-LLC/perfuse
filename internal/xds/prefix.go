package xds

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// prefixes are the conventional prefixes of the XDS namespaces, the ones IHE's and the Direct Project's samples use.
var prefixes = map[string]string{
	nsSOAP12: "soapenv", nsWSA: "wsa", nsXOP: "xop", "urn:direct:addressing": "direct",
	"urn:ihe:iti:xds-b:2007": "xdsb", "urn:oasis:names:tc:ebxml-regrep:xsd:lcm:3.0": "lcm",
	"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0": "rim", "urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0": "rs",
	"urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0": "query", "http://www.w3.org/2001/XMLSchema-instance": "xsi",
}

const nsXML = "http://www.w3.org/XML/1998/namespace"

// prefixed rewrites XML as encoding/xml writes it, every element redeclaring its namespace as the default, into the
// prefixed form: each known namespace bound once, on the root, to its usual prefix. Both mean the same, but ONC's XDR
// validator (SITE, on Apache Axiom) faults on the first ("Local name may not be null or empty"). A namespace without a
// usual prefix stays the default, declared only where it changes. Text and attribute values are kept. A body whose root
// is not in one of these namespaces (HL7 v3's, for PIX and PDQ) is returned as it is.
func prefixed(in []byte) ([]byte, error) {
	used, rootNS, err := namespacesIn(in)
	if err != nil {
		return nil, err
	}
	if _, ok := prefixes[rootNS]; !ok {
		return in, nil
	}
	d := xml.NewDecoder(bytes.NewReader(in))
	var out bytes.Buffer
	defaults := []string{""}
	root := true
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		switch v := t.(type) {
		case xml.StartElement:
			def := defaults[len(defaults)-1]
			out.WriteByte('<')
			p, known := prefixes[v.Name.Space]
			if known {
				out.WriteString(p + ":")
			}
			out.WriteString(v.Name.Local)
			if root {
				for _, ns := range sortedKnown() {
					if used[ns] {
						out.WriteString(` xmlns:` + prefixes[ns] + `="` + ns + `"`)
					}
				}
				root = false
			}
			if !known && v.Name.Space != def {
				def = v.Name.Space
				out.WriteString(` xmlns="` + attrEscape(def) + `"`)
			}
			defaults = append(defaults, def)
			for _, a := range v.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				out.WriteByte(' ')
				switch {
				case a.Name.Space == "":
				case a.Name.Space == nsXML || a.Name.Space == "xml":
					out.WriteString("xml:")
				default:
					ap, ok := prefixes[a.Name.Space]
					if !ok {
						return nil, errors.New("an attribute in namespace " + a.Name.Space + " has no prefix to write it with")
					}
					out.WriteString(ap + ":")
				}
				out.WriteString(a.Name.Local + `="` + attrEscape(a.Value) + `"`)
			}
			out.WriteByte('>')
		case xml.EndElement:
			defaults = defaults[:len(defaults)-1]
			out.WriteString("</")
			if p, ok := prefixes[v.Name.Space]; ok {
				out.WriteString(p + ":")
			}
			out.WriteString(v.Name.Local + ">")
		case xml.CharData:
			out.WriteString(textEscape(string(v)))
		case xml.Comment:
			out.WriteString("<!--" + string(v) + "-->")
		}
	}
}

// namespacesIn is every namespace an element or attribute of the XML is in, and the root element's.
func namespacesIn(in []byte) (map[string]bool, string, error) {
	used := map[string]bool{}
	root := ""
	first := true
	d := xml.NewDecoder(bytes.NewReader(in))
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			return used, root, nil
		}
		if err != nil {
			return nil, "", err
		}
		if v, ok := t.(xml.StartElement); ok {
			if first {
				root, first = v.Name.Space, false
			}
			used[v.Name.Space] = true
			for _, a := range v.Attr {
				used[a.Name.Space] = true
			}
		}
	}
}

func sortedKnown() []string {
	return []string{nsSOAP12, nsWSA, nsXOP, "urn:direct:addressing", "http://www.w3.org/2001/XMLSchema-instance", "urn:ihe:iti:xds-b:2007",
		"urn:oasis:names:tc:ebxml-regrep:xsd:lcm:3.0", "urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0",
		"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0", "urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0"}
}

var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")
var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\r", "&#xD;", "\n", "&#xA;", "\t", "&#x9;")

func textEscape(s string) string { return textEscaper.Replace(s) }
func attrEscape(s string) string { return attrEscaper.Replace(s) }
