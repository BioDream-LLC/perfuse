package sof

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
)

// Formats are the output formats a view's rows are written in.
var Formats = map[string]string{
	"csv":     "text/csv",
	"ndjson":  "application/x-ndjson",
	"json":    "application/json",
	"parquet": "application/vnd.apache.parquet",
}

// Writer writes rows in one format. Close finishes the output (a JSON array's ], a Parquet footer).
type Writer interface {
	Write(Row) error
	Close() error
}

// NewWriter makes a writer for format (csv, ndjson, json, parquet). header applies to csv.
func NewWriter(w io.Writer, format string, cols []*Column, header bool) (Writer, error) {
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		if header {
			names := make([]string, len(cols))
			for i, c := range cols {
				names[i] = c.Name
			}
			if err := cw.Write(names); err != nil {
				return nil, err
			}
		}
		return &csvWriter{w: cw}, nil
	case "ndjson", "json":
		bw := bufio.NewWriter(w)
		return &jsonWriter{w: bw, cols: cols, array: format == "json"}, nil
	case "parquet":
		return &parquetWriter{w: w, cols: cols}, nil
	}
	return nil, fmt.Errorf("the output format %q is not one of csv, ndjson, json and parquet", format)
}

type csvWriter struct{ w *csv.Writer }

// cell is a value as CSV text: empty for null, JSON for a collection, the number or text as written otherwise.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		return x.String()
	case int64:
		return strconv.FormatInt(x, 10)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (c *csvWriter) Write(r Row) error {
	rec := make([]string, len(r))
	for i, v := range r {
		rec[i] = cell(v)
	}
	return c.w.Write(rec)
}

func (c *csvWriter) Close() error {
	c.w.Flush()
	return c.w.Error()
}

type jsonWriter struct {
	w     *bufio.Writer
	cols  []*Column
	array bool
	n     int
}

func (j *jsonWriter) Write(r Row) error {
	// Written by hand to keep the columns in order.
	var b bytes.Buffer
	b.WriteByte('{')
	for i, c := range j.cols {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c.Name)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(r[i])
		if err != nil {
			return err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	switch {
	case j.array && j.n == 0:
		j.w.WriteString("[")
	case j.array:
		j.w.WriteString(",\n")
	}
	j.n++
	j.w.Write(b.Bytes())
	if !j.array {
		j.w.WriteByte('\n')
	}
	return nil
}

func (j *jsonWriter) Close() error {
	if j.array {
		if j.n == 0 {
			j.w.WriteString("[")
		}
		j.w.WriteString("]\n")
	}
	return j.w.Flush()
}

// Parquet: one row group, one PLAIN-encoded, uncompressed data page per column, every column optional. Booleans are
// BOOLEAN, FHIR integers INT64, decimals DOUBLE, and everything else (strings, codes, dates, collections as JSON) a UTF-8
// BYTE_ARRAY. Rows are held until Close, which is fine for the row counts a synchronous run returns; bulk export writes one
// file per view.

type parquetWriter struct {
	w    io.Writer
	cols []*Column
	rows []Row
}

func (p *parquetWriter) Write(r Row) error {
	p.rows = append(p.rows, r)
	return nil
}

const (
	ptBoolean   = 0
	ptInt64     = 2
	ptDouble    = 5
	ptByteArray = 6
)

func parquetType(c *Column) int {
	if c.Collection {
		return ptByteArray
	}
	switch c.Type {
	case "boolean":
		return ptBoolean
	case "integer", "positiveInt", "unsignedInt", "integer64":
		return ptInt64
	case "decimal":
		return ptDouble
	}
	return ptByteArray
}

func (p *parquetWriter) Close() error {
	var file bytes.Buffer
	file.WriteString("PAR1")
	type chunk struct {
		typ          int
		offset, size int64
		values       int
	}
	chunks := make([]chunk, len(p.cols))
	for ci, col := range p.cols {
		typ := parquetType(col)
		var defs []bool
		var data bytes.Buffer
		var bits []bool
		for _, r := range p.rows {
			v := r[ci]
			if v == nil {
				defs = append(defs, false)
				continue
			}
			ok := true
			switch typ {
			case ptBoolean:
				b, isB := v.(bool)
				ok = isB
				bits = append(bits, b)
			case ptInt64:
				i, err := toInt64(v)
				if err != nil {
					ok = false
					break
				}
				_ = binary.Write(&data, binary.LittleEndian, i)
			case ptDouble:
				f, err := toFloat(v)
				if err != nil {
					ok = false
					break
				}
				_ = binary.Write(&data, binary.LittleEndian, math.Float64bits(f))
			default:
				s := cell(v)
				_ = binary.Write(&data, binary.LittleEndian, uint32(len(s)))
				data.WriteString(s)
			}
			if !ok {
				return fmt.Errorf("column %s: %v is not a %s", col.Name, v, col.Type)
			}
			defs = append(defs, true)
		}
		if typ == ptBoolean {
			data.Write(packBits(bits))
		}
		levels := rleBitPacked(defs)
		var page bytes.Buffer
		_ = binary.Write(&page, binary.LittleEndian, uint32(len(levels)))
		page.Write(levels)
		page.Write(data.Bytes())

		var hdr thrift
		hdr.i32(1, 0) // DATA_PAGE
		hdr.i32(2, int32(page.Len()))
		hdr.i32(3, int32(page.Len()))
		hdr.beginStruct(5)
		hdr.i32(1, int32(len(p.rows)))
		hdr.i32(2, 0) // PLAIN
		hdr.i32(3, 3) // RLE
		hdr.i32(4, 3) // RLE
		hdr.endStruct()
		hdr.stop()

		start := int64(file.Len())
		file.Write(hdr.b.Bytes())
		file.Write(page.Bytes())
		chunks[ci] = chunk{typ: typ, offset: start, size: int64(file.Len()) - start, values: len(p.rows)}
	}

	var m thrift
	m.i32(1, 1) // version
	m.beginList(2, tStruct, len(p.cols)+1)
	m.listStructBegin()
	m.str(4, "schema")
	m.i32(5, int32(len(p.cols)))
	m.listStructEnd()
	for ci, col := range p.cols {
		m.listStructBegin()
		m.i32(1, int32(chunks[ci].typ))
		m.i32(3, 1) // OPTIONAL
		m.str(4, col.Name)
		if chunks[ci].typ == ptByteArray {
			m.i32(6, 0) // UTF8
			m.beginStruct(10)
			m.beginStruct(1) // STRING
			m.endStruct()
			m.endStruct()
		}
		m.listStructEnd()
	}
	m.i64(3, int64(len(p.rows)))
	m.beginList(4, tStruct, 1)
	m.listStructBegin()
	var total int64
	for _, c := range chunks {
		total += c.size
	}
	m.beginList(1, tStruct, len(p.cols))
	for ci, col := range p.cols {
		c := chunks[ci]
		m.listStructBegin()
		m.i64(2, c.offset)
		m.beginStruct(3)
		m.i32(1, int32(c.typ))
		m.beginList(2, tI32, 2)
		m.listI32(0)
		m.listI32(3)
		m.beginList(3, tBinary, 1)
		m.listStr(col.Name)
		m.i32(4, 0) // UNCOMPRESSED
		m.i64(5, int64(c.values))
		m.i64(6, c.size)
		m.i64(7, c.size)
		m.i64(9, c.offset)
		m.endStruct()
		m.listStructEnd()
	}
	m.i64(2, total)
	m.i64(3, int64(len(p.rows)))
	m.listStructEnd()
	m.str(6, "Perfuse (SQL on FHIR)")
	m.stop()

	file.Write(m.b.Bytes())
	_ = binary.Write(&file, binary.LittleEndian, uint32(m.b.Len()))
	file.WriteString("PAR1")
	_, err := p.w.Write(file.Bytes())
	return err
}

func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case json.Number:
		return x.Int64()
	case float64:
		return int64(x), nil
	}
	return 0, fmt.Errorf("not an integer")
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case int64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	case float64:
		return x, nil
	}
	return 0, fmt.Errorf("not a number")
}

