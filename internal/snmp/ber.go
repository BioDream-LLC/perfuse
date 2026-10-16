// Package snmp is a small read-only SNMPv2c agent: GET, GETNEXT and GETBULK over UDP against a table
// of values the caller fills in, and SNMPv2 traps. Messages are BER (X.690) encoded here; nothing in it
// is cryptography (v2c's community string is a plain-text password, which is why the agent only
// answers the networks it is told to).
package snmp

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// BER tags.
const (
	tagInteger   = 0x02
	tagOctets    = 0x04
	tagNull      = 0x05
	tagOID       = 0x06
	tagSequence  = 0x30
	tagIPAddress = 0x40
	tagCounter32 = 0x41
	tagGauge32   = 0x42
	tagTimeTicks = 0x43
	tagCounter64 = 0x46

	noSuchObject   = 0x80
	noSuchInstance = 0x81
	endOfMibView   = 0x82

	pduGet      = 0xa0
	pduGetNext  = 0xa1
	pduResponse = 0xa2
	pduSet      = 0xa3
	pduGetBulk  = 0xa5
	pduTrapV2   = 0xa7
)

// OID is an object identifier.
type OID []uint32

// ParseOID reads 1.3.6.1... (a leading dot is allowed).
func ParseOID(s string) (OID, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	if s == "" {
		return nil, errors.New("snmp: empty OID")
	}
	var o OID
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("snmp: %q is not an OID", s)
		}
		o = append(o, uint32(n))
	}
	if len(o) < 2 || o[0] > 2 {
		return nil, fmt.Errorf("snmp: %q is not an OID", s)
	}
	return o, nil
}

// MustOID is ParseOID for constants.
func MustOID(s string) OID {
	o, err := ParseOID(s)
	if err != nil {
		panic(err)
	}
	return o
}

func (o OID) String() string {
	parts := make([]string, len(o))
	for i, n := range o {
		parts[i] = strconv.FormatUint(uint64(n), 10)
	}
	return strings.Join(parts, ".")
}

// Append makes a child OID.
func (o OID) Append(n ...uint32) OID { return append(slices.Clone(o), n...) }

// Compare orders OIDs as SNMP walks them.
func Compare(a, b OID) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

// Value is one variable's value.
type Value struct {
	Tag   byte
	Int   int64
	Bytes []byte
	OID   OID
}

// Integer, String, Gauge, Counter, Counter64, Ticks and ObjectID make values.
func Integer(n int64) Value    { return Value{Tag: tagInteger, Int: n} }
func String(s string) Value    { return Value{Tag: tagOctets, Bytes: []byte(s)} }
func Gauge(n uint32) Value     { return Value{Tag: tagGauge32, Int: int64(n)} }
func Counter(n uint32) Value   { return Value{Tag: tagCounter32, Int: int64(n)} }
func Counter64(n uint64) Value { return Value{Tag: tagCounter64, Int: int64(n)} }
func Ticks(n uint32) Value     { return Value{Tag: tagTimeTicks, Int: int64(n)} }
func ObjectID(o OID) Value     { return Value{Tag: tagOID, OID: o} }

// VarBind is a name and its value.
type VarBind struct {
	Name  OID
	Value Value
}

// --- BER encoding ---

func encLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte(n)}, b...)
		n >>= 8
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

func tlv(tag byte, body []byte) []byte {
	return append(append([]byte{tag}, encLen(len(body))...), body...)
}

func encInt(n int64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	// Drop leading bytes that only repeat the sign.
	for len(b) > 1 && (b[0] == 0 && b[1]&0x80 == 0 || b[0] == 0xff && b[1]&0x80 != 0) {
		b = b[1:]
	}
	return b
}

