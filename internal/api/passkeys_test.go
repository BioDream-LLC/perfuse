package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// End-to-end passkey tests, using a software authenticator so real signatures go through the real handlers.
//
// The one that matters most is the account-takeover attempt: every passkey implementation that has been broken was broken the
// same way, by a credential registered in one session being attached to a different account.

const passkeyDomain = "perfuse.example.org"

func passkeyHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.server.PasskeyRPID = passkeyDomain
	h.server.PasskeyRPName = "Perfuse"
	h.server.PasskeyOrigins = []string{"https://" + passkeyDomain}
	h.handler = h.server.Handler()

	return h
}

// testAuthenticator is a software authenticator, the same shape as the one in internal/webauthn.
type testAuthenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
}

func newTestAuthenticator(t *testing.T) *testAuthenticator {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}

	return &testAuthenticator{key: key, credentialID: id}
}

func (a *testAuthenticator) coseKey() []byte {
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))

	var b []byte
	b = append(b, 0xa5)
	b = append(b, 0x01, 0x02)
	b = append(b, 0x03, 0x26)
	b = append(b, 0x20, 0x01)
	b = append(b, 0x21, 0x58, 0x20)
	b = append(b, x...)
	b = append(b, 0x22, 0x58, 0x20)
	b = append(b, y...)

	return b
}

func (a *testAuthenticator) authData(flags byte, withCredential bool, signCount uint32) []byte {
	hash := sha256.Sum256([]byte(passkeyDomain))

	out := append([]byte{}, hash[:]...)
	out = append(out, flags)

	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, signCount)
	out = append(out, count...)

	if withCredential {
		out = append(out, make([]byte, 16)...) // AAGUID
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(a.credentialID)))
		out = append(out, length...)
		out = append(out, a.credentialID...)
		out = append(out, a.coseKey()...)
	}

	return out
}

func cborBytes(data []byte) []byte {
	var out []byte
	switch {
	case len(data) < 24:
		out = append(out, byte(0x40|len(data)))
	case len(data) < 256:
		out = append(out, 0x58, byte(len(data)))
	default:
		out = append(out, 0x59, byte(len(data)>>8), byte(len(data)))
	}

	return append(out, data...)
}

func (a *testAuthenticator) attestation(authData []byte) []byte {
	var b []byte
	b = append(b, 0xa3)
	b = append(b, 0x63, 'f', 'm', 't')
	b = append(b, 0x64, 'n', 'o', 'n', 'e')
	b = append(b, 0x67, 'a', 't', 't', 'S', 't', 'm', 't')
	b = append(b, 0xa0)
	b = append(b, 0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a')
	b = append(b, cborBytes(authData)...)

	return b
}

func passkeyClientData(kind, challenge string) []byte {
	encoded, _ := json.Marshal(map[string]any{
		"type":        kind,
		"challenge":   challenge,
		"origin":      "https://" + passkeyDomain,
		"crossOrigin": false,
	})

	return encoded
}

// beginRegistration asks for a challenge as the given role.
func beginRegistration(t *testing.T, h *harness, role string) string {
	t.Helper()

	rec := h.do(role, http.MethodPost, "/api/passkeys/register/begin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin failed: %d %s", rec.Code, rec.Body.String())
	}

	var options struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if options.Challenge == "" {
		t.Fatal("no challenge was issued")
	}

	return options.Challenge
}

// finishRegistration completes a registration with a genuine attestation.
func (a *testAuthenticator) finishRegistration(
	t *testing.T, h *harness, role, challenge, label string,
) *httptest.ResponseRecorder {
	t.Helper()

	clientData := passkeyClientData("webauthn.create", challenge)
	authData := a.authData(0x01|0x04|0x40, true, 0) // present, verified, attested

	body := map[string]any{
		"label":     label,
		"challenge": challenge,
		"response": map[string]any{
			"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
			"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
			"type":  "public-key",
			"response": map[string]any{
				"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
				"attestationObject": base64.RawURLEncoding.EncodeToString(a.attestation(authData)),
			},
		},
	}

	return h.do(role, http.MethodPost, "/api/passkeys/register/finish", body)
}

