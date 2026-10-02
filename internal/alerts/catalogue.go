package alerts

// Metadata describing each kind, so an editor can present a rule instead of asking somebody to know the file format.
//
// # Why this lives here and not in the browser
//
// The threshold field means a different thing for every kind. It is a share of messages for error-rate, a count of messages for
// queue-depth, a number of seconds for queue-age, a count of attempts for queue-stuck, and a share of normal traffic for
// below-rhythm. Two of those are fractions between nought and one, and the rest are plain counts.
//
// That is a trap rather than a detail. Somebody who means five percent and types 5 into an error-rate rule gets a threshold of five
// hundred percent, the file loads, nothing complains, and the alert they carefully wrote can never fire. The same number in a
// queue-depth rule is a perfectly ordinary five messages. No amount of care in the browser fixes this, because the browser cannot
// know which kinds are shares unless something tells it - and if the something is a second list maintained by hand, it will drift
// from the evaluators and start lying.
//
// So the units are declared once, next to the kinds, and a test pairs this against the evaluator table in both directions.
//
// # Why direction is here too
//
// Most rules fire when a measurement rises above the threshold. Two fire when it falls below: no-traffic and below-rhythm, both of
// which are about silence. An editor that says "fires above" on those would state the opposite of what the rule does, which is worse
// than saying nothing.

// Unit describes what a threshold counts, so a form can label the box and pick a sensible step.
type Unit string

// The units a threshold can be in.
const (
	// UnitShare is a fraction between 0 and 1. Shown as a percentage, because nobody thinks in shares.
	UnitShare Unit = "share"
	// UnitMessages is a count of messages.
	UnitMessages Unit = "messages"
	// UnitSeconds is an age or a duration in seconds.
	UnitSeconds Unit = "seconds"
	// UnitAttempts is a count of delivery attempts.
	UnitAttempts Unit = "attempts"
	// UnitNone is for kinds that have nothing to threshold. channel-down is either true or it is not.
	UnitNone Unit = "none"
)

// Direction says which way a measurement has to move for the rule to fire.
type Direction string

// The two directions.
const (
	// Above fires when the measurement exceeds the threshold.
	Above Direction = "above"
	// Below fires when the measurement falls under the threshold, which is how silence is detected.
	Below Direction = "below"
)

// KindInfo is everything an editor needs to offer one kind of rule.
type KindInfo struct {
	Kind Kind `json:"kind"`

	// Label is what to call it on screen. Sentence case, because it appears in a list and in a sentence.
	Label string `json:"label"`

	// Summary is one line saying what the rule watches.
	Summary string `json:"summary"`

	// Detail is the part worth reading before choosing a threshold, including the trap if there is one.
	Detail string `json:"detail"`

	// Unit is what the threshold counts.
	Unit Unit `json:"unit"`

	// Direction says whether the rule fires above or below the threshold.
	Direction Direction `json:"direction"`

	// Default is a starting threshold that is defensible rather than merely valid.
	Default float64 `json:"default"`

	// Channel reports whether narrowing by channel means anything for this kind.
	Channel bool `json:"channel"`

	// Destination reports whether narrowing by destination means anything. Only the queue rules have destinations, because a queue
	// belongs to one destination of one channel; offering the field elsewhere would invite a rule that silently matches nothing.
	Destination bool `json:"destination"`
}

