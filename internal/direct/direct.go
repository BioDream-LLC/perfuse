// Package direct is Direct secure messaging as an edge system: a message to a Direct address is signed with this address's
// certificate and encrypted to the recipient's, as S/MIME, and sent to a HISP by SMTP; a message received from the HISP
// (by SMTP or collected over IMAP) is decrypted and its signature checked against the trust anchors before anything reads it.
//
// It follows the Direct Project Applicability Statement for Secure Health Transport 1.2 and the XDM specification (IHE ITI TF-1
// Annex 15) for documents with metadata. It is not a HISP: the HISP's accreditation, its certificate policy and its directory
// stay with the HISP.
package direct

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
)

// Attachment is one part of a Direct message.
type Attachment struct {
	Name     string
	MimeType string
	Content  []byte
}

// Message is a Direct message before it is protected, or after it is opened.
type Message struct {
	From, Subject string
	To            []string
	Text          string
	Attachments   []Attachment
	MessageID     string
	Date          time.Time
	// DispositionNotification asks the recipient for an MDN (the Direct Implementation Guide for Delivery Notification).
	DispositionNotification bool
}

func boundary() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "perfuse-" + hex.EncodeToString(b)
}

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// wrap64 base64-encodes with 76-character lines, as MIME requires.
func wrap64(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for len(s) > 76 {
		out.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	out.WriteString(s + "\r\n")
	return out.String()
}

// inner writes the message's content: the text and each attachment as a multipart/mixed entity, with CRLF line ends, the
// form that is signed.
func inner(m Message) []byte {
	var b bytes.Buffer
	bd := boundary()
	report := len(m.Attachments) == 1 && m.Attachments[0].MimeType == "message/disposition-notification"
	if report {
		// An MDN is a multipart/report (RFC 6522, RFC 8098): the human-readable part, then the machine-readable one in 7bit.
		b.WriteString("Content-Type: multipart/report; report-type=disposition-notification; boundary=\"" + bd + "\"\r\n\r\n")
	} else {
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + bd + "\"\r\n\r\n")
	}
	b.WriteString("--" + bd + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(crlf(m.Text) + "\r\n")
	for _, a := range m.Attachments {
		if report {
			b.WriteString("--" + bd + "\r\nContent-Type: message/disposition-notification\r\nContent-Transfer-Encoding: 7bit\r\n\r\n")
			b.WriteString(crlf(string(a.Content)))
			continue
		}
		mt := a.MimeType
		if mt == "" {
			mt = "application/octet-stream"
		}
		b.WriteString("--" + bd + "\r\nContent-Type: " + mt + "; name=\"" + a.Name + "\"\r\nContent-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"" + a.Name + "\"\r\n\r\n")
		b.WriteString(wrap64(a.Content))
	}
	b.WriteString("--" + bd + "--\r\n")
	return b.Bytes()
}

// Protect builds the RFC 5322 message the HISP receives: the content signed (multipart/signed, SHA-256) by sender's
// certificate, and that whole entity encrypted (application/pkcs7-mime enveloped-data) to every recipient certificate and
// the sender's own, so the sender can read what it sent. Headers a HISP routes by (From, To, Message-ID, Date) stay outside.
func Protect(m Message, sender cms.KeyPair, chain []*x509.Certificate, recipients []*x509.Certificate) ([]byte, error) {
	if len(m.To) == 0 {
		return nil, errors.New("direct: a message needs at least one recipient")
	}
	if len(recipients) == 0 {
		return nil, errors.New("direct: no recipient certificate was found, so the message cannot be encrypted to anyone")
	}
	content := inner(m)
	key, ok := sender.Key.(crypto.Signer)
	if !ok {
		return nil, errors.New("direct: the sender's key cannot sign")
	}
	sig, err := cms.Sign(content, sender.Certificate, key, chain)
	if err != nil {
		return nil, err
	}
	bd := boundary()
	var signed bytes.Buffer
	signed.WriteString(`Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary="` + bd + "\"\r\n\r\n")
	signed.WriteString("--" + bd + "\r\n")
	signed.Write(content)
	signed.WriteString("\r\n--" + bd + "\r\n")
	signed.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n")
	signed.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	signed.WriteString(wrap64(sig))
	signed.WriteString("--" + bd + "--\r\n")
	env, err := cms.Encrypt(signed.Bytes(), append(append([]*x509.Certificate{}, recipients...), sender.Certificate), false)
	if err != nil {
		return nil, err
	}
	if m.MessageID == "" {
		m.MessageID = "<" + hex.EncodeToString(randBytes(12)) + "@" + domainOf(m.From) + ">"
	}
	if m.Date.IsZero() {
		m.Date = time.Now()
	}
	var out bytes.Buffer
	out.WriteString("From: " + m.From + "\r\nTo: " + strings.Join(m.To, ", ") + "\r\n")
	out.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	out.WriteString("Message-ID: " + m.MessageID + "\r\nDate: " + m.Date.Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\n")
	if m.DispositionNotification {
		// The Direct Implementation Guide for Delivery Notification: ask for processed and dispatched MDNs.
		out.WriteString("Disposition-Notification-Options: X-DIRECT-FINAL-DESTINATION-DELIVERY=optional,true\r\n")
		out.WriteString("Disposition-Notification-To: " + m.From + "\r\n")
	}
	out.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n")
	out.WriteString("Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"smime.p7m\"\r\n\r\n")
	out.WriteString(wrap64(env))
	return out.Bytes(), nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func domainOf(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		addr = a.Address
	}
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return "perfuse.local"
}

// Opened is a received message after decryption and signature checking.
type Opened struct {
	Message
	// Signer is the certificate that signed it, checked to a trust anchor.
	Signer *x509.Certificate
	// SignerAddress is the Direct address (or domain) the signer's certificate is for.
	SignerAddress string
	// XDM is the documents and metadata when an attachment is an XDM package.
	XDM []XDMDocument
	// MDN is the disposition notification when the message is one: its kind (processed, dispatched, failed) and the
	// Message-ID it is about.
	MDN *Disposition
}

// Disposition is an MDN's report.
type Disposition struct {
	Kind, OriginalMessageID string
}

// ErrUntrusted is a message whose signature does not lead to a trust anchor.
var ErrUntrusted = errors.New("direct: the message is not signed by a trusted sender")

// Open reads a message from the HISP: decrypts it with whichever of keys it was encrypted to, verifies the signature against
// anchors, and checks that the signing certificate belongs to the From address (or its domain), as the Applicability
// Statement requires. An unsigned, untrusted or misattributed message is an error: it is never passed on as if it were Direct.
func Open(raw []byte, keys []cms.KeyPair, anchors *x509.CertPool) (*Opened, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("direct: the message cannot be read: %w", err)
	}
	mt, _, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || (mt != "application/pkcs7-mime" && mt != "application/x-pkcs7-mime") {
		return nil, fmt.Errorf("direct: the message is not encrypted S/MIME (Content-Type %q); Direct messages always are", msg.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return nil, err
	}
	der := decodeBody(body, msg.Header.Get("Content-Transfer-Encoding"))
	plain, err := cms.Decrypt(der, keys)
	if err != nil {
		return nil, err
	}
	signed, err := mail.ReadMessage(bytes.NewReader(append(plain, '\r', '\n')))
	if err != nil {
		return nil, fmt.Errorf("direct: the decrypted content cannot be read: %w", err)
	}
	smt, sp, err := mime.ParseMediaType(signed.Header.Get("Content-Type"))
	if err != nil || smt != "multipart/signed" {
		return nil, fmt.Errorf("%w: the content is not multipart/signed", ErrUntrusted)
	}
	content, sig, err := splitSigned(plain, sp["boundary"])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUntrusted, err)
	}
	signer, err := cms.Verify(sig, content, anchors, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUntrusted, err)
	}
	from := msg.Header.Get("From")
	fromAddr := from
	if a, err := mail.ParseAddress(from); err == nil {
		fromAddr = a.Address
	}
	bound := certBoundTo(signer.Certificate, fromAddr)
	if bound == "" {
		return nil, fmt.Errorf("%w: it is signed by %s, whose certificate is not for %s", ErrUntrusted, signer.Certificate.Subject.CommonName, fromAddr)
	}
	out := &Opened{Signer: signer.Certificate, SignerAddress: bound}
	out.From = from
	out.To = splitAddrs(msg.Header.Get("To"))
	dec := new(mime.WordDecoder)
	out.Subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	out.MessageID = msg.Header.Get("Message-ID")
	out.Date, _ = msg.Header.Date()
	if err := readContent(content, &out.Message, out); err != nil {
		return nil, err
	}
	for _, a := range out.Attachments {
		if strings.HasSuffix(strings.ToLower(a.Name), ".zip") || a.MimeType == "application/zip" {
			if docs, err := ReadXDM(a.Content); err == nil {
				out.XDM = append(out.XDM, docs...)
			}
		}
	}
	return out, nil
}

