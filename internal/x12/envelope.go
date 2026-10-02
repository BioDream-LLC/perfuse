package x12

import (
	"fmt"
	"strconv"
	"strings"
)

// Envelope validation.
//
// X12 carries its own counts. SE01 states how many segments were in the transaction
// set, GE01 how many transaction sets were in the group, IEA01 how many groups were in
// the interchange. Control numbers are repeated at both ends of each envelope: ISA13
// must match IEA02, GS06 must match GE02, ST02 must match SE02.
//
// All of that exists for one reason - so a truncated or spliced file can be detected -
// and a parser that ignores it throws away the format's best safety feature.
//
// This is the X12 version of the problem the SFTP source has with a half-written file.
// Half an 837 parses perfectly well. It is simply missing claims, and nothing about the
// remaining structure looks wrong. Without the counts, the first sign of trouble is a
// payer reporting fewer claims than were sent, weeks later, with no way to tell which
// ones went missing.

// Problem is one envelope fault.
type Problem struct {
	// Segment is where the fault was found, for example "SE" or "IEA".
	Segment string
	// Message explains what is wrong in terms somebody can act on.
	Message string
	// Fatal marks a fault that means the file cannot be trusted at all, as against
	// one worth reporting but survivable.
	//
	// The distinction matters because the two call for different handling: a
	// mismatched segment count means content is missing and the file must be rejected
	// to the sender, while a control number that does not match its own trailer is
	// usually a generation bug in an otherwise complete file.
	Fatal bool

	// Code is the X12 syntax error code for this fault, from data element 720.
	//
	// Present because an acknowledgement has to report a code, and deriving one by matching the prose above was the
	// first attempt: it broke as soon as a message was worded differently from the pattern, silently downgrading a
	// specific fault to the generic "has data element errors". A partner told code 8 goes looking for the wrong kind of
	// problem.
	//
	// Empty means no specific code applies, and the acknowledgement uses 8 deliberately rather than by accident.
	Code string
}

// X12 syntax error codes, from data element 720. Only the ones this parser can actually detect are named - a code we cannot
// substantiate would send a trading partner looking for a fault that is not there.
const (
	// CodeMissingTrailer is 2: the transaction set trailer is missing.
	CodeMissingTrailer = "2"

	// CodeControlNumberMismatch is 3: the control number in the header and trailer disagree.
	CodeControlNumberMismatch = "3"

	// CodeSegmentCountMismatch is 4: the stated segment count does not match what is present.
	//
	// The most important one to get right, because it is what a truncated file produces, and truncation is the failure
	// this whole envelope check exists to catch.
	CodeSegmentCountMismatch = "4"

	// CodeSetCountMismatch is 5: the stated number of transaction sets does not match.
	CodeSetCountMismatch = "5"
)

func (p Problem) String() string {
	kind := "warning"
	if p.Fatal {
		kind = "error"
	}
	return fmt.Sprintf("%s: %s: %s", kind, p.Segment, p.Message)
}

// Validation is the result of checking an interchange's envelope.
type Validation struct {
	Problems []Problem
	// Counts are what was found, for reporting alongside what was expected.
	Segments         int
	TransactionSets  int
	FunctionalGroups int
}

// OK reports whether the interchange is free of fatal faults.
func (v Validation) OK() bool {
	for _, p := range v.Problems {
		if p.Fatal {
			return false
		}
	}
	return true
}

// Err returns a single error describing every fatal fault, or nil.
//
// Every fault, not the first. A trading partner given one problem at a time will send
// four more broken files, and each round trip is a day.
func (v Validation) Err() error {
	var fatal []string
	for _, p := range v.Problems {
		if p.Fatal {
			fatal = append(fatal, p.Segment+": "+p.Message)
		}
	}
	if len(fatal) == 0 {
		return nil
	}
	return fmt.Errorf("x12: the interchange envelope is not intact:\n  %s", strings.Join(fatal, "\n  "))
}

// Warnings returns the non-fatal problems.
func (v Validation) Warnings() []string {
	var out []string
	for _, p := range v.Problems {
		if !p.Fatal {
			out = append(out, p.Segment+": "+p.Message)
		}
	}
	return out
}

// Validate checks the envelope structure and its self-declared counts.
func (m *Message) Validate() Validation {
	v := Validation{Segments: len(m.segs)}

	v.checkInterchange(m)
	v.checkGroups(m)
	v.checkTransactionSets(m)

	return v
}

func (v *Validation) add(segment, message string, fatal bool) {
	v.addCoded(segment, message, fatal, "")
}

// addCoded records a problem with its X12 syntax error code.
func (v *Validation) addCoded(segment, message string, fatal bool, code string) {
	v.Problems = append(v.Problems, Problem{Segment: segment, Message: message, Fatal: fatal, Code: code})
}

