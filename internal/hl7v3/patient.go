package hl7v3

import (
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Patient demographics, which is what the overwhelming majority of real v3 traffic carries.
//
// The PRPA domain - Patient Registry - covers record added, revised and merged, plus the two queries that matter: find a
// patient's identifiers in another system (PIX, ITI-45) and find a patient by demographics (PDQ, ITI-47). Between them these
// account for most v3 messages a hospital will ever send or receive, because they are how a patient's records are stitched
// together across departments that bought different systems a decade apart.

// Patient is the demographic content of a patient registry message.
//
// Every field distinguishes absent from null-with-reason, for the reason set out in the package documentation: an empty
// birth date and a birth date the patient declined to give are different facts, and a demographic match that treats them
// alike will resolve an identity that should have been left for a human.
type Patient struct {
	// IDs are every identifier given for this patient, in document order.
	//
	// A slice rather than a single identifier because that is the whole point of PIX: a patient has a number in the
	// hospital's system, another in the lab's, another in the regional registry, and possibly a national one. The
	// first is not more authoritative than the rest.
	IDs []II

	// Names in document order. Several is normal - a legal name and a preferred name, or a maiden name.
	Names []Name

	// Gender is the administrative gender code, from administrativeGenderCode.
	//
	// Administrative rather than clinical, and the distinction is real: this field drives which ward a patient is
	// placed on and how they are addressed, not their clinical sex. Systems that conflate them produce both the wrong
	// bed and the wrong reference range.
	Gender Coded

	// BirthTime is the date of birth, with its precision preserved.
	BirthTime Timestamp

	// Deceased says whether the patient is recorded as dead, and when.
	//
	// A separate indicator from the time because a system may know that a patient has died without knowing when, and
	// treating an absent time as "not dead" would send an appointment letter to a bereaved family.
	Deceased     bool
	DeceasedTime Timestamp

	// MultipleBirth says whether the patient is one of a multiple birth, and the order.
	//
	// Worth carrying because twins with the same surname and birth date are the classic false-positive in demographic
	// matching, and the birth order is often the only thing distinguishing them.
	MultipleBirth      bool
	MultipleBirthOrder string

	// MaritalStatus, ReligiousAffiliation, Race and Ethnicity, when given.
	MaritalStatus        Coded
	ReligiousAffiliation Coded
	Race                 Coded
	Ethnicity            Coded

	// Addresses and Telecoms in document order.
	Addresses []Address
	Telecoms  []Telecom

	// LanguageCodes are the languages the patient communicates in, most preferred first when the sender says.
	//
	// Operationally significant rather than demographic trivia: it decides whether an interpreter is booked.
	LanguageCodes []string

	// ProviderOrganizationID identifies the organisation whose patient this is - the assigning authority for the
	// primary identifier, in effect.
	ProviderOrganizationID II

	// StatusCode is the record's status: active, inactive, or nullified. A nullified record is one that should never
	// have existed and must not be treated as a patient who has become inactive.
	StatusCode string

	// Node is the patient element, for anything not modelled here.
	Node *xtree.Node
}

// PrimaryName returns the legal name when one is marked, otherwise the first.
//
// Falls back rather than returning nothing, because most senders do not mark a use at all and a caller wanting "the name"
// should not get an empty string from a message that plainly has one.
func (p Patient) PrimaryName() Name {
	for _, n := range p.Names {
		if strings.EqualFold(n.Use, "L") {
			return n
		}
	}
	for _, n := range p.Names {
		if n.Presence == Present {
			return n
		}
	}

	return Name{Presence: Absent}
}

// IDIn returns the identifier assigned by a particular authority.
//
// The way to ask "what is this patient's number in that system", which is the question PIX exists to answer. Matching on the
// root rather than on position, because the order identifiers appear in is the sender's business and depending on it works
// until the day the sender reorders them.
func (p Patient) IDIn(rootOID string) (II, bool) {
	for _, id := range p.IDs {
		if id.Presence == Present && id.Root == rootOID {
			return id, true
		}
	}

	return II{Presence: Absent}, false
}

// HomeAddress returns the patient's home address, preferring one marked primary.
func (p Patient) HomeAddress() Address {
	var home Address
	found := false

	for _, a := range p.Addresses {
		if a.Presence != Present {
			continue
		}

		// BAD is a use value, so it is mutually exclusive with H and HP and the switch below already excludes it.
		//
		// An earlier version of this function also called Undeliverable() here, which read like a safeguard and was dead
		// code: no address can be both use="H" and use="BAD". Removing it changed no test, which is how it was found -
		// the test written to cover it stayed green with it deleted. A check that cannot fire is worse than no check,
		// because the next person reads it as protection that exists.
		switch strings.ToUpper(a.Use) {
		case "HP":
			return a
		case "H", "":
			if !found {
				home, found = a, true
			}
		}
	}

	if found {
		return home
	}

	return Address{Presence: Absent}
}

// ExtractPatient reads patient demographics from anywhere in a message.
//
// Searches rather than paths to a fixed location, because the nesting differs between interactions: a record-added message
// puts the patient under registrationEvent/subject1, a query response puts it under
// registrationEvent/subject1/patient/patientPerson, and both are correct. A path that works for one silently returns nothing
// for the other, and returning nothing from a message that plainly contains a patient is worse than an error.
//
// Returns nil when there is no patient element at all, which is normal for an acknowledgement.
func (m *Message) ExtractPatient() *Patient {
	if m.Root == nil {
		return nil
	}

	// patientPerson is the v3 name for the person inside a patient role, and is the more specific of the two - so it is
	// looked for first, because a patient element usually contains a patientPerson and reading the outer one would miss
	// the names.
	person := m.Root.Find("patientPerson")
	patientRole := m.Root.Find("patient")

	if person == nil {
		// Some senders put demographics directly on the patient element.
		person = patientRole
	}
	if person == nil {
		return nil
	}

	p := &Patient{Node: person}

	// Identifiers come from the patient role, not the person: in the v3 model an identifier is assigned to a role, and
	// the same human in two roles has two identifiers. Falls back to the person when the role has none.
	idSource := patientRole
	if idSource == nil {
		idSource = person
	}
	for _, idNode := range idSource.All("id") {
		if id := parseII(idNode); id.Presence != Absent {
			p.IDs = append(p.IDs, id)
		}
	}
	if len(p.IDs) == 0 && idSource != person {
		for _, idNode := range person.All("id") {
			if id := parseII(idNode); id.Presence != Absent {
				p.IDs = append(p.IDs, id)
			}
		}
	}

	for _, nameNode := range person.All("name") {
		if n := parseName(nameNode); n.Presence != Absent {
			p.Names = append(p.Names, n)
		}
	}

	p.Gender = parseCoded(person.First("administrativeGenderCode"))
	p.BirthTime = parseTimestamp(person.First("birthTime"))
	p.MaritalStatus = parseCoded(person.First("maritalStatusCode"))
	p.ReligiousAffiliation = parseCoded(person.First("religiousAffiliationCode"))
	p.Race = parseCoded(person.First("raceCode"))
	p.Ethnicity = parseCoded(person.First("ethnicGroupCode"))

	p.Deceased = boolAttr(person.First("deceasedInd"))
	p.DeceasedTime = parseTimestamp(person.First("deceasedTime"))
	// A death time without the indicator still means dead. A sender that gives the date has said more than one that
	// sets a flag, and treating the record as living because a boolean was absent would send post to a bereaved family.
	if p.DeceasedTime.Presence == Present {
		p.Deceased = true
	}

	p.MultipleBirth = boolAttr(person.First("multipleBirthInd"))
	p.MultipleBirthOrder = attr(person.First("multipleBirthOrderNumber"), "value")
	if p.MultipleBirthOrder != "" {
		p.MultipleBirth = true
	}

	for _, addrNode := range person.All("addr") {
		if a := parseAddress(addrNode); a.Presence != Absent {
			p.Addresses = append(p.Addresses, a)
		}
	}
	// An address may sit on the role rather than the person.
	if len(p.Addresses) == 0 && patientRole != nil && patientRole != person {
		for _, addrNode := range patientRole.All("addr") {
			if a := parseAddress(addrNode); a.Presence != Absent {
				p.Addresses = append(p.Addresses, a)
			}
		}
	}

	for _, telNode := range person.All("telecom") {
		if tel := parseTelecom(telNode); tel.Presence != Absent {
			p.Telecoms = append(p.Telecoms, tel)
		}
	}
	if len(p.Telecoms) == 0 && patientRole != nil && patientRole != person {
		for _, telNode := range patientRole.All("telecom") {
			if tel := parseTelecom(telNode); tel.Presence != Absent {
				p.Telecoms = append(p.Telecoms, tel)
			}
		}
	}

	for _, lang := range person.All("languageCommunication") {
		if code := attr(lang.First("languageCode"), "code"); code != "" {
			p.LanguageCodes = append(p.LanguageCodes, code)
		}
	}

	if patientRole != nil {
		p.StatusCode = attr(patientRole.First("statusCode"), "code")
		if org := patientRole.Find("providerOrganization"); org != nil {
			p.ProviderOrganizationID = parseII(org.First("id"))
		}
	}

	return p
}

// boolAttr reads a v3 boolean, which lives in a value attribute.
func boolAttr(n *xtree.Node) bool {
	if n == nil {
		return false
	}

	return strings.EqualFold(attr(n, "value"), "true")
}

// QueryParameter is one criterion from a demographic query.
type QueryParameter struct {
	// Name is the parameter element name: livingSubjectName, livingSubjectId, livingSubjectBirthTime,
	// livingSubjectAdministrativeGender, patientAddress, and so on.
	Name string

	// Values are the semantic parameter values, as text where they are text.
	Values []string

	// Node is the parameter element, for reading it as a typed datatype.
	Node *xtree.Node
}

// Query is a demographic query, from PRPA_IN201305UV02 or its relatives.
type Query struct {
	// ID identifies the query, and is what a response refers back to.
	ID II

	// Parameters are the criteria, in document order.
	Parameters []QueryParameter

	// ResponseModalityCode and ResponsePriorityCode say how the sender wants to be answered.
	ResponseModalityCode string
	ResponsePriorityCode string

	// InitialQuantity is how many matches the sender will accept.
	//
	// Worth honouring. A query with no limit against a national registry can match tens of thousands of people, and
	// answering it fully is both useless to the asker and a disclosure of everybody who happens to share a surname.
	InitialQuantity string

	// Node is the queryByParameter element.
	Node *xtree.Node
}

// ExtractQuery reads the query parameters from a query interaction.
//
// Returns nil when this message is not a query.
func (m *Message) ExtractQuery() *Query {
	if m.Root == nil {
		return nil
	}
	qbp := m.Root.Find("queryByParameter")
	if qbp == nil {
		return nil
	}

	q := &Query{
		ID:                   parseII(qbp.First("queryId")),
		ResponseModalityCode: attr(qbp.First("responseModalityCode"), "code"),
		ResponsePriorityCode: attr(qbp.First("responsePriorityCode"), "code"),
		InitialQuantity:      attr(qbp.First("initialQuantity"), "value"),
		Node:                 qbp,
	}

	// The criteria live inside parameterList, not directly under queryByParameter. Iterating the wrong level yields no
	// parameters at all from a query that plainly has three, which is what the first version of this did - and it took a
	// real IHE message to notice, because an invented example would have been written whichever way the code expected.
	//
	// Falls back to queryByParameter's own children, because a few senders do put them there.
	criteria := qbp.First("parameterList")
	if criteria == nil {
		criteria = qbp
	}

	for _, child := range criteria.Children {
		name := localName(child.Name)
		switch name {
		case "queryId", "statusCode", "responseModalityCode", "responsePriorityCode",
			"initialQuantity", "initialQuantityCode", "modifyCode", "matchCriterionList",
			"parameterList", "sortControl":
			continue
		}

		param := QueryParameter{Name: name, Node: child}

		// The semantic value sits under a value element, or occasionally directly on the parameter. Collected as text
		// for filtering; a caller wanting a typed value reads Node.
		//
		// semanticsText is deliberately not read: it names the model attribute this parameter maps to
		// ("LivingSubject.birthTime"), which is documentation of the query rather than anything somebody searched for.
		for _, valueNode := range child.All("value") {
			if v := parameterText(valueNode); v != "" {
				param.Values = append(param.Values, v)
			}
		}
		if len(param.Values) == 0 {
			if v := parameterText(child); v != "" {
				param.Values = append(param.Values, v)
			}
		}

		q.Parameters = append(q.Parameters, param)
	}

	return q
}

// Parameter returns a query parameter by name.
func (q Query) Parameter(name string) (QueryParameter, bool) {
	for _, p := range q.Parameters {
		if p.Name == name {
			return p, true
		}
	}

	return QueryParameter{}, false
}

// parameterText reads a parameter value as text, from whichever attribute carries it.
//
// A value may be an II with root and extension, a coded value with a code, a timestamp with a value attribute, or a name
// made of parts. All reduce to text for the purpose of filtering and logging a query.
func parameterText(n *xtree.Node) string {
	if n == nil {
		return ""
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return ""
	}

	// An identifier: both parts, because an extension without its root is not an identifier.
	if root := attr(n, "root"); root != "" {
		if ext := attr(n, "extension"); ext != "" {
			return ext + "^^^" + root
		}

		return root
	}
	if code := attr(n, "code"); code != "" {
		return code
	}
	if value := attr(n, "value"); value != "" {
		return value
	}

	// A name or address: join the parts.
	if len(n.Children) > 0 {
		var parts []string
		for _, child := range n.Children {
			if t := strings.TrimSpace(child.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}

	return strings.TrimSpace(n.Text)
}
