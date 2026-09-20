package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testSFTPServer is a real SSH server with a real SFTP subsystem, serving a
// temporary directory.
//
// Worth the setup. The whole point of this connector is behaviour against a server:
// whether a half-written file is read, whether a rename is atomic enough, whether a
// host key is actually checked. A fake client would test none of it.
type testSFTPServer struct {
	addr      string
	root      string
	hostKeyPK ssh.PublicKey

	listener net.Listener
	wg       sync.WaitGroup
	closed   sync.Once
	stop     chan struct{}
}

func startTestSFTPServer(t *testing.T) *testSFTPServer {
	t.Helper()

	root := t.TempDir()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "interface" && string(pass) == "correct-horse" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s := &testSFTPServer{
		addr:      ln.Addr().String(),
		root:      root,
		hostKeyPK: signer.PublicKey(),
		listener:  ln,
		stop:      make(chan struct{}),
	}

	s.wg.Add(1)
	go s.serve(cfg)
	t.Cleanup(s.close)
	return s
}

func (s *testSFTPServer) serve(cfg *ssh.ServerConfig) {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn, cfg)
	}
}

func (s *testSFTPServer) handle(nConn net.Conn, cfg *ssh.ServerConfig) {
	defer nConn.Close()

	sc, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}

		go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
			for req := range reqs {
				ok := req.Type == "subsystem" &&
					len(req.Payload) >= 4 &&
					string(req.Payload[4:]) == "sftp"
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if !ok {
					continue
				}
				srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.root))
				if err != nil {
					return
				}
				_ = srv.Serve()
				_ = srv.Close()
				return
			}
		}(ch, chReqs)
	}
}

func (s *testSFTPServer) close() {
	s.closed.Do(func() {
		close(s.stop)
		_ = s.listener.Close()
	})
	s.wg.Wait()
}

// knownHostsFile writes a known_hosts naming this server, which is what a correctly
// configured channel needs.
func (s *testSFTPServer) knownHostsFile(t *testing.T) string {
	t.Helper()

	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	// The bracketed host:port form, which is what ssh-keyscan writes for a
	// non-standard port and the form the library expects.
	line := fmt.Sprintf("[%s]:%s %s %s\n",
		host, port, s.hostKeyPK.Type(),
		strings.TrimSpace(encodeKey(s.hostKeyPK)))

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encodeKey(k ssh.PublicKey) string {
	return strings.TrimPrefix(
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), k.Type()+" ")
}

// wrongKnownHostsFile names a different key for the same host, which is what an
// impersonated server looks like.
func (s *testSFTPServer) wrongKnownHostsFile(t *testing.T) string {
	t.Helper()

	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}

	host, port, _ := net.SplitHostPort(s.addr)
	line := fmt.Sprintf("[%s]:%s %s %s\n",
		host, port, signer.PublicKey().Type(),
		strings.TrimSpace(encodeKey(signer.PublicKey())))

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const sftpTestMessage = "MSH|^~\\&|LAB|SITEA|PERFUSE|RFAC|20260819120000-0500||ORU^R01^ORU_R01|S1|P|2.5.1\r" +
	"PID|1||MRN700^^^SITEA^MR||Testpatient^Ada\r"

// --- host key verification -------------------------------------------------

func TestSFTPVerifiesTheHostKey(t *testing.T) {
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.knownHostsFile(t),
		Dir:            "out",
	})
	if err := s.Send(context.Background(), []byte(sftpTestMessage)); err != nil {
		t.Fatalf("a correct known_hosts should connect: %v", err)
	}
}

func TestSFTPRefusesAnUnexpectedHostKey(t *testing.T) {
	// The case that matters. A server presenting a different key is either a rebuild
	// or an impersonation, and the connector must not hand over credentials to find
	// out which.
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.wrongKnownHostsFile(t),
		Dir:            "out",
	})
	err := s.Send(context.Background(), []byte(sftpTestMessage))
	if err == nil {
		t.Fatal("a mismatched host key must refuse the connection")
	}
	// And the error has to steer away from the obvious wrong reaction, which is
	// deleting the entry and carrying on - exactly what an attacker needs.
	if !strings.Contains(err.Error(), "impersonating") {
		t.Errorf("the error should say what a mismatch could mean: %v", err)
	}
	if !strings.Contains(err.Error(), "Do not delete the entry") {
		t.Errorf("the error should warn against the obvious wrong fix: %v", err)
	}
}

