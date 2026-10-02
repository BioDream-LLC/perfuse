package attach

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
)

// Rule says which field to extract and when.
type Rule struct {
	// Path is the field to extract, as an HL7 path such as OBX-5.5. Required.
	Path string `yaml:"path"`

	// MinBytes is the size below which a value is left alone. Defaults to 4096.
	//
	// A threshold rather than always extracting, because a short value in the same field as a long one is ordinary -
	// OBX-5 holds a numeric result on one message and an embedded PDF on the next - and moving a six-byte value out
	// of the message costs a row and a lookup to save nothing.
	MinBytes int `yaml:"min_bytes,omitempty"`

	// Repeats extracts every repeat of the field rather than only the first.
	//
	// Off by default: a message with one document is the common case, and quietly extracting from repeat five of a
	// field somebody thought was single would surprise them.
	Repeats bool `yaml:"repeats,omitempty"`
}

// DefaultMinBytes is the threshold when a rule does not set one.
//
// Four kilobytes: comfortably above any clinical text value, comfortably below any real document, and small enough
// that base64 of a one-page scan is caught.
const DefaultMinBytes = 4096

// Validate fills defaults and refuses a rule that cannot work.
func (r *Rule) Validate() error {
	r.Path = strings.TrimSpace(r.Path)
	if r.Path == "" {
		return fmt.Errorf("an attachment rule needs a path, such as OBX-5.5")
	}
	if _, err := hl7.ParsePath(r.Path); err != nil {
		return fmt.Errorf("attachment rule path %q is not a field reference: %w", r.Path, err)
	}
	if r.MinBytes == 0 {
		r.MinBytes = DefaultMinBytes
	}
	if r.MinBytes < 64 {
		// A threshold this low would extract ordinary field values, turning every message into a message plus a
		// dozen rows and making the store larger rather than smaller.
		return fmt.Errorf("attachment rule for %s has min_bytes of %d, which is small enough to extract ordinary "+
			"field values; that makes the store larger rather than smaller", r.Path, r.MinBytes)
	}

	// MSH carries the metadata every part of the engine routes on, and extracting from it would leave a message
	// whose control ID or type could not be read without a database lookup.
	if strings.HasPrefix(strings.ToUpper(r.Path), "MSH-") {
		return fmt.Errorf("attachment rule path %s is in MSH, which carries the control ID and message type "+
			"everything routes on; extracting from it would make a stored message unreadable on its own", r.Path)
	}

	return nil
}

// Extract moves payloads out of a message according to the rules.
//
// The message is rewritten by replacing values in place rather than by rebuilding it from parsed segments. That
// matters: rebuilding would normalise the message, and a stored message has to remain byte-for-byte evidence of what
// a sender sent apart from the payloads deliberately removed.
func Extract(message []byte, rules []Rule) (*Result, error) {
	out := &Result{Message: message}
	if len(rules) == 0 {
		return out, nil
	}

	msg, err := hl7.Parse(message)
	if err != nil {
		// Not an error worth failing on: an unparseable message cannot have attachments extracted, and it still
		// needs to be stored and acknowledged as unparseable. Returned unchanged.
		return out, nil
	}

	// Collected first, applied after, because replacing as we go would invalidate the offsets of everything later in
	// the message.
	type replacement struct {
		value string
		path  string
	}
	var found []replacement

	for _, rule := range rules {
		paths := []string{rule.Path}
		if rule.Repeats {
			paths = expandRepeats(msg, rule.Path)
		}

		for _, path := range paths {
			value := msg.MustGet(path)
			if len(value) < rule.MinBytes {
				continue
			}
			found = append(found, replacement{value: value, path: path})
		}
	}

	if len(found) == 0 {
		return out, nil
	}

	text := string(message)
	for _, f := range found {
		digest := Compute([]byte(f.value))
		token := digest.Token()

		// Replaced once per occurrence rather than globally, so two fields holding identical payloads each get their
		// own token - which is correct, because both must be reassembled.
		before := len(text)
		text = strings.Replace(text, f.value, token, 1)
		if len(text) == before {
			// The value was not found literally, which happens when a path resolves through escaping so the parsed
			// value differs from the bytes in the message. Skipped rather than guessed at: a partial replacement
			// would corrupt the message, and corrupting a message to save space is never the right trade.
			continue
		}

		out.Attachments = append(out.Attachments, Attachment{
			Digest:  digest,
			Size:    len(f.value),
			Path:    f.path,
			Payload: []byte(f.value),
		})
		out.Extracted++
		out.Saved += len(f.value) - len(token)
	}

	out.Message = []byte(text)
	return out, nil
}

// expandRepeats turns a path into one path per repeat present.
func expandRepeats(msg *hl7.Message, path string) []string {
	// Bounded rather than open-ended. A message claiming hundreds of repeats is malformed or hostile, and walking
	// all of them to find payloads is work an attacker chooses for us.
	const maxRepeats = 64

	var out []string
	for i := 1; i <= maxRepeats; i++ {
		indexed := withRepeat(path, i)
		if msg.MustGet(indexed) == "" {
			break
		}
		out = append(out, indexed)
	}
	if len(out) == 0 {
		return []string{path}
	}
	return out
}

// withRepeat inserts a repeat index into a path, so OBX-5.5 becomes OBX-5[2].5.
func withRepeat(path string, index int) string {
	dash := strings.Index(path, "-")
	if dash < 0 {
		return path
	}
	rest := path[dash+1:]
	dot := strings.Index(rest, ".")
	if dot < 0 {
		return fmt.Sprintf("%s[%d]", path, index)
	}
	return fmt.Sprintf("%s-%s[%d].%s", path[:dash], rest[:dot], index, rest[dot+1:])
}
