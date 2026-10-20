package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/direct"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// startDirectSource takes this Direct address's messages every way it is configured to: SMTP deliveries from the HISP
// (listen), the address's mailbox at the HISP (imap), and XDR (xdr). Each message is opened (decrypted, its signature and
// signer checked) and its content handed to the channel: every document of an XDM package, else every attachment, else
// the text. A message that cannot be opened, or that the channel refuses, is refused at DATA (SMTP), moved to the error
// folder (IMAP) or answered with a failure (XDR), so the sender learns of it rather than Perfuse accepting what it then
// drops.
func (c *Channel) startDirectSource() error {
	src := c.cfg.Source.Direct
	if src == nil {
		return fmt.Errorf("channel %q: source type direct but no direct block", c.cfg.Name)
	}
	address := strings.ToLower(src.Address)
	var self cms.KeyPair
	var anchors *x509.CertPool
	if src.Listen != "" || src.IMAP != nil {
		var err error
		if self, _, anchors, err = LoadDirectAddress(src.DirectAddress); err != nil {
			return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
		}
	}
	if src.Listen != "" {
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
		r := &direct.Receiver{Hostname: domainPart(address), TLS: tlsCfg, RequireTLS: tlsCfg != nil, MaxBytes: src.MaxBytes, Log: c.log,
			Accept: func(rcpt string) bool { return rcpt == address },
			Deliver: func(ctx context.Context, from string, to []string, raw []byte) error {
				return c.receiveDirect(ctx, raw, self, anchors)
			}}
		c.directLn = ln
		c.log.Info("direct listening", "addr", ln.Addr().String(), "address", address, "starttls", tlsCfg != nil)
		go func() { _ = r.Serve(ln) }()
	}
	if src.IMAP != nil {
		if err := c.startDirectIMAP(self, anchors); err != nil {
			_ = c.stopDirectSource()
			return err
		}
	}
	if src.XDR != nil {
		if err := c.startDirectXDR(address); err != nil {
			_ = c.stopDirectSource()
			return err
		}
	}
	return nil
}

func domainPart(addr string) string { return addr[strings.LastIndexByte(addr, '@')+1:] }

// startDirectIMAP polls the address's mailbox at the HISP, as the email source polls one: each message is opened as Direct,
// and one that cannot be is moved to the error folder rather than read again at every poll.
func (c *Channel) startDirectIMAP(self cms.KeyPair, anchors *x509.CertPool) error {
	src := c.cfg.Source.Direct
	cfg := src.IMAP.EmailSource(src.MaxBytes)
	if errs := cfg.Validate(); len(errs) > 0 {
		return fmt.Errorf("channel %q: direct.imap: %w", c.cfg.Name, errors.Join(errs...))
	}
	dial, auth, err := emailDial(cfg)
	if err != nil {
		return fmt.Errorf("channel %q: direct.imap: %w", c.cfg.Name, err)
	}
	c.log.Info("direct mailbox polling", "host", cfg.Host, "mailbox", cfg.Mailbox, "address", src.Address)
	p := &kafkaPoller{stop: make(chan struct{})}
	c.directPoll = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-p.stop; cancel() }()
		for {
			err := c.pollIMAP(ctx, cfg, dial, auth, func(ctx context.Context, raw []byte) (string, error) {
				return "", c.receiveDirect(ctx, raw, self, anchors)
			})
			if err != nil && ctx.Err() == nil {
				c.log.Error("reading the Direct mailbox failed and will be retried at the next poll", "host", cfg.Host, "err", err)
			}
			select {
			case <-p.stop:
				return
			case <-time.After(cfg.PollInterval):
			}
		}
	}()
	return nil
}

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

