package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/translate"
)

// Migrating from Mirth, in the browser.
//
// The command line already does this, and the command line is the wrong place for it. Somebody deciding
// whether to move off Mirth is usually not the person who lives in a terminal, and the decision is made
// by looking at a list of their own channels and seeing how many come across clean. That is a picture,
// not a log.
//
// So this exists to answer one question visually: *of the channels we actually run, how many work?* The
// answer is per-channel and it is the whole sales argument, which is why it is worth an endpoint rather
// than a documentation page telling people to install a binary first.

// maxImportBytes bounds one request.
//
// A Mirth channel export is XML and verbose, but a single channel is tens of kilobytes and a whole
// server's export is a few megabytes. Ten is generous and still small enough that a mistaken upload
// cannot exhaust memory.
const maxImportBytes = 10 << 20

type importRequest struct {
	// XML is one or more Mirth channel exports. A whole-server export is a single document containing
	// many channels, and a per-channel export is one document, so both shapes have to work.
	XML string `json:"xml"`
}

// importedChannel is one channel's outcome.
type importedChannel struct {
	Name       string           `json:"name"`
	SourceName string           `json:"sourceName"`
	YAML       string           `json:"yaml"`
	Confidence string           `json:"confidence"`
	Notes      []translate.Note `json:"notes"`
	Counts     translate.Counts `json:"counts"`
	Blocked    bool             `json:"blocked"`

	// Portability answers a different question from the rest of this response.
	//
	// Everything else here answers "what must I rewrite to move to Perfuse", which only interests somebody who has already
	// decided to look at Perfuse. This answers "how much of this only runs on one vendor's software", which a site wants
	// answered before it has any opinion about Perfuse at all - and it needs nothing installed, because it runs against a
	// channel export.
	Portability mirth.Portability `json:"portability"`

	// PortabilityVerdict is that reading in a sentence, so the interface does not have to compose one from counts and risk
	// dropping the qualification that keeps it honest.
	PortabilityVerdict string `json:"portabilityVerdict"`
}

type importResponse struct {
	// Channels in the order they should be read: the ones needing attention first, because a list of
	// forty channels where thirty-seven are clean should open on the three that are not.
	Channels []importedChannel `json:"channels"`

	// Summary is the headline number, which is the only thing most readers want.
	Summary importSummary `json:"summary"`

	// Failed names documents that could not be read at all, separately from channels that translated
	// with blockers. Those are different problems and mixing them makes both look worse.
	Failed []string `json:"failed"`
}

type importSummary struct {
	Total    int `json:"total"`
	Clean    int `json:"clean"`
	Warnings int `json:"warnings"`
	Blocked  int `json:"blocked"`

	// ScriptedSteps and DeclarativeSteps together say how much of the logic became configuration rather
	// than remaining code. That ratio is the honest measure of how much a migration actually gained,
	// and hiding it would be flattering rather than useful.
	DeclarativeSteps int `json:"declarativeSteps"`
	ScriptedSteps    int `json:"scriptedSteps"`
}

// handleImportMirth translates pasted Mirth channel XML and reports what came across.
//
// Editor rather than admin: this writes nothing. It answers a question about a file the caller already
// has, and requiring an administrator to answer it would put the evaluation behind a permission request.
func (s *Server) handleImportMirth(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req importRequest
	if !s.decode(w, r, &req) {
		return
	}

	text := strings.TrimSpace(req.XML)
	if text == "" {
		s.fail(w, r, http.StatusBadRequest, "there is no XML to read",
			"paste a Mirth channel export, or a whole-server export containing several")
		return
	}
	if len(text) > maxImportBytes {
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			"that export is larger than this endpoint accepts",
			"translate it with the perfuse translate command instead, which has no size limit")
		return
	}

	channels, failures := readMirthDocuments(text)
	if len(channels) == 0 {
		// Deliberately a 400 with an explanation rather than an empty success. An empty list rendered
		// as "0 channels, all clean" would be a lie of exactly the kind this codebase avoids.
		s.fail(w, r, http.StatusBadRequest,
			"no Mirth channel was found in that XML",
			"a channel export starts with a <channel> element; a whole-server export contains several")
		return
	}

	resp := importResponse{Failed: failures}

	for _, ch := range channels {
		result := translate.Channel(ch)
		portability := ch.ScanJava().Portability()

		resp.Channels = append(resp.Channels, importedChannel{
			Name:       result.Name,
			SourceName: result.SourceName,
			YAML:       result.YAML,
			Confidence: string(result.Confidence),
			Notes:      result.Notes,
			Counts:     result.Counts,
			Blocked:    result.Blocked(),

			Portability:        portability,
			PortabilityVerdict: portability.Verdict(),
		})

		resp.Summary.Total++
		resp.Summary.DeclarativeSteps += result.Counts.Declarative
		resp.Summary.ScriptedSteps += result.Counts.Scripted

		switch {
		case result.Blocked():
			resp.Summary.Blocked++
		case result.Counts.Warnings > 0:
			resp.Summary.Warnings++
		default:
			resp.Summary.Clean++
		}
	}

	// Worst first. A list of forty channels where thirty-seven are clean should open on the three that
	// are not, because those three are the entire remaining work and the other thirty-seven need no
	// reading at all.
	sort.SliceStable(resp.Channels, func(i, j int) bool {
		a, b := resp.Channels[i], resp.Channels[j]
		if a.Blocked != b.Blocked {
			return a.Blocked
		}
		if a.Counts.Warnings != b.Counts.Warnings {
			return a.Counts.Warnings > b.Counts.Warnings
		}
		return a.Name < b.Name
	})

	if resp.Failed == nil {
		// An empty array rather than null, because the front end will call .length on it and a nil
		// slice marshals as null. This exact mistake shipped once already in the content search.
		resp.Failed = []string{}
	}

	s.ok(w, resp)
}

