package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mailbox"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// startEmailSource polls a mailbox. Each poll signs in, reads what is unread, and signs out: a mail server drops idle sessions
// and throttles held ones, and a fresh session each time means a lost connection costs one poll, not a stuck channel.
func (c *Channel) startEmailSource() error {
	cfg := c.cfg.Source.Email
	if cfg == nil {
		return fmt.Errorf("channel %q has an email source with no configuration", c.cfg.Name)
	}
	for _, w := range cfg.Warnings() {
		c.log.Warn(w)
	}
	dial, auth, err := emailDial(cfg)
	if err != nil {
		return err
	}
	c.log.Info("email source starting", "protocol", cfg.Protocol, "host", cfg.Host, "mailbox", cfg.Mailbox, "take", cfg.Take)
	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-p.stop; cancel() }()
		// POP3 mail left on the server (not taken, or failed) is remembered by its unique id for the life of the channel, so
		// it is reported once rather than at every poll.
		leftOnServer := map[string]bool{}
		for {
			var err error
			if cfg.Protocol == "pop3" {
				err = c.pollPOP3(ctx, cfg, dial, auth, leftOnServer)
			} else {
				err = c.pollIMAP(ctx, cfg, dial, auth, func(ctx context.Context, raw []byte) (string, error) {
					msgs, skip, err := emailMessages(cfg, raw)
					if err != nil || skip != "" {
						return skip, err
					}
					return "", c.feedEmail(ctx, msgs)
				})
			}
			if err != nil && ctx.Err() == nil {
				c.log.Error("reading the mailbox failed and will be retried at the next poll", "host", cfg.Host, "err", err)
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

func emailDial(cfg *config.EmailSource) (mailbox.Dial, mailbox.Auth, error) {
	d := mailbox.Dial{Host: cfg.Host, Port: cfg.Port, Security: cfg.Security, Timeout: 60 * time.Second}
	if cfg.TLS != nil {
		s := *cfg.TLS
		s.Enabled = true
		tc, err := tlsconf.ForSender(&s)
		if err != nil {
			return d, mailbox.Auth{}, err
		}
		d.TLS = tc
	} else {
		d.TLS = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	}
	a := mailbox.Auth{Username: cfg.Username, Password: cfg.ResolvedPassword()}
	if o := cfg.ResolvedOAuth(); o != nil {
		src := &mailbox.OAuth{Provider: o.Provider, Tenant: o.Tenant, TokenURL: o.TokenURL, ClientID: o.ClientID,
			ClientSecret: o.ClientSecret, RefreshToken: o.RefreshToken, Scope: o.Scope, Protocol: cfg.Protocol}
		if err := src.Validate(); err != nil {
			return d, a, err
		}
		a.Token = src.Token
	}
	return d, a, nil
}

// emailMessages turns one email into the messages the channel takes, or says why it takes none.
func emailMessages(cfg *config.EmailSource, raw []byte) ([][]byte, string, error) {
	if cfg.Take == config.EmailTakeRaw {
		if len(cfg.AllowedSenders) > 0 {
			p, err := mailbox.Parse(raw)
			if err != nil {
				return nil, "", err
			}
			if !mailbox.SenderAllowed(p.From, cfg.AllowedSenders) {
				return nil, "the sender " + p.From + " is not in allowed_senders", nil
			}
		}
		return [][]byte{raw}, "", nil
	}
	p, err := mailbox.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	if !mailbox.SenderAllowed(p.From, cfg.AllowedSenders) {
		return nil, "the sender " + p.From + " is not in allowed_senders", nil
	}
	if cfg.Take == config.EmailTakeBody {
		if len(p.Text) == 0 {
			return nil, "it has no text body", nil
		}
		return [][]byte{[]byte(p.Text)}, "", nil
	}
	var out [][]byte
	for _, a := range p.Attachments {
		if cfg.AttachmentWanted(a.Filename) && len(a.Data) > 0 {
			out = append(out, a.Data)
		}
	}
	if len(out) == 0 {
		return nil, "it has no attachment matching attachment_names", nil
	}
	return out, "", nil
}

// feedEmail hands an email's messages to the channel; every one must be accepted for the email to count as handled.
func (c *Channel) feedEmail(ctx context.Context, msgs [][]byte) error {
	for i, m := range msgs {
		if _, err := c.handle(ctx, m); err != nil {
			return err
		}
		if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
			return fmt.Errorf("message %d of %d in the email was %s", i+1, len(msgs), outcome)
		}
	}
	return nil
}

// pollIMAP reads a mailbox once. take handles one message: a reason not to take it (it moves to the error folder with a
// warning), or an error (it moves there with the error), or neither (it is handled, and moved, flagged or deleted).
func (c *Channel) pollIMAP(ctx context.Context, cfg *config.EmailSource, d mailbox.Dial, a mailbox.Auth,
	take func(ctx context.Context, raw []byte) (string, error)) error {
	conn, err := mailbox.DialIMAP(ctx, d)
	if err != nil {
		return err
	}
	defer conn.Logout()
	if err := conn.Login(ctx, a); err != nil {
		return err
	}
	for _, f := range []string{cfg.MoveTo, cfg.ErrorFolder} {
		if cfg.AfterRead == config.EmailAfterMove || f == cfg.ErrorFolder {
			if err := conn.Create(f); err != nil {
				return err
			}
		}
	}
	if err := conn.Select(cfg.Mailbox); err != nil {
		return err
	}
	uids, err := conn.Unseen()
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if ctx.Err() != nil {
			return nil
		}
		toError := func(why error) {
			c.log.Error("an email could not be handled; it was moved to the error folder", "uid", uid, "folder", cfg.ErrorFolder, "err", why)
			if err := conn.Move(uid, cfg.ErrorFolder); err != nil {
				c.log.Error("an email could not be moved to the error folder, so it will be read again", "uid", uid, "err", err)
			}
		}
		raw, err := conn.Fetch(uid, cfg.MaxMessageSize)
		if errors.Is(err, mailbox.ErrTooLarge) {
			toError(err)
			continue
		}
		if err != nil {
			return err
		}
		skip, err := take(ctx, raw)
		if err != nil {
			toError(err)
			continue
		}
		if skip != "" {
			c.log.Warn("an email was not taken and was moved to the error folder", "uid", uid, "why", skip, "folder", cfg.ErrorFolder)
			if err := conn.Move(uid, cfg.ErrorFolder); err != nil {
				return err
			}
			continue
		}
		switch cfg.AfterRead {
		case config.EmailAfterMove:
			err = conn.Move(uid, cfg.MoveTo)
		case config.EmailAfterFlag:
			err = conn.Flag(uid, `\Seen`)
		case config.EmailAfterDelete:
			err = conn.Delete(uid)
		}
		if err != nil {
			c.log.Error("an email was handled but could not be moved, flagged or deleted, so it will be read again", "uid", uid, "err", err)
			return err
		}
	}
	return nil
}

