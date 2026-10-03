package x12

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Eligibility (270/271), claim status requests (276) and enrolment (834): the transactions a provider's billing office and a payer's
// enrolment team run every day.
//
// The 270 asks a payer whether a patient is covered and what they will owe; the 271 answers with EB segments, one per benefit, each
// saying for which service types, at what level (individual or family), in or out of network, and an amount or a percentage. A 271 is a
// list of facts that a person then has to assemble into "covered, $25 copay, $1,200 of the $2,000 deductible left", so ReadEligibility
// does the assembling. The CAQH CORE operating rules say which of those facts a payer must return; CheckCORE says which are missing.
//
// Versions are the HIPAA-adopted 5010 ones: 005010X279A1 (270/271), 005010X212 (276/277), 005010X220A1 (834).

// Person is a person or an organisation in a request.
type Person struct {
	// LastName is the surname, or the organisation's name.
	LastName  string `json:"lastName"`
	FirstName string `json:"firstName,omitempty"`
	// ID is the identifier: a payer ID, an NPI, a member ID.
	ID string `json:"id"`
	// DOB is CCYYMMDD, for a person.
	DOB string `json:"dob,omitempty"`
	// Gender is M, F or U.
	Gender string `json:"gender,omitempty"`
}

// Envelope is what every built interchange needs.
type Envelope struct {
	SenderID      string    `json:"senderId"`
	ReceiverID    string    `json:"receiverId"`
	Production    bool      `json:"production,omitempty"`
	ControlNumber int       `json:"controlNumber,omitempty"`
	Now           time.Time `json:"-"`
}

// EligibilityRequest is a 270.
type EligibilityRequest struct {
	Envelope
	Payer    Person `json:"payer"`
	Provider Person `json:"provider"`
	// Subscriber is the insured. Dependent is set when the patient is someone else on the subscriber's plan.
	Subscriber Person  `json:"subscriber"`
	Dependent  *Person `json:"dependent,omitempty"`
	// ServiceTypes are EQ01 codes; empty means 30, Health Benefit Plan Coverage, which is what CORE says a payer must answer in full.
	ServiceTypes []string `json:"serviceTypes,omitempty"`
	// ServiceDate is CCYYMMDD; empty means today.
	ServiceDate string `json:"serviceDate,omitempty"`
	// Trace is TRN02, returned in the 271 so the answer can be matched to the question. Empty means generated.
	Trace string `json:"trace,omitempty"`
}

// builder writes segments with the standard 5010 delimiters: * elements, : components, ^ repetitions, ~ segments.
type builder struct {
	segs []string
}

func (b *builder) add(elems ...string) {
	for len(elems) > 1 && elems[len(elems)-1] == "" {
		elems = elems[:len(elems)-1]
	}
	b.segs = append(b.segs, strings.Join(elems, "*"))
}

func checkDelimiters(fields ...string) error {
	for _, f := range fields {
		if strings.ContainsAny(f, "*~:^\r\n") {
			return fmt.Errorf("%q contains an X12 delimiter, which would split the segment it is written into", f)
		}
	}
	return nil
}

// wrap puts a transaction set in an ISA/GS envelope.
func (e Envelope) wrap(functional, version, setID string, body func(b *builder, st string, now time.Time)) []byte {
	now := e.Now
	if now.IsZero() {
		now = time.Now()
	}
	ctrl := e.ControlNumber
	if ctrl <= 0 {
		ctrl = int(now.Unix() % 1000000000)
	}
	usage := "T"
	if e.Production {
		usage = "P"
	}
	pad := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s + strings.Repeat(" ", n-len(s))
	}
	ctrl9 := fmt.Sprintf("%09d", ctrl)
	isa := strings.Join([]string{"ISA", "00", pad("", 10), "00", pad("", 10), "ZZ", pad(e.SenderID, 15), "ZZ",
		pad(e.ReceiverID, 15), now.Format("060102"), now.Format("1504"), "^", "00501", ctrl9, "0", usage, ":"}, "*")
	gs := strings.Join([]string{"GS", functional, e.SenderID, e.ReceiverID, now.Format("20060102"), now.Format("1504"),
		strconv.Itoa(ctrl), "X", version}, "*")

	st := fmt.Sprintf("%04d", ctrl%10000)
	b := &builder{}
	b.add("ST", setID, st, version)
	body(b, st, now)
	b.add("SE", strconv.Itoa(len(b.segs)+1), st)

	var out strings.Builder
	out.WriteString(isa + "~" + gs + "~")
	for _, s := range b.segs {
		out.WriteString(s + "~")
	}
	out.WriteString("GE*1*" + strconv.Itoa(ctrl) + "~IEA*1*" + ctrl9 + "~")
	return []byte(out.String())
}

