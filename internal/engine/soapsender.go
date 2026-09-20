package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/soap"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// SOAPSender posts a SOAP request per message.
type SOAPSender struct {
	cfg    *config.SOAPDestination
	name   string
	client *http.Client
}

// NewSOAPSender builds the sender.
func NewSOAPSender(d config.Destination) (*SOAPSender, error) {
	if d.SOAP == nil {
		return nil, fmt.Errorf("destination %q is a soap destination with no soap block", d.Name)
	}

	client := &http.Client{Timeout: d.SOAP.Timeout}

	if d.SOAP.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(d.SOAP.TLS)
		if err != nil {
			return nil, err
		}
		client.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}

	return &SOAPSender{cfg: d.SOAP, name: d.Name, client: client}, nil
}

// Send posts one request.
func (s *SOAPSender) Send(ctx context.Context, msg []byte) error {
	_, err := s.SendForResponse(ctx, msg)
	return err
}

// SendForResponse posts one request and returns the response body, satisfying Responder.
//
// Implemented so a response transformer can inspect the reply, which matters more for SOAP than for anything else
// here: these services routinely answer with a 200 and a body describing a rejection, and the fault codes that
// should count as success are entirely site-specific.
func (s *SOAPSender) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	parsed, err := hl7.Parse(msg)
	if err != nil {
		return nil, fmt.Errorf("building the SOAP request needs the message parsed, and a transformation "+
			"produced something invalid: %w", err)
	}

	body, missing := renderXMLTemplate(s.cfg.Body, parsed)
	if len(missing) > 0 {
		// Refused rather than sent with empty elements. A service receiving an empty patient identifier either
		// rejects it with a fault about its own schema, or accepts it and files the record against nobody.
		return nil, fmt.Errorf("the SOAP body refers to %s, which this message does not carry",
			strings.Join(missing, ", "))
	}

	header, _ := renderXMLTemplate(s.cfg.Header, parsed)

	resp, err := soap.Call(ctx, s.client, soap.Request{
		URL:         s.cfg.URL,
		Action:      s.cfg.Action,
		Body:        body,
		Headers:     header,
		Version:     s.cfg.SOAPVersion(),
		HTTPHeaders: s.cfg.Headers,
		Username:    s.cfg.Username,
		Password:    s.cfg.Password,
	})
	if err != nil {
		return nil, err
	}

	if resp.Fault != nil {
		if s.faultCountsAsSuccess(resp.Fault) {
			// Recorded as delivered. A service answering a resend with "already submitted" is telling you the
			// message arrived, and retrying forever against a receiver that already has it is worse than
			// accepting its word.
			return resp.Body, nil
		}
		return resp.Body, resp.Fault
	}

	// A non-2xx with no fault is still a failure, and the status is all there is to report. Some services answer
	// 500 with an HTML error page, which is why the body is included but bounded.
	if resp.Status < 200 || resp.Status >= 300 {
		return resp.Body, fmt.Errorf("the service answered %d with no SOAP fault: %s",
			resp.Status, firstLine(resp.Body, 200))
	}

	return resp.Body, nil
}

// faultCountsAsSuccess reports whether a fault was configured as acceptable.
//
// Matched on the code and on the reason, because services are inconsistent about which one carries the meaning -
// some return a generic code with the useful text in the reason. Substring rather than exact, since the reason
// often includes an identifier that changes per message.
func (s *SOAPSender) faultCountsAsSuccess(fault *soap.Fault) bool {
	for _, accept := range s.cfg.FaultIsSuccess {
		accept = strings.TrimSpace(accept)
		if accept == "" {
			continue
		}
		if strings.Contains(fault.Code, accept) || strings.Contains(fault.Reason, accept) {
			return true
		}
	}
	return false
}

// Describe names the destination, never the password.
func (s *SOAPSender) Describe() string {
	if s.cfg.Action != "" {
		return fmt.Sprintf("%s (SOAP %s, %s)", s.cfg.URL, s.cfg.SOAPVersion(), s.cfg.Action)
	}
	return fmt.Sprintf("%s (SOAP %s)", s.cfg.URL, s.cfg.SOAPVersion())
}

// Close releases nothing that is not shared.
func (s *SOAPSender) Close() error { return nil }

// renderXMLTemplate substitutes message values, escaped for XML.
//
// Escaping happens here rather than being left to the template author, because an unescaped ampersand in a
// patient's name produces an envelope the service rejects as malformed - and the fault says the request was bad
// rather than naming the name.
func renderXMLTemplate(template string, msg *hl7.Message) (string, []string) {
	if strings.TrimSpace(template) == "" {
		return "", nil
	}

	var missing []string
	var b strings.Builder

	rest := template
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			b.WriteString(rest)
			break
		}

		b.WriteString(rest[:start])
		path := strings.TrimSpace(rest[start+2 : start+end])
		rest = rest[start+end+1:]

		value := msg.MustGet(path)
		if value == "" {
			missing = append(missing, path)
			continue
		}
		b.WriteString(soap.XMLEscape(value))
	}

	return b.String(), missing
}

func firstLine(body []byte, limit int) string {
	s := string(body)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	if s == "" {
		return "(no body)"
	}
	return s
}
