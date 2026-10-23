// Package umlink carries X12 278s to a payer's utilization management system, and its answers back, the ways those systems are
// reached in practice: X12 in the body of an HTTPS POST; CAQH CORE's SOAP envelope (Connectivity Rule vC2.2.0, the one most
// payers and CMS's esMD run, and vC4.0.0); CAQH CORE's HTTP MIME multipart envelope (vC2.2.0); and batch files over SFTP.
//
// Written from CAQH's published Connectivity Rules (vC2.2.0 and vC4.0.0), their XML Schema and WSDL, and CMS's esMD X12N 278
// Companion Guide. It is not CORE certified: certification is done by CAQH's authorised testing vendors.
//
// The real-time links return the answer the UM system gave, which may be a 278 response or, when it could not read the request,
// a 999 or TA1: the caller reads which. The batch link returns ErrAnswerLater once the file is delivered, and Poll collects what
// comes back.
package umlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Transports.
const (
	// TransportX12 POSTs the interchange itself, Content-Type application/edi-x12, and reads the answer from the response body.
	TransportX12 = "x12"
	// TransportCORESOAP wraps it in a CAQH CORE SOAP 1.2 envelope (COREEnvelopeRealTimeRequest).
	TransportCORESOAP = "core-soap"
	// TransportCOREMIME sends it as CAQH CORE HTTP MIME multipart/form-data (Connectivity Rule vC2.2.0 envelope A).
	TransportCOREMIME = "core-mime"
	// TransportSFTP puts it in a folder on the UM system's SFTP server and collects the answers from another.
	TransportSFTP = "sftp"
)

// CAQH CORE Connectivity Rule versions.
const (
	CORE220 = "2.2.0"
	CORE400 = "C4.0.0"
)

// The CORE payload types for the 278 request and response (Connectivity Rule vC2.2.0 table 4.4.3, unchanged in vC4.0.0).
const (
	PayloadType278Request  = "X12_278_Request_005010X217E1_2"
	PayloadType278Response = "X12_278_Response_005010X217E1_2"
	// The 278 inquiry (005010X215), as the vC3.1.0 payload type table names it for real time; not CORE-required.
	PayloadType278InquiryRequest  = "X12_278_Request_005010X215"
	PayloadType278InquiryResponse = "X12_278_Response_005010X215"
)

// ErrAnswerLater is returned by a batch link once the request is delivered: the answer arrives later, through Poll.
var ErrAnswerLater = errors.New("delivered; the answer comes later")

// Config says how to reach the UM system.
type Config struct {
	// Transport is one of the Transport constants; empty is x12.
	Transport string
	// URL is the endpoint, for the HTTP transports.
	URL string

	// COREVersion is the CAQH CORE Connectivity Rule: 2.2.0 (the default) or C4.0.0. CORERuleVersion is what is written in
	// the envelope's CORERuleVersion, when the partner wants something else than the version itself (esMD's sample says v2.2.0).
	COREVersion     string
	CORERuleVersion string
	// SenderID and ReceiverID are the CORE envelope's, as the partner's connectivity guide gives them.
	SenderID, ReceiverID string

	// Username and Password are CORE's user name and password (WS-Security UsernameToken in SOAP, UserName and Password parts in
	// MIME), or HTTP Basic for the x12 transport.
	Username, Password string
	// BearerToken is sent as Authorization: Bearer, when there is no OAuth.
	BearerToken string
	// OAuth, when TokenURL is set, gets a token with the client-credentials grant and sends it as a bearer token.
	OAuth OAuth

	// ClientCert and ClientKey are files for X.509 mutual TLS, which CORE requires (vC4.0.0) or offers (vC2.2.0).
	ClientCert, ClientKey string
	// CAFile trusts the UM system's server certificate when it is not from a public authority.
	CAFile string

	// Timeout bounds one exchange. Zero is 60 seconds, CORE's real-time answer window.
	Timeout time.Duration

	// SFTP is the batch link.
	SFTP SFTP

	// Now and NewID replace the clock and the payload ID generator, for tests.
	Now   func() time.Time
	NewID func() string
}

// OAuth is an OAuth 2.0 client-credentials client.
type OAuth struct {
	TokenURL, ClientID, ClientSecret, Scope string
}

