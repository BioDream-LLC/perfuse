package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/store"
	"gopkg.in/yaml.v3"
)

// Writing shared mapping tables from the interface.
//
// These were readable and not writable. The Tables section reported which channels each table affected and said
// "No shared tables yet" on a fresh installation, with nothing to press - so creating one meant writing YAML on
// the server by hand, which is the thing this product exists to stop people doing.
//
// Mapping tables are also the place where provenance matters most. The section's own text says it: most of what
// makes an interface hard to maintain is that nobody knows why a mapping is the way it is, and the person who
// knew has left. So who decided and why are part of the form rather than optional extras, and an entry without a
// reason is allowed but noticed.

// tableWriteRequest is a table being created or replaced.
type tableWriteRequest struct {
	// Name is how channels refer to this table.
	Name string `json:"name"`

	// Describes says what the mapping is for, in words.
	Describes string `json:"describes"`

	// Entries are the mappings themselves.
	Entries []codeset.Entry `json:"entries"`

	// Default is what an unmatched value becomes. Empty means leave it alone.
	Default string `json:"default,omitempty"`

	// Strict refuses a value the table does not know rather than passing it through.
	Strict bool `json:"strict,omitempty"`

	// DecidedBy and Source record where this mapping came from.
	DecidedBy string `json:"decidedBy,omitempty"`
	Source    string `json:"source,omitempty"`

	// File is which table file to write to. Empty means the shared one beside the channels.
	File string `json:"file,omitempty"`
}

// tableWriteResponse reports what was written.
type tableWriteResponse struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Entries int      `json:"entries"`
	Notes   []string `json:"notes"`
}

// defaultTableFile is where a table goes when nobody says otherwise.
//
// The .codeset.yaml suffix is the existing convention: the channel loader treats it as a companion file so it is
// never mistaken for a channel definition, and the documentation uses it throughout. Writing to tables.yaml
// instead would produce a file the loader would try to read as a channel.
const defaultTableFile = "shared.codeset.yaml"

// handleWriteTable creates or replaces one shared mapping table.
func (s *Server) handleWriteTable(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req tableWriteRequest
	if !s.decode(w, r, &req) {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		s.fail(w, r, http.StatusBadRequest, "a table needs a name, because channels refer to it by name")
		return
	}
	if strings.TrimSpace(req.Describes) == "" {
		// Required rather than optional, and this is a deliberate friction.
		//
		// A table called "codes" with no description is exactly the artefact the section warns about: nobody
		// knows what it is for, so nobody dares change it and nobody dares delete it. One sentence now saves
		// somebody an afternoon in two years.
		s.fail(w, r, http.StatusBadRequest,
			"say what this table is for in a sentence; a mapping nobody can explain becomes one nobody dares change")
		return
	}
	if len(req.Entries) == 0 {
		s.fail(w, r, http.StatusBadRequest, "a table with no entries maps nothing")
		return
	}

	// Entries are checked for the mistakes that are silent at runtime.
	seen := map[string]int{}
	for i, e := range req.Entries {
		if strings.TrimSpace(e.From) == "" {
			s.fail(w, r, http.StatusBadRequest,
				fmt.Sprintf("entry %d has nothing to map from", i+1))
			return
		}
		// A duplicate "from" is the one that hurts: whichever wins is an implementation detail, and the value
		// that loses will look like it was never mapped at all.
		if prev, dup := seen[e.From]; dup {
			s.fail(w, r, http.StatusBadRequest,
				fmt.Sprintf("%q is mapped twice, in entries %d and %d; one of them would be silently ignored",
					e.From, prev+1, i+1))
			return
		}
		seen[e.From] = i
	}

	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	file := strings.TrimSpace(req.File)
	if file == "" {
		file = defaultTableFile
	}

	table := codeset.Table{
		Name:      req.Name,
		Describes: strings.TrimSpace(req.Describes),
		Entries:   req.Entries,
		Default:   req.Default,
		Strict:    req.Strict,
		DecidedBy: strings.TrimSpace(req.DecidedBy),
		Source:    strings.TrimSpace(req.Source),
		DecidedOn: time.Now().UTC().Format("2006-01-02"),
	}
	if table.DecidedBy == "" {
		// Falls back to whoever is signed in, because "decided by nobody" is worse than a name that turns out
		// to be the wrong one. It can be edited.
		table.DecidedBy = sess.Username
	}

	existing, err := repo.ReadTables(file)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	notes := []string{}
	replaced := false
	for i := range existing.Tables {
		if strings.EqualFold(existing.Tables[i].Name, req.Name) {
			notes = append(notes, fmt.Sprintf(
				"Replaced the existing %q, which had %d entr%s.",
				req.Name, len(existing.Tables[i].Entries),
				plural(len(existing.Tables[i].Entries), "y", "ies")))
			existing.Tables[i] = table
			replaced = true
			break
		}
	}
	if !replaced {
		existing.Tables = append(existing.Tables, table)
	}

	raw, err := yaml.Marshal(existing)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	if err := repo.WriteTables(file, raw); err != nil {
		s.failErr(w, r, err)
		return
	}

	// Said rather than left to be discovered. A table nothing references does nothing, and somebody who has
	// just carefully entered thirty mappings deserves to know that before they go looking for the effect.
	notes = append(notes, fmt.Sprintf(
		"Nothing uses this yet. Add a map step to a channel with table: %s to put it to work.", req.Name))

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "table.write",
		Target: req.Name,
		Detail: fmt.Sprintf("%d entries in %s", len(req.Entries), file),
		IP:     clientIP(r),
	})

	s.ok(w, tableWriteResponse{
		Name:    table.Name,
		File:    file,
		Entries: len(table.Entries),
		Notes:   notes,
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
