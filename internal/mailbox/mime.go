package mailbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"strings"
	"time"
)

// Part is one attachment.
type Part struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Parsed is a message taken apart.
type Parsed struct {
	From        string // the address only, lower case
	Subject     string
	MessageID   string
	Date        time.Time
	Text        string // the first text/plain body, or text/html when there is no plain one
	Attachments []Part
}

var wordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, in io.Reader) (io.Reader, error) {
	// UTF-8 and US-ASCII are read as they are; Latin-1 is widened. Anything else is passed through rather than refused, so a
	// subject in an unexpected charset does not stop the message from being read.
	if strings.EqualFold(charset, "iso-8859-1") || strings.EqualFold(charset, "latin1") || strings.EqualFold(charset, "windows-1252") {
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return strings.NewReader(string(r)), nil
	}
	return in, nil
}}

// Parse reads an RFC 5322 message with its MIME parts. Nesting deeper than 10 levels is refused.
func Parse(raw []byte) (Parsed, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Parsed{}, fmt.Errorf("not an email message: %w", err)
	}
	p := Parsed{MessageID: strings.Trim(m.Header.Get("Message-Id"), "<> ")}
	if s, err := wordDecoder.DecodeHeader(m.Header.Get("Subject")); err == nil {
		p.Subject = s
	} else {
		p.Subject = m.Header.Get("Subject")
	}
	if addr, err := mail.ParseAddress(m.Header.Get("From")); err == nil {
		p.From = strings.ToLower(addr.Address)
	} else if list, err := mail.ParseAddressList(m.Header.Get("From")); err == nil && len(list) > 0 {
		p.From = strings.ToLower(list[0].Address)
	}
	if d, err := m.Header.Date(); err == nil {
		p.Date = d
	}
	var html string
	err = walk(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Header.Get("Content-Disposition"),
		m.Body, 0, &p, &html)
	if err != nil {
		return p, err
	}
	if p.Text == "" {
		p.Text = html
	}
	return p, nil
}

func walk(ctype, cte, disp string, body io.Reader, depth int, p *Parsed, html *string) error {
	if depth > 10 {
		return errors.New("the message nests its parts more than 10 deep")
	}
	if ctype == "" {
		ctype = "text/plain"
	}
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		mt, params = "application/octet-stream", map[string]string{}
	}
	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return errors.New("a multipart part has no boundary")
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("reading a MIME part: %w", err)
			}
			if err := walk(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"), part, depth+1, p, html); err != nil {
				return err
			}
		}
	}
	data, err := io.ReadAll(decoder(cte, body))
	if err != nil {
		return fmt.Errorf("decoding a MIME part: %w", err)
	}
	dispType, dparams, _ := mime.ParseMediaType(disp)
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	if n, err := wordDecoder.DecodeHeader(name); err == nil {
		name = n
	}
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		name = ""
	}
	attached := strings.EqualFold(dispType, "attachment") || name != "" || mt == "message/rfc822"
	switch {
	case !attached && mt == "text/plain" && p.Text == "":
		p.Text = string(data)
	case !attached && mt == "text/html" && *html == "":
		*html = string(data)
	case attached || !strings.HasPrefix(mt, "text/"):
		p.Attachments = append(p.Attachments, Part{Filename: name, ContentType: mt, Data: data})
	}
	return nil
}

func decoder(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &stripSpace{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// stripSpace drops the line breaks base64 bodies are wrapped with.
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	for {
		n, err := s.r.Read(p)
		j := 0
		for _, c := range p[:n] {
			if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
				p[j] = c
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}

// SenderAllowed reports whether a sender matches an allow list of addresses (alice@example.org) and domains (example.org or
// @example.org). An empty list allows everyone.
func SenderAllowed(from string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	from = strings.ToLower(strings.TrimSpace(from))
	at := strings.LastIndex(from, "@")
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.Contains(strings.TrimPrefix(a, "@"), "@") {
			if a == from {
				return true
			}
			continue
		}
		if at >= 0 && from[at+1:] == strings.TrimPrefix(a, "@") {
			return true
		}
	}
	return false
}
