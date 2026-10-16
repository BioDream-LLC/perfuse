package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// What an email source takes from each message.
const (
	EmailTakeAttachments = "attachments"
	EmailTakeBody        = "body"
	EmailTakeRaw         = "raw"
)

// After an email is handled.
const (
	EmailAfterMove   = "move"
	EmailAfterFlag   = "flag"
	EmailAfterDelete = "delete"
)

// EmailOAuth signs in with OAuth 2.0 (SASL XOAUTH2), which Microsoft 365 and Gmail require in place of a password.
type EmailOAuth struct {
	// Provider is microsoft, google or custom (with token_url).
	Provider string `yaml:"provider"`
	// Tenant is the Microsoft Entra tenant id or domain; organizations when empty.
	Tenant string `yaml:"tenant,omitempty"`
	// TokenURL is the token endpoint for a custom provider (https); the provider's own is used otherwise.
	TokenURL string `yaml:"token_url,omitempty"`
	// ClientID is the app registration's client (application) id.
	ClientID string `yaml:"client_id"`
	// ClientSecret may be ${ENV}. With no refresh_token it selects Microsoft's app-only client-credentials grant.
	ClientSecret string `yaml:"client_secret,omitempty"`
	// RefreshToken may be ${ENV}: a mailbox a person consented to. Required for Gmail.
	RefreshToken string `yaml:"refresh_token,omitempty"`
	// Scope overrides the provider's default for the protocol.
	Scope string `yaml:"scope,omitempty"`
}

// EmailSource reads mail from a mailbox and turns attachments, bodies or whole messages into messages.
type EmailSource struct {
	// Protocol is imap (the default) or pop3.
	Protocol string `yaml:"protocol,omitempty"`
	// Host is the mail server, such as outlook.office365.com, imap.gmail.com or pop.gmail.com.
	Host string `yaml:"host"`
	// Port defaults to 993 for IMAP and 995 for POP3 over TLS, 143 and 110 otherwise.
	Port int `yaml:"port,omitempty"`
	// Security is tls (the default), starttls, or none (only to this machine).
	Security string `yaml:"security,omitempty"`
	// TLS adds a CA file or a client certificate to the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
	// Username is the mailbox to sign in as, usually its address.
	Username string `yaml:"username"`
	// Password may be ${ENV}; for Gmail an app password. Ignored with oauth.
	Password string `yaml:"password,omitempty"`
	// OAuth signs in with OAuth 2.0 (XOAUTH2) instead of the password: required by Microsoft 365, preferred by Gmail.
	OAuth *EmailOAuth `yaml:"oauth,omitempty"`
	// Mailbox is the IMAP folder read; INBOX by default.
	Mailbox string `yaml:"mailbox,omitempty"`
	// Take is attachments (each one a message, the default), body (the text body) or raw (the whole message).
	Take string `yaml:"take,omitempty"`
	// AttachmentNames limits which attachments are taken, by file name pattern (*.hl7). Empty takes every attachment.
	AttachmentNames []string `yaml:"attachment_names,omitempty"`
	// AllowedSenders limits whose mail is read: addresses and domains. The From header can be forged, so this keeps out
	// accidents and casual mail, not an attacker.
	AllowedSenders []string `yaml:"allowed_senders,omitempty"`
	// AfterRead is move (to move_to, the default for IMAP), flag (mark it read) or delete (the only choice for POP3).
	AfterRead string `yaml:"after_read,omitempty"`
	// MoveTo is the folder handled mail moves to; Processed by default. Created when missing.
	MoveTo string `yaml:"move_to,omitempty"`
	// ErrorFolder is where IMAP mail that could not be handled, or that was not taken, moves; Error by default.
	ErrorFolder string `yaml:"error_folder,omitempty"`
	// PollInterval between checks; 60s by default, at least 10s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
	// MaxMessageSize refuses a larger email; 25 MiB by default.
	MaxMessageSize int64 `yaml:"max_message_size,omitempty"`
}

// ResolvedPassword is the password with an environment reference expanded.
func (e *EmailSource) ResolvedPassword() string { return resolveSecret(e.Password) }

// ResolvedOAuth is the OAuth settings with environment references expanded.
func (e *EmailSource) ResolvedOAuth() *EmailOAuth {
	if e.OAuth == nil {
		return nil
	}
	o := *e.OAuth
	o.ClientSecret, o.RefreshToken = resolveSecret(o.ClientSecret), resolveSecret(o.RefreshToken)
	return &o
}

