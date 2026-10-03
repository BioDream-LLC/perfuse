package amqp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// The AMQP 1.0 type system (OASIS AMQP 1.0, part 1): what every frame and message section is written in.
//
// Decoded into plain Go values: nil, bool, the sized unsigned and signed integers, time.Time, []byte, string, Symbol, []any for lists and
// arrays, map[any]any for maps, and Described for a described type. Encoded from the same, using the smallest form for each value - which
// is what brokers send and what the specification recommends.

// Symbol is an AMQP symbol: an ASCII name, distinct from a string on the wire.
type Symbol string

// Described is a value with a descriptor, usually a ulong code naming a performative or a message section.
type Described struct {
	Descriptor any
	Value      any
}

// ULong is a ulong, kept distinct from other integers so descriptors encode as ulongs.
type ULong uint64

// UInt is a uint.
type UInt uint32

// UShort is a ushort.
type UShort uint16

// UByte is a ubyte.
type UByte uint8

var errShort = errors.New("amqp: the encoded value is truncated")

type decoder struct {
	b   []byte
	pos int
}

func (d *decoder) need(n int) error {
	if d.pos+n > len(d.b) {
		return errShort
	}
	return nil
}

func (d *decoder) u8() (byte, error) {
	if err := d.need(1); err != nil {
		return 0, err
	}
	d.pos++
	return d.b[d.pos-1], nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 {
		return nil, errShort
	}
	if err := d.need(n); err != nil {
		return nil, err
	}
	d.pos += n
	return d.b[d.pos-n : d.pos], nil
}

