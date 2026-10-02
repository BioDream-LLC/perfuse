package delimited

import (
	"fmt"
	"strconv"
	"strings"
)

// Message is what a filter or a transformation step sees.
//
// # Why a slice and not a Record
//
// The unit was already decided, by shipped code rather than by this file: Settings has a Split option, defaulting to true, and
// the engine either delivers each row as its own message or the whole document as one. So there is no choice left to make here
// - a message is one row when splitting and every row when not, and both are a slice.
//
// The pleasant consequence is that the multi-row case needs no special machinery. expr.Prepared already returns every value a
// path addresses, because HL7 fields repeat, so "PatientID" across four hundred rows behaves exactly like a repeating HL7
// field: exists, in and =~ ask about any of them, and the ordering comparisons ask about the single one. That is the same
// answer NCPDP arrived at for repeated segments, and it is not a coincidence - it is the reason the resolver interface returns
// candidates instead of a value.
type Message []Record

// Path addresses a field by column name or by one-based position.
//
// # Two forms, because files arrive both ways
//
// A file with a header row is addressed by name - "PatientID" - which survives a supplier reordering columns, and reordering
// is common enough that position-only addressing would be fragile.
//
// A file without a header has to be addressed by position, written with a leading hash: "#3". The hash is required rather than
// inferred from the value looking like a number, because a column legitimately named "2024" exists in every file exported with
// years as headers, and guessing there would read the wrong field silently.
type Path struct {
	// Column is the name to match, case-insensitively. Empty when the path is positional.
	Column string

	// Position is one-based. Zero when the path is by name.
	Position int
}

// ParsePath reads a path expression.
func ParsePath(raw string) (Path, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Path{}, fmt.Errorf("an empty path addresses nothing")
	}

	if after, ok := strings.CutPrefix(trimmed, "#"); ok {
		position, err := strconv.Atoi(strings.TrimSpace(after))
		if err != nil {
			return Path{}, fmt.Errorf("%q is not a column position: write #3 for the third column", trimmed)
		}
		if position < 1 {
			// One-based to match every spreadsheet and every other tool's error messages, which is the same reason
			// Record.At is one-based. Accepting #0 would quietly mean "the first column" to this code and "before the
			// first column" to the person who wrote it.
			return Path{}, fmt.Errorf("column positions start at 1, not %d", position)
		}
		return Path{Position: position}, nil
	}

	// A bare number is refused rather than treated as a position, because a header row of years or codes makes "2024" a
	// real column name. Refusing names the fix instead of guessing which was meant.
	if _, err := strconv.Atoi(trimmed); err == nil {
		return Path{}, fmt.Errorf("%q is ambiguous: write #%s for the column at that position, or quote it if a column really is named %s", trimmed, trimmed, trimmed)
	}

	return Path{Column: trimmed}, nil
}

// Canonical renders the path in one form, so that two spellings of the same field compare equal.
func (p Path) Canonical() string {
	if p.Position > 0 {
		return "#" + strconv.Itoa(p.Position)
	}
	// Upper-cased because Get matches case-insensitively, so PatientID and patientid are the same field and a copy from one
	// to the other is a copy to itself.
	return strings.ToUpper(p.Column)
}

// String renders the path as written.
func (p Path) String() string {
	if p.Position > 0 {
		return "#" + strconv.Itoa(p.Position)
	}
	return p.Column
}

// Read returns the value this path addresses in every record, in order.
//
// A record that does not have the field contributes nothing rather than an empty string. The distinction matters for the same
// reason it does in HL7: a column absent from the header and a column present but blank are different facts, and collapsing
// them would make exists useless on a file whose supplier stopped sending a column.
func Read(m Message, p Path) []string {
	out := make([]string, 0, len(m))
	for _, record := range m {
		if p.Position > 0 {
			if v, ok := record.At(p.Position); ok {
				out = append(out, v)
			}
			continue
		}
		if v, ok := record.Get(p.Column); ok {
			out = append(out, v)
		}
	}

	return out
}

// ReadOne returns the value when exactly one record carries it.
//
// Not-exactly-one is reported as absent so that a transformation step skips rather than reading a field from one row and
// writing it to another. That is the rule NCPDP arrived at for ambiguous paths, and the failure it prevents is the same:
// silently operating on the wrong one of several candidates.
func ReadOne(m Message, p Path) (string, bool) {
	values := Read(m, p)
	if len(values) != 1 {
		return "", false
	}

	return values[0], true
}

// Set writes a value to the field this path addresses.
//
// # Why writing to many rows is refused
//
// With split on, which is the default, a message is one row and this is unambiguous. With split off a message is the whole
// document, and "set PatientID" across four hundred rows is almost certainly not what somebody meant - it would overwrite
// four hundred different patients with one value. So it is refused, naming the count, rather than done.
//
// Reading across rows stays allowed, because asking whether any row matches is a reasonable question and answering it
// destroys nothing.
func Set(m Message, p Path, value string) (Message, error) {
	if len(m) == 0 {
		return m, fmt.Errorf("there are no records to write to")
	}
	if len(m) > 1 {
		return m, fmt.Errorf("%s addresses a field in each of %d records, and writing one value to all of them would overwrite %d different rows: split the document into a message per row to transform rows individually", p, len(m), len(m))
	}

	out := clone(m)
	record := &out[0]

	if p.Position > 0 {
		if p.Position > len(record.Fields) {
			// Extending is deliberate: a file whose supplier has not yet started sending a trailing column still has to be
			// writable, and refusing would make a mapping impossible to write until the data arrived.
			for len(record.Fields) < p.Position {
				record.Fields = append(record.Fields, "")
			}
		}
		record.Fields[p.Position-1] = value

		return out, nil
	}

	for i, column := range record.Columns {
		if strings.EqualFold(column, p.Column) {
			for len(record.Fields) <= i {
				record.Fields = append(record.Fields, "")
			}
			record.Fields[i] = value

			return out, nil
		}
	}

	// A named column that is not in the header cannot be added, because the header is shared by every record in the
	// document and this message may be one row of many. Adding it here would produce a document whose rows have different
	// widths, which is not a delimited file any more.
	if len(record.Columns) == 0 {
		return m, fmt.Errorf("%s is a column name, but this file has no header row: address it by position instead, as #n", p)
	}

	return m, fmt.Errorf("%s is not a column in this file, whose columns are %s", p, strings.Join(record.Columns, ", "))
}

// clone deep-copies so that a failed step leaves the original untouched.
//
// Fields is copied because it is written to. Columns is shared: it is the header, it is the same slice for every record in the
// document by construction, and nothing here writes to it - Set refuses to add a column rather than appending to the header.
func clone(m Message) Message {
	out := make(Message, len(m))
	for i, record := range m {
		fields := make([]string, len(record.Fields))
		copy(fields, record.Fields)
		out[i] = Record{Fields: fields, Columns: record.Columns, Line: record.Line}
	}

	return out
}
