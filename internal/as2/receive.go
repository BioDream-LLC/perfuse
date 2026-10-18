package as2

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
)

// Failure is why a message was not accepted, as the receipt names it (RFC 4130 7.4.3).
type Failure struct {
	// Kind is the disposition modifier: decryption-failed, authentication-failed, integrity-check-failed,
	// insufficient-message-security, unexpected-processing-error.
	Kind string
	Err  error
}

func (f *Failure) Error() string { return "as2: " + f.Kind + ": " + f.Err.Error() }
func (f *Failure) Unwrap() error { return f.Err }

func fail(kind string, format string, args ...any) *Failure {
	return &Failure{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// ReceiveOptions says what an incoming message needs.
type ReceiveOptions struct {
	// Keys are ours, for decrypting.
	Keys []cms.KeyPair
	// Partner holds the sender's certificate (and any authority it chains to), for its signature.
	Partner *x509.CertPool
	// RequireSigned and RequireEncrypted refuse a message that is not.
	RequireSigned, RequireEncrypted bool
	// MaxExpanded bounds what a compressed message may expand to; zero means 1 GiB.
	MaxExpanded int64
}

// Received is one message, opened.
type Received struct {
	From, To, MessageID string
	Payload             []byte
	ContentType         string
	FileName            string
	Signed, Encrypted   bool
	// Compressed reports that the message, or its signed content, was compressed (RFC 5402).
	Compressed bool
	// SignedBy is the certificate subject of the signer.
	SignedBy string
	// MIC is what the receipt returns.
	MIC string

	// What the sender asked for.
	MDN       MDNMode
	SignedMDN bool
	AsyncURL  string
	MICAlg    string
}

// Requested reads what receipt a message asks for, from its headers alone, so a refusal can still be answered.
func Requested(h http.Header) (mode MDNMode, signed bool, asyncURL, micalg string) {
	mode = MDNNone
	if h.Get("Disposition-Notification-To") != "" {
		mode = MDNSync
		if u := strings.TrimSpace(h.Get("Receipt-Delivery-Option")); u != "" {
			mode, asyncURL = MDNAsync, u
		}
	}
	opts := strings.ToLower(h.Get("Disposition-Notification-Options"))
	signed = strings.Contains(opts, "pkcs7-signature")
	micalg = "sha-256"
	if i := strings.Index(opts, "signed-receipt-micalg"); i >= 0 {
		rest := opts[i:]
		if j := strings.Index(rest, "="); j >= 0 {
			for _, a := range strings.Split(rest[j+1:], ",") {
				a = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(a), ";"))
				if a == "sha-256" || a == "sha256" {
					micalg = "sha-256"
					break
				}
				if a == "sha1" || a == "sha-1" {
					micalg = "sha1"
				}
			}
		}
	}
	return mode, signed, asyncURL, micalg
}

