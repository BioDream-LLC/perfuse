package snmp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBase is where Perfuse's own values sit when no base is set. BioDream LLC has no IANA enterprise number yet, so it is in
// Net-SNMP's experimental space; an operator who wants a stable OID in their monitoring sets their own enterprise's number.
const DefaultBase = "1.3.6.1.4.1.8072.9999.9999.16"

// ChannelRow is one channel as the channel table shows it.
type ChannelRow struct {
	Name                        string
	Running                     bool
	Received, Delivered, Failed uint64
	Queued                      uint64
	OldestQueuedSeconds         uint64
}

// Figures is what the agent reports, gathered fresh at most once a second.
type Figures struct {
	Version  string
	Name     string
	Channels []ChannelRow
	Broken   int
}

// Event is an alert as a trap carries it.
type Event struct {
	Firing      bool
	Kind        string
	Severity    string
	Channel     string
	Destination string
	Summary     string
}

// Config is what the service needs; a zero Listen means no agent, no Traps means no traps.
type Config struct {
	Listen    string
	Community string
	Allow     []string
	Traps     []string
	Base      string
	Contact   string
	Location  string
}

// Validate refuses settings that would open the agent to guessing or to the whole network.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" && len(c.Traps) == 0 {
		return nil
	}
	if c.Base != "" {
		o, err := ParseOID(c.Base)
		if err != nil || len(o) < 7 || !strings.HasPrefix(o.String(), "1.3.6.1.4.1.") {
			return errors.New("the SNMP base OID must be under 1.3.6.1.4.1 (an enterprise number), such as " + DefaultBase)
		}
	}
	if len(c.Community) < 6 {
		return errors.New("the SNMP community must be at least 6 characters: it is a password, sent in clear")
	}
	switch strings.ToLower(c.Community) {
	case "public", "private":
		return errors.New("public and private are the first communities anyone tries: choose another")
	}
	if strings.TrimSpace(c.Listen) != "" {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("the SNMP listen address %q needs a port, such as :161", c.Listen)
		}
		if len(c.Allow) == 0 {
			return errors.New("list the networks your monitoring asks from (SNMP allow): the agent answers nobody else")
		}
		if _, err := ParseAllow(c.Allow); err != nil {
			return err
		}
	}
	for _, t := range c.Traps {
		if strings.TrimSpace(t) == "" {
			return errors.New("an SNMP trap target is empty")
		}
	}
	return nil
}

// ParseAllow reads networks and single addresses.
func ParseAllow(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if p, err := netip.ParsePrefix(a); err == nil {
			out = append(out, p.Masked())
		} else if ip, err := netip.ParseAddr(a); err == nil {
			out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
		} else {
			return nil, fmt.Errorf("%q is not an address or a network such as 10.0.0.0/24", a)
		}
	}
	return out, nil
}

// Service runs the agent and sends traps for Perfuse.
type Service struct {
	cfg     Config
	base    OID
	figures func() Figures
	log     *slog.Logger
	started time.Time

	mu    sync.Mutex
	agent *Agent
	err   string
	sent  int
	fails int
}

// New makes a service; figures is called to fill the table.
func New(cfg Config, figures func() Figures, log *slog.Logger) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Base == "" {
		cfg.Base = DefaultBase
	}
	base, err := ParseOID(cfg.Base)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{cfg: cfg, base: base, figures: figures, log: log, started: time.Now()}, nil
}

