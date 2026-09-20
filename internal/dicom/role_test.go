package dicom

import (
	"net"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// TestRoleSelectionFromRealSCU checks that role selection proposed by DCMTK is decoded correctly.
//
// This exists because the retrieve interoperability tests cannot verify role negotiation, and that was worth finding out
// rather than assuming. Removing the SCP role proposal entirely left every C-GET test passing: dcmqrscp echoes back scp=true
// even when scp=false was proposed, granting the role unilaterally and sending the images regardless.
//
// So the negotiation is verified from the other side instead. DCMTK's getscu is a real C-GET initiator, and pointing it at our
// server means its encoder against our decoder - which is the part that would otherwise be checked only against itself.
//
// The retrieve then fails, because our server does not answer C-GET. That is expected and is not what is being tested.
func TestRoleSelectionFromRealSCU(t *testing.T) {
	if _, err := exec.LookPath("getscu"); err != nil {
		t.Skip("getscu is not installed; install dcmtk to verify role selection against a real initiator")
	}

	var mu sync.Mutex
	var seen []RoleSelection
	var contexts []PresentationContext
	done := make(chan struct{})
	var once sync.Once

	srv := &Server{
		AETitle: "ROLETEST",
		Handler: func(*DataSet, []byte) error { return nil },
		OnAssociate: func(req *AssociateRequest) {
			mu.Lock()
			seen = append(seen, req.Roles...)
			contexts = append(contexts, req.Contexts...)
			mu.Unlock()
			once.Do(func() { close(done) })
		},
	}

	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}

	// The retrieve is expected to fail; the association is the part under test.
	cmd := exec.Command("getscu",
		"-aec", "ROLETEST", "-aet", "GETSCUTEST",
		"-k", "QueryRetrieveLevel=STUDY",
		"-k", "PatientID=NOBODY",
		"-k", "StudyInstanceUID=1.2.3.4.5",
		"127.0.0.1", port)
	_ = cmd.Run()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("getscu never associated")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(seen) == 0 {
		t.Fatal("a real C-GET initiator proposed no role selection items, so either it changed or the decoder is wrong")
	}

	// Every role a C-GET initiator proposes for a storage class asks to be the provider, because the images come back on
	// this association. Any of them being SCU-only would mean the sub-item was misread.
	storageRoles := 0
	for _, r := range seen {
		if r.SOPClass == "" {
			t.Errorf("a role selection item decoded with no SOP class: %+v", r)
			continue
		}
		if r.SCPRole {
			storageRoles++
		}
	}

	if storageRoles == 0 {
		t.Errorf("none of the %d role items proposed the provider role, which is the only thing a C-GET initiator "+
			"would propose them for", len(seen))
	}

	// The retrieve context itself is proposed without a role, so the counts must differ. Equal counts would suggest a role
	// was invented per context rather than read.
	if len(seen) >= len(contexts) {
		t.Errorf("%d role items for %d contexts; the C-GET context itself carries no role, so there should be fewer",
			len(seen), len(contexts))
	}

	t.Logf("a real getscu proposed %d contexts and %d provider roles, all decoded", len(contexts), storageRoles)
}
