package dicom

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The DICOM upper layer protocol.
//
// DICOM over TCP is a small framing protocol carrying seven kinds of PDU. Everything is big endian, which is worth
// stating because the data sets inside are usually little endian - so a single connection has both byte orders in it and
// mixing them up produces lengths in the billions rather than an error.

// PDU types.
const (
	pduAssociateRequest byte = 0x01
	pduAssociateAccept  byte = 0x02
	pduAssociateReject  byte = 0x03
	pduData             byte = 0x04
	pduReleaseRequest   byte = 0x05
	pduReleaseResponse  byte = 0x06
	pduAbort            byte = 0x07
)

// Item types inside an association PDU.
const (
	itemApplicationContext byte = 0x10
	itemPresentationCtxRQ  byte = 0x20
	itemPresentationCtxAC  byte = 0x21
	itemAbstractSyntax     byte = 0x30
	itemTransferSyntax     byte = 0x40
	itemUserInformation    byte = 0x50
	itemMaxLength          byte = 0x51
	itemImplementationUID  byte = 0x52
	itemImplementationName byte = 0x55
)

// ApplicationContextUID is the only application context in practice.
const ApplicationContextUID = "1.2.840.10008.3.1.1.1"

// VerificationSOPClass is C-ECHO, which is how every site tests a DICOM connection before trying anything else.
const VerificationSOPClass = "1.2.840.10008.1.1"

// Presentation context results.
const (
	pcAccepted                 byte = 0
	pcRejectedUser             byte = 1
	pcRejectedNoReason         byte = 2
	pcRejectedAbstractSyntax   byte = 3
	pcRejectedTransferSyntaxes byte = 4
)

// ErrAssociationRejected means the remote refused the association.
var ErrAssociationRejected = errors.New("dicom: the remote rejected the association")

// ErrAborted means the association was aborted.
var ErrAborted = errors.New("dicom: the association was aborted")

// maxPDULength is what this implementation advertises it can receive.
//
// 128 KB rather than something larger. The remote fragments to whatever we advertise, and a smaller value means more
// PDUs but bounded memory per connection - which matters more here, because a modality sending a large study should not
// be able to make the receiver allocate the whole image at once.
const maxPDULength = 128 * 1024

// pdu is one raw protocol data unit.
type pdu struct {
	Type byte
	Data []byte
}

// readPDU reads one PDU from a connection.
func readPDU(r io.Reader, limit uint32) (*pdu, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	// Byte 1 is reserved and must be ignored rather than checked. Some implementations put rubbish there, and refusing
	// them would mean refusing real equipment over a byte the standard says to skip.
	length := binary.BigEndian.Uint32(header[2:6])

	if limit > 0 && length > limit {
		// Refused before allocating. Without this, a peer declaring a four gigabyte PDU makes the receiver try to
		// allocate it, which is a denial of service costing the attacker six bytes.
		return nil, fmt.Errorf("dicom: the remote declared a %d byte PDU, above the %d byte limit this "+
			"connection agreed to", length, limit)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("dicom: a PDU header promised %d bytes and the connection ended early: %w",
			length, err)
	}

	return &pdu{Type: header[0], Data: data}, nil
}

