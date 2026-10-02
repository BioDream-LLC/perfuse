package oidc

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims is what a provider said about a person.
//
// A small set on purpose. Every claim read here is one Perfuse depends on, and a claim it does not read cannot become an
// accidental part of the security model.
type Claims struct {
	// Subject is the provider's stable identifier for the person. Never a name or an email.
	//
	// This is what a Perfuse account is keyed on, and the reason matters: an email address is reassigned when somebody
	// leaves, and keying on it means their replacement inherits their access.
	Subject string

	// Issuer is which provider vouched for them.
	Issuer string

	// Email and EmailVerified are for display and for matching an existing local account.
	Email         string
	EmailVerified bool

	// Name is for display only.
	Name string

	// PreferredUsername is for display only, and is deliberately not used for identity.
	PreferredUsername string

	// Groups are the provider's group claims, used to decide a role.
	Groups []string

	// ExpiresAt and IssuedAt bound the token's life.
	ExpiresAt time.Time
	IssuedAt  time.Time

	// Patient is the SMART launch context, naming the one patient a token is for.
	//
	// A top-level claim rather than something inside scope, which is how SMART defines it: the scopes say what kinds
	// of resource an app may touch and this says whose.
	Patient string

	// Encounter is the SMART encounter launch context.
	Encounter string

	// Scope is the space-separated scope claim, present on an access token and normally absent on an ID token.
	//
	// Carried through verification rather than parsed separately by the caller, so that whatever reads the scopes is
	// reading a value from a token whose signature, issuer, audience and expiry have already been checked. A caller
	// that decoded the payload itself to find the scopes would be reading a string a stranger wrote.
	Scope string
}

// claimSet is the wire form.
type claimSet struct {
	Issuer            string          `json:"iss"`
	Subject           string          `json:"sub"`
	Audience          json.RawMessage `json:"aud"`
	Expiry            int64           `json:"exp"`
	IssuedAt          int64           `json:"iat"`
	Nonce             string          `json:"nonce"`
	Email             string          `json:"email"`
	EmailVerified     *bool           `json:"email_verified"`
	Name              string          `json:"name"`
	PreferredUsername string          `json:"preferred_username"`
	Groups            []string        `json:"groups"`
	Scope             string          `json:"scope"`
	Patient           string          `json:"patient"`
	Encounter         string          `json:"encounter"`
}

// VerifyOptions is what an ID token is checked against.
type VerifyOptions struct {
	// Issuer is who the token must claim to be from.
	Issuer string

	// ClientID is the audience the token must be for.
	ClientID string

	// Nonce is the value sent in the authentication request.
	//
	// Checked because it is what ties this token to this sign-in attempt. Without it a token captured from one session can
	// be replayed into another.
	Nonce string

	// Algorithms are the signing algorithms accepted. Empty accepts all implemented ones.
	//
	// Configurable so a site whose provider signs with one algorithm can refuse the rest, which removes a class of attack
	// that depends on a verifier being flexible.
	Algorithms []string

	// Now overrides the clock, for tests.
	Now func() time.Time

	// Leeway tolerates clock difference between here and the provider. Defaults to a minute.
	Leeway time.Duration
}

