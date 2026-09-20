package profile

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
)

// FromSample: building a channel from a handful of example messages.
//
// # Why this exists separately from Synthesise
//
// Synthesise works from traffic a channel has already handled, which presupposes a channel and a feed. The situation this
// addresses is the opposite and far more common outside a hospital: there is no channel, nothing is running, and what
// somebody has is the sample message the laboratory attached to an email.
//
// A single HL7 interface is commonly costed in the tens of thousands with maintenance on top, and one of the three things
// consistently named as sinking these projects is that nobody in-house has done HL7 before. A four-doctor practice has
// neither the money nor the analyst. The sample message is the one artefact they reliably have.
//
// # Why this reports its confidence instead of just producing a file
//
// One message tells you very little, and the danger is that it looks like it tells you a lot. Every field in one sample
// is populated in one hundred per cent of messages. Every code appears once and looks mandatory. Every segment appears
// exactly once, so nothing is repeating and nothing is optional.
//
// A generator that emitted a confident-looking channel from that would be actively harmful: somebody would deploy it,
// and the first message with two OBX segments or an absent PID-8 would behave in a way nobody predicted.
//
// So this reports what it observed, what it guessed, and what it cannot know from the sample given - and the count of
// samples is the first thing it talks about.

// SampleReading is what a set of pasted messages supports concluding.
type SampleReading struct {
	// Messages is how many parsed.
	Messages int `json:"messages"`

	// Unreadable is how many did not.
	Unreadable int `json:"unreadable"`

	// Types are the message types found.
	Types []TypeCount `json:"types"`

	// Observed are things the sample genuinely shows.
	Observed []string `json:"observed"`

	// Guessed are choices made that the sample does not determine.
	//
	// Separate from Observed deliberately. A reader who cannot tell which is which has to verify everything or trust
	// everything, and both are worse than knowing where to look.
	Guessed []string `json:"guessed"`

	// Unknowable are questions this sample cannot answer at all.
	//
	// The most important list, and the one a confident-looking generator leaves out. With one message nobody can know
	// which fields are optional, which segments repeat, or which codes the sender uses beyond the ones that happened
	// to appear.
	Unknowable []string `json:"unknowable"`

	// YAML is the proposed channel.
	YAML string `json:"yaml"`

	// Confidence is low, moderate or reasonable, in words rather than a number.
	//
	// A percentage would imply a calculation. This is a judgement about how many samples there are and how varied
	// they were, and saying so in words is more honest than dressing it up as 62%.
	Confidence string `json:"confidence"`
}

// ReadSample splits pasted text into messages, profiles them and proposes a channel.
//
// The text may hold several messages one after another, with or without MLLP framing, and with either line ending.
// Senders paste what they were emailed, and refusing a message because it arrived with Windows line endings would be an
// obstruction rather than a check.
func ReadSample(text, channelName string) (*SampleReading, error) {
	messages := SplitMessages(text)
	if len(messages) == 0 {
		return nil, fmt.Errorf("no HL7 messages were found in that text. A message starts with MSH followed by the " +
			"separator it uses, usually MSH|. If what you have is a specification document rather than an example " +
			"message, there is nothing here that can read it yet")
	}

	var good [][]byte
	var unreadable int
	for _, m := range messages {
		if _, err := hl7.Parse(m); err != nil {
			unreadable++
			continue
		}
		good = append(good, m)
	}

	if len(good) == 0 {
		return nil, fmt.Errorf("%d message(s) were found but none could be parsed. The usual causes are a paste that "+
			"was cut off partway through the MSH segment, or text that begins with something other than MSH",
			len(messages))
	}

	if channelName == "" {
		channelName = "from-sample"
	}

	// An unusual field separator, which parses and produces nonsense.
	//
	// Worth checking because it is silent. HL7 takes whatever character follows MSH from the message itself, so a
	// sample that has been through a word processor - separators replaced with typographic look-alikes - parses
	// perfectly and yields fields that are all wrong. Nothing errors. The channel would be built against a message
	// nobody sent.
	//
	// Found by writing a test that asserted such a message is refused and watching it pass instead.
	var separatorWarning string
	if sep := fieldSeparatorOf(good[0]); sep != "|" {
		separatorWarning = fmt.Sprintf("this sample uses %q as its field separator rather than the usual %q. That is "+
			"legal - HL7 takes the separator from the message - but it is also what a sample looks like after it has "+
			"been through a word processor, which replaces the separators with characters that look identical on "+
			"screen. If the sender really uses %q the proposal below is correct; if not, ask for the sample as a "+
			"plain text attachment", sep, "|", sep)
	}

	report := Build(good)
	suggestions, _ := Suggest(report, SuggestOptions{})

	out := &SampleReading{
		Messages:   report.Messages,
		Unreadable: unreadable,
		Types:      report.Types,
		YAML: Synthesise(report, suggestions, SynthesisOptions{
			ChannelName: channelName,
			Description: fmt.Sprintf("proposed from %d sample message(s); read every line before running this",
				report.Messages),
		}),
	}

	describeSampleConfidence(out, report)

	if separatorWarning != "" {
		// First in the list, because if it applies then everything else in the proposal is derived from misread
		// fields and there is no point reading further.
		out.Unknowable = append([]string{separatorWarning}, out.Unknowable...)
		out.Confidence = "low"
	}

	return out, nil
}

