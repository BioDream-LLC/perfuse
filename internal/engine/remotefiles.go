package engine

import (
	"context"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/ftpconn"
	"github.com/biodream-llc/perfuse/internal/vfs"
)

// The three remote file sources. Each one is a connection function and a log line, because everything that decides
// whether messages survive lives in the shared poller: the settle rule, the all-or-nothing disposal, the ordering, the
// batch limit. That is the whole point of the abstraction and it is worth noticing how little is left here.
//
// A connection per poll for all three. Polls are half a minute apart by default and a firewall will close an idle
// connection somewhere in between, so a held connection means the failure lands on a file rather than on a reconnect.

// startFTPSource begins polling an FTP or FTPS directory.
func (c *Channel) startFTPSource() error {
	cfg := c.cfg.Source.FTP
	if cfg == nil {
		return fmt.Errorf("channel %q has an ftp source but no ftp block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("ftp source", "channel", c.cfg.Name, "warning", w)
	}

	settings := vfs.FTPSettings{
		Config: ftpconn.Config{
			Host:               cfg.Host,
			User:               cfg.User,
			Password:           cfg.Password,
			Security:           cfg.Security,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			Timeout:            cfg.Timeout,
		},
		Root: cfg.Root,
	}

	// Verified now, because a wrong password or an unreachable host is a configuration error rather than a transient one
	// and should be reported while somebody is looking at the channel rather than half an hour later in a log.
	probe, err := vfs.DialFTP(settings)
	if err != nil {
		return fmt.Errorf("channel %q cannot reach the FTP server: %w", c.cfg.Name, err)
	}
	_ = probe.Close()

	c.files = newFilePoller(c, "ftp", &cfg.FilePoll, func(context.Context) (vfs.FS, error) {
		return vfs.DialFTP(settings)
	})

	c.log.Info("polling an FTP directory",
		"channel", c.cfg.Name, "host", cfg.Host, "security", string(cfg.Security),
		"root", cfg.Root, "dir", cfg.Dir, "every", cfg.PollInterval, "settles_for", cfg.StableFor)

	c.files.start()
	return nil
}

// startSMBSource begins polling a Windows file share.
func (c *Channel) startSMBSource() error {
	cfg := c.cfg.Source.SMB
	if cfg == nil {
		return fmt.Errorf("channel %q has an smb source but no smb block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("smb source", "channel", c.cfg.Name, "warning", w)
	}

	settings := vfs.SMBSettings{
		Host:     cfg.Host,
		Share:    cfg.Share,
		User:     cfg.User,
		Password: cfg.Password,
		Domain:   cfg.Domain,
		Root:     cfg.Root,
		Timeout:  cfg.Timeout,
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	probe, err := vfs.DialSMB(probeCtx, settings)
	if err != nil {
		return fmt.Errorf("channel %q cannot reach the share: %w", c.cfg.Name, err)
	}
	_ = probe.Close()

	c.files = newFilePoller(c, "smb", &cfg.FilePoll, func(ctx context.Context) (vfs.FS, error) {
		return vfs.DialSMB(ctx, settings)
	})

	c.log.Info("polling a Windows share",
		"channel", c.cfg.Name, "host", cfg.Host, "share", cfg.Share,
		"root", cfg.Root, "dir", cfg.Dir, "every", cfg.PollInterval, "settles_for", cfg.StableFor)

	c.files.start()
	return nil
}

// startWebDAVSource begins polling a WebDAV collection.
func (c *Channel) startWebDAVSource() error {
	cfg := c.cfg.Source.WebDAV
	if cfg == nil {
		return fmt.Errorf("channel %q has a webdav source but no webdav block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("webdav source", "channel", c.cfg.Name, "warning", w)
	}

	settings := vfs.WebDAVSettings{
		URL:                cfg.URL,
		User:               cfg.User,
		Password:           cfg.Password,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		Timeout:            cfg.Timeout,
	}

	// The URL is checked here, but no request is made. Unlike the other two, constructing a WebDAV client involves no
	// network, and issuing a PROPFIND purely to validate would make starting a channel a write-adjacent operation on
	// somebody else's server. The first poll reports a bad URL or bad credentials clearly enough.
	probe, err := vfs.NewWebDAV(settings)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	_ = probe.Close()

	c.files = newFilePoller(c, "webdav", &cfg.FilePoll, func(context.Context) (vfs.FS, error) {
		return vfs.NewWebDAV(settings)
	})

	c.log.Info("polling a WebDAV collection",
		"channel", c.cfg.Name, "url", cfg.URL, "dir", cfg.Dir,
		"every", cfg.PollInterval, "settles_for", cfg.StableFor)

	c.files.start()
	return nil
}