func (e Envelope) missing() []string {
	var m []string
	if strings.TrimSpace(e.SenderID) == "" {
		m = append(m, "sender ID (ISA06)")
	}
	if strings.TrimSpace(e.ReceiverID) == "" {
		m = append(m, "receiver ID (ISA08)")
	}
	return m
}

func personOrOrg(p Person) string {
	if p.FirstName != "" {
		return "1"
	}
	return "2"
}

// BuildEligibilityInquiry renders a 270, or says what is missing.
func BuildEligibilityInquiry(req EligibilityRequest) ([]byte, error) {
	missing := req.missing()
	need := func(v, what string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, what)
		}
	}
	need(req.Payer.LastName, "payer name")
	need(req.Payer.ID, "payer ID")
	need(req.Provider.LastName, "provider name")
	need(req.Provider.ID, "provider NPI")
	need(req.Subscriber.LastName, "subscriber last name")
	need(req.Subscriber.ID, "subscriber member ID")
	if len(missing) > 0 {
		return nil, fmt.Errorf("a 270 needs %s", strings.Join(missing, ", "))
	}
	if !validNPI(req.Provider.ID) {
		return nil, fmt.Errorf("provider NPI %q fails the NPI check digit", req.Provider.ID)
	}
	if req.Dependent != nil && strings.TrimSpace(req.Dependent.LastName) == "" {
		return nil, fmt.Errorf("a dependent needs a last name")
	}
	fields := []string{req.SenderID, req.ReceiverID, req.Payer.LastName, req.Payer.ID, req.Provider.LastName, req.Provider.FirstName,
		req.Provider.ID, req.Subscriber.LastName, req.Subscriber.FirstName, req.Subscriber.ID, req.Subscriber.DOB, req.ServiceDate,
		req.Trace}
	if req.Dependent != nil {
		fields = append(fields, req.Dependent.LastName, req.Dependent.FirstName, req.Dependent.DOB)
	}
	fields = append(fields, req.ServiceTypes...)
	if err := checkDelimiters(fields...); err != nil {
		return nil, err
	}
	types := req.ServiceTypes
	if len(types) == 0 {
		types = []string{"30"}
	}

	return req.wrap("HS", "005010X279A1", "270", func(b *builder, st string, now time.Time) {
		date := req.ServiceDate
		if date == "" {
			date = now.Format("20060102")
		}
		trace := req.Trace
		if trace == "" {
			trace = st + now.Format("150405")
		}
		b.add("BHT", "0022", "13", trace, now.Format("20060102"), now.Format("1504"))
		b.add("HL", "1", "", "20", "1")
		b.add("NM1", "PR", "2", req.Payer.LastName, "", "", "", "", "PI", req.Payer.ID)
		b.add("HL", "2", "1", "21", "1")
		b.add("NM1", "1P", personOrOrg(req.Provider), req.Provider.LastName, req.Provider.FirstName, "", "", "", "XX", req.Provider.ID)

		patient := func(hl, parent, code string, entity string, p Person, withID bool) {
			child := "0"
			if code == "22" && req.Dependent != nil {
				child = "1"
			}
			b.add("HL", hl, parent, code, child)
			if code == "22" && req.Dependent == nil || code == "23" {
				// The trace goes on the patient's loop: the subscriber's when the subscriber is the patient, the dependent's otherwise.
				b.add("TRN", "1", trace, "9"+padID(req.SenderID))
			}
			id, q := "", ""
			if withID {
				id, q = p.ID, "MI"
			}
			b.add("NM1", entity, "1", p.LastName, p.FirstName, "", "", "", q, id)
			if p.DOB != "" || p.Gender != "" {
				b.add("DMG", map[bool]string{true: "D8", false: ""}[p.DOB != ""], p.DOB, p.Gender)
			}
			if code == "22" && req.Dependent != nil {
				return
			}
			b.add("DTP", "291", "D8", date)
			for _, t := range types {
				b.add("EQ", t)
			}
		}
		patient("3", "2", "22", "IL", req.Subscriber, true)
		if req.Dependent != nil {
			patient("4", "3", "23", "03", *req.Dependent, false)
		}
	}), nil
}

