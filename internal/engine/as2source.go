package engine

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/as2"
	"github.com/biodream-llc/perfuse/internal/cms"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// DefaultAS2MaxMessageSize bounds one AS2 request when the source sets no limit.
const DefaultAS2MaxMessageSize = 50 << 20

type as2Partner struct {
	cfg  config.AS2Partner
	pool *x509.CertPool
}

// startAS2Source listens for AS2 messages from the partners the source lists.
func (c *Channel) startAS2Source() error {
	src := c.cfg.Source.AS2
	if src == nil {
		return fmt.Errorf("channel %q: source type as2 but no as2 block", c.cfg.Name)
	}
	self, chain, err := loadAS2KeyPair(src.CertFile, src.KeyFile)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	partners := map[string]as2Partner{}
	for _, p := range src.Partners {
		ap := as2Partner{cfg: p}
		if p.CertFile != "" {
			if _, ap.pool, err = loadPartnerCert(p.CertFile); err != nil {
				return fmt.Errorf("channel %q: partner %s: %w", c.cfg.Name, p.ID, err)
			}
		}
		partners[p.ID] = ap
	}
	tlsCfg, err := tlsconf.ForListener(src.TLS)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	if tlsCfg == nil {
		c.log.Warn("as2 source", "detail", "serving AS2 over plain HTTP: the payload is encrypted, but most partner agreements require HTTPS as well")
	}
	maxSize := src.MaxMessageSize
	if maxSize <= 0 {
		maxSize = DefaultAS2MaxMessageSize
	}
	mux := http.NewServeMux()
	mux.HandleFunc(src.AS2Path(), c.handleAS2(src, self, chain, partners, maxSize))
	srv := &http.Server{Addr: src.Listen, Handler: mux, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 5 * time.Minute}
	ln, err := net.Listen("tcp", src.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	c.httpServer = srv
	c.log.Info("as2 listening", "addr", src.Listen, "path", src.AS2Path(), "as2_id", src.ID, "partners", len(partners), "tls", tlsCfg != nil)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("the as2 listener stopped", "error", err)
		}
	}()
	return nil
}

// isReceipt reports whether a request is an asynchronous receipt rather than a message.
func isReceipt(h http.Header, body []byte) bool {
	mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	if mt == "multipart/report" {
		return true
	}
	return mt == "multipart/signed" && h.Get("Disposition-Notification-To") == "" &&
		bytes.Contains(body, []byte("message/disposition-notification"))
}

func (c *Channel) handleAS2(src *config.AS2Source, self *cms.KeyPair, chain []*x509.Certificate, partners map[string]as2Partner,
	maxSize int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "AS2 messages are sent with POST", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSize+1))
		if err != nil {
			http.Error(w, "could not read the request", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxSize {
			http.Error(w, fmt.Sprintf("the message is larger than the %d byte limit", maxSize), http.StatusRequestEntityTooLarge)
			return
		}
		from, to := as2.Unquote(r.Header.Get("AS2-From")), as2.Unquote(r.Header.Get("AS2-To"))
		if to != src.ID {
			http.Error(w, fmt.Sprintf("AS2-To %q is not this server's AS2 identifier", to), http.StatusForbidden)
			return
		}
		partner, known := partners[from]
		if !known {
			// No receipt: there is no agreement with this sender, so nothing to sign one with in good faith.
			c.log.Warn("as2: a message from an unknown partner was refused", "as2_from", from)
			http.Error(w, fmt.Sprintf("AS2-From %q is not a partner of this server", from), http.StatusForbidden)
			return
		}

		if isReceipt(r.Header, body) {
			c.receiveAS2Receipt(w, r.Header, body)
			return
		}

		got, err := as2.Receive(r.Header, body, as2.ReceiveOptions{Keys: []cms.KeyPair{*self}, Partner: partner.pool,
			RequireSigned: !partner.cfg.AllowUnsigned, RequireEncrypted: !partner.cfg.AllowUnencrypted})
		var failure *as2.Failure
		if err != nil {
			if !errors.As(err, &failure) {
				failure = &as2.Failure{Kind: "unexpected-processing-error", Err: err}
			}
			c.log.Warn("as2: a message was refused", "as2_from", from, "message_id", r.Header.Get("Message-ID"), "reason", err.Error())
		} else {
			ctx := r.Context()
			if _, herr := c.handle(ctx, got.Payload); herr != nil {
				failure = &as2.Failure{Kind: "unexpected-processing-error", Err: herr}
				c.log.Warn("as2: a message could not be processed", "as2_from", from, "message_id", got.MessageID, "reason", herr.Error())
			} else {
				c.log.Info("as2: message received", "as2_from", from, "message_id", got.MessageID, "file", got.FileName,
					"signed", got.Signed, "encrypted", got.Encrypted)
			}
		}

		mode, signed, asyncURL, _ := as2.Requested(r.Header)
		if mode == as2.MDNNone {
			if failure != nil {
				http.Error(w, failure.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		rc := as2.Receipt{From: src.ID, To: from, OriginalMessageID: r.Header.Get("Message-ID"), Failure: failure}
		if got != nil {
			rc.MIC = got.MIC
		}
		if signed {
			rc.Signer, rc.Chain = self, chain
		}
		h, b, err := as2.BuildReceipt(rc)
		if err != nil {
			http.Error(w, "the receipt could not be built", http.StatusInternalServerError)
			return
		}
		if mode == as2.MDNAsync {
			// The message is answered now; the receipt goes to the URL the partner gave, on its own connection.
			w.WriteHeader(http.StatusOK)
			go c.postAS2Receipt(asyncURL, h, b)
			return
		}
		for k, v := range h {
			w.Header()[k] = v
		}
		_, _ = w.Write(b)
	}
}

// postAS2Receipt delivers an asynchronous receipt, trying a few times.
func (c *Channel) postAS2Receipt(url string, h http.Header, body []byte) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			c.log.Error("as2: the receipt URL is not usable", "url", url, "error", err)
			return
		}
		for k, v := range h {
			req.Header[k] = v
		}
		res, err := client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("HTTP %d", res.StatusCode)
		}
		c.log.Warn("as2: posting a receipt failed", "url", url, "attempt", attempt, "error", err)
		time.Sleep(time.Duration(attempt*attempt) * 5 * time.Second)
	}
}

// receiveAS2Receipt checks an asynchronous receipt against the message it answers.
func (c *Channel) receiveAS2Receipt(w http.ResponseWriter, h http.Header, body []byte) {
	rr, err := as2.ReadReceipt(h, body, nil, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sent, ok := takeAS2(rr.OriginalMessageID)
	if !ok {
		c.log.Warn("as2: a receipt arrived for a message this server did not send, or answered already", "original_message_id", rr.OriginalMessageID)
		w.WriteHeader(http.StatusOK)
		return
	}
	// Read again with the partner's certificate now that the message, and so the partner, is known.
	if sent.roots != nil || sent.requireSigned {
		if rr, err = as2.ReadReceipt(h, body, sent.roots, sent.requireSigned); err != nil {
			c.log.Error("as2: a receipt did not verify", "destination", sent.destination, "message_id", sent.built.MessageID, "error", err)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	if err := rr.Check(sent.built); err != nil {
		c.log.Error("as2: the partner's receipt says the message was not accepted as sent", "destination", sent.destination,
			"message_id", sent.built.MessageID, "error", err)
	} else {
		c.log.Info("as2: receipt confirms delivery", "destination", sent.destination, "message_id", sent.built.MessageID,
			"disposition", strings.TrimSpace(rr.Disposition))
	}
	w.WriteHeader(http.StatusOK)
}