// readMirthDocuments pulls every channel out of pasted XML.
//
// Both shapes have to work: a per-channel export is one <channel> document, and a whole-server export is
// a <list> of them. Asking somebody to split a server export by hand before they can see whether Perfuse
// is worth trying would lose most of the people who were willing to look.
func readMirthDocuments(text string) ([]*mirth.Channel, []string) {
	var channels []*mirth.Channel
	var failures []string

	// Try the whole document first: mirth.ParseChannel handles a single channel, and a list needs
	// splitting. Splitting on the element boundary is crude but the alternative - a second XML model for
	// the container - buys nothing, since the container carries no information worth keeping.
	if ch, err := mirth.ParseChannel(strings.NewReader(text)); err == nil {
		return []*mirth.Channel{ch}, nil
	}

	for i, part := range splitChannels(text) {
		ch, err := mirth.ParseChannel(strings.NewReader(part))
		if err != nil {
			failures = append(failures, describeFailure(i, part, err))
			continue
		}
		channels = append(channels, ch)
	}

	return channels, failures
}

// splitChannels cuts a multi-channel export into single-channel documents.
//
// The opening tag carries attributes in every real export - <channel version="4.5.2"> - so matching a
// bare "<channel>" finds nothing, which is how the first version of this failed. It also has to avoid
// matching <channelId> and similar, so the tag name must be followed by whitespace or the closing
// bracket and nothing else.
func splitChannels(text string) []string {
	const closing = "</channel>"

	var parts []string
	rest := text

	for {
		start := findOpenChannel(rest)
		if start < 0 {
			return parts
		}

		end := strings.Index(rest[start:], closing)
		if end < 0 {
			// An unterminated channel is returned as a part anyway, so the parser reports it as a broken
			// document rather than this function silently dropping it. A channel that vanishes without
			// comment is the failure mode that matters here.
			return append(parts, rest[start:])
		}

		parts = append(parts, rest[start:start+end+len(closing)])
		rest = rest[start+end+len(closing):]
	}
}

// findOpenChannel returns the index of the next <channel ...> opening tag, or -1.
func findOpenChannel(text string) int {
	offset := 0
	for {
		i := strings.Index(text[offset:], "<channel")
		if i < 0 {
			return -1
		}
		at := offset + i

		after := at + len("<channel")
		if after < len(text) {
			switch c := text[after]; c {
			case '>', ' ', '\t', '\r', '\n':
				return at
			}
		}

		// Something like <channelId>. Keep looking.
		offset = after
	}
}

// describeFailure names a document that could not be read, using its channel name when it has one.
//
// "document 3 of 40" is almost useless to somebody looking at a list of their own channels; the name is
// what they recognise.
func describeFailure(index int, part string, err error) string {
	name := nameFromXML(part)
	if name == "" {
		return fmt.Sprintf("document %d: %s", index+1, err)
	}
	return name + ": " + err.Error()
}

func nameFromXML(part string) string {
	const open = "<name>"
	const close = "</name>"

	start := strings.Index(part, open)
	if start < 0 {
		return ""
	}
	rest := part[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}
