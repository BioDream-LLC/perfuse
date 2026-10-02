package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The question this endpoint answers is "of the channels we actually run, how many work" - so the tests
// are about whether the answer is honest and readable, not about the translation itself, which
// internal/translate already tests.

// mirthChannel builds a minimal but real Mirth channel export.
func mirthChannel(name string) string {
	return `<channel version="4.5.2">
  <id>` + name + `-id</id>
  <name>` + name + `</name>
  <description>a channel</description>
  <enabled>true</enabled>
  <sourceConnector version="4.5.2">
    <name>sourceConnector</name>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties" version="4.5.2">
      <listenerConnectorProperties version="4.5.2">
        <host>0.0.0.0</host>
        <port>6661</port>
      </listenerConnectorProperties>
    </properties>
    <transformer version="4.5.2">
      <elements/>
    </transformer>
    <filter version="4.5.2">
      <elements/>
    </filter>
    <mode>SOURCE</mode>
    <enabled>true</enabled>
  </sourceConnector>
  <destinationConnectors>
    <connector version="4.5.2">
      <name>to the lab</name>
      <properties class="com.mirth.connect.connectors.tcp.TcpDispatcherProperties" version="4.5.2">
        <remoteAddress>10.0.0.5</remoteAddress>
        <remotePort>7001</remotePort>
      </properties>
      <transformer version="4.5.2">
        <elements/>
      </transformer>
      <filter version="4.5.2">
        <elements/>
      </filter>
      <mode>DESTINATION</mode>
      <enabled>true</enabled>
    </connector>
  </destinationConnectors>
</channel>`
}

func TestImportingOneMirthChannelReportsIt(t *testing.T) {
	h := newHarness(t)

	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: mirthChannel("adt-feed")})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeBody[importResponse](t, rec)

	if resp.Summary.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Summary.Total)
	}
	if len(resp.Channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(resp.Channels))
	}
	if resp.Channels[0].SourceName != "adt-feed" {
		t.Errorf("source name = %q, want adt-feed", resp.Channels[0].SourceName)
	}
	if resp.Channels[0].YAML == "" {
		t.Error("no YAML was produced, so there is nothing to import")
	}
}

func TestAWholeServerExportIsSplitIntoChannels(t *testing.T) {
	// Asking somebody to split a server export by hand before they can see whether Perfuse is worth
	// trying would lose most of the people who were willing to look.
	xml := `<list>` + mirthChannel("adt-feed") + mirthChannel("lab-results") +
		mirthChannel("orders") + `</list>`

	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: xml})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeBody[importResponse](t, rec)

	if resp.Summary.Total != 3 {
		t.Fatalf("total = %d, want 3: the server export was not split", resp.Summary.Total)
	}

	names := map[string]bool{}
	for _, c := range resp.Channels {
		names[c.SourceName] = true
	}
	for _, want := range []string{"adt-feed", "lab-results", "orders"} {
		if !names[want] {
			t.Errorf("channel %q was lost in the split", want)
		}
	}
}

func TestChannelsNeedingAttentionComeFirst(t *testing.T) {
	// A list of forty channels where thirty-seven are clean should open on the three that are not.
	// Those three are the entire remaining work.
	broken := strings.Replace(mirthChannel("needs-work"),
		`<properties class="com.mirth.connect.connectors.tcp.TcpDispatcherProperties" version="4.5.2">
        <remoteAddress>10.0.0.5</remoteAddress>
        <remotePort>7001</remotePort>
      </properties>`,
		`<properties class="com.mirth.connect.connectors.dimse.DICOMDispatcherProperties" version="4.5.2">
        <host>10.0.0.9</host>
      </properties>`, 1)

	xml := `<list>` + mirthChannel("aaa-clean") + broken + `</list>`

	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: xml})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeBody[importResponse](t, rec)
	if len(resp.Channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(resp.Channels))
	}

	// The one with something wrong is first, even though its name sorts last.
	first := resp.Channels[0]
	if first.Counts.Blockers == 0 && first.Counts.Warnings == 0 {
		t.Errorf("a clean channel sorted first; the one needing attention was buried: %+v",
			resp.Channels)
	}
}

func TestNoChannelInTheXMLIsAnErrorNotAnEmptySuccess(t *testing.T) {
	// "0 channels, all clean" would be a lie of exactly the kind this codebase avoids.
	h := newHarness(t)

	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import",
		importRequest{XML: `<somethingElse><name>not a channel</name></somethingElse>`})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel") {
		t.Errorf("the error does not say what was missing: %s", rec.Body.String())
	}
}

func TestEmptyXMLSaysWhatToPaste(t *testing.T) {
	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: "   "})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "export") {
		t.Errorf("the error does not say what to paste: %s", rec.Body.String())
	}
}

func TestABrokenChannelInAServerExportDoesNotLoseTheGoodOnes(t *testing.T) {
	// One malformed channel in a forty-channel export must not cost the other thirty-nine.
	xml := `<list>` + mirthChannel("good-one") +
		`<channel><name>truncated-one</name><sourceConnector` + `</list>`

	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: xml})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeBody[importResponse](t, rec)

	if resp.Summary.Total != 1 {
		t.Errorf("total = %d, want 1 good channel", resp.Summary.Total)
	}
	if len(resp.Failed) == 0 {
		t.Error("the broken channel vanished without comment")
	}
	// And it is named, because "document 2 of 2" is useless to somebody looking at their own channels.
	if !strings.Contains(strings.Join(resp.Failed, " "), "truncated-one") {
		t.Errorf("the failure does not name the channel: %v", resp.Failed)
	}
}

func TestFailedIsAnArrayNotNull(t *testing.T) {
	// A nil slice marshals as null and the front end calls .length on it. This exact mistake shipped
	// once already in the content search.
	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: mirthChannel("fine")})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"failed":[]`) {
		t.Errorf("failed is not an empty array: %s", rec.Body.String())
	}
}

func TestAViewerCannotImport(t *testing.T) {
	h := newHarness(t)
	rec := h.do(string(store.RoleViewer), "POST", "/api/mirth/import", importRequest{XML: mirthChannel("adt")})
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", rec.Code)
	}
}

func TestTheSummaryReportsHowMuchStayedAsScript(t *testing.T) {
	// The ratio of declarative to scripted is the honest measure of what a migration gained. Hiding it
	// would be flattering rather than useful.
	h := newHarness(t)
	rec := h.do(string(store.RoleEditor), "POST", "/api/mirth/import", importRequest{XML: mirthChannel("adt")})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "declarativeSteps") || !strings.Contains(body, "scriptedSteps") {
		t.Errorf("the summary omits the declarative/scripted split: %s", body)
	}
}
