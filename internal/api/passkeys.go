package api

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/webauthn"
)

// Passkey endpoints.
//
// Four operations: begin a registration, finish it, begin a sign-in, finish it. Plus listing and deleting, which are ordinary
// account management.
//
// # The two things this file is careful about
//
// First, a registration must attach the passkey to the account that asked for it and to no other. Every account takeover
// through a passkey implementation works the same way: a credential registered in one session gets attached to a different
// account. So the account comes from the session that requested the challenge, is recorded with the challenge, and is read
// back from the challenge rather than from anything the client sends.
//
// Second, a failed sign-in must not say why. A message distinguishing "no such credential" from "wrong signature" turns the
// endpoint into an oracle, and there is nothing a legitimate person does differently in either case. The detail goes in the
// log.

// Challenge purposes, kept as constants because a typo would let a registration challenge satisfy a sign-in.
const (
	purposeRegister     = "register"
	purposeAuthenticate = "authenticate"
)

// passkeyConfig assembles the relying party configuration from the server's settings.
func (s *Server) passkeyConfig() (webauthn.Config, error) {
	config := webauthn.Config{
		RPID:    s.PasskeyRPID,
		RPName:  s.PasskeyRPName,
		Origins: s.PasskeyOrigins,

		// Verification is required.
		//
		// A passkey here replaces a password rather than supplementing one, so "somebody touched a key" is not enough:
		// a stolen security key would be a working credential. Requiring a fingerprint, face or PIN means the key alone
		// is not sufficient.
		RequireUserVerification: true,

		// What the operator requires of the authenticator itself, when they have said anything. Empty verifies
		// nothing and claims nothing.
		Attestation: s.PasskeyAttestation,
	}
	if config.RPName == "" {
		config.RPName = "Perfuse"
	}

	if err := config.Valid(); err != nil {
		return webauthn.Config{}, err
	}

	return config, nil
}

// handleBeginPasskeyRegistration issues a challenge for adding a passkey.
func (s *Server) handleBeginPasskeyRegistration(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	config, err := s.passkeyConfig()
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable,
			"passkeys are not configured on this server: "+err.Error())

		return
	}

	scoped := s.storeFor(sess)

	existing, err := scoped.ListPasskeys(r.Context(), sess.UserID)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	options, challenge, err := webauthn.BeginRegistration(
		config, userHandleFor(sess.UserID), sess.Username, sess.Username, existing)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	// The account is recorded with the challenge, and this is the line that prevents an account takeover.
	//
	// When the response comes back, the account is read from here rather than from the session or from anything the client
	// sends. A client that switched accounts between the two calls cannot attach its new credential to the first account.
	if err := scoped.StoreChallenge(r.Context(), challenge, sess.UserID, purposeRegister); err != nil {
		s.failErr(w, r, err)

		return
	}

	s.ok(w, options)
}

