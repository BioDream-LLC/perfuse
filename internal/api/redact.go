package api

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// secretFields are the YAML keys whose values are credentials.
//
// A list of names rather than a guess at what looks secret, because guessing fails in both directions: it would miss
// "passcode" and it would redact a field called "token_endpoint", which is a URL somebody needs to read.
//
// Kept in one place, and there is a test that walks every configuration struct in internal/config looking for a field
// whose name suggests a credential and is absent from this list. A new destination type with a new password field would
// otherwise be redacted nowhere, and nothing would say so.
var secretFields = map[string]bool{
	"password":       true,
	"passcode":       true,
	"passphrase":     true,
	"token":          true,
	"bearer_token":   true,
	"client_secret":  true,
	"secret":         true,
	"api_key":        true,
	"private_key":    true,
	"key":            false, // Deliberately not redacted: "key" is an S3 object key and a routing key far more often
	"bind_password":  true,
	"sasl_password":  true,
	"shared_secret":  true,
	"access_key":     true,
	"secret_key":     true,
	"session_token":  true,
	"auth_token":     true,
	"webhook_secret": true,

	// Found by the drift guard in redact_test.go the first time it ran, which is exactly what it is for: both of
	// these are real credentials that this list missed, and nothing else would have said so.
	"key_passphrase":    true, // an SFTP private key passphrase
	"secret_access_key": true, // the secret half of an AWS key pair

	// Not redacted, and each is a decision rather than an oversight.
	"access_key_id": false, // an identifier, useless alone, and needed to tell two accounts apart
	"dsn":           false, // handled separately: sqldb.Redact strips only the password, keeping host and database
}

// redactedPlaceholder replaces a secret value.
//
// Fixed rather than length-preserving. A run of asterisks matching the real length tells anybody reading it how long the
// password is, which is a meaningful head start - and a placeholder that varies looks like data.
const redactedPlaceholder = "**redacted**"

// redactSecretsInYAML replaces credential values in a channel definition.
//
// Operates on the parsed YAML tree rather than by pattern-matching text, because a regular expression over YAML gets the
// hard cases wrong in both directions: a password containing a colon and a space, a value in block scalar form, a
// commented-out line that still holds a real credential from last week.
//
// Comments, ordering and formatting survive, because this output is what an editor reads and reason-for-change comments
// are frequently the most useful thing in the file.
func redactSecretsInYAML(raw []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		// Refused rather than returned unredacted. A file that will not parse is exactly the case where a naive
		// fallback would hand back the original with every credential in it.
		return nil, fmt.Errorf("this channel definition could not be parsed, so its credentials cannot be "+
			"redacted and it will not be shown: %w", err)
	}

	if !redactNode(&root) {
		// Nothing was redacted, so the original bytes are returned untouched.
		//
		// This matters beyond efficiency. Re-marshalling reformats the document - it re-indents, requotes and moves
		// comments - so a channel with no credentials in it would come back different from the file on disk for no
		// reason. Two tests caught exactly that, comparing a download against the file it came from.
		//
		// It also means the common case is byte-for-byte faithful, and only a definition that actually contains a
		// credential is rewritten.
		return raw, nil
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// redactNode walks the tree replacing secret values, reporting whether it changed anything.
func redactNode(node *yaml.Node) bool {
	if node == nil {
		return false
	}

	changed := false

	if node.Kind == yaml.MappingNode {
		// Mapping contents alternate key, value.
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]

			if key.Kind == yaml.ScalarNode && secretFields[strings.ToLower(strings.TrimSpace(key.Value))] {
				// Only scalars are replaced. A secret held as a list or a map is not a shape this
				// configuration uses, and blanking a whole subtree would silently discard structure.
				if value.Kind == yaml.ScalarNode && value.Value != "" {
					value.Value = redactedPlaceholder
					value.Tag = "!!str"
					// The style is reset, or a value that was a block scalar keeps its indentation
					// markers and the result does not round-trip.
					value.Style = 0
					changed = true
				}
				continue
			}

			if redactNode(value) {
				changed = true
			}
		}
		return changed
	}

	for _, child := range node.Content {
		if redactNode(child) {
			changed = true
		}
	}

	return changed
}

// secretFieldNames lists the redacted keys in a stable order, for a test or a description.
func secretFieldNames() []string {
	out := make([]string, 0, len(secretFields))
	for name, redacted := range secretFields {
		if redacted {
			out = append(out, name)
		}
	}
	// Sorted, because Go maps range randomly and anything user-visible has to be stable.
	sort.Strings(out)
	return out
}

// mayReadSecrets reports whether a role may see credentials in a channel definition.
//
// Editor and above. The reasoning is not that an editor is more trusted with secrets in the abstract - it is that an
// editor can already change a destination to point at a host they control, so withholding the existing password buys
// very little while making ordinary editing painful.
//
// A viewer is different in kind. That role exists to watch message flow, and it was handing out the password for every
// downstream system: SFTP, databases, API tokens. Nothing about read-only monitoring requires those.
func mayReadSecrets(role string) bool {
	switch role {
	case "editor", "admin", "platform":
		return true
	default:
		return false
	}
}
