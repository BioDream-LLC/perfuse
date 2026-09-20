package dicom

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// RetrieveRequest asks an archive to send objects back.
type RetrieveRequest struct {
	// Level is the level being retrieved: STUDY, SERIES or IMAGE.
	Level QueryLevel

	// Match identifies what to retrieve - normally a study or series UID.
	//
	// Unlike a query, empty keys are pointless here: a retrieve names what it wants rather than describing it. A key with
	// no value is silently useless rather than an error at the archive, so it is refused before sending.
	Match []Element

	// PatientRoot uses the patient root information model.
	PatientRoot bool

	// SOPClasses are the storage classes to accept. Empty accepts a default set.
	//
	// This is the awkward part of C-GET and the reason C-MOVE persists despite being harder to operate. The images come
	// back on the same association, so every kind of object that might arrive has to be negotiated up front - and an
	// archive holding an object whose class was not offered cannot send it. C-MOVE avoids the problem by sending to a
	// separate listener that accepts everything.
	SOPClasses []string

	// MaxObjects stops after this many objects. Zero means no limit.
	MaxObjects int
}

// RetrievedObject is one object the archive sent back.
type RetrievedObject struct {
	// Raw is a complete file representation, with a meta group declaring the transfer syntax it arrived in.
	//
	// Wrapped for the same reason a stored object is: what arrives on the wire is not self-describing, and anything
	// holding it afterwards would have to be told its encoding separately.
	Raw []byte

	// DataSet is the parsed object.
	DataSet *DataSet

	// SOPInstanceUID identifies it.
	SOPInstanceUID string
}

// RetrieveResult reports what a retrieve transferred.
//
// The counts come from the archive rather than from what we received, which is what makes them worth reporting: a
// difference between Completed and the number of objects handed to the caller means the archive believes it sent something
// that did not arrive.
type RetrieveResult struct {
	Completed int
	Failed    int
	Warning   int
	Remaining int
}

// ErrRetrieveRefused reports that the archive refused the retrieve.
var ErrRetrieveRefused = errors.New("dicom: the retrieve was refused")

// DefaultRetrieveSOPClasses are the storage classes offered when none are configured.
//
// A pragmatic list rather than the whole registry. Every class offered costs a presentation context, associations have a
// practical limit on how many they carry, and an archive that holds nothing but CT and MR gains nothing from us offering to
// receive structured reports.
//
// The consequence of the list being wrong is specific and worth stating: an object whose class is absent cannot be sent, and
// a conforming archive reports that as a failed sub-operation rather than as an error - so it shows up as a retrieve that
// mostly worked.
var DefaultRetrieveSOPClasses = []string{
	"1.2.840.10008.5.1.4.1.1.1",     // Computed Radiography Image Storage
	"1.2.840.10008.5.1.4.1.1.1.1",   // Digital X-Ray Image Storage - For Presentation
	"1.2.840.10008.5.1.4.1.1.2",     // CT Image Storage
	"1.2.840.10008.5.1.4.1.1.4",     // MR Image Storage
	"1.2.840.10008.5.1.4.1.1.6.1",   // Ultrasound Image Storage
	"1.2.840.10008.5.1.4.1.1.7",     // Secondary Capture Image Storage
	"1.2.840.10008.5.1.4.1.1.12.1",  // X-Ray Angiographic Image Storage
	"1.2.840.10008.5.1.4.1.1.20",    // Nuclear Medicine Image Storage
	"1.2.840.10008.5.1.4.1.1.88.11", // Basic Text SR Storage
	"1.2.840.10008.5.1.4.1.1.128",   // Positron Emission Tomography Image Storage
	"1.2.840.10008.5.1.4.1.1.481.1", // RT Image Storage
}

