// Package mailbox reads mail from IMAP4rev1 (RFC 3501) and POP3 (RFC 1939) servers, signing in with a password or with OAuth 2.0
// (SASL XOAUTH2, as Microsoft 365 and Gmail require), and takes messages apart (RFC 5322 and MIME) so an attachment or the text
// body can become a message on a channel.
//
// Only what an inbound feed needs: select a mailbox, find unread mail, fetch it whole, then move, flag or delete it. Standard
// library only; TLS is crypto/tls.
package mailbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Security says how the connection is protected.
const (
	// SecurityTLS is TLS from the first byte (IMAPS 993, POP3S 995). The default.
	SecurityTLS = "tls"
	// SecurityStartTLS starts in clear and upgrades before anything is sent (IMAP 143 STARTTLS, POP3 110 STLS).
	SecurityStartTLS = "starttls"
	// SecurityNone is clear text, for a server on this machine or in a test only.
	SecurityNone = "none"
)

// Dial says where the server is.
type Dial struct {
	Host     string
	Port     int
	Security string
	// TLS is the client TLS configuration; nil uses the system roots and verifies the host name.
	TLS *tls.Config
	// Timeout bounds each step; zero is 60 seconds.
	Timeout time.Duration
}

// Auth is how to sign in: a password, or an OAuth access token from Token (XOAUTH2).
type Auth struct {
	Username string
	Password string
	Token    func(context.Context) (string, error)
}

func (d Dial) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 60 * time.Second
	}
	return d.Timeout
}

func (d Dial) tlsConfig() *tls.Config {
	if d.TLS != nil {
		c := d.TLS.Clone()
		if c.ServerName == "" {
			c.ServerName = d.Host
		}
		return c
	}
	return &tls.Config{ServerName: d.Host, MinVersion: tls.VersionTLS12}
}

func (d Dial) connect(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
	nd := &net.Dialer{Timeout: d.timeout()}
	if d.Security == SecurityTLS || d.Security == "" {
		return (&tls.Dialer{NetDialer: nd, Config: d.tlsConfig()}).DialContext(ctx, "tcp", addr)
	}
	return nd.DialContext(ctx, "tcp", addr)
}

