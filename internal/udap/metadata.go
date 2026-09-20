// Package udap implements the client half of HL7 Security for Scalable Registration, Authentication, and Authorization — the
// specification TEFCA's Facilitated FHIR exchange rests on.
//
// UDAP is a public key infrastructure bolted onto OAuth 2.0. A trust community issues X.509 certificates to its members; a member
// proves who it is by signing JWTs with the private key and attaching the certificate chain in the JWT header. That replaces the shared
// client secret of ordinary OAuth, which does not scale to thousands of organisations who have never met.
//
// Why this package exists at all. The TEFCA item in the queue said the exchange "cannot be written speculatively: it is tested against a
// real QHIN or it is not tested". That was half right. Joining a QHIN needs onboarding and issued certificates and cannot be faked. But
// the security profile underneath it is a published HL7 implementation guide with public reference servers, and one of them answers on
// the open internet. So the part that can be verified is verified here, and the part that cannot is refused at the boundary rather than
// guessed at.
//
// The distinction matters because of what happened with SAML in this repository: a thousand lines of tests passed, five of them
// specifically about signature canonicalisation, while no real identity provider could authenticate anybody. Both halves had been
// written here and agreed with each other. The lesson taken from it is that a security implementation is worth nothing until something
// somebody else wrote has accepted or rejected it.
//
// Specification references:
//   - HL7 UDAP Security STU 1: http://hl7.org/fhir/us/udap-security/STU1/
//   - TEFCA SOP Facilitated FHIR Implementation v2.0, effective 8 March 2026
package udap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotSupported means the server answered 404 at its UDAP metadata URL.
//
// The specification gives that a specific meaning: a client SHOULD conclude the server does not support UDAP workflows. Distinguished
// from every other failure because it is an answer rather than a fault - the server is up, it simply does not do this.
var ErrNotSupported = errors.New("udap: the server does not support UDAP workflows")

// Metadata is a server's UDAP metadata document.
//
// Field names follow the specification exactly rather than Go convention, because the wire names are the contract and a translation
// layer between them is one more place for a typo to become a silent absence. The struct tags are what matter.
type Metadata struct {
	UDAPVersionsSupported []string `json:"udap_versions_supported"`
	UDAPProfilesSupported []string `json:"udap_profiles_supported"`

	// AuthorizationExtensionsSupported lists the authorization extension objects the server understands. For business-to-business
	// exchange under TEFCA this must include hl7-b2b, which is how the purpose of use and the requesting organisation travel.
	AuthorizationExtensionsSupported []string `json:"udap_authorization_extensions_supported"`
	AuthorizationExtensionsRequired  []string `json:"udap_authorization_extensions_required"`

	CertificationsSupported []string `json:"udap_certifications_supported"`
	CertificationsRequired  []string `json:"udap_certifications_required"`

	GrantTypesSupported []string `json:"grant_types_supported"`
	ScopesSupported     []string `json:"scopes_supported"`

	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`

	TokenEndpointAuthMethodsSupported       []string `json:"token_endpoint_auth_methods_supported"`
	TokenEndpointAuthSigningAlgValues       []string `json:"token_endpoint_auth_signing_alg_values_supported"`
	RegistrationEndpointJWTSigningAlgValues []string `json:"registration_endpoint_jwt_signing_alg_values_supported"`

	// SignedMetadata is a JWT repeating the endpoints, signed with the server's community certificate. It is the only part of this
	// document that carries any assurance; everything above it arrived over a transport and could have been rewritten.
	SignedMetadata string `json:"signed_metadata"`
}

// SupportsClientCredentials reports whether the server offers the grant TEFCA's business-to-business exchange uses.
func (m *Metadata) SupportsClientCredentials() bool {
	return containsFold(m.GrantTypesSupported, "client_credentials")
}

// SupportsB2BExtension reports whether the server understands the hl7-b2b authorization extension object.
//
// Worth checking before a token request rather than after a refusal: without it there is nowhere to put the purpose of use, and a
// TEFCA exchange with no stated purpose is not a TEFCA exchange.
func (m *Metadata) SupportsB2BExtension() bool {
	return containsFold(m.AuthorizationExtensionsSupported, "hl7-b2b")
}

// RequiresB2BExtension reports whether the server demands hl7-b2b in every token request.
func (m *Metadata) RequiresB2BExtension() bool {
	return containsFold(m.AuthorizationExtensionsRequired, "hl7-b2b")
}

// SupportsDynamicRegistration reports whether the server accepts UDAP dynamic client registration.
func (m *Metadata) SupportsDynamicRegistration() bool {
	return containsFold(m.UDAPProfilesSupported, "udap_dcr")
}

// MetadataURL builds the well-known URL for a FHIR base URL.
//
// The community parameter is optional in the specification and exists for servers holding certificates from more than one trust
// community: it tells the server which community's certificate to sign its metadata with. Without it a server may pick one the client
// does not trust, and the failure looks like a bad signature rather than a mismatched community.
func MetadataURL(baseURL, community string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return "", errors.New("udap: no base URL")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("udap: base URL %q: %w", baseURL, err)
	}

	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		// Refused rather than allowed with a warning. Every assurance in UDAP rests on certificates, and a metadata document fetched
		// over plain HTTP can be replaced wholesale in transit - including the signed part, by an attacker who simply supplies their
		// own. Localhost is permitted because that is where reference servers and tests live.
		return "", fmt.Errorf("udap: %q is not https, so its metadata could be replaced in transit", baseURL)
	}

	out := trimmed + "/.well-known/udap"

	if community != "" {
		out += "?community=" + url.QueryEscape(community)
	}

	return out, nil
}

// Fetch retrieves a server's UDAP metadata.
//
// Deliberately does no verification. Fetching and trusting are separate steps with separate failure modes, and keeping them apart is
// what makes it possible to write a test that fetches a real document and then proves the verifier rejects a tampered copy of it.
func Fetch(ctx context.Context, client *http.Client, baseURL, community string) (*Metadata, error) {
	target, err := MetadataURL(baseURL, community)
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("udap: building the metadata request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("udap: fetching %s: %w", target, err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotSupported
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("udap: %s answered %d", target, res.StatusCode)
	}

	// Bounded. A metadata document is a few kilobytes; anything enormous is either a fault or a deliberate attempt to exhaust memory
	// before authentication has happened at all.
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("udap: reading %s: %w", target, err)
	}

	var md Metadata
	if err := json.Unmarshal(body, &md); err != nil {
		return nil, fmt.Errorf("udap: %s did not return UDAP metadata: %w", target, err)
	}

	return &md, nil
}

// containsFold reports whether list holds value, ignoring case.
func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}

	return false
}
