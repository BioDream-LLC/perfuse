package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Reading a provider's metadata through the endpoint the browser uses.
//
// Tested against documents two real providers published, because the point of this endpoint is to work for providers nobody here has tested.
// A hand-written metadata document would prove the parser can read a hand-written metadata document.
//
// The security half matters as much as the parsing. This endpoint takes a URL from whoever can edit the configuration and makes the server
// fetch it, which is a way to probe a network from outside it or to read cloud instance credentials. Those refusals are tested too.

func readMetadata(t *testing.T, h *harness, body metadataRequest) (int, metadataResponse, string) {
	t.Helper()

	rec := h.do("admin", http.MethodPost, "/api/signon/saml/metadata", body)

	var out metadataResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding the response: %v", err)
		}
	}

	return rec.Code, out, rec.Body.String()
}

func TestReadingKeycloakMetadataFillsInTheConfiguration(t *testing.T) {
	h := newHarness(t)

	doc, err := os.ReadFile("../saml/testdata/keycloak-metadata.xml")
	if err != nil {
		t.Skipf("no Keycloak metadata fixture: %v", err)
	}

	code, out, body := readMetadata(t, h, metadataRequest{Document: string(doc)})
	if code != http.StatusOK {
		t.Fatalf("a real Keycloak metadata document was refused with %d: %s", code, body)
	}

	if out.EntityID == "" {
		t.Error("no entity id, which is what every assertion's Issuer has to match")
	}
	if out.SSOURL == "" {
		t.Error("no sign-on URL, so there would be nowhere to send anybody")
	}

	// The certificate, in the form the configuration file holds, is the whole reason this exists. An administrator had to produce this by hand
	// from the same document.
	if !strings.Contains(out.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("no certificate came back in PEM: %q", out.CertPEM)
	}

	if len(out.Certificates) == 0 {
		t.Fatal("no certificate was described, so somebody would be asked to trust a wall of base64 with nothing said about it")
	}

	// Described in terms a person can check against their provider's console. A fingerprint is the only way to notice a document that was
	// altered between the provider and here.
	first := out.Certificates[0]
	if first.Subject == "" || first.NotAfter == "" || first.Fingerprint == "" {
		t.Errorf("the certificate description is incomplete: %+v", first)
	}
	if strings.Count(first.Fingerprint, ":") != 31 {
		t.Errorf("the fingerprint does not look like a SHA-256 fingerprint: %q", first.Fingerprint)
	}
}

func TestReadingEntraMetadataWorksToo(t *testing.T) {
	h := newHarness(t)

	// Entra writes its metadata without namespace prefixes where Keycloak uses md:, which is the difference that would break a parser written
	// against one vendor's document. Both are checked because the endpoint exists for the providers nobody here has tried.
	doc, err := os.ReadFile("../saml/testdata/entra-metadata.xml")
	if err != nil {
		t.Skipf("no Entra metadata fixture: %v", err)
	}

	code, out, body := readMetadata(t, h, metadataRequest{Document: string(doc)})
	if code != http.StatusOK {
		t.Fatalf("a real Entra metadata document was refused with %d: %s", code, body)
	}

	if !strings.Contains(out.EntityID, "sts.windows.net") {
		t.Errorf("the entity id does not look like Entra's: %q", out.EntityID)
	}
	if !strings.Contains(out.SSOURL, "login.microsoftonline.com") {
		t.Errorf("the sign-on URL does not look like Entra's: %q", out.SSOURL)
	}
	if len(out.Certificates) == 0 {
		t.Error("no signing certificate")
	}
}

func TestAnExpiredCertificateIsSaidToBeExpired(t *testing.T) {
	h := newHarness(t)

	doc, err := os.ReadFile("../saml/testdata/keycloak-metadata.xml")
	if err != nil {
		t.Skip("no fixture")
	}

	_, out, _ := readMetadata(t, h, metadataRequest{Document: string(doc)})
	if len(out.Certificates) == 0 {
		t.Skip("no certificate to check")
	}

	// Reported as a boolean rather than only as a date. A date leaves somebody comparing it by eye, and a signing certificate quietly
	// expiring is an outage nobody predicted.
	for _, cert := range out.Certificates {
		if cert.NotAfter == "" {
			t.Error("a certificate came back with no expiry date")
		}
		_ = cert.Expired
	}
}

