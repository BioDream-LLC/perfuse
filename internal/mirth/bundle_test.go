package mirth

import (
	"path/filepath"
	"strings"
	"testing"
)

// The corpus each engine wrote itself (scripts/mirth-engine-corpus.sh): three channels, a code template library and a channel group, as
// per-channel exports, a group export, a library export and the engine's own server backup. These run without any engine, so CI holds the
// importer to every engine's real documents.

func engineDirs(t *testing.T) []string {
	t.Helper()
	dirs, err := filepath.Glob("testdata/engines/*")
	if err != nil || len(dirs) == 0 {
		t.Fatal("no engine corpus under testdata/engines; regenerate it with ./scripts/mirth-engine-corpus.sh")
	}
	return dirs
}

func TestEveryEnginesServerBackupReads(t *testing.T) {
	for _, dir := range engineDirs(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			b, err := ParseBundleFile(filepath.Join(dir, "server-configuration.xml"))
			if err != nil {
				t.Fatal(err)
			}
			if b.Kind != "server backup" {
				t.Errorf("kind %q", b.Kind)
			}
			if !strings.HasSuffix(filepath.Base(dir), "-"+b.Version) {
				t.Errorf("no engine version read from the backup")
			}
			if len(b.Channels) != 3 {
				t.Fatalf("%d channels, want 3", len(b.Channels))
			}
			if len(b.Groups) != 1 || b.Groups[0].Name != "Admissions and labs" || len(b.Groups[0].ChannelIDs) != 2 {
				t.Errorf("groups: %+v", b.Groups)
			}
			if len(b.Libraries) != 1 || len(b.Libraries[0].Templates) != 2 {
				t.Fatalf("libraries: %+v", b.Libraries)
			}

			adt := "c0a1e5d0-0000-4000-8000-000000000001"
			libs := b.LibrariesFor(adt)
			if len(libs) != 1 || !strings.Contains(libs[0].Source(), "function formatMRN") {
				t.Errorf("the ADT channel does not see the site library: %+v", libs)
			}
			if strings.Contains(libs[0].Source(), "TEST") {
				t.Error("a drag-and-drop snippet was written into the library file; Mirth never loads those into a script's scope")
			}
			if len(b.LibrariesFor("c0a1e5d0-0000-4000-8000-000000000003")) != 0 {
				t.Error("a channel the library is not enabled for was given it")
			}
			if b.GroupOf(adt) != "Admissions and labs" {
				t.Errorf("group of the ADT channel: %q", b.GroupOf(adt))
			}
			for _, c := range b.Channels {
				if c.Source.Transport == "" || len(c.Destinations) == 0 {
					t.Errorf("%s lost its connectors in the backup", c.Name)
				}
			}
		})
	}
}

func TestEveryEnginesGroupAndLibraryExportsRead(t *testing.T) {
	for _, dir := range engineDirs(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			g, err := ParseBundleFile(filepath.Join(dir, "channel-group.xml"))
			if err != nil {
				t.Fatal(err)
			}
			if g.Kind != "channel group" || len(g.Channels) != 2 || len(g.Groups) != 1 {
				t.Errorf("group export: kind %q, %d channels, %d groups", g.Kind, len(g.Channels), len(g.Groups))
			}

			l, err := ParseBundleFile(filepath.Join(dir, "code-template-libraries.xml"))
			if err != nil {
				t.Fatal(err)
			}
			if l.Kind != "code template libraries" || len(l.Libraries) != 1 {
				t.Fatalf("library export: kind %q, %d libraries", l.Kind, len(l.Libraries))
			}
			fn := l.Libraries[0].Templates[0]
			if fn.Type != "FUNCTION" || !strings.Contains(fn.Code, "function formatMRN") || len(fn.Contexts) == 0 {
				t.Errorf("the function template did not survive: %+v", fn)
			}
		})
	}
}

func TestEveryEnginesChannelExportsRead(t *testing.T) {
	for _, dir := range engineDirs(t) {
		files, _ := filepath.Glob(filepath.Join(dir, "channel-*.xml"))
		for _, f := range files {
			if strings.HasSuffix(f, "channel-group.xml") {
				continue
			}
			t.Run(filepath.Base(dir)+"/"+filepath.Base(f), func(t *testing.T) {
				b, err := ParseBundleFile(f)
				if err != nil {
					t.Fatal(err)
				}
				if b.Kind != "channel" || len(b.Channels) != 1 {
					t.Fatalf("kind %q, %d channels", b.Kind, len(b.Channels))
				}
			})
		}
	}
}

func TestABundleRefusesWhatIsNotAnExport(t *testing.T) {
	if _, err := ParseBundle(strings.NewReader("<html><body/></html>")); err == nil {
		t.Error("an HTML page was read as a Mirth export")
	}
	if _, err := ParseBundle(strings.NewReader("<list><string>x</string></list>")); err == nil {
		t.Error("a list of strings was read as a Mirth export")
	}
}