func (d *decoder) u32() (uint32, error) {
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// Decode reads one value.
func Decode(b []byte) (any, int, error) {
	d := &decoder{b: b}
	v, err := d.value()
	return v, d.pos, err
}

func (d *decoder) value() (any, error) {
	code, err := d.u8()
	if err != nil {
		return nil, err
	}
	if code == 0x00 {
		desc, err := d.value()
		if err != nil {
			return nil, err
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		return Described{Descriptor: desc, Value: v}, nil
	}
	return d.typed(code)
}

func (d *decoder) typed(code byte) (any, error) {
	fixed := func(n int) ([]byte, error) { return d.take(n) }
	switch code {
	case 0x40:
		return nil, nil
	case 0x41:
		return true, nil
	case 0x42:
		return false, nil
	case 0x56:
		b, err := d.u8()
		return b != 0, err
	case 0x50:
		b, err := d.u8()
		return UByte(b), err
	case 0x51:
		b, err := d.u8()
		return int8(b), err
	case 0x60:
		b, err := fixed(2)
		if err != nil {
			return nil, err
		}
		return UShort(binary.BigEndian.Uint16(b)), nil
	case 0x61:
		b, err := fixed(2)
		if err != nil {
			return nil, err
		}
		return int16(binary.BigEndian.Uint16(b)), nil
	case 0x43:
		return UInt(0), nil
	case 0x52:
		b, err := d.u8()
		return UInt(b), err
	case 0x70:
		v, err := d.u32()
		return UInt(v), err
	case 0x44:
		return ULong(0), nil
	case 0x53:
		b, err := d.u8()
		return ULong(b), err
	case 0x80:
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return ULong(binary.BigEndian.Uint64(b)), nil
	case 0x54:
		b, err := d.u8()
		return int32(int8(b)), err
	case 0x71:
		v, err := d.u32()
		return int32(v), err
	case 0x55:
		b, err := d.u8()
		return int64(int8(b)), err
	case 0x81:
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return int64(binary.BigEndian.Uint64(b)), nil
	case 0x72:
		v, err := d.u32()
		return math.Float32frombits(v), err
	case 0x82:
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case 0x73:
		v, err := d.u32()
		return rune(v), err
	case 0x83:
		b, err := fixed(8)
		if err != nil {
			return nil, err
		}
		return time.UnixMilli(int64(binary.BigEndian.Uint64(b))).UTC(), nil
	case 0x98:
		b, err := fixed(16)
		return append([]byte(nil), b...), err
	case 0x74, 0x84, 0x94:
		// decimal32, decimal64, decimal128: carried as their bytes, which nothing here interprets.
		_, err := fixed(map[byte]int{0x74: 4, 0x84: 8, 0x94: 16}[code])
		return nil, err
	case 0xa0, 0xa1, 0xa3, 0xb0, 0xb1, 0xb3:
		var n int
		if code&0xf0 == 0xa0 {
			b, err := d.u8()
			if err != nil {
				return nil, err
			}
			n = int(b)
		} else {
			v, err := d.u32()
			if err != nil {
				return nil, err
			}
			n = int(v)
		}
		b, err := d.take(n)
		if err != nil {
			return nil, err
		}
		switch code & 0x0f {
		case 0x00:
			return append([]byte(nil), b...), nil
		case 0x01:
			return string(b), nil
		default:
			return Symbol(b), nil
		}
	case 0x45:
		return []any{}, nil
	case 0xc0, 0xd0, 0xc1, 0xd1:
		var count int
		if code == 0xc0 || code == 0xc1 {
			if _, err := d.u8(); err != nil {
				return nil, err
			}
			c, err := d.u8()
			if err != nil {
				return nil, err
			}
			count = int(c)
		} else {
			if _, err := d.u32(); err != nil {
				return nil, err
			}
			c, err := d.u32()
			if err != nil {
				return nil, err
			}
			count = int(c)
		}
		if count > len(d.b)-d.pos {
			return nil, errShort
		}
		items := make([]any, 0, count)
		for i := 0; i < count; i++ {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		if code == 0xc0 || code == 0xd0 {
			return items, nil
		}
		m := make(map[any]any, count/2)
		for i := 0; i+1 < len(items); i += 2 {
			m[mapKey(items[i])] = items[i+1]
		}
		return m, nil
	case 0xe0, 0xf0:
		var count int
		if code == 0xe0 {
			if _, err := d.u8(); err != nil {
				return nil, err
			}
			c, err := d.u8()
			if err != nil {
				return nil, err
			}
			count = int(c)
		} else {
			if _, err := d.u32(); err != nil {
				return nil, err
			}
			c, err := d.u32()
			if err != nil {
				return nil, err
			}
			count = int(c)
		}
		elem, err := d.u8()
		if err != nil {
			return nil, err
		}
		var desc any
		if elem == 0x00 {
			if desc, err = d.value(); err != nil {
				return nil, err
			}
			if elem, err = d.u8(); err != nil {
				return nil, err
			}
		}
		if count > len(d.b)-d.pos+1 {
			return nil, errShort
		}
		items := make([]any, 0, count)
		for i := 0; i < count; i++ {
			v, err := d.typed(elem)
			if err != nil {
				return nil, err
			}
			if desc != nil {
				v = Described{Descriptor: desc, Value: v}
			}
			items = append(items, v)
		}
		return items, nil
	}
	return nil, fmt.Errorf("amqp: unknown type code 0x%02x", code)
}

// mapKey makes a decoded value usable as a Go map key: binary keys become strings.
func mapKey(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// Encode appends v.
func Encode(dst []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(dst, 0x40)
	case bool:
		if x {
			return append(dst, 0x41)
		}
		return append(dst, 0x42)
	case UByte:
		return append(dst, 0x50, byte(x))
	case UShort:
		return binary.BigEndian.AppendUint16(append(dst, 0x60), uint16(x))
	case UInt:
		switch {
		case x == 0:
			return append(dst, 0x43)
		case x < 256:
			return append(dst, 0x52, byte(x))
		}
		return binary.BigEndian.AppendUint32(append(dst, 0x70), uint32(x))
	case ULong:
		switch {
		case x == 0:
			return append(dst, 0x44)
		case x < 256:
			return append(dst, 0x53, byte(x))
		}
		return binary.BigEndian.AppendUint64(append(dst, 0x80), uint64(x))
	case int:
		return Encode(dst, int64(x))
	case int32:
		if x >= -128 && x <= 127 {
			return append(dst, 0x54, byte(int8(x)))
		}
		return binary.BigEndian.AppendUint32(append(dst, 0x71), uint32(x))
	case int64:
		if x >= -128 && x <= 127 {
			return append(dst, 0x55, byte(int8(x)))
		}
		return binary.BigEndian.AppendUint64(append(dst, 0x81), uint64(x))
	case time.Time:
		return binary.BigEndian.AppendUint64(append(dst, 0x83), uint64(x.UnixMilli()))
	case []byte:
		if len(x) < 256 {
			return append(append(dst, 0xa0, byte(len(x))), x...)
		}
		return append(binary.BigEndian.AppendUint32(append(dst, 0xb0), uint32(len(x))), x...)
	case string:
		if len(x) < 256 {
			return append(append(dst, 0xa1, byte(len(x))), x...)
		}
		return append(binary.BigEndian.AppendUint32(append(dst, 0xb1), uint32(len(x))), x...)
	case Symbol:
		if len(x) < 256 {
			return append(append(dst, 0xa3, byte(len(x))), x...)
		}
		return append(binary.BigEndian.AppendUint32(append(dst, 0xb3), uint32(len(x))), x...)
	case []Symbol:
		// An array of symbols, which is how offered and desired capabilities are written.
		var body []byte
		for _, s := range x {
			body = binary.BigEndian.AppendUint32(body, uint32(len(s)))
			body = append(body, s...)
		}
		dst = append(dst, 0xf0)
		dst = binary.BigEndian.AppendUint32(dst, uint32(4+1+len(body)))
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(x)))
		return append(append(dst, 0xb3), body...)
	case []any:
		if len(x) == 0 {
			return append(dst, 0x45)
		}
		var body []byte
		for _, e := range x {
			body = Encode(body, e)
		}
		dst = append(dst, 0xd0)
		dst = binary.BigEndian.AppendUint32(dst, uint32(4+len(body)))
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(x)))
		return append(dst, body...)
	case map[any]any:
		var body []byte
		for k, val := range x {
			body = Encode(body, k)
			body = Encode(body, val)
		}
		dst = append(dst, 0xd1)
		dst = binary.BigEndian.AppendUint32(dst, uint32(4+len(body)))
		dst = binary.BigEndian.AppendUint32(dst, uint32(2*len(x)))
		return append(dst, body...)
	case Described:
		return Encode(Encode(append(dst, 0x00), x.Descriptor), x.Value)
	}
	panic(fmt.Sprintf("amqp: cannot encode %T", v))
}

// list builds a performative's field list, dropping trailing nulls as the specification allows.
func list(fields ...any) []any {
	for len(fields) > 0 && fields[len(fields)-1] == nil {
		fields = fields[:len(fields)-1]
	}
	return fields
}

func described(code uint64, fields ...any) Described {
	return Described{Descriptor: ULong(code), Value: list(fields...)}
}

// descriptorCode reads a described value's ulong code, or 0.
func descriptorCode(v any) (uint64, []any) {
	d, ok := v.(Described)
	if !ok {
		return 0, nil
	}
	code, _ := d.Descriptor.(ULong)
	fields, _ := d.Value.([]any)
	return uint64(code), fields
}

func field(fields []any, i int) any {
	if i < len(fields) {
		return fields[i]
	}
	return nil
}

func asUint(v any) uint64 {
	switch x := v.(type) {
	case UByte:
		return uint64(x)
	case UShort:
		return uint64(x)
	case UInt:
		return uint64(x)
	case ULong:
		return uint64(x)
	}
	return 0
}
