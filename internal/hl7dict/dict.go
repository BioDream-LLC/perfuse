// Package hl7dict names the fields of HL7 v2 segments.
//
// This exists for one screen: a message viewer that can tell you PID-5.1 is the
// patient's family name instead of leaving you to count carets. Anyone who has
// debugged an interface has done that counting by hand, and it is the single most
// common source of the off-by-one mistakes that put a value in the wrong field.
//
// It is not the whole standard. HL7 v2 has hundreds of segments and thousands of
// fields, and the ones here are those that appear in ADT, ORU, ORM and SIU
// traffic. An unknown field is reported as unknown rather than guessed at.
package hl7dict

import (
	"fmt"
	"strings"
)

// Field describes one field of a segment.
type Field struct {
	// Name is the field's name from the standard.
	Name string
	// Description adds context where the name alone is not enough.
	Description string
	// Components names the parts of a composite field, in order.
	Components []string
	// Table is the HL7 table number that constrains the value, if any.
	Table string
	// Repeats reports whether the field may repeat.
	Repeats bool
}

// Segment describes one segment.
type Segment struct {
	Name        string
	Description string
	// Fields is indexed by HL7 field number, so Fields[5] is field 5. Index 0 is
	// unused, which keeps the numbering the same as the standard and the same as
	// what people write in a ticket.
	Fields map[int]Field
}

