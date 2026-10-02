package x12

import (
	"encoding/base64"
	"fmt"
	"mime"
	"strconv"
	"strings"
	"time"
)

// Building a 006020X314 275: a document sent to support a claim.
//
// # Where the structure comes from
//
// Not from the X12 TR3, which is licensed and which this project does not hold. From the two published companion guides for
// 006020X314 that describe it: the CMS esMD guide (AR2024.04.0) and UnitedHealthcare's. Where they agree, this follows them. Where
// they differ, both are satisfied if that is possible and the choice is written down if it is not:
//
//   - BGN01 is 02 for an unsolicited attachment and 11 for a response to a 277 request (UHC; esMD accepts only 02).
//   - NM1*PR is the payer, with PI (both). NM1*41 is the submitter with 46 (esMD requires it; UHC does not list it) - sent when a
//     submitter is given. NM1*1P is the provider with an NPI under XX (UHC; esMD's sample). NM1*QC is the patient under MI (both).
//   - REF*X1 carries the provider's claim identifier (esMD) and DTP*472 the claim's service date (UHC) - each sent when given.
//   - TRN01 is 1 with the claim's PWK06 attachment control number, or 2 with the 277's 2200D TRN02 (UHC).
//   - DTP*368 is the date the information was submitted, CAT01 AE, OOI*1*47*ATTACHMENT (esMD).
//   - CAT02 is HL for an HL7 document (esMD) and IA for an image (UHC): chosen from the content type, which satisfies both.
//   - BDS01 is B64 and BDS03 the base64 of a MIME entity whose body is itself base64 (UHC's "double" form, which it says will be
//     the only accepted form from 8020). BDS02 is the length of BDS03.
//
// What this does not do: validate against the TR3, apply any trading partner's own rules, or sign anything. CMS-0053-F also adopts
// an electronic signature standard for attachments; none is applied here, and a partner that requires one will reject this.
// A companion guide governs, and TestAttachment6020FollowsThePublishedGuides records which rule came from which.

// AttachmentParty names one participant in a 275.
type AttachmentParty struct {
	Name      string // organisation name, or a person's last name
	FirstName string
	ID        string
}

// AttachmentRequest describes one 275 to build.
type AttachmentRequest struct {
	SenderID, ReceiverID string // ISA06 and ISA08, qualified ZZ
	Production           bool   // ISA15: P when true, T otherwise
	ControlNumber        int    // ISA13, GS06 and ST02; derived from the clock when zero

	// Solicited is true when this answers a 277 request for additional information. TraceNumber is then the 277's 2200D TRN02;
	// otherwise it is the attachment control number the claim carried in PWK06.
	Solicited   bool
	TraceNumber string

	Reference string // BGN02, the sender's unique identifier for this transaction

	Payer     AttachmentParty // NM1*PR, ID is the payer identifier
	Submitter AttachmentParty // NM1*41, ID is the submitter's ETIN; optional
	Provider  AttachmentParty // NM1*1P, ID is the NPI
	Patient   AttachmentParty // NM1*QC, ID is the member identifier

	ProviderClaimID string // REF*X1, optional
	ServiceDate     string // DTP*472, CCYYMMDD or CCYYMMDD-CCYYMMDD, optional

	Document    []byte
	ContentType string // e.g. application/pdf, text/xml for a C-CDA
	Filename    string

	Now time.Time // zero means time.Now
}

