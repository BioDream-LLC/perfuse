package x12

import (
	"strings"
	"testing"
)

// Two transaction sets in one functional group.
const twoSets = "ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~" +
	"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"CLM*ACCT-1*500~" +
	"SE*3*0001~" +
	"ST*837*0002*005010X222A1~" +
	"CLM*ACCT-2*750~" +
	"SE*3*0002~" +
	"GE*2*1~" +
	"IEA*1*000000001~"

// An 837 and an 835 in the same interchange, in separate groups with different
// implementation guides in GS08.
const twoGroups = "ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~" +
	"GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"CLM*ACCT-1*500~" +
	"SE*3*0001~" +
	"GE*1*1~" +
	"GS*HP*SUBMITTERID*RECEIVERID*20260819*1253*2*X*005010X221A1~" +
	"ST*835*0002*005010X221A1~" +
	"CLP*ACCT-1*1*500*400~" +
	"SE*3*0002~" +
	"GE*1*2~" +
	"IEA*2*000000001~"

func TestSplitProducesOneInterchangePerSet(t *testing.T) {
	m, err := Parse([]byte(twoSets))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.SetCount(); got != 2 {
		t.Fatalf("SetCount() = %d, want 2", got)
	}

	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("Split() returned %d, want 2", len(parts))
	}

	for i, p := range parts {
		if got := p.SetCount(); got != 1 {
			t.Errorf("part %d holds %d transaction sets, want 1", i, got)
		}
		// Each output must stand on its own, which is the whole point.
		if v := p.Validate(); !v.OK() {
			t.Errorf("part %d does not pass its own envelope check: %v", i, v.Err())
		}
	}

	if got, _ := parts[0].Get("CLM01"); got != "ACCT-1" {
		t.Errorf("part 0 CLM01 = %q", got)
	}
	if got, _ := parts[1].Get("CLM01"); got != "ACCT-2" {
		t.Errorf("part 1 CLM01 = %q", got)
	}
}

func TestSplitCorrectsTheTrailerCounts(t *testing.T) {
	// The original says two sets in the group. Each part holds one, so GE01 has to say
	// one - otherwise every part fails the envelope check it was split to satisfy.
	m, _ := Parse([]byte(twoSets))
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if got, _ := p.Get("GE01"); got != "1" {
			t.Errorf("part %d GE01 = %q, want 1", i, got)
		}
		if got, _ := p.Get("IEA01"); got != "1" {
			t.Errorf("part %d IEA01 = %q, want 1", i, got)
		}
	}
}

func TestSplitPreservesTheISAByteForByte(t *testing.T) {
	// ISA is fixed width. Rebuilding it from parsed elements risks changing the padding,
	// and an ISA that is no longer 106 bytes is one whose delimiters cannot be read from
	// their byte offsets. Copying the original bytes makes that mistake unavailable.
	m, _ := Parse([]byte(twoSets))
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}

	original, _ := m.Segment("ISA", 1)
	for i, p := range parts {
		isa, ok := p.Segment("ISA", 1)
		if !ok {
			t.Fatalf("part %d has no ISA", i)
		}
		if string(isa.Raw()) != string(original.Raw()) {
			t.Errorf("part %d ISA changed\n got: %q\nwant: %q", i, isa.Raw(), original.Raw())
		}
		if len(isa.Raw())+1 != 106 {
			t.Errorf("part %d ISA is %d bytes with its terminator, want 106", i, len(isa.Raw())+1)
		}
	}
}

func TestSplitKeepsEachSetInItsOwnFunctionalGroup(t *testing.T) {
	// GS08 carries the implementation guide. Wrapping every set in the first group's GS
	// would relabel an 835 as an 837, and the receiver would reject it or try to read it
	// as one.
	m, err := Parse([]byte(twoGroups))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("Split() returned %d, want 2", len(parts))
	}

	if got, _ := parts[0].Get("GS08"); got != "005010X222A1" {
		t.Errorf("part 0 GS08 = %q, want the professional claim guide", got)
	}
	if got, _ := parts[0].Get("ST01"); got != "837" {
		t.Errorf("part 0 ST01 = %q", got)
	}

	if got, _ := parts[1].Get("GS08"); got != "005010X221A1" {
		t.Errorf("part 1 GS08 = %q, want the remittance guide", got)
	}
	if got, _ := parts[1].Get("ST01"); got != "835" {
		t.Errorf("part 1 ST01 = %q", got)
	}
	// And the functional identifier, which is how a receiver routes the file.
	if got, _ := parts[1].Get("GS01"); got != "HP" {
		t.Errorf("part 1 GS01 = %q, want HP", got)
	}
}