// padID makes a TRN03 originating company identifier: ten characters, from whatever identifies the sender.
func padID(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, strings.ToUpper(s))
	if len(s) > 9 {
		return s[:9]
	}
	return s + strings.Repeat("0", 9-len(s))
}

// Benefit is one EB segment, read.
type Benefit struct {
	// Code is EB01 and Meaning says it in words: 1 active coverage, 6 inactive, A coinsurance, B co-payment, C deductible, G out of
	// pocket, and so on.
	Code    string `json:"code"`
	Meaning string `json:"meaning"`
	// Level is EB02: IND individual, FAM family, EMP employee only...
	Level string `json:"level,omitempty"`
	// ServiceTypes are EB03, repeated.
	ServiceTypes []string `json:"serviceTypes,omitempty"`
	// InsuranceType is EB04: HM HMO, PR PPO, MB Medicare Part B...
	InsuranceType string `json:"insuranceType,omitempty"`
	// Plan is EB05, the plan's description.
	Plan string `json:"plan,omitempty"`
	// Period is EB06: 23 calendar year, 29 remaining, 24 year to date, 27 visit...
	Period string `json:"period,omitempty"`
	// Amount is EB07; Percent is EB08 as a fraction, 0.2 meaning 20%.
	Amount  string `json:"amount,omitempty"`
	Percent string `json:"percent,omitempty"`
	// Quantity is EB09 qualifier and EB10 value, e.g. visits.
	Quantity string `json:"quantity,omitempty"`
	// AuthRequired is EB11: Y, N or U.
	AuthRequired string `json:"authRequired,omitempty"`
	// InNetwork is EB12: Y in network, N out of network, W not applicable - both.
	InNetwork string   `json:"inNetwork,omitempty"`
	Messages  []string `json:"messages,omitempty"`
	Dates     []Date   `json:"dates,omitempty"`
}

// Date is a DTP: a qualifier, its meaning, and the value.
type Date struct {
	Qualifier string `json:"qualifier"`
	Meaning   string `json:"meaning"`
	Value     string `json:"value"`
}

// Rejection is an AAA segment: the payer could not answer, and why.
type Rejection struct {
	Where    string `json:"where"`
	Valid    string `json:"valid"`
	Reason   string `json:"reason"`
	Meaning  string `json:"meaning"`
	FollowUp string `json:"followUp,omitempty"`
}

// Eligibility is a 271, assembled.
type Eligibility struct {
	Trace      string      `json:"trace,omitempty"`
	Payer      Person      `json:"payer"`
	Provider   Person      `json:"provider"`
	Subscriber Person      `json:"subscriber"`
	Dependent  *Person     `json:"dependent,omitempty"`
	Rejections []Rejection `json:"rejections"`
	// Status is active, inactive or unknown, from the EB01 for service type 30 (or any, when 30 is absent).
	Status   string    `json:"status"`
	Plan     string    `json:"plan,omitempty"`
	Dates    []Date    `json:"dates"`
	Benefits []Benefit `json:"benefits"`
	// Summary is the answer a front desk wants, in a few lines.
	Summary []string `json:"summary"`
}

// IsEligibilityResponse reports whether the interchange carries a 271.
func (m *Message) IsEligibilityResponse() bool { return m.transactionSetOfType("271") != nil }

