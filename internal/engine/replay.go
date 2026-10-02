package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/shadow"
)

// Answering "what would this change have done?" against traffic that already happened.
//
// This is the thing every integration engineer wants and no tool provides. Shadowing tells you
// what a candidate does to messages arriving from now on, which means learning that a change is
// safe takes as long as it takes for the interesting messages to turn up - and the awkward ones
// are rare by definition. Running the candidate against messages already in the store answers
// the same question immediately, using the traffic that actually occurs on this feed rather
// than fixtures somebody invented.
//
// The output is deliberately a count and a list of examples rather than a verdict. "This changes
// 12 of your last 5,000 messages, here they are" is something a person can check. "Safe" is a
// claim this cannot support, because whether a difference is wanted is exactly the judgement
// that cannot be automated.

// ReplayDifference is one message that the two versions disagree about.
type ReplayDifference struct {
	// ControlID and MessageType identify the message so it can be found again in the store.
	ControlID   string `json:"controlId"`
	MessageType string `json:"messageType"`

	// Fields are the paths that differ. Empty with Verdict set means the disagreement is
	// about whether to accept the message at all, which no field diff can express.
	Fields []shadow.FieldDifference `json:"fields,omitempty"`

	// Verdict describes a difference in acceptance rather than content.
	Verdict string `json:"verdict,omitempty"`
}

// ReplayReport is what a retroactive run produces.
type ReplayReport struct {
	// Examined is how many stored messages were run through both versions.
	Examined int `json:"examined"`
	// Identical is how many came out the same.
	Identical int `json:"identical"`
	// Changed is how many differ. This is the number that matters.
	Changed int `json:"changed"`
	// Unparseable counts stored messages neither version could read. Reported rather than
	// hidden: a store full of them means the sample is not representative, and a run that
	// silently skipped them would look reassuring for the wrong reason.
	Unparseable int `json:"unparseable"`

	// Differences are examples, bounded. A candidate that rewrites every message would
	// otherwise produce a report too long to read, which buries the one case somebody
	// needed to see.
	Differences []ReplayDifference `json:"differences"`
	// Truncated says the example list was cut short.
	Truncated bool `json:"truncated"`

	// FieldCounts is how many messages each differing path accounts for. This is usually
	// the most useful part: one path against every message is a deliberate change, and one
	// path against three messages out of five thousand is the edge case worth looking at.
	FieldCounts map[string]int `json:"fieldCounts,omitempty"`
}

// maxReplayExamples bounds the example list.
//
// Fifty is enough to see a pattern and few enough to read. The counts cover the rest, which is
// what somebody actually reasons about: it is the shape of the change that matters, not every
// instance of it.
const maxReplayExamples = 50

// Replay runs stored messages through two versions of a channel and reports where they differ.
//
// Neither version can deliver anything. Both are built with newShadowChannel, so there is no
// sender object in existence for either - "this cannot reach a receiver" is a fact about what
// is in memory rather than a promise about what a function does. That property is what makes it
// safe to run a candidate against real production traffic.
func Replay(
	ctx context.Context,
	current, candidate *config.Channel,
	messages [][]byte,
	opts shadow.DiffOptions,
	log *slog.Logger,
) (*ReplayReport, error) {
	if log == nil {
		log = slog.Default()
	}

	live, err := newShadowChannel(current, log)
	if err != nil {
		return nil, fmt.Errorf("preparing the current version: %w", err)
	}
	next, err := newShadowChannel(candidate, log)
	if err != nil {
		return nil, fmt.Errorf("preparing the candidate: %w", err)
	}

	// Differences starts empty rather than nil so it serialises as [], not null. The case that leaves
	// it empty is a replay that found nothing wrong, which is the outcome somebody running one is
	// hoping for.
	report := &ReplayReport{FieldCounts: map[string]int{}, Differences: []ReplayDifference{}}

	for _, raw := range messages {
		// Checked per message rather than only at the top: a replay over fifty thousand
		// messages is long enough that somebody will navigate away, and finishing the work
		// after they have gone is waste.
		if err := ctx.Err(); err != nil {
			return report, err
		}

		liveOut, liveErr := live.TransformOnly(ctx, raw)
		nextOut, nextErr := next.TransformOnly(ctx, raw)

		// A message neither version can read is not a difference between them. Counted so
		// the report says the sample was imperfect rather than implying it was clean.
		if liveErr != nil && nextErr != nil {
			report.Unparseable++
			continue
		}

		report.Examined++

		// One version failing where the other did not is the most important kind of
		// difference and the easiest to under-report, because there is no field to compare.
		if (liveErr == nil) != (nextErr == nil) {
			report.Changed++
			report.add(raw, ReplayDifference{Verdict: describeErrorSplit(liveErr, nextErr)})
			continue
		}

		if liveOut.Accepted != nextOut.Accepted {
			report.Changed++
			report.add(raw, ReplayDifference{
				Verdict: describeAcceptance(liveOut, nextOut),
			})
			continue
		}

		// Both declined it. Whether they declined for the same stated reason is worth
		// reporting, because a filter rewritten to reject for a different reason is a change
		// somebody may not have intended even though the outcome matches today.
		if !liveOut.Accepted {
			if liveOut.RejectedBy != nextOut.RejectedBy {
				report.Changed++
				report.add(raw, ReplayDifference{
					Verdict: fmt.Sprintf(
						"both versions exclude this message, but for different reasons: %s and %s",
						liveOut.RejectedBy, nextOut.RejectedBy),
				})
				continue
			}
			report.Identical++
			continue
		}

		fields := diffFor(current, liveOut.Message, nextOut.Message, opts)
		if len(fields) == 0 {
			report.Identical++
			continue
		}

		report.Changed++
		for _, f := range fields {
			report.FieldCounts[f.Path]++
		}
		report.add(raw, ReplayDifference{Fields: fields})
	}

	return report, nil
}

// add records an example, bounded.
func (r *ReplayReport) add(raw []byte, d ReplayDifference) {
	if len(r.Differences) >= maxReplayExamples {
		r.Truncated = true
		return
	}
	d.ControlID, d.MessageType = shadow.Identify(raw)
	r.Differences = append(r.Differences, d)
}

// describeErrorSplit says which version failed, in words rather than as an error pair.
func describeErrorSplit(liveErr, nextErr error) string {
	if liveErr == nil {
		return "the candidate fails on this message where the current version succeeds: " +
			nextErr.Error()
	}
	return "the candidate handles this message where the current version fails: " + liveErr.Error()
}

// describeAcceptance says which way the acceptance changed.
func describeAcceptance(live, next shadow.Result) string {
	if live.Accepted {
		return "the candidate excludes this message (" + next.RejectedBy +
			") where the current version forwards it"
	}
	return "the candidate forwards this message where the current version excludes it (" +
		live.RejectedBy + ")"
}

// diffFor picks the comparison that suits the data type.
//
// Dispatched on the channel rather than sniffed from the bytes. Sniffing would work almost always and fail in the one case that
// matters: a v2 message whose first characters happen to look like XML, or a v3 document from a sender that omits the
// declaration, would be compared with the wrong walker and reported as wholly different - which reads as a candidate that
// rewrites the entire message.
func diffFor(cfg *config.Channel, live, candidate []byte, opts shadow.DiffOptions) []shadow.FieldDifference {
	switch cfg.Type() {
	case config.DataHL7v3:
		return shadow.DiffXML(live, candidate, opts)
	case config.DataX12:
		return shadow.DiffX12(live, candidate, opts)
	}

	return shadow.Diff(live, candidate, opts)
}
