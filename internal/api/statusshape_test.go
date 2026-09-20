package api

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// readSource reads a file from this package, for the structural checks below.
func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// Every transport that sends a status payload must send the same fields.
//
// The dashboard's first tile read "undefined/undefined" for as long as a page stayed open. The REST handler included channelsTotal
// and channelRunning; the two server-sent-event writers built their own map and did not. The dashboard reads both into one variable,
// so the tile was correct for two seconds - until the first event arrived and replaced the state with a shape missing those fields.
//
// The interface cast the event payload to the shared response type on arrival, so TypeScript raised nothing. A cast is an assertion
// that something has a shape, not a check that it does, and this one was wrong.
//
// This test pins the fields rather than comparing two code paths, because there is now only one path. If a field is added to
// statusBody deliberately, this list is the place to say so - and the failure is the reminder that the interface reads it.
func TestTheStatusPayloadCarriesEveryFieldTheDashboardReads(t *testing.T) {
	// What web/src/Dashboard.tsx and web/src/api.ts read off a status payload. Missing any of these renders as undefined rather
	// than as an error, which is why a test is the only thing that notices.
	required := []string{
		"channels",
		"channelsTotal",
		"channelRunning",
		"time",
	}

	// A real runtime over an empty directory. The zero value is not usable - States() dereferences the channel repository - and an
	// empty install is the case that produced the bug anyway, because that is when the counts are zero rather than absent.
	repo, err := NewChannelRepo(t.TempDir())
	if err != nil {
		t.Fatalf("could not create a channel repository: %v", err)
	}

	body := statusBody(NewRuntime(repo, nil, nil))

	// Marshalled and re-read, so this checks what actually goes over the wire rather than what is in the map.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("the status payload does not marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the status payload does not round trip: %v", err)
	}

	var missing []string
	for _, field := range required {
		if _, ok := out[field]; !ok {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the status payload is missing %s.\n\n"+
			"The dashboard reads these and renders whatever it finds. A missing number becomes the word \"undefined\" on the "+
			"landing page rather than an error anywhere, which is exactly how this went unnoticed.",
			strings.Join(missing, ", "))
	}

	// An empty runtime must still report zero rather than omitting the counts. Omitting them is what produced the original bug,
	// and a fresh install is the case most likely to hit it.
	for _, field := range []string{"channelsTotal", "channelRunning"} {
		v, ok := out[field]
		if !ok {
			continue
		}
		if v == nil {
			t.Errorf("%s is null on an empty runtime; it must be 0, because the interface prints it directly", field)
		}
	}
}

// TestEveryStatusSenderUsesTheBuilder is the structural half.
//
// The field list above only helps if every sender goes through statusBody. Three did not, which is how they drifted, so this reads
// the source for a status send that builds its own map.
func TestEveryStatusSenderUsesTheBuilder(t *testing.T) {
	body, err := readSource("runtime.go")
	if err != nil {
		t.Fatalf("could not read runtime.go: %v", err)
	}

	lines := strings.Split(body, "\n")

	for i, line := range lines {
		if !strings.Contains(line, `send("status"`) && !strings.Contains(line, `"channelRunning"`) {
			continue
		}

		// A send that passes statusBody is correct. One that opens a map literal is building its own copy.
		if strings.Contains(line, `send("status"`) && !strings.Contains(line, "statusBody") {
			t.Errorf("runtime.go:%d sends a status event without statusBody: %s\n"+
				"Every sender must share one builder, or a field added for one transport goes missing from the other - "+
				"which is what put the word \"undefined\" on the dashboard.", i+1, strings.TrimSpace(line))
		}

		// channelRunning should be spelled in exactly one place now.
		if strings.Contains(line, `"channelRunning"`) {
			inBuilder := false
			for back := i; back >= 0 && back > i-30; back-- {
				if strings.Contains(lines[back], "func statusBody(") {
					inBuilder = true

					break
				}
				if strings.HasPrefix(lines[back], "func ") {
					break
				}
			}
			if !inBuilder {
				t.Errorf("runtime.go:%d names channelRunning outside statusBody: %s",
					i+1, strings.TrimSpace(line))
			}
		}
	}
}
