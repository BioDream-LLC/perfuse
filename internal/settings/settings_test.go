package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()

	r, err := NewRegistry(Default())
	if err != nil {
		t.Fatalf("the shipped registry is invalid: %v", err)
	}

	return r
}

// TestEverySettingIsFullyDescribed is the drift guard.
//
// The whole point of one registry is that the interface renders whatever it is told, so anything the interface needs has to be
// present here or the control is unusable. This is also the test that fires when somebody adds a setting and stops at the
// plumbing.
func TestEverySettingIsFullyDescribed(t *testing.T) {
	r := testRegistry(t)

	for _, s := range r.All() {
		if s.Help == "" {
			t.Errorf("%s has no help text", s.Key)
		}
		if s.Subgroup == "" {
			t.Errorf("%s has no subgroup, so it would render above the first heading", s.Key)
		}
		// A label that repeats the key teaches nobody anything, and a label ending in a colon renders as
		// "Retention days::" once the control adds its own.
		if strings.HasSuffix(s.Label, ":") {
			t.Errorf("%s has a label ending in a colon", s.Key)
		}
		if strings.ToLower(s.Label) == strings.ToLower(s.Key) {
			t.Errorf("%s has a label that is just its key", s.Key)
		}
		// Help text is what replaces a flag's usage line. One sentence fragment does not.
		if len(s.Help) < 40 {
			t.Errorf("%s has help text too short to be useful: %q", s.Key, s.Help)
		}
		// A number with a slider and no unit renders as a bare figure somebody has to guess the meaning of.
		if s.Kind == KindInt && s.Unit == "" {
			t.Errorf("%s is a number with no unit", s.Key)
		}
		if s.Kind == KindChoice {
			for _, c := range s.Choices {
				if c.Help == "" {
					t.Errorf("%s option %q has no help, so the consequence of picking it is not stated",
						s.Key, c.Value)
				}
			}
		}
	}
}

// TestASecretIsNeverReadableThroughTheStore pins the one mistake in this package that would matter.
func TestASecretIsNeverReadableThroughTheStore(t *testing.T) {
	// The pairing is enforced at construction, so a secret declared with a visible widget cannot be registered at all.
	_, err := NewRegistry([]Setting{{
		Key: "a.token", Group: "G", Subgroup: "S", Label: "Token",
		Help:   "A credential long enough to satisfy the length check in the drift guard test.",
		Kind:   KindSecret,
		Widget: WidgetText, // wrong on purpose
		Effect: EffectLive, Default: "",
	}})
	if err == nil {
		t.Fatal("a secret was accepted with a widget that would render its value")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("the error should say what is wrong, got: %v", err)
	}
}

// TestAnUnknownKeyInTheFileIsRefused covers the silent-typo failure.
//
// A mistyped key that loads without complaint is a setting somebody believes they changed. This is the same rule the channel
// loader already follows.
func TestAnUnknownKeyInTheFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	// retentionDay, not retentionDays.
	if err := os.WriteFile(path, []byte("data:\n  retentionDay: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore(testRegistry(t), path)
	if err == nil {
		t.Fatal("a misspelled setting name loaded without complaint")
	}
	if !strings.Contains(err.Error(), "retentionDay") {
		t.Errorf("the error should name the key, got: %v", err)
	}
}

// TestAValueOutOfRangeIsRefusedAtLoad covers a file edited by hand.
func TestAValueOutOfRangeIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	if err := os.WriteFile(path, []byte("data:\n  retentionDays: -5\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(testRegistry(t), path); err == nil {
		t.Fatal("a negative retention was accepted, which would mean deleting nothing or everything")
	}
}

// TestAPartialFileUsesDefaultsForTheRest is what makes a one-line settings file reasonable.
func TestAPartialFileUsesDefaultsForTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	if err := os.WriteFile(path, []byte("data:\n  retentionDays: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}

	if got := store.Int("data.retentionDays"); got != 7 {
		t.Errorf("retentionDays = %d, want 7", got)
	}
	// Untouched, so it must still be the declared default rather than a zero value.
	if got := store.Bool("data.storeMessages"); !got {
		t.Error("storeMessages came back false, but its default is true - an absent key returned a zero value")
	}
	if got := store.String("alerts.minSeverity"); got != "warning" {
		t.Errorf("minSeverity = %q, want the default warning", got)
	}
}

// TestSettingsSurviveARoundTrip covers writing and reading back.
func TestSettingsSurviveARoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}

	err = store.Set(map[string]any{
		"data.retentionDays":   90,
		"data.storePayloads":   false,
		"alerts.minSeverity":   "error",
		"alerts.webhook":       "https://hooks.example.org/abc",
		"signin.passkeyDomain": "perfuse.example.org",
	})
	if err != nil {
		t.Fatalf("saving failed: %v", err)
	}

	reopened, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatalf("the file this package just wrote does not load: %v", err)
	}

	if got := reopened.Int("data.retentionDays"); got != 90 {
		t.Errorf("retentionDays = %d, want 90", got)
	}
	if reopened.Bool("data.storePayloads") {
		t.Error("storePayloads came back true after being set to false")
	}
	if got := reopened.String("alerts.minSeverity"); got != "error" {
		t.Errorf("minSeverity = %q, want error", got)
	}
	// False is a real value, not an absence. Without IsSet, an interface cannot tell "turned off" from
	// "never touched", which for a secret is the difference between blank and configured.
	if !reopened.IsSet("data.storePayloads") {
		t.Error("a value explicitly set to false reports as unset")
	}
	if reopened.IsSet("data.purgeHour") {
		t.Error("a value never set reports as set")
	}
}

