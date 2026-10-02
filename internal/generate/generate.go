// Package generate makes synthetic HL7 v2 messages.
//
// Two uses. Somebody trying Perfuse needs traffic to look at, and a channel test
// needs a fixture that is realistic enough to exercise the thing being tested. A
// generator is a paid extension in Mirth, which tells you people want one.
//
// Everything here is invented. The names come from a fixed list of obviously
// fictional ones, the identifiers are sequential within a run, and the addresses
// are made up. That is not a disclaimer, it is the design: a generator that
// produced plausible-looking real names would eventually put one into a test
// fixture, a bug report and then a repository.
//
// Realism is spent where it matters for testing and nowhere else. Timestamps carry
// offsets, repeating fields actually repeat, a name sometimes has a middle initial
// and sometimes does not, and roughly one message in twelve is deliberately
// awkward: a missing optional field, an unusual but legal separator, an empty
// component. Feeding a channel nothing but perfectly formed messages tells you
// very little.
package generate

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

// Kind is what to produce.
type Kind string

const (
	// ADT is admissions, discharges and transfers: the commonest feed.
	ADT Kind = "ADT"
	// ORU is observation results, which is where repeating OBX segments and
	// abnormal flags live.
	ORU Kind = "ORU"
	// ORM is orders.
	ORM Kind = "ORM"
	// MDM carries a clinical document.
	MDM Kind = "MDM"
)

// Options controls generation.
type Options struct {
	Kind Kind

	// Count is how many to produce.
	Count int

	// Seed makes a run reproducible. Zero picks one, which is right for a demo and
	// wrong for a test fixture, so callers writing tests should set it.
	Seed uint64

	// Facility and Application identify the pretend sender.
	Facility    string
	Application string

	// Receiver identifies the pretend receiver.
	ReceivingApplication string
	ReceivingFacility    string

	// Version is the HL7 version to claim. Defaults to 2.5.1.
	Version string

	// Since anchors the timestamps. Zero means recent.
	Since time.Time

	// Spread is how far apart the messages are in time. Zero means a few seconds
	// apart, which looks like a real feed rather than a burst with identical
	// timestamps that no sorting can distinguish.
	Spread time.Duration

	// Awkward is the proportion of messages given a legal but inconvenient shape,
	// between 0 and 1. Zero uses the default; set Perfect to turn it off.
	Awkward float64

	// Perfect turns off awkward messages, for a demo where every message should
	// sail through.
	Perfect bool

	// Patients is how many distinct patients to cycle through. A feed about one
	// patient does not exercise anything; a feed where every message is a
	// different patient does not either, because real feeds revisit people.
	Patients int
}

// Defaults.
const (
	DefaultVersion  = "2.5.1"
	DefaultAwkward  = 1.0 / 12.0
	DefaultPatients = 8
	DefaultSpread   = 7 * time.Second
)

func (o Options) normalise() Options {
	if o.Kind == "" {
		o.Kind = ADT
	}
	if o.Count <= 0 {
		o.Count = 1
	}
	if o.Version == "" {
		o.Version = DefaultVersion
	}
	if o.Facility == "" {
		o.Facility = "SYNTHSITE"
	}
	if o.Application == "" {
		o.Application = "SYNTHEHR"
	}
	if o.ReceivingApplication == "" {
		o.ReceivingApplication = "PERFUSE"
	}
	if o.ReceivingFacility == "" {
		o.ReceivingFacility = "TESTFAC"
	}
	if o.Since.IsZero() {
		o.Since = time.Now().Add(-time.Duration(o.Count) * DefaultSpread)
	}
	if o.Spread <= 0 {
		o.Spread = DefaultSpread
	}
	if o.Awkward <= 0 {
		o.Awkward = DefaultAwkward
	}
	if o.Perfect {
		o.Awkward = 0
	}
	if o.Patients <= 0 {
		o.Patients = DefaultPatients
	}
	return o
}

