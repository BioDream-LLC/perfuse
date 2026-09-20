package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Opening an existing channel in the form instead of the text editor.
//
// # Why this did not exist
//
// Editing a channel has always opened raw YAML, deliberately. Reversing a hand-written file into
// form state risks silently changing what a live channel does: a field the form does not know about
// gets dropped, the file is saved, and an interface that has run for two years quietly stops doing
// something it used to do. Nobody finds out until a downstream system complains.
//
// That reasoning is sound and it is why the form was creation-only. But the consequence was that the
// graphical builder helped with the minority of the work. Interface analysts spend most of their time
// changing existing channels, not creating new ones - a lab adds a result type, a sender starts
// padding an MRN, a destination needs one more filter. Every one of those dropped somebody into YAML.
//
// # What makes it safe
//
// The check, not the mapping. The mapping is nearly free because the form model already carries yaml
// tags and the build endpoint encodes it directly, so reading a file back is the same decoder in
// reverse. The safety comes from proving the round trip is faithful before offering the form:
//
//  1. Decode the file into the form model with unknown fields refused. A key the form does not know
//     is the exact failure mode this guards against, and refusing it here names the key.
//  2. Re-encode the model and load both the original and the re-encoding through the same
//     config.Load a file on disk goes through.
//  3. Compare the two loaded channels in canonical form. If they differ in any way, the form cannot
//     represent this file and the text editor is the only honest answer.
//
// Only when the loaded channels are identical is the form offered. That is a proof rather than a
// hope: whatever the form saves is known in advance to mean the same thing as what was there.
//
// # Comments are a warning, not a refusal
//
// A file whose semantics survive the round trip but whose bytes do not is almost always one with
// comments or hand-chosen key order. Refusing those would make the feature useless, since commented
// channel files are good practice. So it is offered with a warning that says exactly what will be
// lost. Losing a comment is recoverable and visible in the history; losing a setting is neither.

type parseRequest struct {
	YAML string `json:"yaml"`
}

type parseResponse struct {
	// Editable says whether the form can represent this channel faithfully. When false, the caller
	// must use the text editor.
	Editable bool `json:"editable"`

	// Model is the form state, present only when Editable. Deliberately absent otherwise rather
	// than partially filled: a half-populated form is worse than none, because it looks usable.
	Model *buildModel `json:"model,omitempty"`

	// Why explains a refusal in words a person can act on.
	Why string `json:"why,omitempty"`

	// Warnings are things that will change if the form saves this channel, when the change does not
	// affect behaviour. Comments and formatting.
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) handleParseChannel(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req parseRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		s.fail(w, r, http.StatusBadRequest, "there is no channel here to read")
		return
	}

	res := parseChannelForForm(req.YAML)

	// A refusal is a successful answer to the question that was asked, not an error. The caller
	// asked whether the form can edit this file; "no, because of this" is the answer.
	s.ok(w, res)
}

