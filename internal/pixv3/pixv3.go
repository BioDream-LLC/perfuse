// Package pixv3 is the HL7 version 3 face of the patient index: the IHE Patient Identifier Cross-referencing and Patient
// Demographics Query profiles as most hospitals outside the FHIR world still speak them.
//
//   - ITI-44, Patient Identity Feed HL7 V3: a source adds a patient (PRPA_IN201301UV02), revises one (PRPA_IN201302UV02)
//     or says two records are the same person (PRPA_IN201304UV02). Answered with MCCI_IN000002UV01.
//   - ITI-45, PIXV3 Query: the other identifiers of the person holding one (PRPA_IN201309UV02 -> PRPA_IN201310UV02).
//   - ITI-47, PDQV3 Query: the people matching some demographics (PRPA_IN201305UV02 -> PRPA_IN201306UV02), with
//     continuation (QUQI_IN000003UV01) and cancel (QUQI_IN000003UV01_Cancel).
//
// The answers come from the same patient index as PIXm, PDQm, Patient/$match and the index screen, so a record fed in v3
// is found over FHIR and the reverse. The messages are written by hand, in schema order, for the reason hl7v3.Ack gives.
package pixv3

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/natid"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// The interactions, which are also the SOAP actions with urn:hl7-org:v3: before them.
const (
	FeedAdd           = "PRPA_IN201301UV02"
	FeedRevise        = "PRPA_IN201302UV02"
	FeedMerge         = "PRPA_IN201304UV02"
	PIXQuery          = "PRPA_IN201309UV02"
	PIXResponse       = "PRPA_IN201310UV02"
	PDQQuery          = "PRPA_IN201305UV02"
	PDQResponse       = "PRPA_IN201306UV02"
	PDQContinue       = "QUQI_IN000003UV01"
	PDQCancel         = "QUQI_IN000003UV01_Cancel"
	Acknowledgement   = "MCCI_IN000002UV01"
	ActionPrefix      = "urn:hl7-org:v3:"
	errUnknownKey     = "204" // HL7 table 0357: Unknown Key Identifier
	errRequiredAbsent = "101" // HL7 table 0357: Required field missing
	errCodeSystem     = "2.16.840.1.113883.12.357"
)

// Index is what the service needs of the patient index: *mpi.Store.
type Index interface {
	Settings(ctx context.Context, tenant string) (mpi.Settings, error)
	ByIdentifier(ctx context.Context, tenant, ident string) (*mpi.Person, error)
	KnowsSystem(ctx context.Context, tenant, system string) (bool, error)
	Find(ctx context.Context, tenant string, q mpi.Record, set mpi.Settings, limit int) ([]mpi.Match, error)
	RegisterWithSettings(ctx context.Context, tenant, actor string, r mpi.Record) (*mpi.Outcome, error)
	MergeIdentifiers(ctx context.Context, tenant, actor, fromIdent, intoIdent, why string) (bool, error)
}

// Service answers the three transactions for one patient index.
type Service struct {
	Index Index
	// Now overrides the clock, for tests.
	Now func() time.Time

	mu      sync.Mutex
	pending map[string]*pdqResult // PDQ continuations by query id
}

// Request is one received interaction, with who sent it.
type Request struct {
	Tenant string
	Actor  string // the account, for the index's history
	Body   []byte // the interaction's XML: the SOAP body's first element
}

// Answer is the interaction to send back and its SOAP action, plus a line for the audit log.
type Answer struct {
	Action  string
	Body    []byte
	Target  string // what the request was about, for the audit log: an identifier, never demographics
	Outcome string // ok, not-found or refused
}

