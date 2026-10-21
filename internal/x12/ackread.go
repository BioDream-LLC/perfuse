package x12

import (
	"fmt"
	"strings"
)

// Reading the acknowledgements a trading partner sends back: TA1 for the envelope, 999 (or the older 997) for the transaction
// sets inside it.
//
// A partner that cannot read what it was sent answers with one of these rather than with the transaction asked for, both in
// real time (a CORE server answers a 278 it cannot process with a 999 or a TA1 in place of the 278 response) and in batch (a
// 999 file arrives for every file sent). Treating one as "not the answer expected" loses the only explanation there is, so it is
// read for what it says.

// Acknowledgement is what a TA1 and a 999 or 997 in one interchange report.
type Acknowledgement struct {
	// Interchange is the TA1, when the interchange carried one.
	Interchange *InterchangeAck `json:"interchange,omitempty"`
	// Groups are the 999 or 997 transaction sets, one per functional group acknowledged.
	Groups []GroupAck `json:"groups,omitempty"`
	// Kind is 999 or 997 when there are groups.
	Kind string `json:"kind,omitempty"`
}

// InterchangeAck is a TA1: the status of one interchange's envelope.
type InterchangeAck struct {
	// ControlNumber is the ISA13 of the interchange acknowledged (TA101).
	ControlNumber string `json:"controlNumber"`
	// Code is TA104: A accepted, E accepted with errors, R rejected.
	Code string `json:"code"`
	// Note is TA105, the interchange note code; 000 is no error.
	Note string `json:"note"`
}

// GroupAck is one 999 or 997: the status of one functional group and of its transaction sets.
type GroupAck struct {
	// Functional is AK101, the group's functional identifier (HI for a 278).
	Functional string `json:"functional"`
	// ControlNumber is AK102, the GS06 of the group acknowledged.
	ControlNumber string `json:"controlNumber"`
	// Status is AK901: A accepted, E accepted with errors, P partly accepted, R rejected (M and W also exist: rejected for a
	// failed authentication or message, and for a failed validity check).
	Status string `json:"status"`
	// Sets are the transaction sets reported on (AK2 to IK5 or AK5).
	Sets []SetAck `json:"sets,omitempty"`
}

// SetAck is one transaction set's status inside a 999 or 997.
type SetAck struct {
	// ID and ControlNumber are AK201 and AK202: the transaction set and its ST02.
	ID            string `json:"id"`
	ControlNumber string `json:"controlNumber"`
	// Status is IK501 (AK501 in a 997): A, E, M, R, W or X.
	Status string `json:"status"`
	// Errors are the faults reported: each segment one (IK3/AK3), each element one (IK4/AK4), and the set's own codes (IK502 on).
	Errors []AckError `json:"errors,omitempty"`
}

// AckError is one fault an acknowledgement names.
type AckError struct {
	// Segment and Position are the segment ID and its position in the set (IK301, IK302), for a segment or element fault.
	Segment  string `json:"segment,omitempty"`
	Position string `json:"position,omitempty"`
	// Element is the element's position (IK401), for an element fault.
	Element string `json:"element,omitempty"`
	// Code is the syntax error code (IK304, IK403 or IK502 on); Value is the bad value when the partner copied it (IK404).
	Code  string `json:"code"`
	Value string `json:"value,omitempty"`
}

// IsAcknowledgement reports whether an interchange is an acknowledgement and nothing else: a TA1 with no functional group, or
// 999 or 997 transaction sets.
func (m *Message) IsAcknowledgement() bool {
	if m.transactionSetOfType("999") != nil || m.transactionSetOfType("997") != nil {
		return true
	}
	return len(m.Segments("TA1")) > 0 && len(m.Segments("ST")) == 0
}

// ReadAcknowledgement reads the TA1 and the 999 or 997 transaction sets an interchange carries.
func (m *Message) ReadAcknowledgement() (*Acknowledgement, error) {
	out := &Acknowledgement{}
	if ta1 := m.Segments("TA1"); len(ta1) > 0 {
		s := ta1[0]
		out.Interchange = &InterchangeAck{ControlNumber: s.Element(1).String(), Code: s.Element(4).String(), Note: s.Element(5).String()}
	}
	var group *GroupAck
	var set *SetAck
	var segment AckError
	for _, s := range m.segs {
		switch s.ID {
		case "ST":
			switch id := s.Element(1).String(); id {
			case "999", "997":
				out.Kind = id
				group, set = nil, nil
			default:
				group, set = nil, nil
			}
		case "AK1":
			if out.Kind == "" {
				continue
			}
			out.Groups = append(out.Groups, GroupAck{Functional: s.Element(1).String(), ControlNumber: s.Element(2).String()})
			group = &out.Groups[len(out.Groups)-1]
			set = nil
		case "AK2":
			if group == nil {
				continue
			}
			group.Sets = append(group.Sets, SetAck{ID: s.Element(1).String(), ControlNumber: s.Element(2).String()})
			set = &group.Sets[len(group.Sets)-1]
		case "IK3", "AK3":
			if set == nil {
				continue
			}
			segment = AckError{Segment: s.Element(1).String(), Position: s.Element(2).String(), Code: s.Element(4).String()}
			set.Errors = append(set.Errors, segment)
		case "IK4", "AK4":
			if set == nil {
				continue
			}
			// IK401 is a composite: the element's position, then the component's.
			set.Errors = append(set.Errors, AckError{Segment: segment.Segment, Position: segment.Position,
				Element: s.Element(1).Component(1), Code: s.Element(3).String(), Value: s.Element(4).String()})
		case "IK5", "AK5":
			if set == nil {
				continue
			}
			set.Status = s.Element(1).String()
			for n := 2; n <= 6; n++ {
				if c := s.Element(n).String(); c != "" {
					set.Errors = append(set.Errors, AckError{Code: c})
				}
			}
		case "AK9":
			if group != nil {
				group.Status = s.Element(1).String()
			}
		}
	}
	if out.Interchange == nil && len(out.Groups) == 0 {
		return nil, fmt.Errorf("this interchange carries no TA1, 999 or 997")
	}
	return out, nil
}

