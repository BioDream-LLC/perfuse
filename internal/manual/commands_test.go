package manual

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every command the binary offers must be explained somewhere in the manual.
//
// The configuration keys have had this guarantee for a while and it works: 261 of them are documented because a test fails
// when one is not. The commands had no such guarantee, and the result was predictable - perfuse deident turns real traffic
// into a shareable corpus, which is the difference between being able to send somebody a reproduction and not, and the
// manual did not mention it at all.
//
// Mentioning a command is a low bar deliberately. This test is a net for things nobody wrote about, not a judgement of how
// well they were written about; the chapters are prose and prose cannot be measured by a test.
func TestEveryCommandIsMentionedInTheManual(t *testing.T) {
	usage, err := os.ReadFile(filepath.Join("..", "..", "cmd", "perfuse", "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	// The commands as the usage text advertises them, so this reads the same list a reader is shown. A separate test in
	// cmd/perfuse asserts that list matches what the binary actually dispatches, so neither can drift alone.
	found := regexp.MustCompile(`(?m)^  perfuse ([a-z-]+)`).FindAllStringSubmatch(string(usage), -1)

	commands := map[string]bool{}
	for _, m := range found {
		commands[m[1]] = true
	}

	// Asking for the usage is not a feature to document.
	delete(commands, "help")
	delete(commands, "version")

	if len(commands) < 10 {
		t.Fatalf("only found %d commands in the usage text, so this test is not reading it properly", len(commands))
	}

	doc, _ := buildForTest(t)
	prose := strings.ToLower(doc.HTML())

	var missing []string
	for name := range commands {
		// Either spelling counts: prose says "the deident command" as often as "perfuse deident".
		if !strings.Contains(prose, "perfuse "+name) && !strings.Contains(prose, name+" command") {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these commands ship but the manual never mentions them: %v", missing)
	}
}