// TestTheFileIsWrittenPrivately covers permissions.
//
// A settings file can hold a webhook address, which is a bearer credential in a URL for most chat systems.
func TestTheFileIsWrittenPrivately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(map[string]any{"data.retentionDays": 45}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the settings file is mode %o, want 600", perm)
	}
}

// TestOneBadFieldSavesNothing covers the all-or-nothing rule.
//
// The interface sends a whole form. Applying the good fields and rejecting the bad one would leave the server in a state neither
// the operator nor the file describes, and the operator would be looking at an error.
func TestOneBadFieldSavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}

	// The bad field deliberately sorts AFTER the good one. Keys are validated in sorted order, so putting the bad field
	// first would mean the loop never reaches the good one - and a partial-application bug affecting later fields would
	// pass this test while being entirely present. That is how the first version of this test was wrong.
	err = store.Set(map[string]any{
		"data.retentionDays":   60,                     // valid, sorts first
		"signin.passkeyDomain": "https://not.a.domain", // invalid, sorts last
	})
	if err == nil {
		t.Fatal("a URL was accepted as a passkey domain")
	}

	// The good field must not have been applied, in memory...
	if got := store.Int("data.retentionDays"); got == 60 {
		t.Error("a rejected save applied an earlier field, so the running server and the file now disagree")
	}
	// ...nor on disk. Both are checked because they fail independently: an in-memory apply with no write survives
	// until a restart, and a write with no apply takes effect only after one.
	if _, err := os.Stat(path); err == nil {
		t.Error("a rejected save wrote the file")
	}

	// And the reverse order, so neither arrangement is the only one covered.
	err = store.Set(map[string]any{
		"alerts.webhook":     "http://insecure.example.org/hook", // invalid, sorts first
		"data.retentionDays": 60,                                 // valid, sorts last
	})
	if err == nil {
		t.Fatal("a plain http webhook was accepted")
	}
	if got := store.Int("data.retentionDays"); got == 60 {
		t.Error("a rejected save applied a later field")
	}
}

// TestAPastedURLIsRefusedForThePasskeyDomain covers the commonest real mistake.
func TestAPastedURLIsRefusedForThePasskeyDomain(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(testRegistry(t), filepath.Join(dir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"https://perfuse.example.org",
		"perfuse.example.org:8443",
		"perfuse.example.org/app",
		" perfuse.example.org",
		"localhost",
	} {
		if err := store.Set(map[string]any{"signin.passkeyDomain": bad}); err == nil {
			t.Errorf("%q was accepted as a passkey domain", bad)
		}
	}

	if err := store.Set(map[string]any{"signin.passkeyDomain": "perfuse.example.org"}); err != nil {
		t.Errorf("a valid bare domain was refused: %v", err)
	}
}

// TestAFlagThatDisagreesWithTheFileIsReported covers the precedence rule being visible.
//
// The file wins, so a flag that disagrees does nothing. Silence here is how a flag stays in a service definition for years with
// everybody believing it.
func TestAFlagThatDisagreesWithTheFileIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(map[string]any{"data.retentionDays": 90}); err != nil {
		t.Fatal(err)
	}

	conflicts := store.Conflicts(map[string]any{"data.retentionDays": 30})
	if len(conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1: %v", len(conflicts), conflicts)
	}
	// Both values have to appear, or the message tells somebody there is a problem without telling them what it is.
	for _, want := range []string{"retention-days", "30", "90"} {
		if !strings.Contains(conflicts[0], want) {
			t.Errorf("the conflict message is missing %q: %s", want, conflicts[0])
		}
	}

	// An agreeing flag is not a conflict.
	if got := store.Conflicts(map[string]any{"data.retentionDays": 90}); len(got) != 0 {
		t.Errorf("a flag matching the file was reported as a conflict: %v", got)
	}
}

