package profile

import (
	"fmt"
	"sort"
	"strings"
)

// Synthesis: a channel YAML generated from observed traffic.
//
// The profile says what a feed actually sends. This turns that into a channel that handles it: a filter for the message types present,
// mapping tables for the code sets seen, and comments saying where every line came from. The result is a hypothesis, not a decision -
// every generated mapping entry carries a `why` explaining why it is there, and no `to` because only a person knows what the code
// should become.
//
// Nothing produced here runs without review. A synthesised channel that is accepted without being read is more dangerous than no channel
// at all, because somebody believes it was built deliberately.

// SynthesisOptions controls what is generated.
type SynthesisOptions struct {
	// ChannelName defaults to "synthesised" when empty.
	ChannelName string
	// Description is the description line in the channel. Defaults to a sentence saying where it came from.
	Description string
	// SourceListen is where the channel will listen. Defaults to 127.0.0.1:0 (any free port).
	SourceListen string
	// DestinationDir is where files go. Defaults to /var/spool/perfuse/<name>.
	DestinationDir string
	// MinFillForContract is the fill rate above which a field is treated as always populated (contract candidate).
	// Defaults to 1.0 (only fields present in every single message).
	MinFillForContract float64
}

func (o SynthesisOptions) withDefaults(r *Report) SynthesisOptions {
	if o.ChannelName == "" {
		o.ChannelName = "synthesised"
	}
	if o.Description == "" {
		o.Description = fmt.Sprintf("generated from %d recorded messages; review every line before running this", r.Messages)
	}
	if o.SourceListen == "" {
		o.SourceListen = "127.0.0.1:0"
	}
	if o.DestinationDir == "" {
		o.DestinationDir = "/var/spool/perfuse/" + o.ChannelName
	}
	if o.MinFillForContract <= 0 || o.MinFillForContract > 1 {
		o.MinFillForContract = 1.0
	}

	return o
}

// Synthesise produces a channel YAML from a profile report.
//
// The result is a string rather than a parsed structure because comments are the most important part: every section carries the
// reasoning, and a data structure cannot hold interleaved comments. A reviewer who disagrees needs to see why a line exists, not merely
// that it does.
func Synthesise(r *Report, suggestions []Suggestion, opts SynthesisOptions) string {
	opts = opts.withDefaults(r)

	var b strings.Builder

	b.WriteString("# GENERATED — review every line.\n")
	b.WriteString("#\n")
	b.WriteString(fmt.Sprintf("# Synthesised from %d messages actually received on this feed.\n", r.Messages))
	b.WriteString("# Nothing here is a decision. It is a hypothesis drawn from traffic, and a confident wrong mapping\n")
	b.WriteString("# in clinical data is worse than no mapping - because somebody accepts it.\n")
	b.WriteString("#\n")
	if r.Unreadable > 0 {
		b.WriteString(fmt.Sprintf("# %d of the %d messages could not be parsed and are excluded from everything below.\n", r.Unreadable, r.Messages+r.Unreadable))
		b.WriteString("#\n")
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("name: %s\n", opts.ChannelName))
	b.WriteString(fmt.Sprintf("description: %s\n", opts.Description))
	b.WriteString("\n")
	b.WriteString("source:\n")
	b.WriteString("  type: mllp\n")
	b.WriteString(fmt.Sprintf("  listen: %s\n", opts.SourceListen))
	b.WriteString("\n")

	// Filter: only the message types actually seen.
	if len(r.Types) > 0 {
		b.WriteString("# Message types observed in the sample. A filter keeps the channel from processing something it\n")
		b.WriteString("# was not designed for, and makes that visible as a filter count rather than a silent failure.\n")
		types := make([]string, 0, len(r.Types))
		for _, t := range r.Types {
			types = append(types, fmt.Sprintf("'%s'", t.Type))
		}
		if len(types) == 1 {
			b.WriteString(fmt.Sprintf("filter: MSH-9 == %s\n", types[0]))
		} else {
			b.WriteString(fmt.Sprintf("filter: MSH-9 in [%s]\n", strings.Join(types, ", ")))
		}
		b.WriteString("\n")
		for _, t := range r.Types {
			b.WriteString(fmt.Sprintf("#   %s: %d messages (%.0f%%)\n", t.Type, t.Count, t.Rate*100))
		}
		b.WriteString("\n")
	}

	// Mapping tables from code sets actually seen.
	tables := synthesiseTables(r, suggestions)
	if len(tables) > 0 {
		b.WriteString("# Mapping tables, stubbed from codes actually sent.\n")
		b.WriteString("#\n")
		b.WriteString("# The \"from\" is what this sender sends. The \"to\" is blank because only a person knows what it\n")
		b.WriteString("# should become. The \"why\" says where the entry came from, so disagreement can be with the\n")
		b.WriteString("# reasoning rather than only the conclusion.\n")
		b.WriteString("#\n")
		b.WriteString("# To use these: fill in the \"to\" values and move the table to a companion codeset file, or\n")
		b.WriteString("# leave them inline and fill in the \"to\" values here.\n")
		b.WriteString("\n")
		b.WriteString("transformations:\n")
		for _, t := range tables {
			writeTable(&b, t)
		}
		b.WriteString("\n")
	}

	// Contract candidates: fields always populated.
	contract := synthesiseContract(r, opts)
	if len(contract) > 0 {
		b.WriteString("# Fields present in every message. These are contract candidates: if the sender ever stops\n")
		b.WriteString("# populating one, something has changed and the channel should refuse rather than deliver garbage.\n")
		b.WriteString("#\n")
		b.WriteString("# Not enforced automatically. Move the ones that matter to a contract file and let the receiver\n")
		b.WriteString("# decide which are load-bearing, because \"always present in the sample\" and \"always present in\n")
		b.WriteString("# the future\" are different claims.\n")
		b.WriteString("#\n")
		for _, f := range contract {
			b.WriteString(fmt.Sprintf("#   %s", f.path))
			if f.name != "" {
				b.WriteString(fmt.Sprintf(" (%s)", f.name))
			}
			b.WriteString(fmt.Sprintf(" — present in %d/%d = %.0f%%\n", f.present, f.total, f.rate*100))
		}
		b.WriteString("\n")
	}

	// Destination.
	b.WriteString("destinations:\n")
	b.WriteString("  - name: archive\n")
	b.WriteString("    type: file\n")
	b.WriteString(fmt.Sprintf("    dir: %s\n", opts.DestinationDir))
	b.WriteString("\n")
	b.WriteString("# Add more destinations as needed. This one writes to disk so nothing is lost while\n")
	b.WriteString("# the real destinations are being configured.\n")

	return b.String()
}

