package hl7dict

// Additional HL7 v2 tables.
//
// Every table here is named by a field this package already describes. A field
// saying "this is table 0112" while the dictionary carries no 0112 means the
// message browser shows a bare code forever, with nothing telling the reader
// that a meaning exists. Filling the gap is most of what turns the viewer from
// "labelled" into "readable".
//
// These are the HL7-defined values. A site that has extended a table locally
// will see its own codes pass through undecoded, which is correct: inventing a
// meaning for a local code is worse than showing the code.
func init() {
	for table, values := range extraTables {
		if _, exists := Tables[table]; exists {
			// Refuse to overwrite. A duplicated table would mean two sources of
			// truth for the same code, and the losing one would be invisible.
			panic("hl7dict: table " + table + " is defined twice")
		}
		Tables[table] = values
	}
}

var extraTables = map[string]map[string]string{
	// 0002 Marital status
	"0002": {
		"A": "Separated", "D": "Divorced", "M": "Married", "S": "Single",
		"W": "Widowed", "C": "Common law", "G": "Living together",
		"P": "Domestic partner", "R": "Registered domestic partner",
		"E": "Legally separated", "N": "Annulled", "I": "Interlocutory",
		"B": "Unmarried", "U": "Unknown", "O": "Other", "T": "Unreported",
	},

	// 0003 is already carried in dict.go (event type).

	// 0005 Race
	"0005": {
		"1002-5": "American Indian or Alaska Native",
		"2028-9": "Asian",
		"2054-5": "Black or African American",
		"2076-8": "Native Hawaiian or Other Pacific Islander",
		"2106-3": "White",
		"2131-1": "Other race",
	},

	// 0006 Religion
	"0006": {
		"ABC": "Agnostic", "AGN": "Agnostic", "ATH": "Atheist",
		"BAP": "Baptist", "BUD": "Buddhist", "CAT": "Roman Catholic",
		"CHR": "Christian", "EPI": "Episcopalian", "HIN": "Hindu",
		"JEW": "Judaism", "LUT": "Lutheran", "MET": "Methodist",
		"MOS": "Muslim", "MOR": "Latter Day Saints", "NON": "No religious affiliation",
		"ORT": "Orthodox", "OTH": "Other", "PEN": "Pentecostal",
		"PRE": "Presbyterian", "PRO": "Protestant", "QUA": "Quaker",
		"SIK": "Sikh", "UNI": "Unitarian", "VAR": "Unknown",
	},

	// 0007 Admission type
	"0007": {
		"A": "Accident", "C": "Elective", "E": "Emergency", "L": "Labor and delivery",
		"N": "Newborn", "R": "Routine", "U": "Urgent",
	},

	// 0018 Patient type
	"0018": {
		"I": "Inpatient", "O": "Outpatient", "E": "Emergency",
		"R": "Recurring", "O2": "Observation",
	},

	// 0023 Admit source
	"0023": {
		"1": "Physician referral", "2": "Clinic referral", "3": "HMO referral",
		"4": "Transfer from a hospital", "5": "Transfer from a skilled nursing facility",
		"6": "Transfer from another health care facility", "7": "Emergency room",
		"8": "Court or law enforcement", "9": "Information not available",
	},

	// 0038 Order status
	"0038": {
		"A":  "Some, but not all, results available",
		"CA": "Order was cancelled", "CM": "Order is completed",
		"DC": "Order was discontinued", "ER": "Error, order not found",
		"HD": "Order is on hold", "IP": "In process, unspecified",
		"RP": "Order has been replaced", "SC": "In process, scheduled",
	},

	// 0052 Diagnosis type
	"0052": {
		"A": "Admitting", "W": "Working", "F": "Final",
	},

	// 0062 Event reason
	"0062": {
		"01": "Patient request", "02": "Physician or health practitioner order",
		"03": "Census management", "O": "Other", "U": "Unknown",
	},

	// 0063 Relationship
	"0063": {
		"SEL": "Self", "SPO": "Spouse", "DOM": "Life partner", "CHD": "Child",
		"GCH": "Grandchild", "NCH": "Natural child", "SCH": "Stepchild",
		"FCH": "Foster child", "DEP": "Handicapped dependent",
		"WRD": "Ward of court", "PAR": "Parent", "MTH": "Mother", "FTH": "Father",
		"GRD": "Guardian", "GRP": "Grandparent", "SIB": "Sibling",
		"BRO": "Brother", "SIS": "Sister", "EXF": "Extended family",
		"FND": "Friend", "EME": "Employee", "EMR": "Employer",
		"ASC": "Associate", "TRA": "Trainer", "MGR": "Manager",
		"OWN": "Owner", "NON": "None", "OTH": "Other", "UNK": "Unknown",
		"CAR": "Care giver", "ANN": "Annuitant",
	},

	// 0065 Specimen action code
	"0065": {
		"A": "Add ordered tests to an existing specimen",
		"G": "Generated order, reflex order",
		"L": "Lab to obtain the specimen from the patient",
		"O": "Specimen obtained by a service other than the lab",
		"P": "Pending specimen, order sent before delivery",
		"R": "Revised order", "S": "Schedule the tests specified below",
	},

	// 0069 Hospital service
	"0069": {
		"MED": "Medical service", "SUR": "Surgical service",
		"URO": "Urology service", "PUL": "Pulmonary service",
		"CAR": "Cardiac service", "OBS": "Obstetrics service",
		"PSY": "Psychiatric service", "ONC": "Oncology service",
		"NEU": "Neurology service", "ORT": "Orthopaedic service",
		"PED": "Paediatric service", "REN": "Renal service",
	},

	// 0074 Diagnostic service section
	"0074": {
		"AU": "Audiology", "BG": "Blood gases", "BLB": "Blood bank",
		"CH": "Chemistry", "CP": "Cytopathology", "CT": "CAT scan",
		"CTH": "Cardiac catheterisation", "CUS": "Cardiac ultrasound",
		"EC": "Electrocardiac", "EN": "Electroneuro", "GE": "Genetics",
		"HM": "Haematology", "ICU": "Bedside ICU monitoring",
		"IMG": "Diagnostic imaging", "IMM": "Immunology",
		"LAB": "Laboratory", "MB": "Microbiology", "MCB": "Mycobacteriology",
		"MYC": "Mycology", "NMR": "Nuclear magnetic resonance",
		"NMS": "Nuclear medicine scan", "NRS": "Nursing service measures",
		"OSL": "Outside laboratory", "OT": "Occupational therapy",
		"OTH": "Other", "OUS": "OB ultrasound", "PF": "Pulmonary function",
		"PHR": "Pharmacy", "PHY": "Physician, history and physical",
		"PT": "Physical therapy", "RAD": "Radiology",
		"RC": "Respiratory care", "RT": "Radiation therapy",
		"RUS": "Radiology ultrasound", "RX": "Radiograph",
		"SP": "Surgical pathology", "SR": "Serology",
		"TX": "Toxicology", "VUS": "Vascular ultrasound",
		"VR": "Virology", "XRC": "Cineradiograph",
	},

	// 0076 Message type. Worth having: it is what MSH-9.1 means, and a reader
	// looking at a browser full of message codes wants to know which is which.
	"0076": {
		"ACK": "General acknowledgement", "ADT": "Admit, discharge, transfer",
		"ADR": "ADT response", "BAR": "Add or change billing account",
		"DFT": "Detailed financial transaction", "DOC": "Document response",
		"DSR": "Display response", "MDM": "Medical document management",
		"MFN": "Master file notification", "MFK": "Master file acknowledgement",
		"OMG": "General clinical order", "OML": "Laboratory order",
		"OMP": "Pharmacy or treatment order", "ORM": "Order message",
		"ORU": "Observation result, unsolicited", "ORR": "Order response",
		"OSU": "Order status update", "PPR": "Patient problem",
		"QBP": "Query by parameter", "QRY": "Query, original mode",
		"RAS": "Pharmacy administration", "RDE": "Pharmacy encoded order",
		"RDS": "Pharmacy dispense", "REF": "Patient referral",
		"RGV": "Pharmacy give", "RSP": "Segment pattern response",
		"SIU": "Schedule information unsolicited", "SRM": "Schedule request",
		"SRR": "Schedule request response", "SSU": "Specimen status update",
		"VXU": "Unsolicited vaccination record update",
	},

	// 0092 Re-admission indicator
	"0092": {
		"R": "Re-admission",
	},

	// 0105 Source of comment
	"0105": {
		"L": "Ancillary, filler, department is the source of the comment",
		"P": "Orderer, placer, is the source of the comment",
		"O": "Other system is the source of the comment",
	},

	// 0112 Discharge disposition. Clinically important: it is how a discharge
	// summary distinguishes "went home" from "died".
	"0112": {
		"01": "Discharged to home or self care",
		"02": "Discharged to another short term hospital",
		"03": "Discharged to a skilled nursing facility",
		"04": "Discharged to an intermediate care facility",
		"05": "Discharged to another institution",
		"06": "Discharged to home under organised home health care",
		"07": "Left against medical advice",
		"08": "Discharged to home under a home IV provider",
		"09": "Admitted as an inpatient to this hospital",
		"20": "Died",
		"30": "Still a patient",
		"40": "Died at home",
		"41": "Died in a medical facility",
		"42": "Died, place unknown",
	},

	// 0117 Account status
	"0117": {
		"O": "Open", "C": "Closed",
	},

	// 0127 Allergen type
	"0127": {
		"DA": "Drug allergy", "FA": "Food allergy",
		"MA": "Miscellaneous allergy", "MC": "Miscellaneous contraindication",
		"EA": "Environmental allergy", "AA": "Animal allergy",
		"PA": "Plant allergy", "LA": "Pollen allergy",
	},

	// 0128 Allergy severity. This is the one that decides whether a clinician
	// reads the rest of the entry.
	"0128": {
		"SV": "Severe", "MO": "Moderate", "MI": "Mild", "U": "Unknown",
	},

	// 0131 Contact role
	"0131": {
		"BP": "Billing contact person", "CP": "Contact person",
		"EP": "Emergency contact person", "PR": "Person preparing the referral",
		"E": "Employer", "C": "Emergency contact", "F": "Federal agency",
		"I": "Insurance company", "N": "Next of kin", "S": "State agency",
		"U": "Unknown", "O": "Other",
	},

	// 0136 Yes/no indicator
	"0136": {
		"Y": "Yes", "N": "No",
	},

	// 0189 Ethnic group
	"0189": {
		"2135-2": "Hispanic or Latino",
		"2186-5": "Not Hispanic or Latino",
		"U":      "Unknown",
	},

	// 0278 Filler status. The standard writes these values in mixed case, but
	// lookup upper-cases the incoming code, so the keys have to be uppercase or
	// the entries would exist and never match. The meanings keep their casing.
	"0278": {
		"BLOCKED": "Blocked", "BOOKED": "Booked", "CANCELLED": "Cancelled",
		"COMPLETE": "Complete", "DELETED": "Deleted", "DISCONTINUED": "Discontinued",
		"OVERBOOK": "Overbook", "PENDING": "Pending", "STARTED": "Started",
		"WAITLIST": "Waitlist",
	},

	// 0287 Problem or goal action code
	"0287": {
		"AD": "Add", "CO": "Correct", "DE": "Delete", "LI": "Link",
		"UC": "Unchanged", "UN": "Unlink", "UP": "Update",
	},

	// 0357 Message error condition. Worth carrying: this is what a rejecting
	// system says went wrong, and it is usually the only clue.
	"0357": {
		"0":   "Message accepted",
		"100": "Segment sequence error",
		"101": "Required field missing",
		"102": "Data type error",
		"103": "Table value not found",
		"200": "Unsupported message type",
		"201": "Unsupported event code",
		"202": "Unsupported processing id",
		"203": "Unsupported version id",
		"204": "Unknown key identifier",
		"205": "Duplicate key identifier",
		"206": "Application record locked",
		"207": "Application internal error",
	},

	// 0369 Specimen role
	"0369": {
		"B": "Blind sample", "C": "Calibrator", "E": "Electronic QC",
		"F": "Specimen used for testing proficiency",
		"G": "Group, pooled", "L": "Pool",
		"P": "Patient", "Q": "Control specimen",
		"R": "Replicate", "V": "Verifying calibrator",
	},

	// 0443 Provider role
	"0443": {
		"AD": "Admitting", "AT": "Attending", "CP": "Consulting provider",
		"FHCP": "Family health care professional", "PP": "Primary care provider",
		"RP": "Referring provider", "RT": "Referred to provider",
	},

	// 0487 Specimen type
	"0487": {
		"BLD": "Whole blood", "BLDA": "Blood, arterial", "BLDV": "Blood, venous",
		"BLDC": "Blood, capillary", "SER": "Serum", "PLAS": "Plasma",
		"UR": "Urine", "URT": "Urine, catheter", "URC": "Urine, clean catch",
		"CSF": "Cerebrospinal fluid", "STL": "Stool", "SPT": "Sputum",
		"SAL": "Saliva", "SWB": "Swab", "TISS": "Tissue", "BON": "Bone",
		"HAR": "Hair", "NAIL": "Nail", "AMN": "Amniotic fluid",
		"BAL": "Bronchoalveolar lavage", "WND": "Wound",
	},

	// 0516 Error severity
	"0516": {
		"E": "Error", "F": "Fatal error", "I": "Information", "W": "Warning",
	},
}
