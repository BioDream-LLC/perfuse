package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

// A channel with a patient index destination registers each ADT's patient, links the same patient from two systems, and
// follows an A40 merge.
func TestAChannelFeedsThePatientIndex(t *testing.T) {
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "mpi.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	idx, err := mpi.Open(context.Background(), pool.Write)
	if err != nil {
		t.Fatal(err)
	}
	PatientIndex = idx
	t.Cleanup(func() { PatientIndex = nil })

	dir := t.TempDir()
	ch := startYAML(t, dir, "adt", `name: adt
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: index
    type: mpi
    mpi:
      authorities: {HOSP: "urn:oid:1.2.3", CLIN: "urn:oid:1.2.4"}
`)
	ch.SetTenant("default")
	send := func(m string) {
		t.Helper()
		if _, err := ch.handle(context.Background(), []byte(m)); err != nil {
			t.Fatal(err)
		}
		if got := ch.LastOutcomeForTest(); got != Delivered {
			t.Fatalf("outcome %v", got)
		}
	}
	send("MSH|^~\\&|REG|HOSP|MPI|X|20261017||ADT^A04|1|P|2.5\rPID|1||100^^^HOSP^MR||Moreau^Claire||19770912|F|||3 Rue X^^Lyon^^69001\r")
	send("MSH|^~\\&|REG|CLIN|MPI|X|20261017||ADT^A04|2|P|2.5\rPID|1||A7^^^CLIN^MR||MOREAU^CLAIRE||19770912|F|||^^Lyon^^69001\r")
	send("MSH|^~\\&|REG|HOSP|MPI|X|20261017||ADT^A04|3|P|2.5\rPID|1||101^^^HOSP^MR||Moreau^Clara||19770912|F\r")
	p, err := idx.ByIdentifier(context.Background(), "default", "urn:oid:1.2.4|A7")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 2 {
		t.Fatalf("the hospital and clinic records were not linked: %d records", len(p.Records))
	}
	// The hospital registered Clara twice; A40 merges 101 into 100.
	send("MSH|^~\\&|REG|HOSP|MPI|X|20261017||ADT^A40|4|P|2.5\rPID|1||100^^^HOSP^MR||Moreau^Claire\rMRG|101^^^HOSP^MR\r")
	p, _ = idx.ByIdentifier(context.Background(), "default", "urn:oid:1.2.3|101")
	if len(p.Records) != 3 {
		t.Errorf("after the merge %d records", len(p.Records))
	}
}
