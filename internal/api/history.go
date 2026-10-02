package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/vcs"
)

// Channel history.
//
// This is a paid extension in Mirth, and it is a paid extension because channels
// live in a database there, so tracking changes means building a change-tracking
// system. Here the channels are files, so the history already exists. These
// handlers read it.

// handleChannelHistory lists the commits that touched one channel.
func (s *Server) handleChannelHistory(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	path, err := channels.PathFor(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	repo := &vcs.Repo{Dir: channels.Dir}
	status := repo.Status()
	if !status.Available {
		// Not an error. Plenty of deployments will not use git, and the answer is
		// "history is unavailable, here is how to have it" rather than a failure.
		s.ok(w, map[string]any{
			"available": false,
			"reason":    status.Reason,
			"commits":   []vcs.Commit{},
		})
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	commits, err := repo.History(path, limit)
	if err != nil {
		// A channel created through the interface and never committed has no
		// history, which is a normal state rather than a fault.
		commits = nil
	}
	if commits == nil {
		commits = []vcs.Commit{}
	}

	// Whether this specific file has uncommitted changes, not just the directory.
	// An edit saved through the interface is on disk and is not history until
	// something commits it, and somebody looking at this page needs to know that
	// about the channel in front of them.
	base := baseName(path)
	uncommitted := false
	for _, f := range append(status.Modified, status.Untracked...) {
		if f == base {
			uncommitted = true
			break
		}
	}

	s.ok(w, map[string]any{
		"available":   true,
		"branch":      status.Branch,
		"commits":     commits,
		"uncommitted": uncommitted,
		"file":        base,
	})
}

// handleChannelVersion returns a channel as it was at one commit, with a diff
// against what is on disk now.
func (s *Server) handleChannelVersion(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")
	hash := r.PathValue("hash")

	path, err := channels.PathFor(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	repo := &vcs.Repo{Dir: channels.Dir}
	raw, err := repo.FileAt(path, hash)
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepo) || errors.Is(err, vcs.ErrGitMissing) {
			s.fail(w, r, http.StatusNotImplemented, "file history is unavailable here")
			return
		}
		s.fail(w, r, http.StatusNotFound, err.Error())
		return
	}

	diff, _ := repo.Diff(path, hash)

	// Validated as well as returned. An old version that no longer loads is worth
	// knowing about before somebody restores it, because the channel would then
	// fail to start and the interface would have offered them the button.
	var problem string
	if _, err := channels.Validate(raw); err != nil {
		problem = err.Error()
	}

	s.ok(w, map[string]any{
		"hash":  hash,
		"yaml":  string(raw),
		"diff":  string(diff),
		"valid": problem == "",
		"error": problem,
	})
}

// handleRestoreChannelVersion writes an old version back over the current file.
//
// It is a normal update rather than a special path: the same validation runs, the
// same audit entry is written, and the change appears in git as a new commit
// rather than as a rewrite of history. Restoring is going forwards to an earlier
// state, which is the only kind of undo that leaves a record.
func (s *Server) handleRestoreChannelVersion(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")
	hash := r.PathValue("hash")

	path, err := channels.PathFor(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	repo := &vcs.Repo{Dir: channels.Dir}
	raw, err := repo.FileAt(path, hash)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, err.Error())
		return
	}

	updated, err := channels.Update(name, raw)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "channel.restore",
		Target:   name,
		Detail:   "restored the version from commit " + hash,
		IP:       clientIP(r),
	})

	// A running channel keeps running the version it started with. Restarting it
	// here would interrupt a live feed as a side effect of a configuration change,
	// which should be a separate decision.
	s.ok(w, map[string]any{
		"channel":  updated.Name,
		"restored": hash,
		"note": "The file is updated. A running channel keeps the version it " +
			"started with until it is restarted.",
	})
}

// handleChannelRepoStatus reports whether history is available at all, so the
// interface can decide whether to offer it.
func (s *Server) handleChannelRepoStatus(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	repo := &vcs.Repo{Dir: channels.Dir}
	s.ok(w, repo.Status())
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
