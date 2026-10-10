package vpn

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestStrongSwanForReal reads a live IKEv2 tunnel between two strongSwan gateways in Docker, through the real swanctl. Skipped
// unless PERFUSE_STRONGSWAN names the swanctl wrapper scripts/strongswan-up.sh prints:
//
//	PERFUSE_STRONGSWAN=$(scripts/strongswan-up.sh) go test ./internal/vpn -run StrongSwanForReal; scripts/strongswan-down.sh
func TestStrongSwanForReal(t *testing.T) {
	swanctl := os.Getenv("PERFUSE_STRONGSWAN")
	if swanctl == "" {
		t.Skip("set PERFUSE_STRONGSWAN to the swanctl wrapper from scripts/strongswan-up.sh")
	}
	ctx := context.Background()
	theirs := Settings{Phase1Encryption: []string{"aes256"}, Phase1Integrity: []string{"sha256"}, Phase1DHGroups: []string{"14"},
		Phase2Encryption: []string{"AES256-GCM-16"}, Networks: []string{"10.2.0.0/16"}}
	tun := &Tunnel{Name: "partner", StrongSwan: &StrongSwanSource{Connection: "gw-gw", Command: swanctl}, Theirs: theirs}
	m := &Monitor{File: &File{Tunnels: []*Tunnel{tun}}}

	st := m.Status(ctx, tun)
	if st.State != Up {
		t.Fatalf("the tunnel is up, and Perfuse read %s: %s", st.State, st.Detail)
	}
	if len(st.Endpoints) != 1 || st.Endpoints[0].Address != "172.30.0.3" {
		t.Errorf("endpoints %+v, want the other gateway 172.30.0.3", st.Endpoints)
	}
	if strings.Join(st.Reported.Networks, ",") != "10.1.0.0/16" || len(st.Mismatches) != 0 {
		t.Errorf("reported %+v, mismatches %v; both sides agree", st.Reported, st.Mismatches)
	}

	// A partner whose sheet says DH group 19 cannot agree with what the tunnel is actually using.
	wrong := *tun
	wrong.Theirs.Phase1DHGroups = []string{"19"}
	if st := (&Monitor{File: &File{Tunnels: []*Tunnel{&wrong}}}).Status(ctx, &wrong); len(st.Mismatches) == 0 {
		t.Errorf("DH group 19 against MODP_2048 found no mismatch: %+v", st)
	}

	// The other side goes away: once our gateway gives up on it, the tunnel reads down.
	if out, err := exec.Command("docker", "stop", "-t", "1", "perfuse-swan-theirs").CombinedOutput(); err != nil {
		t.Fatalf("stopping the other gateway: %v %s", err, out)
	}
	if out, err := exec.Command("docker", "exec", "perfuse-swan-ours", "swanctl", "--terminate", "--ike", "gw-gw", "--force").CombinedOutput(); err != nil {
		t.Fatalf("terminating the SA: %v %s", err, out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st = (&Monitor{File: m.File}).Status(ctx, tun)
		if st.State == Down || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if st.State != Down {
		t.Errorf("with the other gateway gone, read %s: %s", st.State, st.Detail)
	}
}