// Start opens the agent's port, when one is set.
func (s *Service) Start(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.Listen) == "" {
		return nil
	}
	allow, err := ParseAllow(s.cfg.Allow)
	if err != nil {
		return err
	}
	a := &Agent{Source: s.Table, Log: s.log}
	a.Configure(s.cfg.Community, allow)
	if err := a.Listen(s.cfg.Listen); err != nil {
		s.mu.Lock()
		s.err = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("the SNMP agent could not listen on %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.agent = a
	s.mu.Unlock()
	go a.Serve(ctx)
	return nil
}

// Status is what the console shows.
type Status struct {
	Running   bool     `json:"running"`
	Listen    string   `json:"listen"`
	Address   string   `json:"address,omitempty"`
	Error     string   `json:"error,omitempty"`
	Allow     []string `json:"allow"`
	Traps     []string `json:"traps"`
	Base      string   `json:"base"`
	TrapsSent int      `json:"trapsSent"`
	TrapFails int      `json:"trapFailures"`
}

// Status reports the agent's state. A nil service is "off".
func (s *Service) Status() Status {
	if s == nil {
		return Status{Allow: []string{}, Traps: []string{}, Base: DefaultBase}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Running: s.agent != nil, Listen: s.cfg.Listen, Error: s.err, Allow: append([]string{}, s.cfg.Allow...),
		Traps: append([]string{}, s.cfg.Traps...), Base: s.base.String(), TrapsSent: s.sent, TrapFails: s.fails}
	if s.agent != nil {
		st.Address = s.agent.Addr().String()
	}
	return st
}

// Table is every value the agent serves: the system group, then .1 scalars and .2 the channel table under the base.
func (s *Service) Table() Table {
	f := s.figures()
	up := time.Since(s.started)
	t := Table{
		{Name: SysDescr, Value: String("Perfuse " + f.Version + " healthcare integration engine")},
		{Name: SysObjectID, Value: ObjectID(s.base)},
		{Name: SysUpTime, Value: Ticks(uint32(up / (10 * time.Millisecond)))},
		{Name: SysContact, Value: String(s.cfg.Contact)},
		{Name: SysName, Value: String(f.Name)},
		{Name: SysLocation, Value: String(s.cfg.Location)},
	}
	scalar := func(n uint32, v Value) { t = append(t, VarBind{Name: s.base.Append(1, n, 0), Value: v}) }
	rows := append([]ChannelRow(nil), f.Channels...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	var running int
	var recv, deliv, failed, queued uint64
	for i, c := range rows {
		if c.Running {
			running++
		}
		recv += c.Received
		deliv += c.Delivered
		failed += c.Failed
		queued += c.Queued
		row := uint32(i + 1)
		col := func(n uint32, v Value) { t = append(t, VarBind{Name: s.base.Append(2, 1, n, row), Value: v}) }
		col(1, Integer(int64(row)))
		col(2, String(c.Name))
		col(3, Integer(map[bool]int64{true: 1, false: 2}[c.Running]))
		col(4, Counter64(c.Received))
		col(5, Counter64(c.Delivered))
		col(6, Counter64(c.Failed))
		col(7, Gauge(clamp32(c.Queued)))
		col(8, Gauge(clamp32(c.OldestQueuedSeconds)))
	}
	scalar(1, String(f.Version))
	scalar(2, Gauge(uint32(len(rows))))
	scalar(3, Gauge(uint32(running)))
	scalar(4, Gauge(uint32(max(f.Broken, 0))))
	scalar(5, Counter64(recv))
	scalar(6, Counter64(deliv))
	scalar(7, Counter64(failed))
	scalar(8, Gauge(clamp32(queued)))
	return t
}

func clamp32(n uint64) uint32 {
	if n > 0xffffffff {
		return 0xffffffff
	}
	return uint32(n)
}

// Notify sends a trap for an alert firing (.0.1) or resolving (.0.2) to every target.
func (s *Service) Notify(e Event) {
	if s == nil || len(s.cfg.Traps) == 0 {
		return
	}
	trap := s.base.Append(0, map[bool]uint32{true: 1, false: 2}[e.Firing])
	v := func(n uint32, x string) VarBind { return VarBind{Name: s.base.Append(3, n, 0), Value: String(x)} }
	vbs := []VarBind{v(1, e.Kind), v(2, e.Severity), v(3, e.Channel), v(4, e.Destination), v(5, e.Summary)}
	for _, target := range s.cfg.Traps {
		addr := strings.TrimSpace(target)
		if _, _, err := net.SplitHostPort(addr); err != nil {
			addr = net.JoinHostPort(addr, "162")
		}
		go func(addr string) {
			err := Trap(addr, s.cfg.Community, time.Since(s.started), trap, vbs...)
			s.mu.Lock()
			if err != nil {
				s.fails++
			} else {
				s.sent++
			}
			s.mu.Unlock()
			if err != nil {
				s.log.Warn("SNMP trap not sent", "to", addr, "err", err)
			}
		}(addr)
	}
}

// MIB describes the values, for a monitoring tool to load.
func MIB(base string) string {
	if base == "" {
		base = DefaultBase
	}
	r := strings.NewReplacer("BASE", strings.ReplaceAll(base, ".", " "))
	return r.Replace(`PERFUSE-MIB DEFINITIONS ::= BEGIN
-- Perfuse's monitoring values. The base OID is the one set in Settings, Monitoring, SNMP: { BASE }.
IMPORTS OBJECT-TYPE, NOTIFICATION-TYPE, Gauge32, Counter64, Integer32 FROM SNMPv2-SMI DisplayString FROM SNMPv2-TC;

perfuse OBJECT IDENTIFIER ::= { BASE }
pfScalars OBJECT IDENTIFIER ::= { perfuse 1 }
pfVersion          OBJECT-TYPE SYNTAX DisplayString MAX-ACCESS read-only STATUS current DESCRIPTION "Perfuse's version." ::= { pfScalars 1 }
pfChannels         OBJECT-TYPE SYNTAX Gauge32 MAX-ACCESS read-only STATUS current DESCRIPTION "Channels configured." ::= { pfScalars 2 }
pfChannelsRunning  OBJECT-TYPE SYNTAX Gauge32 MAX-ACCESS read-only STATUS current DESCRIPTION "Channels running." ::= { pfScalars 3 }
pfChannelsBroken   OBJECT-TYPE SYNTAX Gauge32 MAX-ACCESS read-only STATUS current DESCRIPTION "Channel files that could not be loaded." ::= { pfScalars 4 }
pfReceived         OBJECT-TYPE SYNTAX Counter64 MAX-ACCESS read-only STATUS current DESCRIPTION "Messages received since start, all channels." ::= { pfScalars 5 }
pfDelivered        OBJECT-TYPE SYNTAX Counter64 MAX-ACCESS read-only STATUS current DESCRIPTION "Messages delivered since start." ::= { pfScalars 6 }
pfFailed           OBJECT-TYPE SYNTAX Counter64 MAX-ACCESS read-only STATUS current DESCRIPTION "Messages that could not be delivered since start." ::= { pfScalars 7 }
pfQueued           OBJECT-TYPE SYNTAX Gauge32 MAX-ACCESS read-only STATUS current DESCRIPTION "Messages waiting in delivery queues." ::= { pfScalars 8 }

pfChannelTable OBJECT IDENTIFIER ::= { perfuse 2 }
-- pfChannelEntry { pfChannelTable 1 }: 1 index, 2 name, 3 status (1 running, 2 stopped), 4 received, 5 delivered,
-- 6 failed (Counter64 since start), 7 queued (Gauge32), 8 age of the oldest queued message in seconds (Gauge32).

pfTrapVars OBJECT IDENTIFIER ::= { perfuse 3 }
-- Variables a trap carries: 1 alert kind, 2 severity, 3 channel, 4 destination, 5 summary (DisplayString).

pfTraps OBJECT IDENTIFIER ::= { perfuse 0 }
pfAlertFiring   NOTIFICATION-TYPE STATUS current DESCRIPTION "An alert started (kind, severity, channel, destination, summary)." ::= { pfTraps 1 }
pfAlertResolved NOTIFICATION-TYPE STATUS current DESCRIPTION "An alert ended." ::= { pfTraps 2 }
END
`)
}
