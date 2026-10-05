package v2fhir

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Race, ethnicity and language, as US Core carries them for USCDI.
//
// Race and ethnicity go in US Core's extensions only when the code is CDC Race and Ethnicity (CDCREC, OID 2.16.840.1.113883.6.238),
// which is what US Core binds them to; a local code would assert a category nobody can verify. The five OMB race categories and two
// ethnicity categories are ombCategory, any other CDCREC code is detailed, and the text the sender gave is the required text.

const systemCDCREC = "urn:oid:2.16.840.1.113883.6.238"

var ombRace = map[string]string{
	"1002-5": "American Indian or Alaska Native", "2028-9": "Asian", "2054-5": "Black or African American",
	"2076-8": "Native Hawaiian or Other Pacific Islander", "2106-3": "White",
}

var ombEthnicity = map[string]string{"2135-2": "Hispanic or Latino", "2186-5": "Not Hispanic or Latino"}

func (c *converter) omb(field, url string, categories map[string]string) *fhir.Extension {
	var subs []fhir.Extension
	var texts []string
	for i := 1; i <= c.repeatCount(field); i++ {
		p := fmt.Sprintf("%s(%d)", field, i)
		code, text := c.get(p+".1"), c.get(p+".2")
		system := strings.ToUpper(c.get(p + ".3"))
		if code == "" {
			continue
		}
		if system != "CDCREC" && system != "HL70005" && system != "HL70189" && system != "2.16.840.1.113883.6.238" {
			c.note("warning", p, "Patient.extension", "%s code %q is not a CDC Race and Ethnicity code, so it is not carried in %s", field, code,
				url[strings.LastIndex(url, "/")+1:])
			continue
		}
		// HL7 table 0189's own letters are not CDC codes: "N^Not Hispanic or Latino^HL70189", from an NHS Wales sample, became
		// CDCREC code N, which does not exist. They are mapped to the CDC codes they mean; U says nothing, so it is not carried.
		if system == "HL70189" {
			switch strings.ToUpper(code) {
			case "H":
				code = "2135-2"
			case "N":
				code = "2186-5"
			case "U":
				c.note("info", p, "Patient.extension", "ethnicity is unknown (HL7 0189 U), so no ethnicity code is carried")
				continue
			}
		}
		display, isOMB := categories[code]
		if text == "" {
			text = display
		}
		name := "detailed"
		if isOMB {
			name = "ombCategory"
		}
		subs = append(subs, fhir.Extension{URL: name, ValueCoding: &fhir.Coding{System: systemCDCREC, Code: code, Display: display}})
		if text != "" {
			texts = append(texts, text)
		}
	}
	if len(subs) == 0 {
		return nil
	}
	text := strings.Join(texts, ", ")
	subs = append(subs, fhir.Extension{URL: "text", ValueString: &text})
	return &fhir.Extension{URL: url, Extension: subs}
}

// iso6392to1 maps the three-letter ISO 639-2 codes v2 senders use to the two-letter codes BCP 47 requires where one exists.
var iso6392to1 = map[string]string{
	"eng": "en", "spa": "es", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de", "ita": "it", "por": "pt", "rus": "ru",
	"chi": "zh", "zho": "zh", "jpn": "ja", "kor": "ko", "vie": "vi", "ara": "ar", "hin": "hi", "pol": "pl", "tgl": "tl",
	"per": "fa", "fas": "fa", "urd": "ur", "ben": "bn", "pan": "pa", "guj": "gu", "hat": "ht", "gre": "el", "ell": "el",
	"heb": "he", "som": "so", "amh": "am", "ukr": "uk", "tha": "th", "khm": "km", "lao": "lo", "nep": "ne", "swa": "sw",
}

// language reads PID-15 into a BCP 47 language, which US Core binds communication.language to.
func (c *converter) language(field string) *fhir.CodeableConcept {
	code, text := strings.TrimSpace(c.get(field+".1")), c.get(field+".2")
	if code == "" {
		return nil
	}
	lc := strings.ToLower(code)
	if two, ok := iso6392to1[lc]; ok {
		lc = two
	}
	if len(lc) < 2 || len(lc) > 3 || strings.Trim(lc, "abcdefghijklmnopqrstuvwxyz-") != "" {
		c.note("warning", field, "Patient.communication", "language %q is not an ISO 639 code, so it was kept as text only", code)
		return &fhir.CodeableConcept{Text: firstNonEmpty(text, code)}
	}
	return &fhir.CodeableConcept{Coding: []fhir.Coding{{System: "urn:ietf:bcp:47", Code: lc}}, Text: text}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
