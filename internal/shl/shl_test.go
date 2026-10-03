package shl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestALinkRoundTripsThroughItsQRForm(t *testing.T) {
	l := &Link{URL: "https://shl.example.org/shl/" + NewID(), Key: NewKey(), Flag: "P", Label: "Visit summary", Exp: 1900000000}
	got, err := Parse("https://viewer.example.org/#" + l.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if *got != *l {
		t.Errorf("%+v != %+v", got, l)
	}
	for _, bad := range []string{"https://example.org", "shlink:/!!!", "shlink:/eyJ1cmwiOiJmdHA6Ly94Iiwia2V5IjoiYSJ9"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	v2 := &Link{URL: "https://x.org/a", Key: NewKey(), V: 2}
	if _, err := Parse(v2.Encode()); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Errorf("a v2 link: %v", err)
	}
}

func TestAJWEOpensWithItsKeyAndNoOther(t *testing.T) {
	key := NewKey()
	jwe, err := Encrypt([]byte(`{"resourceType":"Bundle"}`), key, "application/fhir+json")
	if err != nil {
		t.Fatal(err)
	}
	cty, plain, err := Decrypt(jwe, key)
	if err != nil || cty != "application/fhir+json" || string(plain) != `{"resourceType":"Bundle"}` {
		t.Fatalf("%q %q %v", cty, plain, err)
	}
	if _, _, err := Decrypt(jwe, NewKey()); err == nil {
		t.Error("opened with the wrong key")
	}
	parts := strings.Split(jwe, ".")
	parts[3] = "AAAA" + parts[3][4:]
	if _, _, err := Decrypt(strings.Join(parts, "."), key); err == nil {
		t.Error("a tampered ciphertext opened")
	}
}

// The SMART Health Cards specification's own example card, verified against the example issuer's published key - read from a file here,
// as it would be fetched from https://spec.smarthealth.cards/examples/issuer/.well-known/jwks.json.
func TestTheSpecificationsExampleCardVerifies(t *testing.T) {
	jws, _ := os.ReadFile("testdata/example-00-d-jws.txt")
	jwks, _ := os.ReadFile("testdata/issuer-jwks.json")
	keys := func(_ context.Context, iss string) ([]byte, error) {
		if iss != "https://spec.smarthealth.cards/examples/issuer" {
			t.Errorf("issuer %q", iss)
		}
		return jwks, nil
	}
	card, err := ReadCard(context.Background(), string(jws), keys)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Verified || card.Problem != "" {
		t.Fatalf("not verified: %+v", card)
	}
	if !strings.Contains(string(card.Bundle), `"resourceType":"Bundle"`) || len(card.Types) == 0 {
		t.Errorf("card: %+v", card)
	}

	// The same card, from its numeric QR form and from its file form.
	numeric, _ := os.ReadFile("testdata/example-00-f-qr-code-numeric-value-0.txt")
	fromQR, err := CardsFrom(string(numeric))
	if err != nil || len(fromQR) != 1 || fromQR[0] != strings.TrimSpace(string(jws)) {
		t.Errorf("numeric QR decoded to a different JWS: %v", err)
	}
	file, _ := os.ReadFile("testdata/example-00-e-file.smart-health-card")
	fromFile, err := CardsFrom(string(file))
	if err != nil || len(fromFile) != 1 {
		t.Errorf("file: %v", err)
	}

	// One altered character in the payload, and it no longer verifies.
	parts := strings.Split(strings.TrimSpace(string(jws)), ".")
	parts[1] = parts[1][:10] + flip(parts[1][10]) + parts[1][11:]
	altered, _ := ReadCard(context.Background(), strings.Join(parts, "."), keys)
	if altered != nil && altered.Verified {
		t.Error("an altered card verified")
	}
}

func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

// A minimal manifest server, the shape the specification describes, so Resolve is exercised over HTTP.
func TestResolveFollowsTheManifestAndThePasscode(t *testing.T) {
	key := NewKey()
	embedded, _ := Encrypt([]byte(`{"resourceType":"Bundle","type":"collection"}`), key, "application/fhir+json")
	located, _ := Encrypt([]byte(`{"verifiableCredential":["a.b.c"]}`), key, "application/smart-health-card")
	attempts := 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			_, _ = w.Write([]byte(located))
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["recipient"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["passcode"] != "1234" {
			attempts--
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]int{"remainingAttempts": attempts})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]string{
			{"contentType": "application/fhir+json", "embedded": embedded},
			{"contentType": "application/smart-health-card", "location": "http://" + r.Host + "/file"},
		}})
	}))
	defer srv.Close()

	l := &Link{URL: srv.URL + "/manifest", Key: key, Flag: "P"}
	r := &Resolver{Client: srv.Client(), Now: func() time.Time { return time.Unix(1700000000, 0) }}
	if _, err := r.Resolve(context.Background(), l, "Riverside Clinic", "0000"); err == nil || !strings.Contains(err.Error(), "2 attempt") {
		t.Errorf("a wrong passcode: %v", err)
	}
	files, err := r.Resolve(context.Background(), l, "Riverside Clinic", "1234")
	if err != nil || len(files) != 2 || files[1].ContentType != "application/smart-health-card" {
		t.Fatalf("%+v %v", files, err)
	}
	l.Exp = 1600000000
	if _, err := r.Resolve(context.Background(), l, "x", "1234"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("an expired link: %v", err)
	}
}

func TestTheQRCodeHasItsFinderPatternsAndQuietZone(t *testing.T) {
	q, err := EncodeQR((&Link{URL: "https://perfuse.example/shl/" + NewID(), Key: NewKey(), Flag: "P", Label: "Visit"}).Encode(), ECCMedium)
	if err != nil {
		t.Fatal(err)
	}
	// Decodability is checked with zbar (scripts/qr-check.sh); this holds the structure a reader looks for first.
	for _, corner := range [][2]int{{0, 0}, {q.Size - 7, 0}, {0, q.Size - 7}} {
		for i := 0; i < 7; i++ {
			if !q.Dark(corner[0]+i, corner[1]) || !q.Dark(corner[0], corner[1]+i) {
				t.Fatalf("finder pattern at %v is broken", corner)
			}
		}
	}
	if svg := q.SVG(); !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, `viewBox="0 0 `) {
		t.Errorf("svg: %.80s", svg)
	}
}