func splitAddrs(h string) []string {
	list, err := mail.ParseAddressList(h)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

// certBoundTo says which of the address or its domain the certificate is for: an address certificate names the address
// in a subjectAltName rfc822Name, a domain certificate the domain in a dNSName. "" means neither.
func certBoundTo(c *x509.Certificate, addr string) string {
	addr = strings.ToLower(addr)
	for _, e := range c.EmailAddresses {
		if strings.ToLower(e) == addr {
			return addr
		}
	}
	domain := strings.ToLower(domainOf(addr))
	for _, d := range c.DNSNames {
		if strings.ToLower(d) == domain {
			return domain
		}
	}
	return ""
}

func decodeBody(b []byte, enc string) []byte {
	if strings.EqualFold(strings.TrimSpace(enc), "base64") || enc == "" {
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), "")); err == nil {
			return d
		}
	}
	return b
}

// splitSigned finds the signed content (exactly as sent, headers and all, with its final CRLF removed as RFC 1847 requires)
// and the signature part of a multipart/signed entity.
func splitSigned(entity []byte, bd string) ([]byte, []byte, error) {
	if bd == "" {
		return nil, nil, errors.New("multipart/signed with no boundary")
	}
	delim := []byte("--" + bd)
	parts := bytes.Split(entity, delim)
	if len(parts) < 3 {
		return nil, nil, errors.New("multipart/signed with fewer than two parts")
	}
	// The part is everything after the delimiter line's CRLF up to the CRLF that begins the next delimiter (RFC 2046: that
	// CRLF belongs to the delimiter, not to the part). Exactly one is removed at each end, or the signed bytes change.
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
	return content, decodeBody(sb, sigPart.Header.Get("Content-Transfer-Encoding")), nil
}

