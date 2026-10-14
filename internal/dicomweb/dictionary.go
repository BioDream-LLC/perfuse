package dicomweb

// vrOf is the value representation of a tag in an implicit VR data set, where the file does not say.
//
// The attributes a router, a QIDO search and a viewer read: patient, study, series, instance, equipment, frame of
// reference, image plane, image pixel, VOI LUT and SOP common modules, plus the sequences that commonly hold them.
// Anything else is UN, which DICOM JSON carries as binary: honest, where a guessed VR would turn a number into nonsense.
func vrOf(tag uint32) string {
	if tag&0xFFFF == 0 {
		return "UL" // group length
	}
	if (tag>>16)&1 == 1 {
		return "UN" // private
	}
	if vr, ok := dictionary[tag]; ok {
		return vr
	}
	switch tag >> 16 {
	case 0xFFFE:
		return "UN"
	}
	return "UN"
}

var dictionary = map[uint32]string{
	// SOP common, general study, patient
	0x00080005: "CS", 0x00080008: "CS", 0x00080012: "DA", 0x00080013: "TM", 0x00080016: "UI", 0x00080018: "UI",
	0x00080020: "DA", 0x00080021: "DA", 0x00080022: "DA", 0x00080023: "DA", 0x00080030: "TM", 0x00080031: "TM",
	0x00080032: "TM", 0x00080033: "TM", 0x00080050: "SH", 0x00080051: "SQ", 0x00080056: "CS", 0x00080060: "CS",
	0x00080061: "CS", 0x00080062: "UI", 0x00080064: "CS", 0x00080068: "CS", 0x00080070: "LO", 0x00080080: "LO",
	0x00080081: "ST", 0x00080090: "PN", 0x00080100: "SH", 0x00080102: "SH", 0x00080104: "LO", 0x00080201: "SH",
	0x00081010: "SH", 0x00081030: "LO", 0x00081032: "SQ", 0x0008103E: "LO", 0x00081040: "LO", 0x00081050: "PN",
	0x00081060: "PN", 0x00081070: "PN", 0x00081090: "LO", 0x00081110: "SQ", 0x00081111: "SQ", 0x00081115: "SQ",
	0x00081120: "SQ", 0x00081140: "SQ", 0x00081150: "UI", 0x00081155: "UI", 0x00081190: "UR", 0x00082111: "ST",
	0x00082112: "SQ", 0x00089215: "SQ",
	0x00100010: "PN", 0x00100020: "LO", 0x00100021: "LO", 0x00100030: "DA", 0x00100032: "TM", 0x00100040: "CS",
	0x00101000: "LO", 0x00101001: "PN", 0x00101010: "AS", 0x00101020: "DS", 0x00101030: "DS", 0x00101040: "LO",
	0x00102160: "SH", 0x00104000: "LT",
	// equipment, acquisition
	0x00180015: "CS", 0x00180020: "CS", 0x00180021: "CS", 0x00180022: "CS", 0x00180023: "CS", 0x00180050: "DS",
	0x00180060: "DS", 0x00180088: "DS", 0x00181000: "LO", 0x00181020: "LO", 0x00181030: "LO", 0x00181150: "IS",
	0x00181151: "IS", 0x00181152: "IS", 0x00181164: "DS", 0x00181210: "SH", 0x00185100: "CS", 0x00180086: "IS",
	0x00180087: "DS", 0x00180080: "DS", 0x00180081: "DS", 0x00180091: "IS", 0x00181314: "DS",
	// study, series, instance, frame of reference, image plane
	0x0020000D: "UI", 0x0020000E: "UI", 0x00200010: "SH", 0x00200011: "IS", 0x00200012: "IS", 0x00200013: "IS",
	0x00200020: "CS", 0x00200032: "DS", 0x00200037: "DS", 0x00200052: "UI", 0x00200060: "CS", 0x00201002: "IS",
	0x00201040: "LO", 0x00201041: "DS", 0x00201206: "IS", 0x00201208: "IS", 0x00201209: "IS", 0x00204000: "LT",
	// image pixel, VOI LUT, presentation
	0x00280002: "US", 0x00280004: "CS", 0x00280006: "US", 0x00280008: "IS", 0x00280009: "AT", 0x00280010: "US",
	0x00280011: "US", 0x00280030: "DS", 0x00280034: "IS", 0x00280100: "US", 0x00280101: "US", 0x00280102: "US",
	0x00280103: "US", 0x00280106: "US", 0x00280107: "US", 0x00281050: "DS", 0x00281051: "DS", 0x00281052: "DS",
	0x00281053: "DS", 0x00281054: "LO", 0x00281055: "LO", 0x00282110: "CS", 0x00282112: "DS", 0x00282114: "CS",
	0x00283010: "SQ", 0x00285000: "SQ",
	// requests, procedures, results
	0x00321032: "PN", 0x00321060: "LO", 0x00321064: "SQ", 0x00400244: "DA", 0x00400245: "TM", 0x00400253: "SH",
	0x00400254: "LO", 0x00400275: "SQ", 0x00401001: "SH", 0x0040A040: "CS", 0x0040A043: "SQ", 0x0040A160: "UT",
	0x0040A730: "SQ", 0x0040A010: "CS",
	// enhanced multi-frame
	0x52009229: "SQ", 0x52009230: "SQ", 0x00209113: "SQ", 0x00209116: "SQ", 0x00289110: "SQ", 0x00289132: "SQ",
	// pixel data and padding
	0x7FE00010: "OW", 0xFFFCFFFC: "OB",
	// encapsulated documents
	0x00420011: "OB", 0x00420012: "LO",
}
