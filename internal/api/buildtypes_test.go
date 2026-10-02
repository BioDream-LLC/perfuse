package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Guards that the browser offers exactly the transports the server accepts.
//
// The existing field guard cannot see this and never could. It walks the Go build model's paths and asks whether the browser mentions
// each one, so a transport absent from the browser's type union looks like a single missing field rather than a transport nobody can
// choose. A transport the browser offers and the server has never heard of is invisible to it entirely, because there is no model path
// to walk.
//
// Both directions have already happened. A broker destination was in the Go model and missing from the browser's labels, so it was
// never offered; a whole queue block could not be written; and this guard's first run found two more - see below. Every one was found
// by hand, which is the argument for the guard.
//
// The two directions fail differently and both are bad:
//
//   - In the server and not the browser: a transport nobody can choose. It is documented, it works, and the only way to use it is to
//     write YAML by hand - which is the situation the builder exists to end.
//   - In the browser and not the server: worse. Somebody picks it, fills in the form, saves, and the server refuses the whole channel.
//     Not the destination - the channel, because a channel is refused wholesale when anything in it is broken. It reads as the builder
//     being broken rather than as one option that should never have been on the list.

// goTypeConstants extracts the string values of a typed constant group from a Go file.
//
// Read from the source rather than by importing the package and reflecting, because there is nothing to reflect over: these are
// untyped-ish string constants, and a list of them written out here by hand is exactly the stale copy this guard is meant to replace.
func goTypeConstants(t *testing.T, path, typeName string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// e.g. `DestinationMLLP DestinationType = "mllp"`
	pattern := regexp.MustCompile(`\b\w+\s+` + regexp.QuoteMeta(typeName) + `\s*=\s*"([^"]+)"`)

	var out []string

	for _, m := range pattern.FindAllStringSubmatch(string(data), -1) {
		out = append(out, m[1])
	}

	sort.Strings(out)

	// A positive control. If the pattern stops matching - a rename, a reformat, a move to another file - every comparison below
	// passes while reading nothing, which is how a guard comes to report agreement it never checked.
	if len(out) < 5 {
		t.Fatalf("found only %d %s constants in %s, so the pattern is not reading the real declarations", len(out), typeName, path)
	}

	return out
}

// tsUnionMembers extracts the string members of an exported TypeScript union type.
func tsUnionMembers(t *testing.T, path, typeName string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	start := strings.Index(text, "export type "+typeName+" =")
	if start < 0 {
		t.Fatalf("no exported type %s in %s", typeName, path)
	}

	// The union runs to the first blank line, since these are written one member per line.
	end := strings.Index(text[start:], "\n\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", typeName)
	}

	member := regexp.MustCompile(`'([^']+)'`)

	var out []string

	for _, m := range member.FindAllStringSubmatch(text[start:start+end], -1) {
		out = append(out, m[1])
	}

	sort.Strings(out)

	if len(out) < 5 {
		t.Fatalf("found only %d members of %s, so the pattern is not reading the real union", len(out), typeName)
	}

	return out
}

// knownTypeGaps records a transport deliberately absent from one side, with the reason.
//
// A two-way ratchet like the field guard's: an unlisted gap fails, and a listed gap that no longer exists also fails. The second half
// matters more than it sounds. An excuse outlives the thing it excused, and then it reads as a decision somebody made rather than a
// note nobody removed - which has already happened twice in this codebase, both times removing the check that would have found a real
// gap.
var knownTypeGaps = map[string]string{}

func TestTheBuilderOffersEveryDestinationTheServerAccepts(t *testing.T) {
	inServer := goTypeConstants(t, "../config/config.go", "DestinationType")
	inBrowser := tsUnionMembers(t, "../../web/src/model.ts", "DestinationType")

	compareTransports(t, "destination", inServer, inBrowser)
}

