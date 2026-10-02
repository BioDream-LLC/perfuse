// Package ldap speaks enough LDAP to authenticate a person and read their groups.
//
// Hospitals run Active Directory almost universally, and AD speaks LDAP. OpenID Connect is the better answer where it is
// reachable - see internal/oidc, and the note there about Mirth having no single sign-on at any price - but an
// integration engine often sits on an isolated VLAN where a browser cannot reach an identity provider at all. That is
// what this is for.
//
// Hand-rolled BER rather than a dependency, for the same reason as DICOM and STOMP: the encoding is small, and what
// arrives on this socket decides whether somebody gets into a system that moves patient data.
//
// encoding/asn1 in the standard library is not usable here. It targets DER and rejects the context-specific tags and
// implicit tagging that LDAP uses throughout, so the parts that matter would need hand-rolling anyway.
package ldap

import (
	"errors"
	"fmt"
	"math"
)

// BER tag classes and the constructed bit, from X.690.
const (
	classUniversal   = 0x00
	classApplication = 0x40
	classContext     = 0x80
	constructed      = 0x20
)

// Universal tags used by LDAP.
const (
	tagBoolean     = 0x01
	tagInteger     = 0x02
	tagOctetString = 0x04
	tagEnumerated  = 0x0a
	tagSequence    = 0x10
	tagSet         = 0x11
)

// packet is a BER element being built.
//
// Built as a tree and flattened once at the end, because BER puts a length before its contents: writing forwards would
// mean either two passes over everything or patching lengths afterwards, and patching is where an encoder quietly emits
// a length that disagrees with what follows it.
type packet struct {
	tag      byte
	value    []byte
	children []*packet
}

// newPacket starts an element with a raw value.
func newPacket(tag byte, value []byte) *packet {
	return &packet{tag: tag, value: value}
}

// newSequence starts a constructed element.
func newSequence(tag byte) *packet {
	return &packet{tag: tag | constructed}
}

// add appends a child and returns it.
func (p *packet) add(child *packet) *packet {
	p.children = append(p.children, child)
	return child
}

// addInteger appends an integer.
func (p *packet) addInteger(v int64) *packet {
	return p.add(newPacket(tagInteger, encodeInteger(v)))
}

// addEnumerated appends an enumerated value.
func (p *packet) addEnumerated(v int64) *packet {
	return p.add(newPacket(tagEnumerated, encodeInteger(v)))
}

// addString appends an octet string.
func (p *packet) addString(s string) *packet {
	return p.add(newPacket(tagOctetString, []byte(s)))
}

// addBoolean appends a boolean.
func (p *packet) addBoolean(v bool) *packet {
	b := byte(0x00)
	if v {
		// 0xff rather than 0x01. DER requires all bits set, and some servers are strict about it even over BER.
		b = 0xff
	}
	return p.add(newPacket(tagBoolean, []byte{b}))
}

// bytes flattens the element and everything under it.
func (p *packet) bytes() []byte {
	content := p.value
	if len(p.children) > 0 {
		content = nil
		for _, c := range p.children {
			content = append(content, c.bytes()...)
		}
	}

	out := []byte{p.tag}
	out = append(out, encodeLength(len(content))...)
	out = append(out, content...)
	return out
}

// encodeLength writes a BER length.
//
// Short form under 128, long form above. The definite form is always used: the indefinite form is legal BER but a server
// is not required to accept it, and it saves nothing here because the length is known before anything is written.
func encodeLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}

	var size []byte
	for v := n; v > 0; v >>= 8 {
		size = append([]byte{byte(v & 0xff)}, size...)
	}
	return append([]byte{byte(0x80 | len(size))}, size...)
}

// encodeInteger writes a BER integer.
//
// Two's complement, shortest form, with the sign bit handled: a positive value whose top byte has the high bit set needs
// a leading zero, or the far end reads it as negative. Message IDs are small so this rarely triggers, but a message ID
// of 128 would otherwise be read as -128 and the response would be matched to nothing.
func encodeInteger(v int64) []byte {
	if v == 0 {
		return []byte{0x00}
	}

	var out []byte
	if v > 0 {
		for n := v; n > 0; n >>= 8 {
			out = append([]byte{byte(n & 0xff)}, out...)
		}
		if out[0]&0x80 != 0 {
			out = append([]byte{0x00}, out...)
		}
		return out
	}

	// Negative values appear in LDAP only as error codes that we send back to nobody, but encoding them correctly
	// costs three lines and an encoder that silently mangles them is worse than one that cannot.
	n := v
	for {
		out = append([]byte{byte(n & 0xff)}, out...)
		n >>= 8
		if n == -1 && out[0]&0x80 != 0 {
			break
		}
		if n == 0 && out[0]&0x80 == 0 {
			break
		}
	}
	return out
}

