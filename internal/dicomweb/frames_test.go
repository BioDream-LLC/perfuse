package dicomweb

import (
	"errors"
	"testing"
)

// TestFramesWithoutFragmentsAreRefused is the regression test for a panic on stored objects.
//
// Encapsulated pixel data claiming several frames but holding no fragment items made the basic offset table read index
// px.Fragments[0] of an empty slice, which took down the request serving it.
func TestFramesWithoutFragmentsAreRefused(t *testing.T) {
	o := &Object{Raw: make([]byte, 64)}
	for _, px := range []Attr{
		{Offset: 0}, // no fragments at all
		{Offset: 40, Fragments: []Fragment{{Offset: 20, Length: 4}}},  // first fragment before the table
		{Offset: 0, Fragments: []Fragment{{Offset: 1000, Length: 4}}}, // first fragment past the end
	} {
		if _, err := o.fragmentGroups(px, 3); !errors.Is(err, ErrFramesUnavailable) {
			t.Errorf("fragments %+v: err = %v, want ErrFramesUnavailable", px.Fragments, err)
		}
	}
}
