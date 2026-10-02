package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
)

// The most important property is that the output loads and does what the notes
// claim. A translator whose output does not load is obvious; one whose output loads
// and quietly means something different is the dangerous case, so most of these
// assert on behaviour rather than on text.

func translated(t *testing.T, ch *mirth.Channel) (*Result, *config.Channel) {
	t.Helper()
	res := Channel(ch)

	path := filepath.Join(t.TempDir(), res.FileName())
	if err := os.WriteFile(path, []byte(res.YAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("the translated channel does not load: %v\n\n%s", err, res.YAML)
	}
	return res, cfg
}

// mllpSource is the shape of a Mirth TCP listener as the parser reports it.
func mllpSource() mirth.Connector {
	return mirth.Connector{
		Name:            "sourceConnector",
		Mode:            mirth.ModeSource,
		Enabled:         true,
		Transport:       "TCP Listener",
		PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
		Properties: map[string]string{
			"listenerConnectorProperties.host": "10.0.0.5",
			"listenerConnectorProperties.port": "6661",
		},
	}
}

func mllpDestination(name, host, port string) mirth.Connector {
	return mirth.Connector{
		Name:            name,
		Mode:            mirth.ModeDestination,
		Enabled:         true,
		Transport:       "TCP Sender",
		PropertiesClass: "com.mirth.connect.connectors.tcp.TcpDispatcherProperties",
		Properties: map[string]string{
			"remoteAddress": host,
			"remotePort":    port,
		},
	}
}

func TestASimpleChannelTranslatesCompletely(t *testing.T) {
	res, cfg := translated(t, &mirth.Channel{
		Name:         "ADT Inbound",
		Description:  "Receives ADT and forwards it.",
		Enabled:      true,
		Source:       mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Registry", "10.0.0.9", "6662")},
	})

	if res.Confidence != Complete {
		t.Errorf("confidence = %s, notes: %v", res.Confidence, res.Notes)
	}
	if cfg.Name != "adt-inbound" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.Source.Listen != "10.0.0.5:6661" {
		t.Errorf("listen = %q", cfg.Source.Listen)
	}
	if len(cfg.Destinations) != 1 || cfg.Destinations[0].Address != "10.0.0.9:6662" {
		t.Errorf("destinations = %+v", cfg.Destinations)
	}
}

func TestChannelNamesBecomeUsableFileNames(t *testing.T) {
	// A Mirth name like "ADT Inbound (Site A) - v2" is entirely normal and would
	// otherwise produce a filename nobody can type.
	cases := map[string]string{
		"ADT Inbound":               "adt-inbound",
		"ADT Inbound (Site A) - v2": "adt-inbound-site-a-v2",
		"Lab/Results  →  Registry":  "lab-results-registry",
		"  spaced  ":                "spaced",
		"!!!":                       "unnamed-channel",
		"":                          "unnamed-channel",
		"ORU_R01.inbound":           "oru-r01-inbound",
	}
	for in, want := range cases {
		if got := sanitiseName(in); got != want {
			t.Errorf("sanitiseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestADisabledChannelStaysDisabled(t *testing.T) {
	// A channel that was off in Mirth was off for a reason, and quietly enabling it
	// during a migration would start a feed nobody asked for.
	_, cfg := translated(t, &mirth.Channel{
		Name:         "Old Feed",
		Enabled:      false,
		Source:       mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	})

	if cfg.IsEnabled() {
		t.Error("a disabled Mirth channel should translate to a disabled Perfuse channel")
	}
}

func TestRuleBuilderFiltersBecomeAFilterExpression(t *testing.T) {
	ch := &mirth.Channel{
		Name:         "Filtered",
		Enabled:      true,
		Source:       mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Filter = mirth.Filter{Rules: []mirth.Rule{{
		Sequence:  0,
		Name:      "drop A28",
		Kind:      mirth.RuleBuilder,
		Operator:  "NONE",
		Field:     "msg['MSH']['MSH.9']['MSH.9.2'].toString()",
		Condition: "NOT_EQUAL",
		// Mirth stores values as JavaScript literals, quotes included.
		Values: []string{`"A28"`},
	}}}

	res, cfg := translated(t, ch)

	// Double-quoting is the bug worth guarding: it produces a filter that compares
	// against a string containing quote characters, never matches, and drops
	// everything while looking plausible.
	if cfg.Filter != `MSH-9.2 != "A28"` {
		t.Errorf("filter = %q, want MSH-9.2 != \"A28\"", cfg.Filter)
	}
	if res.Counts.FilterRules != 1 {
		t.Errorf("FilterRules = %d", res.Counts.FilterRules)
	}
}

func TestSeveralFilterValuesBecomeAnInList(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Multi", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Filter = mirth.Filter{Rules: []mirth.Rule{{
		Kind: mirth.RuleBuilder, Operator: "NONE",
		Field:     "msg['MSH']['MSH.4']['MSH.4.1'].toString()",
		Condition: "EQUALS",
		Values:    []string{`"SITEA"`, `"SITEB"`},
	}}}

	_, cfg := translated(t, ch)
	// Clearer than a chain of ors, and it is what Mirth's several-value EQUALS
	// actually means.
	if cfg.Filter != `MSH-4.1 in ["SITEA", "SITEB"]` {
		t.Errorf("filter = %q", cfg.Filter)
	}
}

func TestContainsBecomesAnEscapedRegex(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Contains", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Filter = mirth.Filter{Rules: []mirth.Rule{{
		Kind: mirth.RuleBuilder, Operator: "NONE",
		Field:     "msg['OBX']['OBX.3']['OBX.3.1'].toString()",
		Condition: "CONTAINS",
		Values:    []string{`"GLU.1"`},
	}}}

	_, cfg := translated(t, ch)
	// The dot has to be escaped or the filter matches more than the original did,
	// which is a widening nobody would notice.
	// The emitted text carries an escaped backslash, which the filter parser
	// unescapes back to a regex matching a literal dot. Without the escape the
	// pattern would match any character there, widening the filter silently.
	if !strings.Contains(cfg.Filter, `GLU\\.1`) {
		t.Errorf("filter = %q, the literal should be regex-escaped", cfg.Filter)
	}
}

