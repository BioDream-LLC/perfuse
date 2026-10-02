package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sending a message as email.
//
// Mirth has an email sender and this did not, which made it the cheapest real gap to close.
// It is also the one destination whose purpose is usually not integration: almost nobody
// routes clinical data by email, and almost everybody wants an alert when a particular
// message arrives. A daily report that a feed produced no results, a note to a coordinator
// when a specific order type appears - those are email, and doing them today means an HTTP
// destination pointed at something else that can send mail.
//
// Which is why the safety wording below matters more here than in any other destination.

// SMTPDestination configures delivery by email.
type SMTPDestination struct {
	// Host is the mail server, with an optional port. Port 587 is assumed, which is
	// submission with STARTTLS - the port a modern server expects a client on. Port 25 is
	// server-to-server and is usually either blocked or unauthenticated.
	Host string `yaml:"host"`

	// From is the envelope sender. Required, because a message with no sender is
	// discarded silently by a great deal of mail infrastructure, which makes it the
	// hardest possible failure to diagnose.
	From string `yaml:"from"`

	// To, CC and BCC are recipients. At least one To is required.
	To  []string `yaml:"to"`
	CC  []string `yaml:"cc,omitempty"`
	BCC []string `yaml:"bcc,omitempty"`

	// Subject may contain field references in the same notation as everywhere else, so a
	// subject can name the message it is about.
	Subject string `yaml:"subject,omitempty"`

	// Body is the message text. Field references are substituted, as in Subject. When it
	// is empty the message itself is the body.
	Body string `yaml:"body,omitempty"`

	// Attach sends the message as an attachment rather than in the body, which is what
	// anybody forwarding a message for a human to look at actually wants.
	Attach bool `yaml:"attach,omitempty"`

	// AttachName names the attachment. Defaults to the control ID with a .hl7 suffix.
	AttachName string `yaml:"attach_name,omitempty"`

	// Username and Password authenticate to the server. Both or neither.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// StartTLS upgrades the connection before authenticating. It defaults to on, and
	// turning it off with a username set is refused rather than allowed: sending a
	// password over an unencrypted connection is not a trade-off worth offering.
	StartTLS *bool `yaml:"starttls,omitempty"`

	// InsecureSkipVerify accepts any certificate. Named unambiguously because that is
	// what it does.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// Timeout bounds the whole conversation with the server.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// UsesTLS reports whether the connection is upgraded before authenticating.
func (s *SMTPDestination) UsesTLS() bool {
	return s.StartTLS == nil || *s.StartTLS
}

// Port returns the port to dial, defaulting to submission.
func (s *SMTPDestination) Port() string {
	if i := strings.LastIndex(s.Host, ":"); i >= 0 {
		return s.Host[i+1:]
	}
	return "587"
}

// Address returns host:port, adding the default port when none was given.
func (s *SMTPDestination) Address() string {
	if strings.Contains(s.Host, ":") {
		return s.Host
	}
	return s.Host + ":587"
}

// Recipients returns every address the message is sent to.
//
// BCC recipients are included here and deliberately not written into a header. That is what
// blind means, and getting it wrong discloses a list of addresses - which in this setting
// can be a list of who is being told about a patient.
func (s *SMTPDestination) Recipients() []string {
	out := make([]string, 0, len(s.To)+len(s.CC)+len(s.BCC))
	out = append(out, s.To...)
	out = append(out, s.CC...)
	out = append(out, s.BCC...)
	return out
}

// validateSMTP checks an email destination.
func (d *Destination) validateSMTP() []error {
	s := d.SMTP
	if s == nil {
		return []error{errors.New(
			"an smtp destination needs an smtp block with a host, a from address and at least one recipient")}
	}

	var problems []error

	if strings.TrimSpace(s.Host) == "" {
		problems = append(problems, errors.New("smtp.host is required"))
	}
	if strings.TrimSpace(s.From) == "" {
		problems = append(problems, errors.New(
			"smtp.from is required: a message with no sender is discarded silently by a great deal of "+
				"mail infrastructure, which makes it the hardest kind of failure to find"))
	}
	if len(s.To) == 0 {
		problems = append(problems, errors.New("smtp needs at least one address in to"))
	}

	for _, addr := range s.Recipients() {
		if !strings.Contains(addr, "@") {
			problems = append(problems, fmt.Errorf("smtp recipient %q does not look like an email address", addr))
		}
	}
	if s.From != "" && !strings.Contains(s.From, "@") {
		problems = append(problems, fmt.Errorf("smtp.from %q does not look like an email address", s.From))
	}

	// A password sent in the clear is not a trade-off worth offering, so this is refused
	// rather than warned about.
	if s.Username != "" && !s.UsesTLS() {
		problems = append(problems, errors.New(
			"smtp has a username but starttls is off, which would send the password in the clear: "+
				"either leave starttls on, or remove the username if the server genuinely wants no authentication"))
	}
	if s.Username != "" && s.Password == "" {
		problems = append(problems, errors.New("smtp.username is set but smtp.password is empty"))
	}
	if s.Password != "" && s.Username == "" {
		problems = append(problems, errors.New("smtp.password is set but smtp.username is empty"))
	}

	// The most important check here. Email is not a clinical transport: it is very often
	// unencrypted between servers, it is retained indefinitely in places nobody audits, and
	// it is delivered to whatever address was typed. Sending a whole message body by email
	// is a disclosure, so it has to be asked for explicitly rather than arrived at by
	// leaving a field blank.
	if !s.Attach && strings.TrimSpace(s.Body) == "" {
		problems = append(problems, errors.New(
			"smtp would email the whole message as the body: set body to a summary, or set attach to true "+
				"to send the message as an attachment, and say so deliberately - email is stored and forwarded "+
				"by systems outside your control, so a message body is a disclosure rather than a delivery"))
	}

	return problems
}
