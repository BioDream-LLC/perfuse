package dicom

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// StoreHandler is called for each received object.
//
// The raw bytes handed over are a complete file representation - preamble, meta group declaring the negotiated transfer
// syntax, then the data set - rather than what arrived on the wire. An object on the wire is not self-describing, so
// anything holding it afterwards would have to be told its encoding separately, and every layer that forgot would guess
// wrong.
//
// Returning an error refuses the object, and the error's kind decides what the sender is told: a Retryable error becomes
// "out of resources", which tells the sender to try again later, and anything else becomes "cannot understand", which
// tells it not to. That distinction is the difference between a modality retrying a transient failure and retrying a
// corrupt object forever.
type StoreHandler func(ds *DataSet, raw []byte) error

// Retryable marks an error as worth the sender trying again.
type Retryable struct{ Err error }

func (r *Retryable) Error() string { return r.Err.Error() }
func (r *Retryable) Unwrap() error { return r.Err }

// ImplementationUID identifies this software in an association.
//
// A UID under a registered root would be correct for a shipped product; this is a placeholder derived from the project
// name until one is formally registered. Some equipment logs it, and a few implementations key quirks off it, so it wants to be
// stable rather than random.
const ImplementationUID = "1.2.826.0.1.3680043.10.1337.1"

// ImplementationName is limited to sixteen characters by the protocol, and longer values make some peers reject the whole
// association - which would be an absurd way to fail.
const ImplementationName = "PERFUSE"

// Server is a C-STORE service class provider.
//
// What a hospital points a modality or a PACS at. Accepts an association, receives objects, acknowledges each one, and
// answers C-ECHO because that is how every site tests a DICOM connection before trying anything else - a listener that
// stores images but cannot answer an echo looks broken to the person commissioning it.
type Server struct {
	// AETitle is what this server calls itself. Sites configure the far end to match it exactly.
	//
	// When set, an association naming a different called AE title is rejected with the specific reason for that, so the
	// operator at the other end is told which field is wrong rather than that the connection failed.
	AETitle string

	// AllowedCallingAE lists the AE titles permitted to connect. Empty accepts any caller.
	//
	// The called title, above, is the name a caller dials - so it is checked but proves nothing about who is calling.
	// This is the other half, and it was missing: who called was logged and otherwise ignored, so any host that could
	// reach the port could push images into a clinical channel.
	AllowedCallingAE []string

	// SOPClasses are the classes to accept. Empty accepts any, which is what a storage-only relay wants: it does not
	// interpret the object, so refusing an unfamiliar class would refuse valid images for no benefit.
	SOPClasses []string

	// TransferSyntaxes are the encodings to accept, in order of preference. Empty accepts the uncompressed three.
	TransferSyntaxes []string

	// Handler receives each stored object. Required.
	Handler StoreHandler

	// OnAssociate is called with each accepted association request, before it is answered. Optional.
	//
	// Exists so what a peer proposed can be inspected - which is the only way to check the association decoder against
	// another implementation's encoder rather than against itself.
	OnAssociate func(req *AssociateRequest)

	// MaxObjectBytes bounds one received object. Zero applies a default.
	MaxObjectBytes int

	// IdleTimeout closes an association that has gone silent. Zero means never, which is usually right: a modality
	// holds an association open between studies.
	IdleTimeout time.Duration

	// TLS, when set, encrypts inbound associations and can require a client certificate.
	//
	// Mirth needs its paid SSL Manager extension for this and the community forks cannot ship it at all, so encrypted
	// imaging is a place where being a rewrite rather than a fork is a real advantage.
	TLS *tls.Config

	Log *slog.Logger

	listener net.Listener
	mu       sync.Mutex
	closed   bool
	wg       sync.WaitGroup
}

// DefaultMaxObjectBytes bounds one object when nothing else says.
//
// 512 MB. Large enough for any single instance including uncompressed whole-slide tiles, small enough that a peer cannot
// exhaust memory by claiming to send something enormous.
const DefaultMaxObjectBytes = 512 << 20

