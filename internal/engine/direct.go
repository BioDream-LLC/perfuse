package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/direct"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// DefaultDirectCertsDir is the server's Direct address book, read when a destination names no recipient_certs_dir. Set at
// startup.
var DefaultDirectCertsDir string

// directSMTPRoots is nil (the system roots) except in tests, which run a HISP stand-in with a test certificate.
var directSMTPRoots *x509.CertPool

// LoadDirectAddress reads an address's certificate, chain, key and anchors.
func LoadDirectAddress(a config.DirectAddress) (cms.KeyPair, []*x509.Certificate, *x509.CertPool, error) {
	pair, err := tls.LoadX509KeyPair(a.CertFile, a.KeyFile)
	if err != nil {
		return cms.KeyPair{}, nil, nil, fmt.Errorf("direct: the certificate and key for %s cannot be loaded: %w", a.Address, err)
	}
	var chain []*x509.Certificate
	for _, der := range pair.Certificate {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return cms.KeyPair{}, nil, nil, err
		}
		chain = append(chain, c)
	}
	anchors, err := os.ReadFile(a.AnchorsFile)
	if err != nil {
		return cms.KeyPair{}, nil, nil, fmt.Errorf("direct: the trust anchors cannot be read: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(anchors) {
		return cms.KeyPair{}, nil, nil, fmt.Errorf("direct: %s holds no PEM certificate", a.AnchorsFile)
	}
	return cms.KeyPair{Certificate: chain[0], Key: pair.PrivateKey}, chain[1:], pool, nil
}

// readPEMCerts reads every certificate in a PEM file.
func readPEMCerts(path string) ([]*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return out, nil
		}
		if blk.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
				out = append(out, c)
			}
		}
	}
}

// DirectSender sends each message as a Direct secure message through the HISP.
type DirectSender struct {
	cfg     *config.DirectDestination
	self    cms.KeyPair
	chain   []*x509.Certificate
	anchors *x509.CertPool
	fields  *SMTPSender // field substitution only
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	lookup  func(ctx context.Context, address, server string) ([]*x509.Certificate, error)
	now     func() time.Time
	// checker validates recipient certificates (chain, AIA issuers, revocation); built on first use.
	checker   *direct.Checker
	checkerMu sync.Mutex
}

// NewDirectSender builds the sender; the address's certificate and key are loaded now, so a bad path fails at deploy.
func NewDirectSender(d config.Destination) (*DirectSender, error) {
	cfg := d.Direct
	if cfg == nil {
		return nil, fmt.Errorf("destination %q is a direct destination with no direct block", d.Name)
	}
	self, chain, anchors, err := LoadDirectAddress(cfg.DirectAddress)
	if err != nil {
		return nil, err
	}
	var dl net.Dialer
	return &DirectSender{cfg: cfg, self: self, chain: chain, anchors: anchors, fields: &SMTPSender{dataType: config.DataHL7},
		dial: dl.DialContext, now: time.Now, lookup: func(ctx context.Context, address, dns string) ([]*x509.Certificate, error) {
			certs, _, err := direct.FindCertificates(ctx, address, dns, cfg.LDAP)
			return certs, err
		}}, nil
}

// SetDataType decides substitution and the attachment's default name.
func (s *DirectSender) SetDataType(t config.DataType) { s.fields.dataType = t }

// recipientCert finds a recipient's certificate: the directory first (address.pem, then domain.pem), then DNS, then the
// recipient domain's LDAP directory. A certificate that does not chain to the anchors (with the issuers its AIA names
// fetched), has expired, is revoked on its issuer's CRL, or is not for the address or its domain is not used: sending to
// it would encrypt the message for someone the recipient never vouched for.
func (s *DirectSender) recipientCert(ctx context.Context, addr string) ([]*x509.Certificate, error) {
	addr = strings.ToLower(addr)
	domain := addr[strings.LastIndexByte(addr, '@')+1:]
	var found []*x509.Certificate
	dir := s.cfg.RecipientCertsDir
	if dir == "" {
		dir = DefaultDirectCertsDir
	}
	if dir != "" {
		for _, n := range []string{addr, domain} {
			if certs, err := readPEMCerts(filepath.Join(dir, n+".pem")); err == nil && len(certs) > 0 {
				found = certs
				break
			}
		}
	}
	if found == nil {
		certs, err := s.lookup(ctx, addr, s.cfg.DNS)
		if err != nil {
			return nil, err
		}
		found = certs
	}
	var ok []*x509.Certificate
	var why []string
	for _, c := range found {
		if !forAddress(c, addr) {
			why = append(why, c.Subject.CommonName+" is not for "+addr)
			continue
		}
		if err := s.certChecker().Check(ctx, c); err != nil {
			why = append(why, c.Subject.CommonName+": "+err.Error())
			continue
		}
		ok = append(ok, c)
	}
	if len(ok) == 0 {
		if len(why) > 0 {
			return nil, fmt.Errorf("direct: no usable certificate for %s (%s)", addr, strings.Join(why, "; "))
		}
		return nil, fmt.Errorf("direct: no certificate was found for %s, in the directory, in DNS or in LDAP", addr)
	}
	return ok, nil
}

