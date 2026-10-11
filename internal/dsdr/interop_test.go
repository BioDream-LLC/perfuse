package dsdr

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// A DSDR signature made against real PKI software and checked by implementations that are not Perfuse's:
//
//   - the CA, certificates, OCSP responder and time-stamping authority are OpenSSL's (openssl ca, openssl ocsp, openssl ts);
//   - OpenSSL verifies the time-stamp tokens embedded in the signature over the data XAdES says they cover, and the embedded
//     OCSP response;
//   - xmlsec1 (libxmlsec, in Docker) verifies the XML signature itself - the XPath Filter 2.0 exclusion of the signers, the
//     exclusive canonicalisation, the signed properties reference and the RSA signature - with the certificate chained to the CA.
//
// Skipped without openssl, or without Docker for the xmlsec1 half. PERFUSE_DSDR_INTEROP=1 makes a missing tool a failure.
func TestDSDRAgainstOpenSSLAndXmlsec(t *testing.T) {
	need := func(tool string) bool {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("PERFUSE_DSDR_INTEROP") != "" {
				t.Fatalf("%s is not installed", tool)
			}
			return false
		}
		return true
	}
	if !need("openssl") {
		t.Skip("openssl is not installed")
	}
	home, _ := os.UserHomeDir()
	// Under the home directory, which Docker on macOS (Colima) shares with containers; /tmp it does not.
	dir, err := os.MkdirTemp(home, ".perfuse-dsdr-")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PERFUSE_DSDR_KEEP") == "" {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	} else {
		t.Log("keeping", dir)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("openssl", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("openssl %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	ocspPort := freePort(t)
	ocspURL := fmt.Sprintf("http://127.0.0.1:%d", ocspPort)
	write(t, dir, "ca.cnf", fmt.Sprintf(`[ca]
default_ca = myca
[myca]
dir = .
database = index.txt
new_certs_dir = .
serial = serial
default_md = sha256
policy = anything
copy_extensions = none
default_days = 2
unique_subject = no
[anything]
commonName = supplied
[ext_signer]
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, nonRepudiation
authorityInfoAccess = OCSP;URI:%s
subjectAltName = URI:urn:oid:2.16.840.1.113883.4.6:1234567893
[ext_tsa]
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, timeStamping
[ext_ocsp]
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = OCSPSigning
[tsa_section]
dir = .
serial = tsaserial
signer_cert = tsa.crt
certs = ca.crt
signer_key = tsa.key
signer_digest = sha256
default_policy = 1.2.3.4.1
digests = sha256
ess_cert_id_alg = sha256
accuracy = secs:1
ordering = no
tsa_name = no
ess_cert_id_chain = no
`, ocspURL))
	write(t, dir, "index.txt", "")
	write(t, dir, "serial", "1000\n")
	write(t, dir, "tsaserial", "01\n")
	run("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", "ca.key", "-out", "ca.crt", "-days", "2",
		"-subj", "/CN=Interop Healthcare Root CA", "-addext", "basicConstraints=critical,CA:TRUE",
		"-addext", "keyUsage=critical,keyCertSign,cRLSign")
	for _, c := range []struct{ name, cn, ext string }{
		{"signer", "Pat Clinician", "signer"}, {"tsa", "Interop TSA", "tsa"}, {"ocsp", "Interop OCSP Responder", "ocsp"}} {
		run("req", "-newkey", "rsa:2048", "-nodes", "-keyout", c.name+".key", "-out", c.name+".csr", "-subj", "/CN="+c.cn)
		run("ca", "-batch", "-config", "ca.cnf", "-cert", "ca.crt", "-keyfile", "ca.key", "-in", c.name+".csr", "-out", c.name+".crt",
			"-extensions", "ext_"+c.ext, "-notext")
	}

	// OpenSSL's OCSP responder, answering from the CA's database.
	ocspCmd := exec.Command("openssl", "ocsp", "-index", "index.txt", "-port", fmt.Sprint(ocspPort), "-rsigner", "ocsp.crt",
		"-rkey", "ocsp.key", "-CA", "ca.crt", "-nrequest", "20", "-ignore_err")
	ocspCmd.Dir = dir
	if err := ocspCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ocspCmd.Process.Kill(); _, _ = ocspCmd.Process.Wait() })
	// Ready when it answers a real request. A bare connect-and-close as a probe stalls OpenSSL's single-threaded responder.
	ready := false
	for i := 0; i < 50 && !ready; i++ {
		time.Sleep(100 * time.Millisecond)
		cmd := exec.Command("openssl", "ocsp", "-issuer", "ca.crt", "-cert", "signer.crt", "-url", ocspURL, "-noverify", "-timeout", "2")
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		ready = strings.Contains(string(out), "signer.crt: good")
	}
	if !ready {
		t.Fatal("OpenSSL's OCSP responder did not come up")
	}

	// OpenSSL as the time-stamping authority: each request is answered by openssl ts -reply.
	tsa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := io.ReadAll(r.Body)
		name := fmt.Sprintf("q%d", time.Now().UnixNano())
		_ = os.WriteFile(filepath.Join(dir, name+".tsq"), q, 0o600)
		cmd := exec.Command("openssl", "ts", "-reply", "-config", "ca.cnf", "-section", "tsa_section", "-queryfile", name+".tsq",
			"-out", name+".tsr")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "OPENSSL_CONF="+filepath.Join(dir, "ca.cnf"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("openssl ts -reply: %v\n%s", err, out)
			w.WriteHeader(500)
			return
		}
		resp, _ := os.ReadFile(filepath.Join(dir, name+".tsr"))
		w.Header().Set("Content-Type", "application/timestamp-reply")
		_, _ = w.Write(resp)
	}))
	defer tsa.Close()

	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "signer.crt"), filepath.Join(dir, "signer.key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := x509.ParseCertificate(pair.Certificate[0])
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	caCert := pemCert(t, caPEM)

	res, err := Sign(discharge(t), Options{Key: pair.PrivateKey.(crypto.Signer), Chain: []*x509.Certificate{signer, caCert},
		Role: "207R00000X", RoleDisplay: "Internal Medicine", TSAURL: tsa.URL})
	if err != nil {
		t.Fatal(err)
	}
	if res.Level != "X-L" || !res.Conforms {
		t.Fatalf("against OpenSSL's OCSP responder and TSA: %v", res)
	}
	reports, err := Verify(res.Document, VerifyOptions{Roots: roots})
	if err != nil || len(reports) != 1 || !reports[0].Sound() || reports[0].Level != "X-L" || !strings.HasPrefix(reports[0].Revocation, "good") {
		t.Fatalf("Perfuse's own verification: %v %+v", err, reports)
	}

	// The signature as XML, out of its base64.
	sigXML := decodedSignature(t, res.Document)
	_ = os.WriteFile(filepath.Join(dir, "signed.xml"), res.Document, 0o600)

	// OpenSSL checks each time-stamp over the bytes XAdES says it covers, and the TSA's signature on it.
	sv, err := xmldsig.CanonicaliseNamed(sigXML, "SignatureValue", nsDSig, true)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "sigvalue.c14n", string(sv))
	write(t, dir, "sigts.der", string(b64decode(t, between(t, sigXML, "SignatureTimeStamp", "EncapsulatedTimeStamp"))))
	out := run("ts", "-verify", "-data", "sigvalue.c14n", "-in", "sigts.der", "-token_in", "-CAfile", "ca.crt", "-untrusted", "tsa.crt")
	if !strings.Contains(out, "Verification: OK") {
		t.Fatalf("OpenSSL did not verify the signature time-stamp: %s", out)
	}
	var refs []byte
	for _, name := range []string{"SignatureValue", "SignatureTimeStamp", "CompleteCertificateRefs", "CompleteRevocationRefs"} {
		ns := nsXAdES
		if name == "SignatureValue" {
			ns = nsDSig
		}
		c, err := xmldsig.CanonicaliseNamed(sigXML, name, ns, true)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, c...)
	}
	write(t, dir, "refs.c14n", string(refs))
	write(t, dir, "refsts.der", string(b64decode(t, between(t, sigXML, "SigAndRefsTimeStamp", "EncapsulatedTimeStamp"))))
	out = run("ts", "-verify", "-data", "refs.c14n", "-in", "refsts.der", "-token_in", "-CAfile", "ca.crt", "-untrusted", "tsa.crt")
	if !strings.Contains(out, "Verification: OK") {
		t.Fatalf("OpenSSL did not verify the references time-stamp (XAdES-X): %s", out)
	}

	// OpenSSL reads the embedded OCSP response and checks its signature and the certificate's status.
	write(t, dir, "ocsp.der", string(b64decode(t, between(t, sigXML, "OCSPValues", "EncapsulatedOCSPValue"))))
	out = run("ocsp", "-respin", "ocsp.der", "-issuer", "ca.crt", "-cert", "signer.crt", "-CAfile", "ca.crt", "-no_nonce")
	if !strings.Contains(out, "Response verify OK") || !strings.Contains(out, "signer.crt: good") {
		t.Fatalf("OpenSSL did not accept the embedded OCSP response: %s", out)
	}

	if !need("docker") || exec.Command("docker", "info").Run() != nil {
		t.Skip("docker is not running, for the xmlsec1 half")
	}
	if out, _ := exec.Command("docker", "info", "--format", "{{.OSType}}").Output(); strings.TrimSpace(string(out)) != "linux" {
		t.Skip("docker runs " + strings.TrimSpace(string(out)) + " containers here, and xmlsec1 comes in a Linux one")
	}
	// xmlsec1 verifies the signature in place of the signatureText that carries it. The participant is subtracted from the
	// digest, so the document it digests is the same.
	plain := replaceSignatureText(t, res.Document, sigXML)
	_ = os.WriteFile(filepath.Join(dir, "plain.xml"), plain, 0o644)
	_ = os.Chmod(dir, 0o755)
	_ = os.Chmod(filepath.Join(dir, "ca.crt"), 0o644)
	docker := func(file string) (string, error) {
		cmd := exec.Command("docker", "run", "--rm", "-v", dir+":/w", "alpine:3", "sh", "-c",
			"apk add -q xmlsec >/dev/null 2>&1 && xmlsec1 --verify --trusted-pem /w/ca.crt "+
				"--id-attr:Id http://uri.etsi.org/01903/v1.3.2#:SignedProperties /w/"+file)
		o, err := cmd.CombinedOutput()
		return string(o), err
	}
	o, err := docker("plain.xml")
	if err != nil || !strings.Contains(o, "Verification status: OK") {
		t.Fatalf("xmlsec1 did not verify the signature: %v\n%s", err, o)
	}
	t.Logf("xmlsec1: %s", strings.TrimSpace(o))
	// And xmlsec1 refuses it once the clinical content is altered - so its OK above was about this document.
	_ = os.WriteFile(filepath.Join(dir, "tampered.xml"), bytes.Replace(plain, []byte("1 g IV"), []byte("2 g IV"), 1), 0o644)
	if o, err := docker("tampered.xml"); err == nil || !strings.Contains(o, "Verification status: FAILED") {
		t.Fatalf("xmlsec1 accepted an altered document:\n%s", o)
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func write(t *testing.T, dir, name, content string) {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pemCert(t *testing.T, p []byte) *x509.Certificate {
	pair, err := tls.X509KeyPair(p, nil)
	if err == nil && len(pair.Certificate) > 0 {
		c, _ := x509.ParseCertificate(pair.Certificate[0])
		return c
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p)
	s := strings.SplitN(string(p), "-----BEGIN CERTIFICATE-----", 2)[1]
	s = strings.SplitN(s, "-----END CERTIFICATE-----", 2)[0]
	der, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decodedSignature(t *testing.T, doc []byte) []byte {
	texts, err := findSignatureTexts(doc)
	if err != nil || len(texts) == 0 {
		t.Fatal("no signatureText", err)
	}
	content := b64decode(t, texts[0].content)
	sig, err := xmldsig.Element(content, "Signature", nsDSig)
	if err != nil || sig == nil {
		t.Fatal("no signature", err)
	}
	return sig
}

func replaceSignatureText(t *testing.T, doc, sig []byte) []byte {
	s := string(doc)
	i := strings.Index(s, "<sdtc:signatureText")
	j := strings.Index(s, "</sdtc:signatureText>")
	if i < 0 || j < 0 {
		t.Fatal("no signatureText to replace")
	}
	return []byte(s[:i] + string(sig) + s[j+len("</sdtc:signatureText>"):])
}

// between returns the text of the first inner element inside the first outer element.
func between(t *testing.T, doc []byte, outer, inner string) string {
	s := string(doc)
	i := strings.Index(s, ":"+outer)
	if i < 0 {
		t.Fatalf("no %s", outer)
	}
	s = s[i:]
	i = strings.Index(s, ":"+inner+">")
	if i < 0 {
		t.Fatalf("no %s in %s", inner, outer)
	}
	s = s[i+len(inner)+2:]
	return s[:strings.Index(s, "<")]
}

func b64decode(t *testing.T, s string) []byte {
	b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
