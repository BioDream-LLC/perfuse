package dicom

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DIMSE, the message service DICOM operations are expressed in.
//
// A DIMSE message is a command set - always encoded implicit VR little endian, whatever the association negotiated for
// data - optionally followed by a data set encoded with the negotiated syntax. Both travel inside P-DATA PDUs as
// presentation data values, each tagged with a presentation context and two flag bits saying whether it is command or
// data and whether it is the last fragment.
//
// The command set being a fixed encoding while the data set is negotiated is the detail that trips people up: one PDU
// can contain both, in two different encodings.

// Command field values.
const (
	cmdCStoreRQ  uint16 = 0x0001
	cmdCStoreRSP uint16 = 0x8001
	cmdCEchoRQ   uint16 = 0x0030
	cmdCEchoRSP  uint16 = 0x8030
)

// Command elements.
var (
	tagCommandGroupLength     = Tag{0x0000, 0x0000}
	tagAffectedSOPClassUID    = Tag{0x0000, 0x0002}
	tagCommandField           = Tag{0x0000, 0x0100}
	tagMessageID              = Tag{0x0000, 0x0110}
	tagMessageIDRespondingTo  = Tag{0x0000, 0x0120}
	tagPriority               = Tag{0x0000, 0x0700}
	tagCommandDataSetType     = Tag{0x0000, 0x0800}
	tagStatus                 = Tag{0x0000, 0x0900}
	tagAffectedSOPInstanceUID = Tag{0x0000, 0x1000}
)

// DIMSE statuses. Only the ones an SCP actually answers with.
const (
	// StatusSuccess means stored.
	StatusSuccess uint16 = 0x0000
	// StatusRefusedOutOfResources means the receiver could not store it. A failure the sender should retry later.
	StatusRefusedOutOfResources uint16 = 0xA700
	// StatusErrorCannotUnderstand means the data set could not be read. Retrying will not help.
	StatusErrorCannotUnderstand uint16 = 0xC000
	// StatusErrorDataSetDoesNotMatchSOPClass means the object is not what its class says it is.
	StatusErrorDataSetDoesNotMatchSOPClass uint16 = 0xA900
)

// StatusText explains a DIMSE status.
//
// The distinction that matters to a sender is retryable versus not: out of resources means try again later, cannot
// understand means never. A sender that treats them alike either gives up on a transient problem or retries a corrupt
// object forever.
func StatusText(status uint16) string {
	switch {
	case status == StatusSuccess:
		return "stored"
	case status == StatusRefusedOutOfResources:
		return "refused: the receiver is out of resources; this is worth retrying later"
	case status == StatusErrorCannotUnderstand:
		return "error: the receiver could not read the data set; retrying will not help"
	case status == StatusErrorDataSetDoesNotMatchSOPClass:
		return "error: the data set does not match the SOP class it was sent as"
	case status&0xF000 == 0xB000:
		return fmt.Sprintf("warning 0x%04X: stored, with something the receiver wanted to mention", status)
	case status&0xF000 == 0xA000, status&0xF000 == 0xC000:
		return fmt.Sprintf("error 0x%04X", status)
	}
	return fmt.Sprintf("status 0x%04X", status)
}

// IsSuccess reports whether a status means the object was stored.
//
// Warning statuses count as stored, because they are: the receiver kept the object and noted something about it. Treating
// a warning as a failure makes a sender resend objects the receiver already has.
func IsSuccess(status uint16) bool {
	return status == StatusSuccess || status&0xF000 == 0xB000
}

// commandSet builds an implicit VR little endian command set with a correct group length.
//
// The group length has to be the byte count of everything after it, and getting it wrong is the classic DIMSE bug: some
// peers ignore it and work, others read exactly that many bytes and truncate the command. So it is computed rather than
// tracked, from the encoded bytes themselves.
func commandSet(elements []Element) []byte {
	var body bytes.Buffer
	for _, e := range elements {
		writeImplicitElement(&body, e)
	}

	var out bytes.Buffer
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(body.Len()))
	writeImplicitElement(&out, Element{Tag: tagCommandGroupLength, Value: length})
	out.Write(body.Bytes())

	return out.Bytes()
}

