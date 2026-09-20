package main

import (
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/biodream-llc/perfuse/internal/webauthn"
)

// Building the passkey attestation policy from the command line.
//
// Its own file because it is a security control with three ways to be configured wrongly, and each refusal needs a sentence
// explaining what the operator would have believed.

// passkeyAttestationPolicy reads the roots and the model allow-list.
//
// Returns the zero policy when neither is given, which verifies nothing and claims nothing.
func passkeyAttestationPolicy(rootsFile, models string) (webauthn.AttestationPolicy, error) {
	var policy webauthn.AttestationPolicy

	rootsFile = strings.TrimSpace(rootsFile)
	models = strings.TrimSpace(models)

	if rootsFile != "" {
		pem, err := os.ReadFile(rootsFile)
		if err != nil {
			return policy, fmt.Errorf("reading the passkey attestation roots: %w", err)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			// Refused rather than left empty. An empty pool would make Configured report false, so
			// attestation would silently not be verified on a server whose command line says it is - which
			// is the worst available outcome for a control like this.
			return policy, fmt.Errorf("%s contains no certificates this can read; attestation would then "+
				"not be verified at all on a server configured to verify it", rootsFile)
		}
		policy.Roots = pool
	}

	if models != "" {
		policy.AllowedAAGUIDs = map[string]bool{}
		for _, raw := range strings.Split(models, ",") {
			id := normaliseModelID(raw)
			if id == "" {
				continue
			}
			// Checked for length here rather than compared loosely later. An AAGUID is sixteen bytes, and a
			// truncated one in a configuration file would match nothing - so every enrolment would be
			// refused and the operator would be looking at their authenticators rather than at their typo.
			if len(id) != 32 {
				return policy, fmt.Errorf("%q is not an authenticator model identifier; these are 16 "+
					"bytes, written as 32 hex digits with or without dashes", strings.TrimSpace(raw))
			}
			if _, err := hex.DecodeString(id); err != nil {
				return policy, fmt.Errorf("%q is not hexadecimal", strings.TrimSpace(raw))
			}
			policy.AllowedAAGUIDs[id] = true
		}
	}

	if err := policy.Validate(); err != nil {
		return webauthn.AttestationPolicy{}, fmt.Errorf("-passkey-allowed-models: %w", err)
	}

	return policy, nil
}

// normaliseModelID puts an AAGUID into the form the policy compares: lowercase hex, no dashes.
//
// Vendors publish them with dashes and people paste them with whatever case they were given, so both are accepted. A comparison
// that required one exact spelling would fail for a reason nobody could see in their own configuration file.
func normaliseModelID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "")

	return s
}
