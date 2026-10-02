package api

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/saml"
	"github.com/biodream-llc/perfuse/internal/store"
)

// What a real Entra sign-in produces on the users screen.
//
// Both of the things checked here were found by signing in to a running Perfuse with a real Microsoft account and then looking at the users
// table, rather than by reading code. Neither is a protocol fault - the signature, the request tie and the conditions were all handled
// correctly first time - and both would have made an Entra deployment unpleasant to administer while every test passed.
//
// The assertion is the captured one from internal/saml/testdata, so these are checks against a document Microsoft actually signed.

// entraAssertion verifies the captured Entra response and returns it.
//
// Verified rather than merely parsed, at the instant it was captured. That costs a few lines of configuration and means these tests work with a
// document that has been proved genuine, rather than with whatever happens to be in the file.
// Verified once for the whole test binary.
//
// The replay cache refuses a second use of the same assertion id, which is correct and is the protection working: an assertion is a
// single-use document and verifying the same one three times is exactly what it exists to stop. So it is verified once and shared.
var entraOnce = sync.OnceValues(verifyCapturedEntraResponse)

func entraAssertion(t *testing.T) *saml.Assertion {
	t.Helper()

	a, err := entraOnce()
	if err != nil {
		if strings.Contains(err.Error(), "no captured") {
			t.Skip(err.Error())
		}
		t.Fatalf("the captured Entra response did not verify, so these tests would be checking a document of unknown provenance: %v", err)
	}

	return a
}

func verifyCapturedEntraResponse() (*saml.Assertion, error) {
	raw, err := os.ReadFile("../saml/testdata/entra-response.xml")
	if err != nil {
		return nil, fmt.Errorf("no captured Entra response: %w", err)
	}

	certPEM, err := os.ReadFile("../saml/testdata/entra-idp.crt")
	if err != nil {
		return nil, fmt.Errorf("no captured Entra certificate: %w", err)
	}

	sp, err := saml.New(saml.Config{
		EntityID:   "urn:perfuse:local-signin",
		ACSPath:    "https://sp.example.test:8544/auth/saml/acs",
		IdPSSOURL:  "https://login.microsoftonline.com/00000000-1111-2222-3333-444444444444/saml2",
		IdPCertPEM: string(certPEM),
	})
	if err != nil {
		return nil, err
	}

	// Inside the captured assertion's validity window, and the request it answers.
	captured := time.Date(2026, 9, 20, 1, 20, 0, 0, time.UTC)

	return sp.VerifyAt(raw, captured, func(id string) bool { return id == "_5f38855a20de8466ac2e7063c8ebcfdf" })
}

func TestAnEntraAccountGetsAReadableName(t *testing.T) {
	a := entraAssertion(t)

	name := samlUsername(a)

	// The failure this replaces. Entra's default NameID is an opaque persistent identifier, and with no email claim to fall back on the
	// account was created as 99d-dljf9omwoabcgx3kpkzm22vvzapha5ktwctmnc4 - which is then what appears beside every channel that account
	// changes, in every audit entry, and on the users screen.
	if strings.HasPrefix(name, "99d-") || len(name) > 30 {
		t.Errorf("an Entra account would be named %q, which is the NameID rather than anything a person could recognise", name)
	}

	// It comes from the user principal name in the name claim, which is where Entra puts it.
	if name != "perfuse-entra" {
		t.Errorf("expected the account to be named from the user principal name, got %q", name)
	}
}

func TestEntraSendsNoEmailClaim(t *testing.T) {
	a := entraAssertion(t)

	// Asserted rather than assumed, because the username handling exists to work around it. If a future Entra starts sending an email claim,
	// the workaround becomes unnecessary and somebody should be told rather than leaving it in place forever.
	email := samlFirstAttr(a, "email", "mail", "emailAddress",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress")

	if email != "" {
		t.Errorf("Entra now sends an email claim (%q), so samlUsername no longer needs to fall back to the user principal name", email)
	}
}

func TestEntrasDisplayNameIsFound(t *testing.T) {
	a := entraAssertion(t)

	// Entra writes this claim under http://schemas.microsoft.com/identity/claims/displayname, a different namespace from the xmlsoap one the
	// lookup names. It matches because samlFirstAttr compares by URI suffix, which is worth a test: a stricter comparison would look more
	// correct and would silently lose the display name for every Microsoft tenant.
	display := samlFirstAttr(a, "displayName", "name", "cn",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name")

	if display == "" {
		t.Error("no display name was found in a real Entra assertion, so the suffix matching in samlFirstAttr has stopped working")
	}
}

func TestASAMLAccountIsNotRecordedAsOIDC(t *testing.T) {
	// The users screen showed auth_source "oidc" for an account created by a SAML sign-in, because the store derives the source from the
	// issuer and a SAML issuer is an https URL exactly like an OIDC one. A site running both could not tell its accounts apart, which is the
	// question that matters when somebody leaves and every route in has to be closed.
	identity := store.ExternalIdentity{
		Issuer:   "https://login.microsoftonline.com/00000000-1111-2222-3333-444444444444/saml2",
		Subject:  "99d-dljF9OMwOaBcgx3KpkZm22VVZApHa5KTwCtmNc4",
		Username: "perfuse-entra",
		Role:     store.RoleAdmin,
		Source:   store.AuthSAML,
	}

	if got := store.SourceForTesting(identity); got != store.AuthSAML {
		t.Errorf("a SAML identity is recorded as %q, so the users screen names the wrong protocol", got)
	}

	// And the derivation still works for the cases that rely on it, or this fix would have broken LDAP.
	ldap := store.ExternalIdentity{Issuer: "ldap://directory.example.invalid"}
	if got := store.SourceForTesting(ldap); got != store.AuthLDAP {
		t.Errorf("an LDAP identity is now recorded as %q", got)
	}

	oidc := store.ExternalIdentity{Issuer: "https://accounts.example.invalid"}
	if got := store.SourceForTesting(oidc); got != store.AuthOIDC {
		t.Errorf("an OIDC identity is now recorded as %q", got)
	}
}