// startDirectXDR takes XDR: an ITI-41 Provide and Register request posted to /xdr, as a HISP sends what it converted from
// Direct mail or a partner sends point to point. Each document goes to the channel; the answer is a RegistryResponse, a
// failure naming why when a document is refused or the message is for another address.
func (c *Channel) startDirectXDR(address string) error {
	x := c.cfg.Source.Direct.XDR
	var tlsCfg *tls.Config
	if x.TLSCert != "" {
		pair, err := tls.LoadX509KeyPair(x.TLSCert, x.TLSKey)
		if err != nil {
			return fmt.Errorf("channel %q: the XDR TLS certificate: %w", c.cfg.Name, err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
		if x.ClientCAFile != "" {
			pem, err := os.ReadFile(x.ClientCAFile)
			if err != nil {
				return fmt.Errorf("channel %q: direct.xdr.client_ca_file: %w", c.cfg.Name, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return fmt.Errorf("channel %q: direct.xdr.client_ca_file holds no PEM certificate", c.cfg.Name)
			}
			tlsCfg.ClientCAs, tlsCfg.ClientAuth = pool, tls.RequireAndVerifyClientCert
		}
	}
	ln, err := net.Listen("tcp", x.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /xdr", func(w http.ResponseWriter, r *http.Request) { c.serveXDR(w, r, address) })
	srv := &http.Server{Handler: mux, TLSConfig: tlsCfg, ReadHeaderTimeout: 30 * time.Second, ReadTimeout: 10 * time.Minute}
	c.directXDR = srv
	c.log.Info("direct XDR listening", "addr", ln.Addr().String(), "path", "/xdr", "address", address, "tls", tlsCfg != nil,
		"client_certificates", x.ClientCAFile != "")
	go func() {
		if tlsCfg != nil {
			_ = srv.ServeTLS(ln, "", "")
		} else {
			_ = srv.Serve(ln)
		}
	}()
	return nil
}

func (c *Channel) serveXDR(w http.ResponseWriter, r *http.Request, address string) {
	answer := func(status int, rr xds.RegistryResponse, relatesTo string) {
		body, _ := xml.Marshal(rr)
		b, ct := xds.Outgoing{Action: xds.ActionProvideAndRegister + "Response", RelatesTo: relatesTo, Body: body}.Write()
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = w.Write(b)
	}
	env, err := xds.ReadEnvelope(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, xds.MaxMessage))
	if err != nil {
		b, ct := xds.Fault("", "", "Sender", err.Error())
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(b)
		return
	}
	fail := func(code, why string) {
		c.log.Warn("an XDR submission was refused", "from", env.DirectFrom, "why", why)
		answer(http.StatusOK, xds.Response([]xds.Problem{{Code: code, Context: why}}), env.MessageID)
	}
	if env.Action != xds.ActionProvideAndRegister {
		b, ct := xds.Fault("", env.MessageID, "Sender", "an XDR endpoint takes ITI-41 Provide and Register ("+xds.ActionProvideAndRegister+"), not "+env.Action)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(b)
		return
	}
	// A Direct addressing block names whom the documents are for; one that does not name this address is somebody else's
	// mail and is not taken. Without one it is point-to-point XDR, for whoever runs this endpoint.
	if len(env.DirectTo) > 0 {
		mine := false
		for _, to := range env.DirectTo {
			if strings.EqualFold(to, address) {
				mine = true
			}
		}
		if !mine {
			fail("XDSRepositoryError", "the submission is addressed to "+strings.Join(env.DirectTo, ", ")+", not "+address)
			return
		}
	}
	var req xds.ProvideAndRegisterRequest
	if err := xml.Unmarshal(env.Body, &req); err != nil {
		fail("XDSRepositoryMetadataError", "the body is not a ProvideAndRegisterDocumentSetRequest: "+err.Error())
		return
	}
	if len(req.Documents) == 0 {
		fail("XDSMissingDocument", "the submission carries no document")
		return
	}
	for i := range req.Documents {
		if err := env.Resolve(&req.Documents[i]); err != nil {
			fail("XDSMissingDocument", err.Error())
			return
		}
	}
	for i, d := range req.Documents {
		if _, err := c.handle(r.Context(), d.Content); err != nil {
			fail("XDSRepositoryError", fmt.Sprintf("document %d of %d (%s) was not taken: %v", i+1, len(req.Documents), d.ID, err))
			return
		}
		if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
			fail("XDSRepositoryError", fmt.Sprintf("document %d of %d (%s) was %s", i+1, len(req.Documents), d.ID, outcome))
			return
		}
	}
	c.log.Info("direct XDR received", "from", env.DirectFrom, "documents", len(req.Documents))
	answer(http.StatusOK, xds.Response(nil), env.MessageID)
}

func (c *Channel) stopDirectSource() error {
	var err error
	if c.directLn != nil {
		err = c.directLn.Close()
		c.directLn = nil
	}
	if c.directPoll != nil {
		c.directPoll.close()
		c.directPoll = nil
	}
	if c.directXDR != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.directXDR.Shutdown(ctx)
		cancel()
		c.directXDR = nil
	}
	return err
}