// Message is one generated message and what it is meant to be.
type Message struct {
	// Raw is the ER7 bytes, segments separated by carriage returns.
	Raw []byte

	// ControlID is MSH-10.
	ControlID string
	// Type and Event are MSH-9.1 and MSH-9.2.
	Type  string
	Event string
	// Patient is the identifier used, so a caller can assert on it.
	Patient string
	// At is the message timestamp.
	At time.Time
	// Awkward records that this one was deliberately given an inconvenient shape,
	// and what shape, so a test that fails on it can tell why.
	Awkward string
}

// Generator produces messages.
type Generator struct {
	opts Options
	rng  *rand.Rand
	seq  int
}

// New builds a generator.
func New(opts Options) *Generator {
	opts = opts.normalise()
	seed := opts.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &Generator{
		opts: opts,
		// Two seeds because PCG takes two; deriving the second keeps one Seed
		// option reproducible.
		rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
}

// All produces the whole batch.
func (g *Generator) All() []Message {
	out := make([]Message, 0, g.opts.Count)
	for range g.opts.Count {
		out = append(out, g.Next())
	}
	return out
}

// Next produces one message.
func (g *Generator) Next() Message {
	g.seq++
	at := g.opts.Since.Add(time.Duration(g.seq-1) * g.opts.Spread)
	p := g.patient(g.seq)

	awkward := ""
	if g.rng.Float64() < g.opts.Awkward {
		awkward = awkwardShapes[g.rng.IntN(len(awkwardShapes))]
	}

	switch g.opts.Kind {
	case ORU:
		return g.oru(p, at, awkward)
	case ORM:
		return g.orm(p, at, awkward)
	case MDM:
		return g.mdm(p, at, awkward)
	default:
		return g.adt(p, at, awkward)
	}
}

// awkwardShapes are legal things senders really do that a channel should survive.
var awkwardShapes = []string{
	// An optional field simply absent. The commonest real-world shape and the one
	// most transformations forget about.
	"missing-optional",
	// A field with a component present and the rest empty.
	"empty-components",
	// Trailing empty fields, which some senders emit and some trim.
	"trailing-separators",
	// A repeating field with two values, where code often reads only the first.
	"extra-repetition",
	// An escape sequence in a text field: an ampersand is the subcomponent
	// separator, so a name containing one has to be escaped.
	"escaped-value",
	// A Z segment, which every real site has and no standard describes.
	"z-segment",
}

// patient returns a stable pretend patient for a sequence number, so a feed
// revisits people the way a real one does.
func (g *Generator) patient(seq int) person {
	idx := (seq - 1) % g.opts.Patients
	base := people[idx%len(people)]
	base.MRN = fmt.Sprintf("SYN%06d", 100000+idx)
	base.AccountNumber = fmt.Sprintf("ACC%06d", 500000+idx)
	return base
}

type person struct {
	Family        string
	Given         string
	Middle        string
	Sex           string
	Born          string
	MRN           string
	AccountNumber string
	Street        string
	City          string
	State         string
	Postal        string
}

// people are deliberately and obviously fictional. See the package comment: a
// generator that produced plausible real names would eventually put one into a
// bug report.
var people = []person{
	{Family: "Testpatient", Given: "Alpha", Middle: "Q", Sex: "F", Born: "19910228",
		Street: "1 Example Way", City: "Springfield", State: "ZZ", Postal: "00001"},
	{Family: "Sampleson", Given: "Bravo", Sex: "M", Born: "19700101",
		Street: "2 Placeholder Rd", City: "Springfield", State: "ZZ", Postal: "00002"},
	{Family: "Fixtureton", Given: "Charlie", Middle: "R", Sex: "M", Born: "19850715",
		Street: "3 Synthetic St", City: "Testville", State: "ZZ", Postal: "00003"},
	{Family: "Mocklin", Given: "Delta", Sex: "F", Born: "20010930",
		Street: "4 Notreal Ave", City: "Testville", State: "ZZ", Postal: "00004"},
	{Family: "Stubbs", Given: "Echo", Middle: "T", Sex: "F", Born: "19631112",
		Street: "5 Invented Ln", City: "Nowhere", State: "ZZ", Postal: "00005"},
	{Family: "Dummett", Given: "Foxtrot", Sex: "M", Born: "19781203",
		Street: "6 Imaginary Ct", City: "Nowhere", State: "ZZ", Postal: "00006"},
	{Family: "Placeholder", Given: "Golf", Sex: "U", Born: "19950620",
		Street: "7 Fabricated Dr", City: "Springfield", State: "ZZ", Postal: "00007"},
	{Family: "Exampleby", Given: "Hotel", Middle: "K", Sex: "M", Born: "19520408",
		Street: "8 Contrived Pl", City: "Testville", State: "ZZ", Postal: "00008"},
}

func (p person) nameField(includeMiddle bool) string {
	if includeMiddle && p.Middle != "" {
		return fmt.Sprintf("%s^%s^%s", p.Family, p.Given, p.Middle)
	}
	return fmt.Sprintf("%s^%s", p.Family, p.Given)
}

// hl7Time formats a timestamp the way a real sender does, with an offset.
//
// The offset is included on purpose. HL7 permits a bare local time, and a
// generator that always omitted the offset would never exercise the code that has
// to guess one — which is exactly the code most likely to be wrong.
func hl7Time(t time.Time) string {
	return t.Format("20060102150405-0700")
}

func hl7Date(t time.Time) string {
	return t.Format("20060102")
}

// msh builds the header.
func (g *Generator) msh(kind, event, structure, controlID string, at time.Time) string {
	return fmt.Sprintf("MSH|^~\\&|%s|%s|%s|%s|%s||%s^%s^%s|%s|P|%s",
		g.opts.Application, g.opts.Facility,
		g.opts.ReceivingApplication, g.opts.ReceivingFacility,
		hl7Time(at), kind, event, structure, controlID, g.opts.Version)
}

func (g *Generator) controlID(prefix string) string {
	return fmt.Sprintf("%s%06d", prefix, g.seq)
}

// pid builds the patient identification segment.
func (g *Generator) pid(p person, awkward string) string {
	identifiers := fmt.Sprintf("%s^^^%s^MR", p.MRN, g.opts.Facility)
	if awkward == "extra-repetition" {
		// Two identifiers. Code that reads only the first repetition is a common
		// and quiet bug, so the generator should produce this shape.
		identifiers += fmt.Sprintf("~%s^^^SSA^SS", "999"+p.MRN[3:])
	}

	name := p.nameField(g.rng.IntN(2) == 0)
	if awkward == "escaped-value" {
		// An ampersand is the subcomponent separator, so a name containing one has
		// to arrive escaped. A parser that resolves escapes only sometimes will
		// show it.
		name = fmt.Sprintf("%s \\T\\ Partner^%s", p.Family, p.Given)
	}

	address := fmt.Sprintf("%s^^%s^%s^%s", p.Street, p.City, p.State, p.Postal)
	if awkward == "empty-components" {
		address = fmt.Sprintf("%s^^^^", p.Street)
	}

	sex := p.Sex
	if awkward == "missing-optional" {
		sex = ""
	}

	return fmt.Sprintf("PID|1||%s||%s||%s|%s|||%s",
		identifiers, name, p.Born, sex, address)
}

func (g *Generator) adt(p person, at time.Time, awkward string) Message {
	// Weighted towards A08, because in a real feed most traffic is updates rather
	// than admissions. A generator with an even spread produces a feed nobody has.
	events := []string{"A01", "A08", "A08", "A08", "A02", "A03", "A04", "A31", "A28"}
	event := events[g.rng.IntN(len(events))]

	structure := "ADT_A01"
	switch event {
	case "A02":
		structure = "ADT_A02"
	case "A03":
		structure = "ADT_A03"
	case "A28", "A31":
		structure = "ADT_A05"
	case "A08":
		structure = "ADT_A08"
	}

	controlID := g.controlID("ADT")
	segments := []string{
		g.msh("ADT", event, structure, controlID, at),
		fmt.Sprintf("EVN|%s|%s", event, hl7Time(at.Add(-time.Minute))),
		g.pid(p, awkward),
	}

	// A28 is a registration with no visit, so it legitimately has no PV1. Emitting
	// one anyway would be the generator teaching a wrong lesson.
	if event != "A28" {
		class := "I"
		if event == "A04" {
			class = "O"
		}
		ward := []string{"ICU", "MED", "SUR", "ED"}[g.rng.IntN(4)]
		pv1 := fmt.Sprintf("PV1|1|%s|%s^%d^%02d^%s||||%s^Attending^Doctor|||%s",
			class, ward, 100+g.rng.IntN(20), 1+g.rng.IntN(4), g.opts.Facility,
			fmt.Sprintf("D%04d", 1000+g.rng.IntN(500)),
			[]string{"MED", "SUR", "CAR", "ORT"}[g.rng.IntN(4)])
		if event == "A03" {
			// A discharge carries a discharge time and disposition, and a channel
			// that maps encounters needs to see them.
			pv1 += strings.Repeat("|", 34) + hl7Time(at)
		}
		if awkward == "trailing-separators" {
			pv1 += "||||"
		}
		segments = append(segments, pv1)
	}

	if awkward == "z-segment" {
		segments = append(segments,
			fmt.Sprintf("ZPD|1|%s|LOCAL", p.AccountNumber))
	}

	return Message{
		Raw:       []byte(strings.Join(segments, "\r") + "\r"),
		ControlID: controlID,
		Type:      "ADT",
		Event:     event,
		Patient:   p.MRN,
		At:        at,
		Awkward:   awkward,
	}
}

// observation is one lab result the generator knows how to emit.
type observation struct {
	Code   string
	Name   string
	Unit   string
	Low    float64
	High   float64
	Digits int
}

var observations = []observation{
	{"718-7", "Hemoglobin", "g/dL", 12.0, 16.0, 1},
	{"6690-2", "Leukocytes", "10*3/uL", 4.0, 11.0, 1},
	{"777-3", "Platelets", "10*3/uL", 150, 400, 0},
	{"2345-7", "Glucose", "mg/dL", 70, 110, 0},
	{"2160-0", "Creatinine", "mg/dL", 0.6, 1.3, 2},
	{"2951-2", "Sodium", "mmol/L", 135, 145, 0},
	{"2823-3", "Potassium", "mmol/L", 3.5, 5.1, 1},
	{"1751-7", "Albumin", "g/dL", 3.5, 5.0, 1},
}

func (g *Generator) oru(p person, at time.Time, awkward string) Message {
	controlID := g.controlID("ORU")
	filler := fmt.Sprintf("F%08d", 10000000+g.seq)

	segments := []string{
		g.msh("ORU", "R01", "ORU_R01", controlID, at),
		g.pid(p, awkward),
		fmt.Sprintf("PV1|1|I|MED^%d^01^%s", 100+g.rng.IntN(20), g.opts.Facility),
		fmt.Sprintf("OBR|1||%s|CBC^Complete Blood Count^L|||%s|||||||%s||||||||%s|F",
			filler, hl7Time(at.Add(-30*time.Minute)),
			hl7Time(at.Add(-35*time.Minute)), hl7Time(at)),
	}

	// Between two and five results, because a real panel has several and code that
	// handles one OBX often mishandles the third.
	count := 2 + g.rng.IntN(4)
	for i := range count {
		o := observations[(g.seq+i)%len(observations)]

		// Roughly a fifth are out of range. Abnormal flags are the reason anybody
		// reads OBX-8, so a generator that produced only normal results would leave
		// that path untested.
		value := o.Low + g.rng.Float64()*(o.High-o.Low)
		flag := "N"
		switch {
		case g.rng.Float64() < 0.12:
			value = o.High * (1.1 + g.rng.Float64()*0.5)
			flag = "H"
		case g.rng.Float64() < 0.1:
			value = o.Low * (0.4 + g.rng.Float64()*0.4)
			flag = "L"
		}

		obx := fmt.Sprintf("OBX|%d|NM|%s^%s^LN||%.*f|%s|%.*f-%.*f|%s|||F|||%s",
			i+1, o.Code, o.Name, o.Digits, value, o.Unit,
			o.Digits, o.Low, o.Digits, o.High, flag, hl7Time(at))

		if awkward == "missing-optional" && i == 1 {
			// No units and no reference range. Senders do this, and a converter that
			// assumes both are present will produce a resource with no unit.
			obx = fmt.Sprintf("OBX|%d|NM|%s^%s^LN||%.*f||||||F",
				i+1, o.Code, o.Name, o.Digits, value)
		}
		segments = append(segments, obx)

		if awkward == "escaped-value" && i == 0 {
			segments = append(segments,
				"NTE|1|L|Specimen slightly haemolysed \\T\\ result may be affected")
		}
	}

	if awkward == "z-segment" {
		segments = append(segments, fmt.Sprintf("ZLB|1|%s|AUTOVERIFIED", filler))
	}

	return Message{
		Raw:       []byte(strings.Join(segments, "\r") + "\r"),
		ControlID: controlID,
		Type:      "ORU",
		Event:     "R01",
		Patient:   p.MRN,
		At:        at,
		Awkward:   awkward,
	}
}

func (g *Generator) orm(p person, at time.Time, awkward string) Message {
	controlID := g.controlID("ORM")
	placer := fmt.Sprintf("P%08d", 20000000+g.seq)

	orders := []struct{ code, name string }{
		{"CBC", "Complete Blood Count"},
		{"BMP", "Basic Metabolic Panel"},
		{"CXR", "Chest X-Ray"},
		{"URIN", "Urinalysis"},
		{"TSH", "Thyroid Stimulating Hormone"},
	}
	o := orders[g.rng.IntN(len(orders))]

	control := []string{"NW", "NW", "NW", "CA", "XO"}[g.rng.IntN(5)]

	segments := []string{
		g.msh("ORM", "O01", "ORM_O01", controlID, at),
		g.pid(p, awkward),
		fmt.Sprintf("PV1|1|I|MED^%d^01^%s", 100+g.rng.IntN(20), g.opts.Facility),
		fmt.Sprintf("ORC|%s|%s||||||||||%s^Ordering^Doctor",
			control, placer, fmt.Sprintf("D%04d", 1000+g.rng.IntN(500))),
		fmt.Sprintf("OBR|1|%s||%s^%s^L|||%s", placer, o.code, o.name, hl7Time(at)),
	}

	if awkward == "z-segment" {
		segments = append(segments, "ZOR|1|ROUTINE")
	}
	if awkward == "trailing-separators" {
		segments[len(segments)-1] += "|||||"
	}

	return Message{
		Raw:       []byte(strings.Join(segments, "\r") + "\r"),
		ControlID: controlID,
		Type:      "ORM",
		Event:     "O01",
		Patient:   p.MRN,
		At:        at,
		Awkward:   awkward,
	}
}

// SyntheticSurnames returns the family names this generator invents.
//
// Exported so the fixture check in internal/compliance can read them rather than keeping its
// own copy. Two lists of the same names drift, and the way that drift presents is a guard
// rejecting output produced by this repository's own generator - which is what happened: seven
// of the eight names here were absent from that check, so writing a fixture from generate
// output, the obvious thing to do, failed the build with a warning about real patient data.
//
// Sorted, because a caller printing them should not see a different order each run.
func SyntheticSurnames() []string {
	out := make([]string, 0, len(people))
	for _, p := range people {
		out = append(out, p.Family)
	}
	sort.Strings(out)
	return out
}
