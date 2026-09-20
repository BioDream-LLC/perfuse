package config

import (
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/delimited"
	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/ncpdp"
	"github.com/biodream-llc/perfuse/internal/x12"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
)

// Validation runs at load, not at first message. Every error names the file, the
// field and what would have to change, because the person reading it is often
// not the person who wrote the file.

// Validate checks a channel and compiles its expressions.
func (c *Channel) Validate() error {
	var errs []error

	if strings.TrimSpace(c.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if strings.ContainsAny(c.Name, "\r\n\t") {
		errs = append(errs, errors.New("name must not contain control characters"))
	}

	errs = append(errs, c.Source.validate()...)
	errs = append(errs, c.validateSourcePayloadAgreement()...)

	// Checked before the filter, transformations and scripts are compiled, because on
	// an X12 channel those are refused outright and compiling them first would report
	// two errors for one mistake.
	dataTypeErrs := c.validateDataType()
	errs = append(errs, dataTypeErrs...)
	hl7Semantics := c.Type() == DataHL7

	if c.Shadow != nil {
		if err := c.Shadow.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	if c.Filter != "" && hl7Semantics {
		compiled, err := expr.Parse(c.Filter)
		if err != nil {
			errs = append(errs, fmt.Errorf("filter: %w", err))
		} else {
			c.compiled = compiled
		}
	}

	// The v3 filter is compiled here for the same reason the v2 one is: a bad path or an uncompilable pattern
	// should stop the channel starting rather than fail on every message of a channel that is already running.
	//
	// Its own field rather than reusing c.compiled, because the two are different types evaluating against
	// different message models - and a single field would mean one of them being type-asserted at the point of
	// use, which is where a v2 channel would eventually be handed a v3 expression.
	if expr := c.HL7v3.FilterExpression(); expr != "" && c.Type() == DataHL7v3 {
		compiled, err := hl7v3.ParseFilter(expr)
		if err != nil {
			errs = append(errs, fmt.Errorf("hl7v3.filter: %w", err))
		} else {
			c.v3compiled = compiled
		}
	}

	// Transformations and scripts are compiled here rather than on first use, so
	// that a broken step or a syntax error stops the channel from starting
	// instead of surfacing when a patient is admitted.
	// Loaded before the transformations are compiled, because a step may name a table and compilation is what
	// resolves the name. Doing it the other way round reports "no such table" for every reference - which is
	// exactly what happened the first time this was run on a real channel.
	if len(c.Tables) > 0 {
		tables, err := loadCodeSets(c.Tables, filepath.Dir(c.path))
		if err != nil {
			errs = append(errs, err)
		} else {
			c.tables = tables
		}
	}

	if len(c.Transformations) > 0 && hl7Semantics {
		pipeline, err := compileTransformations(c.Transformations, c.tables)
		if err != nil {
			errs = append(errs, fmt.Errorf("transformations: %w", err))
		} else {
			c.pipeline = pipeline
		}
	}

	// The v3 steps are compiled here, after the tables, for the same reason as the v2 ones: a map step binds its table
	// now so that a channel naming a table that does not exist refuses to start. Looking it up per message would mean
	// either every message failing or - far worse - every message passing through untranslated.
	if c.Type() == DataHL7v3 && c.HL7v3 != nil && len(c.HL7v3.Transformations) > 0 {
		steps, err := hl7v3.CompileSteps(c.HL7v3.Transformations, c.tables)
		if err != nil {
			errs = append(errs, fmt.Errorf("hl7v3.transformations: %w", err))
		} else {
			c.HL7v3.compiled = steps
		}
	}

	// The imaging steps. Validated rather than compiled, because a named step carries no expression to parse - what can be wrong
	// with one is a malformed tag in a keep list, and that is a check rather than a compilation.
	if c.Type() == DataDICOM && c.Imaging != nil && len(c.Imaging.Transformations) > 0 {
		ok := true

		for i, st := range c.Imaging.Transformations {
			if err := st.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("dicom.transformations[%d]: %w", i, err))
				ok = false
			}
		}

		if ok {
			c.Imaging.compiled = c.Imaging.Transformations
		}
	}

	// The SCRIPT steps. Same reasoning as the v3 ones, and the same compiler, because a prescription and a v3 document are both
	// XML addressed by the same path grammar - internal/eprescribe already borrows hl7v3.Path for its filter.
	//
	// The v3-only steps are refused first, so that a channel using nullflavor is told what to use instead rather than being told
	// its step compiled and then having it apply an attribute a pharmacy system will not understand.
	if c.Type() == DataScript && c.Script != nil && len(c.Script.Transformations) > 0 {
		if stepErrs := validateScriptSteps(c.Script.Transformations); len(stepErrs) > 0 {
			errs = append(errs, stepErrs...)
		} else {
			steps, err := hl7v3.CompileSteps(c.Script.Transformations, c.tables)
			if err != nil {
				errs = append(errs, fmt.Errorf("script.transformations: %w", err))
			} else {
				c.Script.compiled = steps
			}
		}
	}

	// The X12 steps, same reasoning again: a map step binds its table now, so a channel naming one that does not exist
	// refuses to start rather than passing every claim through untranslated.
	if c.Type() == DataX12 && c.X12 != nil && len(c.X12.Transformations) > 0 {
		steps, err := x12.CompileSteps(c.X12.Transformations, c.tables)
		if err != nil {
			errs = append(errs, fmt.Errorf("x12.transformations: %w", err))
		} else {
			c.X12.compiled = steps
		}
	}

	// The pharmacy steps, for the same reason.
	if c.Type() == DataNCPDP && c.NCPDP != nil && len(c.NCPDP.Transformations) > 0 {
		steps, err := ncpdp.CompileSteps(c.NCPDP.Transformations, c.tables)
		if err != nil {
			errs = append(errs, fmt.Errorf("ncpdp.transformations: %w", err))
		} else {
			c.NCPDP.compiled = steps
		}
	}

	// The delimited steps and filter.
	//
	// Both compiled here rather than in datatype.go because c.tables is in scope here and a map step needs it. The filter has
	// no such need but is compiled alongside, so that a channel with an unparseable filter and valid steps fails on the
	// filter rather than starting and dropping every row.
	if c.Type() == DataDelimited && c.Delimited != nil {
		if len(c.Delimited.Transformations) > 0 {
			compiled, err := delimited.CompileSteps(c.Delimited.Transformations, c.tables)
			if err != nil {
				errs = append(errs, fmt.Errorf("delimited.transformations: %w", err))
			} else {
				c.Delimited.compiledSteps = compiled
			}
		}
		if c.Delimited.Filter != "" {
			compiled, err := delimited.ParseFilter(c.Delimited.Filter)
			if err != nil {
				errs = append(errs, fmt.Errorf("delimited.filter: %w", err))
			} else {
				c.Delimited.compiledFilter = compiled
			}
		}
	}

	// A destination response transformer is a script, so it makes the channel scripted just as a
	// channel-level script does. Without this, a channel whose only script was a response transformer
	// got no script engine, and the engine had to ask people to add an empty scripts block to make one
	// appear - which is a requirement with no meaning, invented to work around this line.
	needsEngine := !c.Scripts.Empty()
	for _, d := range c.Destinations {
		// A script destination needs an engine for exactly the same reason as a response transformer, and missing it
		// here produced exactly the same symptom: a channel whose file plainly contains a script refusing to start
		// because "the channel has no script engine". The comment above was written about the first instance of this
		// bug; this is the second, and the loop now covers both rather than one.
		if strings.TrimSpace(d.ResponseTransformer) != "" || d.Type == DestinationJavaScript {
			needsEngine = true
			break
		}
	}
	if needsEngine && c.Scripts == nil {
		c.Scripts = &Scripts{}
	}

	if c.Contract != nil {
		if err := c.Contract.load(filepath.Dir(c.path)); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, c.Attachments.Validate()...)

	// Compiled whenever this data type runs any script slot at all.
	//
	// # The gate has now been wrong twice, in the same way
	//
	// It was hl7Semantics, which meant a v3 channel with a script silently got no compiled script: it loaded, started, and ran
	// nothing. That was fixed by adding v3 to the condition - which fixed the one case in front of somebody and left the shape
	// of the bug in place.
	//
	// So when X12, raw, delimited, pharmacy and SCRIPT gained a preprocessor, they inherited it. The handlers invoked the
	// preprocessor correctly, the loader validated the script correctly, and nothing compiled it, so PreprocessorScript
	// returned nil and the call did nothing. Three correct pieces and no working feature.
	//
	// Derived from scriptSlotsRun rather than a list of type names, because that table is already the answer to "does this
	// format run scripts" and a second list would eventually disagree with it. Adding a format to the table now makes its
	// scripts compile, and there is no separate place to forget.
	if needsEngine && len(scriptSlotsRun[c.Type()]) > 0 {
		notes, err := c.Scripts.compile(c.Name, filepath.Dir(c.path))
		if err != nil {
			errs = append(errs, fmt.Errorf("scripts: %w", err))
		} else {
			c.scriptNotes = notes
		}
	}

	if len(c.Destinations) == 0 {
		// A channel with nowhere to send is almost always an unfinished edit. It
		// would otherwise start, acknowledge every message and discard the lot.
		errs = append(errs, errors.New("at least one destination is required"))
	}

	seen := map[string]bool{}
	for i := range c.Destinations {
		d := &c.Destinations[i]
		label := d.Name
		if label == "" {
			label = fmt.Sprintf("destination %d", i+1)
		}

		if strings.TrimSpace(d.Name) == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", label))
		} else if seen[d.Name] {
			errs = append(errs, fmt.Errorf("destination name %q is used more than once", d.Name))
		}
		seen[d.Name] = true

		for _, err := range d.validate(c.Type()) {
			errs = append(errs, fmt.Errorf("destination %q: %w", label, err))
		}
	}

	if c.IsEnabled() && len(c.EnabledDestinations()) == 0 {
		errs = append(errs, errors.New("channel is enabled but every destination is disabled"))
	}

	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Path: c.path, Channel: c.Name, Errors: errs}
}

