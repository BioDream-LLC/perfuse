package mailbox

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"
)

const sample = "From: \"Lab System\" <Results@Lab.Example>\r\n" +
	"To: feed@lab.example\r\n" +
	"Subject: =?UTF-8?Q?R=C3=A9sultats?= batch\r\n" +
	"Message-ID: <abc-1@lab.example>\r\n" +
	"Date: Fri, 16 Oct 2026 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"b1\"\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b2\"\r\n" +
	"\r\n" +
	"--b2\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Two results attached =E2=80=94 see below.\r\n" +
	"--b2\r\n" +
	"Content-Type: text/html\r\n" +
	"\r\n" +
	"<p>Two results</p>\r\n" +
	"--b2--\r\n" +
	"--b1\r\n" +
	"Content-Type: application/octet-stream; name=\"oru1.hl7\"\r\n" +
	"Content-Disposition: attachment; filename=\"oru1.hl7\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"TVNIfF5+XCZ8TEFCfHxISVN8fDIwMjYxMDE2fHxPUlVeUjAxfDF8UHwyLjUNUElEfDF8fDEyMw==\r\n" +
	"--b1\r\n" +
	"Content-Type: text/csv; name=\"..\\\\..\\\\evil.csv\"\r\n" +
	"Content-Disposition: attachment\r\n" +
	"\r\n" +
	"a,b\r\n1,2\r\n" +
	"--b1--\r\n"

func TestParseFindsBodyAndAttachments(t *testing.T) {
	p, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if p.From != "results@lab.example" || p.Subject != "Résultats batch" || p.MessageID != "abc-1@lab.example" {
		t.Errorf("headers %+v", p)
	}
	if !strings.Contains(p.Text, "Two results attached — see below.") {
		t.Errorf("text %q", p.Text)
	}
	if len(p.Attachments) != 2 {
		t.Fatalf("attachments %d", len(p.Attachments))
	}
	if a := p.Attachments[0]; a.Filename != "oru1.hl7" || !strings.HasPrefix(string(a.Data), "MSH|^~\\&|LAB") {
		t.Errorf("first %q %q", a.Filename, a.Data)
	}
	// A path in a file name never survives: only its last element is kept.
	if a := p.Attachments[1]; a.Filename != "evil.csv" || string(a.Data) != "a,b\r\n1,2" {
		t.Errorf("second %q %q", a.Filename, a.Data)
	}
}

func TestSenderAllowed(t *testing.T) {
	allow := []string{"results@lab.example", "@clinic.example", "partner.example"}
	for from, want := range map[string]bool{
		"results@lab.example": true, "other@lab.example": false, "a@clinic.example": true, "b@partner.example": true,
		"c@evilpartner.example": false, "": false,
	} {
		if got := SenderAllowed(from, allow); got != want {
			t.Errorf("%q: %v", from, got)
		}
	}
	if !SenderAllowed("anyone@x", nil) {
		t.Error("an empty list allows everyone")
	}
}

