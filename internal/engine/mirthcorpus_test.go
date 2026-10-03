package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
	"github.com/biodream-llc/perfuse/internal/translate"
	"github.com/biodream-llc/perfuse/mllp"
)

// A channel each engine exported itself, translated from that engine's own server backup, and then run.
//
// The channel maps PID-3.1 into a channel variable, calls formatMRN from the site's code template library with it, and filters to ADT.
// Loading proves the YAML is valid; only running proves the library is reachable and the mapper's variable is where $('mrn') looks. The
// first run found it was not: the mapper had been translated as a JavaScript global, so the library padded an empty string.
func TestEveryEnginesADTChannelRunsWithItsLibrary(t *testing.T) {
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
			res := translate.Bundle(b)

			out := t.TempDir()
			for _, l := range res.Libraries {
				p := filepath.Join(out, l.File)
				_ = os.MkdirAll(filepath.Dir(p), 0o750)
				if err := os.WriteFile(p, []byte(l.Source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var yaml string
			for _, r := range res.Channels {
				if r.Name == "adt-inbound-from-ward" {
					yaml = strings.Replace(r.YAML, "0.0.0.0:6661", "127.0.0.1:0", 1)
				}
			}
			path := filepath.Join(out, "adt.yaml")
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			senders := map[string]*recordingSender{}
			ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) {
				s := &recordingSender{name: d.Name}
				senders[d.Name] = s
				return s, nil
			}, quiet())
			if err != nil {
				t.Fatal(err)
			}
			if err := ch.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = ch.Stop(ctx)
			})

			c := &mllp.Client{Addr: ch.server.Addrs(), Timeout: 10 * time.Second}
			defer c.Close()
			for _, m := range []string{
				"MSH|^~\\&|WARD|H|REG|H|20260101120000||ADT^A01|C1|P|2.5\rPID|1||12-34^^^H^MR||DOE^JANE||19800101|F\r",
				"MSH|^~\\&|LAB|H|REG|H|20260101120000||ORU^R01|C2|P|2.5\rPID|1||99^^^H^MR\r",
			} {
				if _, err := c.Send(context.Background(), []byte(m)); err != nil {
					t.Fatal(err)
				}
			}

			archive := senders["archive-to-disk"]
			if archive == nil || archive.count() != 1 {
				t.Fatalf("the archive received %v messages, want 1: the ORU should have been filtered", archive)
			}
			if !strings.Contains(archive.last(), "PID|1||0000001234^") {
				t.Errorf("the MRN was not padded by the site library:\n%s", strings.ReplaceAll(archive.last(), "\r", "\n"))
			}
		})
	}
}
