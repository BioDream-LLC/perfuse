package dicom

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// getArchive is a dcmqrscp holding one study of two instances.
//
// dcmqrscp answers C-GET by default - it has a --disable-get flag, which is how that was established - so the other end of
// these tests is DCMTK rather than ours. That matters more for retrieval than for anything else here, because C-GET requires
// role negotiation and a peer that agrees to it is the only way to know the negotiation is right.
type getArchive struct {
	port     string
	dir      string
	studyUID string
	patient  string
}

func startGetArchive(t *testing.T) *getArchive {
	t.Helper()

	for _, tool := range []string{"dcmqrscp", "dump2dcm", "storescu"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; install dcmtk to run the retrieve interoperability tests", tool)
		}
	}

	dir := t.TempDir()
	db := dir + "/db"
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	cfg := dir + "/qr.cfg"
	contents := "NetworkTCPPort = " + port + "\nMaxPDUSize = 16384\nMaxAssociations = 16\n\n" +
		"HostTable BEGIN\nHostTable END\n\n" +
		"AETable BEGIN\nGETTESTARC " + db + " RW (100, 512mb) ANY\nAETable END\n"
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

	a := &getArchive{
		port:     port,
		dir:      dir,
		studyUID: "1.2.826.0.1.3680043.8.4101.1",
		patient:  "MRN8001",
	}
	a.load(t)
	return a
}

