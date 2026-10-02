package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixturePath = "../../internal/mirth/testdata/adt_channel.xml"

// quietTestLogger keeps server logging out of test output without disabling the
// logging code paths.
func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestExplainText(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"explain", fixturePath}, &out, &errOut); err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, errOut.String())
	}
	got := out.String()

	for _, want := range []string{"CHANNEL", "FLOW", "FINDINGS", "BLOCKER"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}

func TestExplainJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"explain", "-json", fixturePath}, &out, &errOut); err != nil {
		t.Fatalf("run: %v", err)
	}

	var reports []jsonReport
	if err := json.Unmarshal(out.Bytes(), &reports); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}

	r := reports[0]
	if r.Channel != "ADT Inbound - Synthetic Site" {
		t.Errorf("channel = %q", r.Channel)
	}
	if r.Translatable {
		t.Error("translatable = true, want false")
	}
	if r.Blockers != 2 {
		t.Errorf("blockers = %d, want 2", r.Blockers)
	}
	if len(r.Destinations) != 2 {
		t.Errorf("destinations = %v, want 2 entries", r.Destinations)
	}

	// Every finding needs a stable code so downstream tooling can match on it
	// without depending on the prose.
	for i, f := range r.Findings {
		if f.Code == "" {
			t.Errorf("finding %d has no code: %+v", i, f)
		}
		if f.Severity == "" {
			t.Errorf("finding %d has no severity: %+v", i, f)
		}
	}
}

func TestStrictSignalsBlockers(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"explain", "-strict", "-quiet", fixturePath}, &out, &errOut)
	if !errors.Is(err, errBlocking) {
		t.Fatalf("err = %v, want errBlocking", err)
	}
	// Even when signalling failure the summary must still be printed.
	if !strings.Contains(out.String(), "blocked") {
		t.Errorf("summary missing from quiet output:\n%s", out.String())
	}
}

func TestStrictPassesCleanChannel(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.xml")
	body := `<channel version="4.4.0">
	  <name>clean</name>
	  <enabled>true</enabled>
	  <sourceConnector>
	    <transportName>TCP Listener</transportName>
	    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties">
	      <listenerConnectorProperties><port>6000</port></listenerConnectorProperties>
	    </properties>
	    <transformer><elements/></transformer>
	    <filter><elements/></filter>
	  </sourceConnector>
	  <properties class="com.mirth.connect.model.ChannelProperties">
	    <messageStorageMode>PRODUCTION</messageStorageMode>
	    <pruneMetaDataDays>30</pruneMetaDataDays>
	    <pruneContentDays>30</pruneContentDays>
	  </properties>
	</channel>`
	if err := os.WriteFile(clean, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"explain", "-strict", clean}, &out, &errOut); err != nil {
		t.Fatalf("run on a clean channel returned %v", err)
	}
}

func TestDirectoryIsWalked(t *testing.T) {
	dir := t.TempDir()
	// Two copies of the fixture plus a file that is not XML.
	src, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.xml", "nested/b.xml"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, src, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := collectXML([]string{dir})
	if err != nil {
		t.Fatalf("collectXML: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("collectXML found %v, want 2 xml files", files)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"explain", "-quiet", dir}, &out, &errOut); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "2 channel(s) read") {
		t.Errorf("summary did not report both channels:\n%s", out.String())
	}
}

func TestUnreadableFileDoesNotStopTheRun(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.xml"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	// A channel group export, not a channel: readable XML, wrong root element.
	if err := os.WriteFile(filepath.Join(dir, "bad.xml"),
		[]byte(`<channelGroup><name>group</name></channelGroup>`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"explain", "-quiet", dir}, &out, &errOut); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "1 channel(s) read, 1 unreadable") {
		t.Errorf("summary did not account for the unreadable file:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "skipped") {
		t.Errorf("stderr did not name the skipped file:\n%s", errOut.String())
	}
}

func TestNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(nil, &out, &errOut); !errors.Is(err, errNoArgs) {
		t.Fatalf("err = %v, want errNoArgs", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"frobnicate"}, &out, &errOut); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"version"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "perfuse ") {
		t.Errorf("version output = %q", out.String())
	}
}
