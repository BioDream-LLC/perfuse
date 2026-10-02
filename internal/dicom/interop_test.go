package dicom

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Interoperability tests against DCMTK, the OFFIS toolkit most hospital equipment and most other DICOM software is built
// against.
//
// Skipped when DCMTK is absent, so the suite still runs anywhere, but these are the tests that matter most: everything
// else in this package proves the implementation agrees with my reading of the standard, and that shares an author with
// the implementation. Talking to storescu and storescp proves it agrees with the reference.
func dcmtk(t *testing.T, tool string) string {
	t.Helper()
	path, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s is not installed; install DCMTK to run interoperability tests", tool)
	}
	return path
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&Client{Addr: addr}).dial(context.Background())
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing came up on %s", addr)
}

func TestOurClientEchoesToDCMTKStorescp(t *testing.T) {
	dcmtk(t, "storescp")

	dir := t.TempDir()
	cmd := exec.Command("storescp", "--aetitle", "DCMTKSCP", "-od", dir, "11112")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitForPort(t, "127.0.0.1:11112")

	client := &Client{Addr: "127.0.0.1:11112", CalledAE: "DCMTKSCP", CallingAE: "PERFUSE", Timeout: 15 * time.Second}
	if err := client.Echo(context.Background()); err != nil {
		t.Fatalf("C-ECHO to DCMTK failed: %v", err)
	}
}

func TestOurClientStoresToDCMTKStorescp(t *testing.T) {
	dcmtk(t, "storescp")

	dir := t.TempDir()
	cmd := exec.Command("storescp", "--aetitle", "DCMTKSCP", "-od", dir, "11113")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitForPort(t, "127.0.0.1:11113")

	raw, err := os.ReadFile(filepath.Join("testdata", explicitFixture))
	if err != nil {
		t.Fatal(err)
	}

	client := &Client{Addr: "127.0.0.1:11113", CalledAE: "DCMTKSCP", CallingAE: "PERFUSE", Timeout: 20 * time.Second}
	if err := client.Send(context.Background(), raw); err != nil {
		t.Fatalf("C-STORE to DCMTK failed: %v", err)
	}

	// DCMTK writes what it received. If the file is there, the reference implementation accepted our association, our
	// presentation context, our command set and our data set - which is the whole protocol.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("DCMTK acknowledged the object but wrote nothing")
	}

	stored, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := Parse(stored)
	if err != nil {
		t.Fatalf("DCMTK wrote something we cannot read back: %v", err)
	}
	if got := ds.Text(TagPatientID); got != "MRN0009001" {
		t.Errorf("the object DCMTK stored has patient id %q, want MRN0009001", got)
	}
	if got := ds.Text(TagSOPInstanceUID); got != "1.2.826.0.1.3680043.8.1055.1" {
		t.Errorf("SOP instance UID = %q", got)
	}
}

func TestDCMTKStorescuStoresToOurServer(t *testing.T) {
	dcmtk(t, "storescu")

	var received [][]byte
	var receivedIDs []string

	srv := &Server{
		AETitle: "PERFUSE",
		Handler: func(ds *DataSet, raw []byte) error {
			received = append(received, raw)
			receivedIDs = append(receivedIDs, ds.Text(TagPatientID))
			return nil
		},
		IdleTimeout: 15 * time.Second,
	}
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	addr := srv.Addr()
	host, port, err := splitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("storescu",
		"-aec", "PERFUSE", "-aet", "DCMTKSCU",
		host, port,
		filepath.Join("testdata", explicitFixture)).CombinedOutput()
	if err != nil {
		t.Fatalf("storescu failed: %v\n%s", err, out)
	}

	if len(received) != 1 {
		t.Fatalf("received %d objects from storescu, want 1\nstorescu said: %s", len(received), out)
	}
	if receivedIDs[0] != "MRN0009001" {
		t.Errorf("received patient id %q, want MRN0009001", receivedIDs[0])
	}
}

func TestDCMTKEchoscuReachesOurServer(t *testing.T) {
	// The first thing anybody runs against a new DICOM endpoint. A listener that stores images but cannot answer an echo
	// looks broken to whoever is commissioning it.
	dcmtk(t, "echoscu")

	srv := &Server{
		AETitle:     "PERFUSE",
		Handler:     func(*DataSet, []byte) error { return nil },
		IdleTimeout: 10 * time.Second,
	}
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	host, port, err := splitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("echoscu", "-aec", "PERFUSE", "-aet", "DCMTKSCU", host, port).CombinedOutput()
	if err != nil {
		t.Fatalf("echoscu failed: %v\n%s", err, out)
	}
}

func TestOurServerRejectsTheWrongCalledAETitle(t *testing.T) {
	// A site's only access control on a DICOM endpoint is frequently the AE title, so this has to actually work - and the
	// rejection has to name which title was wrong, or the operator at the other end is guessing between two.
	dcmtk(t, "echoscu")

	srv := &Server{
		AETitle:     "PERFUSE",
		Handler:     func(*DataSet, []byte) error { return nil },
		IdleTimeout: 10 * time.Second,
	}
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	host, port, err := splitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("echoscu", "-aec", "WRONGNAME", "-aet", "DCMTKSCU", host, port).CombinedOutput()
	if err == nil {
		t.Errorf("an association with the wrong called AE title succeeded\n%s", out)
	}
}

func splitHostPort(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port, nil
}
