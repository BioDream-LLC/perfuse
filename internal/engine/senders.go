package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crypto/tls"
	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/edifact"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"github.com/biodream-llc/perfuse/internal/x12"
	"github.com/biodream-llc/perfuse/mllp"
	"net"
)

// DefaultSenderFactory builds the sender for a configured destination.
func DefaultSenderFactory(d config.Destination) (Sender, error) {
	switch d.Type {
	case config.DestinationMLLP:
		return NewMLLPSender(d)
	case config.DestinationFile:
		return NewFileSender(d)
	case config.DestinationFHIR:
		return NewFHIRSender(d, nil)
	case config.DestinationCDA:
		return NewCDASender(d, nil)
	case config.DestinationHTTP:
		return NewHTTPSender(d, nil)
	case config.DestinationDatabase:
		return NewDatabaseSender(d, nil)
	case config.DestinationSFTP:
		return NewSFTPSender(d, nil)
	case config.DestinationTCP:
		if d.TCP == nil {
			return nil, fmt.Errorf("destination %q is tcp but has no tcp block", d.Name)
		}
		return NewTCPSender(*d.TCP)
	case config.DestinationS3:
		return NewS3Sender(d)
	case config.DestinationFTP:
		return NewFTPSender(d)
	case config.DestinationDocument:
		return NewDocumentSender(d)
	case config.DestinationSOAP:
		return NewSOAPSender(d)
	case config.DestinationDICOM:
		return NewDICOMSender(d)
	case config.DestinationDICOMweb:
		return NewDICOMwebSender(d)
	case config.DestinationXDS:
		return NewXDSSender(d)
	case config.DestinationDirect:
		return NewDirectSender(d)
	case config.DestinationAS2:
		return NewAS2Sender(d)
	case config.DestinationBroker:
		return NewBrokerSender(d)
	case config.DestinationKafka:
		return NewKafkaSender(d)
	case config.DestinationSQS:
		return NewSQSSender(d)
	case config.DestinationSNS:
		return NewSNSSender(d)
	case config.DestinationAMQP:
		return NewAMQPSender(d)
	case config.DestinationAzureBlob:
		return NewAzureBlobSender(d)
	case config.DestinationMQTT:
		return NewMQTTSender(d)
	case config.DestinationIBMMQ:
		return NewIBMMQSender(d)
	case config.DestinationPubSub:
		return NewPubSubSender(d)
	case config.DestinationGCS:
		return NewGCSSender(d)
	case config.DestinationJavaScript:
		// A placeholder the channel replaces, because a script destination needs the channel's script engine and the
		// factory sees only the destination. Returning an error here instead would make the factory the thing that
		// refuses a valid configuration.
		return &unbuiltScriptSender{name: d.Name}, nil
	case config.DestinationSMTP:
		return NewSMTPSender(d)
	case config.DestinationChannel:
		// The default factory has no router, because routing needs the set of running channels and
		// that lives above this package. A server that supports routing supplies its own factory;
		// this path is what a test or a one-off tool gets, and it says so rather than pretending.
		return NewChannelSender(d, nil)
	default:
		return nil, fmt.Errorf("destination type %q is not implemented", d.Type)
	}
}

// MLLPSender forwards messages to a remote MLLP listener and treats a negative
// acknowledgement as a delivery failure.
type MLLPSender struct {
	client *mllp.Client
	addr   string
}

// NewMLLPSender builds a sender for an mllp destination.
func NewMLLPSender(d config.Destination) (*MLLPSender, error) {
	client := &mllp.Client{
		Addr:    d.Address,
		Timeout: d.Timeout,
	}

	if d.TLS.IsEnabled() {
		cfg, err := tlsconf.ForSender(d.TLS)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
		// The client already had a Dial hook for exactly this. The dialer is wrapped
		// rather than replaced so the configured timeout still bounds the whole
		// thing, handshake included: a TLS handshake against an unresponsive host
		// hangs just as long as a TCP connect.
		client.Dial = func(ctx context.Context, addr string) (net.Conn, error) {
			dialer := &tls.Dialer{
				NetDialer: &net.Dialer{},
				Config:    cfg,
			}
			return dialer.DialContext(ctx, "tcp", addr)
		}
	}

	return &MLLPSender{addr: d.Address, client: client}, nil
}