func (s *Source) validate() []error {
	var errs []error

	for _, err := range s.TLS.Validate(true) {
		errs = append(errs, fmt.Errorf("source: %w", err))
	}

	switch s.Type {
	case "":
		errs = append(errs, errors.New("source.type is required (mllp)"))
	case SourceHTTP:
		errs = append(errs, s.HTTP.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to an http source; use http.listen"))
		}

	case SourceBroker:
		errs = append(errs, s.Broker.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a broker source; it connects out to the broker rather than listening"))
		}

	case SourceDICOMQuery:
		errs = append(errs, s.DICOMQuery.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a dicom_query source; it polls an archive rather than listening"))
		}

	case SourceDICOM:
		errs = append(errs, s.DICOM.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a dicom source; use dicom.listen"))
		}

	case SourceSOAP:
		errs = append(errs, s.SOAP.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a soap source; use soap.listen"))
		}

	case SourceSerial:
		if s.Serial == nil {
			errs = append(errs, errors.New("a serial source needs a serial block with a port and a baud rate"))
			break
		}
		errs = append(errs, s.Serial.Validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a serial source, which reads a device rather than listening"))
		}

	case SourceFTP:
		if s.FTP == nil {
			errs = append(errs, errors.New("an ftp source needs an ftp block with a host"))
			break
		}
		if err := s.FTP.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to an ftp source, which polls rather than listens"))
		}

	case SourceSMB:
		if s.SMB == nil {
			errs = append(errs, errors.New("an smb source needs an smb block with a host and a share"))
			break
		}
		if err := s.SMB.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to an smb source, which polls rather than listens"))
		}

	case SourceWebDAV:
		if s.WebDAV == nil {
			errs = append(errs, errors.New("a webdav source needs a webdav block with a url"))
			break
		}
		if err := s.WebDAV.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a webdav source, which polls rather than listens"))
		}

	case SourceTCP:
		if s.TCP == nil {
			errs = append(errs, errors.New(
				"a tcp source needs a tcp block saying what to listen on and how messages are framed"))
			break
		}
		errs = append(errs, s.TCP.Validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a tcp source; use tcp.listen"))
		}

	case SourceFile:
		if s.File == nil {
			errs = append(errs, errors.New(
				"a file source needs a file block with a root directory to read"))
			break
		}
		if err := s.File.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a file source, which polls a directory rather than listening"))
		}

	case SourceSFTP:
		if s.SFTP == nil {
			errs = append(errs, errors.New(
				"an sftp source needs an sftp block with a host, user and dir"))
			break
		}
		if err := s.SFTP.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to an sftp source, which polls rather than listens"))
		}

	case SourceDatabase:
		if s.Database == nil {
			errs = append(errs, errors.New(
				"a database source needs a database block with a driver, dsn and query"))
			break
		}
		if err := s.Database.Validate(); err != nil {
			errs = append(errs, err)
		}
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a database source, which polls rather "+
					"than listens"))
		}

	case SourceJavaScript:
		if s.JavaScript == nil {
			errs = append(errs, errors.New(
				"a javascript source needs a javascript block with a script"))
			break
		}
		errs = append(errs, s.JavaScript.validate()...)
		if s.Listen != "" {
			errs = append(errs, errors.New(
				"source.listen does not apply to a javascript source; it runs a script on a timer rather than listening"))
		}

	case SourceMLLP:
		if strings.TrimSpace(s.Listen) == "" {
			errs = append(errs, errors.New("source.listen is required, for example \":6661\""))
		} else if err := validateListenAddr(s.Listen); err != nil {
			errs = append(errs, fmt.Errorf("source.listen: %w", err))
		}
	default:
		errs = append(errs, fmt.Errorf(
			"source.type %q is not supported; use mllp, tcp, http, file, database, sftp, ftp, smb, webdav, "+
				"soap, dicom, dicom_query, broker, serial or javascript", s.Type))
	}

	if s.Type != SourceSerial && s.Serial != nil {
		errs = append(errs, errors.New("a serial block only applies to a serial source"))
	}
	if s.Type != SourceFTP && s.FTP != nil {
		errs = append(errs, errors.New("an ftp block only applies to an ftp source"))
	}
	if s.Type != SourceSMB && s.SMB != nil {
		errs = append(errs, errors.New("an smb block only applies to an smb source"))
	}
	if s.Type != SourceWebDAV && s.WebDAV != nil {
		errs = append(errs, errors.New("a webdav block only applies to a webdav source"))
	}
	if s.Type != SourceTCP && s.TCP != nil {
		errs = append(errs, errors.New("a tcp block only applies to a tcp source"))
	}
	if s.Type != SourceFile && s.File != nil {
		errs = append(errs, errors.New("a file block only applies to a file source"))
	}
	if s.Type != SourceSFTP && s.SFTP != nil {
		errs = append(errs, errors.New("an sftp block only applies to an sftp source"))
	}
	if s.Type != SourceDatabase && s.Database != nil {
		errs = append(errs, errors.New("a database block only applies to a database source"))
	}
	if s.Type != SourceHTTP && s.HTTP != nil {
		errs = append(errs, errors.New("an http block only applies to an http source"))
	}
	if s.Type != SourceJavaScript && s.JavaScript != nil {
		errs = append(errs, errors.New("a javascript block only applies to a javascript source"))
	}

	if s.MaxMessageSize < 0 {
		errs = append(errs, errors.New("source.max_message_size must not be negative"))
	}
	if s.IdleTimeout < 0 {
		errs = append(errs, errors.New("source.idle_timeout must not be negative"))
	}
	if s.MaxConnections < 0 {
		errs = append(errs, errors.New("source.max_connections must not be negative"))
	}

	switch s.Ack.When {
	case "", AckOnReceipt, AckOnDelivery:
	default:
		errs = append(errs, fmt.Errorf(
			"source.ack.when %q is not valid; use on_receipt or on_delivery", s.Ack.When))
	}

	return errs
}

