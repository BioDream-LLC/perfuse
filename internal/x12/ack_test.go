package x12

import (
	"strings"
	"testing"
	"time"
)

// A real 270 eligibility enquiry, which is the smallest genuine interchange worth acknowledging.
//
// Written with the standard delimiters. A second copy with unusual ones appears further down, because answering with the
// wrong separator is the mistake that matters most here.
const eligibility270 = "ISA*00*          *00*          *ZZ*SUBMITTERID    " +
	"*ZZ*PAYERID        *260822*0930*^*00501*000000101*0*P*:~" +
	"GS*HS*SUBMITTERID*PAYERID*20260822*0930*101*X*005010X279A1~" +
	"ST*270*0001*005010X279A1~" +
	"BHT*0022*13*TRACK001*20260822*0930~" +
	"HL*1**20*1~" +
	"NM1*PR*2*ACME HEALTH PLAN*****PI*PAYERID~" +
	"HL*2*1*21*1~" +
	"NM1*1P*2*ST JOSEPHS HOSPITAL*****XX*1234567893~" +
	"HL*3*2*22*0~" +
	"TRN*1*TRACE12345*9SUBMITTER~" +
	"NM1*IL*1*TURNER*ROBERT****MI*MEMBER0001~" +
	"DMG*D8*19710304*M~" +
	"DTP*291*D8*20260822~" +
	"EQ*30~" +
	"SE*13*0001~" +
	"GE*1*101~" +
	"IEA*1*000000101~"

