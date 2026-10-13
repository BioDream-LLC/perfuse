package expr

import "testing"

func TestTheCSpellingsOfAndOrAndNotWork(t *testing.T) {
	for _, pair := range [][2]string{
		{`MSH-9.1 == "ADT" && MSH-9.2 != "A28"`, `MSH-9.1 == "ADT" and MSH-9.2 != "A28"`},
		{`MSH-9.1 == "ADT" || MSH-9.1 == "ORU"`, `MSH-9.1 == "ADT" or MSH-9.1 == "ORU"`},
		{`!(MSH-9.1 == "ADT")`, `not (MSH-9.1 == "ADT")`},
	} {
		a, errA := lex(pair[0])
		b, errB := lex(pair[1])
		if errA != nil || errB != nil {
			t.Fatalf("%q: %v / %v", pair[0], errA, errB)
		}
		if len(a) != len(b) {
			t.Fatalf("%q lexes to %d tokens, %q to %d", pair[0], len(a), pair[1], len(b))
		}
		for i := range a {
			if a[i].kind != b[i].kind || a[i].text != b[i].text {
				t.Errorf("%q token %d: %v, want %v", pair[0], i, a[i], b[i])
			}
		}
	}
}
