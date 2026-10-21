package fhirserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recordingUM keeps the 278s it is sent.
type recordingUM struct {
	fakeUM
	raw [][]byte
}

func (u *recordingUM) send(ctx context.Context, b []byte) ([]byte, error) {
	u.raw = append(u.raw, append([]byte(nil), b...))
	return u.fakeUM.send(ctx, b)
}

// The PAS 2.2.1 guide's own request examples (testdata/pas, CC0) become 278s that say what the PAS request says, in the places
// PAS names for it: UM01 from extension-serviceItemRequestType, UM02 from extension-certificationType, UM06 from
// extension-levelOfServiceCode, UM04 from the item's place of service, the admission dates of an admission review, the care
// team's provider as the service provider, and an institutional claim's service as SV2. Before this test only UM02 was carried.
//
// With PERFUSE_TEST_X278_CHECK naming an outside 278 checker (a program taking 278 files and exiting non-zero on an error),
// each 278 is also given to it.
func TestThePASExamplesBecome278sThatSayTheSame(t *testing.T) {
	want := map[string][]string{
		"HomecareAuthorizationBundleExample":        {"~UM*HS*I*42*11:B~", "~SV1*HC:G0154**UN*1~", "~SV1*HC:B4184**UN*1~"},
		"HomecareAuthorizationUpdateBundleExample":  {"~UM*HS*3*42*11:B~"},
		"MedicalServicesAuthorizationBundleExample": {"~UM*HS*I*1", "~DTP*AAH*D8*20050510~", "~SV1*HC:99212**UN*1~"},
		"ReferralAuthorizationBundleExample":        {"~UM*SC*I*3*11:B**U~", "~HI*ABK:G894~", "~NM1*SJ*1*WATSON*SUSAN****XX*1234567893~"},
		"SurgicalRequestBundleExample":              {"~UM*AR*I*2*21:B**E~", "~DTP*435*D8*20200702~", "~DTP*096*D8*20200709~", "~HI*ABK:C189~", "~SV2**HC:33510**UN*1~"},
	}
	dir := t.TempDir()
	for name, parts := range want {
		body, err := os.ReadFile(filepath.Join("testdata", "pas", "Bundle-"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		um := &recordingUM{fakeUM: fakeUM{answers: map[string]string{}}}
		f := newPASFixture(t)
		srv := NewServer(f.store, "http://example.test/fhir", nil)
		srv.Auth = OpenAuth{}
		srv.PAS = &PAS{Decide: pasRules, Now: newPASFixtureClock, UM: &PASUM{Send: um.send, SenderID: "PERFUSE", ReceiverID: "PAYER", PayerID: "12345"}}
		code, b := pasPost(t, srv.Handler(), "/Claim/$submit", string(body))
		if code != 200 || len(um.raw) != 1 {
			t.Fatalf("%s: %d, %d 278s sent: %v", name, code, len(um.raw), b)
		}
		got := string(um.raw[0])
		for _, p := range parts {
			if !strings.Contains(got, p) {
				t.Errorf("%s: no %s in\n%s", name, p, strings.ReplaceAll(got, "~", "~\n"))
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name+".x12"), um.raw[0], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if checker := os.Getenv("PERFUSE_TEST_X278_CHECK"); checker != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.x12"))
		out, err := exec.Command(checker, files...).CombinedOutput()
		t.Logf("%s", out)
		if err != nil {
			t.Fatalf("the outside check failed: %v", err)
		}
	}
}