// synthesisTable is one mapping table with its path and entries.
type synthesisTable struct {
	path    string
	name    string
	entries []synthesisEntry
	strict  bool
}

type synthesisEntry struct {
	from string
	why  string
}

// synthesiseTables builds mapping table stubs from code set fields.
func synthesiseTables(r *Report, suggestions []Suggestion) []synthesisTable {
	// Index suggestions by path for the role and evidence.
	byPath := map[string]Suggestion{}
	for _, s := range suggestions {
		byPath[s.Path] = s
	}

	var tables []synthesisTable

	for _, seg := range r.Segments {
		for _, f := range seg.Fields {
			if len(f.Codes) == 0 {
				continue
			}

			// Only fields identified as code sets.
			sugg, has := byPath[f.Path]
			if !has || sugg.Role != RoleCodeSet {
				// Check if the dictionary says it's table-constrained.
				if f.Table == "" {
					continue
				}
			}

			t := synthesisTable{
				path: f.Path,
				name: f.Name,
			}

			for _, c := range f.Codes {
				why := fmt.Sprintf("seen %d times (%.0f%% of messages with this segment)", c.Count, float64(c.Count)/float64(seg.Messages)*100)
				if c.Meaning != "" {
					why += ", standard meaning: " + c.Meaning
				}
				if !c.Known {
					why += " [not in the standard table]"
				}
				t.entries = append(t.entries, synthesisEntry{from: c.Code, why: why})
			}

			tables = append(tables, t)
		}
	}

	return tables
}

// writeTable writes one mapping step.
func writeTable(b *strings.Builder, t synthesisTable) {
	b.WriteString(fmt.Sprintf("  - map:\n"))
	b.WriteString(fmt.Sprintf("      path: %s\n", t.path))
	if t.name != "" {
		b.WriteString(fmt.Sprintf("      # %s\n", t.name))
	}
	b.WriteString("      table:\n")
	for _, e := range t.entries {
		// The `to` is blank - only a person knows what this code should become.
		b.WriteString(fmt.Sprintf("        %s: \"\"    # %s\n", e.from, e.why))
	}
}

// contractField is one field always populated.
type contractField struct {
	path    string
	name    string
	present int
	total   int
	rate    float64
}

// synthesiseContract finds fields populated in every message.
func synthesiseContract(r *Report, opts SynthesisOptions) []contractField {
	var out []contractField

	for _, seg := range r.Segments {
		for _, f := range seg.Fields {
			if f.FillRate < opts.MinFillForContract {
				continue
			}
			if f.Present == 0 {
				continue
			}
			out = append(out, contractField{
				path:    f.Path,
				name:    f.Name,
				present: f.Present,
				total:   seg.Messages,
				rate:    f.FillRate,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })

	return out
}