// Receive opens one AS2 request: decrypts, verifies, and computes the MIC. A *Failure names the receipt's error.
func Receive(h http.Header, body []byte, o ReceiveOptions) (*Received, error) {
	r := &Received{From: Unquote(h.Get("AS2-From")), To: Unquote(h.Get("AS2-To")), MessageID: strings.TrimSpace(h.Get("Message-ID"))}
	r.MDN, r.SignedMDN, r.AsyncURL, r.MICAlg = Requested(h)
	ct := h.Get("Content-Type")
	if ct == "" {
		return r, fail("unexpected-processing-error", "the request has no Content-Type")
	}
	if enc := h.Get("Content-Transfer-Encoding"); strings.EqualFold(strings.TrimSpace(enc), "base64") {
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), "")); err == nil {
			body = d
		}
	}

	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return r, fail("unexpected-processing-error", "Content-Type %q cannot be read", ct)
	}
	// The entity as sent, its MIME headers having travelled as HTTP headers: in the order Build writes them, so the MIC of an
	// unsigned message matches.
	head := "Content-Type: " + ct + "\r\n"
	if v := h.Get("Content-Transfer-Encoding"); v != "" && !strings.EqualFold(strings.TrimSpace(v), "base64") {
		head += "Content-Transfer-Encoding: " + v + "\r\n"
	}
	if v := h.Get("Content-Disposition"); v != "" {
		head += "Content-Disposition: " + v + "\r\n"
	}
	entity := append([]byte(head+"\r\n"), body...)

	if isPKCS7(mt) && !isCompressed(params, body) {
		der := cmsBytes(body)
		inner, err := cms.Decrypt(der, o.Keys)
		if err != nil {
			return r, fail("decryption-failed", "%v", err)
		}
		r.Encrypted = true
		entity = inner
		hdr, ib, err := splitEntity(inner)
		if err != nil {
			return r, fail("decryption-failed", "what decrypted is not a MIME entity: %v", err)
		}
		if mt, params, err = mime.ParseMediaType(hdr.Get("Content-Type")); err != nil {
			return r, fail("unexpected-processing-error", "the decrypted entity has no readable Content-Type")
		}
		body = ib
		if strings.EqualFold(strings.TrimSpace(hdr.Get("Content-Transfer-Encoding")), "base64") {
			if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), "")); err == nil {
				body = d
			}
		}
	}
	// Compressed before it was signed or encrypted (RFC 5402 sections 3.1, 3.2, 3.4, 3.5), or a signed message compressed
	// whole (3.3, 3.6): either way it is opened here, and what it held is read on as if it had arrived so.
	if isPKCS7(mt) && isCompressed(params, body) {
		inner, err := cms.Decompress(cmsBytes(body), o.MaxExpanded)
		if err != nil {
			return r, fail("decompression-failed", "%v", err)
		}
		r.Compressed = true
		entity = inner
		hdr, ib, err := splitEntity(inner)
		if err != nil {
			return r, fail("decompression-failed", "what decompressed is not a MIME entity: %v", err)
		}
		if mt, params, err = mime.ParseMediaType(hdr.Get("Content-Type")); err != nil {
			return r, fail("unexpected-processing-error", "the decompressed entity has no readable Content-Type")
		}
		body = ib
	}
	if o.RequireEncrypted && !r.Encrypted {
		return r, fail("insufficient-message-security", "the message is not encrypted, and this partner must encrypt")
	}

	payloadEntity := entity
	if mt == "multipart/signed" {
		content, sig, err := splitSigned(body, params["boundary"])
		if err != nil {
			return r, fail("integrity-check-failed", "%v", err)
		}
		signer, err := cms.Verify(sig, content, o.Partner, time.Now())
		if err != nil {
			// A signature over content with bare LF line ends, where the signer canonicalised to CRLF first.
			if alt := toCRLF(content); !bytes.Equal(alt, content) {
				if s2, err2 := cms.Verify(sig, alt, o.Partner, time.Now()); err2 == nil {
					signer, err, content = s2, nil, alt
				}
			}
		}
		if err != nil {
			return r, fail("authentication-failed", "%v", err)
		}
		r.Signed = true
		if signer != nil && signer.Certificate != nil {
			r.SignedBy = signer.Certificate.Subject.String()
		}
		payloadEntity = content
		alg := strings.ToLower(params["micalg"])
		if alg == "" {
			alg = r.MICAlg
		}
		r.MICAlg = alg
	}
	if o.RequireSigned && !r.Signed {
		return r, fail("insufficient-message-security", "the message is not signed, and this partner must sign")
	}
	// For a signed message the MIC is over what was signed, compressed or not (RFC 5402 section 4.1); for an unsigned one, over
	// the uncompressed entity (4.2, 4.3), which is what payloadEntity already is.
	r.MIC = mic(payloadEntity, r.MICAlg)

	ph, pb, err := splitEntity(payloadEntity)
	if err == nil && !r.Compressed {
		if pmt, pparams, perr := mime.ParseMediaType(ph.Get("Content-Type")); perr == nil && isPKCS7(pmt) {
			cb := pb
			if strings.EqualFold(strings.TrimSpace(ph.Get("Content-Transfer-Encoding")), "base64") {
				if d, derr := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(pb)), "")); derr == nil {
					cb = d
				}
			}
			if isCompressed(pparams, cb) {
				inner, derr := cms.Decompress(cmsBytes(cb), o.MaxExpanded)
				if derr != nil {
					return r, fail("decompression-failed", "%v", derr)
				}
				r.Compressed = true
				ph, pb, err = splitEntity(inner)
			}
		}
	}
	if err != nil {
		return r, fail("unexpected-processing-error", "the payload is not a MIME entity: %v", err)
	}
	r.ContentType = ph.Get("Content-Type")
	switch strings.ToLower(strings.TrimSpace(ph.Get("Content-Transfer-Encoding"))) {
	case "base64":
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(pb)), "")); err == nil {
			pb = d
		}
	case "quoted-printable":
		if d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(pb))); err == nil {
			pb = d
		}
	}
	r.Payload = pb
	if _, dp, err := mime.ParseMediaType(ph.Get("Content-Disposition")); err == nil {
		r.FileName = dp["filename"]
	}
	return r, nil
}

func isPKCS7(mt string) bool {
	return mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime"
}

// isCompressed reports whether a pkcs7-mime body is CompressedData: by its smime-type, or by its content when a sender left the
// parameter out.
func isCompressed(params map[string]string, body []byte) bool {
	switch strings.ToLower(params["smime-type"]) {
	case "compressed-data":
		return true
	case "enveloped-data", "signed-data":
		return false
	}
	return cms.IsCompressed(cmsBytes(body))
}

