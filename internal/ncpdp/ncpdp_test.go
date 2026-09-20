package ncpdp

import (
	"strings"
	"testing"
)

// A published example, parsed field by field.
//
// Taken from vendor documentation of the transaction header rather than invented, because a fixed-width
// layout is exactly the thing that looks right while being one byte out. Every field is checked, not just
// the length: a layout shifted by one still totals fifty-six.
func TestThePublishedHeaderExampleParsesFieldByField(t *testing.T) {
	const raw = "123456D1B101        1234567890123     202108210000000100"
	if len(raw) != HeaderLength {
		t.Fatalf("the example is %d bytes, not %d, so the test data is wrong before the code is", len(raw), HeaderLength)
	}

	// A minimal claim segment appended, since a header alone is not a transmission.
	msg, err := Parse([]byte(raw + "\x1e07\x1cD2RX1234"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	h := msg.Header
	for _, tc := range []struct{ name, got, want string }{
		{"BIN", h.BIN, "123456"},
		{"version", h.VersionRelease, "D1"},
		{"transaction code", h.TransactionCode, "B1"},
		{"processor control number", h.ProcessorControlNumber, "01"},
		{"transaction count", h.TransactionCount, "1"},
		{"service provider qualifier", h.ServiceProviderIDQualifier, "23"},
		{"service provider ID", h.ServiceProviderID, "4567890123"},
		{"date of service", h.DateOfService, "20210821"},
		{"software vendor ID", h.SoftwareVendorCertificationID, "0000000100"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	if TransactionName(h.TransactionCode) != "billing" {
		t.Errorf("B1 described as %q", TransactionName(h.TransactionCode))
	}
}

// The distinction the package comment is about.
//
// 0x1C starts a field rather than separating two, so the text between the segment identifier and the
// first marker is the identifier alone. A reader treating it as a separator gets one extra field at the
// front of every segment and every value lands under the identifier before it - and most of those values
// still look plausible, which is why this needs asserting rather than eyeballing.
func TestAFieldMarkerStartsAFieldRatherThanSeparatingTwo(t *testing.T) {
	msg := mustParse(t, header("B1")+"\x1e01\x1cC419800101\x1cCAJOHN")

	seg, ok := msg.Transactions[0].Segment(SegPatient)
	if !ok {
		t.Fatal("no patient segment")
	}
	if len(seg.Fields) != 2 {
		var ids []string
		for _, f := range seg.Fields {
			ids = append(ids, f.ID+"="+f.Value)
		}
		t.Fatalf("got %d fields (%s), want 2; an extra field at the front means the markers were read as separators",
			len(seg.Fields), strings.Join(ids, " "))
	}
	if got, _ := seg.Get("C4"); got != "19800101" {
		t.Errorf("date of birth = %q, want 19800101", got)
	}
	if got, _ := seg.Get("CA"); got != "JOHN" {
		t.Errorf("first name = %q, want JOHN", got)
	}
}

// A short header must not be padded.
//
// Fixed width with no internal separators means a missing byte does not fail, it shifts. The BIN absorbs
// the version, the version absorbs the transaction code, and a B1 billing request is read as something
// else entirely - with every field still the right length.
func TestAShortHeaderIsRefusedRatherThanPadded(t *testing.T) {
	short := header("B1")[:HeaderLength-1]
	_, err := Parse([]byte(short + "\x1e07\x1cD2RX1"))
	if err == nil {
		t.Fatal("a fifty-five byte header was accepted, which shifts every field after the gap")
	}
	if !strings.Contains(err.Error(), "shifts") {
		t.Errorf("the refusal does not explain what a short header does: %v", err)
	}

	// And it must say how far out the boundary is, because that is the only clue to what went wrong at
	// the sender. Fifty-five bytes of header means the separator lands one byte early.
	if !strings.Contains(err.Error(), "short by 1") {
		t.Errorf("the refusal does not say how short the header is: %v", err)
	}
}

// A transmission shorter than a header at all is the easy case, and worth keeping separate: the check
// that catches it is a length test, and the check that catches a short header followed by segments is
// not.
func TestATransmissionShorterThanAHeaderIsRefused(t *testing.T) {
	if _, err := Parse([]byte("123456D0B1")); err == nil {
		t.Fatal("ten bytes were accepted as a transmission")
	}
}

func TestSegmentsAndFieldsSurviveARoundTrip(t *testing.T) {
	original := Message{
		Header: Header{
			BIN:                           "610097",
			VersionRelease:                "D0",
			TransactionCode:               TxBilling,
			ProcessorControlNumber:        "9999",
			ServiceProviderIDQualifier:    "01",
			ServiceProviderID:             "1234567893",
			DateOfService:                 "20260828",
			SoftwareVendorCertificationID: "PERFUSE",
		},
		Transactions: []Transaction{{Segments: []Segment{
			{ID: SegPatient, Fields: []Field{{"C4", "19800101"}, {"CA", "JOHN"}, {"CB", "SMITH"}}},
			{ID: SegClaim, Fields: []Field{{"D2", "RX1234"}, {"D7", "00093-0058-01"}, {"E7", "30"}}},
			{ID: SegPricing, Fields: []Field{{"D9", "4500"}, {"DQ", "3200"}}},
		}}},
	}
	original.SetHeaderCount()

	wire, err := Build(original)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	back, err := Parse(wire)
	if err != nil {
		t.Fatalf("Parse of our own output: %v", err)
	}

	if back.Header != original.Header {
		t.Errorf("header changed:\n got %+v\nwant %+v", back.Header, original.Header)
	}
	if len(back.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(back.Transactions))
	}
	if len(back.Transactions[0].Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(back.Transactions[0].Segments))
	}
	for i, want := range original.Transactions[0].Segments {
		got := back.Transactions[0].Segments[i]
		if got.ID != want.ID {
			t.Errorf("segment %d id = %q, want %q", i, got.ID, want.ID)
		}
		if len(got.Fields) != len(want.Fields) {
			t.Errorf("segment %s has %d fields, want %d", want.ID, len(got.Fields), len(want.Fields))
			continue
		}
		for j := range want.Fields {
			if got.Fields[j] != want.Fields[j] {
				t.Errorf("segment %s field %d = %+v, want %+v", want.ID, j, got.Fields[j], want.Fields[j])
			}
		}
	}
}

// Repeating fields must all survive. A compound has one ingredient per repeat, so keeping the last means
// dispensing one ingredient of a mixture.
func TestARepeatingFieldKeepsEveryValue(t *testing.T) {
	msg := mustParse(t, header("B1")+"\x1e10\x1cUE00093-0058-01\x1cUE00054-0018-25\x1cUE00185-0151-01")

	seg, ok := msg.Transactions[0].Segment(SegCompound)
	if !ok {
		t.Fatal("no compound segment")
	}
	all := seg.All("UE")
	if len(all) != 3 {
		t.Fatalf("got %d ingredients, want 3; a map keyed by field id would keep one and dispense a different medicine", len(all))
	}
	if all[0] != "00093-0058-01" || all[2] != "00185-0151-01" {
		t.Errorf("ingredients out of order or altered: %v", all)
	}
}

func TestMultipleTransactionsAreSplitOnTheGroupSeparator(t *testing.T) {
	msg := mustParse(t, header("B1")+"\x1e07\x1cD2RX1\x1d\x1e07\x1cD2RX2\x1d\x1e07\x1cD2RX3")
	if len(msg.Transactions) != 3 {
		t.Fatalf("got %d transactions, want 3", len(msg.Transactions))
	}
	for i, want := range []string{"RX1", "RX2", "RX3"} {
		seg, ok := msg.Transactions[i].Segment(SegClaim)
		if !ok {
			t.Fatalf("transaction %d has no claim segment", i+1)
		}
		if got, _ := seg.Get("D2"); got != want {
			t.Errorf("transaction %d prescription number = %q, want %q", i+1, got, want)
		}
	}
}

// A trailing group separator is a sender's habit, not an empty claim.
func TestATrailingGroupSeparatorDoesNotBecomeAnEmptyTransaction(t *testing.T) {
	msg := mustParse(t, header("B1")+"\x1e07\x1cD2RX1\x1d")
	if len(msg.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(msg.Transactions))
	}
}

// An over-long field is refused, because a shortened identifier is somebody else's valid identifier.
func TestAnOverLongHeaderFieldIsRefusedRatherThanTruncated(t *testing.T) {
	m := Message{
		Header: Header{
			BIN:               "6100970", // seven digits in a six-digit field
			VersionRelease:    "D0",
			TransactionCode:   TxBilling,
			ServiceProviderID: "1234567893",
		},
		Transactions: []Transaction{{Segments: []Segment{{ID: SegClaim, Fields: []Field{{"D2", "RX1"}}}}}},
	}
	_, err := Build(m)
	if err == nil {
		t.Fatal("a seven-digit BIN was written into a six-character field")
	}
	if !strings.Contains(err.Error(), "somebody else") {
		t.Errorf("the refusal does not say why truncation is worse than failing: %v", err)
	}
}

// There is no escape mechanism in the standard, so a value containing a separator cannot be sent.
// Writing it anyway would split into fields nobody sent and give the next segment fields belonging to
// this one.
func TestAValueContainingASeparatorIsRefused(t *testing.T) {
	m := Message{
		Header:       Header{BIN: "610097", VersionRelease: "D0", TransactionCode: TxBilling},
		Transactions: []Transaction{{Segments: []Segment{{ID: SegClaim, Fields: []Field{{"D2", "RX\x1c1"}}}}}},
	}
	if _, err := Build(m); err == nil {
		t.Fatal("a value containing a field separator was written")
	}
}

func TestMoreThanFourTransactionsIsRefused(t *testing.T) {
	one := Transaction{Segments: []Segment{{ID: SegClaim, Fields: []Field{{"D2", "RX1"}}}}}
	m := Message{
		Header:       Header{BIN: "610097", VersionRelease: "D0", TransactionCode: TxBilling},
		Transactions: []Transaction{one, one, one, one, one},
	}
	if _, err := Build(m); err == nil {
		t.Fatal("five transactions were written into a transmission whose count field holds one digit")
	}
}

// A count that disagrees with the segments means a claim was lost. Reported rather than corrected,
// because correcting it destroys the only evidence that anything went missing.
func TestAnAnnouncedCountThatDisagreesIsReportedNotCorrected(t *testing.T) {
	msg := mustParse(t, headerCount("B1", "3")+"\x1e07\x1cD2RX1")
	err := msg.CheckHeaderCount()
	if err == nil {
		t.Fatal("a header announcing three claims with one present was accepted")
	}
	if !strings.Contains(err.Error(), "lost") {
		t.Errorf("the message does not suggest a claim went missing: %v", err)
	}
	// And the parse itself must have succeeded, so the operator can see what did arrive.
	if len(msg.Transactions) != 1 {
		t.Errorf("got %d transactions, want the one that was actually sent", len(msg.Transactions))
	}
}

func TestBatchFramingIsStrippedBeforeTheHeaderIsMeasured(t *testing.T) {
	// STX, G1, ten-character reference number, then the ordinary header.
	wire := "\x02G1REF0000001" + header("B1") + "\x1e07\x1cD2RX9\x03"
	msg, err := Parse([]byte(wire))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Header.TransactionCode != TxBilling {
		t.Errorf("transaction code = %q; the batch prefix was not stripped, so the header was measured from the wrong place",
			msg.Header.TransactionCode)
	}
	seg, ok := msg.Transactions[0].Segment(SegClaim)
	if !ok {
		t.Fatal("no claim segment")
	}
	if got, _ := seg.Get("D2"); got != "RX9" {
		t.Errorf("prescription number = %q, want RX9", got)
	}
}

// A batch header or trailer is a file control record, not a claim. Reading one as a transaction would
// invent a claim out of a file's bookkeeping.
func TestABatchHeaderRecordIsNotReadAsATransaction(t *testing.T) {
	for _, id := range []string{"G0", "G9"} {
		_, err := Parse([]byte("\x02" + id + "REF0000001" + header("B1") + "\x1e07\x1cD2RX1\x03"))
		if err == nil {
			t.Errorf("a %s record was parsed as a transaction", id)
		}
	}
}

// Data before the first segment marker belongs to no field. Attaching it to the segment that follows is
// the silent option.
func TestDataOutsideAnySegmentIsReported(t *testing.T) {
	_, err := Parse([]byte(header("B1") + "GARBAGE\x1e07\x1cD2RX1"))
	if err == nil {
		t.Fatal("bytes outside any segment were accepted")
	}
}

func TestAHeaderWithNoSegmentsIsRefused(t *testing.T) {
	_, err := Parse([]byte(header("B1")))
	if err == nil {
		t.Fatal("a header with nothing after it was accepted as a transmission")
	}
	// The message should name the transaction in words, since B1 means nothing to most readers.
	if !strings.Contains(err.Error(), "billing") {
		t.Errorf("the message does not say what the transaction was: %v", err)
	}
}

func TestAWrongLengthSegmentIdentifierIsRefused(t *testing.T) {
	for _, id := range []string{"7", "007"} {
		_, err := Parse([]byte(header("B1") + "\x1e" + id + "\x1cD2RX1"))
		if err == nil {
			t.Errorf("segment identifier %q was accepted", id)
		}
	}
}

// Right-trimmed only. A leading space can be part of a name or an address, and trimming both edits
// patient data on the way through.
func TestFieldValuesAreRightTrimmedOnly(t *testing.T) {
	msg := mustParse(t, header("B1")+"\x1e01\x1cCB SMITH   ")
	seg, _ := msg.Transactions[0].Segment(SegPatient)
	got, _ := seg.Get("CB")
	if got != " SMITH" {
		t.Errorf("last name = %q, want %q", got, " SMITH")
	}
}

func TestEmptyInputIsRefused(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Error("nil was parsed as a transmission")
	}
	if _, err := Parse([]byte("")); err == nil {
		t.Error("an empty transmission was parsed")
	}
}

func TestSegmentAndTransactionNamesAreAvailableForLogs(t *testing.T) {
	if SegmentName(SegDUR) != "drug utilisation review" {
		t.Errorf("segment 08 described as %q", SegmentName(SegDUR))
	}
	if TransactionName(TxReversal) != "reversal" {
		t.Errorf("B2 described as %q", TransactionName(TxReversal))
	}
	// An unknown code returns empty rather than a fabricated description.
	if TransactionName("ZZ") != "" {
		t.Errorf("an unknown transaction code was described as %q", TransactionName("ZZ"))
	}
}

// header builds a valid fifty-six byte header for a transaction code.
func header(code string) string { return headerCount(code, "1") }

func headerCount(code, count string) string {
	pad := func(s string, n int) string {
		for len(s) < n {
			s += " "
		}
		return s[:n]
	}
	return pad("610097", 6) + pad("D0", 2) + pad(code, 2) + pad("9999", 10) +
		pad(count, 1) + pad("01", 2) + pad("1234567893", 15) + pad("20260828", 8) + pad("PERFUSE", 10)
}

func mustParse(t *testing.T, s string) Message {
	t.Helper()
	m, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}
