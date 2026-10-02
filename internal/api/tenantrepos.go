package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// TenantRepos hands out one ChannelRepo per tenant.
//
// Channels stay in files. That is the invariant everything else in this program
// rests on, and multi-tenancy does not get to break it - it just means a directory
// per tenant instead of one directory.
//
// # The layout, and why it is asymmetric
//
//	<base>/*.yaml            the default tenant's channels
//	<base>/<tenant>/*.yaml   any other tenant's channels
//
// The default tenant uses the base directory itself rather than <base>/main. That
// asymmetry is deliberate and it is the whole point: every installation running
// today points -channels at a directory of loose yaml files. Requiring them to
// become <base>/main/*.yaml would make every existing channel vanish on upgrade,
// and an operator discovering that has already lost their feeds.
//
// It works because ChannelRepo.files() skips directories and does not recurse, so
// the default tenant cannot see another tenant's subdirectory. That is load-bearing
// rather than incidental, and there is a test asserting it.
type TenantRepos struct {
	base string

	mu    sync.Mutex
	repos map[tenant.ID]*ChannelRepo
}

// NewTenantRepos roots a set of per-tenant repositories at base.
func NewTenantRepos(base string) (*TenantRepos, error) {
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("resolving the channel directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	return &TenantRepos{base: abs, repos: map[tenant.ID]*ChannelRepo{}}, nil
}

// Base reports the root directory.
func (tr *TenantRepos) Base() string { return tr.base }

// DirFor returns the directory a tenant's channels live in.
//
// Exported because the error messages elsewhere are much more useful when they can
// name the path somebody needs to look at.
func (tr *TenantRepos) DirFor(id tenant.ID) (string, error) {
	if string(id) == store.DefaultTenant {
		return tr.base, nil
	}
	if err := tenant.ValidateID(id); err != nil {
		return "", err
	}

	dir := filepath.Join(tr.base, string(id))

	// Belt and braces. The id is already validated to contain no dots or slashes,
	// so this cannot currently fail - which is exactly when a check is cheap and
	// worth keeping, because the validation and this join are edited by different
	// people at different times. A tenant directory outside the base is one tenant
	// reading another's channel definitions, or /etc.
	clean := filepath.Clean(dir)
	if clean != dir || !strings.HasPrefix(clean, tr.base+string(filepath.Separator)) {
		return "", fmt.Errorf("tenant %q resolves to %q, which is outside the channel directory %q", id, clean, tr.base)
	}
	return clean, nil
}

// Repo returns the repository for a tenant, creating the directory on first use.
func (tr *TenantRepos) Repo(id tenant.ID) (*ChannelRepo, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	if r, ok := tr.repos[id]; ok {
		return r, nil
	}

	dir, err := tr.DirFor(id)
	if err != nil {
		return nil, err
	}
	r, err := NewChannelRepo(dir)
	if err != nil {
		return nil, fmt.Errorf("preparing channel directory for tenant %q: %w", id, err)
	}
	tr.repos[id] = r
	return r, nil
}

// Forget drops a cached repository.
//
// Called when a tenant is deleted. The directory is deliberately left on disk: a
// channel file is a definition somebody wrote, frequently the only record of how an
// interface was configured, and deleting a customer should not silently destroy it.
// Removing the directory is a separate, explicit act.
func (tr *TenantRepos) Forget(id tenant.ID) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.repos, id)
}

// Known returns the tenants that currently have a directory on disk.
//
// Reads the filesystem rather than the cache, because the answer wanted is "what is
// actually there", which is what somebody investigating a missing channel needs.
func (tr *TenantRepos) Known() ([]tenant.ID, error) {
	entries, err := os.ReadDir(tr.base)
	if err != nil {
		return nil, err
	}

	// The default tenant always counts: its channels are the loose files in base,
	// and it exists whether or not anybody has created a subdirectory.
	out := []tenant.ID{tenant.ID(store.DefaultTenant)}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := tenant.ID(e.Name())
		// A directory that is not a valid tenant id is skipped rather than
		// reported. Somebody's backup folder or a .git directory in there is not an
		// error, and refusing to start over it would be obnoxious.
		if tenant.ValidateID(id) != nil {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}
