package vpn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const awsAnswer = `<?xml version="1.0" encoding="UTF-8"?>
<DescribeVpnConnectionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
 <vpnConnectionSet><item>
  <vpnConnectionId>vpn-0abc</vpnConnectionId><state>available</state>
  <customerGatewayConfiguration>&lt;pre_shared_key&gt;SECRETKEY&lt;/pre_shared_key&gt;</customerGatewayConfiguration>
  <options><localIpv4NetworkCidr>10.20.0.0/16</localIpv4NetworkCidr><remoteIpv4NetworkCidr>192.168.10.0/24</remoteIpv4NetworkCidr>
   <tunnelOptionSet><item>
    <phase1EncryptionAlgorithmSet><item><value>AES256</value></item></phase1EncryptionAlgorithmSet>
    <phase1IntegrityAlgorithmSet><item><value>SHA2-256</value></item></phase1IntegrityAlgorithmSet>
    <phase1DHGroupNumberSet><item><value>14</value></item></phase1DHGroupNumberSet>
    <ikeVersionSet><item><value>ikev2</value></item></ikeVersionSet>
    <phase1LifetimeSeconds>28800</phase1LifetimeSeconds><phase2LifetimeSeconds>3600</phase2LifetimeSeconds>
   </item></tunnelOptionSet></options>
  <vgwTelemetry>
   <item><outsideIpAddress>3.1.1.1</outsideIpAddress><status>UP</status><lastStatusChange>2026-10-08T10:00:00Z</lastStatusChange><statusMessage>1 BGP ROUTES</statusMessage></item>
   <item><outsideIpAddress>3.1.1.2</outsideIpAddress><status>DOWN</status><lastStatusChange>2026-10-08T11:00:00Z</lastStatusChange><statusMessage></statusMessage></item>
  </vgwTelemetry>
 </item></vpnConnectionSet>
</DescribeVpnConnectionsResponse>`

func TestAnAWSConnectionWithOneTunnelDownIsPartial(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Query().Get("Action") != "DescribeVpnConnections" || r.URL.Query().Get("VpnConnectionId.1") != "vpn-0abc" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(awsAnswer))
	}))
	defer srv.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	tun := &Tunnel{Name: "lab", Partner: "Acme Lab", AWS: &AWSSource{Region: "us-east-1", ConnectionID: "vpn-0abc", Endpoint: srv.URL},
		Theirs: Settings{Phase1Encryption: []string{"aes-256"}, Phase1Integrity: []string{"sha256"}, Phase1DHGroups: []string{"modp2048"},
			Phase1Lifetime: 86400, Networks: []string{"192.168.10.0/24"}}}
	m := &Monitor{File: &File{Tunnels: []*Tunnel{tun}}}
	st := m.Statuses(context.Background())[0]
	if !strings.Contains(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") || !strings.Contains(auth, "/us-east-1/ec2/aws4_request") {
		t.Errorf("not signed for ec2: %q", auth)
	}
	if st.State != Partial || len(st.Endpoints) != 2 || st.Endpoints[0].Address != "3.1.1.1" || !st.Endpoints[0].Up {
		t.Fatalf("status %+v", st)
	}
	if len(st.Mismatches) != 1 || !strings.Contains(st.Mismatches[0], "phase 1 lifetime: ours 28800s, theirs 86400s") {
		t.Errorf("only the lifetime disagrees (AES256 is aes-256, SHA2-256 is sha256, 14 is modp2048): %v", st.Mismatches)
	}
	sheet := Sheet(tun, st)
	for _, want := range []string{"Acme Lab", "| Phase 1 encryption | AES256 | aes-256 |", "| Networks | 10.20.0.0/16 | 192.168.10.0/24 |",
		"3.1.1.2: down", "phase 1 lifetime"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet lacks %q:\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, "SECRETKEY") {
		t.Error("the pre-shared key reached the sheet")
	}
}

func TestAWSWithoutCredentialsSaysWhatIsNeeded(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	m := &Monitor{File: &File{Tunnels: []*Tunnel{{Name: "x", AWS: &AWSSource{Region: "us-east-1", ConnectionID: "vpn-1"}}}}}
	st := m.Statuses(context.Background())[0]
	if st.State != Unknown || !strings.Contains(st.Detail, "ec2:DescribeVpnConnections") {
		t.Errorf("%+v", st)
	}
}

