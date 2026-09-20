package xmldsig

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// rootCloseOffset finds the byte offset of the root element's closing tag.
//
// Found by parsing rather than by searching for the last "</" in the file. A closing tag can appear inside a CDATA
// section, a comment or an attribute value, and a signature inserted at the wrong offset produces a document that
// either fails to parse or - worse - parses into something different from what was signed.
func rootCloseOffset(doc []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	depth := 0
	for {
		// The offset is read before the token, because InputOffset after reading an end tag is past it.
		before := dec.InputOffset()

		tok, err := dec.RawToken()
		if err == io.EOF {
			return 0, fmt.Errorf("signing: the document has no root element to sign")
		}
		if err != nil {
			return 0, fmt.Errorf("signing: %w", err)
		}

		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				return int(before), nil
			}
		}
	}
}