// segments holds the dictionary.
var segments = map[string]Segment{
	"MSH": {
		Name:        "MSH",
		Description: "Message header. Every message starts with one.",
		Fields: map[int]Field{
			1:  {Name: "Field Separator", Description: "The character separating fields, usually |"},
			2:  {Name: "Encoding Characters", Description: "Component, repeat, escape and subcomponent separators"},
			3:  {Name: "Sending Application", Components: []string{"Namespace ID", "Universal ID", "Universal ID Type"}},
			4:  {Name: "Sending Facility", Components: []string{"Namespace ID", "Universal ID", "Universal ID Type"}},
			5:  {Name: "Receiving Application", Components: []string{"Namespace ID", "Universal ID", "Universal ID Type"}},
			6:  {Name: "Receiving Facility", Components: []string{"Namespace ID", "Universal ID", "Universal ID Type"}},
			7:  {Name: "Date/Time of Message"},
			8:  {Name: "Security"},
			9:  {Name: "Message Type", Components: []string{"Message Code", "Trigger Event", "Message Structure"}, Table: "0076"},
			10: {Name: "Message Control ID", Description: "Unique per message; an acknowledgement echoes it in MSA-2"},
			11: {Name: "Processing ID", Description: "P production, T training, D debugging", Table: "0103"},
			12: {Name: "Version ID", Description: "The HL7 version the sender used"},
			13: {Name: "Sequence Number"},
			14: {Name: "Continuation Pointer"},
			15: {Name: "Accept Acknowledgment Type", Description: "Whether a commit acknowledgement is wanted", Table: "0155"},
			16: {Name: "Application Acknowledgment Type", Table: "0155"},
			17: {Name: "Country Code"},
			18: {Name: "Character Set"},
			19: {Name: "Principal Language of Message"},
			21: {Name: "Message Profile Identifier", Repeats: true},
		},
	},

	"EVN": {
		Name:        "EVN",
		Description: "Event type. Says what happened and when.",
		Fields: map[int]Field{
			1: {Name: "Event Type Code", Table: "0003"},
			2: {Name: "Recorded Date/Time"},
			3: {Name: "Date/Time of Planned Event"},
			4: {Name: "Event Reason Code", Table: "0062"},
			5: {Name: "Operator ID", Repeats: true, Components: []string{"ID Number", "Family Name", "Given Name"}},
			6: {Name: "Event Occurred"},
			7: {Name: "Event Facility"},
		},
	},

	"PID": {
		Name:        "PID",
		Description: "Patient identification.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Patient ID", Description: "Deprecated; use PID-3"},
			3: {Name: "Patient Identifier List", Repeats: true,
				Components:  []string{"ID Number", "Check Digit", "Check Digit Scheme", "Assigning Authority", "Identifier Type Code", "Assigning Facility"},
				Description: "Repeats: a patient commonly has an MRN and a national identifier"},
			4: {Name: "Alternate Patient ID", Description: "Deprecated"},
			5: {Name: "Patient Name", Repeats: true,
				Components: []string{"Family Name", "Given Name", "Middle Name", "Suffix", "Prefix", "Degree", "Name Type Code"}},
			6:  {Name: "Mother's Maiden Name", Components: []string{"Family Name", "Given Name"}},
			7:  {Name: "Date/Time of Birth"},
			8:  {Name: "Administrative Sex", Table: "0001"},
			9:  {Name: "Patient Alias", Repeats: true},
			10: {Name: "Race", Repeats: true, Table: "0005"},
			11: {Name: "Patient Address", Repeats: true,
				Components: []string{"Street Address", "Other Designation", "City", "State", "Postal Code", "Country", "Address Type"}},
			12: {Name: "County Code"},
			13: {Name: "Phone Number - Home", Repeats: true,
				Components: []string{"Telephone Number", "Telecommunication Use Code", "Equipment Type", "Email Address"}},
			14: {Name: "Phone Number - Business", Repeats: true},
			15: {Name: "Primary Language"},
			16: {Name: "Marital Status", Table: "0002"},
			17: {Name: "Religion", Table: "0006"},
			18: {Name: "Patient Account Number"},
			19: {Name: "SSN Number", Description: "Deprecated; identifiers belong in PID-3"},
			20: {Name: "Driver's License Number"},
			21: {Name: "Mother's Identifier", Repeats: true},
			22: {Name: "Ethnic Group", Repeats: true, Table: "0189"},
			23: {Name: "Birth Place"},
			24: {Name: "Multiple Birth Indicator", Table: "0136"},
			25: {Name: "Birth Order"},
			26: {Name: "Citizenship", Repeats: true},
			27: {Name: "Veterans Military Status"},
			28: {Name: "Nationality"},
			29: {Name: "Patient Death Date and Time"},
			30: {Name: "Patient Death Indicator", Table: "0136"},
			31: {Name: "Identity Unknown Indicator", Table: "0136"},
			32: {Name: "Identity Reliability Code", Repeats: true},
			33: {Name: "Last Update Date/Time"},
			34: {Name: "Last Update Facility"},
		},
	},

	"PV1": {
		Name:        "PV1",
		Description: "Patient visit. The encounter, not the person.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Patient Class", Table: "0004",
				Description: "I inpatient, O outpatient, E emergency, P preadmit, R recurring, B obstetrics"},
			3: {Name: "Assigned Patient Location",
				Components: []string{"Point of Care", "Room", "Bed", "Facility", "Location Status", "Person Location Type", "Building", "Floor"}},
			4: {Name: "Admission Type", Table: "0007"},
			5: {Name: "Preadmit Number"},
			6: {Name: "Prior Patient Location"},
			7: {Name: "Attending Doctor", Repeats: true,
				Components: []string{"ID Number", "Family Name", "Given Name", "Middle Name", "Suffix", "Prefix"}},
			8:  {Name: "Referring Doctor", Repeats: true},
			9:  {Name: "Consulting Doctor", Repeats: true},
			10: {Name: "Hospital Service", Table: "0069"},
			11: {Name: "Temporary Location"},
			12: {Name: "Preadmit Test Indicator"},
			13: {Name: "Re-admission Indicator", Table: "0092"},
			14: {Name: "Admit Source", Table: "0023"},
			15: {Name: "Ambulatory Status", Repeats: true},
			16: {Name: "VIP Indicator"},
			17: {Name: "Admitting Doctor", Repeats: true},
			18: {Name: "Patient Type", Table: "0018"},
			19: {Name: "Visit Number", Description: "What makes an encounter identifiable across messages"},
			20: {Name: "Financial Class", Repeats: true},
			36: {Name: "Discharge Disposition", Table: "0112"},
			37: {Name: "Discharged to Location"},
			38: {Name: "Diet Type"},
			39: {Name: "Servicing Facility"},
			41: {Name: "Account Status", Table: "0117"},
			42: {Name: "Pending Location"},
			43: {Name: "Prior Temporary Location"},
			44: {Name: "Admit Date/Time"},
			45: {Name: "Discharge Date/Time"},
			46: {Name: "Current Patient Balance"},
			47: {Name: "Total Charges"},
			50: {Name: "Alternate Visit ID"},
			51: {Name: "Visit Indicator"},
		},
	},

	"PV2": {
		Name:        "PV2",
		Description: "Patient visit, additional information.",
		Fields: map[int]Field{
			3:  {Name: "Admit Reason"},
			8:  {Name: "Expected Admit Date/Time"},
			9:  {Name: "Expected Discharge Date/Time"},
			24: {Name: "Patient Status Code"},
			25: {Name: "Visit Priority Code"},
		},
	},

	"NK1": {
		Name:        "NK1",
		Description: "Next of kin and associated parties.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Name", Components: []string{"Family Name", "Given Name", "Middle Name"}},
			3: {Name: "Relationship", Table: "0063"},
			4: {Name: "Address", Components: []string{"Street Address", "Other Designation", "City", "State", "Postal Code"}},
			5: {Name: "Phone Number", Repeats: true},
			6: {Name: "Business Phone Number", Repeats: true},
			7: {Name: "Contact Role", Table: "0131"},
		},
	},

	"OBR": {
		Name:        "OBR",
		Description: "Observation request. One report or order.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Placer Order Number", Description: "The ordering system's identifier"},
			3: {Name: "Filler Order Number", Description: "The performing lab's identifier, often the accession number"},
			4: {Name: "Universal Service Identifier",
				Components:  []string{"Identifier", "Text", "Coding System", "Alternate Identifier", "Alternate Text", "Alternate Coding System"},
				Description: "What was ordered or reported"},
			5:  {Name: "Priority", Description: "Deprecated; see OBR-27"},
			6:  {Name: "Requested Date/Time"},
			7:  {Name: "Observation Date/Time", Description: "When the specimen was collected or the observation made"},
			8:  {Name: "Observation End Date/Time"},
			9:  {Name: "Collection Volume"},
			10: {Name: "Collector Identifier", Repeats: true},
			11: {Name: "Specimen Action Code", Table: "0065"},
			12: {Name: "Danger Code"},
			13: {Name: "Relevant Clinical Information"},
			14: {Name: "Specimen Received Date/Time"},
			15: {Name: "Specimen Source"},
			16: {Name: "Ordering Provider", Repeats: true,
				Components: []string{"ID Number", "Family Name", "Given Name"}},
			17: {Name: "Order Callback Phone Number", Repeats: true},
			18: {Name: "Placer Field 1"},
			19: {Name: "Placer Field 2"},
			20: {Name: "Filler Field 1"},
			21: {Name: "Filler Field 2"},
			22: {Name: "Results Rpt/Status Chng Date/Time", Description: "When the report was released"},
			23: {Name: "Charge to Practice"},
			24: {Name: "Diagnostic Serv Sect ID", Table: "0074"},
			25: {Name: "Result Status", Table: "0123",
				Description: "F final, P preliminary, C corrected, X cancelled"},
			26: {Name: "Parent Result"},
			27: {Name: "Quantity/Timing", Components: []string{"Quantity", "Interval", "Duration", "Start Date/Time", "End Date/Time", "Priority"}},
			28: {Name: "Result Copies To", Repeats: true},
			29: {Name: "Parent"},
			30: {Name: "Transportation Mode"},
			31: {Name: "Reason for Study", Repeats: true},
			32: {Name: "Principal Result Interpreter"},
			33: {Name: "Assistant Result Interpreter", Repeats: true},
			34: {Name: "Technician", Repeats: true},
			35: {Name: "Transcriptionist", Repeats: true},
			36: {Name: "Scheduled Date/Time"},
		},
	},

	"OBX": {
		Name:        "OBX",
		Description: "Observation result. One measurement or finding.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Value Type", Table: "0125",
				Description: "NM numeric, ST string, TX text, CE coded, SN structured numeric, DT date"},
			3: {Name: "Observation Identifier",
				Components:  []string{"Identifier", "Text", "Coding System", "Alternate Identifier", "Alternate Text", "Alternate Coding System"},
				Description: "What was measured, ideally a LOINC code"},
			4: {Name: "Observation Sub-ID", Description: "Groups related results, such as the parts of one culture"},
			5: {Name: "Observation Value", Repeats: true,
				Description: "Interpreted according to OBX-2"},
			6: {Name: "Units", Components: []string{"Identifier", "Text", "Coding System"},
				Description: "Without a coded unit a receiver cannot convert the value safely"},
			7: {Name: "References Range", Description: "Usually free text such as 12.0-16.0"},
			8: {Name: "Abnormal Flags", Repeats: true, Table: "0078",
				Description: "H high, L low, HH critical high, LL critical low, A abnormal"},
			9:  {Name: "Probability"},
			10: {Name: "Nature of Abnormal Test", Repeats: true},
			11: {Name: "Observation Result Status", Table: "0085",
				Description: "F final, P preliminary, C corrected, D deleted, X cannot be obtained"},
			12: {Name: "Effective Date of Reference Range"},
			13: {Name: "User Defined Access Checks"},
			14: {Name: "Date/Time of the Observation"},
			15: {Name: "Producer's ID"},
			16: {Name: "Responsible Observer", Repeats: true},
			17: {Name: "Observation Method", Repeats: true,
				Description: "The same analyte measured two ways is not always comparable"},
			18: {Name: "Equipment Instance Identifier", Repeats: true},
			19: {Name: "Date/Time of the Analysis"},
			23: {Name: "Performing Organization Name"},
			24: {Name: "Performing Organization Address"},
		},
	},

	"ORC": {
		Name:        "ORC",
		Description: "Common order.",
		Fields: map[int]Field{
			1: {Name: "Order Control", Table: "0119",
				Description: "NW new, CA cancel, DC discontinue, HD hold, CM completed"},
			2:  {Name: "Placer Order Number"},
			3:  {Name: "Filler Order Number"},
			4:  {Name: "Placer Group Number"},
			5:  {Name: "Order Status", Table: "0038"},
			6:  {Name: "Response Flag"},
			7:  {Name: "Quantity/Timing"},
			9:  {Name: "Date/Time of Transaction"},
			10: {Name: "Entered By", Repeats: true},
			11: {Name: "Verified By", Repeats: true},
			12: {Name: "Ordering Provider", Repeats: true,
				Components: []string{"ID Number", "Family Name", "Given Name"}},
			13: {Name: "Enterer's Location"},
			14: {Name: "Call Back Phone Number", Repeats: true},
			15: {Name: "Order Effective Date/Time"},
			16: {Name: "Order Control Code Reason"},
			17: {Name: "Entering Organization"},
			21: {Name: "Ordering Facility Name", Repeats: true},
		},
	},

	// TXA carries the metadata for a clinical document, which is how a CDA
	// travels: the document itself is base64 in OBX-5 and everything about it is
	// here. Field 13 in particular is the document being replaced, and mistaking
	// it for field 14 makes a replacement look like a new document.
	"TXA": {
		Name:        "TXA",
		Description: "Document notification: what the document is, who wrote it, and what it replaces.",
		Fields: map[int]Field{
			1:  {Name: "Set ID"},
			2:  {Name: "Document Type", Description: "DS discharge summary, CN consultation, HP history and physical, OP operative note"},
			3:  {Name: "Document Content Presentation", Description: "AP application data, TX machine-readable text, TEXT display text"},
			4:  {Name: "Activity Date/Time"},
			5:  {Name: "Primary Activity Provider Code/Name"},
			6:  {Name: "Origination Date/Time"},
			7:  {Name: "Transcription Date/Time"},
			8:  {Name: "Edit Date/Time"},
			9:  {Name: "Originator Code/Name"},
			10: {Name: "Assigned Document Authenticator"},
			11: {Name: "Transcriptionist Code/Name"},
			12: {Name: "Unique Document Number", Description: "The document's own identifier, not the message's"},
			13: {Name: "Parent Document Number", Description: "The document this one replaces. Not field 14"},
			14: {Name: "Placer Order Number"},
			15: {Name: "Filler Order Number"},
			16: {Name: "Unique Document File Name"},
			17: {Name: "Document Completion Status", Description: "AU authenticated, DI dictated, DO documented, IN incomplete, IP in progress, LA legally authenticated, PA pre-authenticated"},
			18: {Name: "Document Confidentiality Status"},
			19: {Name: "Document Availability Status", Description: "AV available, CA deleted, OB obsolete, UN unavailable"},
			20: {Name: "Document Storage Status"},
			21: {Name: "Document Change Reason"},
			22: {Name: "Authentication Person, Time Stamp"},
			23: {Name: "Distributed Copies"},
		},
	},

	"MSA": {
		Name:        "MSA",
		Description: "Message acknowledgement.",
		Fields: map[int]Field{
			1: {Name: "Acknowledgment Code", Table: "0008",
				Description: "AA accepted, AE error, AR rejected; CA/CE/CR in enhanced mode"},
			2: {Name: "Message Control ID", Description: "Echoes MSH-10 of the message being answered"},
			3: {Name: "Text Message", Description: "Why, for a human"},
			4: {Name: "Expected Sequence Number"},
			6: {Name: "Error Condition"},
		},
	},

	"ERR": {
		Name:        "ERR",
		Description: "Error detail on an acknowledgement.",
		Fields: map[int]Field{
			1: {Name: "Error Code and Location", Description: "Deprecated"},
			2: {Name: "Error Location"},
			3: {Name: "HL7 Error Code", Table: "0357"},
			4: {Name: "Severity", Table: "0516"},
			5: {Name: "Application Error Code"},
			8: {Name: "User Message"},
		},
	},

	"DG1": {
		Name:        "DG1",
		Description: "Diagnosis.",
		Fields: map[int]Field{
			1:  {Name: "Set ID"},
			2:  {Name: "Diagnosis Coding Method", Description: "Deprecated"},
			3:  {Name: "Diagnosis Code", Components: []string{"Identifier", "Text", "Coding System"}},
			4:  {Name: "Diagnosis Description", Description: "Deprecated; use DG1-3.2"},
			5:  {Name: "Diagnosis Date/Time"},
			6:  {Name: "Diagnosis Type", Table: "0052"},
			15: {Name: "Diagnosis Priority"},
			16: {Name: "Diagnosing Clinician", Repeats: true},
		},
	},

	"AL1": {
		Name:        "AL1",
		Description: "Patient allergy information.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Allergen Type Code", Table: "0127"},
			3: {Name: "Allergen Code/Mnemonic/Description"},
			4: {Name: "Allergy Severity Code", Table: "0128"},
			5: {Name: "Allergy Reaction Code", Repeats: true},
			6: {Name: "Identification Date"},
		},
	},

	"IN1": {
		Name:        "IN1",
		Description: "Insurance.",
		Fields: map[int]Field{
			1:  {Name: "Set ID"},
			2:  {Name: "Insurance Plan ID"},
			3:  {Name: "Insurance Company ID", Repeats: true},
			4:  {Name: "Insurance Company Name", Repeats: true},
			5:  {Name: "Insurance Company Address", Repeats: true},
			8:  {Name: "Group Number"},
			11: {Name: "Insured's Group Emp Name", Repeats: true},
			12: {Name: "Plan Effective Date"},
			13: {Name: "Plan Expiration Date"},
			15: {Name: "Plan Type"},
			16: {Name: "Name of Insured", Repeats: true},
			17: {Name: "Insured's Relationship to Patient", Table: "0063"},
			36: {Name: "Policy Number"},
		},
	},

	"GT1": {
		Name:        "GT1",
		Description: "Guarantor.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Guarantor Number", Repeats: true},
			3: {Name: "Guarantor Name", Repeats: true,
				Components: []string{"Family Name", "Given Name", "Middle Name"}},
			5:  {Name: "Guarantor Address", Repeats: true},
			6:  {Name: "Guarantor Phone Number - Home", Repeats: true},
			11: {Name: "Guarantor Relationship", Table: "0063"},
		},
	},

	"SPM": {
		Name:        "SPM",
		Description: "Specimen.",
		Fields: map[int]Field{
			1:  {Name: "Set ID"},
			2:  {Name: "Specimen ID"},
			4:  {Name: "Specimen Type", Table: "0487"},
			8:  {Name: "Specimen Source Site"},
			11: {Name: "Specimen Role", Table: "0369"},
			17: {Name: "Specimen Collection Date/Time"},
			18: {Name: "Specimen Received Date/Time"},
			20: {Name: "Specimen Reject Reason", Repeats: true},
		},
	},

	"SCH": {
		Name:        "SCH",
		Description: "Scheduling activity information.",
		Fields: map[int]Field{
			1:  {Name: "Placer Appointment ID"},
			2:  {Name: "Filler Appointment ID"},
			6:  {Name: "Event Reason"},
			7:  {Name: "Appointment Reason"},
			8:  {Name: "Appointment Type"},
			11: {Name: "Appointment Timing Quantity", Repeats: true},
			16: {Name: "Filler Contact Person", Repeats: true},
			20: {Name: "Entered by Person", Repeats: true},
			25: {Name: "Filler Status Code", Table: "0278"},
		},
	},

	"NTE": {
		Name:        "NTE",
		Description: "Notes and comments. Applies to whatever segment precedes it.",
		Fields: map[int]Field{
			1: {Name: "Set ID"},
			2: {Name: "Source of Comment", Table: "0105"},
			3: {Name: "Comment", Repeats: true},
			4: {Name: "Comment Type"},
		},
	},

	"PD1": {
		Name:        "PD1",
		Description: "Patient additional demographic.",
		Fields: map[int]Field{
			3: {Name: "Patient Primary Facility", Repeats: true},
			4: {Name: "Patient Primary Care Provider Name & ID", Repeats: true},
		},
	},

	"ROL": {
		Name:        "ROL",
		Description: "Role.",
		Fields: map[int]Field{
			1: {Name: "Role Instance ID"},
			2: {Name: "Action Code", Table: "0287"},
			3: {Name: "Role", Table: "0443"},
			4: {Name: "Role Person", Repeats: true},
		},
	},

	"ZDS": {
		Name:        "ZDS",
		Description: "A Z-segment: locally defined, so its meaning is site-specific.",
		Fields:      map[int]Field{},
	},
}