func TestAndOrAreCarriedAcross(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Joined", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Filter = mirth.Filter{Rules: []mirth.Rule{
		{Kind: mirth.RuleBuilder, Operator: "NONE",
			Field:     "msg['MSH']['MSH.9']['MSH.9.1'].toString()",
			Condition: "EQUALS", Values: []string{`"ADT"`}},
		{Kind: mirth.RuleBuilder, Operator: "AND",
			Field:     "msg['PID']['PID.3']['PID.3.1'].toString()",
			Condition: "EXISTS"},
	}}

	_, cfg := translated(t, ch)
	if !strings.Contains(cfg.Filter, " and ") {
		t.Errorf("filter = %q", cfg.Filter)
	}
	if !strings.Contains(cfg.Filter, "PID-3.1 exists") {
		t.Errorf("filter = %q", cfg.Filter)
	}
}

func TestAJavaScriptFilterBecomesAScriptFilter(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Scripted", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Filter = mirth.Filter{Rules: []mirth.Rule{{
		Sequence: 0, Name: "require MRN", Kind: mirth.RuleJavaScript,
		Script: "return msg['PID']['PID.3']['PID.3.1'].toString().length > 0;",
	}}}

	_, cfg := translated(t, ch)

	// Not approximated. The filter language has no function calls on purpose, so
	// anything beyond a comparison genuinely cannot be expressed there and
	// pretending otherwise would be a silent behaviour change.
	if cfg.Scripts.Filter == "" {
		t.Fatal("the JavaScript rule should have become a script filter")
	}
	if !strings.Contains(cfg.Scripts.Filter, "PID.3.1") {
		t.Errorf("script filter = %q", cfg.Scripts.Filter)
	}
}

