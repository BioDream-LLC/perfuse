package dicom

import (
	"bytes"
	"fmt"
	"net"
)

// incoming is one complete DIMSE message read from an association, with the context it arrived on.
//
// Distinct from what readResponse returns because a retrieve is bidirectional: the archive sends C-STORE requests to us while
// also sending C-GET responses about its own progress, and the two interleave. The context identifier has to be carried
// through because the reply goes back on the same one, and during a C-GET that is a storage context rather than the retrieve
// context the request went out on.
type incoming struct {
	contextID byte
	command   *command
	data      []byte
}

// readIncoming reads the next complete message, whichever direction it belongs to.
//
// The reason this cannot reuse readResponse is that readResponse decides when a message is finished by whether the command
// declared a data set. That is right for a request-response exchange and not enough here, because the messages arriving are
// of several kinds at once and the context identifier of each has to survive to the reply.
func readIncoming(conn net.Conn, maxLength uint32) (incoming, error) {
	var commandBuf, dataBuf bytes.Buffer
	var cmd *command
	var contextID byte

	for {
		p, err := readPDU(conn, maxPDULength)
		if err != nil {
			return incoming{}, err
		}

		switch p.Type {
		case pduData:
			values, err := decodePDataPDU(p.Data)
			if err != nil {
				return incoming{}, err
			}

			for _, value := range values {
				// Recorded from whichever fragment arrives, because the reply must go back on the context the request came
				// in on. During a retrieve that is a storage context, not the one the C-GET was sent on.
				contextID = value.ContextID

				if value.IsCommand {
					commandBuf.Write(value.Data)
					if !value.IsLast {
						continue
					}

					decoded, err := decodeCommand(commandBuf.Bytes())
					if err != nil {
						return incoming{}, err
					}
					cmd = decoded

					if !cmd.HasDataSet {
						return incoming{contextID: contextID, command: cmd}, nil
					}
					continue
				}

				dataBuf.Write(value.Data)
				if value.IsLast && cmd != nil {
					return incoming{contextID: contextID, command: cmd, data: dataBuf.Bytes()}, nil
				}
			}

		case pduReleaseRequest:
			// The archive is finishing. Answered rather than ignored, so it closes cleanly instead of timing out and
			// logging a failed association against our AE title.
			_ = writePDU(conn, pduReleaseResponse, []byte{0, 0, 0, 0})
			return incoming{}, fmt.Errorf("dicom: the remote released the association mid-retrieve")

		case pduAbort:
			return incoming{}, fmt.Errorf("%w during a retrieve", ErrAborted)

		default:
			return incoming{}, fmt.Errorf("dicom: expected a message and got PDU type %d", p.Type)
		}
	}
}
