package drug

import "testing"

func TestSchedulesAreReadHoweverThePrescriberWroteThem(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Schedule
	}{
		{"CII", ScheduleII},
		{"C-II", ScheduleII},
		{"cii", ScheduleII},
		{"2", ScheduleII},
		{"C2", ScheduleII},
		{"Schedule II", ScheduleII},
		{"CLASS 2", ScheduleII},
		{"C.III.", ScheduleIII},
		{"IV", ScheduleIV},
		{"5", ScheduleV},
		{"", ScheduleNone},
		{"   ", ScheduleNone},
	} {
		got, err := ParseSchedule(tc.in)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSchedule(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// An unrecognised schedule must not read as uncontrolled.
//
// Defaulting to "not a controlled substance" is the dangerous direction: a typo would turn an opioid
// into something a downstream system will happily allow eleven refills on, and nothing in the message
// would look wrong.
func TestAnUnrecognisedScheduleIsRefusedRatherThanTreatedAsUncontrolled(t *testing.T) {
	for _, in := range []string{"CVI", "6", "X", "CII-B", "narcotic"} {
		got, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) = %v with no error; an unknown schedule read as %q would allow refills on a controlled drug", in, got, got)
		}
	}
}

func TestScheduleTwoPermitsNoRefills(t *testing.T) {
	if err := ScheduleII.RefillsAllowed(1); err == nil {
		t.Error("a schedule II prescription was allowed a refill")
	}
	if err := ScheduleII.RefillsAllowed(0); err != nil {
		t.Errorf("a schedule II prescription with no refills was refused: %v", err)
	}
	// The message must say what to do instead, or the reader is left thinking the drug cannot be
	// dispensed again at all.
	err := ScheduleII.RefillsAllowed(3)
	if !contains(err.Error(), "new prescription") {
		t.Errorf("the refusal does not say a new prescription is needed: %v", err)
	}
}

func TestSchedulesThreeToFivePermitFiveRefills(t *testing.T) {
	for _, s := range []Schedule{ScheduleIII, ScheduleIV, ScheduleV} {
		if err := s.RefillsAllowed(5); err != nil {
			t.Errorf("%v refused five refills: %v", s, err)
		}
		if err := s.RefillsAllowed(6); err == nil {
			t.Errorf("%v allowed six refills", s)
		}
	}
}

// An uncontrolled medicine has no federal refill limit, and zero must not be reported as one.
func TestAnUncontrolledMedicineHasNoFederalRefillLimit(t *testing.T) {
	limit, federal := ScheduleNone.MaxRefills()
	if federal {
		t.Error("an uncontrolled medicine reports a federal refill limit")
	}
	if limit >= 0 {
		t.Errorf("limit = %d; a non-negative limit reads as a cap, and zero would read as no refills allowed", limit)
	}
	if err := ScheduleNone.RefillsAllowed(11); err != nil {
		t.Errorf("eleven refills of an uncontrolled medicine were refused: %v", err)
	}
}

func TestScheduleOneCannotBePrescribed(t *testing.T) {
	if ScheduleI.Prescribable() {
		t.Error("schedule I reports as prescribable")
	}
	if err := ScheduleI.RefillsAllowed(0); err == nil {
		t.Error("a schedule I prescription with no refills was accepted; it should not exist at all")
	}
	for _, s := range []Schedule{ScheduleII, ScheduleIII, ScheduleIV, ScheduleV, ScheduleNone} {
		if !s.Prescribable() {
			t.Errorf("%v reports as not prescribable", s)
		}
	}
}

func TestEveryScheduledSubstanceNeedsTwoFactorSigning(t *testing.T) {
	for _, s := range []Schedule{ScheduleII, ScheduleIII, ScheduleIV, ScheduleV} {
		if !s.RequiresEPCS() {
			t.Errorf("%v does not require EPCS, so a channel would forward it unsigned and the pharmacy must refuse it", s)
		}
	}
	if ScheduleNone.RequiresEPCS() {
		t.Error("an uncontrolled medicine requires EPCS, which would block ordinary prescriptions")
	}
}

func TestANegativeRefillCountIsRefused(t *testing.T) {
	if err := ScheduleIV.RefillsAllowed(-1); err == nil {
		t.Error("a negative refill count was accepted")
	}
}
