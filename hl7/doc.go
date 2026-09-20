// Package hl7 reads HL7 version 2 messages.
//
// It has no dependencies outside the standard library, and a test enforces that. It does no network I/O, writes no
// files and starts no goroutines, so it is safe to use from anywhere.
//
// # Reading a message
//
//	msg, err := hl7.Parse(raw)
//	if err != nil {
//		return err
//	}
//	mrn := msg.MustGet("PID-3.1")
//	name := msg.MustGet("PID-5.1")
//	msgType, event, _ := msg.Type()   // "ADT", "A01"
//
// Paths are the notation people already use when they talk about HL7: segment, field, then optional component and
// subcomponent, as in PID-5.1 or OBX-5.2.3. Repeats are addressed with square brackets, as in PID-3[2].1.
//
// Get returns an error for a path it cannot parse; MustGet returns the empty string instead. Both are safe on absent
// segments and fields, because a real feed is full of both and a parser that panics on missing data is useless for
// the job. Absence is not an error — a message that simply does not carry PID-8 yields "" rather than a failure.
//
// # Absent and empty
//
// HL7 distinguishes a field that was not sent from one sent deliberately empty, and clinically those can mean
// different things - "no allergies recorded" is not "no known allergies". Value.Exists reports the difference where
// callers need it, while String flattens both to "" for the common case.
//
// # Copying, and the one way to get this wrong
//
// Parse copies its input, so the returned Message stays valid however the caller reuses its buffer. That costs
// about 3% and one allocation per message.
//
// WithZeroCopy skips the copy and keeps a reference to the caller's slice instead. It is faster, and it makes the
// Message unsafe to use after that slice is written again. Reading into a reused buffer is the normal, efficient way
// to read from a network in Go, so the trap catches careful code, and the symptom is silently wrong field values
// rather than an error - one patient's data under another patient's name. Use it for input that is immutable or
// outlives the Message, and not otherwise.
//
// # Acknowledgements
//
// AckFor builds an ACK or NAK for a message, including for one that failed to parse, which is the case that matters
// most: a sender needs a reply even when what it sent was not a message. See AckOptions.
//
// # What this package does not do
//
// It does not write or modify messages. It has no notion of message structure beyond segments, so it will not tell
// you that a PV1 belongs to a particular visit group. It does not validate against a version's tables. Those are
// deliberate omissions rather than oversights: this is an indexer for real traffic, which is frequently not
// standard-conformant and still has to be routed.
package hl7