// parseChannelForForm decides whether the form can faithfully represent a channel file.
func parseChannelForForm(text string) parseResponse {
	// The file has to load before anything else is worth saying. A file that does not load is a
	// job for the text editor no matter what the form could do with it, and the loader's own error
	// is more useful than anything said here.
	original, err := config.Load(bytes.NewReader([]byte(text)), "(existing)")
	if err != nil {
		return parseResponse{
			Editable: false,
			Why: "This file does not load, so the form cannot show it. Fix it in the text editor " +
				"first: " + err.Error(),
		}
	}

	var model buildModel
	dec := yaml.NewDecoder(bytes.NewReader([]byte(text)))
	// The whole point. An unknown key here is a setting the form would silently drop.
	dec.KnownFields(true)
	if err := dec.Decode(&model); err != nil {
		return parseResponse{
			Editable: false,
			Why:      "The form cannot represent everything in this channel: " + tidyYAMLError(err),
		}
	}

	regenerated, err := marshalChannel(model)
	if err != nil {
		return parseResponse{
			Editable: false,
			Why:      "This channel could not be written back out from the form: " + err.Error(),
		}
	}

	// Load the regeneration the same way, then compare what the engine would actually run. Comparing
	// the loaded channels rather than the two texts is what lets comments and key order differ
	// without being treated as a change in meaning.
	rebuilt, err := config.Load(bytes.NewReader([]byte(regenerated)), "(from the form)")
	if err != nil {
		return parseResponse{
			Editable: false,
			Why: "The form would not be able to save this channel back correctly, so it is safer " +
				"to edit the file directly. " + err.Error(),
		}
	}

	before, err := canonical(original)
	if err != nil {
		return parseResponse{Editable: false, Why: "This channel could not be compared: " + err.Error()}
	}
	after, err := canonical(rebuilt)
	if err != nil {
		return parseResponse{Editable: false, Why: "This channel could not be compared: " + err.Error()}
	}

	if before != after {
		return parseResponse{
			Editable: false,
			Why: "Opening this channel in the form would change what it does, so the form will not " +
				"offer to edit it. " + firstDifference(before, after) +
				" Use the text editor, where nothing is rewritten.",
		}
	}

	res := parseResponse{Editable: true, Model: &model}

	// Behaviour is proven identical at this point, so anything left is presentation.
	if regenerated != text {
		if hasComments(text) {
			res.Warnings = append(res.Warnings,
				"This file has comments. Saving from the form will remove them, because the form "+
					"rebuilds the file from the settings it shows. The comments stay in the "+
					"channel's history, and the text editor keeps them.")
		} else {
			res.Warnings = append(res.Warnings,
				"Saving from the form will tidy the layout of this file. What the channel does will "+
					"not change.")
		}
	}

	return res
}

// canonical renders a loaded channel in one fixed form so two channels can be compared for meaning
// rather than for spelling.
func canonical(c *config.Channel) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// firstDifference names the first line on which two canonical renderings diverge.
//
// Without this the refusal is "the form would change something" and the reader has no idea what, so
// they either give up on the form permanently or assume the check is broken. Naming the line turns it
// into something actionable - usually a setting worth adding to the form.
func firstDifference(before, after string) string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")

	for i := 0; i < len(b) || i < len(a); i++ {
		var bl, al string
		if i < len(b) {
			bl = strings.TrimSpace(b[i])
		}
		if i < len(a) {
			al = strings.TrimSpace(a[i])
		}
		if bl == al {
			continue
		}
		switch {
		case al == "":
			return fmt.Sprintf("It would drop %q.", bl)
		case bl == "":
			return fmt.Sprintf("It would add %q.", al)
		default:
			return fmt.Sprintf("It would change %q to %q.", bl, al)
		}
	}
	return ""
}

// hasComments reports whether the file carries a comment, so the warning can say the right thing.
//
// Deliberately crude: it looks for a hash outside quotes. Being wrong here costs a slightly less
// specific warning and nothing else, which is not worth a YAML lexer.
func hasComments(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			return true
		}
		if i := strings.Index(line, " #"); i >= 0 {
			if strings.Count(line[:i], `"`)%2 == 0 && strings.Count(line[:i], "'")%2 == 0 {
				return true
			}
		}
	}
	return false
}

// tidyYAMLError turns the decoder's complaint into something a person can act on.
//
// yaml.v3 reports an unknown key as "line 12: field foo not found in type api.buildSource", which
// names a Go type the reader has never heard of and cannot look up. The key and the line are the
// useful parts.
func tidyYAMLError(err error) string {
	msg := err.Error()

	var keys []string
	for _, part := range strings.Split(msg, "\n") {
		part = strings.TrimSpace(part)
		const marker = "field "
		i := strings.Index(part, marker)
		if i < 0 {
			continue
		}
		rest := part[i+len(marker):]
		j := strings.Index(rest, " ")
		if j < 0 {
			continue
		}
		keys = append(keys, rest[:j])
	}

	switch len(keys) {
	case 0:
		return msg
	case 1:
		return fmt.Sprintf("the setting %q is not something the form knows about, so editing this "+
			"channel here could lose it. Use the text editor.", keys[0])
	default:
		return fmt.Sprintf("these settings are not something the form knows about, so editing this "+
			"channel here could lose them: %s. Use the text editor.", strings.Join(keys, ", "))
	}
}
