package main

import (
	"log/slog"
	"reflect"
	"sort"

	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// channelTables exposes the mapping tables the loaded channels use, for the FHIR terminology endpoints.
//
// Read from the channel files on every call rather than cached at startup.
//
// Deliberate: an analyst editing a mapping wants to query it, and a cache would answer with what the table said when the process
// started. The files are small and this endpoint is not on the message path. More importantly it keeps the file as the single source of
// truth - the same rule as channels themselves - so what a client is told here is what the running channel will do.
type channelTables struct {
	repo *api.ChannelRepo
	log  *slog.Logger
}

// Tables merges the tables from every channel that loads.
//
// A channel whose file will not parse contributes nothing and is not an error here: it is already reported at startup and in the
// console, and refusing to answer any terminology question because one unrelated channel has a typo would be a poor trade.
func (c *channelTables) Tables() map[string]*codeset.Table {
	out := map[string]*codeset.Table{}

	// The converter's own mappings first, always.
	//
	// Published even on an installation with no tables of its own, because they are the more useful half: what this engine does
	// with a v2 code by default was only discoverable by reading Go source, and it is the question asked most often about an
	// integration engine. They are prefixed, so a site's own table can never be shadowed by one of these or the other way round.
	for _, t := range v2fhir.BuiltinTables() {
		out[t.Name] = t
	}

	if c.repo == nil {
		return out
	}

	channels, err := loadedChannels(c.repo)
	if err != nil {
		if c.log != nil {
			c.log.Warn("could not read the channels to collect mapping tables", "error", err)
		}

		return out
	}

	// Sorted, so which channel is seen first does not depend on directory order. Without this, two channels defining the same table
	// differently would produce a different answer between restarts, which is the hardest kind of fault to be told about.
	sort.Slice(channels, func(i, j int) bool { return channels[i].Name < channels[j].Name })

	for _, ch := range channels {
		set := ch.CodeSets()
		if set == nil {
			continue
		}

		for _, name := range set.Names() {
			table, ok := set.Table(name)
			if !ok {
				continue
			}

			// Two channels naming the same table differently is a real conflict and is reported.
			//
			// Not resolved, because there is no correct resolution: one of the two mappings is being applied to somebody's
			// data and this cannot tell which was intended. Saying so is the only honest option, and it is worth saying
			// loudly - a sex code mapped one way in admissions and another in results is the kind of disagreement that
			// takes weeks to notice.
			if existing, seen := out[name]; seen && !sameTable(existing, table) {
				if c.log != nil {
					c.log.Warn("two channels define the same mapping table differently, so a translation depends "+
						"on which channel handled the message",
						"table", name, "channel", ch.Name)
				}

				continue
			}

			out[name] = table
		}
	}

	return out
}

// sameTable reports whether two tables say the same thing.
//
// Compares the parts that change an answer. The compiled index is derived and the provenance fields do not affect a translation, so
// two files describing the same mapping with different wording in decided_by are not a conflict.
func sameTable(a, b *codeset.Table) bool {
	if a.Default != b.Default || a.Strict != b.Strict {
		return false
	}

	return reflect.DeepEqual(a.Entries, b.Entries)
}
