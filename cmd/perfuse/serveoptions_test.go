package main

import (
	"bytes"
	"flag"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/settings"
)

// The manual's Server Options chapter is generated from serve's own flags.
//
// Twenty-five serve flags were in neither the manual nor the reference: a reader had to run -h to learn that, say,
// -multi-tenant or -retention-days existed. The chapter is written from the flag set itself, with the console setting each
// flag seeds, so it can neither describe a flag that is gone nor miss one that was added.
//
// PERFUSE_UPDATE_MANUAL=1 go test ./cmd/perfuse -run ServerOptions rewrites it.

const serverOptionsPath = "../../internal/manual/chapters/server-options.md"

type serveFlag struct{ name, kind, def, help string }

// serveFlags reads every flag serve defines, through its own usage output.
func serveFlags(t *testing.T) []serveFlag {
	t.Helper()
	var usage bytes.Buffer
	if err := cmdServe([]string{"-h"}, &usage, &usage); err != nil && err != flag.ErrHelp && !strings.Contains(err.Error(), "help") {
		t.Fatalf("serve -h: %v", err)
	}
	// "  -name type\n    \thelp (default x)" per flag, help possibly over several lines.
	head := regexp.MustCompile(`^  -([a-z0-9-]+)(?: (\S+))?$`)
	var out []serveFlag
	for _, line := range strings.Split(usage.String(), "\n") {
		if m := head.FindStringSubmatch(line); m != nil {
			out = append(out, serveFlag{name: m[1], kind: m[2]})
			continue
		}
		if len(out) > 0 && strings.HasPrefix(line, "    \t") {
			f := &out[len(out)-1]
			if f.help != "" {
				f.help += " "
			}
			f.help += strings.TrimPrefix(line, "    \t")
		}
	}
	if len(out) < 50 {
		t.Fatalf("read %d flags from serve -h; the usage format changed", len(out))
	}
	def := regexp.MustCompile(` \(default (.+)\)$`)
	for i := range out {
		if m := def.FindStringSubmatchIndex(out[i].help); m != nil {
			out[i].def = out[i].help[m[2]:m[3]]
			out[i].help = out[i].help[:m[0]]
		}
		if out[i].kind == "" {
			out[i].kind = "on/off"
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func renderServerOptions(t *testing.T) string {
	consoleOf := map[string]string{}
	for _, s := range settings.Default() {
		if s.Flag != "" {
			where := string(s.Group)
			if s.Subgroup != "" {
				where += " › " + s.Subgroup
			}
			consoleOf[s.Flag] = "**Settings › " + where + "**: " + s.Label
		}
	}
	cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }
	var b strings.Builder
	b.WriteString("# Server Options\n\n")
	b.WriteString("Every option `perfuse serve` takes, generated from the program itself, so this list cannot name an option that does not " +
		"exist or leave one out. Most are also a setting in the web interface, named in the last column: a value saved there wins over " +
		"the flag at the next start, and the flag remains the way to set it from a service definition. The listen address, the " +
		"database and the channel directory stay flags only, because they say where the server finds what it would be editing.\n\n")
	b.WriteString("Secrets are never flags. An option that needs a password or a token names an environment variable that holds it.\n\n")
	b.WriteString("## All options\n\n")
	b.WriteString("| Option | Takes | Default | What it does | In the console |\n|---|---|---|---|---|\n")
	for _, f := range serveFlags(t) {
		def := f.def
		if def == "" {
			def = "—"
		} else {
			def = "`" + strings.Trim(def, `"`) + "`"
		}
		console := consoleOf[f.name]
		if console == "" {
			console = "—"
		}
		b.WriteString("| `-" + f.name + "` | " + f.kind + " | " + cell(def) + " | " + cell(f.help) + " | " + cell(console) + " |\n")
	}
	return b.String()
}

func TestTheServerOptionsChapterIsCurrent(t *testing.T) {
	want := renderServerOptions(t)
	if os.Getenv("PERFUSE_UPDATE_MANUAL") == "1" {
		if err := os.WriteFile(serverOptionsPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(serverOptionsPath)
	if err != nil || string(got) != want {
		t.Fatalf("internal/manual/chapters/server-options.md is not what serve's flags say; " +
			"run PERFUSE_UPDATE_MANUAL=1 go test ./cmd/perfuse -run ServerOptions")
	}
}