func TestAnAzureConnectionIsReadWithEntraCredentials(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "app-secret" || r.PostForm.Get("scope") != srv.URL+"/.default" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error_description":"AADSTS7000215: Invalid client secret provided.\nTrace ID: x"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3599}`))
	})
	mux.HandleFunc("GET /subscriptions/sub1/resourceGroups/rg/providers/Microsoft.Network/connections/to-clinic", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"properties":{"connectionStatus":"NotConnected","connectionProtocol":"IKEv2",
			"ipsecPolicies":[{"saLifeTimeSeconds":27000,"ipsecEncryption":"GCMAES256","ipsecIntegrity":"GCMAES256","ikeEncryption":"AES256",
			"ikeIntegrity":"SHA256","dhGroup":"DHGroup14","pfsGroup":"None"}]}}`))
	})
	t.Setenv("AZ_SECRET", "app-secret")
	tun := &Tunnel{Name: "clinic", Azure: &AzureSource{Subscription: "sub1", ResourceGroup: "rg", Connection: "to-clinic", TenantID: "t1",
		ClientID: "c1", ClientSecretEnv: "AZ_SECRET", ManagementURL: srv.URL, TokenURL: srv.URL + "/token"},
		Theirs: Settings{IKEVersions: []string{"IKEv1"}, Phase1DHGroups: []string{"group 14"}}}
	st := (&Monitor{File: &File{Tunnels: []*Tunnel{tun}}}).Statuses(context.Background())[0]
	if st.State != Down || st.Endpoints[0].Message != "NotConnected" {
		t.Errorf("status %+v", st)
	}
	if len(st.Mismatches) != 1 || !strings.Contains(st.Mismatches[0], "IKE version") {
		t.Errorf("IKEv2 against IKEv1 is the only disagreement: %v", st.Mismatches)
	}

	t.Setenv("AZ_SECRET", "wrong")
	tun.Azure.ClientID = "c2" // another app, so the cached token is not used
	st = (&Monitor{File: &File{Tunnels: []*Tunnel{tun}}}).Statuses(context.Background())[0]
	if st.State != Unknown || !strings.Contains(st.Detail, "AADSTS7000215") || strings.Contains(st.Detail, "Trace ID") {
		t.Errorf("a refused secret: %+v", st)
	}
}

const swanctlOut = `gw-gw: #1, ESTABLISHED, IKEv2, 4a3c4d1e3f5a2b1c_i* 9f8e7d6c5b4a3210_r
  local  'moon.example.org' @ 192.0.2.1[4500]
  remote 'sun.example.org' @ 198.51.100.2[4500]
  AES_CBC-256/HMAC_SHA2_256_128/PRF_HMAC_SHA2_256/MODP_2048
  established 25s ago, rekeying in 13844s
  net-net: #1, reqid 1, INSTALLED, TUNNEL, ESP:AES_GCM_16-256
    installed 25s ago, rekeying in 3304s, expires in 3935s
    in  c9a37c19,      0 bytes,     0 packets
    out cb3e9a09,      0 bytes,     0 packets
    local  10.1.0.0/16
    remote 10.2.0.0/16
`

func TestAStrongSwanTunnelIsReadFromSwanctl(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var args []string
	m := &Monitor{Now: func() time.Time { return now }, Run: func(_ context.Context, name string, a ...string) ([]byte, error) {
		args = append([]string{name}, a...)
		return []byte(swanctlOut), nil
	}}
	tun := &Tunnel{Name: "partner", StrongSwan: &StrongSwanSource{Connection: "gw-gw"},
		Theirs: Settings{Phase1Encryption: []string{"aes256"}, Phase1Integrity: []string{"sha256"}, Phase1DHGroups: []string{"14"},
			Phase2Encryption: []string{"AES256-GCM-16"}, Networks: []string{"10.2.0.0/16"}}}
	m.File = &File{Tunnels: []*Tunnel{tun}}
	st := m.Status(context.Background(), tun)
	if strings.Join(args, " ") != "swanctl --list-sas --ike gw-gw" {
		t.Errorf("ran %v", args)
	}
	if st.State != Up || st.Endpoints[0].Address != "198.51.100.2" || !st.Endpoints[0].Since.Equal(now.Add(-25*time.Second)) {
		t.Fatalf("status %+v", st)
	}
	if strings.Join(st.Reported.Networks, ",") != "10.1.0.0/16" || st.Reported.Phase1DHGroups[0] != "MODP_2048" || len(st.Mismatches) != 0 {
		t.Errorf("reported %+v mismatches %v", st.Reported, st.Mismatches)
	}

	// IKE up with no child SA passes no traffic; a failing swanctl says why.
	m = &Monitor{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Split(swanctlOut, "  net-net")[0]), nil
	}}
	if st := m.Status(context.Background(), tun); st.State != Partial {
		t.Errorf("no child SA: %+v", st)
	}
	m = &Monitor{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("connecting to 'unix:///var/run/charon.vici' failed: Permission denied"), errors.New("exit status 1")
	}}
	if st := m.Status(context.Background(), tun); st.State != Unknown || !strings.Contains(st.Detail, "Permission denied") {
		t.Errorf("swanctl failing: %+v", st)
	}
	m = &Monitor{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	if st := m.Status(context.Background(), tun); st.State != Down {
		t.Errorf("no SA: %+v", st)
	}
}

func TestReadingsAreCachedForAMinute(t *testing.T) {
	now := time.Now()
	runs := 0
	tun := &Tunnel{Name: "p", StrongSwan: &StrongSwanSource{Connection: "gw-gw"}}
	m := &Monitor{Now: func() time.Time { return now }, File: &File{Tunnels: []*Tunnel{tun}},
		Run: func(context.Context, string, ...string) ([]byte, error) { runs++; return []byte(swanctlOut), nil }}
	m.Statuses(context.Background())
	m.Statuses(context.Background())
	now = now.Add(61 * time.Second)
	m.Statuses(context.Background())
	if runs != 2 {
		t.Errorf("swanctl ran %d times", runs)
	}
}

func TestTheFileIsChecked(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vpn.yaml")
	for body, want := range map[string]string{
		"tunnels:\n  - partner: x\n": "no name",
		"tunnels:\n  - name: a\n    aws: {region: us-east-1, connection_id: x}\n":                              "connection_id",
		"tunnels:\n  - name: a\n    strongswan: {}\n":                                                          "connection",
		"tunnels:\n  - name: a\n    aws: {region: r, connection_id: vpn-1}\n    strongswan: {connection: c}\n": "one source",
		"tunnels:\n  - name: a\n  - name: a\n":                                                                 "twice",
	} {
		_ = os.WriteFile(p, []byte(body), 0o600)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
	_ = os.WriteFile(p, []byte("tunnels:\n  - name: a\n    partner: Acme\n    theirs: {peer_address: 203.0.113.9, ike_versions: [ikev2]}\n"), 0o600)
	if f, err := Load(p); err != nil || f.Tunnels[0].Theirs.PeerAddress != "203.0.113.9" {
		t.Errorf("%v %v", f, err)
	}
}
