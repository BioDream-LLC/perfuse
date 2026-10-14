// Package iis speaks the transport every US immunization registry shares: the CDC's IIS SOAP web service.
//
// In 2011 an expert panel convened by the CDC and the American Immunization Registry Association chose one SOAP
// interface for submitting HL7 v2 messages to Immunization Information Systems, and the registries adopted it. The
// WSDL is small - two operations, connectivityTest and submitSingleMessage - and the HL7 message (a VXU to report a
// shot, a QBP^Q11 to ask for a patient's history) travels as escaped text inside the SOAP body. The answer comes back
// the same way: an ACK, or an RSP^K11 carrying the history and the forecast.
//
// The generic SOAP destination could be configured to do this, but every site would have to type the same envelope,
// get the namespace right, put the credentials in the body rather than in HTTP authentication, and then read the HL7
// acknowledgement out of the reply to learn whether the registry kept the record. A registry answers a rejected shot
// with HTTP 200 and an AR inside; treating the 200 as success is how immunizations silently fail to be reported. So
// this package does the CDC envelope, and judges the acknowledgement, once.
//
// What it does not do: the WSDL's username and password are the registry's application credentials and are sent as the
// WSDL says, in the body. Mutual TLS, where a registry requires it, is the destination's TLS setting as for any HTTPS
// call. Nothing here is specific to one state; where states differ (facility ID required or not, SOAP 1.1 accepted or
// not, warnings sent as AE) the destination's settings say so.
package iis

import (
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/soap"
)

// Namespace is the target namespace of the CDC WSDL.
const Namespace = "urn:cdc:iisb:2011"

// The SOAP actions the WSDL binds each operation to.
const (
	ActionSubmit       = "urn:cdc:iisb:2011:submitSingleMessage"
	ActionConnectivity = "urn:cdc:iisb:2011:connectivityTest"
)

// Profile is the name a channel uses to ask for this transport.
const Profile = "cdc-iis"

// WSDL is the service description, served by a Perfuse IIS endpoint at ?wsdl.
//
// Written to the CDC's published interface (same namespace, element and operation names, SOAP 1.2 binding) so a client
// generated from either one can call the other.
//
//go:embed cdc-iis.wsdl
var WSDL []byte

// SubmitBody is the Body content of a submitSingleMessage request.
//
// Every value is escaped. The HL7 message in particular has to be: its own encoding characters include an ampersand,
// so an unescaped message makes an envelope the registry rejects as malformed XML, and the fault says nothing about why.
func SubmitBody(username, password, facilityID string, message []byte) string {
	var b strings.Builder
	b.WriteString(`<submitSingleMessage xmlns="` + Namespace + `">`)
	b.WriteString("<username>" + soap.XMLEscape(username) + "</username>")
	b.WriteString("<password>" + soap.XMLEscape(password) + "</password>")
	// Omitted rather than sent empty. The WSDL makes it optional, and several registries reject an empty facilityID as
	// an unknown facility, where an absent one means "the account's default".
	if strings.TrimSpace(facilityID) != "" {
		b.WriteString("<facilityID>" + soap.XMLEscape(facilityID) + "</facilityID>")
	}
	b.WriteString("<hl7Message>" + soap.XMLEscape(string(message)) + "</hl7Message>")
	b.WriteString("</submitSingleMessage>")
	return b.String()
}

// ConnectivityBody is the Body content of a connectivityTest request.
func ConnectivityBody(echo string) string {
	return `<connectivityTest xmlns="` + Namespace + `"><echoBack>` + soap.XMLEscape(echo) + `</echoBack></connectivityTest>`
}

// ConnectivityAnswer is what a Perfuse endpoint returns to connectivityTest.
//
// The wording follows the CDC's reference implementation, which is what registry test harnesses compare against.
func ConnectivityAnswer(echo string) string {
	return "End Point is ready. Echo: " + echo
}

// ResponseBody wraps the answer to an operation in its response element.
func ResponseBody(operation, value string) string {
	return fmt.Sprintf(`<%sResponse xmlns="%s"><return>%s</return></%sResponse>`,
		operation, Namespace, soap.XMLEscape(value), operation)
}