// signIn performs a full passkey sign-in and returns the response.
func (a *testAuthenticator) signIn(t *testing.T, h *harness, signCount uint32) *httptest.ResponseRecorder {
	t.Helper()

	// Begin, unauthenticated.
	req := httptest.NewRequest(http.MethodPost, "/api/passkeys/signin/begin", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in begin failed: %d %s", rec.Code, rec.Body.String())
	}

	var options struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}

	clientData := passkeyClientData("webauthn.get", options.Challenge)
	authData := a.authData(0x01|0x04, false, signCount)

	digest := sha256.Sum256(clientData)
	message := append(append([]byte{}, authData...), digest[:]...)
	signed := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"challenge": options.Challenge,
		"response": map[string]any{
			"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
			"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
			"type":  "public-key",
			"response": map[string]any{
				"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
				"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
				"signature":         base64.RawURLEncoding.EncodeToString(signature),
			},
		},
	})

	req = httptest.NewRequest(http.MethodPost, "/api/passkeys/signin/finish", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	return rec
}

func TestAPasskeyRegistersAndThenSignsIn(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "admin")
	rec := authenticator.finishRegistration(t, h, "admin", challenge, "MacBook Touch ID")
	if rec.Code != http.StatusOK {
		t.Fatalf("registration failed: %d %s", rec.Code, rec.Body.String())
	}

	var registered passkeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.Label != "MacBook Touch ID" {
		t.Errorf("label is %q", registered.Label)
	}
	if !strings.Contains(registered.Algorithm, "ES256") {
		t.Errorf("algorithm is %q", registered.Algorithm)
	}
	// No key material in the response: there is nothing an interface does with it.
	if strings.Contains(rec.Body.String(), "publicKey") {
		t.Errorf("the response carries key material: %s", rec.Body.String())
	}

	// It is listed.
	rec = h.do("admin", http.MethodGet, "/api/passkeys", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing failed: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "MacBook Touch ID") {
		t.Errorf("the passkey is not listed: %s", rec.Body.String())
	}

	// And it signs in.
	rec = authenticator.signIn(t, h, 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in failed: %d %s", rec.Code, rec.Body.String())
	}

	// A session cookie must have been set, or the sign-in achieved nothing.
	var gotCookie bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			gotCookie = true
			if !cookie.HttpOnly {
				t.Error("the session cookie is readable from JavaScript")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("the session cookie SameSite is %v", cookie.SameSite)
			}
		}
	}
	if !gotCookie {
		t.Error("no session cookie was set, so the sign-in produced nothing usable")
	}
}

// TestACredentialCannotBeAttachedToAnotherAccount is the account takeover.
//
// Every broken passkey implementation was broken this way: a credential registered in one session attached to a different
// account. Here one account requests a challenge and another tries to complete it.
func TestACredentialCannotBeAttachedToAnotherAccount(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	// The viewer asks for a challenge.
	challenge := beginRegistration(t, h, "viewer")

	// The admin tries to complete it, which would attach the viewer's challenge to the admin account - or, with the roles
	// reversed, put an attacker's key on an administrator.
	rec := authenticator.finishRegistration(t, h, "admin", challenge, "stolen")
	if rec.Code == http.StatusOK {
		t.Fatal("a registration challenge issued to one account was completed by another")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("the refusal was %d rather than 403: %s", rec.Code, rec.Body.String())
	}

	// And neither account ends up with a passkey.
	for _, role := range []string{"admin", "viewer"} {
		rec := h.do(role, http.MethodGet, "/api/passkeys", nil)
		if strings.Contains(rec.Body.String(), "stolen") {
			t.Errorf("%s ended up with the credential: %s", role, rec.Body.String())
		}
	}
}

// TestAChallengeCannotBeUsedTwice covers replay at the endpoint.
func TestAChallengeCannotBeUsedTwice(t *testing.T) {
	h := passkeyHarness(t)
	first := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "admin")

	if rec := first.finishRegistration(t, h, "admin", challenge, "one"); rec.Code != http.StatusOK {
		t.Fatalf("the first registration failed: %s", rec.Body.String())
	}

	// The same challenge again, with a different authenticator.
	second := newTestAuthenticator(t)
	rec := second.finishRegistration(t, h, "admin", challenge, "two")
	if rec.Code == http.StatusOK {
		t.Error("a challenge was used twice")
	}
}