// BuildAttachment6020 renders a 275 interchange, or explains what is missing.
func BuildAttachment6020(req AttachmentRequest) ([]byte, error) {
	var missing []string
	need := func(v, what string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, what)
		}
	}
	need(req.SenderID, "sender ID (ISA06)")
	need(req.ReceiverID, "receiver ID (ISA08)")
	need(req.Reference, "transaction reference (BGN02)")
	need(req.TraceNumber, "trace number (TRN02): the claim's PWK06, or the 277's TRN02 when solicited")
	need(req.Payer.Name, "payer name")
	need(req.Payer.ID, "payer identifier")
	need(req.Provider.Name, "provider name")
	need(req.Provider.ID, "provider NPI")
	need(req.Patient.Name, "patient last name")
	need(req.Patient.ID, "patient member identifier")
	need(req.ContentType, "document content type")
	if len(req.Document) == 0 {
		missing = append(missing, "the document")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("a 275 needs %s", strings.Join(missing, ", "))
	}
	if _, _, err := mime.ParseMediaType(req.ContentType); err != nil {
		return nil, fmt.Errorf("content type %q is not a MIME type: %v", req.ContentType, err)
	}
	if req.Provider.ID != "" && !validNPI(req.Provider.ID) {
		// Checked because a payer matches the attachment to the claim partly by billing NPI, and a mistyped one is an attachment
		// that sits unmatched while the claim is denied for missing documentation.
		return nil, fmt.Errorf("provider NPI %q fails the NPI check digit", req.Provider.ID)
	}

	fields := []string{req.SenderID, req.ReceiverID, req.Reference, req.TraceNumber, req.Payer.Name, req.Payer.ID,
		req.Submitter.Name, req.Submitter.FirstName, req.Submitter.ID, req.Provider.Name, req.Provider.FirstName, req.Provider.ID,
		req.Patient.Name, req.Patient.FirstName, req.Patient.ID, req.ProviderClaimID, req.ServiceDate, req.Filename}
	for _, f := range fields {
		if strings.ContainsAny(f, "*~:^\r\n") {
			return nil, fmt.Errorf("%q contains an X12 delimiter, which would split the segment it is written into", f)
		}
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	ctrl := req.ControlNumber
	if ctrl <= 0 {
		ctrl = int(now.Unix() % 1000000000)
	}
	usage := "T"
	if req.Production {
		usage = "P"
	}
	pad := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s + strings.Repeat(" ", n-len(s))
	}
	ctrl9 := fmt.Sprintf("%09d", ctrl)
	date8, date6, hhmm := now.Format("20060102"), now.Format("060102"), now.Format("1504")

	var segs []string
	add := func(elems ...string) {
		// Trailing empty elements are dropped, as X12 requires.
		for len(elems) > 1 && elems[len(elems)-1] == "" {
			elems = elems[:len(elems)-1]
		}
		segs = append(segs, strings.Join(elems, "*"))
	}

	isa := strings.Join([]string{"ISA", "00", pad("", 10), "00", pad("", 10), "ZZ", pad(req.SenderID, 15), "ZZ",
		pad(req.ReceiverID, 15), date6, hhmm, "^", "00602", ctrl9, "0", usage, ":"}, "*")
	gs := strings.Join([]string{"GS", "PI", req.SenderID, req.ReceiverID, date8, hhmm, strconv.Itoa(ctrl), "X", "006020X314"}, "*")

	st := strconv.Itoa(ctrl % 10000)
	add("ST", "275", st, "006020X314")
	purpose, traceType := "02", "1"
	if req.Solicited {
		purpose, traceType = "11", "2"
	}
	add("BGN", purpose, req.Reference, date8)
	add("NM1", "PR", "2", req.Payer.Name, "", "", "", "", "PI", req.Payer.ID)
	if req.Submitter.ID != "" {
		kind := "2"
		if req.Submitter.FirstName != "" {
			kind = "1"
		}
		add("NM1", "41", kind, req.Submitter.Name, req.Submitter.FirstName, "", "", "", "46", req.Submitter.ID)
	}
	providerKind := "2"
	if req.Provider.FirstName != "" {
		providerKind = "1"
	}
	add("NM1", "1P", providerKind, req.Provider.Name, req.Provider.FirstName, "", "", "", "XX", req.Provider.ID)
	add("NM1", "QC", "1", req.Patient.Name, req.Patient.FirstName, "", "", "", "MI", req.Patient.ID)
	if req.ProviderClaimID != "" {
		add("REF", "X1", req.ProviderClaimID)
	}
	if req.ServiceDate != "" {
		format := "D8"
		if strings.Contains(req.ServiceDate, "-") {
			format = "RD8"
		}
		add("DTP", "472", format, req.ServiceDate)
	}
	add("LX", "1")
	add("TRN", traceType, req.TraceNumber)
	add("DTP", "368", "D8", date8)
	add("CAT", "AE", transmissionFor(req.ContentType))
	add("OOI", "1", "47", "ATTACHMENT")

	payload := base64.StdEncoding.EncodeToString(mimeEntity(req.ContentType, req.Filename, req.Document))
	// Appended directly: the data element is counted, not scanned, so it must not pass through add's trimming.
	segs = append(segs, "BDS*B64*"+strconv.Itoa(len(payload))+"*"+payload)

	add("SE", strconv.Itoa(len(segs)+1), st)

	var b strings.Builder
	b.WriteString(isa + "~" + gs + "~")
	for _, s := range segs {
		b.WriteString(s + "~")
	}
	b.WriteString("GE*1*" + strconv.Itoa(ctrl) + "~IEA*1*" + ctrl9 + "~")
	return []byte(b.String()), nil
}

// transmissionFor picks CAT02: HL for an HL7 document, IA for anything rendered as an image or file.
func transmissionFor(contentType string) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch mt {
	case "text/xml", "application/xml", "application/hl7-v3+xml", "application/cda+xml":
		return "HL"
	}
	return "IA"
}

// mimeEntity wraps the document as the single-part MIME entity BDS03 carries.
func mimeEntity(contentType, filename string, doc []byte) []byte {
	var b strings.Builder
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: " + contentType + "\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	if filename != "" {
		b.WriteString("Content-Disposition: " + mime.FormatMediaType("attachment", map[string]string{"filename": filename}) + "\r\n")
	}
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString(doc)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

// validNPI applies the NPI check digit: Luhn over the prefix 80840 and the first nine digits.
func validNPI(npi string) bool {
	if len(npi) != 10 {
		return false
	}
	digits := "80840" + npi[:9]
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if (len(digits)-1-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	check := (10 - sum%10) % 10
	return npi[9] >= '0' && npi[9] <= '9' && int(npi[9]-'0') == check
}