// element is a decoded BER element.
type element struct {
	tag      byte
	value    []byte
	children []*element
}

// class returns the tag's class bits.
func (e *element) class() byte { return e.tag & 0xc0 }

// isConstructed reports whether the element has children.
func (e *element) isConstructed() bool { return e.tag&constructed != 0 }

// number returns the tag number without class or constructed bits.
func (e *element) number() byte { return e.tag & 0x1f }

// errTruncated means the buffer ended inside an element.
var errTruncated = errors.New("the LDAP message ends part way through an element")

// parseElement decodes one element and returns how many bytes it used.
//
// Depth is bounded. A hostile or broken server could otherwise send a few bytes that nest thousands deep and exhaust the
// stack before anything looks at the contents - and this runs before authentication, so it is reachable by anyone who
// can open a socket to whatever we were told is the directory.
func parseElement(b []byte, depth int) (*element, int, error) {
	const maxDepth = 32
	if depth > maxDepth {
		return nil, 0, fmt.Errorf("the LDAP message nests more than %d levels deep", maxDepth)
	}
	if len(b) < 2 {
		return nil, 0, errTruncated
	}

	tag := b[0]
	pos := 1

	// High tag numbers are not supported. LDAP does not use them, and accepting them would mean decoding a
	// multi-byte tag that no real server sends - untested code on an unauthenticated path.
	if tag&0x1f == 0x1f {
		return nil, 0, errors.New("multi-byte BER tags are not used by LDAP")
	}

	length := int(b[pos])
	pos++

	if length&0x80 != 0 {
		count := length & 0x7f
		if count == 0 {
			// Indefinite length: contents run to an end-of-contents marker. Refused rather than supported,
			// because it needs a scan for a terminator and no LDAP server sends it.
			return nil, 0, errors.New("indefinite BER lengths are not supported")
		}
		if count > 4 {
			return nil, 0, fmt.Errorf("an LDAP element claims a length needing %d bytes, which is not credible", count)
		}
		if pos+count > len(b) {
			return nil, 0, errTruncated
		}
		length = 0
		for i := 0; i < count; i++ {
			length = length<<8 | int(b[pos+i])
			if length > math.MaxInt32 {
				return nil, 0, errors.New("an LDAP element claims an implausible length")
			}
		}
		pos += count
	}

	if length < 0 || pos+length > len(b) {
		// The commonest real failure, and worth naming precisely: a length that runs past the end means either a
		// short read or a decoder that has lost its place, and those need different fixes.
		return nil, 0, errTruncated
	}

	e := &element{tag: tag, value: b[pos : pos+length]}
	pos += length

	if e.isConstructed() {
		rest := e.value
		for len(rest) > 0 {
			child, used, err := parseElement(rest, depth+1)
			if err != nil {
				return nil, 0, err
			}
			e.children = append(e.children, child)
			rest = rest[used:]
		}
	}

	return e, pos, nil
}

// integer reads an element's value as an integer.
func (e *element) integer() (int64, error) {
	if len(e.value) == 0 {
		return 0, errors.New("an LDAP integer has no bytes")
	}
	if len(e.value) > 8 {
		return 0, fmt.Errorf("an LDAP integer is %d bytes, which does not fit", len(e.value))
	}

	v := int64(0)
	if e.value[0]&0x80 != 0 {
		v = -1
	}
	for _, b := range e.value {
		v = v<<8 | int64(b)
	}
	return v, nil
}

// text reads an element's value as a string.
func (e *element) text() string { return string(e.value) }

// child returns the nth child, or an error naming what was expected.
func (e *element) child(n int) (*element, error) {
	if n >= len(e.children) {
		return nil, fmt.Errorf("an LDAP message has %d parts where part %d was expected", len(e.children), n+1)
	}
	return e.children[n], nil
}
