package dicom

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// findArchive is a running dcmqrscp, or a skip.
//
// The whole value of these tests is that the other end is not ours. dcmqrscp is DCMTK's queryable archive, and it is the
// same code path a real PACS vendor tests their own conformance against, so a query it answers is a query built to the
// standard rather than to my reading of it.
type findArchive struct {
	port string
	dir  string
}

func startFindArchive(t *testing.T) *findArchive {
	t.Helper()

	for _, tool := range []string{"dcmqrscp", "dump2dcm", "storescu"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; install dcmtk to run the query interoperability tests", tool)
		}
	}

	dir := t.TempDir()
	db := dir + "/db"
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}

	// A port per archive. A fixed one collided across tests: the second dcmqrscp could not bind, queries went to the
	// first one's database, and the failures looked like query bugs rather than a harness bug. Found by probing a
	// hand-started archive, which behaved differently from the test.
	port := freePort(t)
	cfg := dir + "/qr.cfg"
	// ANY rather than a host table entry. A host table matches on the resolved name, and whether 127.0.0.1 resolves to
	// "localhost" varies by machine, which made this fail once already for a reason that has nothing to do with DICOM.
	contents := "NetworkTCPPort = " + port + "\nMaxPDUSize = 16384\nMaxAssociations = 16\n\n" +
		"HostTable BEGIN\nHostTable END\n\n" +
		"AETable BEGIN\nQRTESTARC " + db + " RW (100, 512mb) ANY\nAETable END\n"
	if err := os.WriteFile(cfg, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("dcmqrscp", "-c", cfg, port)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Skipf("dcmqrscp would not start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	a := &findArchive{port: port, dir: dir}
	a.load(t)
	return a
}

// load puts three studies in the archive: two for one patient, one for another.
//
// Two for one patient matters. A query that returns one result cannot tell the difference between matching correctly and
// matching nothing but the first row, and a query that returns everything looks identical to a query with a broken filter.
func (a *findArchive) load(t *testing.T) {
	t.Helper()

	studies := []struct{ mrn, name, modality, accession, uid, date, desc string }{
		{"MRN0009001", "FROST^IVY^MARIE", "CT", "ACC77321", "1.2.826.0.1.3680043.8.2101.1", "20260801", "CT CHEST W CONTRAST"},
		{"MRN0009001", "FROST^IVY^MARIE", "MR", "ACC77322", "1.2.826.0.1.3680043.8.2101.2", "20260815", "MR BRAIN WO CONTRAST"},
		{"MRN0009002", "OKONKWO^ADA", "CT", "ACC77323", "1.2.826.0.1.3680043.8.2101.3", "20260820", "CT ABDOMEN PELVIS"},
	}

	var files []string
	for i, s := range studies {
		dump := "(0008,0005) CS [ISO_IR 100]\n" +
			"(0008,0016) UI [1.2.840.10008.5.1.4.1.1.7]\n" +
			"(0008,0018) UI [" + s.uid + ".1]\n" +
			"(0008,0020) DA [" + s.date + "]\n" +
			"(0008,0030) TM [101500]\n" +
			"(0008,0050) SH [" + s.accession + "]\n" +
			"(0008,0060) CS [" + s.modality + "]\n" +
			"(0008,1030) LO [" + s.desc + "]\n" +
			"(0010,0010) PN [" + s.name + "]\n" +
			"(0010,0020) LO [" + s.mrn + "]\n" +
			"(0010,0030) DA [19910228]\n" +
			"(0010,0040) CS [F]\n" +
			"(0020,000D) UI [" + s.uid + "]\n" +
			"(0020,000E) UI [" + s.uid + ".10]\n" +
			"(0020,0011) IS [1]\n" +
			"(0020,0013) IS [1]\n"

		dumpPath := a.dir + "/s" + itoa(i) + ".dump"
		dcmPath := a.dir + "/s" + itoa(i) + ".dcm"
		if err := os.WriteFile(dumpPath, []byte(dump), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("dump2dcm", "-g", dumpPath, dcmPath).CombinedOutput(); err != nil {
			t.Fatalf("dump2dcm: %v: %s", err, out)
		}
		files = append(files, dcmPath)
	}

	// Waited for rather than assumed ready. dcmqrscp takes a moment to open its port, and a store that races it fails in a
	// way that looks like a query bug three tests later.
	deadline := time.Now().Add(10 * time.Second)
	for {
		args := append([]string{"-aec", "QRTESTARC", "-aet", "PERFUSETEST", "127.0.0.1", a.port}, files...)
		out, err := exec.Command("storescu", args...).CombinedOutput()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not load the archive: %v: %s", err, out)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (a *findArchive) client() *Client {
	return &Client{
		Addr:      "127.0.0.1:" + a.port,
		CalledAE:  "QRTESTARC",
		CallingAE: "PERFUSETEST",
		Timeout:   15 * time.Second,
	}
}

func itoa(i int) string {
	return string(rune('1' + i))
}

// freePort asks the kernel for an unused port and returns it as a string.
//
// Closed again before use, which is a race in principle. In practice nothing else on a test machine claims it in the
// microseconds between, and the alternative - a fixed port - already caused a whole afternoon's worth of confusing
// failures.
func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()

	return strconv.Itoa(addr.Port)
}

// TestFindStudiesByPatientID queries a real archive for one patient's studies.
//
// Two expected, and the patient with one study must not appear. That is the assertion that catches a filter which is being
// ignored, which is the failure mode that matters: an ignored filter returns everything and still looks like it worked.
func TestFindStudiesByPatientID(t *testing.T) {
	archive := startFindArchive(t)
	client := archive.client()

	results, err := client.Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO", Value: []byte("MRN0009001")},
			// Empty means "return this field". Present and empty is a request; absent means the archive need not send it.
			{Tag: TagPatientName, VR: "PN"},
			{Tag: TagAccessionNumber, VR: "SH"},
			{Tag: TagStudyInstanceUID, VR: "UI"},
			{Tag: TagStudyDate, VR: "DA"},
		},
	})
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	if len(results) != 2 {
		var got []string
		for _, r := range results {
			got = append(got, r.DataSet.Text(TagAccessionNumber)+"/"+r.DataSet.Text(TagPatientID))
		}
		t.Fatalf("expected 2 studies for MRN0009001, got %d: %v", len(results), got)
	}

	seen := map[string]bool{}
	for _, r := range results {
		if id := r.DataSet.Text(TagPatientID); id != "MRN0009001" {
			t.Errorf("a result came back for patient %q, which was not the one queried", id)
		}
		seen[r.DataSet.Text(TagAccessionNumber)] = true
	}

	for _, want := range []string{"ACC77321", "ACC77322"} {
		if !seen[want] {
			t.Errorf("accession %s was not among the results", want)
		}
	}
}