// handleFinishPasskeyRegistration verifies a registration and stores the credential.
func (s *Server) handleFinishPasskeyRegistration(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	config, err := s.passkeyConfig()
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "passkeys are not configured on this server")

		return
	}

	var body struct {
		Label     string                        `json:"label"`
		Challenge string                        `json:"challenge"`
		Response  webauthn.RegistrationResponse `json:"response"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	challenge, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(body.Challenge))
	if err != nil || len(challenge) == 0 {
		s.fail(w, r, http.StatusBadRequest, "the challenge is missing or malformed")

		return
	}

	// Consumed before verification, so it cannot be reused whether or not this attempt succeeds. A challenge that
	// survived a failed attempt could be retried with a different response until one worked.
	challengeUserID, err := s.Store.ConsumeChallenge(r.Context(), challenge, purposeRegister)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest,
			"this registration has expired or was already completed; start again")

		return
	}

	// The account comes from the challenge, and must match the session that is asking.
	//
	// Both checks matter. Reading from the challenge stops a client attaching a credential to an account it merely
	// requested a challenge for earlier; comparing with the session stops a challenge issued to one person being
	// completed by another who obtained it.
	if challengeUserID != sess.UserID {
		s.log().Warn("a passkey registration challenge was completed by a different account",
			"challenge_account", challengeUserID, "session_account", sess.UserID, "ip", clientIP(r))
		s.fail(w, r, http.StatusForbidden, "this registration was started by a different account")

		return
	}

	credential, err := webauthn.FinishRegistration(config, challenge, body.Response)
	if err != nil {
		// The reason is logged, not returned. A message naming the failed check helps somebody constructing a response
		// far more than it helps the person who tapped the wrong key.
		s.log().Warn("a passkey registration failed verification",
			"user", sess.Username, "err", err, "ip", clientIP(r))
		s.fail(w, r, http.StatusBadRequest, "that passkey could not be registered; try again")

		return
	}

	credential.Label = passkeyLabel(body.Label, credential)

	scoped := s.storeFor(sess)
	if err := scoped.AddPasskey(r.Context(), sess.UserID, credential); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.fail(w, r, http.StatusConflict,
				"that passkey is already registered, either here or on another account")

			return
		}
		s.failErr(w, r, err)

		return
	}

	s.log().Info("a passkey was registered",
		"user", sess.Username, "label", credential.Label, "algorithm", credential.Algorithm.Name())
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "passkey.register",
		Target:   credential.Label,
		Detail:   credential.Algorithm.Name(),
		IP:       clientIP(r),
	})

	s.ok(w, passkeyView(*credential))
}

// handleBeginPasskeySignIn issues a challenge for signing in.
//
// Takes no username. A discoverable credential lets the authenticator offer whichever passkeys it holds for this site, and the
// account is identified by the credential that comes back - which also means this endpoint discloses nothing about who has an
// account.
func (s *Server) handleBeginPasskeySignIn(w http.ResponseWriter, r *http.Request) {
	config, err := s.passkeyConfig()
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "passkeys are not configured on this server")

		return
	}

	options, challenge, err := webauthn.BeginAuthentication(config, nil)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	// No account, because none is known yet.
	if err := s.Store.ScopeUnchecked(store.DefaultTenant).
		StoreChallenge(r.Context(), challenge, 0, purposeAuthenticate); err != nil {
		s.failErr(w, r, err)

		return
	}

	s.ok(w, options)
}

// handleFinishPasskeySignIn verifies an assertion and starts a session.
func (s *Server) handleFinishPasskeySignIn(w http.ResponseWriter, r *http.Request) {
	config, err := s.passkeyConfig()
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "passkeys are not configured on this server")

		return
	}

	var body struct {
		Challenge string                          `json:"challenge"`
		Response  webauthn.AuthenticationResponse `json:"response"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// Throttled on the source address, exactly as password sign-in is.
	//
	// A passkey cannot be guessed, so this is not about brute force. It is about the work each attempt costs: every one
	// performs a signature verification and a database lookup, and an unthrottled endpoint that does public-key
	// cryptography on demand is a way to spend a server's time cheaply.
	if delay, blocked := s.limiter().Blocked(clientIP(r)); blocked {
		s.fail(w, r, http.StatusTooManyRequests,
			"too many attempts; wait "+delay.Round(time.Second).String())

		return
	}

	challenge, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(body.Challenge))
	if err != nil || len(challenge) == 0 {
		s.fail(w, r, http.StatusBadRequest, "the challenge is missing or malformed")

		return
	}

	if _, err := s.Store.ConsumeChallenge(r.Context(), challenge, purposeAuthenticate); err != nil {
		s.fail(w, r, http.StatusUnauthorized, "that sign-in has expired; try again")

		return
	}

	credentialID, err := base64.RawURLEncoding.DecodeString(body.Response.RawID)
	if err != nil || len(credentialID) == 0 {
		s.failPasskeySignIn(w, r, "the credential identifier is malformed", nil)

		return
	}

	// The lookup that identifies the account. Unscoped by tenant deliberately: there is no tenant to scope by until the
	// credential is found, because finding it is what identifies the account.
	credential, userID, tenantID, err := s.Store.PasskeyByCredentialID(r.Context(), credentialID)
	if err != nil {
		s.failPasskeySignIn(w, r, "no such credential", err)

		return
	}

	scoped := s.Store.ScopeUnchecked(tenantID)

	user, err := scoped.GetUserByID(r.Context(), userID)
	if err != nil {
		s.failPasskeySignIn(w, r, "the credential's account no longer exists", err)

		return
	}
	if user.Disabled {
		// Checked here as well as at session lookup. A disabled account must not be able to start a new session, and
		// relying on the next lookup to notice would hand out a cookie that works for exactly one request.
		s.failPasskeySignIn(w, r, "the account is disabled", nil)

		return
	}

	result, err := webauthn.FinishAuthentication(config, challenge, &credential, body.Response)
	if err != nil {
		s.failPasskeySignIn(w, r, "verification failed", err)

		return
	}

	if result.CloneWarning {
		// Not a refusal, for the reasons in the webauthn package: many authenticators legitimately keep their counter at
		// zero. But a counter that was advancing and then went backwards is worth an operator seeing.
		s.log().Warn("a passkey's sign counter went backwards, which can mean a cloned authenticator",
			"user", user.Username, "label", credential.Label,
			"stored", credential.SignCount, "presented", result.SignCount, "ip", clientIP(r))
	}

	if err := s.Store.RecordPasskeyUse(r.Context(), credentialID, result.SignCount); err != nil {
		// Not fatal. Failing a sign-in because a bookkeeping write failed would turn a minor problem into an outage, and
		// the counter is advisory.
		s.log().Warn("could not record a passkey's use", "user", user.Username, "err", err)
	}

	token, err := scoped.StartSessionFor(r.Context(), user, clientIP(r), r.UserAgent())
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	s.limiter().Succeeded(clientIP(r))

	s.log().Info("a passkey sign-in succeeded",
		"user", user.Username, "label", credential.Label, "verified", result.UserVerified)
	_ = scoped.Audit(r.Context(), store.AuditEntry{
		Username: user.Username,
		Action:   "passkey.signin",
		Target:   credential.Label,
		IP:       clientIP(r),
	})

	// The same helper the password and federated paths use, so a passkey session is identical to any other - including
	// its cookie flags. A second place setting those is a second place to get Secure or HttpOnly wrong.
	s.finishLogin(w, r, token, user)
}

