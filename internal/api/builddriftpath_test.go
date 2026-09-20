package api

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// yamlPaths walks a struct and returns every yaml key as a dotted path from the root.
//
// # Why a path and not a name
//
// TestTheBuilderCanExpressEveryChannelSetting compares bare key names, and that made it satisfiable by coincidence.
// x12.transformations was reported as present because the channel has a top-level transformations key, so the builder was
// declared able to express a setting whose form section does not exist. Found while adding ncpdp.transformations, which the
// same hole would have hidden.
//
// Qualifying by path also solves what the bare version needed owning-type names for: shadow.channel and
// destinations.channel are distinct paths, so they no longer collide.
func yamlPaths(rt reflect.Type, prefix string, depth int) map[string]bool {
	out := map[string]bool{}

	// Bounded because the config graph has cycles through pointers in places, and an unbounded walk would not return.
	if depth > 8 {
		return out
	}

	for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return out
	}

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)

		// Unexported fields are compiled artefacts - a parsed filter, a prepared pipeline - and are not part of the file
		// format at all.
		// Anonymous is the exception, and getting this wrong is what made the first version of this test report dozens of
		// gaps that were not gaps. An embedded struct contributes its fields to the yaml whether or not its type name is
		// exported, and the builder embeds several unexported ones - buildTCPFraming, buildFilePoll - precisely to share
		// them. Skipping those made the model look as though it lacked every field they carry.
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}

		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			// An embedded or inlined struct contributes its own fields at this level rather than under a key.
			for k := range yamlPaths(f.Type, prefix, depth+1) {
				out[k] = true
			}
			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = true

		for k := range yamlPaths(f.Type, path, depth+1) {
			out[k] = true
		}
	}

	return out
}

// notInTheBuilderByPath excuses a path the form deliberately does not offer.
//
// Separate from notInTheBuilder, which excuses bare names, because the point of this test is that a bare name is not
// specific enough to excuse anything.
var notInTheBuilderByPath = map[string]string{
	// The shadow block is owned by the shadow tab, which is already excused by name in notInTheBuilder. Its inner settings
	// are excused there too, one by one - except this, whose name is shared with a destination's timeout and so could not
	// be excused by name without excusing that as well. Which is the argument for a path-qualified list.
	"shadow.timeout": "an inner setting of the shadow block, which the shadow tab owns",
}

// knownBuilderGaps are settings the form cannot express today.
//
// # These are debt, not decisions
//
// Deliberately a separate list from notInTheBuilderByPath, and the distinction is the whole point. That list says "a form is
// the wrong place for this". This one says "the form should offer this and does not". Merging them would relabel real gaps as
// design, which is the kind of tidy-looking dishonesty this project exists to avoid.
//
// # Why the list exists rather than the test simply failing
//
// It was not knowable before this test was written. The bare-name guardrail could not see any of these, because it asked
// whether a key *name* appeared anywhere in the model rather than whether *this setting* had a field. source.ftp.dir looked
// covered because file and sftp sources both have a dir. They do; FTP does not.
//
// So the list is a ratchet. Every entry is a real gap between the loader and the form, the count is the size of the debt, and
// a new gap fails the build. It must only ever shrink.
//
// # It is empty, and that is the point
//
// Every gap it recorded has been closed: an HTTP source's TLS, acknowledgement and size limit, client TLS on the HTTP and
// SOAP destinations, the map step's shape, and the whole JavaScript Reader source - which docs/queue.md had listed as built
// since 24 August while the form could not configure it at all.
//
// So the ratchet now sits at zero, which means the next setting added to the loader without a form field fails the build. It
// is kept rather than deleted because an empty list with a count of zero is a stronger statement than no list: it says the
// gap was measured, not that nobody looked.
//
// # The first version of this list was wrong, and badly
//
// It recorded 97 entries. 63 of those were not gaps: the walk skipped embedded structs whose type name is unexported, and the
// builder embeds several of those on purpose to share fields - buildTCPFraming, buildFilePoll. So every field they carry
// looked absent from the model.
//
// The correction only happened because a gap looked implausible. buildTCPDest visibly inlines buildTCPFraming, so
// destinations.tcp.framing being reported as missing had to be either a builder bug or a test bug, and it was the test. A
// number that large should have prompted that check before it was written down rather than after.
var knownBuilderGaps = map[string]struct{}{}

