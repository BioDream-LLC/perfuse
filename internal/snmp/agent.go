package snmp

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Table is every variable the agent serves, in OID order. Source makes it afresh (it is called at
// most once a second, and the result cached).
type Table []VarBind

// Agent answers SNMPv2c GET, GETNEXT and GETBULK requests.
type Agent struct {
	Community string
	// Allow lists the networks that may ask; empty refuses everyone (never open to the world).
	Allow  []netip.Prefix
	Source func() Table
	Log    *slog.Logger

	mu    sync.Mutex
	cache Table
	at    time.Time
	conn  *net.UDPConn
}

// Configure changes the community and the networks allowed, while the agent serves.
func (a *Agent) Configure(community string, allow []netip.Prefix) {
	a.mu.Lock()
	a.Community, a.Allow = community, allow
	a.mu.Unlock()
}

func (a *Agent) table() Table {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil || time.Since(a.at) > time.Second {
		t := a.Source()
		slices.SortFunc(t, func(x, y VarBind) int { return Compare(x.Name, y.Name) })
		a.cache, a.at = t, time.Now()
	}
	return a.cache
}

// Listen opens the UDP port.
func (a *Agent) Listen(addr string) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	a.conn = c
	return nil
}

// Addr is where the agent listens.
func (a *Agent) Addr() net.Addr { return a.conn.LocalAddr() }

// Serve answers requests until ctx ends.
func (a *Agent) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { a.conn.Close() })
	defer stop()
	buf := make([]byte, 65535)
	for {
		n, from, err := a.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			continue
		}
		if !a.allowed(from.Addr().Unmap()) {
			continue
		}
		if resp := a.Handle(buf[:n]); resp != nil {
			_, _ = a.conn.WriteToUDPAddrPort(resp, from)
		}
	}
}

func (a *Agent) allowed(ip netip.Addr) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.Allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Handle answers one request, or returns nil for one to ignore (wrong community, not v2c, garbled).
func (a *Agent) Handle(pkt []byte) []byte {
	m, err := decode(pkt)
	a.mu.Lock()
	community := a.Community
	a.mu.Unlock()
	if err != nil || m.version != 1 || community == "" ||
		subtle.ConstantTimeCompare(m.community, []byte(community)) != 1 {
		return nil
	}
	resp := message{version: 1, community: m.community, pdu: pduResponse, requestID: m.requestID}
	t := a.table()
	switch m.pdu {
	case pduGet:
		for _, vb := range m.vbs {
			resp.vbs = append(resp.vbs, t.get(vb.Name))
		}
	case pduGetNext:
		for _, vb := range m.vbs {
			resp.vbs = append(resp.vbs, t.next(vb.Name))
		}
	case pduGetBulk:
		nonRep, maxRep := int(m.a), int(m.b)
		nonRep = max(0, min(nonRep, len(m.vbs)))
		maxRep = max(0, min(maxRep, 64))
		for _, vb := range m.vbs[:nonRep] {
			resp.vbs = append(resp.vbs, t.next(vb.Name))
		}
		rest := m.vbs[nonRep:]
		cur := make([]OID, len(rest))
		for i, vb := range rest {
			cur[i] = vb.Name
		}
		for r := 0; r < maxRep && len(rest) > 0; r++ {
			done := true
			for i := range rest {
				vb := t.next(cur[i])
				resp.vbs = append(resp.vbs, vb)
				cur[i] = vb.Name
				if vb.Value.Tag != endOfMibView {
					done = false
				}
			}
			if done || len(resp.vbs) > 500 {
				break
			}
		}
	case pduSet:
		resp.a, resp.b = 17, 1 // notWritable: this agent is read-only
		resp.vbs = m.vbs
	default:
		return nil
	}
	out := resp.encode()
	if len(out) > 1400 && m.pdu == pduGetBulk {
		// Too big for one datagram: answer with fewer repetitions.
		for len(resp.vbs) > len(m.vbs) && len(out) > 1400 {
			resp.vbs = resp.vbs[:len(resp.vbs)-len(m.vbs)]
			out = resp.encode()
		}
	}
	return out
}

func (t Table) get(o OID) VarBind {
	i, found := slices.BinarySearchFunc(t, o, func(vb VarBind, o OID) int { return Compare(vb.Name, o) })
	if found {
		return t[i]
	}
	return VarBind{Name: o, Value: Value{Tag: noSuchObject}}
}

func (t Table) next(o OID) VarBind {
	i, found := slices.BinarySearchFunc(t, o, func(vb VarBind, o OID) int { return Compare(vb.Name, o) })
	if found {
		i++
	}
	if i < len(t) {
		return t[i]
	}
	return VarBind{Name: o, Value: Value{Tag: endOfMibView}}
}

// Standard OIDs.
var (
	SysDescr    = MustOID("1.3.6.1.2.1.1.1.0")
	SysObjectID = MustOID("1.3.6.1.2.1.1.2.0")
	SysUpTime   = MustOID("1.3.6.1.2.1.1.3.0")
	SysContact  = MustOID("1.3.6.1.2.1.1.4.0")
	SysName     = MustOID("1.3.6.1.2.1.1.5.0")
	SysLocation = MustOID("1.3.6.1.2.1.1.6.0")
	trapOID     = MustOID("1.3.6.1.6.3.1.1.4.1.0")
)

// Trap sends an SNMPv2 trap to host:port with the uptime, the trap's OID and its variables.
func Trap(addr, community string, uptime time.Duration, trap OID, vbs ...VarBind) error {
	c, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	all := append([]VarBind{{SysUpTime, Ticks(uint32(uptime / (10 * time.Millisecond)))}, {trapOID, ObjectID(trap)}}, vbs...)
	m := message{version: 1, community: []byte(community), pdu: pduTrapV2, requestID: time.Now().UnixNano() & 0x7fffffff, vbs: all}
	_, err = c.Write(m.encode())
	return err
}

// GetNextRequest is a v2c GETNEXT for one name (a client's side, for tests and tools).
func GetNextRequest(community string, o OID) []byte {
	return message{version: 1, community: []byte(community), pdu: pduGetNext, requestID: 1,
		vbs: []VarBind{{Name: o, Value: Value{Tag: tagNull}}}}.encode()
}

// FirstVarBind reads the first variable of a response.
func FirstVarBind(pkt []byte) (VarBind, bool) {
	m, err := decode(pkt)
	if err != nil || len(m.vbs) == 0 || m.vbs[0].Value.Tag == endOfMibView {
		return VarBind{}, false
	}
	return m.vbs[0], true
}

// Text is a value in words, as snmpwalk prints it.
func (v Value) Text() string {
	switch v.Tag {
	case tagOctets:
		return string(v.Bytes)
	case tagOID:
		return v.OID.String()
	case tagNull, noSuchObject, noSuchInstance, endOfMibView:
		return ""
	}
	return strconv.FormatInt(v.Int, 10)
}
