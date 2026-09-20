// Package v2fhir maps HL7 v2 messages to FHIR resources.
//
// Mapping is where interoperability projects actually fail, so the rules this
// package follows are stated rather than implied:
//
//   - A value that cannot be mapped is preserved as text, never guessed at. A
//     local code turned into a plausible-looking standard code is worse than an
//     uncoded value, because the receiver believes it.
//   - Every mapping decision that involved judgement is reported as a note, so a
//     human can check the ones that matter instead of reading the whole output.
//   - Timestamps are converted, not copied. HL7 v2 timestamps are not ISO 8601,
//     and a dateTime carrying a local time with no offset is the single most
//     common reason a converted resource is rejected.
//   - Units keep their original text and gain a UCUM code only when the mapping
//     is certain. A wrong unit code is a patient-safety defect, not a formatting
//     problem.
package v2fhir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Options controls a conversion.
type Options struct {
	// Version is the FHIR release to produce. Defaults to fhir.DefaultVersion.
	Version fhir.Version

	// BaseURL is used to build absolute references in a bundle. When empty,
	// urn:uuid entries are used, which is what a transaction bundle wants.
	BaseURL string

	// AssigningAuthoritySystems maps an HL7 assigning authority to a URI, so an
	// MRN gets a real namespace instead of a bare value. Without this an
	// identifier is ambiguous between facilities.
	AssigningAuthoritySystems map[string]string

	// DefaultIdentifierSystem is used when an authority has no configured URI.
	// A urn:oid or a site URL is correct; leaving it empty is honest but makes
	// the identifier unmatchable.
	DefaultIdentifierSystem string

	// ClaimUSCore adds US Core profile URLs to meta.profile. Only set this when
	// the output has actually been checked against US Core, because asserting a
	// profile that does not hold is worse than asserting none.
	ClaimUSCore bool

	// Timezone is applied to v2 timestamps that carry no offset. HL7 v2 allows a
	// bare local time, and FHIR does not, so something has to supply it. Defaults
	// to UTC, which is at least explicit.
	Timezone *time.Location

	// IncludeProvenanceNotes records where each resource came from as a note.
	IncludeProvenanceNotes bool
}

func (o Options) version() fhir.Version {
	if o.Version == "" {
		// The release the structs populate, not the newest one. Converting to a release whose field spellings these structs do
		// not use produces a resource that declares one thing and carries another.
		return fhir.ResourceShapeVersion
	}
	return o.Version
}

func (o Options) location() *time.Location {
	if o.Timezone == nil {
		return time.UTC
	}
	return o.Timezone
}

// Note records a mapping decision that a human may want to check.
type Note struct {
	// Severity is "info", "warning" or "error".
	Severity string `json:"severity"`
	// Source is the HL7 path the note came from, such as "PID-8".
	Source string `json:"source,omitempty"`
	// Target is the FHIR path it affected.
	Target string `json:"target,omitempty"`
	// Message explains the decision.
	Message string `json:"message"`
}

// Result is the outcome of a conversion.
type Result struct {
	// Bundle holds every resource produced, as a transaction so the whole message
	// lands or none of it does.
	Bundle *fhir.Bundle `json:"-"`

	// Notes records mapping decisions worth checking.
	Notes []Note `json:"notes"`

	// MessageType and TriggerEvent identify what was converted.
	MessageType  string `json:"messageType"`
	TriggerEvent string `json:"triggerEvent"`

	// Version is the FHIR release produced.
	Version fhir.Version `json:"version"`
}

// Warnings returns only the notes that flag a possible problem.
func (r *Result) Warnings() []Note {
	var out []Note
	for _, n := range r.Notes {
		if n.Severity != "info" {
			out = append(out, n)
		}
	}
	return out
}

// ResourceCounts summarises what was produced, by type.
func (r *Result) ResourceCounts() map[string]int {
	out := map[string]int{}
	if r.Bundle == nil {
		return out
	}
	for _, e := range r.Bundle.Entry {
		if e.Resource != nil {
			out[e.Resource.ResourceTypeName()]++
		}
	}
	return out
}

