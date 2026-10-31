// Package edifact reads and writes UN/EDIFACT interchanges (ISO 9735): the UNA service string advice, the UNB/UNZ
// interchange envelope, optional UNG/UNE functional groups, and UNH/UNT messages, with the release character and, for
// syntax version 4, the repetition separator. It checks the envelope's counts and references, addresses data elements for
// filters, splits an interchange into one per message, and builds a CONTRL acknowledgement.
//
// It reads any message type (ORDERS, INVOIC, IFTSTA, MEDRPT ...); it does not hold the directories that say which segments
// a message type may contain, so it checks structure, not content.
package edifact

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Delimiters are the service characters, from UNA or the defaults.
type Delimiters struct {
	Component  byte // :
	Element    byte // +
	Decimal    byte // .
	Release    byte // ?
	Repetition byte // * in syntax 4; ' ' (none) before it
	Segment    byte // '
}

// Default is ISO 9735's default service characters (syntax levels A and B), used when there is no UNA.
var Default = Delimiters{Component: ':', Element: '+', Decimal: '.', Release: '?', Repetition: ' ', Segment: '\''}

// Element is one data element: its repetitions, each a list of components.
type Element [][]string

// Segment is one segment: its tag and data elements.
type Segment struct {
	Tag      string
	Elements []Element
}

// Value returns element e (1-based), repetition r (1-based), component c (1-based), or "".
func (s Segment) Value(e, r, c int) string {
	if e < 1 || e > len(s.Elements) {
		return ""
	}
	el := s.Elements[e-1]
	if r < 1 || r > len(el) {
		return ""
	}
	comps := el[r-1]
	if c < 1 || c > len(comps) {
		return ""
	}
	return comps[c-1]
}

// Message is one UNH ... UNT message.
type Message struct {
	// Segments runs from UNH to UNT inclusive.
	Segments []Segment
}

// Ref is the message reference number (UNH 0062).
func (m Message) Ref() string { return m.Segments[0].Value(1, 1, 1) }

// Type is the message type and version, such as ORDERS:D:96A:UN.
func (m Message) Type() string {
	h := m.Segments[0]
	parts := []string{h.Value(2, 1, 1), h.Value(2, 1, 2), h.Value(2, 1, 3), h.Value(2, 1, 4)}
	return strings.TrimRight(strings.Join(parts, ":"), ":")
}

// Group is a UNG ... UNE functional group.
type Group struct {
	Header, Trailer Segment
	Messages        []Message
}

// Interchange is one UNB ... UNZ interchange.
type Interchange struct {
	Delims Delimiters
	// HasUNA records whether the interchange began with a service string advice, so it is written back the same way.
	HasUNA bool
	Header Segment // UNB
	// Groups is set when the interchange uses functional groups; Messages otherwise.
	Groups   []Group
	Messages []Message
	Trailer  Segment // UNZ
}

// Ref is the interchange control reference (UNB 0020).
func (ic *Interchange) Ref() string { return ic.Header.Value(5, 1, 1) }

// Sender and Recipient are the interchange's UNB parties (0004 and 0010) with their code qualifiers.
func (ic *Interchange) Sender() (id, qualifier string) {
	return ic.Header.Value(2, 1, 1), ic.Header.Value(2, 1, 2)
}
func (ic *Interchange) Recipient() (id, qualifier string) {
	return ic.Header.Value(3, 1, 1), ic.Header.Value(3, 1, 2)
}

// Syntax is the syntax identifier and version (UNB 0001:0002), such as UNOC:3.
func (ic *Interchange) Syntax() string {
	return ic.Header.Value(1, 1, 1) + ":" + ic.Header.Value(1, 1, 2)
}

// AllMessages lists the messages, whether or not they are in groups.
func (ic *Interchange) AllMessages() []Message {
	if len(ic.Groups) == 0 {
		return ic.Messages
	}
	var out []Message
	for _, g := range ic.Groups {
		out = append(out, g.Messages...)
	}
	return out
}

