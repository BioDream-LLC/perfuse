package engine

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Sending a message, or a note about one, as email.
//
// Standard library only: net/smtp is enough for submission with STARTTLS and PLAIN auth,
// which is what every mail server made this century accepts. Adding a dependency for this
// would be a poor trade against a project whose whole build is eight pure-Go packages.
//
// Two things are handled carefully because getting them wrong discloses information.
//
// BCC recipients are given to the server in the envelope and never written into a header.
// That is what blind means. In this setting the list is often who is being told about a
// patient, so leaking it is worse than the usual embarrassment.
//
// Headers are refused if they contain a newline. An attacker-controlled field reaching a
// header is header injection, and the consequence is arbitrary extra recipients or a forged
// body. Since a subject can carry field references, and a field can carry anything a sending
// system put in it, the value is checked rather than trusted.

// SMTPSender delivers by email.
type SMTPSender struct {
	cfg  config.SMTPDestination
	name string

	// dataType decides whether the attachment is named .hl7 or .x12, and is set through
	// the DataTypeAware interface after construction.
	dataType config.DataType

	// dialer is swapped in tests. There is no embedded mail server here, and pointing a
	// test at a real one would make the suite depend on the network.
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewSMTPSender builds an email sender.
func NewSMTPSender(d config.Destination) (*SMTPSender, error) {
	if d.SMTP == nil {
		return nil, errors.New("smtp destination has no smtp block")
	}
	return &SMTPSender{
		cfg:      *d.SMTP,
		name:     d.Name,
		dataType: config.DataHL7,
		dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}, nil
}

// SetDataType records the format, which names the attachment.
func (s *SMTPSender) SetDataType(t config.DataType) { s.dataType = t }

func (s *SMTPSender) Describe() string {
	return fmt.Sprintf("email to %s via %s", strings.Join(s.cfg.To, ", "), s.cfg.Address())
}

// Close releases resources. There are none: a connection is opened per message and closed
// when it is sent, because a mail server will drop an idle session anyway and a stale one
// fails on the next delivery rather than at the point it was lost.
func (s *SMTPSender) Close() error { return nil }

// Send delivers one message.
func (s *SMTPSender) Send(ctx context.Context, raw []byte) error {
	body, err := s.compose(raw)
	if err != nil {
		return err
	}

	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// The context bounds the dial as well as the timeout, so a shutdown does not wait for
	// an unresponsive mail server.
	conn, err := s.dialContext(ctx, timeout)
	if err != nil {
		return fmt.Errorf("smtp: connecting to %s: %w", s.cfg.Address(), err)
	}
	// A deadline on the connection bounds the whole conversation rather than each write.
	// Without it a server that accepts the connection and then stops talking holds the
	// destination open indefinitely, which for a queued destination stops the queue.
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer conn.Close()

	host := s.cfg.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	defer func() { _ = client.Quit() }()

	if s.cfg.UsesTLS() {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			// Reported rather than silently continuing in the clear. A destination
			// configured for TLS that quietly sends without it is worse than a failure,
			// because nothing anywhere records that the promise was broken.
			return errors.New("smtp: the server does not offer STARTTLS, and this destination " +
				"is configured to require it; set starttls: false only if the traffic genuinely " +
				"does not need protecting")
		}
		cfg := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: s.cfg.InsecureSkipVerify, //nolint:gosec // named plainly in config
			MinVersion:         tls.VersionTLS12,
		}
		if err := client.StartTLS(cfg); err != nil {
			return fmt.Errorf("smtp: starting TLS: %w", err)
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)
		if err := client.Auth(auth); err != nil {
			// The password is never in the error. A failed login logged with its
			// credentials is how one ends up in a ticket.
			return fmt.Errorf("smtp: authenticating as %s: %w", s.cfg.Username, err)
		}
	}

	if err := client.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp: sender %s refused: %w", s.cfg.From, err)
	}
	for _, to := range s.cfg.Recipients() {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp: recipient %s refused: %w", to, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp: writing the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: completing the message: %w", err)
	}

	return nil
}

// dialContext opens the connection, bounded by both the context and the timeout.
func (s *SMTPSender) dialContext(ctx context.Context, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.dialer(ctx, "tcp", s.cfg.Address())
}

