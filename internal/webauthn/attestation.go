package webauthn

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Verifying attestation, when an operator has said what to verify it against.
//
// Attestation answers one question: is this credential on a genuine device of a particular model. It never answered "is this
// legitimate" - the origin binding, the challenge and the private key staying on the authenticator do that, and they do it
// whether attestation is verified or not.
//
// It was deliberately not verified here, and the reasoning was that proper verification needs a maintained root store of
// authenticator vendor certificates, and that half-verifying is worse than not: a chain checked against no roots produces a
// confident-looking result that means nothing.
//
// That reasoning holds, and it turns out to point at a narrower answer rather than at no answer. The problem was never the
// verification - it is a certificate chain and a signature. The problem was who maintains the roots. So the roots are supplied
// by the operator, for the models they have approved, which is exactly the situation where attestation is worth anything: a
// hospital that has bought two thousand of one security key and wants to refuse anything else.
//
// With no roots configured, nothing is verified and nothing claims to be. That is the same position as before, stated in one
// place instead of a comment.

// ErrAttestation means the attestation statement was rejected.
var ErrAttestation = errors.New("webauthn: the authenticator's attestation was not accepted")

// AttestationPolicy is what an operator requires of an authenticator.
type AttestationPolicy struct {
	// Roots are the vendor certificates a chain must reach. Empty means attestation is not verified.
	//
	// Supplied rather than bundled. A root store shipped with the application is a root store nobody updates, and an
	// expired root in it turns every registration into a support call - whereas an operator who configured these knows
	// what they configured and why.
	Roots *x509.CertPool

	// AllowedAAGUIDs restricts registration to these authenticator models, keyed lowercase without dashes.
	//
	// Only meaningful with Roots set, and refused without them. The AAGUID inside the authenticator data is
	// self-asserted: without a verified attestation, a browser can claim any model it likes, so an allow-list on its
	// own is theatre that reads as a control.
	AllowedAAGUIDs map[string]bool

	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Configured reports whether attestation is verified at all.
func (p AttestationPolicy) Configured() bool {
	return p.Roots != nil && len(p.Roots.Subjects()) > 0
}

// Validate refuses a policy that cannot mean what it says.
//
// Called at startup rather than at registration. A misconfigured policy that only reveals itself when somebody tries to enrol a
// passkey is one that reveals itself to the person least able to fix it.
func (p AttestationPolicy) Validate() error {
	if len(p.AllowedAAGUIDs) > 0 && !p.Configured() {
		return fmt.Errorf("an authenticator allow-list was given without any attestation roots, so it would " +
			"decide nothing: the model identifier inside a registration is self-asserted, and without a " +
			"verified attestation a browser can claim any model it likes")
	}

	return nil
}

// aaguidExtension is the OID carrying the AAGUID inside an attestation certificate.
//
// 1.3.6.1.4.1.45724.1.1.4, from the FIDO specification. Checked against the AAGUID in the authenticator data, because that one is
// outside the certificate and therefore not attested by it - a device could present a genuine certificate for one model and claim
// to be another, and the allow-list would then be checking the claim rather than the certificate.
var aaguidExtension = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 45724, 1, 1, 4}

