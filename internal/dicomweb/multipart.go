package dicomweb

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// Part is one body part of a multipart/related message.
type Part struct {
	ContentType string
	Location    string
	Body        []byte
}

// NewBoundary returns a random boundary, long enough that it cannot occur in a DICOM object by chance.
func NewBoundary() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "perfuse-" + hex.EncodeToString(b[:])
}

// WriteMultipart writes parts as multipart/related and returns the Content-Type header for it.
//
// type= names the parts' media type, which PS3.18 requires and which a STOW-RS server checks before reading any part.
func WriteMultipart(w io.Writer, partType string, parts []Part) (string, error) {
	boundary := NewBoundary()
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return "", err
	}
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		ct := p.ContentType
		if ct == "" {
			ct = partType
		}
		h.Set("Content-Type", ct)
		if p.Location != "" {
			h.Set("Content-Location", p.Location)
		}
		pw, err := mw.CreatePart(h)
		if err != nil {
			return "", err
		}
		if _, err := pw.Write(p.Body); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf(`multipart/related; type="%s"; boundary=%s`, partType, boundary), nil
}

// ParseMediaType reads a media type and its parameters, accepting the unquoted parameter values DICOMweb clients send.
//
// RFC 2045 requires type=application/dicom to be quoted, because "/" is a special character, and mime.ParseMediaType
// refuses it unquoted. OHIF, dcmjs and others send it unquoted (OHIF's omitQuotationForMultipartRequest), as do several
// archives, so a strict reader answers 406 to a viewer asking for frames in an ordinary way. When the strict reader fails,
// parameters are split on ";" and "=" instead, with quotes removed and names lower-cased.
func ParseMediaType(v string) (string, map[string]string, error) {
	if mt, params, err := mime.ParseMediaType(v); err == nil {
		return mt, params, nil
	}
	pieces := strings.Split(v, ";")
	mt := strings.ToLower(strings.TrimSpace(pieces[0]))
	if mt == "" || strings.Count(mt, "/") != 1 || strings.ContainsAny(mt, " \t\"") {
		return "", nil, fmt.Errorf("%q is not a media type", v)
	}
	params := map[string]string{}
	for _, p := range pieces[1:] {
		name, value, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			if strings.TrimSpace(p) == "" {
				continue
			}
			return "", nil, fmt.Errorf("the parameter %q in %q has no value", p, v)
		}
		params[strings.ToLower(strings.TrimSpace(name))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return mt, params, nil
}

// ReadMultipart reads a multipart/related body, bounded by maxPart and maxTotal bytes.
func ReadMultipart(contentType string, body io.Reader, maxPart, maxTotal int64) (partType string, parts []Part, err error) {
	media, params, err := ParseMediaType(contentType)
	if err != nil {
		return "", nil, fmt.Errorf("the Content-Type %q cannot be read: %w", contentType, err)
	}
	if !strings.EqualFold(media, "multipart/related") {
		return "", nil, fmt.Errorf("the body is %s; DICOMweb sends objects as multipart/related", media)
	}
	if params["boundary"] == "" {
		return "", nil, errors.New("the multipart/related Content-Type names no boundary")
	}
	partType = params["type"]
	mr := multipart.NewReader(body, params["boundary"])
	var total int64
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return partType, parts, fmt.Errorf("the multipart body is malformed: %w", err)
		}
		var buf bytes.Buffer
		n, err := io.Copy(&buf, io.LimitReader(p, maxPart+1))
		if err != nil {
			return partType, parts, err
		}
		if n > maxPart {
			return partType, parts, fmt.Errorf("a part is larger than the %d byte limit", maxPart)
		}
		total += n
		if total > maxTotal {
			return partType, parts, fmt.Errorf("the request is larger than the %d byte limit", maxTotal)
		}
		ct := p.Header.Get("Content-Type")
		if ct == "" {
			ct = partType
		}
		parts = append(parts, Part{ContentType: ct, Location: p.Header.Get("Content-Location"), Body: buf.Bytes()})
	}
	return partType, parts, nil
}