func TestAMapperCopyingAFieldBecomesADeclarativeStep(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Mapped", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Name: "copy the MRN to the alternate id",
		Kind:     mirth.StepMapper,
		Variable: "msg['PID']['PID.4']['PID.4.1']",
		Mapping:  "msg['PID']['PID.3']['PID.3.1']",
	}}}

	res, cfg := translated(t, ch)

	if res.Counts.Declarative != 1 {
		t.Errorf("Declarative = %d, notes: %v", res.Counts.Declarative, res.Notes)
	}
	if len(cfg.Transformations) != 1 {
		t.Fatalf("transformations = %d", len(cfg.Transformations))
	}
	// Declarative rather than script, because a copy is unambiguous and a
	// declarative step is readable and checkable in a way a script is not.
	if cfg.Transformations[0].Copy == nil {
		t.Errorf("step = %+v, want a copy", cfg.Transformations[0])
	}
}

func TestAMapperWritingALiteralBecomesASetStep(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Literal", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Name: "stamp the receiving facility",
		Kind:     mirth.StepMapper,
		Variable: "msg['MSH']['MSH.6']['MSH.6.1']",
		Mapping:  `'RFAC1'`,
	}}}

	_, cfg := translated(t, ch)
	if len(cfg.Transformations) != 1 || cfg.Transformations[0].Set == nil {
		t.Fatalf("want one set step, got %+v", cfg.Transformations)
	}
	if cfg.Transformations[0].Set.Value != "RFAC1" {
		t.Errorf("value = %q", cfg.Transformations[0].Set.Value)
	}
}

func TestAMapperExpressionIsCarriedOverRatherThanGuessedAt(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Expr", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Name: "derive something",
		Kind:     mirth.StepMapper,
		Variable: "msg['PID']['PID.5']['PID.5.1']",
		Mapping:  "msg['PID']['PID.5']['PID.5.1'].toString().toUpperCase().substring(0, 10)",
	}}}

	res, cfg := translated(t, ch)

	// Rewriting this into a chain of declarative steps is exactly where a
	// translator introduces a difference nobody notices.
	if res.Counts.Scripted != 1 {
		t.Errorf("Scripted = %d", res.Counts.Scripted)
	}
	if cfg.Scripts.Transformer == "" {
		t.Fatal("the expression should have been carried over as a script")
	}
	if !strings.Contains(cfg.Scripts.Transformer, "toUpperCase") {
		t.Errorf("script = %q", cfg.Scripts.Transformer)
	}
}

func TestJavaScriptStepsAreCarriedOverInOrder(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Ordered", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{
		{Sequence: 0, Name: "first", Kind: mirth.StepJavaScript, Script: "var a = 1;"},
		{Sequence: 1, Name: "second", Kind: mirth.StepJavaScript, Script: "var b = 2;"},
		{Sequence: 2, Name: "third", Kind: mirth.StepJavaScript, Script: "var c = 3;"},
	}}

	_, cfg := translated(t, ch)
	script := cfg.Scripts.Transformer

	// Order matters: a later step routinely depends on a variable an earlier one
	// set, so reordering them would break the channel in a way that is hard to see.
	first := strings.Index(script, "var a")
	second := strings.Index(script, "var b")
	third := strings.Index(script, "var c")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("not all steps were carried over:\n%s", script)
	}
	if !(first < second && second < third) {
		t.Errorf("steps are out of order:\n%s", script)
	}
	// And each is labelled, so somebody reading the result can find the original.
	if !strings.Contains(script, "From step 1: second") {
		t.Errorf("steps should be labelled with their origin:\n%s", script)
	}
}

func TestXSLTIsReportedAsABlocker(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Stylesheet", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Name: "transform", Kind: mirth.StepXSLT,
		Script: "<xsl:stylesheet/>",
	}}}

	res, _ := translated(t, ch)
	if res.Confidence != Blocked {
		t.Errorf("confidence = %s", res.Confidence)
	}
	if res.Counts.Blockers == 0 {
		t.Error("XSLT should be a blocker")
	}
}

func TestAnExternalScriptIsABlockerBecauseItWasNotExported(t *testing.T) {
	ch := &mirth.Channel{
		Name: "External", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Name: "shared code", Kind: mirth.StepExternalScript,
		Script: "/opt/mirth/scripts/shared.js",
	}}}

	res, _ := translated(t, ch)
	// The file is not in the export, so no translator could have it. Saying where
	// to look is the only useful answer.
	if res.Counts.Blockers == 0 {
		t.Error("an external script should be a blocker")
	}
	if !strings.Contains(notesText(res), "shared.js") {
		t.Errorf("the note should name the file: %s", notesText(res))
	}
}

