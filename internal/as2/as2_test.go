package as2

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
)

type party struct {
	name string
	kp   cms.KeyPair
	pool *x509.CertPool
	pem  []byte // key then certificate, as pyas2lib reads them
	cert []byte // certificate PEM
}

// newParty makes a self-signed certificate, which is how most AS2 partners exchange keys.
func newParty(t *testing.T, name string) party {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name, Organization: []string{name}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return party{name: name, kp: cms.KeyPair{Certificate: c, Key: k}, pool: pool, pem: append(keyPEM, certPEM...), cert: certPEM}
}

func TestSendReceiveAndReceipt(t *testing.T) {
	us, partner := newParty(t, "PERFUSE"), newParty(t, "PAYER")
	payload := []byte("ISA*00*          *00*          *ZZ*PERFUSE        *ZZ*PAYER          *261017*1200*^*00501*000000001*0*P*:~")

	for _, c := range []struct {
		name          string
		sign, encrypt bool
	}{{"signed and encrypted", true, true}, {"signed", true, false}, {"encrypted", false, true}, {"neither", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			o := Outgoing{From: "PERFUSE", To: "PAYER", Payload: payload, ContentType: "application/edi-x12", FileName: "claims.x12",
				MDN: MDNSync, SignedMDN: true}
			if c.sign {
				o.Signer = &us.kp
			}
			if c.encrypt {
				o.EncryptTo = partner.kp.Certificate
			}
			built, err := Build(o)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Receive(built.Header, built.Body, ReceiveOptions{Keys: []cms.KeyPair{partner.kp}, Partner: us.pool})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Payload, payload) || got.FileName != "claims.x12" || got.Signed != c.sign || got.Encrypted != c.encrypt ||
				got.From != "PERFUSE" || got.To != "PAYER" || got.MDN != MDNSync || !got.SignedMDN {
				t.Fatalf("%+v", got)
			}
			if got.MIC != built.MIC {
				t.Errorf("MIC %q, sent %q", got.MIC, built.MIC)
			}
			h, b, err := BuildReceipt(Receipt{From: got.To, To: got.From, OriginalMessageID: got.MessageID, MIC: got.MIC, Signer: &partner.kp})
			if err != nil {
				t.Fatal(err)
			}
			rr, err := ReadReceipt(h, b, partner.pool, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := rr.Check(built); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestReceiveRefuses(t *testing.T) {
	us, partner, stranger := newParty(t, "PERFUSE"), newParty(t, "PAYER"), newParty(t, "STRANGER")
	o := Outgoing{From: "PERFUSE", To: "PAYER", Payload: []byte("x"), Signer: &us.kp, EncryptTo: partner.kp.Certificate}
	built, _ := Build(o)
	kind := func(err error) string {
		var f *Failure
		if errors.As(err, &f) {
			return f.Kind
		}
		return "(" + err.Error() + ")"
	}
	if _, err := Receive(built.Header, built.Body, ReceiveOptions{Keys: []cms.KeyPair{stranger.kp}, Partner: us.pool}); kind(err) != "decryption-failed" {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := Receive(built.Header, built.Body, ReceiveOptions{Keys: []cms.KeyPair{partner.kp}, Partner: stranger.pool}); kind(err) != "authentication-failed" {
		t.Errorf("wrong partner: %v", err)
	}
	plain, _ := Build(Outgoing{From: "PERFUSE", To: "PAYER", Payload: []byte("x")})
	if _, err := Receive(plain.Header, plain.Body, ReceiveOptions{RequireSigned: true}); kind(err) != "insufficient-message-security" {
		t.Errorf("unsigned: %v", err)
	}
	if _, err := Receive(plain.Header, plain.Body, ReceiveOptions{RequireEncrypted: true}); kind(err) != "insufficient-message-security" {
		t.Errorf("unencrypted: %v", err)
	}
	// A failure receipt names the failure, and the sender reads it as one.
	h, b, _ := BuildReceipt(Receipt{From: "PAYER", To: "PERFUSE", OriginalMessageID: built.MessageID, Failure: fail("decryption-failed", "no key")})
	rr, err := ReadReceipt(h, b, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rr.Check(built); err == nil || !strings.Contains(err.Error(), "decryption-failed") {
		t.Errorf("failure receipt: %v", err)
	}
	// A receipt with the wrong MIC means what arrived is not what was sent.
	h, b, _ = BuildReceipt(Receipt{From: "PAYER", To: "PERFUSE", OriginalMessageID: built.MessageID, MIC: "AAAA, sha-256"})
	rr, _ = ReadReceipt(h, b, nil, false)
	if err := rr.Check(built); err == nil || !strings.Contains(err.Error(), "MIC") {
		t.Errorf("wrong MIC: %v", err)
	}
}

const pyHarness = `
import sys, json, base64, urllib.request
from pyas2lib import Organization, Partner, Message, Mdn
d = '/work/'
rd = lambda n: open(d + n, 'rb').read()
mode, enc_alg = sys.argv[1], sys.argv[2]
org = Organization(as2_name='PAYER', sign_key=rd('payer.pem'), sign_key_pass=None, decrypt_key=rd('payer.pem'), decrypt_key_pass=None)
partner = Partner(as2_name='PERFUSE', verify_cert=rd('perfuse.crt'), encrypt_cert=rd('perfuse.crt'), validate_certs=False,
                  sign=True, encrypt=True, enc_alg=enc_alg, digest_alg='sha256', mdn_mode='SYNC', mdn_digest_alg='sha256')
out = {}
if mode == 'send':
    # pyas2lib sends to Perfuse over HTTP and reads Perfuse's synchronous receipt.
    msg = Message(org, partner)
    msg.build(b'UNA:+.? \'UNB+UNOC:3+PAYER+PERFUSE+261017:1200+1\'UNZ+0+1\'', filename='orders.edi', content_type='application/edifact')
    req = urllib.request.Request(sys.argv[3], data=msg.content, method='POST')
    for k, v in msg.headers.items():
        req.add_header(k, v)
    with urllib.request.urlopen(req) as r:
        raw = b''.join(k.encode() + b': ' + v.encode() + b'\r\n' for k, v in r.getheaders()) + b'\r\n' + r.read()
    status, detail = Mdn().parse(raw, lambda mid, rcpt: msg)
    out = {'status': status, 'detail': detail, 'mic': msg.mic.decode() if isinstance(msg.mic, bytes) else msg.mic}
else:
    # pyas2lib receives what Perfuse wrote, and answers with a signed receipt.
    req = json.load(open(d + 'request.json'))
    raw = b''.join(k.encode() + b': ' + v.encode() + b'\r\n' for k, v in req['headers'].items()) + b'\r\n' + base64.b64decode(req['body'])
    msg = Message()
    status, exc, mdn = msg.parse(raw, lambda as2_id: org, lambda as2_id: partner)
    out = {'status': status, 'error': str(exc[0]) if exc and exc[0] else '', 'payload': base64.b64encode(msg.content or b'').decode()}
    if mdn is None:
        mdn = Mdn(mdn_mode='SYNC')
        mdn.build(message=msg, status=status)
    out['mdn_headers'] = dict(mdn.headers)
    out['mdn_body'] = base64.b64encode(mdn.content).decode()
print(json.dumps(out))
`

// Against pyas2lib, an independent AS2 implementation, in both directions and with both its default cipher (Triple DES)
// and AES-256. Set PERFUSE_TEST_PYAS2LIB=pf-pyas2lib (a Docker image with pyas2lib 1.4.3 installed).
func TestInteroperatesWithPyAS2Lib(t *testing.T) {
	image := os.Getenv("PERFUSE_TEST_PYAS2LIB")
	if image == "" {
		t.Skip("set PERFUSE_TEST_PYAS2LIB to a Docker image with pyas2lib")
	}
	us, payer := newParty(t, "PERFUSE"), newParty(t, "PAYER")
	home, _ := os.UserHomeDir()
	work, err := os.MkdirTemp(filepath.Join(home, ".pftest"), "as2run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	for name, b := range map[string][]byte{"payer.pem": payer.pem, "perfuse.crt": us.cert, "h.py": []byte(pyHarness)} {
		if err := os.WriteFile(filepath.Join(work, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	py := func(args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"run", "--rm", "--add-host=host.docker.internal:host-gateway", "-v", work + ":/work", image,
			"python", "/work/h.py"}, args...)...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		if err := cmd.Run(); err != nil {
			t.Fatalf("pyas2lib %v: %v\n%s", args, err, e.String())
		}
		var out map[string]any
		if err := json.Unmarshal(o.Bytes(), &out); err != nil {
			t.Fatalf("pyas2lib said %q: %v\n%s", o.String(), err, e.String())
		}
		return out
	}

	// pyas2lib to Perfuse, with a synchronous signed receipt.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	var received *Received
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got, err := Receive(r.Header, body, ReceiveOptions{Keys: []cms.KeyPair{us.kp}, Partner: payer.pool, RequireSigned: true, RequireEncrypted: true})
		rc := Receipt{From: "PERFUSE", To: "PAYER", OriginalMessageID: r.Header.Get("Message-ID"), Signer: &us.kp}
		var f *Failure
		if errors.As(err, &f) {
			rc.Failure = f
			t.Errorf("Perfuse refused pyas2lib's message: %v", err)
		} else {
			received, rc.MIC = got, got.MIC
		}
		h, b, _ := BuildReceipt(rc)
		for k, v := range h {
			w.Header()[k] = v
		}
		_, _ = w.Write(b)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	for _, alg := range []string{"tripledes_192_cbc", "aes_256_cbc"} {
		out := py("send", alg, "http://host.docker.internal:"+strconv.Itoa(port)+"/as2")
		if out["status"] != "processed" {
			t.Errorf("%s: pyas2lib read Perfuse's receipt as %v (%v)", alg, out["status"], out["detail"])
		}
		if received == nil || !bytes.HasPrefix(received.Payload, []byte("UNA:+.? 'UNB")) || received.FileName != "orders.edi" {
			t.Errorf("%s: Perfuse received %+v", alg, received)
		}
	}

	// Perfuse to pyas2lib: pyas2lib opens it and answers; Perfuse checks the receipt and its MIC.
	built, err := Build(Outgoing{From: "PERFUSE", To: "PAYER", Payload: []byte("ISA*00*~GS*HC~ST*837*0001~"), ContentType: "application/edi-x12",
		FileName: "claims.x12", Signer: &us.kp, EncryptTo: payer.kp.Certificate, MDN: MDNSync, SignedMDN: true})
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{}
	for k := range built.Header {
		hdr[k] = built.Header.Get(k)
	}
	req, _ := json.Marshal(map[string]any{"headers": hdr, "body": built.Body})
	if err := os.WriteFile(filepath.Join(work, "request.json"), req, 0o600); err != nil {
		t.Fatal(err)
	}
	out := py("receive", "aes_256_cbc")
	if out["status"] != "processed" {
		t.Fatalf("pyas2lib refused Perfuse's message: %v %v", out["status"], out["error"])
	}
	if p, _ := out["payload"].(string); p != "SVNBKjAwKn5HUypIQ35TVCo4MzcqMDAwMX4=" {
		t.Errorf("pyas2lib read %q", p)
	}
	mh := http.Header{}
	for k, v := range out["mdn_headers"].(map[string]any) {
		mh.Set(k, v.(string))
	}
	body, _ := out["mdn_body"].(string)
	mb := decode64(t, body)
	rr, err := ReadReceipt(mh, mb, payer.pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := rr.Check(built); err != nil {
		t.Error(err)
	}
}

func decode64(t *testing.T, s string) []byte {
	t.Helper()
	var b []byte
	if err := json.Unmarshal([]byte(`"`+s+`"`), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