// ReadEligibility assembles a 271.
func (m *Message) ReadEligibility() (*Eligibility, error) {
	segs := m.transactionSetOfType("271")
	if segs == nil {
		return nil, fmt.Errorf("the interchange has no 271; it carries %s", strings.Join(m.TransactionSets(), ", "))
	}
	out := &Eligibility{Rejections: []Rejection{}, Dates: []Date{}, Benefits: []Benefit{}, Summary: []string{}}

	level := "" // the HL level code we are under: 20, 21, 22, 23
	var cur *Benefit
	for _, s := range segs {
		el := func(n int) string { return s.Element(n).String() }
		switch s.ID {
		case "BHT":
			out.Trace = el(3)
		case "HL":
			level = el(3)
			cur = nil
		case "TRN":
			if el(1) == "2" || out.Trace == "" {
				out.Trace = el(2)
			}
		case "NM1":
			p := Person{LastName: el(3), FirstName: el(4), ID: el(9)}
			switch level {
			case "20":
				out.Payer = p
			case "21":
				out.Provider = p
			case "22":
				out.Subscriber = p
			case "23":
				out.Dependent = &p
			}
		case "DMG":
			target := &out.Subscriber
			if level == "23" && out.Dependent != nil {
				target = out.Dependent
			}
			target.DOB, target.Gender = el(2), el(3)
		case "AAA":
			out.Rejections = append(out.Rejections, Rejection{Where: hlName(level), Valid: el(1), Reason: el(3),
				Meaning: aaaReasons[el(3)], FollowUp: aaaFollowUp[el(4)]})
		case "EB":
			b := Benefit{Code: el(1), Meaning: ebMeanings[el(1)], Level: el(2), ServiceTypes: s.Element(3).Repetitions(),
				InsuranceType: el(4), Plan: el(5), Period: el(6), Amount: el(7), Percent: el(8), AuthRequired: el(11), InNetwork: el(12)}
			if el(9) != "" {
				b.Quantity = el(10) + " " + quantityQualifiers[el(9)]
			}
			if len(b.ServiceTypes) == 1 && b.ServiceTypes[0] == "" {
				b.ServiceTypes = nil
			}
			out.Benefits = append(out.Benefits, b)
			cur = &out.Benefits[len(out.Benefits)-1]
		case "MSG":
			if cur != nil {
				cur.Messages = append(cur.Messages, el(1))
			}
		case "DTP":
			d := Date{Qualifier: el(1), Meaning: dtpMeanings[el(1)], Value: el(3)}
			if cur != nil {
				cur.Dates = append(cur.Dates, d)
			} else {
				out.Dates = append(out.Dates, d)
			}
		case "LS":
			// A loop boundary inside a benefit (2120C, the benefit's related entity); the MSG and DTP after LE belong to the EB again.
		}
	}

	out.Status, out.Plan = coverageStatus(out.Benefits)
	out.Summary = summarise(out)
	return out, nil
}

func hlName(code string) string {
	return map[string]string{"20": "payer", "21": "provider", "22": "subscriber", "23": "dependent"}[code]
}

