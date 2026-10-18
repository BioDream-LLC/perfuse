package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// AS2Partner is a trading partner an AS2 source accepts messages from.
type AS2Partner struct {
	// ID is the partner's AS2 identifier, as it sends it in AS2-From.
	ID string `yaml:"id"`
	// CertFile is the partner's certificate (PEM), for its signatures. Often self-signed: AS2 partners exchange certificates
	// directly rather than through an authority.
	CertFile string `yaml:"cert_file"`
	// AllowUnsigned and AllowUnencrypted accept a message from this partner that is not signed, or not encrypted. Off: such a
	// message is refused with a receipt saying why.
	AllowUnsigned    bool `yaml:"allow_unsigned,omitempty"`
	AllowUnencrypted bool `yaml:"allow_unencrypted,omitempty"`
}

// AS2Source receives AS2 (RFC 4130): EDI posted over HTTP, signed and encrypted with S/MIME, answered with a receipt (an
// MDN) carrying a digest of what arrived. The payload is what the channel handles; X12 and EDIFACT are the usual ones.
type AS2Source struct {
	// Listen is the address to accept on, such as ":4080".
	Listen string `yaml:"listen"`
	// Path is the URL path partners post to. Default /as2.
	Path string `yaml:"path,omitempty"`
	// TLS serves HTTPS. Most partners require it in their agreement, on top of the S/MIME.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
	// ID is our AS2 identifier, which partners send in AS2-To. A message to any other is refused.
	ID string `yaml:"id"`
	// CertFile and KeyFile are our certificate and key (PEM): partners encrypt to the certificate, and receipts are signed
	// with the key.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// Partners are who may send here. A message from anyone else is refused.
	Partners []AS2Partner `yaml:"partners"`
	// MaxMessageSize bounds one request; default 50 MB.
	MaxMessageSize int64 `yaml:"max_message_size,omitempty"`
	// DuplicateWindow is how long a partner's Message-ID is remembered, so a message sent again (the partner never got our
	// receipt, and retried) is answered with a receipt and not passed on twice (RFC 4130 section 7.5). Default 30 days.
	DuplicateWindow time.Duration `yaml:"duplicate_window,omitempty"`
}

// DefaultAS2DuplicateWindow is how long Message-IDs are remembered when the source sets no window.
const DefaultAS2DuplicateWindow = 30 * 24 * time.Hour

// Duplicates is the duplicate window, with its default.
func (s *AS2Source) Duplicates() time.Duration {
	if s.DuplicateWindow <= 0 {
		return DefaultAS2DuplicateWindow
	}
	return s.DuplicateWindow
}

// AS2Path is the path, with its default.
func (s *AS2Source) AS2Path() string {
	if s.Path == "" {
		return "/as2"
	}
	return s.Path
}

func (s *AS2Source) validate() []error {
	if s == nil {
		return []error{fmt.Errorf("source type as2 needs an as2 block")}
	}
	var errs []error
	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, fmt.Errorf("source.as2.listen is required, such as \":4080\""))
	}
	if s.DuplicateWindow < 0 || (s.DuplicateWindow > 0 && s.DuplicateWindow < time.Hour) {
		errs = append(errs, fmt.Errorf("source.as2.duplicate_window must be an hour or more: partners retry for hours, and a shorter window passes their resends on twice"))
	}
	if strings.TrimSpace(s.ID) == "" {
		errs = append(errs, fmt.Errorf("source.as2.id is required: our AS2 identifier, which partners send in AS2-To"))
	}
	if s.CertFile == "" || s.KeyFile == "" {
		errs = append(errs, fmt.Errorf("source.as2 needs cert_file and key_file: partners encrypt to the certificate, and receipts are signed with the key"))
	}
	if len(s.Partners) == 0 {
		errs = append(errs, fmt.Errorf("source.as2.partners lists nobody, so every message would be refused"))
	}
	seen := map[string]bool{}
	for i, p := range s.Partners {
		if strings.TrimSpace(p.ID) == "" {
			errs = append(errs, fmt.Errorf("source.as2.partners[%d] needs an id", i))
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("source.as2.partners lists %q twice", p.ID))
		}
		seen[p.ID] = true
		if p.CertFile == "" && !p.AllowUnsigned {
			errs = append(errs, fmt.Errorf("source.as2.partners %q needs cert_file to check its signatures (or allow_unsigned)", p.ID))
		}
	}
	if s.Path != "" && !strings.HasPrefix(s.Path, "/") {
		errs = append(errs, fmt.Errorf("source.as2.path %q must start with /", s.Path))
	}
	return errs
}

