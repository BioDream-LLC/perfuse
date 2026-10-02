package webauthn

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// The two operations a caller performs: register a passkey, and verify a sign-in with one.

// Credential is a stored passkey.
//
// What is kept is a public key, an algorithm and a counter. There is no secret here: this whole record could be published
// without allowing anybody to sign in, which is the property that makes passkeys worth the work. A stolen password database
// is a breach; a stolen credential table is a list of public keys.
type Credential struct {
	// ID is the credential identifier the authenticator chose, as raw bytes.
	ID []byte

	// PublicKey is the COSE-encoded public key, stored as it arrived.
	//
	// Stored encoded rather than parsed so that the bytes verified at registration are the bytes used later. Re-encoding
	// a key risks producing a different encoding of the same key, and then a signature check is against something that
	// was never verified.
	PublicKey []byte

	// Algorithm is what this credential signs with, fixed at registration.
	//
	// Stored rather than re-derived. This is the field that makes algorithm confusion impossible: verification reads it
	// from here, and nothing at sign-in can influence it.
	Algorithm Algorithm

	// SignCount is the authenticator's counter as of the last successful sign-in.
	SignCount uint32

	// AAGUID identifies the authenticator model. Shown to a person so they can tell their keys apart.
	//
	// Trustworthy only when ModelAttested is true. Without a verified attestation this is whatever the browser said,
	// so it must not carry an access decision - which is why the two travel together rather than the AAGUID being
	// stored alone and its provenance forgotten.
	AAGUID []byte

	// ModelAttested reports that the AAGUID above was confirmed by a certificate chaining to a configured root.
	//
	// Stored per credential rather than inferred from the current configuration, because the configuration changes. A
	// credential registered before any roots were configured was never attested, and it must not start to look
	// attested the day somebody adds them.
	ModelAttested bool

	// UserVerified records whether the authenticator verified who the person was at registration.
	//
	// Worth keeping because it says what this credential is capable of. One registered with only user presence cannot
	// later satisfy a user-verification requirement, and knowing that in advance is better than a sign-in that fails.
	UserVerified bool

	// Label is what a person calls this passkey.
	Label string

	// CreatedAt and LastUsed for the interface.
	//
	// LastUsed matters operationally: a passkey nobody has used in a year is either a lost device or a spare, and both
	// are worth reviewing.
	CreatedAt time.Time
	LastUsed  *time.Time
}

// IDBase64 gives the credential identifier in the form the browser uses.
func (c Credential) IDBase64() string { return base64.RawURLEncoding.EncodeToString(c.ID) }

// RegistrationOptions is what the browser needs to create a passkey.
//
// Marshalled to JSON and handed to navigator.credentials.create, so the field names are the specification's.
type RegistrationOptions struct {
	Challenge        string                 `json:"challenge"`
	RP               relyingParty           `json:"rp"`
	User             userEntity             `json:"user"`
	PubKeyCredParams []credentialParameter  `json:"pubKeyCredParams"`
	Timeout          int                    `json:"timeout"`
	AuthenticatorSel authenticatorSelection `json:"authenticatorSelection"`

	// ExcludeCredentials lists the passkeys this account already has.
	//
	// Sent so an authenticator refuses to create a second credential for an account it already holds one for. Without it
	// somebody registering twice on the same device gets two passkeys that do the same thing, and then cannot tell which
	// to delete.
	ExcludeCredentials []credentialDescriptor `json:"excludeCredentials,omitempty"`

	// Attestation is "none".
	//
	// Asked for deliberately. Requesting attestation and then not verifying it would be worse than not asking: it sends a
	// certificate chain identifying the authenticator's make and batch, which is a privacy cost paid for nothing. Asking
	// for none means less data crosses the wire and none of it is data this would trust anyway.
	Attestation string `json:"attestation"`
}

type relyingParty struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type userEntity struct {
	// ID is an opaque handle for the account, base64url encoded.
	//
	// Deliberately not a username or an email address. The authenticator stores this and may display it, and on a shared
	// or lost device that would disclose who has an account on a hospital's integration engine.
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type credentialParameter struct {
	Type string    `json:"type"`
	Alg  Algorithm `json:"alg"`
}

type authenticatorSelection struct {
	// ResidentKey and RequireResidentKey ask for a discoverable credential, which is what allows signing in without
	// typing a username first.
	ResidentKey        string `json:"residentKey,omitempty"`
	RequireResidentKey bool   `json:"requireResidentKey"`
	UserVerification   string `json:"userVerification"`
}

type credentialDescriptor struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"`
	Transports []string `json:"transports,omitempty"`
}

