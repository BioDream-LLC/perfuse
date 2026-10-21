package umlink

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/sftpconn"
)

// fileStore is a batch link's two folders.
type fileStore interface {
	put(ctx context.Context, name string, body []byte) error
	list(ctx context.Context) ([]string, error)
	get(ctx context.Context, name string) ([]byte, error)
	remove(ctx context.Context, name string) error
}

// sftpStore opens one SFTP session per operation. A batch link sends and polls a few times a minute at most, and a session held
// open between them is one a firewall closes silently, so the next send hangs until it times out.
type sftpStore struct {
	cfg     SFTP
	timeout time.Duration
}

func (s *sftpStore) dial() (*sftpconn.Conn, error) {
	return sftpconn.Dial(sftpconn.Settings{Host: s.cfg.Address, User: s.cfg.User, Password: s.cfg.Password, KeyFile: s.cfg.KeyFile,
		KnownHostsFile: s.cfg.KnownHosts, Timeout: s.timeout})
}

// put writes under a temporary name and renames, so the UM system never picks up half a file.
func (s *sftpStore) put(_ context.Context, name string, body []byte) error {
	c, err := s.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	final := path.Join(s.cfg.Outbound, name)
	tmp := path.Join(s.cfg.Outbound, "."+name+".part")
	f, err := c.Client.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s on the SFTP server: %w", tmp, err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = c.Client.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := c.Client.PosixRename(tmp, final); err != nil {
		if err := c.Client.Rename(tmp, final); err != nil {
			_ = c.Client.Remove(tmp)
			return fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
		}
	}
	return nil
}

func (s *sftpStore) list(_ context.Context) ([]string, error) {
	c, err := s.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	entries, err := c.Client.ReadDir(s.cfg.Inbound)
	if err != nil {
		return nil, fmt.Errorf("listing %s on the SFTP server: %w", s.cfg.Inbound, err)
	}
	var names []string
	for _, e := range entries {
		// Hidden names are files still being written, by the convention put follows too.
		if e.Mode().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *sftpStore) get(_ context.Context, name string) ([]byte, error) {
	c, err := s.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	f, err := c.Client.Open(path.Join(s.cfg.Inbound, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

func (s *sftpStore) remove(_ context.Context, name string) error {
	c, err := s.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Client.Remove(path.Join(s.cfg.Inbound, name))
}
