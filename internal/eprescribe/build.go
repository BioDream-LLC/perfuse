package eprescribe

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// Namespace is the SCRIPT XML namespace.
//
// Written on output but not required on input. Real senders differ on whether they qualify elements, and
// rejecting an unqualified message would refuse valid prescriptions over a prefix.
const Namespace = "http://www.ncpdp.org/schema/SCRIPT"

// DefaultVersion and DefaultRelease describe SCRIPT 10.6, which remains the version most widely deployed.
const (
	DefaultVersion = "010"
	DefaultRelease = "006"
)

func marshal(m Message) ([]byte, error) {
	// Version and release are filled in rather than left blank. A receiver uses them to decide how to read
	// the message, and an absent version has it guess - usually at whatever it deployed most recently.
	if m.Version == "" {
		m.Version = DefaultVersion
	}
	if m.Release == "" {
		m.Release = DefaultRelease
	}

	body, err := xml.MarshalIndent(outMessage{
		Namespace: Namespace,
		Version:   m.Version,
		Release:   m.Release,
		Header:    m.Header,
		Body:      m.Body,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("writing SCRIPT: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.Write(body)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// outMessage exists so the namespace attribute is written without putting it on the parse struct, where
// it would make an unqualified incoming message fail to match.
type outMessage struct {
	XMLName   xml.Name `xml:"Message"`
	Namespace string   `xml:"xmlns,attr"`
	Version   string   `xml:"version,attr"`
	Release   string   `xml:"release,attr"`
	Header    Header   `xml:"Header"`
	Body      Body     `xml:"Body"`
}
