package publichealth

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/hl7"
)

// Electronic laboratory reporting: a lab's ORU^R01, reshaped into the HL7 2.5.1 ELR message public health agencies receive
// (HL7 Version 2.5.1 Implementation Guide: Electronic Laboratory Reporting to Public Health, Release 1, US Realm, with its
// errata and clarifications). Only the orders carrying a reportable result are sent.
//
// The source's own content is carried, never invented: a field the guide requires that the lab did not send stays empty and is
// named in the notes, because a guessed specimen or ordering provider in a report to public health is worse than a gap the
// agency can see and ask about. The parts that are the sender's rather than the lab's - who sends, who receives, the ordering
// facility when the order does not name it - come from configuration.

// elrProfile is MSH-21 for ELR 2.5.1 Release 1 without acknowledgement, the only profile the guide defines for a sender.
const elrProfile = "PHLabReport-NoAck^phLabResultsELRv251^2.16.840.1.113883.9.11^ISO"

// HD is an HL7 hierarchic designator: a name, and the universal identifier and its type (ISO for an OID, CLIA for a lab's
// CLIA number). ELR identifies every application and facility this way.
type HD struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	ID        string `json:"id" yaml:"id"`
	Type      string `json:"type" yaml:"type"`
}

func (h HD) encode(sep string) string {
	return strings.TrimRight(strings.Join([]string{hl7.Escape(h.Namespace, hl7.DefaultSeparators()),
		hl7.Escape(h.ID, hl7.DefaultSeparators()), h.Type}, sep), sep)
}

func (h HD) complete() bool { return h.ID != "" && h.Type != "" }

// Software is the SFT segment Perfuse adds for itself, since it changes the message on the way.
type Software struct {
	Vendor, Version, Name, BinaryID string
	Installed                       time.Time
}

// ELROptions are the parts of an ELR message that are the sender's, not the lab's.
type ELROptions struct {
	Now                                     time.Time
	SendingApplication, SendingFacility     HD
	ReceivingApplication, ReceivingFacility HD
	// Processing is MSH-11: P production (the default), T training, D debugging.
	Processing string
	Software   Software
	// OrderingFacility is used for ORC-21 to ORC-24 when the order does not name the facility, which ELR requires and many
	// lab feeds leave out because the lab and the facility are the same organisation.
	OrderingFacility Facility
	// PerformingLab is used for OBX-23 and OBX-24 when a result does not say which lab performed it; CLIA is its CLIA number.
	PerformingLab     Facility
	PerformingLabCLIA string
	// PlacerAuthority and FillerAuthority are the assigning authorities given to order and specimen numbers that arrive
	// without one (ORC-2, ORC-3, OBR-2, OBR-3, SPM-2): the ordering system's, and the lab's.
	PlacerAuthority, FillerAuthority HD
}

// ELR is a built report and what went into it.
type ELR struct {
	Message  []byte    `json:"-"`
	Triggers []Trigger `json:"triggers"`
	// Notes are what the report could not take from the source, and what it changed.
	Notes []string `json:"notes"`
	// Dropped is how many orders were left out because nothing in them is reportable.
	Dropped int `json:"dropped"`
}

// ErrNothingReportable is returned, with the report, when no result in the message is reportable.
var ErrNothingReportable = errors.New("nothing in this message is reportable")

// v2 coding system names, as OBR-4, OBX-3 and OBX-5 carry them, for the trigger lookup.
var v2Systems = map[string]string{
	"LN": systemURLs["LOINC"], "SCT": systemURLs["SNOMED"], "SNM": systemURLs["SNOMED"],
	"I10": systemURLs["ICD-10"], "I10C": systemURLs["ICD-10"], "ICD10CM": systemURLs["ICD-10"],
}

type elrSegment []string // fields as encoded with the standard separators; [0] is the segment name

func (s elrSegment) get(n int) string {
	if n < len(s) {
		return s[n]
	}
	return ""
}

func (s *elrSegment) set(n int, v string) {
	for len(*s) <= n {
		*s = append(*s, "")
	}
	(*s)[n] = v
}

func (s elrSegment) String() string {
	end := len(s)
	for end > 1 && s[end-1] == "" {
		end--
	}
	return strings.Join(s[:end], "|")
}

type elrObservation struct {
	obx   elrSegment
	notes []elrSegment
}

