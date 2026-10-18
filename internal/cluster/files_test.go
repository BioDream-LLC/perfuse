package cluster

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestFilesAreKeptTheSameOnEveryServer(t *testing.T) {
	url := testURL(t)
	db := openURL(t, url)
	ctx := context.Background()
	dirA, dirB := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<none>"
		}
		return string(b)
	}
	var mu sync.Mutex
	var changed []string
	mk := func(node, dir string) *Files {
		f, err := NewFiles(ctx, db, node, []Area{{Name: "channels", Dir: dir}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.Changed = func(area, rel string, deleted bool) {
			mu.Lock()
			defer mu.Unlock()
			changed = append(changed, node+":"+rel)
		}
		return f
	}
	write(dirA, "adt.yaml", "name: adt\n")
	write(dirA, "lab/results.yaml", "name: results\n")
	write(dirA, ".hidden", "no")
	a, b := mk("a", dirA), mk("b", dirB)
	sync := func() {
		t.Helper()
		for _, f := range []*Files{a, b, a, b} {
			if err := f.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	sync()
	if read(dirB, "adt.yaml") != "name: adt\n" || read(dirB, "lab/results.yaml") != "name: results\n" || read(dirB, ".hidden") != "<none>" {
		t.Fatalf("b has %q %q %q", read(dirB, "adt.yaml"), read(dirB, "lab/results.yaml"), read(dirB, ".hidden"))
	}

	// A change on b reaches a, and a is told.
	write(dirB, "adt.yaml", "name: adt\ndescription: edited on b\n")
	mu.Lock()
	changed = nil
	mu.Unlock()
	sync()
	if read(dirA, "adt.yaml") != "name: adt\ndescription: edited on b\n" {
		t.Fatalf("a has %q", read(dirA, "adt.yaml"))
	}
	mu.Lock()
	if len(changed) != 1 || changed[0] != "a:adt.yaml" {
		t.Errorf("changed %v", changed)
	}
	mu.Unlock()

	// A deletion travels.
	if err := os.Remove(filepath.Join(dirA, "lab/results.yaml")); err != nil {
		t.Fatal(err)
	}
	sync()
	if read(dirB, "lab/results.yaml") != "<none>" {
		t.Error("the deletion did not reach b")
	}

	// The same file changed on both between looks: the first to reach the store wins; the other is kept aside.
	write(dirA, "adt.yaml", "name: adt\ndescription: from a\n")
	write(dirB, "adt.yaml", "name: adt\ndescription: from b\n")
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	sync()
	if read(dirA, "adt.yaml") != "name: adt\ndescription: from a\n" || read(dirB, "adt.yaml") != "name: adt\ndescription: from a\n" {
		t.Fatalf("a %q, b %q", read(dirA, "adt.yaml"), read(dirB, "adt.yaml"))
	}
	if read(dirB, "adt.yaml.conflict-b") != "name: adt\ndescription: from b\n" {
		t.Errorf("b's version was not kept aside: %q", read(dirB, "adt.yaml.conflict-b"))
	}

	// A third server joining with a stale file of its own takes the cluster's, keeping its own aside, and adds what only it has.
	dirC := t.TempDir()
	write(dirC, "adt.yaml", "stale\n")
	write(dirC, "new.yaml", "name: new\n")
	c := mk("c", dirC)
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	sync()
	if read(dirC, "adt.yaml") != "name: adt\ndescription: from a\n" || read(dirC, "adt.yaml.conflict-c") != "stale\n" ||
		read(dirA, "new.yaml") != "name: new\n" {
		t.Errorf("c joined badly: %q %q; a has new.yaml %q", read(dirC, "adt.yaml"), read(dirC, "adt.yaml.conflict-c"), read(dirA, "new.yaml"))
	}
	names, _ := a.list(Area{Name: "channels", Dir: dirA})
	sort.Strings(names)
	if len(names) != 2 {
		t.Errorf("a lists %v: conflict copies and hidden files are not shared", names)
	}
	if !safeRel("lab/x.yaml") || safeRel("../x") || safeRel("/etc/passwd") || safeRel("a/../../x") {
		t.Error("safeRel")
	}
}
