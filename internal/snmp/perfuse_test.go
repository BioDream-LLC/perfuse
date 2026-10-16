package snmp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConfigRefusesWeakSettings(t *testing.T) {
	for _, c := range []Config{
		{Listen: ":1161", Community: "public", Allow: []string{"10.0.0.0/8"}},
		{Listen: ":1161", Community: "abc", Allow: []string{"10.0.0.0/8"}},
		{Listen: ":1161", Community: "s3cret-community"},
		{Listen: "1161", Community: "s3cret-community", Allow: []string{"10.0.0.0/8"}},
		{Listen: ":1161", Community: "s3cret-community", Allow: []string{"not-a-net"}},
		{Listen: ":1161", Community: "s3cret-community", Allow: []string{"127.0.0.1"}, Base: "1.3.6.1.2.1"},
		{Traps: []string{"10.0.0.1"}, Community: "pub"},
	} {
		if c.Validate() == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
	if err := (Config{}).Validate(); err != nil {
		t.Errorf("off: %v", err)
	}
	if err := (Config{Listen: "127.0.0.1:0", Community: "s3cret-community", Allow: []string{"127.0.0.1"}}).Validate(); err != nil {
		t.Errorf("good: %v", err)
	}
}

func TestServiceAnswersAndTraps(t *testing.T) {
	trapConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer trapConn.Close()
	svc, err := New(Config{Listen: "127.0.0.1:0", Community: "s3cret-community", Allow: []string{"127.0.0.1"},
		Traps: []string{trapConn.LocalAddr().String()}}, func() Figures {
		return Figures{Version: "v9", Name: "lab", Channels: []ChannelRow{{Name: "b-adt", Running: true, Received: 7, Queued: 2},
			{Name: "a-lab", Failed: 1}}}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	st := svc.Status()
	if !st.Running || st.Address == "" {
		t.Fatalf("status %+v", st)
	}
	c, err := net.Dial("udp", st.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	base := MustOID(DefaultBase)
	ask := func(o OID) VarBind {
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write(GetNextRequest("s3cret-community", o)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		vb, ok := FirstVarBind(buf[:n])
		if !ok {
			t.Fatalf("no answer for %s", o)
		}
		return vb
	}
	// The first channel in name order is a-lab; row 1, column 2 is its name.
	if vb := ask(base.Append(2, 1, 2)); vb.Value.Text() != "a-lab" {
		t.Errorf("first channel %q", vb.Value.Text())
	}
	if vb := ask(base.Append(1, 5)); vb.Value.Text() != "7" {
		t.Errorf("received %q", vb.Value.Text())
	}
	svc.Notify(Event{Firing: true, Kind: "queue_depth", Severity: "critical", Channel: "b-adt", Summary: "deep"})
	_ = trapConn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := trapConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decode(buf[:n])
	if err != nil || m.pdu != pduTrapV2 {
		t.Fatalf("trap %v %v", m.pdu, err)
	}
	var texts []string
	for _, vb := range m.vbs {
		texts = append(texts, vb.Value.Text())
	}
	if !strings.Contains(strings.Join(texts, "|"), "queue_depth|critical|b-adt") {
		t.Errorf("trap carries %v", texts)
	}
	if !strings.Contains(MIB(""), "PERFUSE-MIB") {
		t.Error("no MIB")
	}
}