// Segments lists every segment in order, UNB to UNZ.
func (ic *Interchange) Segments() []Segment {
	out := []Segment{ic.Header}
	if len(ic.Groups) == 0 {
		for _, m := range ic.Messages {
			out = append(out, m.Segments...)
		}
	} else {
		for _, g := range ic.Groups {
			out = append(out, g.Header)
			for _, m := range g.Messages {
				out = append(out, m.Segments...)
			}
			out = append(out, g.Trailer)
		}
	}
	return append(out, ic.Trailer)
}

// splitSegments splits on the segment terminator, honouring the release character, and drops the line breaks many
// senders put after each terminator.
func splitSegments(data []byte, d Delimiters) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == d.Release && i+1 < len(data) {
			cur.WriteByte(b)
			cur.WriteByte(data[i+1])
			i++
			continue
		}
		if b == d.Segment {
			if s := strings.TrimLeft(cur.String(), "\r\n"); strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(b)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// splitOn splits s on sep, honouring the release character but leaving it in place for the next level.
func splitOn(s string, sep, release byte) []string {
	if sep == ' ' || sep == 0 {
		return []string{s}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == release && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unescape(s string, release byte) string {
	if strings.IndexByte(s, release) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == release && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func parseSegment(raw string, d Delimiters) Segment {
	parts := splitOn(raw, d.Element, d.Release)
	seg := Segment{Tag: strings.TrimSpace(parts[0])}
	for _, p := range parts[1:] {
		var el Element
		reps := []string{p}
		if seg.Tag != "UNB" && seg.Tag != "UNA" {
			reps = splitOn(p, d.Repetition, d.Release)
		}
		for _, r := range reps {
			comps := splitOn(r, d.Component, d.Release)
			for i := range comps {
				comps[i] = unescape(comps[i], d.Release)
			}
			el = append(el, comps)
		}
		seg.Elements = append(seg.Elements, el)
	}
	return seg
}

// Parse reads one interchange. Envelope counts are not checked here; see Check.
func Parse(data []byte) (*Interchange, error) {
	data = bytes.TrimLeft(data, " \r\n\t\ufeff")
	ic := &Interchange{Delims: Default}
	if bytes.HasPrefix(data, []byte("UNA")) {
		if len(data) < 9 {
			return nil, errors.New("edifact: UNA is shorter than its nine characters")
		}
		una := data[3:9]
		ic.Delims = Delimiters{Component: una[0], Element: una[1], Decimal: una[2], Release: una[3], Repetition: una[4], Segment: una[5]}
		ic.HasUNA = true
		data = data[9:]
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("UNB")) {
		return nil, errors.New("edifact: the interchange does not start with UNB (or UNA then UNB)")
	}
	// Syntax 4 uses * for repetition by default even without UNA; earlier levels have none.
	segs := splitSegments(data, ic.Delims)
	if len(segs) < 2 {
		return nil, errors.New("edifact: an interchange needs at least UNB and UNZ")
	}
	ic.Header = parseSegment(segs[0], ic.Delims)
	if v := ic.Header.Value(1, 1, 2); v == "4" && !ic.HasUNA {
		ic.Delims.Repetition = '*'
	}
	var group *Group
	var msg *Message
	for i, raw := range segs[1:] {
		s := parseSegment(raw, ic.Delims)
		switch s.Tag {
		case "UNG":
			if msg != nil {
				return nil, fmt.Errorf("edifact: UNG inside message %s, which has no UNT", msg.Ref())
			}
			ic.Groups = append(ic.Groups, Group{Header: s})
			group = &ic.Groups[len(ic.Groups)-1]
		case "UNE":
			if group == nil {
				return nil, errors.New("edifact: UNE with no UNG")
			}
			group.Trailer = s
			group = nil
		case "UNH":
			if msg != nil {
				return nil, fmt.Errorf("edifact: message %s has no UNT before the next UNH", msg.Ref())
			}
			msg = &Message{Segments: []Segment{s}}
		case "UNT":
			if msg == nil {
				return nil, errors.New("edifact: UNT with no UNH")
			}
			msg.Segments = append(msg.Segments, s)
			if group != nil {
				group.Messages = append(group.Messages, *msg)
			} else {
				if len(ic.Groups) > 0 {
					return nil, errors.New("edifact: a message outside a group in an interchange that uses groups")
				}
				ic.Messages = append(ic.Messages, *msg)
			}
			msg = nil
		case "UNZ":
			if msg != nil {
				return nil, fmt.Errorf("edifact: message %s has no UNT", msg.Ref())
			}
			if group != nil {
				return nil, errors.New("edifact: a group has no UNE")
			}
			if i != len(segs)-2 {
				return nil, errors.New("edifact: segments after UNZ; send one interchange at a time")
			}
			ic.Trailer = s
		default:
			if msg == nil {
				return nil, fmt.Errorf("edifact: segment %s is outside any message", s.Tag)
			}
			msg.Segments = append(msg.Segments, s)
		}
	}
	if ic.Trailer.Tag != "UNZ" {
		return nil, errors.New("edifact: the interchange has no UNZ, so it may have been cut short")
	}
	if len(ic.AllMessages()) == 0 {
		return nil, errors.New("edifact: the interchange holds no message")
	}
	return ic, nil
}

// Problem is one thing wrong with an interchange's envelope.
type Problem struct {
	Where, What string
}

func (p Problem) String() string { return p.Where + ": " + p.What }

// Check compares the envelope's own counts and references with what it holds: how a truncated or spliced transfer shows.
func Check(ic *Interchange) []Problem {
	var out []Problem
	for _, m := range ic.AllMessages() {
		unt := m.Segments[len(m.Segments)-1]
		if n, err := strconv.Atoi(unt.Value(1, 1, 1)); err != nil || n != len(m.Segments) {
			out = append(out, Problem{"message " + m.Ref(), fmt.Sprintf("UNT says %s segments and the message has %d", unt.Value(1, 1, 1), len(m.Segments))})
		}
		if unt.Value(2, 1, 1) != m.Ref() {
			out = append(out, Problem{"message " + m.Ref(), fmt.Sprintf("UNT names reference %q", unt.Value(2, 1, 1))})
		}
	}
	for _, g := range ic.Groups {
		ref := g.Header.Value(5, 1, 1)
		if n, err := strconv.Atoi(g.Trailer.Value(1, 1, 1)); err != nil || n != len(g.Messages) {
			out = append(out, Problem{"group " + ref, fmt.Sprintf("UNE says %s messages and the group has %d", g.Trailer.Value(1, 1, 1), len(g.Messages))})
		}
		if g.Trailer.Value(2, 1, 1) != ref {
			out = append(out, Problem{"group " + ref, fmt.Sprintf("UNE names reference %q", g.Trailer.Value(2, 1, 1))})
		}
	}
	want := len(ic.Messages)
	if len(ic.Groups) > 0 {
		want = len(ic.Groups)
	}
	if n, err := strconv.Atoi(ic.Trailer.Value(1, 1, 1)); err != nil || n != want {
		out = append(out, Problem{"interchange " + ic.Ref(), fmt.Sprintf("UNZ says %s and the interchange has %d", ic.Trailer.Value(1, 1, 1), want)})
	}
	if ic.Trailer.Value(2, 1, 1) != ic.Ref() {
		out = append(out, Problem{"interchange " + ic.Ref(), fmt.Sprintf("UNZ names reference %q", ic.Trailer.Value(2, 1, 1))})
	}
	return out
}

func escape(s string, d Delimiters) string {
	special := []byte{d.Component, d.Element, d.Release, d.Segment}
	if d.Repetition != ' ' {
		special = append(special, d.Repetition)
	}
	if !strings.ContainsAny(s, string(special)) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if bytes.IndexByte(special, s[i]) >= 0 {
			b.WriteByte(d.Release)
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func writeSegment(b *bytes.Buffer, s Segment, d Delimiters) {
	b.WriteString(s.Tag)
	for _, el := range s.Elements {
		b.WriteByte(d.Element)
		for ri, rep := range el {
			if ri > 0 {
				b.WriteByte(d.Repetition)
			}
			for ci, c := range rep {
				if ci > 0 {
					b.WriteByte(d.Component)
				}
				b.WriteString(escape(c, d))
			}
		}
	}
	b.WriteByte(d.Segment)
}

// Bytes writes the interchange, with a UNA when it arrived with one or its delimiters are not the defaults.
func (ic *Interchange) Bytes() []byte {
	var b bytes.Buffer
	d := ic.Delims
	if ic.HasUNA || d.Component != Default.Component || d.Element != Default.Element || d.Release != Default.Release ||
		d.Segment != Default.Segment {
		b.WriteString("UNA")
		b.Write([]byte{d.Component, d.Element, d.Decimal, d.Release, d.Repetition, d.Segment})
	}
	for _, s := range ic.Segments() {
		writeSegment(&b, s, d)
	}
	return b.Bytes()
}

func seg(tag string, els ...[]string) Segment {
	s := Segment{Tag: tag}
	for _, e := range els {
		s.Elements = append(s.Elements, Element{e})
	}
	return s
}

// Split returns one interchange per message, each with the original UNB (and group header) and trailers counting one.
func Split(ic *Interchange) []*Interchange {
	var out []*Interchange
	one := func(g *Group, m Message) {
		n := &Interchange{Delims: ic.Delims, HasUNA: ic.HasUNA, Header: ic.Header}
		if g != nil {
			ng := Group{Header: g.Header, Messages: []Message{m},
				Trailer: seg("UNE", []string{"1"}, []string{g.Header.Value(5, 1, 1)})}
			n.Groups = []Group{ng}
		} else {
			n.Messages = []Message{m}
		}
		n.Trailer = seg("UNZ", []string{"1"}, []string{ic.Ref()})
		out = append(out, n)
	}
	if len(ic.Groups) == 0 {
		for _, m := range ic.Messages {
			one(nil, m)
		}
	} else {
		for i := range ic.Groups {
			for _, m := range ic.Groups[i].Messages {
				one(&ic.Groups[i], m)
			}
		}
	}
	return out
}

// Action is a CONTRL action code (data element 0083).
type Action string

const (
	// Accepted (7): this level acknowledged, lower levels too unless reported.
	Accepted Action = "7"
	// Rejected (4): this level and everything in it rejected.
	Rejected Action = "4"
)

// CONTRL builds the syntax and service report for an interchange: UCI naming it, accepted or rejected, from us (the
// interchange's recipient) back to its sender. ourRef is our own interchange reference for the CONTRL.
func CONTRL(ic *Interchange, action Action, ourRef string, now time.Time) *Interchange {
	sid, sq := ic.Sender()
	rid, rq := ic.Recipient()
	syntax := []string{ic.Header.Value(1, 1, 1), ic.Header.Value(1, 1, 2)}
	date := now.UTC().Format("060102")
	if syntax[1] == "4" {
		date = now.UTC().Format("20060102")
	}
	header := seg("UNB", syntax, nonEmpty(rid, rq), nonEmpty(sid, sq), []string{date, now.UTC().Format("1504")}, []string{ourRef})
	uci := seg("UCI", []string{ic.Ref()}, nonEmpty(sid, sq), nonEmpty(rid, rq), []string{string(action)})
	msgRef := "1"
	unh := seg("UNH", []string{msgRef}, []string{"CONTRL", "D", "3", "UN"})
	unt := seg("UNT", []string{"3"}, []string{msgRef})
	return &Interchange{Delims: ic.Delims, HasUNA: ic.HasUNA, Header: header,
		Messages: []Message{{Segments: []Segment{unh, uci, unt}}},
		Trailer:  seg("UNZ", []string{"1"}, []string{ourRef})}
}

func nonEmpty(v ...string) []string {
	for len(v) > 1 && v[len(v)-1] == "" {
		v = v[:len(v)-1]
	}
	return v
}

// Describe names an interchange for logs: its message types and parties, never content.
func Describe(ic *Interchange) string {
	types := map[string]int{}
	var order []string
	for _, m := range ic.AllMessages() {
		t := m.Segments[0].Value(2, 1, 1)
		if types[t] == 0 {
			order = append(order, t)
		}
		types[t]++
	}
	parts := make([]string, 0, len(order))
	for _, t := range order {
		parts = append(parts, fmt.Sprintf("%d %s", types[t], t))
	}
	s, _ := ic.Sender()
	r, _ := ic.Recipient()
	return strings.Join(parts, ", ") + " from " + s + " to " + r
}