func TestAcknowledgeBeforeProcessingIsFlagged(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Early Ack", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Properties["sourceConnectorProperties.respondAfterProcessing"] = "false"

	res, cfg := translated(t, ch)

	if cfg.Source.Ack.When != config.AckOnReceipt {
		t.Errorf("ack.when = %q, want on_receipt", cfg.Source.Ack.When)
	}
	// This is behaviour worth calling out: the original told senders AA even when
	// delivery failed, and a migration is a good moment to notice that.
	if !strings.Contains(notesText(res), "AA even when delivery failed") {
		t.Errorf("notes = %s", notesText(res))
	}
	if !strings.Contains(notesText(res), "queue") {
		t.Error("the note should suggest a queue, since on_receipt without one loses messages")
	}
}

func TestMirthQueueSettingsAreCarriedAcross(t *testing.T) {
	dest := mllpDestination("Registry", "10.0.0.9", "6662")
	dest.Properties["destinationConnectorProperties.queueEnabled"] = "true"
	dest.Properties["destinationConnectorProperties.retryCount"] = "10"
	dest.Properties["destinationConnectorProperties.retryIntervalMillis"] = "10000"

	_, cfg := translated(t, &mirth.Channel{
		Name: "Queued", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{dest},
	})

	// A destination that was queued in Mirth and is not queued here loses messages
	// during an outage that previously survived one.
	q := cfg.Destinations[0].Queue
	if !q.IsEnabled() {
		t.Fatal("the queue should have been carried across")
	}
	if q.MaxAttempts != 10 {
		t.Errorf("max_attempts = %d", q.MaxAttempts)
	}
}

func TestConcurrentQueueThreadsAreCalledOut(t *testing.T) {
	dest := mllpDestination("Registry", "10.0.0.9", "6662")
	dest.Properties["destinationConnectorProperties.queueEnabled"] = "true"
	dest.Properties["destinationConnectorProperties.threadCount"] = "5"

	res, _ := translated(t, &mirth.Channel{
		Name: "Threaded", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{dest},
	})

	// The consequence is a discharge for a patient the receiver never admitted, so
	// this needs saying rather than silently behaving differently.
	text := notesText(res)
	if !strings.Contains(text, "out of order") {
		t.Errorf("notes should explain the reordering: %s", text)
	}
	if !strings.Contains(text, "never admitted") {
		t.Errorf("the note should say what goes wrong: %s", text)
	}
}

func TestADestinationTransformerIsFlaggedAsHavingNoEquivalent(t *testing.T) {
	dest := mllpDestination("Registry", "10.0.0.9", "6662")
	dest.Transformer = mirth.Transformer{Steps: []mirth.Step{
		{Sequence: 0, Name: "per-destination", Kind: mirth.StepJavaScript, Script: "var x = 1;"},
	}}

	res, _ := translated(t, &mirth.Channel{
		Name: "PerDest", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{dest},
	})

	// Perfuse transforms once, before fan-out. Silently applying a destination
	// transformer to every destination would change what other receivers get.
	if !strings.Contains(notesText(res), "one channel per destination") {
		t.Errorf("notes = %s", notesText(res))
	}
}

func TestAPreprocessorGoesToThePreprocessorStage(t *testing.T) {
	// It used to be smuggled into the start of the transformer, because Perfuse had no earlier stage.
	// It has one now, and it runs before parsing - if anything earlier than Mirth's before-the-filter -
	// so the line-ending repair below still works on a message that would not otherwise parse, which is
	// the case people actually write preprocessors for.
	res, cfg := translated(t, &mirth.Channel{
		Name: "Pre", Enabled: true, Source: mllpSource(),
		Destinations:        []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
		PreprocessingScript: "message = message.replace(/\\r\\n/g, '\\r');",
	})

	if !strings.Contains(cfg.Scripts.Preprocessor, "replace") {
		t.Errorf("the preprocessor did not reach the preprocessor stage: %q", cfg.Scripts.Preprocessor)
	}
	if strings.Contains(cfg.Scripts.Transformer, "replace") {
		t.Error("the preprocessor is still being copied into the transformer as well")
	}
	if !strings.Contains(notesText(res), "before the message is parsed") {
		t.Errorf("the note does not say when it runs: %s", notesText(res))
	}
}