// Rejected reports whether anything acknowledged was refused: the interchange, a group, or a transaction set.
func (a *Acknowledgement) Rejected() bool {
	if a.Interchange != nil && a.Interchange.Code == "R" {
		return true
	}
	for _, g := range a.Groups {
		if g.Status == "R" || g.Status == "M" || g.Status == "W" || g.Status == "X" {
			return true
		}
		for _, s := range g.Sets {
			if s.Status == "R" || s.Status == "M" || s.Status == "W" || s.Status == "X" {
				return true
			}
		}
	}
	return false
}

// Summary says in one line what an acknowledgement reports, with the codes and their meaning where known.
func (a *Acknowledgement) Summary() string {
	var parts []string
	if t := a.Interchange; t != nil {
		p := "interchange " + strings.TrimLeft(t.ControlNumber, "0") + " " + ackCodeWord(t.Code)
		if t.Note != "" && t.Note != "000" {
			p += " (TA1 note " + t.Note
			if w := ta1Notes[t.Note]; w != "" {
				p += ": " + w
			}
			p += ")"
		}
		parts = append(parts, p)
	}
	for _, g := range a.Groups {
		p := "group " + g.ControlNumber + " " + ackCodeWord(g.Status)
		for _, s := range g.Sets {
			if s.Status == "A" && len(s.Errors) == 0 {
				continue
			}
			q := "set " + s.ID + " " + s.ControlNumber + " " + ackCodeWord(s.Status)
			var errs []string
			for _, e := range s.Errors {
				where := e.Segment
				if e.Element != "" {
					where += fmt.Sprintf("%02s", e.Element)
				}
				if e.Position != "" {
					where += " at segment " + e.Position
				}
				w := "code " + e.Code
				if where != "" {
					w = where + " " + w
				}
				errs = append(errs, strings.TrimSpace(w))
			}
			if len(errs) > 0 {
				q += " (" + strings.Join(errs, "; ") + ")"
			}
			p += ", " + q
		}
		parts = append(parts, p)
	}
	kind := a.Kind
	if kind == "" {
		kind = "TA1"
	}
	return kind + ": " + strings.Join(parts, "; ")
}

func ackCodeWord(code string) string {
	switch code {
	case "A":
		return "accepted"
	case "E":
		return "accepted with errors"
	case "P":
		return "partly accepted"
	case "R":
		return "rejected"
	case "M":
		return "rejected (message authentication failed)"
	case "W":
		return "rejected (validity check failed)"
	case "X":
		return "rejected (content could not be analysed)"
	case "":
		return "with no status"
	}
	return "with status " + code
}

// ta1Notes are the TA105 interchange note codes (X12 element I18).
var ta1Notes = map[string]string{
	"001": "control numbers in the ISA and IEA do not match",
	"002": "this control standard is not supported",
	"003": "this version of the controls is not supported",
	"004": "the segment terminator is invalid",
	"005": "invalid sender ID qualifier",
	"006": "invalid sender ID",
	"007": "invalid receiver ID qualifier",
	"008": "invalid receiver ID",
	"009": "unknown receiver ID",
	"010": "invalid authorization information qualifier",
	"011": "invalid authorization information",
	"012": "invalid security information qualifier",
	"013": "invalid security information",
	"014": "invalid interchange date",
	"015": "invalid interchange time",
	"016": "invalid interchange standards identifier",
	"017": "invalid interchange version ID",
	"018": "invalid interchange control number",
	"019": "invalid acknowledgment requested value",
	"020": "invalid test indicator",
	"021": "invalid number of included groups",
	"022": "invalid control structure",
	"023": "improper (premature) end of file",
	"024": "invalid interchange content",
	"025": "duplicate interchange control number",
	"026": "invalid data element separator",
	"027": "invalid component element separator",
}
