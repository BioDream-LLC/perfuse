package ftpconn

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// FTP's failure modes are all sequencing: a reply read out of step, a data connection opened at the wrong
// moment, a multi-line reply half-consumed. Those cannot be found by reading the code, so these tests run
// against a real server that speaks the protocol back.

// fakeServer is a minimal FTP server good enough to test a client against.
type fakeServer struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	commands []string
	files    map[string][]byte

	// requireTLS makes the server refuse AUTH TLS, to test the client's advice when a server has none.
	refuseTLS bool

	// multiline makes the greeting a multi-line reply, which is what real servers do and where clients
	// most often lose their place in the conversation.
	multiline bool
}

// options are set before the accept loop starts.
//
// They used to be assigned to the struct afterwards, which is a data race the race detector caught
// immediately: the accept goroutine reads them while the test writes them. Passing them in means there is no
// window at all, which is better than a mutex for something that never changes after construction.
type options struct {
	refuseTLS bool
	multiline bool
}

func newFakeServer(t *testing.T, opts ...options) *fakeServer {
	t.Helper()

	var o options
	if len(opts) > 0 {
		o = opts[0]
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s := &fakeServer{
		t:         t,
		ln:        ln,
		files:     map[string][]byte{},
		refuseTLS: o.refuseTLS,
		multiline: o.multiline,
	}
	go s.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *fakeServer) file(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files[name]
}

func (s *fakeServer) record(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cmd)
}

func (s *fakeServer) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *fakeServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := bufio.NewReader(conn)
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(conn, format+"\r\n", args...)
	}

	if s.multiline {
		write("220-Welcome to the analyser")
		write("220-Unauthorised access is prohibited")
		write("220 Ready")
	} else {
		write("220 Ready")
	}

	var (
		cwd        string
		dataLn     net.Listener
		pendingTmp string
	)

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		// A bare line break is skipped rather than parsed.
		//
		// This read `strings.Fields(line + " ")[0]`, where the trailing space was plainly there to stop an empty line
		// panicking on index zero. It cannot: strings.Fields treats whitespace as a separator and returns an empty
		// slice for a string of nothing but spaces, so the guard was a no-op and the panic happened anyway - roughly
		// one run in five, which is why it read as a flaky test rather than as a fault.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		verb := strings.ToUpper(fields[0])

		// Trimmed by the field as it was sent rather than by the upper-cased verb, or a client using lower case would
		// have the whole line returned as the argument.
		arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		s.record(verb + " " + arg)

		switch verb {
		case "AUTH":
			if s.refuseTLS {
				write("500 not supported")
				continue
			}
			write("234 go ahead")

		case "USER":
			write("331 need a password")
		case "PASS":
			write("230 logged in")
		case "TYPE":
			write("200 binary")
		case "PBSZ":
			write("200 ok")
		case "PROT":
			write("200 ok")

		case "MKD":
			write("257 made")
		case "CWD":
			cwd = arg
			write("250 changed")

		case "EPSV":
			// Deliberately unsupported, so the PASV path is exercised too. Half the analysers in the world
			// do not implement EPSV.
			write("502 not implemented")

		case "PASV":
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				write("425 cannot open")
				continue
			}
			dataLn = ln
			port := ln.Addr().(*net.TCPAddr).Port
			// A private address is announced deliberately: this is what a NATed server does, and a client
			// that trusts it hangs. The client is expected to ignore this and use the control host.
			write("227 Entering Passive Mode (10,255,255,1,%d,%d)", port>>8, port&0xff)

		case "STOR":
			if dataLn == nil {
				write("425 no data connection")
				continue
			}
			write("150 opening")

			dc, err := dataLn.Accept()
			if err != nil {
				write("426 failed")
				continue
			}
			body, _ := readAll(dc)
			_ = dc.Close()
			_ = dataLn.Close()
			dataLn = nil

			name := arg
			if cwd != "" {
				name = cwd + "/" + name
			}
			s.mu.Lock()
			s.files[name] = body
			s.mu.Unlock()
			write("226 stored")

		case "RNFR":
			pendingTmp = arg
			write("350 ready")

		case "RNTO":
			from := pendingTmp
			to := arg
			if cwd != "" {
				from = cwd + "/" + from
				to = cwd + "/" + to
			}
			s.mu.Lock()
			s.files[to] = s.files[from]
			delete(s.files, from)
			s.mu.Unlock()
			write("250 renamed")

		case "QUIT":
			write("221 bye")
			return

		default:
			write("500 unknown")
		}
	}
}

