package tefca

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAFacilitatedFHIRQueryAgainstTheUDAPReferenceServer runs the whole Facilitated FHIR path the tefca destination uses
// against the udap-dotnet reference implementation: discovery and the signed metadata checked against the community
// root, dynamic registration with this participant's certificate, a client_credentials token whose hl7-b2b extension
// carries the TEFCA Exchange Purpose, and the Patient search with that token, audited.
//
// PERFUSE_TEST_UDAP_DEVDAYS names the directory internal/udap's reference test uses (ca.crt, subca.crt, client.crt,
// client.key), with the tutorial's auth, certificate and FHIR servers running in Docker.
//
// The tutorial's servers are configured as a TEFCA partner would be in one respect: the FHIR server advertises and requires
// hl7-b2b (udap.metadata.options.json, UdapAuthorizationExtensionsSupported and Required), which the auth server already
// enforces for Community1. They are HL7 FAST SSRAA rather than TEFCA, so the purpose is sent as an HL7 PurposeOfUse code.
//
// The tutorial's FHIR server passes our token through its authorization check and then fails inside its own demo data
// layer on every read (a library mismatch in the tutorial: MissingMethodException in its search indexer), so the search
// answers 500 there. What this proves is everything up to and including the FHIR server accepting the token; a 401 or
// 403 would fail it.
func TestAFacilitatedFHIRQueryAgainstTheUDAPReferenceServer(t *testing.T) {
	dir := os.Getenv("PERFUSE_TEST_UDAP_DEVDAYS")
	if dir == "" {
		t.Skip("PERFUSE_TEST_UDAP_DEVDAYS is not set (the udap-dotnet tutorial's servers and a community client certificate)")
	}
	tmp := t.TempDir()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	chain := filepath.Join(tmp, "chain.pem")
	if err := os.WriteFile(chain, append(read("client.crt"), read("subca.crt")...), 0o600); err != nil {
		t.Fatal(err)
	}
	anchors := filepath.Join(tmp, "anchors.pem")
	if err := os.WriteFile(anchors, append(read("ca.crt"), read("subca.crt")...), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(read("ca.crt"))
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, strings.Replace(addr, "host.docker.internal", "127.0.0.1", 1))
		},
	}}

	audit := &AuditLog{}
	p, err := NewParticipant(TEFCAConfig{OrganizationName: "Perfuse test hospital", OrganizationOID: "2.16.840.1.113883.3.9999",
		QHINEndpoint: "https://host.docker.internal:7017/fhir/r4", ParticipantType: "provider",
		CertificatePath: chain, KeyPath: filepath.Join(dir, "client.key"),
		SupportedPurposes: []string{PurposeTreatment}}, audit)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := NewExchange(p.Config(), ExchangeConfig{PartnerFHIRBase: "https://host.docker.internal:7017/fhir/r4",
		ClientURI: "https://perfuse.example.test/udap-client", TrustAnchorPath: anchors, ClientName: "Perfuse test hospital",
		Contacts: []string{"mailto:interfaces@perfuse.example.test"}, Scope: "system/Patient.read",
		// The reference server implements HL7 FAST SSRAA, whose purpose codes are HL7 PurposeOfUse; it refuses the TEFCA
		// Exchange Purpose codes a QHIN requires, which is the case this setting exists for.
		PurposeCodes: "hl7"}, client)
	if err != nil {
		t.Fatal(err)
	}
	p.AttachExchange(ex)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A purpose this participant does not declare is refused here, and audited, before anything is sent.
	if _, err := p.QueryFHIR(ctx, QueryRequest{PatientID: "MRN-1", Purpose: PurposePayment}); err == nil ||
		!strings.Contains(err.Error(), "not one of the purposes") {
		t.Fatalf("an undeclared purpose: %v", err)
	}

	bundle, err := p.QueryFHIR(ctx, QueryRequest{PatientID: "urn:oid:2.16.840.1.113883.3.9999|MRN-1", Purpose: PurposeTreatment})
	switch {
	case err == nil:
		if !strings.Contains(string(bundle.Raw), `"resourceType"`) {
			t.Fatalf("the search answered no Bundle: %.300s", bundle.Raw)
		}
	case strings.Contains(err.Error(), "answered 401"), strings.Contains(err.Error(), "answered 403"):
		t.Fatalf("the FHIR server refused our token: %v", err)
	case strings.Contains(err.Error(), "answered 500"):
		t.Logf("the token was accepted and the tutorial's data layer failed, as it does on every read: %.200s", err)
	default:
		t.Fatalf("the query failed before reaching the FHIR server: %v", err)
	}

	entries := audit.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if len(entries) != 2 || entries[0].Success || entries[1].ExchangeType != "facilitated-fhir-query" ||
		entries[1].RespondingOrg != "https://host.docker.internal:7017/fhir/r4" {
		t.Fatalf("audit trail %+v", entries)
	}
}