// readContent reads the signed MIME entity: text, attachments, and an MDN report.
func readContent(entity []byte, m *Message, o *Opened) error {
	e, err := mail.ReadMessage(bytes.NewReader(append(entity, '\r', '\n')))
	if err != nil {
		return fmt.Errorf("direct: the content cannot be read: %w", err)
	}
	return readPart(textproto.MIMEHeader(e.Header), e.Body, m, o)
}

func readPart(h textproto.MIMEHeader, body io.Reader, m *Message, o *Opened) error {
	mt, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		mt = "text/plain"
	}
	switch {
	case strings.HasPrefix(mt, "multipart/"):
		r := multipart.NewReader(body, params["boundary"])
		for {
			p, err := r.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("direct: a MIME part cannot be read: %w", err)
			}
			if err := readPart(p.Header, p, m, o); err != nil {
				return err
			}
		}
	case mt == "message/disposition-notification":
		b, _ := io.ReadAll(body)
		b = decodeBody2(b, h.Get("Content-Transfer-Encoding"))
		tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(b, "\r\n"), '\r', '\n', '\r', '\n'))))
		fields, _ := tp.ReadMIMEHeader()
		disp := fields.Get("Disposition")
		kind := disp
		if i := strings.LastIndex(disp, ";"); i >= 0 {
			kind = strings.TrimSpace(disp[i+1:])
		}
		if i := strings.IndexByte(kind, '/'); i >= 0 {
			kind = kind[i+1:]
		}
		o.MDN = &Disposition{Kind: strings.ToLower(strings.TrimSpace(kind)), OriginalMessageID: fields.Get("Original-Message-ID")}
		return nil
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	b = decodeBody2(b, h.Get("Content-Transfer-Encoding"))
	_, dp, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := dp["filename"]
	if name == "" {
		name = params["name"]
	}
	if mt == "text/plain" && name == "" && m.Text == "" {
		m.Text = string(b)
		return nil
	}
	if name == "" {
		name = "attachment"
	}
	m.Attachments = append(m.Attachments, Attachment{Name: name, MimeType: mt, Content: b})
	return nil
}