func writeImplicitElement(w io.Writer, e Element) {
	value := e.Value
	if len(value)%2 == 1 {
		// Every DICOM value is an even number of bytes. UIDs pad with a null, everything else with a space; the
		// command set is all UIDs and numbers, so a null is right here.
		value = append(append([]byte{}, value...), 0x00)
	}

	header := make([]byte, 8)
	binary.LittleEndian.PutUint16(header[0:2], e.Tag.Group)
	binary.LittleEndian.PutUint16(header[2:4], e.Tag.Element)
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(value)))

	_, _ = w.Write(header)
	_, _ = w.Write(value)
}

func uint16Value(v uint16) []byte {
	out := make([]byte, 2)
	binary.LittleEndian.PutUint16(out, v)
	return out
}

// cStoreRequest builds a C-STORE-RQ command set.
func cStoreRequest(messageID uint16, sopClass, sopInstance string) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCStoreRQ)},
		{Tag: tagMessageID, Value: uint16Value(messageID)},
		// Medium priority. Low and high exist and essentially nothing honours them, so choosing anything else would be
		// implying a behaviour that does not happen.
		{Tag: tagPriority, Value: uint16Value(0x0000)},
		// 0x0000 here means "a data set follows". 0x0101 means none does, which is what a response uses.
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0000)},
		{Tag: tagAffectedSOPInstanceUID, Value: []byte(sopInstance)},
	})
}

// cStoreResponse builds a C-STORE-RSP command set.
func cStoreResponse(messageID uint16, sopClass, sopInstance string, status uint16) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCStoreRSP)},
		{Tag: tagMessageIDRespondingTo, Value: uint16Value(messageID)},
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0101)},
		{Tag: tagStatus, Value: uint16Value(status)},
		{Tag: tagAffectedSOPInstanceUID, Value: []byte(sopInstance)},
	})
}

// cEchoResponse builds a C-ECHO-RSP command set.
func cEchoResponse(messageID uint16, sopClass string, status uint16) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(sopClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCEchoRSP)},
		{Tag: tagMessageIDRespondingTo, Value: uint16Value(messageID)},
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0101)},
		{Tag: tagStatus, Value: uint16Value(status)},
	})
}

// cEchoRequest builds a C-ECHO-RQ command set.
func cEchoRequest(messageID uint16) []byte {
	return commandSet([]Element{
		{Tag: tagAffectedSOPClassUID, Value: []byte(VerificationSOPClass)},
		{Tag: tagCommandField, Value: uint16Value(cmdCEchoRQ)},
		{Tag: tagMessageID, Value: uint16Value(messageID)},
		{Tag: tagPriority, Value: uint16Value(0x0000)},
		{Tag: tagCommandDataSetType, Value: uint16Value(0x0101)},
	})
}

// command is a decoded DIMSE command set.
type command struct {
	Field       uint16
	MessageID   uint16
	SOPClass    string
	SOPInstance string
	Status      uint16
	HasDataSet  bool

	// Sub-operation counts, which only retrieve responses carry.
	//
	// Worth decoding rather than ignoring because they are the archive's own account of what it sent, and comparing that
	// against what arrived is the only way to notice that an archive believes it delivered a study which did not fully
	// turn up. A retrieve reporting success with a non-zero failed count is a real and misleading DICOM outcome.
	Remaining uint16
	Completed uint16
	Failed    uint16
	Warning   uint16
}