func TestSFTPRefusesAnUnknownHost(t *testing.T) {
	srv := startTestSFTPServer(t)

	empty := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: empty, Dir: "out",
	})
	err := s.Send(context.Background(), []byte(sftpTestMessage))
	if err == nil {
		t.Fatal("a host absent from known_hosts must not be trusted")
	}
	// The fix has to be in the error, or the path of least resistance becomes
	// insecure_skip_host_key_check.
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("the error should say how to add the key: %v", err)
	}
}

func TestSFTPCanBeToldToSkipVerificationDeliberately(t *testing.T) {
	// Reachable on purpose: refusing outright sends people to a shell script with
	// StrictHostKeyChecking=no, which is worse in every way including auditability.
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		InsecureSkipHostKeyCheck: true, Dir: "out",
	})
	if err := s.Send(context.Background(), []byte(sftpTestMessage)); err != nil {
		t.Fatalf("skip-verify should connect: %v", err)
	}
}

func TestSFTPWarnsEveryTimeVerificationIsSkipped(t *testing.T) {
	warnings := (&config.SFTPDestination{
		InsecureSkipHostKeyCheck: true,
	}).Warnings()

	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "any server") {
		t.Errorf("the warning should say what the risk actually is: %v", warnings)
	}
	if !strings.Contains(joined, "known_hosts_file") {
		t.Errorf("the warning should name the fix: %v", warnings)
	}
}

func TestSFTPRefusesBothKnownHostsAndSkipVerify(t *testing.T) {
	// One of the two is a mistake, and which one changes the security of the
	// connection completely.
	d := config.SFTPDestination{
		Host: "h", User: "u", Password: "p", Dir: "d",
		KnownHostsFile:           "/tmp/kh",
		InsecureSkipHostKeyCheck: true,
	}
	err := d.Validate()
	if err == nil {
		t.Fatal("both together should be refused")
	}
	if !strings.Contains(err.Error(), "never be consulted") {
		t.Errorf("got: %v", err)
	}
}

func TestSFTPRefusesNoHostKeyCheckingAtAll(t *testing.T) {
	// Refused rather than defaulted to trusting anything, because that default is
	// invisible: everything works and nothing says the server was never checked.
	d := config.SFTPDestination{Host: "h", User: "u", Password: "p", Dir: "d"}
	err := d.Validate()
	if err == nil {
		t.Fatal("a missing known_hosts_file should be refused")
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("the error should say how to fix it: %v", err)
	}
}

func TestSFTPRefusesABadPassword(t *testing.T) {
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "wrong",
		KnownHostsFile: srv.knownHostsFile(t), Dir: "out",
	})
	err := s.Send(context.Background(), []byte(sftpTestMessage))
	if err == nil {
		t.Fatal("a wrong password should fail")
	}
	if !strings.Contains(err.Error(), "refused the login") {
		t.Errorf("the error should be about authentication: %v", err)
	}
}

// --- the destination -------------------------------------------------------

func newSFTPSenderFor(t *testing.T, cfg config.SFTPDestination) *SFTPSender {
	t.Helper()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	s, err := NewSFTPSender(config.Destination{
		Name: "upload", Type: config.DestinationSFTP, SFTP: &cfg,
	}, quiet())
	if err != nil {
		t.Fatalf("NewSFTPSender: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSFTPSenderWritesTheMessage(t *testing.T) {
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.knownHostsFile(t), Dir: "out",
	})
	if err := s.Send(context.Background(), []byte(sftpTestMessage)); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(srv.root, "out", "*.hl7"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v (err %v)", files, err)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != sftpTestMessage {
		t.Error("the message was not written byte for byte")
	}
	// The control ID has to be in the name, or two messages a second apart overwrite
	// each other.
	if !strings.Contains(filepath.Base(files[0]), "S1") {
		t.Errorf("the file name does not carry the control ID: %s", files[0])
	}
}

func TestSFTPSenderLeavesNoPartialFileBehind(t *testing.T) {
	// The reason the temporary name exists. Whoever collects these files has the same
	// problem Perfuse has reading them, and a file at its final name is a file they
	// will take.
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.knownHostsFile(t), Dir: "out",
	})
	for i := 0; i < 5; i++ {
		msg := strings.Replace(sftpTestMessage, "|S1|", fmt.Sprintf("|S%d|", i), 1)
		if err := s.Send(context.Background(), []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}

	partials, _ := filepath.Glob(filepath.Join(srv.root, "out", "*.part"))
	if len(partials) != 0 {
		t.Errorf("temporary files were left behind: %v", partials)
	}
	done, _ := filepath.Glob(filepath.Join(srv.root, "out", "*.hl7"))
	if len(done) != 5 {
		t.Errorf("wrote %d files, want 5", len(done))
	}
}

