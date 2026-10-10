package fhirserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixedAuth answers every request the same way, recording that it was asked.
type fixedAuth struct {
	who   string
	asked int
}

func (f *fixedAuth) Authenticate(*http.Request) (*Caller, error) {
	f.asked++
	if f.who == "" {
		return nil, errors.New("refused")
	}
	return &Caller{Name: f.who}, nil
}

func (f *fixedAuth) Describe() string { return f.who }

// With SMART configured, a Perfuse API token is refused unless the switch is on, a JWT always goes to the SMART check, and the
// switch is read per request.
func TestAPITokensAreAcceptedBesideSMARTOnlyWhenSwitchedOn(t *testing.T) {
	on := false
	smart, api := &fixedAuth{who: "smart-app"}, &fixedAuth{who: "api-token"}
	a := &EitherAuth{SMART: smart, API: api, Allow: func() bool { return on }}
	req := func(tok string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/Patient", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}
	const apiTok, jwt = "Q2hhbmdlTWVQbGVhc2VJdElzQVRva2Vu", "aaa.bbb.ccc"

	if c, _ := a.Authenticate(req(apiTok)); c == nil || c.Name != "smart-app" || api.asked != 0 {
		t.Fatalf("switched off, an API token reached the API token lookup (asked %d)", api.asked)
	}
	on = true
	if c, err := a.Authenticate(req(apiTok)); err != nil || c.Name != "api-token" {
		t.Fatalf("switched on, an API token was not accepted: %v %+v", err, c)
	}
	if c, _ := a.Authenticate(req(jwt)); c == nil || c.Name != "smart-app" || api.asked != 1 {
		t.Errorf("a JWT was offered to the API token lookup")
	}
	on = false
	if c, _ := a.Authenticate(req(apiTok)); c == nil || c.Name != "smart-app" || api.asked != 1 {
		t.Errorf("switching it off again did not take effect on the next request")
	}
}
