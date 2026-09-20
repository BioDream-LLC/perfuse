package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/ftpconn"
	"github.com/biodream-llc/perfuse/mllp"
)

// FTPSender uploads a file per message.
//
// A connection per message rather than a pooled one. FTP sessions are stateful - a current directory, a
// transfer mode, a half-finished rename - and a pooled session that failed part way through leaves the next
// message to inherit whatever state it was in. These servers are also frequently the kind that drop idle
// connections without saying so, which turns a pool into a source of intermittent failures on the first
// message after a quiet period. Reconnecting costs a few hundred milliseconds and buys a session that is
// provably clean.
type FTPSender struct {
	cfg  *config.FTPDestination
	name string
}

// NewFTPSender builds the sender.
func NewFTPSender(d config.Destination) (*FTPSender, error) {
	if d.FTP == nil {
		return nil, fmt.Errorf("destination %q is an ftp destination with no ftp block", d.Name)
	}
	return &FTPSender{cfg: d.FTP, name: d.Name}, nil
}

// Send uploads one message.
func (s *FTPSender) Send(ctx context.Context, msg []byte) error {
	name, err := s.fileName(msg)
	if err != nil {
		return err
	}

	body := msg
	if s.cfg.Framed {
		body = mllp.Frame(msg)
	}

	security := ftpconn.Security(s.cfg.Security)
	if security == "" {
		security = ftpconn.SecurityExplicit
	}

	// The context's deadline is honoured if it is tighter than the configured timeout, so a channel-level
	// timeout is not silently extended by this connector.
	timeout := s.cfg.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}

	client, err := ftpconn.Dial(ftpconn.Config{
		Host:               s.cfg.Host,
		User:               s.cfg.User,
		Password:           s.cfg.Password,
		Security:           security,
		InsecureSkipVerify: s.cfg.InsecureSkipVerify,
		Timeout:            timeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	return client.Store(s.cfg.Dir, name, body, s.cfg.TempSuffix)
}

// Describe names the destination, never the password.
func (s *FTPSender) Describe() string {
	scheme := "ftps"
	if s.cfg.Security == config.FTPSecurityNone {
		scheme = "ftp"
	}
	return fmt.Sprintf("%s://%s/%s", scheme, s.cfg.Host, strings.TrimPrefix(s.cfg.Dir, "/"))
}

// Close releases nothing, because nothing is held between messages.
func (s *FTPSender) Close() error { return nil }

// fileName renders the name for one message.
//
// The same placeholder vocabulary and the same missing-control-id behaviour as the file, SFTP and S3
// destinations: something unique rather than an empty string, because a name collision overwrites a message
// and the receiver never knows it happened.
func (s *FTPSender) fileName(raw []byte) (string, error) {
	tmpl := s.cfg.FileName
	if tmpl == "" {
		tmpl = "${timestamp}-${control_id}.hl7"
	}

	msg, err := hl7.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("naming the file needs the message parsed, and a transformation produced "+
			"something invalid: %w", err)
	}

	controlID := ""
	msgType := ""
	if msh, ok := msg.Segment("MSH", 1); ok {
		controlID = msh.Field(10).String()
		msgType = strings.ReplaceAll(msh.Field(9).String(), "^", "_")
	}
	if controlID == "" {
		controlID = fmt.Sprintf("noid-%d", time.Now().UnixNano())
	}

	now := time.Now().UTC()
	out := tmpl
	out = strings.ReplaceAll(out, "${timestamp}", now.Format("20060102150405"))
	out = strings.ReplaceAll(out, "${date}", now.Format("20060102"))
	out = strings.ReplaceAll(out, "${control_id}", controlID)
	out = strings.ReplaceAll(out, "${message_type}", msgType)
	out = strings.ReplaceAll(out, "${channel}", s.name)

	return sanitiseFileName(out), nil
}