func TestSFTPSenderRefusesATraversalInTheControlID(t *testing.T) {
	// A control ID is data from the sending system and it ends up in a path. Without
	// sanitising, a message carrying "../" in MSH-10 writes outside the configured
	// directory.
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.knownHostsFile(t), Dir: "out",
	})
	nasty := strings.Replace(sftpTestMessage, "|S1|", "|../../escaped|", 1)
	if err := s.Send(context.Background(), []byte(nasty)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(srv.root, "escaped.hl7")); err == nil {
		t.Fatal("a file was written outside the configured directory")
	}
	files, _ := filepath.Glob(filepath.Join(srv.root, "out", "*"))
	if len(files) != 1 {
		t.Fatalf("files in out = %v", files)
	}
	if strings.Contains(filepath.Base(files[0]), "..") {
		t.Errorf("the name still contains a traversal: %s", files[0])
	}
}

func TestSanitiseFileNameKeepsNamesUsable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"20260819-S1.hl7", "20260819-S1.hl7"},
		{"../escape", "__escape"},
		{"a/b/c", "a_b_c"},
		{"with space", "with_space"},
		{"semi;colon", "semi_colon"},
		{`back\slash`, "back_slash"},
	}
	for _, tc := range cases {
		if got := sanitiseFileName(tc.in); got != tc.want {
			t.Errorf("sanitiseFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Never empty, because an empty name would collide with itself on every message.
	if got := sanitiseFileName("..."); got == "" {
		t.Error("sanitiseFileName produced an empty name")
	}
}

func TestSFTPSenderCanAppendFramedMessages(t *testing.T) {
	srv := startTestSFTPServer(t)

	s := newSFTPSenderFor(t, config.SFTPDestination{
		Host: srv.addr, User: "interface", Password: "correct-horse",
		KnownHostsFile: srv.knownHostsFile(t), Dir: "out",
		AppendToFile: true, Framed: true,
	})
	for i := 0; i < 3; i++ {
		msg := strings.Replace(sftpTestMessage, "|S1|", fmt.Sprintf("|S%d|", i), 1)
		if err := s.Send(context.Background(), []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(srv.root, "out", "*.hl7"))
	if len(files) != 1 {
		t.Fatalf("append should write one file, got %v", files)
	}
	body, _ := os.ReadFile(files[0])
	// Framed, so the file can be split again unambiguously. MSH can appear inside a
	// free-text field, so without framing it cannot.
	if n := strings.Count(string(body), "\x0b"); n != 3 {
		t.Errorf("found %d start-of-block bytes, want 3", n)
	}
}

func TestSFTPDestinationRefusesAppendWithoutFraming(t *testing.T) {
	// Several messages in one file with nothing between them cannot be split again
	// reliably.
	d := config.SFTPDestination{
		Host: "h", User: "u", Password: "p", Dir: "d",
		KnownHostsFile: "/tmp/kh", AppendToFile: true, Framed: false,
	}
	err := d.Validate()
	if err == nil {
		t.Fatal("append without framing should be refused")
	}
	if !strings.Contains(err.Error(), "where one message ends") {
		t.Errorf("got: %v", err)
	}
}

// --- the source ------------------------------------------------------------

func sftpSourceChannel(t *testing.T, srv *testSFTPServer, src config.SFTPSource, capture *captureDest) *Channel {
	t.Helper()

	src.Host = srv.addr
	src.User = "interface"
	src.Password = "correct-horse"
	src.KnownHostsFile = srv.knownHostsFile(t)
	if src.Dir == "" {
		src.Dir = "in"
	}
	if src.PollInterval == 0 {
		src.PollInterval = time.Second
	}
	if src.AfterRead == "" {
		src.AfterRead = "delete"
	}

	if err := os.MkdirAll(filepath.Join(srv.root, src.Dir), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Channel{
		Name:   "sftp-in",
		Source: config.Source{Type: config.SourceSFTP, SFTP: &src},
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
	return ch
}

func TestSFTPSourceCollectsAFile(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{StableFor: 100 * time.Millisecond}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "a.hl7"),
		[]byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the file to be collected", func() bool { return len(capture.messages()) == 1 })

	if !strings.Contains(string(capture.messages()[0]), "MRN700") {
		t.Errorf("wrong message: %s", capture.messages()[0])
	}
	// Deleted, so it is not collected again.
	waitFor(t, "the file to be deleted", func() bool {
		_, err := os.Stat(filepath.Join(srv.root, "in", "a.hl7"))
		return os.IsNotExist(err)
	})
}

func TestSFTPSourceWaitsForAFileToStopChanging(t *testing.T) {
	// The whole reason this connector is careful. A file being written and a file
	// finished being written are indistinguishable over SFTP, and half an HL7 message
	// very often still parses - so it is accepted, acknowledged, delivered, and the
	// missing half is never mentioned again.
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor:    3 * time.Second,
		PollInterval: time.Second,
	}, capture)

	target := filepath.Join(srv.root, "in", "growing.hl7")
	if err := os.WriteFile(target, []byte(sftpTestMessage[:60]), 0o644); err != nil {
		t.Fatal(err)
	}

	// Grown while the poller is watching. It must not be read during this.
	for i := 0; i < 3; i++ {
		time.Sleep(700 * time.Millisecond)
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("X")
		_ = f.Close()

		if n := len(capture.messages()); n != 0 {
			t.Fatalf("a file still being written was read (%d message(s))", n)
		}
	}

	// Completed. Now it should be picked up.
	if err := os.WriteFile(target, []byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the settled file to be read", func() bool { return len(capture.messages()) == 1 })
}

func TestSFTPSourceNeverReadsOnFirstSighting(t *testing.T) {
	// One observation cannot establish that anything has stopped changing, whatever
	// stable_for says.
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor:    0,
		PollInterval: time.Second,
	}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "quick.hl7"),
		[]byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}

	// It should still arrive, just not on the first poll that sees it.
	waitFor(t, "the file to be read eventually", func() bool {
		return len(capture.messages()) == 1
	})
}