// Get retrieves objects with C-GET, calling fn for each one.
//
// C-GET rather than C-MOVE, and that choice is the point. A C-MOVE tells the archive to send the images somewhere else,
// which means running a listener to receive them and having the archive configured in advance with our AE title, address and
// port. Every one of those is a thing a site gets wrong once and blames on us. C-GET brings the images back down the
// association we already opened, so there is nothing to configure at the far end beyond permission to query.
//
// Mirth has neither. Its DICOM support cannot ask an archive for anything.
func (c *Client) Get(ctx context.Context, req RetrieveRequest, fn func(RetrievedObject) error) (RetrieveResult, error) {
	var result RetrieveResult

	if !req.Level.Valid() {
		return result, fmt.Errorf("dicom: %q is not a retrieve level", req.Level)
	}
	if req.Level == LevelPatient && !req.PatientRoot {
		return result, errors.New("dicom: a PATIENT level retrieve needs the patient root information model")
	}
	if len(req.Match) == 0 {
		return result, errors.New("dicom: a retrieve needs at least one key saying what to retrieve")
	}
	for _, e := range req.Match {
		if len(e.Value) == 0 {
			// Refused rather than sent. A retrieve names what it wants; an empty key describes nothing, and an archive
			// given one either ignores it or matches everything, neither of which is what the caller meant.
			return result, fmt.Errorf("dicom: the retrieve key %s has no value; a retrieve has to say which object it "+
				"wants rather than asking for the field back", e.Tag)
		}
	}

	getClass := StudyRootQueryGET
	if req.PatientRoot {
		getClass = PatientRootQueryGET
	}

	storageClasses := req.SOPClasses
	if len(storageClasses) == 0 {
		storageClasses = DefaultRetrieveSOPClasses
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = conn.Close() }()

	// The retrieve context, then one per storage class we are willing to receive. Odd numbers only: presentation context
	// identifiers must be odd, and an even one is a protocol violation some archives reject the whole association over.
	contexts := []PresentationContext{{
		ID:               1,
		AbstractSyntax:   getClass,
		TransferSyntaxes: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
	}}
	roles := make([]RoleSelection, 0, len(storageClasses))

	id := byte(3)
	for _, class := range storageClasses {
		if id > 250 {
			// A hard limit rather than a silent truncation. Presentation context identifiers are a single byte, so a
			// caller offering too many classes has to be told rather than quietly given fewer than it asked for.
			return result, fmt.Errorf("dicom: %d storage classes is more than one association can carry",
				len(storageClasses))
		}
		contexts = append(contexts, PresentationContext{
			ID:               id,
			AbstractSyntax:   class,
			TransferSyntaxes: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
		})
		// SCU false, SCP true: for these classes the archive initiates and we receive. This is the whole reason role
		// selection exists, and without it an archive has no way to know we would accept a store.
		roles = append(roles, RoleSelection{SOPClass: class, SCPRole: true})
		id += 2
	}

	accepted, acceptedRoles, maxLength, err := c.associateWithRoles(conn, contexts, roles)
	if err != nil {
		return result, err
	}

	getCtx, ok := contextFor(accepted, 1)
	if !ok {
		return result, fmt.Errorf("%w: the remote accepted the association but refused the C-GET context, so it does "+
			"not support retrieval under the %s information model", ErrRetrieveRefused, modelName(req.PatientRoot))
	}

	// Checked before sending anything. An archive that accepted the retrieve context but agreed to no storage role will
	// answer the C-GET and send no images, which presents as an empty study rather than as a refusal.
	agreed := 0
	for _, class := range storageClasses {
		if AcceptedSCPRole(acceptedRoles, class) {
			agreed++
		}
	}
	if agreed == 0 {
		return result, fmt.Errorf("%w: the archive would not agree to send objects back on this association, so a "+
			"C-GET would transfer nothing; the archive may only support C-MOVE, which needs a listener of our own",
			ErrRetrieveRefused)
	}

	syntax := orDefault(getCtx.Accepted, ImplicitVRLittleEndian)

	identifier := append([]Element{
		{Tag: TagQueryRetrieveLevel, VR: "CS", Value: []byte(req.Level)},
	}, req.Match...)

	query, err := Encode(identifier, syntax)
	if err != nil {
		return result, err
	}

	const messageID = 1
	if err := writePDataPDUs(conn, getCtx.ID, getRQ(getClass, messageID), true, maxLength); err != nil {
		return result, err
	}
	if err := writePDataPDUs(conn, getCtx.ID, query, false, maxLength); err != nil {
		return result, err
	}

	return c.receiveRetrieved(conn, accepted, maxLength, messageID, req, fn)
}

