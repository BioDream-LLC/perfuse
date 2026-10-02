package soap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The failure that matters most here is a fault that goes unrecognised, because that records a rejected message as
// delivered. So most of these tests are about recognising faults from services that label them differently.

func TestASoap11FaultIsRecognised(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <soap:Fault>
      <faultcode>soap:Client</faultcode>
      <faultstring>Patient identifier not recognised</faultstring>
      <detail><error>PID-3 does not match any known patient</error></detail>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`)

	fault := ParseFault(body)
	if fault == nil {
		t.Fatal("a 1.1 fault was not recognised, so a rejected message would be recorded as delivered")
	}
	if fault.Code != "soap:Client" {
		t.Errorf("code = %q", fault.Code)
	}
	if fault.Reason != "Patient identifier not recognised" {
		t.Errorf("reason = %q", fault.Reason)
	}
	// The detail is frequently the only part that names the field that was rejected.
	if !strings.Contains(fault.Detail, "PID-3") {
		t.Errorf("detail = %q, want it to name the field", fault.Detail)
	}
}

func TestASoap12FaultIsRecognised(t *testing.T) {
	// The element names are completely different. A parser that only knew 1.1 would report "no fault" on half of
	// all services, which is worse than not looking at all.
	body := []byte(`<?xml version="1.0"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope">
  <env:Body>
    <env:Fault>
      <env:Code><env:Value>env:Sender</env:Value></env:Code>
      <env:Reason><env:Text xml:lang="en">Message rejected by the receiving system</env:Text></env:Reason>
    </env:Fault>
  </env:Body>
</env:Envelope>`)

	fault := ParseFault(body)
	if fault == nil {
		t.Fatal("a 1.2 fault was not recognised")
	}
	if fault.Code != "env:Sender" {
		t.Errorf("code = %q", fault.Code)
	}
	if !strings.Contains(fault.Reason, "rejected") {
		t.Errorf("reason = %q", fault.Reason)
	}
}

func TestAnyNamespacePrefixIsRecognised(t *testing.T) {
	// Real services use every prefix imaginable and some use none. Refusing to recognise a fault because it was
	// labelled soapenv rather than soap would mean treating a rejection as a success.
	for _, prefix := range []string{"soap", "soapenv", "SOAP-ENV", "s", ""} {
		open, close := "<Fault>", "</Fault>"
		if prefix != "" {
			open, close = "<"+prefix+":Fault>", "</"+prefix+":Fault>"
		}

		body := []byte(`<Envelope><Body>` + open +
			`<faultcode>Client</faultcode><faultstring>no</faultstring>` +
			close + `</Body></Envelope>`)

		if ParseFault(body) == nil {
			t.Errorf("a fault with prefix %q was not recognised", prefix)
		}
	}
}

func TestASuccessfulResponseIsNotAFault(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body><SubmitResponse><Status>Accepted</Status></SubmitResponse></soap:Body>
</soap:Envelope>`)

	if fault := ParseFault(body); fault != nil {
		t.Errorf("a successful response was read as a fault: %+v", fault)
	}
}

func TestTheWordFaultInOrdinaryContentIsNotAFault(t *testing.T) {
	// Reporting a fault with no information would turn a successful delivery into an unexplained failure - and
	// clinical text contains the word.
	body := []byte(`<Envelope><Body><Result><Note>No cardiac Fault detected</Note></Result></Body></Envelope>`)

	if fault := ParseFault(body); fault != nil {
		t.Errorf("the word Fault in content was read as a fault: %+v", fault)
	}
}

func TestAFaultErrorMessageIsReadable(t *testing.T) {
	// This ends up in a log at three in the morning. "500" is not enough.
	fault := &Fault{
		Code:   "soap:Client",
		Reason: "Patient identifier not recognised",
		Detail: "PID-3\n  does not match",
	}

	msg := fault.Error()
	if !strings.Contains(msg, "not recognised") {
		t.Errorf("the message omits the reason: %q", msg)
	}
	if !strings.Contains(msg, "PID-3") {
		t.Errorf("the message omits the detail: %q", msg)
	}
	// One line, or the next log line looks like a separate event.
	if strings.Contains(msg, "\n") {
		t.Errorf("the message spans several lines: %q", msg)
	}
}

func TestTheEnvelopeUsesTheRightNamespace(t *testing.T) {
	// A 1.2 service rejects a 1.1 envelope outright, and the fault it returns is about the version rather than
	// about the message - which sends somebody looking at their template.
	v11, err := Envelope("<Submit/>", "", V11)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v11), ns11) {
		t.Errorf("1.1 envelope has the wrong namespace: %s", v11)
	}

	v12, err := Envelope("<Submit/>", "", V12)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v12), ns12) {
		t.Errorf("1.2 envelope has the wrong namespace: %s", v12)
	}
}