// defaultTransferSyntaxes are the three uncompressed encodings every implementation supports.
//
// Explicit VR little endian first because it is unambiguous, then implicit which is the standard's mandatory default, then
// big endian which is retired but still emitted by older equipment. Compressed syntaxes are not offered by default: this
// server hands objects on unchanged, and accepting a compression it cannot describe would be claiming a capability.
var defaultTransferSyntaxes = []string{
	ExplicitVRLittleEndian,
	ImplicitVRLittleEndian,
	ExplicitVRBigEndian,
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Listen starts accepting associations on addr.
func (s *Server) Listen(addr string) error {
	if s.Handler == nil {
		return errors.New("dicom: a store server needs a handler, or received objects would be acknowledged and " +
			"then discarded")
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if s.TLS != nil {
		ln = tls.NewListener(ln, s.TLS)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.accept(ln)
	}()

	return nil
}

// Addr reports the address being listened on, which is how a test finds the port when it asked for :0.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Close stops listening and waits for associations in progress.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.listener
	s.mu.Unlock()

	var err error
	if ln != nil {
		err = ln.Close()
	}
	s.wg.Wait()
	return err
}

func (s *Server) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.log().Error("the DICOM listener stopped accepting", "err", err)
			}
			return
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			if err := s.serve(conn); err != nil && !errors.Is(err, io.EOF) {
				s.log().Warn("a DICOM association ended with an error",
					"err", err, "remote", conn.RemoteAddr().String())
			}
		}()
	}
}

// serve handles one association from start to release.
func (s *Server) serve(conn net.Conn) error {
	if s.IdleTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.IdleTimeout))
	}

	first, err := readPDU(conn, maxPDULength)
	if err != nil {
		return err
	}
	if first.Type != pduAssociateRequest {
		// Aborted rather than ignored. A peer that opens with anything else is either broken or not speaking DICOM, and
		// leaving the connection open would hold a slot until the idle timeout.
		_ = writePDU(conn, pduAbort, []byte{0, 0, 2, 2})
		return fmt.Errorf("dicom: the first PDU was type %d rather than an association request", first.Type)
	}

	req, err := decodeAssociateRequest(first.Data)
	if err != nil {
		_ = writePDU(conn, pduAbort, []byte{0, 0, 2, 6})
		return err
	}

	if s.OnAssociate != nil {
		s.OnAssociate(req)
	}

	// The called AE title check, with the specific rejection reason for it. Generic rejection would leave the operator at
	// the other end guessing which of two names is wrong.
	if s.AETitle != "" && !strings.EqualFold(strings.TrimSpace(req.CalledAE), s.AETitle) {
		s.log().Warn("rejected an association for the wrong called AE title",
			"called", req.CalledAE, "expected", s.AETitle, "calling", req.CallingAE)
		_ = writePDU(conn, pduAssociateReject, []byte{0, 1, 1, 7})
		return nil
	}

	// The calling AE title check.
	//
	// Result 1, source 1, reason 3 is "calling AE title not recognised", which is the specific code for this and tells
	// the operator at the other end which name to fix rather than that the connection failed.
	if !s.callerAllowed(req.CallingAE) {
		s.log().Warn("rejected an association from an AE title that is not on the allowlist",
			"calling", req.CallingAE, "called", req.CalledAE, "allowed", strings.Join(s.AllowedCallingAE, ","))
		_ = writePDU(conn, pduAssociateReject, []byte{0, 1, 1, 3})
		return nil
	}

	accepted := s.negotiate(req)

	anyAccepted := false
	for _, ctx := range accepted {
		if ctx.Result == pcAccepted {
			anyAccepted = true
			break
		}
	}
	if !anyAccepted {
		// Accepted with every context refused would leave the sender with an open association it cannot use, and some
		// senders sit there until they time out. Rejecting says so immediately.
		s.log().Warn("rejected an association with no usable presentation contexts",
			"calling", req.CallingAE, "offered", len(req.Contexts))
		_ = writePDU(conn, pduAssociateReject, []byte{0, 1, 1, 2})
		return nil
	}

	if err := writePDU(conn, pduAssociateAccept,
		encodeAssociateAccept(*req, accepted, ImplementationUID, ImplementationName)); err != nil {
		return err
	}

	s.log().Info("DICOM association accepted",
		"calling", req.CallingAE, "called", req.CalledAE,
		"contexts", len(accepted), "remote", conn.RemoteAddr().String())

	return s.messages(conn, req, accepted)
}

