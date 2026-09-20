package dicom

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// Client is a C-STORE service class user.
//
// Sends objects to a PACS or an archive. Deliberately one association per Send rather than a pool: DICOM associations are
// stateful and a half-broken one is worse than none, so a fresh association per operation trades a little latency for the
// property that a failure cannot poison the next attempt.
type Client struct {
	// Addr is the remote host and port.
	Addr string

	// CalledAE is what the remote calls itself, and CallingAE what this end calls itself.
	//
	// Both matter: sites configure the remote to accept one specific calling title and reject everything else, which is
	// their only access control. A mismatch is the commonest reason a first connection fails.
	CalledAE  string
	CallingAE string

	// TransferSyntaxes are the encodings to offer, in order of preference. Empty offers the uncompressed three.
	TransferSyntaxes []string

	// Timeout bounds the whole operation. Zero applies a default.
	Timeout time.Duration

	// TLS, when set, wraps the connection.
	//
	// Worth noting that Mirth needs its paid SSL Manager extension for this, and the community forks cannot ship it at
	// all - so encrypted DICOM is one of the places where being a rewrite rather than a fork is an advantage.
	TLS *tls.Config
}

// DefaultClientTimeout bounds an operation when nothing else says.
//
// Two minutes. A single large instance over a slow link genuinely takes that long, and a shorter default would present as
// intermittent failures on exactly the studies people care most about.
const DefaultClientTimeout = 2 * time.Minute

// Echo verifies connectivity with a C-ECHO.
//
// The first thing to try against a new remote, and worth exposing separately: it proves the network path, the AE titles
// and the association negotiation all work without needing an object to send. When a site says "DICOM is not working",
// this is the question being asked.
func (c *Client) Echo(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	contexts := []PresentationContext{{
		ID:               1,
		AbstractSyntax:   VerificationSOPClass,
		TransferSyntaxes: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
	}}

	accepted, maxLength, err := c.associate(conn, contexts)
	if err != nil {
		return err
	}

	usable, ok := firstAccepted(accepted)
	if !ok {
		return fmt.Errorf("%w: the remote accepted the association but refused the verification context",
			ErrAssociationRejected)
	}

	if err := writePDataPDUs(conn, usable.ID, cEchoRequest(1), true, maxLength); err != nil {
		return err
	}

	cmd, _, err := readResponse(conn, maxLength)
	if err != nil {
		return err
	}
	if !IsSuccess(cmd.Status) {
		return fmt.Errorf("dicom: the remote answered an echo with %s", StatusText(cmd.Status))
	}

	return c.release(conn)
}

