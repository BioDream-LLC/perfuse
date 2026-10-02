package config

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/delimited"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/steps"
)

// Delimited configures a delimited channel.
//
// Mirth's Delimited data type: CSV, tab-separated, and the long tail of formats a laboratory analyser or a bureau service emits.
type Delimited struct {
	// Delimiter separates columns, as a character or a name.
	//
	// A name is accepted because a tab cannot be written into a YAML file unambiguously, and a configuration file containing
	// an invisible character is unreadable to whoever opens it next.
	Delimiter string `yaml:"delimiter,omitempty"`

	// Quote wraps values containing the delimiter. Defaults to a double quote; "none" disables it.
	Quote string `yaml:"quote,omitempty"`

	// Comment starts a line to ignore.
	Comment string `yaml:"comment,omitempty"`

	// HasHeader treats the first row as column names.
	HasHeader bool `yaml:"has_header,omitempty"`

	// Columns names the columns when the file has no header row.
	Columns []string `yaml:"columns,omitempty"`

	// TrimSpace removes surrounding whitespace from every value.
	TrimSpace bool `yaml:"trim_space,omitempty"`

	// Relaxed accepts rows whose field count differs from the header.
	Relaxed bool `yaml:"relaxed,omitempty"`

	// Split makes each row its own message. Defaults to true.
	//
	// This is the decision that shapes a delimited channel and it deserves saying out loud. A file of five thousand results is
	// either one message or five thousand. One message means one acknowledgement, one entry in the browser, and one failure
	// that takes the whole file with it. Five thousand means each row is filtered, transformed, delivered and retried on its
	// own, and a single bad row does not stop the rest.
	//
	// Splitting is the default because per-row handling is what almost every site wants and because the alternative is
	// discovered late: a channel that looked fine in testing with a three-row file fails an entire night's transfer over one
	// malformed line.
	Split *bool `yaml:"split,omitempty"`

	// KeepBlankLines stops blank lines being skipped.
	//
	// Skipping is the default, because every file ends in a newline and a record of empty fields is not a record. This exists
	// for the rare format where a blank line is meaningful.
	KeepBlankLines bool `yaml:"keep_blank_lines,omitempty"`

	// Filter decides which messages continue, written against column names.
	//
	// Here rather than on the channel because the channel-level filter compiles HL7 paths. Two settings named filter would be
	// confusing, but one setting that means different things depending on dataType is worse: a delimited channel that
	// silently compiled its filter as HL7 would match nothing and drop everything, which is the failure the top-level
	// refusal exists to prevent.
	Filter string `yaml:"filter,omitempty"`

	// Transformations are the declarative steps, addressing columns rather than HL7 fields.
	//
	// Separate from the channel's transformations for the same reason as Filter, and it is the arrangement X12 and NCPDP
	// already use.
	Transformations []steps.Step `yaml:"transformations,omitempty"`

	// compiled holds the parsed filter and steps. Unexported so that a caller cannot read a filter that was never compiled -
	// the bug that let a destination filter be skipped for a whole class of channel.
	compiledFilter expr.Expr[delimited.Message]
	compiledSteps  *steps.Steps[delimited.Message]
}

// FilterExpr returns the compiled filter, or nil when there is none.
func (d *Delimited) FilterExpr() expr.Expr[delimited.Message] {
	if d == nil {
		return nil
	}

	return d.compiledFilter
}

// Steps returns the compiled transformation steps.
func (d *Delimited) Steps() *steps.Steps[delimited.Message] {
	if d == nil {
		return nil
	}

	return d.compiledSteps
}

// Splits reports whether each row becomes its own message.
func (d *Delimited) Splits() bool {
	if d == nil || d.Split == nil {
		return true
	}
	return *d.Split
}

// Settings converts the configuration into parser settings.
func (d *Delimited) Settings() (delimited.Settings, error) {
	if d == nil {
		return delimited.Settings{SkipBlankLines: true}.Resolved(), nil
	}

	out := delimited.Settings{
		HasHeader:      d.HasHeader,
		Columns:        d.Columns,
		TrimSpace:      d.TrimSpace,
		Relaxed:        d.Relaxed,
		SkipBlankLines: !d.KeepBlankLines,
	}

	delim, err := delimited.DelimiterFromName(d.Delimiter)
	if err != nil {
		return out, err
	}
	out.Delimiter = delim

	switch strings.ToLower(strings.TrimSpace(d.Quote)) {
	case "":
		out.Quote = '"'
	case "none", "off":
		out.Quote = 0
	default:
		runes := []rune(strings.TrimSpace(d.Quote))
		if len(runes) != 1 {
			return out, fmt.Errorf("delimited.quote is %q; use a single character or \"none\"", d.Quote)
		}
		out.Quote = runes[0]
	}

	if trimmed := strings.TrimSpace(d.Comment); trimmed != "" {
		runes := []rune(trimmed)
		if len(runes) != 1 {
			return out, fmt.Errorf("delimited.comment is %q; use a single character", d.Comment)
		}
		out.Comment = runes[0]
	}

	return out.Resolved(), nil
}

func validateDelimited(c *Channel) []error {
	var errs []error

	settings, err := c.Delimited.Settings()
	if err != nil {
		return []error{err}
	}
	errs = append(errs, settings.Validate()...)

	if c.Delimited == nil {
		// Allowed. A channel that says nothing gets comma-separated with no header, which is the commonest shape - and
		// requiring a block for it would be ceremony.
		return errs
	}

	if !c.Delimited.HasHeader && len(c.Delimited.Columns) == 0 {
		// A warning would be ignored, so this is refused. Without names, every transformation has to refer to columns by
		// position, and the day a supplier inserts a column in the middle every one of them silently reads the wrong field.
		errs = append(errs, fmt.Errorf("channel %q parses delimited data but names no columns and expects no header row, "+
			"so transformations could only refer to columns by position - which breaks silently the day a column is "+
			"inserted; set has_header or list the columns", c.Name))
	}

	return errs
}
