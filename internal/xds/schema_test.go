package xds

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every message Perfuse writes, checked against the official ebXML Registry 3.0 and XDS.b schemas with xmllint, when
// PERFUSE_XDS_SCHEMA names the directory holding them (the NIST XDS Toolkit's toolkitx/schema/v3).
func TestMessagesAreValidAgainstTheSchemas(t *testing.T) {
	dir := os.Getenv("PERFUSE_XDS_SCHEMA")
	if dir == "" {
		t.Skip("set PERFUSE_XDS_SCHEMA to the ebXML 3.0 / XDS.b schema directory")
	}
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("no xmllint")
	}
	reg, srv := rig(t)
	m := meta()
	m.ServiceStart = time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	m.AuthorInstitution = "Example Hospital^^^^^^^^^1.2.3.4"
	submit(t, srv, m, "<ClinicalDocument>schema</ClinicalDocument>")
	sub, _ := Build(meta(), time.Now())
	pnr, _ := xml.Marshal(ProvideAndRegisterRequest{Submit: *sub, Documents: []Document{{ID: "Document01", Inner: "c2NoZW1h"}}})
	query, _ := xml.Marshal(FindDocuments(patient, ""))
	res, _ := reg.Query(context.Background(), FindDocuments(patient, ""))
	qresp, _ := xml.Marshal(QueryResponse(res, nil, "LeafClass", ""))
	xresp, _ := xml.Marshal(QueryResponse(res, nil, "LeafClass", reg.HomeCommunityID))
	refs, _ := xml.Marshal(QueryResponse(res, nil, "ObjectRef", ""))
	fail, _ := xml.Marshal(QueryResponse(nil, []Problem{problem(ErrQueryMissingParam, "$x", "missing")}, "", ""))
	rresp, _ := xml.Marshal(Response([]Problem{problem(ErrMetadata, "Document01", "something"), {Code: ErrMetadata, Context: "w", Warning: true}}))
	uid := res.Documents[0].ExternalID(IDDocUniqueID)
	ret, _ := reg.Retrieve(context.Background(), &RetrieveRequest{Requests: []DocumentRequest{{RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: uid}}}, "")
	// On the wire the document is an xop:Include, which XOP processing replaces with its base64 before the schema applies
	// (an Include inside a base64Binary element is not schema-valid on its own, by design); so it is checked as resolved.
	ret.Documents[0].Document.Inner = base64.StdEncoding.EncodeToString(ret.Documents[0].Document.Content)
	retresp, _ := xml.Marshal(ret)
	retreq, _ := xml.Marshal(RetrieveRequest{Requests: []DocumentRequest{{RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: uid}}})
	for _, c := range []struct {
		name, xsd string
		doc       []byte
	}{
		{"ProvideAndRegisterDocumentSetRequest", "XDS.b_DocumentRepository.xsd", pnr},
		{"AdhocQueryRequest", "query.xsd", query},
		{"AdhocQueryResponse (LeafClass)", "query.xsd", qresp},
		{"AdhocQueryResponse (XCA, home)", "query.xsd", xresp},
		{"AdhocQueryResponse (ObjectRef)", "query.xsd", refs},
		{"AdhocQueryResponse (failure)", "query.xsd", fail},
		{"RegistryResponse", "rs.xsd", rresp},
		{"RetrieveDocumentSetRequest", "XDS.b_DocumentRepository.xsd", retreq},
		{"RetrieveDocumentSetResponse", "XDS.b_DocumentRepository.xsd", retresp},
	} {
		f := filepath.Join(t.TempDir(), "doc.xml")
		if err := os.WriteFile(f, c.doc, 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		cmd := exec.Command("xmllint", "--noout", "--schema", filepath.Join(dir, c.xsd), f)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Errorf("%s does not validate:\n%s", c.name, strings.ReplaceAll(out.String(), f, "doc.xml"))
		}
	}
}