// TestOurOwnAcknowledgementPassesOurOwnValidation is the strongest check available.
//
// The parser already knows how to find a bad interchange: mismatched segment counts, control numbers that disagree with
// their trailers. Running that over an acknowledgement we generated catches every counting mistake, and counting is where
// hand-written X12 goes wrong. An acknowledgement that fails the validation it is reporting on is embarrassing in a way a
// trading partner will point out.
func TestOurOwnAcknowledgementPassesOurOwnValidation(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	for _, level := range []AckLevel{Ack997, Ack999, AckTA1} {
		ack, err := Ack(AckOptions{
			Original:        original,
			Level:           level,
			Status:          StatusAccepted,
			SenderID:        "PERFUSE",
			SenderQualifier: "ZZ",
			ControlNumber:   500,
			Now:             time.Date(2026, 8, 22, 9, 35, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("%s: %v", level, err)
		}

		parsed, err := Parse(ack)
		if err != nil {
			t.Fatalf("%s cannot be parsed by our own parser: %v\n%s", level, err, ack)
		}

		v := parsed.Validate()
		if !v.OK() {
			t.Errorf("%s fails our own envelope validation: %v\n%s", level, v.Err(), ack)
		}

		// Structural validity is not enough on its own, and this is not a hypothetical: an early version of Ack asked
		// for segment occurrence 0 where this package counts from 1, so every echoed field came back empty. The
		// acknowledgement still passed the check above, because an ISA with blank receiver fields is structurally
		// perfect - and would have been routed nowhere at all.
		isa, ok := parsed.Segment("ISA", 1)
		if !ok {
			t.Fatalf("%s has no ISA:\n%s", level, ack)
		}
		if strings.TrimSpace(isa.Element(8).String()) == "" {
			t.Errorf("%s names no receiver, so it is valid X12 that goes nowhere:\n%s", level, ack)
		}
		if strings.TrimSpace(isa.Element(6).String()) == "" {
			t.Errorf("%s names no sender:\n%s", level, ack)
		}
		for _, w := range v.Warnings() {
			t.Errorf("%s produced a warning: %s\n%s", level, w, ack)
		}
	}
}

// TestTheAcknowledgementUsesThePartnersDelimiters is the one that would break a real interface.
//
// A partner sending "|" as their element separator has a system configured for it. Answering with "*" produces a file their
// parser reads as one enormous element - and X12 has no way to detect that, so it fails as a data problem rather than a
// syntax one. The separator is a property of the relationship, and the file they sent is the best statement of it.
func TestTheAcknowledgementUsesThePartnersDelimiters(t *testing.T) {
	// The same interchange with unusual delimiters: | for elements, ! for components, \n for segments.
	unusual := strings.NewReplacer("*", "|", ":~", "!\n", "~", "\n").Replace(eligibility270)

	original, err := Parse([]byte(unusual))
	if err != nil {
		t.Fatalf("the unusual-delimiter fixture does not parse, so this test proves nothing: %v", err)
	}
	if original.Delimiters().Element != '|' {
		t.Fatalf("the fixture's element separator is %q, not the pipe this test is about",
			string(original.Delimiters().Element))
	}

	ack, err := Ack(AckOptions{
		Original:        original,
		Level:           Ack999,
		Status:          StatusAccepted,
		SenderID:        "PERFUSE",
		SenderQualifier: "ZZ",
		ControlNumber:   500,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(ack), "ISA|00|") {
		t.Errorf("the acknowledgement did not use the partner's element separator:\n%s", ack)
	}
	if strings.Contains(string(ack), "ISA*00*") {
		t.Errorf("the acknowledgement used the default separator against a partner who does not:\n%s", ack)
	}

	// And it must still be readable, which is the half proving the delimiters were used consistently rather than
	// substituted in one place.
	parsed, err := Parse(ack)
	if err != nil {
		t.Fatalf("the acknowledgement is not parseable: %v\n%s", err, ack)
	}
	if !parsed.Validate().OK() {
		t.Errorf("it does not validate: %v\n%s", parsed.Validate().Err(), ack)
	}
}

// TestA999AndA997AreDifferentDocuments covers a real trap.
//
// The temptation is to treat these as the same document with a different number, and they are not: a 999 sits in a GS with
// functional code HN and reports faults with IK segments, while a 997 uses FA and AK. Mixing the families produces something
// that looks right and that a strict partner rejects.
func TestA999AndA997AreDifferentDocuments(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	build := func(level AckLevel, status AckStatus, problems []Problem) string {
		ack, err := Ack(AckOptions{
			Original: original, Level: level, Status: status, Problems: problems,
			SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
		})
		if err != nil {
			t.Fatal(err)
		}

		return string(ack)
	}

	faults := []Problem{{Segment: "CLM", Message: "segment count does not match", Fatal: false}}

	nine := build(Ack999, StatusAcceptedWithErrors, faults)
	seven := build(Ack997, StatusAcceptedWithErrors, faults)

	// The functional identifier code in GS01.
	if !strings.Contains(nine, "GS*HN*") {
		t.Errorf("a 999 must sit in a GS with functional code HN:\n%s", nine)
	}
	if !strings.Contains(seven, "GS*FA*") {
		t.Errorf("a 997 must sit in a GS with functional code FA:\n%s", seven)
	}

	// The error and trailer segment families.
	if !strings.Contains(nine, "IK3*") || !strings.Contains(nine, "IK5*") {
		t.Errorf("a 999 reports faults with IK segments:\n%s", nine)
	}
	if strings.Contains(nine, "AK3*") || strings.Contains(nine, "AK5*") {
		t.Errorf("a 999 must not use the 997's AK3/AK5:\n%s", nine)
	}
	if !strings.Contains(seven, "AK3*") || !strings.Contains(seven, "AK5*") {
		t.Errorf("a 997 reports faults with AK segments:\n%s", seven)
	}
	if strings.Contains(seven, "IK3*") {
		t.Errorf("a 997 must not use the 999's IK3:\n%s", seven)
	}

	// Both use AK1 and AK9, which is the part that genuinely is shared.
	for name, doc := range map[string]string{"999": nine, "997": seven} {
		if !strings.Contains(doc, "AK1*") || !strings.Contains(doc, "AK9*") {
			t.Errorf("%s is missing AK1 or AK9:\n%s", name, doc)
		}
		if !strings.Contains(doc, "ST*"+name) {
			t.Errorf("%s does not identify itself in ST01:\n%s", name, doc)
		}
	}
}

// TestSenderAndReceiverSwap covers the routing.
//
// Getting this backwards produces a file the partner routes to somebody else or discards, and neither failure produces an
// error anybody sees.
func TestSenderAndReceiverSwap(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusAccepted,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse(ack)
	if err != nil {
		t.Fatal(err)
	}
	isa, ok := parsed.Segment("ISA", 1)
	if !ok {
		t.Fatal("no ISA in our own acknowledgement")
	}

	// ISA06 is us; ISA08 is them. The original's ISA06 was SUBMITTERID.
	if got := strings.TrimSpace(isa.Element(6).String()); got != "PERFUSE" {
		t.Errorf("ISA06 is %q, expected our own identifier", got)
	}
	if got := strings.TrimSpace(isa.Element(8).String()); got != "SUBMITTERID" {
		t.Errorf("ISA08 is %q, expected the original sender", got)
	}

	// ISA14: an acknowledgement must not itself request one, or two systems acknowledge each other for ever.
	if got := strings.TrimSpace(isa.Element(14).String()); got != "0" {
		t.Errorf("ISA14 is %q; the acknowledgement is requesting an acknowledgement", got)
	}
}

// TestTheISAIsFixedWidth covers the format's least forgiving detail.
//
// Every ISA element has an exact width and a partner's parser reads by byte offset - which is the same reason the parser
// reads delimiters from fixed positions rather than scanning. A short element shifts everything after it and produces a
// failure nobody can diagnose from the message.
func TestTheISAIsFixedWidth(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	// A short sender ID, which is the case that would produce a short element.
	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusAccepted,
		SenderID: "PF", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The ISA segment is exactly 106 bytes including its terminator.
	line := string(ack)
	end := strings.IndexByte(line, '~')
	if end < 0 {
		t.Fatal("no segment terminator found")
	}
	isaLength := end + 1
	if isaLength != 106 {
		t.Errorf("the ISA is %d bytes, not the required 106:\n%s", isaLength, line[:min(end+1, len(line))])
	}

	// And an over-long identifier must be truncated rather than allowed to shift the layout.
	long, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusAccepted,
		SenderID:        "THIS-IDENTIFIER-IS-FAR-TOO-LONG-FOR-FIFTEEN",
		SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if end := strings.IndexByte(string(long), '~'); end+1 != 106 {
		t.Errorf("an over-long sender identifier produced a %d byte ISA", end+1)
	}
}

// TestTheStatusMappingDistinguishesAcceptedWithErrorsFromRejected covers a commercially significant distinction.
//
// Accepted-with-errors means the payer is processing the claims and the sender should fix their generator. Rejected means
// nobody is processing anything and the sender must resend. Reporting the first as the second causes duplicate claims, which
// is how a provider ends up on a fraud report.
func TestTheStatusMappingDistinguishesAcceptedWithErrorsFromRejected(t *testing.T) {
	if got := StatusFor(nil); got != StatusAccepted {
		t.Errorf("no problems mapped to %q", got)
	}

	cosmetic := []Problem{{Segment: "GS", Message: "control number does not match its trailer", Fatal: false}}
	if got := StatusFor(cosmetic); got != StatusAcceptedWithErrors {
		t.Errorf("a survivable fault mapped to %q; rejecting it would stop a working revenue cycle", got)
	}

	// A fatal fault means content is missing. Accepting a claims file that is missing claims means the missing ones are
	// never resent and never paid.
	missing := []Problem{{Segment: "SE", Message: "segment count does not match", Fatal: true}}
	if got := StatusFor(missing); got != StatusRejected {
		t.Errorf("a fatal fault mapped to %q; accepting a file missing claims means they are never paid", got)
	}

	// A fatal fault among survivable ones still rejects.
	mixed := append(append([]Problem{}, cosmetic...), missing...)
	if got := StatusFor(mixed); got != StatusRejected {
		t.Errorf("a fatal fault alongside survivable ones mapped to %q", got)
	}
}

// TestPartialIsNotSentInATransactionSetTrailer covers a code-level detail a strict partner checks.
//
// AK5 and IK5 report on one transaction set, which cannot be partially accepted - partial applies to a group of them.
func TestPartialIsNotSentInATransactionSetTrailer(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusPartiallyAccepted,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(ack), "IK5*P") {
		t.Errorf("P was sent in a transaction set trailer, which has no meaning there:\n%s", ack)
	}
	// The group trailer may carry P, which is where it belongs.
	if !strings.Contains(string(ack), "AK9*P") {
		t.Errorf("the group trailer does not carry the partial status:\n%s", ack)
	}
}

// TestADelimiterInAReceivedValueCannotSplitOurSegment is the X12 injection case.
//
// X12 has no escape mechanism at all: a delimiter inside an element is indistinguishable from a delimiter. So a partner
// identifier containing our element separator cannot be encoded, only removed - and leaving it in would let a received value
// split our own segment, acknowledging a claim against the wrong control number.
func TestADelimiterInAReceivedValueCannotSplitOurSegment(t *testing.T) {
	// A partner whose identifier contains the element separator. Contrived, but the point is that nothing stops it
	// arriving and there is no encoding that would make it safe.
	hostile := strings.Replace(eligibility270,
		"*ZZ*PAYERID        *", "*ZZ*PAY*ID         *", 1)

	original, err := Parse([]byte(hostile))
	if err != nil {
		t.Skipf("the hostile fixture does not parse, so there is nothing to test here: %v", err)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusAccepted,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The result must still be a valid interchange. If a received value split a segment, the counts stop matching and
	// our own validation notices - which is exactly why that validation is the assertion here.
	parsed, err := Parse(ack)
	if err != nil {
		t.Fatalf("a received value broke our acknowledgement: %v\n%s", err, ack)
	}
	if v := parsed.Validate(); !v.OK() {
		t.Errorf("a received value made our acknowledgement invalid: %v\n%s", v.Err(), ack)
	}

	isa, _ := parsed.Segment("ISA", 1)
	if strings.TrimSpace(isa.Element(6).String()) != "PERFUSE" {
		t.Errorf("our own identifier moved, so a received value shifted the layout:\n%s", ack)
	}
}

// TestATA1CanAnswerAFileTooBrokenToUnderstand is the whole reason TA1 exists.
//
// It rides directly inside an ISA with no functional group, so it can be sent when the file is too broken to know what group
// it would even belong to. A 999 cannot answer that, because a 999 must name the group it is acknowledging.
func TestATA1CanAnswerAFileTooBrokenToUnderstand(t *testing.T) {
	// An interchange with an envelope and nothing recognisable inside it.
	broken := "ISA*00*          *00*          *ZZ*SUBMITTERID    " +
		"*ZZ*PAYERID        *260822*0930*^*00501*000000101*0*P*:~" +
		"IEA*1*000000101~"

	original, err := Parse([]byte(broken))
	if err != nil {
		t.Fatalf("an envelope with no content should still parse: %v", err)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: AckTA1, Status: StatusRejected,
		Problems:        original.Validate().Problems,
		SenderID:        "PERFUSE",
		SenderQualifier: "ZZ",
		ControlNumber:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(ack), "TA1*") {
		t.Errorf("no TA1 segment:\n%s", ack)
	}
	// No functional group at all, which is the point.
	if strings.Contains(string(ack), "GS*") || strings.Contains(string(ack), "GE*") {
		t.Errorf("a TA1 must not sit inside a functional group:\n%s", ack)
	}
	// IEA01 counts zero groups, and must agree with there being none.
	if !strings.Contains(string(ack), "IEA*0*") {
		t.Errorf("IEA01 does not count zero groups:\n%s", ack)
	}
	if !strings.Contains(string(ack), "TA1*000000101*260822*R*") {
		t.Errorf("the TA1 does not reference the original control number and a rejection:\n%s", ack)
	}
}

// TestAnAcknowledgementRefusesToGuessOurOwnIdentity covers the required sender.
//
// An interchange whose ISA06 the partner does not recognise is discarded before anybody reads it, so a guessed value produces
// silence indistinguishable from not sending anything - and somebody spends a week looking in the wrong place.
func TestAnAcknowledgementRefusesToGuessOurOwnIdentity(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	for _, opts := range []AckOptions{
		{Original: original, SenderID: "", SenderQualifier: "ZZ"},
		{Original: original, SenderID: "PERFUSE", SenderQualifier: ""},
	} {
		if _, err := Ack(opts); err == nil {
			t.Error("an acknowledgement was built without a complete sender identity")
		}
	}

	if _, err := Ack(AckOptions{SenderID: "PERFUSE", SenderQualifier: "ZZ"}); err == nil {
		t.Error("an acknowledgement was built for no interchange")
	}
}

// TestTheTestIndicatorIsEchoed covers the same hazard as v3's processing code.
//
// Answering a test file with a production acknowledgement makes test traffic look live in the partner's own logs.
func TestTheTestIndicatorIsEchoed(t *testing.T) {
	testFile := strings.Replace(eligibility270, "*0*P*:~", "*0*T*:~", 1)

	original, err := Parse([]byte(testFile))
	if err != nil {
		t.Fatal(err)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusAccepted,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse(ack)
	if err != nil {
		t.Fatal(err)
	}
	isa, _ := parsed.Segment("ISA", 1)
	if got := strings.TrimSpace(isa.Element(15).String()); got != "T" {
		t.Errorf("a test interchange was acknowledged with ISA15 %q; its traffic would look live", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

// TestGS08NamesTheImplementationGuide covers a value that reaches a real trading partner.
//
// GS08 is the version, release and industry identifier code, and it names the implementation guide - not the ISA12 version
// with digits appended. An earlier version of Ack produced "005010000" that way, which means nothing: a partner validating
// against the guide rejects it, and that rejection arrives as a negative acknowledgement to our acknowledgement.
func TestGS08NamesTheImplementationGuide(t *testing.T) {
	original, err := Parse([]byte(eligibility270))
	if err != nil {
		t.Fatal(err)
	}

	cases := map[AckLevel]string{
		Ack999: "005010X231A1",
		Ack997: "005010X230",
	}

	for level, want := range cases {
		ack, err := Ack(AckOptions{
			Original: original, Level: level, Status: StatusAccepted,
			SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
		})
		if err != nil {
			t.Fatal(err)
		}

		parsed, err := Parse(ack)
		if err != nil {
			t.Fatal(err)
		}
		gs, ok := parsed.Segment("GS", 1)
		if !ok {
			t.Fatalf("%s has no GS", level)
		}
		if got := strings.TrimSpace(gs.Element(8).String()); got != want {
			t.Errorf("%s GS08 is %q, expected the guide identifier %q", level, got, want)
		}
	}

	// A pre-5010 interchange must not be answered with a 5010 guide identifier, because that guide did not exist and
	// claiming it is worse than stating only the version.
	old := strings.Replace(eligibility270, "*00501*", "*00401*", 1)
	oldMsg, err := Parse([]byte(old))
	if err != nil {
		t.Skipf("the 4010 fixture does not parse: %v", err)
	}
	ack, err := Ack(AckOptions{
		Original: oldMsg, Level: Ack999, Status: StatusAccepted,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ack), "005010X231A1") {
		t.Errorf("a 4010 interchange was answered with a 5010 guide identifier:\n%s", ack)
	}
}

// TestATruncatedFileIsReportedAsTruncated is the case that matters most in this file.
//
// Truncation is the failure the whole envelope check exists to catch, and the acknowledgement has to say so specifically.
// An earlier version derived the code by matching the prose of the message - looking for "segment count" in a message that
// says "SE01 says 13 segment(s) ... but 9 are present". It matched nothing and reported code 8, which tells a partner the
// segment has data element errors. They then look for a bad element in a file whose only problem is that half of it is gone.
func TestATruncatedFileIsReportedAsTruncated(t *testing.T) {
	// The same interchange with segments removed and the counts left alone, which is exactly what a transfer that stopped
	// early looks like.
	truncated := strings.Replace(eligibility270,
		"NM1*IL*1*TURNER*ROBERT****MI*MEMBER0001~DMG*D8*19710304*M~DTP*291*D8*20260822~EQ*30~", "", 1)

	original, err := Parse([]byte(truncated))
	if err != nil {
		t.Fatalf("a truncated file should still parse - that is what makes it dangerous: %v", err)
	}

	v := original.Validate()
	if v.OK() {
		t.Fatal("the truncated fixture passes validation, so this test proves nothing")
	}

	// The validation must carry a code, not only prose.
	found := false
	for _, p := range v.Problems {
		if p.Code == CodeSegmentCountMismatch {
			found = true

			break
		}
	}
	if !found {
		t.Errorf("no problem carries the segment count code; the acknowledgement would fall back to the generic 8: %+v",
			v.Problems)
	}

	ack, err := Ack(AckOptions{
		Original: original, Level: Ack999, Status: StatusFor(v.Problems), Problems: v.Problems,
		SenderID: "PERFUSE", SenderQualifier: "ZZ", ControlNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// IK3 with code 4, which says the segment count does not match.
	if !strings.Contains(string(ack), "IK3*SE***4") {
		t.Errorf("the acknowledgement does not report a segment count mismatch:\n%s", ack)
	}
	if strings.Contains(string(ack), "IK3*SE***8") {
		t.Errorf("a truncated file was reported as having data element errors:\n%s", ack)
	}
	// And rejected, because accepting a file missing claims means they are never resent and never paid.
	if !strings.Contains(string(ack), "IK5*R") || !strings.Contains(string(ack), "AK9*R") {
		t.Errorf("a truncated claims file was not rejected:\n%s", ack)
	}
}
