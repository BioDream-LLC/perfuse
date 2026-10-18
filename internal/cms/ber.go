package cms

import (
	"errors"
)

// ToDER rewrites BER as DER where Go's encoding/asn1 needs it: indefinite lengths become definite, and a constructed
// OCTET STRING (a stream of segments) becomes one primitive OCTET STRING.
//
// BouncyCastle, and so most Java AS2 gateways, write CMS with indefinite lengths ("streaming" BER), and encoding/asn1
// reads DER only. The rewrite changes the encoding and not a value, so what decrypts or decompresses afterwards is what the
// sender put in. It is not used on anything a signature is checked over: signed attributes are DER already, and changing
// their bytes would break the signature.
func ToDER(b []byte) ([]byte, error) {
	out, rest, err := berElement(b, 0)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, errors.New("cms: bytes after the end of the BER value")
	}
	return out, nil
}

const berMaxDepth = 64

// berElement reads one element and returns it as DER, and what follows it.
func berElement(b []byte, depth int) (der, rest []byte, err error) {
	if depth > berMaxDepth {
		return nil, nil, errors.New("cms: BER nested too deep")
	}
	if len(b) < 2 {
		return nil, nil, errors.New("cms: truncated BER")
	}
	// The identifier: one byte, or more in the high-tag-number form.
	idLen := 1
	if b[0]&0x1f == 0x1f {
		for idLen < len(b) && b[idLen]&0x80 != 0 {
			idLen++
		}
		idLen++
		if idLen > len(b) || idLen > 6 {
			return nil, nil, errors.New("cms: BER tag cannot be read")
		}
	}
	id := b[:idLen]
	constructed := b[0]&0x20 != 0
	p := b[idLen:]
	if len(p) == 0 {
		return nil, nil, errors.New("cms: truncated BER length")
	}

	var content []byte
	if p[0] == 0x80 {
		// Indefinite: children until the end-of-contents marker.
		if !constructed {
			return nil, nil, errors.New("cms: a primitive BER value with an indefinite length")
		}
		p = p[1:]
		var children [][]byte
		for {
			if len(p) >= 2 && p[0] == 0 && p[1] == 0 {
				p = p[2:]
				break
			}
			if len(p) == 0 {
				return nil, nil, errors.New("cms: BER value never ends")
			}
			var child []byte
			if child, p, err = berElement(p, depth+1); err != nil {
				return nil, nil, err
			}
			children = append(children, child)
		}
		rest = p
		content = joinChildren(id, children)
	} else {
		n, lenLen, err := berLength(p)
		if err != nil {
			return nil, nil, err
		}
		p = p[lenLen:]
		if n > len(p) {
			return nil, nil, errors.New("cms: BER length runs past the end")
		}
		body := p[:n]
		rest = p[n:]
		if constructed {
			var children [][]byte
			for len(body) > 0 {
				var child []byte
				if child, body, err = berElement(body, depth+1); err != nil {
					return nil, nil, err
				}
				children = append(children, child)
			}
			content = joinChildren(id, children)
		} else {
			content = body
		}
	}

	if len(id) == 1 && id[0] == 0x24 {
		// A constructed OCTET STRING: its segments' contents, as one primitive OCTET STRING.
		id = []byte{0x04}
		if content, err = octetSegments(content); err != nil {
			return nil, nil, err
		}
	}
	return append(append(append([]byte{}, id...), derLength(len(content))...), content...), rest, nil
}

// joinChildren is the content of a constructed value from its children's DER.
func joinChildren(_ []byte, children [][]byte) []byte {
	var out []byte
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

// octetSegments concatenates the contents of a run of OCTET STRING segments, already DER.
func octetSegments(b []byte) ([]byte, error) {
	out := []byte{}
	for len(b) > 0 {
		if b[0] != 0x04 {
			return nil, errors.New("cms: a segment of an OCTET STRING is not an OCTET STRING")
		}
		n, l, err := berLength(b[1:])
		if err != nil || 1+l+n > len(b) {
			return nil, errors.New("cms: an OCTET STRING segment cannot be read")
		}
		out = append(out, b[1+l:1+l+n]...)
		b = b[1+l+n:]
	}
	return out, nil
}

func berLength(p []byte) (n, size int, err error) {
	if len(p) == 0 {
		return 0, 0, errors.New("cms: truncated BER length")
	}
	if p[0] < 0x80 {
		return int(p[0]), 1, nil
	}
	k := int(p[0] & 0x7f)
	if k == 0 || k > 4 || 1+k > len(p) {
		return 0, 0, errors.New("cms: BER length cannot be read")
	}
	for i := 1; i <= k; i++ {
		n = n<<8 | int(p[i])
	}
	if n < 0 {
		return 0, 0, errors.New("cms: BER length out of range")
	}
	return n, 1 + k, nil
}

func derLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for v := n; v > 0; v >>= 8 {
		b = append([]byte{byte(v)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}
