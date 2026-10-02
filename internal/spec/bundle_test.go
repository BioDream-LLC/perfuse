package spec

import (
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/transform"
)

func bundleChannel(t *testing.T, name string, mutate func(*config.Channel)) *config.Channel {
	t.Helper()

	c := &config.Channel{
		Name:   name,
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{
			{Name: "registry", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if mutate != nil {
		mutate(c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("%s is not valid: %v", name, err)
	}

	return c
}

// TestTheBundleIsSortedAndComplete covers the properties that make the document worth keeping.
func TestTheBundleIsSortedAndComplete(t *testing.T) {
	when := time.Date(2026, 8, 22, 16, 30, 0, 0, time.UTC)

	// Deliberately out of order, because a directory listing and a Go map both range in an order nobody chose.
	channels := []*config.Channel{
		bundleChannel(t, "zeta", nil),
		bundleChannel(t, "alpha", nil),
		bundleChannel(t, "middle", nil),
	}

	md := Bundle(channels, nil, when)

	a, z := strings.Index(md, "alpha"), strings.Index(md, "zeta")
	if a < 0 || z < 0 {
		t.Fatalf("not every channel is in the document:\n%s", md)
	}
	if a > z {
		t.Error("the document is not sorted by name, so two runs cannot be diffed - which removes the main " +
			"reason to keep it in version control")
	}

	if !strings.Contains(md, "22 August 2026") {
		t.Error("the document does not say when it was generated, which invites somebody to trust a stale copy")
	}
}

// TestTheBundleNestsEachChannelUnderOneTitle covers heading demotion.
//
// Each per-channel document starts at a single hash, so pasted in unchanged the result would have four top-level titles and
// no structure - and the contents list every wiki builds from headings would be nonsense.
func TestTheBundleNestsEachChannelUnderOneTitle(t *testing.T) {
	md := Bundle([]*config.Channel{bundleChannel(t, "adt", nil)}, nil, time.Now())

	var topLevel int
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "# ") {
			topLevel++
		}
	}

	if topLevel != 1 {
		t.Errorf("the document has %d top-level headings, want 1", topLevel)
	}
}

// TestTheDataSummaryNamesWhatChangesContent is what an auditor actually reads.
func TestTheDataSummaryNamesWhatChangesContent(t *testing.T) {
	plain := bundleChannel(t, "passthrough", nil)

	changer := bundleChannel(t, "masker", func(c *config.Channel) {
		c.Transformations = []transform.Step{{
			Description: "drop the account number",
			Clear:       &transform.ClearStep{Path: "PID-18"},
		}}
	})

	v3 := bundleChannel(t, "pdq", func(c *config.Channel) {
		yes := true
		c.DataType = config.DataHL7v3
		c.Source = config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		}
		c.HL7v3 = &config.HL7v3Options{
			Acknowledge:  &yes,
			SenderDevice: "PERFUSE",
			SenderOID:    "2.16.840.1.113883.3.999",
			Transformations: []hl7v3.Step{{
				NullFlavor: &hl7v3.V3NullFlavorStep{Path: "//birthTime", Reason: "MSK"},
			}},
		}
	})

	md := Bundle([]*config.Channel{plain, changer, v3}, nil, time.Now())

	summary := md[strings.Index(md, "## What this server does with the data"):]
	summary = summary[:strings.Index(summary, "---")]

	if !strings.Contains(summary, "masker") {
		t.Errorf("the summary does not name the channel that changes content:\n%s", summary)
	}

	// The one that matters: a v3 channel's steps live under hl7v3 and were invisible everywhere else until today.
	if !strings.Contains(summary, "pdq") {
		t.Errorf("the summary does not name the v3 channel that masks a birth date, so an auditor reading "+
			"this would be told no such thing happens:\n%s", summary)
	}

	if strings.Contains(summary, "No interface changes message content") {
		t.Errorf("the summary claims nothing changes content while two channels do:\n%s", summary)
	}
}

// TestASiteWithNoTransformationsSaysSo covers the other direction.
//
// Worth its own case, because "nothing is listed" and "there is nothing to list" look identical to a reader and mean
// different things - the first is a broken document.
func TestASiteWithNoTransformationsSaysSo(t *testing.T) {
	md := Bundle([]*config.Channel{bundleChannel(t, "passthrough", nil)}, nil, time.Now())

	if !strings.Contains(md, "No interface changes message content") {
		t.Error("a site with no transformations does not say so, leaving a reader unable to tell an empty " +
			"list from a missing one")
	}
}

// TestAPipeInAValueDoesNotBreakTheTable covers table escaping.
//
// A pipe would split the row and shift every column after it, producing a table that renders perfectly and says the wrong
// system talks to the wrong system. An HL7 address or a destination name is a plausible place for one.
func TestAPipeInAValueDoesNotBreakTheTable(t *testing.T) {
	c := bundleChannel(t, "adt", func(c *config.Channel) {
		c.Destinations[0].Name = "a|b"
	})

	md := Bundle([]*config.Channel{c}, nil, time.Now())

	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "| adt ") {
			continue
		}
		// Five columns means six pipes. An unescaped one in a value would make seven.
		if got := strings.Count(line, "|") - strings.Count(line, "\\|"); got != 6 {
			t.Errorf("the inventory row has %d unescaped pipes, want 6 - a value is breaking the table:\n%s",
				got, line)
		}
	}
}