// JSON renders the bundle.
func (r *Result) JSON() ([]byte, error) {
	if r.Bundle == nil {
		return nil, fmt.Errorf("v2fhir: no bundle was produced")
	}
	return fhir.MarshalBundleIndent(r.Bundle, r.Version)
}

// converter carries state for one message.
type converter struct {
	msg  *hl7.Message
	opts Options
	res  *Result

	// idSeed makes resource ids deterministic for the same input, so converting
	// the same message twice produces the same ids and a receiving server sees an
	// update rather than a duplicate.
	idSeed string
}

// Convert maps an HL7 v2 message to FHIR.
//
// The message type decides what is produced: ADT yields a Patient and an
// Encounter, ORU yields a DiagnosticReport with its Observations, ORM and OML
// yield a ServiceRequest.
func Convert(m *hl7.Message, opts Options) (*Result, error) {
	if m == nil {
		return nil, fmt.Errorf("v2fhir: no message")
	}

	msgType, event, _ := m.Type()
	res := &Result{
		MessageType:  msgType,
		TriggerEvent: event,
		Version:      opts.version(),
		// Empty rather than nil, so the wire carries [] and not null. A caller told to expect a list
		// should not have to handle two spellings of "nothing to report", and the case that produces
		// nothing is a message that mapped cleanly - the one least likely to be tried by hand.
		Notes: []Note{},
	}

	c := &converter{msg: m, opts: opts, res: res, idSeed: seedFor(m)}

	bundle := &fhir.Bundle{
		Type:      fhir.BundleTransaction,
		Timestamp: c.now(),
	}
	bundle.SetResourceID(c.deterministicID("Bundle", m.ControlID()))
	if id := m.ControlID(); id != "" {
		// Carrying the original control ID means a converted bundle can be traced
		// back to the v2 message it came from, which is the first thing anyone
		// asks during an incident.
		bundle.Identifier = &fhir.Identifier{
			System: "urn:ietf:rfc:3986",
			Value:  "urn:uuid:" + c.deterministicID("msg", id),
		}
	}
	res.Bundle = bundle

	switch strings.ToUpper(msgType) {
	case "ADT":
		c.convertADT()
	case "ORU":
		c.convertORU()
	case "ORM", "OML", "OMG":
		c.convertOrder()
	case "":
		return nil, fmt.Errorf("v2fhir: MSH-9 has no message type, so there is nothing to map")
	default:
		// Producing the patient is still useful, and saying what was skipped is
		// better than silently emitting a near-empty bundle.
		c.note("warning", "MSH-9", "",
			"message type %s is not mapped; only the patient was converted", msgType)
		if patient := c.buildPatient(); patient != nil {
			c.addEntry(patient, "Patient", patientConditionalURL(patient))
		}
	}

	return res, nil
}

func (c *converter) convertADT() {
	patient := c.buildPatient()
	if patient == nil {
		c.note("error", "PID", "Patient", "no PID segment, so no patient could be built")
		return
	}
	c.addEntry(patient, "Patient", patientConditionalURL(patient))

	// A28 and A31 are person-level messages with no visit, so an encounter would
	// be invented rather than mapped.
	event := strings.ToUpper(c.res.TriggerEvent)
	if event == "A28" || event == "A31" || event == "A29" || event == "A24" {
		c.note("info", "MSH-9.2", "",
			"%s is a person-level message with no visit, so no Encounter was created", event)
		return
	}

	if enc := c.buildEncounter(patient); enc != nil {
		c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
	} else {
		c.note("info", "PV1", "Encounter", "no PV1 segment, so no Encounter was created")
	}
}

