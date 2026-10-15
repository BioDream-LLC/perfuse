package direct

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strings"
	"time"
)

// Receiver is the SMTP server a HISP delivers a Direct address's mail to: the Direct "edge" side of SMTP. It accepts mail
// only for the addresses it holds certificates for, requires STARTTLS before MAIL when it has a certificate (a Direct
// message is encrypted end to end, and the hop is protected as well), and hands each message to Deliver. A message Deliver
// refuses is refused at DATA with the reason, so the HISP keeps it and reports it.
type Receiver struct {
	Hostname string
	// Accept says whether mail for an address is taken.
	Accept func(rcpt string) bool
	// Deliver is given each message; an error refuses it.
	Deliver func(ctx context.Context, from string, to []string, raw []byte) error
	TLS     *tls.Config
	// RequireTLS refuses MAIL before STARTTLS.
	RequireTLS bool
	MaxBytes   int64
	Log        *slog.Logger
}

// Serve accepts connections until the listener is closed.
func (r *Receiver) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go r.session(c)
	}
}

func (r *Receiver) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Receiver) session(c net.Conn) {
	defer c.Close()
	max := r.MaxBytes
	if max <= 0 {
		max = 64 << 20
	}
	host := r.Hostname
	if host == "" {
		host = "perfuse"
	}
	rd := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	say := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		_ = w.Flush()
	}
	deadline := func() { _ = c.SetDeadline(time.Now().Add(5 * time.Minute)) }
	deadline()
	say("220 %s ESMTP Perfuse Direct", host)
	var from string
	var to []string
	secure := false
	for {
		deadline()
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			from, to = "", nil
			if strings.EqualFold(verb, "HELO") {
				say("250 %s", host)
				continue
			}
			fmt.Fprintf(w, "250-%s\r\n250-SIZE %d\r\n250-8BITMIME\r\n", host, max)
			if r.TLS != nil && !secure {
				fmt.Fprintf(w, "250-STARTTLS\r\n")
			}
			say("250 ENHANCEDSTATUSCODES")
		case "STARTTLS":
			if r.TLS == nil || secure {
				say("454 4.7.0 TLS not available")
				continue
			}
			say("220 2.0.0 Ready to start TLS")
			tc := tls.Server(c, r.TLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			c = tc
			rd, w = bufio.NewReader(tc), bufio.NewWriter(tc)
			secure, from, to = true, "", nil
		case "MAIL":
			if r.RequireTLS && !secure {
				say("530 5.7.0 Must issue a STARTTLS command first")
				continue
			}
			addr, ok := pathArg(arg, "FROM:")
			if !ok {
				say("501 5.5.4 Syntax: MAIL FROM:<address>")
				continue
			}
			from, to = addr, nil
			say("250 2.1.0 OK")
		case "RCPT":
			if from == "" && !strings.Contains(strings.ToUpper(arg), "<>") {
				say("503 5.5.1 MAIL first")
				continue
			}
			addr, ok := pathArg(arg, "TO:")
			if !ok {
				say("501 5.5.4 Syntax: RCPT TO:<address>")
				continue
			}
			if r.Accept != nil && !r.Accept(strings.ToLower(addr)) {
				say("550 5.1.1 %s is not a Direct address here", addr)
				continue
			}
			to = append(to, strings.ToLower(addr))
			say("250 2.1.5 OK")
		case "DATA":
			if len(to) == 0 {
				say("503 5.5.1 RCPT first")
				continue
			}
			say("354 End data with <CR><LF>.<CR><LF>")
			raw, err := readData(rd, max)
			if err != nil {
				say("552 5.3.4 %v", err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err = r.Deliver(ctx, from, to, raw)
			cancel()
			if err != nil {
				r.log().Warn("a Direct message was refused", "from", from, "to", to, "error", err)
				say("554 5.7.1 %s", strings.ReplaceAll(err.Error(), "\n", " "))
			} else {
				say("250 2.0.0 OK")
			}
			from, to = "", nil
		case "RSET":
			from, to = "", nil
			say("250 2.0.0 OK")
		case "NOOP":
			say("250 2.0.0 OK")
		case "QUIT":
			say("221 2.0.0 Bye")
			return
		default:
			say("502 5.5.2 Command not recognised")
		}
	}
}

func pathArg(arg, prefix string) (string, bool) {
	if !strings.HasPrefix(strings.ToUpper(arg), prefix) {
		return "", false
	}
	rest := strings.TrimSpace(arg[len(prefix):])
	end := strings.IndexByte(rest, '>')
	if !strings.HasPrefix(rest, "<") || end < 0 {
		return "", false
	}
	addr := rest[1:end]
	if addr == "" {
		return "", true
	}
	if _, err := mail.ParseAddress(addr); err != nil {
		return "", false
	}
	return addr, true
}

func readData(rd *bufio.Reader, max int64) ([]byte, error) {
	var out []byte
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil, errors.New("the connection closed during DATA")
			}
			return nil, err
		}
		if string(line) == ".\r\n" || string(line) == ".\n" {
			return out, nil
		}
		if len(line) > 1 && line[0] == '.' {
			line = line[1:]
		}
		out = append(out, line...)
		if int64(len(out)) > max {
			return nil, fmt.Errorf("the message is over %d bytes", max)
		}
	}
}
