// Package ftpconn uploads files over FTP and FTPS using only the standard library.
//
// FTP is old, chatty and not a good protocol. It is also still how a large number of hospital systems accept
// files, usually because the far end is a laboratory analyser or a bureau service whose software has not been
// touched since it worked. Refusing to support it does not make those systems modern; it makes Perfuse
// unusable at those sites.
//
// Implemented here rather than taken as a dependency for the same reason as S3: the eight-dependency bill of
// materials is a real argument with whoever reviews software before a hospital installs it. FTP's control
// channel is line-oriented text over TCP, so this is a few hundred lines of net and crypto/tls.
//
// # What is supported, and what is refused
//
// Passive mode only. Active mode requires the server to open a connection back to the client, which every
// hospital firewall built this century blocks, and offering it would mean people configuring it and then
// debugging a firewall.
//
// Explicit FTPS (AUTH TLS) is supported and is the default when TLS is asked for. Implicit FTPS on port 990
// is also supported because some analysers only speak that.
//
// Plain FTP is supported and warns. It sends the password in clear text, and a site that has to use it should
// be told once rather than left to find out from a packet capture.
package ftpconn

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Security describes how the connection is protected.
type Security string

const (
	// SecurityNone is plain FTP. The password crosses the network in clear text.
	SecurityNone Security = "none"

	// SecurityExplicit upgrades a plain connection with AUTH TLS. The usual choice.
	SecurityExplicit Security = "explicit"

	// SecurityImplicit expects TLS from the first byte, conventionally on port 990.
	SecurityImplicit Security = "implicit"
)

// Config describes the server.
type Config struct {
	// Host is the server, with an optional port. Defaults to 21, or 990 for implicit TLS.
	Host string

	// User and Password authenticate. An empty user means "anonymous".
	User     string
	Password string

	// Security selects TLS. Defaults to explicit, because that is the right answer and defaulting to
	// plain FTP would make the insecure choice the quiet one.
	Security Security

	// InsecureSkipVerify accepts any certificate.
	//
	// Needed more often than anybody would like: these servers frequently have a self-signed certificate
	// generated in 2011 that nobody can reissue. It is a separate, named setting so that choosing it is
	// deliberate and visible in the channel file.
	InsecureSkipVerify bool

	// Timeout bounds the whole transfer. Defaults to 60s.
	Timeout time.Duration
}

func (c Config) address() string {
	host := strings.TrimSpace(c.Host)
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if c.Security == SecurityImplicit {
		return net.JoinHostPort(host, "990")
	}
	return net.JoinHostPort(host, "21")
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 60 * time.Second
	}
	return c.Timeout
}

// Client is one FTP session.
type Client struct {
	cfg  Config
	conn net.Conn
	text *textproto.Conn

	// tlsConfig is kept so a data connection can be wrapped with the same settings as the control
	// connection. A server that requires TLS on the control channel and accepts a plaintext data channel is
	// the worst of both, and some do allow it - so this always protects both.
	tlsConfig *tls.Config
}

// Dial opens a session and logs in.
func Dial(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("an FTP destination needs a host")
	}
	if cfg.Security == "" {
		cfg.Security = SecurityExplicit
	}

	c := &Client{cfg: cfg}

	if cfg.Security != SecurityNone {
		c.tlsConfig = &tls.Config{
			ServerName:         hostOnly(cfg.Host),
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // deliberate, named setting
			MinVersion:         tls.VersionTLS12,
		}
	}

	conn, err := net.DialTimeout("tcp", cfg.address(), cfg.timeout())
	if err != nil {
		return nil, err
	}

	// One deadline for the whole session rather than per-operation. An FTP upload is a short conversation,
	// and a per-operation deadline would let a server keep a connection alive indefinitely by answering
	// slowly but regularly.
	_ = conn.SetDeadline(time.Now().Add(cfg.timeout()))

	if cfg.Security == SecurityImplicit {
		conn = tls.Client(conn, c.tlsConfig)
	}

	c.conn = conn
	c.text = textproto.NewConn(conn)

	if _, _, err := c.expect(220); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("the server did not greet us: %w", err)
	}

	if cfg.Security == SecurityExplicit {
		if err := c.startTLS(); err != nil {
			_ = c.Close()
			return nil, err
		}
	}

	if err := c.login(); err != nil {
		_ = c.Close()
		return nil, err
	}

	// Binary mode always. HL7 is bytes, and ASCII mode rewrites line endings - which would silently
	// corrupt segment separators and produce a file the receiver cannot parse, blamed on the sender.
	if err := c.cmd(200, "TYPE I"); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("the server refused binary mode: %w", err)
	}

	return c, nil
}

