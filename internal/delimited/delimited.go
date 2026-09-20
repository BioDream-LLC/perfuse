// Package delimited parses records separated and split by configured characters.
//
// This is Mirth's Delimited data type: CSV, tab-separated, pipe-separated, fixed-column files, and the long tail of formats a
// laboratory analyser or a bureau service emits because somebody wrote it in 1994 and it works.
//
// Deliberately not a general CSV library. What matters here is what a healthcare feed does that a well-formed CSV does not: a
// header row that may or may not be present, a trailing empty line that must not become an empty record, columns referenced by
// name so a transformation does not break when a supplier inserts one, and the ability to treat a whole file as one message or
// each row as its own.
//
// The last of those is the decision that shapes everything else. A file of five thousand results is either one message or five
// thousand, and the two are not interchangeable: one message means one acknowledgement and one failure for the whole file, and
// five thousand means each row can be filtered, transformed and retried on its own.
package delimited

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Settings describe a delimited format.
type Settings struct {
	// Delimiter separates columns. Defaults to a comma.
	Delimiter rune

	// Quote wraps a value containing the delimiter. Defaults to a double quote. Zero disables quoting entirely.
	//
	// Disabling matters: some analysers emit values containing bare quotes and never use them for grouping, and a parser that
	// treats those as quoting produces one enormous field and no error.
	Quote rune

	// Comment starts a line to ignore. Zero means none.
	Comment rune

	// HasHeader treats the first row as column names.
	HasHeader bool

	// Columns names the columns when there is no header row.
	//
	// Given explicitly rather than inferred, because a file with no header has no names, and referring to columns by position
	// makes every transformation break the day a supplier inserts one in the middle.
	Columns []string

	// TrimSpace removes surrounding whitespace from every value.
	TrimSpace bool

	// SkipBlankLines ignores lines that are empty or contain only delimiters.
	//
	// On by default in effect, because a trailing newline is universal and a record of empty fields is not a record. A file
	// ending in a newline would otherwise produce a final empty message on every transfer.
	SkipBlankLines bool

	// Relaxed accepts rows whose field count differs from the header.
	//
	// Off by default. A short row is usually a truncated file or a delimiter inside an unquoted value, and accepting it
	// silently means the columns after that point are shifted - which produces a result under the wrong patient rather than
	// an error.
	Relaxed bool
}

// Resolved returns the settings with defaults applied.
func (s Settings) Resolved() Settings {
	out := s
	if out.Delimiter == 0 {
		out.Delimiter = ','
	}
	return out
}

// Validate reports problems with the settings.
func (s Settings) Validate() []error {
	var errs []error

	r := s.Resolved()

	if r.Delimiter == '\n' || r.Delimiter == '\r' {
		errs = append(errs, errors.New("the delimiter cannot be a newline, because that is what separates records"))
	}
	if r.Quote != 0 && r.Quote == r.Delimiter {
		errs = append(errs, fmt.Errorf("the quote character and the delimiter are both %q, so there would be no way to "+
			"tell a quoted value from the next column", r.Quote))
	}
	if r.Comment != 0 && r.Comment == r.Delimiter {
		errs = append(errs, fmt.Errorf("the comment character and the delimiter are both %q", r.Comment))
	}
	if s.HasHeader && len(s.Columns) > 0 {
		// Refused rather than resolved by precedence. Both being set means somebody expects one of them to win, and which
		// one is not guessable from the file.
		errs = append(errs, errors.New("has_header and columns are both set; use one - either the file names its columns "+
			"or you do"))
	}

	seen := map[string]bool{}
	for _, name := range s.Columns {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			errs = append(errs, errors.New("a column name is empty; name every column, because a nameless one cannot be "+
				"referred to in a transformation"))
			continue
		}
		if seen[strings.ToLower(trimmed)] {
			errs = append(errs, fmt.Errorf("column %q is named twice, so a reference to it would be ambiguous", trimmed))
			continue
		}
		seen[strings.ToLower(trimmed)] = true
	}

	return errs
}

