package privacy

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/queue"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

const nhsSys = "https://fhir.nhs.uk/Id/nhs-number"

// adt carries the person's hospital number and NHS number.
func adt(mrn, nhs string) []byte {
	return []byte("MSH|^~\\&|PAS|HOSP|EPR|HOSP|20260101120000||ADT^A04|" + mrn + "|P|2.5\r" +
		"EVN|A04|20260101120000\r" +
		"PID|1||" + mrn + "^^^HOSP^MR~" + nhs + "^^^NHS^NH||JONES^ANN||19700101|F\r")
}

type rig struct {
	svc   *Service
	msgs  *msgstore.Store
	fhir  *fhirserver.Store
	index *mpi.Store
	queue *queue.Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "p.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	r := &rig{}
	if r.msgs, err = msgstore.NewStoreWithReader(pool.Write, pool.Read); err != nil {
		t.Fatal(err)
	}
	r.msgs.StorePayloads, r.msgs.IndexIdentity = true, true
	if r.fhir, err = fhirserver.NewStore(pool.Write, fhir.R4); err != nil {
		t.Fatal(err)
	}
	if r.index, err = mpi.Open(ctx, pool.Write); err != nil {
		t.Fatal(err)
	}
	if r.queue, err = queue.NewStore(pool.Write, pool.Read); err != nil {
		t.Fatal(err)
	}
	if r.svc, err = Open(ctx, pool.Write); err != nil {
		t.Fatal(err)
	}
	r.svc.Messages, r.svc.FHIR, r.svc.Index, r.svc.Queue = r.msgs, r.fhir, r.index, r.queue
	return r
}

