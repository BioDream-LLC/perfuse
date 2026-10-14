package engine

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/iis"
)

const iisVXU = "MSH|^~\\&|EHR|CLINIC|IIS|STATE|20261014||VXU^V04^VXU_V04|CTL1|P|2.5.1|||ER|AL|||||Z22^CDCPHINVS\r" +
	"PID|1||12345^^^CLINIC^MR||DOE^JANE||20200101|F\r" +
	"RXA|0|1|20261014|20261014|08^HepB^CVX|0.5|mL^mL^UCUM\r"

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// startRegistry runs a Perfuse channel answering as an immunization registry does.
func startRegistry(t *testing.T) (string, *recordingSender) {
	t.Helper()
	addr := freeAddr(t)
	cfg, err := config.Load(strings.NewReader(`
name: registry
source:
  type: soap
  soap:
    listen: "`+addr+`"
    path: /iis
    profile: cdc-iis
    username: clinic
    password: s3cret
destinations:
  - name: out
    type: file
    dir: /tmp/unused
`), "registry.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSender{name: "registry"}
	startChannelFor(t, cfg, sink)
	return "http://" + addr + "/iis", sink
}

func iisSender(t *testing.T, url, password string) *SOAPSender {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(`
name: report
source:
  type: http
  http:
    listen: "127.0.0.1:0"
destinations:
  - name: registry
    type: soap
    soap:
      url: "`+url+`"
      profile: cdc-iis
      username: clinic
      password: "`+password+`"
      facility_id: F100
`), "report.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSOAPSender(cfg.Destinations[0])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAVaccinationTravelsThroughTheCDCInterfaceAndTheAckComesBack(t *testing.T) {
	url, sink := startRegistry(t)
	s := iisSender(t, url, "s3cret")

	reply, err := s.SendForResponse(context.Background(), []byte(iisVXU))
	if err != nil {
		t.Fatalf("submission failed: %v", err)
	}
	if !strings.Contains(string(reply), "MSA|AA|CTL1") {
		t.Errorf("the reply is not the registry's ACK: %q", reply)
	}
	if sink.count() != 1 || sink.last() != iisVXU {
		t.Fatalf("the registry received %d messages; last %q", sink.count(), sink.last())
	}
}

func TestAWrongPasswordIsASecurityFaultNotADelivery(t *testing.T) {
	url, sink := startRegistry(t)
	_, err := iisSender(t, url, "wrong").SendForResponse(context.Background(), []byte(iisVXU))
	if err == nil || !strings.Contains(err.Error(), "username or password") {
		t.Fatalf("got %v", err)
	}
	if sink.count() != 0 {
		t.Error("a message with the wrong password was accepted")
	}
}

func TestConnectivityTestEchoesWithoutSubmitting(t *testing.T) {
	url, sink := startRegistry(t)
	got, err := iisSender(t, url, "wrong").ConnectivityTest(context.Background(), "ping")
	if err != nil || got != "End Point is ready. Echo: ping" {
		t.Fatalf("got %q, %v", got, err)
	}
	if sink.count() != 0 {
		t.Error("connectivityTest submitted a message")
	}
}

func TestTheRegistryEndpointServesItsWSDL(t *testing.T) {
	url, _ := startRegistry(t)
	resp, err := http.Get(url + "?wsdl")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), iis.ActionSubmit) {
		t.Errorf("not the WSDL: %.200s", body)
	}
}

func TestARegistryRejectionWithHTTP200IsAFailure(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = r.Header.Get("Content-Type") + "\n" + string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = io.WriteString(w, `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body>`+
			iis.ResponseBody("submitSingleMessage",
				"MSH|^~\\&|IIS|STATE|EHR|CLINIC|20261014||ACK^V04^ACK|9|P|2.5.1\nMSA|AR|CTL1\n"+
					"ERR||PID^1^7|101^Required field missing^HL70357|E||||Birth date is required\n")+
			`</soap:Body></soap:Envelope>`)
	}))
	defer srv.Close()

	reply, err := iisSender(t, srv.URL, "s3cret").SendForResponse(context.Background(), []byte(iisVXU))
	if err == nil || !strings.Contains(err.Error(), "Birth date is required") {
		t.Fatalf("a rejection was not reported: %v", err)
	}
	if !strings.Contains(string(reply), "MSA|AR") {
		t.Errorf("the rejecting ACK is not the reply: %q", reply)
	}
	for _, want := range []string{`action="urn:cdc:iisb:2011:submitSingleMessage"`, "<facilityID>F100</facilityID>",
		"<password>s3cret</password>"} {
		if !strings.Contains(seen, want) {
			t.Errorf("the request lacks %q:\n%s", want, seen)
		}
	}
}

func TestTheProfileRefusesAHandWrittenBody(t *testing.T) {
	_, err := config.Load(strings.NewReader(`
name: report
source:
  type: http
  http:
    listen: "127.0.0.1:0"
destinations:
  - name: registry
    type: soap
    soap:
      url: "https://iis.example/ws"
      profile: cdc-iis
      body: "<x/>"
`), "report.yaml")
	if err == nil || !strings.Contains(err.Error(), "remove soap.body") || !strings.Contains(err.Error(), "soap.username") {
		t.Fatalf("got %v", err)
	}
}