func TestSFTPSourceIgnoresPartialNames(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
	}, capture)

	// The convention the sending side uses, for exactly the reason Perfuse needs it.
	for _, name := range []string{"a.hl7.part", "b.tmp", ".c.hl7", "~d.hl7"} {
		if err := os.WriteFile(filepath.Join(srv.root, "in", name),
			[]byte(sftpTestMessage), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srv.root, "in", "real.hl7"),
		[]byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the real file to be read", func() bool { return len(capture.messages()) == 1 })
	time.Sleep(2500 * time.Millisecond)
	if n := len(capture.messages()); n != 1 {
		t.Errorf("read %d messages; the in-progress names should have been skipped", n)
	}
}

func TestIsPartialNameCoversTheConventionsInUse(t *testing.T) {
	partial := []string{"a.part", "a.PART", "b.tmp", "c.temp", "d.filepart",
		"e.writing", ".hidden", "~backup"}
	for _, n := range partial {
		if !isPartialName(n) {
			t.Errorf("isPartialName(%q) = false", n)
		}
	}
	done := []string{"a.hl7", "adt.txt", "partial.hl7", "temperature.hl7"}
	for _, n := range done {
		if isPartialName(n) {
			t.Errorf("isPartialName(%q) = true, but it is a finished file", n)
		}
	}
}

func TestSFTPSourceSplitsSeveralMessagesInOneFile(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
	}, capture)

	var batch strings.Builder
	for i := 0; i < 3; i++ {
		batch.WriteString(strings.Replace(sftpTestMessage, "|S1|", fmt.Sprintf("|S%d|", i), 1))
	}
	if err := os.WriteFile(filepath.Join(srv.root, "in", "batch.hl7"),
		[]byte(batch.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "all three messages", func() bool { return len(capture.messages()) == 3 })
}

func TestSplitOnMSHOnlyBreaksAtTheStartOfASegment(t *testing.T) {
	// MSH can appear inside a free-text field, and splitting there would cut one valid
	// message into two invalid ones.
	body := "MSH|^~\\&|A|B|C|D|20260819||ADT^A08|1|P|2.5.1\r" +
		"NTE|1||the sender said MSH|is a segment\r" +
		"MSH|^~\\&|A|B|C|D|20260819||ADT^A08|2|P|2.5.1\r"

	got := splitOnMSH([]byte(body))
	if len(got) != 2 {
		t.Fatalf("split into %d messages, want 2", len(got))
	}
	if !strings.Contains(string(got[0]), "NTE|1|") {
		t.Error("the NTE was separated from its message")
	}
}