func decodeBody2(b []byte, enc string) []byte {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), "")); err == nil {
			return d
		}
	case "quoted-printable":
		if d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(b))); err == nil {
			return d
		}
	}
	return b
}

// MDN builds a processed disposition notification for a message received (the Direct Implementation Guide for Delivery
// Notification): the content to protect and send back to the sender.
func MDN(received *Opened, ourAddress, kind string) Message {
	report := "Reporting-UA: " + domainOf(ourAddress) + "; Perfuse\r\nFinal-Recipient: rfc822;" + ourAddress + "\r\n" +
		"Original-Message-ID: " + received.MessageID + "\r\nDisposition: automatic-action/MDN-sent-automatically;" + kind + "\r\n"
	return Message{From: ourAddress, To: []string{addressOf(received.From)}, Subject: "Processed: " + received.Subject,
		Text: "Your message was " + kind + ".", Attachments: []Attachment{{Name: "", MimeType: "message/disposition-notification",
			Content: []byte(report)}}}
}

func addressOf(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

// XDM.

// XDMDocument is one document from an XDM package, with what its metadata says.
type XDMDocument struct {
	Name, MimeType, Title, PatientID, UniqueID, TypeCode string
	Content                                              []byte
}

// WriteXDM packages documents as IHE XDM: IHE_XDM/SUBSET01/METADATA.XML (the XDS metadata), the documents beside it, and
// INDEX.HTM and README.TXT at the top, as ITI TF-1 Annex 15 lays out.
func WriteXDM(metadata []byte, docs map[string][]byte, readme string) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	add := func(name string, b []byte) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	var index strings.Builder
	index.WriteString("<html><body><h1>IHE XDM package</h1><ul>")
	for name, b := range docs {
		if err := add("IHE_XDM/SUBSET01/"+name, b); err != nil {
			return nil, err
		}
		index.WriteString(`<li><a href="IHE_XDM/SUBSET01/` + name + `">` + name + `</a></li>`)
	}
	index.WriteString("</ul></body></html>")
	if err := add("IHE_XDM/SUBSET01/METADATA.XML", metadata); err != nil {
		return nil, err
	}
	if err := add("INDEX.HTM", []byte(index.String())); err != nil {
		return nil, err
	}
	if readme == "" {
		readme = "This package was made by Perfuse, an Apache-2.0 healthcare integration engine.\r\n"
	}
	if err := add("README.TXT", []byte(readme)); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadXDM reads an XDM package's documents and what its METADATA.XML says about each (by the URI slot that names the file).
func ReadXDM(zipped []byte) ([]XDMDocument, error) {
	z, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return nil, fmt.Errorf("direct: the XDM package is not a ZIP: %w", err)
	}
	files := map[string][]byte{}
	var metadata []byte
	var total int64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > 64<<20 || total > 256<<20 {
			return nil, errors.New("direct: the XDM package is too large to open")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(b))
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.EqualFold(path.Base(name), "METADATA.XML") {
			metadata = b
			continue
		}
		files[name] = b
	}
	if metadata == nil {
		return nil, errors.New("direct: the ZIP has no METADATA.XML, so it is not an XDM package")
	}
	return xdmDocuments(metadata, files)
}

// sha1Hex is a document's XDS hash.
func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}