// decodeCommand reads a command set.
func decodeCommand(data []byte) (*command, error) {
	// Always implicit VR little endian, whatever the association negotiated for data. A parser that used the negotiated
	// syntax here would misread every command on an explicit VR association - which is most of them.
	elements, err := readElements(data, false, binary.LittleEndian)
	if err != nil {
		return nil, err
	}

	out := &command{}
	for _, e := range elements {
		switch e.Tag {
		case tagCommandField:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Field = v
			}
		case tagMessageID, tagMessageIDRespondingTo:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.MessageID = v
			}
		case tagAffectedSOPClassUID:
			out.SOPClass = e.String()
		case tagAffectedSOPInstanceUID:
			out.SOPInstance = e.String()
		case tagStatus:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Status = v
			}
		case tagNumberOfRemainingSuboperations:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Remaining = v
			}
		case tagNumberOfCompletedSuboperations:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Completed = v
			}
		case tagNumberOfFailedSuboperations:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Failed = v
			}
		case tagNumberOfWarningSuboperations:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.Warning = v
			}

		case tagCommandDataSetType:
			if v, ok := e.Uint16(binary.LittleEndian); ok {
				out.HasDataSet = v != 0x0101
			}
		}
	}

	if out.Field == 0 {
		return nil, errors.New("dicom: this DIMSE message has no command field, so there is no way to know what " +
			"operation was requested")
	}

	return out, nil
}

// pdvHeader flag bits.
const (
	pdvCommand byte = 0x01
	pdvLast    byte = 0x02
)

// writePDataPDUs writes a payload as one or more P-DATA PDUs, fragmenting to the negotiated maximum.
//
// Fragmentation is mandatory rather than optional: the remote told us its maximum and sending more in one PDU is a
// protocol violation that some peers abort on. A large study is the normal case, so this path is always exercised.
func writePDataPDUs(w io.Writer, contextID byte, payload []byte, isCommand bool, maxLength uint32) error {
	if maxLength == 0 || maxLength > maxPDULength {
		maxLength = maxPDULength
	}

	// Six bytes of PDU header plus six of PDV header have to fit inside the maximum along with the fragment.
	const overhead = 12
	chunk := int(maxLength) - overhead
	if chunk < 1024 {
		chunk = 1024
	}

	for offset := 0; offset < len(payload) || offset == 0; {
		end := offset + chunk
		if end >= len(payload) {
			end = len(payload)
		}
		last := end >= len(payload)

		fragment := payload[offset:end]

		var flags byte
		if isCommand {
			flags |= pdvCommand
		}
		if last {
			flags |= pdvLast
		}

		body := make([]byte, 0, 6+len(fragment))
		lengthBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(lengthBytes, uint32(len(fragment)+2))
		body = append(body, lengthBytes...)
		body = append(body, contextID, flags)
		body = append(body, fragment...)

		if err := writePDU(w, pduData, body); err != nil {
			return err
		}

		offset = end
		if last {
			break
		}
	}

	return nil
}

// pdv is one presentation data value.
type pdv struct {
	ContextID byte
	IsCommand bool
	IsLast    bool
	Data      []byte
}

// decodePDataPDU splits a P-DATA PDU into its presentation data values.
//
// Several per PDU is legal and real equipment does it - a small command and its small data set in one PDU is efficient -
// so a decoder that assumed one would drop the data set of every small object.
func decodePDataPDU(data []byte) ([]pdv, error) {
	var out []pdv
	offset := 0

	for offset+6 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if length < 2 {
			return nil, errors.New("dicom: a presentation data value declares less than its own header")
		}
		if offset+4+length > len(data) {
			return nil, errors.New("dicom: a presentation data value runs past the end of its PDU")
		}

		contextID := data[offset+4]
		flags := data[offset+5]

		out = append(out, pdv{
			ContextID: contextID,
			IsCommand: flags&pdvCommand != 0,
			IsLast:    flags&pdvLast != 0,
			Data:      bytes.Clone(data[offset+6 : offset+4+length]),
		})

		offset += 4 + length
	}

	if len(out) == 0 {
		return nil, errors.New("dicom: a P-DATA PDU contained no presentation data values")
	}

	return out, nil
}
