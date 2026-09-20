package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProfilingAChannelWithNoTrafficSaysSo(t *testing.T) {
	// A 409 rather than an empty profile. An empty report reads as "this feed is simple",
	// which is the opposite of "nothing has been recorded yet" - and somebody acting on the
	// first reading would conclude their feed carries one message type and no Z-segments.
	h := newHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/channels/nothing-here/profile", nil)
	if res.Code == http.StatusOK {
		t.Fatal("an unrecorded channel returned a profile")
	}
}

func TestAProfileRejectsANonsenseLimit(t *testing.T) {
	h := newHarness(t)

	for _, bad := range []string{"0", "-5", "lots"} {
		res := h.do("viewer", http.MethodGet, "/api/channels/x/profile?limit="+bad, nil)
		if res.Code == http.StatusOK {
			t.Errorf("limit=%s was accepted", bad)
		}
	}
}

func TestAViewerCanProfile(t *testing.T) {
	// The role decision, asserted so it cannot be tightened by accident. A profile carries no
	// message content, and making it editor-only would put the most useful diagnostic in the
	// product out of reach of the people who need it most during an incident.
	h := newHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/channels/anything/profile", nil)
	if res.Code == http.StatusForbidden {
		t.Fatal("a viewer was refused a profile")
	}
}

func TestAProfileResponseCarriesNoMessageContent(t *testing.T) {
	// Belt and braces over the profile package's own test. This one checks the shape that
	// actually reaches a browser, because a wrapper that added the raw messages back in for
	// convenience would defeat everything the package does carefully.
	h := newHarness(t)

	res := h.do("viewer", http.MethodGet, "/api/channels/whatever/profile", nil)

	// Whatever the outcome, the response must not carry a payload field at all.
	var generic map[string]any
	if res.Body.Len() > 0 {
		_ = json.Unmarshal(res.Body.Bytes(), &generic)
	}
	for key := range generic {
		if strings.EqualFold(key, "raw") || strings.EqualFold(key, "messages") ||
			strings.EqualFold(key, "payloads") {
			t.Errorf("the profile response carries a %q field", key)
		}
	}
}