func TestAPostprocessorGoesToThePostprocessorStage(t *testing.T) {
	// It used to be emitted commented out, because running a delivery-time script during transformation
	// could record something that had not been sent. There is a real post-delivery stage now, so it runs.
	res, cfg := translated(t, &mirth.Channel{
		Name: "Post", Enabled: true, Source: mllpSource(),
		Destinations:         []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
		PostprocessingScript: "updateTracking(msg);",
	})

	if !strings.Contains(cfg.Scripts.Postprocessor, "updateTracking") {
		t.Fatalf("the postprocessor did not reach the postprocessor stage: %q",
			cfg.Scripts.Postprocessor)
	}
	if strings.Contains(cfg.Scripts.Postprocessor, "// updateTracking") {
		t.Error("the postprocessor is still commented out, so it will never run")
	}
	if strings.Contains(notesText(res), "no post-delivery stage") {
		t.Error("the note still claims Perfuse has no post-delivery stage")
	}
}

func TestDeployAndUndeployScriptsAreCarriedOver(t *testing.T) {
	// These were reported as having nowhere to go, which was true when the note was written and stopped
	// being true when Perfuse grew the stages. A translator that undersells what it can carry tells
	// somebody a migration is harder than it is, and they believe it.
	res, cfg := translated(t, &mirth.Channel{
		Name: "Lifecycle", Enabled: true, Source: mllpSource(),
		Destinations:   []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
		DeployScript:   "globalMap.put('codes', loadCodes());",
		UndeployScript: "logger.info('stopping');",
	})

	if !strings.Contains(cfg.Scripts.Deploy, "loadCodes") {
		t.Errorf("the deploy script was lost: %q", cfg.Scripts.Deploy)
	}
	if !strings.Contains(cfg.Scripts.Undeploy, "stopping") {
		t.Errorf("the undeploy script was lost: %q", cfg.Scripts.Undeploy)
	}

	// The one behaviour difference is called out, because it changes what happens on a bad day.
	if !strings.Contains(notesText(res), "does not start") {
		t.Errorf("the note does not mention that a failing deploy stops the start: %s", notesText(res))
	}
}

func TestAChannelWhoseOnlyScriptIsADeployScriptStillGetsAScriptsBlock(t *testing.T) {
	// Without deploy counting as a script, the block was omitted entirely and the script was silently
	// dropped - which is the exact failure this translator exists to avoid.
	_, cfg := translated(t, &mirth.Channel{
		Name: "OnlyDeploy", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
		DeployScript: "logger.info('starting');",
	})

	if cfg.Scripts == nil {
		t.Fatal("no scripts block, so the deploy script was dropped")
	}
	if !strings.Contains(cfg.Scripts.Deploy, "starting") {
		t.Errorf("the deploy script was dropped: %+v", cfg.Scripts)
	}
}

func TestFilePermissionIsGrantedOnlyWhenScriptsNeedIt(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Files", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Kind: mirth.StepJavaScript,
		Script: "var codes = FileUtil.read('/etc/perfuse/codes.txt');",
	}}}

	_, cfg := translated(t, ch)
	found := false
	for _, a := range cfg.Scripts.Allow {
		if a == "file" {
			found = true
		}
	}
	if !found {
		t.Errorf("file access should have been granted, got %v", cfg.Scripts.Allow)
	}
}

func TestNoPermissionsAreGrantedWithoutReason(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Plain", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Kind: mirth.StepJavaScript, Script: "var x = 1;",
	}}}

	_, cfg := translated(t, ch)
	// The default is denial, and a translator that granted everything would defeat
	// the point of having the setting at all.
	if len(cfg.Scripts.Allow) != 0 {
		t.Errorf("nothing should have been granted, got %v", cfg.Scripts.Allow)
	}
}