func (c *Client) startTLS() error {
	if err := c.cmd(234, "AUTH TLS"); err != nil {
		return fmt.Errorf("the server would not start TLS: %w; if it genuinely has no TLS, set "+
			"security to none and accept that the password crosses the network in clear text", err)
	}

	tlsConn := tls.Client(c.conn, c.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("the TLS handshake failed: %w", err)
	}

	c.conn = tlsConn
	c.text = textproto.NewConn(tlsConn)

	// PBSZ and PROT ask for the data channel to be encrypted too. Without PROT P the file itself goes in
	// clear text while the password did not, which is a false sense of security and, with patient data, the
	// part that actually matters.
	if err := c.cmd(200, "PBSZ 0"); err != nil {
		return fmt.Errorf("the server refused PBSZ 0: %w", err)
	}
	if err := c.cmd(200, "PROT P"); err != nil {
		return fmt.Errorf("the server would not encrypt the data channel: %w; it would send the file "+
			"itself in clear text", err)
	}

	return nil
}

func (c *Client) login() error {
	user := c.cfg.User
	if user == "" {
		user = "anonymous"
	}

	code, msg, err := c.command("USER " + user)
	if err != nil {
		return err
	}

	switch {
	case code == 230:
		// Logged in with no password wanted.
		return nil
	case code == 331:
		if err := c.cmd(230, "PASS "+c.cfg.Password); err != nil {
			// The server's own text is kept, because "530 Login incorrect" and "530 This account is
			// disabled" need different people to fix them.
			return fmt.Errorf("the server rejected the login: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unexpected reply to USER: %d %s", code, msg)
	}
}

// Store uploads one file, creating the directory if it is missing.
//
// Written to a temporary name and renamed, which is the same courtesy the SFTP connector provides and for the
// same reason: whoever collects these files is usually a scheduled job that takes whatever it finds, and
// without the rename it eventually takes half a message.
func (c *Client) Store(dir, name string, body []byte, tempSuffix string) error {
	if dir != "" && dir != "." {
		if err := c.ensureDir(dir); err != nil {
			return err
		}
		if err := c.cmd(250, "CWD "+dir); err != nil {
			return fmt.Errorf("could not change to %s: %w", dir, err)
		}
	}

	target := name
	if tempSuffix != "" {
		target = name + tempSuffix
	}

	data, err := c.openPassive()
	if err != nil {
		return err
	}

	// The reply to STOR arrives before the transfer completes, so the 150 is read here and the 226 after
	// the data connection closes. Reading them in the wrong order deadlocks, which is the classic FTP
	// implementation bug.
	if _, _, err := c.commandExpect(150, 125, "STOR "+target); err != nil {
		_ = data.Close()
		return fmt.Errorf("the server refused the upload: %w", err)
	}

	if _, err := data.Write(body); err != nil {
		_ = data.Close()
		return fmt.Errorf("the upload failed part way: %w", err)
	}

	// Closing the data connection is what tells the server the file is complete.
	if err := data.Close(); err != nil {
		return err
	}

	if _, _, err := c.commandlessExpect(226, 250); err != nil {
		return fmt.Errorf("the server did not confirm the upload: %w", err)
	}

	if tempSuffix != "" {
		if err := c.cmd(350, "RNFR "+target); err != nil {
			return fmt.Errorf("the file uploaded but could not be renamed from %s: %w", target, err)
		}
		if err := c.cmd(250, "RNTO "+name); err != nil {
			return fmt.Errorf("the file uploaded but could not be renamed to %s: %w", name, err)
		}
	}

	return nil
}

// ensureDir creates a directory, ignoring the error if it exists.
//
// MKD on an existing directory is an error on most servers and success on a few, and there is no portable way
// to ask. So it is attempted and the failure ignored, and a genuine problem surfaces on the CWD that follows -
// which reports the real reason rather than a misleading "could not create".
func (c *Client) ensureDir(dir string) error {
	_ = c.cmd(257, "MKD "+dir)
	return nil
}

// openPassive asks for a data connection and dials it.
func (c *Client) openPassive() (net.Conn, error) {
	// EPSV first, because it works with IPv6 and returns a port without an address, which avoids the
	// commonest FTP failure of all: a server behind NAT announcing its private address in the PASV reply
	// and the client dutifully dialling somewhere unreachable.
	if _, msg, err := c.commandExpect(229, 0, "EPSV"); err == nil {
		if port, ok := parseEPSV(msg); ok {
			return c.dialData(hostOnly(c.cfg.Host), port)
		}
	}

	_, msg, err := c.commandExpect(227, 0, "PASV")
	if err != nil {
		return nil, fmt.Errorf("the server would not open a data connection: %w", err)
	}

	host, port, ok := parsePASV(msg)
	if !ok {
		return nil, fmt.Errorf("could not understand the server's PASV reply: %s", msg)
	}

	// The control connection's host is used rather than the one in the reply. A server behind NAT reports
	// its private address here, and trusting it is the single most common way an FTP client hangs.
	_ = host
	return c.dialData(hostOnly(c.cfg.Host), port)
}

func (c *Client) dialData(host string, port int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), c.cfg.timeout())
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(c.cfg.timeout()))

	if c.tlsConfig != nil {
		// The same settings as the control channel, so a file cannot travel in clear text on a connection
		// whose password did not.
		tlsConn := tls.Client(conn, c.tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("the data connection's TLS handshake failed: %w", err)
		}
		return tlsConn, nil
	}

	return conn, nil
}