// validate checks one destination.
//
// dataType decides how a destination filter is compiled. Passed in rather than read from a back-pointer, because a
// destination filter is written in the paths of whichever format the channel carries, and compiling an X12 filter as HL7
// would either fail for the wrong reason or - worse - succeed, since CLM and ISA are plausible HL7 segment names.
func (d *Destination) validate(dataType DataType) []error {
	var errs []error

	switch d.Type {
	case "":
		errs = append(errs, errors.New("type is required (mllp or file)"))

	case DestinationMLLP:
		if strings.TrimSpace(d.Address) == "" {
			errs = append(errs, errors.New("address is required for an mllp destination"))
		} else if err := validateDialAddr(d.Address); err != nil {
			errs = append(errs, fmt.Errorf("address: %w", err))
		}
		if d.Dir != "" {
			errs = append(errs, errors.New("dir does not apply to an mllp destination"))
		}

	case DestinationFile:
		if strings.TrimSpace(d.Dir) == "" {
			errs = append(errs, errors.New("dir is required for a file destination"))
		}
		if d.Address != "" {
			errs = append(errs, errors.New("address does not apply to a file destination"))
		}

	case DestinationFHIR:
		if d.FHIR == nil {
			errs = append(errs, errors.New("a fhir destination needs a fhir block with a url"))
			break
		}
		errs = append(errs, d.FHIR.validate()...)
		if d.Address != "" {
			errs = append(errs, errors.New("address does not apply to a fhir destination; use fhir.url"))
		}
		if d.Dir != "" {
			errs = append(errs, errors.New("dir does not apply to a fhir destination"))
		}

	case DestinationHTTP:
		errs = append(errs, d.HTTP.validate()...)
		if d.Address != "" {
			errs = append(errs, errors.New(
				"address does not apply to an http destination; use http.url"))
		}

	case DestinationS3:
		errs = append(errs, validateS3Dest(d)...)

	case DestinationFTP:
		errs = append(errs, validateFTPDest(d)...)

	case DestinationDocument:
		errs = append(errs, validateDocumentDest(d)...)

	case DestinationDICOM:
		errs = append(errs, validateDICOMDest(d)...)

	case DestinationJavaScript:
		errs = append(errs, validateJavaScriptDest(d)...)

	case DestinationBroker:
		errs = append(errs, validateBrokerDest(d)...)

	case DestinationSOAP:
		errs = append(errs, validateSOAPDest(d)...)

	case DestinationTCP:
		if d.TCP == nil {
			errs = append(errs, errors.New(
				"a tcp destination needs a tcp block saying the address and how messages are framed"))
			break
		}
		errs = append(errs, d.TCP.Validate()...)
		if d.Address != "" && d.TCP.Address == "" {
			// Accepted rather than refused: address is where every other socket destination puts it, so somebody
			// putting it there is following the pattern rather than making a mistake.
			d.TCP.Address = d.Address
		}

	case DestinationSFTP:
		if d.SFTP == nil {
			errs = append(errs, errors.New(
				"an sftp destination needs an sftp block with a host, user and dir"))
			break
		}
		if err := d.SFTP.Validate(); err != nil {
			errs = append(errs, err)
		}
		if d.Dir != "" {
			errs = append(errs, errors.New(
				"dir does not apply to an sftp destination; use sftp.dir"))
		}

	case DestinationDatabase:
		if d.Database == nil {
			errs = append(errs, errors.New(
				"a database destination needs a database block with a driver, dsn and statement"))
			break
		}
		if err := d.Database.Validate(); err != nil {
			errs = append(errs, err)
		}
		if d.Address != "" {
			errs = append(errs, errors.New(
				"address does not apply to a database destination; use database.dsn"))
		}

	case DestinationCDA:
		if d.CDA == nil {
			errs = append(errs, errors.New("a cda destination needs a cda block with a url or a dir"))
			break
		}
		errs = append(errs, d.CDA.validate()...)
		if d.Address != "" {
			errs = append(errs, errors.New("address does not apply to a cda destination; use cda.url"))
		}
		if d.Dir != "" {
			errs = append(errs, errors.New("dir does not apply to a cda destination; use cda.dir"))
		}

	case DestinationSMTP:
		errs = append(errs, d.validateSMTP()...)
		if d.Address != "" {
			errs = append(errs, errors.New("address does not apply to an smtp destination; use smtp.host"))
		}
		if d.Dir != "" {
			errs = append(errs, errors.New("dir does not apply to an smtp destination"))
		}

	case DestinationChannel:
		errs = append(errs, validateChannelDest(d)...)
		if d.Address != "" {
			errs = append(errs, errors.New(
				"address does not apply to a channel destination; a routed message does not travel "+
					"over the network, it is handed straight to the other channel"))
		}
		if d.Dir != "" {
			errs = append(errs, errors.New("dir does not apply to a channel destination"))
		}

	default:
		// The list is built from the constants rather than written out here.
		//
		// It used to be a hand-written sentence naming nine types while seventeen were supported, so somebody who mistyped s3 or
		// soap was told it was not a supported transport - which is a message that sends them to look for a missing feature rather
		// than at their own spelling. A list of names beside the names it describes will drift; this one cannot.
		errs = append(errs, fmt.Errorf("type %q is not supported; use %s", d.Type, strings.Join(destinationTypeNames(), ", ")))
	}

	if d.Type != DestinationFHIR && d.FHIR != nil {
		errs = append(errs, errors.New("a fhir block only applies to a fhir destination"))
	}
	if d.Type != DestinationCDA && d.CDA != nil {
		errs = append(errs, errors.New("a cda block only applies to a cda destination"))
	}
	if d.Type != DestinationHTTP && d.HTTP != nil {
		errs = append(errs, errors.New("an http block only applies to an http destination"))
	}
	if d.Type != DestinationSFTP && d.SFTP != nil {
		errs = append(errs, errors.New("an sftp block only applies to an sftp destination"))
	}
	if d.Type != DestinationDatabase && d.Database != nil {
		errs = append(errs, errors.New("a database block only applies to a database destination"))
	}
	if d.Type != DestinationSMTP && d.SMTP != nil {
		errs = append(errs, errors.New("an smtp block only applies to an smtp destination"))
	}
	if d.Type != DestinationChannel && d.Channel != nil {
		errs = append(errs, errors.New("a channel block only applies to a channel destination"))
	}

	// A response transformer on a destination that never receives a reply would be a script that never
	// runs on a channel whose file plainly contains it. Refused, with the list of destinations that do
	// get a reply, because the useful thing to say is which ones this works on.
	if strings.TrimSpace(d.ResponseTransformer) != "" {
		// The list has to match which senders actually implement engine.Responder, and it did not.
		//
		// It said mllp, http and fhir. In fact only MLLP and SOAP return a reply a script can read: http and fhir
		// were accepted here and then refused when the channel started, and soap - which does work - was refused
		// here and so could never be used at all.
		//
		// Wrong in both directions, which is the shape of a list maintained by hand next to the thing it describes.
		// There is now a test in internal/engine that walks the sender factory and fails if the two disagree.
		switch d.Type {
		case DestinationMLLP, DestinationSOAP:
		default:
			errs = append(errs, fmt.Errorf(
				"destination %q is a %s destination, which receives no reply to inspect, so a "+
					"response_transformer would never run; it applies to mllp and soap destinations",
				d.Name, d.Type))
		}
	}

	// Egress, with the default policy.
	//
	// Checked during ordinary validation rather than only when a stricter policy is configured, because the addresses
	// blocked by default are never a message destination: an instance metadata service holds this machine's own
	// credentials, and a SOAP destination with a response transformer can read a reply back into a script.
	errs = append(errs, d.checkEgress(egress.Default)...)

	errs = append(errs, d.Queue.validate()...)

	for _, err := range d.TLS.Validate(false) {
		errs = append(errs, fmt.Errorf("destination %q: %w", d.Name, err))
	}

	if d.Filter != "" {
		switch dataType {
		case DataX12:
			compiled, err := x12.ParseFilter(d.Filter)
			if err != nil {
				errs = append(errs, fmt.Errorf("filter: %w", err))
			} else {
				d.x12compiled = compiled
			}
		case DataNCPDP:
			compiled, err := ncpdp.ParseFilter(d.Filter)
			if err != nil {
				errs = append(errs, fmt.Errorf("filter: %w", err))
			} else {
				d.ncpdpcompiled = compiled
			}
		default:
			// HL7 v2, and every type that refuses destination filters at load - for those the refusal is reported
			// elsewhere and compiling here would add a second error for one mistake.
			compiled, err := expr.Parse(d.Filter)
			if err != nil {
				errs = append(errs, fmt.Errorf("filter: %w", err))
			} else {
				d.compiled = compiled
			}
		}
	}

	if d.Timeout < 0 {
		errs = append(errs, errors.New("timeout must not be negative"))
	}
	if d.Retry.Attempts < 0 {
		errs = append(errs, errors.New("retry.attempts must not be negative"))
	}
	if d.Retry.Backoff < 0 {
		errs = append(errs, errors.New("retry.backoff must not be negative"))
	}
	if d.Retry.MaxBackoff < 0 {
		errs = append(errs, errors.New("retry.max_backoff must not be negative"))
	}
	if d.Retry.MaxBackoff > 0 && d.Retry.Backoff > d.Retry.MaxBackoff {
		errs = append(errs, errors.New("retry.backoff is larger than retry.max_backoff"))
	}

	return errs
}