func encUint(n uint64) []byte {
	b := new(big.Int).SetUint64(n).Bytes()
	if len(b) == 0 {
		return []byte{0}
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return b
}

func encOID(o OID) []byte {
	if len(o) < 2 {
		return []byte{0}
	}
	b := []byte{byte(o[0]*40 + o[1])}
	for _, n := range o[2:] {
		var tmp []byte
		tmp = append(tmp, byte(n&0x7f))
		for n >>= 7; n > 0; n >>= 7 {
			tmp = append([]byte{byte(n&0x7f | 0x80)}, tmp...)
		}
		b = append(b, tmp...)
	}
	return b
}

func encValue(v Value) []byte {
	switch v.Tag {
	case tagInteger:
		return tlv(tagInteger, encInt(v.Int))
	case tagOctets, tagIPAddress:
		return tlv(v.Tag, v.Bytes)
	case tagOID:
		return tlv(tagOID, encOID(v.OID))
	case tagCounter32, tagGauge32, tagTimeTicks, tagCounter64:
		return tlv(v.Tag, encUint(uint64(v.Int)))
	case noSuchObject, noSuchInstance, endOfMibView:
		return []byte{v.Tag, 0}
	}
	return []byte{tagNull, 0}
}

func encVarBinds(vbs []VarBind) []byte {
	var body []byte
	for _, vb := range vbs {
		body = append(body, tlv(tagSequence, append(tlv(tagOID, encOID(vb.Name)), encValue(vb.Value)...))...)
	}
	return tlv(tagSequence, body)
}

// message is a v2c message.
type message struct {
	version   int64
	community []byte
	pdu       byte
	requestID int64
	// errStatus/errIndex, or for GETBULK non-repeaters/max-repetitions
	a, b int64
	vbs  []VarBind
}

func (m message) encode() []byte {
	pdu := tlv(tagInteger, encInt(m.requestID))
	pdu = append(pdu, tlv(tagInteger, encInt(m.a))...)
	pdu = append(pdu, tlv(tagInteger, encInt(m.b))...)
	pdu = append(pdu, encVarBinds(m.vbs)...)
	body := tlv(tagInteger, encInt(m.version))
	body = append(body, tlv(tagOctets, m.community)...)
	body = append(body, tlv(m.pdu, pdu)...)
	return tlv(tagSequence, body)
}

// --- BER decoding ---

type reader struct{ b []byte }

func (r *reader) next() (tag byte, body []byte, err error) {
	if len(r.b) < 2 {
		return 0, nil, errors.New("snmp: short message")
	}
	tag = r.b[0]
	n := int(r.b[1])
	off := 2
	if n&0x80 != 0 {
		k := n & 0x7f
		if k == 0 || k > 3 || len(r.b) < 2+k {
			return 0, nil, errors.New("snmp: bad length")
		}
		n = 0
		for _, c := range r.b[2 : 2+k] {
			n = n<<8 | int(c)
		}
		off += k
	}
	if len(r.b) < off+n {
		return 0, nil, errors.New("snmp: truncated")
	}
	body = r.b[off : off+n]
	r.b = r.b[off+n:]
	return tag, body, nil
}

func (r *reader) expect(want byte) ([]byte, error) {
	tag, body, err := r.next()
	if err != nil {
		return nil, err
	}
	if tag != want {
		return nil, fmt.Errorf("snmp: tag %#x, want %#x", tag, want)
	}
	return body, nil
}

func decInt(b []byte) (int64, error) {
	if len(b) == 0 || len(b) > 8 {
		return 0, errors.New("snmp: bad integer")
	}
	n := int64(int8(b[0]))
	for _, c := range b[1:] {
		n = n<<8 | int64(c)
	}
	return n, nil
}

func decOID(b []byte) (OID, error) {
	if len(b) == 0 || len(b) > 256 {
		return nil, errors.New("snmp: bad OID")
	}
	o := OID{uint32(b[0]) / 40, uint32(b[0]) % 40}
	if b[0] >= 80 {
		o = OID{2, uint32(b[0]) - 80}
	}
	var n uint32
	for i, c := range b[1:] {
		if n > 1<<25 {
			return nil, errors.New("snmp: OID part too large")
		}
		n = n<<7 | uint32(c&0x7f)
		if c&0x80 == 0 {
			o = append(o, n)
			n = 0
		} else if i == len(b)-2 {
			return nil, errors.New("snmp: OID cut short")
		}
	}
	return o, nil
}

func decode(pkt []byte) (message, error) {
	var m message
	top := &reader{pkt}
	seq, err := top.expect(tagSequence)
	if err != nil {
		return m, err
	}
	r := &reader{seq}
	vb, err := r.expect(tagInteger)
	if err != nil {
		return m, err
	}
	if m.version, err = decInt(vb); err != nil {
		return m, err
	}
	if m.community, err = r.expect(tagOctets); err != nil {
		return m, err
	}
	tag, pdu, err := r.next()
	if err != nil {
		return m, err
	}
	m.pdu = tag
	pr := &reader{pdu}
	for _, dst := range []*int64{&m.requestID, &m.a, &m.b} {
		b, err := pr.expect(tagInteger)
		if err != nil {
			return m, err
		}
		if *dst, err = decInt(b); err != nil {
			return m, err
		}
	}
	list, err := pr.expect(tagSequence)
	if err != nil {
		return m, err
	}
	lr := &reader{list}
	for len(lr.b) > 0 && len(m.vbs) < 128 {
		one, err := lr.expect(tagSequence)
		if err != nil {
			return m, err
		}
		or := &reader{one}
		ob, err := or.expect(tagOID)
		if err != nil {
			return m, err
		}
		o, err := decOID(ob)
		if err != nil {
			return m, err
		}
		v := Value{Tag: tagNull}
		if len(or.b) > 0 {
			vt, vbody, err := or.next()
			if err != nil {
				return m, err
			}
			v.Tag = vt
			switch vt {
			case tagInteger:
				v.Int, _ = decInt(vbody)
			case tagCounter32, tagGauge32, tagTimeTicks, tagCounter64:
				for _, c := range vbody {
					v.Int = v.Int<<8 | int64(c)
				}
			case tagOID:
				v.OID, _ = decOID(vbody)
			default:
				v.Bytes = vbody
			}
		}
		m.vbs = append(m.vbs, VarBind{Name: o, Value: v})
	}
	return m, nil
}
