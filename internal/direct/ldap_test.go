package direct

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func ldapTestCert(t *testing.T, email, dnsName string) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: email + dnsName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if email != "" {
		tmpl.EmailAddresses = []string{email}
	}
	if dnsName != "" {
		tmpl.DNSNames = []string{dnsName}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

// Against OpenLDAP, when PERFUSE_TEST_OPENLDAP is container:host:port for an osixia/openldap container started with
// LDAP_DOMAIN=direct.clinic.example and LDAP_ADMIN_PASSWORD=pftest-admin, and anonymous read of the published attributes
// granted, as a certificate directory grants it (osixia's default lets only the administrator read):
//
//	printf 'dn: olcDatabase={1}mdb,cn=config\nchangetype: modify\nadd: olcAccess\nolcAccess: {2}to dn.subtree="dc=direct,dc=clinic,dc=example" attrs=entry,objectClass,mail,cn,userSMIMECertificate,userCertificate by * read\n' |
//	  docker exec -i CONTAINER ldapmodify -Q -Y EXTERNAL -H ldapi:///
//
// The entries are written with the container's own ldapadd, so the directory holds them the way OpenLDAP stores them, not
// the way this package would.
func TestCertificatesAreFoundInOpenLDAP(t *testing.T) {
	spec := os.Getenv("PERFUSE_TEST_OPENLDAP")
	if spec == "" {
		t.Skip("set PERFUSE_TEST_OPENLDAP=container:host:port")
	}
	container, server, _ := strings.Cut(spec, ":")
	stamp := time.Now().Format("150405.000000")
	stamp = strings.ReplaceAll(stamp, ".", "")
	person := "intake" + stamp + "@direct.clinic.example"
	personCert := ldapTestCert(t, person, "")
	domainCert := ldapTestCert(t, "", "direct.clinic.example")
	b64 := func(c *x509.Certificate) string { return base64.StdEncoding.EncodeToString(c.Raw) }
	ldif := "dn: cn=intake" + stamp + ",dc=direct,dc=clinic,dc=example\nobjectClass: inetOrgPerson\ncn: intake" + stamp + "\nsn: Intake\n" +
		"mail: " + person + "\nuserSMIMECertificate:: " + b64(personCert) + "\n\n"
	// The domain-bound certificate once per container: a second run finds the first's.
	add := func(text string) {
		cmd := exec.Command("docker", "exec", "-i", container, "ldapadd", "-x", "-c", "-H", "ldap://localhost", "-D",
			"cn=admin,dc=direct,dc=clinic,dc=example", "-w", "pftest-admin")
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil && !bytes.Contains(out, []byte("Already exists")) {
			t.Fatalf("ldapadd: %v %s", err, out)
		}
	}
	add(ldif)
	add("dn: cn=domain,dc=direct,dc=clinic,dc=example\nobjectClass: inetOrgPerson\ncn: domain\nsn: Domain\nmail: direct.clinic.example\n" +
		"userCertificate;binary:: " + b64(domainCert) + "\n\n")

	ctx := context.Background()
	certs, err := LDAPCertificates(ctx, person, server)
	if err != nil || len(certs) != 1 || !certs[0].Equal(personCert) {
		t.Fatalf("the address's certificate: %v %d", err, len(certs))
	}
	// An address with no entry of its own gets the domain's certificate.
	certs, err = LDAPCertificates(ctx, "nobody"+stamp+"@direct.clinic.example", server)
	if err != nil || len(certs) == 0 || len(certs[0].DNSNames) == 0 || certs[0].DNSNames[0] != "direct.clinic.example" {
		t.Fatalf("the domain's certificate: %v %d", err, len(certs))
	}
	// DNS has nothing for this invented domain, so FindCertificates falls through to the directory, and says so.
	certs, from, err := FindCertificates(ctx, person, "127.0.0.1:1", server)
	if err != nil || from != "ldap" || len(certs) != 1 {
		t.Fatalf("DNS then LDAP: %v %q %d", err, from, len(certs))
	}
	if _, _, err := FindCertificates(ctx, person, "127.0.0.1:1", "off"); err == nil {
		t.Error("ldap off still asked the directory")
	}
}

// With no directory named, the domain's SRV record names it; an invented domain has none, which is said plainly.
func TestADomainWithNoDirectoryIsSaidSo(t *testing.T) {
	_, err := LDAPCertificates(context.Background(), "a@nowhere.invalid", "")
	if err == nil || !strings.Contains(err.Error(), "_ldap._tcp.nowhere.invalid") {
		t.Errorf("%v", err)
	}
	if _, err := LDAPCertificates(context.Background(), "not-an-address", "127.0.0.1:1"); err == nil {
		t.Error("a non-address was looked up")
	}
}
