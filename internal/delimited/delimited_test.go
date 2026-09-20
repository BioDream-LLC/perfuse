package delimited

import (
	"strings"
	"testing"
)

// TestParseWithHeader reads the ordinary case.
func TestParseWithHeader(t *testing.T) {
	data := "PatientID,LastName,Result\nMRN001,Frost,4.2\nMRN002,Okonkwo,5.1\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	if got, ok := records[0].Get("PatientID"); !ok || got != "MRN001" {
		t.Errorf("PatientID came back %q (found %v)", got, ok)
	}
	// Case-insensitive, because a supplier changing capitalisation has not made a breaking change.
	if got, ok := records[1].Get("lastname"); !ok || got != "Okonkwo" {
		t.Errorf("lastname came back %q (found %v)", got, ok)
	}
	// One-based, matching how every spreadsheet and every conversation about a file numbers columns.
	if got, ok := records[0].At(1); !ok || got != "MRN001" {
		t.Errorf("column 1 came back %q", got)
	}
	if _, ok := records[0].At(0); ok {
		t.Error("column 0 resolved to something; positions are one-based")
	}
}

// TestTrailingNewlineDoesNotMakeARecord is the case that matters on every real file.
//
// Every file ends in a newline. A parser that turns that into an empty record produces one bogus message per transfer, forever,
// and it looks like the supplier sending something wrong.
func TestTrailingNewlineDoesNotMakeARecord(t *testing.T) {
	for _, data := range []string{
		"PatientID,Result\nMRN001,4.2\n",
		"PatientID,Result\nMRN001,4.2\n\n",
		"PatientID,Result\nMRN001,4.2\n,\n",
	} {
		records, err := Parse([]byte(data), Settings{HasHeader: true, SkipBlankLines: true})
		if err != nil {
			t.Fatalf("parse %q: %v", data, err)
		}
		if len(records) != 1 {
			t.Errorf("%q produced %d records, want 1", data, len(records))
		}
	}
}

// TestShortRowIsRefused checks a truncated row is an error rather than a shifted one.
//
// This is the failure worth being strict about. A missing field shifts every column after it, so a result lands under the wrong
// heading - and in a laboratory feed that means a value attributed to the wrong test or the wrong patient.
func TestShortRowIsRefused(t *testing.T) {
	data := "PatientID,LastName,Result\nMRN001,Frost,4.2\nMRN002,Okonkwo\n"

	_, err := Parse([]byte(data), Settings{HasHeader: true})
	if err == nil {
		t.Fatal("a short row was accepted")
	}

	// The message must name the line and the expected columns, because a file of five thousand rows is otherwise unsearchable.
	for _, want := range []string{"line 3", "PatientID, LastName, Result"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not mention %q", err, want)
		}
	}
}

// TestRelaxedAcceptsShortRows checks the escape hatch works when asked for.
func TestRelaxedAcceptsShortRows(t *testing.T) {
	data := "PatientID,LastName,Result\nMRN002,Okonkwo\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true, Relaxed: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	// The missing column reports as absent rather than as empty, so a transformation can tell the difference.
	if _, ok := records[0].Get("Result"); ok {
		t.Error("a missing column reported a value")
	}
}

// TestNamedColumnsWithoutHeader checks a headerless file.
func TestNamedColumnsWithoutHeader(t *testing.T) {
	data := "MRN001|Frost|4.2\nMRN002|Okonkwo|5.1\n"

	records, err := Parse([]byte(data), Settings{
		Delimiter: '|',
		Columns:   []string{"PatientID", "LastName", "Result"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records", len(records))
	}
	if got, _ := records[0].Get("Result"); got != "4.2" {
		t.Errorf("Result came back %q", got)
	}
}

// TestQuotedValueContainingDelimiter checks quoting.
func TestQuotedValueContainingDelimiter(t *testing.T) {
	data := "PatientID,Comment\nMRN001,\"raised, repeat in 6 weeks\"\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := records[0].Get("Comment"); got != "raised, repeat in 6 weeks" {
		t.Errorf("the quoted comment came back as %q", got)
	}
}

// TestBareQuotesWithQuotingDisabled is the analyser case.
//
// Some instruments emit values containing a bare quote - 5'2" for a height, or an inch mark in a comment - and never use quotes
// for grouping. A parser that treats those as quoting swallows the rest of the file into one field and reports no error at all,
// which is the worst available outcome.
func TestBareQuotesWithQuotingDisabled(t *testing.T) {
	data := "PatientID,Comment\nMRN001,height 5'2\" per notes\nMRN002,fine\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true, Quote: 0})
	if err != nil {
		t.Fatalf("parse with quoting off: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 - a bare quote swallowed the rest of the file", len(records))
	}
	if got, _ := records[1].Get("PatientID"); got != "MRN002" {
		t.Errorf("the second record came back as %q", got)
	}
}

// TestCommentLinesIgnored checks comment handling.
func TestCommentLinesIgnored(t *testing.T) {
	data := "# exported 2026-08-21\nPatientID,Result\nMRN001,4.2\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true, Comment: '#'})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	if got, _ := records[0].Get("PatientID"); got != "MRN001" {
		t.Errorf("the comment was not skipped; got %q", got)
	}
}

// TestValidateRefusals checks the settings that cannot work.
func TestValidateRefusals(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		want     string
	}{
		{"newline delimiter", Settings{Delimiter: '\n'}, "separates records"},
		{"quote equals delimiter", Settings{Delimiter: ',', Quote: ','}, "no way to tell"},
		{"header and columns", Settings{HasHeader: true, Columns: []string{"A"}}, "use one"},
		{"empty column name", Settings{Columns: []string{"A", "  "}}, "cannot be referred to"},
		{"duplicate column", Settings{Columns: []string{"A", "a"}}, "named twice"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := c.settings.Validate()
			if len(errs) == 0 {
				t.Fatalf("%s was accepted", c.name)
			}
			joined := ""
			for _, e := range errs {
				joined += e.Error() + "; "
			}
			if !strings.Contains(joined, c.want) {
				t.Errorf("the refusal was %q, which does not explain it", joined)
			}
		})
	}
}

// TestDelimiterFromName checks the written names.
//
// Named delimiters exist because a tab cannot be put into a YAML file unambiguously and a file containing an invisible character
// is unreadable to the next person.
func TestDelimiterFromName(t *testing.T) {
	cases := map[string]rune{
		"":          ',',
		"comma":     ',',
		"tab":       '\t',
		"TAB":       '\t',
		"pipe":      '|',
		"semicolon": ';',
		"space":     ' ',
		"~":         '~',
		"\\u001F":   0x1F,
	}

	for input, want := range cases {
		got, err := DelimiterFromName(input)
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("%q gave %q, want %q", input, got, want)
		}
	}

	if _, err := DelimiterFromName("not a delimiter"); err == nil {
		t.Error("a multi-character name was accepted")
	}
}

// TestLineNumbersSurviveHeaderAndComments checks the reported line is the one in the file.
//
// "Row 3", "record 3" and "line 3" are three different numbers once a header and a comment exist, and only the last is useful to
// somebody opening the file in an editor.
func TestLineNumbersSurviveHeaderAndComments(t *testing.T) {
	data := "PatientID,Result\nMRN001,4.2\nMRN002,5.1\n"

	records, err := Parse([]byte(data), Settings{HasHeader: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if records[0].Line != 2 {
		t.Errorf("the first data row reports line %d, want 2", records[0].Line)
	}
	if records[1].Line != 3 {
		t.Errorf("the second data row reports line %d, want 3", records[1].Line)
	}
}