// TestTheSameCredentialCannotBeRegisteredOnTwoAccounts covers the endpoint's half of the integrity property.
func TestTheSameCredentialCannotBeRegisteredOnTwoAccounts(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "admin")
	if rec := authenticator.finishRegistration(t, h, "admin", challenge, "mine"); rec.Code != http.StatusOK {
		t.Fatalf("the first registration failed: %s", rec.Body.String())
	}

	// The same credential identifier, offered by a different account.
	challenge = beginRegistration(t, h, "editor")
	rec := authenticator.finishRegistration(t, h, "editor", challenge, "theirs")
	if rec.Code == http.StatusOK {
		t.Error("one credential was registered on two accounts")
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("the refusal was %d rather than 409: %s", rec.Code, rec.Body.String())
	}
}

// TestAFailedSignInSaysNothingUseful covers the oracle.
//
// A message distinguishing "no such credential" from "wrong signature" lets somebody work out which credential identifiers
// exist. There is nothing a legitimate person does differently in either case.
func TestAFailedSignInSaysNothingUseful(t *testing.T) {
	h := passkeyHarness(t)
	registered := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "admin")
	if rec := registered.finishRegistration(t, h, "admin", challenge, "real"); rec.Code != http.StatusOK {
		t.Fatalf("registration failed: %s", rec.Body.String())
	}

	// An unknown credential.
	unknown := newTestAuthenticator(t)
	unknownRec := unknown.signIn(t, h, 1)

	// A known credential with a wrong key: same identifier, different key.
	wrongKey := newTestAuthenticator(t)
	wrongKey.credentialID = registered.credentialID
	wrongKeyRec := wrongKey.signIn(t, h, 1)

	if unknownRec.Code != http.StatusUnauthorized || wrongKeyRec.Code != http.StatusUnauthorized {
		t.Fatalf("statuses were %d and %d, expected 401 for both", unknownRec.Code, wrongKeyRec.Code)
	}

	// The bodies must be identical, or the difference is the oracle.
	if unknownRec.Body.String() != wrongKeyRec.Body.String() {
		t.Errorf("an unknown credential and a wrong signature give different answers:\n%s\n%s",
			unknownRec.Body.String(), wrongKeyRec.Body.String())
	}
	// And neither may name what went wrong.
	for _, leak := range []string{"signature", "not found", "no such", "credential id"} {
		if strings.Contains(strings.ToLower(unknownRec.Body.String()), leak) {
			t.Errorf("the refusal mentions %q: %s", leak, unknownRec.Body.String())
		}
	}
}

// TestADisabledAccountCannotSignInWithAPasskey covers deprovisioning reaching this path too.
//
// Checked before a session is minted rather than relying on the next lookup to notice, which would hand out a cookie that works
// for exactly one request.
func TestADisabledAccountCannotSignInWithAPasskey(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "editor")
	if rec := authenticator.finishRegistration(t, h, "editor", challenge, "leaver"); rec.Code != http.StatusOK {
		t.Fatalf("registration failed: %s", rec.Body.String())
	}

	// It works first.
	if rec := authenticator.signIn(t, h, 1); rec.Code != http.StatusOK {
		t.Fatalf("the sign-in did not work to begin with: %d %s", rec.Code, rec.Body.String())
	}

	// Now disable the account, as a departure would.
	scoped := h.store.ScopeUnchecked(store.DefaultTenant)
	users, err := scoped.ListUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Role == store.RoleEditor {
			if err := scoped.SetDisabled(t.Context(), u.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	if rec := authenticator.signIn(t, h, 2); rec.Code == http.StatusOK {
		t.Error("a disabled account signed in with its passkey")
	}
}

// TestAPasskeyCanOnlyBeRemovedByItsOwner covers the delete path.
//
// An administrator removing somebody else's passkey could lock them out or push them back onto a password. The account
// management path for that is disabling the account, which is visible and audited as what it is.
func TestAPasskeyCanOnlyBeRemovedByItsOwner(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "editor")
	rec := authenticator.finishRegistration(t, h, "editor", challenge, "editors-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("registration failed: %s", rec.Body.String())
	}

	var registered passkeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}

	// The admin tries to remove it.
	rec = h.do("admin", http.MethodDelete, "/api/passkeys/"+registered.ID, nil)
	if rec.Code == http.StatusOK {
		t.Error("an administrator removed somebody else's passkey")
	}

	// The owner can.
	rec = h.do("editor", http.MethodDelete, "/api/passkeys/"+registered.ID, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("the owner could not remove their own passkey: %d %s", rec.Code, rec.Body.String())
	}
}

