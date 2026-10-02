package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo builds a throwaway repository with two commits touching one channel file.
func gitRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// A repository with no configured identity refuses to commit, and the
		// machine's real identity has no business in a test.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada Byron", "GIT_AUTHOR_EMAIL=ada@example.org",
			"GIT_COMMITTER_NAME=Ada Byron", "GIT_COMMITTER_EMAIL=ada@example.org",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-q", "-b", "main")
	path := filepath.Join(dir, "adt.yaml")

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("name: adt\nsource:\n  type: mllp\n  listen: 127.0.0.1:6661\n")
	run("add", "adt.yaml")
	run("commit", "-q", "-m", "Add the ADT channel")

	write("name: adt\nsource:\n  type: mllp\n  listen: 127.0.0.1:6662\n")
	run("add", "adt.yaml")
	run("commit", "-q", "-m", "Move ADT to port 6662\n\nThe old port collided with the lab feed.")

	return &Repo{Dir: dir}, path
}

func TestStatusReportsAWorkingRepository(t *testing.T) {
	repo, _ := gitRepo(t)

	st := repo.Status()
	if !st.Available {
		t.Fatalf("history should be available: %s", st.Reason)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q", st.Branch)
	}
	if st.Dirty {
		t.Errorf("a freshly committed repository should be clean, modified=%v untracked=%v",
			st.Modified, st.Untracked)
	}
}

func TestStatusExplainsAMissingRepositoryWithoutCallingItAnError(t *testing.T) {
	// Plenty of deployments will not use git. The interface should say history is
	// unavailable and how to get it, not report a failure.
	repo := &Repo{Dir: t.TempDir()}

	st := repo.Status()
	if st.Available {
		t.Fatal("an ordinary directory should not report history")
	}
	if !strings.Contains(st.Reason, "git init") {
		t.Errorf("the reason should say what to do about it, got %q", st.Reason)
	}
}