// The fault elements the WSDL declares, each carrying Code, Reason and Detail.
const (
	FaultUnknown              = "fault"
	FaultUnsupportedOperation = "UnsupportedOperationFault"
	FaultSecurity             = "SecurityFault"
	FaultMessageTooLarge      = "MessageTooLargeFault"
)

// FaultEnvelope is a whole SOAP 1.2 fault response with the WSDL's typed detail.
//
// The typed detail matters: a client generated from the WSDL maps SecurityFault and MessageTooLargeFault to separate
// exceptions, and a generic fault reaches its code as "unknown", which is how a wrong password ends up retried
// all night.
func FaultEnvelope(sender bool, element string, code int, reason, detail string) string {
	value := "soap:Receiver"
	if sender {
		value = "soap:Sender"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body><soap:Fault>` +
		`<soap:Code><soap:Value>` + value + `</soap:Value></soap:Code>` +
		`<soap:Reason><soap:Text xml:lang="en">` + soap.XMLEscape(reason) + `</soap:Text></soap:Reason>` +
		`<soap:Detail><` + element + ` xmlns="` + Namespace + `">` +
		fmt.Sprintf("<Code>%d</Code>", code) +
		"<Reason>" + soap.XMLEscape(reason) + "</Reason>" +
		"<Detail>" + soap.XMLEscape(detail) + "</Detail>" +
		`</` + element + `></soap:Detail></soap:Fault></soap:Body></soap:Envelope>`
}

// Request is one inbound call, after the envelope has been opened.
type Request struct {
	// Operation is submitSingleMessage or connectivityTest.
	Operation string

	Username   string
	Password   string
	FacilityID string

	// Message is the HL7 message, with its carriage returns restored.
	Message []byte

	// Echo is connectivityTest's echoBack.
	Echo string
}

// ErrUnsupportedOperation means the body held neither operation the WSDL defines.
var ErrUnsupportedOperation = errors.New("iis: unsupported operation")

// ParseRequest reads the Body content of a request.
//
// Matched on local names, ignoring namespace prefixes. Real clients use every prefix there is, and a registry client
// generated by a toolkit that puts the namespace on each child is just as correct as one that sets it once.
func ParseRequest(body []byte) (*Request, error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	var op *Request
	var current string
	var text strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if op == nil {
				switch name {
				case "submitSingleMessage", "connectivityTest":
					op = &Request{Operation: name}
				case "Envelope", "Body", "Header":
				default:
					return nil, fmt.Errorf("%w %q: this endpoint answers submitSingleMessage and connectivityTest",
						ErrUnsupportedOperation, name)
				}
				continue
			}
			current = name
			text.Reset()
		case xml.CharData:
			if current != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if op == nil || current == "" || t.Name.Local != current {
				continue
			}
			value := text.String()
			switch current {
			case "username":
				op.Username = strings.TrimSpace(value)
			case "password":
				op.Password = value
			case "facilityID":
				op.FacilityID = strings.TrimSpace(value)
			case "hl7Message":
				op.Message = []byte(restoreSegments(value))
			case "echoBack":
				op.Echo = value
			}
			current = ""
		}
	}

	if op == nil {
		return nil, fmt.Errorf("%w: the SOAP Body is empty", ErrUnsupportedOperation)
	}
	if op.Operation == "submitSingleMessage" && len(strings.TrimSpace(string(op.Message))) == 0 {
		return nil, errors.New("iis: submitSingleMessage carries no hl7Message")
	}
	return op, nil
}

// ParseReturn takes the answer out of a response envelope: the text of the return element.
func ParseReturn(envelope []byte) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(envelope)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", errors.New("the registry's answer has no return element; it is not a CDC IIS response")
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "return" {
			continue
		}
		var value string
		if err := dec.DecodeElement(&value, &start); err != nil {
			return "", fmt.Errorf("the registry's return element could not be read: %w", err)
		}
		return value, nil
	}
}

// Ack is a registry's acknowledgement, judged.
type Ack struct {
	// Message is the HL7 message the registry returned, with carriage returns restored: an ACK, or an RSP for a query.
	Message []byte

	// Code is MSA-1.
	Code string

	// Problems are the ERR segments, one line each, severity first.
	Problems []string

	// Rejected is true when the registry did not keep the message.
	Rejected bool
}

// JudgeAck reads the HL7 message a registry returned and decides whether the record was kept.
//
// AA (or CA) is accepted. AR and CR are rejections. AE is the case the CDC guide makes subtle: registries send it both
// for "accepted, with warnings" and for "this part of the message was not stored", and the ERR segment's severity
// (ERR-4) says which. Any E severity means data was lost and is a rejection; only warnings and information is
// acceptance. warningsOnlyIsError treats any AE as a rejection, for the sites that want a person to look at every one.
func JudgeAck(returned string, warningsOnlyIsError bool) (*Ack, error) {
	raw := restoreSegments(returned)
	if !strings.HasPrefix(strings.TrimSpace(raw), "MSH") {
		return nil, fmt.Errorf("the registry answered with something that is not an HL7 message: %s", firstLine(raw))
	}

	msg, err := hl7.Parse([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("the registry's acknowledgement does not parse as HL7: %w", err)
	}

	ack := &Ack{Message: []byte(raw), Code: strings.ToUpper(strings.TrimSpace(msg.MustGet("MSA-1")))}

	severeError := false
	for _, seg := range msg.Segments("ERR") {
		severity := strings.ToUpper(strings.TrimSpace(fieldText(seg.Field(4))))
		if severity == "E" || severity == "" {
			// An ERR with no severity is read as an error. The 2.5.1 guide requires ERR-4, so an empty one is from an
			// older registry, where every ERR was an error.
			severeError = true
		}
		ack.Problems = append(ack.Problems, describeErr(seg, severity))
	}

	switch ack.Code {
	case "AA", "CA":
	case "AR", "CR":
		ack.Rejected = true
	case "AE", "CE":
		ack.Rejected = severeError || warningsOnlyIsError
	case "":
		return nil, errors.New("the registry's answer has no MSA-1, so it cannot be told whether the record was kept")
	default:
		return nil, fmt.Errorf("the registry answered with acknowledgement code %q, which HL7 does not define", ack.Code)
	}
	return ack, nil
}

// Error describes a rejection.
func (a *Ack) Error() string {
	detail := "no ERR segment says why"
	if len(a.Problems) > 0 {
		detail = strings.Join(a.Problems, "; ")
	}
	return fmt.Sprintf("the registry did not keep the message (MSA-1 %s): %s", a.Code, detail)
}

func describeErr(seg hl7.Segment, severity string) string {
	location := fieldText(seg.Field(2))
	code := fieldText(seg.Field(3))
	text := fieldText(seg.Field(8))
	if text == "" {
		text = fieldText(seg.Field(7))
	}
	var parts []string
	if severity == "" {
		severity = "E"
	}
	parts = append(parts, severity)
	if location != "" {
		parts = append(parts, "at "+location)
	}
	if code != "" {
		parts = append(parts, code)
	}
	if text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

// fieldText renders a field's components as readable text.
func fieldText(v hl7.Value) string {
	if !v.Exists() {
		return ""
	}
	n := v.Repeat(1).ComponentCount()
	if n <= 1 {
		return strings.TrimSpace(v.Repeat(1).String())
	}
	var parts []string
	for i := 1; i <= n; i++ {
		if c := strings.TrimSpace(v.Repeat(1).Component(i).String()); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, " ")
}

// restoreSegments turns the LF an XML parser leaves behind back into HL7's CR, ending with exactly one.
//
// XML requires a parser to normalise a literal CR in character data to LF, so a message carried in an element arrives
// with every separator changed. Restoring CR is what HL7 specifies, so it is a repair, never a guess.
func restoreSegments(s string) string {
	s = strings.Trim(s, " \t")
	s = strings.ReplaceAll(s, "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	s = strings.Trim(s, "\r \t")
	if s == "" {
		return s
	}
	return s + "\r"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	if s == "" {
		return "(nothing)"
	}
	return s
}