// SFTP is where batch files go and come from.
type SFTP struct {
	// Address is host:port.
	Address string
	// User and Password, or KeyFile (an OpenSSH private key), sign in.
	User, Password, KeyFile string
	// KnownHosts is an OpenSSH known_hosts file the server's key must be in. Required: an unchecked host key sends patients'
	// requests to whoever answers.
	KnownHosts string
	// Outbound is the folder requests (and the 999s for answers) are put in; Inbound the folder answers are collected from.
	Outbound, Inbound string
}

// Link is a configured connection to a UM system.
type Link struct {
	cfg    Config
	client *http.Client
	files  fileStore

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New checks a configuration and returns the link.
func New(cfg Config) (*Link, error) {
	if cfg.Transport == "" {
		cfg.Transport = TransportX12
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = newUUID
	}
	if cfg.COREVersion == "" {
		cfg.COREVersion = CORE220
	}
	if cfg.CORERuleVersion == "" {
		cfg.CORERuleVersion = cfg.COREVersion
	}
	l := &Link{cfg: cfg}
	switch cfg.Transport {
	case TransportX12, TransportCORESOAP, TransportCOREMIME:
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("the UM system's address %q is not an http(s) URL", cfg.URL)
		}
		if u.Scheme == "http" && !isLoopback(u.Hostname()) && (cfg.Password != "" || cfg.BearerToken != "" || cfg.OAuth.ClientSecret != "") {
			return nil, errors.New("the UM system's address is http, and a password or token would cross the network unencrypted; use https")
		}
	case TransportSFTP:
		s := cfg.SFTP
		if s.Address == "" || s.User == "" || s.Outbound == "" || s.Inbound == "" {
			return nil, errors.New("an SFTP link needs an address, a user, an outbound folder and an inbound folder")
		}
		if s.KnownHosts == "" {
			return nil, errors.New("an SFTP link needs a known_hosts file with the server's key")
		}
		if s.Password == "" && s.KeyFile == "" {
			return nil, errors.New("an SFTP link needs a password or a key file")
		}
		if l.files == nil {
			l.files = &sftpStore{cfg: s, timeout: cfg.Timeout}
		}
	default:
		return nil, fmt.Errorf("unknown transport %q: x12, core-soap, core-mime or sftp", cfg.Transport)
	}
	if cfg.Transport != TransportSFTP {
		switch cfg.COREVersion {
		case CORE220, CORE400:
		default:
			return nil, fmt.Errorf("unknown CAQH CORE Connectivity Rule version %q: 2.2.0 or C4.0.0", cfg.COREVersion)
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.ClientCert != "" || cfg.ClientKey != "" {
			cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("the client certificate: %w", err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("the CA file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("the CA file %s holds no PEM certificate", cfg.CAFile)
			}
			tc.RootCAs = pool
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = tc
		l.client = &http.Client{Timeout: cfg.Timeout, Transport: tr}
	}
	if cfg.OAuth.TokenURL != "" {
		u, err := url.Parse(cfg.OAuth.TokenURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
			return nil, errors.New("the OAuth token URL must be https: the client secret is sent to it")
		}
	}
	return l, nil
}

// Transport names the transport in use.
func (l *Link) Transport() string { return l.cfg.Transport }

// Batch reports whether answers come back later, through Poll.
func (l *Link) Batch() bool { return l.cfg.Transport == TransportSFTP }

// Send delivers a 278 request. A real-time link returns the X12 the UM system answered with; a batch link returns
// ErrAnswerLater once the file is delivered. An answer the transport itself refused (a CORE envelope error, a SOAP fault, an
// HTTP error) is an error naming it.
func (l *Link) Send(ctx context.Context, request []byte) ([]byte, error) {
	switch l.cfg.Transport {
	case TransportCORESOAP:
		return l.sendSOAP(ctx, request)
	case TransportCOREMIME:
		return l.sendMIME(ctx, request)
	case TransportSFTP:
		name := "278_" + l.cfg.Now().UTC().Format("20060102T150405") + "_" + l.cfg.NewID() + ".x12"
		if err := l.files.put(ctx, name, request); err != nil {
			return nil, err
		}
		return nil, ErrAnswerLater
	default:
		return l.sendX12(ctx, request)
	}
}

// Poll collects the files waiting in a batch link's inbound folder, oldest name first, and gives each to handle. A reply
// handle returns (the 999 for a 278 response) is put in the outbound folder; a file handle accepted is then removed. A file
// handle fails on stays, and is offered again on the next Poll.
func (l *Link) Poll(ctx context.Context, handle func(name string, body []byte) (reply []byte, err error)) error {
	if l.files == nil {
		return errors.New("this link has no inbound folder: its answers come back in real time")
	}
	names, err := l.files.list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		body, err := l.files.get(ctx, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		reply, err := handle(name, body)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if len(reply) > 0 {
			if err := l.files.put(ctx, "999_"+strings.TrimSuffix(name, ".x12")+".x12", reply); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := l.files.remove(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *Link) sendX12(ctx context.Context, request []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.URL, bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/edi-x12")
	req.Header.Set("Accept", "application/edi-x12")
	if err := l.authorize(ctx, req); err != nil {
		return nil, err
	}
	if l.cfg.Username != "" && req.Header.Get("Authorization") == "" {
		req.SetBasicAuth(l.cfg.Username, l.cfg.Password)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the UM system answered %s", resp.Status)
	}
	return out, nil
}

// authorize adds a bearer token: OAuth's, got and kept until a minute before it expires, or the configured one.
func (l *Link) authorize(ctx context.Context, req *http.Request) error {
	if l.cfg.OAuth.TokenURL == "" {
		if l.cfg.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+l.cfg.BearerToken)
		}
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.token == "" || !l.cfg.Now().Before(l.expires) {
		form := url.Values{"grant_type": {"client_credentials"}}
		if l.cfg.OAuth.Scope != "" {
			form.Set("scope", l.cfg.OAuth.Scope)
		}
		treq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.OAuth.TokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		treq.Header.Set("Accept", "application/json")
		if l.cfg.OAuth.ClientID != "" {
			treq.SetBasicAuth(url.QueryEscape(l.cfg.OAuth.ClientID), url.QueryEscape(l.cfg.OAuth.ClientSecret))
		}
		resp, err := l.client.Do(treq)
		if err != nil {
			return fmt.Errorf("the OAuth token request: %w", err)
		}
		defer resp.Body.Close()
		var tok struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			Error       string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
		if resp.StatusCode/100 != 2 || tok.AccessToken == "" {
			return fmt.Errorf("the OAuth token endpoint answered %s %s", resp.Status, tok.Error)
		}
		life := time.Duration(tok.ExpiresIn) * time.Second
		if life <= 0 {
			life = 5 * time.Minute
		}
		l.token, l.expires = tok.AccessToken, l.cfg.Now().Add(life-time.Minute)
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	return nil
}

// ---------------------------------------------------------------------------------------------------------- CORE envelope

func (l *Link) namespace() string {
	if l.cfg.COREVersion == CORE400 {
		return "http://www.caqh.org/SOAP/WSDL/CORERuleC4.0.0.xsd"
	}
	return "http://www.caqh.org/SOAP/WSDL/CORERule2.2.0.xsd"
}

// payloadType is the CORE payload type for a 278: an inquiry (005010X215) or a request for review (005010X217), read from
// the interchange's own version.
func payloadType(request []byte) string {
	if bytes.Contains(request, []byte("005010X215")) {
		return PayloadType278InquiryRequest
	}
	return PayloadType278Request
}

// metadata are the envelope's fields, in the order the schema's sequence has them.
func (l *Link) metadata(request []byte) [][2]string {
	return [][2]string{
		{"PayloadType", payloadType(request)},
		{"ProcessingMode", "RealTime"},
		{"PayloadID", l.cfg.NewID()},
		// TimeStamp is an xsd:dateTime with its zone, in UTC (vC2.2.0 table 4.4.2).
		{"TimeStamp", l.cfg.Now().UTC().Format("2006-01-02T15:04:05Z")},
		{"SenderID", l.cfg.SenderID},
		{"ReceiverID", l.cfg.ReceiverID},
		{"CORERuleVersion", l.cfg.CORERuleVersion},
	}
}

// coreResponse is a COREEnvelopeRealTimeResponse, read by local names.
type coreResponse struct {
	PayloadType  string `xml:"PayloadType"`
	PayloadID    string `xml:"PayloadID"`
	Payload      corePayload
	ErrorCode    string `xml:"ErrorCode"`
	ErrorMessage string `xml:"ErrorMessage"`
}

type corePayload struct {
	Text    string `xml:",chardata"`
	Include struct {
		Href string `xml:"href,attr"`
	} `xml:"Include"`
}

func (p *coreResponse) payload(parts map[string][]byte) ([]byte, error) {
	if !strings.EqualFold(p.ErrorCode, "Success") {
		msg := strings.TrimSpace(p.ErrorMessage)
		if msg != "" {
			msg = ": " + msg
		}
		return nil, fmt.Errorf("the UM system's CORE envelope answered %s%s", orWord(p.ErrorCode, "no ErrorCode"), msg)
	}
	if href := p.Payload.Include.Href; href != "" {
		body, ok := parts[strings.TrimPrefix(href, "cid:")]
		if !ok {
			return nil, fmt.Errorf("the answer's payload refers to MIME part %s, which is not in the answer", href)
		}
		return decodePayload(body), nil
	}
	return decodePayload([]byte(p.Payload.Text)), nil
}

// decodePayload takes a payload as X12 text, or as base64 when it is not (CORE requires base64 for payloads with characters that
// are not printable, and vC4.0.0's schema types every payload base64Binary).
func decodePayload(b []byte) []byte {
	t := bytes.TrimSpace(b)
	if bytes.HasPrefix(t, []byte("ISA")) {
		return t
	}
	if d, err := base64.StdEncoding.DecodeString(string(bytes.Join(bytes.Fields(t), nil))); err == nil && bytes.HasPrefix(bytes.TrimSpace(d), []byte("ISA")) {
		return bytes.TrimSpace(d)
	}
	return t
}

func orWord(v, w string) string {
	if v == "" {
		return w
	}
	return v
}

// ------------------------------------------------------------------------------------------------------------------ SOAP

const (
	soap12NS   = "http://www.w3.org/2003/05/soap-envelope"
	wsseNS     = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	passwordTx = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText"
	xopNS      = "http://www.w3.org/2004/08/xop/include"
)

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (l *Link) sendSOAP(ctx context.Context, request []byte) ([]byte, error) {
	mtom := l.cfg.COREVersion == CORE400
	var env strings.Builder
	env.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	env.WriteString(`<soapenv:Envelope xmlns:soapenv="` + soap12NS + `">`)
	if l.cfg.Username != "" {
		// The WS-Security UsernameToken CORE vC2.2.0 names for user name and password authentication (§4.2.2.3).
		env.WriteString(`<soapenv:Header><wsse:Security xmlns:wsse="` + wsseNS + `" soapenv:mustUnderstand="true">` +
			`<wsse:UsernameToken><wsse:Username>` + xmlText(l.cfg.Username) + `</wsse:Username>` +
			`<wsse:Password Type="` + passwordTx + `">` + xmlText(l.cfg.Password) + `</wsse:Password>` +
			`</wsse:UsernameToken></wsse:Security></soapenv:Header>`)
	}
	env.WriteString(`<soapenv:Body><cor:COREEnvelopeRealTimeRequest xmlns:cor="` + l.namespace() + `">`)
	for _, f := range l.metadata(request) {
		env.WriteString("<" + f[0] + ">" + xmlText(f[1]) + "</" + f[0] + ">")
	}
	payloadID := "payload-" + l.cfg.NewID() + "@perfuse"
	if mtom {
		// vC4.0.0 sends the payload as an MTOM attachment (§4.1.3.4).
		env.WriteString(`<Payload><xop:Include xmlns:xop="` + xopNS + `" href="cid:` + payloadID + `"/></Payload>`)
	} else {
		// vC2.2.0's real-time Payload is a string; X12 with printable delimiters goes as text, as esMD's sample shows it.
		env.WriteString("<Payload>" + xmlText(string(request)) + "</Payload>")
	}
	env.WriteString(`</cor:COREEnvelopeRealTimeRequest></soapenv:Body></soapenv:Envelope>`)

	action := "RealTimeTransaction"
	if mtom {
		action = "http://www.caqh.org/SOAP/WSDL/CORETransactions/RealTimeTransaction"
	}
	var body bytes.Buffer
	contentType := `application/soap+xml; charset=UTF-8; action="` + action + `"`
	if mtom {
		w := multipart.NewWriter(&body)
		root := "root-" + l.cfg.NewID() + "@perfuse"
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", `application/xop+xml; charset=UTF-8; type="application/soap+xml"`)
		h.Set("Content-Transfer-Encoding", "binary")
		h.Set("Content-ID", "<"+root+">")
		p, _ := w.CreatePart(h)
		_, _ = p.Write([]byte(env.String()))
		h = textproto.MIMEHeader{}
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Transfer-Encoding", "binary")
		h.Set("Content-ID", "<"+payloadID+">")
		p, _ = w.CreatePart(h)
		_, _ = p.Write(request)
		_ = w.Close()
		contentType = `multipart/related; boundary="` + w.Boundary() + `"; type="application/xop+xml"; start="<` + root +
			`>"; start-info="application/soap+xml"; action="` + action + `"`
	} else {
		body.WriteString(env.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.URL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if err := l.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	root, parts, err := splitRelated(resp.Header.Get("Content-Type"), raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Body struct {
			Response *coreResponse `xml:"COREEnvelopeRealTimeResponse"`
			Fault    *struct {
				Code struct {
					Value   string `xml:"Value"`
					Subcode struct {
						Value string `xml:"Value"`
					} `xml:"Subcode"`
				} `xml:"Code"`
				Reason struct {
					Text string `xml:"Text"`
				} `xml:"Reason"`
			} `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(root, &doc); err != nil {
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("the UM system answered %s", resp.Status)
		}
		return nil, fmt.Errorf("the UM system's answer is not a SOAP envelope: %w", err)
	}
	if f := doc.Body.Fault; f != nil {
		code := localName(f.Code.Value)
		if sub := localName(f.Code.Subcode.Value); sub != "" {
			code += "/" + sub
		}
		return nil, fmt.Errorf("the UM system answered with a SOAP fault, %s: %s", code, strings.TrimSpace(f.Reason.Text))
	}
	if doc.Body.Response == nil {
		return nil, fmt.Errorf("the UM system answered %s with no COREEnvelopeRealTimeResponse", resp.Status)
	}
	return doc.Body.Response.payload(parts)
}

func localName(q string) string {
	if i := strings.LastIndex(q, ":"); i >= 0 {
		return q[i+1:]
	}
	return q
}

// splitRelated returns the root of a multipart/related (MTOM) answer and its other parts by Content-ID, or the whole body when
// the answer is not multipart.
func splitRelated(contentType string, raw []byte) ([]byte, map[string][]byte, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return raw, nil, nil
	}
	r := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	parts := map[string][]byte{}
	var root []byte
	start := strings.Trim(params["start"], "<>")
	for {
		p, err := r.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("the answer's MIME parts: %w", err)
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return nil, nil, err
		}
		id := strings.Trim(p.Header.Get("Content-ID"), "<>")
		if root == nil && (start == "" || id == start) {
			root = b
			continue
		}
		parts[id] = b
	}
	if root == nil {
		return nil, nil, errors.New("the answer's MIME root part is missing")
	}
	return root, parts, nil
}

// ------------------------------------------------------------------------------------------------------------------ MIME

func (l *Link) sendMIME(ctx context.Context, request []byte) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range l.metadata(request) {
		_ = w.WriteField(f[0], f[1])
	}
	if l.cfg.Username != "" {
		_ = w.WriteField("UserName", l.cfg.Username)
		_ = w.WriteField("Password", l.cfg.Password)
	}
	// The payload is a file part (vC2.2.0 §4.2.1: "sent as a MIME Multipart attachment").
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="Payload"; filename="278.x12"`)
	h.Set("Content-Type", "application/octet-stream")
	p, _ := w.CreatePart(h)
	_, _ = p.Write(request)
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.URL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := l.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the UM system answered %s", resp.Status)
	}
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return nil, fmt.Errorf("the UM system's answer is %q, not a CORE MIME multipart envelope", resp.Header.Get("Content-Type"))
	}
	r := multipart.NewReader(io.LimitReader(resp.Body, 32<<20), params["boundary"])
	var out coreResponse
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the answer's MIME parts: %w", err)
		}
		b, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		switch part.FormName() {
		case "PayloadType":
			out.PayloadType = string(b)
		case "ErrorCode":
			out.ErrorCode = strings.TrimSpace(string(b))
		case "ErrorMessage":
			out.ErrorMessage = string(b)
		case "Payload":
			out.Payload.Text = string(b)
		}
	}
	return out.payload(nil)
}

// ------------------------------------------------------------------------------------------------------------------ helpers

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
