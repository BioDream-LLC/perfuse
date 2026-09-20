package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// SFTPSource collects messages from a directory on an SFTP server.
//
// Extremely common and rarely spoken about. A laboratory or a radiology system
// writes a file every few minutes, an SFTP server holds it, and something has to
// come and get it. Half the feeds described as "we send you HL7" are this.
//
// The part worth care is not the transfer. It is that a file being written to and a
// file finished being written to look identical over SFTP, so a poll that is even
// slightly too eager collects half a message. That produces a truncated but often
// parseable HL7 message, which is the worst outcome available: it is accepted,
// acknowledged, stored and delivered, and the missing half is never mentioned again.
type SFTPSource struct {
	// Host is the server, with an optional port. Defaults to 22.
	Host string `yaml:"host"`

	// User is the login name.
	User string `yaml:"user"`

	// Password authenticates with a password. Prefer KeyFile.
	Password string `yaml:"password,omitempty"`

	// KeyFile is a private key. The better choice, and the one hospital security
	// teams ask for.
	KeyFile string `yaml:"key_file,omitempty"`

	// KeyPassphrase decrypts KeyFile if it is encrypted.
	KeyPassphrase string `yaml:"key_passphrase,omitempty"`

	// KnownHostsFile verifies the server's identity.
	//
	// Required unless InsecureSkipHostKeyCheck is set. Without it there is nothing
	// to distinguish the real server from anything that answers on that address,
	// and the credentials are handed over before anybody notices.
	KnownHostsFile string `yaml:"known_hosts_file,omitempty"`

	// InsecureSkipHostKeyCheck accepts any host key.
	//
	// Exists because refusing outright sends people to a shell script with
	// StrictHostKeyChecking=no, which is worse in every way including auditability.
	// It warns loudly and it is never the default.
	InsecureSkipHostKeyCheck bool `yaml:"insecure_skip_host_key_check,omitempty"`

	// Dir is the remote directory to poll.
	Dir string `yaml:"dir"`

	// Pattern selects files by glob. Defaults to everything.
	Pattern string `yaml:"pattern,omitempty"`

	// PollInterval defaults to 30s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`

	// AfterRead decides what happens to a file once its messages are accepted:
	// "move" (the default), "delete", or "leave".
	AfterRead string `yaml:"after_read,omitempty"`

	// MoveTo is the remote directory files are moved into after reading. Required
	// when AfterRead is "move".
	MoveTo string `yaml:"move_to,omitempty"`

	// ErrorDir receives files that could not be processed, so a bad file stops
	// being retried without being lost.
	ErrorDir string `yaml:"error_dir,omitempty"`

	// StableFor is how long a file's size and modification time must be unchanged
	// before it is read. Defaults to 5s.
	//
	// This is the setting that stops half a message being collected. Zero means
	// reading whatever is there, which works right up until a file is large enough
	// or a network slow enough that writing takes longer than a poll.
	StableFor time.Duration `yaml:"stable_for,omitempty"`

	// Framed says the file contains MLLP-framed messages. When false, the file is
	// split on MSH boundaries.
	Framed bool `yaml:"framed,omitempty"`

	// MaxFileSize refuses a file larger than this many bytes. Defaults to 64MB.
	MaxFileSize int64 `yaml:"max_file_size,omitempty"`

	// Timeout bounds a single connection. Defaults to 60s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// SFTPDestination writes each message to a file on an SFTP server.
type SFTPDestination struct {
	// Host is the server, with an optional port. Defaults to 22.
	Host string `yaml:"host"`

	// User is the login name.
	User string `yaml:"user"`

	// Password authenticates with a password. Prefer KeyFile.
	Password string `yaml:"password,omitempty"`

	// KeyFile is a private key.
	KeyFile string `yaml:"key_file,omitempty"`

	// KeyPassphrase decrypts KeyFile if it is encrypted.
	KeyPassphrase string `yaml:"key_passphrase,omitempty"`

	// KnownHostsFile verifies the server's identity.
	KnownHostsFile string `yaml:"known_hosts_file,omitempty"`

	// InsecureSkipHostKeyCheck accepts any host key.
	InsecureSkipHostKeyCheck bool `yaml:"insecure_skip_host_key_check,omitempty"`

	// Dir is the remote directory to write into.
	Dir string `yaml:"dir"`

	// FileName templates the name. Defaults to a timestamp plus the control ID.
	FileName string `yaml:"file_name,omitempty"`

	// TempSuffix is appended while a file is being written, and removed by a rename
	// once it is complete. Defaults to ".part".
	//
	// The same courtesy this connector needs on the way in. Whoever collects these
	// files has the identical problem, and a rename within a directory is atomic on
	// every server worth using.
	TempSuffix string `yaml:"temp_suffix,omitempty"`

	// Framed wraps each message in MLLP framing, so a file holding several messages
	// can be split again unambiguously.
	Framed bool `yaml:"framed,omitempty"`

	// AppendToFile writes every message into one file per day rather than a file
	// per message.
	AppendToFile bool `yaml:"append,omitempty"`

	// Timeout bounds a single connection. Defaults to 60s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// afterReadModes are the recognised values for SFTPSource.AfterRead.
var afterReadModes = map[string]string{
	"move":   "move the file to move_to",
	"delete": "delete the file",
	"leave":  "leave the file alone and remember it",
}

func (s *SFTPSource) applyDefaults() {
	if s.PollInterval == 0 {
		s.PollInterval = 30 * time.Second
	}
	if s.AfterRead == "" {
		s.AfterRead = "move"
	}
	if s.StableFor == 0 {
		s.StableFor = 5 * time.Second
	}
	if s.MaxFileSize == 0 {
		s.MaxFileSize = 64 << 20
	}
	if s.Timeout == 0 {
		s.Timeout = 60 * time.Second
	}
	if s.Pattern == "" {
		s.Pattern = "*"
	}
}

// Validate checks an SFTP source.
func (s *SFTPSource) Validate() error {
	s.applyDefaults()

	if strings.TrimSpace(s.Host) == "" {
		return errors.New("sftp.host is required")
	}
	if strings.TrimSpace(s.User) == "" {
		return errors.New("sftp.user is required")
	}
	if s.Password == "" && s.KeyFile == "" {
		return errors.New("sftp needs either password or key_file: there is no " +
			"anonymous SFTP")
	}
	if strings.TrimSpace(s.Dir) == "" {
		return errors.New("sftp.dir is required, naming the remote directory to poll")
	}

	if err := validateHostKeyChecking(s.KnownHostsFile, s.InsecureSkipHostKeyCheck); err != nil {
		return err
	}

	if _, ok := afterReadModes[s.AfterRead]; !ok {
		return fmt.Errorf("sftp.after_read is %q; use move, delete or leave",
			s.AfterRead)
	}
	if s.AfterRead == "move" && strings.TrimSpace(s.MoveTo) == "" {
		return errors.New("sftp.after_read is move, so move_to is required, naming the " +
			"remote directory to move a file into once its messages are accepted")
	}
	if s.AfterRead != "move" && s.MoveTo != "" {
		return fmt.Errorf("sftp.move_to is set but after_read is %q, so nothing would "+
			"be moved there", s.AfterRead)
	}
	if s.MoveTo != "" && path.Clean(s.MoveTo) == path.Clean(s.Dir) {
		// Moving a file to where it already is leaves it to be found again on the next
		// poll, forever, and every message in it is resent every time.
		return errors.New("sftp.move_to is the same directory as sftp.dir, so a file " +
			"would be found again on the next poll and every message in it resent")
	}
	if s.ErrorDir != "" && path.Clean(s.ErrorDir) == path.Clean(s.Dir) {
		return errors.New("sftp.error_dir is the same directory as sftp.dir, so a file " +
			"that cannot be processed would be retried forever")
	}

	if s.StableFor < 0 {
		return errors.New("sftp.stable_for cannot be negative")
	}
	if s.PollInterval < time.Second {
		return fmt.Errorf("sftp.poll_interval is %s: under a second this is a busy "+
			"loop against the server rather than a poll", s.PollInterval)
	}
	if s.MaxFileSize < 1024 {
		return fmt.Errorf("sftp.max_file_size is %d bytes, which is smaller than many "+
			"single HL7 messages", s.MaxFileSize)
	}
	if s.Timeout <= 0 {
		return errors.New("sftp.timeout must be positive")
	}
	return nil
}

// Warnings reports things worth saying at every start.
func (s *SFTPSource) Warnings() []string {
	var out []string

	if s.InsecureSkipHostKeyCheck {
		// The loudest warning this package produces, and it is repeated at every start
		// on purpose. The connection is encrypted and unauthenticated, which is a
		// different and less obvious problem than not being encrypted at all.
		out = append(out, "insecure_skip_host_key_check is set, so Perfuse will accept "+
			"any server answering on that address and hand it these credentials. The "+
			"transfer is encrypted but the server is not verified. Record the real host "+
			"key and set known_hosts_file")
	}
	if s.Password != "" && s.KeyFile == "" && !strings.Contains(s.Password, "${") {
		out = append(out, "the SFTP password is literal text in the channel file. "+
			"Channels are meant to live in version control, so prefer ${ENV_VAR}, or "+
			"better a key_file")
	}
	if s.StableFor == 0 {
		out = append(out, "stable_for is zero, so a file will be read the moment it is "+
			"seen. A file still being written looks exactly like a finished one over "+
			"SFTP, and half an HL7 message often parses")
	}
	if s.AfterRead == "leave" {
		out = append(out, "after_read is leave, so files stay where they are and Perfuse "+
			"remembers which it has read. That record does not survive rebuilding its "+
			"database, and every remaining file would then be read again")
	}
	if s.ErrorDir == "" {
		out = append(out, "no error_dir is set, so a file that cannot be processed stays "+
			"in place and is retried on every poll. Set error_dir so one bad file does "+
			"not become a permanent warning nobody reads")
	}
	return out
}

// Validate checks an SFTP destination.
func (s *SFTPDestination) Validate() error {
	if s.Timeout == 0 {
		s.Timeout = 60 * time.Second
	}
	if s.TempSuffix == "" {
		s.TempSuffix = ".part"
	}

	if strings.TrimSpace(s.Host) == "" {
		return errors.New("sftp.host is required")
	}
	if strings.TrimSpace(s.User) == "" {
		return errors.New("sftp.user is required")
	}
	if s.Password == "" && s.KeyFile == "" {
		return errors.New("sftp needs either password or key_file")
	}
	if strings.TrimSpace(s.Dir) == "" {
		return errors.New("sftp.dir is required, naming the remote directory to write into")
	}

	if err := validateHostKeyChecking(s.KnownHostsFile, s.InsecureSkipHostKeyCheck); err != nil {
		return err
	}

	if s.TempSuffix != "" && !strings.HasPrefix(s.TempSuffix, ".") {
		// A suffix that is not an extension often means the receiving side's own glob
		// picks the partial file up, which is the problem the suffix exists to solve.
		return fmt.Errorf("sftp.temp_suffix is %q: it should start with a dot, so "+
			"whoever collects these files can exclude it with a pattern", s.TempSuffix)
	}
	if s.AppendToFile && s.TempSuffix != "" && s.TempSuffix != ".part" {
		// Appending cannot use a temporary name, since the file has to stay put.
		return errors.New("sftp.append writes into one file that stays in place, so " +
			"temp_suffix does not apply to it")
	}
	if s.AppendToFile && !s.Framed {
		// Several messages in one file with nothing between them cannot be split again
		// reliably: MSH can appear inside a free-text field.
		return errors.New("sftp.append puts several messages in one file, so framed " +
			"must be true as well, otherwise whoever reads the file cannot tell where " +
			"one message ends and the next begins")
	}
	if s.Timeout <= 0 {
		return errors.New("sftp.timeout must be positive")
	}
	return nil
}

// Warnings reports things worth saying about an SFTP destination.
func (s *SFTPDestination) Warnings() []string {
	var out []string
	if s.InsecureSkipHostKeyCheck {
		out = append(out, "insecure_skip_host_key_check is set, so Perfuse will send "+
			"these messages to any server answering on that address without verifying "+
			"it is the right one. Record the real host key and set known_hosts_file")
	}
	if s.Password != "" && s.KeyFile == "" && !strings.Contains(s.Password, "${") {
		out = append(out, "the SFTP password is literal text in the channel file. "+
			"Prefer ${ENV_VAR}, or better a key_file")
	}
	if s.TempSuffix == "" && !s.AppendToFile {
		out = append(out, "no temp_suffix is set, so a file appears at its final name "+
			"while it is still being written. Whoever collects these files has the same "+
			"problem Perfuse has reading them, and may take half a message")
	}
	return out
}

// validateHostKeyChecking is shared, because getting this wrong is the same mistake
// in both directions.
func validateHostKeyChecking(knownHosts string, skip bool) error {
	if skip && knownHosts != "" {
		// One of the two is a mistake, and which one changes the security of the
		// connection completely.
		return errors.New("sftp has both known_hosts_file and " +
			"insecure_skip_host_key_check: the file would never be consulted, so one of " +
			"the two is a mistake")
	}
	if !skip && strings.TrimSpace(knownHosts) == "" {
		// Refused rather than defaulted to trusting anything. This is the whole
		// security of SFTP: without it the credentials go to whatever answers.
		return errors.New("sftp needs known_hosts_file, so the server can be verified. " +
			"Without it there is nothing to distinguish the real server from anything " +
			"answering on that address, and the credentials are handed over before " +
			"anyone notices. Get the key with " +
			"\"ssh-keyscan -H the-host >> known_hosts\", check it against what the " +
			"other side says it should be, and point known_hosts_file at that file. If " +
			"you genuinely cannot, set insecure_skip_host_key_check and read the warning")
	}
	return nil
}