// cmsBytes is a CMS body as DER or BER, decoding the base64 some senders apply without saying so.
func cmsBytes(b []byte) []byte {
	if len(b) > 0 && b[0] != 0x30 {
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), "")); err == nil {
			return d
		}
	}
	return b
}

func toCRLF(b []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

// splitSigned finds the signed content (exactly as sent, headers and all, with the CRLF before the next delimiter removed
// as RFC 1847 requires) and the signature of a multipart/signed body.
func splitSigned(body []byte, bd string) ([]byte, []byte, error) {
	if bd == "" {
		return nil, nil, errors.New("multipart/signed with no boundary")
	}
	delim := []byte("--" + bd)
	parts := bytes.Split(body, delim)
	if len(parts) < 3 {
		return nil, nil, errors.New("multipart/signed with fewer than two parts")
	}
	content := parts[1]
	switch {
	case bytes.HasPrefix(content, []byte("\r\n")):
		content = content[2:]
	case bytes.HasPrefix(content, []byte("\n")):
		content = content[1:]
	}
	switch {
	case bytes.HasSuffix(content, []byte("\r\n")):
		content = content[:len(content)-2]
	case bytes.HasSuffix(content, []byte("\n")):
		content = content[:len(content)-1]
	}
	sigPart, err := mail.ReadMessage(bytes.NewReader(bytes.TrimLeft(parts[2], "\r\n")))
	if err != nil {
		return nil, nil, fmt.Errorf("the signature part cannot be read: %v", err)
	}
	sb, _ := io.ReadAll(sigPart.Body)
	if strings.EqualFold(strings.TrimSpace(sigPart.Header.Get("Content-Transfer-Encoding")), "base64") || sigPart.Header.Get("Content-Transfer-Encoding") == "" {
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(sb)), "")); err == nil {
			sb = d
		}
	}
	return content, sb, nil
}

// Receipt is an MDN to send back.
type Receipt struct {
	// From is our AS2 identifier, To the partner's (the reverse of the message).
	From, To          string
	OriginalMessageID string
	MIC               string
	// Failure, when set, makes this a failure receipt naming it.
	Failure *Failure
	// Signer signs the receipt, when the partner asked for a signed one and we have a key.
	Signer *cms.KeyPair
	Chain  []*x509.Certificate
}

// BuildReceipt makes an MDN's headers and body.
func BuildReceipt(rc Receipt) (http.Header, []byte, error) {
	bd := boundary()
	disposition := "automatic-action/MDN-sent-automatically; processed"
	text := "The AS2 message was received and processed.\r\n"
	if rc.Failure != nil {
		disposition += "/error: " + rc.Failure.Kind
		text = "The AS2 message could not be processed: " + strings.ReplaceAll(rc.Failure.Err.Error(), "\n", " ") + "\r\n"
	}
	var b bytes.Buffer
	b.WriteString(`Content-Type: multipart/report; report-type=disposition-notification; boundary="` + bd + "\"\r\n\r\n")
	b.WriteString("--" + bd + "\r\nContent-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n")
	b.WriteString(text + "\r\n")
	b.WriteString("--" + bd + "\r\nContent-Type: message/disposition-notification\r\nContent-Transfer-Encoding: 7bit\r\n\r\n")
	b.WriteString("Reporting-UA: Perfuse\r\n")
	b.WriteString("Original-Recipient: rfc822; " + rc.From + "\r\n")
	b.WriteString("Final-Recipient: rfc822; " + rc.From + "\r\n")
	b.WriteString("Original-Message-ID: " + rc.OriginalMessageID + "\r\n")
	if rc.MIC != "" {
		b.WriteString("Received-Content-MIC: " + rc.MIC + "\r\n")
	}
	b.WriteString("Disposition: " + disposition + "\r\n")
	b.WriteString("\r\n--" + bd + "--\r\n")
	entity := b.Bytes()
	if rc.Signer != nil {
		var err error
		if entity, err = signEntity(entity, rc.Signer, rc.Chain); err != nil {
			return nil, nil, err
		}
	}
	h, body, err := splitEntity(entity)
	if err != nil {
		return nil, nil, err
	}
	h.Set("AS2-Version", Version)
	h.Set("AS2-From", quoteID(rc.From))
	h.Set("AS2-To", quoteID(rc.To))
	h.Set("Message-ID", msgID(rc.From))
	h.Set("MIME-Version", "1.0")
	h.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	h.Set("Subject", "AS2 receipt")
	return h, body, nil
}

