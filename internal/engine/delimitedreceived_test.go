package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// What Received counts on a delimited channel.
//
// # Why this needs a test of its own
//
// A delimited document becomes many messages. That was a deliberate design decision — one row at a time, so a filter can drop a
// single bad row rather than the whole file, and so one unparseable line does not take four hundred good ones with it.
//
// It has a consequence nobody chose: Received counts rows, not documents. A site sending one file an hour with four hundred rows
// in it sees four hundred received per hour, and a dashboard or an alert threshold built against a channel that used to handle one
// message per file would read that as a hundredfold traffic increase.
//
// The design note that recorded this decision flagged the metric consequence as needing to be written down, and it never was:
// nothing in docs or the manual said which unit Received uses, and no test asserted it either way. So the number was correct,
// undocumented, and free to change without anything noticing.
//
// This locks it. Not because rows are obviously the right unit — an argument for documents is available, since that is what the
// sender sent — but because it is what the code does, an operator can be told, and a change to it should be a decision rather
// than a side effect of editing the loop.

// TestReceivedCountsRowsNotDocumentsOnADelimitedChannel is the assertion the design note asked for.
func TestReceivedCountsRowsNotDocumentsOnADelimitedChannel(t *testing.T) {
	sink := &recordingSender{}

	// Four rows in one document, so the two candidate answers are far enough apart that neither can be reached by accident.
	const document = "ward,mrn\nA1,111\nB2,222\nC3,333\nD4,444\n"

	c := delimitedChannelFor(t, sink)

	if _, err := c.handle(context.Background(), []byte(document)); err != nil {
		t.Fatalf("handling the document: %v", err)
	}

	const rows = 4

	stats := c.Stats()
	if stats.Received != rows {
		t.Errorf("Received is %d for a %d-row document; if this is now counting documents that is a change to what every "+
			"delimited dashboard and alert threshold measures, and it needs to be a decision rather than a side effect",
			stats.Received, rows)
	}

	// Delivered has to agree, or the two metrics are measuring different things and a "delivered / received" ratio - which is
	// the obvious way to spot a channel dropping traffic - would be meaningless on this format.
	if stats.Delivered != rows {
		t.Errorf("Delivered is %d against Received %d; the two must count the same unit or their ratio means nothing",
			stats.Delivered, stats.Received)
	}

	if sink.count() != rows {
		t.Errorf("%d message(s) reached the destination for %d rows", sink.count(), rows)
	}
}

// TestAHeaderRowIsNotCountedAsReceived is the boundary of the answer above.
//
// If Received counts rows then the header is the one row it must not count, because a header is structure rather than data and a
// site reading "five received" for a four-patient file would be right to distrust the number.
func TestAHeaderRowIsNotCountedAsReceived(t *testing.T) {
	sink := &recordingSender{}

	c := delimitedChannelFor(t, sink)

	if _, err := c.handle(context.Background(), []byte("ward,mrn\nA1,111\n")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if got := c.Stats().Received; got != 1 {
		t.Errorf("Received is %d for a document with a header and one data row; the header is structure, not a message", got)
	}
}

// delimitedChannelFor builds a started delimited channel with a header and comma delimiter.
func delimitedChannelFor(t *testing.T, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: delimited-received
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: tcp
    tcp:
      address: 127.0.0.1:1
      framing: length
`), "delimitedreceived.yaml")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	return startChannelFor(t, cfg, sink)
}
