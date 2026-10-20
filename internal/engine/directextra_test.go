package engine

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/direct"
	"github.com/biodream-llc/perfuse/internal/generate"
	"github.com/biodream-llc/perfuse/internal/mailbox"
)

// A HISP that keeps mail in a mailbox: the Direct message is read over IMAP, opened, and passed on; something that is not
// Direct moves to the error folder. Against GreenMail (PERFUSE_TEST_GREENMAIL, user feed:feedpass@lab.example).
func TestADirectMessageIsReadFromTheHISPMailbox(t *testing.T) {
	host := os.Getenv("PERFUSE_TEST_GREENMAIL")
	if host == "" {
		t.Skip("set PERFUSE_TEST_GREENMAIL to a GreenMail host (greenmail/standalone, user feed:feedpass@lab.example)")
	}
	if c, err := net.DialTimeout("tcp", host+":3143", time.Second); err != nil {
		t.Skip("no GreenMail IMAP")
	} else {
		_ = c.Close()
	}
	p := newDirectPKI(t)
	_, _, senderPair := p.issue(t, "referrals@direct.hospital.example", false)
	recipCert, recipKey, recipPair := p.issue(t, "intake@direct.clinic.example", false)
	sealed, err := direct.Protect(direct.Message{From: "referrals@direct.hospital.example", To: []string{"intake@direct.clinic.example"},
		Subject: "Referral", Attachments: []direct.Attachment{{Name: "referral.txt", MimeType: "text/plain", Content: []byte("A referral for DOE, JOHN.")}}},
		senderPair, nil, []*x509.Certificate{recipPair.Certificate})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Format("150405.000")
	inbox, done, errs := "DirectIn-"+stamp, "DirectDone-"+stamp, "DirectErr-"+stamp
	imap, err := mailbox.DialIMAP(t.Context(), mailbox.Dial{Host: host, Port: 3143, Security: mailbox.SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer imap.Logout()
	if err := imap.Login(t.Context(), mailbox.Auth{Username: "feed", Password: "feedpass"}); err != nil {
		t.Fatal(err)
	}
	_ = imap.Create(inbox)
	for _, m := range [][]byte{sealed, mailWithAttachment("someone@else.example", "x.txt", []byte("plain mail, not Direct"))} {
		if err := imap.Append(inbox, m); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(strings.NewReader(fmt.Sprintf(`name: direct-mailbox
dataType: raw
source:
  type: direct
  direct:
    address: intake@direct.clinic.example
    cert_file: %s
    key_file: %s
    anchors_file: %s
    imap:
      host: %s
      port: 3143
      security: none
      username: feed
      password: feedpass
      mailbox: %s
      move_to: %s
      error_folder: %s
      poll_interval: 10s
destinations:
  - name: out
    type: file
    dir: /tmp/unused
`, recipCert, recipKey, p.anchorFile, strings.Replace(host, "127.0.0.1", "localhost", 1), inbox, done, errs)), "mailbox.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSender{}
	startChannelFor(t, cfg, sink)
	count := func(box string) int {
		if err := imap.Select(box); err != nil {
			return -1
		}
		u, _ := imap.Unseen()
		return len(u)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !(count(done) == 1 && count(errs) == 1) {
		time.Sleep(300 * time.Millisecond)
	}
	if d, e, i := count(done), count(errs), count(inbox); d != 1 || e != 1 || i != 0 {
		t.Fatalf("processed %d, error %d, left %d", d, e, i)
	}
	if sink.count() != 1 || sink.last() != "A referral for DOE, JOHN." {
		t.Fatalf("the channel got %d messages: %q", sink.count(), sink.last())
	}
}

// XDR end to end: an xds destination with a Direct addressing block posts to a direct source's XDR endpoint, which
// passes the document on; a submission addressed to somebody else is refused with a RegistryResponse failure.
func TestXDRFromAnXDSDestinationToADirectSource(t *testing.T) {
	listen := freePort(t)
	cfg, err := config.Load(strings.NewReader(`name: xdr-in
dataType: raw
source:
  type: direct
  direct:
    address: intake@direct.clinic.example
    xdr:
      listen: "`+listen+`"
destinations:
  - name: out
    type: file
    dir: /tmp/unused
`), "xdr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSender{}
	startChannelFor(t, cfg, sink)

	doc := generate.New(generate.Options{Kind: generate.MDM, Count: 1, Seed: 7}).All()[0].Raw
	send := func(to string) error {
		d := config.Destination{Name: "xdr", Type: config.DestinationXDS, XDS: &config.XDSDestination{URL: "http://" + listen + "/xdr",
			SourceID: "2.16.840.1.113883.3.9999.2", PatientDomain: "2.16.840.1.113883.19.5.99999.2",
			FacilityType:    config.XDSCode{Code: "22232009", Scheme: "2.16.840.1.113883.6.96"},
			PracticeSetting: config.XDSCode{Code: "394802001", Scheme: "2.16.840.1.113883.6.96"},
			DirectFrom:      "referrals@direct.hospital.example", DirectTo: []string{to}, Timeout: 10 * time.Second}}
		s, err := NewXDSSender(d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s.Describe(), "XDR to "+to) {
			t.Errorf("describe: %s", s.Describe())
		}
		return s.Send(context.Background(), doc)
	}
	if err := send("intake@direct.clinic.example"); err != nil {
		t.Fatal(err)
	}
	if sink.count() != 1 || !strings.Contains(sink.last(), "<ClinicalDocument") {
		t.Fatalf("the channel got %d messages: %.120q", sink.count(), sink.last())
	}
	err = send("someone@direct.elsewhere.example")
	if err == nil || !strings.Contains(err.Error(), "addressed to someone@direct.elsewhere.example") {
		t.Fatalf("a submission for another address: %v", err)
	}
	if sink.count() != 1 {
		t.Errorf("a refused submission reached the channel")
	}
}

// A Direct source needs one way in; XDR alone needs no certificate, and an XDR endpoint off this machine needs TLS.
func TestDirectSourceWaysInAreChecked(t *testing.T) {
	load := func(direct string) error {
		_, err := config.Load(strings.NewReader("name: d\ndataType: raw\nsource:\n  type: direct\n  direct:\n"+direct+
			"destinations:\n  - name: out\n    type: file\n    dir: /tmp/unused\n"), "d.yaml")
		return err
	}
	if err := load("    address: a@b.example\n"); err == nil || !strings.Contains(err.Error(), "direct.listen") {
		t.Errorf("no way in: %v", err)
	}
	if err := load("    address: a@b.example\n    xdr:\n      listen: 127.0.0.1:8445\n"); err != nil {
		t.Errorf("XDR on this machine with no certificate: %v", err)
	}
	if err := load("    address: a@b.example\n    xdr:\n      listen: 0.0.0.0:8445\n"); err == nil || !strings.Contains(err.Error(), "tls_cert") {
		t.Errorf("XDR off this machine without TLS: %v", err)
	}
	if err := load("    address: a@b.example\n    imap:\n      host: imap.hisp.example\n      username: u\n      password: p\n"); err == nil ||
		!strings.Contains(err.Error(), "cert_file") {
		t.Errorf("IMAP without the address's certificate: %v", err)
	}
}

// XDR to the NIST XDS Toolkit's Document Recipient simulator, which checks the metadata (PERFUSE_NIST_REC, its PnR endpoint:
// http://localhost:18080/xdstools/sim/default__pfrec/rep/xdrpr).
func TestXDRToTheNISTDocumentRecipient(t *testing.T) {
	url := os.Getenv("PERFUSE_NIST_REC")
	if url == "" {
		t.Skip("set PERFUSE_NIST_REC to a NIST XDS Toolkit Document Recipient's PnR endpoint")
	}
	doc := generate.New(generate.Options{Kind: generate.MDM, Count: 1}).All()[0].Raw
	s, err := NewXDSSender(config.Destination{Name: "xdr", Type: config.DestinationXDS, XDS: &config.XDSDestination{URL: url,
		SourceID: "2.16.840.1.113883.3.9999.2", PatientDomain: "2.16.840.1.113883.19.5.99999.2",
		FacilityType:    config.XDSCode{Code: "22232009", Scheme: "2.16.840.1.113883.6.96"},
		PracticeSetting: config.XDSCode{Code: "394802001", Scheme: "2.16.840.1.113883.6.96"},
		DirectFrom:      "referrals@direct.hospital.example", DirectTo: []string{"intake@direct.clinic.example"}, Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), doc); err != nil {
		t.Fatalf("the Document Recipient refused it: %v", err)
	}
}

// XDR to ONC SITE's Direct XDR validator, which applies the Direct XDR checklist the (h)(2) certification test uses
// (PERFUSE_SITE_XDR=https://ccda.healthit.gov/xdrmessagevalidator/Dispatcher/XDRService). It answers Success with only
// Info notes when the message passes; before Perfuse wrote the metadata with prefixes, it faulted on the first byte.
func TestXDRToONCSITE(t *testing.T) {
	url := os.Getenv("PERFUSE_SITE_XDR")
	if url == "" {
		t.Skip("set PERFUSE_SITE_XDR to ONC SITE's XDR validator endpoint")
	}
	doc := generate.New(generate.Options{Kind: generate.MDM, Count: 1}).All()[0].Raw
	s, err := NewXDSSender(config.Destination{Name: "xdr", Type: config.DestinationXDS, XDS: &config.XDSDestination{URL: url,
		SourceID: "2.16.840.1.113883.3.9999.2", PatientDomain: "2.16.840.1.113883.19.5.99999.2",
		FacilityType:    config.XDSCode{Code: "22232009", Scheme: "2.16.840.1.113883.6.96"},
		PracticeSetting: config.XDSCode{Code: "394802001", Scheme: "2.16.840.1.113883.6.96"},
		DirectFrom:      "perfuse-test@direct.hospital.example", DirectTo: []string{"intake@direct.clinic.example"}, Timeout: 60 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), doc); err != nil {
		t.Fatalf("SITE refused it: %v", err)
	}
}
