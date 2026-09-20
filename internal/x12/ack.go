package x12

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Acknowledgements. A trading partner who sends a claims file and receives nothing does not know whether the money is
// coming, and will eventually resend - which is how a payer ends up with duplicate claims and a provider ends up on a
// fraud report.
//
// X12 has three acknowledgements and they answer different questions. Sending the wrong one is worse than sending none,
// because it answers a question nobody asked and leaves the real one open.
//
//   - TA1 acknowledges the interchange envelope itself: did the ISA and IEA make sense. It is the only one that can be
//     sent when the file is too broken to identify what is inside it, and it rides inside an ISA/IEA of its own.
//   - 997 Functional Acknowledgement reports on syntax: were the segments in a permitted order, were the elements the
//     right type and length. It is the older of the two and still what many partners expect.
//   - 999 Implementation Acknowledgement reports on syntax and on conformance to a HIPAA implementation guide, which is
//     a stricter question. Mandated for HIPAA transactions, and what a clearinghouse will usually want back for an 837.
//
// # Why the 997 is still here
//
// The 999 supersedes the 997 for HIPAA transactions, so the temptation is to implement only the newer one. That would be a
// mistake: plenty of trading partners - particularly for non-HIPAA transactions and older payer connections - are
// configured to expect a 997 and will treat a 999 as an unrecognised file. Which one to send is a property of the trading
// partner relationship, not of the message, so it is a setting rather than a decision this code makes.

// AckLevel says which acknowledgement to generate.
type AckLevel string

const (
	// AckTA1 acknowledges only the interchange envelope.
	AckTA1 AckLevel = "TA1"

	// Ack997 is the Functional Acknowledgement.
	Ack997 AckLevel = "997"

	// Ack999 is the Implementation Acknowledgement, and what HIPAA transactions expect.
	Ack999 AckLevel = "999"
)

// AckStatus is the outcome being reported.
type AckStatus string

const (
	// StatusAccepted means the whole thing was taken: AK501/IK501 code A.
	StatusAccepted AckStatus = "A"

	// StatusAcceptedWithErrors means it was taken and something in it was wrong: code E.
	//
	// Distinct from rejection in a way that matters commercially. Accepted-with-errors means the payer is processing the
	// claims and the sender should fix their generator; rejected means nobody is processing anything and the sender must
	// resend. Reporting the first as the second causes duplicate claims.
	StatusAcceptedWithErrors AckStatus = "E"

	// StatusPartiallyAccepted means some transaction sets were taken and some were not: code P.
	StatusPartiallyAccepted AckStatus = "P"

	// StatusRejected means none of it was taken: code R.
	StatusRejected AckStatus = "R"
)

// AckOptions configures an acknowledgement.
type AckOptions struct {
	// Original is the interchange being acknowledged.
	Original *Message

	// Level says which acknowledgement to build.
	Level AckLevel

	// Status is the outcome. Use StatusFor rather than choosing by hand.
	Status AckStatus

	// Problems are the faults to report. Usually the output of Validate.
	Problems []Problem

	// SenderID and SenderQualifier are our own interchange identifier, becoming ISA06 and ISA05.
	//
	// Required. These must be the values the trading partner has configured for us, and there is no way to guess them:
	// an interchange whose ISA06 the partner does not recognise is discarded before anybody reads it, so a wrong value
	// here produces silence that looks exactly like not sending anything.
	SenderID        string
	SenderQualifier string

	// ControlNumber is our interchange control number, ISA13.
	//
	// Should increase and should not repeat, because a partner detecting a duplicate control number will usually discard
	// the interchange as an accidental resend.
	ControlNumber int

	// GroupControlNumber is GS06. Defaults to ControlNumber when zero.
	GroupControlNumber int

	// TestIndicator sets ISA15: T for test, P for production.
	//
	// Echoed from the original by StatusFor's caller rather than fixed, for the same reason as v3's processing code:
	// answering a test file with a production acknowledgement makes test traffic look live in the partner's logs.
	TestIndicator string

	// Now overrides the clock, for tests.
	Now time.Time
}