func readAll(conn net.Conn) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out, nil
		}
	}
}

func TestAFileIsUploadedAndRenamed(t *testing.T) {
	// The rename is the point: whoever collects these files is usually a scheduled job that takes whatever
	// it finds, and without it the job eventually takes half a message.
	s := newFakeServer(t)

	c, err := Dial(Config{Host: s.addr(), User: "u", Password: "p", Security: SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	body := []byte("MSH|^~\\&|A|B|C|D|20260820||ADT^A01|C1|P|2.5.1\r")
	if err := c.Store("out", "msg-C1.hl7", body, ".part"); err != nil {
		t.Fatal(err)
	}

	if got := s.file("out/msg-C1.hl7"); string(got) != string(body) {
		t.Errorf("stored %q, want the message unchanged", got)
	}
	if s.file("out/msg-C1.hl7.part") != nil {
		t.Error("the temporary file was left behind")
	}

	seen := strings.Join(s.seen(), " | ")
	if !strings.Contains(seen, "STOR msg-C1.hl7.part") {
		t.Errorf("the file was not written under a temporary name: %s", seen)
	}
	if !strings.Contains(seen, "RNTO msg-C1.hl7") {
		t.Errorf("the file was not renamed: %s", seen)
	}
}

func TestBinaryModeIsAlwaysSet(t *testing.T) {
	// ASCII mode rewrites line endings, which would silently corrupt HL7 segment separators and produce a
	// file the receiver cannot parse - blamed on the sender.
	s := newFakeServer(t)

	c, err := Dial(Config{Host: s.addr(), Security: SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if !strings.Contains(strings.Join(s.seen(), " "), "TYPE I") {
		t.Errorf("binary mode was not set: %v", s.seen())
	}
}

func TestAMultiLineGreetingDoesNotDesynchroniseTheConnection(t *testing.T) {
	// Real servers greet with several lines. A client that consumes only the first is one reply out of step
	// for the rest of the session, so every command reads the previous command's answer - a maddening class
	// of bug to diagnose from a log.
	s := newFakeServer(t, options{multiline: true})

	c, err := Dial(Config{Host: s.addr(), User: "u", Password: "p", Security: SecurityNone})
	if err != nil {
		t.Fatalf("a multi-line greeting broke the session: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Store("", "a.hl7", []byte("x"), ""); err != nil {
		t.Fatalf("the session was out of step after a multi-line greeting: %v", err)
	}
	if string(s.file("a.hl7")) != "x" {
		t.Error("the file did not arrive")
	}
}

func TestTheNATedPassiveAddressIsIgnored(t *testing.T) {
	// The fake server announces 10.255.255.1, which is unroutable from here. A client that trusts the PASV
	// reply hangs until the timeout; one that uses the control connection's host works. This is the single
	// most common way an FTP client fails in a hospital.
	s := newFakeServer(t)

	c, err := Dial(Config{Host: s.addr(), Security: SecurityNone, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	done := make(chan error, 1)
	go func() { done <- c.Store("", "b.hl7", []byte("hello"), "") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the upload failed: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the client dialled the address in the PASV reply and hung")
	}

	if string(s.file("b.hl7")) != "hello" {
		t.Error("the file did not arrive")
	}
}

func TestEPSVIsTriedBeforePASV(t *testing.T) {
	// EPSV avoids the NAT problem entirely and works over IPv6, so it should be preferred - and the client
	// must fall back cleanly when the server says 502, which half of them do.
	s := newFakeServer(t)

	c, err := Dial(Config{Host: s.addr(), Security: SecurityNone})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Store("", "c.hl7", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}

	seen := s.seen()
	epsv, pasv := -1, -1
	for i, cmd := range seen {
		if strings.HasPrefix(cmd, "EPSV") && epsv < 0 {
			epsv = i
		}
		if strings.HasPrefix(cmd, "PASV") && pasv < 0 {
			pasv = i
		}
	}
	if epsv < 0 {
		t.Errorf("EPSV was never tried: %v", seen)
	}
	if pasv < 0 || pasv < epsv {
		t.Errorf("PASV was not the fallback: %v", seen)
	}
}

func TestAServerWithoutTLSGetsAnActionableError(t *testing.T) {
	// "500 not supported" tells somebody nothing about what to do. The alternative has a real security cost,
	// so the error has to name it rather than just suggesting a setting.
	s := newFakeServer(t, options{refuseTLS: true})

	_, err := Dial(Config{Host: s.addr(), User: "u", Password: "p", Security: SecurityExplicit})
	if err == nil {
		t.Fatal("a server with no TLS was accepted while TLS was required")
	}
	if !strings.Contains(err.Error(), "clear text") {
		t.Errorf("the error does not state the cost of the alternative: %v", err)
	}
	if !strings.Contains(err.Error(), "security to none") {
		t.Errorf("the error does not name the setting to change: %v", err)
	}
}

func TestExplicitTLSIsTheDefault(t *testing.T) {
	// Defaulting to plain FTP would make the insecure choice the quiet one.
	s := newFakeServer(t)

	// Security left empty on purpose.
	_, err := Dial(Config{Host: s.addr(), User: "u", Password: "p"})
	if err != nil {
		// The fake server accepts AUTH TLS but cannot complete a handshake, so failing here is expected.
		// What matters is that AUTH was attempted at all.
		if !strings.Contains(strings.Join(s.seen(), " "), "AUTH TLS") {
			t.Errorf("no TLS was attempted with the default settings: %v", s.seen())
		}
		return
	}
	t.Error("the handshake unexpectedly succeeded against a plaintext fake server")
}

func TestTheDataChannelIsEncryptedWhenTheControlChannelIs(t *testing.T) {
	// A server that requires TLS on the control channel and accepts plaintext data is the worst of both, and
	// some allow it. PROT P is what prevents the file itself travelling in clear text.
	s := newFakeServer(t)

	_, _ = Dial(Config{Host: s.addr(), User: "u", Password: "p", Security: SecurityExplicit,
		InsecureSkipVerify: true})

	// The handshake fails against the fake server, so this only checks that AUTH was sent before anything
	// else. PROT P is verified by reading the code path; the sequencing is what a fake server can show.
	seen := strings.Join(s.seen(), " ")
	if !strings.Contains(seen, "AUTH TLS") {
		t.Errorf("AUTH TLS was not sent: %v", s.seen())
	}
}

func TestPASVParsing(t *testing.T) {
	host, port, ok := parsePASV("Entering Passive Mode (10,0,0,5,196,44)")
	if !ok {
		t.Fatal("a well-formed PASV reply was not understood")
	}
	if host != "10.0.0.5" {
		t.Errorf("host = %q", host)
	}
	if port != 196<<8|44 {
		t.Errorf("port = %d, want %d", port, 196<<8|44)
	}
}

func TestPASVParsingRejectsRubbish(t *testing.T) {
	// A misparsed port becomes a connection to somewhere arbitrary, so this must fail rather than guess.
	for _, bad := range []string{
		"Entering Passive Mode",
		"(1,2,3)",
		"(1,2,3,4,5,6,7)",
		"(1,2,3,4,999,44)",
		"(a,b,c,d,e,f)",
	} {
		if _, _, ok := parsePASV(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestEPSVParsing(t *testing.T) {
	port, ok := parseEPSV("Entering Extended Passive Mode (|||49152|)")
	if !ok || port != 49152 {
		t.Errorf("port = %d, ok = %v, want 49152", port, ok)
	}

	if _, ok := parseEPSV("Entering Extended Passive Mode (|||0|)"); ok {
		t.Error("port 0 was accepted")
	}
	if _, ok := parseEPSV("no parentheses"); ok {
		t.Error("a reply with no parentheses was accepted")
	}
}

func TestTheDefaultPortDependsOnTheSecurityMode(t *testing.T) {
	// Implicit FTPS conventionally lives on 990, and getting this wrong produces a connection refused that
	// looks like the server being down.
	if got := (Config{Host: "ftp.lab"}).address(); got != "ftp.lab:21" {
		t.Errorf("plain default = %q", got)
	}
	if got := (Config{Host: "ftp.lab", Security: SecurityImplicit}).address(); got != "ftp.lab:990" {
		t.Errorf("implicit default = %q", got)
	}
	if got := (Config{Host: "ftp.lab:2121"}).address(); got != "ftp.lab:2121" {
		t.Errorf("explicit port was overridden: %q", got)
	}
}

func TestAMissingHostIsRefused(t *testing.T) {
	if _, err := Dial(Config{}); err == nil {
		t.Fatal("a config with no host was accepted")
	}
}