func TestEncodeMailbox(t *testing.T) {
	for in, want := range map[string]string{"INBOX": "INBOX", "Résultats": "R&AOk-sultats", "A&B": "A&-B", "日本語": "&ZeVnLIqe-"} {
		if got := EncodeMailbox(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestOAuthRefreshAndCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt-1" ||
			r.Form.Get("scope") != "https://outlook.office.com/IMAP.AccessAsUser.All offline_access" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"at-1","expires_in":3600,"refresh_token":"rt-2"}`)
	}))
	defer srv.Close()
	o := &OAuth{Provider: ProviderMicrosoft, TokenURL: srv.URL, ClientID: "c", RefreshToken: "rt-1", Protocol: "imap"}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tok, err := o.Token(context.Background())
		if err != nil || tok != "at-1" {
			t.Fatalf("%q %v", tok, err)
		}
	}
	if calls != 1 || o.RefreshToken != "rt-2" {
		t.Errorf("calls %d, refresh token %q", calls, o.RefreshToken)
	}
	if (&OAuth{Provider: ProviderGoogle, ClientID: "c", ClientSecret: "s"}).Validate() == nil {
		t.Error("Gmail without a refresh token was accepted")
	}
	if (&OAuth{Provider: ProviderCustom, ClientID: "c", RefreshToken: "r", TokenURL: "http://idp.example/token"}).Validate() == nil {
		t.Error("plain-http token URL was accepted")
	}
	if got := (&OAuth{Provider: ProviderMicrosoft, Tenant: "contoso.example"}).Endpoint(); got != "https://login.microsoftonline.com/contoso.example/oauth2/v2.0/token" {
		t.Error(got)
	}
}

// TestGreenMail runs against GreenMail (greenmail/standalone, ports 3025/3143/3993/3110/3995, user feed:feedpass@lab.example):
//
//	PERFUSE_TEST_GREENMAIL=127.0.0.1 go test ./internal/mailbox -run GreenMail
func TestGreenMail(t *testing.T) {
	host := os.Getenv("PERFUSE_TEST_GREENMAIL")
	if host == "" {
		t.Skip("set PERFUSE_TEST_GREENMAIL to a GreenMail host")
	}
	ctx := context.Background()
	auth := Auth{Username: "feed", Password: "feedpass"}
	insecure := &tls.Config{InsecureSkipVerify: true} // GreenMail's own certificate
	send := func(subject string) {
		msg := strings.Replace(sample, "Subject: =?UTF-8?Q?R=C3=A9sultats?= batch", "Subject: "+subject, 1)
		if err := smtp.SendMail(host+":3025", nil, "results@lab.example", []string{"feed@lab.example"}, []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("imaps", func(t *testing.T) {
		send("imap-" + time.Now().Format("150405.000"))
		c, err := DialIMAP(ctx, Dial{Host: host, Port: 3993, Security: SecurityTLS, TLS: insecure})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Logout()
		if err := c.Login(ctx, Auth{Username: "feed", Password: "wrong"}); err == nil {
			t.Fatal("a wrong password was accepted")
		}
		if err := c.Login(ctx, auth); err != nil {
			t.Fatal(err)
		}
		if err := c.Select("INBOX"); err != nil {
			t.Fatal(err)
		}
		if err := c.Create("Processed"); err != nil {
			t.Fatal(err)
		}
		if err := c.Create("Processed"); err != nil {
			t.Fatalf("creating twice: %v", err)
		}
		uids, err := c.Unseen()
		if err != nil || len(uids) == 0 {
			t.Fatalf("unseen %v %v", uids, err)
		}
		raw, err := c.Fetch(uids[0], 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(raw)
		if err != nil || len(p.Attachments) != 2 {
			t.Fatalf("parsed %d attachments, %v", len(p.Attachments), err)
		}
		if _, err := c.Fetch(uids[0], 10); err == nil {
			t.Error("a message over the limit was read")
		}
		// Still unread: BODY.PEEK leaves \Seen alone.
		again, _ := c.Unseen()
		if len(again) != len(uids) {
			t.Errorf("fetching marked it read: %v then %v", uids, again)
		}
		if err := c.Move(uids[0], "Processed"); err != nil {
			t.Fatal(err)
		}
		after, _ := c.Unseen()
		for _, u := range after {
			if u == uids[0] {
				t.Error("the moved message is still in INBOX")
			}
		}
		if err := c.Select("Processed"); err != nil {
			t.Fatal(err)
		}
		moved, _ := c.Unseen()
		if len(moved) == 0 {
			t.Error("nothing arrived in Processed")
		}
		for _, u := range moved {
			if err := c.Delete(u); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("imap-starttls-and-flag", func(t *testing.T) {
		send("flag-" + time.Now().Format("150405.000"))
		c, err := DialIMAP(ctx, Dial{Host: host, Port: 3143, Security: SecurityNone})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Logout()
		if err := c.Login(ctx, auth); err != nil {
			t.Fatal(err)
		}
		if err := c.Select("INBOX"); err != nil {
			t.Fatal(err)
		}
		uids, _ := c.Unseen()
		if len(uids) == 0 {
			t.Fatal("no mail")
		}
		for _, u := range uids {
			if err := c.Flag(u, `\Seen`); err != nil {
				t.Fatal(err)
			}
		}
		if left, _ := c.Unseen(); len(left) != 0 {
			t.Errorf("still unread after flagging: %v", left)
		}
	})

	t.Run("pop3s", func(t *testing.T) {
		send("pop-" + time.Now().Format("150405.000"))
		c, err := DialPOP3(ctx, Dial{Host: host, Port: 3995, Security: SecurityTLS, TLS: insecure})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login(ctx, auth); err != nil {
			t.Fatal(err)
		}
		list, err := c.List()
		if err != nil || len(list) == 0 {
			t.Fatalf("list %v %v", list, err)
		}
		raw, err := c.Retr(list[0].Number, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if p, err := Parse(raw); err != nil || len(p.Attachments) != 2 {
			t.Fatalf("parsed %+v %v", p, err)
		}
		for _, m := range list {
			if err := c.Dele(m.Number); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Quit(); err != nil {
			t.Fatal(err)
		}
		c2, err := DialPOP3(ctx, Dial{Host: host, Port: 3110, Security: SecurityNone})
		if err != nil {
			t.Fatal(err)
		}
		defer c2.Quit()
		if err := c2.Login(ctx, auth); err != nil {
			t.Fatal(err)
		}
		if left, _ := c2.List(); len(left) != 0 {
			t.Errorf("%d messages left after DELE and QUIT", len(left))
		}
	})
}
