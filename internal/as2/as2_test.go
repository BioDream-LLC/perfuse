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
		name                    string
		sign, encrypt, compress bool
	}{{"signed and encrypted", true, true, false}, {"signed", true, false, false}, {"encrypted", false, true, false},
		{"neither", false, false, false}, {"compressed, signed and encrypted", true, true, true}, {"compressed and signed", true, false, true},
		{"compressed and encrypted", false, true, true}, {"compressed only", false, false, true}} {
		t.Run(c.name, func(t *testing.T) {
			o := Outgoing{From: "PERFUSE", To: "PAYER", Payload: payload, ContentType: "application/edi-x12", FileName: "claims.x12",
				MDN: MDNSync, SignedMDN: true, Compress: c.compress}
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
			if !bytes.Equal(got.Payload, payload) || got.FileName != "claims.x12" || got.Signed != c.sign || got.Encrypted != c.encrypt || got.Compressed != c.compress ||
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

// RFC 5402 section 3.3: a signed message compressed whole. The MIC is over what was signed, the uncompressed entity; and the
// compressed layer arrives as streaming BER with indefinite lengths, as Java gateways write it.
func TestReceiveASignedMessageCompressedWholeInBER(t *testing.T) {
	us, partner := newParty(t, "PERFUSE"), newParty(t, "PAYER")
	payload := []byte("UNA:+.? 'UNB+UNOC:3+PERFUSE+PAYER+261017:1200+1'UNZ+0+1'")
	built, err := Build(Outgoing{From: "PERFUSE", To: "PAYER", Payload: payload, ContentType: "application/edifact", Signer: &us.kp,
		MDN: MDNSync})
	if err != nil {
		t.Fatal(err)
	}
	signed := []byte("Content-Type: " + built.Header.Get("Content-Type") + "\r\n\r\n")
	signed = append(signed, built.Body...)
	z, err := cms.Compress(signed)
	if err != nil {
		t.Fatal(err)
	}
	h := built.Header.Clone()
	h.Set("Content-Type", `application/pkcs7-mime; smime-type=compressed-data; name="smime.p7z"`)
	h.Set("Content-Transfer-Encoding", "binary")
	got, err := Receive(h, indefinite(t, z), ReceiveOptions{Partner: us.pool, RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Compressed || !got.Signed || !bytes.Equal(got.Payload, payload) || got.MIC != built.MIC {
		t.Fatalf("compressed %v signed %v payload %q MIC %q (sent %q)", got.Compressed, got.Signed, got.Payload, got.MIC, built.MIC)
	}
	_ = partner
}

// indefinite re-encodes the outer three constructed layers of DER with indefinite lengths, as a streaming encoder writes them.
func indefinite(t *testing.T, der []byte) []byte {
	t.Helper()
	var wrap func(b []byte, depth int) []byte
	wrap = func(b []byte, depth int) []byte {
		if depth == 0 || b[0]&0x20 == 0 {
			return b
		}
		n, l := int(b[1]), 1
		if b[1]&0x80 != 0 {
			k := int(b[1] & 0x7f)
			n = 0
			for i := 0; i < k; i++ {
				n = n<<8 | int(b[2+i])
			}
			l = 1 + k
		}
		content := b[1+l : 1+l+n]
		var inner []byte
		for len(content) > 0 {
			cn, cl := int(content[1]), 1
			if content[1]&0x80 != 0 {
				k := int(content[1] & 0x7f)
				cn = 0
				for i := 0; i < k; i++ {
					cn = cn<<8 | int(content[2+i])
				}
				cl = 1 + k
			}
			inner = append(inner, wrap(content[:1+cl+cn], depth-1)...)
			content = content[1+cl+cn:]
		}
		return append(append([]byte{b[0], 0x80}, inner...), 0, 0)
	}
	out := wrap(der, 3)
	if bytes.Equal(out, der) {
		t.Fatal("nothing was re-encoded")
	}
	return out
}

// A compressed message that would expand past the limit is refused with decompression-failed, not read into memory.
func TestReceiveRefusesACompressionBomb(t *testing.T) {
	us := newParty(t, "PERFUSE")
	big := bytes.Repeat([]byte("A"), 4<<20)
	built, err := Build(Outgoing{From: "PERFUSE", To: "PAYER", Payload: big, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Body) > 64<<10 {
		t.Fatalf("4 MB of one letter compressed to %d bytes", len(built.Body))
	}
	_, err = Receive(built.Header, built.Body, ReceiveOptions{Partner: us.pool, MaxExpanded: 1 << 20})
	var f *Failure
	if !errors.As(err, &f) || f.Kind != "decompression-failed" {
		t.Fatalf("got %v", err)
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
compress = sys.argv[-1] == 'compress'
org = Organization(as2_name='PAYER', sign_key=rd('payer.pem'), sign_key_pass=None, decrypt_key=rd('payer.pem'), decrypt_key_pass=None)
partner = Partner(as2_name='PERFUSE', verify_cert=rd('perfuse.crt'), encrypt_cert=rd('perfuse.crt'), validate_certs=False,
                  sign=True, encrypt=True, enc_alg=enc_alg, digest_alg='sha256', mdn_mode='SYNC', mdn_digest_alg='sha256',
                  compress=compress)
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
    out = {'status': status, 'error': str(exc[0]) if exc and exc[0] else '', 'payload': base64.b64encode(msg.content or b'').decode(),
           'compressed': bool(msg.compressed)}
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
	for _, alg := range []string{"tripledes_192_cbc", "aes_256_cbc", "aes_256_cbc compress"} {
		received = nil
		args := append([]string{"send"}, strings.Fields(alg)...)
		args = append(args[:2], "http://host.docker.internal:"+strconv.Itoa(port)+"/as2")
		if strings.HasSuffix(alg, "compress") {
			args = append(args, "compress")
		}
		out := py(args...)
		if out["status"] != "processed" {
			t.Errorf("%s: pyas2lib read Perfuse's receipt as %v (%v)", alg, out["status"], out["detail"])
		}
		if received == nil || !bytes.HasPrefix(received.Payload, []byte("UNA:+.? 'UNB")) || received.FileName != "orders.edi" ||
			received.Compressed != strings.HasSuffix(alg, "compress") {
			t.Errorf("%s: Perfuse received %+v", alg, received)
		}
	}

	// Perfuse to pyas2lib: pyas2lib opens it and answers; Perfuse checks the receipt and its MIC.
	for _, compress := range []bool{false, true} {
		pyReceives(t, py, work, us, payer, compress)
	}
}

// pyReceives sends pyas2lib one message from Perfuse and checks its receipt.
func pyReceives(t *testing.T, py func(...string) map[string]any, work string, us, payer party, compress bool) {
	t.Helper()
	built, err := Build(Outgoing{From: "PERFUSE", To: "PAYER", Payload: []byte("ISA*00*~GS*HC~ST*837*0001~"), ContentType: "application/edi-x12",
		FileName: "claims.x12", Signer: &us.kp, EncryptTo: payer.kp.Certificate, MDN: MDNSync, SignedMDN: true, Compress: compress})
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
	if c, _ := out["compressed"].(bool); c != compress {
		t.Errorf("pyas2lib saw compressed=%v, sent compressed=%v", c, compress)
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

// Whatever arrives, Receive answers with a message or a failure and never panics.
func FuzzReceive(f *testing.F) {
	z, _ := cms.Compress([]byte("Content-Type: text/plain\r\n\r\nhello"))
	f.Add(`application/pkcs7-mime; smime-type=compressed-data`, z)
	f.Add(`application/pkcs7-mime`, []byte{0x30, 0x80, 0x06, 0x00, 0x00, 0x00})
	f.Add(`multipart/signed; boundary=b; micalg=sha-256`, []byte("--b\r\nContent-Type: text/plain\r\n\r\nx\r\n--b--"))
	f.Fuzz(func(t *testing.T, ct string, body []byte) {
		h := http.Header{}
		h.Set("Content-Type", ct)
		h.Set("AS2-From", "A")
		h.Set("AS2-To", "B")
		_, _ = Receive(h, body, ReceiveOptions{MaxExpanded: 1 << 20})
	})
}
