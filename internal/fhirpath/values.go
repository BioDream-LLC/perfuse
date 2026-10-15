// Package fhirpath evaluates FHIRPath (HL7 FHIRPath N1, 2.0.0) over FHIR resources held as decoded JSON.
//
// It has no FHIR model: an element's type is known when FHIR JSON says it (a choice element's suffix, such as valueQuantity,
// a resource's resourceType) or inferred from the JSON value otherwise. That covers what SQL on FHIR views and CQL-free
// quality measures need; navigation that depends on the model (a primitive's own type when it is not a choice) is answered
// from the value's shape.
package fhirpath

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Decimal is an exact decimal number: unscaled / 10^scale. Precision matters to FHIRPath (1.0 and 1.00 differ in
// lowBoundary), so the scale is kept as written.
type Decimal struct {
	u     *big.Int
	scale int
}

// ParseDecimal reads a decimal literal (optionally signed, optionally with an exponent).
func ParseDecimal(s string) (Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Decimal{}, false
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return Decimal{}, false
		}
		exp = e
		s = s[:i]
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return Decimal{}, false
	}
	digits := intPart + frac
	for _, c := range digits {
		if c < '0' || c > '9' {
			return Decimal{}, false
		}
	}
	u, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		u = new(big.Int)
	}
	if neg {
		u.Neg(u)
	}
	d := Decimal{u: u, scale: len(frac) - exp}
	if d.scale < 0 {
		u.Mul(u, pow10(-d.scale))
		d.scale = 0
	}
	return d, true
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// DecimalFromInt is an integer as a decimal.
func DecimalFromInt(i int64) Decimal { return Decimal{u: big.NewInt(i), scale: 0} }

func (d Decimal) rescale(s int) *big.Int {
	if s == d.scale {
		return new(big.Int).Set(d.u)
	}
	return new(big.Int).Mul(d.u, pow10(s-d.scale))
}

func align(a, b Decimal) (*big.Int, *big.Int, int) {
	s := max(a.scale, b.scale)
	return a.rescale(s), b.rescale(s), s
}

// Cmp compares two decimals by value.
func (d Decimal) Cmp(o Decimal) int {
	a, b, _ := align(d, o)
	return a.Cmp(b)
}

// Add, Sub, Mul and Div are exact except Div, which keeps 8 places beyond the operands (trailing zeros removed).
func (d Decimal) Add(o Decimal) Decimal {
	a, b, s := align(d, o)
	return Decimal{u: a.Add(a, b), scale: s}
}

func (d Decimal) Sub(o Decimal) Decimal {
	a, b, s := align(d, o)
	return Decimal{u: a.Sub(a, b), scale: s}
}

func (d Decimal) Mul(o Decimal) Decimal {
	return Decimal{u: new(big.Int).Mul(d.u, o.u), scale: d.scale + o.scale}
}