// ReceiptResult is a receipt, read.
type ReceiptResult struct {
	OriginalMessageID string
	Disposition       string
	MIC               string
	Signed            bool
	Text              string
}

// Failed reports the receipt's error, or "" when it says processed.
func (r *ReceiptResult) Failed() string {
	d := strings.ToLower(r.Disposition)
	if i := strings.Index(d, ";"); i >= 0 {
		d = d[i+1:]
	}
	if strings.Contains(d, "error") || strings.Contains(d, "failure") || strings.Contains(d, "failed") || !strings.Contains(d, "processed") {
		return strings.TrimSpace(r.Disposition)
	}
	return ""
}

// ReadReceipt reads an MDN; partner verifies a signed one (nil skips that check), requireSigned refuses an unsigned one.
func ReadReceipt(h http.Header, body []byte, partner *x509.CertPool, requireSigned bool) (*ReceiptResult, error) {
	ct := h.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, fmt.Errorf("as2: the receipt's Content-Type %q cannot be read", ct)
	}
	out := &ReceiptResult{}
	report := body
	reportCT := ct
	if mt == "multipart/signed" {
		content, sig, err := splitSigned(body, params["boundary"])
		if err != nil {
			return nil, fmt.Errorf("as2: the receipt: %w", err)
		}
		if partner != nil {
			if _, err := cms.Verify(sig, content, partner, time.Now()); err != nil {
				if _, err2 := cms.Verify(sig, toCRLF(content), partner, time.Now()); err2 != nil {
					return nil, fmt.Errorf("as2: the receipt's signature does not verify: %w", err)
				}
			}
		}
		out.Signed = true
		rh, rb, err := splitEntity(content)
		if err != nil {
			return nil, fmt.Errorf("as2: the signed receipt: %w", err)
		}
		report, reportCT = rb, rh.Get("Content-Type")
		if mt, params, err = mime.ParseMediaType(reportCT); err != nil {
			return nil, errors.New("as2: the signed receipt has no readable Content-Type")
		}
	}
	if requireSigned && !out.Signed {
		return nil, errors.New("as2: the receipt is not signed, and a signed one was asked for")
	}
	if mt != "multipart/report" {
		return nil, fmt.Errorf("as2: the receipt is %s, not multipart/report", mt)
	}
	mr := multipart.NewReader(bytes.NewReader(report), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("as2: the receipt cannot be read: %w", err)
		}
		b, _ := io.ReadAll(p)
		pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		switch pt {
		case "message/disposition-notification":
			// Fields are header lines; folded lines are joined.
			m, err := mail.ReadMessage(bytes.NewReader(append(bytes.TrimLeft(b, "\r\n"), []byte("\r\n\r\n")...)))
			if err != nil {
				return nil, fmt.Errorf("as2: the disposition cannot be read: %w", err)
			}
			out.OriginalMessageID = strings.TrimSpace(m.Header.Get("Original-Message-ID"))
			out.Disposition = strings.TrimSpace(m.Header.Get("Disposition"))
			out.MIC = strings.TrimSpace(m.Header.Get("Received-Content-MIC"))
		case "text/plain":
			out.Text = strings.TrimSpace(string(b))
		}
	}
	if out.Disposition == "" {
		return nil, errors.New("as2: the receipt has no disposition")
	}
	return out, nil
}

// SameMIC compares two MICs, ignoring spacing and the algorithm's spelling.
func SameMIC(a, b string) bool {
	norm := func(s string) (string, string) {
		parts := strings.SplitN(s, ",", 2)
		v := strings.TrimSpace(parts[0])
		alg := ""
		if len(parts) == 2 {
			alg = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(parts[1])), "-", "")
		}
		return v, alg
	}
	av, aa := norm(a)
	bv, ba := norm(b)
	return av == bv && (aa == ba || aa == "" || ba == "")
}

// Check confirms a receipt is for the message sent and says it was processed with the MIC we computed.
func (r *ReceiptResult) Check(sent *Built) error {
	if r.OriginalMessageID != "" && strings.Trim(r.OriginalMessageID, "<> ") != strings.Trim(sent.MessageID, "<> ") {
		return fmt.Errorf("as2: the receipt is for message %s, not %s", r.OriginalMessageID, sent.MessageID)
	}
	if f := r.Failed(); f != "" {
		return fmt.Errorf("as2: the partner did not accept the message: %s", f)
	}
	if r.MIC == "" {
		return errors.New("as2: the receipt carries no MIC, so it does not prove what arrived")
	}
	if !SameMIC(r.MIC, sent.MIC) {
		return fmt.Errorf("as2: the receipt's MIC %q is not the message's %q: what arrived is not what was sent", r.MIC, sent.MIC)
	}
	return nil
}