type elrSpecimen struct {
	spm elrSegment
	obx []elrSegment
}

type elrOrder struct {
	orc, obr     elrSegment
	notes        []elrSegment
	observations []elrObservation
	specimens    []elrSpecimen
	triggers     []Trigger
}

// BuildELR reshapes a lab's ORU^R01 into an ELR 2.5.1 message, keeping the orders with a reportable result.
func BuildELR(m *hl7.Message, triggers *TriggerSet, opts ELROptions) (*ELR, error) {
	if typ, event, _ := m.Type(); typ != "ORU" || event != "R01" {
		return nil, fmt.Errorf("ELR is built from a lab result (ORU^R01), and this is %s^%s", typ, event)
	}
	var missing []string
	for name, h := range map[string]HD{"sending facility": opts.SendingFacility, "receiving application": opts.ReceivingApplication,
		"receiving facility": opts.ReceivingFacility} {
		if !h.complete() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ELR identifies sender and receiver by OID or CLIA number; give the %s an id and a type",
			strings.Join(missing, " and "))
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	report := &ELR{}
	note := func(format string, a ...any) { report.Notes = append(report.Notes, fmt.Sprintf(format, a...)) }

	sep := m.Separators()
	var pid, pv1 elrSegment
	var head, sft []elrSegment // PID's NK1s, and the sender's SFTs
	var orders []*elrOrder
	var cur *elrOrder
	lastWasSPM := false
	skipped := map[string]bool{}
	for i := 0; i < m.SegmentCount(); i++ {
		s, _ := m.SegmentAt(i)
		name := s.Name()
		if name == "MSH" {
			continue
		}
		seg := splitSegment(s.Raw(), sep)
		switch name {
		case "SFT":
			sft = append(sft, seg)
		case "PID":
			if pid == nil {
				pid = seg
			}
		case "NK1":
			head = append(head, seg)
		case "PV1":
			pv1 = seg
		case "ORC":
			cur = &elrOrder{orc: seg}
			orders = append(orders, cur)
			lastWasSPM = false
		case "OBR":
			if cur == nil || cur.obr != nil {
				cur = &elrOrder{}
				orders = append(orders, cur)
			}
			cur.obr = seg
			lastWasSPM = false
		case "OBX":
			switch {
			case cur == nil || cur.obr == nil:
				note("an OBX before any OBR was left out")
			case lastWasSPM:
				sp := &cur.specimens[len(cur.specimens)-1]
				sp.obx = append(sp.obx, seg)
			default:
				cur.observations = append(cur.observations, elrObservation{obx: seg})
			}
		case "NTE":
			switch {
			case cur == nil:
				if !skipped["NTE"] {
					note("a note before the first order was left out")
					skipped["NTE"] = true
				}
			case len(cur.observations) > 0 && !lastWasSPM:
				o := &cur.observations[len(cur.observations)-1]
				o.notes = append(o.notes, seg)
			default:
				cur.notes = append(cur.notes, seg)
			}
		case "SPM":
			if cur == nil || cur.obr == nil {
				note("an SPM before any OBR was left out")
				continue
			}
			cur.specimens = append(cur.specimens, elrSpecimen{spm: seg})
			lastWasSPM = true
		default:
			if !skipped[name] {
				skipped[name] = true
				note("%s is not part of an ELR message and was left out", name)
			}
		}
	}
	if pid == nil {
		return nil, errors.New("the message has no PID, and a lab report to public health is about a patient")
	}

	var keep []*elrOrder
	for _, o := range orders {
		if o.obr == nil {
			note("an ORC with no OBR was left out")
			continue
		}
		o.triggers = orderTriggers(o, triggers)
		if len(o.triggers) == 0 {
			report.Dropped++
			continue
		}
		keep = append(keep, o)
		report.Triggers = append(report.Triggers, o.triggers...)
	}
	if len(keep) == 0 {
		return report, ErrNothingReportable
	}

	out := []string{elrMSH(m, opts)}
	for _, s := range sft {
		out = append(out, s.String())
	}
	out = append(out, elrSFT(opts.Software))
	pid.set(1, "1")
	pid.set(10, raceCodes(pid.get(10)))
	pid.set(22, ethnicityCodes(pid.get(22)))
	elrPatientChecks(pid, note)
	out = append(out, pid.String())
	for i, nk := range head {
		nk.set(1, strconv.Itoa(i+1))
		out = append(out, nk.String())
	}
	if pv1 != nil {
		pv1.set(1, "1")
		out = append(out, pv1.String())
	}
	for i, o := range keep {
		out = append(out, elrOrderSegments(o, i+1, opts, note)...)
	}
	report.Message = []byte(strings.Join(out, "\r") + "\r")
	return report, nil
}

