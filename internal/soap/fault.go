package soap

import "fmt"

// FaultKind is whose fault a failure was.
//
// The distinction is not cosmetic. A client fault tells the caller its request was wrong and it should not repeat it
// unchanged; a server fault says the request was fine and retrying later may work. Getting it backwards either sends
// somebody hunting for a bug in a correct request, or has a client retry a malformed one forever.
type FaultKind string

const (
	// ClientFault means the caller sent something this endpoint cannot accept.
	ClientFault FaultKind = "client"
	// ServerFault means the request was acceptable and processing it failed.
	ServerFault FaultKind = "server"
)

// code11 returns the SOAP 1.1 faultcode value.
//
// The 1.1 codes are the literal strings Client and Server, prefixed with the envelope namespace prefix. The 1.2 names
// are Sender and Receiver instead, which is one of several places the two versions say the same thing differently.
func (k FaultKind) code11() string {
	if k == ServerFault {
		return "soap:Server"
	}
	return "soap:Client"
}

func (k FaultKind) code12() string {
	if k == ServerFault {
		return "soap:Receiver"
	}
	return "soap:Sender"
}

// ContentType returns the content type for a response.
//
// In 1.2 the action travels as a parameter of the content type; in 1.1 it is a separate SOAPAction header. A response
// has no action, so the parameter is only emitted when one is given - which it is not, for a reply.
func ContentType(version Version, action string) string {
	if version == V12 {
		if action != "" {
			return fmt.Sprintf(`application/soap+xml; charset=utf-8; action="%s"`, XMLEscape(action))
		}
		return "application/soap+xml; charset=utf-8"
	}
	return "text/xml; charset=utf-8"
}

// ResponseEnvelope wraps a body in an envelope for a reply.
//
// Separate from Envelope, which builds a request and can carry headers. A reply never needs headers here, and having
// one function do both would mean a caller passing an empty header string on every use.
func ResponseEnvelope(version Version, body string) string {
	ns := ns11
	if version == V12 {
		ns = ns12
	}
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<soap:Envelope xmlns:soap="%s"><soap:Body>%s</soap:Body></soap:Envelope>`,
		ns, body)
}

// FaultEnvelope builds a fault response.
//
// The two versions differ completely in structure, not just in names: 1.1 has faultcode and faultstring as bare
// children of Fault, while 1.2 nests Code/Value and Reason/Text. A caller given the wrong shape sees a fault it cannot
// parse, and reports "no fault" - which is the worst outcome, because a rejection reads as a success.
func FaultEnvelope(version Version, kind FaultKind, reason string) string {
	escaped := XMLEscape(collapse(reason))

	if version == V12 {
		body := fmt.Sprintf(
			`<soap:Fault><soap:Code><soap:Value>%s</soap:Value></soap:Code>`+
				`<soap:Reason><soap:Text xml:lang="en">%s</soap:Text></soap:Reason></soap:Fault>`,
			kind.code12(), escaped)
		return ResponseEnvelope(version, body)
	}

	body := fmt.Sprintf(
		`<soap:Fault><faultcode>%s</faultcode><faultstring>%s</faultstring></soap:Fault>`,
		kind.code11(), escaped)
	return ResponseEnvelope(version, body)
}
