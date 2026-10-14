package iis

import (
	"strings"
	"testing"
)

const vxu = "MSH|^~\\&|EHR|CLINIC|IIS|STATE|20261014||VXU^V04^VXU_V04|1|P|2.5.1|||ER|AL|||||Z22^CDCPHINVS\r" +
	"PID|1||12345^^^CLINIC^MR||DOE^JANE^A&B||20200101|F\r"

func TestTheSubmitBodyEscapesTheMessageAndOmitsAnEmptyFacility(t *testing.T) {
	body := SubmitBody("user<1>", "p&ss", "", []byte(vxu))
	if strings.Contains(body, "facilityID") {
		t.Errorf("an empty facility ID was sent: %s", body)
	}
	for _, want := range []string{"user&lt;1&gt;", "p&amp;ss", "A&amp;B", `xmlns="urn:cdc:iisb:2011"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %s", want, body)
		}
	}
	if !strings.Contains(SubmitBody("u", "p", "F1", []byte(vxu)), "<facilityID>F1</facilityID>") {
		t.Error("a facility ID was not sent")
	}
}

func TestARequestRoundTripsWithCarriageReturnsRestored(t *testing.T) {
	// A parser turns CR in character data into LF; the message must come back with HL7's separator.
	body := strings.ReplaceAll(SubmitBody("u", "p", "F1", []byte(vxu)), "\r", "\n")
	req, err := ParseRequest([]byte(`<urn:Envelope xmlns:urn="x"><urn:Body>` + body + `</urn:Body></urn:Envelope>`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Operation != "submitSingleMessage" || req.Username != "u" || req.Password != "p" || req.FacilityID != "F1" {
		t.Fatalf("got %+v", req)
	}
	if string(req.Message) != vxu {
		t.Errorf("message changed:\n%q\n%q", req.Message, vxu)
	}
}

func TestPrefixedChildrenAreRead(t *testing.T) {
	body := `<ns:connectivityTest xmlns:ns="urn:cdc:iisb:2011"><ns:echoBack>hello</ns:echoBack></ns:connectivityTest>`
	req, err := ParseRequest([]byte(body))
	if err != nil || req.Operation != "connectivityTest" || req.Echo != "hello" {
		t.Fatalf("got %+v, %v", req, err)
	}
}

func TestAnOperationTheWSDLDoesNotDefineIsRefusedByName(t *testing.T) {
	_, err := ParseRequest([]byte(`<submitBatch xmlns="urn:cdc:iisb:2011"/>`))
	if err == nil || !strings.Contains(err.Error(), "submitBatch") {
		t.Fatalf("got %v", err)
	}
}

func TestASubmitWithNoMessageIsRefused(t *testing.T) {
	if _, err := ParseRequest([]byte(`<submitSingleMessage><username>u</username></submitSingleMessage>`)); err == nil {
		t.Fatal("accepted a submit with no message")
	}
}

func ack(code string, errs ...string) string {
	s := "MSH|^~\\&|IIS|STATE|EHR|CLINIC|20261014||ACK^V04^ACK|9|P|2.5.1\rMSA|" + code + "|1\r"
	for _, e := range errs {
		s += e + "\r"
	}
	return s
}

func TestAcknowledgementsAreJudgedByCodeAndSeverity(t *testing.T) {
	cases := []struct {
		name       string
		returned   string
		strict     bool
		wantReject bool
	}{
		{"accepted", ack("AA"), false, false},
		{"rejected", ack("AR", "ERR||PID^1^7|101^Required field missing^HL70357|E"), false, true},
		{"warnings only", ack("AE", "ERR||RXA^1^9|103^Table value not found^HL70357|W"), false, false},
		{"warnings, strict", ack("AE", "ERR||RXA^1^9|103^Table value not found^HL70357|W"), true, true},
		{"an error among warnings", ack("AE", "ERR||RXA^1^9|103|W", "ERR||RXA^1^5|101|E"), false, true},
		{"no severity is an error", ack("AE", "ERR||RXA^1^5|101"), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Sent through XML with LF, as a registry's answer arrives.
			got, err := JudgeAck(strings.ReplaceAll(c.returned, "\r", "\n"), c.strict)
			if err != nil {
				t.Fatal(err)
			}
			if got.Rejected != c.wantReject {
				t.Errorf("rejected = %v, want %v (%s)", got.Rejected, c.wantReject, got.Error())
			}
			if !strings.HasSuffix(string(got.Message), "\r") || strings.Contains(string(got.Message), "\n") {
				t.Errorf("separators not restored: %q", got.Message)
			}
		})
	}
}

func TestARejectionNamesTheFieldAndTheReason(t *testing.T) {
	got, err := JudgeAck(ack("AR", "ERR||PID^1^7|101^Required field missing^HL70357|E||||Birth date is required"), false)
	if err != nil {
		t.Fatal(err)
	}
	msg := got.Error()
	for _, want := range []string{"AR", "PID 1 7", "Birth date is required"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q lacks %q", msg, want)
		}
	}
}

func TestAnAnswerThatIsNotHL7IsAnErrorNotASuccess(t *testing.T) {
	for _, s := range []string{"", "Invalid username or password", "MSH|^~\\&|IIS|S|E|C|1||ACK|1|P|2.5.1\r"} {
		if _, err := JudgeAck(s, false); err == nil {
			t.Errorf("%q was judged", s)
		}
	}
}

func TestTheReturnElementIsReadFromAResponseEnvelope(t *testing.T) {
	env := `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body>` +
		ResponseBody("submitSingleMessage", ack("AA")) + `</soap:Body></soap:Envelope>`
	got, err := ParseReturn([]byte(env))
	if err != nil || !strings.HasPrefix(got, "MSH|") {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ParseReturn([]byte("<html>Bad gateway</html>")); err == nil {
		t.Error("an HTML page was read as an answer")
	}
}

func TestTheWSDLDeclaresBothOperationsAndTheirActions(t *testing.T) {
	w := string(WSDL)
	for _, want := range []string{Namespace, ActionSubmit, ActionConnectivity, "hl7Message", "SecurityFault", "soap12:binding"} {
		if !strings.Contains(w, want) {
			t.Errorf("WSDL lacks %q", want)
		}
	}
}

func TestAFaultCarriesTheTypedDetail(t *testing.T) {
	f := FaultEnvelope(true, FaultSecurity, 401, "bad <password>", "x")
	for _, want := range []string{"soap:Sender", "<SecurityFault", "<Code>401</Code>", "bad &lt;password&gt;"} {
		if !strings.Contains(f, want) {
			t.Errorf("fault lacks %q: %s", want, f)
		}
	}
}