// BeginRegistration builds the options for creating a passkey.
//
// The challenge is returned separately so the caller stores it against the session. It is deliberately not derivable from the
// options: a caller that had to extract it from JSON would be one line away from not storing it at all, and an unstored
// challenge is one that cannot be checked.
func BeginRegistration(
	config Config, userHandle []byte, username, displayName string, existing []Credential,
) (RegistrationOptions, []byte, error) {
	if err := config.Valid(); err != nil {
		return RegistrationOptions{}, nil, err
	}
	if len(userHandle) == 0 {
		return RegistrationOptions{}, nil, errors.New("a user handle is required")
	}

	challenge, err := NewChallenge()
	if err != nil {
		return RegistrationOptions{}, nil, err
	}

	parameters := make([]credentialParameter, 0, len(SupportedAlgorithms))
	for _, algorithm := range SupportedAlgorithms {
		parameters = append(parameters, credentialParameter{Type: "public-key", Alg: algorithm})
	}

	exclude := make([]credentialDescriptor, 0, len(existing))
	for _, credential := range existing {
		exclude = append(exclude, credentialDescriptor{Type: "public-key", ID: credential.IDBase64()})
	}

	userVerification := "preferred"
	if config.RequireUserVerification {
		userVerification = "required"
	}

	if displayName == "" {
		displayName = username
	}

	return RegistrationOptions{
		Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		RP:        relyingParty{ID: config.RPID, Name: config.RPName},
		User: userEntity{
			ID:          base64.RawURLEncoding.EncodeToString(userHandle),
			Name:        username,
			DisplayName: displayName,
		},
		PubKeyCredParams: parameters,
		Timeout:          int(ChallengeLifetime / time.Millisecond),
		AuthenticatorSel: authenticatorSelection{
			ResidentKey:        "preferred",
			RequireResidentKey: false,
			UserVerification:   userVerification,
		},
		ExcludeCredentials: exclude,
		Attestation:        "none",
	}, challenge, nil
}

// RegistrationResponse is what the browser sends back.
type RegistrationResponse struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
	} `json:"response"`
}