// splitSegment splits a segment into fields and re-encodes each with the standard separators, so a message sent with unusual
// encoding characters comes out as ELR requires (MSH-2 ^~\&). A standard separator that was plain text in the source is
// escaped, or it would become structure.
func splitSegment(raw []byte, sep hl7.Separators) elrSegment {
	std := hl7.DefaultSeparators()
	var fields elrSegment
	var b strings.Builder
	for _, c := range raw {
		switch c {
		case sep.Field:
			fields = append(fields, b.String())
			b.Reset()
		case sep.Component:
			b.WriteByte(std.Component)
		case sep.Repeat:
			b.WriteByte(std.Repeat)
		case sep.Escape:
			b.WriteByte(std.Escape)
		case sep.Subcomponent:
			b.WriteByte(std.Subcomponent)
		case std.Component:
			b.WriteString(`\S\`)
		case std.Repeat:
			b.WriteString(`\R\`)
		case std.Escape:
			b.WriteString(`\E\`)
		case std.Subcomponent:
			b.WriteString(`\T\`)
		case std.Field:
			b.WriteString(`\F\`)
		default:
			b.WriteByte(c)
		}
	}
	return append(fields, b.String())
}

func elrMSH(m *hl7.Message, opts ELROptions) string {
	msh := make(elrSegment, 22)
	msh[0] = "MSH"
	msh[2] = `^~\&`
	msh[3] = opts.SendingApplication.encode("^")
	msh[4] = opts.SendingFacility.encode("^")
	msh[5] = opts.ReceivingApplication.encode("^")
	msh[6] = opts.ReceivingFacility.encode("^")
	msh[7] = opts.Now.Format("20060102150405-0700")
	msh[9] = "ORU^R01^ORU_R01"
	msh[10] = "ELR" + m.ControlID()
	if len(msh[10]) > 50 {
		msh[10] = msh[10][:50]
	}
	msh[11] = "P"
	if opts.Processing != "" {
		msh[11] = opts.Processing
	}
	msh[12] = "2.5.1"
	msh[15] = "NE"
	msh[16] = "NE"
	msh[21] = elrProfile
	// MSH-1 is the field separator itself, so the fields are joined from MSH-2.
	return "MSH|" + strings.Join(msh[2:], "|")
}

func elrSFT(sw Software) string {
	esc := func(s string) string { return hl7.Escape(s, hl7.DefaultSeparators()) }
	s := elrSegment{"SFT", esc(sw.Vendor) + "^L", esc(sw.Version), esc(sw.Name), esc(sw.BinaryID)}
	if !sw.Installed.IsZero() {
		s.set(6, sw.Installed.Format("20060102150405-0700"))
	}
	return s.String()
}

// elrPatientChecks names what ELR requires of the patient that the lab did not send.
func elrPatientChecks(pid elrSegment, note func(string, ...any)) {
	if id := pid.get(3); !strings.Contains(id, "&") {
		note("PID-3: the patient identifier has no assigning authority OID, which ELR requires")
	}
	for n, what := range map[int]string{5: "the patient's name", 7: "the date of birth", 8: "the administrative sex"} {
		if pid.get(n) == "" {
			note("PID-%d: the lab did not send %s", n, what)
		}
	}
}

// orderTriggers finds the reportable codes in an order: the test ordered, each test resulted, and each coded result.
func orderTriggers(o *elrOrder, triggers *TriggerSet) []Trigger {
	var found []Trigger
	seen := map[string]bool{}
	look := func(where, field string) {
		for _, rep := range strings.Split(field, "~") {
			c := strings.Split(rep, "^")
			for _, at := range []int{0, 3} {
				if len(c) <= at+2 {
					continue
				}
				code, text, sysName := c[at], c[at+1], c[at+2]
				system := v2Systems[strings.ToUpper(sysName)]
				if system == "" || code == "" {
					continue
				}
				tc, ok := triggers.match(system, code)
				if !ok || seen[system+"|"+code] {
					continue
				}
				seen[system+"|"+code] = true
				found = append(found, Trigger{Resource: where, System: system, Code: code, Display: text,
					CodeDisplay: tc.Display, Condition: tc.Condition, ValueSet: tc.ValueSet, Version: tc.Version})
			}
		}
	}
	look("OBR-4", o.obr.get(4))
	for _, ob := range o.observations {
		look("OBX-3", ob.obx.get(3))
		if t := ob.obx.get(2); t == "CE" || t == "CWE" {
			look("OBX-5", ob.obx.get(5))
		}
	}
	return found
}

func elrOrderSegments(o *elrOrder, seq int, opts ELROptions, note func(string, ...any)) []string {
	obr := o.obr
	orc := o.orc
	if orc == nil {
		orc = elrSegment{"ORC"}
		note("OBR %d: the lab sent no ORC, so it was built from the OBR", seq)
	}
	orc.set(1, "RE")
	if orc.get(2) == "" {
		orc.set(2, obr.get(2))
	}
	if orc.get(3) == "" {
		orc.set(3, obr.get(3))
	}
	if orc.get(12) == "" {
		orc.set(12, obr.get(16))
	}
	if orc.get(21) == "" && opts.OrderingFacility.Name != "" {
		f := opts.OrderingFacility
		esc := func(s string) string { return hl7.Escape(s, hl7.DefaultSeparators()) }
		orc.set(21, esc(f.Name)+"^L")
		orc.set(22, address(f))
		orc.set(23, phone(f.Phone))
		note("OBR %d: the ordering facility (ORC-21 to ORC-23) is the configured facility, not the lab's", seq)
	}
	for n, what := range map[int]string{21: "the ordering facility", 22: "its address", 23: "its phone"} {
		if orc.get(n) == "" {
			note("ORC-%d: nothing says %s, which ELR requires", n, what)
		}
	}
	obr.set(1, strconv.Itoa(seq))
	for _, seg := range []*elrSegment{&orc, &obr} {
		withAuthority(seg, 2, opts.PlacerAuthority)
		withAuthority(seg, 3, opts.FillerAuthority)
	}
	orc.set(12, providerIDs(orc.get(12)))
	obr.set(16, providerIDs(obr.get(16)))
	obr.set(28, providerIDs(obr.get(28)))
	for n, what := range map[int]string{3: "the filler order number", 7: "when the specimen was collected", 16: "the ordering provider",
		22: "when the result was reported", 25: "the result status"} {
		if obr.get(n) == "" {
			note("OBR %d: OBR-%d is empty; the lab did not send %s", seq, n, what)
		}
	}
	elrOrderChecks(orc, obr, seq, note)
	segs := []string{orc.String(), obr.String()}
	for i, n := range o.notes {
		n.set(1, strconv.Itoa(i+1))
		segs = append(segs, n.String())
	}
	for i, ob := range o.observations {
		ob.obx.set(1, strconv.Itoa(i+1))
		ob.obx.set(16, providerIDs(ob.obx.get(16)))
		ob.obx.set(25, providerIDs(ob.obx.get(25)))
		if ob.obx.get(23) == "" && opts.PerformingLab.Name != "" {
			lab := opts.PerformingLab
			esc := func(s string) string { return hl7.Escape(s, hl7.DefaultSeparators()) }
			xon := esc(lab.Name)
			if opts.PerformingLabCLIA != "" {
				xon += "^^^^^CLIA&2.16.840.1.113883.4.7&ISO^XX^^^" + esc(opts.PerformingLabCLIA)
			}
			ob.obx.set(23, xon)
			if ob.obx.get(24) == "" {
				ob.obx.set(24, address(lab))
			}
			if i == 0 {
				note("OBR %d: the performing lab (OBX-23, OBX-24) is the configured lab; the result did not name one", seq)
			}
		}
		if ob.obx.get(23) == "" {
			note("OBR %d OBX %d: OBX-23 is empty; nothing says which lab performed the test, which ELR requires", seq, i+1)
		}
		if ob.obx.get(2) == "CE" {
			// ELR 2.5.1 has no CE results: a coded result is CWE, with the same components.
			ob.obx.set(2, "CWE")
		}
		segs = append(segs, ob.obx.String())
		for j, n := range ob.notes {
			n.set(1, strconv.Itoa(j+1))
			segs = append(segs, n.String())
		}
	}
	specimens := o.specimens
	if len(specimens) == 0 {
		specimens = []elrSpecimen{{spm: specimenFromOBR(obr)}}
		note("OBR %d: the lab sent no SPM, so the specimen was taken from OBR-15 and the collection time from OBR-7", seq)
	}
	for i, sp := range specimens {
		sp.spm.set(1, strconv.Itoa(i+1))
		if sp.spm.get(4) == "" {
			note("OBR %d: SPM-4 is empty; nothing says what the specimen was, which ELR requires", seq)
		}
		withEIPAuthority(&sp.spm, 2, opts.PlacerAuthority, opts.FillerAuthority)
		if sp.spm.get(18) == "" {
			note("OBR %d: SPM-18 is empty; the lab did not send when it received the specimen, which ELR requires", seq)
		}
		if sp.spm.get(17) == "" && obr.get(7) != "" {
			sp.spm.set(17, obr.get(7))
		}
		segs = append(segs, sp.spm.String())
		for j, x := range sp.obx {
			x.set(1, strconv.Itoa(j+1))
			segs = append(segs, x.String())
		}
	}
	return segs
}

// specimenFromOBR builds an SPM from the fields that carried the specimen before 2.5: OBR-15 (specimen source) for the type,
// OBR-7 for collection and OBR-14 for receipt.
func specimenFromOBR(obr elrSegment) elrSegment {
	spm := elrSegment{"SPM", "1"}
	if id := obr.get(3); id != "" {
		spm.set(2, "^"+strings.ReplaceAll(id, "^", "&"))
	}
	// OBR-15.1 is the specimen type as a coded element in subcomponents; SPM-4 is the same coded element as components.
	spm.set(4, strings.ReplaceAll(strings.Split(obr.get(15), "^")[0], "&", "^"))
	spm.set(17, obr.get(7))
	spm.set(18, obr.get(14))
	return spm
}

// elrOrderChecks is a hook for checks that need both segments once they are final.
func elrOrderChecks(orc, obr elrSegment, seq int, note func(string, ...any)) {
	if orc.get(12) != obr.get(16) {
		note("OBR %d: ORC-12 and OBR-16 name different ordering providers; ELR requires them to agree", seq)
	}
}

// withAuthority gives an entity identifier (EI) the assigning authority it arrived without.
func withAuthority(seg *elrSegment, n int, h HD) {
	v := seg.get(n)
	if v == "" || !h.complete() {
		return
	}
	c := strings.Split(v, "^")
	if len(c) >= 3 && c[2] != "" {
		return
	}
	seg.set(n, c[0]+"^"+h.encode("^"))
}

// withEIPAuthority does the same for SPM-2, whose two parts (placer and filler number) are EIs written in subcomponents.
func withEIPAuthority(seg *elrSegment, n int, placer, filler HD) {
	v := seg.get(n)
	if v == "" {
		return
	}
	c := strings.SplitN(v, "^", 2)
	for len(c) < 2 {
		c = append(c, "")
	}
	for i, h := range []HD{placer, filler} {
		sub := strings.Split(c[i], "&")
		if sub[0] == "" || (len(sub) >= 3 && sub[2] != "") || !h.complete() {
			continue
		}
		c[i] = sub[0] + "&" + h.encode("&")
	}
	seg.set(n, strings.TrimRight(strings.Join(c, "^"), "^"))
}

// npiAuthority is the NPI's assigning authority in full: CMS's OID for the NPI registry.
const npiAuthority = "NPI&2.16.840.1.113883.4.6&ISO"

// providerIDs completes each XCN whose identifier is an NPI known only by the name "NPI": ELR requires the assigning
// authority's OID (XCN-9) and the identifier type (XCN-13), and both are fixed for an NPI.
func providerIDs(field string) string {
	if field == "" {
		return field
	}
	reps := strings.Split(field, "~")
	for r, rep := range reps {
		c := strings.Split(rep, "^")
		for len(c) < 13 {
			c = append(c, "")
		}
		if c[0] == "" {
			continue
		}
		auth := strings.Split(c[8], "&")[0]
		if auth == "NPI" || (c[8] == "" && c[12] == "NPI") {
			c[8] = npiAuthority
			c[12] = "NPI"
		}
		reps[r] = strings.TrimRight(strings.Join(c, "^"), "^")
	}
	return strings.Join(reps, "~")
}

// ELR binds race to HL7 table 0005 and ethnicity to 0189. Senders using the CDC race and ethnicity codes send the same race
// codes under another system name, and ethnicity as a code ELR does not use; each keeps the sender's coding as the alternate.
var cdcEthnicity = map[string]string{"2135-2": "H", "2186-5": "N"}

func raceCodes(field string) string {
	return recode(field, func(code, text, system string) (string, string, bool) {
		if strings.EqualFold(system, "CDCREC") {
			return code, text, true
		}
		return "", "", false
	}, "HL70005")
}

func ethnicityCodes(field string) string {
	return recode(field, func(code, text, system string) (string, string, bool) {
		if strings.EqualFold(system, "CDCREC") && cdcEthnicity[code] != "" {
			return cdcEthnicity[code], text, true
		}
		return "", "", false
	}, "HL70189")
}

func recode(field string, to func(code, text, system string) (string, string, bool), system string) string {
	if field == "" {
		return field
	}
	reps := strings.Split(field, "~")
	for r, rep := range reps {
		c := strings.Split(rep, "^")
		for len(c) < 3 {
			c = append(c, "")
		}
		code, text, ok := to(c[0], c[1], c[2])
		if !ok || (len(c) > 3 && strings.Join(c[3:], "") != "") {
			continue
		}
		reps[r] = strings.Join([]string{code, text, system, c[0], c[1], c[2]}, "^")
	}
	return strings.Join(reps, "~")
}

func address(f Facility) string {
	esc := func(s string) string { return hl7.Escape(s, hl7.DefaultSeparators()) }
	return strings.TrimRight(strings.Join([]string{esc(f.Line), "", esc(f.City), esc(f.State), esc(f.PostalCode), "USA", "B"}, "^"), "^")
}

// phone writes a North American number as ELR wants it, an XTN with country, area and local number in their own components;
// any other number goes in the unformatted component, which ELR does not support, so it is left out.
func phone(number string) string {
	var digits strings.Builder
	for _, r := range number {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) == 11 && d[0] == '1' {
		d = d[1:]
	}
	if len(d) != 10 {
		return ""
	}
	return "^WPN^PH^^1^" + d[:3] + "^" + d[3:]
}

// ELRConfig is the sender's side of ELR as a file: who sends, who receives, and what fills the gaps a lab feed leaves.
type ELRConfig struct {
	SendingApplication   HD       `yaml:"sending_application"`
	SendingFacility      HD       `yaml:"sending_facility"`
	ReceivingApplication HD       `yaml:"receiving_application"`
	ReceivingFacility    HD       `yaml:"receiving_facility"`
	Processing           string   `yaml:"processing"`
	OrderingFacility     Facility `yaml:"ordering_facility"`
	PerformingLab        Facility `yaml:"performing_lab"`
	PerformingLabCLIA    string   `yaml:"performing_lab_clia"`
	PlacerAuthority      HD       `yaml:"placer_authority"`
	FillerAuthority      HD       `yaml:"filler_authority"`
}

// LoadELRConfig reads an ELRConfig, refusing fields it does not know so a misspelt key is not silently ignored.
func LoadELRConfig(path string) (ELRConfig, error) {
	var c ELRConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Processing != "" && c.Processing != "P" && c.Processing != "T" && c.Processing != "D" {
		return c, fmt.Errorf("%s: processing is P, T or D, not %q", path, c.Processing)
	}
	return c, nil
}

// Options turns the file into build options.
func (c ELRConfig) Options(now time.Time, sw Software) ELROptions {
	return ELROptions{Now: now, SendingApplication: c.SendingApplication, SendingFacility: c.SendingFacility,
		ReceivingApplication: c.ReceivingApplication, ReceivingFacility: c.ReceivingFacility, Processing: c.Processing,
		Software: sw, OrderingFacility: c.OrderingFacility, PerformingLab: c.PerformingLab,
		PerformingLabCLIA: c.PerformingLabCLIA, PlacerAuthority: c.PlacerAuthority, FillerAuthority: c.FillerAuthority}
}
