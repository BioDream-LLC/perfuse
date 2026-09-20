package api

import (
	"net/http"
	"strconv"

	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
)

// What is actually arriving on a channel.
//
// GET /api/channels/{name}/profile reads recorded messages and reports the shape of the feed:
// which message types, which segments, how often each field is populated, which codes the sender
// really uses, and what repeats.
//
// Viewer is enough. The profile carries no message content by construction - counts, rates and
// shapes only, with coded values as the deliberate exception because a code table value is not
// identifying. Anybody who can already see that a channel exists and how many messages it has
// handled learns nothing new about a patient from this.

type profileResponse struct {
	*profile.Report

	// Available is how many recorded messages the channel has bodies for, so the reader can
	// judge the sample. A feed profiled from its last hundred messages during a quiet night
	// looks different from the same feed profiled over a week.
	Available int `json:"available"`

	// Suggestions say what each field appears to be for, with the counted evidence.
	//
	// Proposals and nothing more. Nothing here writes a channel: a confident wrong mapping in clinical data is
	// worse than no mapping, because somebody accepts it - so every suggestion carries the facts it was drawn
	// from and a person can disagree with the reasoning rather than only the conclusion.
	Suggestions []profile.Suggestion `json:"suggestions,omitempty"`

	// SuggestionsUnavailable says why there are none, when there are none.
	//
	// Its own field rather than an empty list. An empty list reads as "nothing to say about these fields", and the
	// real answer is usually "the sample is too small to say anything", which is a different statement and the
	// one somebody can act on.
	SuggestionsUnavailable string `json:"suggestionsUnavailable,omitempty"`
}

// handleChannelProfile profiles recorded traffic for one channel.
func (s *Server) handleChannelProfile(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}
	messageStore := rt.Messages

	// Default of a thousand: enough for the rates to mean something, small enough to answer
	// while somebody is looking at the screen. A profile is a thing people re-run.
	limit := 1000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			s.fail(w, r, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = n
	}

	messages, err := messageStore.RecentPayloads(r.Context(), name, limit)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(messages) == 0 {
		// A 409 rather than an empty profile. An empty report reads as "this feed is simple",
		// which is the opposite of "nothing has been recorded yet".
		s.fail(w, r, http.StatusConflict,
			"no messages have been recorded for this channel yet, so there is nothing to profile")
		return
	}

	available, err := messageStore.CountPayloads(r.Context(), name)
	if err != nil {
		available = len(messages)
	}

	// The channel's own data type decides which profiler reads the corpus.
	dataType := ""
	if repo, ok := s.channelsFor(w, r, sess); ok {
		if ch, err := repo.Get(name); err == nil && ch != nil {
			dataType = string(ch.DataType)
		}
	}

	report := profile.BuildFor(dataType, messages)
	suggestions, why := profile.Suggest(report, profile.SuggestOptions{})

	s.ok(w, profileResponse{
		Report:                 report,
		Available:              available,
		Suggestions:            suggestions,
		SuggestionsUnavailable: why,
	})
}

// handleSynthesise generates a channel YAML from observed traffic.
//
// POST rather than GET because it carries the channel name and generates something new each time depending on the traffic. Viewer is
// enough for the same reason as the profile itself: no message content, and the output is a hypothesis to be reviewed.
func (s *Server) handleSynthesise(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}
	messageStore := rt.Messages

	limit := 1000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			s.fail(w, r, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = n
	}

	messages, err := messageStore.RecentPayloads(r.Context(), name, limit)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if len(messages) == 0 {
		s.fail(w, r, http.StatusConflict,
			"no messages have been recorded for this channel yet, so there is nothing to synthesise from")
		return
	}

	dataType := ""
	if repo, ok := s.channelsFor(w, r, sess); ok {
		if ch, err := repo.Get(name); err == nil && ch != nil {
			dataType = string(ch.DataType)
		}
	}

	report := profile.BuildFor(dataType, messages)
	suggestions, _ := profile.Suggest(report, profile.SuggestOptions{})

	channelName := name + "-synthesised"
	yaml := profile.Synthesise(report, suggestions, profile.SynthesisOptions{
		ChannelName: channelName,
	})

	s.ok(w, map[string]any{"yaml": yaml, "channel": channelName, "messages": report.Messages})
}