// checkInterchange verifies ISA against IEA.
func (v *Validation) checkInterchange(m *Message) {
	isa, hasISA := m.Segment("ISA", 1)
	iea, hasIEA := m.Segment("IEA", 1)

	if !hasIEA {
		// Fatal, and the most likely single cause of a bad X12 file: the transfer
		// stopped early. Everything before it may be perfectly valid, which is
		// exactly why it must not be accepted.
		v.add("IEA", "the interchange has no IEA trailer, so it is incomplete - most often a transfer that stopped early", true)
		return
	}
	if !hasISA {
		return // Parse already refused anything without an ISA.
	}

	// IEA01 is the number of functional groups.
	groups := m.Segments("GS")
	v.FunctionalGroups = len(groups)
	if want, ok := intElement(iea, 1); ok && want != len(groups) {
		v.add("IEA", fmt.Sprintf(
			"IEA01 says %d functional group(s) but %d are present; content is missing or duplicated",
			want, len(groups)), true)
	}

	// ISA13 and IEA02 are the same control number written twice.
	//
	// A mismatch is usually a generation bug rather than a truncation - the file is
	// complete but assembled wrongly - so it is reported without being fatal. Being
	// strict here would reject otherwise usable files from a partner whose software
	// has a bug they will not fix quickly.
	sent := strings.TrimSpace(isa.Element(13).String())
	back := strings.TrimSpace(iea.Element(2).String())
	if sent != "" && back != "" && sent != back {
		v.add("IEA", fmt.Sprintf(
			"the interchange control number is %q in ISA13 and %q in IEA02; they are the same number written twice and should match",
			sent, back), false)
	}
}

// checkGroups verifies each GS against its GE.
func (v *Validation) checkGroups(m *Message) {
	groups := m.Segments("GS")
	ends := m.Segments("GE")

	if len(groups) != len(ends) {
		v.add("GE", fmt.Sprintf(
			"%d GS header(s) and %d GE trailer(s); every functional group needs both",
			len(groups), len(ends)), true)
		return
	}

	// Transaction sets are counted per group by walking the segments in order, since
	// a bare count of ST segments cannot tell which group each belongs to.
	perGroup := transactionSetsPerGroup(m)

	for i, gs := range groups {
		ge := ends[i]

		if want, ok := intElement(ge, 1); ok {
			got := 0
			if i < len(perGroup) {
				got = perGroup[i]
			}
			if want != got {
				v.add("GE", fmt.Sprintf(
					"GE01 says %d transaction set(s) in functional group %d but %d are present; claims or remittances are missing",
					want, i+1, got), true)
			}
		}

		sent := strings.TrimSpace(gs.Element(6).String())
		back := strings.TrimSpace(ge.Element(2).String())
		if sent != "" && back != "" && sent != back {
			v.add("GE", fmt.Sprintf(
				"the group control number is %q in GS06 and %q in GE02", sent, back), false)
		}
	}
}

// transactionSetsPerGroup counts ST segments between each GS and its GE.
func transactionSetsPerGroup(m *Message) []int {
	var out []int
	current := -1
	for _, s := range m.segs {
		switch s.ID {
		case "GS":
			out = append(out, 0)
			current = len(out) - 1
		case "ST":
			if current >= 0 {
				out[current]++
			}
		case "GE":
			current = -1
		}
	}
	return out
}

// checkTransactionSets verifies each ST against its SE.
func (v *Validation) checkTransactionSets(m *Message) {
	starts := m.Segments("ST")
	v.TransactionSets = len(starts)

	ends := m.Segments("SE")
	if len(starts) != len(ends) {
		v.addCoded("SE", fmt.Sprintf(
			"%d ST header(s) and %d SE trailer(s); every transaction set needs both",
			len(starts), len(ends)), true, CodeMissingTrailer)

		return
	}

	counts := segmentsPerTransactionSet(m)

	for i, st := range starts {
		se := ends[i]

		// SE01 counts every segment from ST to SE inclusive. This is the check that
		// actually catches a truncated file, because it is the only one that knows how
		// much should have been in the middle.
		if want, ok := intElement(se, 1); ok {
			got := 0
			if i < len(counts) {
				got = counts[i]
			}
			if want != got {
				v.addCoded("SE", fmt.Sprintf(
					"SE01 says %d segment(s) in transaction set %s but %d are present; the transaction set is truncated or has been altered",
					want, strings.TrimSpace(se.Element(2).String()), got), true, CodeSegmentCountMismatch)
			}
		}

		sent := strings.TrimSpace(st.Element(2).String())
		back := strings.TrimSpace(se.Element(2).String())
		if sent != "" && back != "" && sent != back {
			v.addCoded("SE", fmt.Sprintf(
				"the transaction set control number is %q in ST02 and %q in SE02", sent, back), false,
				CodeControlNumberMismatch)
		}
	}
}

// segmentsPerTransactionSet counts segments from each ST to its SE inclusive.
func segmentsPerTransactionSet(m *Message) []int {
	var out []int
	counting := false
	n := 0
	for _, s := range m.segs {
		switch {
		case s.ID == "ST":
			counting = true
			n = 1
		case s.ID == "SE" && counting:
			n++
			out = append(out, n)
			counting = false
		case counting:
			n++
		}
	}
	// An ST with no SE: record what was counted so the caller sees a mismatch rather
	// than a missing entry, which would silently pass.
	if counting {
		out = append(out, n)
	}
	return out
}

// intElement reads an element as an integer.
//
// Reports failure rather than defaulting to zero. A non-numeric count is a fault in
// its own right, and treating it as zero would turn it into a spurious mismatch that
// sends somebody looking for missing segments that were never missing.
func intElement(s Segment, n int) (int, bool) {
	raw := strings.TrimSpace(s.Element(n).String())
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return v, true
}