// Div is d / o, or false when o is zero.
func (d Decimal) Div(o Decimal) (Decimal, bool) {
	if o.u.Sign() == 0 {
		return Decimal{}, false
	}
	places := max(d.scale, o.scale) + 8
	num := new(big.Int).Mul(d.u, pow10(places+o.scale))
	den := new(big.Int).Mul(o.u, pow10(d.scale))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	// Round half away from zero on the last place.
	if new(big.Int).Abs(new(big.Int).Mul(r, big.NewInt(2))).Cmp(new(big.Int).Abs(den)) >= 0 {
		if (num.Sign() < 0) != (den.Sign() < 0) {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return Decimal{u: q, scale: places}.trim(), true
}

// trim removes trailing zeros from the fraction.
func (d Decimal) trim() Decimal {
	u := new(big.Int).Set(d.u)
	s := d.scale
	ten := big.NewInt(10)
	m := new(big.Int)
	for s > 0 {
		q, r := new(big.Int).QuoRem(u, ten, m)
		if r.Sign() != 0 {
			break
		}
		u, s = q, s-1
	}
	return Decimal{u: u, scale: s}
}

// Neg is -d.
func (d Decimal) Neg() Decimal { return Decimal{u: new(big.Int).Neg(d.u), scale: d.scale} }

// IsInt says whether d has no fraction.
func (d Decimal) IsInt() (int64, bool) {
	t := d.trim()
	if t.scale != 0 || !t.u.IsInt64() {
		return 0, false
	}
	return t.u.Int64(), true
}

// Float is d as a float64.
func (d Decimal) Float() float64 {
	f, _ := new(big.Rat).SetFrac(d.u, pow10(d.scale)).Float64()
	return f
}

// Scale is the number of digits after the point as written.
func (d Decimal) Scale() int { return d.scale }

func (d Decimal) String() string {
	s := new(big.Int).Abs(d.u).String()
	neg := d.u.Sign() < 0
	if d.scale > 0 {
		if len(s) <= d.scale {
			s = strings.Repeat("0", d.scale-len(s)+1) + s
		}
		s = s[:len(s)-d.scale] + "." + s[len(s)-d.scale:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// MarshalJSON writes the number as written.
func (d Decimal) MarshalJSON() ([]byte, error) { return []byte(d.String()), nil }

// Precision of a date, a dateTime or a time: how much of it was written.
type Precision int

// The precisions, from coarsest.
const (
	PYear Precision = iota + 1
	PMonth
	PDay
	PHour
	PMinute
	PSecond
	PMillisecond
)

// DateTime is a FHIRPath Date, DateTime or Time with the precision it was written to. A Time uses only the clock fields.
type DateTime struct {
	Kind      string // "Date", "DateTime" or "Time"
	Year      int
	Month     int
	Day       int
	Hour      int
	Minute    int
	Second    int
	Milli     int
	Precision Precision
	// HasZone and ZoneMinutes are the offset; a DateTime without one is compared as if local (FHIRPath leaves it to the
	// implementation; Perfuse compares unzoned values as UTC).
	HasZone     bool
	ZoneMinutes int
}

// ParseDateTime reads a FHIR date, dateTime or instant ("2010-10-10", "2015-02-07T13:28:17.239+02:00"), or a FHIRPath
// literal body (without @). kind "" chooses Date or DateTime by whether there is a T.
func ParseDateTime(s, kind string) (DateTime, bool) {
	var d DateTime
	date, clock, hasT := strings.Cut(s, "T")
	if kind == "" {
		kind = "Date"
		if hasT {
			kind = "DateTime"
		}
	}
	d.Kind = kind
	parts := strings.Split(date, "-")
	if len(parts) == 0 || len(parts) > 3 || len(parts[0]) != 4 {
		return d, false
	}
	var err error
	if d.Year, err = strconv.Atoi(parts[0]); err != nil {
		return d, false
	}
	d.Precision = PYear
	if len(parts) > 1 {
		if len(parts[1]) != 2 {
			return d, false
		}
		if d.Month, err = strconv.Atoi(parts[1]); err != nil || d.Month < 1 || d.Month > 12 {
			return d, false
		}
		d.Precision = PMonth
	}
	if len(parts) > 2 {
		if len(parts[2]) != 2 {
			return d, false
		}
		if d.Day, err = strconv.Atoi(parts[2]); err != nil || d.Day < 1 || d.Day > daysIn(d.Year, d.Month) {
			return d, false
		}
		d.Precision = PDay
	}
	if hasT && clock != "" {
		if d.Precision != PDay {
			return d, false
		}
		t, ok := parseClock(clock, true)
		if !ok {
			return d, false
		}
		d.Hour, d.Minute, d.Second, d.Milli, d.Precision, d.HasZone, d.ZoneMinutes = t.Hour, t.Minute, t.Second, t.Milli, t.Precision, t.HasZone, t.ZoneMinutes
	}
	return d, true
}

// ParseTime reads a time ("12:34:00", "12:34:00.123").
func ParseTime(s string) (DateTime, bool) {
	t, ok := parseClock(strings.TrimPrefix(s, "T"), false)
	t.Kind = "Time"
	return t, ok
}

func parseClock(s string, zone bool) (DateTime, bool) {
	var d DateTime
	if zone {
		if strings.HasSuffix(s, "Z") {
			d.HasZone, s = true, strings.TrimSuffix(s, "Z")
		} else if i := strings.LastIndexAny(s, "+-"); i > 0 {
			z := s[i:]
			s = s[:i]
			hh, mm, ok := strings.Cut(z[1:], ":")
			if !ok || len(hh) != 2 || len(mm) != 2 {
				return d, false
			}
			h, e1 := strconv.Atoi(hh)
			m, e2 := strconv.Atoi(mm)
			if e1 != nil || e2 != nil || h > 14 || m > 59 {
				return d, false
			}
			d.HasZone, d.ZoneMinutes = true, h*60+m
			if z[0] == '-' {
				d.ZoneMinutes = -d.ZoneMinutes
			}
		}
	}
	main, frac, hasFrac := strings.Cut(s, ".")
	f := strings.Split(main, ":")
	if len(f) == 0 || len(f) > 3 {
		return d, false
	}
	vals := []*int{&d.Hour, &d.Minute, &d.Second}
	for i, p := range f {
		if len(p) != 2 {
			return d, false
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return d, false
		}
		*vals[i] = v
	}
	if d.Hour > 23 || d.Minute > 59 || d.Second > 59 {
		return d, false
	}
	d.Precision = PHour + Precision(len(f)-1)
	if hasFrac {
		if len(f) != 3 || frac == "" || len(frac) > 9 {
			return d, false
		}
		for len(frac) < 3 {
			frac += "0"
		}
		ms, err := strconv.Atoi(frac[:3])
		if err != nil {
			return d, false
		}
		d.Milli, d.Precision = ms, PMillisecond
	}
	return d, true
}

func daysIn(y, m int) int {
	if m == 0 {
		return 31
	}
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (d DateTime) String() string {
	var b strings.Builder
	if d.Kind != "Time" {
		fmt.Fprintf(&b, "%04d", d.Year)
		if d.Precision >= PMonth {
			fmt.Fprintf(&b, "-%02d", d.Month)
		}
		if d.Precision >= PDay {
			fmt.Fprintf(&b, "-%02d", d.Day)
		}
		if d.Precision <= PDay {
			return b.String()
		}
		b.WriteByte('T')
	}
	fmt.Fprintf(&b, "%02d", d.Hour)
	if d.Precision >= PMinute {
		fmt.Fprintf(&b, ":%02d", d.Minute)
	}
	if d.Precision >= PSecond {
		fmt.Fprintf(&b, ":%02d", d.Second)
	}
	if d.Precision >= PMillisecond {
		fmt.Fprintf(&b, ".%03d", d.Milli)
	}
	if d.Kind != "Time" && d.HasZone {
		if d.ZoneMinutes == 0 {
			b.WriteString("Z")
		} else {
			z := d.ZoneMinutes
			sign := '+'
			if z < 0 {
				sign, z = '-', -z
			}
			fmt.Fprintf(&b, "%c%02d:%02d", sign, z/60, z%60)
		}
	}
	return b.String()
}

// instant is the moment, in milliseconds, for comparison (unzoned as UTC); a Time counts from midnight.
func (d DateTime) instant() int64 {
	if d.Kind == "Time" {
		return int64(((d.Hour*60+d.Minute)*60+d.Second)*1000 + d.Milli)
	}
	m, day := d.Month, d.Day
	if m == 0 {
		m = 1
	}
	if day == 0 {
		day = 1
	}
	t := time.Date(d.Year, time.Month(m), day, d.Hour, d.Minute, d.Second, d.Milli*1e6, time.UTC)
	return t.UnixMilli() - int64(d.ZoneMinutes)*60_000
}

// compareDT compares two values; ok is false when the answer depends on precision neither has (FHIRPath's empty).
func compareDT(a, b DateTime) (int, bool) {
	if (a.Kind == "Time") != (b.Kind == "Time") {
		return 0, false
	}
	// Seconds and milliseconds are one precision for comparison.
	pa, pb := a.Precision, b.Precision
	if pa == PMillisecond {
		pa = PSecond
	}
	if pb == PMillisecond {
		pb = PSecond
	}
	if pa != pb {
		// Compare to the shared precision; equal there is unknown.
		p := min(pa, pb)
		ta, tb := a.truncate(p), b.truncate(p)
		c := cmp64(ta.instant(), tb.instant())
		if c == 0 {
			return 0, false
		}
		return c, true
	}
	if a.HasZone != b.HasZone && a.Precision > PDay {
		// One zoned, one not: compared as UTC rather than refused (the common case is an instant against a FHIR dateTime).
		_ = 0
	}
	return cmp64(a.instant(), b.instant()), true
}

func (d DateTime) truncate(p Precision) DateTime {
	t := d
	if p < PMonth {
		t.Month = 0
	}
	if p < PDay {
		t.Day = 0
	}
	if p < PHour {
		t.Hour = 0
	}
	if p < PMinute {
		t.Minute = 0
	}
	if p < PSecond {
		t.Second, t.Milli = 0, 0
	}
	t.Precision = p
	return t
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Quantity is a number with a unit (UCUM code or a calendar duration word).
type Quantity struct {
	Value Decimal
	Unit  string
}

func (q Quantity) String() string {
	if q.Unit == "" {
		return q.Value.String()
	}
	return q.Value.String() + " '" + q.Unit + "'"
}

// Item is one value in a collection, with its FHIR type when known (from a choice suffix or resourceType).
type Item struct {
	V any // map[string]any, []any never, string, bool, int64, Decimal, DateTime, Quantity
	T string
	// resource is the resource this item was navigated from, for getResourceKey and %resource.
}

// Collection is a FHIRPath collection.
type Collection []Item

// ErrEval is an evaluation error: a type mismatch or a singleton expected, which FHIRPath treats as an error.
var ErrEval = errors.New("fhirpath")

func evalErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEval, fmt.Sprintf(format, args...))
}

// FromJSON makes an item from a decoded JSON value (decoded with UseNumber, so numbers keep their precision).
func FromJSON(v any, t string) Item {
	switch x := v.(type) {
	case json.Number:
		s := x.String()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				if t == "" {
					t = "integer"
				}
				return Item{V: i, T: t}
			}
		}
		d, _ := ParseDecimal(s)
		if t == "" {
			t = "decimal"
		}
		return Item{V: d, T: t}
	case float64:
		d, _ := ParseDecimal(strconv.FormatFloat(x, 'f', -1, 64))
		if i, ok := d.IsInt(); ok && !strings.Contains(strconv.FormatFloat(x, 'f', -1, 64), ".") {
			return Item{V: i, T: orType(t, "integer")}
		}
		return Item{V: d, T: orType(t, "decimal")}
	case bool:
		return Item{V: x, T: orType(t, "boolean")}
	case string:
		return Item{V: x, T: t}
	case map[string]any:
		if t == "" {
			if rt, ok := x["resourceType"].(string); ok {
				t = rt
			}
		}
		return Item{V: x, T: t}
	}
	return Item{V: v, T: t}
}

func orType(t, def string) string {
	if t == "" {
		return def
	}
	return t
}

// ToJSON is an item as a JSON-ready value: numbers keep their written precision, dates their written form.
func ToJSON(it Item) any {
	switch x := it.V.(type) {
	case Decimal:
		return json.Number(x.String())
	case int64:
		return x
	case DateTime:
		return x.String()
	case Quantity:
		m := map[string]any{"value": json.Number(x.Value.String())}
		if x.Unit != "" {
			m["unit"] = x.Unit
			m["code"] = x.Unit
		}
		return m
	}
	return it.V
}
