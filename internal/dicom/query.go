package dicom

// Query and retrieve information models.
//
// Two of the three defined models. Study Root is what practically everything uses and what any archive supporting query at
// all will support; Patient Root is offered because a few older systems accept only that one, and negotiating both costs a
// single extra presentation context.
//
// Patient/Study Only is deliberately absent: it was retired from the standard, and offering a retired model to an archive
// that still accepts it would produce queries nobody can support later.
const (
	// StudyRootQueryFIND is C-FIND under the study root model.
	StudyRootQueryFIND = "1.2.840.10008.5.1.4.1.2.2.1"
	// StudyRootQueryMOVE is C-MOVE under the study root model.
	StudyRootQueryMOVE = "1.2.840.10008.5.1.4.1.2.2.2"
	// StudyRootQueryGET is C-GET under the study root model.
	StudyRootQueryGET = "1.2.840.10008.5.1.4.1.2.2.3"

	// PatientRootQueryFIND is C-FIND under the patient root model.
	PatientRootQueryFIND = "1.2.840.10008.5.1.4.1.2.1.1"
	// PatientRootQueryMOVE is C-MOVE under the patient root model.
	PatientRootQueryMOVE = "1.2.840.10008.5.1.4.1.2.1.2"
	// PatientRootQueryGET is C-GET under the patient root model.
	PatientRootQueryGET = "1.2.840.10008.5.1.4.1.2.1.3"
)

// QueryLevel is how much of the hierarchy a query asks about.
//
// The levels are a hierarchy - a patient has studies, a study has series, a series has instances - and the level says which
// rung the answers come back on. Getting it wrong is the single most common query mistake: asking at STUDY level and
// expecting one response per image produces one per study, and the caller concludes the archive is missing data.
type QueryLevel string

const (
	// LevelPatient returns one response per patient. Only valid under the patient root model.
	LevelPatient QueryLevel = "PATIENT"
	// LevelStudy returns one response per study, and is what almost every useful query wants.
	LevelStudy QueryLevel = "STUDY"
	// LevelSeries returns one response per series.
	LevelSeries QueryLevel = "SERIES"
	// LevelImage returns one response per instance, which for a large CT is hundreds.
	LevelImage QueryLevel = "IMAGE"
)

// Valid reports whether a level is one of the four.
func (l QueryLevel) Valid() bool {
	switch l {
	case LevelPatient, LevelStudy, LevelSeries, LevelImage:
		return true
	}
	return false
}

// Query and retrieve DIMSE command fields.
const (
	cmdCFindRQ   uint16 = 0x0020
	cmdCFindRSP  uint16 = 0x8020
	cmdCMoveRQ   uint16 = 0x0021
	cmdCMoveRSP  uint16 = 0x8021
	cmdCGetRQ    uint16 = 0x0010
	cmdCGetRSP   uint16 = 0x8010
	cmdCCancelRQ uint16 = 0x0FFF
)

// DIMSE statuses that query and retrieve use beyond plain success.
const (
	// statusPending means a response carries a match and more may follow.
	statusPending uint16 = 0xFF00
	// statusPendingInexact means the same but warns that some optional keys were not supported.
	//
	// Treated as a match rather than an error, because an archive that ignored one optional key is still answering the
	// question. Whether it matters is the caller's judgement, so it is reported rather than swallowed.
	statusPendingInexact uint16 = 0xFF01

	// statusCancelled means the request was cancelled and the matching stopped.
	statusCancelled uint16 = 0xFE00
)

// IsPending reports whether a status means "here is a match, more may follow".
func IsPending(status uint16) bool {
	return status == statusPending || status == statusPendingInexact
}

// Tags used by query and retrieve that the storage path did not already need.
//
// The ones already declared for parsing stored objects are reused rather than restated, because two declarations of the
// same tag is how a typo in one of them survives.
var (
	TagQueryRetrieveLevel             = Tag{0x0008, 0x0052}
	TagNumberOfStudies                = Tag{0x0020, 0x1200}
	TagModalitiesInStudy              = Tag{0x0008, 0x0061}
	TagNumberOfSeries                 = Tag{0x0020, 0x1206}
	TagNumberOfInstances              = Tag{0x0020, 0x1208}
	TagRetrieveAETitle                = Tag{0x0008, 0x0054}
	tagNumberOfRemainingSuboperations = Tag{0x0000, 0x1020}
	tagNumberOfCompletedSuboperations = Tag{0x0000, 0x1021}
	tagNumberOfFailedSuboperations    = Tag{0x0000, 0x1022}
	tagNumberOfWarningSuboperations   = Tag{0x0000, 0x1023}
	tagMoveDestination                = Tag{0x0000, 0x0600}
)

// findRQ builds a C-FIND-RQ command set.
func findRQ(sopClass string, messageID uint16) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCFindRQ)},
		{Tag: tagMessageID, Value: uint16Value(messageID)},
		// Priority is medium. The standard defines low, medium and high, and archives overwhelmingly ignore it; sending
		// high on every query would be a request to be deprioritised by any archive that does not.
		{Tag: tagPriority, Value: uint16Value(0x0000)},
		// A data set follows, which for C-FIND is the identifier being matched.
		// 0x0000 means a data set follows, which for C-FIND is the identifier being matched. Not optional here: a
		// C-FIND with no identifier is a query with no keys.
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0000)},
	})
}

// findRSP builds a C-FIND-RSP command set.
func findRSP(sopClass string, respondingTo, status uint16, hasDataSet bool) []byte {
	// 0x0101 means no data set follows, which is what the final response uses; a pending response carrying a match says
	// one does.
	dataSetType := uint16(0x0101)
	if hasDataSet {
		dataSetType = 0x0000
	}

	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCFindRSP)},
		{Tag: tagMessageIDRespondingTo, Value: uint16Value(respondingTo)},
		{Tag: tagStatus, Value: uint16Value(status)},
		{Tag: tagCommandDataSetType, Value: uint16Value(dataSetType)},
	})
}
