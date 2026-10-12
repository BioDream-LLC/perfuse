package main

import (
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/settings"
)

// TestASavedSettingReachesItsFlag: a setting saved in the console that takes effect at a restart was recorded, reported as
// winning over the command line, and then ignored, because the code reads the flag.
func TestASavedSettingReachesItsFlag(t *testing.T) {
	reg, err := settings.NewRegistry(settings.Default())
	if err != nil {
		t.Fatal(err)
	}
	st, err := settings.NewStore(reg, filepath.Join(t.TempDir(), "s.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(map[string]any{"data.storeMessages": false, "engine.drainFor": "45s", "fleet.label": "east"}); err != nil {
		t.Fatal(err)
	}
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	store := fset.Bool("store-messages", true, "")
	drain := fset.Duration("drain-for", 0, "")
	label := fset.String("fleet-label", "", "")
	payloads := fset.Bool("store-payloads", true, "")
	if err := fset.Parse([]string{"-fleet-label", "west"}); err != nil {
		t.Fatal(err)
	}
	if err := applyFileSettings(fset, st); err != nil {
		t.Fatal(err)
	}
	if *store || *drain != 45*time.Second || *label != "east" {
		t.Errorf("store-messages=%v drain-for=%v fleet-label=%q; want the file's false, 45s, east", *store, *drain, *label)
	}
	if !*payloads {
		t.Error("a setting the file does not hold was changed")
	}
}

// TestAFlagTheFileDoesNotHoldIsRecorded: the console showed a setting at its default while the server ran with the flag.
func TestAFlagTheFileDoesNotHoldIsRecorded(t *testing.T) {
	reg, _ := settings.NewRegistry(settings.Default())
	st, _ := settings.NewStore(reg, filepath.Join(t.TempDir(), "s.yaml"))
	_ = st.Set(map[string]any{"fleet.label": "east"})
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	fset.Bool("pas", false, "")
	fset.String("fleet-label", "", "")
	_ = fset.Parse([]string{"-pas", "-fleet-label", "west"})
	if err := st.SeedMissing(flagSettings(fset)); err != nil {
		t.Fatal(err)
	}
	if !st.Bool("payers.pas") {
		t.Error("-pas was given and the console still says it is off")
	}
	if st.String("fleet.label") != "east" {
		t.Error("a value already in the file was overwritten by a flag")
	}
}
