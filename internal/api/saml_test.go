package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/saml"
)

// Tests that somebody can actually sign in with SAML.
//
// The package that does the hard part existed for weeks with 1,068 lines of tests and no callers: OIDC had eight importers, LDAP six,
// SAML none. It was correct and unreachable, which from a user's side is the same as absent. These tests exercise the two endpoints
// that make it real, because a parser nobody can reach is not a feature.

// samlTestIdP is a fake identity provider: a key, its certificate, and the ability to sign a response.
type samlTestIdP struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func newSAMLTestIdP(t *testing.T) samlTestIdP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "Test IdP"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return samlTestIdP{
		key:  key,
		cert: cert,
		pem:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// signResponse produces a Response signed at the Response level, the way ADFS and several others do by default.
//
// The signing is duplicated from the saml package's own tests rather than shared, because a test helper that imports the thing it is
// testing to build its input can only ever confirm the code agrees with itself.
func (idp samlTestIdP) signResponse(t *testing.T, respID, inResponseTo, nameID, acsURL, audience string, attrs map[string][]string) string {
	t.Helper()

	now := time.Now().UTC()

	var attrXML strings.Builder
	if len(attrs) > 0 {
		attrXML.WriteString(`<saml:AttributeStatement>`)

		for name, values := range attrs {
			attrXML.WriteString(fmt.Sprintf(`<saml:Attribute Name="%s">`, name))

			for _, v := range values {
				attrXML.WriteString(fmt.Sprintf(`<saml:AttributeValue>%s</saml:AttributeValue>`, v))
			}

			attrXML.WriteString(`</saml:Attribute>`)
		}

		attrXML.WriteString(`</saml:AttributeStatement>`)
	}

	assertion := fmt.Sprintf(
		`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s_a" IssueInstant="%s" Version="2.0">`+
			`<saml:Issuer>https://idp.test/metadata</saml:Issuer>`+
			`<saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">%s</saml:NameID></saml:Subject>`+
			`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s">`+
			`<saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction>`+
			`</saml:Conditions>`+
			`<saml:AuthnStatement SessionIndex="_sess"/>`+
			`%s`+
			`</saml:Assertion>`,
		respID, now.Format(time.RFC3339), nameID,
		now.Add(-1*time.Minute).Format(time.RFC3339), now.Add(5*time.Minute).Format(time.RFC3339),
		audience, attrXML.String(),
	)

	inReply := ""
	if inResponseTo != "" {
		inReply = fmt.Sprintf(` InResponseTo="%s"`, inResponseTo)
	}

	body := fmt.Sprintf(
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="%s" Version="2.0" IssueInstant="%s"`+
			` Destination="%s"%s>`+
			`<saml:Issuer xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">https://idp.test/metadata</saml:Issuer>`+
			`{SIGNATURE}`+
			`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>`+
			`%s`+
			`</samlp:Response>`,
		respID, now.Format(time.RFC3339), acsURL, inReply, assertion,
	)

	return strings.Replace(body, `{SIGNATURE}`, idp.sign(t, body, respID), 1)
}

func (idp samlTestIdP) sign(t *testing.T, bodyWithPlaceholder, refID string) string {
	t.Helper()

	// Uses the saml package's own signing, because a valid XML signature needs the same canonicalisation the verifier uses and a
	// second implementation of that would drift - and when it drifted these tests would keep passing while agreeing about the
	// wrong bytes. Signing grants nothing: a document signed with a key the service provider does not trust is still rejected,
	// which TestSAMLResponseSignedByTheWrongKeyIsRefused relies on.
	sig, err := saml.SignDocument(bodyWithPlaceholder, refID, idp.key)
	if err != nil {
		t.Fatal(err)
	}

	return sig
}

// samlHarness is a server with SAML configured against a fake identity provider.
type samlHarness struct {
	*harness

	idp    samlTestIdP
	acsURL string
}

func newSAMLHarness(t *testing.T, tweak func(*SAMLConfig)) samlHarness {
	t.Helper()

	h := newHarness(t)
	idp := newSAMLTestIdP(t)

	const acs = "https://perfuse.test/auth/saml/acs"

	cfg := &SAMLConfig{
		EntityID:        "https://perfuse.test",
		ACSURL:          acs,
		IdPSSOURL:       "https://idp.test/sso",
		IdPCertPEM:      idp.pem,
		GroupsAttribute: "groups",
		Roles: &oidc.RoleMapping{
			Admin:  []string{"perfuse-admins"},
			Editor: []string{"perfuse-editors"},
			Viewer: []string{"perfuse-viewers"},
		},
		CreateUsers: true,
		Label:       "Sign in with Test IdP",
	}

	if tweak != nil {
		tweak(cfg)
	}

	sp, err := saml.New(saml.Config{
		EntityID:         cfg.EntityID,
		ACSPath:          cfg.ACSURL,
		IdPSSOURL:        cfg.IdPSSOURL,
		IdPCertPEM:       cfg.IdPCertPEM,
		AllowUnsolicited: cfg.AllowUnsolicited,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg.SP = sp
	h.server.SAML = cfg

	return samlHarness{harness: h, idp: idp, acsURL: acs}
}

// start follows the sign-in redirect and returns the request id the server is now waiting for.
func (sh samlHarness) start(t *testing.T, returnTo string) string {
	t.Helper()

	target := "/auth/saml/start"
	if returnTo != "" {
		target += "?returnTo=" + url.QueryEscape(returnTo)
	}

	req := httptest.NewRequest(http.MethodGet, target, nil)
	res := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusFound {
		t.Fatalf("start returned %d, want 302: %s", res.Code, res.Body.String())
	}

	loc := res.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://idp.test/sso") {
		t.Fatalf("the browser was not sent to the identity provider: %q", loc)
	}

	// The request id is inside the deflated, base64-encoded AuthnRequest. Rather than decode it, read what the server recorded -
	// there is exactly one outstanding request at this point.
	sh.server.ensureSAMLRequests()
	sh.server.samlPending.mu.Lock()
	defer sh.server.samlPending.mu.Unlock()

	if len(sh.server.samlPending.ids) != 1 {
		t.Fatalf("the server is tracking %d outstanding requests, want 1", len(sh.server.samlPending.ids))
	}

	for id := range sh.server.samlPending.ids {
		return id
	}

	return ""
}

// post sends a SAMLResponse to the assertion consumer endpoint.
func (sh samlHarness) post(t *testing.T, response string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{}
	form.Set("SAMLResponse", base64.StdEncoding.EncodeToString([]byte(response)))

	req := httptest.NewRequest(http.MethodPost, "/auth/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(res, req)

	return res
}

func TestSAMLSignInCreatesASession(t *testing.T) {
	sh := newSAMLHarness(t, nil)

	requestID := sh.start(t, "/channels")

	response := sh.idp.signResponse(t, "_r1", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors"}})

	res := sh.post(t, response)
	if res.Code != http.StatusFound {
		t.Fatalf("returned %d, want 302: %s", res.Code, res.Body.String())
	}

	// The session cookie is the whole point: without it nothing was signed in.
	var session string

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}

	if session == "" {
		t.Fatal("no session cookie was set, so nobody was signed in")
	}

	// And it has to work. A cookie that does not authenticate a request is the same failure as no cookie, one step later.
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})

	who := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(who, req)

	if who.Code != http.StatusOK {
		t.Fatalf("the session cookie does not authenticate: %d %s", who.Code, who.Body.String())
	}

	var me struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(who.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}

	// The username comes from the email local part rather than the NameID, because a NameID is often opaque and nobody wants to
	// read one in an audit log next to a channel that changed.
	if me.Username != "grace" {
		t.Errorf("username = %q, want grace", me.Username)
	}

	// The role comes from the group, which is the part an administrator configures and the part that decides what somebody can do.
	if me.Role != "editor" {
		t.Errorf("role = %q, want editor", me.Role)
	}

	// Sent where they were going, not to the root.
	if loc := res.Header().Get("Location"); loc != "/channels" {
		t.Errorf("redirected to %q, want /channels", loc)
	}
}

func TestSAMLGroupsDecideTheRole(t *testing.T) {
	for _, c := range []struct{ group, want string }{
		{"perfuse-admins", "admin"},
		{"perfuse-editors", "editor"},
		{"perfuse-viewers", "viewer"},
	} {
		t.Run(c.group, func(t *testing.T) {
			sh := newSAMLHarness(t, nil)
			requestID := sh.start(t, "")

			response := sh.idp.signResponse(t, "_r_"+c.group, requestID, c.group+"@hospital.test", sh.acsURL,
				"https://perfuse.test", map[string][]string{"groups": {c.group}})

			res := sh.post(t, response)
			if res.Code != http.StatusFound {
				t.Fatalf("returned %d: %s", res.Code, res.Body.String())
			}

			var session string

			for _, ck := range res.Result().Cookies() {
				if ck.Name == sessionCookie {
					session = ck.Value
				}
			}

			if session == "" {
				t.Fatal("no session")
			}

			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})

			who := httptest.NewRecorder()
			sh.server.Handler().ServeHTTP(who, req)

			var me struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(who.Body.Bytes(), &me)

			if me.Role != c.want {
				t.Errorf("role = %q, want %q", me.Role, c.want)
			}
		})
	}
}

