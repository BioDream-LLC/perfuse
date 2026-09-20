// Package vcs reads the history of channel files from git.
//
// Channel history is a paid extension elsewhere, and it is a paid extension
// because those engines keep channels in a database, so tracking changes means
// building a change-tracking system. Perfuse keeps channels in files, so the
// history already exists and has since the first commit. This package only has to
// read it.
//
// It shells out to git rather than embedding a git library. The library would add
// a large dependency to a binary whose selling point is having four, and it would
// buy nothing: a deployment keeping its channels under version control has git
// installed, and one that does not gets a clear "not a git repository" rather than
// a subtly different implementation of the same thing.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotARepo means the directory is not under version control.
//
// Distinguished from every other failure because it is the one case that is not a
// problem: plenty of deployments will not use git, and the interface should say
// "history is unavailable because these files are not in a repository" rather
// than reporting an error.
var ErrNotARepo = errors.New("the channel directory is not a git repository")

// ErrGitMissing means git is not installed.
var ErrGitMissing = errors.New("git is not installed")

// Repo reads history for files in one directory.
type Repo struct {
	// Dir is the channel directory. It may be a subdirectory of the repository.
	Dir string

	// Timeout bounds one git invocation. A repository on a slow network mount
	// should not hang a request forever.
	Timeout time.Duration
}

// DefaultTimeout bounds a git call.
const DefaultTimeout = 10 * time.Second

// Commit is one change to a file.
type Commit struct {
	Hash      string    `json:"hash"`
	ShortHash string    `json:"shortHash"`
	Author    string    `json:"author"`
	Email     string    `json:"email,omitempty"`
	At        time.Time `json:"at"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body,omitempty"`
}

// Status describes whether history is available at all.
type Status struct {
	Available bool `json:"available"`
	// Reason explains why not, in words somebody can act on.
	Reason string `json:"reason,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Dirty means there are uncommitted changes, which matters: an edit made
	// through the interface is written to a file and is not history until
	// something commits it.
	Dirty bool `json:"dirty"`
	// Untracked is files present but never committed.
	Untracked []string `json:"untracked,omitempty"`
	// Modified is tracked files with uncommitted edits.
	Modified []string `json:"modified,omitempty"`
}

func (r *Repo) timeout() time.Duration {
	if r.Timeout <= 0 {
		return DefaultTimeout
	}
	return r.Timeout
}

// run executes git in the channel directory.
func (r *Repo) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	// A pager would block forever waiting for a terminal that is not there.
	cmd.Env = append(cmd.Environ(), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitMissing
		}
		detail := strings.TrimSpace(stderr.String())
		if strings.Contains(detail, "not a git repository") {
			return nil, ErrNotARepo
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), detail)
	}
	return stdout.Bytes(), nil
}

// Status reports whether history is available and what is uncommitted.
func (r *Repo) Status() Status {
	branch, err := r.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		reason := err.Error()
		switch {
		case errors.Is(err, ErrGitMissing):
			reason = "git is not installed, so file history cannot be read"
		case errors.Is(err, ErrNotARepo):
			reason = "these channel files are not in a git repository. " +
				"Run git init in the channel directory to get a full history of who " +
				"changed what, with no other setup"
		}
		return Status{Reason: reason}
	}

	st := Status{
		Available: true,
		Branch:    strings.TrimSpace(string(branch)),
	}

	// Porcelain format is the stable one; the human-readable output is explicitly
	// not promised to stay the same between versions.
	out, err := r.run("status", "--porcelain", "--", ".")
	if err != nil {
		return st
	}
	// Only the trailing newline is trimmed, never leading whitespace. In porcelain
	// format the first two characters are the status and the first of them is a
	// space for an unstaged change, so trimming the output as a whole shifts every
	// filename left by one and silently reports "dt.yaml" for "adt.yaml".
	for line := range strings.SplitSeq(strings.Trim(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		code, name := line[:2], strings.TrimSpace(line[3:])
		st.Dirty = true
		if code == "??" {
			st.Untracked = append(st.Untracked, name)
			continue
		}
		st.Modified = append(st.Modified, name)
	}
	return st
}

// History returns the commits that touched one file, newest first.
func (r *Repo) History(file string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	// Unit separator between fields and record separator between commits, because
	// a commit subject can contain anything including tabs and newlines. Splitting
	// on a delimiter somebody can type is how a log parser breaks on the one
	// commit that matters.
	const format = "%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%b%x1e"

	out, err := r.run("log",
		fmt.Sprintf("--max-count=%d", limit),
		"--format="+format,
		"--follow", // survive a rename, which happens when a channel is renamed
		"--", filepath.Base(file))
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// FileAt returns a file's contents at one commit, which is what makes a diff and
// a rollback possible.
func (r *Repo) FileAt(file, hash string) ([]byte, error) {
	if err := validRef(hash); err != nil {
		return nil, err
	}
	return r.run("show", hash+":./"+filepath.Base(file))
}

// Diff returns a unified diff of one file between a commit and now.
func (r *Repo) Diff(file, hash string) ([]byte, error) {
	if err := validRef(hash); err != nil {
		return nil, err
	}
	return r.run("diff", hash, "--", filepath.Base(file))
}

// validRef refuses anything that is not plainly a commit hash.
//
// The hash reaches this from a URL. git arguments are not shell-interpreted here
// because exec passes them directly, so this is not about shell injection: it is
// about refusing revision syntax such as "HEAD@{upstream}" or a path that would
// make git read something outside the repository.
func validRef(hash string) error {
	if len(hash) < 4 || len(hash) > 64 {
		return fmt.Errorf("%q is not a commit hash", hash)
	}
	for _, c := range hash {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return fmt.Errorf("%q is not a commit hash", hash)
	}
	return nil
}

func parseLog(out []byte) []Commit {
	var commits []Commit
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.Split(record, "\x1f")
		if len(fields) < 6 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[4])
		c := Commit{
			Hash:      fields[0],
			ShortHash: fields[1],
			Author:    fields[2],
			Email:     fields[3],
			At:        at,
			Subject:   fields[5],
		}
		if len(fields) > 6 {
			c.Body = strings.TrimSpace(fields[6])
		}
		commits = append(commits, c)
	}
	return commits
}