func TestADatabaseConnectionInAScriptIsABlocker(t *testing.T) {
	ch := &mirth.Channel{
		Name: "DB", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{{
		Sequence: 0, Kind: mirth.StepJavaScript,
		Script: "var c = DatabaseConnectionFactory.createDatabaseConnection('org.postgresql.Driver', url);",
	}}}

	res, _ := translated(t, ch)
	if res.Counts.Blockers == 0 {
		t.Error("a database connection should be a blocker")
	}
	// Worth mentioning because it is also the commonest cause of a Mirth channel
	// stalling, so reproducing it would be the wrong instinct.
	if !strings.Contains(notesText(res), "stalling under load") {
		t.Errorf("notes = %s", notesText(res))
	}
}

func TestMessageStorageModesAreReported(t *testing.T) {
	for mode, want := range map[string]string{
		"METADATA": "store-payloads=false",
		"DISABLED": "store-messages=false",
	} {
		ch := &mirth.Channel{
			Name: "Storage " + mode, Enabled: true, Source: mllpSource(),
			Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
			Properties:   mirth.ChannelProperties{MessageStorageMode: mode},
		}
		res, _ := translated(t, ch)
		// Perfuse stores payloads by default, so a channel that stored nothing would
		// silently start holding clinical data at rest.
		if !strings.Contains(notesText(res), want) {
			t.Errorf("mode %s: notes should mention %s, got %s", mode, want, notesText(res))
		}
	}
}

func TestAnUnknownSourceIsABlockerWithALoadableFile(t *testing.T) {
	src := mllpSource()
	src.Transport = "JMS Listener"
	src.PropertiesClass = "com.mirth.connect.connectors.jms.JmsReceiverProperties"

	res, cfg := translated(t, &mirth.Channel{
		Name: "JMS", Enabled: true, Source: src,
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	})

	if res.Counts.Blockers == 0 {
		t.Error("an unsupported source should be a blocker")
	}
	// The file still has to load, so the rest of the translation can be reviewed
	// rather than being lost because one connector was unsupported.
	if cfg.Name == "" {
		t.Error("the channel should still load")
	}
}

func TestAChannelWithNoTranslatableDestinationStillLoads(t *testing.T) {
	dest := mirth.Connector{
		Name: "Queue Out", Mode: mirth.ModeDestination, Enabled: true,
		Transport:       "JMS Sender",
		PropertiesClass: "com.mirth.connect.connectors.jms.JmsDispatcherProperties",
		Properties:      map[string]string{},
	}

	_, cfg := translated(t, &mirth.Channel{
		Name: "No Dest", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{dest},
	})

	if len(cfg.Destinations) == 0 {
		t.Error("a placeholder destination should keep the file loadable")
	}
}

func TestTheOutputCarriesAHeaderNamingTheOriginal(t *testing.T) {
	res := Channel(&mirth.Channel{
		Name: "ADT Inbound", Enabled: true, MirthVersion: "4.4.0", Revision: 12,
		Source:       mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
	})

	// A migration is reviewed by somebody who has to sign it off, and an
	// unannotated wall of YAML cannot be reviewed.
	if !strings.Contains(res.YAML, `Mirth channel "ADT Inbound"`) {
		t.Error("the header should name the original channel")
	}
	if !strings.Contains(res.YAML, "4.4.0") {
		t.Error("the header should record the Mirth version")
	}
	if !strings.Contains(res.YAML, "revision 12") {
		t.Error("the header should record the channel revision")
	}
}

func TestNotesSortBlockersFirst(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Mixed", Enabled: true, Source: mllpSource(),
		Destinations: []mirth.Connector{mllpDestination("Out", "10.0.0.9", "6662")},
		DeployScript: "logger.info('deployed');",
	}
	ch.Source.Transformer = mirth.Transformer{Steps: []mirth.Step{
		{Sequence: 0, Kind: mirth.StepXSLT, Script: "<xsl:stylesheet/>"},
	}}

	res := Channel(ch)
	res.SortNotes()

	if len(res.Notes) < 2 {
		t.Fatalf("notes = %d", len(res.Notes))
	}
	// Somebody scanning a long report reads the top of it.
	if res.Notes[0].Severity != "blocker" {
		t.Errorf("first note has severity %q, want blocker", res.Notes[0].Severity)
	}
}

