package engine

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/soap"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// defaultSOAPMaxBody bounds an inbound envelope when nothing else says.
//
// Generous compared with MLLP, because a SOAP caller sending a document base64-encoded inside XML is the reason this
// transport gets chosen at all, and base64 costs a third on top.
const defaultSOAPMaxBody = 32 << 20

// startSOAPSource serves a SOAP endpoint for this channel.
func (c *Channel) startSOAPSource() error {
	src := c.cfg.Source.SOAP
	if src == nil {
		return errors.New("a soap source needs a soap block")
	}

	maxSize := src.MaxMessageSize
	if maxSize <= 0 {
		maxSize = defaultSOAPMaxBody
	}

	tlsCfg, err := tlsconf.ForListener(src.TLS)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}

	if src.Token == "" && src.Username == "" {
		// Warned at every start rather than once at load, exactly like the HTTP source. An unauthenticated endpoint
		// accepting clinical messages is the kind of thing set up for a test and then forgotten.
		c.log.Warn("soap source", "detail",
			"this endpoint accepts envelopes from anyone that can reach it; set a token or a username")
	}

	readTimeout := src.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 30 * time.Second
	}

	mux := http.NewServeMux()
	mux.HandleFunc(src.SOAPPath(), c.handleSOAPRequest(src, maxSize))

	srv := &http.Server{
		Addr:              src.Listen,
		Handler:           mux,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		// Generous, for the same reason as the HTTP source: the response is not written until the message has been
		// delivered, so a slow downstream receiver must not look like a timeout to the sender.
		WriteTimeout: 5 * time.Minute,
	}

	ln, err := net.Listen("tcp", src.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}

	c.httpServer = srv
	c.log.Info("soap listening",
		"addr", src.Listen, "path", src.SOAPPath(), "version", src.SOAPVersion(),
		"tls", tlsCfg != nil, "authenticated", src.Token != "" || src.Username != "")

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("the soap listener stopped", "error", err)
		}
	}()

	return nil
}

// handleSOAPRequest accepts one envelope.
func (c *Channel) handleSOAPRequest(src *config.SOAPSource, maxSize int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A WSDL request is a GET and must be answered before anything else, because a caller fetching a WSDL has no
		// envelope to send and refusing it with "method not allowed" tells them nothing.
		if r.Method == http.MethodGet && r.URL.RawQuery != "" && strings.EqualFold(r.URL.RawQuery, "wsdl") {
			c.serveWSDL(w, r, src)
			return
		}

		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "this endpoint accepts SOAP requests by POST", http.StatusMethodNotAllowed)
			return
		}

		if !c.soapAuthorised(r, src) {
			// A 401 with the scheme named, rather than a fault. Authentication is a transport concern, and a caller
			// that gets a SOAP fault about credentials has to parse XML to discover it needs to log in.
			if src.Username != "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="perfuse"`)
			}
			http.Error(w, "not authorised", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxSize)+1))
		if err != nil {
			c.writeSOAPFault(w, src, soap.ClientFault,
				"the request body could not be read: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > maxSize {
			// A client fault, not a server one: the caller sent something too large, and telling it so is what stops
			// it retrying the same oversized request forever.
			c.writeSOAPFault(w, src, soap.ClientFault,
				fmt.Sprintf("the envelope is larger than the %d byte limit this endpoint accepts", maxSize),
				http.StatusRequestEntityTooLarge)
			return
		}

		raw, err := extractFromEnvelope(body, src)
		if err != nil {
			// Reported as the caller's fault, because it is: the envelope arrived and did not contain what this
			// endpoint was configured to expect. Saying which element was expected is the difference between a
			// fixable error and a support call.
			c.writeSOAPFault(w, src, soap.ClientFault, err.Error(), http.StatusBadRequest)
			return
		}

		ack, err := c.handle(r.Context(), raw)
		if err != nil {
			c.writeSOAPFault(w, src, soap.ServerFault,
				"the message could not be processed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// A NAK is a successful exchange reporting a rejected message, so it goes back as a normal response unless
		// the site has said otherwise. HL7 already has a way to say "received and rejected", and turning that into a
		// transport fault tells the sender its request was malformed when it was not - and many clients then retry
		// forever.
		if src.FaultOnNak && isNegativeAck(ack) {
			c.writeSOAPFault(w, src, soap.ClientFault, string(ack), http.StatusInternalServerError)
			return
		}

		c.writeSOAPResponse(w, src, ack)
	}
}

// soapAuthorised checks whichever mechanism is configured.
//
// Constant-time comparison on both, so a caller cannot learn a token or a password by timing repeated attempts.
func (c *Channel) soapAuthorised(r *http.Request, src *config.SOAPSource) bool {
	if src.Token != "" {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
			return false
		}
		got := strings.TrimSpace(header[len(prefix):])
		return subtle.ConstantTimeCompare([]byte(got), []byte(src.Token)) == 1
	}

	if src.Username != "" {
		user, pass, ok := r.BasicAuth()
		if !ok {
			return false
		}
		// Both compared even when the username is already wrong, so the reply takes the same time either way.
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(src.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(src.Password)) == 1
		return userOK && passOK
	}

	// No authentication configured. Reported as a warning at load, exactly like the HTTP source, rather than being
	// silently permitted here.
	return true
}

