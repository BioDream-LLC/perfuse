// Package soap sends SOAP requests and reads SOAP faults, using only the standard library.
//
// SOAP is a fair amount of ceremony around an HTTP POST, and it is still how a great deal of hospital middleware
// accepts data - patient administration systems, document repositories, regional record services, anything
// specified in the decade when this was the answer. Mirth has a Web Service connector, so this is a Tier 2
// migration blocker rather than a nice-to-have.
//
// # Why not a generated client
//
// The usual approach is to take a WSDL and generate Go types from it. That is the right answer for an application
// talking to one known service, and the wrong one here: an integration engine's destination is configured by
// somebody editing a channel, not recompiled, so the envelope has to be assembled at run time from a template
// they can see and change. It also means there is no code generation step and nothing to regenerate when the far
// end publishes a new WSDL.
//
// # What this deliberately does not do
//
// No WSDL parsing, no WS-Security, no MTOM, no WS-Addressing. Those are each a specification in their own right,
// and pretending to support one while getting a detail wrong is worse than not offering it - a signature that
// almost verifies is indistinguishable from an attack at the far end. WS-Security in particular is where a
// half-implementation would do real harm.
//
// What is here: SOAP 1.1 and 1.2 envelopes, a templated body, the SOAPAction header, and fault detection that
// reads the fault string rather than reporting "500".
package soap

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Version selects the envelope namespace and how the action is sent.
type Version string

const (
	// V11 is SOAP 1.1: text/xml with a SOAPAction header. Still the commonest in hospital middleware by a wide
	// margin, which is why it is the default.
	V11 Version = "1.1"

	// V12 is SOAP 1.2: application/soap+xml with the action as a content-type parameter.
	V12 Version = "1.2"
)

const (
	ns11 = "http://schemas.xmlsoap.org/soap/envelope/"
	ns12 = "http://www.w3.org/2003/05/soap-envelope"
)

// Request is one call.
type Request struct {
	// URL is the endpoint.
	URL string

	// Action is the SOAPAction. Required by most 1.1 services and ignored by some.
	Action string

	// Body is the XML that goes inside the envelope's Body element, already rendered.
	Body string

	// Headers is XML that goes inside the envelope's Header element. Empty means no header element at all,
	// rather than an empty one - some services reject an empty Header.
	Headers string

	// Version defaults to 1.1.
	Version Version

	// HTTPHeaders are added to the request, for an API key or a routing header.
	HTTPHeaders map[string]string

	// Username and Password use HTTP basic authentication, which a surprising number of these services expect
	// in preference to anything in the envelope.
	Username string
	Password string
}

// Response is what came back.
type Response struct {
	// Status is the HTTP status code.
	Status int

	// Body is the whole response, kept so a caller can inspect it or hand it to a response transformer.
	Body []byte

	// Fault is set when the response contained a SOAP fault.
	Fault *Fault
}

// Fault is a SOAP fault, flattened across both versions.
type Fault struct {
	// Code is the fault code, for example "soap:Client".
	Code string

	// Reason is the human-readable fault string. The useful part, and the reason this package parses faults at
	// all rather than reporting a status code.
	Reason string

	// Detail is whatever the service put in the detail element, which is often the only thing that says which
	// field was wrong.
	Detail string
}

// Error makes a fault usable as an error.
func (f *Fault) Error() string {
	var b strings.Builder
	b.WriteString("the service returned a SOAP fault")
	if f.Code != "" {
		fmt.Fprintf(&b, " (%s)", f.Code)
	}
	if f.Reason != "" {
		fmt.Fprintf(&b, ": %s", f.Reason)
	}
	if f.Detail != "" {
		// Included because it is frequently the only part that names the field that was rejected, and a fault
		// without it sends somebody to read a WSDL.
		fmt.Fprintf(&b, " — %s", collapse(f.Detail))
	}
	return b.String()
}