// Send stores one object.
//
// The object's own SOP class and instance UIDs are used, read from the data set, because a C-STORE that announces a
// different class from the object it carries is refused by any conforming receiver - and the refusal says the data set
// does not match its class, which sends people looking at the image rather than at the sender.
func (c *Client) Send(ctx context.Context, raw []byte) error {
	ds, err := Parse(raw)
	if err != nil {
		return fmt.Errorf("dicom: this object cannot be sent because it cannot be read: %w", err)
	}

	sopClass := ds.Text(TagSOPClassUID)
	if sopClass == "" {
		sopClass = ds.Text(TagMediaStorageSOPClassUID)
	}
	sopInstance := ds.Text(TagSOPInstanceUID)
	if sopInstance == "" {
		sopInstance = ds.Text(TagMediaStorageSOPInstanceUID)
	}

	if sopClass == "" || sopInstance == "" {
		// Refused here rather than sent with empty identifiers. A receiver given a blank SOP instance UID either rejects
		// it or, worse, stores it and overwrites the last object with a blank identifier.
		return errors.New("dicom: this object has no SOP class or instance UID, so there is no way to announce " +
			"what is being sent; a receiver would reject it or store it over another object")
	}

	// Sent in the syntax the object is already in. Transcoding would mean re-encoding pixel data, which this does not do
	// and should not pretend to.
	syntax := ds.TransferSyntax
	offered := []string{syntax}
	if syntax != ImplicitVRLittleEndian {
		// Implicit is the standard's mandatory default, so offering it as a fallback costs nothing and rescues the case
		// where a receiver supports only that.
		offered = append(offered, ImplicitVRLittleEndian)
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	contexts := []PresentationContext{{
		ID:               1,
		AbstractSyntax:   sopClass,
		TransferSyntaxes: offered,
	}}

	accepted, maxLength, err := c.associate(conn, contexts)
	if err != nil {
		return err
	}

	usable, ok := firstAccepted(accepted)
	if !ok {
		// Named specifically. "Association failed" would be wrong - it succeeded, and the remote then declined this
		// particular kind of object, which is a different conversation with the far end.
		reason := "the remote refused every presentation context offered"
		if len(accepted) > 0 {
			reason = accepted[0].ResultText()
		}
		return fmt.Errorf("%w: %s (SOP class %s)", ErrAssociationRejected, reason, sopClass)
	}

	// The data set has to be sent without its preamble and meta group: those belong to the file representation, and a
	// receiver given them reads the meta group as though it were the object.
	body := stripFileMeta(raw)

	if err := writePDataPDUs(conn, usable.ID,
		cStoreRequest(1, sopClass, sopInstance), true, maxLength); err != nil {
		return err
	}
	if err := writePDataPDUs(conn, usable.ID, body, false, maxLength); err != nil {
		return err
	}

	cmd, _, err := readResponse(conn, maxLength)
	if err != nil {
		return err
	}
	if !IsSuccess(cmd.Status) {
		return fmt.Errorf("dicom: the remote refused %s: %s", sopInstance, StatusText(cmd.Status))
	}

	return c.release(conn)
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultClientTimeout
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, err
	}

	if c.TLS != nil {
		tlsConn := tls.Client(conn, c.TLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("dicom: the TLS handshake with %s failed: %w", c.Addr, err)
		}
		conn = tlsConn
	}

	// One deadline for the whole operation rather than per read. A partial transfer that stalls is the failure this
	// prevents, and a per-read deadline resets on every fragment so a slow trickle could hold the connection forever.
	_ = conn.SetDeadline(time.Now().Add(timeout))

	return conn, nil
}

func (c *Client) associate(conn net.Conn, contexts []PresentationContext) ([]PresentationContext, uint32, error) {
	ctxs, _, maxLength, err := c.associateWithRoles(conn, contexts, nil)
	return ctxs, maxLength, err
}

// associateWithRoles negotiates an association, proposing roles.
//
// Roles are only needed by C-GET, where the archive sends images back on the association we opened - so for those SOP
// classes we are the provider rather than the user, and an archive cannot infer that.
func (c *Client) associateWithRoles(conn net.Conn, contexts []PresentationContext, roles []RoleSelection) (
	[]PresentationContext, []RoleSelection, uint32, error) {
	syntaxes := c.TransferSyntaxes
	if len(syntaxes) > 0 {
		for i := range contexts {
			contexts[i].TransferSyntaxes = syntaxes
		}
	}

	req := AssociateRequest{
		CalledAE:           orDefault(c.CalledAE, "ANY-SCP"),
		CallingAE:          orDefault(c.CallingAE, ImplementationName),
		Contexts:           contexts,
		Roles:              roles,
		MaxPDULength:       maxPDULength,
		ImplementationUID:  ImplementationUID,
		ImplementationName: ImplementationName,
	}

	if err := writePDU(conn, pduAssociateRequest, encodeAssociateRequest(req)); err != nil {
		return nil, nil, 0, err
	}

	p, err := readPDU(conn, maxPDULength)
	if err != nil {
		return nil, nil, 0, err
	}

	switch p.Type {
	case pduAssociateAccept:
		ctxs, accepted, maxLength, err := decodeAssociateAccept(p.Data)
		if err != nil {
			return nil, nil, 0, err
		}
		return ctxs, accepted, maxLength, nil
	case pduAssociateReject:
		// The decoded reason, not the numbers. Association rejection is where DICOM connections fail most often and the
		// reasons are genuinely actionable - "the called AE title was not recognised" names the field to fix.
		return nil, nil, 0, fmt.Errorf("%w: %s", ErrAssociationRejected, rejectReason(p.Data))
	case pduAbort:
		return nil, nil, 0, ErrAborted
	default:
		return nil, nil, 0, fmt.Errorf("dicom: the remote answered an association request with PDU type %d", p.Type)
	}
}

