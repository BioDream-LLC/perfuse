package engine

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mailbox"
)

func mailWithAttachment(from, name string, body []byte) []byte {
	return []byte("From: " + from + "\r\nTo: feed@lab.example\r\nSubject: results\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"x\"\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nattached\r\n--x\r\n" +
		"Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(body) + "\r\n--x--\r\n")
}

func TestEmailMessagesTakesWhatIsAsked(t *testing.T) {
	raw := mailWithAttachment("Lab <results@lab.example>", "a.hl7", []byte(lsADT))
	cfg := &config.EmailSource{Host: "h", Username: "u", Password: "p", AttachmentNames: []string{"*.HL7"}}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	msgs, skip, err := emailMessages(cfg, raw)
	if err != nil || skip != "" || len(msgs) != 1 || string(msgs[0]) != lsADT {
		t.Fatalf("%q %q %v", msgs, skip, err)
	}
	cfg.AttachmentNames = []string{"*.csv"}
	if _, skip, _ := emailMessages(cfg, raw); skip == "" {
		t.Error("an attachment outside attachment_names was taken")
	}
	cfg.AttachmentNames, cfg.AllowedSenders = nil, []string{"clinic.example"}
	if _, skip, _ := emailMessages(cfg, raw); !strings.Contains(skip, "allowed_senders") {
		t.Errorf("a sender outside allowed_senders: %q", skip)
	}
	cfg.AllowedSenders, cfg.Take = nil, config.EmailTakeBody
	if msgs, _, _ := emailMessages(cfg, raw); len(msgs) != 1 || string(msgs[0]) != "attached" {
		t.Errorf("body %q", msgs)
	}
	cfg.Take = config.EmailTakeRaw
	if msgs, _, _ := emailMessages(cfg, raw); len(msgs) != 1 || string(msgs[0]) != string(raw) {
		t.Error("raw")
	}
}

func TestEmailSourceSettingsAreChecked(t *testing.T) {
	bad := []config.EmailSource{
		{Host: "h", Username: "u"},
		{Host: "mail.example", Username: "u", Password: "p", Security: "none"},
		{Protocol: "pop3", Host: "h", Username: "u", Password: "p", AfterRead: "move"},
		{Host: "h", Username: "u", Password: "p", MoveTo: "INBOX"},
		{Host: "h", Username: "u", OAuth: &config.EmailOAuth{Provider: "google", ClientID: "c", ClientSecret: "s"}},
		{Host: "h", Username: "u", Password: "p", PollInterval: time.Second},
	}
	for i, b := range bad {
		if len(b.Validate()) == 0 {
			t.Errorf("%d was accepted: %+v", i, b)
		}
	}
	ok := config.EmailSource{Protocol: "pop3", Host: "h", Username: "u", Password: "${P}"}
	if errs := ok.Validate(); len(errs) > 0 || ok.Port != 995 || ok.AfterRead != "delete" {
		t.Errorf("%v %+v", errs, ok)
	}
}

// Against GreenMail: three emails arrive - a good HL7 attachment, one that is not HL7, and one from a sender not allowed. The good
// one is delivered and moves to Processed; the other two move to Error.
func TestAnEmailChannelAgainstGreenMail(t *testing.T) {
	host := os.Getenv("PERFUSE_TEST_GREENMAIL")
	if host == "" {
		t.Skip("set PERFUSE_TEST_GREENMAIL to a GreenMail host (greenmail/standalone, user feed:feedpass@lab.example)")
	}
	if c, err := net.DialTimeout("tcp", host+":3143", time.Second); err != nil {
		t.Skip("no GreenMail IMAP")
	} else {
		_ = c.Close()
	}
	stamp := time.Now().Format("150405.000")
	inbox, done, errs := "In-"+stamp, "Done-"+stamp, "Err-"+stamp
	imap, err := mailbox.DialIMAP(t.Context(), mailbox.Dial{Host: host, Port: 3143, Security: mailbox.SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := imap.Login(t.Context(), mailbox.Auth{Username: "feed", Password: "feedpass"}); err != nil {
		t.Fatal(err)
	}
	_ = imap.Create(inbox)
	for _, m := range [][]byte{
		mailWithAttachment("results@lab.example", "good.hl7", []byte(lsADT)),
		mailWithAttachment("results@lab.example", "bad.hl7", []byte("not hl7")),
		mailWithAttachment("someone@else.example", "x.hl7", []byte(lsADT)),
	} {
		if err := imap.Append(inbox, m); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	runChannel(t, fmt.Sprintf(`
name: mail
source:
  type: email
  email:
    host: %s
    port: 3143
    security: none
    username: feed
    password: feedpass
    mailbox: %s
    move_to: %s
    error_folder: %s
    attachment_names: ["*.hl7"]
    allowed_senders: [lab.example]
destinations:
  - name: out
    type: file
    dir: %s
`, strings.Replace(host, "127.0.0.1", "localhost", 1), inbox, done, errs, out))

	count := func(box string) int {
		if err := imap.Select(box); err != nil {
			return -1
		}
		u, _ := imap.Unseen()
		return len(u)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !(count(done) == 1 && count(errs) == 2) {
		time.Sleep(300 * time.Millisecond)
	}
	if d, e, i := count(done), count(errs), count(inbox); d != 1 || e != 2 || i != 0 {
		t.Fatalf("processed %d, error %d, left %d", d, e, i)
	}
	files, _ := filepath.Glob(filepath.Join(out, "*"))
	if len(files) != 1 {
		t.Fatalf("delivered %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "ADT^A01") {
		t.Errorf("delivered %q", b)
	}
	imap.Logout()
}

// POP3 against GreenMail's second user: the good email is delivered and deleted; the one not taken stays on the server.
func TestAnEmailChannelOverPOP3AgainstGreenMail(t *testing.T) {
	host := os.Getenv("PERFUSE_TEST_GREENMAIL")
	if host == "" {
		t.Skip("set PERFUSE_TEST_GREENMAIL (GreenMail with user pop:poppass@lab.example)")
	}
	send := func(m []byte) {
		if err := smtp.SendMail(host+":3025", nil, "results@lab.example", []string{"pop@lab.example"}, m); err != nil {
			t.Fatal(err)
		}
	}
	send(mailWithAttachment("results@lab.example", "good.hl7", []byte(lsADT)))
	send(mailWithAttachment("results@lab.example", "notes.txt", []byte("hello")))
	out := t.TempDir()
	runChannel(t, fmt.Sprintf(`
name: popmail
source:
  type: email
  email:
    protocol: pop3
    host: localhost
    port: 3110
    security: none
    username: pop
    password: poppass
    attachment_names: ["*.hl7"]
destinations:
  - name: out
    type: file
    dir: %s
`, out))
	deadline := time.Now().Add(15 * time.Second)
	var files []string
	for time.Now().Before(deadline) && len(files) == 0 {
		files, _ = filepath.Glob(filepath.Join(out, "*"))
		time.Sleep(300 * time.Millisecond)
	}
	if len(files) != 1 {
		t.Fatalf("delivered %v", files)
	}
	time.Sleep(time.Second)
	c, err := mailbox.DialPOP3(t.Context(), mailbox.Dial{Host: host, Port: 3110, Security: mailbox.SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Quit()
	if err := c.Login(t.Context(), mailbox.Auth{Username: "pop", Password: "poppass"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := c.List(); len(left) != 1 {
		t.Errorf("%d emails left on the server; want the one not taken", len(left))
	}
}
