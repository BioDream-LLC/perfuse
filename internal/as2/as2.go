// Package as2 is Applicability Statement 2 (RFC 4130): EDI over HTTP, signed and encrypted with S/MIME, with a signed
// receipt (an MDN) that names a digest of what arrived. Payers and trading partners use it for X12 and EDIFACT.
//
// The S/MIME is internal/cms, the same code Direct messaging uses. What is here is AS2's own: its headers, the message
// integrity check (MIC), and the receipt.
package as2

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms"
)

// Version is the AS2-Version Perfuse speaks: 1.2 (RFC 4130 plus compression, which it does not use; 1.1 receivers read it).
const Version = "1.2"

// MDNMode is whether and how a receipt is asked for.
type MDNMode string

const (
	// MDNNone asks for no receipt.
	MDNNone MDNMode = "none"
	// MDNSync asks for the receipt in the HTTP response.
	MDNSync MDNMode = "sync"
	// MDNAsync asks for the receipt to be posted later to a URL.
	MDNAsync MDNMode = "async"
)

// Outgoing is one message to send.
type Outgoing struct {
	From, To    string // AS2 identifiers
	Payload     []byte
	ContentType string // application/edi-x12, application/edifact, application/hl7-v2 ...
	FileName    string
	Subject     string

	// Signer signs the payload; nil sends it unsigned.
	Signer *cms.KeyPair
	Chain  []*x509.Certificate
	// EncryptTo encrypts the (signed) entity to the partner's certificate; nil sends it unencrypted.
	EncryptTo *x509.Certificate

	MDN MDNMode
	// SignedMDN asks for the receipt to be signed.
	SignedMDN bool
	// AsyncURL is where an asynchronous receipt is to be posted.
	AsyncURL string
	// MessageID is generated when empty.
	MessageID string
	// Compress compresses the payload with zlib before it is signed and encrypted (RFC 5402 sections 3.2 and 3.5, the order
	// most gateways use).
	Compress bool
}

// Built is what goes on the wire, and what the receipt is checked against.
type Built struct {
	Header http.Header
	Body   []byte
	// MIC is the message integrity check the receipt must return: "base64, sha-256".
	MIC       string
	MessageID string
}

func boundary() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "----=_Part_perfuse_" + hex.EncodeToString(b)
}

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

// mic is the RFC 4130 7.3.1 digest: over the MIME entity that was signed (or that would have been), headers included.
func mic(entity []byte, alg string) string {
	switch strings.ToLower(alg) {
	case "sha1", "sha-1":
		s := sha1.Sum(entity)
		return base64.StdEncoding.EncodeToString(s[:]) + ", sha1"
	default:
		s := sha256.Sum256(entity)
		return base64.StdEncoding.EncodeToString(s[:]) + ", sha-256"
	}
}

// payloadEntity is the payload as a MIME entity: binary, so EDI's segment terminators and any byte survive unchanged.
func payloadEntity(o Outgoing) []byte {
	ct := o.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: " + ct + "\r\n")
	b.WriteString("Content-Transfer-Encoding: binary\r\n")
	if o.FileName != "" {
		b.WriteString(`Content-Disposition: attachment; filename="` + strings.ReplaceAll(o.FileName, `"`, "") + "\"\r\n")
	}
	b.WriteString("\r\n")
	b.Write(o.Payload)
	return b.Bytes()
}

// signEntity wraps entity in multipart/signed (RFC 1847) and returns the whole entity, headers included.
func signEntity(entity []byte, kp *cms.KeyPair, chain []*x509.Certificate) ([]byte, error) {
	key, ok := kp.Key.(crypto.Signer)
	if !ok {
		return nil, errors.New("as2: our key cannot sign")
	}
	sig, err := cms.Sign(entity, kp.Certificate, key, chain)
	if err != nil {
		return nil, err
	}
	bd := boundary()
	var b bytes.Buffer
	b.WriteString(`Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary="` + bd + "\"\r\n\r\n")
	b.WriteString("--" + bd + "\r\n")
	b.Write(entity)
	b.WriteString("\r\n--" + bd + "\r\n")
	b.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	b.WriteString(wrap64(sig))
	b.WriteString("--" + bd + "--\r\n")
	return b.Bytes(), nil
}