// negotiate decides which presentation contexts to accept.
func (s *Server) negotiate(req *AssociateRequest) []PresentationContext {
	offered := s.TransferSyntaxes
	if len(offered) == 0 {
		offered = defaultTransferSyntaxes
	}

	out := make([]PresentationContext, 0, len(req.Contexts))

	for _, ctx := range req.Contexts {
		result := PresentationContext{ID: ctx.ID, AbstractSyntax: ctx.AbstractSyntax}

		// Verification is always accepted regardless of the configured class list. It carries no object, and refusing an
		// echo makes a working connection look broken to whoever is commissioning it.
		classOK := ctx.AbstractSyntax == VerificationSOPClass || len(s.SOPClasses) == 0
		if !classOK {
			for _, allowed := range s.SOPClasses {
				if allowed == ctx.AbstractSyntax {
					classOK = true
					break
				}
			}
		}
		if !classOK {
			result.Result = pcRejectedAbstractSyntax
			out = append(out, result)
			continue
		}

		// Our preference order, not theirs. The acceptor chooses, and choosing explicit VR where both are offered means
		// the data set says its own types rather than relying on a dictionary.
		chosen := ""
		for _, preferred := range offered {
			for _, theirs := range ctx.TransferSyntaxes {
				if preferred == theirs {
					chosen = preferred
					break
				}
			}
			if chosen != "" {
				break
			}
		}

		if chosen == "" {
			result.Result = pcRejectedTransferSyntaxes
			out = append(out, result)
			continue
		}

		result.Accepted = chosen
		out = append(out, result)
	}

	return out
}

// messages handles DIMSE traffic until release or abort.
func (s *Server) messages(conn net.Conn, req *AssociateRequest, accepted []PresentationContext) error {
	maxObject := s.MaxObjectBytes
	if maxObject <= 0 {
		maxObject = DefaultMaxObjectBytes
	}

	byID := map[byte]PresentationContext{}
	for _, ctx := range accepted {
		if ctx.Result == pcAccepted {
			byID[ctx.ID] = ctx
		}
	}

	var commandBuf, dataBuf bytes.Buffer
	var currentContext byte
	var pending *command

	for {
		if s.IdleTimeout > 0 {
			_ = conn.SetDeadline(time.Now().Add(s.IdleTimeout))
		}

		p, err := readPDU(conn, maxPDULength)
		if err != nil {
			return err
		}

		switch p.Type {
		case pduReleaseRequest:
			// Answered before closing. A sender that gets no release response logs an error even though everything
			// arrived, which produces support calls about successful transfers.
			if err := writePDU(conn, pduReleaseResponse, []byte{0, 0, 0, 0}); err != nil {
				return err
			}
			s.log().Info("DICOM association released", "calling", req.CallingAE)
			return nil

		case pduAbort:
			s.log().Warn("the remote aborted the association", "calling", req.CallingAE)
			return ErrAborted

		case pduData:
			values, err := decodePDataPDU(p.Data)
			if err != nil {
				return err
			}

			for _, value := range values {
				if _, ok := byID[value.ContextID]; !ok {
					return fmt.Errorf("dicom: the remote sent data on presentation context %d, which was not "+
						"accepted", value.ContextID)
				}
				currentContext = value.ContextID

				if value.IsCommand {
					commandBuf.Write(value.Data)
					if !value.IsLast {
						continue
					}

					cmd, err := decodeCommand(commandBuf.Bytes())
					commandBuf.Reset()
					if err != nil {
						return err
					}

					if !cmd.HasDataSet {
						if err := s.respondWithoutData(conn, req, cmd, currentContext); err != nil {
							return err
						}
						continue
					}
					// The data set follows in subsequent PDVs, so the command is held until it arrives.
					// Deliberately a local rather than a field on Server: two modalities sending at once would
					// interleave their pending commands, and each object would then be acknowledged against the
					// other's message id.
					pending = cmd
					continue
				}

				dataBuf.Write(value.Data)
				if dataBuf.Len() > maxObject {
					// Aborted rather than allowed to continue. An object beyond the limit will not be stored, and
					// reading the rest of it before saying so wastes the sender's time and this receiver's memory.
					_ = writePDU(conn, pduAbort, []byte{0, 0, 0, 0})
					return fmt.Errorf("dicom: an incoming object exceeded the %d byte limit", maxObject)
				}
				if !value.IsLast {
					continue
				}

				raw := bytes.Clone(dataBuf.Bytes())
				dataBuf.Reset()

				if err := s.deliver(conn, req, currentContext, byID[currentContext], raw, pending); err != nil {
					return err
				}
				pending = nil
			}

		default:
			return fmt.Errorf("dicom: unexpected PDU type %d during an association", p.Type)
		}
	}
}

