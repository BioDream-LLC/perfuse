package tefca

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/udap"
)

// Facilitated FHIR exchange, per the Sequoia Project SOP effective 8 March 2026.
//
// What this is and what it is not. The SOP describes a FHIR query between TEFCA participants, secured by UDAP - a public key
// infrastructure over OAuth 2.0 in which a trust community issues certificates and members authenticate by signing JWTs with them. That
// security layer is implemented in internal/udap and verified against a live reference server: a registration signed here was refused by
// securedcontrols.net with "Untrusted: Certificate is not a member of community", which means the document was parsed, its signature
// checked and its chain walked before membership was judged.
//
// The part that cannot be verified from here is membership itself. A certificate issued through QHIN onboarding is not something code
// produces, and without one no real exchange completes. So this refuses to start without a configured identity and trust anchors rather
// than pretending, and the refusal names what is missing.
//
// The honest summary: the transport is built and the security is verified against somebody else's implementation. Whether a particular
// QHIN accepts it is unknown until somebody with credentials tries.

// ExchangeConfig is what a Facilitated FHIR exchange needs beyond the participant details in TEFCAConfig.
type ExchangeConfig struct {
	// PartnerFHIRBase is the FHIR base URL of the responding participant. UDAP metadata is discovered relative to it.
	PartnerFHIRBase string `yaml:"partner_fhir_base"`

	// ClientURI identifies this client to the trust community and must appear as a URI in the certificate's subject alternative names.
	ClientURI string `yaml:"client_uri"`

	// TrustAnchorPath holds the community's certificate authorities in PEM form.
	//
	// Required. Verifying a server's signed metadata against the certificate that arrived inside it proves only that one party made both,
	// which was a real SAML defect in this repository: the verifier preferred the embedded certificate and authenticated an attacker as
	// an administrator while every test passed.
	TrustAnchorPath string `yaml:"trust_anchor_path"`

	// ClientName and Contacts go into the software statement at registration. The specification requires a mailto address so that the
	// other side has somebody to tell when an exchange starts failing.
	ClientName string   `yaml:"client_name"`
	Contacts   []string `yaml:"contacts"`

	// Scope is the space-delimited list requested at registration. System scopes, since this is machine to machine.
	Scope string `yaml:"scope"`

	// ClientID, once a server has assigned one. Empty means register on first use.
	ClientID string `yaml:"client_id,omitempty"`

	// AllowUnanchoredMetadata relaxes chain verification of the partner's signed metadata.
	//
	// For sandboxes whose community authority nobody would install. Never for production: with it on, any party can sign a metadata
	// document and be believed about where to send client assertions.
	AllowUnanchoredMetadata bool `yaml:"allow_unanchored_metadata,omitempty"`
}

// Validate checks the exchange configuration.
func (e ExchangeConfig) Validate() error {
	if strings.TrimSpace(e.PartnerFHIRBase) == "" {
		return errors.New("tefca: partner_fhir_base is required, since that is where the partner's UDAP metadata is discovered")
	}

	if strings.TrimSpace(e.ClientURI) == "" {
		return errors.New("tefca: client_uri is required and must match a URI in the certificate's subject alternative names")
	}

	if strings.TrimSpace(e.ClientName) == "" {
		return errors.New("tefca: client_name is required for the software statement")
	}

	hasMailto := false

	for _, contact := range e.Contacts {
		if strings.HasPrefix(strings.ToLower(contact), "mailto:") {
			hasMailto = true
		}
	}

	if !hasMailto {
		return errors.New("tefca: contacts must include a mailto address, because the specification requires one and because a " +
			"failing exchange needs somebody to tell")
	}

	if strings.TrimSpace(e.Scope) == "" {
		return errors.New("tefca: scope is required at registration")
	}

	if strings.TrimSpace(e.TrustAnchorPath) == "" && !e.AllowUnanchoredMetadata {
		return errors.New("tefca: trust_anchor_path is required. Without the community's certificate authorities, a partner's signed " +
			"metadata can only be checked against the certificate that arrived with it, which proves nothing. Set " +
			"allow_unanchored_metadata only against a sandbox")
	}

	return nil
}

// Exchange is a configured Facilitated FHIR client.
//
// Holds the registered client_id and the current access token, because re-registering on every query would be both rude and slow, and
// re-fetching a token that has fifty minutes left is a request nobody needs.
type Exchange struct {
	participant TEFCAConfig
	cfg         ExchangeConfig

	identity *udap.Identity
	anchors  udap.TrustAnchors

	client *http.Client

	mu       sync.Mutex
	clientID string
	token    *udap.AccessToken

	// now is the clock, injectable for tests.
	now func() time.Time
}

