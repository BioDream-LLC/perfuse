package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/as2"
	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// AS2Sender sends each message to a trading partner over AS2. With a synchronous receipt (the default) a message is
// delivered only when the partner's receipt says processed and names the digest of what was sent; anything else fails the
// delivery, so the engine retries it.
type AS2Sender struct {
	cfg      *config.AS2Destination
	name     string
	client   *http.Client
	self     *cms.KeyPair
	chain    []*x509.Certificate
	partner  *x509.Certificate
	roots    *x509.CertPool
	dataType config.DataType
}

// SetDataType sets the payload's default content type.
func (s *AS2Sender) SetDataType(t config.DataType) { s.dataType = t }

// loadAS2KeyPair reads a certificate (with any chain after it) and its key.
func loadAS2KeyPair(certFile, keyFile string) (*cms.KeyPair, []*x509.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("as2: the certificate and key cannot be loaded: %w", err)
	}
	var chain []*x509.Certificate
	for _, der := range pair.Certificate {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, err
		}
		chain = append(chain, c)
	}
	return &cms.KeyPair{Certificate: chain[0], Key: pair.PrivateKey}, chain[1:], nil
}

// loadPartnerCert reads a partner's certificate and makes the pool its signatures are checked against: the certificate
// itself, since partners usually exchange self-signed ones, and any authority after it in the file.
func loadPartnerCert(path string) (*x509.Certificate, *x509.CertPool, error) {
	certs, err := readPEMCerts(path)
	if err != nil {
		return nil, nil, fmt.Errorf("as2: the partner certificate %s cannot be read: %w", path, err)
	}
	if len(certs) == 0 {
		return nil, nil, fmt.Errorf("as2: %s holds no PEM certificate", path)
	}
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return certs[0], pool, nil
}

