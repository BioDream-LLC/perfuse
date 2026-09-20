package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
)

// tableUse is one place a shared mapping table is actually used.
type tableUse struct {
	Channel string `json:"channel"`

	// Step describes where in the channel, so somebody can find it rather than reading the whole file. The step's
	// own description when it has one, otherwise the path it maps.
	Step string `json:"step"`

	// Path is the field being mapped, which is what makes an impact concrete: "this changes PID-8 on six channels"
	// is actionable in a way "six channels are affected" is not.
	Path string `json:"path"`

	// Running reports whether the channel is running now. Editing a table under a running channel takes effect on
	// the next message, so this is the difference between a change and a change in production.
	Running bool `json:"running"`
}

// tableImpact is one table and everything it touches.
type tableImpact struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Describes string `json:"describes,omitempty"`

	// Provenance, carried through so the impact view can show who decided this and why. The whole reason the
	// codeset format records it is that a mapping nobody can explain becomes one nobody dares change.
	DecidedBy string `json:"decidedBy,omitempty"`
	DecidedOn string `json:"decidedOn,omitempty"`
	Source    string `json:"source,omitempty"`

	Entries int `json:"entries"`

	// UsedBy is every use, sorted. Empty means the table is loaded and referenced by nobody, which is worth showing
	// rather than hiding - an unused table is either a mistake or dead weight, and both are worth knowing.
	UsedBy []tableUse `json:"usedBy"`

	// Channels and Running are counts over UsedBy, because the headline question is "how many channels" and making
	// the interface count an array is how a number ends up disagreeing with the list beneath it.
	Channels int `json:"channels"`
	Running  int `json:"running"`

	// Paths is every distinct field this table writes to, sorted. A table used on one field is a much smaller
	// change than the same table used on five.
	Paths []string `json:"paths"`
}

type codesetsResponse struct {
	Tables []tableImpact `json:"tables"`

	// Unreferenced counts tables loaded by some channel but used by none, so the interface can say so once rather
	// than leaving somebody to notice a lot of empty lists.
	Unreferenced int `json:"unreferenced"`
}

