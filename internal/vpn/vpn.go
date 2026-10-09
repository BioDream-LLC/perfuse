// Package vpn reports the state of the site-to-site VPN tunnels partner traffic runs over - AWS Site-to-Site VPN, Azure VPN
// Gateway connections and strongSwan - and keeps each partner's connection sheet: both sides' tunnel settings, and whether
// they agree.
//
// A tunnel that is down looks, from an interface engine, exactly like a partner whose engine is stopped: connections time out.
// Saying "the tunnel is down" instead is the difference between calling the network team and calling the partner.
//
// Read only. Nothing here changes a tunnel, and no pre-shared key is ever read, kept or shown.
package vpn

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the -vpn file.
type File struct {
	Tunnels []*Tunnel `yaml:"tunnels"`
}

// Tunnel is one partner's tunnel and where its state is read.
type Tunnel struct {
	Name    string `yaml:"name"`
	Partner string `yaml:"partner"`
	// Contact is who to call at the partner about the tunnel.
	Contact string `yaml:"contact,omitempty"`
	Notes   string `yaml:"notes,omitempty"`

	// Exactly one of these says where the state comes from.
	AWS        *AWSSource        `yaml:"aws,omitempty"`
	Azure      *AzureSource      `yaml:"azure,omitempty"`
	StrongSwan *StrongSwanSource `yaml:"strongswan,omitempty"`

	// Ours and Theirs are the two sides' settings as agreed; what a cloud reports for our side is added to Ours.
	Ours   Settings `yaml:"ours"`
	Theirs Settings `yaml:"theirs"`
}

// Settings is one side of a tunnel. Lists are what that side accepts; empty means not stated.
type Settings struct {
	PeerAddress string   `yaml:"peer_address,omitempty" json:"peerAddress,omitempty"`
	Networks    []string `yaml:"networks,omitempty" json:"networks,omitempty"`
	IKEVersions []string `yaml:"ike_versions,omitempty" json:"ikeVersions,omitempty"`
	// Phase 1 (IKE) and phase 2 (IPsec) proposals.
	Phase1Encryption []string `yaml:"phase1_encryption,omitempty" json:"phase1Encryption,omitempty"`
	Phase1Integrity  []string `yaml:"phase1_integrity,omitempty" json:"phase1Integrity,omitempty"`
	Phase1DHGroups   []string `yaml:"phase1_dh_groups,omitempty" json:"phase1DhGroups,omitempty"`
	Phase2Encryption []string `yaml:"phase2_encryption,omitempty" json:"phase2Encryption,omitempty"`
	Phase2Integrity  []string `yaml:"phase2_integrity,omitempty" json:"phase2Integrity,omitempty"`
	Phase2DHGroups   []string `yaml:"phase2_dh_groups,omitempty" json:"phase2DhGroups,omitempty"`
	// Lifetimes in seconds.
	Phase1Lifetime int `yaml:"phase1_lifetime,omitempty" json:"phase1Lifetime,omitempty"`
	Phase2Lifetime int `yaml:"phase2_lifetime,omitempty" json:"phase2Lifetime,omitempty"`
}

// Kind is where a tunnel's state is read.
func (t *Tunnel) Kind() string {
	switch {
	case t.AWS != nil:
		return "AWS Site-to-Site VPN"
	case t.Azure != nil:
		return "Azure VPN Gateway"
	case t.StrongSwan != nil:
		return "strongSwan"
	}
	return "settings only"
}

// Load reads and checks the -vpn file.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i, t := range f.Tunnels {
		where := fmt.Sprintf("%s: tunnel %d (%s)", path, i+1, t.Name)
		n := 0
		for _, set := range []bool{t.AWS != nil, t.Azure != nil, t.StrongSwan != nil} {
			if set {
				n++
			}
		}
		switch {
		case t.Name == "":
			return nil, fmt.Errorf("%s has no name", where)
		case seen[t.Name]:
			return nil, fmt.Errorf("%s: the name is used twice", where)
		case n > 1:
			return nil, fmt.Errorf("%s: name one source, aws, azure or strongswan", where)
		case t.AWS != nil && (t.AWS.Region == "" || !strings.HasPrefix(t.AWS.ConnectionID, "vpn-")):
			return nil, fmt.Errorf("%s: aws needs region and connection_id (vpn-...)", where)
		case t.Azure != nil && (t.Azure.Subscription == "" || t.Azure.ResourceGroup == "" || t.Azure.Connection == ""):
			return nil, fmt.Errorf("%s: azure needs subscription, resource_group and connection", where)
		case t.StrongSwan != nil && t.StrongSwan.Connection == "":
			return nil, fmt.Errorf("%s: strongswan needs connection, the IKE connection's name in swanctl.conf", where)
		}
		seen[t.Name] = true
	}
	return &f, nil
}

