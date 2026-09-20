package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// Shared mapping tables were readable and not writable.
//
// The section reported which channels each table affected and said "No shared tables yet" on a fresh
// installation, with nothing to press. Creating one meant writing YAML on the server by hand, which is the thing
// this product exists to stop people doing. Found by the sweep reporting the section had no operable controls.
func TestWritingAMappingTable(t *testing.T) {
	h := newHarness(t)

	good := map[string]any{
		"name":      "sex-codes",
		"describes": "How this hospital's sex codes map to what the receiver expects",
		"entries": []map[string]any{
			{"from": "1", "to": "M", "why": "agreed with the lab in 2019"},
			{"from": "2", "to": "F"},
		},
		"decidedBy": "an integration analyst",
	}

	t.Run("a table can be created", func(t *testing.T) {
		res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", good)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", res.Code, res.Body.String())
		}
		// It must say that nothing uses it yet. Somebody who has just entered thirty mappings deserves to know
		// that before they go looking for the effect.
		if !strings.Contains(res.Body.String(), "Nothing uses this yet") {
			t.Errorf("the response does not say the table is unused: %s", res.Body.String())
		}
	})

	t.Run("it appears in the listing afterwards", func(t *testing.T) {
		res := h.do(string(store.RoleViewer), "GET", "/api/codesets", nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "sex-codes") {
			t.Errorf("the table just written is not listed: %s", res.Body.String())
		}
	})

	t.Run("replacing it says what it replaced", func(t *testing.T) {
		second := map[string]any{
			"name":      "sex-codes",
			"describes": "Revised after the receiver changed",
			"entries":   []map[string]any{{"from": "1", "to": "male"}},
		}
		res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", second)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Code, res.Body.String())
		}
		// Replacing a table silently would let somebody discard two years of decisions without noticing.
		if !strings.Contains(res.Body.String(), "Replaced") {
			t.Errorf("replacing a table did not say so: %s", res.Body.String())
		}
	})
}

// The refusals are the interesting part, because each one is a mistake that is silent at runtime.
func TestAMappingTableIsRefusedWhenItWouldMisbehaveQuietly(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "no name",
			body: map[string]any{"describes": "something", "entries": []map[string]any{{"from": "a", "to": "b"}}},
			want: "name",
		},
		{
			name: "no description",
			body: map[string]any{"name": "t", "entries": []map[string]any{{"from": "a", "to": "b"}}},
			want: "what this table is for",
		},
		{
			name: "no entries",
			body: map[string]any{"name": "t", "describes": "something"},
			want: "maps nothing",
		},
		{
			// The one that actually bites. Whichever duplicate wins is an implementation detail, and the value
			// that loses looks like it was never mapped at all - which is debugged as a missing mapping.
			name: "the same value mapped twice",
			body: map[string]any{
				"name":      "t",
				"describes": "something",
				"entries": []map[string]any{
					{"from": "1", "to": "M"},
					{"from": "1", "to": "F"},
				},
			},
			want: "twice",
		},
		{
			name: "an entry with nothing to map from",
			body: map[string]any{
				"name":      "t",
				"describes": "something",
				"entries":   []map[string]any{{"from": "", "to": "M"}},
			},
			want: "nothing to map from",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := h.do(string(store.RoleEditor), "PUT", "/api/codesets/table", c.body)

			if res.Code == http.StatusOK {
				t.Fatalf("a table with %s was accepted", c.name)
			}
			if res.Code == http.StatusInternalServerError {
				t.Fatalf("status = 500; bad input deserves a refusal that explains: %s", res.Body.String())
			}
			if !strings.Contains(strings.ToLower(res.Body.String()), c.want) {
				t.Errorf("the refusal does not mention %q: %s", c.want, res.Body.String())
			}
		})
	}
}

// A viewer must not be able to change what every channel maps through.
func TestWritingAMappingTableNeedsMoreThanReadAccess(t *testing.T) {
	h := newHarness(t)

	res := h.do(string(store.RoleViewer), "PUT", "/api/codesets/table", map[string]any{
		"name":      "t",
		"describes": "something",
		"entries":   []map[string]any{{"from": "a", "to": "b"}},
	})
	if res.Code != http.StatusForbidden {
		t.Errorf("a viewer got %d writing a mapping table, want 403", res.Code)
	}
}

// A tables file that cannot be parsed must not be overwritten.
//
// Somebody's hand-written file that this cannot read is still their file, and replacing it with one table would
// destroy the rest of it.
func TestAnUnreadableTablesFileIsNotOverwritten(t *testing.T) {
	h := newHarness(t)

	if err := h.server.Channels.WriteTables("tables.yaml", []byte("tables:\n  - name: fine\n    entries: []\n")); err != nil {
		t.Fatal(err)
	}
	// Now corrupt it the way a hand edit would. Written directly rather than through WriteTables, which would
	// refuse it - that refusal is a separate guard and this test is about the read side.
	if err := os.WriteFile(filepath.Join(h.dir, "tables.yaml"),
		[]byte("tables:\n\t- broken: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := h.server.Channels.ReadTables("tables.yaml"); err == nil {
		t.Fatal("an unparseable tables file was read as if it were fine, so writing would have replaced it")
	}
}
