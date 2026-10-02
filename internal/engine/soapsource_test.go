package engine

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/soap"
)

func envelopeAround(body string) []byte {
	return []byte(`<?xml version="1.0"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">` +
		`<soap:Body>` + body + `</soap:Body></soap:Envelope>`)
}

const sampleMessage = "MSH|^~\\&|A|B|C|D|20260821||ADT^A01|1|P|2.5.1\rPID|1||MRN1^^^A^MR||FROST^IVY\r"

func TestAMessageInANamedElementIsExtracted(t *testing.T) {
	src := &config.SOAPSource{Element: "Message"}
	body := envelopeAround("<Message>" + soap.XMLEscape(sampleMessage) + "</Message>")

	got, err := extractFromEnvelope(body, src)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sampleMessage {
		t.Errorf("extracted %q", string(got))
	}
}

func TestAnyNamespacePrefixWorks(t *testing.T) {
	// Real callers use every prefix imaginable and some use none. A parser insisting on one would reject perfectly
	// good requests from half of them, and the rejection would look like a Perfuse bug because the request is valid.
	for _, prefix := range []string{"soap", "soapenv", "SOAP-ENV", "s", ""} {
		open, close := "soap:", "soap:"
		ns := `xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"`
		if prefix == "" {
			open, close = "", ""
			ns = `xmlns="http://schemas.xmlsoap.org/soap/envelope/"`
		} else {
			open, close = prefix+":", prefix+":"
			ns = `xmlns:` + prefix + `="http://schemas.xmlsoap.org/soap/envelope/"`
		}

		body := []byte(`<` + open + `Envelope ` + ns + `><` + open + `Body>` +
			`<Message>` + soap.XMLEscape(sampleMessage) + `</Message>` +
			`</` + close + `Body></` + close + `Envelope>`)

		got, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message"})
		if err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
			continue
		}
		if string(got) != sampleMessage {
			t.Errorf("prefix %q extracted %q", prefix, string(got))
		}
	}
}

func TestEscapedAmpersandsSurvive(t *testing.T) {
	// HL7's own encoding characters include an ampersand, so a caller placing a message in an element must escape it.
	// If &amp; arrives as a literal, every message using a subcomponent separator is corrupted - silently, and in a
	// way that looks like the sender's fault.
	withAmp := "MSH|^~\\&|A|B|C|D|20260821||ADT^A01|1|P|2.5.1\rPID|1||MRN1||SMITH^JOHN^^^DR&MD\r"
	body := envelopeAround("<Message>" + soap.XMLEscape(withAmp) + "</Message>")

	got, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != withAmp {
		t.Errorf("the ampersand did not survive: %q", string(got))
	}
	if strings.Contains(string(got), "&amp;") {
		t.Error("the message still contains an escaped ampersand, so it was not unescaped")
	}
}

func TestXMLNormalisedLineFeedsBecomeCarriageReturnsAgain(t *testing.T) {
	// Found by sending a real envelope through a running server and reading the delivered bytes.
	//
	// The XML specification requires a parser to normalise a literal carriage return in character data to a line
	// feed, so an HL7 message placed directly into an element arrives with every segment separator changed. Perfuse's
	// parser accepts LF, so the message routes fine and the damage is invisible here - it surfaces at the far end,
	// where a receiver expecting the standard separator rejects a message this engine considered correct.
	body := envelopeAround("<Message>MSH|^~\\&amp;|A|B|C|D|1||ADT^A01|1|P|2.5.1\nPID|1||MRN1\n</Message>")

	got, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(got), "\n") {
		t.Errorf("line feeds survived extraction: %q", string(got))
	}
	if n := strings.Count(string(got), "\r"); n != 2 {
		t.Errorf("found %d carriage returns, want 2 - one per segment", n)
	}
	if !strings.HasSuffix(string(got), "\r") {
		t.Error("the final segment terminator is missing, which strict receivers reject")
	}
}