// StatusFor maps validation problems to an acknowledgement status.
//
// A fatal problem means the content cannot be trusted, so the file is rejected: a mismatched segment count means segments
// are missing, and accepting a claims file that is missing claims means the missing ones are never resent and never paid.
//
// A non-fatal problem is accepted-with-errors, because the content is complete and usable while something about its
// generation is wrong. Rejecting those would stop a working revenue cycle over a cosmetic fault.
func StatusFor(problems []Problem) AckStatus {
	fatal := false
	for _, p := range problems {
		if p.Fatal {
			fatal = true

			break
		}
	}

	switch {
	case fatal:
		return StatusRejected
	case len(problems) > 0:
		return StatusAcceptedWithErrors
	default:
		return StatusAccepted
	}
}

// Ack builds an acknowledgement interchange.
//
// Uses the original interchange's delimiters. That is deliberate: a partner who sends "|" as their element separator has a
// system configured for it, and answering with "*" tends to produce a file their parser reads as one enormous element. The
// separator is a property of the relationship, and the original file is the best available statement of it.
func Ack(opts AckOptions) ([]byte, error) {
	if opts.Original == nil {
		return nil, fmt.Errorf("no interchange to acknowledge")
	}
	if opts.SenderID == "" || opts.SenderQualifier == "" {
		// Refused rather than defaulted. An interchange whose ISA06 the partner does not recognise is discarded before
		// anybody reads it, so a guessed value produces silence indistinguishable from sending nothing at all - and
		// somebody spends a week looking in the wrong place.
		return nil, fmt.Errorf("a sender ID and qualifier are required; these must be the values the trading " +
			"partner has configured for us, and an interchange they do not recognise is discarded unread")
	}

	level := opts.Level
	if level == "" {
		level = Ack999
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	d := opts.Original.Delimiters()
	// A TA1 may be answering a file too broken to have yielded delimiters. Fall back to the common ones rather than
	// writing an interchange with a zero byte in it.
	if d.Element == 0 {
		d = Delimiters{Element: '*', Component: ':', Segment: '~'}
	}

	// Segment occurrences and element positions are both one-based here, matching every X12 implementation guide and how
	// HL7 is handled elsewhere in this codebase. The first version of this file asked for occurrence 0, which returns
	// nothing - so every echoed field came out empty and the acknowledgement still passed structural validation because
	// an ISA with blank receiver fields is structurally fine. It would have been routed nowhere.
	isa, _ := opts.Original.Segment("ISA", 1)

	// Their identifier becomes our receiver, and ours becomes the sender. Getting this backwards produces a file the
	// partner routes to somebody else, or discards.
	theirQualifier := elementString(isa, 5)
	theirID := elementString(isa, 6)

	test := opts.TestIndicator
	if test == "" {
		test = elementString(isa, 15)
	}
	if test == "" {
		test = "P"
	}

	control := opts.ControlNumber
	if control <= 0 {
		// Zero is not a valid interchange control number and a partner may discard it. Derived from the original's so
		// the two are traceable to each other, which is more use in a log than a random number.
		if n, err := strconv.Atoi(strings.TrimSpace(elementString(isa, 13))); err == nil && n > 0 {
			control = n
		} else {
			control = 1
		}
	}
	groupControl := opts.GroupControlNumber
	if groupControl <= 0 {
		groupControl = control
	}

	var b strings.Builder

	// ISA is fixed width: every element has an exact length and a partner's parser reads by byte offset. A short element
	// shifts everything after it, which is why these are padded rather than written as they come.
	writeISA(&b, d, isaFields{
		SenderQualifier:   opts.SenderQualifier,
		SenderID:          opts.SenderID,
		ReceiverQualifier: theirQualifier,
		ReceiverID:        theirID,
		Now:               now,
		ControlNumber:     control,
		TestIndicator:     test,
		Version:           versionOrDefault(opts.Original.Version()),
	})

	if level == AckTA1 {
		// A TA1 rides directly inside the interchange with no functional group, which is the whole point of it: it can
		// be sent when the file is too broken to know what functional group it would belong to.
		writeSegment(&b, d, "TA1",
			strings.TrimSpace(elementString(isa, 13)),
			strings.TrimSpace(elementString(isa, 9)),
			ta1Code(opts.Status),
			ta1NoteCode(opts.Problems))
		writeSegment(&b, d, "IEA", "0", padControl(control))

		return []byte(b.String()), nil
	}

	return buildFunctionalAck(opts, level, d, isa, now, control, groupControl)
}

// isaFields are the values that go into an ISA.
type isaFields struct {
	SenderQualifier   string
	SenderID          string
	ReceiverQualifier string
	ReceiverID        string
	Now               time.Time
	ControlNumber     int
	TestIndicator     string
	Version           string
}

// writeISA writes the fixed-width interchange header.
//
// Every element has an exact width and a partner's parser reads ISA by byte offset - which is the same reason Parse reads
// the delimiters from fixed positions. A short element shifts everything after it and produces a file that fails in a way
// nobody can diagnose from the error.
func writeISA(b *strings.Builder, d Delimiters, f isaFields) {
	e := string(d.Element)

	repeat := "^"
	if d.Repeat != 0 {
		repeat = string(d.Repeat)
	}

	fields := []string{
		"ISA",
		"00",                        // ISA01 authorisation information qualifier
		pad("", 10),                 // ISA02
		"00",                        // ISA03 security information qualifier
		pad("", 10),                 // ISA04
		pad(f.SenderQualifier, 2),   // ISA05
		pad(f.SenderID, 15),         // ISA06
		pad(f.ReceiverQualifier, 2), // ISA07
		pad(f.ReceiverID, 15),       // ISA08
		f.Now.Format("060102"),      // ISA09
		f.Now.Format("1504"),        // ISA10
		repeat,                      // ISA11
		f.Version,                   // ISA12
		padControl(f.ControlNumber), // ISA13
		"0",                         // ISA14 acknowledgement requested: no, or two systems acknowledge for ever
		f.TestIndicator,             // ISA15
		string(d.Component),         // ISA16
	}

	b.WriteString(strings.Join(fields, e))
	b.WriteByte(d.Segment)
}

// buildFunctionalAck writes a 997 or a 999.
func buildFunctionalAck(
	opts AckOptions, level AckLevel, d Delimiters, isa Segment,
	now time.Time, control, groupControl int,
) ([]byte, error) {
	const setControl = "0001"

	var b strings.Builder

	theirQualifier := elementString(isa, 5)
	theirID := elementString(isa, 6)

	test := opts.TestIndicator
	if test == "" {
		test = elementString(isa, 15)
	}
	if test == "" {
		test = "P"
	}

	writeISA(&b, d, isaFields{
		SenderQualifier:   opts.SenderQualifier,
		SenderID:          opts.SenderID,
		ReceiverQualifier: theirQualifier,
		ReceiverID:        theirID,
		Now:               now,
		ControlNumber:     control,
		TestIndicator:     test,
		Version:           versionOrDefault(opts.Original.Version()),
	})

	functionalCode := "FA"
	if level == Ack999 {
		functionalCode = "HN"
	}

	writeSegment(&b, d, "GS",
		functionalCode, opts.SenderID, theirID,
		now.Format("20060102"), now.Format("1504"),
		strconv.Itoa(groupControl), "X",
		guideIdentifier(level, opts.Original.Version()))

	// Count segments from ST to SE inclusive, because SE01 states that count and a partner checks it. Getting it wrong
	// makes our own acknowledgement fail the very validation it is reporting on.
	segments := 0

	writeSegment(&b, d, "ST", string(level), setControl)
	segments++

	gs, hasGS := opts.Original.Segment("GS", 1)
	originalFunctional, originalGroupControl := "", ""
	if hasGS {
		originalFunctional = elementString(gs, 1)
		originalGroupControl = elementString(gs, 6)
	}
	writeSegment(&b, d, "AK1", originalFunctional, originalGroupControl)
	segments++

	// AK2 opens a report on one transaction set, and keeps that name in both the 997 and the 999.
	//
	// What differs is the error detail and the trailer: a 997 uses AK3 for a segment fault, AK4 for an element fault and
	// AK5 to close the transaction set. A 999 uses IK3, IK4 and IK5 for the same three things. Mixing the two families
	// produces a document that looks right and that a strict partner rejects.
	segmentPrefix := "AK"
	if level == Ack999 {
		segmentPrefix = "IK"
	}

	for _, st := range opts.Original.Segments("ST") {
		writeSegment(&b, d, "AK2", elementString(st, 1), elementString(st, 2))
		segments++

		// Segment-level faults. Only written when there are any: an empty AK3 is not valid.
		for _, p := range opts.Problems {
			if p.Segment == "" || p.Segment == "ISA" || p.Segment == "IEA" {
				// Envelope faults belong in a TA1, not here. Reporting an ISA fault as a transaction set fault sends
				// the partner looking in the wrong place.
				continue
			}
			// AK3/IK3: segment ID, position in the transaction set, loop identifier, syntax error code.
			//
			// The position is left empty rather than guessed. A wrong position sends somebody to the wrong line of a
			// file with thousands of them, which is worse than no position at all.
			writeSegment(&b, d, segmentPrefix+"3", p.Segment, "", "", syntaxErrorCode(p))
			segments++
		}

		// AK5/IK5 closes the transaction set with its status.
		writeSegment(&b, d, segmentPrefix+"5", string(transactionSetStatus(opts.Status)))
		segments++
	}

	// AK9 closes the group in both documents.
	included := len(opts.Original.Segments("ST"))
	accepted := included
	if opts.Status == StatusRejected {
		accepted = 0
	}
	writeSegment(&b, d, "AK9",
		string(opts.Status),
		strconv.Itoa(included),
		strconv.Itoa(included),
		strconv.Itoa(accepted))
	segments++

	// SE01 is the segment count including ST and SE themselves.
	segments++ // the SE about to be written
	writeSegment(&b, d, "SE", strconv.Itoa(segments), setControl)

	writeSegment(&b, d, "GE", "1", strconv.Itoa(groupControl))
	writeSegment(&b, d, "IEA", "1", padControl(control))

	return []byte(b.String()), nil
}

// transactionSetStatus maps a group status to a transaction set status.
//
// AK5 and IK5 do not accept P: a single transaction set is either accepted, accepted with errors, or rejected. Partial
// applies to a group of them. Passing P through produces a code a strict partner rejects.
func transactionSetStatus(s AckStatus) AckStatus {
	if s == StatusPartiallyAccepted {
		return StatusAcceptedWithErrors
	}

	return s
}

// syntaxErrorCode maps a problem to an AK3/IK3 syntax error code.
//
// The codes are from the X12 data element 720 table. Only the ones this parser can actually detect are mapped, and anything
// else becomes 8 "segment has data element errors" rather than a guess - a wrong code tells the partner to look for the
// wrong kind of fault.
func syntaxErrorCode(p Problem) string {
	// Taken from the problem rather than derived from its wording.
	//
	// The first version matched on the prose - looking for "segment count" in a message that actually said "SE01 says 13
	// segment(s) ... but 9 are present". It found nothing and silently reported code 8, which tells a partner the segment
	// has data element errors when the real fault is that their file was cut short. They then go looking for a bad
	// element in a file whose only problem is that half of it is missing.
	if p.Code != "" {
		return p.Code
	}

	// 8: segment has data element errors. Used when nothing more specific can be substantiated, which is honest - a code
	// we cannot support sends somebody looking for a fault that is not there.
	return "8"
}

// ta1Code maps a status to TA1's interchange acknowledgement code.
//
// TA1 has only three: A accepted, E accepted with errors, R rejected. Partial has no meaning for an envelope.
func ta1Code(s AckStatus) string {
	switch s {
	case StatusAccepted:
		return "A"
	case StatusRejected:
		return "R"
	default:
		return "E"
	}
}

// ta1NoteCode gives TA1's interchange note code.
//
// 000 means no error. Only the envelope faults this parser detects are mapped; anything else becomes 024 "invalid
// interchange content", which is honest about not knowing more.
func ta1NoteCode(problems []Problem) string {
	for _, p := range problems {
		if p.Segment != "ISA" && p.Segment != "IEA" {
			// TA1 reports on the envelope only. A transaction set fault belongs in a 999, and reporting it here
			// sends the partner looking at their ISA when the fault is inside the file.
			continue
		}
		switch p.Code {
		case CodeSetCountMismatch:
			// 021: number of included groups does not match the actual count.
			return "021"
		case CodeControlNumberMismatch:
			// 022: interchange control number in the header and trailer do not match.
			return "022"
		}

		// 024: invalid interchange content. Honest about knowing there is a fault and not which.
		return "024"
	}

	return "000"
}

// writeSegment writes one segment with its terminator, trimming trailing empty elements.
//
// Trailing empties are trimmed because X12 treats an omitted trailing element and an empty one as the same thing, and every
// partner's parser handles the shorter form while a few object to trailing separators.
func writeSegment(b *strings.Builder, d Delimiters, id string, elements ...string) {
	for len(elements) > 0 && elements[len(elements)-1] == "" {
		elements = elements[:len(elements)-1]
	}

	b.WriteString(id)
	for _, e := range elements {
		b.WriteByte(d.Element)
		// Sanitised rather than escaped. X12 has no escape mechanism at all: a delimiter inside an element is
		// indistinguishable from a delimiter, so the only safe handling is to remove it. Leaving it in would let a
		// received value split our own segment, which is the X12 equivalent of the XML injection this codebase already
		// closed once.
		b.WriteString(stripDelimiters(e, d))
	}
	b.WriteByte(d.Segment)
}

// stripDelimiters removes any delimiter from a value.
//
// X12 has no escape sequence. A partner's identifier containing our element separator cannot be encoded, only removed, and
// removing it is far better than emitting a segment that splits in the wrong place - a claim acknowledged against the wrong
// control number is worse than one acknowledged with a truncated identifier.
func stripDelimiters(s string, d Delimiters) string {
	replacer := make([]string, 0, 8)
	for _, c := range []byte{d.Element, d.Component, d.Segment, d.Repeat} {
		if c != 0 {
			replacer = append(replacer, string(c), "")
		}
	}

	return strings.NewReplacer(replacer...).Replace(s)
}

// pad fixes a value to an exact width, which ISA requires.
func pad(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}

	return s + strings.Repeat(" ", width-len(s))
}

