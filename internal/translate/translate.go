// Package translate converts a Mirth channel into a Perfuse channel definition.
//
// `perfuse explain` reports what blocks a migration. This does most of the
// migration, which is a different and much larger claim, so it is deliberately
// conservative about what it will assert.
//
// Three rules shape the whole thing.
//
// **Nothing is silently dropped.** Every part of the source channel is either
// translated, carried across verbatim as a script, or reported as needing a human.
// A translator that quietly omitted a step would produce a channel that starts,
// runs, and loses data — the worst possible outcome and the reason people distrust
// migration tools.
//
// **Declarative where it is certain, script where it is not.** A Mapper step that
// copies a field becomes a declarative step, because that is readable and
// checkable. A JavaScript step becomes a script, because Perfuse runs Mirth's
// JavaScript unchanged and rewriting it into something that looks equivalent is
// exactly where a translator would introduce a difference nobody notices.
//
// **The output is meant to be read.** It carries comments explaining what came
// from where, and what was left for a person. A migration is reviewed by somebody
// who has to sign off on it, and an unannotated wall of YAML cannot be reviewed.
package translate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Confidence is how much of a channel came across mechanically.
type Confidence string

const (
	// Complete means everything translated and nothing needs a decision.
	Complete Confidence = "complete"
	// Reviewable means it translated, but something should be looked at: a script
	// carried over, a transport guessed, a setting with no equivalent.
	Reviewable Confidence = "reviewable"
	// Partial means part of the channel could not be translated and the result is
	// a starting point rather than a migration.
	Partial Confidence = "partial"
	// Blocked means the channel cannot run here without rewriting: JVM interop, a
	// transport that does not exist, a database connector.
	Blocked Confidence = "blocked"
)

// Note is something the reader needs to know.
type Note struct {
	// Severity is info, warning or blocker.
	Severity string `json:"severity"`

	// Where locates it, for example "destination 1 step 3".
	Where string `json:"where,omitempty"`

	// Message says what happened.
	Message string `json:"message"`

	// Action says what to do about it, when there is something to do. Separated
	// from Message so a report can show only the actionable half.
	Action string `json:"action,omitempty"`
}

// Result is one translated channel.
type Result struct {
	// Name is the Perfuse channel name.
	Name string `json:"name"`

	// SourceName is the original Mirth channel name, which may differ once
	// sanitised.
	SourceName string `json:"sourceName"`

	// YAML is the channel definition.
	YAML string `json:"yaml"`

	// Confidence summarises how much needs a human.
	Confidence Confidence `json:"confidence"`

	// Notes explain everything that was not a straight translation.
	Notes []Note `json:"notes"`

	// Counts summarise the work, for a one-line report over many channels.
	Counts Counts `json:"counts"`
}

// Counts summarise what happened.
type Counts struct {
	// Declarative is steps turned into declarative transformations.
	Declarative int `json:"declarative"`
	// Scripted is steps carried across as JavaScript.
	Scripted int `json:"scripted"`
	// FilterRules is rules turned into a filter expression.
	FilterRules int `json:"filterRules"`
	// Blockers is things that stop it running.
	Blockers int `json:"blockers"`
	// Warnings is things to look at.
	Warnings int `json:"warnings"`
}

// Blockers reports whether anything stops the channel running.
func (r *Result) Blocked() bool { return r.Counts.Blockers > 0 }

// builder accumulates the translation.
type builder struct {
	ch     *mirth.Channel
	notes  []Note
	counts Counts

	// scriptSteps are transformer steps carried over verbatim, in order.
	scriptSteps []mirth.Step
	// scriptFilter are filter rules carried over as a script filter.
	scriptFilter []mirth.Rule
}

func (b *builder) note(severity, where, message, action string) {
	b.notes = append(b.notes, Note{
		Severity: severity, Where: where, Message: message, Action: action,
	})
	switch severity {
	case "blocker":
		b.counts.Blockers++
	case "warning":
		b.counts.Warnings++
	}
}

