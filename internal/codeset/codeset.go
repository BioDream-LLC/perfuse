// Package codeset holds mapping tables that outlive the channel that first needed one.
//
// The domain tax is largely re-deriving a mapping somebody already derived. A site's sex codes, patient classes,
// order statuses and facility identifiers get translated the same way in every channel that touches them, and
// today each of those translations is a separate table inside a separate channel. Forty channels means forty
// copies and forty places to fix the next time the receiving system changes.
//
// So a table becomes a named object referenced by name. That much is obvious and it is the smaller half of the
// value.
//
// # The half nobody builds
//
// The larger half is provenance. Most of what makes an interface hard to maintain is not the mapping - it is
// that nobody knows *why*. "We strip leading zeros because the lab's MRN column is numeric" is knowledge that
// exists in one person's head and leaves the building when they retire, and the mapping that remains becomes
// something nobody dares change and nobody dares delete.
//
// So every table records who decided and when, every entry may record its own reason, and both survive into the
// file that goes into version control. A mapping with a reason can be argued with. A mapping without one becomes
// superstition.
//
// # Why the file is separate from the channel
//
// Because it is shared. A table inside a channel is a table only that channel can use, which is the problem.
// And a shared table needs its own history: "who changed the sex mapping, and when" is a question about the
// table, not about whichever channel happened to be edited that day.
package codeset

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Entry is one mapping, with the reason it exists.
type Entry struct {
	// From is the incoming value.
	From string `yaml:"from" json:"from"`

	// To is what it becomes.
	To string `yaml:"to" json:"to"`

	// Why records why this particular entry exists, when it is not obvious.
	//
	// Per entry rather than only per table, because the unobvious ones are usually individual: a table of
	// twelve sex codes has one strange row, and that row is the one somebody will want to delete in three
	// years.
	Why string `yaml:"why,omitempty" json:"why,omitempty"`

	// Since records when this entry was added, for a table that grew over time.
	Since string `yaml:"since,omitempty" json:"since,omitempty"`
}

// Table is a named mapping.
type Table struct {
	// Name is how channels refer to it.
	Name string `yaml:"name" json:"name"`

	// Describes says what this table is for, in words.
	//
	// Required, not optional. A table called "sex" tells the next person nothing about whether it maps into the
	// hospital's codes or out of them, and getting that backwards is a silent data error rather than a
	// failure.
	Describes string `yaml:"describes" json:"describes"`

	// Entries are the mappings.
	Entries []Entry `yaml:"entries" json:"entries"`

	// Default applies when a value is not in the table. Empty means keep the original.
	Default string `yaml:"default,omitempty" json:"default,omitempty"`

	// Strict makes an unmapped value an error rather than a pass-through.
	Strict bool `yaml:"strict,omitempty" json:"strict,omitempty"`

	// DecidedBy and DecidedOn are the provenance of the table as a whole.
	//
	// Free text, deliberately. A name, a team, a ticket number and "the 2019 migration" are all real answers,
	// and a schema that only accepted one of them would get an empty field or a lie.
	DecidedBy string `yaml:"decided_by,omitempty" json:"decidedBy,omitempty"`
	DecidedOn string `yaml:"decided_on,omitempty" json:"decidedOn,omitempty"`

	// Source records where the mapping came from - a specification, a conversation, an observed feed.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// index is the compiled lookup.
	index map[string]Entry
}

// Validate checks a table.
func (t *Table) Validate() []error {
	var errs []error

	if strings.TrimSpace(t.Name) == "" {
		errs = append(errs, fmt.Errorf("a mapping table needs a name, because channels refer to it by name"))
	}

	if strings.TrimSpace(t.Describes) == "" {
		// Refused rather than warned. A table called "sex" does not say whether it maps into the hospital's
		// codes or out of them, and getting that backwards is a silent data error - the receiver accepts the
		// message and the value means something else.
		errs = append(errs, fmt.Errorf(
			"table %q needs a description saying what it maps and in which direction; \"sex\" alone does not "+
				"say whether it converts into this hospital's codes or out of them", t.Name))
	}

	if len(t.Entries) == 0 {
		errs = append(errs, fmt.Errorf("table %q has no entries", t.Name))
	}

	seen := map[string]string{}
	for _, e := range t.Entries {
		if strings.TrimSpace(e.From) == "" {
			// An empty From would silently map every absent value, which is a different operation and almost
			// never what somebody meant.
			errs = append(errs, fmt.Errorf(
				"table %q has an entry with no \"from\" value; to supply a value where none arrived, use a "+
					"set step rather than a mapping", t.Name))
			continue
		}

		if existing, dup := seen[e.From]; dup {
			// Two rows for the same input is always a mistake, and which one wins depends on ordering nobody
			// intended to specify.
			errs = append(errs, fmt.Errorf(
				"table %q maps %q twice, to %q and to %q; one of them is being ignored and it is not obvious "+
					"which", t.Name, e.From, existing, e.To))
			continue
		}
		seen[e.From] = e.To
	}

	if t.Strict && t.Default != "" {
		// Both together is a contradiction: strict says an unmapped value is an error, and a default says it is
		// not. Silently preferring one would make the file lie about what it does.
		errs = append(errs, fmt.Errorf(
			"table %q sets both strict and a default; strict means an unmapped value is an error, a default "+
				"means it is not", t.Name))
	}

	return errs
}