// TestAnEmptyServerProducesAnHonestDocument covers the degenerate case.
func TestAnEmptyServerProducesAnHonestDocument(t *testing.T) {
	md := Bundle(nil, nil, time.Now())

	if !strings.Contains(md, "no channels loaded") {
		t.Errorf("a server with no channels produces a document that does not say so:\n%s", md)
	}
}

// TestABrokenFileIsNamedRatherThanOmitted covers the property that makes the document trustworthy.
//
// Found by running it: config.LoadDir refuses the whole directory when one file is invalid, so a single unparseable channel
// produced no inventory at all - only a warning in a log nobody was reading. That is right for starting an engine, where a
// site that believes it runs forty interfaces must not silently run thirty-nine, and exactly wrong for an inventory.
func TestABrokenFileIsNamedRatherThanOmitted(t *testing.T) {
	md := Bundle(
		[]*config.Channel{bundleChannel(t, "adt", nil)},
		map[string]string{"broken.yaml": "source.type \"nonsense\" is not supported"},
		time.Now(),
	)

	if !strings.Contains(md, "broken.yaml") {
		t.Errorf("the document does not name the file it could not read, so a reader cannot tell it is "+
			"incomplete:\n%s", md)
	}
	if !strings.Contains(md, "not supported") {
		t.Error("the document names the broken file without saying what is wrong with it")
	}

	// And the valid channel is still described. A broken file must not cost the reader the other thirty-nine.
	if !strings.Contains(md, "adt") {
		t.Error("a broken file suppressed the channels that did load")
	}

	// Before the inventory, not after it. A limitation in an appendix is a limitation nobody reads until they have
	// finished believing the parts above it.
	if strings.Index(md, "broken.yaml") > strings.Index(md, "## Every interface on this server") {
		t.Error("the broken files are listed after the inventory rather than before it")
	}
}

// TestAServerWhereEveryFileIsBrokenStillSaysSo covers the case that would otherwise read as an empty site.
func TestAServerWhereEveryFileIsBrokenStillSaysSo(t *testing.T) {
	md := Bundle(nil, map[string]string{"a.yaml": "bad", "b.yaml": "also bad"}, time.Now())

	if strings.Contains(md, "no channels loaded") {
		t.Error("a server whose files all failed to load is described as having no channels, which reads as " +
			"a site with nothing configured rather than one that is broken")
	}
	if !strings.Contains(md, "a.yaml") || !strings.Contains(md, "b.yaml") {
		t.Errorf("not every broken file is named:\n%s", md)
	}
}

// TestTheSummaryAgreesWithItself covers verb agreement in the generated sentences.
//
// A small thing with a disproportionate effect. The document read "1 interface change message content", which invites the
// reader to treat the whole thing as machine output nobody checked - the opposite of what it is for.
func TestTheSummaryAgreesWithItself(t *testing.T) {
	one := bundleChannel(t, "one", func(c *config.Channel) {
		c.Transformations = []transform.Step{{Clear: &transform.ClearStep{Path: "PID-18"}}}
	})
	two := bundleChannel(t, "two", func(c *config.Channel) {
		c.Transformations = []transform.Step{{Clear: &transform.ClearStep{Path: "PID-19"}}}
	})

	single := Bundle([]*config.Channel{one}, nil, time.Now())
	if !strings.Contains(single, "1 interface changes message content") {
		t.Errorf("the singular sentence does not agree:\n%s", firstLineContaining(single, "message content"))
	}

	plural := Bundle([]*config.Channel{one, two}, nil, time.Now())
	if !strings.Contains(plural, "2 interfaces change message content") {
		t.Errorf("the plural sentence does not agree:\n%s", firstLineContaining(plural, "message content"))
	}
}

// firstLineContaining pulls one line out for an error message, so a failure shows the sentence rather than the document.
func firstLineContaining(md, want string) string {
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}

	return "(not found)"
}
