package udap

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole business-to-business flow against a real UDAP authorization server and FHIR server, with a client certificate the
// community trusts: discovery, the signed metadata checked against the community's root, dynamic registration, a token with the
// hl7-b2b extension, and a FHIR read with that token.
//
// The servers are the udap-dotnet reference implementation's tutorial (github.com/JoeShook/udap-dotnet-tutorial: Duende
// IdentityServer with Udap.Server, and a FHIR server publishing UDAP metadata), built from source and run in Docker. The client
// certificate is issued from that community's sub-CA, as a member's would be. PERFUSE_TEST_UDAP_DEVDAYS names a directory with
// ca.crt (the community root), subca.crt, client.crt and client.key; the certificate's SAN URI is the client URI.
//
// The tutorial's servers name themselves host.docker.internal, which the Mac does not resolve, so the test dials 127.0.0.1.
func TestTheWholeFlowAgainstTheUDAPReferenceServer(t *testing.T) {
	dir := os.Getenv("PERFUSE_TEST_UDAP_DEVDAYS")
	if dir == "" {
		t.Skip("PERFUSE_TEST_UDAP_DEVDAYS is not set (the udap-dotnet tutorial's servers and a community client certificate)")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cert := func(name string) *x509.Certificate {
		blk, _ := pem.Decode(read(name))
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatal(name, err)
		}
		return c
	}
	root, sub, leaf := cert("ca.crt"), cert("subca.crt"), cert("client.crt")
	blk, _ := pem.Decode(read("client.key"))
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		// The servers' TLS certificate is issued by the same community root.
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			addr = strings.Replace(addr, "host.docker.internal", "127.0.0.1", 1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const base = "https://host.docker.internal:7017/fhir/r4"
	md, err := Fetch(ctx, client, base, "")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	inters := x509.NewCertPool()
	inters.AddCert(sub)
	claims, err := VerifySignedMetadata(md, VerifyOptions{BaseURL: base, Anchors: TrustAnchors{Roots: roots, Intermediates: inters}})
	if err != nil {
		t.Fatalf("the server's signed metadata did not verify against the community root: %v", err)
	}
	if claims.RegistrationEndpoint == "" || claims.TokenEndpoint == "" {
		t.Fatalf("the signed metadata names no endpoints: %+v", claims)
	}

	id := &Identity{ClientURI: leaf.URIs[0].String(), PrivateKey: k.(*rsa.PrivateKey), Chain: []*x509.Certificate{leaf, sub}}
	reg, err := id.Register(ctx, client, claims.RegistrationEndpoint, RegistrationRequest{
		ClientName: "Perfuse UDAP test client",
		Contacts:   []string{"mailto:support@perfuse.example.test"},
		Scope:      "system/Patient.read",
	})
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	if reg.ClientID == "" || reg.Cancelled() {
		t.Fatalf("registration gave no client: %+v", reg)
	}
	t.Logf("registered as %s with scope %q", reg.ClientID, reg.Scope)

	tok, err := id.RequestToken(ctx, client, TokenRequest{
		ClientID: reg.ClientID, TokenEndpoint: claims.TokenEndpoint, Scope: "system/Patient.read",
		B2B: &B2BExtension{OrganizationID: "https://perfuse.example.test/org", OrganizationName: "Perfuse test",
			PurposeOfUse: []string{"urn:oid:2.16.840.1.113883.5.8#TREAT"}},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.AccessToken == "" || tok.Expired(time.Now()) {
		t.Fatalf("no usable token: %+v", tok)
	}

	// The token is judged by the FHIR server's own authorization check. That server's demo data layer then fails on every
	// read (a library version mismatch inside the tutorial: MissingMethodException in its search indexer, with any token and
	// on any resource), so what this proves is the gate: our token passes it, a forged one and none do not.
	const patient = base + "/Patient/9fb5aaa39b6241a69784114a92e56025"
	status := func(bearer string) (int, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, patient, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Accept", "application/fhir+json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return res.StatusCode, string(body)
	}
	code, body := status(tok.AccessToken)
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("the FHIR server refused our token: %d %.300s", code, body)
	}
	if code != http.StatusOK && !strings.Contains(body, "SearchIndexer") {
		t.Fatalf("the FHIR read with our token answered %d: %.300s", code, body)
	}
	var got struct{ ResourceType string }
	if code == http.StatusOK && (json.Unmarshal([]byte(body), &got) != nil || got.ResourceType != "Patient") {
		t.Fatalf("the FHIR read answered something other than the Patient: %.300s", body)
	}
	if code, _ := status("forged." + tok.AccessToken[strings.Index(tok.AccessToken, ".")+1:]); code != http.StatusUnauthorized {
		t.Errorf("a forged token answered %d, want 401", code)
	}

	// Without the token the same read is refused: the server really is protected.
	if code, _ := status(""); code != http.StatusUnauthorized {
		t.Errorf("the FHIR read without a token answered %d, want 401", code)
	}
}
