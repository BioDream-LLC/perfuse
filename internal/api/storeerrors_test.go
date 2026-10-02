package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every error the store defines is either mapped to a status by failErr or deliberately handled where it arises.
//
// failErr turns anything it does not recognise into 500 "something went wrong". The store defines its errors one by
// one, so each new one is a way for that list to fall behind: ErrTokenNotFound, ErrTenantNotFound and
// ErrChallengeNotFound all had, and revoking a token that does not exist answered 500. This reads both and fails on
// the gap.
func TestEveryStoreErrorIsMapped(t *testing.T) {
	// Raised only by the sign-in paths, which answer them on the spot rather than through failErr - and must, because
	// mapping "wrong password" and "account disabled" to distinct statuses here would tell an attacker which it was.
	handledWhereRaised := map[string]bool{
		"ErrInvalidCredentials": true,
		"ErrDisabled":           true,
		"ErrNoExternalMatch":    true,
	}

	defined := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join("..", "store", "*.go"))
	re := regexp.MustCompile(`(?m)^\s*(?:var\s+)?(Err[A-Z]\w+)\s*=\s*errors\.New`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			defined[m[1]] = true
		}
	}
	if len(defined) < 5 {
		t.Fatalf("found only %d store errors; the pattern no longer matches how they are declared", len(defined))
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (s *Server) failErr(")
	if start < 0 {
		t.Fatal("failErr not found in server.go")
	}
	end := strings.Index(body[start:], "\n}\n")
	failErr := body[start : start+end]

	for name := range defined {
		if handledWhereRaised[name] {
			continue
		}
		if !strings.Contains(failErr, "store."+name) {
			t.Errorf("store.%s is not mapped in failErr, so it answers 500", name)
		}
	}
}
