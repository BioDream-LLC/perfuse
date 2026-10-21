package umlink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	request278  = "ISA*00*          *00*          *ZZ*PERFUSE        *ZZ*PAYER          *261021*1200*^*00501*000000001*0*T*:~ST*278*0001~SE*2*0001~"
	response278 = "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PERFUSE        *261021*1200*^*00501*000000009*0*T*:~ST*278*0009~SE*2*0009~"
)

func fixed(l *Config) {
	l.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	n := 0
	l.NewID = func() string { n++; return fmt.Sprintf("f81d4fae-7dec-11d0-a765-00a0c91e6b%02d", n) }
}

// coreRequest is a COREEnvelopeRealTimeRequest as the rule's schema has it, read by a server written from the rule.
type coreRequest struct {
	XMLName         xml.Name
	PayloadType     string `xml:"PayloadType"`
	ProcessingMode  string `xml:"ProcessingMode"`
	PayloadID       string `xml:"PayloadID"`
	TimeStamp       string `xml:"TimeStamp"`
	SenderID        string `xml:"SenderID"`
	ReceiverID      string `xml:"ReceiverID"`
	CORERuleVersion string `xml:"CORERuleVersion"`
	Payload         struct {
		Text    string `xml:",chardata"`
		Include struct {
			Href string `xml:"href,attr"`
		} `xml:"http://www.w3.org/2004/08/xop/include Include"`
	} `xml:"Payload"`
}

type soapEnvelope struct {
	XMLName xml.Name `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Header  struct {
		Security struct {
			MustUnderstand string `xml:"http://www.w3.org/2003/05/soap-envelope mustUnderstand,attr"`
			Token          struct {
				Username string `xml:"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd Username"`
				Password struct {
					Type  string `xml:"Type,attr"`
					Value string `xml:",chardata"`
				} `xml:"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd Password"`
			} `xml:"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd UsernameToken"`
		} `xml:"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd Security"`
	} `xml:"http://www.w3.org/2003/05/soap-envelope Header"`
	Body struct {
		Request coreRequest `xml:",any"`
	} `xml:"http://www.w3.org/2003/05/soap-envelope Body"`
}

func soapAnswer(ns, payload, code string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` +
		`<c:COREEnvelopeRealTimeResponse xmlns:c="` + ns + `"><PayloadType>X12_278_Response_005010X217E1_2</PayloadType>` +
		`<ProcessingMode>RealTime</ProcessingMode><PayloadID>a81d44ae-7dec-11d0-a765-00a0c91e6ba0</PayloadID>` +
		`<TimeStamp>2026-10-21T12:00:01Z</TimeStamp><SenderID>PayerB</SenderID><ReceiverID>HospitalA</ReceiverID>` +
		`<CORERuleVersion>2.2.0</CORERuleVersion><Payload>` + payload + `</Payload><ErrorCode>` + code + `</ErrorCode>` +
		`<ErrorMessage></ErrorMessage></c:COREEnvelopeRealTimeResponse></s:Body></s:Envelope>`
}

