package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
)

// TestReplaySaysWhichReasonThereIsNothingToCompare covers two situations that were reported identically.
//
// A channel that has never received anything needs somebody to look at the feed. A channel with records whose bodies were not
// kept needs somebody to look at a setting. Both got "no messages have been recorded for this channel yet", which sends the
// second person to check a feed that is working perfectly.
func TestReplaySaysWhichReasonThereIsNothingToCompare(t *testing.T) {
	h := newMessageHarness(t)

	yaml := "name: adt\nsource:\n  type: mllp\n  listen: 127.0.0.1:0\ndestinations:\n" +
		"  - name: out\n    type: file\n    dir: /tmp\n"

	if _, err := h.server.Channels.Create([]byte(yaml)); err != nil {
		t.Fatal(err)
	}

	t.Run("nothing has arrived", func(t *testing.T) {
		rec := h.do("editor", http.MethodPost, "/api/channels/adt/replay",
			map[string]any{"yaml": yaml, "limit": 10})

		if rec.Code != http.StatusConflict {
			t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no messages have been recorded") {
			t.Errorf("the message does not say the channel has received nothing:\n%s", rec.Body.String())
		}
	})

	t.Run("messages arrived but their bodies were not kept", func(t *testing.T) {
		// Recorded without a body, which is what happens with payload storage off.
		if _, err := h.server.Runtime.Messages.Record(t.Context(), &msgstore.Message{
			Channel:    "adt",
			ReceivedAt: time.Now().UTC(),
			Outcome:    msgstore.Delivered,
		}); err != nil {
			t.Fatal(err)
		}

		rec := h.do("editor", http.MethodPost, "/api/channels/adt/replay",
			map[string]any{"yaml": yaml, "limit": 10})

		if rec.Code != http.StatusConflict {
			t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if strings.Contains(body, "no messages have been recorded") {
			t.Errorf("a channel with records whose bodies were not kept is reported as having received "+
				"nothing, which sends somebody to check a feed that is working:\n%s", body)
		}
		// And it names the setting, because that is the fix.
		if !strings.Contains(body, "Keep message contents") {
			t.Errorf("the message does not name the setting to change:\n%s", body)
		}
	})
}
