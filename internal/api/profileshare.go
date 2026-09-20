package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Exporting and importing a dialect profile.
//
// # Why this was missing
//
// internal/profile/share.go has held ExportProfile, MarshalProfile and UnmarshalProfile for some time, with a versioned format and a
// SharedProfile type carrying everything a profile needs to travel. Nothing called any of them. Recipes had endpoints; profiles had
// the machinery and no way to reach it.
//
// That is the shape already recorded in this project's own defect list: an implementation with no caller is indistinguishable from a
// feature that does not exist. The queue described this as unbuilt and open, when what was missing was two handlers.
//
// # Why it is safe to share, and why that is asserted rather than assumed
//
// A profile is statistical: fill rates, lengths, shapes, distinct counts, and the codes actually used. Values are kept only for fields
// the dictionary says are table-constrained, because which codes a sender really uses is the most useful single fact in a profile and a
// code table value is not identifying. Identifier values are never listed, and there is a guard proving it rather than a comment
// claiming it.
//
// The whole point of a profile is that it gets pasted into tickets, emails and now files that leave the building. So the export
// re-checks the property at the boundary rather than trusting that the profile was built correctly: a profile assembled by some future
// path that did include values would otherwise become the first thing to walk out of a hospital.

// exportProfileRequest names a channel to profile and the metadata that makes the result worth sharing.
//
// The profile is built here rather than accepted from the caller, and that is the important part. The browser has a profile on screen
// already, so sending it would have been the shorter path - but it would mean the one artefact in this system that must not carry
// patient data was assembled by whatever asked for it. Building it server-side from the stored messages means the guarantee rests on
// the profiler, which has a guard proving it, rather than on a request body.
type exportProfileRequest struct {
	// Channel is the feed to profile.
	Channel string `json:"channel"`

	// Limit is how many recent messages to read. Zero means the same default as the profile screen.
	Limit int `json:"limit"`

	Name        string `json:"name"`
	Description string `json:"description"`

	// Source names the sending system - "Epic 2023", "Cerner Millennium" - which is the field that makes a shared profile
	// findable by somebody with the same problem. Free text on purpose: a fixed list would be wrong within a quarter.
	Source string `json:"source"`

	// DataType is not taken from the request. The channel knows its own format, and a mismatch between the two would produce a
	// profile labelled as one format and built by the profiler for another.
	Tags []string `json:"tags"`
}

// handleProfileExport turns a profile into a file that can be shared.
func (s *Server) handleProfileExport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req exportProfileRequest
	if !s.decode(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.fail(w, r, http.StatusBadRequest,
			"a shared profile needs a name, because the point of sharing it is that somebody else can find it")

		return
	}

	if strings.TrimSpace(req.Source) == "" {
		// Refused rather than defaulted. A profile whose sending system is unknown is nearly useless to a stranger - the
		// question they are asking is "has anybody mapped this system before" - and an empty field invites "Unknown" as the
		// most common answer in a shared library.
		s.fail(w, r, http.StatusBadRequest,
			"a shared profile needs to say which system produced the feed, or nobody else can tell whether it applies to them")

		return
	}

	report, dataType, ok := s.profileForSharing(w, r, sess, strings.TrimSpace(req.Channel), req.Limit)
	if !ok {
		return
	}

	if report.Messages == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"no messages have been recorded for this channel, so a profile of it would describe nothing")

		return
	}

	shared := profile.ExportProfile(report, name, req.Description, req.Source, dataType, req.Tags)

	// Checked at the boundary, not trusted from upstream.
	//
	// This is the last point before the data leaves the building, and it is the only point where being wrong is unrecoverable -
	// a file is gone once it is sent. The profiler is careful today; this stays correct if some future path is not.
	if leaked := identifyingFields(shared); len(leaked) > 0 {
		s.fail(w, r, http.StatusInternalServerError,
			"this profile lists values for fields that are not code tables, so it will not be exported", leaked...)

		return
	}

	data, err := profile.MarshalProfile(shared)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+".profile.json\"")
	_, _ = w.Write(data)
}

// handleProfileImport reads a shared profile back.
func (s *Server) handleProfileImport(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var raw json.RawMessage
	if !s.decode(w, r, &raw) {
		return
	}

	shared, err := profile.UnmarshalProfile(raw)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest,
			"that is not a profile this can read: "+err.Error())

		return
	}

	// An imported profile is checked too, and for a different reason: it arrived from outside, so nobody here knows how it was
	// built. Displaying somebody else's patient identifiers because they exported carelessly would be this instance's disclosure.
	if leaked := identifyingFields(shared); len(leaked) > 0 {
		s.fail(w, r, http.StatusBadRequest,
			"this profile lists values for fields that are not code tables, so it has not been loaded. Whoever produced it "+
				"should be told.", leaked...)

		return
	}

	s.ok(w, map[string]any{
		"profile": shared,

		// Said explicitly because an import that only reports success invites the belief that something was applied. Nothing is:
		// a profile describes a feed, and what to do about it is a separate decision somebody makes with it open beside them.
		"note": "This profile has been read, not applied. Nothing on this server changed.",
	})
}

// identifyingFields returns paths that list values without being table-constrained.
//
// The condition is deliberately the narrow one. A field with a table number is allowed its values, because that is what a code table
// is. Anything else listing values is either a bug in whatever produced the profile or a field the dictionary does not constrain, and
// in both cases the values could be identifiers.
func identifyingFields(p *profile.SharedProfile) []string {
	var bad []string

	for _, seg := range p.Segments {
		for _, f := range seg.Fields {
			if len(f.Codes) > 0 && strings.TrimSpace(f.Table) == "" {
				bad = append(bad, f.Path+" lists "+strconv.Itoa(len(f.Codes))+" value(s) and is not a code table field")
			}
		}
	}

	return bad
}

// profileForSharing builds a profile of a channel's recent traffic.
//
// A narrow copy of what the profile screen does, deliberately not refactored into a shared helper yet. The two want different failure
// behaviour: the screen returns a conflict with an explanation for an empty feed, and this returns a refusal about sharing. Merging
// them would mean one of the two messages became generic, and the message is most of the value in both.
func (s *Server) profileForSharing(
	w http.ResponseWriter, r *http.Request, sess *store.Session, channel string, limit int,
) (*profile.Report, string, bool) {
	if channel == "" {
		s.fail(w, r, http.StatusBadRequest, "which channel's feed should be profiled?")

		return nil, "", false
	}

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return nil, "", false
	}

	if !s.requireMessages(w, r) {
		return nil, "", false
	}

	if limit <= 0 {
		// The same default as the profile screen, so a shared profile describes what the person sharing it was looking at.
		limit = 1000
	}

	messages, err := rt.Messages.RecentPayloads(r.Context(), channel, limit)
	if err != nil {
		s.failErr(w, r, err)

		return nil, "", false
	}

	dataType := ""
	if repo, ok := s.channelsFor(w, r, sess); ok {
		if ch, err := repo.Get(channel); err == nil && ch != nil {
			dataType = string(ch.DataType)
		}
	}

	return profile.BuildFor(dataType, messages), dataType, true
}