// A vC2.2.0 SOAP request is the rule's: SOAP 1.2, the CORERule2.2.0 namespace on the envelope element and unqualified fields in
// the schema's order, the WS-Security UsernameToken with a PasswordText password, the payload as text; the answer's payload
// comes back whether the server sends it as text or as base64.
func TestACORESOAP220RequestIsTheRules(t *testing.T) {
	var got soapEnvelope
	var raw, action string
	answerBase64 := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "application/soap+xml" {
			http.Error(w, "not SOAP 1.2", http.StatusUnsupportedMediaType)
			return
		}
		action = params["action"]
		if err := xml.Unmarshal(b, &got); err != nil {
			t.Errorf("not a SOAP 1.2 envelope: %v", err)
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=UTF-8")
		payload := xmlText(response278)
		if answerBase64 {
			payload = base64.StdEncoding.EncodeToString([]byte(response278))
		}
		_, _ = io.WriteString(w, soapAnswer("http://www.caqh.org/SOAP/WSDL/CORERule2.2.0.xsd", payload, "Success"))
	}))
	defer srv.Close()
	cfg := Config{Transport: TransportCORESOAP, URL: srv.URL, SenderID: "HospitalA", ReceiverID: "PayerB", Username: "hosp",
		Password: "p<&>w"}
	fixed(&cfg)
	l, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, b64 := range []bool{false, true} {
		answerBase64 = b64
		out, err := l.Send(context.Background(), []byte(request278))
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != response278 {
			t.Fatalf("answer %q", out)
		}
	}
	req := got.Body.Request
	if req.XMLName.Space != "http://www.caqh.org/SOAP/WSDL/CORERule2.2.0.xsd" || req.XMLName.Local != "COREEnvelopeRealTimeRequest" {
		t.Errorf("element %v", req.XMLName)
	}
	want := coreRequest{PayloadType: "X12_278_Request_005010X217E1_2", ProcessingMode: "RealTime", PayloadID: req.PayloadID,
		TimeStamp: "2026-10-21T12:00:00Z", SenderID: "HospitalA", ReceiverID: "PayerB", CORERuleVersion: "2.2.0"}
	req.XMLName, req.Payload.Text = xml.Name{}, strings.TrimSpace(req.Payload.Text)
	if p := req.Payload.Text; p != request278 {
		t.Errorf("payload %q", p)
	}
	req.Payload = want.Payload
	if req != want {
		t.Errorf("metadata\n got %+v\nwant %+v", req, want)
	}
	if action != "RealTimeTransaction" {
		t.Errorf("action %q", action)
	}
	tok := got.Header.Security.Token
	if tok.Username != "hosp" || tok.Password.Value != "p<&>w" || !strings.HasSuffix(tok.Password.Type, "#PasswordText") ||
		got.Header.Security.MustUnderstand != "true" {
		t.Errorf("WS-Security %+v", got.Header.Security)
	}
	// The fields are unqualified, as the schema (no elementFormDefault) makes them.
	if !strings.Contains(raw, "<PayloadType>") {
		t.Errorf("fields are qualified:\n%s", raw)
	}
	order := []string{"PayloadType", "ProcessingMode", "PayloadID", "TimeStamp", "SenderID", "ReceiverID", "CORERuleVersion", "Payload"}
	last := -1
	for _, f := range order {
		i := strings.Index(raw, "<"+f+">")
		if i < last {
			t.Errorf("%s out of the schema's order", f)
		}
		last = i
	}
}

