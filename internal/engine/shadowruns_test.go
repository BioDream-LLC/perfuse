package engine

import (
	"context"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// x12Interchange is a minimal 837 with one claim. SE01 must equal the ST-through-SE segment count.
const x12Interchange = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *260830*1200*^*00501*000000001*0*P*:~" +
	"GS*HC*SENDER*RECEIVER*20260830*1200*1*X*005010X222A1~ST*837*0001*005010X222A1~" +
	"BHT*0019*00*0123*20260830*1200*CH~CLM*PATIENT001*500***11:B:1*Y*A*Y*Y~" +
	"REF*D9*CLAIM123~SE*5*0001~GE*1*1~IEA*1*000000001~"

// shadowedX12Pair builds a live X12 channel shadowing a candidate X12 channel.
func shadowedX12Pair(t *testing.T, candidateBody string, capture *captureDest) *Channel {
	t.Helper()

	path := writeChannelFile(t, "x12candidate.yaml", candidateBody)

	cfg := &config.Channel{
		Name:     "live-x12",
		DataType: "x12",
		Source:   config.Source{Type: config.SourceHTTP, HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/in"}},
		Shadow:   &config.Shadow{Channel: path, Sample: 1},
		// TCP rather than MLLP: an MLLP destination is correctly refused on an X12 channel, because MLLP expects an HL7
		// acknowledgement in reply and an X12 receiver will not send one.
		Destinations: []config.Destination{{
			Name: "out", Type: config.DestinationTCP,
			TCP:     &config.TCPDest{Address: "127.0.0.1:1", TCPFraming: config.TCPFraming{Framing: "length"}},
			Timeout: time.Second,
			Retry:   config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ch, err := NewChannel(cfg, func(d config.Destination) (Sender, error) { return capture, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.startShadow(config.LoadFile); err != nil {
		t.Fatalf("startShadow: %v", err)
	}
	t.Cleanup(func() { _ = ch.stopShadow() })

	return ch
}

const x12CandidateBody = `
name: x12candidate
dataType: x12
source:
  type: http
  http:
    listen: 127.0.0.1:0
    path: /in
destinations:
  - name: out
    type: tcp
    tcp:
      address: 127.0.0.1:1
      framing: length
`

// TestAnX12ShadowActuallyRuns is the regression for a defect that made the feature silent for every non-v2 format.
//
// # What was wrong
//
// Channel.handle set up the shadow observation as a deferred call, and it also dispatched to the format-specific handlers -
// handleX12, handleHL7v3, handleDelimited, handlePharmacy. The dispatch happened *first*, so those channels returned from their
// own handler and the deferred observation was never reached.
//
// Meanwhile the config layer permitted a v3 shadow and carried a comment saying shadow mode worked on a v3 channel now that
// there was an XML-aware diff. Both halves existed. Nothing joined them. And X12 was refused with "shadow mode compares HL7
// output so far", which read as a missing comparison when the comparison was only half the problem.
//
// So a v3 channel could be given a shadow that validated, appeared in perfuse check, appeared in the interface, and did
// nothing. That is the failure this project exists to refuse.
//
// # Why the assertion is on Compared and not on differences
//
// A shadow that runs and finds nothing looks exactly like one that never ran, if you only inspect the difference list. Compared
// increments once per message that went through both pipelines, so zero means the comparison did not happen at all.
func TestAnX12ShadowActuallyRuns(t *testing.T) {
	capture := &captureDest{}
	ch := shadowedX12Pair(t, x12CandidateBody, capture)

	if ch.shadow == nil {
		t.Fatal("the channel has no shadow runner, so the configuration was accepted and discarded")
	}

	if _, err := ch.handle(context.Background(), []byte(x12Interchange)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	stats := ch.shadow.Stats()
	if stats.Compared == 0 {
		t.Fatal("the shadow compared 0 messages after one was handled, so it validated at load and never ran")
	}
}

// TestAnIdenticalX12CandidateReportsNoDifferences confirms the comparison is the X12 one.
//
// If this ran through the HL7 walker it would find no MSH in either interchange and could report the whole message as
// different, which is the answer that makes a report untrustworthy.
func TestAnIdenticalX12CandidateReportsNoDifferences(t *testing.T) {
	capture := &captureDest{}
	ch := shadowedX12Pair(t, x12CandidateBody, capture)

	if _, err := ch.handle(context.Background(), []byte(x12Interchange)); err != nil {
		t.Fatalf("handling: %v", err)
	}

	stats := ch.shadow.Stats()
	if stats.Compared == 0 {
		t.Fatal("nothing was compared")
	}
	if stats.Differed != 0 {
		t.Errorf("an identical candidate reported %d differing message(s), which suggests the wrong walker is being used", stats.Differed)
	}
}