// Verify checks an ID token and returns what it says.
//
// Every check here exists because skipping it is a way in, so none is optional and none is configurable away. The order is
// signature first: a token whose signature has not been checked is a string a stranger wrote, and reading claims out of it
// before verifying invites code that trusts them by accident.
func Verify(ctx context.Context, keys *KeySet, token string, opts VerifyOptions) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("oidc: an ID token has three dot-separated parts and this has %d", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("oidc: the token header is not base64url: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("oidc: the token header could not be read: %w", err)
	}

	accepted := opts.Algorithms
	if len(accepted) == 0 {
		accepted = SupportedAlgorithms
	}
	if !containsString(accepted, header.Alg) {
		// Named explicitly. The algorithm comes from the token, so an attacker chooses it, and the only safe response is a
		// list the caller decided in advance.
		return nil, fmt.Errorf("oidc: the token is signed with %q, which is not accepted here", header.Alg)
	}

	key, err := keys.Key(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("oidc: the token signature is not base64url: %w", err)
	}

	// The signed input is the first two parts exactly as they arrived, including whatever encoding the provider used.
	// Re-encoding them would change the bytes and fail every valid token.
	signed := []byte(parts[0] + "." + parts[1])
	if err := verify(header.Alg, key, signed, signature); err != nil {
		return nil, fmt.Errorf("oidc: the token signature is not valid: %w", err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oidc: the token payload is not base64url: %w", err)
	}

	var cs claimSet
	if err := json.Unmarshal(payload, &cs); err != nil {
		return nil, fmt.Errorf("oidc: the token claims could not be read: %w", err)
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	leeway := opts.Leeway
	if leeway <= 0 {
		leeway = time.Minute
	}

	if strings.TrimSuffix(cs.Issuer, "/") != strings.TrimSuffix(opts.Issuer, "/") {
		return nil, fmt.Errorf("oidc: the token is from issuer %q, not %q", cs.Issuer, opts.Issuer)
	}

	if cs.Subject == "" {
		// Without a subject there is nothing to key an account on, and falling back to email would key identity on a
		// value that gets reassigned when people leave.
		return nil, fmt.Errorf("oidc: the token has no subject, so there is no stable identifier for this person")
	}

	audiences, err := decodeAudience(cs.Audience)
	if err != nil {
		return nil, err
	}
	if !containsString(audiences, opts.ClientID) {
		// A token for a different client is a valid token, which is what makes this check load-bearing: without it, a
		// token issued to any other application registered with the same provider would sign somebody in here.
		return nil, fmt.Errorf("oidc: the token was issued for %v, not for this application", audiences)
	}

	if cs.Expiry == 0 {
		return nil, fmt.Errorf("oidc: the token has no expiry")
	}
	expiry := time.Unix(cs.Expiry, 0)
	if now().After(expiry.Add(leeway)) {
		return nil, fmt.Errorf("oidc: the token expired at %s", expiry.UTC().Format(time.RFC3339))
	}

	issued := time.Unix(cs.IssuedAt, 0)
	if cs.IssuedAt != 0 && issued.After(now().Add(leeway)) {
		// A token from the future usually means the clocks disagree by more than the leeway, which is worth saying,
		// because the alternative diagnosis - a forged token - sends people somewhere much more alarming.
		return nil, fmt.Errorf("oidc: the token was issued at %s, which is in the future; the clock here and the "+
			"identity provider's may disagree", issued.UTC().Format(time.RFC3339))
	}

	if opts.Nonce != "" {
		// Constant time, because this is a secret being compared and an early exit leaks how much of it matched.
		if subtle.ConstantTimeCompare([]byte(cs.Nonce), []byte(opts.Nonce)) != 1 {
			return nil, fmt.Errorf("oidc: the token's nonce does not match this sign-in attempt, so it may be a replay " +
				"of an earlier one")
		}
	}

	verified := false
	if cs.EmailVerified != nil {
		verified = *cs.EmailVerified
	}

	return &Claims{
		Subject:           cs.Subject,
		Issuer:            cs.Issuer,
		Email:             cs.Email,
		EmailVerified:     verified,
		Name:              cs.Name,
		PreferredUsername: cs.PreferredUsername,
		Groups:            cs.Groups,
		Scope:             cs.Scope,
		Patient:           cs.Patient,
		Encounter:         cs.Encounter,
		ExpiresAt:         expiry,
		IssuedAt:          issued,
	}, nil
}

// decodeAudience reads an audience that may be a string or an array.
//
// Both are legal and providers differ, so handling only one means working against some providers and not others - and the
// failure is at sign-in, against a token that is perfectly valid.
func decodeAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("oidc: the token has no audience, so there is no way to tell it was meant for this " +
			"application")
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}

	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}

	return nil, fmt.Errorf("oidc: the token's audience is neither a string nor a list of strings")
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