// Call sends a request.
//
// A fault is returned in the Response rather than as an error, so a caller can decide. Some services answer a
// duplicate submission with a fault that a site considers success, and a transport that decided for them would
// have to be worked around with a script.
func Call(ctx context.Context, client *http.Client, req Request) (*Response, error) {
	if strings.TrimSpace(req.URL) == "" {
		return nil, fmt.Errorf("a SOAP request needs a URL")
	}

	version := req.Version
	if version == "" {
		version = V11
	}

	envelope, err := Envelope(req.Body, req.Headers, version)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(envelope))
	if err != nil {
		return nil, err
	}

	switch version {
	case V12:
		// In 1.2 the action is a parameter of the content type, not a header. Sending it as a header instead is
		// a common mistake and produces a service that silently dispatches to the wrong operation, or to none.
		contentType := "application/soap+xml; charset=utf-8"
		if req.Action != "" {
			contentType += fmt.Sprintf(`; action="%s"`, req.Action)
		}
		httpReq.Header.Set("Content-Type", contentType)
	default:
		httpReq.Header.Set("Content-Type", "text/xml; charset=utf-8")
		// Quoted, and set even when empty. A 1.1 service that dispatches on SOAPAction returns a fault about a
		// missing action if the header is absent, and an empty quoted string is the specified way to say "no
		// action" - which is different from not sending it.
		httpReq.Header.Set("SOAPAction", fmt.Sprintf("%q", req.Action))
	}

	for name, value := range req.HTTPHeaders {
		httpReq.Header.Set(name, value)
	}
	if req.Username != "" {
		httpReq.SetBasicAuth(req.Username, req.Password)
	}

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded, because a service answering with a stream would otherwise hold memory until it stopped. Two
	// megabytes is far more than any acknowledgement and small enough to be safe.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	out := &Response{Status: resp.StatusCode, Body: body}
	out.Fault = ParseFault(body)
	return out, nil
}

// Envelope wraps a body in a SOAP envelope.
func Envelope(body, headers string, version Version) ([]byte, error) {
	ns := ns11
	if version == V12 {
		ns = ns12
	}

	if strings.TrimSpace(body) == "" {
		// Refused rather than sent empty. An empty Body is valid XML and almost every service answers it with a
		// fault about a missing operation, which reads as a service problem rather than a template problem.
		return nil, fmt.Errorf("the SOAP body is empty; nothing would be sent but the envelope")
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintf(&b, `<soap:Envelope xmlns:soap="%s">`, ns)

	// Omitted entirely when there is nothing to put in it. Some services reject an empty Header element, and
	// emitting one for tidiness would break them for no benefit.
	if strings.TrimSpace(headers) != "" {
		fmt.Fprintf(&b, `<soap:Header>%s</soap:Header>`, headers)
	}

	fmt.Fprintf(&b, `<soap:Body>%s</soap:Body>`, body)
	b.WriteString(`</soap:Envelope>`)

	return []byte(b.String()), nil
}

// ParseFault finds a SOAP fault in a response, or returns nil.
//
// Both versions, because the element names differ: 1.1 uses faultcode and faultstring, 1.2 uses Code/Value and
// Reason/Text. A parser that only knew one would report "no fault" on half of all services, which is worse than
// not looking - the delivery would be recorded as successful.
//
// Namespace-insensitive on the element names, deliberately. Real services use every prefix imaginable and some
// use none, and refusing to recognise a fault because it was labelled soapenv rather than soap would mean
// treating a rejection as a success.
func ParseFault(body []byte) *Fault {
	if !bytes.Contains(body, []byte("Fault")) {
		// Cheap rejection first: the overwhelming majority of responses are not faults, and parsing every one
		// would make this the slowest part of a delivery.
		return nil
	}

	dec := xml.NewDecoder(bytes.NewReader(body))

	inFault := false
	var fault Fault
	var current string
	var depth int

	for {
		token, err := dec.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if name == "Fault" {
				inFault = true
				depth = 0
				continue
			}
			if inFault {
				depth++
				current = name
			}

		case xml.EndElement:
			if t.Name.Local == "Fault" {
				inFault = false
				continue
			}
			if inFault {
				depth--
			}

		case xml.CharData:
			if !inFault {
				continue
			}
			text := strings.TrimSpace(string(t))
			if text == "" {
				continue
			}

			switch current {
			case "faultcode", "Value":
				if fault.Code == "" {
					fault.Code = text
				}
			case "faultstring", "Text":
				if fault.Reason == "" {
					fault.Reason = text
				}
			case "faultactor":
				// Ignored: it names the node that failed, which is rarely useful to somebody reading a log at
				// three in the morning.
			default:
				// Everything else inside detail. Concatenated rather than structured, because services put
				// wildly different things in there and the useful behaviour is to show it.
				if fault.Detail == "" {
					fault.Detail = text
				} else if len(fault.Detail) < 500 {
					fault.Detail += " " + text
				}
			}
		}
	}

	if fault.Code == "" && fault.Reason == "" && fault.Detail == "" {
		// The word "Fault" appeared but nothing was parsed - probably in ordinary content. Reporting a fault
		// with no information would turn a successful delivery into an unexplained failure.
		return nil
	}

	_ = depth
	return &fault
}

// XMLEscape escapes text for inclusion in an envelope body.
//
// Exported because the caller builds the body from a template and message values, and an unescaped ampersand in a
// patient's name would produce an envelope the service rejects as malformed XML - reported as a fault about the
// request rather than about the name.
func XMLEscape(s string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

func collapse(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