func TestUnquoteLiteralLeavesExpressionsAlone(t *testing.T) {
	// An expression left as-is shows up as obviously wrong in the output, which is
	// better than being silently turned into a plausible-looking string.
	cases := map[string]string{
		`"A28"`:      "A28",
		`'A28'`:      "A28",
		`A28`:        "A28",
		`"a" + "b"`:  `"a" + "b"`,
		`msg['PID']`: `msg['PID']`,
		`""`:         "",
	}
	for in, want := range cases {
		if got := unquoteLiteral(in); got != want {
			t.Errorf("unquoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTheRealFixtureTranslatesAndLoads(t *testing.T) {
	ch, err := mirth.ParseChannelFile("../mirth/testdata/adt_channel.xml")
	if err != nil {
		t.Skip(err)
	}

	res, cfg := translated(t, ch)

	// The fixture deliberately contains awkward things, so it should be blocked
	// rather than reported as complete. What must hold is that the file loads and
	// the recognisable parts came across.
	if res.Confidence != Blocked {
		t.Errorf("confidence = %s, want blocked for a fixture with XSLT and HTTP", res.Confidence)
	}
	if cfg.Filter == "" {
		t.Error("the RuleBuilder filter should have translated")
	}
	if !cfg.HasScripts() {
		t.Error("the JavaScript should have been carried over")
	}
	if len(cfg.Destinations) == 0 {
		t.Error("destinations should be present, even as placeholders")
	}
}

func notesText(res *Result) string {
	var parts []string
	for _, n := range res.Notes {
		parts = append(parts, n.Message, n.Action)
	}
	return strings.Join(parts, " | ")
}

// A channel whose script reaches for Java must be reported at import time, not at first message.
//
// This is the wiring test, distinct from the scanner's own tests in internal/mirth. Those prove the scanner finds
// things; this proves a caller exists and that what it finds reaches the report. An implementation with no caller is
// indistinguishable from a feature that does not exist, and this repository has shipped that twice.
func TestJavaInAScriptBlocksTheImportAndSaysWhatToDo(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Lab Feed",
		Source: mirth.Connector{
			Transport:  "TCP Listener",
			Properties: map[string]string{"listenerConnectorProperties.port": "6661"},
			Transformer: mirth.Transformer{Steps: []mirth.Step{{
				Kind:   mirth.StepJavaScript,
				Script: "var h = Packages.com.acme.hl7.ChecksumHelper.compute(msg);",
			}}},
		},
	}

	res := Channel(ch)

	if res.Confidence != Blocked {
		t.Fatalf("a vendor jar should block the import, got confidence %q", res.Confidence)
	}

	var found *Note
	for i := range res.Notes {
		if strings.Contains(res.Notes[i].Message, "com.acme") {
			found = &res.Notes[i]
		}
	}
	if found == nil {
		t.Fatalf("the Java reference should appear in the notes, got %+v", res.Notes)
	}
	if found.Severity != "blocker" {
		t.Fatalf("expected a blocker, got %q", found.Severity)
	}
	if found.Action == "" {
		t.Fatal("a blocker with no action tells somebody they are stuck without saying what to try")
	}
	// The location has to be precise enough to find the code in Mirth's editor.
	if !strings.Contains(found.Where, "line 1") {
		t.Fatalf("the note should name the line, got %q", found.Where)
	}
}

// A call Perfuse already implements must not be reported as a problem.
//
// The inverse of the test above, and the one that would have failed if the specific ChannelUtil rules had been ordered
// after the catch-all: it would send somebody to rewrite code that already works.
func TestASupportedCallDoesNotBlockTheImport(t *testing.T) {
	ch := &mirth.Channel{
		Name: "Lab Feed",
		Source: mirth.Connector{
			Transport:  "TCP Listener",
			Properties: map[string]string{"listenerConnectorProperties.port": "6661"},
			Transformer: mirth.Transformer{Steps: []mirth.Step{{
				Kind:   mirth.StepJavaScript,
				Script: "logger.info(ChannelUtil.getChannelName());",
			}}},
		},
	}

	res := Channel(ch)
	if res.Confidence == Blocked {
		t.Fatalf("getChannelName is implemented, so it must not block: %+v", res.Notes)
	}
	for _, n := range res.Notes {
		if n.Severity == "blocker" {
			t.Fatalf("unexpected blocker: %+v", n)
		}
	}
}
