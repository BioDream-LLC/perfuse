package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// A whole-server backup pasted into the migration view: written by BridgeLink itself (scripts/mirth-engine-corpus.sh), with three
// channels, a code template library and a channel group.
func TestAServerBackupImportsWithItsLibraryAndGroup(t *testing.T) {
	raw, err := os.ReadFile("../mirth/testdata/engines/bridgelink-26.9.0/server-configuration.xml")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: string(raw)})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeBody[importResponse](t, rec)
	if resp.Kind != "server backup" || resp.Version != "26.9.0" {
		t.Errorf("kind %q version %q", resp.Kind, resp.Version)
	}
	if len(resp.Channels) != 3 || resp.Summary.Blocked != 0 {
		t.Errorf("%d channels, %d blocked", len(resp.Channels), resp.Summary.Blocked)
	}
	if len(resp.Libraries) != 1 || resp.Libraries[0].File != "lib/site-helpers.js" || len(resp.Groups) != 1 {
		t.Fatalf("libraries %+v groups %+v", resp.Libraries, resp.Groups)
	}

	// The order the view uses: the library first, then the channel that includes it. Without the library the channel is refused.
	var adt string
	for _, c := range resp.Channels {
		if c.Name == "adt-inbound-from-ward" {
			adt = c.YAML
		}
	}
	if rec := h.do(string(store.RoleEditor), "POST", "/api/channels", channelPayload{YAML: adt}); rec.Code == http.StatusOK {
		t.Error("a channel including a library that is not there was created")
	}
	lib := resp.Libraries[0]
	if rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/libraries", libraryRequest{File: lib.File, Source: lib.Source}); rec.Code != http.StatusOK {
		t.Fatalf("writing the library: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(string(store.RoleEditor), "POST", "/api/channels", channelPayload{YAML: adt}); rec.Code != http.StatusOK {
		t.Fatalf("creating the channel after its library: %d %s", rec.Code, rec.Body.String())
	}
}

func TestALibraryCannotBeWrittenOutsideLib(t *testing.T) {
	h := newHarness(t)
	for _, file := range []string{"../escape.js", "lib/../adt.yaml", "adt.yaml", "lib/x.yaml", "lib/.hidden.js", "/etc/x.js", "lib/a/b.js"} {
		rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/libraries", libraryRequest{File: file, Source: "x"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q answered %d", file, rec.Code)
		}
	}
	if rec := h.do(string(store.RoleViewer), "POST", "/api/mirth/libraries", libraryRequest{File: "lib/a.js", Source: "x"}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer wrote a library: %d", rec.Code)
	}
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/libraries", libraryRequest{File: "lib/a.js", Source: "function a() {}"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "lib/a.js") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
