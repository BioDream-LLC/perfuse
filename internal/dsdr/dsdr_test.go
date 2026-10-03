package dsdr

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func discharge(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/discharge.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestALegalAuthenticatorSignsAtXAdESXL(t *testing.T) {
	for _, ec := range []bool{false, true} {
		p := newTestPKI(t, ec)
		res, err := Sign(discharge(t), p.options())
		if err != nil {
			t.Fatal(err)
		}
		if res.Participant != "legalAuthenticator" || res.Level != "X-L" || !res.Conforms || len(res.Missing) != 0 {
			t.Fatalf("ec=%v: %v", ec, res)
		}
		if !strings.Contains(res.Thumbnail, "Digitally signed by Authorized Signer Pat Clinician") ||
			!strings.Contains(res.Thumbnail, "Author's signature") {
			t.Errorf("thumbnail %q", res.Thumbnail)
		}
		reports, err := Verify(res.Document, VerifyOptions{Roots: p.roots()})
		if err != nil || len(reports) != 1 {
			t.Fatalf("%v %d", err, len(reports))
		}
		r := reports[0]
		if !r.Sound() || r.Level != "X-L" || !strings.HasPrefix(r.Revocation, "good") || r.TimeStamped == nil ||
			r.Purpose != "8.2.1.1 - Author's signature" || r.Role != "207R00000X - Internal Medicine" || len(r.Problems) != 0 {
			t.Errorf("ec=%v: %+v", ec, r)
		}
	}
}

func TestASecondSignerDoesNotBreakTheFirst(t *testing.T) {
	p := newTestPKI(t, false)
	first, err := Sign(discharge(t), p.options())
	if err != nil {
		t.Fatal(err)
	}
	o := p.options()
	o.SignerName, o.Role, o.RoleDisplay = "Sam Nurse", "163W00000X", "Registered Nurse"
	second, err := Sign(first.Document, o)
	if err != nil {
		t.Fatal(err)
	}
	if second.Participant != "authenticator" || !strings.Contains(second.Thumbnail, "Coauthor's signature") {
		t.Fatalf("%v", second)
	}
	// The authenticator goes after the legalAuthenticator, where CDA's header order puts it.
	d := string(second.Document)
	if strings.Index(d, "<authenticator>") < strings.Index(d, "</legalAuthenticator>") || strings.Index(d, "<authenticator>") > strings.Index(d, "<componentOf>") {
		t.Error("the authenticator is out of CDA header order")
	}
	reports, _ := Verify(second.Document, VerifyOptions{Roots: p.roots()})
	if len(reports) != 2 || !reports[0].Sound() || !reports[1].Sound() {
		t.Fatalf("%+v", reports)
	}
}

func TestChangingTheDocumentBreaksTheSignature(t *testing.T) {
	p := newTestPKI(t, false)
	res, _ := Sign(discharge(t), p.options())
	tampered := bytes.Replace(res.Document, []byte("1 g IV daily"), []byte("2 g IV daily"), 1)
	reports, _ := Verify(tampered, VerifyOptions{})
	if reports[0].Sound() || reports[0].DigestValid || !reports[0].SignatureValid {
		t.Fatalf("%+v", reports[0])
	}
	if !strings.Contains(strings.Join(reports[0].Problems, " "), "changed since it was signed") {
		t.Error(reports[0].Problems)
	}
}

func TestATrustStoreThatDoesNotKnowTheCAIsReported(t *testing.T) {
	p := newTestPKI(t, false)
	other := newTestPKI(t, false)
	res, _ := Sign(discharge(t), p.options())
	reports, _ := Verify(res.Document, VerifyOptions{Roots: other.roots()})
	if reports[0].Sound() || !reports[0].TrustChecked || reports[0].Trusted || !reports[0].DigestValid {
		t.Fatalf("%+v", reports[0])
	}
}

func TestARevokedCertificateIsRefusedAtSigning(t *testing.T) {
	p := newTestPKI(t, false)
	p.revoked = true
	res, err := Sign(discharge(t), p.options())
	if err != nil {
		t.Fatal(err)
	}
	if res.Conforms || res.Level == "X-L" || !strings.Contains(strings.Join(res.Missing, " "), "revoked") {
		t.Fatalf("%v", res)
	}
}

func TestWithoutATimeStampingAuthorityTheShortfallIsNamed(t *testing.T) {
	p := newTestPKI(t, false)
	o := p.options()
	o.TSAURL = ""
	res, err := Sign(discharge(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conforms || res.Level != "EPES" || !strings.Contains(res.Missing[0], "time-stamping authority") {
		t.Fatalf("%v", res)
	}
	reports, _ := Verify(res.Document, VerifyOptions{})
	if !reports[0].Sound() || reports[0].Level != "EPES" {
		t.Fatalf("%+v", reports[0])
	}
}

func TestSigningNeedsARoleAndAKnownPurpose(t *testing.T) {
	p := newTestPKI(t, false)
	o := p.options()
	o.Role = ""
	if _, err := Sign(discharge(t), o); err == nil || !strings.Contains(err.Error(), "ESMD-3") {
		t.Error(err)
	}
	o = p.options()
	o.Purpose = "9.9"
	if _, err := Sign(discharge(t), o); err == nil || !strings.Contains(err.Error(), "Appendix E") {
		t.Error(err)
	}
	if _, err := Sign([]byte(`<Bundle xmlns="http://hl7.org/fhir"/>`), p.options()); err == nil {
		t.Error("a non-CDA document was signed")
	}
}

func TestAnUnsignedDocumentHasNoReports(t *testing.T) {
	r, err := Verify(discharge(t), VerifyOptions{})
	if err != nil || len(r) != 0 {
		t.Fatal(err, r)
	}
}
