package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// Reporting contract state to the interface.
//
// Separate from the alert list on purpose. An alert says "something changed and you should look"; this says
// "here is what this feed is expected to look like and whether it currently does". Somebody arrives here
// because they want to understand a feed, not because they were paged, and those are different needs.
//
// It also has to show the state when nothing is wrong. A page that only appears when there is a problem cannot
// be used to answer "is this feed being watched at all", which is the question an auditor asks.

type contractStatus struct {
	Channel string `json:"channel"`

	// Judged is false when there were too few messages. Reported rather than shown as "passing", because
	// "we have no evidence" must not look like "all well".
	Judged bool `json:"judged"`

	// Holding is true when every expectation held.
	Holding bool `json:"holding"`

	Violations int    `json:"violations"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail,omitempty"`

	// CheckedAt is when this was produced, and StaleSeconds how long ago.
	//
	// Both, because a timestamp alone makes the reader do arithmetic, and a duration alone hides that the check
	// stopped running entirely. A verdict with no age invites trusting a check that died a week ago.
	CheckedAt    time.Time `json:"checkedAt,omitzero"`
	StaleSeconds float64   `json:"staleSeconds"`

	// Expectations is how many are being checked, so a holding result says what it proved.
	Expectations int `json:"expectations"`

	// File is the contract this came from, so somebody can go and read it.
	File string `json:"file,omitempty"`

	// Trend is the recent verdict history. A current state cannot answer "when did this start", which is the first
	// question anybody asks after "what changed".
	Trend Trend `json:"trend"`
}

type contractListResponse struct {
	// Contracts are ordered worst first: violations, then unjudged, then holding. A list sorted by name makes
	// somebody read all of it to find the one that matters.
	Contracts []contractStatus `json:"contracts"`

	// Watching is how many channels have a contract at all, which is the honest headline. Nine channels
	// watched out of forty is a very different situation from nine out of nine.
	Watching int `json:"watching"`

	// Total is how many channels exist.
	Total int `json:"total"`

	// Without names the channels that have no contract, so one can be offered for them.
	//
	// A count alone said thirty-one channels were unwatched and gave no way to act on it, which made this
	// section a report rather than a place to do anything. Never null: an empty list means every channel is
	// watched, and a null would crash the list that renders it.
	Without []string `json:"without"`
}

// handleContracts lists every channel's contract state.
func (s *Server) handleContracts(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt := s.optionalRuntime(sess)

	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	summaries, _, err := channels.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	resp := contractListResponse{
		Contracts: []contractStatus{},
		Total:     len(summaries),
		Without:   []string{},
	}

	for _, summary := range summaries {
		cfg, err := channels.Get(summary.Name)
		if err != nil {
			continue
		}
		if cfg.Contract == nil || cfg.Contract.Contract() == nil {
			resp.Without = append(resp.Without, summary.Name)
			continue
		}

		resp.Watching++

		status := contractStatus{
			Channel:      summary.Name,
			File:         cfg.Contract.File,
			Expectations: len(cfg.Contract.Contract().Expectations),
			Summary: "not checked yet; the first check runs shortly after the channel starts " +
				"receiving messages",
		}

		if rt != nil && rt.Contracts != nil {
			if state, found := rt.Contracts.State(summary.Name); found {
				status.Judged = state.Judged
				status.Violations = state.Violations
				status.Summary = state.Summary
				status.Detail = state.Detail
				status.CheckedAt = state.CheckedAt
				status.StaleSeconds = time.Since(state.CheckedAt).Seconds()
				status.Holding = state.Judged && state.Violations == 0
			}
			status.Trend = rt.Contracts.Trend(summary.Name)
		}

		resp.Contracts = append(resp.Contracts, status)
	}

	// Worst first. Violations, then not-judged, then holding - because not-judged is a real state somebody
	// needs to act on (usually by lowering min_messages or waiting), and burying it under passing channels
	// means it never gets acted on.
	sort.SliceStable(resp.Contracts, func(i, j int) bool {
		a, b := resp.Contracts[i], resp.Contracts[j]
		if (a.Violations > 0) != (b.Violations > 0) {
			return a.Violations > 0
		}
		if a.Violations != b.Violations {
			return a.Violations > b.Violations
		}
		if a.Judged != b.Judged {
			return !a.Judged
		}
		return a.Channel < b.Channel
	})

	s.ok(w, resp)
}