// coverageStatus reads the active or inactive EB for health benefit plan coverage, or any service type when 30 is not answered.
func coverageStatus(bs []Benefit) (string, string) {
	pick := func(only30 bool) (string, string, bool) {
		for _, b := range bs {
			if only30 && !contains(b.ServiceTypes, "30") {
				continue
			}
			switch b.Code {
			case "1", "2", "3", "4", "5":
				return "active", b.Plan, true
			case "6", "7", "8":
				return "inactive", b.Plan, true
			}
		}
		return "", "", false
	}
	if s, p, ok := pick(true); ok {
		return s, p
	}
	if s, p, ok := pick(false); ok {
		return s, p
	}
	return "unknown", ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// summarise says the answer in the terms a front desk uses.
func summarise(e *Eligibility) []string {
	var out []string
	if len(e.Rejections) > 0 {
		for _, r := range e.Rejections {
			out = append(out, fmt.Sprintf("The payer could not answer for the %s: %s.", r.Where, orElse(r.Meaning, "reason "+r.Reason)))
		}
		return out
	}
	who := strings.TrimSpace(e.Subscriber.FirstName + " " + e.Subscriber.LastName)
	if e.Dependent != nil {
		who = strings.TrimSpace(e.Dependent.FirstName + " " + e.Dependent.LastName)
	}
	line := fmt.Sprintf("%s: coverage %s", orElse(who, "The patient"), e.Status)
	if e.Plan != "" {
		line += " under " + e.Plan
	}
	out = append(out, line+".")

	for _, b := range e.Benefits {
		if !contains(b.ServiceTypes, "30") && len(b.ServiceTypes) > 0 {
			continue
		}
		net := map[string]string{"Y": " in network", "N": " out of network"}[b.InNetwork]
		lvl := map[string]string{"IND": "individual ", "FAM": "family "}[b.Level]
		switch b.Code {
		case "B":
			out = append(out, fmt.Sprintf("Co-payment%s: $%s.", net, b.Amount))
		case "A":
			out = append(out, fmt.Sprintf("Coinsurance%s: %s.", net, percent(b.Percent)))
		case "C":
			what := "deductible"
			if b.Period == "29" {
				what = "deductible remaining"
			}
			out = append(out, fmt.Sprintf("%s%s%s: $%s.", strings.ToUpper(lvl[:min(1, len(lvl))])+lvl[min(1, len(lvl)):], what, net,
				b.Amount))
		case "G":
			what := "out-of-pocket maximum"
			if b.Period == "29" {
				what = "out-of-pocket remaining"
			}
			out = append(out, fmt.Sprintf("%s%s%s: $%s.", strings.ToUpper(lvl[:min(1, len(lvl))])+lvl[min(1, len(lvl)):], what, net,
				b.Amount))
		}
	}
	return out
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func percent(p string) string {
	f, err := strconv.ParseFloat(p, 64)
	if err != nil {
		return p
	}
	return strconv.FormatFloat(f*100, 'f', -1, 64) + "%"
}

// COREFinding is one CAQH CORE data content requirement and whether the 271 meets it.
type COREFinding struct {
	Requirement string `json:"requirement"`
	Met         bool   `json:"met"`
	Detail      string `json:"detail,omitempty"`
}

// coreBaseServiceTypes are the service types a CORE-certified payer must return in answer to an inquiry for 30, beside 30 itself.
var coreBaseServiceTypes = []string{"1", "33", "35", "47", "48", "50", "86", "88", "98", "AL", "MH", "UC"}

// CheckCORE holds a 271 to the CAQH CORE Eligibility & Benefits data content rule as read here: what a payer must return for an
// inquiry about health benefit plan coverage (service type 30).
//
// This is a reading of the rule, not CORE certification, which is done by CAQH's authorised testing vendors. It is useful for the
// question a billing office actually has - "why does this payer's answer never tell us the deductible?" - and the answer is a list of
// the requirements and which ones the response met.
func (e *Eligibility) CheckCORE() []COREFinding {
	var out []COREFinding
	add := func(req string, met bool, detail string) {
		out = append(out, COREFinding{Requirement: req, Met: met, Detail: detail})
	}
	if len(e.Rejections) > 0 {
		add("A response to the inquiry, rather than a rejection", false, e.Rejections[0].Meaning)
		return out
	}

	add("Coverage status for service type 30", e.Status != "unknown", "")
	if e.Status != "active" {
		return out
	}
	planDate := false
	for _, d := range e.Dates {
		if d.Qualifier == "346" || d.Qualifier == "356" || d.Qualifier == "291" || d.Qualifier == "307" {
			planDate = true
		}
	}
	add("Plan or eligibility dates (DTP 291, 307, 346 or 356)", planDate, "")
	add("Plan name", e.Plan != "" || anyPlan(e.Benefits), "")

	has := func(code string, remaining bool, net string) bool {
		for _, b := range e.Benefits {
			if b.Code != code || (len(b.ServiceTypes) > 0 && !contains(b.ServiceTypes, "30")) {
				continue
			}
			if remaining != (b.Period == "29") {
				continue
			}
			if net != "" && b.InNetwork != net && b.InNetwork != "W" {
				continue
			}
			return true
		}
		return false
	}
	for _, net := range []struct{ code, name string }{{"Y", "in network"}, {"N", "out of network"}} {
		add("Co-payment, "+net.name, has("B", false, net.code), "EB01 B")
		add("Coinsurance, "+net.name, has("A", false, net.code), "EB01 A")
		add("Deductible, "+net.name, has("C", false, net.code), "EB01 C")
		add("Deductible remaining, "+net.name, has("C", true, net.code), "EB01 C with EB06 29")
	}

	returned := map[string]bool{}
	for _, b := range e.Benefits {
		for _, t := range b.ServiceTypes {
			returned[t] = true
		}
	}
	var absent []string
	for _, t := range coreBaseServiceTypes {
		if !returned[t] {
			absent = append(absent, t)
		}
	}
	sort.Strings(absent)
	detail := "all returned"
	if len(absent) > 0 {
		detail = "not returned: " + strings.Join(absent, ", ")
	}
	add("The base service types (1, 33, 35, 47, 48, 50, 86, 88, 98, AL, MH, UC)", len(absent) == 0, detail)
	return out
}

func anyPlan(bs []Benefit) bool {
	for _, b := range bs {
		if b.Plan != "" {
			return true
		}
	}
	return false
}

// ClaimStatusRequest is a 276.
type ClaimStatusRequest struct {
	Envelope
	Payer      Person `json:"payer"`
	Receiver   Person `json:"receiver"`
	Provider   Person `json:"provider"`
	Subscriber Person `json:"subscriber"`
	// PatientAccount is the provider's claim identifier (CLM01), which the payer echoes back.
	PatientAccount string `json:"patientAccount"`
	// PayerClaimNumber is the payer's claim control number, when known.
	PayerClaimNumber string `json:"payerClaimNumber,omitempty"`
	// ChargeAmount is the claim's total charge, which helps the payer find it.
	ChargeAmount string `json:"chargeAmount,omitempty"`
	// ServiceFrom and ServiceTo are CCYYMMDD.
	ServiceFrom string `json:"serviceFrom"`
	ServiceTo   string `json:"serviceTo,omitempty"`
	Trace       string `json:"trace,omitempty"`
}

// BuildClaimStatusRequest renders a 276, or says what is missing.
func BuildClaimStatusRequest(req ClaimStatusRequest) ([]byte, error) {
	missing := req.missing()
	need := func(v, what string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, what)
		}
	}
	need(req.Payer.LastName, "payer name")
	need(req.Payer.ID, "payer ID")
	need(req.Provider.LastName, "provider name")
	need(req.Provider.ID, "provider NPI")
	need(req.Subscriber.LastName, "subscriber last name")
	need(req.Subscriber.ID, "subscriber member ID")
	need(req.PatientAccount, "patient account number (the claim's CLM01)")
	need(req.ServiceFrom, "service date")
	if len(missing) > 0 {
		return nil, fmt.Errorf("a 276 needs %s", strings.Join(missing, ", "))
	}
	if !validNPI(req.Provider.ID) {
		return nil, fmt.Errorf("provider NPI %q fails the NPI check digit", req.Provider.ID)
	}
	if err := checkDelimiters(req.SenderID, req.ReceiverID, req.Payer.LastName, req.Payer.ID, req.Receiver.LastName, req.Receiver.ID,
		req.Provider.LastName, req.Provider.FirstName, req.Provider.ID, req.Subscriber.LastName, req.Subscriber.FirstName,
		req.Subscriber.ID, req.PatientAccount, req.PayerClaimNumber, req.ChargeAmount, req.ServiceFrom, req.ServiceTo, req.Trace); err != nil {
		return nil, err
	}
	receiver := req.Receiver
	if receiver.LastName == "" {
		receiver = req.Provider
	}

	return req.wrap("HR", "005010X212", "276", func(b *builder, st string, now time.Time) {
		trace := req.Trace
		if trace == "" {
			trace = st + now.Format("150405")
		}
		b.add("BHT", "0010", "13", trace, now.Format("20060102"), now.Format("1504"))
		b.add("HL", "1", "", "20", "1")
		b.add("NM1", "PR", "2", req.Payer.LastName, "", "", "", "", "PI", req.Payer.ID)
		b.add("HL", "2", "1", "21", "1")
		b.add("NM1", "41", personOrOrg(receiver), receiver.LastName, receiver.FirstName, "", "", "", "46", receiver.ID)
		b.add("HL", "3", "2", "19", "1")
		b.add("NM1", "1P", personOrOrg(req.Provider), req.Provider.LastName, req.Provider.FirstName, "", "", "", "XX", req.Provider.ID)
		b.add("HL", "4", "3", "22", "0")
		b.add("NM1", "IL", "1", req.Subscriber.LastName, req.Subscriber.FirstName, "", "", "", "MI", req.Subscriber.ID)
		b.add("TRN", "1", trace)
		if req.PayerClaimNumber != "" {
			b.add("REF", "1K", req.PayerClaimNumber)
		}
		b.add("REF", "EJ", req.PatientAccount)
		if req.ChargeAmount != "" {
			b.add("AMT", "T3", req.ChargeAmount)
		}
		to := req.ServiceTo
		if to == "" {
			to = req.ServiceFrom
		}
		b.add("DTP", "472", "RD8", req.ServiceFrom+"-"+to)
	}), nil
}