// Send forwards the message and waits for the acknowledgement.
//
// An AE or AR is a failure, not a success. Forwarding a message, being told it
// was rejected, and then reporting delivery upstream would lose the data with
// every party believing someone else has it.
func (s *MLLPSender) Send(ctx context.Context, msg []byte) error {
	_, err := s.SendForResponse(ctx, msg)
	return err
}

// SendForResponse delivers the message and returns the acknowledgement, satisfying Responder.
//
// The acknowledgement is returned even when the error is non-nil, because it is usually what explains
// the error - a negative acknowledgement carries the reason in MSA-3, and a response transformer that
// could not see it would be inspecting nothing on precisely the deliveries worth inspecting.
func (s *MLLPSender) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	reply, err := s.client.Send(ctx, msg)
	if err != nil {
		return reply, err
	}

	ack, err := hl7.Parse(reply)
	if err != nil {
		// A reply we cannot read is not proof of delivery.
		return reply, fmt.Errorf("acknowledgement from %s could not be parsed: %w", s.addr, err)
	}

	code := ack.MustGet("MSA-1")
	switch hl7.AckCode(code) {
	case hl7.AckAccept, hl7.CommitAccept:
		return reply, nil
	case "":
		return reply, fmt.Errorf("acknowledgement from %s has no MSA-1", s.addr)
	default:
		if text := ack.MustGet("MSA-3"); text != "" {
			return reply, fmt.Errorf("%s rejected the message with %s: %s", s.addr, code, text)
		}
		return reply, fmt.Errorf("%s rejected the message with %s", s.addr, code)
	}
}

// Describe names the destination for logs.
func (s *MLLPSender) Describe() string { return "mllp " + s.addr }

// Close releases the connection.
func (s *MLLPSender) Close() error { return s.client.Close() }

// FileSender appends messages to a file per day and message type.
//
// One file per message would be tidier and produces millions of inodes on a busy
// feed, which is a problem people discover in production. Messages are stored
// MLLP-framed so a file replays byte for byte.
type FileSender struct {
	dir string

	// Set from the destination's file_name, temp_suffix, append and retain_hours.
	name       string
	fileName   string
	tempSuffix string
	appendTo   bool
	retain     time.Duration
	lastPrune  time.Time
	now        func() time.Time

	mu       sync.Mutex
	files    map[string]*os.File
	day      string
	dataType config.DataType
	sealer   func([]byte) ([]byte, error)
}

// SetSeal makes the sender encrypt each file's bytes (a pgp block).
func (s *FileSender) SetSeal(f func([]byte) ([]byte, error)) { s.sealer = f }

// NewFileSender builds a sender for a file destination, creating the directory.
func NewFileSender(d config.Destination) (*FileSender, error) {
	if err := os.MkdirAll(d.Dir, 0o750); err != nil {
		return nil, err
	}
	suffix := d.TempSuffix
	if suffix == "" {
		suffix = ".part"
	}
	return &FileSender{
		dir: d.Dir, name: d.Name, fileName: strings.TrimSpace(d.FileName), tempSuffix: suffix, appendTo: d.Append,
		retain: time.Duration(d.RetainHours) * time.Hour, now: time.Now,
		files: map[string]*os.File{}, dataType: config.DataHL7,
	}, nil
}

// SetDataType tells the sender what format it is writing.
//
// Without it every archive was written as framed HL7. For an X12 channel that produced a
// file named .hl7, containing claims wrapped in MLLP control characters, that no X12
// receiver would accept - a corrupted archive that looked like a successful delivery.
func (s *FileSender) SetDataType(t config.DataType) {
	s.mu.Lock()
	s.dataType = t
	s.mu.Unlock()
}

