package mirth

import (
	"strings"
	"testing"
)

// Tests for reading the Java scan as a lock-in audit.
//
// The property under test throughout is that the audit does not overstate itself. It reports a large number and says the large number
// is not the finding. Every test below is an assertion that a reassuring answer stays reassuring, because a tool that inflates its
// findings is worth nothing to the person who has to act on it: the first thing their engineer does is check.

func TestOrdinaryJavaIsNotReportedAsLockIn(t *testing.T) {
	// The common case, and the one where honesty costs something. A channel full of SimpleDateFormat and HashMap looks alarming by
	// reference count and is completely portable - all of it working around a JavaScript engine from 2009.
	r := &JavaReport{
		Uses: []JavaUse{
			{Reference: "java.text.SimpleDateFormat", Verdict: VerdictRewritable},
			{Reference: "java.util.HashMap", Verdict: VerdictRewritable},
			{Reference: "org.apache.commons.lang3.StringUtils", Verdict: VerdictRewritable},
			{Reference: "logger.info", Verdict: VerdictSupported},
		},
		Rewritable: 3,
		Supported:  1,
	}

	p := r.Portability()

	if p.VendorOnly != 0 {
		t.Errorf("VendorOnly = %d, want 0 - none of these need the vendor's server", p.VendorOnly)
	}
	if p.Portable != 4 {
		t.Errorf("Portable = %d, want 4", p.Portable)
	}

	verdict := p.Verdict()
	if !strings.Contains(verdict, "Nothing here is locked in") {
		t.Errorf("a fully portable channel was not reported as portable: %q", verdict)
	}

	// The total is still stated, because somebody who greps their own export will find it and wonder what else was shaped.
	if p.Total != 4 {
		t.Errorf("Total = %d, want 4", p.Total)
	}
}

func TestVendorCallsAreTheNumberReported(t *testing.T) {
	// Five vendor calls among three hundred harmless ones is the case the whole distinction exists for.
	uses := []JavaUse{
		{Reference: "com.mirth.connect.server.controllers.ChannelController", Verdict: VerdictOutOfScope},
		{Reference: "com.mirth.connect.userutil.ImmutableConnectorMessage", Verdict: VerdictOutOfScope},
		{Reference: "com.vendor.hl7.Toolkit", Verdict: VerdictOutOfScope},
	}
	for i := 0; i < 40; i++ {
		uses = append(uses, JavaUse{Reference: "java.util.HashMap", Verdict: VerdictRewritable})
	}

	r := &JavaReport{Uses: uses, OutOfScope: 3, Rewritable: 40}
	p := r.Portability()

	if p.VendorOnly != 3 {
		t.Errorf("VendorOnly = %d, want 3", p.VendorOnly)
	}

	verdict := p.Verdict()
	if !strings.Contains(verdict, "3 references") {
		t.Errorf("the verdict does not lead with the count that matters: %q", verdict)
	}

	// And it has to say the other number is not the finding, in the same sentence. A caveat in a separate paragraph gets quoted
	// without the caveat.
	if !strings.Contains(verdict, "not lock-in") {
		t.Errorf("the verdict does not say the larger number is not lock-in, so it reads as though 43 references are: %q",
			verdict)
	}

	// The specific calls are named, because a count is an assertion and a list is evidence somebody can go and look at.
	if len(p.VendorReferences) != 3 {
		t.Fatalf("VendorReferences = %#v, want 3", p.VendorReferences)
	}
	if p.VendorReferences[0] >= p.VendorReferences[1] {
		t.Error("the vendor references are not sorted, so two runs of the same audit would not compare")
	}
}

func TestTheSameVendorCallTwiceIsNamedOnce(t *testing.T) {
	// A loop calling one controller forty times is one dependency, not forty. Counting references is right for the count and wrong
	// for the list.
	r := &JavaReport{
		Uses: []JavaUse{
			{Reference: "com.mirth.connect.server.userutil.ChannelUtil", Verdict: VerdictOutOfScope, Line: 4},
			{Reference: "com.mirth.connect.server.userutil.ChannelUtil", Verdict: VerdictOutOfScope, Line: 9},
		},
		OutOfScope: 2,
	}

	p := r.Portability()

	if len(p.VendorReferences) != 1 {
		t.Errorf("VendorReferences = %#v, want one entry for one dependency", p.VendorReferences)
	}
	if p.VendorOnly != 2 {
		t.Errorf("VendorOnly = %d, want 2 - the count is of references, and both lines have to be visited", p.VendorOnly)
	}
}

