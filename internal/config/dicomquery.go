package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// DICOMQuerySource polls an archive with C-FIND and emits a message per new study.
//
// Mirth has no equivalent connector. Its DICOM support is a listener and a sender - it can receive images pushed at it and
// push them on, and that is all. Asking an archive what it holds has been requested on their forums since 2015.
//
// What makes it worth having is not the query, it is the automation: a prefetch that runs at three in the morning, a
// reconciliation that notices the archive is missing a study the RIS says exists, a worklist built from what was actually
// scanned rather than what was ordered. Those are jobs sites currently do with a scheduled script outside the engine, which
// means they are invisible to it - no retries, no alerting, no message history.
type DICOMQuerySource struct {
	// Address is the archive's host and port. Required.
	//
	// Named to match the DICOM destination rather than shortened. The two were "address" and "addr" for an afternoon, which
	// is the sort of inconsistency somebody hand-writing a channel file gets wrong once and then distrusts the whole
	// format over.
	Address string `yaml:"address"`

	// CalledAE is the archive's AE title. Required in practice: most archives refuse an association addressed to
	// anything else, and that refusal reads as an outage rather than as a configuration mistake.
	CalledAE string `yaml:"called_ae"`

	// CallingAE is the AE title Perfuse presents. Archives commonly use it for access control.
	CallingAE string `yaml:"calling_ae,omitempty"`

	// Level is the query level: PATIENT, STUDY, SERIES or IMAGE. Defaults to STUDY.
	//
	// STUDY is the default because it is what almost every useful query wants and because the alternatives surprise
	// people: IMAGE level against a large CT returns one response per slice, which is hundreds per study.
	Level string `yaml:"level,omitempty"`

	// PatientRoot uses the patient root information model rather than study root.
	PatientRoot bool `yaml:"patient_root,omitempty"`

	// Match are the query keys. A value filters; an empty value asks for the field to be returned.
	//
	// Keys are named rather than numbered - "PatientID" not "0010,0020" - because a channel file is read by people, and a
	// tag number in a configuration file is a lookup every reader has to perform.
	Match map[string]string `yaml:"match,omitempty"`

	// Return are fields to bring back without filtering on them.
	//
	// Separate from Match with an empty value, even though they encode identically, because the intent differs and a
	// reader can see which keys are narrowing the search and which are being collected.
	Return []string `yaml:"return,omitempty"`

	// Interval is how often to poll. Required.
	Interval time.Duration `yaml:"interval"`

	// Window is how far back each poll looks, as a study date range. Zero disables date filtering entirely.
	//
	// A window is what keeps this bounded. Without one, every poll asks the archive for everything it has ever held and
	// then discards what it has seen before - which works on a test archive and is antisocial against a real one holding
	// millions of studies.
	Window time.Duration `yaml:"window,omitempty"`

	// Overlap re-queries this far into the already-polled period. Defaults to one hour.
	//
	// Necessary rather than cautious. A study can be registered with yesterday's date, our clock and the archive's need
	// not agree, and a poll boundary that lines up exactly with an arrival loses it. The overlap re-asks, and the
	// already-seen check stops the duplicate - so the cost of overlapping is a slightly larger query and the cost of not
	// overlapping is a silently missed study.
	Overlap time.Duration `yaml:"overlap,omitempty"`

	// Limit caps the matches accepted from one poll. Defaults to 500.
	//
	// A cap rather than no cap because a mistyped match key turns this into "give me everything", and the first symptom
	// of that against a real archive is the archive's administrator asking who is hammering it.
	Limit int `yaml:"limit,omitempty"`

	// Timeout bounds one poll. Defaults to two minutes.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// TLS wraps the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// EmitOnFirstPoll sends messages for everything the first poll finds. Defaults to false.
	//
	// False matters. Pointing this at an archive that already holds a million studies and having it emit a message for
	// each is not a useful first run, and it is the kind of mistake that is noticed downstream rather than here. The first
	// poll records what exists and emits nothing; the second poll onwards reports what is new.
	EmitOnFirstPoll bool `yaml:"emit_on_first_poll,omitempty"`
}

// DICOM query defaults.
const (
	DefaultQueryOverlap = time.Hour
	DefaultQueryLimit   = 500
	DefaultQueryTimeout = 2 * time.Minute
)

