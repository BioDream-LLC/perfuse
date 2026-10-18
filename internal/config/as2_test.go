package config

import (
	"strings"
	"testing"
	"time"
)

func TestTheAS2DuplicateWindow(t *testing.T) {
	s := &AS2Source{Listen: ":4080", ID: "US", CertFile: "c", KeyFile: "k", Partners: []AS2Partner{{ID: "P", CertFile: "p"}}}
	if s.Duplicates() != 30*24*time.Hour {
		t.Errorf("default %v", s.Duplicates())
	}
	for _, c := range []struct {
		w  time.Duration
		ok bool
	}{{0, true}, {time.Hour, true}, {168 * time.Hour, true}, {time.Minute, false}, {-time.Hour, false}} {
		s.DuplicateWindow = c.w
		errs := s.validate()
		bad := false
		for _, e := range errs {
			if strings.Contains(e.Error(), "duplicate_window") {
				bad = true
			}
		}
		if bad == c.ok {
			t.Errorf("%v: errors %v", c.w, errs)
		}
	}
}