// TestSeedingOnlyHappensOnce covers the migration path for an existing installation.
func TestSeedingOnlyHappensOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedFromFlags(map[string]any{"data.retentionDays": 14}); err != nil {
		t.Fatal(err)
	}
	if got := store.Int("data.retentionDays"); got != 14 {
		t.Fatalf("seeding did not apply: got %d", got)
	}

	// Somebody then changes it in the interface.
	if err := store.Set(map[string]any{"data.retentionDays": 200}); err != nil {
		t.Fatal(err)
	}

	// A restart with the old flag still in the service definition must not undo that.
	restarted, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.SeedFromFlags(map[string]any{"data.retentionDays": 14}); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Int("data.retentionDays"); got != 200 {
		t.Errorf("a restart reseeded from the flag and threw away the saved value: got %d, want 200", got)
	}
}

// TestSettingsAreOrderedForDisplay covers the map-ordering hazard.
func TestSettingsAreOrderedForDisplay(t *testing.T) {
	r := testRegistry(t)

	first := r.All()
	for i := 0; i < 8; i++ {
		again := r.All()
		for j := range first {
			if first[j].Key != again[j].Key {
				t.Fatalf("the order changed between calls at position %d: %s then %s",
					j, first[j].Key, again[j].Key)
			}
		}
	}

	// Groups must also be stable, since they are the navigation.
	groups := r.Groups()
	for i := 0; i < 8; i++ {
		again := r.Groups()
		for j := range groups {
			if groups[j] != again[j] {
				t.Fatalf("group order changed: %v then %v", groups, again)
			}
		}
	}
	if len(groups) < 4 {
		t.Errorf("only %d groups, which is not the organised layout this is for", len(groups))
	}
}

// TestAQuotedNumberIsAccepted covers a file written by hand.
func TestAQuotedNumberIsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	// A hand-written file, quoting inconsistently.
	body := "data:\n  retentionDays: 30\n  storePayloads: \"false\"\nmonitoring:\n  jsonLogs: yes\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(testRegistry(t), path)
	if err != nil {
		t.Fatalf("a reasonably hand-written file was refused: %v", err)
	}
	if store.Bool("data.storePayloads") {
		t.Error(`"false" was read as true`)
	}
	if !store.Bool("monitoring.jsonLogs") {
		t.Error("an unquoted yes was not read as true")
	}
}

// TestAFractionalNumberIsRefusedRatherThanTruncated covers silent rounding.
//
// Somebody writing 1.5 days expected fractions. Rounding to 1 without saying so is a setting that does not do what it says.
func TestAFractionalNumberIsRefusedRatherThanTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")

	if err := os.WriteFile(path, []byte("data:\n  retentionDays: 1.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(testRegistry(t), path); err == nil {
		t.Fatal("a fractional day count was silently truncated")
	}
}

// TestAStoreWithNoFileRefusesToSave covers the engine-only case honestly.
func TestAStoreWithNoFileRefusesToSave(t *testing.T) {
	store, err := NewStore(testRegistry(t), "")
	if err != nil {
		t.Fatal(err)
	}

	// Defaults still work, so the engine runs.
	if got := store.Int("data.retentionDays"); got != 30 {
		t.Errorf("retentionDays = %d, want the default 30", got)
	}

	// But saving says so rather than appearing to work.
	if err := store.Set(map[string]any{"data.retentionDays": 60}); err == nil {
		t.Error("a store with no file accepted a save that could not persist")
	}
}

// TestAnUnknownKeyPanicsRatherThanReturningZero pins the deliberate panic.
//
// A Get of a mistyped retention key returning 0 would mean "keep everything forever", which is the worst possible way for a typo
// to express itself.
func TestAnUnknownKeyPanicsRatherThanReturningZero(t *testing.T) {
	store, err := NewStore(testRegistry(t), "")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if recover() == nil {
			t.Error("reading a setting that does not exist returned a value instead of panicking")
		}
	}()

	_ = store.Get("data.retentionDayz")
}
