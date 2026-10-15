package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"

	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/direct"
)

// startDirectSource listens for the HISP's SMTP deliveries to this Direct address. Each message is opened (decrypted, its
// signature and signer checked) and its content handed to the channel: every document of an XDM package, else every
// attachment, else the text. A message that cannot be opened, or that the channel refuses, is refused at DATA, so the HISP
// keeps it and reports the failure to the sender, rather than Perfuse accepting what it then drops.
func (c *Channel) startDirectSource() error {
	src := c.cfg.Source.Direct
	if src == nil {
		return fmt.Errorf("channel %q: source type direct but no direct block", c.cfg.Name)
	}
	self, _, anchors, err := LoadDirectAddress(src.DirectAddress)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	var tlsCfg *tls.Config
	if src.TLSCert != "" {
		pair, err := tls.LoadX509KeyPair(src.TLSCert, src.TLSKey)
		if err != nil {
			return fmt.Errorf("channel %q: the SMTP TLS certificate: %w", c.cfg.Name, err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", src.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	address := strings.ToLower(src.Address)
	r := &direct.Receiver{Hostname: domainPart(address), TLS: tlsCfg, RequireTLS: tlsCfg != nil, MaxBytes: src.MaxBytes, Log: c.log,
		Accept: func(rcpt string) bool { return rcpt == address },
		Deliver: func(ctx context.Context, from string, to []string, raw []byte) error {
			return c.receiveDirect(ctx, raw, self, anchors)
		}}
	c.directLn = ln
	c.log.Info("direct listening", "addr", ln.Addr().String(), "address", address, "starttls", tlsCfg != nil)
	go func() { _ = r.Serve(ln) }()
	return nil
}

func domainPart(addr string) string { return addr[strings.LastIndexByte(addr, '@')+1:] }

func (c *Channel) receiveDirect(ctx context.Context, raw []byte, self cms.KeyPair, anchors *x509.CertPool) error {
	src := c.cfg.Source.Direct
	opened, err := direct.Open(raw, []cms.KeyPair{self}, anchors)
	if err != nil {
		return err
	}
	if opened.MDN != nil {
		// A notification about something this address sent: logged, not passed on as a message.
		c.log.Info("direct delivery notification", "from", opened.From, "kind", opened.MDN.Kind, "about", opened.MDN.OriginalMessageID)
		return nil
	}
	var parts [][]byte
	switch {
	case len(opened.XDM) > 0:
		for _, d := range opened.XDM {
			parts = append(parts, d.Content)
		}
	case len(opened.Attachments) > 0:
		for _, a := range opened.Attachments {
			parts = append(parts, a.Content)
		}
	default:
		parts = append(parts, []byte(opened.Text))
	}
	c.log.Info("direct message received", "from", opened.From, "signer", opened.SignerAddress, "parts", len(parts), "xdm", len(opened.XDM) > 0)
	for _, p := range parts {
		if _, err := c.handle(ctx, p); err != nil {
			return fmt.Errorf("the message was opened but the channel did not take it: %w", err)
		}
	}
	if src.SMTPHost != "" {
		mdn := direct.MDN(opened, src.Address, "processed")
		var certs []*x509.Certificate
		if opened.Signer != nil {
			certs = []*x509.Certificate{opened.Signer}
		}
		protected, err := direct.Protect(mdn, self, nil, certs)
		if err == nil {
			var d net.Dialer
			err = submitDirect(ctx, d.DialContext, src.SMTPHost, src.Username, src.Password, src.Address, 0, mdn.To, protected)
		}
		if err != nil {
			// The message itself was taken; a lost notification is logged, not a refusal (the HISP would resend a message
			// already delivered).
			c.log.Warn("the delivery notification could not be sent", "to", mdn.To, "error", err)
		}
	}
	return nil
}

func (c *Channel) stopDirectSource() error {
	if c.directLn == nil {
		return nil
	}
	err := c.directLn.Close()
	c.directLn = nil
	return err
}
