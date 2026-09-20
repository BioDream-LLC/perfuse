package mapper

import "testing"

// Independent verification on the pairs where a confident wrong mapping does real clinical harm.
//
// These are all near-miss names: they differ by one word, so pure string similarity rates them
// highly, and a shared value shape (both are dates, both are identifiers) then pushes them over an
// abstention threshold. Date of birth written into a date of death field is the worst of them, and
// the engine has to decline rather than offer its best guess.
func TestDangerousPairsAbstain(t *testing.T) {
	cases := []struct {
		src, dst string
		examples []string
	}{
		{"Date of Birth", "Date of Death", []string{"19800101", "19750612"}},
		{"date_of_birth", "date_of_death", []string{"19800101", "19750612"}},
		{"birthDate", "deathDate", []string{"1980-01-01", "1975-06-12"}},
		{"Patient ID", "Patient Account Number", []string{"MRN00123", "MRN00456"}},
		{"Admit Date", "Discharge Date", []string{"20260101", "20260105"}},
		{"Systolic", "Diastolic", []string{"120", "138"}},
		{"systolic_bp", "diastolic_bp", []string{"120", "138"}},
		{"First Name", "Last Name", []string{"Jane", "Robert"}},
	}

	for _, c := range cases {
		t.Run(c.src+" to "+c.dst, func(t *testing.T) {
			e := New([]TargetField{{Name: c.dst}}, Config{})
			got := e.Suggest(SourceField{Name: c.src, Examples: c.examples})
			if len(got) == 0 {
				return // offering nothing is a valid way to decline
			}
			if !got[0].Abstained {
				t.Errorf("suggested at confidence %d without abstaining; reasoning: %s",
					got[0].Confidence, got[0].Reasoning)
			}
		})
	}
}