func (c *converter) convertORU() {
	patient := c.buildPatient()
	if patient == nil {
		c.note("error", "PID", "Patient", "no PID segment, so results cannot be attached to a patient")
		return
	}
	c.addEntry(patient, "Patient", patientConditionalURL(patient))

	var encRef *fhir.Reference
	if enc := c.buildEncounter(patient); enc != nil {
		c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
		encRef = fhir.Ref("Encounter", enc.ID)
	}

	patientRef := fhir.Ref("Patient", patient.ID)

	// Each OBR is one report; the OBX segments that follow it are its results.
	obrs := c.msg.Segments("OBR")
	if len(obrs) == 0 {
		c.note("warning", "OBR", "DiagnosticReport",
			"no OBR segment, so observations were produced without a report to group them")
		for i := range c.msg.Segments("OBX") {
			if obs := c.buildObservation(i+1, patientRef, encRef, nil); obs != nil {
				c.addEntry(obs, "Observation", "")
			}
		}
		return
	}

	obxGroups := c.groupObservationsByOrder(len(obrs))

	for i := range obrs {
		report := c.buildDiagnosticReport(i+1, patientRef, encRef)
		if report == nil {
			continue
		}

		var specimenRef *fhir.Reference
		if spec := c.buildSpecimen(i+1, patientRef); spec != nil {
			c.addEntry(spec, "Specimen", "")
			specimenRef = fhir.Ref("Specimen", spec.ID)
			report.Specimen = []fhir.Reference{*specimenRef}
		}

		for _, obxIndex := range obxGroups[i] {
			obs := c.buildObservation(obxIndex, patientRef, encRef, specimenRef)
			if obs == nil {
				continue
			}
			c.addEntry(obs, "Observation", "")
			report.Result = append(report.Result, *fhir.Ref("Observation", obs.ID))
		}

		if len(report.Result) == 0 {
			c.note("warning", fmt.Sprintf("OBR(%d)", i+1), "DiagnosticReport.result",
				"this report has no observations attached")
		}
		c.addEntry(report, "DiagnosticReport", "")
	}
}

// groupObservationsByOrder assigns each OBX to the OBR that precedes it.
//
// The grouping has to come from segment order, because OBX-4 (the observation
// sub-identifier) is not reliably populated and OBR-1 is not always sequential.
// Getting this wrong attaches results to the wrong report.
func (c *converter) groupObservationsByOrder(orderCount int) [][]int {
	groups := make([][]int, orderCount)

	current := -1
	obxSeen := 0
	obrSeen := 0

	for i := 0; i < c.msg.SegmentCount(); i++ {
		seg, ok := c.msg.SegmentAt(i)
		if !ok {
			continue
		}
		switch seg.Name() {
		case "OBR":
			obrSeen++
			current = obrSeen - 1
		case "OBX":
			obxSeen++
			if current >= 0 && current < orderCount {
				groups[current] = append(groups[current], obxSeen)
			}
		}
	}
	return groups
}

func (c *converter) convertOrder() {
	patient := c.buildPatient()
	if patient == nil {
		c.note("error", "PID", "Patient", "no PID segment, so the order has no subject")
		return
	}
	c.addEntry(patient, "Patient", patientConditionalURL(patient))
	patientRef := fhir.Ref("Patient", patient.ID)

	var encRef *fhir.Reference
	if enc := c.buildEncounter(patient); enc != nil {
		c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
		encRef = fhir.Ref("Encounter", enc.ID)
	}

	obrs := c.msg.Segments("OBR")
	if len(obrs) == 0 {
		c.note("warning", "OBR", "ServiceRequest", "no OBR or ORC segment, so no order was produced")
		return
	}
	for i := range obrs {
		if sr := c.buildServiceRequest(i+1, patientRef, encRef); sr != nil {
			c.addEntry(sr, "ServiceRequest", "")
		}
	}
}