// compose builds the whole email, headers included.
func (s *SMTPSender) compose(raw []byte) ([]byte, error) {
	subject := s.expand(s.cfg.Subject, raw)
	if strings.TrimSpace(subject) == "" {
		subject = "Message from Perfuse"
	}

	if err := headerSafe("subject", subject); err != nil {
		return nil, err
	}
	if err := headerSafe("from", s.cfg.From); err != nil {
		return nil, err
	}
	for _, to := range append(append([]string{}, s.cfg.To...), s.cfg.CC...) {
		if err := headerSafe("recipient", to); err != nil {
			return nil, err
		}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "From: %s\r\n", s.cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(s.cfg.To, ", "))
	if len(s.cfg.CC) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(s.cfg.CC, ", "))
	}
	// No Bcc header, deliberately. Those recipients are in the envelope only.

	// Encoded, so a subject carrying a name with an accent in it arrives readable rather
	// than as mojibake. mime.QEncoding leaves plain ASCII untouched.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")

	if !s.cfg.Attach {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(s.expand(s.cfg.Body, raw))
		b.WriteString("\r\n")
		return []byte(b.String()), nil
	}

	// A fixed boundary would be a bug the day a body happened to contain it. This is
	// derived from the clock and the recipient count, which is enough: it only has to be
	// absent from this one message.
	boundary := fmt.Sprintf("perfuse-%d-%d", time.Now().UnixNano(), len(s.cfg.To))

	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	body := s.expand(s.cfg.Body, raw)
	if strings.TrimSpace(body) == "" {
		body = "The message is attached."
	}
	b.WriteString(body)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: application/octet-stream; name=%q\r\n", s.attachName(raw))
	fmt.Fprintf(&b, "Content-Disposition: attachment; filename=%q\r\n", s.attachName(raw))
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrapBase64(raw))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)

	return []byte(b.String()), nil
}

// attachName names the attachment.
func (s *SMTPSender) attachName(raw []byte) string {
	if s.cfg.AttachName != "" {
		if name := s.expand(s.cfg.AttachName, raw); name != "" {
			return sanitiseFileName(name)
		}
	}

	ext := ".hl7"
	if s.dataType == config.DataX12 {
		ext = ".x12"
	}

	// The control ID, which is what somebody asking about a message will quote.
	if id := s.value(raw, "MSH-10"); id != "" {
		return sanitiseFileName(id) + ext
	}
	return "message" + ext
}

// expand substitutes field references in a template.
func (s *SMTPSender) expand(template string, raw []byte) string {
	if template == "" || !strings.Contains(template, "{") {
		return template
	}

	var out strings.Builder
	rest := template

	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			out.WriteString(rest)
			break
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			// An unclosed brace is written through rather than treated as a reference.
			// Guessing where it ended would silently change the text.
			out.WriteString(rest)
			break
		}
		close += open

		out.WriteString(rest[:open])
		path := strings.TrimSpace(rest[open+1 : close])
		out.WriteString(s.value(raw, path))
		rest = rest[close+1:]
	}

	return out.String()
}

// value resolves one path, or returns an empty string.
//
// A path that cannot be resolved yields nothing rather than an error. A notification is not
// worth failing a delivery over, and a subject line with a gap in it still tells somebody
// what happened.
func (s *SMTPSender) value(raw []byte, path string) string {
	if s.dataType != config.DataHL7 {
		return ""
	}
	v, err := transform.ValueAt(raw, path)
	if err != nil {
		return ""
	}
	return v
}

// headerSafe refuses a value that would inject a header.
//
// A subject can carry field references, and a field carries whatever a sending system put in
// it. A newline reaching a header is how somebody adds recipients or replaces the body, so
// the value is checked rather than trusted.
func headerSafe(what, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("smtp: the %s contains a line break, which would let the message "+
			"rewrite its own headers; it is refused rather than stripped, because a value "+
			"containing one is not the value anybody intended", what)
	}
	return nil
}

// The attachment name is run through the sftp sender's sanitiseFileName, which already
// strips path separators and "..". A name derived from MSH-10 is a name a sending system
// chose, so it gets the same treatment here as it does when it becomes a real file.

// wrapBase64 encodes at 76 characters a line, as MIME requires.
func wrapBase64(raw []byte) string {
	const width = 76
	encoded := base64.StdEncoding.EncodeToString(raw)

	var b strings.Builder
	for len(encoded) > width {
		b.WriteString(encoded[:width])
		b.WriteString("\r\n")
		encoded = encoded[width:]
	}
	b.WriteString(encoded)
	return b.String()
}
