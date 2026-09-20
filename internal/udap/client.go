package udap

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The client half: proving who we are to somebody else's authorization server.
//
// Everything here is signed with a private key whose certificate was issued by a trust community. Under TEFCA that certificate comes out
// of QHIN onboarding and cannot be self-issued, which is the one part of this exchange that no amount of code replaces.
//
// The shape of the flow: discover the server's endpoints, register dynamically by presenting a signed software statement and receive a
// client_id, then ask for an access token by signing an authentication token that carries the purpose of use. Three signed documents,
// each with its own claim set and its own lifetime limit, and each rejected outright by a conforming server if a single claim is wrong.

// Identity is who this Perfuse instance is within a trust community.
type Identity struct {
	// ClientURI identifies this client application and its operator, and it has to appear as a URI in the certificate's subject
	// alternative names. That binding is the whole point: without it, any member of the community could register as any other.
	ClientURI string

	// PrivateKey signs the software statement and the authentication tokens.
	PrivateKey *rsa.PrivateKey

	// Chain is the certificate chain, leaf first, exactly as it goes into the x5c header.
	Chain []*x509.Certificate
}

// Validate checks an identity before it is used, rather than letting a server discover the problem.
//
// Worth doing here because the failure is otherwise remote and unhelpful. A server refuses a registration whose iss does not match the
// certificate with a generic invalid_client, and somebody then spends an afternoon deciding whether their key, their certificate, their
// clock or their JSON is at fault.
func (id *Identity) Validate() error {
	if id == nil {
		return errors.New("udap: no identity")
	}

	if strings.TrimSpace(id.ClientURI) == "" {
		return errors.New("udap: the identity has no client URI, which is what a trust community knows this client as")
	}

	if id.PrivateKey == nil {
		return errors.New("udap: the identity has no private key, so nothing can be signed")
	}

	if len(id.Chain) == 0 {
		return errors.New("udap: the identity has no certificate chain, so a server has no way to check the signature")
	}

	leaf := id.Chain[0]

	// The key must actually belong to the certificate. A mismatched pair produces signatures that verify against nothing, and the server
	// reports an invalid signature rather than a mismatched key.
	pub, ok := leaf.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("udap: the certificate carries a %T where an RSA key was expected", leaf.PublicKey)
	}

	if pub.N.Cmp(id.PrivateKey.N) != 0 {
		return errors.New("udap: the private key does not match the certificate, so every signature would be rejected")
	}

	if err := certificateNamesURI(leaf, id.ClientURI); err != nil {
		return fmt.Errorf("udap: this client's own certificate does not name it: %w", err)
	}

	return nil
}

// x5cHeader encodes the chain the way RFC 7515 requires: standard base64 of the DER, leaf first.
func (id *Identity) x5cHeader() []string {
	out := make([]string, 0, len(id.Chain))
	for _, cert := range id.Chain {
		out = append(out, base64.StdEncoding.EncodeToString(cert.Raw))
	}

	return out
}

