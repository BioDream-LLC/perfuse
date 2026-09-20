package mirth

import (
	"strings"
	"testing"
)

// scriptChannel builds a channel with one JavaScript transformer step on the source.
func scriptChannel(src string) *Channel {
	return &Channel{
		Source: Connector{
			Transformer: Transformer{
				Steps: []Step{{Kind: StepJavaScript, Script: src}},
			},
		},
	}
}

func TestChannelLifecycleCallsAreReportedAsAFeatureGapNotAsImpossible(t *testing.T) {
	// The distinction this asserts is the whole reason the verdict set has four values. Perfuse can start and stop a
	// channel over its API; it just does not let a script do it. Reporting that as out-of-scope would tell a migrator
	// their channel cannot be moved, which is wrong.
	r := scriptChannel(`ChannelUtil.startChannel(getChannelId("Feed A"));`).ScanJava()

	if len(r.Uses) != 1 {
		t.Fatalf("expected one finding, got %d: %+v", len(r.Uses), r.Uses)
	}
	if r.Uses[0].Verdict != VerdictNeedsFeature {
		t.Fatalf("startChannel should be a feature gap, got %q", r.Uses[0].Verdict)
	}
	if !strings.Contains(r.Uses[0].Advice, "/start") {
		t.Fatalf("the advice should name the endpoint that does this, got %q", r.Uses[0].Advice)
	}
	if r.OutOfScope != 0 {
		t.Fatalf("nothing here is out of scope, but %d was counted", r.OutOfScope)
	}
}

func TestCallsPerfuseAlreadyImplementsAreNotReportedAsProblems(t *testing.T) {
	// getChannelName is implemented, under the same name. A report that flagged it would send somebody to rewrite
	// working code, and the specific rule has to win over the ChannelUtil catch-all for that to hold.
	r := scriptChannel(`
var name = ChannelUtil.getChannelName();
logger.info(name);
var id = UUIDGenerator.getUUID();
`).ScanJava()

	if r.Blocked() {
		t.Fatalf("nothing here blocks a migration, but the report says otherwise: %+v", r.Uses)
	}
	if r.Supported != 2 {
		t.Fatalf("expected 2 supported findings, got %d: %+v", r.Supported, r.Uses)
	}
}

func TestOrdinaryJavaIsReportedWithTheJavaScriptThatReplacesIt(t *testing.T) {
	r := scriptChannel(`
var f = new java.text.SimpleDateFormat("yyyyMMdd");
var m = new java.util.HashMap();
var l = new java.util.ArrayList();
`).ScanJava()

	if r.Rewritable != 3 {
		t.Fatalf("expected 3 rewritable findings, got %d: %+v", r.Rewritable, r.Uses)
	}
	// The advice has to name the replacement. "Rewrite this" is not advice.
	for _, u := range r.Uses {
		if len(u.Advice) < 30 {
			t.Fatalf("advice for %q is too thin to act on: %q", u.Reference, u.Advice)
		}
	}
	if !strings.Contains(r.Uses[0].Advice, "DateUtil") {
		t.Fatalf("SimpleDateFormat advice should name DateUtil, got %q", r.Uses[0].Advice)
	}
}

func TestMirthServerClassesAreReportedAsOutOfScopeAndSayWhy(t *testing.T) {
	// This is the case that started the whole exercise. It must not be softened: those classes are Mirth's own
	// implementation, are proprietary as of 4.6, and expect a Mirth server behind them.
	r := scriptChannel(
		`var c = Packages.com.mirth.connect.server.controllers.ControllerFactory.getFactory();`).ScanJava()

	if r.OutOfScope != 1 {
		t.Fatalf("expected 1 out-of-scope finding, got %d: %+v", r.OutOfScope, r.Uses)
	}
	if !strings.Contains(r.Uses[0].Advice, "proprietary") {
		t.Fatalf("the advice should say why this cannot be loaded, got %q", r.Uses[0].Advice)
	}
}