// Handle answers any of the interactions this package speaks. An error is a request that is not one of them at all,
// which the caller answers with a SOAP fault; everything else, refusals included, is an HL7 answer.
func (s *Service) Handle(ctx context.Context, req Request) (*Answer, error) {
	msg, err := hl7v3.Parse(req.Body)
	if err != nil {
		return nil, err
	}
	switch msg.InteractionID {
	case FeedAdd, FeedRevise, FeedMerge:
		return s.feed(ctx, req, msg)
	case PIXQuery:
		return s.pix(ctx, req, msg)
	case PDQQuery:
		return s.pdq(ctx, req, msg)
	case PDQContinue, PDQCancel:
		return s.pdqContinue(ctx, req, msg)
	}
	return nil, fmt.Errorf("%s is not a patient identity feed, PIX query or PDQ query", msg.InteractionID)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---- identifiers --------------------------------------------------------------------------------------------------

// systemOf is the index's system for a v3 root: the OID as urn:oid:, then the national scheme it belongs to, if any.
func systemOf(root string, set mpi.Settings) string {
	sys := "urn:oid:" + root
	if strings.HasPrefix(root, "urn:") {
		sys = root
	}
	if c := natid.Canonicalise(sys, "0", set.NationalSystems); c.Scheme != nil {
		return c.System
	}
	return sys
}

// identOf is an identifier as the index stores it.
func identOf(id hl7v3.II, set mpi.Settings) string {
	c := natid.Canonicalise(systemOf(id.Root, set), id.Extension, set.NationalSystems)
	return c.System + "|" + c.Value
}

// rootOf is the OID a system is written as in v3, or "" when it has none: v3 identifies an authority by OID only, so an
// identifier under a URL with no OID behind it cannot be given (the FHIR answers carry it).
func rootOf(system string) string {
	if oid, ok := strings.CutPrefix(system, "urn:oid:"); ok {
		return oid
	}
	if sch, ok := natid.ForSystem(system); ok {
		for _, a := range sch.Aliases {
			if oid, ok := strings.CutPrefix(a, "urn:oid:"); ok {
				return oid
			}
			if isOID(a) {
				return a
			}
		}
	}
	if isOID(system) {
		return system
	}
	return ""
}

func isOID(s string) bool {
	if !strings.Contains(s, ".") {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// ownOID is this index's own OID in v3, the enterprise identifier's system when that is an OID, else "" (written as an
// unknown null flavour, which the schemas allow and a receiver can see is not an invented value).
func ownOID(set mpi.Settings) string { return rootOf(set.EUIDSystem) }

type v3id struct{ root, ext string }

// personIDs is every identifier of a person v3 can carry, the enterprise identifier first when it has an OID.
func personIDs(p *mpi.Person, set mpi.Settings) []v3id {
	var out []v3id
	seen := map[string]bool{}
	for _, ident := range p.Identifiers(set.EUIDSystem) {
		sys, val, _ := strings.Cut(ident, "|")
		root := rootOf(sys)
		if root == "" || val == "" || seen[root+"|"+val] {
			continue
		}
		seen[root+"|"+val] = true
		out = append(out, v3id{root, val})
	}
	return out
}

// ---- writing ------------------------------------------------------------------------------------------------------

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func idXML(tag, root, ext string) string {
	switch {
	case root == "":
		return "<" + tag + ` nullFlavor="UNK"/>`
	case ext == "":
		return "<" + tag + ` root="` + esc(root) + `"/>`
	}
	return "<" + tag + ` root="` + esc(root) + `" extension="` + esc(ext) + `"/>`
}

func iiXML(tag string, id hl7v3.II) string {
	if id.Presence != hl7v3.Present {
		return "<" + tag + ` nullFlavor="NI"/>`
	}
	return idXML(tag, id.Root, id.Extension)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// detail is an acknowledgementDetail: an error code from HL7 table 0357 and where in the request it points.
type detail struct{ code, text, location string }

// wrapper writes the transmission wrapper of an answer to msg, up to and including the acknowledgement; the caller adds
// the control act and closes the root element.
func (s *Service) wrapper(b *strings.Builder, interaction string, msg *hl7v3.Message, ackType string, details []detail) {
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString("<" + interaction + ` xmlns="urn:hl7-org:v3" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" ITSVersion="XML_1.0">`)
	b.WriteString(idXML("id", newUUID(), ""))
	b.WriteString(`<creationTime value="` + s.now().UTC().Format("20060102150405") + `"/>`)
	b.WriteString(`<interactionId root="2.16.840.1.113883.1.6" extension="` + strings.TrimSuffix(interaction, "_Cancel") + `"/>`)
	pc := msg.ProcessingCode
	if pc == "" {
		pc = "P"
	}
	b.WriteString(`<processingCode code="` + esc(pc) + `"/><processingModeCode code="T"/><acceptAckCode code="NE"/>`)
	// The answer goes back to the device that asked, from the device it asked.
	device := func(role, tc string, d hl7v3.Device) {
		b.WriteString("<" + role + ` typeCode="` + tc + `"><device classCode="DEV" determinerCode="INSTANCE">`)
		b.WriteString(iiXML("id", d.ID))
		b.WriteString("</device></" + role + ">")
	}
	device("receiver", "RCV", msg.Sender)
	device("sender", "SND", msg.Receiver)
	b.WriteString(`<acknowledgement><typeCode code="` + ackType + `"/><targetMessage>` + iiXML("id", msg.ID) + `</targetMessage>`)
	for _, d := range details {
		b.WriteString(`<acknowledgementDetail typeCode="E"><code code="` + d.code + `" codeSystem="` + errCodeSystem + `"/>`)
		if d.text != "" {
			b.WriteString("<text>" + esc(d.text) + "</text>")
		}
		if d.location != "" {
			b.WriteString("<location>" + esc(d.location) + "</location>")
		}
		b.WriteString("</acknowledgementDetail>")
	}
	b.WriteString("</acknowledgement>")
}

// echo writes an element of the request back, as a query response repeats its query: element names without prefixes,
// attributes without namespaces (xsi:type kept), text escaped.
func echo(b *strings.Builder, n *xtree.Node) {
	if n == nil {
		return
	}
	b.WriteString("<" + n.Name)
	for _, a := range n.Attrs {
		switch a.Namespace {
		case "":
			b.WriteString(" " + a.Name + `="` + esc(a.Value) + `"`)
		case "http://www.w3.org/2001/XMLSchema-instance":
			b.WriteString(" xsi:" + a.Name + `="` + esc(a.Value) + `"`)
		}
	}
	if len(n.Children) == 0 && n.Text == "" {
		b.WriteString("/>")
		return
	}
	b.WriteString(">")
	if len(n.Children) == 0 {
		b.WriteString(esc(n.Text))
	}
	for _, c := range n.Children {
		echo(b, c)
	}
	b.WriteString("</" + n.Name + ">")
}

// ---- ITI-44 ------------------------------------------------------------------------------------------------------

// recordFrom reads the patient of a feed as an index record: its first identifier is the record's own, the rest are
// identifiers it carries.
func recordFrom(p *hl7v3.Patient, set mpi.Settings, source string) (mpi.Record, error) {
	var r mpi.Record
	for _, id := range p.IDs {
		if id.Presence != hl7v3.Present || id.Root == "" || id.Extension == "" {
			continue
		}
		ident := identOf(id, set)
		if r.System == "" {
			r.System, r.Value, _ = strings.Cut(ident, "|")
			continue
		}
		r.Identifiers = append(r.Identifiers, ident)
	}
	if r.System == "" {
		return r, errors.New("the patient has no identifier with both a root and an extension")
	}
	// Identifiers the source holds for the patient in other domains (asOtherIDs): a social security number, another
	// hospital's number. They are what the index cross-references by, as the IHE examples show.
	if p.Node != nil {
		for _, other := range p.Node.All("asOtherIDs") {
			for _, n := range other.All("id") {
				if id := hl7v3.ParseII(n); id.Presence == hl7v3.Present && id.Root != "" && id.Extension != "" {
					r.Identifiers = append(r.Identifiers, identOf(id, set))
				}
			}
		}
	}
	n := p.PrimaryName()
	r.Family = strings.Join(n.Family, " ")
	r.Given = strings.Join(n.Given, " ")
	if p.Gender.Presence == hl7v3.Present {
		r.Sex = strings.ToUpper(p.Gender.Code)
	}
	if bt := digitsOnly(p.BirthTime.Raw); len(bt) >= 8 {
		r.BirthDate = bt[:4] + "-" + bt[4:6] + "-" + bt[6:8]
	}
	if a := p.HomeAddress(); a.Presence == hl7v3.Present {
		r.Line, r.City, r.Postal = strings.Join(a.StreetLines, ", "), a.City, a.PostalCode
	}
	for _, t := range p.Telecoms {
		if t.Presence == hl7v3.Present && strings.EqualFold(t.Scheme, "tel") && t.Value != "" {
			r.Phone = t.Value
			break
		}
	}
	r.Source = source
	return r, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (s *Service) feed(ctx context.Context, req Request, msg *hl7v3.Message) (*Answer, error) {
	set, err := s.Index.Settings(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	ans := &Answer{Action: ActionPrefix + Acknowledgement, Outcome: "ok"}
	refuse := func(code, text, location string) (*Answer, error) {
		var b strings.Builder
		s.wrapper(&b, Acknowledgement, msg, "AE", []detail{{code, text, location}})
		b.WriteString("</" + Acknowledgement + ">")
		ans.Body, ans.Outcome = []byte(b.String()), "refused"
		return ans, nil
	}
	p := msg.ExtractPatient()
	if p == nil {
		return refuse(errRequiredAbsent, "the message names no patient", "//registrationEvent/subject1/patient")
	}
	source := "pixv3"
	if msg.Sender.ID.Root != "" {
		source = "pixv3:" + msg.Sender.ID.Root
	}
	rec, err := recordFrom(p, set, source)
	if err != nil {
		return refuse(errRequiredAbsent, err.Error(), "//registrationEvent/subject1/patient/id")
	}
	ans.Target = rec.Key()
	if _, err := s.Index.RegisterWithSettings(ctx, req.Tenant, req.Actor, rec); err != nil {
		return refuse("207", "the patient index could not take the record: "+err.Error(), "")
	}
	if msg.InteractionID == FeedMerge {
		// The surviving record is the patient; the one it replaces is the prior registration's.
		var prior *xtree.Node
		if rep := msg.Root.Find("replacementOf"); rep != nil {
			if pr := rep.Find("priorRegisteredRole"); pr != nil {
				prior = pr.First("id")
			}
		}
		old := hl7v3.ParseII(prior)
		if old.Presence != hl7v3.Present || old.Extension == "" {
			return refuse(errRequiredAbsent, "a merge names the record it replaces in replacementOf/priorRegistration",
				"//replacementOf/priorRegistration/subject1/priorRegisteredRole/id")
		}
		from := identOf(old, set)
		if _, err := s.Index.MergeIdentifiers(ctx, req.Tenant, req.Actor, from, rec.Key(), "PIX V3 merge from "+source); err != nil {
			return refuse(errUnknownKey, "the record to merge could not be merged: "+err.Error(),
				"//replacementOf/priorRegistration/subject1/priorRegisteredRole/id")
		}
		ans.Target = from + " -> " + rec.Key()
	}
	var b strings.Builder
	s.wrapper(&b, Acknowledgement, msg, "AA", nil)
	b.WriteString("</" + Acknowledgement + ">")
	ans.Body = []byte(b.String())
	return ans, nil
}

// ---- ITI-45 ------------------------------------------------------------------------------------------------------

func (s *Service) pix(ctx context.Context, req Request, msg *hl7v3.Message) (*Answer, error) {
	set, err := s.Index.Settings(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	q := msg.ExtractQuery()
	ans := &Answer{Action: ActionPrefix + PIXResponse, Outcome: "ok"}
	write := func(ackType, code string, details []detail, p *mpi.Person, ids []v3id) *Answer {
		var b strings.Builder
		s.wrapper(&b, PIXResponse, msg, ackType, details)
		b.WriteString(`<controlActProcess classCode="CACT" moodCode="EVN"><code code="PRPA_TE201310UV02" codeSystem="2.16.840.1.113883.1.18"/>`)
		if p != nil && len(ids) > 0 {
			b.WriteString(`<subject typeCode="SUBJ"><registrationEvent classCode="REG" moodCode="EVN"><statusCode code="active"/>`)
			b.WriteString(`<subject1 typeCode="SBJ"><patient classCode="PAT">`)
			for _, id := range ids {
				b.WriteString(idXML("id", id.root, id.ext))
			}
			b.WriteString(`<statusCode code="active"/><patientPerson classCode="PSN" determinerCode="INSTANCE"><name nullFlavor="NA"/></patientPerson>`)
			b.WriteString(`</patient></subject1>`)
			b.WriteString(`<custodian typeCode="CST"><assignedEntity classCode="ASSIGNED">` + idXML("id", ownOID(set), "") + `</assignedEntity></custodian>`)
			b.WriteString(`</registrationEvent></subject>`)
		}
		b.WriteString("<queryAck>")
		if q != nil {
			b.WriteString(iiXML("queryId", q.ID))
		} else {
			b.WriteString(`<queryId nullFlavor="NI"/>`)
		}
		b.WriteString(`<queryResponseCode code="` + code + `"/></queryAck>`)
		if q != nil {
			echo(&b, q.Node)
		}
		b.WriteString("</controlActProcess></" + PIXResponse + ">")
		ans.Body = []byte(b.String())
		return ans
	}
	if q == nil {
		ans.Outcome = "refused"
		return write("AE", "AE", []detail{{errRequiredAbsent, "the query has no queryByParameter", "//queryByParameter"}}, nil, nil), nil
	}
	pidParam, ok := q.Parameter("patientIdentifier")
	var pid hl7v3.II
	if ok {
		pid = hl7v3.ParseII(pidParam.Node.First("value"))
	}
	if pid.Presence != hl7v3.Present || pid.Root == "" || pid.Extension == "" {
		ans.Outcome = "refused"
		return write("AE", "AE", []detail{{errRequiredAbsent, "the query names no patient identifier (root and extension)",
			"/PRPA_IN201309UV02/controlActProcess/queryByParameter/parameterList/patientIdentifier/value"}}, nil, nil), nil
	}
	ident := identOf(pid, set)
	ans.Target = ident
	// The domains asked for: each must be one the index knows (case 5), and only identifiers in them are returned.
	wanted := map[string]bool{}
	var unknown []detail
	n := 0
	for _, prm := range q.Parameters {
		if prm.Name != "dataSource" {
			continue
		}
		n++
		root := hl7v3.ParseII(prm.Node.First("value")).Root
		if root == "" {
			continue
		}
		wanted[root] = true
		known, err := s.Index.KnowsSystem(ctx, req.Tenant, systemOf(root, set))
		if err != nil {
			return nil, err
		}
		if !known && root != ownOID(set) {
			unknown = append(unknown, detail{errUnknownKey, "the index knows no identifiers from " + root,
				fmt.Sprintf("/PRPA_IN201309UV02/controlActProcess/queryByParameter/parameterList/dataSource[%d]/value", n)})
		}
	}
	if len(unknown) > 0 {
		ans.Outcome = "refused"
		return write("AE", "AE", unknown, nil, nil), nil
	}
	p, err := s.Index.ByIdentifier(ctx, req.Tenant, ident)
	if errors.Is(err, mpi.ErrNotFound) {
		ans.Outcome = "not-found"
		return write("AE", "AE", []detail{{errUnknownKey, "the patient identifier is not known",
			"/PRPA_IN201309UV02/controlActProcess/queryByParameter/parameterList/patientIdentifier/value"}}, nil, nil), nil
	}
	if err != nil {
		return nil, err
	}
	var ids []v3id
	for _, id := range personIDs(p, set) {
		if (id.root == pid.Root && id.ext == pid.Extension) || (len(wanted) > 0 && !wanted[id.root]) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ans.Outcome = "not-found"
		return write("AA", "NF", nil, nil, nil), nil
	}
	return write("AA", "OK", nil, p, ids), nil
}

// ---- ITI-47 ------------------------------------------------------------------------------------------------------

type pdqResult struct {
	tenant  string
	set     mpi.Settings
	query   *xtree.Node
	queryID hl7v3.II
	matches []mpi.Match
	domains map[string]bool
	sent    int
	expires time.Time
}

const pdqKeep = 10 * time.Minute

// criteria reads a demographic query into a record the index can match, and the domains asked for.
func criteria(q *hl7v3.Query, set mpi.Settings) (rec mpi.Record, domains map[string]bool, minScore int, used int) {
	domains = map[string]bool{}
	for _, prm := range q.Parameters {
		v := prm.Node.First("value")
		if v == nil {
			continue
		}
		switch prm.Name {
		case "livingSubjectName":
			n := hl7v3.ParseName(v)
			if rec.Family == "" && len(n.Family) > 0 {
				rec.Family, used = strings.Join(n.Family, " "), used+1
			}
			if rec.Given == "" && len(n.Given) > 0 {
				rec.Given, used = strings.Join(n.Given, " "), used+1
			}
		case "livingSubjectAdministrativeGender":
			if c := hl7v3.ParseCoded(v); c.Code != "" {
				rec.Sex, used = strings.ToUpper(c.Code), used+1
			}
		case "livingSubjectBirthTime":
			if d := digitsOnly(hl7v3.TimestampOf(v).Raw); len(d) >= 8 {
				rec.BirthDate, used = d[:4]+"-"+d[4:6]+"-"+d[6:8], used+1
			}
		case "livingSubjectId", "patientIdentifier":
			if id := hl7v3.ParseII(v); id.Root != "" && id.Extension != "" {
				rec.Identifiers, used = append(rec.Identifiers, identOf(id, set)), used+1
			}
		case "patientAddress":
			a := hl7v3.ParseAddress(v)
			if len(a.StreetLines) > 0 {
				rec.Line = strings.Join(a.StreetLines, ", ")
			}
			if a.City != "" || a.PostalCode != "" || len(a.StreetLines) > 0 {
				rec.City, rec.Postal, used = a.City, a.PostalCode, used+1
			}
		case "patientTelecom":
			if t := hl7v3.ParseTelecom(v); t.Value != "" {
				rec.Phone, used = t.Value, used+1
			}
		case "otherIDsScopingOrganization":
			if root := hl7v3.ParseII(v).Root; root != "" {
				domains[root] = true
			}
		}
	}
	if mc := q.Node.Find("minimumDegreeMatch"); mc != nil {
		if v := mc.First("value"); v != nil {
			minScore, _ = strconv.Atoi(v.AttrValue("value"))
		}
	}
	// The record's own identifier must be set for the index's matcher; an identifier asked for goes there.
	if len(rec.Identifiers) > 0 {
		rec.System, rec.Value, _ = strings.Cut(rec.Identifiers[0], "|")
		rec.Identifiers = rec.Identifiers[1:]
	}
	return rec, domains, minScore, used
}

func (s *Service) pdq(ctx context.Context, req Request, msg *hl7v3.Message) (*Answer, error) {
	set, err := s.Index.Settings(ctx, req.Tenant)
	if err != nil {
		return nil, err
	}
	q := msg.ExtractQuery()
	ans := &Answer{Action: ActionPrefix + PDQResponse, Outcome: "ok"}
	if q == nil {
		ans.Outcome = "refused"
		ans.Body = s.pdqAnswer(msg, nil, nil, "AE", []detail{{errRequiredAbsent, "the query has no queryByParameter", "//queryByParameter"}}, set)
		return ans, nil
	}
	rec, domains, minScore, used := criteria(q, set)
	if used == 0 {
		ans.Outcome = "refused"
		ans.Body = s.pdqAnswer(msg, &pdqResult{query: q.Node, queryID: q.ID}, nil, "AE", []detail{{errRequiredAbsent,
			"the query names no demographics this supplier matches on (name, gender, birth time, identifier, address, telecom)",
			"/PRPA_IN201305UV02/controlActProcess/queryByParameter/parameterList"}}, set)
		return ans, nil
	}
	ans.Target = "PDQ query " + q.ID.Root + "^" + q.ID.Extension
	n := 0
	var unknown []detail
	for _, prm := range q.Parameters {
		if prm.Name != "otherIDsScopingOrganization" {
			continue
		}
		n++
		root := hl7v3.ParseII(prm.Node.First("value")).Root
		known, err := s.Index.KnowsSystem(ctx, req.Tenant, systemOf(root, set))
		if err != nil {
			return nil, err
		}
		if !known && root != ownOID(set) {
			unknown = append(unknown, detail{errUnknownKey, "the index knows no identifiers from " + root,
				fmt.Sprintf("/PRPA_IN201305UV02/controlActProcess/queryByParameter/parameterList/otherIDsScopingOrganization[%d]/value", n)})
		}
	}
	if len(unknown) > 0 {
		ans.Outcome = "refused"
		ans.Body = s.pdqAnswer(msg, &pdqResult{query: q.Node, queryID: q.ID}, nil, "AE", unknown, set)
		return ans, nil
	}
	found, err := s.Index.Find(ctx, req.Tenant, rec, set, 100)
	if err != nil {
		return nil, err
	}
	res := &pdqResult{tenant: req.Tenant, set: set, query: q.Node, queryID: q.ID, domains: domains}
	for _, m := range found {
		if int(math.Round(m.Score*100)) < minScore || len(idsFor(m.Person, set, domains)) == 0 {
			continue
		}
		res.matches = append(res.matches, m)
	}
	sort.SliceStable(res.matches, func(i, j int) bool { return res.matches[i].Score > res.matches[j].Score })
	page := len(res.matches)
	if iq, err := strconv.Atoi(q.InitialQuantity); err == nil && iq > 0 && iq < page {
		page = iq
	}
	if len(res.matches) == 0 {
		ans.Outcome = "not-found"
	}
	ans.Body = s.pdqAnswer(msg, res, res.matches[:page], "AA", nil, set)
	res.sent = page
	if page < len(res.matches) && q.ID.Presence == hl7v3.Present {
		res.expires = s.now().Add(pdqKeep)
		s.keep(q.ID, res)
	}
	return ans, nil
}

func idsFor(p *mpi.Person, set mpi.Settings, domains map[string]bool) []v3id {
	var out []v3id
	for _, id := range personIDs(p, set) {
		if len(domains) == 0 || domains[id.root] {
			out = append(out, id)
		}
	}
	return out
}

func queryKey(id hl7v3.II) string { return id.Root + "^" + id.Extension }

func (s *Service) keep(id hl7v3.II, r *pdqResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]*pdqResult{}
	}
	now := s.now()
	for k, v := range s.pending {
		if now.After(v.expires) {
			delete(s.pending, k)
		}
	}
	s.pending[queryKey(id)] = r
}

// pdqAnswer writes PRPA_IN201306UV02. res carries the query to echo and the counts; page is the matches sent now.
func (s *Service) pdqAnswer(msg *hl7v3.Message, res *pdqResult, page []mpi.Match, ackType string, details []detail, set mpi.Settings) []byte {
	var b strings.Builder
	s.wrapper(&b, PDQResponse, msg, ackType, details)
	b.WriteString(`<controlActProcess classCode="CACT" moodCode="EVN"><code code="PRPA_TE201306UV02" codeSystem="2.16.840.1.113883.1.18"/>`)
	for _, m := range page {
		g := m.Person.Golden()
		b.WriteString(`<subject typeCode="SUBJ"><registrationEvent classCode="REG" moodCode="EVN"><id nullFlavor="NA"/><statusCode code="active"/>`)
		b.WriteString(`<subject1 typeCode="SBJ"><patient classCode="PAT">`)
		for _, id := range idsFor(m.Person, set, res.domains) {
			b.WriteString(idXML("id", id.root, id.ext))
		}
		b.WriteString(`<statusCode code="active"/><patientPerson classCode="PSN" determinerCode="INSTANCE">`)
		if g.Family != "" || g.Given != "" {
			b.WriteString("<name>")
			for _, part := range strings.Fields(g.Given) {
				b.WriteString("<given>" + esc(part) + "</given>")
			}
			if g.Family != "" {
				b.WriteString("<family>" + esc(g.Family) + "</family>")
			}
			b.WriteString("</name>")
		} else {
			b.WriteString(`<name nullFlavor="UNK"/>`)
		}
		if g.Phone != "" {
			tel := g.Phone
			if !strings.Contains(tel, ":") {
				tel = "tel:" + tel
			}
			b.WriteString(`<telecom value="` + esc(tel) + `"/>`)
		}
		if g.Sex != "" {
			b.WriteString(`<administrativeGenderCode code="` + esc(g.Sex) + `" codeSystem="2.16.840.1.113883.5.1"/>`)
		}
		if d := digitsOnly(g.BirthDate); len(d) >= 8 {
			b.WriteString(`<birthTime value="` + d[:8] + `"/>`)
		}
		if g.Line != "" || g.City != "" || g.Postal != "" {
			b.WriteString("<addr>")
			if g.Line != "" {
				b.WriteString("<streetAddressLine>" + esc(g.Line) + "</streetAddressLine>")
			}
			if g.City != "" {
				b.WriteString("<city>" + esc(g.City) + "</city>")
			}
			if g.Postal != "" {
				b.WriteString("<postalCode>" + esc(g.Postal) + "</postalCode>")
			}
			b.WriteString("</addr>")
		}
		b.WriteString(`</patientPerson><subjectOf1><queryMatchObservation classCode="COND" moodCode="EVN"><code code="IHE_PDQ"/>`)
		b.WriteString(`<value xsi:type="INT" value="` + strconv.Itoa(int(math.Round(m.Score*100))) + `"/></queryMatchObservation></subjectOf1>`)
		b.WriteString(`</patient></subject1>`)
		b.WriteString(`<custodian typeCode="CST"><assignedEntity classCode="ASSIGNED">` + idXML("id", ownOID(set), "") + `</assignedEntity></custodian>`)
		b.WriteString(`</registrationEvent></subject>`)
	}
	b.WriteString("<queryAck>")
	if res != nil {
		b.WriteString(iiXML("queryId", res.queryID))
	} else {
		b.WriteString(`<queryId nullFlavor="NI"/>`)
	}
	code := "OK"
	switch {
	case ackType != "AA":
		code = "AE"
	case res == nil || len(res.matches) == 0:
		code = "NF"
	}
	b.WriteString(`<queryResponseCode code="` + code + `"/>`)
	if ackType == "AA" && res != nil {
		left := len(res.matches) - res.sent - len(page)
		b.WriteString(`<resultTotalQuantity value="` + strconv.Itoa(len(res.matches)) + `"/>`)
		b.WriteString(`<resultCurrentQuantity value="` + strconv.Itoa(len(page)) + `"/>`)
		b.WriteString(`<resultRemainingQuantity value="` + strconv.Itoa(max(left, 0)) + `"/>`)
	}
	b.WriteString("</queryAck>")
	if res != nil {
		echo(&b, res.query)
	}
	b.WriteString("</controlActProcess></" + PDQResponse + ">")
	return []byte(b.String())
}

func (s *Service) pdqContinue(_ context.Context, req Request, msg *hl7v3.Message) (*Answer, error) {
	var qid hl7v3.II
	var qty int
	if cont := msg.Root.Find("queryContinuation"); cont != nil {
		qid = hl7v3.ParseII(cont.First("queryId"))
		if c := cont.First("continuationQuantity"); c != nil {
			qty, _ = strconv.Atoi(c.AttrValue("value"))
		}
	}
	s.mu.Lock()
	res := s.pending[queryKey(qid)]
	if res != nil && (res.tenant != req.Tenant || s.now().After(res.expires)) {
		res = nil
	}
	if res != nil && msg.InteractionID == PDQCancel {
		delete(s.pending, queryKey(qid))
	}
	s.mu.Unlock()
	if msg.InteractionID == PDQCancel {
		// A cancel is acknowledged whether or not anything was held: the result is gone either way.
		var b strings.Builder
		s.wrapper(&b, Acknowledgement, msg, "AA", nil)
		b.WriteString("</" + Acknowledgement + ">")
		return &Answer{Action: ActionPrefix + Acknowledgement, Body: []byte(b.String()), Target: "PDQ cancel " + queryKey(qid), Outcome: "ok"}, nil
	}
	ans := &Answer{Action: ActionPrefix + PDQResponse, Target: "PDQ continuation " + queryKey(qid), Outcome: "ok"}
	if res == nil {
		ans.Outcome = "refused"
		ans.Body = s.pdqAnswer(msg, &pdqResult{queryID: qid}, nil, "AE", []detail{{errUnknownKey,
			"no query with this id is waiting to continue (results are kept ten minutes)", "//queryContinuation/queryId"}}, mpi.Settings{})
		return ans, nil
	}
	s.mu.Lock()
	from := res.sent
	if qty <= 0 {
		qty = len(res.matches) - from
	}
	to := min(from+qty, len(res.matches))
	page := res.matches[from:to]
	res.sent = from
	body := s.pdqAnswer(msg, res, page, "AA", nil, res.set)
	res.sent = to
	if to >= len(res.matches) {
		delete(s.pending, queryKey(qid))
	} else {
		res.expires = s.now().Add(pdqKeep)
	}
	s.mu.Unlock()
	ans.Body = body
	return ans, nil
}
