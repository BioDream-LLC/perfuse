// Package attach moves large payloads out of a message and puts them back.
//
// An HL7 message carrying a scanned report or a PDF is ordinary in document workflows, and the payload is routinely
// hundreds of times larger than the message around it. Storing that inline means the message store grows at the rate
// of the documents rather than the traffic, a message queued for five destinations is copied five times, and the
// interface has to stream megabytes to show somebody a patient name.
//
// So the payload is extracted, stored once by content, and replaced in the message with a token. Mirth calls these
// attachment handlers and it is a documented migration feature, which is why the token format here is designed to be
// recognisable rather than clever.
//
// # What this package does not decide
//
// It does no storage and no I/O. Extraction produces the rewritten message and the payloads; a Store somewhere else
// decides where those go. That separation is what makes the extraction testable without a database, and it is also
// what lets the same logic run in the WASM playground where there is no database at all.
package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenPrefix begins every token. Chosen to be visibly not clinical data: anybody reading a stored message and
// finding this needs to understand immediately that something was moved rather than lost.
const TokenPrefix = "${perfuse-attachment:"

// TokenSuffix closes a token.
const TokenSuffix = "}"

// Digest identifies a payload by its content.
//
// Content-addressed rather than sequential, which buys deduplication for free: the same document arriving twice, or
// fanned out to five destinations, is stored once. In document workflows resends are common enough that this is a
// large practical saving rather than a theoretical one.
type Digest string

// Compute returns the digest of a payload.
func Compute(payload []byte) Digest {
	sum := sha256.Sum256(payload)
	return Digest(hex.EncodeToString(sum[:]))
}

// Token renders the reference that replaces a payload in a message.
func (d Digest) Token() string { return TokenPrefix + string(d) + TokenSuffix }

// Short is the first twelve characters, for logs and interfaces. Never for lookup - a truncated digest can collide,
// and a collision here would attach the wrong document to a patient.
func (d Digest) Short() string {
	if len(d) <= 12 {
		return string(d)
	}
	return string(d[:12])
}

// Attachment is one extracted payload.
type Attachment struct {
	Digest Digest
	// Size is the payload's length in bytes, kept separately so the interface can report it without loading it.
	Size int
	// Path is where in the message it came from, as an HL7 path. Recorded so a re-inflation failure can say which
	// field is empty rather than only that something is missing.
	Path string
	// Payload is the bytes. Not held by anything long-lived; the Store takes them and this is discarded.
	Payload []byte
}

// Result is what extraction produced.
type Result struct {
	// Message is the rewritten message, with tokens in place of payloads. This is what gets stored and what gets
	// queued, so it is small.
	Message []byte

	// Attachments are the payloads, in the order they appeared.
	Attachments []Attachment

	// Extracted is how many payloads were moved, and Saved how many bytes came out of the message. Both reported
	// because "3 attachments" and "3 attachments totalling 40 MB" are different facts and only the second explains
	// why the store stopped growing.
	Extracted int
	Saved     int
}

// ErrMissingAttachment means a token in a message refers to a payload the store does not have.
//
// Its own error because the correct response is to fail the delivery rather than send a message containing a literal
// token. A receiver given "${perfuse-attachment:abc...}" where a report should be will file it as a report, and
// nobody finds out until somebody opens the record.
type ErrMissingAttachment struct {
	Digest Digest
	Path   string
}

func (e *ErrMissingAttachment) Error() string {
	where := e.Path
	if where == "" {
		where = "somewhere in the message"
	}
	return fmt.Sprintf("the attachment for %s (%s) is not in the store, so this message cannot be reassembled; "+
		"sending it would deliver a placeholder where a document should be", where, e.Digest.Short())
}

// Lookup fetches a payload by digest. Returns false when it is not held.
type Lookup func(Digest) ([]byte, bool)

// Reassemble puts payloads back into a message.
//
// Every token must resolve. A partial reassembly is refused rather than delivered, because a message that is 90%
// correct is indistinguishable from a correct one at the receiving end.
func Reassemble(message []byte, lookup Lookup) ([]byte, error) {
	text := string(message)
	if !strings.Contains(text, TokenPrefix) {
		// The common case, and worth not allocating for: most messages have no attachments.
		return message, nil
	}

	var b strings.Builder
	b.Grow(len(text))

	rest := text
	for {
		start := strings.Index(rest, TokenPrefix)
		if start < 0 {
			b.WriteString(rest)
			break
		}

		b.WriteString(rest[:start])
		after := rest[start+len(TokenPrefix):]

		end := strings.Index(after, TokenSuffix)
		if end < 0 {
			// An unterminated token is corruption rather than an absent attachment, and saying so points at a
			// different cause: something truncated the message after it was stored.
			return nil, fmt.Errorf("a message contains an unterminated attachment token, which means it was " +
				"truncated after being stored")
		}

		digest := Digest(strings.TrimSpace(after[:end]))
		rest = after[end+len(TokenSuffix):]

		payload, found := lookup(digest)
		if !found {
			return nil, &ErrMissingAttachment{Digest: digest}
		}
		b.Write(payload)
	}

	return []byte(b.String()), nil
}

// Tokens lists the digests a message refers to, without needing the payloads.
//
// Used to work out what a queued message depends on, so an attachment is not swept away while something still needs
// it. Sorted by first appearance and deduplicated.
func Tokens(message []byte) []Digest {
	text := string(message)
	if !strings.Contains(text, TokenPrefix) {
		return nil
	}

	var out []Digest
	seen := map[Digest]bool{}

	rest := text
	for {
		start := strings.Index(rest, TokenPrefix)
		if start < 0 {
			return out
		}
		after := rest[start+len(TokenPrefix):]
		end := strings.Index(after, TokenSuffix)
		if end < 0 {
			return out
		}
		digest := Digest(strings.TrimSpace(after[:end]))
		if digest != "" && !seen[digest] {
			seen[digest] = true
			out = append(out, digest)
		}
		rest = after[end+len(TokenSuffix):]
	}
}
