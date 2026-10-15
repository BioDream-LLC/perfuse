package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func directTestCert(t *testing.T, email, dnsName string) (*x509.Certificate, string) {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: email + dnsName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if email != "" {
		tpl.EmailAddresses = []string{email}
	}
	if dnsName != "" {
		tpl.DNSNames = []string{dnsName}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	c, _ := x509.ParseCertificate(der)
	return c, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTheDirectAddressBook(t *testing.T) {
	h := newHarness(t)
	h.server.DirectDir = filepath.Join(h.dir, "direct-certs")
	_, bobPEM := directTestCert(t, "bob@direct.clinic.example", "")
	_, otherPEM := directTestCert(t, "eve@elsewhere.example", "")

	if rr := h.do("viewer", "PUT", "/api/direct/contacts/bob@direct.clinic.example", map[string]any{"certificate": bobPEM}); rr.Code != http.StatusForbidden {
		t.Errorf("a viewer saved a contact: %d", rr.Code)
	}
	if rr := h.do("editor", "PUT", "/api/direct/contacts/bob@direct.clinic.example", map[string]any{"name": "Dr Bob Smith", "certificate": otherPEM}); rr.Code != http.StatusBadRequest {
		t.Errorf("a certificate for someone else was taken: %d %s", rr.Code, rr.Body)
	}
	if rr := h.do("editor", "PUT", "/api/direct/contacts/..%2Fescape", map[string]any{"name": "x"}); rr.Code != http.StatusBadRequest {
		t.Errorf("a path was taken as an address: %d", rr.Code)
	}
	rr := h.do("editor", "PUT", "/api/direct/contacts/bob@direct.clinic.example", map[string]any{"name": "Dr Bob Smith", "organisation": "Clinic", "certificate": bobPEM})
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if _, err := os.Stat(filepath.Join(h.server.DirectDir, "bob@direct.clinic.example.pem")); err != nil {
		t.Fatal("the certificate is not where a direct destination reads it")
	}
	// A domain-bound certificate found in DNS.
	domainCert, _ := directTestCert(t, "", "direct.hospital.example")
	orig := directLookup
	t.Cleanup(func() { directLookup = orig })
	directLookup = func(_ context.Context, addr, _ string) ([]*x509.Certificate, error) {
		if addr == "postmaster@direct.hospital.example" || addr == "any@direct.hospital.example" {
			return []*x509.Certificate{domainCert}, nil
		}
		return nil, nil
	}
	if rr := h.do("editor", "PUT", "/api/direct/contacts/direct.hospital.example", map[string]any{"lookUp": true}); rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if rr := h.do("editor", "PUT", "/api/direct/contacts/x@nowhere.example", map[string]any{"lookUp": true}); rr.Code != http.StatusNotFound {
		t.Errorf("nothing in DNS: %d", rr.Code)
	}
	rr = h.do("viewer", "POST", "/api/direct/lookup", map[string]any{"address": "any@direct.hospital.example"})
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	rr = h.do("viewer", "GET", "/api/direct", nil)
	var got struct {
		Contacts []DirectContact `json:"contacts"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got.Contacts) != 2 || got.Contacts[0].Name != "Dr Bob Smith" || len(got.Contacts[0].Certs) != 1 || got.Contacts[1].Source != "dns" {
		t.Fatalf("%s", rr.Body)
	}
	if rr := h.do("viewer", "GET", "/api/direct/contacts/bob@direct.clinic.example/certificate", nil); rr.Code != http.StatusOK || rr.Body.String() != bobPEM {
		t.Errorf("download: %d", rr.Code)
	}
	if rr := h.do("editor", "DELETE", "/api/direct/contacts/bob@direct.clinic.example", nil); rr.Code != http.StatusOK {
		t.Fatalf("%d", rr.Code)
	}
	if rr := h.do("editor", "DELETE", "/api/direct/contacts/bob@direct.clinic.example", nil); rr.Code != http.StatusNotFound {
		t.Errorf("deleting twice: %d", rr.Code)
	}
}
