package vpn

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// StrongSwanSource reads a strongSwan IKE connection on this host with swanctl --list-sas. Perfuse runs swanctl directly,
// never through a shell, and needs permission to read the charon socket (the vici group, or root).
type StrongSwanSource struct {
	// Connection is the connection's name in swanctl.conf.
	Connection string `yaml:"connection"`
	// Command is the swanctl to run; default swanctl on the PATH.
	Command string `yaml:"command,omitempty"`
}

var (
	ikeLine      = regexp.MustCompile(`^(\S+): #\d+, (\w+), (IKEv\d)`)
	remoteLine   = regexp.MustCompile(`^\s+remote\s.*@ ([0-9A-Fa-f.:]+)`)
	establishedL = regexp.MustCompile(`^\s+established (\d+)s ago`)
	childLine    = regexp.MustCompile(`^\s+(\S+): #\d+, reqid \d+, (\w+), \w+, (?:ESP|AH)(?::(\S+))?`)
	proposalLine = regexp.MustCompile(`^\s+([A-Z0-9_-]+(?:/[A-Z0-9_-]+)+)\s*$`)
	localTS      = regexp.MustCompile(`^\s{4,}local\s+([0-9a-fA-F.:/]+)\s*$`)
)

func (m *Monitor) readStrongSwan(ctx context.Context, t *Tunnel, st *Status) error {
	cmd := t.StrongSwan.Command
	if cmd == "" {
		cmd = "swanctl"
	}
	run := m.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, cmd, "--list-sas", "--ike", t.StrongSwan.Connection)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("swanctl --list-sas failed: %v %s", err, msg)
	}
	parseSwanctl(string(out), t.StrongSwan.Connection, m.now(), st)
	return nil
}

// parseSwanctl reads swanctl --list-sas output for one IKE connection: the IKE SA and whether a child SA is installed.
func parseSwanctl(out, conn string, now time.Time, st *Status) {
	var ep *Endpoint
	children, installed := 0, 0
	inIKE := false
	for _, line := range strings.Split(out, "\n") {
		if m := ikeLine.FindStringSubmatch(line); m != nil {
			inIKE = m[1] == conn
			if inIKE {
				st.Endpoints = append(st.Endpoints, Endpoint{Up: m[2] == "ESTABLISHED", Message: m[2]})
				ep = &st.Endpoints[len(st.Endpoints)-1]
				st.Reported.IKEVersions = []string{m[3]}
			}
			continue
		}
		if !inIKE || ep == nil {
			continue
		}
		if m := remoteLine.FindStringSubmatch(line); m != nil && ep.Address == "" {
			ep.Address = m[1]
		} else if m := establishedL.FindStringSubmatch(line); m != nil {
			var s int
			_, _ = fmt.Sscan(m[1], &s)
			ep.Since = now.Add(-time.Duration(s) * time.Second)
		} else if m := childLine.FindStringSubmatch(line); m != nil {
			children++
			if m[2] == "INSTALLED" {
				installed++
			}
			if m[3] != "" && len(st.Reported.Phase2Encryption) == 0 {
				st.Reported.Phase2Encryption, st.Reported.Phase2Integrity, st.Reported.Phase2DHGroups = proposal(m[3])
			}
		} else if m := localTS.FindStringSubmatch(line); m != nil && children > 0 {
			if !slices.Contains(st.Reported.Networks, m[1]) {
				st.Reported.Networks = append(st.Reported.Networks, m[1])
			}
		} else if m := proposalLine.FindStringSubmatch(line); m != nil && children == 0 && len(st.Reported.Phase1Encryption) == 0 {
			st.Reported.Phase1Encryption, st.Reported.Phase1Integrity, st.Reported.Phase1DHGroups = proposal(m[1])
		}
	}
	switch {
	case len(st.Endpoints) == 0:
		st.State, st.Detail = Down, "No IKE SA for "+conn+": the tunnel is not established."
	case !st.Endpoints[0].Up:
		st.State, st.Detail = Down, "The IKE SA is "+st.Endpoints[0].Message+", not established."
	case installed == 0:
		st.State, st.Detail = Partial, "IKE is established but no child SA is installed: no traffic passes."
	default:
		st.State, st.Detail = Up, fmt.Sprintf("Established, %d of %d child SAs installed.", installed, children)
	}
}

// proposal splits a strongSwan proposal, AES_CBC-256/HMAC_SHA2_256_128/PRF_HMAC_SHA2_256/ECP_256, into its encryption,
// integrity and Diffie-Hellman parts. An AEAD cipher (AES_GCM) has no separate integrity.
func proposal(p string) (enc, integ, dh []string) {
	for _, part := range strings.Split(p, "/") {
		switch {
		case strings.HasPrefix(part, "PRF_"):
		case strings.HasPrefix(part, "HMAC_"), strings.HasPrefix(part, "AES_XCBC"), strings.HasPrefix(part, "AES_CMAC"):
			integ = append(integ, part)
		case strings.HasPrefix(part, "MODP_"), strings.HasPrefix(part, "ECP_"), strings.HasPrefix(part, "CURVE_"):
			dh = append(dh, part)
		default:
			enc = append(enc, part)
		}
	}
	return
}
