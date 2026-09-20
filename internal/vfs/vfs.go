// Package vfs is the small set of file operations a polling connector needs.
//
// # Why an interface rather than four connectors
//
// Perfuse collects files from a local disk, an SFTP server, an FTP server, a Windows share and a WebDAV server. The
// transfer differs in every case. Everything that actually decides whether messages survive does not:
//
//   - A file being written and a file finished being written are indistinguishable. There is no lock, no flag and no
//     notification on any of these transports - there is a size and a modification time, and both are true of a
//     half-written file. Reading too early collects half a message, and HL7 has no terminator, so half a message is
//     very often still parseable. The MSH is intact, the segments that arrived are well formed, the ones that did not
//     are simply absent. It is accepted, acknowledged, stored, delivered, and nothing will ever mention it.
//   - A file must not be disposed of until every message in it has been accepted, because the file is the only copy.
//   - A file that failed must not be picked up again on the next poll forever, and must not be silently dropped.
//
// Written once per transport, those rules would be subtly different five times, and the differences would only appear
// as messages that went missing at one site. So they are written once, here, over the smallest interface that supports
// them.
//
// # What is deliberately not here
//
// No writing, beyond what disposal needs. Sending files is a different problem with different failure modes - a
// partially written file at the far end, which the existing senders handle by writing to a temporary name and renaming.
// Folding both directions into one interface would make each harder to reason about.
//
// No recursion. A poller that descends into subdirectories collects the archive directory it just moved a file into.
package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Entry is one directory entry.
//
// Deliberately not fs.FileInfo. That interface carries a Mode and a Sys, neither of which means anything comparable
// across FTP, WebDAV and a local disk, and a field that means something different per backend is a field somebody will
// eventually rely on.
type Entry struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// FS is a directory a connector can collect files from.
//
// Every method takes a context because every implementation but the local one is a network call, and a poll that cannot
// be abandoned holds up shutdown until a TCP timeout expires.
type FS interface {
	// List returns the entries in dir. It does not recurse.
	List(ctx context.Context, dir string) ([]Entry, error)

	// Open reads one file. The caller closes it.
	Open(ctx context.Context, path string) (io.ReadCloser, error)

	// Remove deletes one file.
	Remove(ctx context.Context, path string) error

	// Rename moves a file, which is how both archiving and error quarantine are done.
	//
	// Implementations must not silently fall back to copy-then-delete. On a local disk a rename across filesystems
	// fails, and a connector that quietly copied instead would leave the original in place to be collected again on
	// the next poll - the same message delivered every thirty seconds until somebody notices.
	Rename(ctx context.Context, from, to string) error

	// MkdirAll creates a directory and its parents, for the archive and error directories.
	MkdirAll(ctx context.Context, dir string) error

	// Join joins path elements using this filesystem's separator.
	//
	// On the interface rather than done with path.Join by the caller, because a Windows share needs backslashes and a
	// local Windows disk accepts either while a local Linux disk does not.
	Join(elem ...string) string

	// Describe names this location for a log line or an error, without credentials in it.
	Describe() string

	// Close releases the connection. Calling it on a filesystem that has none is not an error.
	Close() error
}

// ErrNotSupported is returned by an operation a backend genuinely cannot perform.
//
// Distinct from a failure, because the caller's response differs: a failure is worth retrying and worth an alert, while
// an unsupported operation will never succeed and the configuration has to change instead.
var ErrNotSupported = errors.New("this file transport does not support that operation")

// Unsupported wraps ErrNotSupported with what was attempted and what to do instead.
//
// The message matters more than usual here. "Operation not supported" sends somebody to read the source; naming the
// transport and the alternative lets them fix it.
func Unsupported(transport, op, instead string) error {
	if instead == "" {
		return fmt.Errorf("%s cannot %s: %w", transport, op, ErrNotSupported)
	}
	return fmt.Errorf("%s cannot %s, so %s: %w", transport, op, instead, ErrNotSupported)
}