func TestAMetadataDocumentThatIsNotOneIsRefusedClearly(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		doc    string
		expect string
	}{
		{
			name:   "a service provider's own descriptor",
			doc:    `<EntityDescriptor entityID="https://example.test/sp"><SPSSODescriptor/></EntityDescriptor>`,
			expect: "not an identity provider",
		},
		{
			name: "no signing certificate",
			doc: `<EntityDescriptor entityID="https://example.test/idp"><IDPSSODescriptor>` +
				`<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://example.test/sso"/>` +
				`</IDPSSODescriptor></EntityDescriptor>`,
			expect: "no signing certificate",
		},
		{
			name:   "not XML at all",
			doc:    `{"this": "is json"}`,
			expect: "metadata",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, body := readMetadata(t, h, metadataRequest{Document: c.doc})
			if code == http.StatusOK {
				t.Fatalf("%s was accepted as an identity provider's metadata", c.name)
			}
			if !strings.Contains(body, c.expect) {
				t.Errorf("the refusal does not mention %q, so somebody would have to guess what was wrong: %s", c.expect, body)
			}
		})
	}
}

func TestMetadataFetchingRefusesWhatItShould(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		url    string
		expect string
	}{
		{
			name: "cloud instance metadata, which holds this machine's credentials",
			url:  "https://169.254.169.254/latest/meta-data/",
			// The egress policy's own words, so this test does not go stale if the wording improves.
			expect: "will not fetch",
		},
		{
			name:   "plain http, which anybody in the path could alter",
			url:    "http://idp.example.invalid/metadata",
			expect: "https",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, body := readMetadata(t, h, metadataRequest{URL: c.url})
			if code == http.StatusOK {
				t.Fatalf("%s was fetched", c.name)
			}
			if !strings.Contains(strings.ToLower(body), strings.ToLower(c.expect)) {
				t.Errorf("the refusal does not explain itself in terms of %q: %s", c.expect, body)
			}
		})
	}
}

func TestLoopbackIsReachableOnPurpose(t *testing.T) {
	h := newHarness(t)

	// Not blocked, and that is the intended behaviour rather than an oversight.
	//
	// The egress policy blocks addresses that hold credentials or infrastructure control - cloud instance metadata and the link-local range -
	// and deliberately not private networks, because hospital systems live on private networks and refusing 10.0.0.0/8 would refuse nearly
	// every real deployment. An identity provider on a private address is an ordinary arrangement.
	//
	// What bounds the risk is that this is admin-only, and an administrator can already point a channel destination anywhere. What comes back
	// is a parsed metadata document or a status code, not the response body, so it is a thin oracle at worst.
	//
	// This test exists because the first version of it asserted that loopback was refused, which was wrong, and a test asserting a protection
	// that does not exist is worse than no test: it reads like evidence.
	code, _, body := readMetadata(t, h, metadataRequest{URL: "https://127.0.0.1:9/metadata"})
	if code == http.StatusOK {
		t.Fatal("a metadata document came back from a port nothing listens on")
	}

	// The failure has to be the connection rather than the policy, or the comment above is out of date.
	if strings.Contains(body, "will not fetch") {
		t.Errorf("loopback is now refused by the egress policy, so the reasoning recorded here needs revisiting: %s", body)
	}
}

func TestBothAURLAndADocumentIsRefused(t *testing.T) {
	h := newHarness(t)

	// Refused rather than one being silently preferred. A form that ignored the field somebody filled in would leave them believing they had
	// configured something they had not.
	code, _, body := readMetadata(t, h, metadataRequest{URL: "https://idp.example.invalid/metadata", Document: "<EntityDescriptor/>"})
	if code == http.StatusOK {
		t.Fatal("both a URL and a document were accepted, so one of them was ignored without saying so")
	}
	if !strings.Contains(body, "not both") {
		t.Errorf("the refusal does not say the two are exclusive: %s", body)
	}
}

func TestNothingAtAllIsRefused(t *testing.T) {
	h := newHarness(t)

	code, _, body := readMetadata(t, h, metadataRequest{})
	if code == http.StatusOK {
		t.Fatal("an empty request was accepted")
	}
	if !strings.Contains(body, "paste") && !strings.Contains(body, "URL") {
		t.Errorf("the refusal does not say what to provide: %s", body)
	}
}

func TestReadingMetadataNeedsAnAdministrator(t *testing.T) {
	h := newHarness(t)

	// It makes the server fetch a URL of the caller's choosing, so it is not a thing an editor or a viewer should be able to do.
	for _, role := range []string{"viewer", "editor"} {
		rec := h.do(role, http.MethodPost, "/api/signon/saml/metadata",
			metadataRequest{URL: "https://idp.example.invalid/metadata"})
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Errorf("a %s could read metadata (%d), which makes the server fetch a URL they chose", role, rec.Code)
		}
	}
}