// A vC4.0.0 SOAP request carries its payload as an MTOM attachment, and reads an MTOM answer.
func TestACORESOAP400RequestCarriesItsPayloadByMTOM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "multipart/related" || params["type"] != "application/xop+xml" || params["start-info"] != "application/soap+xml" {
			http.Error(w, "not MTOM: "+r.Header.Get("Content-Type"), http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		root, _ := mr.NextPart()
		rb, _ := io.ReadAll(root)
		var env soapEnvelope
		if err := xml.Unmarshal(rb, &env); err != nil {
			t.Errorf("%v", err)
		}
		att, _ := mr.NextPart()
		ab, _ := io.ReadAll(att)
		if env.Body.Request.XMLName.Space != "http://www.caqh.org/SOAP/WSDL/CORERuleC4.0.0.xsd" ||
			env.Body.Request.CORERuleVersion != "C4.0.0" ||
			"<"+strings.TrimPrefix(env.Body.Request.Payload.Include.Href, "cid:")+">" != att.Header.Get("Content-ID") ||
			string(ab) != request278 || r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("request %+v, attachment %q (%s), auth %q", env.Body.Request, ab, att.Header.Get("Content-ID"),
				r.Header.Get("Authorization"))
		}
		var out strings.Builder
		mw := multipart.NewWriter(&out)
		w.Header().Set("Content-Type", `multipart/related; boundary="`+mw.Boundary()+`"; type="application/xop+xml"; start="<r1>"; start-info="application/soap+xml"`)
		h := map[string][]string{"Content-Type": {`application/xop+xml; type="application/soap+xml"`}, "Content-Id": {"<r1>"}}
		p, _ := mw.CreatePart(h)
		_, _ = io.WriteString(p, soapAnswer("http://www.caqh.org/SOAP/WSDL/CORERuleC4.0.0.xsd",
			`<xop:Include xmlns:xop="http://www.w3.org/2004/08/xop/include" href="cid:p1"/>`, "Success"))
		p, _ = mw.CreatePart(map[string][]string{"Content-Type": {"application/octet-stream"}, "Content-Id": {"<p1>"}})
		_, _ = io.WriteString(p, response278)
		_ = mw.Close()
		_, _ = io.WriteString(w, out.String())
	}))
	defer srv.Close()
	tokens := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if r.PostForm.Get("grant_type") != "client_credentials" || id != "perfuse" || secret != "s3cret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		tokens++
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600,"token_type":"Bearer"}`, tokens)
	}))
	defer ts.Close()
	cfg := Config{Transport: TransportCORESOAP, COREVersion: CORE400, URL: srv.URL, SenderID: "HospitalA", ReceiverID: "PayerB",
		OAuth: OAuth{TokenURL: ts.URL, ClientID: "perfuse", ClientSecret: "s3cret"}}
	fixed(&cfg)
	l, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := l.Send(context.Background(), []byte(request278))
		if err != nil || string(out) != response278 {
			t.Fatalf("%q %v", out, err)
		}
	}
	if tokens != 1 {
		t.Errorf("%d tokens fetched for two requests; the first should have been kept", tokens)
	}
}

// What the CORE envelope refuses is an error that names it: an ErrorCode other than Success, or a SOAP fault.
func TestCOREEnvelopeErrorsAreSaid(t *testing.T) {
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		if strings.Contains(answer, "Fault") {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	cfg := Config{Transport: TransportCORESOAP, URL: srv.URL, SenderID: "A", ReceiverID: "B"}
	l, _ := New(cfg)
	answer = strings.Replace(soapAnswer("http://www.caqh.org/SOAP/WSDL/CORERule2.2.0.xsd", "", "UnAuthorized"),
		"<ErrorMessage></ErrorMessage>", "<ErrorMessage>bad user</ErrorMessage>", 1)
	if _, err := l.Send(context.Background(), []byte(request278)); err == nil || !strings.Contains(err.Error(), "UnAuthorized: bad user") {
		t.Errorf("%v", err)
	}
	answer = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:VersionMismatch</s:Value></s:Code>` +
		`<s:Reason><s:Text xml:lang="en">SOAP 1.1 is not accepted</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`
	if _, err := l.Send(context.Background(), []byte(request278)); err == nil || !strings.Contains(err.Error(), "SOAP fault, VersionMismatch: SOAP 1.1") {
		t.Errorf("%v", err)
	}
}

// A vC2.2.0 HTTP MIME multipart request carries the envelope's fields as form fields and the payload as a file part.
func TestACOREMIMERequestIsTheRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f := r.MultipartForm
		for k, want := range map[string]string{"PayloadType": PayloadType278Request, "ProcessingMode": "RealTime", "SenderID": "A",
			"ReceiverID": "B", "CORERuleVersion": "2.2.0", "UserName": "u", "Password": "pw", "TimeStamp": "2026-10-21T12:00:00Z"} {
			if got := strings.Join(f.Value[k], ","); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		fh := f.File["Payload"]
		if len(fh) != 1 {
			t.Fatalf("payload parts %d", len(fh))
		}
		pf, _ := fh[0].Open()
		pb, _ := io.ReadAll(pf)
		if string(pb) != request278 {
			t.Errorf("payload %q", pb)
		}
		mw := multipart.NewWriter(w)
		w.Header().Set("Content-Type", mw.FormDataContentType())
		for _, kv := range [][2]string{{"PayloadType", PayloadType278Response}, {"ProcessingMode", "RealTime"},
			{"ErrorCode", "Success"}, {"ErrorMessage", ""}} {
			_ = mw.WriteField(kv[0], kv[1])
		}
		p, _ := mw.CreateFormFile("Payload", "278.x12")
		_, _ = io.WriteString(p, response278)
		_ = mw.Close()
	}))
	defer srv.Close()
	cfg := Config{Transport: TransportCOREMIME, URL: srv.URL, SenderID: "A", ReceiverID: "B", Username: "u", Password: "pw"}
	fixed(&cfg)
	l, _ := New(cfg)
	out, err := l.Send(context.Background(), []byte(request278))
	if err != nil || string(out) != response278 {
		t.Fatalf("%q %v", out, err)
	}
}