// NewExchange builds an exchange from configuration, reading the key, certificate and trust anchors from disk.
//
// Refuses at construction rather than at first use. A participant who has misconfigured their certificate should find out when the server
// starts, not when the first clinician is waiting for a record - which is the standing rule in this repository that a feature which
// cannot work is refused at load.
func NewExchange(participant TEFCAConfig, cfg ExchangeConfig, client *http.Client) (*Exchange, error) {
	if err := participant.Validate(); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	identity, err := loadIdentity(cfg.ClientURI, participant.CertificatePath, participant.KeyPath)
	if err != nil {
		return nil, err
	}

	anchors := udap.TrustAnchors{}

	if cfg.TrustAnchorPath != "" {
		pool, err := loadPool(cfg.TrustAnchorPath)
		if err != nil {
			return nil, err
		}

		// The same bundle serves as roots and as intermediates, which is not laziness.
		//
		// A UDAP server is only required to put its leaf certificate in the x5c header, and the public reference sandbox does exactly
		// that - so a client holding only the community root cannot build a path and the failure reads as "certificate signed by unknown
		// authority", which sends somebody looking for the wrong problem. Putting the whole bundle in both pools means one file
		// containing a community's root and its intermediates just works, and it costs nothing: only a self-signed certificate can
		// actually anchor a chain, so listing an intermediate as a root does not make it one.
		anchors.Roots = pool
		anchors.Intermediates = pool
	}

	if client == nil {
		// A long timeout, because a record retrieval crosses organisations and a query that gives up in five seconds fails against
		// perfectly healthy partners.
		client = &http.Client{Timeout: 60 * time.Second}
	}

	return &Exchange{
		participant: participant,
		cfg:         cfg,
		identity:    identity,
		anchors:     anchors,
		client:      client,
		clientID:    cfg.ClientID,
		now:         time.Now,
	}, nil
}

// loadIdentity reads a private key and certificate chain from disk.
func loadIdentity(clientURI, certPath, keyPath string) (*udap.Identity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("tefca: reading the certificate at %s: %w", certPath, err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("tefca: reading the private key at %s: %w", keyPath, err)
	}

	pair, err := tlsKeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	identity := &udap.Identity{ClientURI: clientURI, PrivateKey: pair.key, Chain: pair.chain}

	// Checked here so a misconfiguration is a startup error naming the problem, rather than an invalid_client from a partner naming
	// nothing.
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("tefca: %w", err)
	}

	return identity, nil
}

// loadPool reads PEM certificates into a pool.
func loadPool(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tefca: reading trust anchors at %s: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("tefca: no certificates found in %s", path)
	}

	return pool, nil
}

// Discover fetches and verifies the partner's UDAP metadata.
//
// Separated from the query so the endpoints can be checked without exchanging anything, which is what the GUI's test button does and what
// somebody debugging an onboarding needs first.
func (e *Exchange) Discover(ctx context.Context) (*udap.Metadata, *udap.SignedMetadataClaims, error) {
	md, err := udap.Fetch(ctx, e.client, e.cfg.PartnerFHIRBase, "")
	if err != nil {
		return nil, nil, err
	}

	claims, err := udap.VerifySignedMetadata(md, udap.VerifyOptions{
		BaseURL:         e.cfg.PartnerFHIRBase,
		Anchors:         e.anchors,
		AllowUnanchored: e.cfg.AllowUnanchoredMetadata,
		Now:             e.now(),
	})
	if err != nil {
		return nil, nil, err
	}

	if !md.SupportsClientCredentials() {
		return nil, nil, fmt.Errorf("tefca: %s does not offer the client_credentials grant, so machine-to-machine exchange is not "+
			"possible with it", e.cfg.PartnerFHIRBase)
	}

	if !md.SupportsB2BExtension() {
		// Refused rather than attempted. Without hl7-b2b there is nowhere to state a purpose of use, and a TEFCA exchange with no stated
		// purpose is not one.
		return nil, nil, fmt.Errorf("tefca: %s does not support the hl7-b2b authorization extension, so a purpose of use cannot be "+
			"stated", e.cfg.PartnerFHIRBase)
	}

	return md, claims, nil
}

// accessToken returns a usable token, registering and requesting as needed.
//
// The endpoints come from the verified claims rather than the document body. Using the unsigned copy would make the signature decorative:
// an attacker who can rewrite the response leaves signed_metadata alone, points token_endpoint at their own server, and collects client
// assertions.
func (e *Exchange) accessToken(ctx context.Context, purposes []string) (*udap.AccessToken, error) {
	_, claims, err := e.Discover(ctx)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.token != nil && !e.token.Expired(e.now()) {
		return e.token, nil
	}

	if e.clientID == "" {
		result, err := e.identity.Register(ctx, e.client, claims.RegistrationEndpoint, udap.RegistrationRequest{
			ClientName: e.cfg.ClientName,
			Contacts:   e.cfg.Contacts,
			Scope:      e.cfg.Scope,
			Now:        e.now(),
		})
		if err != nil {
			return nil, err
		}

		e.clientID = result.ClientID
	}

	token, err := e.identity.RequestToken(ctx, e.client, udap.TokenRequest{
		ClientID:      e.clientID,
		TokenEndpoint: claims.TokenEndpoint,
		Scope:         e.cfg.Scope,
		B2B: &udap.B2BExtension{
			// The organisation identifier has to be a URI. An OID is expressed as a urn:oid, which is the form a trust community
			// recognises - a bare dotted number is refused, and the refusal does not say why.
			OrganizationID:   organizationURI(e.participant.OrganizationOID),
			OrganizationName: e.participant.OrganizationName,
			PurposeOfUse:     purposes,
		},
		Now: e.now(),
	})
	if err != nil {
		return nil, err
	}

	e.token = token

	return token, nil
}

