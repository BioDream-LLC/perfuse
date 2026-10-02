package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/ldap"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/peers"
)

// validateSettingsContent checks a proposed settings file before it is written.
//
// Every kind must have a validator, and a kind without one is refused rather than written. That is the difference between
// this and a plain text editor: the whole value is that a file which would break the feature it configures never reaches
// disk.
//
// It matters most for the sign-on files. A malformed oidc.yaml takes single sign-on down at the next restart, and if local
// accounts have been left unused nobody can get in at all - which is the worst possible time to discover a typo.
//
// Validation runs against the real loader for each kind, not a copy of it. A second implementation would accept things the
// real one rejects, which is exactly the failure this prevents.
func validateSettingsContent(kind string, content []byte) error {
	if len(strings.TrimSpace(string(content))) == 0 {
		// Refused. An empty alerts file is arguably meaningful, but an empty OIDC file means the flag points at
		// something unusable - and treating empty as "switched off" would be a way to disable single sign-on by
		// saving a blank page.
		return fmt.Errorf("this file is empty; to switch a feature off, restart without its flag rather than " +
			"saving an empty file")
	}

	switch kind {
	case "alerts":
		return validateAlertsContent(content)
	case "oidc":
		return validateOIDCContent(content)
	case "ldap":
		return validateLDAPContent(content)
	case "peers":
		return validatePeersContent(content)
	default:
		return errUnknownSettingsKind
	}
}

// withTempFile writes content to a temporary file and calls fn with its path.
//
// The loaders take paths rather than bytes, so validation has to give them a file. Written with 0o600 in a temporary
// directory and removed afterwards, because for a moment this file holds the same client secret the real one does.
func withTempFile(content []byte, suffix string, fn func(path string) error) error {
	dir, err := os.MkdirTemp("", "perfuse-settings-check-*")
	if err != nil {
		return fmt.Errorf("a temporary directory could not be created to check this file: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "candidate"+suffix)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}

	return fn(path)
}

// validateAlertsContent checks alert rules.
//
// Parsed from bytes rather than through a temporary file, so the message names the thing the person is editing. An earlier
// version wrote a temporary file and the refusal quoted a path under /var/folders, which reads as an internal error rather
// than as a mistake in the text on screen.
func validateAlertsContent(content []byte) error {
	if _, err := alerts.ParseRules("these alert rules", content); err != nil {
		return fmt.Errorf("not valid: %w", err)
	}
	return nil
}

// validateOIDCContent checks single sign-on configuration.
//
// Deliberately does not contact the provider. Discovery needs the network, and a provider being briefly unreachable must
// not stop somebody saving a corrected client secret - which is very likely why they are editing the file at all.
func validateOIDCContent(content []byte) error {
	return withTempFile(content, ".yaml", func(path string) error {
		if _, err := oidc.LoadFileWithoutDiscovery(path); err != nil {
			return fmt.Errorf("this single sign-on configuration is not valid: %s", withoutTempPath(err, path))
		}
		return nil
	})
}

// validateLDAPContent checks directory configuration.
//
// Does not contact the directory, for the same reason as above.
func validateLDAPContent(content []byte) error {
	return withTempFile(content, ".yaml", func(path string) error {
		if _, err := ldap.LoadConfig(path); err != nil {
			return fmt.Errorf("this directory configuration is not valid: %s", withoutTempPath(err, path))
		}
		return nil
	})
}

// validatePeersContent checks the fleet peer list.
func validatePeersContent(content []byte) error {
	return withTempFile(content, ".yaml", func(path string) error {
		if _, err := peers.Load(path); err != nil {
			return fmt.Errorf("this peer list is not valid: %s", withoutTempPath(err, path))
		}
		return nil
	})
}

// withoutTempPath removes the temporary file's path from a message.
//
// The loaders take paths and put them in their errors, which is right for a server reading a real file and wrong here: a
// refusal quoting /var/folders/.../candidate.yaml reads as an internal error rather than as a mistake in the text somebody
// is looking at. Both the full path and its directory are removed, since different loaders quote different amounts.
func withoutTempPath(err error, path string) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, path+": ", "")
	msg = strings.ReplaceAll(msg, path, "this file")
	msg = strings.ReplaceAll(msg, filepath.Dir(path), "")
	return strings.TrimSpace(msg)
}

// unusedContext keeps the context import honest if a validator later needs one.
//
// Named rather than removed, because the next validator to be added is likely to want a context - discovery, a directory
// bind - and a reader should see that the omission is deliberate rather than an oversight.
var _ = context.Background
