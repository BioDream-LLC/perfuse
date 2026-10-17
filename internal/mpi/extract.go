package mpi

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
)

// FromOptions says how to read a source system's identifiers.
type FromOptions struct {
	// Authorities maps an HL7 assigning authority's namespace (PID-3.4.1) to its system URI, for authorities that send no
	// universal identifier. Unmapped ones become urn:perfuse:aa:<namespace>.
	Authorities map[string]string
	// IDType is the identifier type code (PID-3.5) of the record's own number. Default MR; the first identifier when none has it.
	IDType string
	// SSNSystem names PID-19. Default http://hl7.org/fhir/sid/us-ssn.
	SSNSystem string
}

// Extracted is what a message says about a patient: the record, and a merge it asks for.
type Extracted struct {
	Record Record
	// MergeFrom is the prior identifier an A40 (or A34, A18) merges into the record, as system|value.
	MergeFrom string
}

func (o FromOptions) system(ns, uid, typ string) string {
	switch {
	case uid != "" && strings.EqualFold(typ, "ISO"):
		return "urn:oid:" + uid
	case uid != "" && (strings.HasPrefix(uid, "http") || strings.HasPrefix(uid, "urn:")):
		return uid
	case ns != "":
		if s, ok := o.Authorities[ns]; ok && s != "" {
			return s
		}
		return "urn:perfuse:aa:" + ns
	case uid != "":
		return "urn:oid:" + uid
	}
	return ""
}

// FromHL7 reads the patient from an HL7 v2 message's PID (and MRG for a merge).
func FromHL7(raw []byte, o FromOptions) (*Extracted, error) {
	m, err := hl7.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := m.Segment("PID", 1); !ok {
		return nil, errors.New("mpi: the message has no PID segment, so no patient to register")
	}
	get := func(p string) string { v, _ := m.Get(p); return strings.TrimSpace(v) }
	wantType := o.IDType
	if wantType == "" {
		wantType = "MR"
	}
	type cx struct{ key, typ string }
	read := func(seg string, field int) []cx {
		var out []cx
		for i := 1; i <= 10; i++ {
			base := seg + "-" + strconv.Itoa(field) + "(" + strconv.Itoa(i) + ")"
			v := get(base + ".1")
			if v == "" {
				break
			}
			sys := o.system(get(base+".4.1"), get(base+".4.2"), get(base+".4.3"))
			if sys == "" {
				continue
			}
			out = append(out, cx{sys + "|" + v, strings.ToUpper(get(base + ".5"))})
		}
		return out
	}
	ids := read("PID", 3)
	if len(ids) == 0 {
		return nil, errors.New("mpi: PID-3 holds no identifier with an assigning authority, so the record cannot be told apart")
	}
	own := ids[0]
	for _, c := range ids {
		if c.typ == wantType {
			own = c
			break
		}
	}
	var r Record
	r.System, r.Value, _ = strings.Cut(own.key, "|")
	for _, c := range ids {
		if c.key != own.key {
			r.Identifiers = append(r.Identifiers, c.key)
		}
	}
	if ssn := get("PID-19"); ssn != "" {
		sys := o.SSNSystem
		if sys == "" {
			sys = "http://hl7.org/fhir/sid/us-ssn"
		}
		r.Identifiers = append(r.Identifiers, sys+"|"+ssn)
	}
	r.Family = get("PID-5.1.1")
	if r.Family == "" {
		r.Family = get("PID-5.1")
	}
	r.Given = get("PID-5.2")
	r.BirthDate = get("PID-7.1")
	if r.BirthDate == "" {
		r.BirthDate = get("PID-7")
	}
	r.Sex = get("PID-8")
	r.Line = get("PID-11.1.1")
	if r.Line == "" {
		r.Line = get("PID-11.1")
	}
	r.City, r.Postal = get("PID-11.3"), get("PID-11.5")
	r.Phone = get("PID-13.1")
	if r.Phone == "" {
		r.Phone = get("PID-13.6") + get("PID-13.7")
	}
	out := &Extracted{Record: r}
	if trig := get("MSH-9.2"); trig == "A40" || trig == "A34" || trig == "A18" || trig == "A39" {
		if prior := read("MRG", 1); len(prior) > 0 {
			out.MergeFrom = prior[0].key
		}
	}
	return out, nil
}