// respondWithoutData answers a command that carries no data set, which in practice means C-ECHO.
func (s *Server) respondWithoutData(conn net.Conn, req *AssociateRequest, cmd *command, contextID byte) error {
	switch cmd.Field {
	case cmdCEchoRQ:
		s.log().Debug("answered a DICOM echo", "calling", req.CallingAE)
		return writePDataPDUs(conn, contextID,
			cEchoResponse(cmd.MessageID, VerificationSOPClass, StatusSuccess), true, req.MaxPDULength)

	case cmdCStoreRQ:
		// A store with no data set is a sender bug. Answered rather than ignored, because a sender waiting for a
		// response it never gets holds the association open until it times out.
		return writePDataPDUs(conn, contextID,
			cStoreResponse(cmd.MessageID, cmd.SOPClass, cmd.SOPInstance, StatusErrorCannotUnderstand),
			true, req.MaxPDULength)

	default:
		// Anything else is an operation this does not implement. Refusing it explicitly beats silence, which the sender
		// cannot distinguish from a hang.
		return writePDataPDUs(conn, contextID,
			cStoreResponse(cmd.MessageID, cmd.SOPClass, cmd.SOPInstance, StatusErrorCannotUnderstand),
			true, req.MaxPDULength)
	}
}

// deliver hands a received object to the handler and answers the sender.
func (s *Server) deliver(
	conn net.Conn,
	req *AssociateRequest,
	contextID byte,
	ctx PresentationContext,
	raw []byte,
	cmd *command,
) error {
	if cmd == nil {
		return errors.New("dicom: a data set arrived with no command before it")
	}

	status := StatusSuccess

	// Parsed for the handler's benefit, but a parse failure is reported rather than fatal: the raw bytes are still
	// deliverable, and a receiver that refuses everything it cannot fully parse is less useful than one that hands the
	// bytes on with a note.
	ds, parseErr := ParseDataSet(raw, ctx.Accepted)
	if parseErr != nil {
		s.log().Warn("a received DICOM object could not be parsed",
			"err", parseErr, "sop_instance", cmd.SOPInstance, "bytes", len(raw))
		status = StatusErrorCannotUnderstand
	} else if err := s.Handler(ds, Wrap(raw, ctx.Accepted,
		ds.Text(TagSOPClassUID), ds.Text(TagSOPInstanceUID))); err != nil {
		var retryable *Retryable
		if errors.As(err, &retryable) {
			status = StatusRefusedOutOfResources
		} else {
			status = StatusErrorCannotUnderstand
		}
		s.log().Warn("a received DICOM object was refused",
			"err", err, "sop_instance", cmd.SOPInstance, "status", StatusText(status))
	}

	return writePDataPDUs(conn, contextID,
		cStoreResponse(cmd.MessageID, cmd.SOPClass, cmd.SOPInstance, status), true, req.MaxPDULength)
}

// ParseDataSet parses a data set received over an association.
//
// Objects on the wire have no preamble and no meta group, so the transfer syntax comes from the negotiation rather than
// from the object. That is the whole reason the accepted syntax has to be tracked per presentation context: the bytes
// themselves do not say how to read them.
func ParseDataSet(raw []byte, transferSyntax string) (*DataSet, error) {
	explicit, order, err := encodingFor(transferSyntax)
	if err != nil {
		return nil, err
	}

	elements, err := readElements(raw, explicit, order)
	if err != nil {
		return nil, err
	}

	return &DataSet{TransferSyntax: transferSyntax, Elements: elements}, nil
}

// callerAllowed reports whether a calling AE title may connect.
//
// An empty allowlist accepts anybody, which is the state of a first installation: nobody knows the modality titles until
// something has connected. Refusing by default would break every deployment on day one, and the cost of that is people
// switching the check off wholesale rather than filling it in.
//
// Compared case-insensitively and with surrounding space trimmed, because the wire pads titles to sixteen characters and
// vendors disagree about capitalisation. A comparison that failed on padding would look like an allowlist that never
// matches, and the fix people would reach for is emptying it.
func (s *Server) callerAllowed(calling string) bool {
	if len(s.AllowedCallingAE) == 0 {
		return true
	}

	got := strings.ToUpper(strings.TrimSpace(calling))

	for _, allowed := range s.AllowedCallingAE {
		if strings.ToUpper(strings.TrimSpace(allowed)) == got {
			return true
		}
	}

	return false
}
