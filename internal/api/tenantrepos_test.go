package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

func channelYAML(name, listen string) string {
	return "name: " + name + "\n" +
		"source:\n  type: mllp\n  listen: " + listen + "\n" +
		"destinations:\n  - name: out\n    type: file\n    dir: /tmp\n"
}

func TestDefaultTenantUsesTheBaseDirectory(t *testing.T) {
	// The backward-compatibility guarantee. Every installation running today points
	// -channels at a directory of loose yaml files, and those files must keep working
	// without being moved.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := tr.DirFor(tenant.ID(store.DefaultTenant))
	if err != nil {
		t.Fatal(err)
	}
	if got != tr.Base() {
		t.Errorf("the default tenant's directory is %q, want the base %q", got, tr.Base())
	}
}

func TestOtherTenantsGetASubdirectory(t *testing.T) {
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := tr.DirFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tr.Base(), "acme")
	if got != want {
		t.Errorf("DirFor(acme) = %q, want %q", got, want)
	}
}

func TestDefaultTenantCannotSeeAnotherTenantsChannels(t *testing.T) {
	// Load-bearing rather than incidental. The default tenant reads the base
	// directory, and other tenants are subdirectories of it. If ChannelRepo.files()
	// ever started recursing, the default tenant would list every other tenant's
	// channel definitions - so this is asserted rather than assumed.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	mainRepo, err := tr.Repo(tenant.ID(store.DefaultTenant))
	if err != nil {
		t.Fatal(err)
	}
	acmeRepo, err := tr.Repo("acme")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mainRepo.Dir, "mine.yaml"),
		[]byte(channelYAML("mine", "127.0.0.1:12001")), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acmeRepo.Dir, "theirs.yaml"),
		[]byte(channelYAML("theirs", "127.0.0.1:12002")), 0o640); err != nil {
		t.Fatal(err)
	}

	mine, _, err := mainRepo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Name != "mine" {
		t.Fatalf("the default tenant sees %+v; it must not see another tenant's channels", mine)
	}

	theirs, _, err := acmeRepo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 1 || theirs[0].Name != "theirs" {
		t.Fatalf("acme sees %+v", theirs)
	}
}

func TestTwoTenantsCanHaveAChannelOfTheSameName(t *testing.T) {
	// Separate directories, so "adt-inbound" is a perfectly ordinary name for two
	// different organisations to choose. In a single shared directory it would be a
	// filename collision.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	acme, err := tr.Repo("acme")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := tr.Repo("beta")
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range []*ChannelRepo{acme, beta} {
		if err := os.WriteFile(filepath.Join(r.Dir, "adt-inbound.yaml"),
			[]byte(channelYAML("adt-inbound", "127.0.0.1:12003")), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	for _, r := range []*ChannelRepo{acme, beta} {
		got, _, err := r.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "adt-inbound" {
			t.Errorf("%s sees %+v", r.Dir, got)
		}
	}
}

func TestUnsafeTenantIDsAreRefused(t *testing.T) {
	// The id becomes a path segment. A traversing id would read another tenant's
	// channel definitions, or something outside the channel directory entirely.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []tenant.ID{"..", "../escape", "a/b", `a\b`, "", "UPPER", ".hidden", "api"} {
		if _, err := tr.DirFor(id); err == nil {
			t.Errorf("DirFor(%q) was accepted", id)
		}
		if _, err := tr.Repo(id); err == nil {
			t.Errorf("Repo(%q) was accepted", id)
		}
	}
}

func TestEveryTenantDirectoryStaysUnderTheBase(t *testing.T) {
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []tenant.ID{"a", "acme", "st-josephs", "hospital-2"} {
		got, err := tr.DirFor(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, tr.Base()+string(filepath.Separator)) {
			t.Errorf("DirFor(%q) = %q, which is not under %q", id, got, tr.Base())
		}
	}
}

func TestRepoIsCachedSoTheLockIsShared(t *testing.T) {
	// ChannelRepo holds a mutex serialising writes. Handing out a fresh one per call
	// would give two editors separate locks and let them interleave into a file that
	// is neither version.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	a, err := tr.Repo("acme")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tr.Repo("acme")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("Repo returned a different instance for the same tenant; the write lock would not be shared")
	}
}

func TestForgetDropsTheCacheButKeepsTheFiles(t *testing.T) {
	// A channel file is often the only record of how an interface was configured.
	// Deleting a customer must not silently destroy it.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	r, err := tr.Repo("leaving")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Dir, "kept.yaml")
	if err := os.WriteFile(path, []byte(channelYAML("kept", "127.0.0.1:12004")), 0o640); err != nil {
		t.Fatal(err)
	}

	tr.Forget("leaving")

	if _, err := os.Stat(path); err != nil {
		t.Errorf("the channel file was removed: %v", err)
	}
}

func TestKnownListsTenantDirectories(t *testing.T) {
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []tenant.ID{"acme", "beta"} {
		if _, err := tr.Repo(id); err != nil {
			t.Fatal(err)
		}
	}
	// Things that are not tenants and must be ignored rather than reported: a
	// backup folder, a version control directory, and a loose file.
	for _, name := range []string{".git", "Backup-2024", "not-a-tenant.txt"} {
		p := filepath.Join(dir, name)
		if strings.Contains(name, ".txt") {
			if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	got, err := tr.Known()
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, id := range got {
		ids = append(ids, string(id))
	}
	joined := strings.Join(ids, ",")
	for _, want := range []string{store.DefaultTenant, "acme", "beta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Known() = %v, missing %q", ids, want)
		}
	}
	for _, unwanted := range []string{".git", "Backup-2024", "not-a-tenant"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("Known() = %v, should not include %q", ids, unwanted)
		}
	}
}

func TestKnownAlwaysIncludesTheDefaultTenant(t *testing.T) {
	// Its channels are the loose files in the base directory, so it exists whether or
	// not anybody has created a subdirectory.
	dir := t.TempDir()
	tr, err := NewTenantRepos(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := tr.Known()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != store.DefaultTenant {
		t.Errorf("Known() on an empty base = %v, want just the default tenant", got)
	}
}