func (f *FHIRDestination) validate() []error {
	var errs []error

	if strings.TrimSpace(f.URL) == "" {
		errs = append(errs, errors.New("fhir.url is required"))
	} else {
		u, err := url.Parse(f.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("fhir.url %q is not an absolute URL", f.URL))
		} else if u.Scheme != "https" && u.Scheme != "http" {
			errs = append(errs, fmt.Errorf("fhir.url scheme %q is not http or https", u.Scheme))
		} else if u.Scheme == "http" && !isLocalHost(u.Hostname()) {
			// Clinical data over plain HTTP to a remote host is a decision, not a
			// default. Saying so at load beats discovering it in a packet capture.
			errs = append(errs, fmt.Errorf(
				"fhir.url uses plain http to %s: patient data would cross the network unencrypted; use https",
				u.Hostname()))
		}
	}

	if f.Version != "" {
		if _, err := fhir.ParseVersion(f.Version); err != nil {
			errs = append(errs, fmt.Errorf("fhir.version: %w", err))
		}
	}

	if f.Timezone != "" {
		if _, err := time.LoadLocation(f.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("fhir.timezone %q is not a known location", f.Timezone))
		}
	}

	if f.DefaultIdentifierSystem == "" && len(f.IdentifierSystems) == 0 {
		// Not an error, because a test deployment may not care, but it is the
		// single most common cause of unmatchable patients downstream.
		errs = append(errs, errors.New(
			"fhir needs default_identifier_system or identifier_systems: without a system URI, an MRN is ambiguous between facilities"))
	}

	return errs
}

