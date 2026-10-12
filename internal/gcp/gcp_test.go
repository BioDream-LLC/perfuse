package gcp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAServiceAccountTradesASignedJWTForAToken holds the JWT to what Google's token endpoint checks: RS256 over the
// header and claims with the key's private key, the key id in the header, the scope, and the token URI as audience.
func TestAServiceAccountTradesASignedJWTForAToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			http.Error(w, "grant", 400)
			return
		}
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			http.Error(w, "jwt", 400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			http.Error(w, "signature", 401)
			return
		}
		var h, c map[string]any
		hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
		cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(hb, &h)
		_ = json.Unmarshal(cb, &c)
		if h["alg"] != "RS256" || h["kid"] != "k1" || c["iss"] != "svc@p.iam.gserviceaccount.com" || c["scope"] != ScopePubSub ||
			c["aud"] != "http://"+r.Host+"/token" {
			http.Error(w, fmt.Sprint(h, c), 400)
			return
		}
		_, _ = w.Write([]byte(`{"access_token": "ya29.test", "expires_in": 3600}`))
	}))
	defer srv.Close()
	keyJSON, _ := json.Marshal(ServiceAccountKey{Type: "service_account", ClientEmail: "svc@p.iam.gserviceaccount.com",
		PrivateKeyID: "k1", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), TokenURI: srv.URL + "/token"})
	creds, err := FromKeyJSON(keyJSON, ScopePubSub)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tok, err := creds.Token(context.Background())
		if err != nil || tok != "ya29.test" {
			t.Fatalf("token %q: %v", tok, err)
		}
	}
	if calls != 1 {
		t.Errorf("the token was fetched %d times; it should be reused until it is about to expire", calls)
	}
	if _, err := FromKeyJSON([]byte(`{"type": "authorized_user"}`), ScopePubSub); err == nil {
		t.Error("a user credential was taken for a service account")
	}
}

func up(t *testing.T, env, addr string) string {
	if v := os.Getenv(env); v != "" {
		addr = v
	}
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skipf("nothing at %s; start the emulators with ./scripts/interop-up.sh", addr)
	}
	_ = c.Close()
	return "http://" + addr
}

func TestPubSubAgainstTheEmulator(t *testing.T) {
	ps, _ := NewPubSub(up(t, "PERFUSE_PUBSUB", "127.0.0.1:8085"), "perfuse-test", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	n := time.Now().UnixNano()
	topic, sub := fmt.Sprintf("t%d", n), fmt.Sprintf("s%d", n)
	if err := ps.CreateTopic(ctx, topic); err != nil {
		t.Fatal(err)
	}
	if err := ps.CreateSubscription(ctx, sub, topic, true); err != nil {
		t.Fatal(err)
	}
	body := []byte("MSH|^~\\&|A|B\rPID|1||123\r")
	ids, err := ps.Publish(ctx, topic, Message{Data: body, Attributes: map[string]string{"type": "ADT^A01"}, OrderingKey: "123"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("publish: %v %v", ids, err)
	}
	got := pullOne(ctx, t, ps, sub)
	if !bytes.Equal(got.Data, body) || got.Attributes["type"] != "ADT^A01" || got.ID != ids[0] {
		t.Fatalf("received %+v", got)
	}
	// Not handled: asked for again, and it comes back.
	if err := ps.Nack(ctx, sub, got.AckID); err != nil {
		t.Fatal(err)
	}
	again := pullOne(ctx, t, ps, sub)
	if again.ID != got.ID {
		t.Fatalf("after a nack got %s, want %s again", again.ID, got.ID)
	}
	if err := ps.Ack(ctx, sub, again.AckID); err != nil {
		t.Fatal(err)
	}
	if left, err := ps.Pull(ctx, sub, 10, 2*time.Second); err != nil || len(left) != 0 {
		t.Errorf("after the ack: %d messages, %v", len(left), err)
	}
}

func pullOne(ctx context.Context, t *testing.T, ps *PubSub, sub string) Message {
	t.Helper()
	for i := 0; i < 20; i++ {
		msgs, err := ps.Pull(ctx, sub, 1, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 1 {
			return msgs[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no message")
	return Message{}
}

func TestStorageAgainstAnEmulator(t *testing.T) {
	st, _ := NewStorage(up(t, "PERFUSE_GCS", "127.0.0.1:4443"), fmt.Sprintf("b%d", time.Now().UnixNano()), "NEARLINE", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := st.CreateBucket(ctx, "perfuse-test"); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("OBX|1|TX|||x\r"), 50000)
	for _, name := range []string{"inbound/a b.hl7", "inbound/big.hl7", "other/c.hl7"} {
		data := []byte("MSH|" + name)
		if strings.Contains(name, "big") {
			data = big
		}
		if err := st.Put(ctx, name, data, "application/hl7-v2"); err != nil {
			t.Fatal(err)
		}
	}
	// fake-gcs-server does not page (it answers one page with no token), so paging is not shown here.
	objs, _, err := st.List(ctx, "inbound/", "", 100)
	if err != nil || len(objs) != 2 || objs[1].Size != int64(len(big)) {
		t.Fatalf("list: %v %v", objs, err)
	}
	got, err := st.Get(ctx, "inbound/a b.hl7", 1<<20)
	if err != nil || string(got) != "MSH|inbound/a b.hl7" {
		t.Fatalf("get: %q %v", got, err)
	}
	if got, err := st.Get(ctx, "inbound/big.hl7", 1<<22); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("the large object: %d bytes, %v", len(got), err)
	}
	if _, err := st.Get(ctx, "inbound/big.hl7", 1000); err == nil {
		t.Error("an object over the limit was read")
	}
	if err := st.Delete(ctx, "inbound/a b.hl7"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "inbound/a b.hl7"); err != nil {
		t.Errorf("deleting what is already gone: %v", err)
	}
	if objs, _, _ := st.List(ctx, "inbound/", "", 10); len(objs) != 1 {
		t.Errorf("after the delete: %v", objs)
	}
}
