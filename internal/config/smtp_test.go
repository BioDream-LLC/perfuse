package config

import (
	"strings"
	"testing"
)

func smtpChannel(t *testing.T, smtpBlock string) error {
	t.Helper()
	yaml := `name: mailer
source:
  type: mllp
  listen: 127.0.0.1:17001
destinations:
  - name: notify
    type: smtp
    smtp:
` + smtpBlock
	_, err := Load(strings.NewReader(yaml), "(test)")
	return err
}

func TestAnEmailDestinationLoads(t *testing.T) {
	err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [coordinator@example.org]
      subject: A message arrived
      body: See the interface for details.
`)
	if err != nil {
		t.Fatalf("a valid email destination was refused: %v", err)
	}
}

func TestAnEmailDestinationNeedsARecipient(t *testing.T) {
	err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      body: note
`)
	if err == nil {
		t.Fatal("an email destination with no recipient was accepted")
	}
	if !strings.Contains(err.Error(), "to") {
		t.Errorf("error = %v", err)
	}
}

func TestAnEmailDestinationNeedsASender(t *testing.T) {
	// A message with no envelope sender is discarded silently by a great deal of mail
	// infrastructure, which makes it the hardest possible failure to diagnose.
	err := smtpChannel(t, `      host: mail.example.org
      to: [someone@example.org]
      body: note
`)
	if err == nil {
		t.Fatal("an email destination with no from address was accepted")
	}
	if !strings.Contains(err.Error(), "from") {
		t.Errorf("error = %v", err)
	}
}

func TestEmailingAWholeMessageMustBeAskedForExplicitly(t *testing.T) {
	// The most important check on this destination. Email is stored and forwarded by
	// systems outside anybody's control, so sending a whole clinical message as the body
	// is a disclosure rather than a delivery - and it must not be what happens when a
	// field is left blank.
	err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [someone@example.org]
`)
	if err == nil {
		t.Fatal("a destination that would email the whole message body was accepted silently")
	}
	if !strings.Contains(err.Error(), "disclosure") {
		t.Errorf("the error did not explain why: %v", err)
	}

	// Either resolution is accepted: a summary body, or an explicit attachment.
	if err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [someone@example.org]
      body: a message arrived
`); err != nil {
		t.Errorf("a summary body was refused: %v", err)
	}

	if err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [someone@example.org]
      attach: true
`); err != nil {
		t.Errorf("an explicit attachment was refused: %v", err)
	}
}

func TestAPasswordIsNotSentInTheClear(t *testing.T) {
	// Refused rather than warned about. Sending a password over an unencrypted connection
	// is not a trade-off worth offering.
	err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [someone@example.org]
      body: note
      username: perfuse
      password: hunter2
      starttls: false
`)
	if err == nil {
		t.Fatal("a password over an unencrypted connection was accepted")
	}
	if !strings.Contains(err.Error(), "clear") {
		t.Errorf("error = %v", err)
	}
}

func TestHalfCredentialsAreRefused(t *testing.T) {
	for _, block := range []string{
		"      username: perfuse\n",
		"      password: hunter2\n",
	} {
		err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [someone@example.org]
      body: note
`+block)
		if err == nil {
			t.Errorf("half a credential pair was accepted: %q", block)
		}
	}
}

func TestAnAddressThatIsNotAnAddressIsRefused(t *testing.T) {
	err := smtpChannel(t, `      host: mail.example.org
      from: perfuse@example.org
      to: [not-an-address]
      body: note
`)
	if err == nil {
		t.Fatal("a recipient with no @ was accepted")
	}
}

func TestAnSMTPBlockOnlyAppliesToAnEmailDestination(t *testing.T) {
	// The invariant: a block that does nothing is refused rather than ignored, because a
	// setting sitting in a file that never takes effect is read as if it does.
	yaml := `name: mailer
source:
  type: mllp
  listen: 127.0.0.1:17002
destinations:
  - name: archive
    type: file
    dir: /tmp/mailer
    smtp:
      host: mail.example.org
      from: a@example.org
      to: [b@example.org]
      body: note
`
	_, err := Load(strings.NewReader(yaml), "(test)")
	if err == nil {
		t.Fatal("an smtp block on a file destination was ignored rather than refused")
	}
	if !strings.Contains(err.Error(), "smtp block") {
		t.Errorf("error = %v", err)
	}
}

func TestEmailDefaultsToEncrypted(t *testing.T) {
	cfg := SMTPDestination{Host: "mail.example.org"}
	if !cfg.UsesTLS() {
		t.Error("STARTTLS is not on by default")
	}

	off := false
	cfg.StartTLS = &off
	if cfg.UsesTLS() {
		t.Error("STARTTLS could not be turned off")
	}
}