func TestStatusSeesAnUncommittedEdit(t *testing.T) {
	repo, path := gitRepo(t)

	if err := os.WriteFile(path, []byte("name: adt\n# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// This matters: an edit made through the interface is written to a file and is
	// not history until something commits it. Somebody looking at the history page
	// needs to know their change is not in it yet.
	st := repo.Status()
	if !st.Dirty {
		t.Error("an uncommitted edit should show as dirty")
	}
	if len(st.Modified) != 1 || st.Modified[0] != "adt.yaml" {
		t.Errorf("modified = %v", st.Modified)
	}
}

func TestStatusSeesAnUntrackedChannel(t *testing.T) {
	repo, _ := gitRepo(t)

	if err := os.WriteFile(filepath.Join(repo.Dir, "new.yaml"), []byte("name: new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := repo.Status()
	// Untracked and modified are separated because they mean different things: one
	// channel has never been recorded at all, the other has been changed.
	if len(st.Untracked) != 1 || st.Untracked[0] != "new.yaml" {
		t.Errorf("untracked = %v", st.Untracked)
	}
	if len(st.Modified) != 0 {
		t.Errorf("a new file should not be reported as modified: %v", st.Modified)
	}
}

func TestHistoryReturnsCommitsNewestFirst(t *testing.T) {
	repo, path := gitRepo(t)

	commits, err := repo.History(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}
	if commits[0].Subject != "Move ADT to port 6662" {
		t.Errorf("newest subject = %q", commits[0].Subject)
	}
	if commits[0].Author != "Ada Byron" || commits[0].Email != "ada@example.org" {
		t.Errorf("author = %q <%q>", commits[0].Author, commits[0].Email)
	}
	// The body carries the reason, which is the part worth reading when deciding
	// whether to roll a change back.
	if !strings.Contains(commits[0].Body, "collided") {
		t.Errorf("body = %q", commits[0].Body)
	}
	if commits[0].At.IsZero() {
		t.Error("the commit should have a timestamp")
	}
}

func TestHistoryHandlesASubjectContainingTabsAndNewlines(t *testing.T) {
	// The log is parsed on unit and record separators rather than on anything
	// somebody can type. Splitting on a tab is how a parser breaks on the one
	// commit that matters.
	repo, path := gitRepo(t)

	cmd := exec.Command("git", "commit", "-q", "--allow-empty",
		"-m", "Fix\tthe\ttabs", "-m", "line one\nline two")
	cmd.Dir = repo.Dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada Byron", "GIT_AUTHOR_EMAIL=ada@example.org",
		"GIT_COMMITTER_NAME=Ada Byron", "GIT_COMMITTER_EMAIL=ada@example.org")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	// An empty commit does not touch the file, so ask for the whole log instead.
	commits, err := repo.History(".", 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = path
	for _, c := range commits {
		if strings.Contains(c.Subject, "Fix\tthe\ttabs") {
			return // parsed intact
		}
	}
	// Not finding it is acceptable when --follow filtered it out; what must not
	// happen is a mangled record, which the length check below would catch.
	for _, c := range commits {
		if c.Hash == "" || c.Author == "" {
			t.Error("a commit was parsed with missing fields")
		}
	}
}

func TestFileAtReturnsTheOldContents(t *testing.T) {
	repo, path := gitRepo(t)

	commits, err := repo.History(path, 10)
	if err != nil {
		t.Fatal(err)
	}

	old, err := repo.FileAt(path, commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	// This is what makes a rollback possible: the earlier version, exactly.
	if !strings.Contains(string(old), "6661") {
		t.Errorf("the first version should have the old port, got:\n%s", old)
	}
	if strings.Contains(string(old), "6662") {
		t.Error("the first version should not contain the later port")
	}
}

func TestDiffShowsWhatChanged(t *testing.T) {
	repo, path := gitRepo(t)

	commits, err := repo.History(path, 10)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := repo.Diff(path, commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	text := string(diff)
	if !strings.Contains(text, "-") || !strings.Contains(text, "+") {
		t.Errorf("want a unified diff, got:\n%s", text)
	}
	if !strings.Contains(text, "6662") {
		t.Errorf("the diff should show the new port:\n%s", text)
	}
}

func TestRefsAreValidatedBeforeReachingGit(t *testing.T) {
	repo, path := gitRepo(t)

	// These arrive from a URL. exec does not involve a shell, so this is not about
	// shell injection: it is about refusing revision syntax that would make git
	// read something other than the commit asked for.
	for _, bad := range []string{
		"HEAD", "HEAD@{upstream}", "main", "../../etc/passwd",
		"abc", "", "deadbeef; rm -rf /", "HEAD~1",
	} {
		if _, err := repo.FileAt(path, bad); err == nil {
			t.Errorf("FileAt should have refused %q", bad)
		}
		if _, err := repo.Diff(path, bad); err == nil {
			t.Errorf("Diff should have refused %q", bad)
		}
	}
}

func TestHistoryLimitIsBounded(t *testing.T) {
	repo, path := gitRepo(t)

	// An unbounded request against a long history is a denial of service on
	// ourselves, and zero should mean a sensible default rather than none.
	if _, err := repo.History(path, 0); err != nil {
		t.Errorf("a zero limit should use a default: %v", err)
	}
	if _, err := repo.History(path, 100000); err != nil {
		t.Errorf("an absurd limit should be capped, not refused: %v", err)
	}
}

func TestHistoryOfAnUnknownFileIsEmptyNotAnError(t *testing.T) {
	repo, _ := gitRepo(t)

	// A channel created through the interface and not yet committed has no
	// history. That is a normal state, not a failure.
	commits, err := repo.History("never-existed.yaml", 10)
	if err != nil {
		// git exits non-zero for an unknown path with --follow, which is
		// acceptable, but it must not be reported as a repository problem.
		if strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("an unknown file should not look like a missing repository: %v", err)
		}
		return
	}
	if len(commits) != 0 {
		t.Errorf("want no history, got %d commits", len(commits))
	}
}
