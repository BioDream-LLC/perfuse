package dsdr

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEUDSSValidatesTheSignature sends a signed document to the EU Digital Signature Service (DSS), the European Commission's
// reference XAdES validator, and reads its simple report. Skipped unless DSS_URL names a running DSS demo webapp:
//
//	docker build -t local/dss-demo https://github.com/esig/dss-demonstrations.git && docker run -p 8085:8080 local/dss-demo
//	DSS_URL=http://localhost:8085 go test ./internal/dsdr -run EUDSS -v
//
// The test CA is not on any EU trusted list, so DSS cannot reach TOTAL_PASSED; what it checks is everything else: that it
// recognises the signature as XAdES, finds the signed reference, and that the signature value and the certificate it names
// verify. A broken signature or an unreadable structure is TOTAL_FAILED, which fails this test.
func TestEUDSSValidatesTheSignature(t *testing.T) {
	base := os.Getenv("DSS_URL")
	if base == "" {
		t.Skip("DSS_URL is not set")
	}
	for _, ec := range []bool{false, true} {
		p := newTestPKI(t, ec)
		res, err := Sign(discharge(t), p.options())
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("DSS_SAVE") != "" {
			_ = os.WriteFile(os.Getenv("DSS_SAVE")+map[bool]string{false: "-rsa.xml", true: "-ec.xml"}[ec], res.Document, 0o600)
		}
		// DSDR carries the XAdES signature base64-encoded in sdtc:signatureText, where no generic validator looks. The signature
		// is put back in its place, as xmlsec1 is given it in the interop test: its XPath Filter 2.0 transform subtracts the
		// signer's participant, so the enveloped form verifies exactly as signed.
		plain := replaceSignatureText(t, res.Document, decodedSignature(t, res.Document))
		if os.Getenv("DSS_SAVE") != "" {
			_ = os.WriteFile(os.Getenv("DSS_SAVE")+map[bool]string{false: "-rsa-plain.xml", true: "-ec-plain.xml"}[ec], plain, 0o600)
		}
		body, _ := json.Marshal(map[string]any{
			"signedDocument": map[string]any{"bytes": base64.StdEncoding.EncodeToString(plain), "name": "discharge-signed.xml"},
		})
		c := &http.Client{Timeout: 2 * time.Minute}
		resp, err := c.Post(strings.TrimRight(base, "/")+"/services/rest/validation/validateSignature", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("DSS answered %d: %s", resp.StatusCode, raw)
		}
		var report struct {
			SimpleReport struct {
				SignatureOrTimestampOrEvidenceRecord []struct {
					Signature *struct {
						Indication      string          `json:"Indication"`
						SubIndication   string          `json:"SubIndication"`
						SignatureFormat string          `json:"SignatureFormat"`
						Details         json.RawMessage `json:"AdESValidationDetails"`
						Warnings        []string        `json:"-"`
					} `json:"Signature"`
				} `json:"signatureOrTimestampOrEvidenceRecord"`
			} `json:"SimpleReport"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatalf("%v: %.500s", err, raw)
		}
		if os.Getenv("DSS_SAVE") != "" {
			_ = os.WriteFile(os.Getenv("DSS_SAVE")+map[bool]string{false: "-rsa.json", true: "-ec.json"}[ec], raw, 0o600)
		}
		var found bool
		for _, s := range report.SimpleReport.SignatureOrTimestampOrEvidenceRecord {
			if s.Signature == nil {
				continue
			}
			found = true
			t.Logf("ec=%v: %s %s %s", ec, s.Signature.SignatureFormat, s.Signature.Indication, s.Signature.SubIndication)
			// XAdES-XL: DSS recognises every layer, the time-stamps (signed by the TSA) and the certificate and revocation values
			// for the signer and the TSA alike. The indication stays INDETERMINATE only because the test CA is on no trusted list.
			if s.Signature.Indication == "TOTAL_FAILED" || s.Signature.SignatureFormat != "XAdES-XL" ||
				(s.Signature.Indication == "INDETERMINATE" && s.Signature.SubIndication != "NO_CERTIFICATE_CHAIN_FOUND") {
				t.Errorf("ec=%v: DSS rejects the signature: %s %s %s", ec, s.Signature.SignatureFormat, s.Signature.Indication, s.Signature.SubIndication)
			}
		}
		if !found {
			t.Errorf("ec=%v: DSS found no signature: %.800s", ec, raw)
		}
	}
}