// handleCodesets lists shared mapping tables and what each one affects.
//
// The point of this endpoint is the blast radius. A shared table is shared precisely so that one edit reaches every
// channel that uses it, which is the feature and also the hazard: without this, changing a table is an edit whose
// consequences are invisible until messages start arriving translated differently.
func (s *Server) handleCodesets(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	// Optional: this endpoint describes channel files, and a server without an engine can still answer it. The
	// non-writing accessor, because runtimeFor would emit an error body and this handler would then write a success
	// body after it.
	rt := s.optionalRuntime(sess)

	summaries, _, err := repo.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// Keyed by table name and the file it came from, because two files may legitimately define tables with the same
	// name for different channels and merging them would report an impact that does not exist.
	type key struct{ name, file string }
	impacts := map[key]*tableImpact{}

	for _, summary := range summaries {
		cfg, err := repo.Get(summary.Name)
		if err != nil || cfg == nil {
			continue
		}
		running := rt != nil && rt.IsRunning(cfg.Name)

		tables := cfg.CodeSets()
		if tables == nil {
			continue
		}

		// Every table this channel loads, whether or not it uses them, so a loaded-but-unused table is visible.
		for _, name := range tables.Names() {
			table, found := tables.Table(name)
			if !found {
				continue
			}
			k := key{name: name, file: cfg.TableFileFor(name)}
			if _, exists := impacts[k]; !exists {
				impacts[k] = &tableImpact{
					Name:      name,
					File:      k.file,
					Describes: table.Describes,
					DecidedBy: table.DecidedBy,
					DecidedOn: table.DecidedOn,
					Source:    table.Source,
					Entries:   len(table.Entries),

					// Both initialised, because a nil slice marshals as JSON null and the interface reads
					// .length on them.
					//
					// A table loaded by a channel and used by none leaves both empty, which this endpoint
					// deliberately shows - an unused table is either a mistake or dead weight and both are
					// worth knowing. It blanked the entire console instead. Not the panel: the whole
					// application, because the exception is thrown during a render.
					//
					// The earlier sweep for this fault checked the arrays at the top of each response and
					// missed these, which sit inside the elements of one.
					UsedBy: []tableUse{},
					Paths:  []string{},
				}
			}
		}

		// Then the uses.
		for _, step := range cfg.Transformations {
			if step.Map == nil {
				continue
			}
			use := strings.TrimSpace(step.Map.Use)
			if use == "" {
				continue
			}

			k := key{name: use, file: cfg.TableFileFor(use)}
			impact, exists := impacts[k]
			if !exists {
				// A reference to a table that did not load would already have been refused at load, so this is
				// defensive rather than expected.
				continue
			}

			label := strings.TrimSpace(step.Description)
			if label == "" {
				label = "maps " + step.Map.Path
			}

			impact.UsedBy = append(impact.UsedBy, tableUse{
				Channel: cfg.Name,
				Step:    label,
				Path:    step.Map.Path,
				Running: running,
			})
		}
	}

	// Tables initialised rather than left nil. An install with no code sets is the first thing a new user sees, and a nil
	// slice marshals as null - which made this tab render completely blank. See apinull_test.go.
	// Tables that exist on disk but no channel references yet.
	//
	// Everything above discovers tables through the channels that load them, which meant a table nobody
	// references was invisible here - including one that had just been created. Somebody would write a table,
	// return to this section, and find it saying "no shared tables yet" about the file they had just made.
	//
	// Worth showing for its own sake too: an unreferenced table is either a mistake or dead weight, and both
	// are things to know about rather than things to hide.
	for _, file := range repo.TableFiles() {
		set, err := repo.ReadTables(file)
		if err != nil {
			// A file that cannot be parsed is reported by the channels that fail to load because of it. Skipped
			// rather than failing the whole listing, which would hide every healthy table.
			continue
		}
		for _, table := range set.Tables {
			k := key{name: table.Name, file: file}
			if _, exists := impacts[k]; exists {
				continue
			}
			impacts[k] = &tableImpact{
				Name:      table.Name,
				File:      file,
				Describes: table.Describes,
				DecidedBy: table.DecidedBy,
				DecidedOn: table.DecidedOn,
				Source:    table.Source,
				Entries:   len(table.Entries),
				UsedBy:    []tableUse{},
				Paths:     []string{},
			}
		}
	}

	resp := codesetsResponse{Tables: []tableImpact{}}
	for _, impact := range impacts {
		// Sorted, because Go maps range randomly and this output gets read side by side with the previous version.
		sort.SliceStable(impact.UsedBy, func(i, j int) bool {
			if impact.UsedBy[i].Channel != impact.UsedBy[j].Channel {
				return impact.UsedBy[i].Channel < impact.UsedBy[j].Channel
			}
			return impact.UsedBy[i].Path < impact.UsedBy[j].Path
		})

		seenChannel := map[string]bool{}
		seenPath := map[string]bool{}
		for _, use := range impact.UsedBy {
			if !seenChannel[use.Channel] {
				seenChannel[use.Channel] = true
				impact.Channels++
				if use.Running {
					impact.Running++
				}
			}
			if !seenPath[use.Path] {
				seenPath[use.Path] = true
				impact.Paths = append(impact.Paths, use.Path)
			}
		}
		sort.Strings(impact.Paths)

		if len(impact.UsedBy) == 0 {
			resp.Unreferenced++
		}

		resp.Tables = append(resp.Tables, *impact)
	}

	// Widest impact first. A table on twelve channels is the one somebody needs to think hardest about before
	// editing, and sorting by name would bury it.
	sort.SliceStable(resp.Tables, func(i, j int) bool {
		a, b := resp.Tables[i], resp.Tables[j]
		if a.Channels != b.Channels {
			return a.Channels > b.Channels
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.File < b.File
	})

	s.ok(w, resp)
}