// ResolvedLevel is the query level with its default applied.
func (q *DICOMQuerySource) ResolvedLevel() string {
	if q == nil || strings.TrimSpace(q.Level) == "" {
		return "STUDY"
	}
	return strings.ToUpper(strings.TrimSpace(q.Level))
}

// ResolvedOverlap is the overlap with its default applied.
func (q *DICOMQuerySource) ResolvedOverlap() time.Duration {
	if q == nil || q.Overlap <= 0 {
		return DefaultQueryOverlap
	}
	return q.Overlap
}

// ResolvedLimit is the limit with its default applied.
func (q *DICOMQuerySource) ResolvedLimit() int {
	if q == nil || q.Limit <= 0 {
		return DefaultQueryLimit
	}
	return q.Limit
}

// ResolvedTimeout is the timeout with its default applied.
func (q *DICOMQuerySource) ResolvedTimeout() time.Duration {
	if q == nil || q.Timeout <= 0 {
		return DefaultQueryTimeout
	}
	return q.Timeout
}

func (q *DICOMQuerySource) validate() []error {
	if q == nil {
		return []error{errors.New("a dicom_query source needs a dicom_query block")}
	}

	var errs []error

	if strings.TrimSpace(q.Address) == "" {
		errs = append(errs, fmt.Errorf("needs dicom_query.address, the archive to query"))
	}
	if strings.TrimSpace(q.CalledAE) == "" {
		errs = append(errs, fmt.Errorf("needs dicom_query.called_ae; most archives refuse an association "+
			"addressed to a different AE title, and that refusal looks like the archive being down"))
	}
	if len(q.CalledAE) > 16 || len(q.CallingAE) > 16 {
		errs = append(errs, fmt.Errorf("has an AE title longer than 16 characters, which is the maximum the "+
			"wire format carries; it would be truncated and the archive would be configured with a name we never send"))
	}

	switch q.ResolvedLevel() {
	case "PATIENT":
		if !q.PatientRoot {
			errs = append(errs, fmt.Errorf("queries at PATIENT level, which is only defined under the "+
				"patient root information model; set dicom_query.patient_root"))
		}
	case "STUDY", "SERIES", "IMAGE":
	default:
		errs = append(errs, fmt.Errorf("has dicom_query.level %q; use PATIENT, STUDY, SERIES or IMAGE", q.Level))
	}

	if q.Interval <= 0 {
		errs = append(errs, fmt.Errorf("needs dicom_query.interval, how often to poll"))
	}
	if q.Interval > 0 && q.Interval < 10*time.Second {
		// Refused rather than allowed. An association per poll is not free at the archive end, and a one-second interval
		// against a hospital PACS is indistinguishable from an attack from the archive's point of view.
		errs = append(errs, fmt.Errorf("polls every %s; each poll opens an association, and anything under "+
			"ten seconds will be read as an attack by the archive rather than as a query", q.Interval))
	}

	if q.Window < 0 {
		errs = append(errs, fmt.Errorf("has a negative dicom_query.window"))
	}
	if q.Window > 0 && q.Window < q.Interval {
		// A window shorter than the interval leaves gaps between polls that nothing ever looks at, which loses studies
		// silently. Worth refusing, because the arithmetic is easy to get wrong and the symptom is absence.
		errs = append(errs, fmt.Errorf("looks back %s but polls every %s, so the period between polls is "+
			"never queried and studies arriving in it are missed; the window must be at least the interval", q.Window, q.Interval))
	}

	if q.Overlap < 0 {
		errs = append(errs, fmt.Errorf("has a negative dicom_query.overlap"))
	}

	if len(q.Match) == 0 && len(q.Return) == 0 {
		errs = append(errs, fmt.Errorf("has a dicom_query source with no match or return keys, so it would "+
			"ask the archive for nothing in particular and get back rows with no fields in them"))
	}

	for name := range q.Match {
		if !KnownDICOMKeyword(name) {
			errs = append(errs, fmt.Errorf("matches on dicom key %q, which is not a keyword this "+
				"understands; %s", name, dicomKeywordHint()))
		}
	}
	for _, name := range q.Return {
		if !KnownDICOMKeyword(name) {
			errs = append(errs, fmt.Errorf("asks for dicom key %q, which is not a keyword this "+
				"understands; %s", name, dicomKeywordHint()))
		}
	}

	if q.TLS != nil {
		for _, err := range q.TLS.Validate(false) {
			errs = append(errs, fmt.Errorf("dicom_query tls: %w", err))
		}
	}

	return errs
}