// TestTheBuilderCanExpressEverySettingByPath is the qualified form of the drift guardrail.
//
// It is a second test rather than a replacement, because the bare-name version carries a list of reasons that are still
// correct and re-qualifying every one of them is its own change. This one starts from the paths the bare version already
// excuses and reports anything left.
func TestTheBuilderCanExpressEverySettingByPath(t *testing.T) {
	model := yamlPaths(reflect.TypeOf(buildModel{}), "", 0)
	actual := yamlPaths(reflect.TypeOf(config.Channel{}), "", 0)

	var missing []string
	for path := range actual {
		if model[path] {
			continue
		}
		if _, ok := notInTheBuilderByPath[path]; ok {
			continue
		}
		// Excused by the bare-name list, whose reasons are recorded there. A path whose last segment is excused is
		// excused, so a reason given once for shadow's inner settings still covers them.
		last := path[strings.LastIndex(path, ".")+1:]
		if _, ok := notInTheBuilder[last]; ok {
			continue
		}
		if _, ok := notInTheBuilder[path]; ok {
			continue
		}
		if _, ok := knownBuilderGaps[path]; ok {
			// Recorded debt. Counted by TestTheKnownBuilderGapListOnlyShrinks so it cannot quietly grow.
			continue
		}
		missing = append(missing, path)
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the graphical builder cannot express %d setting(s), by path:\n  %s\n\n"+
			"Each of these is a yaml key the loader reads and the form has no field for. Add it to\n"+
			"buildModel, or to notInTheBuilderByPath with the reason a form is the wrong place for it.\n"+
			"A bare name in notInTheBuilder does not excuse a nested path, which is the hole this test exists to close.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestTheKnownBuilderGapListOnlyShrinks gives the ratchet teeth.
//
// Two failure modes, and both matter.
//
// Growing means somebody added a setting the form cannot express and recorded it here instead of building the field. The
// count is checked rather than left implicit, because a list nobody counts is a list that grows.
//
// Holding a path that is no longer a gap means somebody built the form field and left the entry, which is how a ratchet
// stops ratcheting: the next real gap at that path would be excused by a stale entry.
func TestTheKnownBuilderGapListOnlyShrinks(t *testing.T) {
	// The count when the qualified test was written. Lower it when gaps are closed; never raise it.
	const wasKnown = 80

	if len(knownBuilderGaps) > wasKnown {
		t.Errorf("the known gap list has grown to %d from %d.\n\n"+
			"A new setting the form cannot express should be given a form field, not an entry here.\n"+
			"If a form is genuinely the wrong place for it, it belongs in notInTheBuilderByPath with\n"+
			"the reason - that list is for decisions, this one is for debt.",
			len(knownBuilderGaps), wasKnown)
	}

	// Every entry must still be a real gap, or it is excusing something it no longer describes.
	model := yamlPaths(reflect.TypeOf(buildModel{}), "", 0)
	actual := yamlPaths(reflect.TypeOf(config.Channel{}), "", 0)

	var stale []string
	for path := range knownBuilderGaps {
		switch {
		case model[path]:
			stale = append(stale, path+" (the form can express it now)")
		case !actual[path]:
			stale = append(stale, path+" (no longer a setting)")
		}
	}

	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%d entr(ies) in knownBuilderGaps no longer describe a gap:\n  %s\n\n"+
			"Remove them. A stale entry would excuse a real gap at the same path later.",
			len(stale), strings.Join(stale, "\n  "))
	}
}
