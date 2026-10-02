package analyze

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

func fixture(t *testing.T) *mirth.Channel {
	t.Helper()
	c, err := mirth.ParseChannelFile("../mirth/testdata/adt_channel.xml")
	if err != nil {
		t.Fatalf("ParseChannelFile: %v", err)
	}
	return c
}

// codes returns the set of finding codes in the report, for assertions that do
// not depend on prose or ordering.
func codes(r *Report) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range r.Findings {
		if _, seen := out[f.Code]; !seen {
			out[f.Code] = f
		}
	}
	return out
}

func TestFixtureBlockers(t *testing.T) {
	r := Channel(fixture(t))
	got := codes(r)

	// The fixture has an XSLT step and an unknown third-party plugin. Both stop
	// a mechanical translation and must be blockers.
	for _, want := range []string{"XSLT_STEP", "UNKNOWN_STEP_PLUGIN"} {
		f, ok := got[want]
		if !ok {
			t.Errorf("missing finding %s", want)
			continue
		}
		if f.Severity != Blocker {
			t.Errorf("%s severity = %q, want %q", want, f.Severity, Blocker)
		}
	}

	if r.Translatable() {
		t.Error("Translatable() = true, want false while blockers are present")
	}
}

func TestUnknownPluginIsNamed(t *testing.T) {
	f, ok := codes(Channel(fixture(t)))["UNKNOWN_STEP_PLUGIN"]
	if !ok {
		t.Fatal("missing UNKNOWN_STEP_PLUGIN")
	}
	// A finding has to be specific enough to act on, which means naming the
	// plugin rather than saying an unknown plugin exists.
	if !strings.Contains(f.What, "com.example.someplugin.WidgetStep") {
		t.Errorf("finding does not name the plugin: %q", f.What)
	}
	if !strings.Contains(f.Where, "step 3") {
		t.Errorf("finding does not locate the step: %q", f.Where)
	}
}

func TestStorageFindings(t *testing.T) {
	got := codes(Channel(fixture(t)))

	if f, ok := got["STORAGE_DEVELOPMENT"]; !ok {
		t.Error("missing STORAGE_DEVELOPMENT for a channel left in development storage mode")
	} else if f.Severity != Warning {
		t.Errorf("STORAGE_DEVELOPMENT severity = %q, want %q", f.Severity, Warning)
	}

	// The fixture prunes content sooner than metadata, which is worth a note
	// but is not a problem.
	if f, ok := got["PRUNE_INCONSISTENT"]; ok {
		t.Errorf("unexpected PRUNE_INCONSISTENT: %+v", f)
	}
	if _, ok := got["ATTACHMENTS"]; !ok {
		t.Error("missing ATTACHMENTS note")
	}
	if _, ok := got["NO_PRUNING"]; ok {
		t.Error("unexpected NO_PRUNING when both prune windows are set")
	}
}

func TestNoPruningIsWarned(t *testing.T) {
	c := &mirth.Channel{
		Name: "unpruned",
		Properties: mirth.ChannelProperties{
			MessageStorageMode: "PRODUCTION",
			PruneMetaDataDays:  0,
			PruneContentDays:   0,
		},
	}
	if _, ok := codes(Channel(c))["NO_PRUNING"]; !ok {
		t.Error("missing NO_PRUNING: unbounded message growth is the usual cause of a full disk")
	}
}

func TestPruningIrrelevantWhenStorageDisabled(t *testing.T) {
	c := &mirth.Channel{
		Properties: mirth.ChannelProperties{MessageStorageMode: "DISABLED"},
	}
	if _, ok := codes(Channel(c))["NO_PRUNING"]; ok {
		t.Error("NO_PRUNING reported when message storage is off entirely")
	}
}

func TestScanDetectsJavaInterop(t *testing.T) {
	c := scriptChannel(`var d = new java.util.Date(); logger.info(d);`)
	f, ok := codes(Channel(c))["JAVA_INTEROP"]
	if !ok {
		t.Fatal("missing JAVA_INTEROP for a script calling java.util.Date")
	}
	if f.Severity != Blocker {
		t.Errorf("JAVA_INTEROP severity = %q, want %q", f.Severity, Blocker)
	}
}

func TestScanIgnoresCommentedCode(t *testing.T) {
	c := scriptChannel("// var d = new java.util.Date();\n/* Packages.foo */\nvar x = msg['PID']['PID.3'];")
	if f, ok := codes(Channel(c))["JAVA_INTEROP"]; ok {
		t.Errorf("reported commented-out code as a live dependency: %+v", f)
	}
}

