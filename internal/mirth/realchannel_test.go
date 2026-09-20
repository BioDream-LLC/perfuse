package mirth

import (
	"os"
	"strings"
	"testing"
)

// Perfuse's Mirth importer against a channel Mirth itself wrote.
//
// Every other test in this package reads adt_channel.xml, which says in its own comment that it was written by hand. That made the
// whole package a test of agreement with a document composed here, which is the failure this repository keeps finding elsewhere:
// self-agreement is not evidence.
//
// real_channel_from_mirth.xml was produced by Mirth 4.5.2's own model classes and serialiser - Channel, Connector,
// TcpReceiverProperties and HttpDispatcherProperties constructed in Java, then written by ObjectXMLSerializer, which is the class
// Mirth uses for its own exports. Mirth accepts it: posted to a running 4.5.2 server it is stored with the description intact and both
// transport names present, where the hand-written file is stored as "This channel is invalid. Verify all required extensions are
// loaded correctly" with its destinations discarded.
//
// scripts/mirth-author-channel.sh regenerates it.
//
// The differences the hand-written file got wrong, found by comparing the two documents, are the point of keeping this one:
//
//   - HttpDispatcherProperties has a host, not a url
//   - resourceIds holds entry/string pairs, not bare strings
//   - Channel has no enabled element at all; there is no setter for one in the model
//   - every connector carries a destinationConnectorProperties block with the queueing and retry settings, and the hand-written file
//     omitted it entirely
//
// None of that was discoverable from the server, which reports an invalid channel in one sentence and names nothing.

func TestARealMirthChannelParses(t *testing.T) {
	// The first test in this package that proves anything about Mirth rather than about a document written here.
	channel, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatalf("Perfuse cannot read a channel Mirth wrote: %v", err)
	}

	if channel.Name == "" {
		t.Error("the channel name did not survive the parse")
	}

	if !strings.Contains(channel.Name, "Built By Mirth") {
		t.Errorf("channel name is %q, which is not the fixture's", channel.Name)
	}
}

func TestARealMirthChannelKeepsItsSource(t *testing.T) {
	// A source connector silently reduced to nothing is how Mirth itself mishandled the hand-written file, and an importer that did
	// the same would produce a channel that looks converted and listens to nothing.
	channel, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatal(err)
	}

	if channel.Source.Transport != "TCP Listener" {
		t.Errorf("source transport is %q, want TCP Listener", channel.Source.Transport)
	}
}

func TestARealMirthChannelKeepsItsDestinations(t *testing.T) {
	// The specific thing Mirth discarded when given the hand-written document. If Perfuse drops them too, a migration would report
	// success and deliver nowhere.
	channel, err := ParseChannelFile("testdata/real_channel_from_mirth.xml")
	if err != nil {
		t.Fatal(err)
	}

	if len(channel.Destinations) == 0 {
		t.Fatal("the destinations were dropped, which is exactly what Mirth did with the hand-written fixture")
	}

	found := false

	for _, d := range channel.Destinations {
		if d.Transport == "HTTP Sender" {
			found = true
		}
	}

	if !found {
		names := make([]string, 0, len(channel.Destinations))
		for _, d := range channel.Destinations {
			names = append(names, d.Transport)
		}

		t.Errorf("no HTTP Sender destination; got %v", names)
	}
}

func TestTheHandWrittenFixtureIsLabelledAsNotFromMirth(t *testing.T) {
	// A guard on honesty rather than on behaviour.
	//
	// adt_channel.xml is still useful - it is a small document, and one deliberately carrying an element Mirth does not know - but it
	// is not what Mirth produces, and a future reader who assumes otherwise would draw conclusions about compatibility that this
	// repository has already disproved. The warning has to be in the file, because that is where somebody reads it.
	data, err := os.ReadFile("testdata/adt_channel.xml")
	if err != nil {
		t.Fatal(err)
	}

	raw := string(data)

	for _, want := range []string{"by hand", "real_channel_from_mirth.xml"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the hand-written fixture does not mention %q, so nothing tells a reader it is not a real export", want)
		}
	}
}
