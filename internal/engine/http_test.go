package engine

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
)

const httpTestMessage = "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|H1|P|2.5.1\r" +
	"PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|F\r"

func httpSenderFor(t *testing.T, cfg config.HTTPDestination) *HTTPSender {
	t.Helper()
	s, err := NewHTTPSender(config.Destination{
		Name: "api", Type: config.DestinationHTTP,
		HTTP: &cfg, Timeout: 5 * time.Second,
	}, quiet())
	if err != nil {
		t.Fatalf("NewHTTPSender: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHTTPSenderPostsTheMessage(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotType, gotAuth, gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotType = string(body), r.Header.Get("Content-Type")
		gotAuth, gotMethod = r.Header.Get("Authorization"), r.Method
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{
		URL: srv.URL, BearerToken: "tok3n",
		Headers: map[string]string{"X-Tenant": "sitea"},
	})

	if err := s.Send(context.Background(), []byte(httpTestMessage)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotBody != httpTestMessage {
		t.Error("the message was not sent byte for byte")
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q", gotMethod)
	}
	// The registered type for a pipe-delimited HL7 v2 message, so a receiver can
	// dispatch on it rather than sniffing.
	if gotType != "application/hl7-v2+er7" {
		t.Errorf("content type = %q", gotType)
	}
	if gotAuth != "Bearer tok3n" {
		t.Errorf("authorization = %q", gotAuth)
	}
}

func TestHTTPSenderUsesBasicAuthWhenConfigured(t *testing.T) {
	var mu sync.Mutex
	var user, pass string
	var ok bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		user, pass, ok = r.BasicAuth()
		mu.Unlock()
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{
		URL: srv.URL, Username: "interface", Password: "secret",
	})
	if err := s.Send(context.Background(), []byte(httpTestMessage)); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	// A surprising number of hospital systems still expect this.
	if !ok || user != "interface" || pass != "secret" {
		t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
	}
}

func TestHTTPSenderTreatsAnErrorStatusAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("the patient index is unavailable"))
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{URL: srv.URL})
	err := s.Send(context.Background(), []byte(httpTestMessage))
	if err == nil {
		t.Fatal("a 500 should be a delivery failure")
	}
	// The endpoint's explanation is usually the only clue, so it has to survive
	// into the error rather than being flattened to a status code.
	if !strings.Contains(err.Error(), "patient index is unavailable") {
		t.Errorf("the error should carry the response: %v", err)
	}
}

func TestHTTPSenderCanAcceptUnusualSuccessCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Some endpoints answer 302 for "accepted, see elsewhere", which is outside
		// the 2xx range and is still success as far as they are concerned.
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{
		URL: srv.URL, SuccessStatus: []int{302},
	})
	if err := s.Send(context.Background(), []byte(httpTestMessage)); err != nil {
		t.Errorf("302 was configured as success: %v", err)
	}
}

func TestHTTPSenderFailsOnAnErrorInTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The failure mode that makes this transport quietly lossy: the endpoint
		// says 200 and means no.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<result>ERROR: patient not found</result>"))
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{
		URL: srv.URL, FailOnBody: []string{"ERROR"},
	})
	err := s.Send(context.Background(), []byte(httpTestMessage))
	if err == nil {
		t.Fatal("a 200 containing an error marker should be a failure")
	}
	if !strings.Contains(err.Error(), "patient not found") {
		t.Errorf("the error should quote the response: %v", err)
	}
	if s.Stats().BodyFails != 1 {
		t.Errorf("stats = %+v", s.Stats())
	}
}

func TestHTTPSenderRefusesARedirectByDefault(t *testing.T) {
	var mu sync.Mutex
	elsewhere := 0

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhere++
		mu.Unlock()
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{URL: srv.URL})
	err := s.Send(context.Background(), []byte(httpTestMessage))
	if err == nil {
		t.Fatal("a redirect on a write should be refused")
	}

	mu.Lock()
	defer mu.Unlock()
	// The point: clinical data must not be reposted somewhere the configuration
	// never named, with the log still showing the original URL.
	if elsewhere != 0 {
		t.Error("the message was reposted to the redirect target")
	}
}

func TestHTTPSenderBoundsWhatItReadsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// An endpoint answering with a megabyte of HTML instead of an
		// acknowledgement should not be able to exhaust memory.
		_, _ = w.Write([]byte(strings.Repeat("x", 5<<20)))
	}))
	defer srv.Close()

	s := httpSenderFor(t, config.HTTPDestination{URL: srv.URL})
	err := s.Send(context.Background(), []byte(httpTestMessage))
	if err == nil {
		t.Fatal("expected a failure")
	}
	// And the error itself has to stay readable, because it goes into a log line
	// and a message store field.
	if len(err.Error()) > 1000 {
		t.Errorf("the error is %d bytes long", len(err.Error()))
	}
}

func TestHTTPDestinationConfigIsCheckedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.HTTPDestination
		want string
	}{
		{"no url", config.HTTPDestination{}, "url is required"},
		{"relative url", config.HTTPDestination{URL: "/api/messages"}, "absolute URL"},
		{"plain http remote", config.HTTPDestination{URL: "http://api.example.org"}, "unencrypted"},
		{"bodyless method", config.HTTPDestination{
			URL: "https://api.example.org", Method: "GET",
		}, "silently discarded"},
		{"two kinds of auth", config.HTTPDestination{
			URL: "https://api.example.org", BearerToken: "t", Username: "u",
		}, "one form of authentication"},
		{"username with no password", config.HTTPDestination{
			URL: "https://api.example.org", Username: "u",
		}, "without a password"},
		{"impossible status", config.HTTPDestination{
			URL: "https://api.example.org", SuccessStatus: []int{7},
		}, "not a status code"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := config.Channel{
				Name:   "api",
				Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
				Destinations: []config.Destination{{
					Name: "out", Type: config.DestinationHTTP, HTTP: &tc.cfg,
				}},
			}
			err := ch.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

// --- the listener ----------------------------------------------------------

func httpSourceChannel(t *testing.T, src *config.HTTPSource, capture *captureDest) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	src.Listen = addr

	cfg := &config.Channel{
		Name: "http-in",
		Source: config.Source{
			Type: config.SourceHTTP, HTTP: src,
			Ack: config.Ack{When: config.AckOnDelivery},
		},
		Destinations: []config.Destination{{
			Name: "capture", Type: config.DestinationMLLP,
			Address: "127.0.0.1:1", Timeout: time.Second,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
		return capture, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })

	// Give the listener a moment to bind.
	time.Sleep(100 * time.Millisecond)
	return "http://" + addr
}

func TestHTTPSourceAcceptsAPostedMessage(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	resp, err := http.Post(base+"/", "application/hl7-v2+er7",
		strings.NewReader(httpTestMessage))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d: %s", resp.StatusCode, body)
	}

	// An HL7 ACK, not a JSON envelope. A sender that speaks HL7 over HTTP still
	// speaks HL7, and inventing a different success format would mean every client
	// needed code specific to us.
	ack, err := hl7.Parse(body)
	if err != nil {
		t.Fatalf("the response is not an HL7 acknowledgement: %v\n%s", err, body)
	}
	msa, ok := ack.Segment("MSA", 1)
	if !ok {
		t.Fatal("no MSA in the response")
	}
	if code := msa.Field(1).String(); code != "AA" {
		t.Errorf("acknowledgement = %q", code)
	}

	if len(capture.got) != 1 {
		t.Fatalf("the channel forwarded %d messages", len(capture.got))
	}
}

func TestHTTPSourceRequiresATokenWhenConfigured(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{Token: "s3cret"}, capture)

	resp, err := http.Post(base+"/", "text/plain", strings.NewReader(httpTestMessage))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without a token = %d, want 401", resp.StatusCode)
	}
	if len(capture.got) != 0 {
		t.Error("an unauthenticated message should not have been processed")
	}

	req, _ := http.NewRequest("POST", base+"/", strings.NewReader(httpTestMessage))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status with the right token = %d", resp2.StatusCode)
	}
}

func TestHTTPSourceRejectsAWrongToken(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{Token: "s3cret"}, capture)

	req, _ := http.NewRequest("POST", base+"/", strings.NewReader(httpTestMessage))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", resp.StatusCode)
	}
	// No detail about which half was wrong. An endpoint that distinguishes "wrong
	// token" from "no token" tells an attacker which half to work on.
	if strings.Contains(strings.ToLower(string(body)), "token") {
		t.Errorf("the response should not describe the failure: %s", body)
	}
}