// Tables gives the meaning of coded values for the tables worth explaining in a
// viewer. Only the ones people actually squint at are here.
var Tables = map[string]map[string]string{
	"0001": {
		"M": "Male", "F": "Female", "O": "Other", "U": "Unknown",
		"A": "Ambiguous", "N": "Not applicable",
	},
	"0003": {
		"A01": "Admit / visit notification", "A02": "Transfer a patient",
		"A03": "Discharge / end visit", "A04": "Register a patient",
		"A05": "Pre-admit a patient", "A06": "Change an outpatient to an inpatient",
		"A07": "Change an inpatient to an outpatient", "A08": "Update patient information",
		"A09": "Patient departing", "A10": "Patient arriving",
		"A11": "Cancel admit", "A12": "Cancel transfer",
		"A13": "Cancel discharge", "A14": "Pending admit",
		"A15": "Pending transfer", "A16": "Pending discharge",
		"A17": "Swap patients", "A18": "Merge patient information",
		"A19": "Patient query", "A20": "Bed status update",
		"A21": "Patient goes on leave of absence", "A22": "Patient returns from leave",
		"A23": "Delete a patient record", "A24": "Link patient information",
		"A28": "Add person information", "A29": "Delete person information",
		"A30": "Merge person information", "A31": "Update person information",
		"A34": "Merge patient information, patient ID only",
		"A40": "Merge patient, patient identifier list",
		"A44": "Move account information",
		"R01": "Unsolicited observation message",
	},
	"0004": {
		"I": "Inpatient", "O": "Outpatient", "E": "Emergency",
		"P": "Preadmit", "R": "Recurring patient", "B": "Obstetrics",
		"C": "Commercial account", "N": "Not applicable", "U": "Unknown",
	},
	"0008": {
		"AA": "Application accept", "AE": "Application error", "AR": "Application reject",
		"CA": "Commit accept", "CE": "Commit error", "CR": "Commit reject",
	},
	"0078": {
		"L": "Below low normal", "H": "Above high normal",
		"LL": "Below lower panic limit", "HH": "Above upper panic limit",
		"N": "Normal", "A": "Abnormal", "AA": "Very abnormal",
		"<": "Below absolute low", ">": "Above absolute high",
		"S": "Susceptible", "R": "Resistant", "I": "Intermediate",
		"U": "Significant change up", "D": "Significant change down",
		"B": "Better", "W": "Worse",
		"POS": "Positive", "NEG": "Negative", "IND": "Indeterminate",
		"DET": "Detected", "ND": "Not detected",
	},
	"0085": {
		"C": "Record coming over is a correction", "D": "Deletes the OBX record",
		"F": "Final result", "I": "Specimen in lab, results pending",
		"P": "Preliminary result", "R": "Results entered, not verified",
		"S": "Partial results", "U": "Results status change to final",
		"W": "Post original as wrong", "X": "Results cannot be obtained",
	},
	"0103": {
		"D": "Debugging", "P": "Production", "T": "Training",
	},
	"0123": {
		"O": "Order received, specimen not yet received",
		"I": "No results available, specimen received",
		"S": "No results available, procedure scheduled",
		"A": "Some results available", "P": "Preliminary",
		"C": "Correction to results", "R": "Results stored, not yet verified",
		"F": "Final results", "X": "No results available, order cancelled",
		"Y": "No order on record for this test", "Z": "No record of this patient",
	},
	"0125": {
		"AD": "Address", "CE": "Coded entry", "CF": "Coded element with formatted values",
		"CK": "Composite ID with check digit", "CN": "Composite ID and name",
		"CP": "Composite price", "CX": "Extended composite ID",
		"DT": "Date", "ED": "Encapsulated data", "FT": "Formatted text",
		"ID": "Coded value for HL7 tables", "IS": "Coded value for user tables",
		"NM": "Numeric", "PN": "Person name", "RP": "Reference pointer",
		"SN": "Structured numeric", "ST": "String", "TM": "Time",
		"TN": "Telephone number", "TS": "Time stamp", "TX": "Text",
		"XAD": "Extended address", "XCN": "Extended composite name and number",
		"XON": "Extended organisation name", "XPN": "Extended person name",
		"XTN": "Extended telecommunication number",
	},
	"0155": {
		"AL": "Always", "NE": "Never", "ER": "Only on error", "SU": "Only on success",
	},
	"0119": {
		"NW": "New order", "OK": "Order accepted", "CA": "Cancel order",
		"DC": "Discontinue order", "HD": "Hold order", "CM": "Order is completed",
		"RE": "Observations to follow", "RO": "Replacement order",
		"SC": "Status changed", "UA": "Unable to accept order",
	},
}