// TestThePasskeyEndpointsAreAbsentUntilConfigured covers the default.
//
// There is no safe default for the relying party identifier: guessing it from a Host header would let whoever controls DNS
// decide what a credential is bound to. So nothing that creates or uses a credential exists until somebody sets it.
//
// Amended after the users screen was found opening with "the server sent a response that could not be read" on every
// installation that had not configured passkeys. Listing used to be in this set. Unregistered, it fell through to the handler
// that serves the web application and answered 200 with index.html, so the browser asked for JSON and got a web page - and
// the screen reported that as an error, which it was.
//
// The distinction now drawn: anything that creates or uses a credential is genuinely absent, because there is no safe
// default for what it would be bound to. Reading the list is always available and answers an empty list with
// configured:false, because "you have none" and "this server does not offer them" are different things and a screen that
// cannot tell them apart says neither.
func TestThePasskeyEndpointsAreAbsentUntilConfigured(t *testing.T) {
	h := newHarness(t) // no passkey domain

	// Absent: these would need a relying party identifier to mean anything.
	for _, path := range []string{"/api/passkeys/signin/begin", "/api/passkeys/register/begin", "/api/passkeys/register/finish"} {
		rec := h.do("admin", http.MethodPost, path, map[string]string{})
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d with passkeys unconfigured; nothing should create a credential with no domain to bind it to", path, rec.Code)
		}
	}

	// Present, and honest about the feature being off.
	rec := h.do("admin", http.MethodGet, "/api/passkeys", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing passkeys answered %d with passkeys unconfigured; a screen everybody visits calls this", rec.Code)
	}

	var body struct {
		Passkeys   []map[string]any `json:"passkeys"`
		Configured bool             `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if body.Configured {
		t.Error("configured is true with no relying party identifier set")
	}
	if len(body.Passkeys) != 0 {
		t.Errorf("passkeys were listed with the feature unconfigured: %v", body.Passkeys)
	}
}

// TestAnAssertionForAnotherOriginIsRefusedAtTheEndpoint checks the anti-phishing property survives the wiring.
func TestAnAssertionForAnotherOriginIsRefusedAtTheEndpoint(t *testing.T) {
	h := passkeyHarness(t)
	authenticator := newTestAuthenticator(t)

	challenge := beginRegistration(t, h, "admin")
	if rec := authenticator.finishRegistration(t, h, "admin", challenge, "real"); rec.Code != http.StatusOK {
		t.Fatalf("registration failed: %s", rec.Body.String())
	}

	// A sign-in whose client data names a different origin.
	req := httptest.NewRequest(http.MethodPost, "/api/passkeys/signin/begin", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	var options struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}

	hostile, _ := json.Marshal(map[string]any{
		"type":        "webauthn.get",
		"challenge":   options.Challenge,
		"origin":      "https://evil.example.org",
		"crossOrigin": false,
	})
	authData := authenticator.authData(0x01|0x04, false, 1)

	digest := sha256.Sum256(hostile)
	message := append(append([]byte{}, authData...), digest[:]...)
	signed := sha256.Sum256(message)
	signature, _ := ecdsa.SignASN1(rand.Reader, authenticator.key, signed[:])

	body, _ := json.Marshal(map[string]any{
		"challenge": options.Challenge,
		"response": map[string]any{
			"id":    base64.RawURLEncoding.EncodeToString(authenticator.credentialID),
			"rawId": base64.RawURLEncoding.EncodeToString(authenticator.credentialID),
			"type":  "public-key",
			"response": map[string]any{
				"clientDataJSON":    base64.RawURLEncoding.EncodeToString(hostile),
				"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
				"signature":         base64.RawURLEncoding.EncodeToString(signature),
			},
		},
	})

	req = httptest.NewRequest(http.MethodPost, "/api/passkeys/signin/finish", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("an assertion made for another origin signed in, so the passkey is phishable")
	}
}