func TestSAMLSignInWithNoMatchingGroupIsRefused(t *testing.T) {
	// Refused rather than admitted with the least privilege. A person in no mapped group has not been granted access here, and
	// giving them read access to a clinical integration engine because it seems harmless is a decision nobody made.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_nogroup", requestID, "nobody@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"some-other-group"}})

	res := sh.post(t, response)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("a session was created for somebody in no mapped group")
		}
	}

	if loc := res.Header().Get("Location"); !strings.Contains(loc, "signInError=") {
		t.Errorf("no reason was given to the sign-in page: %q", loc)
	}
}

func TestSAMLResponseSignedByTheWrongKeyIsRefused(t *testing.T) {
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	// A different identity provider entirely, signing an otherwise perfect response.
	other := newSAMLTestIdP(t)
	response := other.signResponse(t, "_r_wrongkey", requestID, "attacker@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-admins"}})

	res := sh.post(t, response)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("a response signed by an unknown key created a session")
		}
	}
}

func TestSAMLResponseAnsweringNoRequestIsRefused(t *testing.T) {
	// The wiring's half of the InResponseTo work. The endpoint must not accept a response that answers nothing, or a valid response
	// obtained anywhere can be posted into anybody's browser.
	sh := newSAMLHarness(t, nil)

	// Deliberately no start call, so the server is waiting for nothing.
	response := sh.idp.signResponse(t, "_r_unsolicited", "", "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-admins"}})

	res := sh.post(t, response)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("an unsolicited response created a session with AllowUnsolicited off")
		}
	}
}