func TestAVendorJarIsNamedRatherThanCalledUnknown(t *testing.T) {
	r := scriptChannel(`var x = Packages.com.acme.hl7.ChecksumHelper.compute(msg);`).ScanJava()

	if r.OutOfScope != 1 {
		t.Fatalf("expected 1 out-of-scope finding, got %d: %+v", r.OutOfScope, r.Uses)
	}
	if !strings.Contains(r.Uses[0].Reference, "com.acme") {
		t.Fatalf("the report should quote what the script wrote, got %q", r.Uses[0].Reference)
	}
}

func TestACommentMentioningJavaIsNotAFinding(t *testing.T) {
	// A migrator who checks one false positive stops trusting the whole report, so this matters more than it looks.
	r := scriptChannel(`
// We used to call java.text.SimpleDateFormat here before switching.
/* Packages.com.mirth.connect.server.controllers is not used any more.
   java.util.HashMap neither. */
var ok = true;
`).ScanJava()

	if len(r.Uses) != 0 {
		t.Fatalf("comments should produce no findings, got %d: %+v", len(r.Uses), r.Uses)
	}
}

func TestAStringLiteralMentioningJavaIsNotAFinding(t *testing.T) {
	r := scriptChannel(`logger.info("this used to use java.util.ArrayList");`).ScanJava()
	if len(r.Uses) != 0 {
		t.Fatalf("string contents should produce no findings, got %d: %+v", len(r.Uses), r.Uses)
	}
}

func TestLineNumbersSurviveCommentStripping(t *testing.T) {
	// The line number is how somebody finds the code in Mirth's editor. Stripping a block comment by deleting it
	// would shift every line after it, which is a wrong answer delivered confidently.
	r := scriptChannel(`var a = 1;
/* a comment
   spanning
   several lines */
var f = new java.text.SimpleDateFormat("yyyy");
`).ScanJava()

	if len(r.Uses) != 1 {
		t.Fatalf("expected one finding, got %+v", r.Uses)
	}
	if r.Uses[0].Line != 5 {
		t.Fatalf("SimpleDateFormat is on line 5, reported on line %d", r.Uses[0].Line)
	}
}

func TestAnEscapedQuoteDoesNotEndTheStringEarly(t *testing.T) {
	// If the escape is mishandled the scanner leaves string state early and starts reporting findings from inside
	// text, which is the false-positive class this whole step exists to avoid.
	r := scriptChannel(`var s = "he said \"java.util.HashMap\" out loud"; var t = 1;`).ScanJava()
	if len(r.Uses) != 0 {
		t.Fatalf("expected no findings, got %+v", r.Uses)
	}
}

func TestEveryScriptLocationInAChannelIsScanned(t *testing.T) {
	// A finding in a deploy script that nothing looks at is a finding nobody sees. Each location is named separately
	// so that missing one shows up as a count, not as silence.
	c := &Channel{
		PreprocessingScript:  `var a = new java.util.HashMap();`,
		PostprocessingScript: `var b = new java.util.ArrayList();`,
		DeployScript:         `ChannelUtil.startChannel("x");`,
		UndeployScript:       `ChannelUtil.stopChannel("x");`,
		Source: Connector{
			Transformer: Transformer{Steps: []Step{{Kind: StepJavaScript, Script: `var c = new java.util.Date();`}}},
			Filter:      Filter{Rules: []Rule{{Kind: RuleJavaScript, Script: `return java.lang.Boolean.TRUE;`}}},
		},
		Destinations: []Connector{{
			Name:        "To Lab",
			Transformer: Transformer{Steps: []Step{{Kind: StepJavaScript, Script: `importPackage(java.util);`}}},
		}},
	}

	r := c.ScanJava()
	if len(r.Uses) != 7 {
		var got []string
		for _, u := range r.Uses {
			got = append(got, u.Where)
		}
		t.Fatalf("expected a finding from all 7 script locations, got %d: %v", len(r.Uses), got)
	}

	// The destination has to be identifiable by its name, or a channel with nine destinations is a guessing game.
	found := false
	for _, u := range r.Uses {
		if strings.Contains(u.Where, "To Lab") {
			found = true
		}
	}
	if !found {
		t.Fatal("the destination's name should appear in the location")
	}
}