// AS2Destination sends each message as AS2: signed with our key, encrypted to the partner's certificate, posted to the
// partner's URL, and, by default, delivered only once the partner's signed receipt names the same digest.
type AS2Destination struct {
	// URL is the partner's AS2 endpoint.
	URL string `yaml:"url"`
	// From is our AS2 identifier; To is the partner's.
	From string `yaml:"from"`
	To   string `yaml:"to"`
	// CertFile and KeyFile are ours (PEM), for signing.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	// PartnerCertFile is the partner's certificate (PEM): messages are encrypted to it and its receipts checked with it.
	PartnerCertFile string `yaml:"partner_cert_file,omitempty"`
	// Unsigned and Unencrypted turn off signing or encryption, for a partner whose agreement says so.
	Unsigned    bool `yaml:"unsigned,omitempty"`
	Unencrypted bool `yaml:"unencrypted,omitempty"`
	// Compress compresses the payload (zlib, RFC 5402) before signing and encryption. Every AS2 receiver that announces
	// version 1.1 or later reads it; ask the partner before turning it on.
	Compress bool `yaml:"compress,omitempty"`
	// Receipt is sync (the default: the receipt comes back in the response, and the message counts as delivered only when it
	// says processed with the right digest), async (posted later to receipt_url) or none.
	Receipt string `yaml:"receipt,omitempty"`
	// UnsignedReceipt asks for a receipt that is not signed. Off: a signed one is asked for, and an unsigned one refused.
	UnsignedReceipt bool `yaml:"unsigned_receipt,omitempty"`
	// ReceiptURL is where the partner posts an asynchronous receipt: an as2 source on this server.
	ReceiptURL string `yaml:"receipt_url,omitempty"`
	// ContentType is the payload's MIME type. Default by the channel's data type: application/edi-x12, application/edifact,
	// application/hl7-v2, otherwise application/octet-stream.
	ContentType string `yaml:"content_type,omitempty"`
	// FileName names the payload for the partner (${control_id}, ${timestamp}, ${date}).
	FileName string `yaml:"file_name,omitempty"`
	// Subject is the request's Subject header.
	Subject string `yaml:"subject,omitempty"`
	// Username and Password send HTTP basic authentication, which some gateways require besides the certificates.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// TLS for the HTTPS connection: a private authority, a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// ReceiptMode is Receipt with its default.
func (d *AS2Destination) ReceiptMode() string {
	if d.Receipt == "" {
		return "sync"
	}
	return d.Receipt
}

func (d *AS2Destination) validate(name string) []error {
	if d == nil {
		return []error{fmt.Errorf("destination %q is as2 but has no as2 block", name)}
	}
	var errs []error
	where := fmt.Sprintf("destination %q", name)
	if u, err := url.Parse(d.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("%s: as2.url %q is not an http or https URL", where, d.URL))
	}
	if strings.TrimSpace(d.From) == "" || strings.TrimSpace(d.To) == "" {
		errs = append(errs, fmt.Errorf("%s: as2 needs from (our AS2 identifier) and to (the partner's)", where))
	}
	if !d.Unsigned && (d.CertFile == "" || d.KeyFile == "") {
		errs = append(errs, fmt.Errorf("%s: as2 signs with cert_file and key_file; set both, or unsigned", where))
	}
	if !d.Unencrypted && d.PartnerCertFile == "" {
		errs = append(errs, fmt.Errorf("%s: as2 encrypts to partner_cert_file; set it, or unencrypted", where))
	}
	switch d.ReceiptMode() {
	case "sync", "none":
	case "async":
		if u, err := url.Parse(d.ReceiptURL); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: as2.receipt async needs receipt_url, where the partner posts the receipt", where))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: as2.receipt %q is not sync, async or none", where, d.Receipt))
	}
	if !d.UnsignedReceipt && d.ReceiptMode() != "none" && d.PartnerCertFile == "" {
		errs = append(errs, fmt.Errorf("%s: a signed receipt is checked with partner_cert_file; set it, or unsigned_receipt", where))
	}
	return errs
}