// load stores one study of two instances.
//
// Two instances rather than one, because a retrieve that transfers a single object cannot show that the interleaving works:
// the archive sends a store, waits for the response, then sends the next, and a client that mishandles the acknowledgement
// stalls on the second one rather than the first.
func (a *getArchive) load(t *testing.T) {
	t.Helper()

	var files []string
	for i := 1; i <= 2; i++ {
		n := strconv.Itoa(i)
		dump := "(0008,0005) CS [ISO_IR 100]\n" +
			"(0008,0016) UI [1.2.840.10008.5.1.4.1.1.7]\n" +
			"(0008,0018) UI [" + a.studyUID + "." + n + "]\n" +
			"(0008,0020) DA [20260821]\n" +
			"(0008,0030) TM [140000]\n" +
			"(0008,0050) SH [ACCGET01]\n" +
			"(0008,0060) CS [CT]\n" +
			"(0008,1030) LO [CT PELVIS]\n" +
			"(0010,0010) PN [THIBODEAUX^RENE]\n" +
			"(0010,0020) LO [" + a.patient + "]\n" +
			"(0010,0030) DA [19700404]\n" +
			"(0010,0040) CS [M]\n" +
			"(0020,000D) UI [" + a.studyUID + "]\n" +
			"(0020,000E) UI [" + a.studyUID + ".10]\n" +
			"(0020,0011) IS [1]\n" +
			"(0020,0013) IS [" + n + "]\n"

		dumpPath := a.dir + "/g" + n + ".dump"
		dcmPath := a.dir + "/g" + n + ".dcm"
		if err := os.WriteFile(dumpPath, []byte(dump), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("dump2dcm", "-g", dumpPath, dcmPath).CombinedOutput(); err != nil {
			t.Fatalf("dump2dcm: %v: %s", err, out)
		}
		files = append(files, dcmPath)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		args := append([]string{"-aec", "GETTESTARC", "-aet", "PERFUSETEST", "127.0.0.1", a.port}, files...)
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

func (a *getArchive) client() *Client {
	return &Client{
		Addr:      "127.0.0.1:" + a.port,
		CalledAE:  "GETTESTARC",
		CallingAE: "PERFUSETEST",
		Timeout:   30 * time.Second,
	}
}

// TestGetStudy retrieves a whole study over one association.
//
// The patient identifier is included alongside the study UID because dcmqrscp answers under the patient root model, where the
// hierarchy requires the keys above the level being retrieved. Real archives enforce the same rule, and finding it cost an
// hour: a retrieve missing the higher key returns success with zero sub-operations, which reads as an empty study rather than
// as a malformed request.
func TestGetStudy(t *testing.T) {
	archive := startGetArchive(t)

	var got []RetrievedObject
	result, err := archive.client().Get(context.Background(), RetrieveRequest{
		Level:       LevelStudy,
		PatientRoot: true,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO", Value: []byte(archive.patient)},
			{Tag: TagStudyInstanceUID, VR: "UI", Value: []byte(archive.studyUID)},
		},
	}, func(o RetrievedObject) error {
		got = append(got, o)
		return nil
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("retrieved %d objects, want 2", len(got))
	}
	if result.Completed != 2 {
		t.Errorf("the archive reported %d completed sub-operations, want 2", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("the archive reported %d failed sub-operations", result.Failed)
	}

	// Each object must be readable and be the study asked for. A retrieve that returns the right number of unreadable
	// objects would otherwise pass.
	for i, o := range got {
		if o.DataSet == nil {
			t.Fatalf("object %d had no parsed data set", i)
		}
		if uid := o.DataSet.Text(TagStudyInstanceUID); uid != archive.studyUID {
			t.Errorf("object %d belongs to study %q, not the one requested", i, uid)
		}
		if o.DataSet.Text(TagPatientName) != "THIBODEAUX^RENE" {
			t.Errorf("object %d has patient name %q", i, o.DataSet.Text(TagPatientName))
		}
		if o.SOPInstanceUID == "" {
			t.Errorf("object %d came back with no SOP instance uid", i)
		}
	}

	if got[0].SOPInstanceUID == got[1].SOPInstanceUID {
		t.Error("both objects have the same SOP instance uid, so the same one was delivered twice")
	}
}

// TestGetWrappedObjectIsReadable checks the retrieved bytes are a valid file.
//
// The object arrives without a meta group, because its transfer syntax belongs to the association. If the wrapping is wrong
// the bytes are still nearly right, so this parses them the way any consumer would rather than trusting the length.
func TestGetWrappedObjectIsReadable(t *testing.T) {
	archive := startGetArchive(t)

	var first []byte
	_, err := archive.client().Get(context.Background(), RetrieveRequest{
		Level:       LevelStudy,
		PatientRoot: true,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO", Value: []byte(archive.patient)},
			{Tag: TagStudyInstanceUID, VR: "UI", Value: []byte(archive.studyUID)},
		},
		MaxObjects: 1,
	}, func(o RetrievedObject) error {
		if first == nil {
			first = o.Raw
		}
		return nil
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("no object was retrieved")
	}

	// Parse rather than ParseDataSet: this is meant to be a complete file, so it must be readable without being told
	// anything about its encoding. That is the whole point of wrapping it.
	ds, err := Parse(first)
	if err != nil {
		t.Fatalf("the retrieved object is not a readable file: %v", err)
	}
	if got := ds.Text(TagPatientName); got != "THIBODEAUX^RENE" {
		t.Errorf("the wrapped object reads back with patient name %q", got)
	}
	if ds.TransferSyntax == "" {
		t.Error("the wrapped object declares no transfer syntax, so nothing downstream could read it")
	}
}

// TestGetRefusesEmptyKey checks that a retrieve key with no value is refused before anything is sent.
//
// An empty key is a query idiom - "return this field" - and meaningless in a retrieve. An archive given one either ignores it
// or matches everything, so refusing locally is the only way the caller learns which it was.
func TestGetRefusesEmptyKey(t *testing.T) {
	client := &Client{Addr: "127.0.0.1:1", CalledAE: "X", Timeout: time.Second}

	_, err := client.Get(context.Background(), RetrieveRequest{
		Level: LevelStudy,
		Match: []Element{{Tag: TagStudyInstanceUID, VR: "UI"}},
	}, nil)

	if err == nil {
		t.Fatal("a retrieve with an empty key was accepted")
	}
	// Refused before dialling, which is the point: the address is unroutable, so a connection error would mean the check
	// happened too late.
	if got := err.Error(); !contains(got, "has to say which object") {
		t.Errorf("the refusal was %q, which does not explain the problem", got)
	}
}

// TestGetMaxObjectsStops checks that a cap stops the transfer.
func TestGetMaxObjectsStops(t *testing.T) {
	archive := startGetArchive(t)

	count := 0
	_, err := archive.client().Get(context.Background(), RetrieveRequest{
		Level:       LevelStudy,
		PatientRoot: true,
		Match: []Element{
			{Tag: TagPatientID, VR: "LO", Value: []byte(archive.patient)},
			{Tag: TagStudyInstanceUID, VR: "UI", Value: []byte(archive.studyUID)},
		},
		MaxObjects: 1,
	}, func(RetrievedObject) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("get with a cap: %v", err)
	}
	if count != 1 {
		t.Fatalf("a cap of 1 delivered %d objects", count)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