func decodeAssociateAcceptOrFail(data []byte) ([]PresentationContext, uint32, error) {
	contexts, _, maxLength, err := decodeAssociateAccept(data)
	if err != nil {
		return nil, 0, err
	}
	return contexts, maxLength, nil
}

// readResponse reads DIMSE PDUs until a complete command has arrived.
func readResponse(conn net.Conn, maxLength uint32) (*command, []byte, error) {
	var commandBuf, dataBuf bytes.Buffer
	var cmd *command

	for {
		p, err := readPDU(conn, maxPDULength)
		if err != nil {
			return nil, nil, err
		}

		switch p.Type {
		case pduData:
			values, err := decodePDataPDU(p.Data)
			if err != nil {
				return nil, nil, err
			}
			for _, value := range values {
				if value.IsCommand {
					commandBuf.Write(value.Data)
					if !value.IsLast {
						continue
					}

					decoded, err := decodeCommand(commandBuf.Bytes())
					if err != nil {
						return nil, nil, err
					}
					cmd = decoded

					// Returning here is only right when no data set follows, and the command itself says whether one
					// does. C-STORE and C-ECHO responses never carry one, which is why returning unconditionally worked
					// until C-FIND: a pending C-FIND response carries the match in the PDVs after the command, so
					// returning early handed back an empty data set and left the match in the stream to be attached to
					// the next response's command. The result was one match instead of two, each paired with the wrong
					// command - plausible enough to pass a test that only counted non-empty results.
					if !cmd.HasDataSet {
						return cmd, nil, nil
					}
					continue
				}

				dataBuf.Write(value.Data)
				if value.IsLast && cmd != nil {
					// The data set is complete and the command that described it has already been read.
					return cmd, dataBuf.Bytes(), nil
				}
			}

		case pduAbort:
			// Distinguished from a refusal. An abort mid-transfer usually means the receiver hit a problem of its own,
			// and reporting it as a rejection would send somebody to check the object instead of the receiver.
			return nil, nil, fmt.Errorf("%w while waiting for a response", ErrAborted)

		default:
			return nil, nil, fmt.Errorf("dicom: expected a response and got PDU type %d", p.Type)
		}
	}
}

func (c *Client) release(conn net.Conn) error {
	if err := writePDU(conn, pduReleaseRequest, []byte{0, 0, 0, 0}); err != nil {
		return err
	}

	// The response is waited for rather than assumed. Closing without it leaves the remote's association in a state some
	// implementations log as an error, and a site reviewing its PACS logs should not find one per study.
	p, err := readPDU(conn, maxPDULength)
	if err != nil {
		// A remote that closes without answering is common enough not to be worth reporting as a failure: everything was
		// already acknowledged, and the object is stored.
		return nil
	}
	if p.Type != pduReleaseResponse && p.Type != pduAbort {
		return fmt.Errorf("dicom: the remote answered a release request with PDU type %d", p.Type)
	}
	return nil
}

// stripFileMeta removes the preamble and meta group so only the data set is sent.
//
// Necessary because those belong to the file representation. A receiver given the meta group reads it as part of the
// object, and the result is a stored image whose first elements are group 2 - which some archives accept and then cannot
// index.
func stripFileMeta(raw []byte) []byte {
	ds, err := Parse(raw)
	if err != nil {
		return raw
	}
	if !ds.HadPreamble {
		// Already a bare data set, as arrives over the network.
		return raw
	}

	offset := preambleLength + len(magic)
	if offset >= len(raw) {
		return raw
	}

	out := &DataSet{}
	consumed, err := readMetaGroup(out, raw[offset:])
	if err != nil {
		return raw
	}

	end := offset + consumed
	if end >= len(raw) {
		return raw
	}
	return raw[end:]
}

func firstAccepted(contexts []PresentationContext) (PresentationContext, bool) {
	for _, ctx := range contexts {
		if ctx.Result == pcAccepted && ctx.Accepted != "" {
			return ctx, true
		}
	}
	return PresentationContext{}, false
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