// envelope is the minimum needed to find a body. Namespace-agnostic on purpose: real callers use every prefix
// imaginable, and a parser that insisted on one would reject perfectly good requests from half of them.
type envelope struct {
	Body struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"Body"`
}

// extractFromEnvelope takes the message out of a SOAP request.
func extractFromEnvelope(body []byte, src *config.SOAPSource) ([]byte, error) {
	var env envelope
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	// Namespaces are not enforced, so Envelope and Body match whatever prefix the caller used.
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("this does not parse as a SOAP envelope: %w", err)
	}

	inner := strings.TrimSpace(string(env.Body.Inner))
	if inner == "" {
		return nil, errors.New("the SOAP Body is empty, so there is no message in this request")
	}

	content := inner
	if name := strings.TrimSpace(src.Element); name != "" {
		found, ok := innerTextOf(inner, name)
		if !ok {
			// Naming both what was wanted and what arrived, because the commonest cause is a namespace prefix or a
			// spelling difference, and neither is guessable from "element not found".
			return nil, fmt.Errorf("the SOAP Body does not contain a %s element; it contains %s",
				name, describeElements(inner))
		}
		content = found
	}

	content = trimXMLPadding(unescapeXML(content))

	if src.Base64 {
		decoded, err := base64.StdEncoding.DecodeString(stripWhitespace(content))
		if err != nil {
			return nil, fmt.Errorf("the %s element is configured as base64 but did not decode: %w",
				src.Element, err)
		}
		return decoded, nil
	}

	// Carriage returns are restored, and this is not optional tidying.
	//
	// The XML specification requires a parser to normalise a literal carriage return in character data to a line
	// feed. So an HL7 message placed directly into an element arrives with every segment separator converted from CR
	// to LF - verified by sending one through a real client and reading the delivered bytes. Perfuse's own parser
	// accepts LF, so the message routes correctly and the problem is invisible here; it surfaces at the far end,
	// where a receiver expecting the standard separator rejects a message Perfuse considered fine.
	//
	// Restoring CR is safe rather than presumptuous: CR is the separator HL7 specifies, so a sender that genuinely
	// used LF was already non-conformant and is improved by the change.
	//
	// Not done for base64 content, where the bytes are exact by construction and altering them would corrupt a
	// payload that deliberately carried its own line endings. That is the reason real services base64-encode, and the
	// configuration says so.
	return []byte(restoreSegmentTerminators(content)), nil
}

// restoreSegmentTerminators turns LF and CRLF back into CR, and guarantees exactly one at the end.
//
// The trailing terminator has to be reconstructed rather than preserved, because XML indentation and a message's own
// final separator are the same character by the time a parser has finished with it - there is no way to tell them
// apart. HL7 requires the last segment to be terminated, so ending with exactly one CR is correct whichever it was.
func restoreSegmentTerminators(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")

	// Trailing separators collapse to one. Several would be read as empty segments, and an empty segment in the
	// middle of nothing is the kind of thing a strict receiver rejects for no reason anybody can explain.
	s = strings.TrimRight(s, "\r")
	if s == "" {
		return s
	}
	return s + "\r"
}

// innerTextOf returns the contents of the first element with the given local name.
//
// Written by hand rather than with a struct, because the element name is configuration and its namespace is unknown.
// Matching on the local name means a caller using ns:Message and one using Message both work, which they must.
func innerTextOf(fragment, localName string) (string, bool) {
	dec := xml.NewDecoder(strings.NewReader(fragment))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		start, ok := tok.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, localName) {
			continue
		}
		var inner string
		if err := dec.DecodeElement(&inner, &start); err != nil {
			return "", false
		}
		return inner, true
	}
}

// describeElements lists the element names present, for an error message.
func describeElements(fragment string) string {
	dec := xml.NewDecoder(strings.NewReader(fragment))
	var names []string
	seen := map[string]bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && !seen[start.Name.Local] {
			seen[start.Name.Local] = true
			names = append(names, start.Name.Local)
			if len(names) >= 8 {
				break
			}
		}
	}
	if len(names) == 0 {
		return "no elements"
	}
	return strings.Join(names, ", ")
}

// unescapeXML turns escaped markup back into text.
//
// Needed because a caller placing an HL7 message in an element has to escape ampersands, and HL7's own encoding
// characters include one - so &amp; arriving as a literal would corrupt every message using an escape sequence.
func unescapeXML(s string) string {
	var out string
	if err := xml.Unmarshal([]byte("<x>"+s+"</x>"), &out); err == nil {
		return out
	}
	// Falls back to the raw text rather than failing: a fragment that will not parse as a single text node is
	// usually one that was never escaped, and handing it over unchanged is better than rejecting it.
	return s
}

// trimXMLPadding removes formatting whitespace without touching HL7's own segment terminator.
//
// strings.TrimSpace cannot be used here, and this is not a nicety. HL7 separates segments with a carriage return, and
// a well-formed message ends with one; XML indentation uses spaces, tabs and newlines. Trimming carriage returns
// alongside the rest strips the final segment terminator, which strict receivers reject and lenient ones accept while
// merging the last segment with whatever follows.
//
// So: spaces, tabs and newlines go, carriage returns stay.
func trimXMLPadding(s string) string {
	return strings.Trim(s, " \t\n")
}

func stripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

// isNegativeAck reports whether an acknowledgement rejects the message.
func isNegativeAck(ack []byte) bool {
	m, err := hl7.Parse(ack)
	if err != nil {
		return false
	}
	code := m.MustGet("MSA-1")
	return strings.HasPrefix(strings.ToUpper(code), "AE") || strings.HasPrefix(strings.ToUpper(code), "AR")
}

// writeSOAPResponse wraps an acknowledgement in an envelope.
func (c *Channel) writeSOAPResponse(w http.ResponseWriter, src *config.SOAPSource, ack []byte) {
	element := src.ResponseElementName()

	var attrs string
	if ns := strings.TrimSpace(src.ResponseNamespace); ns != "" {
		attrs = fmt.Sprintf(` xmlns="%s"`, soap.XMLEscape(ns))
	}

	// The acknowledgement is escaped, not wrapped in CDATA. CDATA cannot contain its own terminator, and while that
	// sequence is unlikely in an ACK, "unlikely" is how corrupt responses happen.
	body := fmt.Sprintf("<%s%s>%s</%s>", element, attrs, soap.XMLEscape(string(ack)), element)

	w.Header().Set("Content-Type", soap.ContentType(src.SOAPVersion(), ""))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(soap.ResponseEnvelope(src.SOAPVersion(), body))); err != nil {
		c.log.Warn("could not write the SOAP response", "err", err)
	}
}

// writeSOAPFault answers with a fault.
func (c *Channel) writeSOAPFault(w http.ResponseWriter, src *config.SOAPSource, kind soap.FaultKind, reason string, status int) {
	w.Header().Set("Content-Type", soap.ContentType(src.SOAPVersion(), ""))
	w.WriteHeader(status)
	if _, err := w.Write([]byte(soap.FaultEnvelope(src.SOAPVersion(), kind, reason))); err != nil {
		c.log.Warn("could not write the SOAP fault", "err", err)
	}
}

// serveWSDL returns the configured WSDL file.
func (c *Channel) serveWSDL(w http.ResponseWriter, r *http.Request, src *config.SOAPSource) {
	if strings.TrimSpace(src.WSDL) == "" {
		// Said plainly rather than answered with a 404, because a caller fetching ?wsdl and getting "not found" will
		// assume the endpoint is wrong rather than that no WSDL was published.
		http.Error(w,
			"this endpoint does not publish a WSDL; it accepts a SOAP envelope containing an HL7 message",
			http.StatusNotFound)
		return
	}

	data, err := os.ReadFile(src.WSDL) // #nosec G304 - an operator-supplied path from the channel file
	if err != nil {
		c.log.Error("could not read the WSDL", "err", err, "path", src.WSDL)
		http.Error(w, "the WSDL could not be read", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		c.log.Warn("could not write the WSDL", "err", err)
	}
	_ = r
}

// stopSOAPSource shuts the listener down.
func (c *Channel) stopSOAPSource(ctx context.Context) error {
	return c.stopHTTPSource(ctx)
}