// Record is one row.
type Record struct {
	// Fields are the values in order.
	Fields []string

	// Columns are the names, in the same order. Empty when the format has none.
	Columns []string

	// Line is the line number the record started on, counting from one.
	//
	// Kept because a rejected row in a file of five thousand is unfindable without it, and "row 3", "record 3" and "line 3"
	// are three different numbers once a header and comments exist.
	Line int
}

// Get returns a field by column name, case-insensitively.
//
// Case-insensitive because a supplier who writes "PatientID" one month and "PATIENTID" the next has not made a breaking change
// and should not cause one.
func (r Record) Get(name string) (string, bool) {
	for i, column := range r.Columns {
		if strings.EqualFold(column, name) && i < len(r.Fields) {
			return r.Fields[i], true
		}
	}
	return "", false
}

// At returns a field by one-based position.
//
// One-based to match how every spreadsheet, every error message from every other tool, and every conversation about a file
// numbers its columns. Zero-based here would be correct for a programmer and wrong for everybody describing the problem.
func (r Record) At(position int) (string, bool) {
	if position < 1 || position > len(r.Fields) {
		return "", false
	}
	return r.Fields[position-1], true
}

// Parse reads every record from a delimited document.
func Parse(data []byte, settings Settings) ([]Record, error) {
	s := settings.Resolved()

	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.Comma = s.Delimiter
	reader.Comment = s.Comment
	reader.TrimLeadingSpace = s.TrimSpace
	// Always variable, and checked here instead. The standard library's own check reports a wrong field count without saying
	// which columns were expected, and that message is the whole value of the error for somebody looking at a supplier's file.
	reader.FieldsPerRecord = -1
	if s.Quote == 0 {
		reader.LazyQuotes = true
	}

	var columns []string
	if len(s.Columns) > 0 {
		columns = make([]string, len(s.Columns))
		for i, name := range s.Columns {
			columns[i] = strings.TrimSpace(name)
		}
	}

	var out []Record
	line := 0

	for {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// The line number comes from the reader's own position, which is correct across quoted values containing
			// newlines - somewhere a count of records would already have gone wrong.
			return nil, fmt.Errorf("delimited: %w", err)
		}
		line++

		if s.TrimSpace {
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
		}

		if s.SkipBlankLines && allEmpty(fields) {
			continue
		}

		if s.HasHeader && columns == nil {
			columns = fields
			continue
		}

		if len(columns) > 0 && len(fields) != len(columns) && !s.Relaxed {
			return nil, fmt.Errorf("delimited: line %d has %d field(s) and the format has %d column(s) (%s); a short "+
				"row is usually a truncated file or a delimiter inside an unquoted value, and accepting it would shift "+
				"every column after it",
				line, len(fields), len(columns), strings.Join(columns, ", "))
		}

		out = append(out, Record{Fields: fields, Columns: columns, Line: line})
	}

	if s.HasHeader && columns == nil {
		return nil, errors.New("delimited: the format expects a header row and the document is empty")
	}

	return out, nil
}

func allEmpty(fields []string) bool {
	for _, f := range fields {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// DelimiterFromName turns a written name into a character.
//
// Named delimiters exist because a tab cannot be typed into a YAML file unambiguously and a configuration file saying "tab" is
// readable where one containing an invisible character is not. The same applies to a pipe, which YAML would otherwise need
// quoted.
func DelimiterFromName(name string) (rune, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "comma", ",":
		return ',', nil
	case "tab", "\\t":
		return '\t', nil
	case "pipe", "|":
		return '|', nil
	case "semicolon", ";":
		return ';', nil
	case "colon", ":":
		return ':', nil
	case "space":
		return ' ', nil
	case "caret", "^":
		return '^', nil
	}

	// A single character is taken literally, so an unusual delimiter does not need a name added here first.
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 1 {
		return runes[0], nil
	}

	// Unicode escapes, for the genuinely obscure. Unit separator (001F) turns up in older laboratory formats.
	if strings.HasPrefix(name, "\\u") || strings.HasPrefix(name, "0x") {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(name, "\\u"), "0x")
		if value, err := strconv.ParseInt(trimmed, 16, 32); err == nil && value > 0 {
			return rune(value), nil
		}
	}

	return 0, fmt.Errorf("delimited: %q is not a delimiter; use a single character, or one of comma, tab, pipe, "+
		"semicolon, colon, space or caret", name)
}