// A password never crosses the network over plain http, except to this machine.
func TestAPasswordIsNeverSentOverPlainHTTP(t *testing.T) {
	if _, err := New(Config{Transport: TransportCORESOAP, URL: "http://um.example/core", Password: "x", Username: "u"}); err == nil {
		t.Error("accepted")
	}
	if _, err := New(Config{Transport: TransportCORESOAP, URL: "http://127.0.0.1:9/core", Password: "x", Username: "u"}); err != nil {
		t.Error(err)
	}
	if _, err := New(Config{Transport: TransportX12, URL: "https://um.example/x12", OAuth: OAuth{TokenURL: "http://auth.example/token"}}); err == nil {
		t.Error("an http token URL was accepted")
	}
}

// A batch link puts the request in the outbound folder whole, says the answer comes later, and Poll hands over each answer,
// puts the 999 it is given beside the requests, and removes the answer; an answer it could not handle stays.
func TestABatchLinkPutsFilesAndCollectsAnswers(t *testing.T) {
	srv := startSFTP(t)
	for _, d := range []string{"out", "in"} {
		_ = os.MkdirAll(filepath.Join(srv.root, d), 0o700)
	}
	cfg := Config{Transport: TransportSFTP, SFTP: SFTP{Address: srv.addr, User: "perfuse", Password: "pw", KnownHosts: srv.knownHosts(t),
		Outbound: "out", Inbound: "in"}}
	fixed(&cfg)
	l, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Send(context.Background(), []byte(request278)); !errors.Is(err, ErrAnswerLater) {
		t.Fatalf("%v", err)
	}
	sent, _ := filepath.Glob(filepath.Join(srv.root, "out", "278_*.x12"))
	if len(sent) != 1 {
		t.Fatalf("%v", sent)
	}
	if b, _ := os.ReadFile(sent[0]); string(b) != request278 {
		t.Fatalf("%q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(srv.root, "out", ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	_ = os.WriteFile(filepath.Join(srv.root, "in", "a.x12"), []byte(response278), 0o600)
	_ = os.WriteFile(filepath.Join(srv.root, "in", "b.x12"), []byte("garbled"), 0o600)
	_ = os.WriteFile(filepath.Join(srv.root, "in", ".c.x12.part"), []byte("half"), 0o600)
	var seen []string
	err = l.Poll(context.Background(), func(name string, body []byte) ([]byte, error) {
		seen = append(seen, name)
		if string(body) != response278 {
			return nil, errors.New("not X12")
		}
		return []byte("ISA*999~"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "b.x12: not X12") {
		t.Errorf("%v", err)
	}
	if strings.Join(seen, ",") != "a.x12,b.x12" {
		t.Errorf("seen %v", seen)
	}
	if _, err := os.Stat(filepath.Join(srv.root, "in", "a.x12")); !os.IsNotExist(err) {
		t.Error("a handled answer was left behind")
	}
	if _, err := os.Stat(filepath.Join(srv.root, "in", "b.x12")); err != nil {
		t.Error("an answer that could not be handled was removed")
	}
	if b, _ := os.ReadFile(filepath.Join(srv.root, "out", "999_a.x12")); string(b) != "ISA*999~" {
		t.Errorf("999 %q", b)
	}
	// An unknown host key is refused.
	cfg.SFTP.KnownHosts = filepath.Join(t.TempDir(), "empty")
	_ = os.WriteFile(cfg.SFTP.KnownHosts, nil, 0o600)
	l, _ = New(cfg)
	if _, err := l.Send(context.Background(), []byte(request278)); err == nil || errors.Is(err, ErrAnswerLater) {
		t.Errorf("sent to a server whose key is not known: %v", err)
	}
}

// ------------------------------------------------------------------------------------------------- a small SFTP server

type sftpServer struct {
	addr, root string
	key        ssh.PublicKey
}

func startSFTP(t *testing.T) *sftpServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "perfuse" && string(p) == "pw" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sftpServer{addr: ln.Addr().String(), root: t.TempDir(), key: signer.PublicKey()}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						return
					}
					go func() {
						for r := range creqs {
							ok := r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp"
							_ = r.Reply(ok, nil)
							if ok {
								srv, _ := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.root))
								_ = srv.Serve()
								_ = srv.Close()
								return
							}
						}
					}()
				}
			}(c)
		}
	}()
	return s
}

func (s *sftpServer) knownHosts(t *testing.T) string {
	host, port, _ := net.SplitHostPort(s.addr)
	p := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[%s]:%s %s\n", host, port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.key))))
	_ = os.WriteFile(p, []byte(line), 0o600)
	return p
}