func TestTheSameSAMLResponseCannotBeUsedTwice(t *testing.T) {
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_replay", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors"}})

	if res := sh.post(t, response); res.Code != http.StatusFound {
		t.Fatalf("the first use failed: %d %s", res.Code, res.Body.String())
	}

	second := sh.post(t, response)

	for _, c := range second.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("the same response signed somebody in twice")
		}
	}
}

func TestTheSignInPageIsToldAboutSAML(t *testing.T) {
	// Without this the endpoints exist and nothing offers them, which is the state the whole package was in.
	sh := newSAMLHarness(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/methods", nil)
	res := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body.String())
	}

	var out struct {
		SAML *struct {
			Label   string `json:"label"`
			StartAt string `json:"startAt"`
		} `json:"saml"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.SAML == nil {
		t.Fatal("the sign-in page is not told that SAML is available, so no button can appear")
	}

	if out.SAML.Label != "Sign in with Test IdP" {
		t.Errorf("label = %q", out.SAML.Label)
	}

	if out.SAML.StartAt != "/auth/saml/start" {
		t.Errorf("startAt = %q", out.SAML.StartAt)
	}
}

func TestTheSignInPageIsNotToldAboutSAMLWhenItIsNotConfigured(t *testing.T) {
	// The positive control for the test above: a page that always showed the button would pass it.
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/methods", nil)
	res := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(res, req)

	var out map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &out)

	if _, present := out["saml"]; present {
		t.Error("SAML is advertised on a server where it is not configured, so the button would answer 404")
	}
}

func TestAReturnToOutsideThisSiteIsIgnored(t *testing.T) {
	// A sign-in endpoint that forwards a browser anywhere after authenticating is an open redirect, and an open redirect on a login
	// page is how a convincing phishing link is built: the domain in the address bar is genuinely Perfuse.
	for _, hostile := range []string{
		"https://evil.test/steal",
		"//evil.test/steal",
		"/\\evil.test",
		"/ok\r\nSet-Cookie: x=1",
	} {
		if got := safeReturnTo(hostile); got != "/" {
			t.Errorf("safeReturnTo(%q) = %q, want /", hostile, got)
		}
	}

	// And a real path still works, or the check would be protecting nothing by breaking everything.
	if got := safeReturnTo("/channels/adt-inbound"); got != "/channels/adt-inbound" {
		t.Errorf("a legitimate path was rejected: %q", got)
	}
}

func TestSAMLReturnToComesFromOurRecordAndNotFromRelayState(t *testing.T) {
	// RelayState is echoed back by the identity provider and therefore arrives from outside. Reading the destination from it would
	// reintroduce the open redirect that safeReturnTo closes, one layer further in, so the destination is read from the request
	// this server recorded.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "/channels")

	response := sh.idp.signResponse(t, "_r_relay", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors"}})

	form := url.Values{}
	form.Set("SAMLResponse", base64.StdEncoding.EncodeToString([]byte(response)))
	form.Set("RelayState", "https://evil.test/steal")

	req := httptest.NewRequest(http.MethodPost, "/auth/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(res, req)

	if loc := res.Header().Get("Location"); loc != "/channels" {
		t.Errorf("redirected to %q; RelayState was followed instead of our own record", loc)
	}
}

func TestARequestCanOnlyBeAnsweredOnce(t *testing.T) {
	// Isolates the endpoint's own barrier from the parser's.
	//
	// TestTheSameSAMLResponseCannotBeUsedTwice does not test this, which I found by breaking it: making the request store recognise
	// an id without consuming it left every SAML test passing, because the assertion-id replay cache in the parser refused the
	// duplicate first. Two barriers were claimed and one was tested.
	//
	// So this uses two genuinely different responses - different assertion ids, so the parser's cache has no opinion - that name the
	// same AuthnRequest. The second must be refused because the request has been answered, and nothing else in the stack has any
	// reason to object. The barrier matters because the parser's cache is in memory and does not survive a restart, while a request
	// that has been answered should stay answered for as long as it is remembered at all.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	first := sh.idp.signResponse(t, "_r_once_a", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors"}})

	if res := sh.post(t, first); res.Code != http.StatusFound {
		t.Fatalf("the first response was refused: %d %s", res.Code, res.Body.String())
	}

	// A different response entirely, naming the same request. This is what an attacker holding a second valid assertion would send.
	second := sh.idp.signResponse(t, "_r_once_b", requestID, "attacker@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-admins"}})

	res := sh.post(t, second)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("a second, different response answered the same request: the request was recognised but never consumed")
		}
	}
}
