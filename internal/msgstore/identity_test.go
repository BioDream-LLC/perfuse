package msgstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/identity"
)

// The same admission the identity package tests with, so a failure here is a failure of the
// storage and search rather than of the extraction.
const admissionHL7 = "MSH|^~\\&|EPIC|HOSP|PERFUSE|DEST|20260921120000||ADT^A01^ADT_A01|MSG00001|P|2.5.1\r" +
	"EVN|A01|20260921120000\r" +
	"PID|1||MRN0012345^^^HOSP^MR~E99887766^^^ENTERPRISE^EPI||SAMPLESON^BRAVO^Q||19700101|M\r" +
	"PV1|1|I|ICU^01^A||||||||||||||||VISIT778899\r"

const patientFHIR = `{"resourceType":"Patient",` +
	`"identifier":[{"value":"MRN0012345"}],` +
	`"name":[{"family":"Sampleson","given":["Bravo"]}],"birthDate":"1970-01-01"}`

// indexing returns a store with identity indexing on, which is not the zero value.
func indexing(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	s.StorePayloads = true
	s.IndexIdentity = true
	return s
}

func recordRaw(t *testing.T, s *Store, channel string, raw []byte) int64 {
	t.Helper()
	return record(t, s, &Message{
		Channel:    channel,
		ReceivedAt: time.Now().UTC(),
		Outcome:    Delivered,
		Raw:        raw,
		Size:       len(raw),
	})
}

func search(t *testing.T, s *Store, q IdentitySearch) *IdentityResult {
	t.Helper()
	res, err := s.SearchByIdentity(context.Background(), q)
	if err != nil {
		t.Fatalf("SearchByIdentity(%+v): %v", q, err)
	}
	return res
}

// A message recorded through Record must be findable by an identifier inside it.
//
// Through Record and SearchByIdentity rather than through recordIdentity, because a test that
// calls the extraction helper directly keeps passing when nothing calls the helper - which is
// a mistake already made once in this repository.
func TestAMessageIsFoundByItsMRN(t *testing.T) {
	s := indexing(t)
	id := recordRaw(t, s, "adt", []byte(admissionHL7))

	res := search(t, s, IdentitySearch{Term: "MRN0012345"})

	if res.Total != 1 || len(res.Matches) != 1 {
		t.Fatalf("total=%d matches=%d, want 1 and 1", res.Total, len(res.Matches))
	}
	if res.Matches[0].Message.ID != id {
		t.Errorf("found message %d, want %d", res.Matches[0].Message.ID, id)
	}
	// The reason, so a list of similar messages says which field was hit.
	if res.Matches[0].MatchedValue != "MRN0012345" {
		t.Errorf("matched value = %q, want the MRN", res.Matches[0].MatchedValue)
	}
}

// The search has to work on the term as a person types it, not as the message wrote it.
func TestTheTermIsMatchedAsPeopleTypeIt(t *testing.T) {
	s := indexing(t)
	recordRaw(t, s, "adt", []byte(admissionHL7))

	// The message says SAMPLESON^BRAVO and MRN0012345. Nobody types either of those.
	for _, term := range []string{
		"Sampleson Bravo",
		"sampleson, bravo",
		"SAMPLESON BRAVO",
		"mrn0012345",
		"MRN-0012345",
		"1970-01-01",
		"19700101",
	} {
		t.Run(term, func(t *testing.T) {
			if res := search(t, s, IdentitySearch{Term: term}); res.Total != 1 {
				t.Errorf("searching %q found %d messages, want 1", term, res.Total)
			}
		})
	}
}

// One search must find the same patient across formats. This is the whole point.
func TestOneSearchFindsThePatientInEveryFormat(t *testing.T) {
	s := indexing(t)
	recordRaw(t, s, "adt", []byte(admissionHL7))
	recordRaw(t, s, "fhir-api", []byte(patientFHIR))

	res := search(t, s, IdentitySearch{Term: "MRN0012345"})

	if res.Total != 2 {
		t.Fatalf("found %d messages, want 2 - an HL7 v2 admission and a FHIR Patient for "+
			"the same MRN, which is the case this feature exists for", res.Total)
	}
	channels := map[string]bool{}
	for _, m := range res.Matches {
		channels[m.Message.Channel] = true
	}
	for _, want := range []string{"adt", "fhir-api"} {
		if !channels[want] {
			t.Errorf("no match from the %q channel; got %v", want, channels)
		}
	}
}