func packBits(bits []bool) []byte {
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// rleBitPacked encodes definition levels (bit width 1) as bit-packed runs of the RLE/bit-packing hybrid.
func rleBitPacked(levels []bool) []byte {
	groups := (len(levels) + 7) / 8
	var b bytes.Buffer
	b.Write(uvarint(uint64(groups<<1 | 1)))
	b.Write(packBits(levels)) // one byte per group of eight
	return b.Bytes()
}

func uvarint(v uint64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, v)
	return buf[:n]
}

// thrift is just enough of Thrift's compact protocol for Parquet metadata.
type thrift struct {
	b     bytes.Buffer
	last  []int16
	field int16
}

const (
	tI32    = 5
	tI64    = 6
	tBinary = 8
	tList   = 9
	tStruct = 12
)

func (t *thrift) header(id int16, typ byte) {
	d := id - t.field
	if d > 0 && d <= 15 {
		t.b.WriteByte(byte(d)<<4 | typ)
	} else {
		t.b.WriteByte(typ)
		t.b.Write(zigzag(int64(id)))
	}
	t.field = id
}

func zigzag(v int64) []byte { return uvarint(uint64((v << 1) ^ (v >> 63))) }

func (t *thrift) i32(id int16, v int32) {
	t.header(id, tI32)
	t.b.Write(zigzag(int64(v)))
}

func (t *thrift) i64(id int16, v int64) {
	t.header(id, tI64)
	t.b.Write(zigzag(v))
}

func (t *thrift) str(id int16, s string) {
	t.header(id, tBinary)
	t.b.Write(uvarint(uint64(len(s))))
	t.b.WriteString(s)
}

func (t *thrift) beginStruct(id int16) {
	t.header(id, tStruct)
	t.last = append(t.last, t.field)
	t.field = 0
}

func (t *thrift) endStruct() {
	t.b.WriteByte(0)
	t.field = t.last[len(t.last)-1]
	t.last = t.last[:len(t.last)-1]
}

func (t *thrift) beginList(id int16, elem byte, n int) {
	t.header(id, tList)
	if n < 15 {
		t.b.WriteByte(byte(n)<<4 | elem)
	} else {
		t.b.WriteByte(0xF0 | elem)
		t.b.Write(uvarint(uint64(n)))
	}
}

func (t *thrift) listStructBegin() {
	t.last = append(t.last, t.field)
	t.field = 0
}

func (t *thrift) listStructEnd() { t.endStruct() }

func (t *thrift) listI32(v int32) { t.b.Write(zigzag(int64(v))) }

func (t *thrift) listStr(s string) {
	t.b.Write(uvarint(uint64(len(s))))
	t.b.WriteString(s)
}

func (t *thrift) stop() { t.b.WriteByte(0) }