// receiveRetrieved reads the interleaved stores and retrieve responses.
//
// This is the part that has no analogue anywhere else in the client. Every other exchange is a request followed by
// responses; here the archive sends C-STORE requests to us while also sending C-GET responses about its own progress, on
// the same association, and both have to be handled in whatever order they arrive.
func (c *Client) receiveRetrieved(conn net.Conn, accepted []PresentationContext, maxLength uint32, messageID uint16,
	req RetrieveRequest, fn func(RetrievedObject) error) (RetrieveResult, error) {

	var result RetrieveResult
	received := 0

	for {
		msg, err := readIncoming(conn, maxLength)
		if err != nil {
			return result, err
		}

		switch msg.command.Field {
		case cmdCStoreRQ:
			// An object. Acknowledged individually, because the archive waits for each response before sending the next
			// and a missing one stalls the transfer rather than failing it.
			object, storeErr := buildRetrievedObject(msg, accepted)

			status := uint16(0x0000)
			if storeErr != nil {
				// 0xA700 is out of resources, which is the status a conforming archive counts as a failed sub-operation
				// and reports in its final response. Reporting success and discarding the object would make the archive
				// believe the study was delivered.
				status = 0xA700
			} else if fn != nil {
				if err := fn(object); err != nil {
					status = 0xA700
					storeErr = err
				}
			}

			if err := writePDataPDUs(conn, msg.contextID,
				cStoreResponse(msg.command.MessageID, msg.command.SOPClass, msg.command.SOPInstance, status),
				true, maxLength); err != nil {
				return result, err
			}

			if storeErr != nil {
				return result, fmt.Errorf("dicom: an object could not be accepted: %w", storeErr)
			}

			received++
			if req.MaxObjects > 0 && received >= req.MaxObjects {
				if err := c.cancel(conn, msg.contextID, messageID, maxLength); err != nil {
					return result, err
				}
				return result, nil
			}

		case cmdCGetRSP:
			result.Completed = int(msg.command.Completed)
			result.Failed = int(msg.command.Failed)
			result.Warning = int(msg.command.Warning)
			result.Remaining = int(msg.command.Remaining)

			if IsPending(msg.command.Status) {
				continue
			}

			if msg.command.Status == statusCancelled {
				return result, nil
			}
			if !IsSuccess(msg.command.Status) {
				// Reported with the counts, because a partial failure is the common case and "three of forty images
				// failed" is a different problem from "the retrieve was refused".
				return result, fmt.Errorf("%w: the archive answered %s after %d object(s), with %d failed",
					ErrRetrieveRefused, StatusText(msg.command.Status), received, result.Failed)
			}

			if result.Failed > 0 {
				// Success with failures is a real DICOM outcome and the most misleading one. Surfaced as an error,
				// because a caller told a retrieve succeeded will not go looking for the images that are missing.
				return result, fmt.Errorf("dicom: the archive completed the retrieve but %d object(s) failed to "+
					"transfer, so the study is incomplete", result.Failed)
			}

			return result, nil

		case cmdCStoreRSP, cmdCEchoRSP:
			// Not expected mid-retrieve, and harmless. Ignored rather than treated as an error, since aborting would
			// discard objects already received over a message that changes nothing.
			continue

		default:
			return result, fmt.Errorf("dicom: unexpected command 0x%04X during a retrieve", msg.command.Field)
		}
	}
}

// buildRetrievedObject turns an incoming store into a self-describing object.
func buildRetrievedObject(msg incoming, accepted []PresentationContext) (RetrievedObject, error) {
	syntax := ImplicitVRLittleEndian
	if ctx, ok := contextFor(accepted, msg.contextID); ok && ctx.Accepted != "" {
		syntax = ctx.Accepted
	}

	ds, err := ParseDataSet(msg.data, syntax)
	if err != nil {
		return RetrievedObject{}, err
	}

	return RetrievedObject{
		Raw:            Wrap(msg.data, syntax, msg.command.SOPClass, msg.command.SOPInstance),
		DataSet:        ds,
		SOPInstanceUID: msg.command.SOPInstance,
	}, nil
}

// contextFor finds an accepted presentation context by id.
func contextFor(contexts []PresentationContext, id byte) (PresentationContext, bool) {
	for _, ctx := range contexts {
		if ctx.ID == id && ctx.Result == pcAccepted {
			return ctx, true
		}
	}
	return PresentationContext{}, false
}

// getRQ builds a C-GET-RQ command set.
func getRQ(sopClass string, messageID uint16) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCGetRQ)},
		{Tag: tagMessageID, Value: uint16Value(messageID)},
		{Tag: tagPriority, Value: uint16Value(0x0000)},
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0000)},
	})
}

// retrieveTimeout is how long to wait for the next message during a retrieve.
//
// Separate from the association timeout because a retrieve is long-lived: a study of six hundred images takes as long as it
// takes, and a timeout covering the whole operation would abort large studies while passing small ones. This bounds the gap
// between messages instead, which is what actually indicates a stalled archive.
const retrieveTimeout = 5 * time.Minute