// padControl formats a control number to ISA13's nine digits.
func padControl(n int) string { return fmt.Sprintf("%09d", n) }

// versionOrDefault gives ISA12, falling back to a version every partner understands.
func versionOrDefault(v string) string {
	v = strings.TrimSpace(v)
	if len(v) == 5 {
		return v
	}

	// 00501 is the HIPAA-mandated version and the safest default. An acknowledgement claiming a version the partner does
	// not support is rejected by the parser rather than by a person.
	return "00501"
}

// elementString reads an element as a trimmed string, or empty when it is not there.
func elementString(s Segment, n int) string {
	if s.ID == "" {
		return ""
	}

	return strings.TrimSpace(s.Element(n).String())
}

// guideIdentifier gives GS08, the version, release and industry identifier code.
//
// This is not the ISA12 version with digits appended, which is what an earlier version of this file did - producing
// "005010000" and reaching a real trading partner as a code meaning nothing. GS08 names the implementation guide, and for a
// HIPAA acknowledgement that guide has its own published identifier:
//
//   - 005010X231A1 for the 999 Implementation Acknowledgement
//   - 005010X230 for the 997 Functional Acknowledgement under HIPAA
//
// A partner validating against the guide will reject a GS08 it does not recognise, and that rejection arrives as a
// negative acknowledgement to our acknowledgement, which is a confusing thing to debug.
//
// For a pre-5010 interchange the bare version is returned instead, because those guide identifiers do not apply and
// claiming a 5010 guide over a 4010 interchange would be worse than saying only the version.
func guideIdentifier(level AckLevel, isaVersion string) string {
	version := versionOrDefault(isaVersion)
	if version != "00501" {
		// A 4010 or older interchange. Answer in its own version rather than asserting a guide that did not exist.
		return version + "0"
	}

	switch level {
	case Ack999:
		return "005010X231A1"
	case Ack997:
		return "005010X230"
	default:
		return "005010"
	}
}
