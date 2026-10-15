package xds

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
)

// The SOAP 1.2 envelope with WS-Addressing that every XDS.b and XCA transaction uses, sent as MTOM/XOP when it carries
// documents (ITI-41, and the answers to ITI-43 and ITI-39) and as plain SOAP otherwise. A receiver accepts both, and a
// document inline as base64 as well as by xop:Include.

const (
	nsSOAP12 = "http://www.w3.org/2003/05/soap-envelope"
	nsWSA    = "http://www.w3.org/2005/08/addressing"
	nsXOP    = "http://www.w3.org/2004/08/xop/include"
	anonWSA  = "http://www.w3.org/2005/08/addressing/anonymous"
)

// Envelope is a received SOAP message.
type Envelope struct {
	Action    string
	MessageID string
	// Body is the first element inside soap:Body, as sent.
	Body []byte
	// Parts are the MTOM attachments by Content-ID (without the angle brackets).
	Parts map[string][]byte
}

type soapIn struct {
	XMLName xml.Name `xml:"Envelope"`
	Header  struct {
		Action    string `xml:"http://www.w3.org/2005/08/addressing Action"`
		MessageID string `xml:"http://www.w3.org/2005/08/addressing MessageID"`
	} `xml:"Header"`
	Body struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"Body"`
}

// MaxMessage bounds a received message; a submission of many large documents is the reason it is large.
const MaxMessage = 256 << 20

// ReadEnvelope reads a SOAP 1.2 message, MTOM or not.
func ReadEnvelope(contentType string, body io.Reader) (*Envelope, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, fmt.Errorf("the Content-Type %q cannot be read: %w", contentType, err)
	}
	env := &Envelope{Parts: map[string][]byte{}}
	var root []byte
	switch strings.ToLower(mt) {
	case "multipart/related":
		boundary := params["boundary"]
		if boundary == "" {
			return nil, errors.New("a multipart/related message without a boundary")
		}
		start := strings.Trim(params["start"], "<>")
		r := multipart.NewReader(io.LimitReader(body, MaxMessage), boundary)
		first := true
		for {
			p, err := r.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("reading the MTOM parts: %w", err)
			}
			b, err := io.ReadAll(p)
			if err != nil {
				return nil, err
			}
			cid := strings.Trim(p.Header.Get("Content-ID"), "<> ")
			if (start != "" && cid == start) || (start == "" && first) {
				root = b
			} else {
				env.Parts[cid] = decodeTransfer(b, p.Header.Get("Content-Transfer-Encoding"))
			}
			first = false
		}
		if root == nil {
			return nil, errors.New("the MTOM message has no root part with the SOAP envelope")
		}
		if a := params["start-info"]; a != "" && params["action"] == "" {
			_ = a
		}
	case "application/soap+xml", "text/xml", "application/xml":
		b, err := io.ReadAll(io.LimitReader(body, MaxMessage))
		if err != nil {
			return nil, err
		}
		root = b
	default:
		return nil, fmt.Errorf("the body is %s; XDS.b is SOAP 1.2 (application/soap+xml), plain or as MTOM", mt)
	}
	var s soapIn
	if err := xml.Unmarshal(root, &s); err != nil {
		return nil, fmt.Errorf("this is not a SOAP envelope: %w", err)
	}
	env.Action = strings.TrimSpace(s.Header.Action)
	if env.Action == "" {
		env.Action = actionParam(contentType)
	}
	env.MessageID = strings.TrimSpace(s.Header.MessageID)
	env.Body = bytes.TrimSpace(s.Body.Inner)
	if len(env.Body) == 0 {
		return nil, errors.New("the SOAP Body is empty")
	}
	return env, nil
}

func decodeTransfer(b []byte, enc string) []byte {
	if strings.EqualFold(strings.TrimSpace(enc), "base64") {
		if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), "")); err == nil {
			return d
		}
	}
	return b
}

// actionParam is the action parameter of a SOAP 1.2 Content-Type, at the top level or inside an MTOM start-info.
func actionParam(ct string) string {
	if _, p, err := mime.ParseMediaType(ct); err == nil {
		if a := p["action"]; a != "" {
			return a
		}
		if si := p["start-info"]; si != "" {
			return actionParam(si)
		}
	}
	return ""
}

var includeHref = regexp.MustCompile(`(?s)<(?:[A-Za-z0-9_.-]+:)?Include\b[^>]*\bhref\s*=\s*["']([^"']+)["']`)

// Resolve fills a Document's Content: from its xop:Include, or from its base64 text.
func (e *Envelope) Resolve(d *Document) error {
	if m := includeHref.FindStringSubmatch(d.Inner); m != nil {
		cid := strings.TrimPrefix(m[1], "cid:")
		if u, err := url.PathUnescape(cid); err == nil {
			cid = u
		}
		b, ok := e.Parts[cid]
		if !ok {
			return fmt.Errorf("document %s points at MTOM part %s, which the message does not carry", d.ID, cid)
		}
		d.Content = b
		return nil
	}
	text := strings.Join(strings.Fields(d.Inner), "")
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return fmt.Errorf("document %s is neither an xop:Include nor base64: %w", d.ID, err)
	}
	d.Content = b
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Outgoing is a SOAP message to send: the body element's XML, and the documents it refers to by xop:Include.
type Outgoing struct {
	Action    string
	RelatesTo string
	To        string
	Body      []byte
	// Attachments are the MTOM parts by Content-ID; with none, the message is plain SOAP 1.2.
	Attachments []Attachment
	MTOM        bool
}

// Attachment is one MTOM part.
type Attachment struct {
	ContentID string
	MimeType  string
	Content   []byte
}

// Include is the xop:Include for an attachment, to put inside a Document element.
func Include(cid string) string {
	return `<xop:Include xmlns:xop="` + nsXOP + `" href="cid:` + url.PathEscape(cid) + `"/>`
}

// Write builds the message, returning the bytes and the Content-Type to send them with.
func (o Outgoing) Write() ([]byte, string) {
	var env bytes.Buffer
	env.WriteString(`<soapenv:Envelope xmlns:soapenv="` + nsSOAP12 + `" xmlns:wsa="` + nsWSA + `"><soapenv:Header>`)
	env.WriteString(`<wsa:Action soapenv:mustUnderstand="1">` + xmlEscape(o.Action) + `</wsa:Action>`)
	if o.RelatesTo != "" {
		env.WriteString(`<wsa:RelatesTo>` + xmlEscape(o.RelatesTo) + `</wsa:RelatesTo>`)
	} else {
		env.WriteString(`<wsa:MessageID>urn:uuid:` + newUUID() + `</wsa:MessageID>`)
		env.WriteString(`<wsa:ReplyTo soapenv:mustUnderstand="1"><wsa:Address>` + anonWSA + `</wsa:Address></wsa:ReplyTo>`)
		if o.To != "" {
			env.WriteString(`<wsa:To soapenv:mustUnderstand="1">` + xmlEscape(o.To) + `</wsa:To>`)
		}
	}
	env.WriteString(`</soapenv:Header><soapenv:Body>`)
	env.Write(o.Body)
	env.WriteString(`</soapenv:Body></soapenv:Envelope>`)
	if !o.MTOM && len(o.Attachments) == 0 {
		return env.Bytes(), `application/soap+xml; charset=UTF-8; action="` + o.Action + `"`
	}
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	rootID := "root." + newUUID() + "@perfuse.health"
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", `application/xop+xml; charset=UTF-8; type="application/soap+xml"; action="`+o.Action+`"`)
	h.Set("Content-Transfer-Encoding", "binary")
	h.Set("Content-ID", "<"+rootID+">")
	pw, _ := w.CreatePart(h)
	_, _ = pw.Write(env.Bytes())
	for _, a := range o.Attachments {
		h := textproto.MIMEHeader{}
		mt := a.MimeType
		if mt == "" {
			mt = "application/octet-stream"
		}
		h.Set("Content-Type", mt)
		h.Set("Content-Transfer-Encoding", "binary")
		h.Set("Content-ID", "<"+a.ContentID+">")
		pw, _ := w.CreatePart(h)
		_, _ = pw.Write(a.Content)
	}
	_ = w.Close()
	ct := fmt.Sprintf(`multipart/related; type="application/xop+xml"; boundary="%s"; start="<%s>"; start-info="application/soap+xml"; action="%s"`,
		w.Boundary(), rootID, o.Action)
	return out.Bytes(), ct
}

// Fault is a SOAP 1.2 fault, for a request that is not an XDS transaction at all; a transaction that fails is answered with
// a RegistryResponse instead, as IHE requires.
func Fault(action, relatesTo, code, reason string) ([]byte, string) {
	body := `<soapenv:Fault><soapenv:Code><soapenv:Value>soapenv:` + code + `</soapenv:Value></soapenv:Code>` +
		`<soapenv:Reason><soapenv:Text xml:lang="en">` + xmlEscape(reason) + `</soapenv:Text></soapenv:Reason></soapenv:Fault>`
	if action == "" {
		action = "http://www.w3.org/2005/08/addressing/soap/fault"
	}
	return Outgoing{Action: action, RelatesTo: relatesTo, Body: []byte(body)}.Write()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