// seed puts the same person in every store: two messages, a queued delivery, a Patient with an Observation, and two
// linked records in the patient index (the second known only by the clinic's number and the NHS number).
func (r *rig) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, raw := range [][]byte{adt("H1", "9990000018"), adt("H1", "9990000018")} {
		if _, err := r.msgs.Record(ctx, &msgstore.Message{Channel: "adt", ReceivedAt: time.Now().UTC(), Outcome: msgstore.Delivered, Raw: raw, Size: len(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	other := adt("H2", "9990000026")
	if _, err := r.msgs.Record(ctx, &msgstore.Message{Channel: "adt", ReceivedAt: time.Now().UTC(), Outcome: msgstore.Delivered, Raw: other, Size: len(other)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.queue.Enqueue(ctx, queue.Item{Channel: "adt", Destination: "epr", Raw: adt("H1", "9990000018")}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"resourceType":"Patient","id":"p1","identifier":[{"system":"` + nhsSys + `","value":"9990000018"}],"name":[{"family":"Jones"}]}`,
		`{"resourceType":"Observation","id":"o1","status":"final","code":{"text":"x"},"subject":{"reference":"Patient/p1"}}`,
		`{"resourceType":"Patient","id":"p2","identifier":[{"system":"` + nhsSys + `","value":"9990000026"}]}`,
	} {
		res, err := fhir.UnmarshalResource([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.fhir.Put(ctx, res); err != nil {
			t.Fatal(err)
		}
	}
	set := mpi.Settings{Trusted: []string{nhsSys}}
	if _, err := r.index.Register(ctx, mpi.TenantOf(""), "t", mpi.Record{System: "urn:oid:1.1", Value: "H1", Family: "Jones", Given: "Ann",
		Identifiers: []string{nhsSys + "|9990000018"}}, set); err != nil {
		t.Fatal(err)
	}
	if _, err := r.index.Register(ctx, mpi.TenantOf(""), "t", mpi.Record{System: "urn:oid:1.2", Value: "C77", Family: "Jones",
		Identifiers: []string{nhsSys + "|999 000 0018"}}, set); err != nil {
		t.Fatal(err)
	}
}

func TestFindExportAndEraseAPersonEverywhere(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	r.seed(t)
	// Asked by the clinic's number only: the patient index widens it to the NHS number and the hospital number.
	f, err := r.svc.Find(ctx, "", []string{"urn:oid:1.2|C77"}, mpi.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"messages": 2, "queue": 1, "fhir": 2, "persons": 1, "pas": 0}
	for k, v := range want {
		if f.Counts[k] != v {
			t.Errorf("%s: %d, want %d (%+v)", k, f.Counts[k], v, f.Counts)
		}
	}
	// The export holds the messages as received and every FHIR version.
	var zipped bytes.Buffer
	if err := r.svc.Export(ctx, "", f, &zipped); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped.Bytes()), int64(zipped.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, zf := range zr.File {
		names[zf.Name] = true
	}
	for _, n := range []string{"README.txt", "manifest.json", "fhir/Patient-p1.v1.json", "fhir/Observation-o1.v1.json", "index.json"} {
		if !names[n] {
			t.Errorf("export lacks %s: %v", n, names)
		}
	}
	msgs := 0
	for n := range names {
		if strings.HasPrefix(n, "messages/") && strings.HasSuffix(n, ".message") {
			msgs++
		}
	}
	if msgs != 2 {
		t.Errorf("%d messages exported", msgs)
	}

	// A legal hold refuses the erasure.
	hid, err := r.svc.PlaceHold(ctx, "", "dpo", []string{nhsSys + "|9990000018"}, "claim 2026/17", "")
	if err != nil {
		t.Fatal(err)
	}
	f, _ = r.svc.Find(ctx, "", []string{"urn:oid:1.2|C77"}, mpi.Settings{})
	if _, err := r.svc.Erase(ctx, "", 1, f, "admin", "dpo"); err != ErrHeld {
		t.Fatalf("erasure under a hold: %v", err)
	}
	// And keeps the messages from the retention window.
	if held := r.svc.HeldNorms(ctx); len(held) != 1 {
		t.Fatalf("held %v", held)
	}
	if err := r.svc.LiftHold(ctx, "", "dpo", hid); err != nil {
		t.Fatal(err)
	}
	f, _ = r.svc.Find(ctx, "", []string{"urn:oid:1.2|C77"}, mpi.Settings{})
	counts, err := r.svc.Erase(ctx, "", 1, f, "admin", "dpo")
	if err != nil {
		t.Fatal(err)
	}
	if counts["messages"] != 2 || counts["queue"] != 1 || counts["fhir"] != 2 || counts["persons"] != 1 || counts["records"] != 2 {
		t.Fatalf("erased %v", counts)
	}
	after, _ := r.svc.Find(ctx, "", []string{nhsSys + "|9990000018"}, mpi.Settings{})
	for _, k := range []string{"messages", "queue", "fhir", "persons"} {
		if after.Counts[k] != 0 {
			t.Errorf("%s still holds %d", k, after.Counts[k])
		}
	}
	// The other person is untouched.
	other, _ := r.svc.Find(ctx, "", []string{nhsSys + "|9990000026"}, mpi.Settings{})
	if other.Counts["messages"] != 1 || other.Counts["fhir"] != 1 {
		t.Fatalf("someone else's records went too: %v", other.Counts)
	}
	// The ledger has the erasure without the numbers.
	ers, _ := r.svc.Erasures(ctx, "")
	if len(ers) != 1 || ers[0].Counts["messages"] != 2 {
		t.Fatalf("ledger %+v", ers)
	}
	for _, h := range ers[0].hashes {
		if strings.Contains(h, "9990000018") {
			t.Fatal("the ledger holds the number")
		}
	}

	// A backup restored: the person's records are back, and applying the ledger again erases them.
	r.seed(t)
	cands, err := r.svc.Candidates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.svc.Reapply(ctx, "", "admin", cands, mpi.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if again["messages"] != 2 || again["fhir"] != 2 {
		t.Fatalf("reapplied %v", again)
	}
	if other, _ := r.svc.Find(ctx, "", []string{nhsSys + "|9990000026"}, mpi.Settings{}); other.Counts["messages"] != 2 {
		t.Fatalf("reapplying took someone else's: %v", other.Counts)
	}
}

func TestRequestsNeedASecondPersonToErase(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	id, err := r.svc.Create(ctx, "", "alice", Request{Kind: "erasure", Subject: "Ann Jones, letter of 3 January", Identifiers: []string{nhsSys + "|999 000 0018"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := r.svc.Get(ctx, "", id)
	if req.Identifiers[0] != nhsSys+"|9990000018" || req.Due != time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02") {
		t.Fatalf("%+v", req)
	}
	if err := r.svc.Approve(ctx, "", "alice", id); err == nil {
		t.Fatal("the person who recorded an erasure approved it")
	}
	if err := r.svc.Approve(ctx, "", "bob", id); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Close(ctx, "", "bob", id, "refused", ""); err == nil {
		t.Fatal("a refusal without its reason was accepted")
	}
	if err := r.svc.Close(ctx, "", "bob", id, "done", "erased"); err != nil {
		t.Fatal(err)
	}
	req, _ = r.svc.Get(ctx, "", id)
	if req.Status != "done" || strings.Contains(req.Identifiers[0], "943476") {
		t.Fatalf("a closed request should keep only the last characters: %+v", req)
	}
	if _, err := r.svc.Create(ctx, "", "alice", Request{Kind: "forget", Subject: "x", Identifiers: []string{"1"}}); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

func TestTheRegisterEscapesFormulas(t *testing.T) {
	csv := string(RegisterCSV(Controller{Name: "=cmd()"}, []Activity{{Channel: "a", Description: "+1"}}))
	if !strings.Contains(csv, "'=cmd()") || !strings.Contains(csv, "'+1") {
		t.Fatal(csv)
	}
	md := string(RegisterMarkdown(Controller{}, []Activity{{Channel: "adt", Source: "mllp", Listen: ":2575", Recipients: []string{"epr"}, Hosts: []string{"epr.example.org"}}}, time.Now()))
	if !strings.Contains(md, "| adt |") || !strings.Contains(md, "for the controller to fill in") {
		t.Fatal(md)
	}
}