func TestScanDetectsSharedState(t *testing.T) {
	c := scriptChannel(`globalMap.put('lastMrn', mrn); router.routeMessage('Other', msg);`)
	got := codes(Channel(c))
	for _, want := range []string{"GLOBAL_MAP", "CHANNEL_CHAINING"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
}

func TestScanReportsEachCodeOnce(t *testing.T) {
	// Five uses of the JVM in one script is one problem, not five.
	c := scriptChannel(`
		var a = new java.util.Date();
		var b = new java.util.Date();
		var f = new java.text.SimpleDateFormat('yyyy');
		var g = new java.lang.String('x');
		var h = java.lang.System.currentTimeMillis();`)

	n := 0
	for _, f := range Channel(c).Findings {
		if f.Code == "JAVA_INTEROP" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("JAVA_INTEROP reported %d times, want 1", n)
	}
}

func TestTrivialScriptsAreNotFindings(t *testing.T) {
	c := &mirth.Channel{
		Name:                 "boilerplate",
		PreprocessingScript:  "return message;",
		PostprocessingScript: "return;",
		DeployScript:         "// nothing to do",
		UndeployScript:       "",
		Properties:           mirth.ChannelProperties{MessageStorageMode: "DISABLED"},
	}
	if f, ok := codes(Channel(c))["CHANNEL_SCRIPT"]; ok {
		t.Errorf("Mirth's prefilled boilerplate reported as a finding: %+v", f)
	}
}

func TestUnsupportedTransportIsBlocker(t *testing.T) {
	c := &mirth.Channel{
		Name: "db poller",
		Source: mirth.Connector{
			Mode:            mirth.ModeSource,
			Enabled:         true,
			Transport:       "Database Reader",
			PropertiesClass: "com.mirth.connect.connectors.jdbc.DatabaseReceiverProperties",
			Properties:      map[string]string{"url": "jdbc:postgresql://db.invalid/lab"},
		},
		Properties: mirth.ChannelProperties{MessageStorageMode: "DISABLED"},
	}
	f, ok := codes(Channel(c))["TRANSPORT_UNSUPPORTED"]
	if !ok {
		t.Fatal("missing TRANSPORT_UNSUPPORTED for a Database Reader source")
	}
	if f.Severity != Blocker {
		t.Errorf("severity = %q, want %q", f.Severity, Blocker)
	}
	if !strings.Contains(f.What, "Database Reader") {
		t.Errorf("finding does not name the transport: %q", f.What)
	}
}

func TestExplainDescribesTheChannel(t *testing.T) {
	r := Channel(fixture(t))
	var sb strings.Builder
	if err := Explain(&sb, r); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	out := sb.String()

	// The description has to answer "what does this channel do" without the
	// reader opening the XML.
	for _, want := range []string{
		"ADT Inbound - Synthetic Site",
		"enabled",
		"TCP Listener",
		"listening on 0.0.0.0:6661",
		"Registry API",
		"HTTP Sender",
		"[disabled]",
		"HL7V2 → JSON",
		"mode DEVELOPMENT",
		"BLOCKER",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Explain output missing %q\n---\n%s", want, out)
		}
	}
}

func TestExplainCleanChannel(t *testing.T) {
	c := &mirth.Channel{
		Name:    "clean",
		Enabled: true,
		Source: mirth.Connector{
			Mode:            mirth.ModeSource,
			Enabled:         true,
			Transport:       "TCP Listener",
			PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
			Properties:      map[string]string{"listenerConnectorProperties.port": "6000"},
		},
		Properties: mirth.ChannelProperties{
			MessageStorageMode: "PRODUCTION",
			PruneMetaDataDays:  30,
			PruneContentDays:   30,
		},
	}
	r := Channel(c)
	if !r.Translatable() {
		t.Fatalf("expected a clean channel to be translatable, findings: %+v", r.Findings)
	}

	var sb strings.Builder
	if err := Explain(&sb, r); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.Contains(sb.String(), "translates mechanically") {
		t.Errorf("clean channel not reported as clean:\n%s", sb.String())
	}
}

func TestFindingsSortedBySeverity(t *testing.T) {
	r := Channel(fixture(t))
	last := -1
	for _, f := range r.Findings {
		if got := f.Severity.rank(); got < last {
			t.Fatalf("findings not ordered by severity: %q after rank %d", f.Severity, last)
		} else {
			last = got
		}
	}
}

// scriptChannel builds a minimal channel whose source transformer runs one
// JavaScript step, for exercising the script scanner.
func scriptChannel(script string) *mirth.Channel {
	return &mirth.Channel{
		Name: "script test",
		Source: mirth.Connector{
			Mode:            mirth.ModeSource,
			Enabled:         true,
			Transport:       "TCP Listener",
			PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
			Transformer: mirth.Transformer{
				Steps: []mirth.Step{{
					Sequence: 0,
					Name:     "step",
					Kind:     mirth.StepJavaScript,
					RawKind:  "com.mirth.connect.plugins.javascriptstep.JavaScriptStep",
					Script:   script,
				}},
			},
		},
		Properties: mirth.ChannelProperties{MessageStorageMode: "DISABLED"},
	}
}