// NewAS2Sender builds the sender, reading every key and certificate now.
func NewAS2Sender(d config.Destination) (*AS2Sender, error) {
	c := d.AS2
	if c == nil {
		return nil, fmt.Errorf("destination %q has no as2 block", d.Name)
	}
	s := &AS2Sender{cfg: c, name: d.Name, dataType: config.DataHL7}
	if !c.Unsigned {
		kp, chain, err := loadAS2KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, err
		}
		s.self, s.chain = kp, chain
	}
	if c.PartnerCertFile != "" {
		cert, pool, err := loadPartnerCert(c.PartnerCertFile)
		if err != nil {
			return nil, err
		}
		s.partner, s.roots = cert, pool
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if c.TLS != nil {
		tc, err := tlsconf.ForSender(c.TLS)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
		transport.TLSClientConfig = tc
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	s.client = &http.Client{Transport: transport, Timeout: timeout,
		// A redirect would re-post the encrypted message somewhere the agreement did not name.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s, nil
}

func (s *AS2Sender) contentType() string {
	if s.cfg.ContentType != "" {
		return s.cfg.ContentType
	}
	switch s.dataType {
	case config.DataX12:
		return "application/edi-x12"
	case config.DataHL7, "":
		return "application/hl7-v2"
	case config.DataHL7v3, config.DataScript:
		return "application/xml"
	}
	return "application/octet-stream"
}

func (s *AS2Sender) fileName(msg []byte) string {
	if s.cfg.FileName == "" {
		return ""
	}
	now := time.Now().UTC()
	r := strings.NewReplacer("${timestamp}", now.Format("20060102150405"), "${date}", now.Format("20060102"),
		"${control_id}", sanitiseKeySegment(interchangeID(msg)))
	return sanitiseFileName(r.Replace(s.cfg.FileName))
}

// interchangeID is a message's own identifier: MSH-10, ISA13 or the UNB interchange reference.
func interchangeID(msg []byte) string {
	t := bytes.TrimLeft(msg, " \r\n\t\ufeff")
	field := func(sep byte, idx int, term string) string {
		end := len(t)
		if i := strings.IndexAny(string(t), term); i >= 0 {
			end = i
		}
		parts := strings.Split(string(t[:end]), string(sep))
		if idx < len(parts) {
			return strings.TrimSpace(parts[idx])
		}
		return ""
	}
	switch {
	case bytes.HasPrefix(t, []byte("MSH")) && len(t) > 3:
		return field(t[3], 9, "\r\n")
	case bytes.HasPrefix(t, []byte("ISA")) && len(t) > 106:
		return field(t[3], 13, string(t[105:106]))
	case bytes.HasPrefix(t, []byte("UNA")), bytes.HasPrefix(t, []byte("UNB")):
		if i := bytes.Index(t, []byte("UNB")); i >= 0 {
			t = t[i:]
			return field('+', 5, "'")
		}
	}
	return "noid-" + uniqueStamp()
}

// Send posts one message and, for a synchronous receipt, checks it.
func (s *AS2Sender) Send(ctx context.Context, msg []byte) error {
	_, err := s.SendForResponse(ctx, msg)
	return err
}

// SendForResponse returns the receipt's text, so a response transformer can read it.
func (s *AS2Sender) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	mode := as2.MDNMode(s.cfg.ReceiptMode())
	o := as2.Outgoing{From: s.cfg.From, To: s.cfg.To, Payload: msg, ContentType: s.contentType(), FileName: s.fileName(msg),
		Subject: s.cfg.Subject, Signer: s.self, Chain: s.chain, MDN: mode, SignedMDN: !s.cfg.UnsignedReceipt, AsyncURL: s.cfg.ReceiptURL}
	if !s.cfg.Unencrypted {
		o.EncryptTo = s.partner
	}
	built, err := as2.Build(o)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(built.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range built.Header {
		req.Header[k] = v
	}
	if s.cfg.Username != "" {
		req.SetBasicAuth(config.ResolveSecret(s.cfg.Username), config.ResolveSecret(s.cfg.Password))
	}
	if mode == as2.MDNAsync {
		rememberAS2(built, s.name, s.roots, !s.cfg.UnsignedReceipt)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("as2: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 10<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return body, fmt.Errorf("as2: the partner answered HTTP %d: %s", res.StatusCode, firstLine(body, 200))
	}
	if mode != as2.MDNSync {
		return body, nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return body, errors.New("as2: the partner sent no receipt, and a synchronous one was asked for")
	}
	rr, err := as2.ReadReceipt(res.Header, body, s.roots, !s.cfg.UnsignedReceipt)
	if err != nil {
		return body, err
	}
	if err := rr.Check(built); err != nil {
		return []byte(rr.Disposition), err
	}
	return []byte(rr.Disposition), nil
}

func (s *AS2Sender) Describe() string {
	return fmt.Sprintf("AS2 %s → %s at %s (receipt %s)", s.cfg.From, s.cfg.To, s.cfg.URL, s.cfg.ReceiptMode())
}

func (s *AS2Sender) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// Asynchronous receipts arrive later, at an as2 source on this server: what was sent is remembered until then.
type pendingAS2 struct {
	built         *as2.Built
	destination   string
	roots         *x509.CertPool
	requireSigned bool
	at            time.Time
}

var (
	as2PendingMu sync.Mutex
	as2Pending   = map[string]pendingAS2{}
)

func rememberAS2(b *as2.Built, destination string, roots *x509.CertPool, requireSigned bool) {
	as2PendingMu.Lock()
	defer as2PendingMu.Unlock()
	now := time.Now()
	for id, p := range as2Pending {
		if now.Sub(p.at) > 7*24*time.Hour {
			delete(as2Pending, id)
		}
	}
	as2Pending[strings.Trim(b.MessageID, "<> ")] = pendingAS2{built: b, destination: destination, roots: roots, requireSigned: requireSigned, at: now}
}

// takeAS2 returns and forgets what was sent with this Message-ID.
func takeAS2(id string) (pendingAS2, bool) {
	as2PendingMu.Lock()
	defer as2PendingMu.Unlock()
	key := strings.Trim(id, "<> ")
	p, ok := as2Pending[key]
	delete(as2Pending, key)
	return p, ok
}
