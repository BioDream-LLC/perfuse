package config

import (
	"sort"
	"strings"
)

// dicomKeywords maps the DICOM keyword to its tag, as "group,element".
//
// Deliberately a short list rather than the whole data dictionary. A channel file naming a tag by number is a lookup every
// reader has to perform, so keywords are the interface; but the full dictionary is several thousand entries, almost none of
// which are query keys, and shipping it would mean accepting names that are legal DICOM and useless in a C-FIND.
//
// So this is the set that appears in real queries. An unknown keyword is refused with the list, which is a better outcome
// than accepting it and having the archive ignore a key nobody realises was misspelled - a query that silently matches more
// than intended.
var dicomKeywords = map[string]string{
	// Patient identification.
	"PatientID":        "0010,0020",
	"PatientName":      "0010,0010",
	"PatientBirthDate": "0010,0030",
	"PatientSex":       "0010,0040",
	"OtherPatientIDs":  "0010,1000",

	// Study identification.
	"StudyInstanceUID":              "0020,000D",
	"StudyID":                       "0020,0010",
	"AccessionNumber":               "0008,0050",
	"StudyDate":                     "0008,0020",
	"StudyTime":                     "0008,0030",
	"StudyDescription":              "0008,1030",
	"ReferringPhysicianName":        "0008,0090",
	"NameOfPhysiciansReadingStudy":  "0008,1060",
	"ModalitiesInStudy":             "0008,0061",
	"NumberOfStudyRelatedSeries":    "0020,1206",
	"NumberOfStudyRelatedInstances": "0020,1208",

	// Series identification.
	"SeriesInstanceUID":               "0020,000E",
	"SeriesNumber":                    "0020,0011",
	"SeriesDate":                      "0008,0021",
	"SeriesTime":                      "0008,0031",
	"SeriesDescription":               "0008,103E",
	"Modality":                        "0008,0060",
	"BodyPartExamined":                "0018,0015",
	"ProtocolName":                    "0018,1030",
	"NumberOfSeriesRelatedInstances":  "0020,1209",
	"PerformedProcedureStepStartDate": "0040,0244",
	"PerformedProcedureStepStartTime": "0040,0245",

	// Instance identification.
	"SOPInstanceUID": "0008,0018",
	"SOPClassUID":    "0008,0016",
	"InstanceNumber": "0020,0013",
	"ContentDate":    "0008,0023",
	"ContentTime":    "0008,0033",

	// Equipment and provenance, which reconciliation queries use.
	"InstitutionName":       "0008,0080",
	"StationName":           "0008,1010",
	"Manufacturer":          "0008,0070",
	"ManufacturerModelName": "0008,1090",
	"RetrieveAETitle":       "0008,0054",
}

// KnownDICOMKeyword reports whether a keyword is one this understands.
func KnownDICOMKeyword(name string) bool {
	_, ok := dicomKeywords[strings.TrimSpace(name)]
	return ok
}

// DICOMTagFor returns the tag for a keyword as "group,element".
func DICOMTagFor(name string) (string, bool) {
	tag, ok := dicomKeywords[strings.TrimSpace(name)]
	return tag, ok
}

// DICOMKeywords lists the understood keywords in order.
//
// Sorted, because it goes into an error message and error messages get diffed and pasted into tickets.
func DICOMKeywords() []string {
	out := make([]string, 0, len(dicomKeywords))
	for name := range dicomKeywords {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// dicomKeywordHint lists the keywords for an error message.
func dicomKeywordHint() string {
	return "the understood keys are " + strings.Join(DICOMKeywords(), ", ")
}