// Send appends the message.
func (s *FileSender) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.prune(now)

	name, ext := s.describe(msg)
	body, err := seal(s.sealer, s.frame(msg))
	if err != nil {
		return err
	}
	if s.fileName != "" && !s.appendTo {
		return s.writeOne(filepath.Join(s.dir, s.render(now, name, ext, msg)), body)
	}

	var filename string
	if s.fileName != "" {
		filename = s.render(now, name, ext, msg)
	} else {
		filename = fmt.Sprintf("%s-%s.%s", now.Format("20060102"), sanitise(name), ext)
	}

	// A new day closes yesterday's files: they are never written again, and an archive kept for months would otherwise
	// hold a descriptor open for every day and message type it has seen.
	if day := now.Format("20060102"); day != s.day {
		for k, f := range s.files {
			_ = f.Close()
			delete(s.files, k)
		}
		s.day = day
	}

	f, ok := s.files[filename]
	if !ok {
		var err error
		f, err = os.OpenFile(filepath.Join(s.dir, filename),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		s.files[filename] = f
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	// Sync before reporting success. Without it, "delivered" means the bytes
	// reached a buffer that a power cut discards, which is not what an
	// acknowledgement should promise.
	return f.Sync()
}

// frame applies the framing the archive is written in.

// HL7 is MLLP-framed so the archive is a valid MLLP stream and can be replayed
// straight back through `perfuse send`. X12 must not be: the control characters are
// not part of the format, and several interchanges written back to back is already
// exactly what an X12 batch file looks like.
// Delimited data must not be framed either, and for a sharper reason than X12: a CSV with control characters around
// every row will not open in a spreadsheet, so the failure is immediate and total rather than subtle. Found by sending a
// real file through and looking at what was written.
func (s *FileSender) frame(msg []byte) []byte {
	out := msg
	// Raw must not be framed for the sharpest reason of the four: the payload may be a PDF or a zip, and control
	// characters wrapped around it produce a file no reader will open. The point of a raw channel is that what
	// arrives at the far end is byte-for-byte what came in.
	//
	// The pharmacy formats are excluded for a reason of their own. An NCPDP claim already uses 0x1C and 0x1E as its own
	// separators, so wrapping it in MLLP's 0x0B and 0x1C puts a second, different meaning on a byte the claim is already
	// using - and a reader unframing it would stop at the first field separator inside the first segment. A prescription
	// is an XML document, and a document with control characters around it is not well-formed XML.
	if s.dataType != config.DataX12 && s.dataType != config.DataDelimited && s.dataType != config.DataEDIFACT &&
		s.dataType != config.DataDICOM && s.dataType != config.DataRaw &&
		s.dataType != config.DataNCPDP && s.dataType != config.DataScript {
		out = mllp.Frame(msg)
	}
	return out
}

// render fills in a file_name template for one message.
func (s *FileSender) render(now time.Time, name, ext string, msg []byte) string {
	controlID := ""
	if s.dataType == config.DataHL7 || s.dataType == "" {
		if m, err := hl7.Parse(msg); err == nil {
			if msh, ok := m.Segment("MSH", 1); ok {
				controlID = msh.Field(10).String()
			}
		}
	}
	if controlID == "" && (s.dataType == config.DataX12 || s.dataType == config.DataEDIFACT) {
		// The interchange's own reference (ISA13, UNB 0020), which is what a trading partner will quote.
		controlID = interchangeID(msg)
	}
	if controlID == "" {
		controlID = "noid-" + uniqueStamp()
	}
	out := s.fileName
	for k, v := range map[string]string{
		"${timestamp}": now.Format("20060102150405"), "${date}": now.Format("20060102"), "${control_id}": controlID,
		"${message_type}": name, "${destination}": s.name,
	} {
		out = strings.ReplaceAll(out, k, v)
	}
	out = sanitiseFileName(out)
	if filepath.Ext(out) == "" {
		out += "." + ext
	}
	return out
}

// writeOne writes a file for one message under a temporary name and renames it into place, so whatever collects the
// folder never reads half a message. A name already taken (the same control ID twice) gets a unique suffix rather than
// overwriting the message that was there.
func (s *FileSender) writeOne(final string, body []byte) error {
	if _, err := os.Lstat(final); err == nil {
		e := filepath.Ext(final)
		final = strings.TrimSuffix(final, e) + "-" + uniqueStamp() + e
	}
	temp := final + s.tempSuffix
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, final); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// prune deletes files older than retain_hours, at most every ten minutes. Only plain files directly in the folder:
// never a subfolder, and never anything a link points at.
func (s *FileSender) prune(now time.Time) {
	if s.retain <= 0 || now.Sub(s.lastPrune) < 10*time.Minute {
		return
	}
	s.lastPrune = now
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < s.retain {
			continue
		}
		p := filepath.Join(s.dir, e.Name())
		if f, ok := s.files[e.Name()]; ok {
			_ = f.Close()
			delete(s.files, e.Name())
		}
		_ = os.Remove(p)
	}
}