// LookupSegment returns the description of a segment.
//
// A segment beginning with Z is locally defined, so it is reported as such rather
// than as unknown: those two mean different things to somebody reading a message.
func LookupSegment(name string) (Segment, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if seg, ok := segments[name]; ok {
		return seg, true
	}
	if strings.HasPrefix(name, "Z") {
		return Segment{
			Name:        name,
			Description: "A Z-segment: locally defined by the sending site, so its meaning is not in the standard.",
			Fields:      map[int]Field{},
		}, true
	}
	return Segment{}, false
}

// LookupField returns the description of one field.
func LookupField(segment string, field int) (Field, bool) {
	seg, ok := LookupSegment(segment)
	if !ok {
		return Field{}, false
	}
	f, ok := seg.Fields[field]
	return f, ok
}

// Describe returns a readable label for a path such as "PID-5.1".
//
// This is what turns counting carets by hand into reading a name, which is the
// point of the whole package.
func Describe(segment string, field, component int) string {
	f, ok := LookupField(segment, field)
	if !ok {
		if _, segKnown := LookupSegment(segment); segKnown {
			return fmt.Sprintf("%s-%d (not in the dictionary)", segment, field)
		}
		return fmt.Sprintf("%s-%d (unknown segment)", segment, field)
	}

	label := f.Name
	if component > 0 && component <= len(f.Components) {
		label += " - " + f.Components[component-1]
	} else if component > 0 {
		label += fmt.Sprintf(" - component %d", component)
	}
	return label
}

// ExplainCode returns the meaning of a coded value, if the table is known.
func ExplainCode(table, code string) (string, bool) {
	if table == "" || code == "" {
		return "", false
	}
	values, ok := Tables[table]
	if !ok {
		return "", false
	}
	meaning, ok := values[strings.ToUpper(strings.TrimSpace(code))]
	return meaning, ok
}

// KnownSegments lists the segments in the dictionary.
func KnownSegments() []string {
	out := make([]string, 0, len(segments))
	for name := range segments {
		out = append(out, name)
	}
	// Insertion sort keeps this dependency-free and the list is tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
