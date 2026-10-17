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

	// Version names this edition of the table, so several can be kept under one name: the one in force, a draft waiting
	// for sign-off, and the ones before. Empty is a table with one edition.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// EffectiveFrom is the first day (YYYY-MM-DD, UTC) this edition is used; empty means from the start. A later approved
	// edition's EffectiveFrom ends this one, so a new mapping can be approved today and take over on the first of the month.
	EffectiveFrom string `yaml:"effective_from,omitempty" json:"effectiveFrom,omitempty"`

	// EffectiveUntil is the last day this edition is used, inclusive; empty means until a later edition takes over.
	EffectiveUntil string `yaml:"effective_until,omitempty" json:"effectiveUntil,omitempty"`

	// Status is approved (the default: in use within its dates), draft (written, waiting for sign-off, never used) or
	// retired (kept for the record, never used).
	Status string `yaml:"status,omitempty" json:"status,omitempty"`

	// Author is the account that saved this edition from the console, which sign-off is checked against.
	Author string `yaml:"author,omitempty" json:"author,omitempty"`

	// ApprovedBy and ApprovedOn record the sign-off of an edition that needed one.
	ApprovedBy string `yaml:"approved_by,omitempty" json:"approvedBy,omitempty"`
	ApprovedOn string `yaml:"approved_on,omitempty" json:"approvedOn,omitempty"`

	// SourceSystem and TargetSystem name the code systems of From and To (http://loinc.org, http://snomed.info/sct, a
	// local URL), so the console can check each entry against an imported code system and FHIR can publish the map.
	SourceSystem string `yaml:"source_system,omitempty" json:"sourceSystem,omitempty"`
	TargetSystem string `yaml:"target_system,omitempty" json:"targetSystem,omitempty"`

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

	switch t.Status {
	case "", StatusApproved, StatusDraft, StatusRetired:
	default:
		errs = append(errs, fmt.Errorf("table %q has status %q; use approved, draft or retired", t.Name, t.Status))
	}
	for _, d := range []struct{ key, value string }{{"effective_from", t.EffectiveFrom}, {"effective_until", t.EffectiveUntil}} {
		if d.value != "" {
			if _, err := time.Parse("2006-01-02", d.value); err != nil {
				errs = append(errs, fmt.Errorf("table %q has %s %q; write a date as YYYY-MM-DD", t.Name, d.key, d.value))
			}
		}
	}
	if t.EffectiveFrom != "" && t.EffectiveUntil != "" && t.EffectiveUntil < t.EffectiveFrom {
		errs = append(errs, fmt.Errorf("table %q ends (%s) before it starts (%s)", t.Name, t.EffectiveUntil, t.EffectiveFrom))
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

// Statuses of a table edition.
const (
	StatusApproved = "approved"
	StatusDraft    = "draft"
	StatusRetired  = "retired"
)

// InUse reports whether the edition may be used at all (approved, not draft or retired).
func (t *Table) InUse() bool { return t.Status == "" || t.Status == StatusApproved }

// InForce reports whether an approved edition's own dates cover day (YYYY-MM-DD). A later edition may still take over; see
// Set.TableAt.
func (t *Table) InForce(day string) bool {
	return t.InUse() && (t.EffectiveFrom == "" || t.EffectiveFrom <= day) && (t.EffectiveUntil == "" || day <= t.EffectiveUntil)
}

// Set is a collection of tables loaded together.
type Set struct {
	Tables []Table `yaml:"tables" json:"tables"`

	// Observe, when set, is told every value a map step could not find in a table, so unmapped codes can be listed for
	// somebody to map. Called on the message's goroutine; it must not block.
	Observe func(table, value string) `yaml:"-" json:"-"`

	byName map[string][]*Table
}

// Validate checks every table and the set as a whole.
func (s *Set) Validate() []error {
	var errs []error

	type edition struct{ name, version string }
	seen := map[edition]bool{}
	starts := map[string]map[string]string{}
	for i := range s.Tables {
		t := &s.Tables[i]
		errs = append(errs, t.Validate()...)

		key := edition{t.Name, t.Version}
		if seen[key] {
			if t.Version == "" {
				errs = append(errs, fmt.Errorf(
					"two tables are called %q; a channel referring to that name would get whichever loaded first. "+
						"To keep several editions, give each a version", t.Name))
			} else {
				errs = append(errs, fmt.Errorf("table %q has two editions called version %q", t.Name, t.Version))
			}
		}
		seen[key] = true

		// Two approved editions starting the same day leave no rule for which one is in force.
		if t.InUse() {
			if starts[t.Name] == nil {
				starts[t.Name] = map[string]string{}
			}
			if other, dup := starts[t.Name][t.EffectiveFrom]; dup {
				from := t.EffectiveFrom
				if from == "" {
					from = "the start"
				}
				errs = append(errs, fmt.Errorf("table %q has two approved editions (%s and %s) in force from %s; give one "+
					"a later effective_from, or make one a draft", t.Name, orUnknown(other), orUnknown(t.Version), from))
			}
			starts[t.Name][t.EffectiveFrom] = t.Version
		}
	}

	return errs
}

// Compile indexes the set for lookup by name.
func (s *Set) Compile() {
	s.byName = make(map[string][]*Table, len(s.Tables))
	for i := range s.Tables {
		s.Tables[i].Compile()
		s.byName[s.Tables[i].Name] = append(s.byName[s.Tables[i].Name], &s.Tables[i])
	}
	for _, list := range s.byName {
		sort.SliceStable(list, func(i, j int) bool { return list[i].EffectiveFrom < list[j].EffectiveFrom })
	}
}

// Table returns the edition of a table in force today (UTC).
func (s *Set) Table(name string) (*Table, bool) {
	return s.TableAt(name, time.Now())
}

// TableAt returns the edition in force at a moment: of the approved editions whose dates cover that day, the one that
// started latest. Drafts and retired editions are never returned.
func (s *Set) TableAt(name string, at time.Time) (*Table, bool) {
	if s == nil {
		return nil, false
	}
	if s.byName == nil {
		s.Compile()
	}
	day := at.UTC().Format("2006-01-02")
	list := s.byName[name]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].InForce(day) {
			return list[i], true
		}
	}
	return nil, false
}