// describe picks the filename stem and extension for a message.
//
// Caller holds the mutex.
func (s *FileSender) describe(msg []byte) (name, ext string) {
	if s.dataType == config.DataDelimited {
		// One file per day rather than per message type, because a delimited row has no type - and "csv" so that whatever
		// picks the file up afterwards treats it as one. The delimiter may not be a comma, but every tool that opens a .csv
		// asks which delimiter it is and none of them can be told about an invented extension.
		return "rows", "csv"
	}

	if s.dataType == config.DataDICOM {
		return "objects", "dcm"
	}

	if s.dataType == config.DataNCPDP {
		// One file per day rather than per message, like delimited rows, because a claim transmission has no type to
		// discriminate on that an operator would recognise. ".ncpdp" rather than ".txt" so that whatever picks it up is
		// not tempted to open it in an editor that strips the non-printable separators - which silently destroys it.
		return "claims", "ncpdp"
	}

	if s.dataType == config.DataScript {
		return "prescriptions", "xml"
	}

	if s.dataType == config.DataEDIFACT {
		// By message type, as X12 is by transaction set: the ORDERS file, not everything the partner sent.
		if ic, err := edifact.Parse(msg); err == nil {
			seen := map[string]bool{}
			var types []string
			for _, m := range ic.AllMessages() {
				if t := m.Segments[0].Value(2, 1, 1); t != "" && !seen[t] {
					seen[t] = true
					types = append(types, t)
				}
			}
			if len(types) > 0 {
				return strings.Join(types, "_"), "edi"
			}
		}
		return "UNKNOWN", "edi"
	}

	if s.dataType == config.DataX12 {
		// The transaction set is the useful discriminator: an operator looking for
		// remittances wants the 835 file, not everything the partner sent today.
		if m, err := x12.Parse(msg); err == nil {
			if sets := m.TransactionSets(); len(sets) > 0 {
				return strings.Join(sets, "_"), "x12"
			}
		}
		return "UNKNOWN", "x12"
	}

	if s.dataType == config.DataHL7v3 {
		// The interaction is the useful discriminator, exactly as the transaction set is for X12: an operator looking
		// for query responses wants the PRPA_IN201306UV02 file rather than everything the peer sent today.
		//
		// Without this branch a v3 channel fell through to the v2 parser below, which cannot read XML - so every file
		// was named UNKNOWN and carried a .hl7 extension. Both were wrong in the way that costs somebody an hour:
		// the name told them nothing, and the extension told them something false, since anything that opens a .hl7
		// expects segments and finds a document.
		if m, err := hl7v3.Parse(msg); err == nil && m.InteractionID != "" {
			return m.InteractionID, "xml"
		}

		return "UNKNOWN", "xml"
	}

	if m, err := hl7.Parse(msg); err == nil {
		typ, event, _ := m.Type()
		if typ != "" {
			out := typ
			if event != "" {
				out += "_" + event
			}
			return out, "hl7"
		}
	}
	return "UNKNOWN", "hl7"
}

// Describe names the destination for logs.
func (s *FileSender) Describe() string { return "file " + s.dir }

// Close closes every open file.
func (s *FileSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for name, f := range s.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.files, name)
	}
	return firstErr
}

// sanitise keeps a message type safe to use as a filename. Message types arrive
// over the wire, so a sender must not be able to choose a path on our disk.
func sanitise(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && i < 32; i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "UNKNOWN"
	}
	return string(out)
}

// RoutingSenderFactory builds senders that can also route to other channels.
//
// Everything except a channel destination is built exactly as the default factory builds it. Wrapping
// rather than duplicating means a new destination type is available to a routing server the moment it
// exists, with nothing to remember.
func RoutingSenderFactory(router ChannelRouter) SenderFactory {
	return func(d config.Destination) (Sender, error) {
		if d.Type == config.DestinationChannel {
			return NewChannelSender(d, router)
		}
		return DefaultSenderFactory(d)
	}
}

// unbuiltScriptSender stands in for a script destination until the channel replaces it.
//
// It fails loudly rather than silently succeeding, so that a code path which somehow used it - a channel built without
// going through NewChannel, say - reports the reason rather than accepting messages and discarding them.
type unbuiltScriptSender struct{ name string }

func (u *unbuiltScriptSender) Send(context.Context, []byte) error {
	return fmt.Errorf("destination %q is a script destination that was never given its script engine; "+
		"the channel was constructed without NewChannel", u.name)
}

func (u *unbuiltScriptSender) Describe() string { return "a script (not yet built)" }
func (u *unbuiltScriptSender) Close() error     { return nil }
