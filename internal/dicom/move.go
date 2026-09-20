package dicom

import (
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// C-MOVE — retrieve via a named destination
// ──────────────────────────────────────────────────────────────────────────────

// MoveRequest asks an archive to send objects to a separate C-STORE SCP.
//
// C-MOVE is the older retrieve mechanism and still extremely common in hospital PACS.
// Unlike C-GET, the archive opens a new association to a named AE title to deliver images.
// The caller receives only status updates, not the actual pixel data.
type MoveRequest struct {
	// Level is the level being retrieved: STUDY, SERIES or IMAGE.
	Level QueryLevel

	// Match identifies what to retrieve — normally a study or series UID.
	Match []Element

	// MoveDestination is the AE title of the C-STORE SCP that should receive images.
	// The archive must have this AE preconfigured with an address and port.
	MoveDestination string

	// PatientRoot uses the patient root information model.
	PatientRoot bool
}

// MoveResult reports what the archive said about the move.
type MoveResult struct {
	Completed int
	Remaining int
	Failed    int
	Warnings  int
}

// MoveStatus is called for each progress update. Return non-nil to cancel.
type MoveStatus func(completed, remaining, failed int) error

// TagMoveDestination is (0000,0600).
var TagMoveDestination = Tag{0x0000, 0x0600}

// Tags for sub-operation counts in C-MOVE-RSP and C-GET-RSP.
var (
	TagNumberOfCompletedSuboperations = Tag{0x0000, 0x1021}
	TagNumberOfRemainingSuboperations = Tag{0x0000, 0x1020}
	TagNumberOfFailedSuboperations    = Tag{0x0000, 0x1022}
	TagNumberOfWarningSuboperations   = Tag{0x0000, 0x1023}
)

// ──────────────────────────────────────────────────────────────────────────────
// Modality Worklist (MWL)
// ──────────────────────────────────────────────────────────────────────────────

// ModalityWorklistSOPClass is the well-known UID for Modality Worklist C-FIND.
const ModalityWorklistSOPClass = "1.2.840.10008.5.1.4.31"

// WorklistRequest queries a Modality Worklist for scheduled procedures.
//
// Modality Worklist tells imaging modalities which patient is being scanned and what
// study to create. Without it, technologists type patient details manually — which means
// typos, swapped MRNs, and studies filed under the wrong patient.
type WorklistRequest struct {
	// Match keys. Common ones:
	//   ScheduledProcedureStepSequence > ScheduledStationAETitle
	//   ScheduledProcedureStepSequence > ScheduledProcedureStepStartDate
	//   PatientName, PatientID
	Match []Element

	// Limit stops after this many matches. Zero means no limit.
	Limit int
}

// WorklistItem is one scheduled procedure returned by the worklist query.
type WorklistItem struct {
	Elements    []Element
	PatientName string
	PatientID   string
	AccessionNo string
	StudyUID    string
	Modality    string
	StationAE   string
	StartDate   string
	StartTime   string
}

// Tags for worklist elements.
var (
	TagScheduledProcedureStepSequence  = Tag{0x0040, 0x0100}
	TagScheduledStationAETitle         = Tag{0x0040, 0x0001}
	TagScheduledProcedureStepStartDate = Tag{0x0040, 0x0002}
	TagScheduledProcedureStepStartTime = Tag{0x0040, 0x0003}
)

// ──────────────────────────────────────────────────────────────────────────────
// MPPS — Modality Performed Procedure Step
// ──────────────────────────────────────────────────────────────────────────────

// MPPSSOPClass is the SOP class for Modality Performed Procedure Step.
const MPPSSOPClass = "1.2.840.10008.3.1.2.3.3"

// MPPSStatus represents the state of a performed procedure step.
type MPPSStatus string

const (
	MPPSInProgress   MPPSStatus = "IN PROGRESS"
	MPPSCompleted    MPPSStatus = "COMPLETED"
	MPPSDiscontinued MPPSStatus = "DISCONTINUED"
)

// MPPSCreate represents an N-CREATE for a new performed procedure step.
type MPPSCreate struct {
	InstanceUID string
	Status      MPPSStatus
	Attributes  []Element
}

// MPPSSet represents an N-SET to update a performed procedure step.
type MPPSSet struct {
	InstanceUID string
	Status      MPPSStatus
	Attributes  []Element
}

// ──────────────────────────────────────────────────────────────────────────────
// Storage Commitment
// ──────────────────────────────────────────────────────────────────────────────

// StorageCommitmentSOPClass is the well-known UID.
const StorageCommitmentSOPClass = "1.2.840.10008.1.20.1"

// CommitRequest asks an archive to confirm safe storage of objects.
type CommitRequest struct {
	TransactionUID string
	Objects        []CommitObject
}

// CommitObject identifies one DICOM object for commitment.
type CommitObject struct {
	SOPClassUID    string
	SOPInstanceUID string
}

// CommitResult reports the archive's commitment response.
type CommitResult struct {
	TransactionUID string
	Success        []CommitObject
	Failed         []CommitFailure
}

// CommitFailure is one object the archive could not commit.
type CommitFailure struct {
	SOPClassUID    string
	SOPInstanceUID string
	FailureReason  uint16
}

// ──────────────────────────────────────────────────────────────────────────────
// DICOMweb (WADO-RS, STOW-RS, QIDO-RS)
// ──────────────────────────────────────────────────────────────────────────────

// DICOMwebClient provides HTTP-based access to a DICOMweb server.
//
// DICOMweb is the modern HTTP-based alternative to traditional DIMSE:
//   - QIDO-RS: Query (replaces C-FIND)
//   - WADO-RS: Retrieve (replaces C-GET/C-MOVE)
//   - STOW-RS: Store (replaces C-STORE)
type DICOMwebClient struct {
	// BaseURL is the DICOMweb service root, e.g. "https://pacs.hospital.org/dicomweb".
	BaseURL string

	// BearerToken for OAuth2 authentication.
	BearerToken string

	// Timeout bounds HTTP operations.
	Timeout time.Duration

	mu sync.Mutex
}

// QIDOQuery searches for studies, series, or instances via QIDO-RS.
type QIDOQuery struct {
	// Level: "studies", "series", or "instances".
	Level string
	// Params are DICOM keyword=value search parameters.
	Params map[string]string
	// Limit bounds the results. Zero means server default.
	Limit int
	// Offset for pagination.
	Offset int
	// StudyUID narrows to a specific study.
	StudyUID string
	// SeriesUID narrows to a specific series.
	SeriesUID string
}

// QIDOResult is one matching study/series/instance.
type QIDOResult struct {
	Elements map[string]interface{}
}

// WADORequest retrieves DICOM objects via WADO-RS.
type WADORequest struct {
	StudyUID    string
	SeriesUID   string // optional
	InstanceUID string // optional
	Frames      []int  // optional: specific frames
	// AcceptType: "multipart/related" (default) or "application/dicom+json" for metadata.
	AcceptType string
}

// STOWRequest stores DICOM objects via STOW-RS.
type STOWRequest struct {
	StudyUID string // target study; empty means server-assigned
	Objects  [][]byte
}

// STOWResult reports the outcome.
type STOWResult struct {
	Stored     int
	Failed     int
	FailedUIDs []string
}

// ──────────────────────────────────────────────────────────────────────────────
// Expanded SOP Storage Classes
// ──────────────────────────────────────────────────────────────────────────────

// CommonStorageClasses lists the SOP classes a comprehensive integration engine should
// accept. This is a superset of the default C-GET list, covering what modern imaging
// equipment actually produces.
var CommonStorageClasses = []string{
	// Basic imaging
	"1.2.840.10008.5.1.4.1.1.1",     // Computed Radiography
	"1.2.840.10008.5.1.4.1.1.1.1",   // Digital X-Ray - Presentation
	"1.2.840.10008.5.1.4.1.1.1.1.1", // Digital X-Ray - Processing
	"1.2.840.10008.5.1.4.1.1.1.2",   // Digital Mammography - Presentation
	"1.2.840.10008.5.1.4.1.1.1.2.1", // Digital Mammography - Processing
	"1.2.840.10008.5.1.4.1.1.2",     // CT Image
	"1.2.840.10008.5.1.4.1.1.2.1",   // Enhanced CT Image
	"1.2.840.10008.5.1.4.1.1.2.2",   // Legacy Converted Enhanced CT
	"1.2.840.10008.5.1.4.1.1.3.1",   // Ultrasound Multi-frame
	"1.2.840.10008.5.1.4.1.1.4",     // MR Image
	"1.2.840.10008.5.1.4.1.1.4.1",   // Enhanced MR Image
	"1.2.840.10008.5.1.4.1.1.4.2",   // MR Spectroscopy
	"1.2.840.10008.5.1.4.1.1.4.3",   // Enhanced MR Color Image
	"1.2.840.10008.5.1.4.1.1.4.4",   // Legacy Converted Enhanced MR
	"1.2.840.10008.5.1.4.1.1.6.1",   // Ultrasound Image
	"1.2.840.10008.5.1.4.1.1.6.2",   // Enhanced US Volume
	"1.2.840.10008.5.1.4.1.1.7",     // Secondary Capture
	"1.2.840.10008.5.1.4.1.1.7.1",   // Multi-frame Single Bit SC
	"1.2.840.10008.5.1.4.1.1.7.2",   // Multi-frame Grayscale Byte SC
	"1.2.840.10008.5.1.4.1.1.7.3",   // Multi-frame Grayscale Word SC
	"1.2.840.10008.5.1.4.1.1.7.4",   // Multi-frame True Color SC

	// Nuclear medicine and PET
	"1.2.840.10008.5.1.4.1.1.20",    // Nuclear Medicine
	"1.2.840.10008.5.1.4.1.1.128",   // PET Image
	"1.2.840.10008.5.1.4.1.1.130",   // Enhanced PET Image
	"1.2.840.10008.5.1.4.1.1.128.1", // Legacy Converted Enhanced PET

	// Angiography and fluoroscopy
	"1.2.840.10008.5.1.4.1.1.12.1",   // X-Ray Angiographic Image
	"1.2.840.10008.5.1.4.1.1.12.1.1", // Enhanced XA Image
	"1.2.840.10008.5.1.4.1.1.12.2",   // X-Ray Radiofluoroscopic Image
	"1.2.840.10008.5.1.4.1.1.12.2.1", // Enhanced XRF Image

	// Radiation therapy
	"1.2.840.10008.5.1.4.1.1.481.1", // RT Image
	"1.2.840.10008.5.1.4.1.1.481.2", // RT Dose
	"1.2.840.10008.5.1.4.1.1.481.3", // RT Structure Set
	"1.2.840.10008.5.1.4.1.1.481.4", // RT Beams Treatment Record
	"1.2.840.10008.5.1.4.1.1.481.5", // RT Plan

	// Structured reports
	"1.2.840.10008.5.1.4.1.1.88.11", // Basic Text SR
	"1.2.840.10008.5.1.4.1.1.88.22", // Enhanced SR
	"1.2.840.10008.5.1.4.1.1.88.33", // Comprehensive SR
	"1.2.840.10008.5.1.4.1.1.88.34", // Comprehensive 3D SR
	"1.2.840.10008.5.1.4.1.1.88.35", // Extensible SR
	"1.2.840.10008.5.1.4.1.1.88.40", // Procedure Log
	"1.2.840.10008.5.1.4.1.1.88.50", // Mammography CAD SR
	"1.2.840.10008.5.1.4.1.1.88.65", // Chest CAD SR
	"1.2.840.10008.5.1.4.1.1.88.67", // X-Ray Radiation Dose SR
	"1.2.840.10008.5.1.4.1.1.88.68", // Radiopharmaceutical Radiation Dose SR
	"1.2.840.10008.5.1.4.1.1.88.69", // Colon CAD SR
	"1.2.840.10008.5.1.4.1.1.88.71", // Acquisition Context SR
	"1.2.840.10008.5.1.4.1.1.88.73", // Patient Radiation Dose SR
	"1.2.840.10008.5.1.4.1.1.88.76", // Enhanced X-Ray Radiation Dose SR

	// Presentation states
	"1.2.840.10008.5.1.4.1.1.11.1", // Grayscale Softcopy Presentation State
	"1.2.840.10008.5.1.4.1.1.11.2", // Color Softcopy Presentation State

	// Whole slide / pathology
	"1.2.840.10008.5.1.4.1.1.77.1.6", // VL Whole Slide Microscopy Image

	// Key objects
	"1.2.840.10008.5.1.4.1.1.88.59", // Key Object Selection Document

	// Encapsulated documents
	"1.2.840.10008.5.1.4.1.1.104.1", // Encapsulated PDF
	"1.2.840.10008.5.1.4.1.1.104.2", // Encapsulated CDA

	// Segmentation
	"1.2.840.10008.5.1.4.1.1.66.4", // Segmentation
	"1.2.840.10008.5.1.4.1.1.30",   // Parametric Map

	// Ophthalmology
	"1.2.840.10008.5.1.4.1.1.77.1.5.1", // Ophthalmic Photography 8 Bit
	"1.2.840.10008.5.1.4.1.1.77.1.5.4", // Ophthalmic Tomography Image

	// Video
	"1.2.840.10008.5.1.4.1.1.77.1.1.1", // Video Endoscopic Image
	"1.2.840.10008.5.1.4.1.1.77.1.4.1", // Video Photographic Image
}