// organizationURI renders an organisation identifier as the URI the specification requires.
func organizationURI(oid string) string {
	trimmed := strings.TrimSpace(oid)
	if trimmed == "" {
		return ""
	}

	// Already a URI of some kind.
	if strings.Contains(trimmed, ":") {
		return trimmed
	}

	return "urn:oid:" + trimmed
}

// QueryPatient performs a Facilitated FHIR patient search against the partner and returns the bundle.
//
// The search parameters are the ones the SOP's patient matching section rests on. Matching on a name alone is refused by the validation in
// Query below, and for a reason worth restating: a wrong match does not produce an error, it produces somebody else's medical record.
func (e *Exchange) QueryPatient(ctx context.Context, req QueryRequest) (*FHIRBundle, error) {
	if err := validateQuery(req); err != nil {
		return nil, err
	}

	purposes := []string{purposeURI(req.Purpose)}

	token, err := e.accessToken(ctx, purposes)
	if err != nil {
		return nil, err
	}

	target, err := patientSearchURL(e.cfg.PartnerFHIRBase, req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("tefca: building the patient query: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	httpReq.Header.Set("Accept", "application/fhir+json")

	res, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tefca: querying %s: %w", e.cfg.PartnerFHIRBase, err)
	}

	defer func() { _ = res.Body.Close() }()

	// Bounded. A bundle is large but not unlimited, and an unbounded read from another organisation's server is a denial of service
	// waiting for a bad day.
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("tefca: reading the response from %s: %w", e.cfg.PartnerFHIRBase, err)
	}

	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		// The token is dropped so the next attempt gets a fresh one. A cached token that the partner has stopped accepting otherwise
		// fails every subsequent query for the rest of its nominal life.
		e.mu.Lock()
		e.token = nil
		e.mu.Unlock()
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tefca: %s answered %d: %s", target, res.StatusCode, firstLine(body))
	}

	var bundle FHIRBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		return nil, fmt.Errorf("tefca: the partner's response is not a FHIR bundle: %w", err)
	}

	return &bundle, nil
}

// patientSearchURL builds the search, with only the parameters that were supplied.
func patientSearchURL(base string, req QueryRequest) (string, error) {
	trimmed := strings.TrimRight(base, "/")

	parsed, err := url.Parse(trimmed + "/Patient")
	if err != nil {
		return "", fmt.Errorf("tefca: %q is not a usable FHIR base: %w", base, err)
	}

	query := url.Values{}

	if req.PatientID != "" {
		query.Set("identifier", req.PatientID)
	}

	if req.PatientName != "" {
		query.Set("name", req.PatientName)
	}

	if req.DOB != "" {
		query.Set("birthdate", req.DOB)
	}

	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

// purposeURI renders a purpose of use as the URI form the specification prefers.
func purposeURI(purpose string) string {
	if strings.Contains(purpose, ":") {
		return purpose
	}

	// The HL7 PurposeOfUse code system. Communities constrain the allowed values and are encouraged to draw from it.
	return "urn:oid:2.16.840.1.113883.5.8#" + strings.ToUpper(purpose)
}

// firstLine trims a body for an error message.
func firstLine(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}

	if len(text) > 300 {
		text = text[:300] + "…"
	}

	return text
}

// keyPair is a parsed certificate and key.
type keyPair struct {
	key   *rsa.PrivateKey
	chain []*x509.Certificate
}

// parsePrivateKey reads a PEM private key in either of the two forms people actually have.
//
// Both PKCS#1 and PKCS#8 are accepted because both are what openssl produces depending on its arguments, and refusing one would be a
// configuration error that reads as a corrupt key.
func parsePrivateKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	rest := keyPEM

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if !strings.Contains(block.Type, "PRIVATE KEY") {
			continue
		}

		if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return key, nil
		}

		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			continue
		}

		if key, ok := parsed.(*rsa.PrivateKey); ok {
			return key, nil
		}

		return nil, fmt.Errorf("tefca: the private key is a %T, and UDAP signing here needs RSA", parsed)
	}

	return nil, errors.New("tefca: no private key found in the key file")
}

// tlsKeyPair parses PEM blocks into a chain and a key.
func tlsKeyPair(certPEM, keyPEM []byte) (*keyPair, error) {
	var chain []*x509.Certificate

	rest := certPEM

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("tefca: parsing a certificate: %w", err)
		}

		chain = append(chain, cert)
	}

	if len(chain) == 0 {
		return nil, errors.New("tefca: no certificates in the certificate file")
	}

	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}

	return &keyPair{key: key, chain: chain}, nil
}
