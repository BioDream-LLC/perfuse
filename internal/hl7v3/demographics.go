package hl7v3

import (
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// The demographic datatypes: names, addresses, telecoms and timestamps.
//
// These are where v3 differs most visibly from v2. A v2 name is five components in a fixed order; a v3 name is a sequence
// of typed parts, and the sequence carries meaning that the parts do not. That distinction is the reason this file keeps the
// parts in order rather than flattening them into fields.

// Name is a person's name as v3 expresses it.
//
// # Why the parts are kept as a sequence
//
// A v3 PN is an ordered list of typed parts, and order is significant: a Spanish or Portuguese name with two family names
// depends on which came first, and a Hungarian name places the family name before the given name. Flattening to
// Family/Given loses this, and the loss shows up as a patient's name printed backwards on a wristband.
//
// Family and Given are offered because most consumers genuinely want them and writing the loop every time invites getting
// it wrong. But Parts is the record, and Formatted uses it.
type Name struct {
	// Use says what the name is for: L legal, P pseudonym, A artist, C licence, I indigenous, ABC alphabetic,
	// IDE ideographic, SYL syllabic. Empty is common and means unstated rather than legal.
	Use string

	// Parts in document order, which is display order.
	Parts []NamePart

	// Family and Given are conveniences drawn from Parts. Several of each is normal.
	Family []string
	Given  []string

	// Prefix and Suffix hold titles and qualifications.
	Prefix []string
	Suffix []string

	Presence   Presence
	NullFlavor NullFlavor
}

// NamePart is one typed piece of a name.
type NamePart struct {
	// Type is family, given, prefix, suffix, or delimiter. A delimiter is literal text the sender wants preserved
	// between parts.
	Type string

	// Value is the text.
	Value string

	// Qualifier refines the part: BR birth, AD adopted, SP spouse, TITLE, VV voorvoegsel.
	Qualifier string
}

// Formatted renders a name for display, honouring the order the sender gave.
//
// Uses the sender's own delimiters when present and single spaces otherwise. Deliberately does not reorder anything: if a
// sender put the family name first, that is how the person writes their name.
func (n Name) Formatted() string {
	if n.Presence != Present {
		return ""
	}

	var b strings.Builder
	for i, p := range n.Parts {
		if p.Type == "delimiter" {
			b.WriteString(p.Value)
			continue
		}
		if i > 0 && n.Parts[i-1].Type != "delimiter" && b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p.Value)
	}

	return strings.TrimSpace(b.String())
}

