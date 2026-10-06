package smartauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/oidc"
)

// The FHIR endpoint accepts what this server issues, with the scopes it granted and nothing more.
func TestTheFHIREndpointAcceptsTheBuiltInServersTokens(t *testing.T) {
	f := newFixture(t)
	_, body := f.token(backendForm(f.assertion(t, nil), "system/*.rs"))
	keys, _ := oidc.NewStaticKeySet(f.srv.Key.JWKS())
	auth, err := fhirserver.NewSMARTAuth(fhirserver.SMARTConfig{Issuer: f.srv.Issuer, Audience: f.srv.Audience, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/Patient", nil)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	if !caller.Allows("Patient", false) || caller.Allows("Patient", true) || caller.Name != "dtr-client" {
		t.Errorf("caller %+v: want read and not write", caller)
	}

	other, _ := fhirserver.NewSMARTAuth(fhirserver.SMARTConfig{Issuer: f.srv.Issuer, Audience: "https://other.example/fhir", Keys: keys})
	if _, err := other.Authenticate(req); err == nil {
		t.Error("a token for this FHIR base was accepted by another")
	}
}