func TestNoHeaderElementWhenThereAreNoHeaders(t *testing.T) {
	// Some services reject an empty Header element, and emitting one for tidiness would break them for nothing.
	envelope, err := Envelope("<Submit/>", "", V11)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope), "Header") {
		t.Errorf("an empty header element was emitted: %s", envelope)
	}
}

func TestHeadersAreIncludedWhenGiven(t *testing.T) {
	envelope, err := Envelope("<Submit/>", "<Auth><Token>abc</Token></Auth>", V11)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envelope), "<soap:Header><Auth>") {
		t.Errorf("the header was not included: %s", envelope)
	}
}

func TestAnEmptyBodyIsRefused(t *testing.T) {
	// Almost every service answers an empty Body with a fault about a missing operation, which reads as a service
	// problem rather than a template problem.
	if _, err := Envelope("", "", V11); err == nil {
		t.Fatal("an empty body was accepted")
	}
}

func TestSoapActionIsSentAsAHeaderIn11(t *testing.T) {
	var gotAction, gotType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("SOAPAction")
		gotType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<Envelope><Body><Ok/></Body></Envelope>`))
	}))
	defer srv.Close()

	resp, err := Call(context.Background(), srv.Client(), Request{
		URL: srv.URL, Action: "urn:Submit", Body: "<Submit/>", Version: V11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Fault != nil {
		t.Errorf("unexpected fault: %v", resp.Fault)
	}

	// Quoted, which is what the specification says and what dispatching services expect.
	if gotAction != `"urn:Submit"` {
		t.Errorf("SOAPAction = %q, want it quoted", gotAction)
	}
	if !strings.HasPrefix(gotType, "text/xml") {
		t.Errorf("content type = %q, want text/xml for 1.1", gotType)
	}
}

func TestSoapActionIsAContentTypeParameterIn12(t *testing.T) {
	// Sending it as a header instead is a common mistake and produces a service that silently dispatches to the
	// wrong operation, or to none.
	var gotType, gotAction string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotAction = r.Header.Get("SOAPAction")
		_, _ = w.Write([]byte(`<Envelope><Body><Ok/></Body></Envelope>`))
	}))
	defer srv.Close()

	if _, err := Call(context.Background(), srv.Client(), Request{
		URL: srv.URL, Action: "urn:Submit", Body: "<Submit/>", Version: V12,
	}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotType, "application/soap+xml") {
		t.Errorf("content type = %q, want application/soap+xml for 1.2", gotType)
	}
	if !strings.Contains(gotType, `action="urn:Submit"`) {
		t.Errorf("content type = %q, want the action as a parameter", gotType)
	}
	if gotAction != "" {
		t.Errorf("SOAPAction header = %q, want it absent in 1.2", gotAction)
	}
}

func TestAFaultIsReturnedInTheResponseRatherThanAsAnError(t *testing.T) {
	// Some services answer a duplicate submission with a fault that a site considers success. A transport that
	// decided for them would have to be worked around with a script.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<Envelope><Body><Fault>` +
			`<faultcode>Client</faultcode><faultstring>Already submitted</faultstring>` +
			`</Fault></Body></Envelope>`))
	}))
	defer srv.Close()

	resp, err := Call(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: "<Submit/>",
	})
	if err != nil {
		t.Fatalf("a fault was returned as a transport error: %v", err)
	}
	if resp.Fault == nil {
		t.Fatal("the fault was not detected")
	}
	if resp.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want it preserved", resp.Status)
	}
	if !strings.Contains(resp.Fault.Reason, "Already submitted") {
		t.Errorf("reason = %q", resp.Fault.Reason)
	}
}

func TestBasicAuthIsSentWhenConfigured(t *testing.T) {
	// A surprising number of these services expect it in preference to anything in the envelope.
	var user, pass string
	var ok bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		_, _ = w.Write([]byte(`<Envelope><Body><Ok/></Body></Envelope>`))
	}))
	defer srv.Close()

	if _, err := Call(context.Background(), srv.Client(), Request{
		URL: srv.URL, Body: "<Submit/>", Username: "interface", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	if !ok || user != "interface" || pass != "secret" {
		t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
	}
}

func TestXMLEscapeProtectsTheEnvelope(t *testing.T) {
	// An unescaped ampersand in a patient's name produces an envelope the service rejects as malformed XML, and
	// the fault is about the request rather than about the name.
	got := XMLEscape(`Smith & Jones <test>`)
	if strings.Contains(got, "&amp;") == false {
		t.Errorf("the ampersand was not escaped: %q", got)
	}
	if strings.Contains(got, "<test>") {
		t.Errorf("the angle brackets were not escaped: %q", got)
	}
}

func TestAMissingURLIsRefused(t *testing.T) {
	if _, err := Call(context.Background(), nil, Request{Body: "<Submit/>"}); err == nil {
		t.Fatal("a request with no URL was accepted")
	}
}