// TestFindRequestedFieldsComeBack checks that an empty key is answered rather than ignored.
//
// This is the part of DICOM querying newcomers get wrong, so it is worth asserting directly rather than inferring from a
// count: a field asked for by sending it empty must come back populated.
func TestFindRequestedFieldsComeBack(t *testing.T) {
	archive := startFindArchive(t)

	results, err := archive.client().Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{
			{Tag: TagAccessionNumber, VR: "SH", Value: []byte("ACC77323")},
			{Tag: TagPatientName, VR: "PN"},
			{Tag: TagPatientID, VR: "LO"},
			{Tag: TagStudyInstanceUID, VR: "UI"},
		},
	})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 study for ACC77323, got %d", len(results))
	}

	ds := results[0].DataSet
	if got := ds.Text(TagPatientID); got != "MRN0009002" {
		t.Errorf("patient id came back as %q, want MRN0009002", got)
	}
	if got := ds.Text(TagPatientName); got != "OKONKWO^ADA" {
		t.Errorf("patient name came back as %q, want OKONKWO^ADA", got)
	}
	if ds.Text(TagStudyInstanceUID) == "" {
		t.Error("the study instance uid was requested and came back empty, so the archive did not answer the key")
	}
}

// TestFindWildcard checks wildcard matching, which is how any search box over DICOM works.
func TestFindWildcard(t *testing.T) {
	archive := startFindArchive(t)

	results, err := archive.client().Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{
			{Tag: TagPatientName, VR: "PN", Value: []byte("FROST*")},
			{Tag: TagPatientID, VR: "LO"},
			{Tag: TagAccessionNumber, VR: "SH"},
		},
	})
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("FROST* matched %d studies, want 2", len(results))
	}
}

// TestFindLimitCancels checks that a limit stops the query.
//
// The limit exists because a mistyped query at image level against a real archive returns hundreds of thousands of
// responses, and the difference between a bug and an outage is whether anything stops it. A limit that returned the right
// count while leaving the association mid-query would pass a naive test and strand the archive, so the query that follows
// proves the connection is still usable.
func TestFindLimitCancels(t *testing.T) {
	archive := startFindArchive(t)
	client := archive.client()

	results, err := client.Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO"},
			{Tag: TagStudyInstanceUID, VR: "UI"},
		},
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("find with a limit: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("a limit of 1 returned %d results", len(results))
	}

	// The archive must still be usable. A cancel that left it confused is the failure this catches.
	after, err := client.Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{{Tag: TagPatientID, VR: "LO"}, {Tag: TagStudyInstanceUID, VR: "UI"}},
	})
	if err != nil {
		t.Fatalf("the archive was left unusable after a cancelled query: %v", err)
	}
	if len(after) != 3 {
		t.Errorf("after a cancelled query an unlimited one returned %d studies, want 3", len(after))
	}
}

// TestFindNoMatches checks that nothing found is not an error.
//
// An empty result and a failed query are different things, and code that cannot tell them apart either alerts on a patient
// having no priors or stays silent when the archive is down.
func TestFindNoMatches(t *testing.T) {
	archive := startFindArchive(t)

	results, err := archive.client().Find(context.Background(), QueryRequest{
		Level: LevelStudy,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO", Value: []byte("MRN-DOES-NOT-EXIST")},
			{Tag: TagStudyInstanceUID, VR: "UI"},
		},
	})
	if err != nil {
		t.Fatalf("a query matching nothing returned an error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("a query for a patient that does not exist returned %d results", len(results))
	}
}

// TestFindSeriesLevel checks a series-level query, which returns a different rung of the hierarchy.
func TestFindSeriesLevel(t *testing.T) {
	archive := startFindArchive(t)

	results, err := archive.client().Find(context.Background(), QueryRequest{
		Level: LevelSeries,
		Match: []Element{
			{Tag: TagStudyInstanceUID, VR: "UI", Value: []byte("1.2.826.0.1.3680043.8.2101.1")},
			{Tag: TagSeriesInstanceUID, VR: "UI"},
			{Tag: TagModality, VR: "CS"},
		},
	})
	if err != nil {
		t.Fatalf("series query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 series, got %d", len(results))
	}
	if got := results[0].DataSet.Text(TagModality); got != "CT" {
		t.Errorf("series modality came back %q, want CT", got)
	}
}