// parseName reads a PN or ON element.
func parseName(n *xtree.Node) Name {
	if n == nil {
		return Name{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return Name{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	name := Name{Use: attr(n, "use"), Presence: Present}

	// A name may also be given as bare text with no parts at all, which is what a sender does when it holds the name as
	// one string. Kept as a single family part rather than discarded, because a name with no structure is still a name.
	if len(n.Children) == 0 {
		text := strings.TrimSpace(n.Text)
		if text == "" {
			return Name{Presence: Absent}
		}
		name.Parts = []NamePart{{Type: "family", Value: text}}
		name.Family = []string{text}

		return name
	}

	for _, child := range n.Children {
		value := strings.TrimSpace(child.Text)
		if value == "" {
			continue
		}

		part := NamePart{
			Type:      localName(child.Name),
			Value:     value,
			Qualifier: attr(child, "qualifier"),
		}
		name.Parts = append(name.Parts, part)

		switch part.Type {
		case "family":
			name.Family = append(name.Family, value)
		case "given":
			name.Given = append(name.Given, value)
		case "prefix":
			name.Prefix = append(name.Prefix, value)
		case "suffix":
			name.Suffix = append(name.Suffix, value)
		}
	}

	if len(name.Parts) == 0 {
		return Name{Presence: Absent}
	}

	return name
}

// Address is a postal address.
//
// Same reasoning as Name: the parts are typed and ordered, and countries disagree about order.
type Address struct {
	// Use is H home, HP primary home, HV holiday, WP work, DIR direct, PUB public, TMP temporary, BAD bad address.
	//
	// BAD matters operationally: it means post to this address is known to be returned, and a system that ignores it
	// will keep sending appointment letters into the void.
	Use string

	Parts []AddressPart

	// Conveniences drawn from Parts.
	StreetLines []string
	City        string
	State       string
	PostalCode  string
	Country     string
	County      string

	Presence   Presence
	NullFlavor NullFlavor
}

// AddressPart is one typed piece of an address.
type AddressPart struct {
	Type  string
	Value string
}

// Formatted renders an address on one line, in the order the sender gave.
func (a Address) Formatted() string {
	if a.Presence != Present {
		return ""
	}

	parts := make([]string, 0, len(a.Parts))
	for _, p := range a.Parts {
		if p.Type == "delimiter" {
			continue
		}
		parts = append(parts, p.Value)
	}

	return strings.Join(parts, ", ")
}

// Undeliverable reports whether the sender flagged this address as known-bad.
//
// Named as a question rather than exposing the code, because "use == BAD" reads as a data quality problem and this is
// actually a positive assertion: somebody tried, post came back, and they recorded it.
func (a Address) Undeliverable() bool { return strings.EqualFold(a.Use, "BAD") }

// parseAddress reads an AD element.
func parseAddress(n *xtree.Node) Address {
	if n == nil {
		return Address{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return Address{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	addr := Address{Use: attr(n, "use"), Presence: Present}

	for _, child := range n.Children {
		value := strings.TrimSpace(child.Text)
		if value == "" {
			continue
		}

		kind := localName(child.Name)
		addr.Parts = append(addr.Parts, AddressPart{Type: kind, Value: value})

		switch kind {
		case "streetAddressLine", "streetName", "houseNumber", "additionalLocator", "unitID":
			addr.StreetLines = append(addr.StreetLines, value)
		case "city":
			addr.City = value
		case "state":
			addr.State = value
		case "postalCode":
			addr.PostalCode = value
		case "country":
			addr.Country = value
		case "county":
			addr.County = value
		}
	}

	if len(addr.Parts) == 0 {
		return Address{Presence: Absent}
	}

	return addr
}

// Telecom is a phone number, email address or other contact point.
type Telecom struct {
	// Scheme is the URI scheme: tel, mailto, fax, http.
	Scheme string

	// Value is the part after the scheme - the number or address itself.
	Value string

	// Raw is the whole thing as sent, kept because a sender that omits the scheme is common and the raw form is what an
	// analyst will search a log for.
	Raw string

	// Use is HP home, WP work, MC mobile, EC emergency contact, and so on.
	Use string

	Presence   Presence
	NullFlavor NullFlavor
}

// parseTelecom reads a TEL element.
func parseTelecom(n *xtree.Node) Telecom {
	if n == nil {
		return Telecom{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return Telecom{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	raw := attr(n, "value")
	if raw == "" {
		raw = strings.TrimSpace(n.Text)
	}
	if raw == "" {
		return Telecom{Presence: Absent}
	}

	t := Telecom{Raw: raw, Use: attr(n, "use"), Presence: Present, Value: raw}
	if i := strings.IndexByte(raw, ':'); i > 0 {
		t.Scheme = strings.ToLower(raw[:i])
		t.Value = raw[i+1:]
	}

	return t
}

// Timestamp is a v3 TS.
//
// The wire format is the same family as v2: YYYY, YYYYMM, YYYYMMDD, then optional time to seconds and fractions, then an
// optional offset. The precision is meaningful and is kept.
type Timestamp struct {
	// Time is the parsed instant. Only meaningful when Presence is Present.
	Time time.Time

	// Raw is the value exactly as sent.
	Raw string

	// Precision says how much of the value the sender actually gave.
	//
	// Kept because a birth date given to the year is not the same claim as one given to the day, and a demographic
	// match that treats "1971" as "1971-01-01" will match the wrong patient with a January birthday.
	Precision Precision

	// HasOffset says whether the sender stated a UTC offset.
	//
	// When false, the instant is in the sender's local time and there is no way to know what that was from the message
	// alone. This is the single most common cause of appointments landing an hour out, and it is worth knowing that the
	// message did not say rather than assuming UTC.
	HasOffset bool

	Presence   Presence
	NullFlavor NullFlavor
}

// Precision is how much of a timestamp was given.
type Precision int

const (
	// PrecisionNone means nothing parseable.
	PrecisionNone Precision = iota
	PrecisionYear
	PrecisionMonth
	PrecisionDay
	PrecisionHour
	PrecisionMinute
	PrecisionSecond
	PrecisionFraction
)

// String names a precision for a log.
func (p Precision) String() string {
	switch p {
	case PrecisionYear:
		return "year"
	case PrecisionMonth:
		return "month"
	case PrecisionDay:
		return "day"
	case PrecisionHour:
		return "hour"
	case PrecisionMinute:
		return "minute"
	case PrecisionSecond:
		return "second"
	case PrecisionFraction:
		return "fraction"
	default:
		return "none"
	}
}

// parseTimestamp reads a TS element, taking the value from the attribute or the text.
func parseTimestamp(n *xtree.Node) Timestamp {
	if n == nil {
		return Timestamp{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return Timestamp{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	raw := attr(n, "value")
	if raw == "" {
		raw = strings.TrimSpace(n.Text)
	}
	if raw == "" {
		return Timestamp{Presence: Absent}
	}

	return ParseTimestamp(raw)
}

// ParseTimestamp reads a v3 timestamp string.
//
// Exported because a channel script frequently has one of these in hand from somewhere else - a query parameter, a CDA
// document - and should not have to reimplement the precision rules.
//
// An unparseable value yields Presence Null with nullFlavor "OTH", which is v3's own way of saying "there is a value here
// and it is not one of the permitted ones". Returning an error instead would mean a single malformed timestamp stopped a
// whole message, and a message whose only fault is one bad date is usually still worth delivering.
func ParseTimestamp(raw string) Timestamp {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Timestamp{Presence: Absent}
	}

	ts := Timestamp{Raw: raw, Presence: Present}

	// Split off the offset first. A leading sign belongs to the offset, never to the date.
	body := raw
	offset := ""
	if i := strings.LastIndexAny(body, "+-"); i > 0 {
		offset = body[i:]
		body = body[:i]
		ts.HasOffset = true
	}
	if strings.HasSuffix(strings.ToUpper(body), "Z") {
		body = body[:len(body)-1]
		offset = "+0000"
		ts.HasOffset = true
	}

	// Fractional seconds.
	fraction := ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		fraction = body[i+1:]
		body = body[:i]
	}

	digits := body
	for _, r := range digits {
		if r < '0' || r > '9' {
			// Not a v3 timestamp at all. Reported as OTH rather than as an error, so one bad date does not stop a
			// message.
			return Timestamp{Raw: raw, Presence: Null, NullFlavor: Other}
		}
	}

	layout := ""
	switch {
	case len(digits) >= 14:
		digits, layout, ts.Precision = digits[:14], "20060102150405", PrecisionSecond
	case len(digits) >= 12:
		digits, layout, ts.Precision = digits[:12], "200601021504", PrecisionMinute
	case len(digits) >= 10:
		digits, layout, ts.Precision = digits[:10], "2006010215", PrecisionHour
	case len(digits) >= 8:
		digits, layout, ts.Precision = digits[:8], "20060102", PrecisionDay
	case len(digits) >= 6:
		digits, layout, ts.Precision = digits[:6], "200601", PrecisionMonth
	case len(digits) >= 4:
		digits, layout, ts.Precision = digits[:4], "2006", PrecisionYear
	default:
		return Timestamp{Raw: raw, Presence: Null, NullFlavor: Other}
	}

	if fraction != "" && ts.Precision == PrecisionSecond {
		ts.Precision = PrecisionFraction
		// Go wants a fixed number of fractional digits in the layout, so pad or trim to nanoseconds.
		if len(fraction) > 9 {
			fraction = fraction[:9]
		}
		digits += "." + fraction
		layout += "." + strings.Repeat("0", len(fraction))
	}

	if offset != "" {
		// A sender may write +05, +0500 or +05:30. Normalised to four digits.
		off := strings.ReplaceAll(offset, ":", "")
		switch len(off) {
		case 3:
			off += "00"
		case 5:
		default:
			if len(off) < 5 {
				off = off + strings.Repeat("0", 5-len(off))
			} else {
				off = off[:5]
			}
		}
		digits += off
		layout += "-0700"
	}

	t, err := time.Parse(layout, digits)
	if err != nil {
		return Timestamp{Raw: raw, Presence: Null, NullFlavor: Other}
	}

	ts.Time = t

	return ts
}

// localName strips a namespace prefix from an element name.
//
// Prefixes are the sender's choice and vary between systems implementing the same version, so matching on them would make
// extraction work for one hospital and silently return nothing for the next.
//
// # It does not fire for a parsed document, and that is worth knowing
//
// xtree.Parse stores xml.Name.Local, so Go's decoder has already resolved the prefix before anything here sees a node - a document
// using hl7:patientPerson arrives with the name "patientPerson". Removing this function breaks no test.
//
// Kept anyway, for a reason that is not sentiment: xtree.New and xtree.Leaf build nodes with whatever name they are handed, so a
// tree assembled in code rather than parsed from bytes can carry a prefix. Path.Resolve is exported and may be given one, and
// there is now a test that does exactly that.
//
// Written down because the alternative is somebody finding this later, correctly observing that nothing covers it, and removing it
// - which would be right about the evidence and wrong about the reason. The property that a prefixed document works is pinned in
// xtree, which is where it is actually delivered.
func localName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}

	return name
}