// Enrollment is an 834, read.
type Enrollment struct {
	Sponsor Person   `json:"sponsor"`
	Payer   Person   `json:"payer"`
	Members []Member `json:"members"`
	// Purpose is BGN08: 2 change (update), 4 verify, RX replace (a full file).
	Purpose string `json:"purpose,omitempty"`
}

// Member is one INS loop.
type Member struct {
	Subscriber bool `json:"subscriber"`
	// Relationship is INS02 in words: self, spouse, child...
	Relationship string `json:"relationship"`
	// Action is INS03 in words: addition, cancellation, change, reinstatement, audit.
	Action       string     `json:"action"`
	SubscriberID string     `json:"subscriberId,omitempty"`
	MemberID     string     `json:"memberId,omitempty"`
	Person       Person     `json:"person"`
	Coverages    []Coverage `json:"coverages"`
}

// Coverage is one HD loop.
type Coverage struct {
	Action string `json:"action"`
	// Line is HD03: HLT medical, DEN dental, VIS vision, PDG prescription drug...
	Line  string `json:"line"`
	Plan  string `json:"plan,omitempty"`
	Level string `json:"level,omitempty"`
	Begin string `json:"begin,omitempty"`
	End   string `json:"end,omitempty"`
}

// IsEnrollment reports whether the interchange carries an 834.
func (m *Message) IsEnrollment() bool { return m.transactionSetOfType("834") != nil }

