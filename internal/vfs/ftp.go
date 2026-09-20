package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/biodream-llc/perfuse/internal/ftpconn"
)

// FTP is a directory on an FTP or FTPS server.
//
// # Why anyone still needs this
//
// FTP is thirty years past its recommended replacement, and a great many hospital systems still export to it. Some of
// them are appliances whose firmware will never be updated; some are billing systems whose vendor charges for a change.
// Refusing to speak it does not make those systems go away, it just means the integration engine cannot be used.
//
// FTPS is the default here for that reason: it is the same protocol with TLS, which most surviving servers do support, and
// it is a much easier conversation with a security team than plain FTP.
//
// # One session per operation set, not one per operation
//
// The poller opens a filesystem per poll and closes it after, which for FTP means one control connection for the whole
// poll. That is deliberate: FTP opens a separate data connection for every transfer, and negotiating a new control
// connection as well would triple the handshakes. Holding it across polls would not work, because an idle FTP control
// connection is closed by every firewall between here and the server.
type FTP struct {
	client *ftpconn.Client
	cfg    ftpconn.Config

	// root is prepended to every path, and is how containment is achieved on a server with no concept of it.
	root string
}

// FTPSettings is what an FTP filesystem needs.
type FTPSettings struct {
	Config ftpconn.Config

	// Root is the directory every path is relative to.
	Root string
}

// DialFTP opens a session.
func DialFTP(s FTPSettings) (*FTP, error) {
	client, err := ftpconn.Dial(s.Config)
	if err != nil {
		return nil, err
	}
	return &FTP{client: client, cfg: s.Config, root: strings.Trim(s.Root, "/")}, nil
}

// resolve joins a caller's path onto the root and refuses anything escaping it.
//
// The server has no notion of a root, so containment is entirely this function's responsibility. Without it, a move_to of
// "../../" is a valid FTP path and the channel writes wherever the account can reach.
func (f *FTP) resolve(p string) (string, error) {
	// Refused before joining, for the reason found in the WebDAV backend: path.Join collapses ".." against the root, so a
	// path that climbs out becomes a *different valid path* rather than an error. Here the prefix check does then catch
	// most of them, but not when no root is configured, and not when the climb lands back inside by coincidence. Refusing
	// the component outright is the only version of this that does not depend on the root being non-empty.
	for _, part := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		if part == ".." {
			return "", fmt.Errorf("%s contains \"..\", which a source will not follow. Climbing out of the "+
				"configured directory is refused rather than quietly resolved to something else", p)
		}
	}

	joined := path.Join(f.root, p)
	cleaned := path.Clean("/" + joined)[1:]

	if f.root != "" && cleaned != f.root && !strings.HasPrefix(cleaned, f.root+"/") {
		return "", fmt.Errorf("%s resolves outside %s, and a source only reads and writes inside its own directory",
			p, f.root)
	}
	if cleaned == "" {
		cleaned = "."
	}
	return cleaned, nil
}

// List returns the entries in a directory.
func (f *FTP) List(ctx context.Context, dir string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	full, err := f.resolve(dir)
	if err != nil {
		return nil, err
	}

	entries, err := f.client.List(full)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Name: e.Name, Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir})
	}
	return out, nil
}

// Open reads a file.
//
// Read entirely into memory rather than streamed, because FTP's data connection has to be closed and the transfer
// completion code read before the control connection can be used again. Handing back a live reader would let a caller hold
// the session open indefinitely and leave the client in a state where the next command fails for no visible reason.
func (f *FTP) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	full, err := f.resolve(p)
	if err != nil {
		return nil, err
	}

	body, err := f.client.Retrieve(full, 1<<30)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// Remove deletes a file.
func (f *FTP) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := f.resolve(p)
	if err != nil {
		return err
	}
	return f.client.Delete(full)
}

// Rename moves a file.
func (f *FTP) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := f.resolve(from)
	if err != nil {
		return err
	}
	dst, err := f.resolve(to)
	if err != nil {
		return err
	}
	return f.client.Rename(src, dst)
}

// MkdirAll creates a directory and its parents.
func (f *FTP) MkdirAll(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := f.resolve(dir)
	if err != nil {
		return err
	}
	return f.client.MakeDir(full)
}

// Join joins path elements with a forward slash, which is what FTP uses regardless of the server's operating system.
func (f *FTP) Join(elem ...string) string { return path.Join(elem...) }

// Describe names the server and directory, without the password.
func (f *FTP) Describe() string {
	who := f.cfg.User
	if who == "" {
		who = "anonymous"
	}
	root := f.root
	if root == "" {
		root = "/"
	}
	return fmt.Sprintf("ftp %s@%s:%s", who, f.cfg.Host, root)
}

// Close ends the session.
func (f *FTP) Close() error {
	if f.client == nil {
		return nil
	}
	err := f.client.Close()
	f.client = nil
	return err
}

// A compile-time check that this satisfies the interface, so a change to FS breaks the build here rather than at a call
// site far away.
var _ FS = (*FTP)(nil)