func TestTheBuilderOffersEverySourceTheServerAccepts(t *testing.T) {
	inServer := goTypeConstants(t, "../config/config.go", "SourceType")
	// Named SourceKind in the browser and SourceType in Go, which is why this pairing has to be written down rather than derived.
	inBrowser := tsUnionMembers(t, "../../web/src/model.ts", "SourceKind")

	compareTransports(t, "source", inServer, inBrowser)
}

func compareTransports(t *testing.T, what string, inServer, inBrowser []string) {
	t.Helper()

	server := map[string]bool{}
	for _, s := range inServer {
		server[s] = true
	}

	browser := map[string]bool{}
	for _, s := range inBrowser {
		browser[s] = true
	}

	used := map[string]bool{}

	for _, name := range inServer {
		if browser[name] {
			continue
		}

		key := what + "." + name
		if reason, ok := knownTypeGaps[key]; ok {
			used[key] = true
			t.Logf("%s is not offered in the browser, deliberately: %s", key, reason)

			continue
		}

		t.Errorf("the server accepts a %s of type %q and the browser never offers it, so it can only be used by writing YAML "+
			"by hand", what, name)
	}

	for _, name := range inBrowser {
		if server[name] {
			continue
		}

		key := what + "." + name
		if reason, ok := knownTypeGaps[key]; ok {
			used[key] = true
			t.Logf("%s is offered without server support, deliberately: %s", key, reason)

			continue
		}

		t.Errorf("the browser offers a %s of type %q and the server has never heard of it, so choosing it produces a channel the "+
			"server refuses outright - and it refuses the whole channel, not just the destination", what, name)
	}

	for key, reason := range knownTypeGaps {
		if !strings.HasPrefix(key, what+".") {
			continue
		}

		if !used[key] {
			t.Errorf("knownTypeGaps still excuses %q (%s) and there is no longer a gap there: an excuse that outlives its "+
				"reason reads as a decision somebody made", key, reason)
		}
	}
}

func TestARawSocketDestinationBuildsAChannelTheServerAccepts(t *testing.T) {
	// The transport this guard found missing, proven end to end rather than merely listed.
	//
	// It was in the server's model with a validator, a framing block and its own tests, and the builder never offered it - so the only
	// way to send to a laboratory instrument was to write YAML by hand. That is the situation the builder exists to end, and it had
	// gone unnoticed because the field guard cannot see a missing transport: it walks model paths and asks whether the browser mentions
	// each, and a transport nobody can choose has no path to walk.
	h := newHarness(t)

	res := h.do("editor", http.MethodPost, "/api/channels/build", map[string]any{
		"name":   "tcp-out",
		"source": map[string]any{"type": "mllp", "listen": ":7300"},
		"destinations": []any{
			map[string]any{
				"name": "instrument",
				"type": "tcp",
				"tcp": map[string]any{
					"address":     "instrument.lab:9100",
					"framing":     "delimited",
					"delimiter":   `\r`,
					"expectReply": true,
					"timeout":     "30s",
				},
			},
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	var out struct {
		YAML     string   `json:"yaml"`
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if !out.OK {
		t.Fatalf("the channel is not valid: %v", out.Problems)
	}

	// Asserted on the generated file rather than on the response being 200, because a setting the form accepts and the file does not
	// carry is the exact failure this codebase keeps finding. Each of these was typed into a control.
	for _, want := range []string{
		"type: tcp",
		"address: instrument.lab:9100",
		"framing: delimited",
		`delimiter: \r`,
		"expect_reply: true",
		"timeout: 30s",
	} {
		if !strings.Contains(out.YAML, want) {
			t.Errorf("the generated file is missing %q:\n%s", want, out.YAML)
		}
	}

	// Nested under tcp:, not flat. Writing these at the destination's top level is a mistake already made once in this file's history
	// - the form accepted the values and the file never carried them.
	if !strings.Contains(out.YAML, "    tcp:\n") {
		t.Errorf("the socket settings are not under a tcp block:\n%s", out.YAML)
	}
}