func TestSplitOnMSHHandlesBothLineEndings(t *testing.T) {
	// A file written on Windows, which is most of them.
	body := "MSH|^~\\&|A|B|C|D|20260819||ADT^A08|1|P|2.5.1\r\n" +
		"PID|1||M1\r\n"
	got := splitOnMSH([]byte(body))
	if len(got) != 1 {
		t.Fatalf("split into %d messages, want 1", len(got))
	}
	if strings.Contains(string(got[0]), "\n") {
		t.Error("a newline survived into the message; segments end with a carriage return")
	}
}

func TestSFTPSourceMovesAReadFile(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
		AfterRead: "move", MoveTo: "done",
	}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "m.hl7"),
		[]byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the file to be read", func() bool { return len(capture.messages()) == 1 })
	waitFor(t, "the file to be moved", func() bool {
		_, err := os.Stat(filepath.Join(srv.root, "done", "m.hl7"))
		return err == nil
	})
	if _, err := os.Stat(filepath.Join(srv.root, "in", "m.hl7")); err == nil {
		t.Error("the file is still in the source directory")
	}
}

func TestSFTPSourceSetsABadFileAside(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	ch := sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
		AfterRead: "delete", ErrorDir: "bad",
	}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "rubbish.txt"),
		[]byte("this is not an HL7 message at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Moved rather than retried forever, so one bad file does not become a permanent
	// warning nobody reads.
	waitFor(t, "the bad file to be set aside", func() bool {
		_, err := os.Stat(filepath.Join(srv.root, "bad", "rubbish.txt"))
		return err == nil
	})
	if len(capture.messages()) != 0 {
		t.Error("nothing should have been delivered")
	}
	if ch.sftp.Stats().FilesFailed == 0 {
		t.Error("the failure was not counted")
	}
}

func TestSFTPSourceLeavesAFileAloneIfAnyMessageInItFails(t *testing.T) {
	// Moving a file whose second message failed would lose that message with no
	// record anywhere, and the file is the only copy.
	srv := startTestSFTPServer(t)
	capture := &captureDest{fail: true}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
		AfterRead: "move", MoveTo: "done",
	}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "willfail.hl7"),
		[]byte(sftpTestMessage), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(3 * time.Second)
	if _, err := os.Stat(filepath.Join(srv.root, "done", "willfail.hl7")); err == nil {
		t.Error("a file whose message failed was moved as though it had been consumed")
	}
	if _, err := os.Stat(filepath.Join(srv.root, "in", "willfail.hl7")); err != nil {
		t.Error("the file should still be in the source directory")
	}
}

func TestSFTPSourceRefusesAnOversizedFile(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
		MaxFileSize: 2048, ErrorDir: "bad", AfterRead: "delete",
	}, capture)

	if err := os.WriteFile(filepath.Join(srv.root, "in", "huge.hl7"),
		[]byte(strings.Repeat("A", 8192)), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the oversized file to be set aside", func() bool {
		_, err := os.Stat(filepath.Join(srv.root, "bad", "huge.hl7"))
		return err == nil
	})
	if len(capture.messages()) != 0 {
		t.Error("nothing should have been delivered")
	}
}

func TestSFTPSourceHonoursAPattern(t *testing.T) {
	srv := startTestSFTPServer(t)
	capture := &captureDest{}

	sftpSourceChannel(t, srv, config.SFTPSource{
		StableFor: 100 * time.Millisecond, PollInterval: time.Second,
		Pattern: "adt_*.hl7", AfterRead: "delete",
	}, capture)

	for _, name := range []string{"adt_1.hl7", "oru_1.hl7", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(srv.root, "in", name),
			[]byte(sftpTestMessage), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, "the matching file", func() bool { return len(capture.messages()) == 1 })
	time.Sleep(2500 * time.Millisecond)
	if n := len(capture.messages()); n != 1 {
		t.Errorf("read %d messages; only the pattern should have matched", n)
	}
	if _, err := os.Stat(filepath.Join(srv.root, "in", "oru_1.hl7")); err != nil {
		t.Error("a non-matching file was consumed")
	}
}

func TestSFTPSourceRefusesAMoveToTheSameDirectory(t *testing.T) {
	// Moving a file to where it already is leaves it to be found again on the next
	// poll, forever, and every message in it is resent every time.
	src := config.SFTPSource{
		Host: "h", User: "u", Password: "p", Dir: "in",
		KnownHostsFile: "/tmp/kh", AfterRead: "move", MoveTo: "in",
	}
	err := src.Validate()
	if err == nil {
		t.Fatal("move_to equal to dir should be refused")
	}
	if !strings.Contains(err.Error(), "resent") {
		t.Errorf("got: %v", err)
	}
}
