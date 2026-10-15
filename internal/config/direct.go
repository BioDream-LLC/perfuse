package config

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// DirectAddress is a Direct address this server holds: its certificate and key, and the anchors it trusts. The same block is
// used to send (sign as this address) and to receive (decrypt for it).
type DirectAddress struct {
	// Address is the Direct address, such as referrals@direct.hospital.example.
	Address string `yaml:"address"`
	// CertFile and KeyFile are the address's certificate (PEM, with its chain after it) and private key (PEM). The HISP or its
	// registration authority issues them; they are the address's identity.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// AnchorsFile is the trust anchors (PEM): the certificate authorities whose senders and recipients this address trusts,
	// such as the DirectTrust bundle. A message signed by anything else is refused.
	AnchorsFile string `yaml:"anchors_file"`
}

func (a *DirectAddress) validate(where string) []error {
	if a == nil {
		return []error{fmt.Errorf("%s needs a direct block", where)}
	}
	var errs []error
	if _, err := mail.ParseAddress(a.Address); err != nil || !strings.Contains(a.Address, "@") {
		errs = append(errs, fmt.Errorf("%s: direct.address %q is not an address", where, a.Address))
	}
	for _, f := range []struct{ name, v string }{{"cert_file", a.CertFile}, {"key_file", a.KeyFile}, {"anchors_file", a.AnchorsFile}} {
		if strings.TrimSpace(f.v) == "" {
			errs = append(errs, fmt.Errorf("%s needs direct.%s", where, f.name))
		}
	}
	return errs
}

// DirectDestination sends each message as Direct: signed as this address, encrypted to each recipient's certificate, and
// submitted to the HISP by SMTP.
type DirectDestination struct {
	DirectAddress `yaml:",inline"`
	// To are the recipients' Direct addresses. Field references are substituted, so a recipient can come from the message.
	To []string `yaml:"to"`
	// Subject and Text go in the message; field references are substituted.
	Subject string `yaml:"subject,omitempty"`
	Text    string `yaml:"text,omitempty"`
	// AttachName names the attachment carrying the message, such as summary.xml; default by data type.
	AttachName string `yaml:"attach_name,omitempty"`
	// XDM packages a CDA document as IHE XDM (a ZIP with its XDS metadata), which many receiving EHRs import directly.
	XDM bool `yaml:"xdm,omitempty"`
	// XDMSourceID is the sending organisation's OID for the XDM metadata (sourceId); needed when xdm is on.
	XDMSourceID string `yaml:"xdm_source_id,omitempty"`
	// RecipientCertsDir holds recipient certificates (PEM, one file per address or domain, address.pem or domain.pem) found
	// in advance; empty reads the server's Direct address book (the Direct screen). Recipients not there are looked up in DNS
	// (CERT records). Direct does not send to a recipient whose certificate cannot be found.
	RecipientCertsDir string `yaml:"recipient_certs_dir,omitempty"`
	// DNS is the resolver for certificate lookup (host:port); empty uses the system's.
	DNS string `yaml:"dns,omitempty"`
	// SMTPHost is the HISP's submission server (host:port, 587 by default); STARTTLS is required.
	SMTPHost string `yaml:"smtp_host"`
	// Username is the account at the HISP's submission server, when it asks for one.
	Username string `yaml:"username,omitempty"`
	// Password is that account's password; use ${ENV} rather than writing it here.
	Password string `yaml:"password,omitempty"`
	// RequestMDN asks the recipient for a delivery notification.
	RequestMDN bool `yaml:"request_mdn,omitempty"`
	// Timeout bounds one submission to the HISP; a minute by default.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

func validateDirectDest(d *Destination) []error {
	s := d.Direct
	where := fmt.Sprintf("destination %q", d.Name)
	if s == nil {
		return []error{fmt.Errorf("%s is a direct destination but has no direct block", where)}
	}
	errs := s.DirectAddress.validate(where)
	if len(s.To) == 0 {
		errs = append(errs, fmt.Errorf("%s needs direct.to, the recipients' Direct addresses", where))
	}
	if strings.TrimSpace(s.SMTPHost) == "" {
		errs = append(errs, fmt.Errorf("%s needs direct.smtp_host, the HISP's SMTP submission server", where))
	}
	if s.XDM && strings.TrimSpace(s.XDMSourceID) == "" {
		errs = append(errs, fmt.Errorf("%s packages XDM, which needs direct.xdm_source_id, the sending organisation's OID", where))
	}
	if (s.Username == "") != (s.Password == "") {
		errs = append(errs, fmt.Errorf("%s has half a credential; set both direct.username and direct.password", where))
	}
	if s.Timeout == 0 {
		s.Timeout = time.Minute
	}
	return errs
}

// DirectSource receives Direct mail the HISP delivers: an SMTP listener for this address's mail. Each message is decrypted,
// its signature checked against the anchors and its signer against From, and its content passed on: an XDM package's
// documents, else each attachment, else the text.
type DirectSource struct {
	DirectAddress `yaml:",inline"`
	// Listen is the SMTP address the HISP delivers to, such as 0.0.0.0:2525.
	Listen string `yaml:"listen"`
	// TLSCert and TLSKey, when set, offer STARTTLS, and it is then required before mail is taken.
	TLSCert string `yaml:"tls_cert,omitempty"`
	TLSKey  string `yaml:"tls_key,omitempty"`
	// MaxBytes bounds one message; 64 MB by default.
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// SMTPHost, Username and Password are the HISP's submission server, used to send a "processed" delivery notification
	// (MDN) to the sender once a message has been taken in. Without it, no notification is sent.
	SMTPHost string `yaml:"smtp_host,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

func (s *DirectSource) validate() []error {
	if s == nil {
		return []error{errors.New("source type direct needs a direct block")}
	}
	errs := s.DirectAddress.validate("source")
	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, errors.New("source needs direct.listen, the SMTP address the HISP delivers to"))
	}
	if (s.TLSCert == "") != (s.TLSKey == "") {
		errs = append(errs, errors.New("source has half a TLS pair; set both direct.tls_cert and direct.tls_key"))
	}
	return errs
}