// A message that matches on two of its identifiers is still one result.
//
// The term has to hit two stored rows for one message, or the grouping is not exercised. This
// fixture puts the MRN in PID-18 as well as PID-3, which real systems do, so one search
// matches the message as both a patient identifier and an account number.
//
// Written that way on the second attempt. The first version searched terms that each matched a
// single row and passed with the GROUP BY deleted, which made it a test of nothing.
func TestAMessageMatchingTwiceIsListedOnce(t *testing.T) {
	pid := make([]string, 19)
	pid[0], pid[1] = "PID", "1"
	pid[3] = "MRN0012345^^^HOSP^MR"
	pid[5] = "SAMPLESON^BRAVO"
	pid[7] = "19700101"
	pid[8] = "M"
	pid[18] = "MRN0012345" // PID-18, the account number, carrying the MRN as some systems do
	mrnAlsoInAccount := "MSH|^~\\&|EPIC|HOSP|PERFUSE|DEST|20260921120000||ADT^A01^ADT_A01|MSG00002|P|2.5.1\r" +
		strings.Join(pid, "|") + "\r"

	s := indexing(t)
	id := recordRaw(t, s, "adt", []byte(mrnAlsoInAccount))

	// Two stored rows carry this value for this one message: one as a patient identifier and
	// one as an account. Confirmed here so the test fails loudly if the fixture stops
	// producing both, rather than quietly going back to testing nothing.
	var rows int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM message_identity WHERE message_id = ? AND norm = ?`,
		id, identity.Normalise("MRN0012345")).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows < 2 {
		t.Fatalf("the fixture stored %d rows for that value; the test needs at least 2 to "+
			"exercise grouping at all", rows)
	}

	res := search(t, s, IdentitySearch{Term: "MRN0012345"})
	if res.Total != 1 {
		t.Errorf("total=%d, want 1: the same message matched on two of its identifiers and "+
			"was counted twice", res.Total)
	}
	if len(res.Matches) != 1 {
		t.Errorf("%d matches, want 1: one message is listed more than once, which reads as "+
			"duplicated traffic", len(res.Matches))
	}
}

// Narrowing by kind must work, because a number can be two things.
func TestSearchingOneKindOnly(t *testing.T) {
	s := indexing(t)
	recordRaw(t, s, "adt", []byte(admissionHL7))

	if res := search(t, s, IdentitySearch{
		Term: "MRN0012345", Kind: identity.KindPatientID,
	}); res.Total != 1 {
		t.Errorf("as a patient ID: total=%d, want 1", res.Total)
	}

	// The same term as an account number is not there, and must not be invented.
	if res := search(t, s, IdentitySearch{
		Term: "MRN0012345", Kind: identity.KindAccount,
	}); res.Total != 0 {
		t.Errorf("as an account: total=%d, want 0", res.Total)
	}
}

// An empty result must be distinguishable from an index that was never built.
//
// A site that never switched indexing on searches for a patient, gets nothing, and concludes
// the messages were never received. That is a clinical conclusion drawn from a configuration
// setting, so the result says which it is.
func TestAnUnindexedStoreSaysSoRatherThanLookingEmpty(t *testing.T) {
	s := open(t)
	s.StorePayloads = true
	s.IndexIdentity = false
	recordRaw(t, s, "adt", []byte(admissionHL7))

	res := search(t, s, IdentitySearch{Term: "MRN0012345"})

	if res.Total != 0 {
		t.Errorf("total=%d, want 0 - nothing was indexed", res.Total)
	}
	if res.Indexed {
		t.Error("Indexed is true on a store that has indexing switched off, so an empty " +
			"result is indistinguishable from a patient who was never seen")
	}

	// And with it on, the flag says so.
	on := indexing(t)
	if res := search(t, on, IdentitySearch{Term: "anything"}); !res.Indexed {
		t.Error("Indexed is false on a store that has indexing switched on")
	}
}

// Keeping no payloads must mean keeping no identity index.
//
// Turning StorePayloads off is how a site says it cannot hold clinical content at rest.
// Extracting patient names into an index at the same time would retain exactly what was
// refused, and would do it without saying so.
func TestNoPayloadsMeansNoIdentityIndex(t *testing.T) {
	s := open(t)
	s.StorePayloads = false
	s.IndexIdentity = true // asked for, and must lose to the stronger choice
	recordRaw(t, s, "adt", []byte(admissionHL7))

	res := search(t, s, IdentitySearch{Term: "MRN0012345"})
	if res.Total != 0 {
		t.Errorf("total=%d, want 0: patient identifiers were indexed on an installation "+
			"that keeps no payloads, which retains content the site chose not to keep",
			res.Total)
	}
	if res.Indexed {
		t.Error("Indexed is true although payloads are not stored")
	}
}

// Pruning a message must take its identifiers with it.
//
// Otherwise retention deletes the payload and leaves the patient's name and MRN behind in a
// searchable index, which is the opposite of what a retention window is for.
func TestPruningRemovesIdentityToo(t *testing.T) {
	s := indexing(t)
	s.RetentionDays = 30

	old := time.Now().UTC().AddDate(0, 0, -90)
	record(t, s, &Message{
		Channel:    "adt",
		ReceivedAt: old,
		Outcome:    Delivered,
		Raw:        []byte(admissionHL7),
		Size:       len(admissionHL7),
	})

	if res := search(t, s, IdentitySearch{Term: "MRN0012345"}); res.Total != 1 {
		t.Fatalf("before pruning: total=%d, want 1", res.Total)
	}

	if _, _, err := s.Prune(context.Background()); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if res := search(t, s, IdentitySearch{Term: "MRN0012345"}); res.Total != 0 {
		t.Errorf("after pruning: total=%d, want 0 - the message is gone but its patient "+
			"identifiers are still searchable", res.Total)
	}

	// Directly, because the search joins to messages and would report nothing once the
	// message row is gone even if the identity rows survived. That would hide exactly the
	// leak this test is about.
	var left int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM message_identity`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d identity rows survived the prune, holding patient identifiers for a "+
			"message that no longer exists", left)
	}
}