func TestSomethingReachableAnotherWayIsNotCalledLockIn(t *testing.T) {
	// Channel lifecycle is the case. ChannelUtil.startChannel has no script equivalent here and there is an API endpoint that does
	// exactly the same thing, so calling it lock-in would overstate the finding in the direction that flatters the argument.
	r := &JavaReport{
		Uses:         []JavaUse{{Reference: "ChannelUtil.startChannel", Verdict: VerdictNeedsFeature}},
		NeedsFeature: 1,
	}

	p := r.Portability()

	if p.VendorOnly != 0 {
		t.Errorf("VendorOnly = %d, want 0 - this is reachable another way", p.VendorOnly)
	}
	if p.NeedsAnotherRoute != 1 {
		t.Errorf("NeedsAnotherRoute = %d, want 1", p.NeedsAnotherRoute)
	}

	if v := p.Verdict(); !strings.Contains(v, "Nothing here is locked to a vendor") {
		t.Errorf("a channel needing only another route was reported as locked in: %q", v)
	}
}

func TestAnExternalScriptIsReportedAsUnknownRatherThanGuessed(t *testing.T) {
	// A path on the old server could hold anything. Both "assume it is fine" and "assume it is lock-in" are guesses presented as
	// findings, and this is the one place where the honest answer is that somebody has to go and fetch the file.
	r := &JavaReport{ExternalScripts: []string{"/opt/mirth/scripts/transform.js"}}

	p := r.Portability()

	if p.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1", p.Unknown)
	}

	v := p.Verdict()
	if !strings.Contains(v, "unknown") {
		t.Errorf("the verdict does not say the contents are unknown: %q", v)
	}
	if !strings.Contains(v, "before concluding") {
		t.Errorf("the verdict does not say what to do about it: %q", v)
	}
}

func TestAChannelWithNoJavaSaysSoPlainly(t *testing.T) {
	p := (&JavaReport{}).Portability()

	if v := p.Verdict(); !strings.Contains(v, "no Java at all") {
		t.Errorf("a channel with no Java did not say so: %q", v)
	}
}

func TestOneReferenceReadsAsOne(t *testing.T) {
	// Grammar, and it matters more than it sounds: "1 references do" in a document shown to whoever signs a renewal undermines
	// every number beside it.
	r := &JavaReport{
		Uses:       []JavaUse{{Reference: "com.mirth.connect.userutil.Foo", Verdict: VerdictOutOfScope}},
		OutOfScope: 1,
	}

	v := r.Portability().Verdict()

	if strings.Contains(v, "1 references") {
		t.Errorf("a single reference is reported in the plural: %q", v)
	}
	if !strings.Contains(v, "1 reference does") {
		t.Errorf("a single reference does not read naturally: %q", v)
	}
}

func TestTheVerdictReadsAsASentenceForOneAndForMany(t *testing.T) {
	// Grammar, tested because the first version of this said "All 1 Java reference work around an old JavaScript engine". This
	// document is shown to whoever signs a renewal, and a sentence that does not parse undermines every number beside it.
	one := (&JavaReport{
		Uses:       []JavaUse{{Reference: "java.util.HashMap", Verdict: VerdictRewritable}},
		Rewritable: 1,
	}).Portability().Verdict()

	if !strings.Contains(one, "1 Java reference works around") {
		t.Errorf("one reference does not read as one: %q", one)
	}

	many := (&JavaReport{
		Uses: []JavaUse{
			{Reference: "java.util.HashMap", Verdict: VerdictRewritable},
			{Reference: "java.text.SimpleDateFormat", Verdict: VerdictRewritable},
		},
		Rewritable: 2,
	}).Portability().Verdict()

	if !strings.Contains(many, "2 Java references work around") {
		t.Errorf("two references do not read as two: %q", many)
	}
}
