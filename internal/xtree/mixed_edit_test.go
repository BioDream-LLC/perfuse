package xtree

import (
	"strings"
	"testing"
)

// TestEditsToMixedContentReachTheOutput guards CDA narrative edits made by scripts.
//
// A mixed-content node is written from Fragments, and Append, Insert, Remove and RemoveName changed only Children: an
// appended element vanished from the output and a removed one stayed in it.
func TestEditsToMixedContentReachTheOutput(t *testing.T) {
	parse := func() *Node {
		n, err := Parse([]byte(`<p>Allergic to <b>penicillin</b> since <i>2010</i>.</p>`))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	out := func(n *Node) string { return string(n.Marshal(0)) }

	n := parse()
	n.Append(Leaf("u", "confirmed"))
	if !strings.Contains(out(n), "<u>confirmed</u>") {
		t.Errorf("appended: %s", out(n))
	}
	n = parse()
	n.Remove(n.Child("b", 0))
	if strings.Contains(out(n), "penicillin") || !strings.Contains(out(n), "Allergic to") {
		t.Errorf("removed: %s", out(n))
	}
	n = parse()
	n.RemoveName("i")
	if strings.Contains(out(n), "2010") {
		t.Errorf("removed by name: %s", out(n))
	}
	n = parse()
	n.Insert(1, Leaf("u", "x"))
	if o := out(n); strings.Index(o, "<u>x</u>") > strings.Index(o, "<i>") || strings.Index(o, "<u>x</u>") < strings.Index(o, "</b>") {
		t.Errorf("inserted at 1: %s", o)
	}
}