// KindCatalogue describes every kind that can be evaluated.
//
// Ordered deliberately rather than alphabetically: the rules an operator reaches for first are first. Delivery failures, then queues
// backing up, then silence, then the quieter conditions that a healthy-looking system can sit in for weeks.
func KindCatalogue() []KindInfo {
	return []KindInfo{
		{
			Kind:    KindErrorRate,
			Label:   "Messages are failing",
			Summary: "Fires when too large a share of a channel's messages do not get through.",
			Detail: "The threshold is a share of messages, not a count and not a percentage - 0.05 means five in a hundred. " +
				"A channel is ignored until it has carried ten messages in the window, because one failure out of one message " +
				"is a hundred per cent error rate and almost never worth waking somebody for.",
			Unit:      UnitShare,
			Direction: Above,
			Default:   0.05,
			Channel:   true,
		},
		{
			Kind:      KindQueueDepth,
			Label:     "A queue is backing up",
			Summary:   "Fires when too many messages are waiting for one destination.",
			Detail:    "Counts messages waiting, so the right threshold depends on how much this feed normally carries.",
			Unit:      UnitMessages,
			Direction: Above,
			Default:   500,
			Channel:   true, Destination: true,
		},
		{
			Kind:    KindQueueAge,
			Label:   "A queue is not draining",
			Summary: "Fires when the oldest message waiting for a destination is older than this.",
			Detail: "Often more useful than depth. A queue of ten thousand that clears in a minute is healthy; a queue of three " +
				"that has not moved in an hour is not.",
			Unit:      UnitSeconds,
			Direction: Above,
			Default:   3600,
			Channel:   true, Destination: true,
		},
		{
			Kind:    KindQueueStuck,
			Label:   "A message is being retried forever",
			Summary: "Fires when something in a queue has been attempted more times than this.",
			Detail: "Catches the one poisonous message that will never succeed, which a depth or age rule can miss entirely " +
				"while the rest of the queue flows past it.",
			Unit:      UnitAttempts,
			Direction: Above,
			Default:   50,
			Channel:   true, Destination: true,
		},
		{
			Kind:    KindNoTraffic,
			Label:   "A feed has gone quiet",
			Summary: "Fires when a channel receives fewer messages than this in the window.",
			Detail: "A fixed threshold, so only use it on feeds that genuinely never stop. Most clinical feeds do stop - the lab " +
				"feed at six, the referral feed all weekend - and a threshold that tolerates Sunday cannot catch a real outage " +
				"on Tuesday. If the feed has a daily or weekly rhythm, use \"a feed is far below normal\" instead.",
			Unit:      UnitMessages,
			Direction: Below,
			Default:   1,
			Channel:   true,
		},
		{
			Kind:    KindBelowRhythm,
			Label:   "A feed is far below normal",
			Summary: "Fires when a channel carries far less than it usually does at this hour of this weekday.",
			Detail: "Compares a channel against its own history rather than a fixed number, so a clinic feed going quiet at 2pm " +
				"on a Tuesday is an alert and the same silence on Sunday is not. The threshold is a share of normal - 0.8 means " +
				"fire when it drops below four fifths of usual. This is the only rule that can report silence on a feed that is " +
				"legitimately quiet most of the week, which is the case of a stopped lab feed: a patient-safety problem that " +
				"raises no error anywhere else.",
			Unit:      UnitShare,
			Direction: Below,
			Default:   0.8,
			Channel:   true,
		},
		{
			Kind:      KindChannelDown,
			Label:     "A channel is not running",
			Summary:   "Fires when a channel that should be running is not.",
			Detail:    "There is nothing to set a threshold on: the channel is either running or it is not.",
			Unit:      UnitNone,
			Direction: Above,
			Default:   1,
			Channel:   true,
		},
		{
			Kind:      KindScriptError,
			Label:     "Scripts are throwing errors",
			Summary:   "Fires when a channel's scripts raise more errors than this in the window.",
			Detail:    "Separate from messages failing, because a script can throw and the message can still be delivered.",
			Unit:      UnitMessages,
			Direction: Above,
			Default:   10,
			Channel:   true,
		},
		{
			Kind:      KindQuarantined,
			Label:     "Database rows are being abandoned",
			Summary:   "Fires when a database source gives up on more rows than this.",
			Detail:    "An abandoned row is data that was read and never became a message, so nothing downstream will ever see it.",
			Unit:      UnitMessages,
			Direction: Above,
			Default:   1,
			Channel:   true,
		},
		{
			Kind:      KindSlowness,
			Label:     "Delivery is slow",
			Summary:   "Fires when the slowest messages take longer than this.",
			Detail:    "Measured at the ninety-ninth percentile, so it reports the tail rather than the average.",
			Unit:      UnitSeconds,
			Direction: Above,
			Default:   30,
			Channel:   true,
		},
		{
			Kind:    KindContract,
			Label:   "A feed stopped matching its contract",
			Summary: "Fires when messages stop looking the way the channel's contract says they must.",
			Detail: "Different in nature from everything above. Nothing is failing when this fires - the messages parse and the " +
				"deliveries succeed - which is exactly why it needs an alert of its own. It is the only condition here that a " +
				"healthy-looking system can be in for weeks.",
			Unit:      UnitMessages,
			Direction: Above,
			Default:   1,
			Channel:   true,
		},
	}
}

// InfoFor returns the metadata for one kind.
func InfoFor(k Kind) (KindInfo, bool) {
	for _, info := range KindCatalogue() {
		if info.Kind == k {
			return info, true
		}
	}

	return KindInfo{}, false
}