// FromFHIR reads a FHIR Patient resource.
func FromFHIR(raw []byte) (*Extracted, error) {
	var p struct {
		ResourceType string `json:"resourceType"`
		Identifier   []struct{ System, Value string }
		Name         []struct {
			Family string
			Given  []string
		}
		BirthDate string
		Gender    string
		Telecom   []struct{ System, Value string }
		Address   []struct {
			Line       []string
			City       string
			PostalCode string
		}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.ResourceType != "Patient" {
		return nil, errors.New("mpi: the resource is a " + p.ResourceType + ", not a Patient")
	}
	var r Record
	for _, id := range p.Identifier {
		if id.System == "" || id.Value == "" {
			continue
		}
		if r.System == "" {
			r.System, r.Value = id.System, id.Value
			continue
		}
		r.Identifiers = append(r.Identifiers, id.System+"|"+id.Value)
	}
	if r.System == "" {
		return nil, errors.New("mpi: the Patient has no identifier with a system, so the record cannot be told apart")
	}
	if len(p.Name) > 0 {
		r.Family = p.Name[0].Family
		if len(p.Name[0].Given) > 0 {
			r.Given = p.Name[0].Given[0]
		}
	}
	r.BirthDate = p.BirthDate
	r.Sex = map[string]string{"male": "M", "female": "F", "other": "O", "unknown": "U"}[strings.ToLower(p.Gender)]
	for _, t := range p.Telecom {
		if t.System == "phone" && r.Phone == "" {
			r.Phone = t.Value
		}
	}
	if len(p.Address) > 0 {
		a := p.Address[0]
		if len(a.Line) > 0 {
			r.Line = a.Line[0]
		}
		r.City, r.Postal = a.City, a.PostalCode
	}
	return &Extracted{Record: r}, nil
}

// ToFHIR writes a person as a FHIR Patient: the best-known demographics and every identifier, the EUID first.
func ToFHIR(p *Person, euidSystem string) map[string]any {
	g := p.Golden()
	var ids []any
	for _, i := range p.Identifiers(euidSystem) {
		sys, val, _ := strings.Cut(i, "|")
		id := map[string]any{"system": sys, "value": val}
		if sys == euidSystem {
			id["use"] = "official"
		}
		ids = append(ids, id)
	}
	pat := map[string]any{"resourceType": "Patient", "id": p.EUID, "identifier": ids, "active": true}
	if g.Family != "" || g.Given != "" {
		name := map[string]any{}
		if g.Family != "" {
			name["family"] = g.Family
		}
		if g.Given != "" {
			name["given"] = []string{g.Given}
		}
		pat["name"] = []any{name}
	}
	if g.BirthDate != "" && normDate(g.BirthDate) != "" {
		pat["birthDate"] = normDate(g.BirthDate)
	}
	if sex, ok := map[string]string{"M": "male", "F": "female", "O": "other", "U": "unknown"}[g.Sex]; ok {
		pat["gender"] = sex
	}
	if g.Phone != "" {
		pat["telecom"] = []any{map[string]any{"system": "phone", "value": g.Phone}}
	}
	if g.Line != "" || g.City != "" || g.Postal != "" {
		a := map[string]any{}
		if g.Line != "" {
			a["line"] = []string{g.Line}
		}
		if g.City != "" {
			a["city"] = g.City
		}
		if g.Postal != "" {
			a["postalCode"] = g.Postal
		}
		pat["address"] = []any{a}
	}
	return pat
}