// fieldSeparatorOf returns the character a message declares as its field separator.
//
// That is MSH-1: the single character immediately after the segment identifier. Read from the message rather than
// assumed, because assuming it is what makes a mangled sample parse into nonsense without complaint.
func fieldSeparatorOf(raw []byte) string {
	if len(raw) < 4 || !strings.HasPrefix(string(raw), "MSH") {
		return ""
	}
	// A rune, not a byte. A typographic substitute is multi-byte, and taking one byte of it would report a fragment.
	for _, r := range string(raw[3:]) {
		return string(r)
	}
	return ""
}

// SplitMessages finds the HL7 messages in a block of text.
//
// Splitting on MSH rather than on a line count, because a message has no fixed number of lines and the segment
// terminator somebody pastes is whatever their mail client used.
func SplitMessages(text string) [][]byte {
	// Strip MLLP framing if it survived a copy and paste. Left in, the leading 0x0B becomes part of the first segment
	// identifier and the message stops being an MSH.
	text = strings.NewReplacer("\x0b", "", "\x1c", "", "\x1d", "").Replace(text)

	// Normalise the terminator. HL7 uses carriage return; text pasted out of a mail client arrives with newlines or
	// both, and a parser that insists on one of the three rejects most of what people actually have.
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")

	var out [][]byte
	var current strings.Builder

	for _, line := range strings.Split(text, "\r") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// A new MSH starts a new message. This is also how a batch pasted as one block gets split.
		if strings.HasPrefix(trimmed, "MSH") && current.Len() > 0 {
			out = append(out, []byte(current.String()))
			current.Reset()
		}
		if current.Len() == 0 && !strings.HasPrefix(trimmed, "MSH") {
			// Anything before the first MSH is prose: an email header, a covering note, a page number from a PDF.
			// Skipped rather than treated as a segment, because it would produce a message that fails to parse and an
			// error blaming the sample rather than the paste.
			continue
		}

		current.WriteString(trimmed)
		current.WriteString("\r")
	}
	if current.Len() > 0 {
		out = append(out, []byte(current.String()))
	}

	return out
}

// describeSampleConfidence fills in what is observed, guessed and unknowable.
func describeSampleConfidence(out *SampleReading, r *Report) {
	// What the sample genuinely shows.
	for _, t := range r.Types {
		out.Observed = append(out.Observed,
			fmt.Sprintf("the sample carries %s", t.Type))
	}
	if len(r.Segments) > 0 {
		var ids []string
		var nonStandard []string
		for _, seg := range r.Segments {
			ids = append(ids, seg.ID)
			if !seg.Standard {
				nonStandard = append(nonStandard, seg.ID)
			}
		}
		out.Observed = append(out.Observed,
			fmt.Sprintf("segments present: %s", strings.Join(ids, ", ")))

		if len(nonStandard) > 0 {
			// Worth its own line. A Z-segment is the thing a specification never mentions and an integration always
			// has to handle, and it is the commonest reason a channel built from a standard template does not work.
			out.Observed = append(out.Observed,
				fmt.Sprintf("%s is not in the HL7 standard, so it is local to this sender and nothing downstream "+
					"will know it without being told", strings.Join(nonStandard, " and ")))
		}
	}

	// Choices this made that the sample does not determine.
	out.Guessed = append(out.Guessed,
		"the listening address and port, because the sample cannot say where the sender will connect",
		"the destination, because the sample says nothing about where these messages should go",
		"the acknowledgement mode, which has to match what the sending system expects")

	// The honest limits.
	switch {
	case r.Messages == 1:
		out.Confidence = "low"
		out.Unknowable = append(out.Unknowable,
			"which fields are optional. In one message every field that is present looks mandatory and every field "+
				"that is absent looks unused",
			"which segments repeat. A single message with one OBX gives no reason to think a result can carry twenty",
			"which codes the sender uses. The ones here appeared once; a sex code of M does not mean F never arrives",
			"whether the sender ever sends a different trigger event, which is usually where an interface first breaks")

	case r.Messages < 10:
		out.Confidence = "low"
		out.Unknowable = append(out.Unknowable,
			fmt.Sprintf("how often each field is really populated. %d messages cannot distinguish a field that is "+
				"always sent from one that happened to be sent %d times", r.Messages, r.Messages),
			"the full set of codes the sender uses, and a code arriving that nothing maps is a common first failure",
			"which trigger events the feed carries beyond those here")

	case r.Messages < 100:
		out.Confidence = "moderate"
		out.Unknowable = append(out.Unknowable,
			"rare trigger events. An event that occurs in one message per thousand will not be in this sample, and "+
				"those are the ones that break interfaces",
			"the tail of the code sets, for the same reason")

	default:
		out.Confidence = "reasonable"
		out.Unknowable = append(out.Unknowable,
			"events rarer than roughly one in the sample size, which for clinical feeds often means the merges, "+
				"cancellations and corrections")
	}

	if out.Unreadable > 0 {
		out.Unknowable = append(out.Unknowable,
			fmt.Sprintf("what was in the %d message(s) that could not be parsed; they were left out of this "+
				"entirely rather than guessed at", out.Unreadable))
	}
}
