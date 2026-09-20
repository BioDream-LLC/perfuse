package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/soap"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// SOAPSource accepts messages wrapped in a SOAP envelope.
//
// The counterpart to the SOAP destination, and the rarer direction: most hospital integration has Perfuse calling a
// service rather than being one. It exists because Mirth's Web Service Listener does, and a site whose sending system
// only speaks SOAP cannot migrate without it.
//
// Deliberately not a general web services framework. It accepts an envelope, takes the message out of it, and answers
// with an envelope. There is no WS-Security, no MTOM, no WS-Addressing, and no generated bindings - the same
// omissions as the destination, for the same reason: a half-implementation of WS-Security is worse than none.
type SOAPSource struct {
	// Listen is the address to serve on. Required.
	Listen string `yaml:"listen"`

	// Path is the URL path to accept envelopes on. Defaults to "/".
	Path string `yaml:"path,omitempty"`

	// Version is the SOAP version to answer in, "1.1" or "1.2". Defaults to 1.1.
	//
	// Requests are accepted in either version regardless, because a sender that gets this wrong is common and
	// rejecting it teaches nobody anything. The setting controls the reply, where getting it wrong means the sender
	// cannot read the answer.
	Version string `yaml:"version,omitempty"`

	// Element names the element inside the SOAP Body holding the message. Empty means the whole body text.
	//
	// Named rather than guessed because a body may carry several elements - a message alongside a sender identifier
	// and a timestamp - and taking the whole body would hand the engine an XML fragment where it expected HL7.
	Element string `yaml:"element,omitempty"`

	// Base64 decodes the element's content before treating it as a message.
	//
	// Common in real services, because HL7 carries carriage returns and callers who did not think about that end up
	// with XML that mangles them. If the content decodes as base64 but was not meant to be, the result is not a
	// message and the engine says so - which is why this is explicit rather than sniffed.
	Base64 bool `yaml:"base64,omitempty"`

	// ResponseElement names the element wrapping the acknowledgement in the reply. Defaults to "AckResponse".
	ResponseElement string `yaml:"response_element,omitempty"`

	// ResponseNamespace is the namespace for the response element, if the caller needs one.
	ResponseNamespace string `yaml:"response_namespace,omitempty"`

	// WSDL is a file served at the listen path with ?wsdl. Empty means none is served.
	//
	// A file rather than something generated. A generated WSDL would describe what this endpoint accepts, which is
	// almost never what the sending system was built against - and a WSDL that is nearly right costs more time than
	// no WSDL at all, because it looks authoritative.
	WSDL string `yaml:"wsdl,omitempty"`

	// TLS encrypts inbound connections and can require a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// Token, when set, is required in an Authorization: Bearer header.
	Token string `yaml:"token,omitempty"`

	// Username and Password, when set, require HTTP basic authentication.
	//
	// Offered alongside a token because SOAP callers overwhelmingly expect basic authentication, and a site whose
	// sending system cannot be changed needs the option it already has.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// MaxMessageSize bounds an inbound body. Zero applies a default.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// ReadTimeout bounds reading a request.
	ReadTimeout time.Duration `yaml:"read_timeout,omitempty"`

	// Ack controls the acknowledgement placed in the response.
	Ack Ack `yaml:"ack,omitempty"`

	// FaultOnNak answers a negative acknowledgement with a SOAP fault instead of a normal response carrying the NAK.
	//
	// Off by default, which is the right default and worth explaining. HL7 already has a way to say "I received this
	// and rejected it", and that is a NAK - a successful exchange reporting a rejected message. Turning the same
	// thing into a transport fault tells the sender its request was malformed, which it was not, and many clients
	// respond by retrying forever.
	//
	// It exists because some callers only look at the HTTP status and would otherwise treat a rejection as success.
	FaultOnNak bool `yaml:"fault_on_nak,omitempty"`
}

// SOAPPath returns the path to serve, defaulted.
func (s *SOAPSource) SOAPPath() string {
	if s == nil || s.Path == "" {
		return "/"
	}
	if !strings.HasPrefix(s.Path, "/") {
		return "/" + s.Path
	}
	return s.Path
}

// SOAPVersion returns the version to answer in, defaulted.
func (s *SOAPSource) SOAPVersion() soap.Version {
	if s != nil && s.Version == "1.2" {
		return soap.V12
	}
	return soap.V11
}

// ResponseElementName returns the response wrapper element, defaulted.
func (s *SOAPSource) ResponseElementName() string {
	if s == nil || strings.TrimSpace(s.ResponseElement) == "" {
		return "AckResponse"
	}
	return strings.TrimSpace(s.ResponseElement)
}

func (s *SOAPSource) validate() []error {
	if s == nil {
		return []error{fmt.Errorf("a soap source needs a soap block")}
	}

	var errs []error

	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, fmt.Errorf("a soap source needs a listen address"))
	}

	switch strings.TrimSpace(s.Version) {
	case "", "1.1", "1.2":
	default:
		errs = append(errs, fmt.Errorf("soap version %q is not one this understands; use 1.1 or 1.2", s.Version))
	}

	if s.Username != "" && s.Password == "" {
		errs = append(errs, fmt.Errorf("the soap source has a username but no password; half a credential will "+
			"reject every request and read to the caller as an outage"))
	}
	if s.Password != "" && s.Username == "" {
		errs = append(errs, fmt.Errorf("the soap source has a password but no username"))
	}

	if s.Token != "" && s.Username != "" {
		// Refused rather than resolved by precedence. Two mechanisms configured means somebody expects one of them
		// to be enforced, and silently ignoring either is how an endpoint ends up less protected than its file says.
		errs = append(errs, fmt.Errorf("the soap source has both a token and a username; configure one, because "+
			"whichever were ignored would leave the endpoint less protected than this file suggests"))
	}

	if s.Base64 && strings.TrimSpace(s.Element) == "" {
		// Base64-decoding a whole body would include the surrounding XML of any sibling elements, which cannot
		// decode - so this combination is always a mistake rather than sometimes one.
		errs = append(errs, fmt.Errorf("the soap source sets base64 but names no element, so it would try to "+
			"decode the whole body including any XML around the message; name the element holding it"))
	}

	if s.MaxMessageSize < 0 {
		errs = append(errs, fmt.Errorf("max_message_size cannot be negative"))
	}

	return errs
}
