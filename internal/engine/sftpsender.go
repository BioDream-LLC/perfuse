package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/sftpconn"
	"github.com/biodream-llc/perfuse/mllp"
)

// SFTPSender writes each message to a file on an SFTP server.
//
// The design decision that matters is the temporary name. A file appearing at its
// final name while it is still being written is indistinguishable, to whoever
// collects it, from a finished file - which is the same problem this engine has
// reading files, and it is only polite to not inflict it. So every file is written
// under a suffix and renamed once it is closed, because a rename within a directory
// is atomic on every server worth using.
type SFTPSender struct {
	name string
	cfg  *config.SFTPDestination
	log  *slog.Logger

	mu    sync.Mutex
	conn  *sftpconn.Conn
	stats SFTPStats
}

// SFTPStats reports what the destination has done.
type SFTPStats struct {
	Written     int64     `json:"written"`
	Failed      int64     `json:"failed"`
	Reconnects  int64     `json:"reconnects"`
	BytesGone   int64     `json:"bytesWritten"`
	LastWrite   time.Time `json:"lastWrite,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	Connected   bool      `json:"connected"`
	CurrentFile string    `json:"currentFile,omitempty"`
}

// NewSFTPSender prepares the destination.
//
// The connection is not opened here. An SFTP server being briefly unreachable is
// completely normal, and refusing to start a channel because of it would turn a
// transient network problem into an outage needing a person.
func NewSFTPSender(d config.Destination, log *slog.Logger) (*SFTPSender, error) {
	if d.SFTP == nil {
		return nil, fmt.Errorf("destination %q has type sftp but no sftp block", d.Name)
	}
	if log == nil {
		log = slog.Default()
	}

	s := &SFTPSender{name: d.Name, cfg: d.SFTP, log: log}
	for _, w := range d.SFTP.Warnings() {
		log.Warn("sftp destination", "destination", d.Name, "warning", w)
	}
	return s, nil
}

// Describe names the destination for the interface and the log.
func (s *SFTPSender) Describe() string {
	return fmt.Sprintf("sftp %s@%s:%s", s.cfg.User, s.cfg.Host, s.cfg.Dir)
}

// settings converts the configuration for the connection layer.
func (s *SFTPSender) settings() sftpconn.Settings {
	return sftpconn.Settings{
		Host:                     s.cfg.Host,
		User:                     s.cfg.User,
		Password:                 s.cfg.Password,
		KeyFile:                  s.cfg.KeyFile,
		KeyPassphrase:            s.cfg.KeyPassphrase,
		KnownHostsFile:           s.cfg.KnownHostsFile,
		InsecureSkipHostKeyCheck: s.cfg.InsecureSkipHostKeyCheck,
		Timeout:                  s.cfg.Timeout,
	}
}

// Send writes one message.
func (s *SFTPSender) Send(ctx context.Context, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConnected(); err != nil {
		s.fail(err)
		return err
	}

	name, err := s.fileName(raw)
	if err != nil {
		s.fail(err)
		return err
	}

	final := path.Join(s.cfg.Dir, name)
	body := raw
	if s.cfg.Framed {
		// Framed through the same writer MLLP uses, so a file Perfuse writes is one
		// Perfuse can read back without a second definition of what framing means.
		var framed strings.Builder
		if err := mllp.NewWriter(&framed).WriteMessage(raw); err != nil {
			s.fail(err)
			return fmt.Errorf("framing the message: %w", err)
		}
		body = []byte(framed.String())
	}

	if s.cfg.AppendToFile {
		if err := s.appendTo(final, body); err != nil {
			// Retried once through a fresh connection. A connection idle between messages
			// is routinely closed by a firewall, and the failure then lands on a message
			// rather than on a reconnect.
			if s.reconnect() == nil {
				err = s.appendTo(final, body)
			}
			if err != nil {
				s.fail(err)
				return err
			}
		}
		s.succeed(final, len(body))
		return nil
	}

	if err := s.writeAndRename(final, body); err != nil {
		if s.reconnect() == nil {
			err = s.writeAndRename(final, body)
		}
		if err != nil {
			s.fail(err)
			return err
		}
	}

	s.succeed(final, len(body))
	return nil
}

// writeAndRename writes under a temporary name and renames once closed.
func (s *SFTPSender) writeAndRename(final string, body []byte) error {
	temp := final + s.cfg.TempSuffix
	if s.cfg.TempSuffix == "" {
		temp = final
	}

	f, err := s.conn.Client.Create(temp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", temp, s.explain(err, path.Dir(final)))
	}

	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		// Removed on failure, so a partial file is never left where somebody's poller
		// will find it. A half message that parses is worse than no message.
		_ = s.conn.Client.Remove(temp)
		return fmt.Errorf("writing %s: %w", temp, err)
	}
	if err := f.Close(); err != nil {
		_ = s.conn.Client.Remove(temp)
		return fmt.Errorf("closing %s: %w", temp, err)
	}

	if temp == final {
		return nil
	}

	if err := s.conn.Client.Rename(temp, final); err != nil {
		// Some servers refuse a rename onto an existing name, and the same control ID
		// arriving twice is a real occurrence rather than a fault.
		if s.conn.Client.Remove(final) == nil {
			if err2 := s.conn.Client.Rename(temp, final); err2 == nil {
				return nil
			}
		}
		_ = s.conn.Client.Remove(temp)
		return fmt.Errorf("renaming %s to %s: %w", temp, final, err)
	}
	return nil
}

// appendTo adds a message to an existing file.
func (s *SFTPSender) appendTo(final string, body []byte) error {
	f, err := s.conn.Client.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("opening %s to append: %w", final, s.explain(err, path.Dir(final)))
	}
	defer f.Close()

	// Seeking to the end rather than trusting the append flag, because not every
	// server honours it and the result of getting it wrong is overwriting the start
	// of a file full of messages.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seeking to the end of %s: %w", final, err)
	}
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("appending to %s: %w", final, err)
	}
	return nil
}

// fileName renders the name for one message.
func (s *SFTPSender) fileName(raw []byte) (string, error) {
	if s.cfg.AppendToFile {
		return time.Now().UTC().Format("20060102") + ".hl7", nil
	}

	tmpl := s.cfg.FileName
	if tmpl == "" {
		tmpl = "${timestamp}-${control_id}.hl7"
	}

	msg, err := hl7.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("naming the file needs the message parsed, and a "+
			"transformation produced something invalid: %w", err)
	}

	controlID := ""
	msgType := ""
	if msh, ok := msg.Segment("MSH", 1); ok {
		controlID = msh.Field(10).String()
		msgType = strings.ReplaceAll(msh.Field(9).String(), "^", "_")
	}
	if controlID == "" {
		// A name collision would overwrite a message, so a missing control ID gets
		// something unique rather than an empty string.
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

// sanitiseFileName removes anything that would escape the directory or confuse a
// shell on the other side.
//
// A control ID is data from the sending system, and it ends up in a path. Without
// this a message carrying "../" in MSH-10 would write outside the configured
// directory, which is a path traversal introduced by trusting a field.
func sanitiseFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "..", "_")

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := strings.Trim(b.String(), ".")
	if out == "" {
		return fmt.Sprintf("message-%d.hl7", time.Now().UnixNano())
	}
	return out
}

// ensureConnected opens the connection if it is not already open.
func (s *SFTPSender) ensureConnected() error {
	if s.conn != nil {
		return nil
	}

	c, err := sftpconn.Dial(s.settings())
	if err != nil {
		return err
	}
	s.conn = c
	s.stats.Connected = true

	// Created rather than required to exist. A directory per day or per channel is a
	// common arrangement, and failing the first message of the month would be a
	// pointless outage.
	if err := s.conn.Client.MkdirAll(s.cfg.Dir); err != nil {
		s.log.Warn("could not create the remote directory, which is only a problem if "+
			"it does not already exist",
			"destination", s.name, "dir", s.cfg.Dir, "error", err)
	}
	return nil
}

// reconnect drops the connection and opens a new one.
func (s *SFTPSender) reconnect() error {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.stats.Connected = false
	s.stats.Reconnects++

	if err := s.ensureConnected(); err != nil {
		return err
	}
	s.log.Info("reconnected to the SFTP server",
		"destination", s.name, "host", s.cfg.Host)
	return nil
}

// explain adds the likeliest cause to a filesystem error.
func (s *SFTPSender) explain(err error, dir string) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "permission denied"):
		return fmt.Errorf("%w. The account %q can log in but cannot write to %s",
			err, s.cfg.User, dir)
	case strings.Contains(msg, "no such file"):
		return fmt.Errorf("%w. %s does not exist and could not be created, which "+
			"usually means the account is confined to a different root than the path "+
			"suggests", err, dir)
	}
	return err
}

func (s *SFTPSender) succeed(name string, n int) {
	s.stats.Written++
	s.stats.BytesGone += int64(n)
	s.stats.LastWrite = time.Now()
	s.stats.CurrentFile = name
}

func (s *SFTPSender) fail(err error) {
	s.stats.Failed++
	if err != nil {
		s.stats.LastError = err.Error()
	}
}

// Stats returns a copy of the counters.
func (s *SFTPSender) Stats() SFTPStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close ends the connection.
func (s *SFTPSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	s.stats.Connected = false
	return err
}