// verifyAttestation checks a statement against the policy.
//
// Returns the verified AAGUID when there is one. An empty return with no error means attestation was not verified, which the
// caller must not treat as verified - hence the bool rather than a nil-means-fine convention.
func verifyAttestation(
	policy AttestationPolicy,
	format string,
	statement CBORValue,
	authData []byte,
	clientDataHash []byte,
	claimedAAGUID []byte,
) (verified bool, err error) {
	if !policy.Configured() {
		return false, nil
	}

	// "none" is what an authenticator sends when it was not asked for attestation, or declined.
	//
	// Refused when a policy is configured, and this is the case that matters most. An operator who has configured
	// roots is saying they require a known device; accepting a registration that asserts nothing would satisfy the
	// letter of the configuration and none of its purpose.
	if format == "none" {
		return false, fmt.Errorf("%w: this authenticator provided no attestation, and this server is "+
			"configured to require one", ErrAttestation)
	}

	if format != "packed" {
		// Refused rather than skipped. The formats not handled here - tpm, android-key, android-safetynet,
		// apple - each need their own verification, and treating an unhandled format as acceptable would mean
		// the policy could be bypassed by presenting one.
		return false, fmt.Errorf("%w: attestation format %q is not verified by this server; configure "+
			"authenticators that use the packed format, or remove the attestation roots to accept any",
			ErrAttestation, format)
	}

	chain, err := attestationChain(statement)
	if err != nil {
		return false, err
	}

	leaf := chain[0]

	// The signature covers the authenticator data and the client data hash, concatenated in that order.
	signed := make([]byte, 0, len(authData)+len(clientDataHash))
	signed = append(signed, authData...)
	signed = append(signed, clientDataHash...)

	sig, ok := statement.MapEntryText("sig")
	if !ok || len(sig.Bytes) == 0 {
		return false, fmt.Errorf("%w: the attestation statement carries no signature", ErrAttestation)
	}

	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, signed, sig.Bytes); err != nil {
		return false, fmt.Errorf("%w: the attestation signature is not valid: %v", ErrAttestation, err)
	}

	// The chain, against the roots the operator supplied.
	//
	// KeyUsages is set to any: an attestation certificate is not a TLS certificate, and the default check would
	// refuse every legitimate one for not being marked for server authentication.
	now := time.Now
	if policy.Now != nil {
		now = policy.Now
	}

	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         policy.Roots,
		Intermediates: intermediates,
		CurrentTime:   now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return false, fmt.Errorf("%w: the attestation certificate does not chain to a configured root: %v",
			ErrAttestation, err)
	}

	// The AAGUID in the certificate must match the one the authenticator reported.
	//
	// Without this the allow-list checks a self-asserted value. A device holding a genuine certificate for model A
	// could report model B, and an operator who allowed only B would have admitted A.
	if certAAGUID, ok := aaguidFromCertificate(leaf); ok {
		if !sameBytes(certAAGUID, claimedAAGUID) {
			return false, fmt.Errorf("%w: the authenticator reported model %s but its certificate attests "+
				"to %s", ErrAttestation, formatAAGUID(claimedAAGUID), formatAAGUID(certAAGUID))
		}
	}

	if len(policy.AllowedAAGUIDs) > 0 {
		key := normaliseAAGUID(claimedAAGUID)
		if !policy.AllowedAAGUIDs[key] {
			// The refusal names the model, because the operator needs it to add the model if they meant to.
			// It does not list the allowed models: a person enrolling a key cannot act on that, and it is
			// inventory information.
			return false, fmt.Errorf("%w: authenticator model %s is not on this server's list of approved "+
				"models", ErrAttestation, formatAAGUID(claimedAAGUID))
		}
	}

	return true, nil
}

// attestationChain pulls the certificate chain out of a packed statement.
func attestationChain(statement CBORValue) ([]*x509.Certificate, error) {
	x5c, ok := statement.MapEntryText("x5c")
	if !ok || len(x5c.Array) == 0 {
		// Self attestation, which carries no certificate. Refused under a policy for the same reason "none" is:
		// an operator requiring a known device is not served by a device vouching for itself.
		return nil, fmt.Errorf("%w: this attestation carries no certificate, so there is nothing to check "+
			"against the configured roots", ErrAttestation)
	}

	var chain []*x509.Certificate
	for i, entry := range x5c.Array {
		if len(entry.Bytes) == 0 {
			return nil, fmt.Errorf("%w: certificate %d in the chain is empty", ErrAttestation, i)
		}
		cert, err := x509.ParseCertificate(entry.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: certificate %d in the chain could not be read: %v",
				ErrAttestation, i, err)
		}
		chain = append(chain, cert)
	}

	return chain, nil
}

// aaguidFromCertificate reads the AAGUID extension, if present.
//
// Absent is not an error. The extension is required for a packed attestation with a certificate, but a device that omits it is
// still presenting a chain that verifies - so the chain is honoured and the model is left unconfirmed rather than the whole
// registration refused for a missing field.
func aaguidFromCertificate(cert *x509.Certificate) ([]byte, bool) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(aaguidExtension) {
			continue
		}

		// The extension value is an OCTET STRING wrapping the sixteen raw bytes.
		var raw []byte
		if _, err := asn1.Unmarshal(ext.Value, &raw); err != nil {
			return nil, false
		}
		if len(raw) != 16 {
			return nil, false
		}

		return raw, true
	}

	return nil, false
}

// normaliseAAGUID renders an AAGUID for comparison: lowercase hex, no dashes.
func normaliseAAGUID(b []byte) string { return strings.ToLower(hex.EncodeToString(b)) }

// formatAAGUID renders an AAGUID the way vendors publish it, for an error message.
func formatAAGUID(b []byte) string {
	if len(b) != 16 {
		return "(unknown)"
	}
	h := hex.EncodeToString(b)

	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// sameBytes compares without regard to length assumptions.
//
// Not constant time, deliberately: both values are public identifiers of a hardware model, and treating a model number as a
// secret would be cargo cult rather than caution.
func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// clientDataHash is the SHA-256 of the client data, which the attestation signature covers.
func clientDataHashOf(clientDataJSON []byte) []byte {
	sum := sha256.Sum256(clientDataJSON)

	return sum[:]
}