// Editions lists every edition of a table, earliest start first, drafts and retired ones included.
func (s *Set) Editions(name string) []*Table {
	if s == nil {
		return nil
	}
	if s.byName == nil {
		s.Compile()
	}
	return append([]*Table(nil), s.byName[name]...)
}

// Has reports whether any edition of a table exists, whatever its status or dates.
func (s *Set) Has(name string) bool { return len(s.Editions(name)) > 0 }

// NoteUnmapped tells Observe about a value a table did not have.
func (s *Set) NoteUnmapped(table, value string) {
	if s != nil && s.Observe != nil {
		s.Observe(table, value)
	}
}

// Names lists the tables, sorted.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}

	out := make([]string, 0, len(s.Tables))
	seen := map[string]bool{}
	for _, t := range s.Tables {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Diff compares two editions: what was added, removed, and mapped to something else.
type Diff struct {
	Added   []Entry       `json:"added"`
	Removed []Entry       `json:"removed"`
	Changed []EntryChange `json:"changed"`
}

// EntryChange is a value whose mapping changed between editions.
type EntryChange struct {
	From   string `json:"from"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Compare says how edition b differs from edition a.
func Compare(a, b *Table) Diff {
	d := Diff{Added: []Entry{}, Removed: []Entry{}, Changed: []EntryChange{}}
	old := map[string]Entry{}
	if a != nil {
		for _, e := range a.Entries {
			old[e.From] = e
		}
	}
	now := map[string]bool{}
	if b != nil {
		for _, e := range b.Entries {
			now[e.From] = true
			if prev, ok := old[e.From]; !ok {
				d.Added = append(d.Added, e)
			} else if prev.To != e.To {
				d.Changed = append(d.Changed, EntryChange{From: e.From, Before: prev.To, After: e.To})
			}
		}
	}
	if a != nil {
		for _, e := range a.Entries {
			if !now[e.From] {
				d.Removed = append(d.Removed, e)
			}
		}
	}
	return d
}

// Describe renders a table for a specification document.
//
// Includes the provenance, because a specification that says what a field becomes without saying why invites the
// receiving team to argue with the mapping - and the answer to that argument is usually "because you asked us to,
// in 2019".
func (t *Table) Describe() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s: %s\n", t.Name, t.Describes)
	if t.Version != "" || t.EffectiveFrom != "" || t.EffectiveUntil != "" {
		fmt.Fprintf(&b, "  version %s", orNone(t.Version))
		if t.EffectiveFrom != "" {
			fmt.Fprintf(&b, ", in force from %s", t.EffectiveFrom)
		}
		if t.EffectiveUntil != "" {
			fmt.Fprintf(&b, " until %s", t.EffectiveUntil)
		}
		fmt.Fprintln(&b)
	}
	if t.ApprovedBy != "" {
		fmt.Fprintf(&b, "  approved by %s on %s\n", t.ApprovedBy, orNone(t.ApprovedOn))
	}

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

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
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
