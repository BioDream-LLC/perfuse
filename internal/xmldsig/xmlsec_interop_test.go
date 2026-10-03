package xmldsig

import (
	"crypto"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Signatures made here, verified by xmlsec1 (libxmlsec, in Docker): the enveloped whole-document form and the form signing
// one element by ID, with RSA and with ECDSA. It caught two faults that every Perfuse-to-Perfuse test passed: the line
// break after the XML declaration was canonicalised, and ECDSA signature values were DER rather than XML-DSig's r and s.
// Skipped without Docker.
func TestXmlsecVerifiesWhatThisSigns(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not running")
	}
	home, _ := os.UserHomeDir()
	dir, err := os.MkdirTemp(home, ".perfuse-xmlsec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_ = os.Chmod(dir, 0o755)

	var script strings.Builder
	script.WriteString("apk add -q xmlsec >/dev/null 2>&1 || exit 9; cd /w; ")
	for _, ec := range []bool{false, true} {
		cert, key := signer(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), ec)
		alg := map[bool]string{false: "rsa", true: "ec"}[ec]
		_ = os.WriteFile(filepath.Join(dir, alg+".pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644)
		for _, ref := range []string{"", "d1"} {
			signed := sign(t, doc, SignOptions{Key: key.(crypto.Signer), Certificate: cert, ReferenceID: ref, Role: "Attending physician"})
			name := alg + "-" + map[string]string{"": "whole", "d1": "element"}[ref] + ".xml"
			_ = os.WriteFile(filepath.Join(dir, name), signed, 0o644)
			script.WriteString("echo " + name + "; xmlsec1 --verify --trusted-pem " + alg + ".pem --id-attr:ID urn:hl7-org:v3:ClinicalDocument " +
				"--id-attr:Id http://uri.etsi.org/01903/v1.3.2#:SignedProperties " + name + " 2>&1; ")
		}
	}
	out, err := exec.Command("docker", "run", "--rm", "-v", dir+":/w", "alpine:3", "sh", "-c", script.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := strings.Count(string(out), "Verification status: OK"); n != 4 {
		t.Fatalf("xmlsec1 verified %d of 4:\n%s", n, out)
	}
}