// ReadEnrollment reads an 834's members and coverages.
func (m *Message) ReadEnrollment() (*Enrollment, error) {
	segs := m.transactionSetOfType("834")
	if segs == nil {
		return nil, fmt.Errorf("the interchange has no 834; it carries %s", strings.Join(m.TransactionSets(), ", "))
	}
	out := &Enrollment{Members: []Member{}}
	var mem *Member
	var cov *Coverage
	for _, s := range segs {
		el := func(n int) string { return s.Element(n).String() }
		switch s.ID {
		case "BGN":
			out.Purpose = el(8)
		case "N1":
			p := Person{LastName: el(2), ID: el(4)}
			switch el(1) {
			case "P5":
				out.Sponsor = p
			case "IN":
				out.Payer = p
			}
		case "INS":
			out.Members = append(out.Members, Member{Subscriber: el(1) == "Y", Relationship: orElse(relationships[el(2)], el(2)),
				Action: orElse(maintenance[el(3)], el(3)), Coverages: []Coverage{}})
			mem, cov = &out.Members[len(out.Members)-1], nil
		case "REF":
			if mem == nil {
				continue
			}
			switch el(1) {
			case "0F":
				mem.SubscriberID = el(2)
			case "23":
				mem.MemberID = el(2)
			}
		case "NM1":
			if mem != nil && el(1) == "IL" {
				mem.Person.LastName, mem.Person.FirstName, mem.Person.ID = el(3), el(4), el(9)
			}
		case "DMG":
			if mem != nil {
				mem.Person.DOB, mem.Person.Gender = el(2), el(3)
			}
		case "HD":
			if mem != nil {
				mem.Coverages = append(mem.Coverages, Coverage{Action: orElse(maintenance[el(1)], el(1)),
					Line: orElse(insuranceLines[el(3)], el(3)), Plan: el(4), Level: el(5)})
				cov = &mem.Coverages[len(mem.Coverages)-1]
			}
		case "DTP":
			if cov != nil {
				switch el(1) {
				case "348":
					cov.Begin = el(3)
				case "349":
					cov.End = el(3)
				}
			}
		}
	}
	return out, nil
}

var ebMeanings = map[string]string{
	"1": "active coverage", "2": "active - full risk capitation", "3": "active - services capitated", "4": "active - services capitated to primary care physician",
	"5": "active - pending investigation", "6": "inactive", "7": "inactive - pending eligibility update", "8": "inactive - pending investigation",
	"A": "coinsurance", "B": "co-payment", "C": "deductible", "CB": "coverage basis", "D": "benefit description", "E": "exclusions",
	"F": "limitations", "G": "out of pocket (stop loss)", "H": "unlimited", "I": "non-covered", "J": "cost containment",
	"K": "reserve", "L": "primary care provider", "M": "pre-existing condition", "MC": "managed care coordinator", "N": "services restricted to following provider",
	"O": "not deemed a medical necessity", "P": "benefit disclaimer", "Q": "second surgical opinion required", "R": "other or additional payer",
	"S": "prior year(s) history", "T": "card(s) reported lost/stolen", "U": "contact following entity for eligibility or benefit information",
	"V": "cannot process", "W": "other source of data", "X": "health care facility", "Y": "spend down",
}

