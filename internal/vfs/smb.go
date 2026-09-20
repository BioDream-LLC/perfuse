package vfs

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"
)

// SMB is a directory on a Windows file share.
//
// # Why this speaks SMB rather than expecting a mount
//
// On Linux and macOS a share can be mounted by the operating system, after which it is an ordinary directory and the file
// source already handles it. That is a perfectly good arrangement, and where it already exists it is the simpler one.
//
// It is not good enough as the only option, for two reasons. Mounting needs a shell on the server, an entry in fstab or
// autofs, and credentials in a file somebody has to place - which is exactly the kind of work this project treats as a
// product defect, because everything is supposed to be configurable from the web interface. And a mount that drops
// reports as an empty directory rather than an error, so a channel goes quiet and reports nothing wrong.
//
// Speaking SMB directly means the share is configured where every other connector is configured, and a share that cannot
// be reached fails loudly.
//
// # SMB1 is not supported and will not be
//
// This is SMB2 and SMB3 only. SMB1 is how several hospital ransomware outbreaks spread, and a library that speaks it is a
// liability whatever the configuration says. A device that only offers SMB1 needs a mount arranged deliberately by
// somebody who has weighed that up, not a checkbox here.
type SMB struct {
	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share

	host      string
	shareName string
	root      string
}

// SMBSettings is what an SMB filesystem needs.
type SMBSettings struct {
	// Host is the server, with an optional port. Port 445 is assumed.
	Host string

	// Share is the share name, without slashes: the "data" in \\server\data.
	Share string

	User     string
	Password string

	// Domain is the Windows domain or workgroup. Empty is usually correct for a local account.
	Domain string

	// Root is the directory within the share that every path is relative to.
	Root string

	Timeout time.Duration
}

// DialSMB connects and opens the share.
func DialSMB(ctx context.Context, s SMBSettings) (*SMB, error) {
	if strings.TrimSpace(s.Host) == "" {
		return nil, fmt.Errorf("an smb source needs a host")
	}
	if strings.TrimSpace(s.Share) == "" {
		return nil, fmt.Errorf("an smb source needs a share name: the \"data\" in \\\\server\\data")
	}
	// Refused rather than trimmed. Somebody who typed a path into the share field means a different thing by it, and
	// silently using only the first component would connect to a share they did not name.
	if strings.ContainsAny(s.Share, `/\`) {
		return nil, fmt.Errorf("the share name %q contains a slash. It should be just the share name, for example "+
			"\"data\" rather than \"\\\\server\\data\\folder\"; put the folder in dir instead", s.Share)
	}

	addr := s.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "445")
	}

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}

	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     s.User,
			Password: s.Password,
			Domain:   s.Domain,
		},
	}

	// DialConn rather than Dial, so the TCP connection is ours and carries the dial timeout above. Dial would make its
	// own with no deadline, and a share on a host that is powered off would hang the poll until the kernel gave up.
	session, err := dialer.DialConn(ctx, conn, addr)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("signing in to %s as %s: %w. A share that requires SMB1 will fail here, and Perfuse "+
			"does not speak SMB1 deliberately", addr, describeUser(s), err)
	}

	share, err := session.Mount(s.Share)
	if err != nil {
		_ = session.Logoff()
		_ = conn.Close()
		return nil, fmt.Errorf("opening the share %q on %s: %w. The account signed in successfully, so this is the "+
			"share name or its permissions rather than the credentials", s.Share, s.Host, err)
	}

	return &SMB{
		conn: conn, session: session, share: share,
		host: s.Host, shareName: s.Share,
		root: strings.Trim(strings.ReplaceAll(s.Root, "\\", "/"), "/"),
	}, nil
}

func describeUser(s SMBSettings) string {
	if s.Domain != "" {
		return s.Domain + `\` + s.User
	}
	if s.User == "" {
		return "a guest"
	}
	return s.User
}

// resolve joins a caller's path onto the root and refuses anything escaping it.
//
// Backslashes are normalised to forward slashes first. The library takes forward slashes, but somebody configuring a
// Windows share will type backslashes, and a path containing both must still be checked as one thing.
func (s *SMB) resolve(p string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")

	// Refused before joining, for the reason found in the WebDAV backend: joining collapses ".." and turns a path that
	// climbs out into a different valid path rather than an error.
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("%s contains \"..\", which a source will not follow. Climbing out of the "+
				"configured directory is refused rather than quietly resolved to something else", p)
		}
	}

	joined := path.Join(s.root, p)
	cleaned := strings.TrimPrefix(path.Clean("/"+joined), "/")

	if s.root != "" && cleaned != s.root && !strings.HasPrefix(cleaned, s.root+"/") {
		return "", fmt.Errorf("%s resolves outside %s, and a source only reads and writes inside its own directory",
			p, s.root)
	}
	if cleaned == "" {
		cleaned = "."
	}
	return cleaned, nil
}

// List returns the entries in a directory.
func (s *SMB) List(ctx context.Context, dir string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	full, err := s.resolve(dir)
	if err != nil {
		return nil, err
	}

	items, err := s.share.ReadDir(full)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(items))
	for _, it := range items {
		if it.Name() == "." || it.Name() == ".." {
			continue
		}
		out = append(out, Entry{
			Name:    it.Name(),
			Size:    it.Size(),
			ModTime: it.ModTime(),
			IsDir:   it.IsDir(),
		})
	}
	return out, nil
}

// Open reads a file.
func (s *SMB) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	return s.share.Open(full)
}

// Remove deletes a file.
func (s *SMB) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := s.resolve(p)
	if err != nil {
		return err
	}
	return s.share.Remove(full)
}

// Rename moves a file.
func (s *SMB) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := s.resolve(from)
	if err != nil {
		return err
	}
	dst, err := s.resolve(to)
	if err != nil {
		return err
	}
	return s.share.Rename(src, dst)
}

// MkdirAll creates a directory and its parents.
func (s *SMB) MkdirAll(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := s.resolve(dir)
	if err != nil {
		return err
	}
	// 0o777 is not a permission here. SMB carries Windows ACLs, not Unix modes, and the library ignores the value; the
	// share's own permissions decide. Written as the permissive value so nobody reads it as a security setting that is
	// being honoured.
	return s.share.MkdirAll(full, 0o777)
}

// Join joins path elements with a forward slash.
//
// Forward rather than backslash because that is what the library takes, and it accepts them on every SMB server. A
// backslash here would be more faithful to how the share is written down and less likely to work.
func (s *SMB) Join(elem ...string) string { return path.Join(elem...) }

// Describe names the share without the password.
func (s *SMB) Describe() string {
	if s.root != "" {
		return fmt.Sprintf(`\\%s\%s\%s`, s.host, s.shareName, strings.ReplaceAll(s.root, "/", `\`))
	}
	return fmt.Sprintf(`\\%s\%s`, s.host, s.shareName)
}

// Close unmounts the share and closes the connection.
//
// All three steps attempted even if an earlier one fails. Returning at the first error would leave a TCP connection open
// for every poll, and a poller running every thirty seconds exhausts file descriptors within a day.
func (s *SMB) Close() error {
	var first error

	if s.share != nil {
		if err := s.share.Umount(); err != nil && first == nil {
			first = err
		}
		s.share = nil
	}
	if s.session != nil {
		if err := s.session.Logoff(); err != nil && first == nil {
			first = err
		}
		s.session = nil
	}
	if s.conn != nil {
		if err := s.conn.Close(); err != nil && first == nil {
			first = err
		}
		s.conn = nil
	}
	return first
}

var _ FS = (*SMB)(nil)