func TestBase64BytesAreNeverRewritten(t *testing.T) {
	// The reason real services base64-encode is to carry exact bytes. Restoring carriage returns in decoded content
	// would corrupt a payload that deliberately contained line feeds of its own.
	payload := "line one\nline two\n"
	encoded := "bGluZSBvbmUKbGluZSB0d28K"

	got, err := extractFromEnvelope(
		envelopeAround("<Message>"+encoded+"</Message>"),
		&config.SOAPSource{Element: "Message", Base64: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("decoded content was rewritten: %q", string(got))
	}
}

func TestBase64ContentIsDecoded(t *testing.T) {
	// Common in real services, because HL7 carries carriage returns and callers who did not think about that end up
	// with XML that mangles them.
	encoded := "TVNIfF5ee1x8QQ==" // not the real message; only the decode path is under test
	body := envelopeAround("<Message>" + encoded + "</Message>")

	_, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message", Base64: true})
	if err != nil {
		t.Fatalf("valid base64 was rejected: %v", err)
	}
}

func TestBase64ThatDoesNotDecodeSaysSo(t *testing.T) {
	body := envelopeAround("<Message>this is not base64 at all!!</Message>")

	_, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message", Base64: true})
	if err == nil {
		t.Fatal("content that is not base64 was accepted as base64")
	}
	if !strings.Contains(err.Error(), "did not decode") {
		t.Errorf("the error does not say what went wrong: %v", err)
	}
}

func TestAMissingElementNamesWhatArrivedInstead(t *testing.T) {
	// The commonest cause is a spelling or namespace difference, and neither is guessable from "element not found".
	body := envelopeAround("<HL7Message>x</HL7Message><Sender>lab</Sender>")

	_, err := extractFromEnvelope(body, &config.SOAPSource{Element: "Message"})
	if err == nil {
		t.Fatal("a body without the named element was accepted")
	}
	if !strings.Contains(err.Error(), "HL7Message") {
		t.Errorf("the error does not say what the body actually contains: %v", err)
	}
}

func TestAnEmptyBodyIsRefused(t *testing.T) {
	_, err := extractFromEnvelope(envelopeAround(""), &config.SOAPSource{})
	if err == nil {
		t.Fatal("an empty SOAP body was accepted")
	}
	if !strings.Contains(err.Error(), "no message") {
		t.Errorf("the error does not explain: %v", err)
	}
}

func TestSomethingThatIsNotAnEnvelopeIsRefused(t *testing.T) {
	_, err := extractFromEnvelope([]byte("just some text"), &config.SOAPSource{})
	if err == nil {
		t.Fatal("a non-envelope was accepted")
	}
}

func TestFaultShapesDifferBetweenVersions(t *testing.T) {
	// The two versions differ in structure, not only in names. A caller given the wrong shape reports "no fault",
	// which means a rejection reads as a success - the worst available outcome.
	v11 := soap.FaultEnvelope(soap.V11, soap.ClientFault, "bad request")
	if !strings.Contains(v11, "<faultcode>") || !strings.Contains(v11, "<faultstring>") {
		t.Errorf("the 1.1 fault does not use faultcode/faultstring: %s", v11)
	}
	if !strings.Contains(v11, "soap:Client") {
		t.Errorf("the 1.1 fault does not name the client code: %s", v11)
	}

	v12 := soap.FaultEnvelope(soap.V12, soap.ClientFault, "bad request")
	if !strings.Contains(v12, "<soap:Value>") || !strings.Contains(v12, "<soap:Text") {
		t.Errorf("the 1.2 fault does not use Code/Value and Reason/Text: %s", v12)
	}
	if !strings.Contains(v12, "soap:Sender") {
		t.Errorf("the 1.2 fault does not use Sender: %s", v12)
	}
}

func TestAServerFaultIsDistinguishedFromAClientFault(t *testing.T) {
	// A client fault says do not repeat this unchanged; a server fault says retrying later may work. Backwards, and
	// either somebody hunts for a bug in a correct request or a client retries a malformed one forever.
	client := soap.FaultEnvelope(soap.V12, soap.ClientFault, "x")
	server := soap.FaultEnvelope(soap.V12, soap.ServerFault, "x")

	if !strings.Contains(client, "Sender") {
		t.Error("a client fault does not say Sender in 1.2")
	}
	if !strings.Contains(server, "Receiver") {
		t.Error("a server fault does not say Receiver in 1.2")
	}
}

func TestTheResponseContentTypeMatchesTheVersion(t *testing.T) {
	// A 1.2 caller given text/xml, or a 1.1 caller given application/soap+xml, may refuse to parse the reply at all.
	if got := soap.ContentType(soap.V11, ""); got != "text/xml; charset=utf-8" {
		t.Errorf("1.1 content type = %q", got)
	}
	if got := soap.ContentType(soap.V12, ""); !strings.HasPrefix(got, "application/soap+xml") {
		t.Errorf("1.2 content type = %q", got)
	}
}

func TestANegativeAcknowledgementIsRecognised(t *testing.T) {
	// Needed for fault_on_nak, and getting it wrong in either direction is bad: a NAK reported as success loses a
	// rejection, and an ACK reported as a NAK turns every delivery into a fault.
	for _, tc := range []struct {
		code string
		want bool
	}{
		{"AA", false},
		{"CA", false},
		{"AE", true},
		{"AR", true},
	} {
		ack := []byte("MSH|^~\\&|A|B|C|D|20260821||ACK|1|P|2.5.1\rMSA|" + tc.code + "|1\r")
		if got := isNegativeAck(ack); got != tc.want {
			t.Errorf("MSA-1 %s: negative = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestAnUnparseableAckIsNotTreatedAsNegative(t *testing.T) {
	// Otherwise a channel configured with fault_on_nak would fault on anything it could not read, turning a local
	// problem into an apparent rejection of the caller's message.
	if isNegativeAck([]byte("not an ack")) {
		t.Error("unparseable output was treated as a negative acknowledgement")
	}
}

func TestBothAuthenticationMechanismsTogetherAreRefused(t *testing.T) {
	// Two mechanisms configured means somebody expects one of them to be enforced, and silently ignoring either
	// leaves the endpoint less protected than the file suggests.
	src := &config.SOAPSource{Listen: ":1", Token: "t", Username: "u", Password: "p"}

	errs := configErrors(t, src)
	if !containsText(errs, "less protected") {
		t.Errorf("both mechanisms were accepted: %v", errs)
	}
}

func TestHalfACredentialIsRefused(t *testing.T) {
	src := &config.SOAPSource{Listen: ":1", Username: "u"}

	errs := configErrors(t, src)
	if !containsText(errs, "half a credential") {
		t.Errorf("a username with no password was accepted: %v", errs)
	}
}

func TestBase64WithoutAnElementIsRefused(t *testing.T) {
	// Decoding a whole body would include the XML around any sibling elements, which cannot decode, so this
	// combination is always a mistake rather than sometimes one.
	src := &config.SOAPSource{Listen: ":1", Base64: true}

	errs := configErrors(t, src)
	if !containsText(errs, "name the element") {
		t.Errorf("base64 with no element was accepted: %v", errs)
	}
}

// configErrors validates a source through a channel, which is the only exported path.
func configErrors(t *testing.T, src *config.SOAPSource) []string {
	t.Helper()

	ch := &config.Channel{
		Name:   "soap-in",
		Source: config.Source{Type: config.SourceSOAP, SOAP: src},
		Destinations: []config.Destination{
			{Name: "out", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}

	err := ch.Validate()
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}

func containsText(errs []string, want string) bool {
	for _, e := range errs {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}