// writePDU writes one PDU.
func writePDU(w io.Writer, kind byte, data []byte) error {
	header := make([]byte, 6)
	header[0] = kind
	binary.BigEndian.PutUint32(header[2:6], uint32(len(data)))

	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// PresentationContext is one offered or accepted pairing of a SOP class with transfer syntaxes.
//
// The negotiation is per context rather than per connection, which is the part people find surprising: a single
// association can accept a CT image in explicit VR and refuse the same image in JPEG, and the sender has to notice
// per-context rather than assuming the association succeeded means everything succeeded.
type PresentationContext struct {
	ID byte

	// AbstractSyntax is the SOP class UID - what kind of thing is being sent.
	AbstractSyntax string

	// TransferSyntaxes are the encodings offered, in order of preference.
	TransferSyntaxes []string

	// Result is filled in on an accepted context. Zero means accepted.
	Result byte

	// Accepted is the single transfer syntax the acceptor chose. Only one, always, because the acceptor picks.
	Accepted string
}

// ResultText explains a presentation context result.
//
// Spelled out because a rejected context is the commonest reason a DICOM transfer fails, and the numeric result on its
// own sends people to look up a table when the answer is usually "we do not both speak this encoding".
func (p PresentationContext) ResultText() string {
	switch p.Result {
	case pcAccepted:
		return "accepted"
	case pcRejectedUser:
		return "rejected by the remote application"
	case pcRejectedNoReason:
		return "rejected with no reason given"
	case pcRejectedAbstractSyntax:
		return "rejected because the remote does not handle this kind of object"
	case pcRejectedTransferSyntaxes:
		return "rejected because none of the offered transfer syntaxes are supported by the remote"
	}
	return fmt.Sprintf("rejected with result %d", p.Result)
}

// AssociateRequest is what an initiator sends to open an association.
type AssociateRequest struct {
	// CalledAE is who the initiator is trying to reach, and CallingAE who it says it is.
	//
	// Both are 16 characters, space padded. Sites routinely configure these to match exactly and reject anything
	// else, so getting the padding wrong looks like a configuration error at the far end rather than a bug here.
	CalledAE  string
	CallingAE string

	Contexts []PresentationContext

	// Roles propose who acts as user and provider per SOP class. Only C-GET needs them.
	Roles []RoleSelection

	MaxPDULength uint32

	ImplementationUID  string
	ImplementationName string
}

// encodeAssociateRequest builds an A-ASSOCIATE-RQ.
func encodeAssociateRequest(req AssociateRequest) []byte {
	var b []byte

	b = append(b, 0x00, 0x01) // protocol version 1
	b = append(b, 0x00, 0x00) // reserved
	b = append(b, padAE(req.CalledAE)...)
	b = append(b, padAE(req.CallingAE)...)
	b = append(b, make([]byte, 32)...) // reserved

	b = append(b, encodeItem(itemApplicationContext, []byte(ApplicationContextUID))...)

	for _, ctx := range req.Contexts {
		var inner []byte
		inner = append(inner, ctx.ID, 0x00, 0x00, 0x00)
		inner = append(inner, encodeItem(itemAbstractSyntax, []byte(ctx.AbstractSyntax))...)
		for _, ts := range ctx.TransferSyntaxes {
			inner = append(inner, encodeItem(itemTransferSyntax, []byte(ts))...)
		}
		b = append(b, encodeItem(itemPresentationCtxRQ, inner)...)
	}

	b = append(b, encodeUserInfo(req.MaxPDULength, req.ImplementationUID, req.ImplementationName, req.Roles...)...)

	return b
}

// encodeAssociateAccept builds an A-ASSOCIATE-AC.
func encodeAssociateAccept(req AssociateRequest, contexts []PresentationContext, implUID, implName string) []byte {
	var b []byte

	b = append(b, 0x00, 0x01)
	b = append(b, 0x00, 0x00)
	// The accept echoes the called and calling titles back. Some initiators check them, so echoing rather than
	// substituting our own is what keeps those working.
	b = append(b, padAE(req.CalledAE)...)
	b = append(b, padAE(req.CallingAE)...)
	b = append(b, make([]byte, 32)...)

	b = append(b, encodeItem(itemApplicationContext, []byte(ApplicationContextUID))...)

	for _, ctx := range contexts {
		var inner []byte
		inner = append(inner, ctx.ID, 0x00, ctx.Result, 0x00)
		if ctx.Result == pcAccepted {
			// Exactly one transfer syntax on an accepted context. Sending several is a protocol error that some
			// initiators tolerate and others abort on, which makes it the kind of bug that works in testing.
			inner = append(inner, encodeItem(itemTransferSyntax, []byte(ctx.Accepted))...)
		}
		b = append(b, encodeItem(itemPresentationCtxAC, inner)...)
	}

	b = append(b, encodeUserInfo(maxPDULength, implUID, implName)...)

	return b
}

func encodeUserInfo(maxLength uint32, implUID, implName string, roles ...RoleSelection) []byte {
	var inner []byte

	maxItem := make([]byte, 4)
	binary.BigEndian.PutUint32(maxItem, maxLength)
	inner = append(inner, encodeItem(itemMaxLength, maxItem)...)

	if implUID != "" {
		inner = append(inner, encodeItem(itemImplementationUID, []byte(implUID))...)
	}
	if implName != "" {
		// Sixteen characters maximum. Longer is a protocol violation that some peers reject the whole association
		// over, which would be an absurd way to fail.
		if len(implName) > 16 {
			implName = implName[:16]
		}
		inner = append(inner, encodeItem(itemImplementationName, []byte(implName))...)
	}

	// Role selection sits here rather than beside the presentation contexts, which is the part of this that reads oddly:
	// a context says what kind of object may cross the association, and this says who is allowed to initiate it.
	for _, r := range roles {
		inner = append(inner, encodeRoleSelection(r)...)
	}

	return encodeItem(itemUserInformation, inner)
}

func encodeItem(kind byte, data []byte) []byte {
	out := make([]byte, 4+len(data))
	out[0] = kind
	binary.BigEndian.PutUint16(out[2:4], uint16(len(data)))
	copy(out[4:], data)
	return out
}

// decodeAssociateRequest reads an A-ASSOCIATE-RQ.
func decodeAssociateRequest(data []byte) (*AssociateRequest, error) {
	if len(data) < 68 {
		return nil, errors.New("dicom: the association request is too short to contain its own header")
	}

	req := &AssociateRequest{
		CalledAE:     strings.TrimSpace(string(data[4:20])),
		CallingAE:    strings.TrimSpace(string(data[20:36])),
		MaxPDULength: maxPDULength,
	}

	items := data[68:]
	for len(items) >= 4 {
		kind := items[0]
		length := int(binary.BigEndian.Uint16(items[2:4]))
		if 4+length > len(items) {
			return nil, errors.New("dicom: an item in the association request runs past the end of the PDU")
		}
		body := items[4 : 4+length]

		switch kind {
		case itemPresentationCtxRQ:
			ctx, err := decodePresentationContextRQ(body)
			if err != nil {
				return nil, err
			}
			req.Contexts = append(req.Contexts, ctx)
		case itemUserInformation:
			decodeUserInfo(body, req)
		}

		items = items[4+length:]
	}

	if len(req.Contexts) == 0 {
		// An association offering nothing cannot be usefully accepted, and saying so is more helpful than accepting
		// it and failing on the first message.
		return nil, errors.New("dicom: the association request offers no presentation contexts, so there is " +
			"nothing that could be sent over it")
	}

	return req, nil
}

func decodePresentationContextRQ(body []byte) (PresentationContext, error) {
	if len(body) < 4 {
		return PresentationContext{}, errors.New("dicom: a presentation context is too short")
	}

	ctx := PresentationContext{ID: body[0]}

	items := body[4:]
	for len(items) >= 4 {
		kind := items[0]
		length := int(binary.BigEndian.Uint16(items[2:4]))
		if 4+length > len(items) {
			return PresentationContext{}, errors.New("dicom: a syntax item runs past the end of its context")
		}
		value := strings.TrimRight(string(items[4:4+length]), "\x00 ")

		switch kind {
		case itemAbstractSyntax:
			ctx.AbstractSyntax = value
		case itemTransferSyntax:
			ctx.TransferSyntaxes = append(ctx.TransferSyntaxes, value)
		}

		items = items[4+length:]
	}

	return ctx, nil
}

func decodeUserInfo(body []byte, req *AssociateRequest) {
	items := body
	for len(items) >= 4 {
		kind := items[0]
		length := int(binary.BigEndian.Uint16(items[2:4]))
		if 4+length > len(items) {
			return
		}
		value := items[4 : 4+length]

		switch kind {
		case itemMaxLength:
			if len(value) >= 4 {
				declared := binary.BigEndian.Uint32(value)
				// Zero means unlimited in the standard. Treated as our own maximum instead, because "unlimited"
				// from a stranger is an invitation to allocate whatever they say.
				if declared > 0 {
					req.MaxPDULength = declared
				}
			}
		case itemImplementationUID:
			req.ImplementationUID = strings.TrimRight(string(value), "\x00 ")
		case itemImplementationName:
			req.ImplementationName = strings.TrimRight(string(value), "\x00 ")
		case itemRoleSelection:
			// Decoded on the requesting side too, so a C-GET initiator's proposal is visible to us. Needed the moment
			// Perfuse answers a C-GET rather than only making one, and useful before that as the only way to verify the
			// decoder against another implementation's encoder.
			if role, ok := decodeRoleSelection(value); ok {
				req.Roles = append(req.Roles, role)
			}
		}

		items = items[4+length:]
	}
}

// decodeAssociateAccept reads an A-ASSOCIATE-AC and returns the accepted contexts and roles.
//
// The roles matter only for C-GET, and only negatively: an acceptor that did not agree to us being the provider will not send
// images, so a retrieve that would silently transfer nothing can be reported before it is attempted.
func decodeAssociateAccept(data []byte) ([]PresentationContext, []RoleSelection, uint32, error) {
	if len(data) < 68 {
		return nil, nil, 0, errors.New("dicom: the association accept is too short")
	}

	var contexts []PresentationContext
	var roles []RoleSelection
	maxLength := uint32(maxPDULength)

	items := data[68:]
	for len(items) >= 4 {
		kind := items[0]
		length := int(binary.BigEndian.Uint16(items[2:4]))
		if 4+length > len(items) {
			break
		}
		body := items[4 : 4+length]

		switch kind {
		case itemPresentationCtxAC:
			if len(body) < 4 {
				break
			}
			ctx := PresentationContext{ID: body[0], Result: body[2]}
			inner := body[4:]
			for len(inner) >= 4 {
				innerKind := inner[0]
				innerLength := int(binary.BigEndian.Uint16(inner[2:4]))
				if 4+innerLength > len(inner) {
					break
				}
				if innerKind == itemTransferSyntax {
					ctx.Accepted = strings.TrimRight(string(inner[4:4+innerLength]), "\x00 ")
				}
				inner = inner[4+innerLength:]
			}
			contexts = append(contexts, ctx)

		case itemUserInformation:
			inner := body
			for len(inner) >= 4 {
				innerKind := inner[0]
				innerLength := int(binary.BigEndian.Uint16(inner[2:4]))
				if 4+innerLength > len(inner) {
					break
				}
				switch {
				case innerKind == itemMaxLength && innerLength >= 4:
					if declared := binary.BigEndian.Uint32(inner[4:8]); declared > 0 {
						maxLength = declared
					}
				case innerKind == itemRoleSelection:
					if role, ok := decodeRoleSelection(inner[4 : 4+innerLength]); ok {
						roles = append(roles, role)
					}
				}
				inner = inner[4+innerLength:]
			}
		}

		items = items[4+length:]
	}

	return contexts, roles, maxLength, nil
}

// AcceptedSCPRole reports whether the acceptor agreed we may act as provider for a SOP class.
//
// Absence counts as refusal. An acceptor that says nothing about a role has not agreed to it, and reading silence as consent
// is how a C-GET ends up waiting for images that were never going to arrive.
func AcceptedSCPRole(roles []RoleSelection, sopClass string) bool {
	for _, r := range roles {
		if r.SOPClass == sopClass {
			return r.SCPRole
		}
	}
	return false
}

// rejectReason explains an A-ASSOCIATE-RJ.
//
// Decoded rather than reported numerically because association rejection is where DICOM connections fail most often, and
// the reasons are genuinely actionable - "called AE title not recognised" tells somebody exactly which field to fix.
func rejectReason(data []byte) string {
	if len(data) < 4 {
		return "rejected, with no reason given"
	}

	source := data[2]
	reason := data[3]

	switch source {
	case 1:
		switch reason {
		case 1:
			return "rejected: no reason given by the remote application"
		case 2:
			return "rejected: the remote does not support this application context"
		case 3:
			return "rejected: the calling AE title was not recognised - check what this end calls itself"
		case 7:
			return "rejected: the called AE title was not recognised - check the name configured for the remote"
		}
	case 2:
		return "rejected by the remote's protocol layer, usually a version mismatch"
	case 3:
		if reason == 1 {
			return "rejected: the remote is temporarily congested"
		}
		return "rejected: the remote has reached its limit of simultaneous associations"
	}

	return fmt.Sprintf("rejected by source %d for reason %d", source, reason)
}

// padAE pads an application entity title to the 16 characters the protocol requires.
//
// Sites routinely configure these to match exactly and reject anything else, so getting the padding wrong presents as a
// configuration problem at the far end rather than as a bug here - which is the sort of thing that costs a day.
func padAE(title string) []byte {
	out := []byte(strings.Repeat(" ", 16))
	if len(title) > 16 {
		title = title[:16]
	}
	copy(out, title)
	return out
}