// failPasskeySignIn refuses a sign-in without saying why.
//
// One message for every failure. Distinguishing "no such credential" from "wrong signature" turns this endpoint into an oracle
// for working out which credential identifiers exist, and there is nothing a legitimate person does differently in either
// case. The reason goes to the log, where an operator can see it and an attacker cannot.
func (s *Server) failPasskeySignIn(w http.ResponseWriter, r *http.Request, reason string, err error) {
	s.limiter().Failed(clientIP(r))

	s.log().Warn("a passkey sign-in failed", "reason", reason, "err", err, "ip", clientIP(r))
	s.fail(w, r, http.StatusUnauthorized, "that passkey did not work")
}

// handleListPasskeys lists the caller's own passkeys.
func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	list, err := s.storeFor(sess).ListPasskeys(r.Context(), sess.UserID)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	views := make([]passkeyResponse, 0, len(list))
	for _, credential := range list {
		views = append(views, passkeyView(credential))
	}

	// configured says whether passkeys can be used at all, so the browser can distinguish "you have none" from "this server does not offer them".
	// Without it the screen had no way to tell those apart and said neither.
	s.ok(w, map[string]any{"passkeys": views, "configured": s.PasskeyRPID != ""})
}

// handleDeletePasskey removes one of the caller's own passkeys.
//
// Only ever the caller's own. An administrator removing somebody else's passkey would be able to lock them out or push them
// back onto a password, and the account-management path for that is disabling the account - which is visible and audited as
// what it is.
func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	raw := r.PathValue("id")
	credentialID, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(credentialID) == 0 {
		s.fail(w, r, http.StatusBadRequest, "that is not a credential identifier")

		return
	}

	scoped := s.storeFor(sess)

	if err := scoped.DeletePasskey(r.Context(), sess.UserID, credentialID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "you have no passkey with that identifier")

			return
		}
		s.failErr(w, r, err)

		return
	}

	s.log().Info("a passkey was removed", "user", sess.Username)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "passkey.delete",
		Target:   raw,
		IP:       clientIP(r),
	})

	s.ok(w, map[string]string{"status": "removed"})
}

// passkeyResponse is a passkey as the interface sees it.
//
// Carries no key material. Not because the public key is sensitive - it is not - but because there is nothing an interface can
// do with it, and sending bytes nobody uses invites somebody to start using them.
type passkeyResponse struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Algorithm    string `json:"algorithm"`
	UserVerified bool   `json:"userVerified"`
	CreatedAt    string `json:"createdAt"`
	LastUsed     string `json:"lastUsed,omitempty"`
}

// passkeyView converts a credential for the interface.
func passkeyView(credential webauthn.Credential) passkeyResponse {
	view := passkeyResponse{
		ID:           credential.IDBase64(),
		Label:        credential.Label,
		Algorithm:    credential.Algorithm.Name(),
		UserVerified: credential.UserVerified,
		CreatedAt:    credential.CreatedAt.UTC().Format(time.RFC3339),
	}
	if credential.LastUsed != nil {
		view.LastUsed = credential.LastUsed.UTC().Format(time.RFC3339)
	}

	return view
}

// passkeyLabel decides what to call a passkey.
//
// A person's own words when they gave any, because "MacBook Touch ID" is what lets them tell three passkeys apart a year later.
// Falling back to the algorithm rather than to something like "Passkey 1", which tells nobody anything.
func passkeyLabel(supplied string, credential *webauthn.Credential) string {
	label := strings.TrimSpace(supplied)
	if label != "" {
		if len(label) > 60 {
			label = label[:60]
		}

		return label
	}

	if credential.UserVerified {
		return "passkey (" + credential.Algorithm.Name() + ")"
	}

	return "security key (" + credential.Algorithm.Name() + ")"
}

// userHandleFor makes the opaque account handle WebAuthn wants.
//
// Deliberately not the username. The authenticator stores this and may display it, and on a shared or lost device that would
// disclose who has an account on a hospital's integration engine. An eight-byte encoding of the account number carries the same
// information to us and none to anybody looking at the device.
func userHandleFor(userID int64) []byte {
	handle := make([]byte, 8)
	binary.BigEndian.PutUint64(handle, uint64(userID))

	return handle
}