// Close ends the session politely, then closes the socket regardless.
func (c *Client) Close() error {
	if c.text != nil {
		// QUIT is a courtesy and its failure does not matter: the socket is closing either way, and a
		// server that has already gone away should not turn into an error the operator sees.
		_ = c.text.PrintfLine("QUIT")
		return c.text.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// cmd sends a command and requires one reply code.
//
// The line is sent verbatim, never as a format string. Commands here carry file names and directory names,
// and a file name containing a percent sign would otherwise be interpreted - producing a corrupted command
// and, with a temporary-file rename, potentially a file left behind under a name nobody can predict. vet
// caught this, which is the second time tonight a vet check has found a real bug rather than a style
// preference.
func (c *Client) cmd(want int, line string) error {
	_, _, err := c.commandExpect(want, 0, line)
	return err
}

func (c *Client) command(line string) (int, string, error) {
	if err := c.text.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	return c.readReply()
}

// commandExpect sends a command and accepts either of two codes.
func (c *Client) commandExpect(first, second int, line string) (int, string, error) {
	if err := c.text.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	return c.expect(first, second)
}

// commandlessExpect reads a reply without sending anything, for the completion code after a transfer.
func (c *Client) commandlessExpect(first, second int) (int, string, error) {
	return c.expect(first, second)
}

func (c *Client) expect(codes ...int) (int, string, error) {
	code, msg, err := c.readReply()
	if err != nil {
		return code, msg, err
	}
	for _, want := range codes {
		if want != 0 && code == want {
			return code, msg, nil
		}
	}
	// The server's own text is included. FTP servers say useful things in these lines and discarding them
	// leaves a number that means nothing to the person reading the log.
	return code, msg, fmt.Errorf("%d %s", code, msg)
}

// readReply reads one reply, joining a multi-line one.
func (c *Client) readReply() (int, string, error) {
	line, err := c.text.ReadLine()
	if err != nil {
		return 0, "", err
	}

	if len(line) < 4 {
		return 0, line, fmt.Errorf("the server sent a reply too short to understand: %q", line)
	}

	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, line, fmt.Errorf("the server sent a reply with no status code: %q", line)
	}

	// A hyphen after the code means more lines follow, terminated by the same code and a space. Getting this
	// wrong leaves the connection one reply out of step, and every later command reads the previous
	// command's answer - which is a maddening class of bug to diagnose from a log.
	if line[3] == '-' {
		prefix := line[:3] + " "
		var body strings.Builder
		body.WriteString(line[4:])
		for {
			next, err := c.text.ReadLine()
			if err != nil {
				return code, body.String(), err
			}
			if strings.HasPrefix(next, prefix) {
				body.WriteString(" ")
				body.WriteString(next[4:])
				break
			}
			body.WriteString(" ")
			body.WriteString(strings.TrimSpace(next))
		}
		return code, body.String(), nil
	}

	return code, line[4:], nil
}

// parsePASV reads the address from a PASV reply: 227 Entering Passive Mode (10,0,0,5,196,44)
func parsePASV(msg string) (string, int, bool) {
	open := strings.LastIndex(msg, "(")
	close := strings.LastIndex(msg, ")")
	if open < 0 || close < open {
		return "", 0, false
	}

	parts := strings.Split(msg[open+1:close], ",")
	if len(parts) != 6 {
		return "", 0, false
	}

	nums := make([]int, 6)
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 || n > 255 {
			return "", 0, false
		}
		nums[i] = n
	}

	host := fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], nums[3])
	return host, nums[4]<<8 | nums[5], true
}

// parseEPSV reads the port from an EPSV reply: 229 Entering Extended Passive Mode (|||49152|)
func parseEPSV(msg string) (int, bool) {
	open := strings.LastIndex(msg, "(")
	close := strings.LastIndex(msg, ")")
	if open < 0 || close < open {
		return 0, false
	}

	inner := msg[open+1 : close]
	parts := strings.Split(inner, "|")
	if len(parts) < 4 {
		return 0, false
	}

	port, err := strconv.Atoi(strings.TrimSpace(parts[3]))
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}