func (s *DirectSender) certChecker() *direct.Checker {
	s.checkerMu.Lock()
	defer s.checkerMu.Unlock()
	if s.checker == nil {
		s.checker = &direct.Checker{Anchors: s.anchors, Now: s.now}
	}
	return s.checker
}

func forAddress(c *x509.Certificate, addr string) bool {
	domain := addr[strings.LastIndexByte(addr, '@')+1:]
	for _, e := range c.EmailAddresses {
		if strings.EqualFold(e, addr) {
			return true
		}
	}
	for _, d := range c.DNSNames {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

func (s *DirectSender) attachment(msg []byte) (direct.Attachment, error) {
	name := s.cfg.AttachName
	trimmed := strings.TrimSpace(string(msg))
	isCDA := strings.HasPrefix(trimmed, "<") && strings.Contains(trimmed[:min(len(trimmed), 2048)], "ClinicalDocument")
	if s.cfg.XDM {
		d, raw, err := xdsDocument(msg)
		if err != nil {
			return direct.Attachment{}, err
		}
		meta, err := xds.MetaFromCDA(d, raw, xds.CDAOptions{SourceID: s.cfg.XDMSourceID})
		if err != nil {
			return direct.Attachment{}, err
		}
		req, err := xds.Build(meta, s.now())
		if err != nil {
			return direct.Attachment{}, err
		}
		z, err := direct.XDMFor(req, "DOC0001.XML", raw)
		if err != nil {
			return direct.Attachment{}, err
		}
		if name == "" {
			name = "xdm.zip"
		}
		return direct.Attachment{Name: name, MimeType: "application/zip", Content: z}, nil
	}
	mt := "application/octet-stream"
	switch {
	case isCDA:
		mt = "text/xml"
		if name == "" {
			name = "document.xml"
		}
	case s.fields.dataType == config.DataHL7:
		mt = "application/hl7-v2"
		if name == "" {
			name = "message.hl7"
		}
	case strings.HasPrefix(trimmed, "{"):
		mt = "application/fhir+json"
		if name == "" {
			name = "resource.json"
		}
	case strings.HasPrefix(trimmed, "<"):
		mt = "application/xml"
	}
	if name == "" {
		name = "message.dat"
	}
	return direct.Attachment{Name: name, MimeType: mt, Content: msg}, nil
}

// Send protects one message and submits it to the HISP.
func (s *DirectSender) Send(ctx context.Context, msg []byte) error {
	var to []string
	for _, t := range s.cfg.To {
		v := strings.TrimSpace(s.fields.expand(t, msg))
		if v == "" {
			return fmt.Errorf("direct: recipient %q came out empty for this message", t)
		}
		if err := headerSafe("recipient", v); err != nil {
			return err
		}
		to = append(to, v)
	}
	subject := s.fields.expand(s.cfg.Subject, msg)
	if strings.TrimSpace(subject) == "" {
		subject = "Message from Perfuse"
	}
	if err := headerSafe("subject", subject); err != nil {
		return err
	}
	var certs []*x509.Certificate
	for _, t := range to {
		c, err := s.recipientCert(ctx, t)
		if err != nil {
			return err
		}
		certs = append(certs, c...)
	}
	att, err := s.attachment(msg)
	if err != nil {
		return err
	}
	raw, err := direct.Protect(direct.Message{From: s.cfg.Address, To: to, Subject: subject, Text: s.fields.expand(s.cfg.Text, msg),
		Attachments: []direct.Attachment{att}, DispositionNotification: s.cfg.RequestMDN}, s.self, s.chain, certs)
	if err != nil {
		return err
	}
	return s.submit(ctx, to, raw)
}

func (s *DirectSender) submit(ctx context.Context, to []string, raw []byte) error {
	return submitDirect(ctx, s.dial, s.cfg.SMTPHost, s.cfg.Username, s.cfg.Password, s.cfg.Address, s.cfg.Timeout, to, raw)
}

// submitDirect hands a protected message to the HISP over SMTP with STARTTLS required.
func submitDirect(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), smtpHost, user, pass,
	from string, timeout time.Duration, to []string, raw []byte) error {
	addr := smtpHost
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "587")
	}
	host, _, _ := net.SplitHostPort(addr)
	if timeout <= 0 {
		timeout = time.Minute
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(dctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("direct: the HISP %s cannot be reached: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("direct: %w", err)
	}
	defer func() { _ = c.Quit() }()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("direct: the HISP does not offer STARTTLS; Direct does not hand a message to a HISP in the clear")
	}
	if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: directSMTPRoots}); err != nil {
		return fmt.Errorf("direct: starting TLS with the HISP: %w", err)
	}
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return fmt.Errorf("direct: signing in to the HISP as %s: %w", user, err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("direct: the HISP refused the sender %s: %w", from, err)
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return fmt.Errorf("direct: the HISP refused the recipient %s: %w", t, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("direct: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("direct: writing the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("direct: the HISP did not take the message: %w", err)
	}
	return nil
}

// Describe names the destination, never the credentials.
func (s *DirectSender) Describe() string {
	return s.cfg.Address + " to " + strings.Join(s.cfg.To, ", ") + " via " + s.cfg.SMTPHost + " (Direct)"
}

// Close releases nothing.
func (s *DirectSender) Close() error { return nil }
