package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/x12xml"
)

// TestAnX12ChannelTakesAndSendsMirthsX12XML runs both halves through a channel: an interchange arrives as the XML
// Mirth and OIE write for X12 and is handled as X12, and a destination set to x12_as: xml receives that XML again.
func TestAnX12ChannelTakesAndSendsMirthsX12XML(t *testing.T) {
	xml, err := x12xml.ToXML([]byte(x12TwoSets))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Channel{
		Name: "claims-xml", DataType: config.DataX12, X12: &config.X12Options{ArrivesAs: "xml"},
		Source: config.Source{Type: config.SourceHTTP, HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/claims"}},
		Destinations: []config.Destination{
			{Name: "as-x12", Type: config.DestinationFile, Dir: t.TempDir()},
			{Name: "as-xml", Type: config.DestinationFile, Dir: t.TempDir(), X12As: "xml"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*captureDest{"as-x12": {}, "as-xml": {}}
	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) { return byName[d.Name], nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.HandleForTest(context.Background(), xml); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("outcome %v", got)
	}
	if got := byName["as-x12"].messages(); len(got) != 1 || string(got[0]) != x12TwoSets {
		t.Errorf("the X12 destination received %q", got)
	}
	if got := byName["as-xml"].messages(); len(got) != 1 || string(got[0]) != string(xml) {
		t.Errorf("the XML destination received %.120q", got)
	}
	// Not X12 XML at all: refused as unparseable, saying what was expected.
	if _, err := ch.HandleForTest(context.Background(), []byte(x12TwoSets)); err == nil && ch.LastOutcomeForTest() != Unparseable {
		t.Errorf("raw X12 on a channel expecting XML: %v", ch.LastOutcomeForTest())
	}
}

func TestX12AsXMLIsRefusedOffAnX12Channel(t *testing.T) {
	cfg := &config.Channel{Name: "adt", Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{Name: "d", Type: config.DestinationFile, Dir: t.TempDir(), X12As: "xml"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "x12_as") {
		t.Errorf("x12_as on an HL7 channel: %v", err)
	}
}