// addEntry appends a resource to the transaction bundle.
//
// A conditional URL turns the entry into an upsert: the server matches on the
// identifier and updates rather than creating a duplicate patient every time an
// A08 arrives. That single detail is the difference between a working feed and a
// registry with fourteen copies of the same person.
func (c *converter) addEntry(r fhir.Resource, resourceType, conditional string) {
	if c.opts.ClaimUSCore {
		if profile := fhir.USCoreProfile(resourceType); profile != "" {
			// The claim is checked before it is made.
			//
			// This used to stamp the profile URL unconditionally, which meant the assertion was only as true as the operator's
			// belief when they set the flag. The option's own documentation told them to enable it "only once the output has
			// actually been checked against US Core" and gave them nothing to check with. Something downstream reads meta.profile
			// and decides it may rely on the elements that profile guarantees - that is the whole purpose of the field - so an
			// unchecked claim is worse than no claim at all.
			//
			// A resource that does not conform is still converted and still sent. It simply does not carry an assertion that is
			// untrue. Dropping the message instead would turn a metadata problem into a lost patient record, and the reasons a
			// resource falls short - a feed that sends no identifier namespace, say - are usually not fixable at this end.
			if ok, result := fhir.ConformsToProfile(r, profile); ok {
				applyProfile(r, profile)
			} else {
				for _, f := range result.Findings {
					c.note("warning", "", f.Path,
						"no US Core claim was added to this %s because it does not conform: %s", resourceType, f.Message)
				}
			}
		}
	}

	entry := fhir.BundleEntry{
		FullURL:  c.fullURL(resourceType, r.ResourceID()),
		Resource: r,
		Request: &fhir.BundleRequest{
			Method: "PUT",
			URL:    fmt.Sprintf("%s/%s", resourceType, r.ResourceID()),
		},
	}
	if conditional != "" {
		// A conditional create leaves an existing record alone; a PUT by id
		// updates it. Using PUT with a deterministic id achieves the upsert
		// without relying on the server supporting conditional create.
		entry.Request.IfNoneExist = conditional
	}
	c.res.Bundle.Entry = append(c.res.Bundle.Entry, entry)
}

func (c *converter) fullURL(resourceType, id string) string {
	if c.opts.BaseURL != "" {
		return strings.TrimRight(c.opts.BaseURL, "/") + "/" + resourceType + "/" + id
	}
	return "urn:uuid:" + id
}

func (c *converter) note(severity, source, target, format string, args ...any) {
	c.res.Notes = append(c.res.Notes, Note{
		Severity: severity,
		Source:   source,
		Target:   target,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (c *converter) get(path string) string {
	v, err := c.msg.Get(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func (c *converter) now() string {
	return time.Now().In(c.opts.location()).Format(time.RFC3339)
}

// seedFor derives a stable seed from the message's identity, so ids are the same
// every time the same message is converted.
func seedFor(m *hl7.Message) string {
	sending := ""
	if msh, ok := m.Segment("MSH", 1); ok {
		sending = msh.Field(4).String()
	}
	return sending + "|" + m.ControlID()
}

// deterministicID hashes a kind and a natural key into a FHIR-safe id.
//
// Deterministic ids matter: a random id per conversion turns every retry into a
// new resource, and a feed that retries produces duplicates that then have to be
// merged by hand.
func (c *converter) deterministicID(kind, key string) string {
	if key == "" {
		key = c.idSeed
	}
	sum := sha256.Sum256([]byte(kind + "|" + key))
	return kind[:1] + hex.EncodeToString(sum[:8])
}

// applyProfile records a profile claim on a resource.
//
// This used to switch over each of the eight resource types, which meant a resource type added later would silently not be stamped -
// no error, just a missing claim. Meta is on the fhir.Resource interface now, so there is nothing to keep in step.
func applyProfile(r fhir.Resource, profile string) {
	r.SetResourceMeta(withProfile(r.ResourceMeta(), profile))
}

func withProfile(meta *fhir.Meta, profile string) *fhir.Meta {
	if meta == nil {
		meta = &fhir.Meta{}
	}
	for _, p := range meta.Profile {
		if p == profile {
			return meta
		}
	}
	meta.Profile = append(meta.Profile, profile)
	return meta
}

func patientConditionalURL(p *fhir.Patient) string {
	for _, id := range p.Identifier {
		if id.System != "" && id.Value != "" {
			return fmt.Sprintf("Patient?identifier=%s|%s", id.System, id.Value)
		}
	}
	return ""
}

func encounterConditionalURL(e *fhir.Encounter) string {
	for _, id := range e.Identifier {
		if id.System != "" && id.Value != "" {
			return fmt.Sprintf("Encounter?identifier=%s|%s", id.System, id.Value)
		}
	}
	return ""
}

// parseDecimal reads a numeric result value, tolerating the comparators v2 puts
// in the same field.
func parseDecimal(s string) (value float64, comparator string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, "", false
	}

	for _, prefix := range []string{"<=", ">=", "<", ">"} {
		if strings.HasPrefix(s, prefix) {
			comparator = prefix
			s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
			break
		}
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, "", false
	}
	return f, comparator, true
}