func TestSplitKeepsTheGroupControlNumber(t *testing.T) {
	m, _ := Parse([]byte(twoGroups))
	parts, _ := m.Split()

	// GS06 and GE02 must still agree within each part, or every part carries a warning
	// it did not have before.
	for i, p := range parts {
		gs, _ := p.Get("GS06")
		ge, _ := p.Get("GE02")
		if gs != ge {
			t.Errorf("part %d: GS06 = %q but GE02 = %q", i, gs, ge)
		}
		if len(p.Validate().Warnings()) != 0 {
			t.Errorf("part %d gained warnings: %v", i, p.Validate().Warnings())
		}
	}
}

func TestASingleSetIsReturnedUnchanged(t *testing.T) {
	// Most files are single-set. Rewriting one would mean a channel configured to split
	// was quietly altering files it had nothing to do to.
	m, err := Parse([]byte(claim837))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("Split() returned %d, want 1", len(parts))
	}
	if string(parts[0].Raw()) != claim837 {
		t.Error("a single-set interchange was rebuilt rather than passed through")
	}
	if parts[0] != m {
		t.Error("a single-set interchange should be the same message, not a copy")
	}
}

func TestSplitRefusesAnInterchangeWithNoTrailer(t *testing.T) {
	// Splitting an incomplete file would turn one detectably broken interchange into
	// several that each look complete. Refusing keeps the fault visible.
	noIEA := strings.Replace(twoSets, "IEA*1*000000001~", "", 1)
	m, err := Parse([]byte(noIEA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Split()
	if err == nil {
		t.Fatal("an interchange with no IEA was split")
	}
	if !strings.Contains(err.Error(), "look intact") {
		t.Errorf("the error should explain the risk, got: %v", err)
	}
}

func TestSplitRefusesAnInterchangeWithNoTransactionSet(t *testing.T) {
	// Returning an empty slice would make the message disappear. A caller that asked
	// for transaction sets and received none needs to hear about it.
	empty := "ISA*00*          *00*          *ZZ*S              *ZZ*R              *260819*1253*^*00501*000000001*0*P*:~" +
		"GS*HC*S*R*20260819*1253*1*X*005010X222A1~GE*0*1~IEA*1*000000001~"
	m, err := Parse([]byte(empty))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Split(); err == nil {
		t.Fatal("an interchange with no transaction set was split silently")
	}
}

func TestSplitRefusesAnInterchangeWithNoGroup(t *testing.T) {
	// A TA1-only interchange is legal and has nothing to split. It must not vanish.
	ta1 := "ISA*00*          *00*          *ZZ*S              *ZZ*R              *260819*1253*^*00501*000000001*0*P*:~" +
		"TA1*000000001*260819*1253*A*000~IEA*0*000000001~"
	m, err := Parse([]byte(ta1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Split(); err == nil {
		t.Fatal("an interchange with no functional group was split silently")
	}
}

func TestSplitPreservesUnusualDelimiters(t *testing.T) {
	// The parts are rebuilt, so this is where a hard-coded "*" would show up.
	piped := strings.NewReplacer("*", "|", "~", "+", ":", ">").Replace(twoSets)
	m, err := Parse([]byte(piped))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		d := p.Delimiters()
		if d.Element != '|' || d.Segment != '+' || d.Component != '>' {
			t.Errorf("part %d delimiters = %q %q %q", i, string(d.Element), string(d.Segment), string(d.Component))
		}
		if strings.Contains(string(p.Raw()), "*") {
			t.Errorf("part %d contains a hard-coded asterisk: %s", i, p.Raw())
		}
	}
}

func TestSplitPreservesEverySegmentOfASet(t *testing.T) {
	// The segments between ST and SE are the actual claim. Dropping one would produce a
	// file that passes its envelope check with a claim missing a diagnosis.
	rich := strings.Replace(twoSets,
		"ST*837*0001*005010X222A1~CLM*ACCT-1*500~SE*3*0001~",
		"ST*837*0001*005010X222A1~BHT*0019*00*1*20260819*1253*CH~NM1*41*2*NAME~CLM*ACCT-1*500~HI*ABK>Z1234~SE*6*0001~", 1)
	m, err := Parse([]byte(rich))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}

	first := parts[0]
	for _, id := range []string{"BHT", "NM1", "CLM", "HI"} {
		if _, ok := first.Segment(id, 1); !ok {
			t.Errorf("part 0 lost its %s segment", id)
		}
	}
	if got, _ := first.Get("SE01"); got != "6" {
		t.Errorf("SE01 = %q, want 6 unchanged", got)
	}
}

func TestSplitDoesNotCarrySegmentsBetweenSets(t *testing.T) {
	// A segment outside any ST/SE pair belongs to no transaction set. Attaching it to a
	// neighbouring one would put a claim's data on the wrong claim.
	m, err := Parse([]byte(twoSets))
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := m.Split()

	// ISA, GS, ST, CLM, SE, GE, IEA and nothing else.
	if got := parts[0].SegmentCount(); got != 7 {
		t.Errorf("part 0 has %d segments, want 7", got)
	}
	if _, ok := parts[0].Segment("CLM", 2); ok {
		t.Error("part 0 picked up the second claim")
	}
}

func TestSetCountOnASingleSetFile(t *testing.T) {
	m, _ := Parse([]byte(claim837))
	if got := m.SetCount(); got != 1 {
		t.Errorf("SetCount() = %d, want 1", got)
	}
}

func TestSetCountAcrossGroups(t *testing.T) {
	m, _ := Parse([]byte(twoGroups))
	if got := m.SetCount(); got != 2 {
		t.Errorf("SetCount() = %d, want 2", got)
	}
}

func TestSplitScalesToManySets(t *testing.T) {
	// A real 837 from a practice management system carries hundreds. Worth asserting the
	// fan-out works at that size rather than only at two, because the counts in every
	// trailer have to be right for all of them.
	var sb strings.Builder
	sb.WriteString("ISA*00*          *00*          *ZZ*SUBMITTERID    *ZZ*RECEIVERID     *260819*1253*^*00501*000000001*0*P*:~")
	sb.WriteString("GS*HC*SUBMITTERID*RECEIVERID*20260819*1253*1*X*005010X222A1~")
	const n = 250
	for i := 1; i <= n; i++ {
		id := "000" + strings.Repeat("0", 0)
		sb.WriteString("ST*837*" + id + itoaPad(i) + "*005010X222A1~")
		sb.WriteString("CLM*ACCT-" + itoaPad(i) + "*500~")
		sb.WriteString("SE*3*" + id + itoaPad(i) + "~")
	}
	sb.WriteString("GE*250*1~")
	sb.WriteString("IEA*1*000000001~")

	m, err := Parse([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Validate(); !v.OK() {
		t.Fatalf("fixture is not valid: %v", v.Err())
	}

	parts, err := m.Split()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != n {
		t.Fatalf("Split() returned %d, want %d", len(parts), n)
	}
	for i, p := range parts {
		if v := p.Validate(); !v.OK() {
			t.Fatalf("part %d does not validate: %v", i, v.Err())
		}
	}
	if got, _ := parts[0].Get("CLM01"); got != "ACCT-001" {
		t.Errorf("first part CLM01 = %q", got)
	}
	if got, _ := parts[n-1].Get("CLM01"); got != "ACCT-250" {
		t.Errorf("last part CLM01 = %q", got)
	}
}

func itoaPad(n int) string {
	s := ""
	switch {
	case n < 10:
		s = "00"
	case n < 100:
		s = "0"
	}
	return s + itoaSmall(n)
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