// xoauth2 is the SASL XOAUTH2 initial response.
func xoauth2(user, token string) string {
	return base64.StdEncoding.EncodeToString([]byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01"))
}

// IMAP is one signed-in IMAP connection. Not safe for concurrent use.
type IMAP struct {
	conn    net.Conn
	r       *bufio.Reader
	d       Dial
	tag     int
	caps    map[string]bool
	maxSize int64
}

// Item is one untagged response, with any literal it carried.
type item struct {
	text     string
	literals [][]byte
}

// DialIMAP connects, reads the greeting and, for starttls, upgrades.
func DialIMAP(ctx context.Context, d Dial) (*IMAP, error) {
	conn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	c := &IMAP{conn: conn, r: bufio.NewReader(conn), d: d, maxSize: 64 << 20}
	c.deadline()
	greet, err := c.line()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("the IMAP server sent no greeting: %w", err)
	}
	if !strings.HasPrefix(greet, "* OK") && !strings.HasPrefix(greet, "* PREAUTH") {
		conn.Close()
		return nil, fmt.Errorf("the IMAP server refused the connection: %s", clip(greet))
	}
	if err := c.capability(); err != nil {
		conn.Close()
		return nil, err
	}
	if d.Security == SecurityStartTLS {
		if !c.caps["STARTTLS"] {
			conn.Close()
			return nil, errors.New("the IMAP server does not offer STARTTLS; use security tls, or none only on a private network")
		}
		if _, err := c.cmd("STARTTLS"); err != nil {
			conn.Close()
			return nil, err
		}
		tc := tls.Client(conn, d.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
		c.conn, c.r = tc, bufio.NewReader(tc)
		if err := c.capability(); err != nil {
			tc.Close()
			return nil, err
		}
	}
	return c, nil
}

// Close drops the connection without logging out.
func (c *IMAP) Close() error { return c.conn.Close() }

// Logout ends the session politely.
func (c *IMAP) Logout() {
	_, _ = c.cmd("LOGOUT")
	_ = c.conn.Close()
}

// Has reports a capability, such as MOVE or UIDPLUS.
func (c *IMAP) Has(capability string) bool { return c.caps[strings.ToUpper(capability)] }

func (c *IMAP) capability() error {
	items, err := c.cmd("CAPABILITY")
	if err != nil {
		return err
	}
	c.caps = map[string]bool{}
	for _, it := range items {
		if f := strings.Fields(it.text); len(f) > 0 && strings.EqualFold(f[0], "CAPABILITY") {
			for _, cp := range f[1:] {
				c.caps[strings.ToUpper(cp)] = true
			}
		}
	}
	return nil
}

func (c *IMAP) deadline() { _ = c.conn.SetDeadline(time.Now().Add(c.d.timeout())) }

// line reads one CRLF line without its ending.
func (c *IMAP) line() (string, error) {
	s, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(s) > 1<<20 {
		return "", errors.New("an IMAP response line is too long")
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// Login signs in with XOAUTH2 when a token source is set, else with the password.
func (c *IMAP) Login(ctx context.Context, a Auth) error {
	if a.Token != nil {
		tok, err := a.Token(ctx)
		if err != nil {
			return fmt.Errorf("getting an OAuth access token: %w", err)
		}
		if !c.caps["AUTH=XOAUTH2"] {
			return errors.New("the IMAP server does not offer XOAUTH2 sign-in")
		}
		resp := xoauth2(a.Username, tok)
		if c.caps["SASL-IR"] {
			_, err = c.cmdCont("AUTHENTICATE XOAUTH2 "+resp, "")
		} else {
			_, err = c.cmdCont("AUTHENTICATE XOAUTH2", resp)
		}
		if err != nil {
			return fmt.Errorf("the IMAP server refused the OAuth token: %w", err)
		}
	} else {
		if c.caps["LOGINDISABLED"] {
			return errors.New("the IMAP server allows no password sign-in on this connection (LOGINDISABLED): use TLS or OAuth")
		}
		if _, err := c.cmd("LOGIN " + quote(a.Username) + " " + quote(a.Password)); err != nil {
			return fmt.Errorf("the IMAP server refused the user name or password: %w", err)
		}
	}
	return c.capability()
}

// Select opens a mailbox for reading and changing.
func (c *IMAP) Select(mailbox string) error {
	_, err := c.cmd("SELECT " + quote(EncodeMailbox(mailbox)))
	if err != nil {
		return fmt.Errorf("opening mailbox %q: %w", mailbox, err)
	}
	return nil
}

// Create makes a mailbox, and is not an error when it exists.
func (c *IMAP) Create(mailbox string) error {
	_, err := c.cmd("CREATE " + quote(EncodeMailbox(mailbox)))
	if err != nil && !strings.Contains(strings.ToUpper(err.Error()), "EXIST") {
		return fmt.Errorf("creating mailbox %q: %w", mailbox, err)
	}
	return nil
}

// DeleteMailbox removes a mailbox: the tests' clean-up on a shared server.
func (c *IMAP) DeleteMailbox(mailbox string) error {
	_, err := c.cmd("DELETE " + quote(EncodeMailbox(mailbox)))
	return err
}

// Unseen lists the UIDs of unread messages, oldest first.
func (c *IMAP) Unseen() ([]uint32, error) {
	items, err := c.cmd("UID SEARCH UNSEEN")
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, it := range items {
		f := strings.Fields(it.text)
		if len(f) == 0 || !strings.EqualFold(f[0], "SEARCH") {
			continue
		}
		for _, n := range f[1:] {
			if u, err := strconv.ParseUint(n, 10, 32); err == nil {
				out = append(out, uint32(u))
			}
		}
	}
	return out, nil
}

// Fetch reads a whole message without marking it read. A message larger than max is refused before it is read.
func (c *IMAP) Fetch(uid uint32, max int64) ([]byte, error) {
	items, err := c.cmd(fmt.Sprintf("UID FETCH %d (RFC822.SIZE)", uid))
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if i := strings.Index(strings.ToUpper(it.text), "RFC822.SIZE "); i >= 0 {
			f := strings.Fields(it.text[i+len("RFC822.SIZE "):])
			if len(f) > 0 {
				if n, err := strconv.ParseInt(strings.TrimRight(f[0], ")"), 10, 64); err == nil && n > max {
					return nil, fmt.Errorf("%w: %d bytes, more than the %d allowed", ErrTooLarge, n, max)
				}
			}
		}
	}
	prev := c.maxSize
	c.maxSize = max + 1024
	defer func() { c.maxSize = prev }()
	items, err = c.cmd(fmt.Sprintf("UID FETCH %d (BODY.PEEK[])", uid))
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if strings.Contains(strings.ToUpper(it.text), "FETCH") && len(it.literals) > 0 {
			return it.literals[0], nil
		}
	}
	return nil, fmt.Errorf("message %d is no longer in the mailbox", uid)
}

// ErrTooLarge is a message over the size allowed.
var ErrTooLarge = errors.New("the message is too large")

// Flag adds a flag, such as \Seen.
func (c *IMAP) Flag(uid uint32, flag string) error {
	_, err := c.cmd(fmt.Sprintf("UID STORE %d +FLAGS.SILENT (%s)", uid, flag))
	return err
}

// Move puts a message in another mailbox: MOVE (RFC 6851) where offered, else copy, mark deleted and expunge.
func (c *IMAP) Move(uid uint32, mailbox string) error {
	if c.caps["MOVE"] {
		_, err := c.cmd(fmt.Sprintf("UID MOVE %d %s", uid, quote(EncodeMailbox(mailbox))))
		return err
	}
	if _, err := c.cmd(fmt.Sprintf("UID COPY %d %s", uid, quote(EncodeMailbox(mailbox)))); err != nil {
		return err
	}
	return c.Delete(uid)
}

// Delete marks a message deleted and expunges it: only it with UIDPLUS (RFC 4315), else every message marked deleted.
func (c *IMAP) Delete(uid uint32) error {
	if err := c.Flag(uid, `\Deleted`); err != nil {
		return err
	}
	if c.caps["UIDPLUS"] {
		_, err := c.cmd(fmt.Sprintf("UID EXPUNGE %d", uid))
		return err
	}
	_, err := c.cmd("EXPUNGE")
	return err
}

// Append adds a message to a mailbox (used by tests and by a test message from the console).
func (c *IMAP) Append(mailbox string, msg []byte) error {
	c.tag++
	tag := fmt.Sprintf("P%d", c.tag)
	c.deadline()
	if _, err := fmt.Fprintf(c.conn, "%s APPEND %s {%d}\r\n", tag, quote(EncodeMailbox(mailbox)), len(msg)); err != nil {
		return err
	}
	l, err := c.line()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(l, "+") {
		return fmt.Errorf("the server refused the message: %s", clip(l))
	}
	if _, err := c.conn.Write(append(append([]byte{}, msg...), '\r', '\n')); err != nil {
		return err
	}
	_, err = c.readUntil(tag)
	return err
}

func (c *IMAP) cmd(command string) ([]item, error) { return c.cmdCont(command, "") }

// cmdCont sends a command and, if the server asks for more ("+"), sends cont (or an empty line, which cancels a SASL exchange
// after an error challenge).
func (c *IMAP) cmdCont(command, cont string) ([]item, error) {
	c.tag++
	tag := fmt.Sprintf("P%d", c.tag)
	c.deadline()
	if _, err := io.WriteString(c.conn, tag+" "+command+"\r\n"); err != nil {
		return nil, err
	}
	var items []item
	sentCont := false
	for {
		l, err := c.line()
		if err != nil {
			return items, err
		}
		switch {
		case strings.HasPrefix(l, "+"):
			reply := ""
			if !sentCont {
				reply = cont
				sentCont = true
			}
			c.deadline()
			if _, err := io.WriteString(c.conn, reply+"\r\n"); err != nil {
				return items, err
			}
		case strings.HasPrefix(l, "* "):
			it, err := c.untagged(l[2:])
			if err != nil {
				return items, err
			}
			items = append(items, it)
		case strings.HasPrefix(l, tag+" "):
			rest := l[len(tag)+1:]
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return items, nil
			}
			return items, errors.New(clip(rest))
		}
	}
}

