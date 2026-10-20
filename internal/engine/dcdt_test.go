package engine

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/direct"
)

// The discovery test cases of ONC's Direct Certificate Discovery Tool (DCDT, site.healthit.gov/direct/dcdt), run live:
// PERFUSE_TEST_DCDT names the tool's domain (dcdt31.healthit.gov in October 2026) and PERFUSE_TEST_DCDT_DNS a resolver
// (8.8.8.8:53 by default). Each case is the certificate a sender must choose for the address, or none. The tool's own
// verdict comes by mail after a message is sent; this asks the same question without sending, by running the Direct
// destination's selection: discovery in DNS then LDAP, binding, the chain to the tool's root (with AIA issuers), and CRLs.
func TestTheDCDTDiscoveryCases(t *testing.T) {
	domain := os.Getenv("PERFUSE_TEST_DCDT")
	if domain == "" {
		t.Skip("set PERFUSE_TEST_DCDT to the DCDT's domain, such as dcdt31.healthit.gov")
	}
	dns := os.Getenv("PERFUSE_TEST_DCDT_DNS")
	if dns == "" {
		dns = "8.8.8.8:53"
	}
	// The tool's trust anchor is its root CA, published where its certificates' AIA points.
	res, err := http.Get("http://pki." + domain + ":10080/" + domain + "_ca_root.cer")
	if err != nil {
		t.Fatal(err)
	}
	der, _ := io.ReadAll(res.Body)
	res.Body.Close()
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("the DCDT root: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	s := &DirectSender{cfg: &config.DirectDestination{DNS: dns}, anchors: pool, now: time.Now,
		lookup: func(ctx context.Context, address, server string) ([]*x509.Certificate, error) {
			c, _, err := direct.FindCertificates(ctx, address, server, "")
			return c, err
		}}
	for _, c := range []struct{ n, dom, want string }{
		{"1", "domain1", "D1_valA"}, {"2", "domain1", "D2_valB"}, {"3", "domain2", "D3_valC"}, {"4", "domain2", "D4_valD"},
		{"5", "domain1", ""}, {"6", "domain4", ""}, {"7", "domain2", ""}, {"8", "domain5", ""}, {"9", "domain1", "D9_valA"},
		{"10", "domain3", "D10_valE"}, {"11", "domain6", ""}, {"12", "domain7", ""}, {"13", "domain8", ""}, {"14", "domain1", "D14_valA"},
		{"15", "domain2", "D15_valC"}, {"16", "domain5", "D16_valC"}, {"17", "domain9", "D17_valA"}, {"18", "domain10", "D18_valA"},
	} {
		t.Run("D"+c.n, func(t *testing.T) {
			got, err := s.recipientCert(context.Background(), "d"+c.n+"@"+c.dom+"."+domain)
			var cns []string
			for _, g := range got {
				cns = append(cns, g.Subject.CommonName)
			}
			switch {
			case c.want == "" && err == nil:
				t.Errorf("a certificate was chosen where none is valid: %v", cns)
			case c.want != "" && (err != nil || len(cns) != 1 || cns[0] != c.want):
				t.Errorf("want %s, got %v (%v)", c.want, cns, err)
			}
		})
	}
}