// FinishRegistration verifies a registration and returns the credential to store.
//
// The challenge is passed in rather than looked up here, so this function has no state and cannot be the place a
// single-use rule is forgotten. Deleting the challenge is the caller's job and is what makes it single-use.
func FinishRegistration(
	config Config, challenge []byte, response RegistrationResponse,
) (*Credential, error) {
	if err := config.Valid(); err != nil {
		return nil, err
	}
	if len(challenge) == 0 {
		// Refused rather than treated as "no challenge to check". An empty challenge reaching here means the caller
		// failed to find one, and the permissive reading of that is a registration nobody challenged.
		return nil, fmt.Errorf("%w: no challenge was issued for this registration", ErrChallenge)
	}
	if response.Type != "public-key" {
		return nil, fmt.Errorf("%w: the credential type is %q", ErrVerification, response.Type)
	}

	clientDataJSON, err := base64.RawURLEncoding.DecodeString(response.Response.ClientDataJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: the client data is not base64url", ErrVerification)
	}

	if err := verifyClientData(clientDataJSON, "webauthn.create", challenge, config); err != nil {
		return nil, err
	}

	attestationBytes, err := base64.RawURLEncoding.DecodeString(response.Response.AttestationObject)
	if err != nil {
		return nil, fmt.Errorf("%w: the attestation object is not base64url", ErrVerification)
	}

	attestation, err := DecodeCBOR(attestationBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: the attestation object is not CBOR: %w", ErrVerification, err)
	}

	authDataValue, ok := attestation.MapEntryText("authData")
	if !ok || authDataValue.Kind != CBORBytes {
		return nil, fmt.Errorf("%w: the attestation object carries no authenticator data", ErrVerification)
	}

	// The attestation statement is verified when, and only when, an operator has configured roots to verify it
	// against. See attestation.go for why the roots are supplied rather than bundled.
	//
	// With nothing configured this does nothing and claims nothing, which is the position this file described before
	// and is still the right default. Attestation answers "which device", never "is this legitimate": the credential is
	// bound to the origin, the challenge is ours, and the private key stays on the authenticator whether the statement
	// is checked or not.

	authData, err := parseAuthenticatorData(authDataValue.Bytes)
	if err != nil {
		return nil, err
	}
	if err := verifyAuthenticatorData(authData, config); err != nil {
		return nil, err
	}

	if authData.Flags&flagAttestedData == 0 || len(authData.CredentialID) == 0 {
		return nil, fmt.Errorf("%w: the registration carries no credential", ErrVerification)
	}

	// The key is parsed to confirm it is usable and to fix the algorithm. Storing a key that cannot be parsed would
	// produce an account whose owner is refused at every sign-in with no explanation.
	key, err := ParseCOSEKey(authData.CredentialPublicKey)
	if err != nil {
		return nil, err
	}

	// The identifier the browser reported must match the one inside the signed data.
	//
	// Checked because the outer value is not covered by anything, so a client could report one identifier while the
	// authenticator attested to another - and then a later sign-in would look up the wrong credential.
	reportedID, err := base64.RawURLEncoding.DecodeString(response.RawID)
	if err != nil {
		return nil, fmt.Errorf("%w: the credential identifier is not base64url", ErrVerification)
	}
	if len(reportedID) != len(authData.CredentialID) {
		return nil, fmt.Errorf("%w: the reported credential identifier differs from the attested one", ErrVerification)
	}
	for i := range reportedID {
		if reportedID[i] != authData.CredentialID[i] {
			return nil, fmt.Errorf("%w: the reported credential identifier differs from the attested one",
				ErrVerification)
		}
	}

	// The attestation itself, against whatever the operator required.
	//
	// After the authenticator data is parsed and before the credential is built, because the verification needs the
	// AAGUID from that data and there is no point building a credential that is about to be refused.
	attestedModel := false
	if policy := config.Attestation; policy.Configured() {
		format := ""
		if f, ok := attestation.MapEntryText("fmt"); ok {
			format = f.Text
		}
		statement, _ := attestation.MapEntryText("attStmt")

		verified, err := verifyAttestation(policy, format, statement,
			authDataValue.Bytes, clientDataHashOf(clientDataJSON), authData.AAGUID)
		if err != nil {
			return nil, err
		}
		attestedModel = verified
	}

	// Copied out of the parse buffer, because the caller stores these and the buffer is a request body.
	credentialID := make([]byte, len(authData.CredentialID))
	copy(credentialID, authData.CredentialID)
	publicKey := make([]byte, len(authData.CredentialPublicKey))
	copy(publicKey, authData.CredentialPublicKey)
	aaguid := make([]byte, len(authData.AAGUID))
	copy(aaguid, authData.AAGUID)

	return &Credential{
		ID:            credentialID,
		PublicKey:     publicKey,
		Algorithm:     key.Algorithm,
		SignCount:     authData.SignCount,
		AAGUID:        aaguid,
		ModelAttested: attestedModel,
		UserVerified:  authData.UserVerified(),
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// AuthenticationOptions is what the browser needs to sign in.
type AuthenticationOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
	Timeout   int    `json:"timeout"`

	// AllowCredentials narrows the choice to this account's passkeys.
	//
	// Empty when signing in without a username first, which is what a discoverable credential allows: the authenticator
	// offers whichever passkeys it holds for this site and the account is identified by the user handle it returns.
	AllowCredentials []credentialDescriptor `json:"allowCredentials,omitempty"`

	UserVerification string `json:"userVerification"`
}

// BeginAuthentication builds the options for signing in.
func BeginAuthentication(config Config, allowed []Credential) (AuthenticationOptions, []byte, error) {
	if err := config.Valid(); err != nil {
		return AuthenticationOptions{}, nil, err
	}

	challenge, err := NewChallenge()
	if err != nil {
		return AuthenticationOptions{}, nil, err
	}

	descriptors := make([]credentialDescriptor, 0, len(allowed))
	for _, credential := range allowed {
		descriptors = append(descriptors, credentialDescriptor{Type: "public-key", ID: credential.IDBase64()})
	}

	userVerification := "preferred"
	if config.RequireUserVerification {
		userVerification = "required"
	}

	return AuthenticationOptions{
		Challenge:        base64.RawURLEncoding.EncodeToString(challenge),
		RPID:             config.RPID,
		Timeout:          int(ChallengeLifetime / time.Millisecond),
		AllowCredentials: descriptors,
		UserVerification: userVerification,
	}, challenge, nil
}

// AuthenticationResponse is what the browser sends back on sign-in.
type AuthenticationResponse struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		UserHandle        string `json:"userHandle"`
	} `json:"response"`
}

