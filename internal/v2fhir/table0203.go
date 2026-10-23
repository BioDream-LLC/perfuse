package v2fhir

import (
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
)

// table0203 is HL7 table 0203, identifier type, as published in terminology.hl7.org (CodeSystem v2-0203). An identifier
// type outside it is a site code and must not be labelled with the v2-0203 system.
var table0203 = map[string]bool{}

func init() {
	for _, code := range strings.Fields(`AC ACSN AIN AM AMA AN ANC AND ANON ANT APRN ASID BA BC BCFN BCT BR BRN BSNR CAII CC CONM
		CY CZ DC DCFN DDS DEA DFN DI DL DN DO DP DPM DR DS DSG EI EN ESN FDR FDRFN FGN FI FILL GI GIN GL GN HC IND IRISTEM JHN
		LACSN LANR LI L&I LN LR MA MB MC MCD MCN MCR MCT MD MI MR MRT MS NBSNR NCT NE NH NI NII NIIP NP NPI OBI OD PA PC PCN PE
		PEN PGN PHC PHE PHO PI PIN PLAC PN PNT PPIN PPN PRC PRN PT QA RI RN RPH RR RRI RRP SAMN SB SID SL SN SNBSN SNO SP SR SRX
		SS STN TAX TN TPR TREG UDI UPIN USID VN VP VS WC WCN WP XV XX`) {
		table0203[code] = true
	}
}

// isTable0203 reports whether code is in HL7 table 0203. NNxxx, a national identifier for the country with ISO 3166
// alpha-3 code xxx, is a pattern rather than a list.
func isTable0203(code string) bool {
	code = strings.ToUpper(code)
	if table0203[code] {
		return true
	}
	return len(code) == 5 && strings.HasPrefix(code, "NN") && strings.Trim(code[2:], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == ""
}

// nationalIdentifierTypes are identifier types a country's HL7 v2 profile adds to table 0203, with the code system its
// FHIR guide names them by. France's (IHE PAM-FR, Interop'Santé): IDNPS for a professional's national number (RPPS or
// ADELI with its prefix), the INS types for the patient's national health identity. FR Core slices on these.
var nationalIdentifierTypes = map[string]string{}

func init() {
	const fr = "https://hl7.fr/ig/fhir/core/CodeSystem/fr-core-cs-v2-0203"
	for _, code := range strings.Fields(`ADELI RPPS RPPSRG IDNPS NDP INS-C INS-NIA INS-NIR INS-NIR-TEST INS-NIR-DEMO FINEJ FINEG
		SIREN SIRET IDNST INTRN EXTRN`) {
		nationalIdentifierTypes[code] = fr
	}
}

// identifierType is an identifier's type from a v2 type code (CX.5, XCN.13): table 0203, a country's own extension of it,
// or, for a code neither knows, the code kept as text with a note.
func (c *converter) identifierType(code, source string) *fhir.CodeableConcept {
	switch {
	case code == "":
		return nil
	case isTable0203(code):
		return fhir.NewCodeableConcept(fhir.SystemIdentifierType, code, identifierTypeDisplay(code))
	case nationalIdentifierTypes[strings.ToUpper(code)] != "":
		return fhir.NewCodeableConcept(nationalIdentifierTypes[strings.ToUpper(code)], strings.ToUpper(code), "")
	}
	// A site's own type code ("PATNUMBER") was labelled as HL7 table 0203, which the validator rejects as an unknown code.
	// It is kept as text.
	c.note("warning", source, "Identifier.type", "identifier type %q is not in HL7 table 0203, so it was kept as text", code)
	return fhir.TextOnly(code)
}
