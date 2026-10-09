package vpn

import (
	"fmt"
	"strings"
)

// Sheet is a partner connection sheet in Markdown: both sides' tunnel settings side by side, where they cannot agree, and
// the tunnel's state when it was last read. It is meant to be sent to the partner, so it holds no pre-shared key and nothing
// about any other partner.
func Sheet(t *Tunnel, st Status) string {
	ours := Merge(t.Ours, st.Reported)
	var b strings.Builder
	title := t.Name
	if t.Partner != "" {
		title = t.Partner + " (" + t.Name + ")"
	}
	fmt.Fprintf(&b, "# VPN connection sheet: %s\n\n", title)
	fmt.Fprintf(&b, "Our side runs %s. State when checked (%s): **%s**. %s\n\n", t.Kind(), st.CheckedAt.UTC().Format("2006-01-02 15:04 UTC"), st.State, st.Detail)
	if t.Contact != "" {
		fmt.Fprintf(&b, "Partner contact: %s\n\n", t.Contact)
	}
	b.WriteString("| Setting | Our side | Partner side |\n|---|---|---|\n")
	row := func(name, a, c string) {
		if a == "" && c == "" {
			return
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, dash(a), dash(c))
	}
	list := func(v []string) string { return strings.Join(v, ", ") }
	num := func(v int) string {
		if v == 0 {
			return ""
		}
		return fmt.Sprintf("%d s", v)
	}
	ourPeer := ours.PeerAddress
	if ourPeer == "" {
		var addrs []string
		for _, e := range st.Endpoints {
			if e.Address != "" {
				addrs = append(addrs, e.Address)
			}
		}
		ourPeer = list(addrs)
	}
	row("Peer (outside) address", ourPeer, t.Theirs.PeerAddress)
	row("Networks", list(ours.Networks), list(t.Theirs.Networks))
	row("IKE version", list(ours.IKEVersions), list(t.Theirs.IKEVersions))
	row("Phase 1 encryption", list(ours.Phase1Encryption), list(t.Theirs.Phase1Encryption))
	row("Phase 1 integrity", list(ours.Phase1Integrity), list(t.Theirs.Phase1Integrity))
	row("Phase 1 DH group", list(ours.Phase1DHGroups), list(t.Theirs.Phase1DHGroups))
	row("Phase 1 lifetime", num(ours.Phase1Lifetime), num(t.Theirs.Phase1Lifetime))
	row("Phase 2 encryption", list(ours.Phase2Encryption), list(t.Theirs.Phase2Encryption))
	row("Phase 2 integrity", list(ours.Phase2Integrity), list(t.Theirs.Phase2Integrity))
	row("Phase 2 PFS group", list(ours.Phase2DHGroups), list(t.Theirs.Phase2DHGroups))
	row("Phase 2 lifetime", num(ours.Phase2Lifetime), num(t.Theirs.Phase2Lifetime))
	b.WriteString("\n")
	if len(st.Endpoints) > 0 {
		b.WriteString("## Tunnels\n\n")
		for _, e := range st.Endpoints {
			state := "down"
			if e.Up {
				state = "up"
			}
			fmt.Fprintf(&b, "- %s: %s", dash(e.Address), state)
			if !e.Since.IsZero() {
				fmt.Fprintf(&b, " since %s", e.Since.UTC().Format("2006-01-02 15:04 UTC"))
			}
			if e.Message != "" && !strings.EqualFold(e.Message, state) {
				fmt.Fprintf(&b, " (%s)", e.Message)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Do the two sides agree?\n\n")
	if len(st.Mismatches) == 0 {
		b.WriteString("Nothing stated on both sides disagrees. A setting only one side states is not checked.\n")
	} else {
		for _, m := range st.Mismatches {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	if t.Notes != "" {
		fmt.Fprintf(&b, "\n## Notes\n\n%s\n", t.Notes)
	}
	b.WriteString("\nPre-shared keys are never written on this sheet: exchange them separately.\n")
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return strings.ReplaceAll(s, "|", "/")
}