// signJWT builds and signs a compact JWS with this identity.
func (id *Identity) signJWT(claims map[string]any) (string, error) {
	header := map[string]any{
		"alg": "RS256",
		"x5c": id.x5cHeader(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("udap: encoding the JWT header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("udap: encoding the JWT claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	hashed, digest := digestFor("RS256", []byte(signingInput))

	signature, err := rsa.SignPKCS1v15(rand.Reader, id.PrivateKey, hashed, digest)
	if err != nil {
		return "", fmt.Errorf("udap: signing: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// nonce makes a jti. Reuse of a jti before its expiry is forbidden, so this has to be unpredictable rather than merely unique.
func nonce() (string, error) {
	raw := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("udap: generating a nonce: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RegistrationRequest is what this client wants to be registered as.
type RegistrationRequest struct {
	// ClientName is shown to humans reading the server's registration list.
	ClientName string

	// Contacts must hold at least one mailto address. The specification requires it, and the reason is operational: when an exchange
	// starts failing, the other side needs somebody to tell.
	Contacts []string

	// Scope is the space-delimited list of scopes wanted. For business-to-business exchange these are system scopes.
	Scope string

	// Now is the clock, for tests.
	Now time.Time
}

// SoftwareStatement builds the signed statement that a registration request carries.
//
// Only the client credentials grant is produced. The specification allows either that or the authorization code grant but forbids both in
// one statement, and TEFCA's business-to-business exchange is machine to machine with no user at a browser. Supporting a grant nothing
// here can use would be a configuration option that produces a registration this client cannot then act on.
func (id *Identity) SoftwareStatement(registrationEndpoint string, req RegistrationRequest) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}

	if strings.TrimSpace(registrationEndpoint) == "" {
		return "", errors.New("udap: a software statement needs the registration endpoint as its audience")
	}

	if req.ClientName == "" {
		return "", errors.New("udap: a software statement needs a client_name")
	}

	if len(req.Contacts) == 0 {
		return "", errors.New("udap: a software statement needs at least one contact, and the specification requires a mailto address")
	}

	hasMailto := false

	for _, contact := range req.Contacts {
		if strings.HasPrefix(strings.ToLower(contact), "mailto:") {
			hasMailto = true
		}
	}

	if !hasMailto {
		// Refused rather than passed on. A server that enforces this answers with a generic rejection, and the operator then looks at
		// their certificate rather than at their contact list.
		return "", errors.New("udap: contacts must include a mailto address, so the other side has somebody to tell when an " +
			"exchange starts failing")
	}

	if strings.TrimSpace(req.Scope) == "" {
		return "", errors.New("udap: a software statement needs a scope")
	}

	issued := req.Now
	if issued.IsZero() {
		issued = time.Now()
	}

	jti, err := nonce()
	if err != nil {
		return "", err
	}

	return id.signJWT(map[string]any{
		"iss": id.ClientURI,
		"sub": id.ClientURI,
		"aud": registrationEndpoint,
		"iat": issued.Unix(),
		// Five minutes exactly. The specification says the lifetime SHALL be five minutes, and a statement is for one-time use with one
		// server, so a longer window is a replayable credential rather than a convenience.
		"exp": issued.Add(5 * time.Minute).Unix(),
		"jti": jti,

		"client_name":                req.ClientName,
		"contacts":                   req.Contacts,
		"grant_types":                []string{"client_credentials"},
		"token_endpoint_auth_method": "private_key_jwt",
		"scope":                      req.Scope,
	})
}

// RegistrationResult is what a server says when it registers a client.
type RegistrationResult struct {
	ClientID    string   `json:"client_id"`
	GrantTypes  []string `json:"grant_types"`
	Scope       string   `json:"scope"`
	ClientName  string   `json:"client_name"`
	RawResponse string   `json:"-"`
}

// Cancelled reports whether the server answered with an empty grant list, which the specification defines as confirming a cancellation.
func (r *RegistrationResult) Cancelled() bool {
	return r != nil && len(r.GrantTypes) == 0
}

// Register performs UDAP dynamic client registration and returns the assigned client_id.
func (id *Identity) Register(ctx context.Context, client *http.Client, registrationEndpoint string,
	req RegistrationRequest,
) (*RegistrationResult, error) {
	statement, err := id.SoftwareStatement(registrationEndpoint, req)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]any{
		"software_statement": statement,
		// Fixed "1" per the specification. A string rather than a number, which is what the example shows and what servers check.
		"udap": "1",
	})
	if err != nil {
		return nil, fmt.Errorf("udap: encoding the registration request: %w", err)
	}

	res, raw, err := post(ctx, client, registrationEndpoint, "application/json", body, nil)
	if err != nil {
		return nil, err
	}

	// 201 for a new registration, 200 for a modification of an existing one. Both are success and both carry a client_id.
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("udap: registration was refused with %d: %s", res.StatusCode, firstLine(raw))
	}

	var out RegistrationResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("udap: the registration response is not JSON: %w", err)
	}

	out.RawResponse = string(raw)

	if out.ClientID == "" && !out.Cancelled() {
		return nil, fmt.Errorf("udap: the server accepted the registration and returned no client_id: %s", firstLine(raw))
	}

	return &out, nil
}

// B2BExtension is the hl7-b2b authorization extension object.
//
// This is how the reason for a request travels. Under TEFCA an exchange without a stated purpose of use is not a TEFCA exchange, and the
// receiving organisation decides what to release based on it - so a wrong value here is a disclosure decision made on false information
// rather than a formatting error.
type B2BExtension struct {
	// OrganizationID identifies the requesting organisation and must be a URI. Required.
	OrganizationID string `json:"organization_id"`

	// PurposeOfUse is one or more codes. Required. TEFCA communities constrain the allowed values; treatment is the common one.
	PurposeOfUse []string `json:"purpose_of_use"`

	OrganizationName string `json:"organization_name,omitempty"`

	// SubjectName, SubjectID and SubjectRole describe the person asking, where there is one. Required if known, and in the US realm the
	// identifier is the requestor's National Provider Identifier.
	SubjectName string `json:"subject_name,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	SubjectRole string `json:"subject_role,omitempty"`

	ConsentPolicy    []string `json:"consent_policy,omitempty"`
	ConsentReference []string `json:"consent_reference,omitempty"`
}

// Validate checks the extension before a server does.
func (b *B2BExtension) Validate() error {
	if b == nil {
		return errors.New("udap: no hl7-b2b extension, so the request states no purpose")
	}

	if strings.TrimSpace(b.OrganizationID) == "" {
		return errors.New("udap: the hl7-b2b extension needs an organization_id")
	}

	if _, err := url.Parse(b.OrganizationID); err != nil || !strings.Contains(b.OrganizationID, ":") {
		// The specification requires a URI. A bare name looks reasonable and is refused by conforming servers, which is a confusing
		// failure to receive after a successful registration.
		return fmt.Errorf("udap: organization_id %q is not a URI, and the specification requires one", b.OrganizationID)
	}

	if len(b.PurposeOfUse) == 0 {
		return errors.New("udap: the hl7-b2b extension needs at least one purpose_of_use, because that is what the other side " +
			"decides disclosure on")
	}

	// A named human requestor needs an identifier and a role. The specification makes both conditional on the name being present, and
	// releasing data to a named person whose role is unstated is exactly the case a receiving organisation would want to refuse.
	if b.SubjectName != "" && b.SubjectID == "" && b.SubjectRole == "" {
		return errors.New("udap: a named subject needs subject_id or subject_role, or the other side is told who is asking " +
			"but nothing about their authority to ask")
	}

	if len(b.ConsentReference) > 0 && len(b.ConsentPolicy) == 0 {
		return errors.New("udap: consent_reference without consent_policy, which the specification does not allow")
	}

	return nil
}

// asMap renders the extension for the JWT, omitting empty members.
func (b *B2BExtension) asMap() map[string]any {
	out := map[string]any{
		"version":         "1",
		"organization_id": b.OrganizationID,
		"purpose_of_use":  b.PurposeOfUse,
	}

	for key, value := range map[string]string{
		"organization_name": b.OrganizationName,
		"subject_name":      b.SubjectName,
		"subject_id":        b.SubjectID,
		"subject_role":      b.SubjectRole,
	} {
		if value != "" {
			out[key] = value
		}
	}

	if len(b.ConsentPolicy) > 0 {
		out["consent_policy"] = b.ConsentPolicy
	}

	if len(b.ConsentReference) > 0 {
		out["consent_reference"] = b.ConsentReference
	}

	return out
}

// TokenRequest asks for an access token.
type TokenRequest struct {
	// ClientID is what the registration returned.
	ClientID string

	// TokenEndpoint must be the endpoint from the verified signed metadata, not the unsigned document.
	TokenEndpoint string

	// Scope is optional at the token endpoint; the server may narrow what was registered.
	Scope string

	// B2B carries the purpose of use. Required for the client credentials grant.
	B2B *B2BExtension

	// Now is the clock, for tests.
	Now time.Time
}

// AuthenticationToken builds the signed client assertion for a token request.
func (id *Identity) AuthenticationToken(req TokenRequest) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}

	if strings.TrimSpace(req.ClientID) == "" {
		return "", errors.New("udap: an authentication token needs the client_id the server assigned at registration")
	}

	if strings.TrimSpace(req.TokenEndpoint) == "" {
		return "", errors.New("udap: an authentication token needs the token endpoint as its audience")
	}

	if err := req.B2B.Validate(); err != nil {
		return "", err
	}

	issued := req.Now
	if issued.IsZero() {
		issued = time.Now()
	}

	jti, err := nonce()
	if err != nil {
		return "", err
	}

	// The audience is the token endpoint. That is what stops a token request captured by one server being replayed at another: a
	// conforming server refuses an assertion whose aud is not itself.
	return id.signJWT(map[string]any{
		"iss": req.ClientID,
		"sub": req.ClientID,
		"aud": req.TokenEndpoint,
		"iat": issued.Unix(),
		// Five minutes is the maximum the specification allows. Anything longer is a bearer credential for whoever intercepts it.
		"exp":        issued.Add(5 * time.Minute).Unix(),
		"jti":        jti,
		"extensions": map[string]any{"hl7-b2b": req.B2B.asMap()},
	})
}

// AccessToken is a successful token response.
type AccessToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`

	// Obtained is when this client received it, so expiry can be judged without trusting the token's own clock.
	Obtained time.Time `json:"-"`
}

// Expired reports whether the token has passed its lifetime, with a margin so a request is not started with a token about to die.
func (t *AccessToken) Expired(now time.Time) bool {
	if t == nil || t.AccessToken == "" {
		return true
	}

	if t.ExpiresIn <= 0 {
		// No lifetime given. Treated as immediately stale rather than eternal: a token cached for ever is a token used after it was
		// revoked.
		return true
	}

	return now.After(t.Obtained.Add(time.Duration(t.ExpiresIn)*time.Second - 30*time.Second))
}

// TokenError is an OAuth error response, kept structured because its code is the most useful diagnostic in the whole flow.
type TokenError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"-"`
	Raw         string `json:"-"`
}

func (e *TokenError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("udap: the authorization server refused the token request: %s (%s)", e.Code, e.Description)
	}

	return fmt.Sprintf("udap: the authorization server refused the token request: %s", e.Code)
}

// RequestToken exchanges a signed authentication token for an access token.
func (id *Identity) RequestToken(ctx context.Context, client *http.Client, req TokenRequest) (*AccessToken, error) {
	assertion, err := id.AuthenticationToken(req)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	// Fixed "1", and its absence is how a server tells a UDAP request from an ordinary OAuth one.
	form.Set("udap", "1")

	if req.Scope != "" {
		form.Set("scope", req.Scope)
	}

	// Deliberately no Authorization header and no client secret. The specification forbids both for this authentication method, and
	// sending one makes a server treat the request as a different kind of client entirely.
	res, raw, err := post(ctx, client, req.TokenEndpoint, "application/x-www-form-urlencoded", []byte(form.Encode()), nil)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		tokenErr := &TokenError{Status: res.StatusCode, Raw: string(raw)}
		if err := json.Unmarshal(raw, tokenErr); err != nil || tokenErr.Code == "" {
			tokenErr.Code = fmt.Sprintf("http %d", res.StatusCode)
			tokenErr.Description = firstLine(raw)
		}

		return nil, tokenErr
	}

	var token AccessToken
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, fmt.Errorf("udap: the token response is not JSON: %w", err)
	}

	if token.AccessToken == "" {
		return nil, fmt.Errorf("udap: the server answered 200 with no access token: %s", firstLine(raw))
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}

	token.Obtained = now

	// Sixty minutes is the maximum the specification allows. A longer one is recorded rather than refused, because refusing an
	// otherwise usable token would break exchange with a server that is merely generous, but it is worth knowing about.
	if token.ExpiresIn > 3600 {
		token.ExpiresIn = 3600
	}

	return &token, nil
}

// post sends a request and reads a bounded response.
func post(ctx context.Context, client *http.Client, target, contentType string, body []byte,
	header http.Header,
) (*http.Response, []byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("udap: building a request to %s: %w", target, err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("udap: posting to %s: %w", target, err)
	}

	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("udap: reading the response from %s: %w", target, err)
	}

	return res, raw, nil
}

// firstLine trims a response body down to something readable in an error message.
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

// unusedCrypto keeps the crypto import meaningful if the signer is generalised beyond RSA.
var _ crypto.Hash = crypto.SHA256