// Channel translates one Mirth channel.
func Channel(ch *mirth.Channel) *Result {
	b := &builder{ch: ch}

	res := &Result{
		SourceName: ch.Name,
		Name:       sanitiseName(ch.Name),
	}

	if res.Name != ch.Name {
		b.note("info", "", fmt.Sprintf(
			"the channel name became %q, because a Perfuse channel name is also its "+
				"file name", res.Name),
			"")
	}

	// Channel-level notes are gathered before the YAML so they appear in the report
	// even when nothing about them reaches the file.
	b.storageNotes()
	b.javaNotes()

	yaml := b.build()

	res.YAML = yaml
	// Empty rather than nil, so the wire carries [] and not null. Every real Mirth channel produces at
	// least one note, so this is unlikely rather than impossible - and "unlikely" is how the same defect
	// reached the FHIR lab, where it crashed the view on the one input that worked perfectly.
	if b.notes == nil {
		res.Notes = []Note{}
	} else {
		res.Notes = b.notes
	}
	res.Counts = b.counts
	res.Confidence = b.confidence()
	return res
}

// javaNotes reports the Java a script reaches for.
//
// Perfuse has no Java runtime and refuses those calls with an error naming the class - but that happens when the script
// runs, which for a source connector means when the first real message arrives. Before this, an import succeeded
// silently, the channel deployed, and the migration failed on live traffic. A migration that looks clean and breaks on
// first traffic is worse than one that refuses at the door, because the person who ran it has already told their
// colleagues it worked.
//
// The severities are not uniform, because the work attached is not uniform. A call Perfuse already implements is
// information. SimpleDateFormat is a warning worth minutes. A vendor jar is a blocker.
func (b *builder) javaNotes() {
	r := b.ch.ScanJava()

	for _, u := range r.Uses {
		severity := "warning"
		switch u.Verdict {
		case mirth.VerdictSupported:
			severity = "info"
		case mirth.VerdictNeedsFeature, mirth.VerdictOutOfScope:
			severity = "blocker"
		}
		b.note(severity,
			fmt.Sprintf("%s, line %d", u.Where, u.Line),
			fmt.Sprintf("this script uses Java: %s", u.Reference),
			u.Advice)
	}

	// An external script is a path on the old server, not content in the export, so nothing can be said about what it
	// contains. Reporting the channel as clean would be a lie by omission.
	for _, path := range r.ExternalScripts {
		b.note("warning", path,
			"this step runs an external script file, which is referenced by path and is not part of the Mirth export",
			"Fetch the file from the old server and paste it into a JavaScript step, then re-run this import to have "+
				"its contents checked.")
	}
}

func (b *builder) confidence() Confidence {
	switch {
	case b.counts.Blockers > 0:
		return Blocked
	case b.counts.Warnings > 0:
		return Reviewable
	case b.counts.Scripted > 0:
		// A carried-over script runs, but nobody has read it in this context. That
		// is reviewable rather than complete: the point of a migration report is to
		// tell somebody where to look, and "look at the scripts" is the answer.
		return Reviewable
	default:
		return Complete
	}
}

// sanitiseName makes a Mirth channel name usable as a Perfuse name and filename.
func sanitiseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed-channel"
	}

	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// Spaces, brackets, slashes and anything else become one dash. A Mirth
			// name like "ADT Inbound (Site A) - v2" is common and would otherwise
			// produce a filename nobody can type.
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed-channel"
	}
	return out
}

// FileName returns the file to write a result to.
func (r *Result) FileName() string { return r.Name + ".yaml" }

// SortNotes orders notes blockers first, so a report leads with what matters.
func (r *Result) SortNotes() {
	rank := map[string]int{"blocker": 0, "warning": 1, "info": 2}
	sort.SliceStable(r.Notes, func(i, j int) bool {
		return rank[r.Notes[i].Severity] < rank[r.Notes[j].Severity]
	})
}