// UserHandleBytes decodes the user handle, which identifies the account for a usernameless sign-in.
func (r AuthenticationResponse) UserHandleBytes() ([]byte, error) {
	if r.Response.UserHandle == "" {
		return nil, nil
	}

	return base64.RawURLEncoding.DecodeString(r.Response.UserHandle)
}

// AuthenticationResult is what a successful sign-in yields.
type AuthenticationResult struct {
	// SignCount is the authenticator's new counter, to be stored.
	SignCount uint32

	// UserVerified says whether this particular sign-in verified who the person was.
	//
	// Distinct from the credential's own capability. A passkey able to verify may still be used with only presence, and a
	// caller requiring verification has to check the sign-in rather than the registration.
	UserVerified bool

	// CloneWarning is set when the sign count did not advance.
	//
	// Advisory rather than fatal. Many authenticators legitimately keep the counter at zero, so refusing the sign-in
	// would lock out perfectly good hardware. But a counter that was advancing and then stopped is worth a log line.
	CloneWarning bool
}

// FinishAuthentication verifies a sign-in.
//
// The credential is passed in, already looked up by identifier, and its stored algorithm is what verifies the signature.
// Nothing in the response influences that choice.
func FinishAuthentication(
	config Config, challenge []byte, credential *Credential, response AuthenticationResponse,
) (*AuthenticationResult, error) {
	if err := config.Valid(); err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, fmt.Errorf("%w: no credential to verify against", ErrVerification)
	}
	if len(challenge) == 0 {
		return nil, fmt.Errorf("%w: no challenge was issued for this sign-in", ErrChallenge)
	}
	if response.Type != "public-key" {
		return nil, fmt.Errorf("%w: the credential type is %q", ErrVerification, response.Type)
	}

	clientDataJSON, err := base64.RawURLEncoding.DecodeString(response.Response.ClientDataJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: the client data is not base64url", ErrVerification)
	}
	if err := verifyClientData(clientDataJSON, "webauthn.get", challenge, config); err != nil {
		return nil, err
	}

	authDataBytes, err := base64.RawURLEncoding.DecodeString(response.Response.AuthenticatorData)
	if err != nil {
		return nil, fmt.Errorf("%w: the authenticator data is not base64url", ErrVerification)
	}
	authData, err := parseAuthenticatorData(authDataBytes)
	if err != nil {
		return nil, err
	}
	if err := verifyAuthenticatorData(authData, config); err != nil {
		return nil, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(response.Response.Signature)
	if err != nil {
		return nil, fmt.Errorf("%w: the signature is not base64url", ErrVerification)
	}

	// The stored key and its stored algorithm. This is the line that makes algorithm confusion impossible.
	key, err := ParseCOSEKey(credential.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: the stored credential key cannot be read: %w", ErrVerification, err)
	}
	if key.Algorithm != credential.Algorithm {
		// The stored algorithm and the stored key disagree, which should be impossible. Refused rather than resolved,
		// because either value might be the corrupted one and choosing between them is guessing about a credential.
		return nil, fmt.Errorf("%w: the stored credential is inconsistent", ErrVerification)
	}

	if err := verifySignature(key, signedData(authDataBytes, clientDataJSON), signature); err != nil {
		return nil, err
	}

	result := &AuthenticationResult{
		SignCount:    authData.SignCount,
		UserVerified: authData.UserVerified(),
	}

	// The sign count. A counter that goes backwards means two authenticators hold the same private key, which means one
	// is a clone.
	//
	// Reported rather than refused. A zero counter is legitimate and common - many authenticators never implement it -
	// so treating any non-advance as an attack would lock out working hardware. A backwards move with both counters
	// non-zero is the case worth surfacing.
	if credential.SignCount > 0 && authData.SignCount > 0 && authData.SignCount <= credential.SignCount {
		result.CloneWarning = true
	}

	return result, nil
}