// readUntil reads responses until the tagged one.
func (c *IMAP) readUntil(tag string) ([]item, error) {
	var items []item
	for {
		l, err := c.line()
		if err != nil {
			return items, err
		}
		if strings.HasPrefix(l, "* ") {
			it, err := c.untagged(l[2:])
			if err != nil {
				return items, err
			}
			items = append(items, it)
			continue
		}
		if strings.HasPrefix(l, tag+" ") {
			rest := l[len(tag)+1:]
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return items, nil
			}
			return items, errors.New(clip(rest))
		}
	}
}

// untagged reads the rest of an untagged response, following literals ({n} at a line's end) across lines.
func (c *IMAP) untagged(first string) (item, error) {
	it := item{}
	l := first
	for {
		n, ok := literalAt(l)
		if !ok {
			it.text += l
			return it, nil
		}
		if int64(n) > c.maxSize {
			return it, fmt.Errorf("%w: the server is sending %d bytes", ErrTooLarge, n)
		}
		it.text += l[:strings.LastIndex(l, "{")] + "\x00"
		buf := make([]byte, n)
		c.deadline()
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return it, err
		}
		it.literals = append(it.literals, buf)
		next, err := c.line()
		if err != nil {
			return it, err
		}
		l = next
	}
}

// literalAt reports a literal's length when a line ends with {n}.
func literalAt(l string) (int, bool) {
	if !strings.HasSuffix(l, "}") {
		return 0, false
	}
	i := strings.LastIndex(l, "{")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(l[i+1:len(l)-1], "+"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// quote is an IMAP quoted string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// EncodeMailbox writes a mailbox name in IMAP's modified UTF-7 (RFC 3501 5.1.3), so a folder such as "Résultats" can be named.
func EncodeMailbox(name string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		u := utf16.Encode(run)
		raw := make([]byte, 0, len(u)*2)
		for _, x := range u {
			raw = append(raw, byte(x>>8), byte(x))
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		enc = strings.TrimRight(enc, "=")
		b.WriteString("&" + strings.ReplaceAll(enc, "/", ",") + "-")
		run = run[:0]
	}
	for _, r := range name {
		if r >= 0x20 && r <= 0x7e {
			flush()
			if r == '&' {
				b.WriteString("&-")
			} else {
				b.WriteRune(r)
			}
			continue
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// dotUnstuff undoes POP3's leading-dot escaping.
func dotUnstuff(b []byte) []byte {
	lines := bytes.Split(b, []byte("\r\n"))
	for i, l := range lines {
		if bytes.HasPrefix(l, []byte("..")) {
			lines[i] = l[1:]
		}
	}
	return bytes.Join(lines, []byte("\r\n"))
}
