package snmp

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestOIDAndIntegers(t *testing.T) {
	o := MustOID(".1.3.6.1.4.1.8072.9999.9999.7.1.200000")
	back, err := decOID(encOID(o))
	if err != nil || Compare(back, o) != 0 {
		t.Fatalf("%v %v", back, err)
	}
	for _, n := range []int64{0, 1, 127, 128, 255, 256, -1, -128, -129, 1 << 40, -1 << 40} {
		got, err := decInt(encInt(n))
		if err != nil || got != n {
			t.Fatalf("%d -> %d %v", n, got, err)
		}
	}
	if _, err := ParseOID("1.x"); err == nil {
		t.Fatal("bad OID parsed")
	}
}

func request(pdu byte, community string, a, b int64, names ...OID) []byte {
	m := message{version: 1, community: []byte(community), pdu: pdu, requestID: 42, a: a, b: b}
	for _, n := range names {
		m.vbs = append(m.vbs, VarBind{Name: n, Value: Value{Tag: tagNull}})
	}
	return m.encode()
}

func TestAgent(t *testing.T) {
	base := MustOID("1.3.6.1.4.1.99999")
	a := &Agent{Community: "public2", Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Source: func() Table {
		return Table{
			{base.Append(1, 2, 0), Gauge(7)},
			{SysDescr, String("Perfuse")},
			{base.Append(1, 1, 0), String("v1")},
			{base.Append(2, 1, 2, 1), String("Carrier")},
		}
	}}
	if a.Handle(request(pduGet, "wrong", 0, 0, SysDescr)) != nil {
		t.Fatal("answered a wrong community")
	}
	m, err := decode(a.Handle(request(pduGet, "public2", 0, 0, SysDescr, base.Append(9, 0))))
	if err != nil || m.pdu != pduResponse || m.requestID != 42 || string(m.vbs[0].Value.Bytes) != "Perfuse" || m.vbs[1].Value.Tag != noSuchObject {
		t.Fatalf("get %+v %v", m, err)
	}
	// A walk visits every variable in order, then ends.
	var seen []string
	at := MustOID("1.3")
	for range 10 {
		m, _ := decode(a.Handle(request(pduGetNext, "public2", 0, 0, at)))
		vb := m.vbs[0]
		if vb.Value.Tag == endOfMibView {
			break
		}
		seen = append(seen, vb.Name.String())
		at = vb.Name
	}
	if len(seen) != 4 || seen[0] != SysDescr.String() || seen[3] != base.Append(2, 1, 2, 1).String() {
		t.Fatalf("walk %v", seen)
	}
	m, _ = decode(a.Handle(request(pduGetBulk, "public2", 0, 10, base)))
	if len(m.vbs) != 4 || m.vbs[0].Value.Bytes == nil || m.vbs[1].Value.Int != 7 || m.vbs[3].Value.Tag != endOfMibView {
		t.Fatalf("bulk %+v", m.vbs)
	}
	m, _ = decode(a.Handle(request(pduSet, "public2", 0, 0, SysDescr)))
	if m.a != 17 {
		t.Fatalf("set should be refused: %+v", m)
	}

	// Over UDP, and only from allowed networks.
	if err := a.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Serve(ctx)
	c, _ := net.Dial("udp", a.Addr().String())
	defer c.Close()
	_, _ = c.Write(request(pduGet, "public2", 0, 0, SysDescr))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := decode(buf[:n]); string(m.vbs[0].Value.Bytes) != "Perfuse" {
		t.Fatal("UDP answer")
	}
	a.Configure("public2", nil)
	_, _ = c.Write(request(pduGet, "public2", 0, 0, SysDescr))
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("answered a network not allowed")
	}
}

func TestTrap(t *testing.T) {
	l, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer l.Close()
	trap := MustOID("1.3.6.1.4.1.99999.0.1")
	if err := Trap(l.LocalAddr().String(), "traps", 90*time.Second, trap, VarBind{MustOID("1.3.6.1.4.1.99999.3"), String("Carrier down")}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := l.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decode(buf[:n])
	if err != nil || m.pdu != pduTrapV2 || string(m.community) != "traps" || m.vbs[0].Value.Int != 9000 || Compare(m.vbs[1].Value.OID, trap) != 0 || string(m.vbs[2].Value.Bytes) != "Carrier down" {
		t.Fatalf("trap %+v %v", m, err)
	}
}
