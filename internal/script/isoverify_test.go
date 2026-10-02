package script

import (
	"strings"
	"testing"
)

// A pooled runtime that is not fully reset lets one message's data appear in the next. For a reader
// running the same script on a timer that means one patient's data surfacing in another's poll.
//
// Assignment without var creates a property on the global object rather than a local, which is the
// path that was leaking: clearing the named bindings the engine installs does not remove it.
func TestNoGlobalLeaksBetweenRuns(t *testing.T) {
	e := New(Options{})

	leak, err := e.Compile("leak", `leakedPatient = "SSN-111-22-3333"; implicitGlobal = 42; return "ok";`, Preprocessor)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := e.Compile("probe", `return typeof leakedPatient + "/" + typeof implicitGlobal;`, Preprocessor)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.Run(leak, &Context{Log: func(string, string) {}}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	res, err := e.Run(probe, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if got := strings.TrimSpace(res.Text); got != "undefined/undefined" {
		t.Errorf("state leaked across runs: %q, want undefined/undefined", got)
	}
}
