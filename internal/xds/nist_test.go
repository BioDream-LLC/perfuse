package xds

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Against the NIST XDS Toolkit's Registry/Repository simulator, when PERFUSE_NIST_RR names its base URL
// (http://localhost:18080/xdstools/sim/default__pfrr). Validation of codes and the patient identity feed are off in the
// simulator; its metadata validation is on.
func TestAgainstTheNISTRegistryRepository(t *testing.T) {
	base := os.Getenv("PERFUSE_NIST_RR")
	if base == "" {
		t.Skip("set PERFUSE_NIST_RR to a NIST XDS Toolkit rr simulator")
	}
	c := &Client{}
	m := meta()
	m.PatientID = "PFNIST" + strings.ReplaceAll(newUUID()[:8], "-", "") + "^^^&1.3.6.1.4.1.21367.13.20.1000&ISO"
	req, err := Build(m, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("<ClinicalDocument xmlns=\"urn:hl7-org:v3\"><title>NIST interop</title></ClinicalDocument>")
	rr, err := c.ProvideAndRegister(context.Background(), base+"/rep/prb", req, map[string][]byte{"Document01": content})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ITI-41: %s", rr.Summary())
	if rr.Failed() {
		t.Fatalf("the NIST repository refused the submission: %s", rr.Summary())
	}
	resp, err := c.Query(context.Background(), base+"/reg/sq", ActionStoredQuery, FindDocuments(m.PatientID, ""))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != ResponseSuccess || len(resp.Objects.Extrinsic) != 1 {
		t.Fatalf("ITI-18: %s %+v", resp.Status, resp.Errors)
	}
	de := resp.Objects.Extrinsic[0]
	t.Logf("ITI-18: %s, repository %s, hash %s", de.ExternalID(IDDocUniqueID), de.Slot1("repositoryUniqueId"), de.Slot1("hash"))
	got, err := c.Retrieve(context.Background(), base+"/rep/ret", ActionRetrieve, &RetrieveRequest{Requests: []DocumentRequest{
		{RepositoryUniqueID: de.Slot1("repositoryUniqueId"), DocumentUniqueID: de.ExternalID(IDDocUniqueID)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Documents) != 1 || string(got.Documents[0].Document.Content) != string(content) {
		t.Fatalf("ITI-43: %+v", got)
	}
	t.Logf("ITI-43: %d bytes, identical", len(got.Documents[0].Document.Content))
}
