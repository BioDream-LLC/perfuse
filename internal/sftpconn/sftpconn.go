// Package sftpconn opens SFTP connections.
//
// Split out from the connectors because the interesting part is authentication and
// host key verification, and both are identical whether a channel is collecting
// files or writing them. Two copies would drift, and the half that drifted would be
// the half that stopped verifying.
package sftpconn

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Settings is what both connectors need in order to connect.
type Settings struct {
	Host                     string
	User                     string
	Password                 string
	KeyFile                  string
	KeyPassphrase            string
	KnownHostsFile           string
	InsecureSkipHostKeyCheck bool
	Timeout                  time.Duration
}

// Conn is an open SFTP session.
type Conn struct {
	Client *sftp.Client
	ssh    *ssh.Client
}

// Dial opens a connection.
func Dial(s Settings) (*Conn, error) {
	if s.Timeout <= 0 {
		s.Timeout = 60 * time.Second
	}

	auth, err := authMethods(s)
	if err != nil {
		return nil, err
	}

	hostKey, err := hostKeyCallback(s)
	if err != nil {
		return nil, err
	}

	addr := s.Host
	if !strings.Contains(addr, ":") {
		addr += ":22"
	}

	cfg := &ssh.ClientConfig{
		User:            s.User,
		Auth:            auth,
		HostKeyCallback: hostKey,
		Timeout:         s.Timeout,
	}

	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, describeDialError(addr, s, err)
	}

	sc, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		// A working SSH login with no SFTP subsystem is a real and confusing
		// configuration, so it gets its own explanation.
		return nil, fmt.Errorf("logged in to %s but could not start an SFTP session, "+
			"which usually means the server has the SFTP subsystem disabled or the "+
			"account is restricted to a shell: %w", addr, err)
	}

	return &Conn{Client: sc, ssh: client}, nil
}

// Close ends the session.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	var first error
	if c.Client != nil {
		first = c.Client.Close()
	}
	if c.ssh != nil {
		if err := c.ssh.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// authMethods builds the authentication list.
func authMethods(s Settings) ([]ssh.AuthMethod, error) {
	var out []ssh.AuthMethod

	if s.KeyFile != "" {
		key, err := os.ReadFile(s.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("reading the SFTP key file: %w", err)
		}

		var signer ssh.Signer
		if s.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(s.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			// The commonest cause by a distance, and the error from the ssh package does
			// not say it.
			if strings.Contains(err.Error(), "passphrase") ||
				strings.Contains(err.Error(), "decrypt") {
				return nil, fmt.Errorf("the SFTP key at %s is encrypted and no "+
					"key_passphrase was given: %w", s.KeyFile, err)
			}
			return nil, fmt.Errorf("the SFTP key at %s could not be parsed. OpenSSH and "+
				"PEM formats are both accepted; a PuTTY .ppk is not and has to be "+
				"converted: %w", s.KeyFile, err)
		}
		out = append(out, ssh.PublicKeys(signer))
	}

	if s.Password != "" {
		out = append(out, ssh.Password(s.Password))
		// Offered as well, because some servers present password authentication as
		// keyboard-interactive and would otherwise refuse a correct password.
		out = append(out, ssh.KeyboardInteractive(
			func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = s.Password
				}
				return answers, nil
			}))
	}

	if len(out) == 0 {
		return nil, errors.New("no SFTP credentials: set password or key_file")
	}
	return out, nil
}

// hostKeyCallback builds the host key verifier.
//
// This function is the security of the whole connector. Getting it wrong is
// invisible: everything works, the transfer is encrypted, and there is nothing in
// any log to say the server was never checked.
func hostKeyCallback(s Settings) (ssh.HostKeyCallback, error) {
	if s.InsecureSkipHostKeyCheck {
		// Deliberately reachable, because refusing outright sends people to a shell
		// script with StrictHostKeyChecking=no, which is worse in every way including
		// auditability. The configuration layer warns at every start.
		return ssh.InsecureIgnoreHostKey(), nil
	}

	if strings.TrimSpace(s.KnownHostsFile) == "" {
		return nil, errors.New("no known_hosts_file and host key checking is not " +
			"disabled, so there is no way to verify the server")
	}

	cb, err := knownhosts.New(s.KnownHostsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("the known_hosts file %s does not exist. Create it "+
				"with \"ssh-keyscan -H %s >> %s\" and check the key against what the "+
				"other side says it should be before trusting it",
				s.KnownHostsFile, hostOnly(s.Host), s.KnownHostsFile)
		}
		return nil, fmt.Errorf("reading %s: %w", s.KnownHostsFile, err)
	}

	// Wrapped so a mismatch explains itself. The library's error is accurate and
	// says nothing about what to do, and this is the one error where the wrong
	// reaction - deleting the entry and carrying on - is both the obvious one and
	// exactly what an attacker needs.
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
			return fmt.Errorf("the host key for %s does not match %s. Either the server "+
				"was rebuilt or reinstalled, or something is impersonating it. Do not "+
				"delete the entry until somebody at the other end confirms the change: "+
				"presented %s, expected %s",
				hostname, s.KnownHostsFile,
				ssh.FingerprintSHA256(key), fingerprintsOf(keyErr.Want))
		}

		return fmt.Errorf("%s is not in %s, so it cannot be verified. Add it with "+
			"\"ssh-keyscan -H %s >> %s\" once you have checked the key is right: %w",
			hostname, s.KnownHostsFile, hostOnly(s.Host), s.KnownHostsFile, err)
	}, nil
}

// describeDialError turns a connection failure into something actionable.
func describeDialError(addr string, s Settings, err error) error {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "unable to authenticate"):
		how := "a password"
		if s.KeyFile != "" {
			how = "the key at " + s.KeyFile
			if s.Password != "" {
				how += " and a password"
			}
		}
		return fmt.Errorf("%s refused the login for user %q using %s. If the key is "+
			"right, check the account is not restricted and that the public half is in "+
			"the server's authorized_keys: %w", addr, s.User, how, err)

	case strings.Contains(msg, "host key"), strings.Contains(msg, "knownhosts"):
		// Passed through: hostKeyCallback has already written a better message than
		// anything that could be added here.
		return err

	case strings.Contains(msg, "i/o timeout"):
		return fmt.Errorf("%s did not answer within %s, which usually means a firewall "+
			"is dropping the connection rather than refusing it: %w", addr, s.Timeout, err)

	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%s refused the connection, so nothing is listening on that "+
			"port. SFTP is normally 22, and is not the same service as FTP on 21: %w",
			addr, err)
	}

	return fmt.Errorf("connecting to %s: %w", addr, err)
}

func hostOnly(host string) string {
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

func fingerprintsOf(want []knownhosts.KnownKey) string {
	out := make([]string, 0, len(want))
	for _, k := range want {
		out = append(out, ssh.FingerprintSHA256(k.Key))
	}
	return strings.Join(out, " or ")
}