// splitEntity separates a MIME entity's headers from its body.
func splitEntity(entity []byte) (http.Header, []byte, error) {
	m, err := mail.ReadMessage(bytes.NewReader(entity))
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(m.Body)
	if err != nil {
		return nil, nil, err
	}
	return http.Header(m.Header), body, nil
}

func msgID(from string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	host := strings.Map(func(r rune) rune {
		if r == ' ' || r == '<' || r == '>' || r == '@' {
			return '_'
		}
		return r
	}, from)
	return "<" + hex.EncodeToString(b) + "@" + host + ">"
}

func quoteID(id string) string {
	if strings.ContainsAny(id, " \t\"()<>@,;:\\") {
		return `"` + strings.ReplaceAll(id, `"`, "") + `"`
	}
	return id
}

// Build makes the HTTP request headers and body for one message.
func Build(o Outgoing) (*Built, error) {
	if o.From == "" || o.To == "" {
		return nil, errors.New("as2: AS2-From and AS2-To are both required")
	}
	entity := payloadEntity(o)
	// Unsigned, the MIC is over the uncompressed entity (RFC 5402 sections 4.2 and 4.3); signed, over what is signed, which
	// is the compressed entity when it is compressed (4.1).
	out := &Built{MIC: mic(entity, "sha-256"), MessageID: o.MessageID}
	if o.Compress {
		z, err := cms.Compress(entity)
		if err != nil {
			return nil, err
		}
		entity = append([]byte("Content-Type: application/pkcs7-mime; smime-type=compressed-data; name=\"smime.p7z\"\r\n"+
			"Content-Transfer-Encoding: binary\r\n"+
			"Content-Disposition: attachment; filename=\"smime.p7z\"\r\n\r\n"), z...)
		if o.Signer != nil {
			out.MIC = mic(entity, "sha-256")
		}
	}
	if out.MessageID == "" {
		out.MessageID = msgID(o.From)
	}
	if o.Signer != nil {
		var err error
		if entity, err = signEntity(entity, o.Signer, o.Chain); err != nil {
			return nil, err
		}
	}
	h := http.Header{}
	var body []byte
	if o.EncryptTo != nil {
		env, err := cms.Encrypt(entity, []*x509.Certificate{o.EncryptTo}, false)
		if err != nil {
			return nil, err
		}
		h.Set("Content-Type", `application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"`)
		h.Set("Content-Transfer-Encoding", "binary")
		h.Set("Content-Disposition", `attachment; filename="smime.p7m"`)
		body = env
	} else {
		eh, eb, err := splitEntity(entity)
		if err != nil {
			return nil, err
		}
		for k, v := range eh {
			h[k] = v
		}
		body = eb
	}
	h.Set("AS2-Version", Version)
	h.Set("AS2-From", quoteID(o.From))
	h.Set("AS2-To", quoteID(o.To))
	h.Set("Message-ID", out.MessageID)
	h.Set("MIME-Version", "1.0")
	h.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	h.Set("Subject", mime.QEncoding.Encode("utf-8", orDefault(o.Subject, "AS2 message")))
	if o.FileName != "" {
		h.Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(o.FileName, `"`, "")+`"`)
		if o.EncryptTo != nil {
			h.Set("Content-Disposition", `attachment; filename="smime.p7m"`)
		}
	}
	if o.MDN != MDNNone && o.MDN != "" {
		h.Set("Disposition-Notification-To", "as2@"+strings.ReplaceAll(o.From, " ", "_"))
		if o.SignedMDN {
			h.Set("Disposition-Notification-Options",
				"signed-receipt-protocol=optional, pkcs7-signature; signed-receipt-micalg=optional, sha-256")
		}
		if o.MDN == MDNAsync {
			if o.AsyncURL == "" {
				return nil, errors.New("as2: an asynchronous receipt needs the URL it is to be posted to")
			}
			h.Set("Receipt-Delivery-Option", o.AsyncURL)
		}
	}
	out.Header, out.Body = h, body
	return out, nil
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// Unquote reads an AS2 identifier header, which may be quoted.
func Unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}