// State is a tunnel's state.
type State string

const (
	Up       State = "up"
	Partial  State = "partial" // a redundant tunnel is down, or a child SA is missing
	Down     State = "down"
	Unknown  State = "unknown" // the source could not be read
	NoSource State = "not monitored"
)

// Endpoint is one IPsec tunnel of a connection (AWS and Azure run two).
type Endpoint struct {
	Address string    `json:"address,omitempty"`
	Up      bool      `json:"up"`
	Since   time.Time `json:"since,omitzero"`
	Message string    `json:"message,omitempty"`
}

// Status is what was read about a tunnel.
type Status struct {
	Name      string     `json:"name"`
	Partner   string     `json:"partner,omitempty"`
	Kind      string     `json:"kind"`
	State     State      `json:"state"`
	Detail    string     `json:"detail"`
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	// Reported is our side's settings as the source reports them.
	Reported   Settings  `json:"reported"`
	Mismatches []string  `json:"mismatches,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
}

// Monitor reads every tunnel's state, at most once a minute each.
type Monitor struct {
	File *File
	HTTP *http.Client
	// Run runs a command and returns its output, for strongSwan; nil runs it.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	Now func() time.Time
	// MaxAge is how long a reading is kept; default a minute.
	MaxAge time.Duration

	mu    sync.Mutex
	cache map[string]Status
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Statuses reads every tunnel, in the file's order.
func (m *Monitor) Statuses(ctx context.Context) []Status {
	if m == nil || m.File == nil {
		return nil
	}
	out := make([]Status, len(m.File.Tunnels))
	var wg sync.WaitGroup
	for i, t := range m.File.Tunnels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = m.Status(ctx, t)
		}()
	}
	wg.Wait()
	return out
}

// Tunnel finds a tunnel by name.
func (m *Monitor) Tunnel(name string) *Tunnel {
	if m == nil || m.File == nil {
		return nil
	}
	for _, t := range m.File.Tunnels {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Status reads one tunnel, from the cache when it is fresh.
func (m *Monitor) Status(ctx context.Context, t *Tunnel) Status {
	maxAge := m.MaxAge
	if maxAge == 0 {
		maxAge = time.Minute
	}
	m.mu.Lock()
	if c, ok := m.cache[t.Name]; ok && m.now().Sub(c.CheckedAt) < maxAge {
		m.mu.Unlock()
		return c
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := Status{Name: t.Name, Partner: t.Partner, Kind: t.Kind(), CheckedAt: m.now()}
	var err error
	switch {
	case t.AWS != nil:
		err = m.readAWS(ctx, t, &st)
	case t.Azure != nil:
		err = m.readAzure(ctx, t, &st)
	case t.StrongSwan != nil:
		err = m.readStrongSwan(ctx, t, &st)
	default:
		st.State, st.Detail = NoSource, "No source is configured; the connection sheet is kept, the state is not read."
	}
	if err != nil {
		st.State, st.Detail = Unknown, err.Error()
	}
	if st.State == "" {
		st.State, st.Detail = summarise(st.Endpoints)
	}
	st.Mismatches = Compare(Merge(t.Ours, st.Reported), t.Theirs)
	m.mu.Lock()
	if m.cache == nil {
		m.cache = map[string]Status{}
	}
	m.cache[t.Name] = st
	m.mu.Unlock()
	return st
}

func summarise(eps []Endpoint) (State, string) {
	up := 0
	for _, e := range eps {
		if e.Up {
			up++
		}
	}
	switch {
	case len(eps) == 0:
		return Down, "No tunnel is established."
	case up == len(eps):
		return Up, fmt.Sprintf("%d of %d tunnels up.", up, len(eps))
	case up == 0:
		return Down, fmt.Sprintf("All %d tunnels are down: traffic to this partner cannot pass.", len(eps))
	default:
		return Partial, fmt.Sprintf("%d of %d tunnels up: traffic passes, without redundancy.", up, len(eps))
	}
}

// Merge fills what our side's settings leave unstated from what the source reports.
func Merge(stated, reported Settings) Settings {
	out := stated
	pick := func(a, b []string) []string {
		if len(a) > 0 {
			return a
		}
		return b
	}
	if out.PeerAddress == "" {
		out.PeerAddress = reported.PeerAddress
	}
	out.Networks = pick(out.Networks, reported.Networks)
	out.IKEVersions = pick(out.IKEVersions, reported.IKEVersions)
	out.Phase1Encryption = pick(out.Phase1Encryption, reported.Phase1Encryption)
	out.Phase1Integrity = pick(out.Phase1Integrity, reported.Phase1Integrity)
	out.Phase1DHGroups = pick(out.Phase1DHGroups, reported.Phase1DHGroups)
	out.Phase2Encryption = pick(out.Phase2Encryption, reported.Phase2Encryption)
	out.Phase2Integrity = pick(out.Phase2Integrity, reported.Phase2Integrity)
	out.Phase2DHGroups = pick(out.Phase2DHGroups, reported.Phase2DHGroups)
	if out.Phase1Lifetime == 0 {
		out.Phase1Lifetime = reported.Phase1Lifetime
	}
	if out.Phase2Lifetime == 0 {
		out.Phase2Lifetime = reported.Phase2Lifetime
	}
	return out
}

// Compare says where the two sides cannot agree: a proposal list with nothing in common, a lifetime that differs. A setting
// only one side states is not a mismatch, because there is nothing to compare it with.
func Compare(ours, theirs Settings) []string {
	var out []string
	common := func(what string, a, b []string) {
		if len(a) == 0 || len(b) == 0 {
			return
		}
		for _, x := range a {
			for _, y := range b {
				if normal(x) == normal(y) {
					return
				}
			}
		}
		out = append(out, fmt.Sprintf("%s: ours %s, theirs %s, nothing in common", what, strings.Join(a, ", "), strings.Join(b, ", ")))
	}
	common("IKE version", ours.IKEVersions, theirs.IKEVersions)
	common("phase 1 encryption", ours.Phase1Encryption, theirs.Phase1Encryption)
	common("phase 1 integrity", ours.Phase1Integrity, theirs.Phase1Integrity)
	common("phase 1 DH group", ours.Phase1DHGroups, theirs.Phase1DHGroups)
	common("phase 2 encryption", ours.Phase2Encryption, theirs.Phase2Encryption)
	common("phase 2 integrity", ours.Phase2Integrity, theirs.Phase2Integrity)
	common("phase 2 DH (PFS) group", ours.Phase2DHGroups, theirs.Phase2DHGroups)
	life := func(what string, a, b int) {
		if a != 0 && b != 0 && a != b {
			out = append(out, fmt.Sprintf("%s lifetime: ours %ds, theirs %ds", what, a, b))
		}
	}
	life("phase 1", ours.Phase1Lifetime, theirs.Phase1Lifetime)
	life("phase 2", ours.Phase2Lifetime, theirs.Phase2Lifetime)
	for _, n := range theirs.Networks {
		if slices.Contains(ours.Networks, n) {
			out = append(out, "network "+n+" is on both sides: traffic for it cannot be routed through the tunnel")
		}
	}
	return out
}

// normal compares algorithm names as each vendor writes them: AES256 and aes-256, SHA2-256 and sha256, IKEv2 and ikev2,
// group 14 and 14.
func normal(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", "_", "", " ", "", "sha2-", "sha", "sha2_", "sha", "group", "", "modp", "").Replace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "dh"), "pfs")
	switch s {
	case "2048":
		return "14"
	case "1024":
		return "2"
	case "ecp256":
		return "19"
	case "ecp384":
		return "20"
	case "3072":
		return "15"
	case "4096":
		return "16"
	case "curve25519":
		return "31"
	case "aescbc256":
		return "aes256"
	case "aescbc128":
		return "aes128"
	case "aesgcm16256", "gcmaes256":
		return "aes256gcm16"
	case "aesgcm16128", "gcmaes128":
		return "aes128gcm16"
	case "hmacsha256128", "prfhmacsha256":
		return "sha256"
	case "hmacsha384192":
		return "sha384"
	case "hmacsha512256":
		return "sha512"
	case "hmacsha196":
		return "sha1"
	}
	return s
}
