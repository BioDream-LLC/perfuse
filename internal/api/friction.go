package api

import (
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/engine"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The friction report: what using this has actually cost somebody.
//
// This replaces a queue item that asked for one real operator to be handed the binary and watched. That item stood at the top of the
// queue for three days and was never going to happen, because the only people who have ever run this software are the person who
// commissioned it and the program that wrote it.
//
// The item's purpose survives it. Every claim in this repository about ease of use rests on the author's judgement of his own work,
// which is the same circularity that let a thousand self-agreeing SAML tests pass while no real identity provider could sign anybody
// in. What was needed was evidence from outside that loop, and a refusal is exactly that: somebody wanted something, the server said
// no, and neither party was guessing.
//
// So the report leads with refusals rather than with achievements. A first-run summary showing how quickly a channel was built would
// be this software marking its own homework; a list of the sentences that stopped somebody is the opposite.

// FrictionReport is what the report endpoint returns.
type FrictionReport struct {
	// Refusals are the messages hit most often, worst first. The point of the whole report.
	Refusals []store.FrictionGroup `json:"refusals"`

	// Total is every refusal recorded, so the list above can be read as a sample of something rather than as everything.
	Total int `json:"total"`

	// Funnel is how far the installation has got. Derived from records that already existed rather than newly written, because a
	// milestone this code records about itself is worth less than one it has to go and find.
	Funnel FrictionFunnel `json:"funnel"`
}

// FrictionFunnel is the sequence a new installation has to get through before Perfuse does anything useful.
//
// Each step is a fact looked up elsewhere: the audit trail for signing in and creating a channel, the message store for traffic. If a
// step has not happened, that is the answer - an installation stuck at "a channel exists but no message has ever arrived" is the most
// interesting state this report can show, and it is invisible from inside.
type FrictionFunnel struct {
	FirstSignIn  *time.Time `json:"firstSignIn,omitempty"`
	FirstChannel *time.Time `json:"firstChannel,omitempty"`
	FirstMessage *time.Time `json:"firstMessage,omitempty"`

	ChannelCount int   `json:"channelCount"`
	MessageCount int64 `json:"messageCount"`

	// Delivered is how many messages reached a destination. The number that separates a channel somebody configured from one that
	// does the job: a channel can receive for weeks and deliver nothing, and from inside the interface that looks like traffic.
	Delivered int64 `json:"delivered"`

	// StuckAt names the first step that has not happened, rather than leaving a reader to work it out from four timestamps. The whole
	// value of the funnel is in saying where somebody stopped, and a report that needs interpreting gets interpreted by whoever wrote
	// the software.
	StuckAt string `json:"stuckAt"`

	// MinutesToFirstMessage is from being able to sign in to a message actually arriving. The single most useful number here, and the
	// one a person watching over somebody's shoulder would have been timing.
	MinutesToFirstMessage *float64 `json:"minutesToFirstMessage,omitempty"`
}

// handleFrictionReport answers with the report.
func (s *Server) handleFrictionReport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if r.Method != http.MethodGet {
		s.fail(w, r, http.StatusMethodNotAllowed, "only GET is supported here")

		return
	}

	// The tenant's runtime, through the accessor that does not write a refusal when there is no engine.
	//
	// Not runtimeFor(): it answers the request itself when the engine is missing, and an installation with no engine yet is precisely
	// the early state this report exists to describe. Reporting nothing would be the wrong answer to the most interesting case.
	rt := s.optionalRuntime(sess)

	report := FrictionReport{
		// Empty rather than nil, so a caller does not have to handle two shapes of nothing.
		Refusals: []store.FrictionGroup{},
	}

	refusals, err := s.Store.TopFriction(r.Context(), 25)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	report.Refusals = refusals

	total, err := s.Store.FrictionCount(r.Context())
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	report.Total = total
	report.Funnel = s.buildFunnel(r, rt)

	// Trimmed here rather than on a schedule.
	//
	// Refusals are 4xx responses, so the table grows slowly, and reading the report is a natural moment to tidy: it needs no timer, no
	// goroutine and nothing that runs when nobody is looking. The cap is generous enough that the counts stay meaningful - the report
	// groups by message, and trimming the oldest rows lowers a count rather than losing a finding.
	if err := s.Store.TrimFriction(r.Context(), 5_000); err != nil {
		s.log().Debug("could not trim the friction table", "err", err)
	}

	s.writeJSON(w, http.StatusOK, report)
}

// buildFunnel works out how far this installation has got.
//
// Every value is looked up from something recorded for another purpose - the audit trail, the channel files, the message store. Nothing
// here writes a milestone about itself, because a milestone this code records to flatter its own funnel is worth nothing, and the point
// of the whole report is evidence that did not come from the author.
func (s *Server) buildFunnel(r *http.Request, rt *Runtime) FrictionFunnel {
	funnel := FrictionFunnel{}

	if at, err := s.Store.FirstAuditAt(r.Context(), "login"); err == nil && at != nil {
		funnel.FirstSignIn = at
	}

	// Two actions, because creating a channel through the builder and saving one over the API are the same milestone to an operator
	// and different words in the audit trail. Missing one would report an installation as stuck before its first channel while it was
	// running three.
	for _, action := range []string{"channel created", "channel saved"} {
		at, err := s.Store.FirstAuditAt(r.Context(), action)
		if err != nil || at == nil {
			continue
		}

		if funnel.FirstChannel == nil || at.Before(*funnel.FirstChannel) {
			funnel.FirstChannel = at
		}
	}

	// The channels that exist now, rather than a count from the audit trail: one created and then deleted should not make the
	// installation look further along than it is. Broken ones count, because a channel that will not load is still one somebody tried
	// to build - and it is a more interesting state than a working one.
	if s.Channels != nil {
		if valid, broken, err := s.Channels.List(); err == nil {
			funnel.ChannelCount = len(valid) + len(broken)
		}
	}

	if rt != nil && rt.Messages != nil {
		// From the beginning of time rather than a recent window, because this is a question about the installation's whole history.
		if stats, err := rt.Messages.Stats(r.Context(), store.DefaultTenant, time.Time{}); err == nil && stats != nil {
			funnel.MessageCount = stats.Total
			funnel.FirstMessage = stats.OldestKept

			// Delivered and partially delivered both count: a message that reached one destination of two has left the building, and
			// calling that undelivered would report an installation as stuck when it is working.
			funnel.Delivered = stats.ByOutcome[string(engine.Delivered)] + stats.ByOutcome[string(engine.PartiallyDelivered)]
		}
	}

	if funnel.FirstSignIn != nil && funnel.FirstMessage != nil {
		minutes := funnel.FirstMessage.Sub(*funnel.FirstSignIn).Minutes()
		funnel.MinutesToFirstMessage = &minutes
	}

	funnel.StuckAt = stuckAt(funnel)

	return funnel
}

// stuckAt names the first step that has not happened.
//
// Named rather than left for a reader to work out from four timestamps, because the whole value of the funnel is in saying where
// somebody stopped. A report that requires interpretation gets interpreted by whoever wrote the software.
func stuckAt(f FrictionFunnel) string {
	switch {
	case f.FirstSignIn == nil:
		return "nobody has signed in"
	case f.ChannelCount == 0 && f.FirstChannel == nil:
		return "signed in, but no channel has ever been created"
	case f.ChannelCount == 0:
		return "a channel was created and none exists now, so it was deleted or would not load"
	case f.FirstMessage == nil:
		return "a channel exists and no message has ever arrived"
	case f.Delivered == 0:
		return "messages arrive and none has been delivered to a destination"
	default:
		return "nothing: messages are received and delivered"
	}
}