// Validate checks the settings and applies defaults.
func (e *EmailSource) Validate() []error {
	var errs []error
	e.Protocol = strings.ToLower(strings.TrimSpace(e.Protocol))
	if e.Protocol == "" {
		e.Protocol = "imap"
	}
	if e.Protocol != "imap" && e.Protocol != "pop3" {
		errs = append(errs, fmt.Errorf("email.protocol %q is not valid; use imap or pop3", e.Protocol))
	}
	if strings.TrimSpace(e.Host) == "" {
		errs = append(errs, errors.New("email.host is required, e.g. outlook.office365.com or imap.gmail.com"))
	}
	if e.Security == "" {
		e.Security = "tls"
	}
	switch e.Security {
	case "tls", "starttls":
	case "none":
		if h := strings.ToLower(e.Host); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			errs = append(errs, errors.New("email.security none sends the password in clear; it is allowed only to this machine (localhost)"))
		}
	default:
		errs = append(errs, fmt.Errorf("email.security %q is not valid; use tls, starttls or none", e.Security))
	}
	if e.Port == 0 {
		switch {
		case e.Protocol == "pop3" && e.Security == "tls":
			e.Port = 995
		case e.Protocol == "pop3":
			e.Port = 110
		case e.Security == "tls":
			e.Port = 993
		default:
			e.Port = 143
		}
	}
	if e.Port < 1 || e.Port > 65535 {
		errs = append(errs, fmt.Errorf("email.port %d is not a port", e.Port))
	}
	if e.TLS != nil {
		errs = append(errs, e.TLS.Validate(false)...)
	}
	if strings.TrimSpace(e.Username) == "" {
		errs = append(errs, errors.New("email.username is required (the mailbox's address, usually)"))
	}
	if e.OAuth != nil {
		o := e.OAuth
		switch o.Provider {
		case "microsoft", "google", "custom":
		default:
			errs = append(errs, fmt.Errorf("email.oauth.provider %q is not valid; use microsoft, google or custom", o.Provider))
		}
		if o.ClientID == "" {
			errs = append(errs, errors.New("email.oauth.client_id is required"))
		}
		if o.Provider == "custom" && o.TokenURL == "" {
			errs = append(errs, errors.New("email.oauth.token_url is required for a custom provider"))
		}
		if o.RefreshToken == "" && o.ClientSecret == "" {
			errs = append(errs, errors.New("email.oauth needs refresh_token (a person's consent) or client_secret (Microsoft 365 app-only access)"))
		}
		if o.Provider == "google" && o.RefreshToken == "" {
			errs = append(errs, errors.New("email.oauth for Gmail needs refresh_token: Google offers no app-only mailbox access"))
		}
		if o.TokenURL != "" && !strings.HasPrefix(o.TokenURL, "https://") && !strings.HasPrefix(o.TokenURL, "http://127.0.0.1") &&
			!strings.HasPrefix(o.TokenURL, "http://localhost") {
			errs = append(errs, errors.New("email.oauth.token_url must be https"))
		}
	} else if e.Password == "" {
		errs = append(errs, errors.New("email needs password (${ENV}, or an app password for Gmail) or oauth"))
	}
	if e.Mailbox == "" {
		e.Mailbox = "INBOX"
	}
	if e.Take == "" {
		e.Take = EmailTakeAttachments
	}
	switch e.Take {
	case EmailTakeAttachments, EmailTakeBody, EmailTakeRaw:
	default:
		errs = append(errs, fmt.Errorf("email.take %q is not valid; use attachments, body or raw", e.Take))
	}
	for _, p := range e.AttachmentNames {
		if _, err := path.Match(strings.ToLower(p), "x"); err != nil {
			errs = append(errs, fmt.Errorf("email.attachment_names %q is not a valid pattern", p))
		}
	}
	if e.AfterRead == "" {
		if e.Protocol == "pop3" {
			e.AfterRead = EmailAfterDelete
		} else {
			e.AfterRead = EmailAfterMove
		}
	}
	switch e.AfterRead {
	case EmailAfterMove, EmailAfterFlag, EmailAfterDelete:
	default:
		errs = append(errs, fmt.Errorf("email.after_read %q is not valid; use move, flag or delete", e.AfterRead))
	}
	if e.Protocol == "pop3" && e.AfterRead != EmailAfterDelete {
		errs = append(errs, errors.New("email.after_read must be delete for POP3: POP3 has no folders or flags, so mail left on the server would be read again"))
	}
	if e.MoveTo == "" {
		e.MoveTo = "Processed"
	}
	if e.ErrorFolder == "" {
		e.ErrorFolder = "Error"
	}
	if e.Protocol == "imap" && (strings.EqualFold(e.MoveTo, e.Mailbox) || strings.EqualFold(e.ErrorFolder, e.Mailbox)) {
		errs = append(errs, errors.New("email.move_to and error_folder must differ from mailbox, or mail would be read again"))
	}
	if e.PollInterval == 0 {
		e.PollInterval = 60 * time.Second
	}
	if e.PollInterval < 10*time.Second {
		errs = append(errs, errors.New("email.poll_interval must be at least 10s: mail servers throttle accounts that poll faster"))
	}
	if e.MaxMessageSize == 0 {
		e.MaxMessageSize = 25 << 20
	}
	if e.MaxMessageSize < 1024 {
		errs = append(errs, errors.New("email.max_message_size must be at least 1024 bytes"))
	}
	return errs
}

// Warnings reports what is worth saying at every start.
func (e *EmailSource) Warnings() []string {
	var out []string
	if e.Password != "" && !strings.HasPrefix(strings.TrimSpace(e.Password), "${") {
		out = append(out, "email.password is written in the channel file; channels go into version control, so prefer ${ENV_VAR}")
	}
	if len(e.AllowedSenders) == 0 {
		out = append(out, "email.allowed_senders is empty, so mail from anyone becomes messages; list the senders you expect")
	}
	return out
}

// AttachmentWanted reports whether an attachment's name matches attachment_names (case-insensitive).
func (e *EmailSource) AttachmentWanted(name string) bool {
	if len(e.AttachmentNames) == 0 {
		return true
	}
	n := strings.ToLower(name)
	for _, p := range e.AttachmentNames {
		if ok, _ := path.Match(strings.ToLower(p), n); ok {
			return true
		}
	}
	return false
}
