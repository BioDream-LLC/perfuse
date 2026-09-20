package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A fake mail server, because a test that needed a real one would depend on the network.
//
// It speaks just enough SMTP to get through a submission: greeting, EHLO with no extensions,
// then accept everything. STARTTLS is deliberately not advertised, which lets the tests check
// that a destination requiring it refuses rather than quietly continuing in the clear.

type fakeSMTP struct {
	// got is the whole conversation; data is only what arrived after DATA.
	//
	// The two are kept apart deliberately. Asserting "the bcc address is not in the
	// headers" against the whole transcript is wrong, because RCPT TO: carries that
	// address legitimately - that is the envelope, which is exactly where a blind
	// recipient belongs. My first version conflated them and reported a leak that was
	// not there.
	got        strings.Builder
	data       strings.Builder
	envelopeTo []string
	from       string
}

// headers returns the header block of the delivered message.
func (f *fakeSMTP) headers() string {
	body := f.data.String()
	if i := strings.Index(body, "\r\n\r\n"); i >= 0 {
		return body[:i]
	}
	return body
}

func (f *fakeSMTP) serve(t *testing.T) (net.Conn, func()) {
	t.Helper()

	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()

		w := bufio.NewWriter(server)
		r := bufio.NewReader(server)

		say := func(s string) {
			_, _ = w.WriteString(s + "\r\n")
			_ = w.Flush()
		}

		say("220 fake.example.org ESMTP")

		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f.got.WriteString(line)

			trimmed := strings.TrimRight(line, "\r\n")

			if inData {
				if trimmed == "." {
					inData = false
					say("250 OK")
					continue
				}
				f.data.WriteString(line)
				continue
			}

			upper := strings.ToUpper(trimmed)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				// No extensions advertised, so no STARTTLS and no AUTH.
				say("250 fake.example.org")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				f.from = trimmed
				say("250 OK")
			case strings.HasPrefix(upper, "RCPT TO:"):
				f.envelopeTo = append(f.envelopeTo, trimmed)
				say("250 OK")
			case upper == "DATA":
				inData = true
				say("354 send it")
			case upper == "QUIT":
				say("221 bye")
				return
			default:
				say("250 OK")
			}
		}
	}()

	return client, func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Log("the fake server did not finish")
		}
	}
}

// senderTo builds a sender wired to the fake server.
func senderTo(t *testing.T, f *fakeSMTP, cfg config.SMTPDestination) *SMTPSender {
	t.Helper()

	// STARTTLS off by default in these tests: the fake server does not offer it, and the
	// refusal is checked in its own test rather than getting in the way of every other one.
	if cfg.StartTLS == nil {
		off := false
		cfg.StartTLS = &off
	}
	if cfg.Host == "" {
		cfg.Host = "fake.example.org:587"
	}
	if cfg.From == "" {
		cfg.From = "perfuse@example.org"
	}
	if len(cfg.To) == 0 {
		cfg.To = []string{"someone@example.org"}
	}

	s, err := NewSMTPSender(config.Destination{Name: "mail", Type: config.DestinationSMTP, SMTP: &cfg})
	if err != nil {
		t.Fatal(err)
	}

	conn, cleanup := f.serve(t)
	t.Cleanup(cleanup)
	s.dialer = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
	return s
}

const smtpMsg = "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|CTRL999|P|2.5\rPID|1||123456||SMITH^JOHN\r"

func TestSMTPSendsAMessage(t *testing.T) {
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{Body: "a message arrived"})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}

	sent := f.got.String()
	if !strings.Contains(sent, "a message arrived") {
		t.Errorf("the body was not sent:\n%s", sent)
	}
	if !strings.Contains(sent, "From: perfuse@example.org") {
		t.Errorf("no From header:\n%s", sent)
	}
}

func TestSMTPKeepsBCCOutOfTheHeaders(t *testing.T) {
	// The list of people being told about a patient is not a list to leak. BCC recipients
	// go in the envelope and nowhere else.
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{
		To:   []string{"visible@example.org"},
		BCC:  []string{"hidden@example.org"},
		Body: "note",
	})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}

	sent := f.got.String()

	// It must reach the server as a recipient.
	envelope := strings.Join(f.envelopeTo, " ")
	if !strings.Contains(envelope, "hidden@example.org") {
		t.Errorf("the bcc recipient was not sent to the server: %q", envelope)
	}

	// And it must not appear anywhere in the delivered message.
	for _, line := range strings.Split(f.data.String(), "\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("a Bcc header was written: %q", line)
		}
	}
	if strings.Contains(f.headers(), "hidden@example.org") {
		t.Errorf("the bcc address appeared in the headers:\n%s", f.headers())
	}
	if strings.Contains(f.data.String(), "hidden@example.org") {
		t.Error("the bcc address appeared in the message body")
	}
	if !strings.Contains(f.headers(), "visible@example.org") {
		t.Error("the visible recipient is missing from the headers")
	}
	_ = sent
}

