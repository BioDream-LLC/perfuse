package xds

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The NIST XDS Toolkit's Initiating Gateway simulator querying Perfuse's Responding Gateway (XCA ITI-38 and ITI-39), when
// PERFUSE_NIST_IG names the simulator (http://localhost:18080/xdstools/sim/default__pfig), PERFUSE_XDS the Perfuse
// server it reaches, and PERFUSE_XDS_TOKEN a token for it. The document is put in Perfuse with ITI-41 first; NIST's
// gateway then finds it over XCA, validates Perfuse's answers, and retrieves it.
func TestNISTInitiatingGatewayQueriesPerfuse(t *testing.T) {
	ig, pf, tok := os.Getenv("PERFUSE_NIST_IG"), os.Getenv("PERFUSE_XDS"), os.Getenv("PERFUSE_XDS_TOKEN")
	if ig == "" || pf == "" {
		t.Skip("set PERFUSE_NIST_IG, PERFUSE_XDS and PERFUSE_XDS_TOKEN")
	}
	m := meta()
	m.PatientID = "PFXCA" + strings.ReplaceAll(newUUID()[:8], "-", "") + "^^^&1.3.6.1.4.1.21367.13.20.1000&ISO"
	req, err := Build(m, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("<ClinicalDocument xmlns=\"urn:hl7-org:v3\"><title>XCA interop</title></ClinicalDocument>")
	perfuse := &Client{Headers: map[string]string{"Authorization": "Bearer " + tok}}
	rr, err := perfuse.ProvideAndRegister(context.Background(), pf+"/xds/repository", req, map[string][]byte{"Document01": content})
	if err != nil || rr.Failed() {
		t.Fatalf("ITI-41 to Perfuse: %v %+v", err, rr)
	}
	nist := &Client{}
	resp, err := nist.Query(context.Background(), ig+"/ig/igq", ActionStoredQuery, FindDocuments(m.PatientID, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NIST IG answered %s, %d documents, errors %+v", resp.Status, len(resp.Objects.Extrinsic), resp.Errors)
	if resp.Status != ResponseSuccess || len(resp.Objects.Extrinsic) != 1 {
		t.Fatalf("XCA query through NIST: %s %+v", resp.Status, resp.Errors)
	}
	de := resp.Objects.Extrinsic[0]
	home := de.Home
	got, err := nist.Retrieve(context.Background(), ig+"/ig/igr", ActionRetrieve, &RetrieveRequest{Requests: []DocumentRequest{
		{HomeCommunityID: home, RepositoryUniqueID: de.Slot1("repositoryUniqueId"), DocumentUniqueID: de.ExternalID(IDDocUniqueID)}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NIST IG retrieve: %s, %d documents", got.Status.Summary(), len(got.Documents))
	if len(got.Documents) != 1 || string(got.Documents[0].Document.Content) != string(content) {
		t.Fatalf("XCA retrieve through NIST: %+v", got.Status)
	}
}
