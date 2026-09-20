package config

import (
	"strings"
	"testing"
)

func soapChannel(t *testing.T, dest *SOAPDestination) *Channel {
	t.Helper()
	return &Channel{
		Name:   "to-the-registry",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{
			Name: "registry", Type: DestinationSOAP, SOAP: dest,
		}},
	}
}

func TestAValidSOAPDestinationLoads(t *testing.T) {
	c := soapChannel(t, &SOAPDestination{
		URL:    "https://registry.nhs.uk/PatientService",
		Action: "urn:SubmitPatient",
		Body:   "<SubmitPatient><Mrn>${PID-3.1}</Mrn></SubmitPatient>",
	})
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid SOAP destination was refused: %v", err)
	}
}

func TestVersion11IsTheDefault(t *testing.T) {
	// Still the commonest in hospital middleware by a wide margin, and a 1.2 envelope sent to a 1.1 service is
	// rejected with a fault about the version rather than the message.
	c := soapChannel(t, &SOAPDestination{
		URL: "https://x/y", Body: "<Submit/>",
	})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.Destinations[0].SOAP.SOAPVersion(); string(got) != "1.1" {
		t.Errorf("version = %q, want 1.1 by default", got)
	}
}

func TestABodyThatAlreadyContainsAnEnvelopeIsRefused(t *testing.T) {
	// The commonest mistake when somebody copies a request out of a capture or a vendor document. Double-wrapping
	// produces a fault about the request structure, which reads as a service problem and sends people to the
	// wrong place entirely.
	c := soapChannel(t, &SOAPDestination{
		URL: "https://x/y",
		Body: `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<soap:Body><Submit/></soap:Body></soap:Envelope>`,
	})

	err := c.Validate()
	if err == nil {
		t.Fatal("a body containing an envelope was accepted")
	}
	if !strings.Contains(err.Error(), "double-wrapping") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
	if !strings.Contains(err.Error(), "added for you") {
		t.Errorf("the error does not say what the setting actually is: %v", err)
	}
}

func TestAMissingBodySaysWhyThereIsNoDefault(t *testing.T) {
	// The element names come from the service's own schema, so no engine can guess them - and saying so stops
	// somebody hunting for a default they will not find.
	c := soapChannel(t, &SOAPDestination{URL: "https://x/y"})

	err := c.Validate()
	if err == nil {
		t.Fatal("a SOAP destination with no body was accepted")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the error does not explain why there is no default: %v", err)
	}
}

func TestAMissingURLIsRefused(t *testing.T) {
	if err := soapChannel(t, &SOAPDestination{Body: "<Submit/>"}).Validate(); err == nil {
		t.Fatal("a SOAP destination with no URL was accepted")
	}
}

func TestAnUnknownVersionIsRefusedWithTheOptions(t *testing.T) {
	c := soapChannel(t, &SOAPDestination{
		URL: "https://x/y", Body: "<Submit/>", Version: "2.0",
	})

	err := c.Validate()
	if err == nil {
		t.Fatal("an unknown SOAP version was accepted")
	}
	if !strings.Contains(err.Error(), "1.2") {
		t.Errorf("the error does not list the valid versions: %v", err)
	}
}

func TestASOAPDestinationWithNoBlockIsRefused(t *testing.T) {
	c := &Channel{
		Name:         "to-the-registry",
		Source:       Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{Name: "registry", Type: DestinationSOAP}},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("a soap destination with no soap block was accepted")
	}
	if !strings.Contains(err.Error(), "soap block") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestFaultIsSuccessIsAllowed(t *testing.T) {
	// A service answering a resend with "already submitted" is telling you the message arrived, and without this
	// a site watches a queue retry forever against a receiver that already has it.
	c := soapChannel(t, &SOAPDestination{
		URL: "https://x/y", Body: "<Submit/>",
		FaultIsSuccess: []string{"already submitted", "DuplicateSubmission"},
	})
	if err := c.Validate(); err != nil {
		t.Fatalf("fault_is_success was refused: %v", err)
	}
}