// isLocalHost reports whether a host is on this machine, where plain HTTP is not
// a network exposure.
func isLocalHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// Resolved returns the destination with defaults filled in, so the runtime never
// has to ask whether a zero means "zero" or "unset".
func (d Destination) Resolved() Destination {
	if d.Timeout == 0 {
		d.Timeout = DefaultDestTimeout
	}
	if d.Retry.Attempts == 0 {
		d.Retry.Attempts = DefaultRetryAttempts
	}
	if d.Retry.Backoff == 0 {
		d.Retry.Backoff = DefaultRetryBackoff
	}
	if d.Retry.MaxBackoff == 0 {
		d.Retry.MaxBackoff = DefaultRetryMaxBackoff
	}
	if d.Retry.MaxBackoff < d.Retry.Backoff {
		d.Retry.MaxBackoff = d.Retry.Backoff
	}
	return d
}

// AckWhen returns the configured acknowledgement timing with its default applied.
func (s Source) AckWhen() AckWhen {
	if s.Ack.When == "" {
		return AckOnDelivery
	}
	return s.Ack.When
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port", addr)
	}
	if port == "" {
		return errors.New("a port is required")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return fmt.Errorf("%q is not a valid port", port)
	}
	// An empty host is correct for a listener: it means every interface.
	if host != "" && net.ParseIP(host) == nil && !isHostname(host) {
		return fmt.Errorf("%q is not a valid address", host)
	}
	return nil
}

func validateDialAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port", addr)
	}
	if host == "" {
		return errors.New("a host is required")
	}
	if port == "" {
		return errors.New("a port is required")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return fmt.Errorf("%q is not a valid port", port)
	}
	if net.ParseIP(host) == nil && !isHostname(host) {
		return fmt.Errorf("%q is not a valid host", host)
	}
	return nil
}

// isHostname checks the shape of a name without resolving it. Resolution at load
// time would make a channel file fail to load because DNS was briefly down,
// which is not the same thing as the file being wrong.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// ValidationError collects everything wrong with one channel, so a person fixing
// a file sees every problem at once instead of one per run.
type ValidationError struct {
	Path    string
	Channel string
	Errors  []error
}

func (e *ValidationError) Error() string {
	var sb strings.Builder
	where := e.Path
	if where == "" {
		where = "channel"
	}
	if e.Channel != "" {
		fmt.Fprintf(&sb, "%s (channel %q) is not valid:", where, e.Channel)
	} else {
		fmt.Fprintf(&sb, "%s is not valid:", where)
	}
	for _, err := range e.Errors {
		fmt.Fprintf(&sb, "\n  - %s", err)
	}
	return sb.String()
}

func (e *ValidationError) Unwrap() []error { return e.Errors }

// ValidateDestinationForTest runs destination validation and returns the problems.
//
// Exported for a drift guard in internal/engine that checks the response-transformer list against the senders that
// actually implement a reply. That guard has to ask the real validator rather than copy its list, because a copy drifts
// the same way the original did - which is exactly what it exists to catch.
//
// Named ForTest so its purpose is unmistakable, and returning the problems rather than a bool so a caller can look for a
// specific one instead of guessing why validation failed.
//
// Checked as HL7 v2, which is what its callers are testing. A test needing another format's destination filter should load
// a channel of that type, so that the data type and the filter cannot disagree.
func ValidateDestinationForTest(d *Destination) []error {
	if d == nil {
		return []error{errors.New("no destination")}
	}
	return d.validate(DataHL7)
}