func (c *Channel) pollPOP3(ctx context.Context, cfg *config.EmailSource, d mailbox.Dial, a mailbox.Auth, left map[string]bool) error {
	conn, err := mailbox.DialPOP3(ctx, d)
	if err != nil {
		return err
	}
	if err := conn.Login(ctx, a); err != nil {
		conn.Close()
		return err
	}
	// Deletions are committed by QUIT, once, at the end; a session that breaks off deletes nothing, so a handled email may be
	// read again (POP3 offers no better), never lost.
	defer func() { _ = conn.Quit() }()
	list, err := conn.List()
	if err != nil {
		return err
	}
	for _, m := range list {
		if ctx.Err() != nil {
			return nil
		}
		if m.UIDL != "" && left[m.UIDL] {
			continue
		}
		leave := func() {
			if m.UIDL != "" {
				left[m.UIDL] = true
			}
		}
		if m.Size > cfg.MaxMessageSize {
			leave()
			c.log.Error("an email is larger than email.max_message_size and was left on the server", "number", m.Number, "size", m.Size)
			continue
		}
		raw, err := conn.Retr(m.Number, cfg.MaxMessageSize)
		if err != nil {
			return err
		}
		msgs, skip, err := emailMessages(cfg, raw)
		if err != nil {
			c.log.Error("an email could not be read and was left on the server", "number", m.Number, "err", err)
			leave()
			continue
		}
		if skip != "" {
			c.log.Warn("an email was not taken and was left on the server", "number", m.Number, "why", skip)
			leave()
			continue
		}
		if err := c.feedEmail(ctx, msgs); err != nil {
			c.log.Error("an email could not be handled and was left on the server", "number", m.Number, "err", err)
			leave()
			continue
		}
		if err := conn.Dele(m.Number); err != nil {
			return err
		}
	}
	return nil
}
