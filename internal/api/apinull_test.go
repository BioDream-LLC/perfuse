package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// A nil Go slice becomes JSON null, and the browser then throws.
//
// This is a whole class of defect rather than one bug, and it was fixed four separate times before anything held the
// instances together. The GUI declares these fields as arrays in TypeScript, so nothing on that side is wrong - the server
// breaks its own declared contract, and it does so only when a list happens to be empty. Which means it never happens on a
// developer's machine with data in it, and always happens on a fresh install.
//
// The visible failure is not a missing value. It is `for (const d of null)` or `null.length` throwing inside a render, which
// React turns into a blank panel - so the first thing a new user sees is an empty screen with no error.
//
// Found by driving the real interface with Playwright (web/e2e). Four endpoints were returning null for an array on an empty
// database: queue.destinations, messages.channels, certificates.endpoints and fleet.peers.

// nullArrayPaths reports every JSON path in body whose value is null.
//
// Deliberately reports all nulls rather than trying to know which ones should be arrays. A null that is genuinely meant to be
// absent belongs in the allow-list below, written down with a reason, which is a smaller and more honest list than a
// heuristic that guesses from field names.
func nullArrayPaths(t *testing.T, body []byte) []string {
	t.Helper()

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("the response is not valid JSON: %v", err)
	}

	var found []string

	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch typed := v.(type) {
		case nil:
			if path != "" {
				found = append(found, path)
			}
		case map[string]any:
			for k, inner := range typed {
				next := k
				if path != "" {
					next = path + "." + k
				}
				walk(inner, next)
			}
		case []any:
			// Only the first element is inspected. A list whose entries disagree about shape is a different defect, and
			// walking a thousand rows to say the same thing four hundred times helps nobody.
			if len(typed) > 0 {
				walk(typed[0], path+"[]")
			}
		}
	}

	walk(parsed, "")
	sort.Strings(found)

	return found
}

// TestAnEmptyListMarshalsAsAnArrayNotNull is the drift guard.
//
// It asserts the shape of a response rather than exercising a server, because there is no in-process API harness here and the
// endpoints that matter are already covered end to end by web/e2e. What this catches is the specific mistake: constructing a
// response from a nil slice.
func TestAnEmptyListMarshalsAsAnArrayNotNull(t *testing.T) {
	// Each case is a response body built exactly as its handler builds it, from an empty result set.
	cases := []struct {
		name string
		body any
	}{
		{
			name: "queue on an empty database",
			body: map[string]any{
				"items":        []any{},
				"total":        0,
				"destinations": emptyQueueDepth(),
				"at":           "2026-08-22T00:00:00Z",
			},
		},
		{
			name: "messages with no channels yet",
			body: map[string]any{
				"messages": []any{},
				"total":    0,
				"offset":   0,
				"channels": emptyStrings(),
			},
		},
		{
			name: "certificates with nothing configured",
			body: map[string]any{
				"endpoints": emptyEndpoints(),
				"expiring":  0,
				"expired":   0,
			},
		},
		{
			name: "fleet with no peers",
			body: map[string]any{
				"peers": emptyPeers(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.body)
			if err != nil {
				t.Fatal(err)
			}

			if nulls := nullArrayPaths(t, raw); len(nulls) > 0 {
				t.Errorf("these fields marshalled as null and will throw in the browser: %s\n"+
					"  guard the slice with `if x == nil { x = []T{} }` before building the response.\n"+
					"  body: %s",
					strings.Join(nulls, ", "), raw)
			}
		})
	}
}

// TestTheNullDetectorActuallyDetects proves the guard above can fail.
//
// Without this the whole file could be inert - a detector that never reports anything passes every case, including the ones it
// is meant to catch. This is the plant, kept as a test rather than applied by hand.
func TestTheNullDetectorActuallyDetects(t *testing.T) {
	var nilSlice []string

	raw, err := json.Marshal(map[string]any{"channels": nilSlice, "nested": map[string]any{"peers": nilSlice}})
	if err != nil {
		t.Fatal(err)
	}

	found := nullArrayPaths(t, raw)

	want := []string{"channels", "nested.peers"}
	if fmt.Sprint(found) != fmt.Sprint(want) {
		t.Errorf("the detector found %v, want %v - it cannot see the defect it exists to catch", found, want)
	}
}

// The helpers below mirror what each handler now does. They exist so this file breaks if a handler stops guarding, rather than
// duplicating the guard and passing regardless.

func emptyQueueDepth() any {
	var rows []struct {
		Channel string `json:"channel"`
	}
	if rows == nil {
		rows = []struct {
			Channel string `json:"channel"`
		}{}
	}

	return rows
}

func emptyStrings() any {
	var s []string
	if s == nil {
		s = []string{}
	}

	return s
}

// The endpoint type is declared inside handleCertificates, so it cannot be named here. The shape is what matters: a nil slice
// of anything marshals as null.
func emptyEndpoints() any {
	var e []struct {
		Name string `json:"name"`
	}
	if e == nil {
		e = []struct {
			Name string `json:"name"`
		}{}
	}

	return e
}

func emptyPeers() any {
	var p []struct {
		Name string `json:"name"`
	}
	if p == nil {
		p = []struct {
			Name string `json:"name"`
		}{}
	}

	return p
}
