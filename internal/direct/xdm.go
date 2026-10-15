package direct

import (
	"encoding/xml"
	"fmt"
	"path"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xds"
)

// xdmDocuments reads an XDM METADATA.XML (a SubmitObjectsRequest) and pairs each DocumentEntry with its file, named by the
// entry's URI slot relative to the SUBSET directory.
func xdmDocuments(metadata []byte, files map[string][]byte) ([]XDMDocument, error) {
	var req xds.SubmitObjectsRequest
	if err := xml.Unmarshal(metadata, &req); err != nil {
		return nil, fmt.Errorf("direct: METADATA.XML is not XDS metadata: %w", err)
	}
	var out []XDMDocument
	for _, de := range req.Objects.Extrinsic {
		uri := de.Slot1("URI")
		var content []byte
		for name, b := range files {
			if uri != "" && (strings.EqualFold(path.Base(name), path.Base(uri))) {
				content = b
			}
		}
		if content == nil {
			continue
		}
		if h := de.Slot1("hash"); h != "" && !strings.EqualFold(h, sha1Hex(content)) {
			return nil, fmt.Errorf("direct: XDM document %s does not match the hash its metadata gives", uri)
		}
		t := ""
		if c := de.Codes(xds.SchemeTypeCode); len(c) > 0 {
			t = c[0].Code
		}
		out = append(out, XDMDocument{Name: path.Base(uri), MimeType: de.MimeType, Title: de.Name.Text(),
			PatientID: de.ExternalID(xds.IDDocPatientID), UniqueID: de.ExternalID(xds.IDDocUniqueID), TypeCode: t, Content: content})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("direct: the XDM package's metadata names no document it carries")
	}
	return out, nil
}

// XDMFor packages one document with its XDS metadata (built by xds.Build or xds.MetaFromCDA): the DocumentEntry gets the
// URI, hash and size XDM requires.
func XDMFor(req *xds.SubmitObjectsRequest, name string, content []byte) ([]byte, error) {
	if len(req.Objects.Extrinsic) != 1 {
		return nil, fmt.Errorf("direct: an XDM package of one document needs one DocumentEntry, not %d", len(req.Objects.Extrinsic))
	}
	de := &req.Objects.Extrinsic[0]
	de.SetSlot("URI", name)
	de.SetSlot("hash", sha1Hex(content))
	de.SetSlot("size", fmt.Sprint(len(content)))
	md, err := xml.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	return WriteXDM(append([]byte(xml.Header), md...), map[string][]byte{name: content}, "")
}