func TestSMTPRefusesAHeaderWithALineBreak(t *testing.T) {
	// A subject can carry field references, and a field carries whatever the sending system
	// put in it. A newline reaching a header lets the message add recipients or replace its
	// own body.
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{
		Subject: "ok\r\nBcc: attacker@example.org",
		Body:    "note",
	})

	err := s.Send(context.Background(), []byte(smtpMsg))
	if err == nil {
		t.Fatal("a subject containing a line break was accepted")
	}
	if !strings.Contains(err.Error(), "line break") {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(f.got.String(), "attacker@example.org") {
		t.Error("the injected header reached the server")
	}
}

func TestSMTPSubstitutesFields(t *testing.T) {
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{
		Subject: "Received {MSH-9.1} control {MSH-10}",
		Body:    "patient {PID-5.1}",
	})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}

	sent := f.got.String()
	if !strings.Contains(sent, "Received ADT control CTRL999") {
		t.Errorf("the subject was not substituted:\n%s", sent)
	}
	if !strings.Contains(sent, "patient SMITH") {
		t.Errorf("the body was not substituted:\n%s", sent)
	}
}

func TestSMTPLeavesAnUnresolvableFieldEmpty(t *testing.T) {
	// A notification is not worth failing a delivery over, and a subject with a gap in it
	// still tells somebody what happened.
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{Subject: "nothing here: {ZZZ-9}", Body: "note"})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatalf("an unresolvable field failed the send: %v", err)
	}
	if !strings.Contains(f.got.String(), "nothing here:") {
		t.Error("the subject was dropped entirely")
	}
}

func TestSMTPWritesAnUnclosedBraceThrough(t *testing.T) {
	// Guessing where the reference ended would silently change the text.
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{Subject: "a {broken reference", Body: "note"})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.got.String(), "a {broken reference") {
		t.Errorf("the literal text was not preserved:\n%s", f.got.String())
	}
}

func TestSMTPAttachesTheMessage(t *testing.T) {
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{Attach: true, Body: "see attached"})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}

	sent := f.got.String()
	if !strings.Contains(sent, "multipart/mixed") {
		t.Errorf("not multipart:\n%s", sent)
	}
	// Named for the control ID, which is what somebody asking about a message will quote.
	if !strings.Contains(sent, "CTRL999.hl7") {
		t.Errorf("the attachment was not named for the control id:\n%s", sent)
	}

	// The attachment must be the message, byte for byte.
	encoded := base64.StdEncoding.EncodeToString([]byte(smtpMsg))
	flat := strings.ReplaceAll(strings.ReplaceAll(sent, "\r\n", ""), "\n", "")
	if !strings.Contains(flat, encoded) {
		t.Error("the attached bytes are not the message")
	}
}

func TestSMTPNamesAnX12AttachmentCorrectly(t *testing.T) {
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{Attach: true, Body: "claims"})
	s.SetDataType(config.DataX12)

	if err := s.Send(context.Background(), []byte("ISA*00*")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.got.String(), ".x12") {
		t.Errorf("an X12 message was attached as something else:\n%s", f.got.String())
	}
}

func TestSMTPRefusesToSendWithoutTLSWhenItWasPromised(t *testing.T) {
	// A destination configured for TLS that quietly sends without it is worse than a
	// failure, because nothing anywhere records that the promise was broken. The fake
	// server does not advertise STARTTLS.
	f := &fakeSMTP{}
	on := true
	s := senderTo(t, f, config.SMTPDestination{Body: "note", StartTLS: &on})

	err := s.Send(context.Background(), []byte(smtpMsg))
	if err == nil {
		t.Fatal("the message was sent in the clear despite starttls being required")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("error = %v", err)
	}
}

func TestSMTPSanitisesTheAttachmentName(t *testing.T) {
	// A name derived from a message field is a name a sending system chose.
	f := &fakeSMTP{}
	s := senderTo(t, f, config.SMTPDestination{
		Attach:     true,
		AttachName: "../../etc/passwd",
		Body:       "note",
	})

	if err := s.Send(context.Background(), []byte(smtpMsg)); err != nil {
		t.Fatal(err)
	}
	sent := f.got.String()
	if strings.Contains(sent, "../") {
		t.Errorf("a traversal survived into the attachment name:\n%s", sent)
	}
}

func TestSMTPDefaultsThePort(t *testing.T) {
	// 587 is submission with STARTTLS, which is where a modern server expects a client.
	// Port 25 is server-to-server and usually either blocked or unauthenticated.
	cfg := config.SMTPDestination{Host: "mail.example.org"}
	if got := cfg.Address(); got != "mail.example.org:587" {
		t.Errorf("Address() = %q", got)
	}

	explicit := config.SMTPDestination{Host: "mail.example.org:2525"}
	if got := explicit.Address(); got != "mail.example.org:2525" {
		t.Errorf("Address() = %q", got)
	}
}

func TestSMTPRecipientsCoverEveryField(t *testing.T) {
	cfg := config.SMTPDestination{
		To:  []string{"a@example.org"},
		CC:  []string{"b@example.org"},
		BCC: []string{"c@example.org"},
	}
	got := strings.Join(cfg.Recipients(), ",")
	for _, want := range []string{"a@example.org", "b@example.org", "c@example.org"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is not a recipient: %q", want, got)
		}
	}
}