// validateSourcePayloadAgreement refuses a source that produces a payload the channel cannot handle.
//
// Both directions matter, and both fail silently otherwise.
//
// A raw source on an HL7 channel delivers a PDF into a path that tries to parse it, so every file is rejected as
// unparseable. The log says the file is not HL7, which is true and beside the point: nobody claimed it was.
//
// An HL7-splitting source on a raw channel is worse, because it appears to work. The source splits a file into messages
// on each MSH and the channel delivers each one without looking, so a batch of forty arrives as forty separate payloads
// when the configuration says the file should have gone across whole.
func (c *Channel) validateSourcePayloadAgreement() []error {
	var errs []error

	raw := c.Type() == DataRaw

	sourceRaw, sourceHasFileSemantics := false, false
	switch {
	case c.Source.File != nil:
		sourceRaw, sourceHasFileSemantics = c.Source.File.Raw, true
	case c.Source.SFTP != nil:
		sourceHasFileSemantics = true
	}

	if !sourceHasFileSemantics {
		return nil
	}

	// Only HL7 v2 wants a file split into messages, and that is the whole basis of the disagreement below.
	//
	// filePoller.split finds message boundaries by looking for MSH, which is meaningful for v2 and meaningless for every
	// other format: a CSV batch, an X12 interchange, a pharmacy transmission, a CDA document and a DICOM object are each one
	// payload per file with no internal MSH to find. The split function's own comment says so, naming a CSV batch
	// specifically.
	//
	// So raw on the source is not a mistake for those formats, it is the correct and only working setting - and this
	// validator used to refuse it, which made a delimited channel unable to read from a file source at all. Without raw the
	// poller reported "no message could be found in it" on every CSV; with raw the loader refused to start. Two pieces of
	// code with contradictory beliefs about the same configuration, and the format most likely to arrive as a file drop was
	// the one that could not.
	wantsMessageSplitting := c.Type() == DataHL7

	if sourceRaw && wantsMessageSplitting {
		errs = append(errs, fmt.Errorf("the source has raw set, so each file is delivered whole and unparsed, but "+
			"dataType is %q, which expects a file to be split into messages at each MSH segment. Either remove raw, or "+
			"set dataType: raw if the payload is not HL7", c.Type()))
	}

	if raw && !sourceRaw {
		errs = append(errs, fmt.Errorf("dataType is raw, so the channel delivers payloads without parsing them, but "+
			"the source does not have raw set, so it will still split each file into messages at every MSH segment. "+
			"A batch of forty would arrive as forty payloads rather than one file. Set raw on the source as well"))
	}

	return errs
}