// A search must not cross tenants.
func TestIdentitySearchIsScopedToOneTenant(t *testing.T) {
	s := indexing(t)
	record(t, s, &Message{
		Channel: "adt", TenantID: "hospital-a", ReceivedAt: time.Now().UTC(),
		Outcome: Delivered, Raw: []byte(admissionHL7), Size: len(admissionHL7),
	})

	if res := search(t, s, IdentitySearch{Term: "MRN0012345", TenantID: "hospital-a"}); res.Total != 1 {
		t.Errorf("the owning tenant found %d, want 1", res.Total)
	}
	if res := search(t, s, IdentitySearch{Term: "MRN0012345", TenantID: "hospital-b"}); res.Total != 0 {
		t.Errorf("another tenant found %d of hospital-a's messages, want 0", res.Total)
	}
}

// An empty term is refused rather than returning the whole index.
func TestAnEmptyTermIsRefused(t *testing.T) {
	s := indexing(t)
	recordRaw(t, s, "adt", []byte(admissionHL7))

	for _, term := range []string{"", "   ", "!!!", "-"} {
		if _, err := s.SearchByIdentity(context.Background(), IdentitySearch{Term: term}); err == nil {
			t.Errorf("searching %q was allowed; a term that normalises to nothing would "+
				"otherwise match every row whose norm is empty", term)
		}
	}
}

// Recording must not fail because a payload cannot be parsed.
func TestAnUnparseablePayloadStillRecords(t *testing.T) {
	s := indexing(t)

	for _, raw := range [][]byte{
		[]byte("this is not a message"),
		{0x00, 0x01, 0x02, 0xff},
		[]byte("MSH|this one starts right and then stops"),
	} {
		id, err := s.Record(context.Background(), &Message{
			Channel: "odd", ReceivedAt: time.Now().UTC(), Outcome: Delivered,
			Raw: raw, Size: len(raw),
		})
		if err != nil {
			t.Errorf("recording a payload identity cannot read failed: %v", err)
		}
		if id == 0 {
			t.Error("no message ID returned")
		}
	}
}