// Compile builds the lookup index.
func (t *Table) Compile() {
	t.index = make(map[string]Entry, len(t.Entries))
	for _, e := range t.Entries {
		t.index[e.From] = e
	}
}

// Lookup translates one value.
//
// Returns the entry as well as the result, so a caller can report the reason a value was changed. That is what
// makes a transformation traceable: "PID-8 M became 1" is a fact, and "because the lab's interface predates the
// HL7 table, per the 2019 migration" is an explanation.
func (t *Table) Lookup(value string) (result string, entry Entry, mapped bool) {
	if t.index == nil {
		t.Compile()
	}

	if e, ok := t.index[value]; ok {
		return e.To, e, true
	}

	if t.Default != "" {
		return t.Default, Entry{}, false
	}

	// The original is kept rather than blanked. An unmapped local code preserved is recoverable; one silently
	// blanked is not, and the receiver cannot tell the difference between "not sent" and "we lost it".
	return value, Entry{}, false
}

// Strictly translates a value and reports an error when it is unmapped and the table is strict.
func (t *Table) Strictly(value string) (string, error) {
	out, _, mapped := t.Lookup(value)
	if !mapped && t.Strict {
		return "", fmt.Errorf("%q is not in the %s table, which is strict: %s",
			value, t.Name, t.Describes)
	}
	return out, nil
}

// Values returns the permitted outputs, sorted.
//
// Useful for generating a contract expectation from a table: if a channel maps a field through a table, the set
// of values that can leave is knowable, and a contract can assert it. That is the two domain-tax features
// meeting - the mapping knows what it produces, and the contract can check nothing else appears.
func (t *Table) Values() []string {
	seen := map[string]bool{}
	for _, e := range t.Entries {
		seen[e.To] = true
	}
	if t.Default != "" {
		seen[t.Default] = true
	}

	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Set is a collection of tables loaded together.
type Set struct {
	Tables []Table `yaml:"tables" json:"tables"`

	byName map[string]*Table
}

// Validate checks every table and the set as a whole.
func (s *Set) Validate() []error {
	var errs []error

	seen := map[string]bool{}
	for i := range s.Tables {
		t := &s.Tables[i]
		errs = append(errs, t.Validate()...)

		if seen[t.Name] {
			errs = append(errs, fmt.Errorf(
				"two tables are called %q; a channel referring to that name would get whichever loaded first",
				t.Name))
		}
		seen[t.Name] = true
	}

	return errs
}

// Compile indexes the set for lookup by name.
func (s *Set) Compile() {
	s.byName = make(map[string]*Table, len(s.Tables))
	for i := range s.Tables {
		s.Tables[i].Compile()
		s.byName[s.Tables[i].Name] = &s.Tables[i]
	}
}

// Table returns a table by name.
func (s *Set) Table(name string) (*Table, bool) {
	if s == nil {
		return nil, false
	}
	if s.byName == nil {
		s.Compile()
	}
	t, ok := s.byName[name]
	return t, ok
}

// Names lists the tables, sorted.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}

	out := make([]string, 0, len(s.Tables))
	for _, t := range s.Tables {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// Describe renders a table for a specification document.
//
// Includes the provenance, because a specification that says what a field becomes without saying why invites the
// receiving team to argue with the mapping - and the answer to that argument is usually "because you asked us to,
// in 2019".
func (t *Table) Describe() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s: %s\n", t.Name, t.Describes)

	if t.DecidedBy != "" || t.DecidedOn != "" {
		fmt.Fprintf(&b, "  decided by %s", orUnknown(t.DecidedBy))
		if t.DecidedOn != "" {
			fmt.Fprintf(&b, " on %s", t.DecidedOn)
		}
		fmt.Fprintln(&b)
	}
	if t.Source != "" {
		fmt.Fprintf(&b, "  from %s\n", t.Source)
	}

	// Sorted, because this ends up in a document that gets diffed between versions.
	entries := append([]Entry(nil), t.Entries...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].From < entries[j].From })

	for _, e := range entries {
		fmt.Fprintf(&b, "  %-12s -> %-12s", e.From, e.To)
		if e.Why != "" {
			fmt.Fprintf(&b, "  (%s)", e.Why)
		}
		fmt.Fprintln(&b)
	}

	switch {
	case t.Strict:
		fmt.Fprintln(&b, "  anything else is an error")
	case t.Default != "":
		fmt.Fprintf(&b, "  anything else becomes %q\n", t.Default)
	default:
		fmt.Fprintln(&b, "  anything else is passed through unchanged")
	}

	return b.String()
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "somebody unrecorded"
	}
	return s
}

// Today is the date format used for Since and DecidedOn.
//
// A plain date rather than a timestamp: the useful granularity for "when was this decided" is the day, and a
// timestamp implies a precision that a human decision does not have.
func Today() string { return time.Now().UTC().Format("2006-01-02") }
