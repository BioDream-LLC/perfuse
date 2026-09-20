package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

const x12TwoSets = "ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~" +
	"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"CLM*ACCT-1*500~" +
	"SE*3*0001~" +
	"ST*837*0002*005010X222A1~" +
	"CLM*ACCT-2*750~" +
	"SE*3*0002~" +
	"GE*2*1~" +
	"IEA*1*000000001~"

// newX12Channel builds a channel with one capturing destination.
func newX12Channel(t *testing.T, opts *config.X12Options) (*Channel, *captureDest) {
	t.Helper()

	cfg := &config.Channel{
		Name:     "claims-in",
		DataType: config.DataX12,
		X12:      opts,
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/claims"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	cap := &captureDest{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return cap, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return ch, cap
}

func TestAnX12ChannelDeliversAWholeInterchange(t *testing.T) {
	ch, cap := newX12Channel(t, nil)

	ack, err := ch.HandleForTest(context.Background(), []byte(x12TwoSets))
	if err != nil {
		t.Fatal(err)
	}

	// X12 acknowledges out of band with a 997 or 999, so there is nothing to return.
	// Returning an HL7 ACK here would send an MSA segment to something that cannot read
	// one.
	if len(ack) != 0 {
		t.Errorf("an X12 channel returned an acknowledgement: %q", ack)
	}

	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %v, want delivered", got)
	}

	got := cap.messages()
	if len(got) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(got))
	}
	if string(got[0]) != x12TwoSets {
		t.Error("the interchange was altered on the way through")
	}
}

func TestAnX12ChannelSplitsWhenAsked(t *testing.T) {
	// The point of splitting is that an operator looking for one claim finds one claim,
	// not a file of four hundred.
	ch, cap := newX12Channel(t, &config.X12Options{Split: true})

	if _, err := ch.HandleForTest(context.Background(), []byte(x12TwoSets)); err != nil {
		t.Fatal(err)
	}

	got := cap.messages()
	if len(got) != 2 {
		t.Fatalf("delivered %d messages, want 2", len(got))
	}
	if !strings.Contains(string(got[0]), "ACCT-1") {
		t.Errorf("first part = %s", got[0])
	}
	if !strings.Contains(string(got[1]), "ACCT-2") {
		t.Errorf("second part = %s", got[1])
	}
	// Each part must be a complete interchange, not a bare transaction set.
	for i, part := range got {
		for _, want := range []string{"ISA*", "GS*", "GE*", "IEA*"} {
			if !strings.Contains(string(part), want) {
				t.Errorf("part %d is missing %s: %s", i, want, part)
			}
		}
	}
}

func TestATruncatedInterchangeIsRefusedByDefault(t *testing.T) {
	// The whole reason to prefer this over a passthrough. A truncated 837 parses
	// perfectly - it is simply missing claims - so without the envelope check the file
	// is accepted, forwarded, and nobody finds out until a payer reports fewer claims
	// than were sent.
	truncated := strings.Replace(x12TwoSets, "ST*837*0002*005010X222A1~CLM*ACCT-2*750~SE*3*0002~", "", 1)

	ch, cap := newX12Channel(t, nil)
	if _, err := ch.HandleForTest(context.Background(), []byte(truncated)); err != nil {
		t.Fatal(err)
	}

	// Unparseable rather than Failed, because Failed becomes a 502 and a 502 tells the
	// sender to retry - and retrying a truncated file never helps.
	if got := ch.LastOutcomeForTest(); got != Unparseable {
		t.Errorf("outcome = %v, want unparseable so the sender is told not to retry", got)
	}
	if n := len(cap.messages()); n != 0 {
		t.Errorf("a file with a bad envelope was delivered anyway (%d messages)", n)
	}
}

func TestTheWarnPolicyDeliversAndRecordsTheFault(t *testing.T) {
	// Some partners have generated wrong counts for years in otherwise complete files.
	// A site that has to process those needs a way to say so in the configuration
	// rather than turning validation off wholesale.
	truncated := strings.Replace(x12TwoSets, "ST*837*0002*005010X222A1~CLM*ACCT-2*750~SE*3*0002~", "", 1)

	ch, cap := newX12Channel(t, &config.X12Options{Envelope: config.EnvelopeWarn})
	if _, err := ch.HandleForTest(context.Background(), []byte(truncated)); err != nil {
		t.Fatal(err)
	}

	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %v, want delivered", got)
	}
	if n := len(cap.messages()); n != 1 {
		t.Errorf("delivered %d messages, want 1", n)
	}
}

func TestTheIgnorePolicySkipsTheCheck(t *testing.T) {
	truncated := strings.Replace(x12TwoSets, "ST*837*0002*005010X222A1~CLM*ACCT-2*750~SE*3*0002~", "", 1)

	ch, cap := newX12Channel(t, &config.X12Options{Envelope: config.EnvelopeIgnore})
	if _, err := ch.HandleForTest(context.Background(), []byte(truncated)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %v, want delivered", got)
	}
	if n := len(cap.messages()); n != 1 {
		t.Errorf("delivered %d messages, want 1", n)
	}
}

func TestSomethingThatIsNotX12IsUnparseable(t *testing.T) {
	ch, cap := newX12Channel(t, nil)

	hl7Message := "MSH|^~\\&|A|B|C|D|20260819||ADT^A01|1|P|2.5.1\rPID|||123\r"
	if _, err := ch.HandleForTest(context.Background(), []byte(hl7Message)); err != nil {
		t.Fatal(err)
	}

	if got := ch.LastOutcomeForTest(); got != Unparseable {
		t.Errorf("outcome = %v, want unparseable", got)
	}
	if n := len(cap.messages()); n != 0 {
		t.Errorf("an HL7 message was delivered by an X12 channel (%d messages)", n)
	}
}

func TestASplitThatCannotBeDoneIsRefusedNotForwardedWhole(t *testing.T) {
	// Forwarding the file whole would deliver something the channel was configured not
	// to deliver, which is worse than failing.
	noIEA := strings.Replace(x12TwoSets, "IEA*1*000000001~", "", 1)

	ch, cap := newX12Channel(t, &config.X12Options{Split: true, Envelope: config.EnvelopeIgnore})
	if _, err := ch.HandleForTest(context.Background(), []byte(noIEA)); err != nil {
		t.Fatal(err)
	}

	if got := ch.LastOutcomeForTest(); got != Unparseable {
		t.Errorf("outcome = %v, want unparseable", got)
	}
	if n := len(cap.messages()); n != 0 {
		t.Errorf("an unsplittable file was delivered whole (%d messages)", n)
	}
}

func TestCombineOutcomes(t *testing.T) {
	// A file where some claims went and some did not is a distinct operational state:
	// resending the whole file would duplicate the ones that succeeded.
	cases := []struct {
		name string
		in   []Outcome
		want Outcome
	}{
		{"all delivered", []Outcome{Delivered, Delivered}, Delivered},
		{"all queued", []Outcome{Queued, Queued}, Queued},
		{"delivered and queued", []Outcome{Delivered, Queued}, Queued},
		{"all failed", []Outcome{Failed, Failed}, Failed},
		{"some failed", []Outcome{Delivered, Failed}, PartiallyDelivered},
		{"queued and failed", []Outcome{Queued, Failed}, PartiallyDelivered},
		{"filtered counts as through", []Outcome{Filtered, Delivered}, Delivered},
		{"nothing", nil, Failed},
	}
	for _, c := range cases {
		if got := combineOutcomes(c.in); got != c.want {
			t.Errorf("%s: combineOutcomes(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