func TestNestedStepsAndRulesAreScanned(t *testing.T) {
	c := &Channel{Source: Connector{Transformer: Transformer{Steps: []Step{{
		Kind: StepJavaScript, Script: `var a = 1;`,
		Children: []Step{{Kind: StepJavaScript, Script: `var b = new java.util.HashMap();`}},
	}}}}}

	r := c.ScanJava()
	if len(r.Uses) != 1 {
		t.Fatalf("a nested step should be scanned, got %+v", r.Uses)
	}
	if !strings.Contains(r.Uses[0].Where, "nested") {
		t.Fatalf("the location should say it is nested, got %q", r.Uses[0].Where)
	}
}

func TestAnExternalScriptIsReportedAsUncheckableRatherThanClean(t *testing.T) {
	// The file is not in the export. Saying "no Java found" would be a lie by omission, and it is the one case where
	// the honest answer is that somebody has to go and fetch the file.
	c := &Channel{Source: Connector{Transformer: Transformer{Steps: []Step{
		{Kind: StepExternalScript, Script: "/opt/mirth/scripts/lookup.js"},
	}}}}

	r := c.ScanJava()
	if len(r.ExternalScripts) != 1 {
		t.Fatalf("expected the external script to be recorded, got %+v", r.ExternalScripts)
	}
	if !strings.Contains(r.Summary(), "could not be checked") {
		t.Fatalf("the summary must not imply the channel is clean: %q", r.Summary())
	}
}

func TestTheSummaryGivesCountsAndNotAPercentage(t *testing.T) {
	// Same rule as the parity checker. "83% translatable" is not a decision; "17 calls to a vendor jar" is.
	r := scriptChannel(`
var a = new java.util.HashMap();
var b = Packages.com.acme.Thing.go();
ChannelUtil.startChannel("x");
var c = ChannelUtil.getChannelName();
`).ScanJava()

	s := r.Summary()
	if strings.Contains(s, "%") {
		t.Fatalf("the summary should not contain a percentage: %q", s)
	}
	for _, want := range []string{"4 Java references", "1 already supported", "1 rewritable"} {
		if !strings.Contains(s, want) {
			t.Fatalf("the summary should contain %q, got %q", want, s)
		}
	}
}

func TestACleanChannelSaysSoPlainly(t *testing.T) {
	r := scriptChannel(`
var pid = msg['PID']['PID.3']['PID.3.1'].toString();
logger.info('processing ' + pid);
`).ScanJava()

	if r.Blocked() || len(r.Uses) != 0 {
		t.Fatalf("plain JavaScript should produce no findings, got %+v", r.Uses)
	}
	if !strings.Contains(r.Summary(), "No Java found") {
		t.Fatalf("unexpected summary: %q", r.Summary())
	}
}

// TestTheScannerWouldFailIfItStoppedLooking is the deliberate-break check.
//
// docs/queue.md records three assertions in one day that could not have failed, all sharing the shape that the
// assertion and the thing it checked came from the same source. This one pins the behaviour that would silently
// disappear first: if stripNonCode ever blanked code as well as comments, every test above would still pass, because
// they would all correctly find nothing.
func TestTheScannerWouldFailIfItStoppedLooking(t *testing.T) {
	stripped := stripNonCode(`var f = new java.text.SimpleDateFormat("yyyy"); // and a comment`)

	if !strings.Contains(stripped, "java.text.SimpleDateFormat") {
		t.Fatalf("stripNonCode removed the code it was supposed to keep: %q", stripped)
	}
	if strings.Contains(stripped, "and a comment") {
		t.Fatalf("stripNonCode kept the comment it was supposed to blank: %q", stripped)
	}
	if strings.Contains(stripped, "yyyy") {
		t.Fatalf("stripNonCode kept a string literal: %q", stripped)
	}
	if len(stripped) != len(`var f = new java.text.SimpleDateFormat("yyyy"); // and a comment`) {
		t.Fatal("stripNonCode changed the length, so reported columns and lines would drift")
	}
}