var aaaReasons = map[string]string{
	"15": "required application data missing", "41": "authorization/access restrictions", "42": "unable to respond at current time",
	"43": "invalid/missing provider identification", "44": "invalid/missing provider name", "45": "invalid/missing provider specialty",
	"46": "invalid/missing provider phone number", "47": "invalid/missing provider state", "48": "invalid/missing referring provider identification number",
	"49": "provider is not primary care physician", "50": "provider ineligible for inquiries", "51": "provider not on file",
	"52": "service dates not within provider plan enrollment", "53": "inquired benefit inconsistent with provider type",
	"54": "inappropriate product/service ID qualifier", "55": "inappropriate product/service ID", "56": "inappropriate date",
	"57": "invalid/missing date(s) of service", "58": "invalid/missing date-of-birth", "60": "date of birth follows date(s) of service",
	"61": "date of death precedes date(s) of service", "62": "date of service not within allowable inquiry period",
	"63": "date of service in future", "64": "invalid/missing patient ID", "65": "invalid/missing patient name",
	"66": "invalid/missing patient gender code", "67": "patient not found", "68": "duplicate patient ID number",
	"69": "inconsistent with patient's age", "70": "inconsistent with patient's gender", "71": "patient birth date does not match that for the patient on the database",
	"72": "invalid/missing subscriber/insured ID", "73": "invalid/missing subscriber/insured name", "74": "invalid/missing subscriber/insured gender code",
	"75": "subscriber/insured not found", "76": "duplicate subscriber/insured ID number", "77": "subscriber found, patient not found",
	"78": "subscriber/insured not in group/plan identified", "79": "invalid participant identification", "80": "no response received - transaction terminated",
	"97": "invalid or missing provider address", "T4": "payer name or identifier missing",
}

var aaaFollowUp = map[string]string{
	"C": "please correct and resubmit", "N": "resubmission not allowed", "P": "please resubmit original transaction",
	"R": "resubmission allowed", "S": "do not resubmit; inquiry initiated to a third party",
	"W": "please wait 30 days and resubmit", "X": "please wait 10 days and resubmit", "Y": "do not resubmit; we will hold your request and respond again shortly",
}

var dtpMeanings = map[string]string{
	"096": "discharge", "102": "issue", "152": "effective date of change", "291": "plan", "307": "eligibility", "318": "added",
	"340": "COBRA begin", "341": "COBRA end", "342": "premium paid to date begin", "343": "premium paid to date end", "346": "plan begin",
	"347": "plan end", "356": "eligibility begin", "357": "eligibility end", "382": "enrollment", "435": "admission",
	"442": "date of death", "458": "certification", "472": "service", "539": "policy effective", "540": "policy expiration",
	"636": "date of last update", "771": "status",
}

var quantityQualifiers = map[string]string{
	"99": "used", "CA": "covered actual", "CE": "covered estimated", "D3": "number of co-insurance days", "DB": "deductible blood units",
	"DY": "days", "HS": "hours", "LA": "life-time reserve actual", "LE": "life-time reserve estimated", "M2": "maximum",
	"MN": "month", "P6": "number of services or procedures", "QA": "quantity approved", "S7": "age, high value", "S8": "age, low value",
	"VS": "visits", "YY": "years",
}

var relationships = map[string]string{
	"01": "spouse", "18": "self", "19": "child", "20": "employee", "15": "ward", "17": "stepson or stepdaughter", "21": "unknown",
	"53": "life partner", "G8": "other relationship",
}

var maintenance = map[string]string{
	"001": "change", "002": "delete", "021": "addition", "024": "cancellation or termination", "025": "reinstatement",
	"026": "correction", "030": "audit or compare", "032": "employee information not applicable",
}

var insuranceLines = map[string]string{
	"AG": "preventive care", "AH": "24-hour care", "AJ": "medicare risk", "AK": "mental health", "DCP": "dental capitation",
	"DEN": "dental", "EPO": "exclusive provider organisation", "FAC": "facility", "HE": "hearing", "HLT": "health",
	"HMO": "health maintenance organisation", "LTC": "long-term care", "LTD": "long-term disability", "MM": "major medical",
	"MOD": "mail order drug", "PDG": "prescription drug", "POS": "point of service", "PPO": "preferred provider organisation",
	"PRA": "practitioners", "STD": "short-term disability", "UR": "utilization review", "VIS": "vision",
}