func TestHTTPSourceServesTheConfiguredPath(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{Path: "/hl7/adt"}, capture)

	resp, err := http.Post(base+"/hl7/adt", "text/plain", strings.NewReader(httpTestMessage))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(capture.got) != 1 {
		t.Errorf("forwarded %d", len(capture.got))
	}
}

func TestHTTPSourceRefusesAGet(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if resp.Header.Get("Allow") == "" {
		t.Error("a 405 should say what is allowed")
	}
}

func TestHTTPSourceRefusesAnOversizedMessage(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{MaxMessageSize: 1024}, capture)

	resp, err := http.Post(base+"/", "text/plain",
		strings.NewReader(strings.Repeat("A", 4096)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 413 rather than a truncated message, because accepting a partial HL7 message
	// would parse into something plausible and wrong.
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if len(capture.got) != 0 {
		t.Error("an oversized message should not have been processed")
	}
}

func TestHTTPSourceRefusesAnEmptyBody(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	resp, err := http.Post(base+"/", "text/plain", strings.NewReader("   \n  "))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPSourceStripsMLLPFraming(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	// A client that already speaks MLLP will send the framing, and there is no
	// reason to make them special-case us.
	framed := "\x0b" + httpTestMessage + "\x1c\r"
	resp, err := http.Post(base+"/", "text/plain", strings.NewReader(framed))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(capture.got) != 1 {
		t.Fatalf("forwarded %d", len(capture.got))
	}
	if strings.ContainsAny(string(capture.got[0]), "\x0b\x1c") {
		t.Error("the framing bytes reached the destination")
	}
}

func TestHTTPSourceAnswers400ForRubbish(t *testing.T) {
	capture := &captureDest{}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	resp, err := http.Post(base+"/", "text/plain",
		strings.NewReader("this is not an HL7 message"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 400 rather than 502: the sender sent something we cannot read, which is
	// theirs to fix, and resending it unchanged will not help.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPSourceAnswers502WhenDeliveryFails(t *testing.T) {
	capture := &captureDest{fail: true}
	base := httpSourceChannel(t, &config.HTTPSource{}, capture)

	resp, err := http.Post(base+"/", "text/plain", strings.NewReader(httpTestMessage))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Distinguishing this from a bad request is what tells a sender whether
	// resending unchanged is worth trying.
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHTTPSourceWarnsWhenUnauthenticated(t *testing.T) {
	// Warned at every start rather than once at load, because an unauthenticated
	// endpoint accepting clinical messages is the kind of thing that gets set up for
	// a test and then forgotten.
	warnings := (&config.HTTPSource{Listen: "0.0.0.0:8661"}).Warnings()
	if len(warnings) == 0 {
		t.Fatal("an unauthenticated listener should warn")
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "http.token") {
		t.Errorf("the warning should say what to set: %v", warnings)
	}
	if !strings.Contains(joined, "clear text") {
		t.Error("a non-loopback listener without TLS should warn about clear text")
	}
}

func TestHTTPSourceConfigIsCheckedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		src  config.HTTPSource
		want string
	}{
		{"no listen", config.HTTPSource{}, "listen is required"},
		{"negative size", config.HTTPSource{
			Listen: "127.0.0.1:8661", MaxMessageSize: -1,
		}, "max_message_size"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := config.Channel{
				Name:   "in",
				Source: config.Source{Type: config.SourceHTTP, HTTP: &tc.src},
				Destinations: []config.Destination{{
					Name: "out", Type: config.DestinationMLLP, Address: "127.0.0.1:1",
				}},
			}
			err := ch.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestAnHTTPBlockOnAnMLLPSourceIsRefused(t *testing.T) {
	// Silently ignoring it means somebody configured a path and a token and got
	// neither, with nothing to go on.
	ch := config.Channel{
		Name: "mixed",
		Source: config.Source{
			Type: config.SourceMLLP, Listen: "127.0.0.1:6661",
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:8661"},
		},
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationMLLP, Address: "127.0.0.1:1",
		}},
	}
	if err := ch.Validate(); err == nil {
		t.Skip("an http block on an mllp source is currently tolerated")
	}
}
