package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/analyze"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Each engine's own server backup, translated and loaded. The corpus is written by Mirth 4.5.2, OIE and BridgeLink themselves
// (scripts/mirth-engine-corpus.sh), so this is the migration a site leaving any of them would run.
func TestEveryEnginesBackupTranslatesAndLoads(t *testing.T) {
	dirs, _ := filepath.Glob("../mirth/testdata/engines/*")
	if len(dirs) == 0 {
		t.Fatal("no engine corpus")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			b, err := mirth.ParseBundleFile(filepath.Join(dir, "server-configuration.xml"))
			if err != nil {
				t.Fatal(err)
			}
			res := Bundle(b)
			if len(res.Channels) != 3 || len(res.Libraries) != 1 || len(res.Groups) != 1 {
				t.Fatalf("%d channels, %d libraries, %d groups", len(res.Channels), len(res.Libraries), len(res.Groups))
			}
			lib := res.Libraries[0]
			if lib.Functions != 1 || lib.Skipped != 1 || len(lib.Channels) != 1 || lib.Channels[0] != "adt-inbound-from-ward" {
				t.Errorf("library: %+v", lib)
			}

			out := t.TempDir()
			if err := os.MkdirAll(filepath.Join(out, LibraryDir), 0o750); err != nil {
				t.Fatal(err)
			}
			for _, l := range res.Libraries {
				if err := os.WriteFile(filepath.Join(out, l.File), []byte(l.Source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range res.Channels {
				if r.Counts.Blockers > 0 {
					for _, n := range r.Notes {
						if n.Severity == "blocker" {
							t.Errorf("%s: blocker: %s", r.Name, n.Message)
						}
					}
				}
				p := filepath.Join(out, r.FileName())
				if err := os.WriteFile(p, []byte(r.YAML), 0o600); err != nil {
					t.Fatal(err)
				}
				ch, err := config.LoadFile(p)
				if err != nil {
					t.Errorf("%s does not load: %v\n%s", r.Name, err, r.YAML)
					continue
				}
				switch r.Name {
				case "adt-inbound-from-ward":
					if ch.Group != "Admissions and labs" || ch.Scripts == nil || len(ch.Scripts.Include) != 1 {
						t.Errorf("ADT: group %q, scripts %+v", ch.Group, ch.Scripts)
					}
					if len(ch.Destinations) != 2 || ch.Destinations[1].HTTP == nil ||
						ch.Destinations[1].HTTP.URL != "https://registry.example.invalid/adt" {
						t.Errorf("ADT destinations: %+v", ch.Destinations)
					}
				case "lab-results-to-warehouse":
					if ch.Source.Type != config.SourceHTTP || ch.Source.HTTP == nil || ch.Source.HTTP.Path != "/results" ||
						!strings.HasSuffix(ch.Source.HTTP.Listen, ":8081") {
						t.Errorf("lab source: %+v", ch.Source)
					}
				case "orders-file-drop":
					if ch.Source.Type != config.SourceFile || ch.Source.File == nil || ch.Source.File.Root != "/var/spool/orders/in" ||
						ch.Source.File.Pattern != "*.hl7" {
						t.Errorf("orders source: %+v", ch.Source)
					}
				}
			}
		})
	}
}

// explain and translate must agree on which connectors are supported. They drifted once: explain called the Database Writer and the
// DICOM connectors unsupported long after translate converted them, so the report a site reads first said its channels were blocked.
func TestExplainAgreesWithTranslateOnEveryEnginesChannels(t *testing.T) {
	dirs, _ := filepath.Glob("../mirth/testdata/engines/*")
	for _, dir := range dirs {
		b, err := mirth.ParseBundleFile(filepath.Join(dir, "server-configuration.xml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range b.Channels {
			explained := analyze.Channel(c)
			translated := Channel(c)
			explainBlocks := false
			for _, f := range explained.Findings {
				if f.Code == "TRANSPORT_UNSUPPORTED" {
					explainBlocks = true
				}
			}
			translateBlocks := false
			for _, n := range translated.Notes {
				if n.Severity == "blocker" && strings.Contains(n.Message, "cannot convert") || strings.Contains(n.Message, "does not implement") {
					translateBlocks = true
				}
			}
			if explainBlocks != translateBlocks {
				t.Errorf("%s/%s: explain says unsupported=%v, translate says %v", filepath.Base(dir), c.Name, explainBlocks, translateBlocks)
			}
		}
	}
}
