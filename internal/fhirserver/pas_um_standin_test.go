package fhirserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/umlink"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// The PAS 2.2.1 examples go to an outside stand-in utilization management system, over every transport, and come back decided
// as it decided them. The stand-in is not Perfuse's: it is written from CAQH's Connectivity Rules (its SOAP envelopes checked by
// libxml2 against CAQH's own schema, read by node-soap from CAQH's WSDL), judges each 278 with a checker written from payers'
// companion guides and answers a request that fails it with a 999, and answers the rest in the CMS esMD samples' shape.
//
// Run it (see docs/verification.md) and set:
//
//	PERFUSE_TEST_UM_STANDIN=https://127.0.0.1:18278   the stand-in
//	PERFUSE_TEST_UM_STANDIN_TLS=DIR                    its ca.crt, and client.crt and client.key from that CA
//	PERFUSE_TEST_UM_STANDIN_SFTP=127.0.0.1:2278        an SFTP server whose ROOT/out the stand-in reads and ROOT/in it writes,
//	PERFUSE_TEST_UM_STANDIN_SFTP_USER, _SFTP_KEY, _SFTP_KNOWN_HOSTS, _SFTP_ROOT
func TestThePASExamplesAreDecidedByAnOutsideUMSystem(t *testing.T) {
	base := os.Getenv("PERFUSE_TEST_UM_STANDIN")
	if base == "" {
		t.Skip("PERFUSE_TEST_UM_STANDIN is not set")
	}
	tlsDir := os.Getenv("PERFUSE_TEST_UM_STANDIN_TLS")
	ca := filepath.Join(tlsDir, "ca.crt")
	cert, key := filepath.Join(tlsDir, "client.crt"), filepath.Join(tlsDir, "client.key")
	oauth := umlink.OAuth{TokenURL: base + "/oauth/token", ClientID: "perfuse-client", ClientSecret: "perfuse-secret"}
	links := map[string]umlink.Config{
		"x12 with a password":    {Transport: umlink.TransportX12, URL: base + "/x12", CAFile: ca, Username: "standin", Password: "standin-pw"},
		"x12 with OAuth":         {Transport: umlink.TransportX12, URL: base + "/x12", CAFile: ca, OAuth: oauth},
		"CORE SOAP vC2.2.0":      {Transport: umlink.TransportCORESOAP, URL: base + "/core/soap", CAFile: ca, Username: "standin", Password: "standin-pw"},
		"CORE SOAP vC4.0.0 MTOM": {Transport: umlink.TransportCORESOAP, COREVersion: umlink.CORE400, URL: base + "/core/soap-c4", CAFile: ca, ClientCert: cert, ClientKey: key, OAuth: oauth},
		"CORE HTTP MIME":         {Transport: umlink.TransportCOREMIME, URL: base + "/core/mime", CAFile: ca, Username: "standin", Password: "standin-pw"},
	}
	if a := os.Getenv("PERFUSE_TEST_UM_STANDIN_SFTP"); a != "" {
		root := os.Getenv("PERFUSE_TEST_UM_STANDIN_SFTP_ROOT")
		links["SFTP batch"] = umlink.Config{Transport: umlink.TransportSFTP, SFTP: umlink.SFTP{Address: a,
			User: os.Getenv("PERFUSE_TEST_UM_STANDIN_SFTP_USER"), KeyFile: os.Getenv("PERFUSE_TEST_UM_STANDIN_SFTP_KEY"),
			KnownHosts: os.Getenv("PERFUSE_TEST_UM_STANDIN_SFTP_KNOWN_HOSTS"), Outbound: root + "/out", Inbound: root + "/in"}}
	}
	// What the stand-in decides: 33510 (the surgical example's bypass) is pended for more information, the rest certified.
	want := map[string]string{
		"HomecareAuthorizationBundleExample":        "A1,A1",
		"MedicalServicesAuthorizationBundleExample": "A1",
		"ReferralAuthorizationBundleExample":        "A1",
		"SurgicalRequestBundleExample":              "A4",
	}
	for name, cfg := range links {
		t.Run(name, func(t *testing.T) {
			cfg.SenderID, cfg.ReceiverID = "PERFUSE", "STANDINUM"
			link, err := umlink.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			send := func(ctx context.Context, b []byte) ([]byte, error) {
				out, err := link.Send(ctx, b)
				if errors.Is(err, umlink.ErrAnswerLater) {
					return nil, ErrUMAnswersLater
				}
				return out, err
			}
			f := newPASFixture(t)
			srv := NewServer(f.store, "http://example.test/fhir", nil)
			srv.Auth = OpenAuth{}
			srv.PAS = &PAS{Decide: pasRules, Now: newPASFixtureClock, UM: &PASUM{Send: send, SenderID: "PERFUSE", ReceiverID: "STANDINUM", PayerID: "12345",
				Inquire: !link.Batch()}}
			h := srv.Handler()
			ids := map[string]string{}
			for example, codes := range want {
				body, err := os.ReadFile(filepath.Join("testdata", "pas", "Bundle-"+example+".json"))
				if err != nil {
					t.Fatal(err)
				}
				code, b := pasPost(t, h, "/Claim/$submit", string(body))
				if code != http.StatusOK {
					t.Fatalf("%s: %d %v", example, code, b)
				}
				cr := claimResponseOf(t, b)
				ids[example] = str(cr["id"])
				if link.Batch() {
					if got := actionCodes(cr); strings.Trim(strings.ReplaceAll(got, "A4", ""), ",") != "" {
						t.Errorf("%s: %s before the batch answer, want every item pended", example, got)
					}
					continue
				}
				if got := actionCodes(cr); got != codes {
					t.Errorf("%s: %s, want %s; notes %s", example, got, codes, toJSON(cr["processNote"]))
				}
			}
			if !link.Batch() {
				// $inquire finds, besides what came through Perfuse, the authorization the stand-in holds from a fax, once,
				// and nothing Perfuse forwarded a second time.
				body, _ := os.ReadFile(filepath.Join("testdata", "pas", "Bundle-MedicalServicesAuthorizationBundleExample.json"))
				code, out := pasPost(t, h, "/Claim/$inquire", string(body))
				if code != http.StatusOK {
					t.Fatalf("$inquire: %d %v", code, out)
				}
				// The stand-in keeps what every run submitted: an earlier run's requests are, to this server, asked for
				// elsewhere, and come back; this run's own never do.
				sent := map[string]bool{}
				rows, err := srv.Store.db.Query(`SELECT trace FROM pas_um_traces`)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var tr string
					_ = rows.Scan(&tr)
					sent[tr] = true
				}
				rows.Close()
				fax := 0
				for _, p := range asSliceAny(out["parameter"]) {
					cr := claimResponseOf(t, asMapAny(asMapAny(p)["resource"]))
					if strings.HasPrefix(str(cr["preAuthRef"]), "FAX-") {
						fax++
					}
					for _, id := range asSliceAny(cr["identifier"]) {
						if im := asMapAny(id); str(im["system"]) == umTraceSystem && sent[str(im["value"])] {
							t.Errorf("$inquire returned this server's own request %s a second time", im["value"])
						}
					}
				}
				if fax != 1 || len(sent) == 0 {
					t.Errorf("$inquire: %d fax authorizations (want 1), %d traces sent: %s", fax, len(sent), toJSON(out))
				}
				return
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				_ = link.Poll(context.Background(), func(_ string, body []byte) ([]byte, error) {
					reply, _, err := srv.ApplyUMFile(context.Background(), body)
					return reply, err
				})
				done := true
				for example, codes := range want {
					_, cr, _ := pasGet(t, h, "/ClaimResponse/"+ids[example])
					if codes != "A4" && actionCodes(cr) != codes {
						done = false
					}
				}
				if done {
					break
				}
				if time.Now().After(deadline) {
					st, _ := srv.UMStatus(context.Background(), 20)
					t.Fatalf("the batch answers did not all arrive: %+v", st)
				}
				time.Sleep(time.Second)
			}
			st, _ := srv.UMStatus(context.Background(), 20)
			if st.Counts["acknowledged"] != len(want) {
				t.Errorf("each 278 should have been acknowledged by a 999: %+v", st.Counts)
			}
		})
	}

	// A wrong password is refused by the CORE envelope, and the request is pended, not decided.
	t.Run("a wrong password", func(t *testing.T) {
		link, _ := umlink.New(umlink.Config{Transport: umlink.TransportCORESOAP, URL: base + "/core/soap", CAFile: ca,
			Username: "standin", Password: "wrong", SenderID: "PERFUSE", ReceiverID: "STANDINUM"})
		if _, err := link.Send(context.Background(), []byte(validRequest(t))); err == nil || !strings.Contains(err.Error(), "UnAuthorized") {
			t.Fatalf("%v", err)
		}
	})

	// A 278 the stand-in's checker fails is answered with a 999 rejecting it, which reads as a rejection.
	t.Run("a 278 the UM system cannot read", func(t *testing.T) {
		link, _ := umlink.New(umlink.Config{Transport: umlink.TransportX12, URL: base + "/x12", CAFile: ca, Username: "standin", Password: "standin-pw"})
		bad := strings.Replace(validRequest(t), "~HL*3*2*22*1~", "~HL*3*2*99*1~", 1)
		out, err := link.Send(context.Background(), []byte(bad))
		if err != nil {
			t.Fatal(err)
		}
		m, err := x12.Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := m.ReadAcknowledgement()
		if err != nil || !ack.Rejected() {
			t.Fatalf("%+v %v\n%s", ack, err, out)
		}
	})

	// vC4.0.0 without the client certificate is refused at the door.
	t.Run("vC4.0.0 without a client certificate", func(t *testing.T) {
		link, _ := umlink.New(umlink.Config{Transport: umlink.TransportCORESOAP, COREVersion: umlink.CORE400, URL: base + "/core/soap-c4",
			CAFile: ca, OAuth: oauth, SenderID: "PERFUSE", ReceiverID: "STANDINUM"})
		if _, err := link.Send(context.Background(), []byte(validRequest(t))); err == nil {
			t.Fatal("accepted without a client certificate")
		}
	})

	// The stand-in's own record agrees: no schema failures, nothing refused that should not have been.
	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(ca)
	pool.AppendCertsFromPEM(pem)
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := c.Get(base + "/log")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var entries []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&entries)
	for _, e := range entries {
		if e["schema"] != nil || e["envelope"] != nil || e["error"] != nil || e["inquiryRejected"] != nil {
			t.Errorf("the stand-in found fault with something sent: %v", e)
		}
	}
}

// validRequest is a 278 Perfuse builds for the referral example, as the stand-in would receive it.
func validRequest(t *testing.T) string {
	t.Helper()
	req := x12.ServiceReviewRequest{
		Envelope: x12.Envelope{SenderID: "PERFUSE", ReceiverID: "STANDINUM", ControlNumber: 4242, Now: time.Now()},
		Payer:    x12.Person{LastName: "PAYER", ID: "12345"}, Provider: x12.Person{LastName: "CLINIC", ID: "1234567893"},
		Subscriber: x12.Person{LastName: "DOE", FirstName: "JANE", ID: "M1", DOB: "19800101", Gender: "F"},
		Events:     []x12.ReviewRequestEvent{{Trace: "T1", Category: "SC", ServiceType: "3", Location: "11", Diagnoses: []string{"G89.4"}}},
	}
	raw, err := x12.BuildServiceReviewRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
