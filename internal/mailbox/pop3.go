package mailbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// POP3 is one signed-in POP3 connection. Deletions take effect at Quit.
type POP3 struct {
	conn net.Conn
	r    *bufio.Reader
	d    Dial
	caps map[string]bool
}

// Message is one message on a POP3 server.
type Message struct {
	Number int
	UIDL   string
	Size   int64
}

// DialPOP3 connects, reads the greeting and, for starttls, upgrades with STLS (RFC 2595).
func DialPOP3(ctx context.Context, d Dial) (*POP3, error) {
	conn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	c := &POP3{conn: conn, r: bufio.NewReader(conn), d: d}
	c.deadline()
	if _, err := c.status(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("the POP3 server refused the connection: %w", err)
	}
	c.capa()
	if d.Security == SecurityStartTLS {
		if !c.caps["STLS"] {
			conn.Close()
			return nil, errors.New("the POP3 server does not offer STLS; use security tls, or none only on a private network")
		}
		if _, err := c.cmd("STLS"); err != nil {
			conn.Close()
			return nil, err
		}
		tc := tls.Client(conn, d.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("STLS: %w", err)
		}
		c.conn, c.r = tc, bufio.NewReader(tc)
		c.capa()
	}
	return c, nil
}

func (c *POP3) deadline() { _ = c.conn.SetDeadline(time.Now().Add(c.d.timeout())) }

// Close drops the connection without committing deletions.
func (c *POP3) Close() error { return c.conn.Close() }

// Quit commits deletions and ends the session.
func (c *POP3) Quit() error {
	_, err := c.cmd("QUIT")
	_ = c.conn.Close()
	return err
}

func (c *POP3) capa() {
	c.caps = map[string]bool{}
	if _, err := c.cmd("CAPA"); err != nil {
		return
	}
	lines, err := c.multiline(1 << 16)
	if err != nil {
		return
	}
	for _, l := range bytes.Split(lines, []byte("\r\n")) {
		f := strings.Fields(strings.ToUpper(string(l)))
		if len(f) == 0 {
			continue
		}
		c.caps[f[0]] = true
		if f[0] == "SASL" {
			for _, m := range f[1:] {
				c.caps["SASL="+m] = true
			}
		}
	}
}

func (c *POP3) status() (string, error) {
	l, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	l = strings.TrimRight(l, "\r\n")
	if strings.HasPrefix(l, "+OK") {
		return strings.TrimSpace(strings.TrimPrefix(l, "+OK")), nil
	}
	if strings.HasPrefix(l, "+") {
		return l, nil
	}
	return "", errors.New(clip(strings.TrimPrefix(l, "-ERR")))
}

func (c *POP3) cmd(line string) (string, error) {
	c.deadline()
	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		return "", err
	}
	return c.status()
}

// multiline reads a dot-terminated response.
func (c *POP3) multiline(max int64) ([]byte, error) {
	var out bytes.Buffer
	for {
		c.deadline()
		l, err := c.r.ReadSlice('\n')
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
		if bytes.Equal(bytes.TrimRight(l, "\r\n"), []byte(".")) && !errors.Is(err, bufio.ErrBufferFull) {
			b := out.Bytes()
			b = bytes.TrimSuffix(b, []byte("\r\n"))
			return dotUnstuff(b), nil
		}
		if int64(out.Len()+len(l)) > max {
			return nil, ErrTooLarge
		}
		out.Write(l)
	}
}

// Login signs in with XOAUTH2 (RFC 5034 AUTH) when a token source is set, else USER and PASS.
func (c *POP3) Login(ctx context.Context, a Auth) error {
	if a.Token != nil {
		tok, err := a.Token(ctx)
		if err != nil {
			return fmt.Errorf("getting an OAuth access token: %w", err)
		}
		resp := xoauth2(a.Username, tok)
		ch, err := c.cmd("AUTH XOAUTH2")
		if err != nil {
			return fmt.Errorf("the POP3 server does not offer XOAUTH2 sign-in: %w", err)
		}
		if strings.HasPrefix(ch, "+") {
			if _, err = c.cmd(resp); err != nil {
				return fmt.Errorf("the POP3 server refused the OAuth token: %w", err)
			}
			return nil
		}
		return nil
	}
	if _, err := c.cmd("USER " + a.Username); err != nil {
		return fmt.Errorf("the POP3 server refused the user name: %w", err)
	}
	if _, err := c.cmd("PASS " + a.Password); err != nil {
		return fmt.Errorf("the POP3 server refused the password: %w", err)
	}
	return nil
}

// List gives each message's number, unique id and size.
func (c *POP3) List() ([]Message, error) {
	if _, err := c.cmd("LIST"); err != nil {
		return nil, err
	}
	body, err := c.multiline(16 << 20)
	if err != nil {
		return nil, err
	}
	byNum := map[int]*Message{}
	var out []Message
	for _, l := range strings.Split(string(body), "\r\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, err1 := strconv.Atoi(f[0])
		size, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 == nil && err2 == nil {
			out = append(out, Message{Number: n, Size: size})
		}
	}
	for i := range out {
		byNum[out[i].Number] = &out[i]
	}
	if _, err := c.cmd("UIDL"); err == nil {
		if body, err := c.multiline(16 << 20); err == nil {
			for _, l := range strings.Split(string(body), "\r\n") {
				f := strings.Fields(l)
				if len(f) < 2 {
					continue
				}
				if n, err := strconv.Atoi(f[0]); err == nil && byNum[n] != nil {
					byNum[n].UIDL = f[1]
				}
			}
		}
	}
	return out, nil
}

// Retr reads one message.
func (c *POP3) Retr(n int, max int64) ([]byte, error) {
	if _, err := c.cmd("RETR " + strconv.Itoa(n)); err != nil {
		return nil, err
	}
	return c.multiline(max)
}

// Dele marks a message for deletion at Quit.
func (c *POP3) Dele(n int) error {
	_, err := c.cmd("DELE " + strconv.Itoa(n))
	return err
}
