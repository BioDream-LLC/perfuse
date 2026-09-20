package hl7dict

import (
	"fmt"
	"strings"
	"testing"
)

// The dictionary is what turns counting carets into reading a name, so these
// tests are mostly about it being correct rather than about it being complete.
// A wrong label is worse than a missing one: somebody reads "Patient Name" over
// a field that holds something else and acts on it.

func TestDescribeNamesTheFieldsPeopleActuallyRead(t *testing.T) {
	cases := []struct {
		segment   string
		field     int
		component int
		want      string
	}{
		// MSH-9 is the message type. Getting this wrong would mislabel every
		// message in the browser.
		{"MSH", 9, 0, "Message Type"},
		{"MSH", 9, 1, "Message Type - Message Code"},
		{"MSH", 9, 2, "Message Type - Trigger Event"},
		{"MSH", 10, 0, "Message Control ID"},
		{"MSH", 12, 0, "Version ID"},

		{"PID", 3, 0, "Patient Identifier List"},
		{"PID", 3, 1, "Patient Identifier List - ID Number"},
		{"PID", 5, 0, "Patient Name"},
		{"PID", 5, 1, "Patient Name - Family Name"},
		{"PID", 5, 2, "Patient Name - Given Name"},
		{"PID", 7, 0, "Date/Time of Birth"},
		{"PID", 8, 0, "Administrative Sex"},

		{"PV1", 2, 0, "Patient Class"},
		{"PV1", 3, 0, "Assigned Patient Location"},

		{"OBX", 3, 0, "Observation Identifier"},
		{"OBX", 5, 0, "Observation Value"},
		{"OBX", 8, 0, "Abnormal Flags"},
	}

	for _, tc := range cases {
		path := fmt.Sprintf("%s-%d", tc.segment, tc.field)
		if tc.component > 0 {
			path += fmt.Sprintf(".%d", tc.component)
		}
		t.Run(path, func(t *testing.T) {
			got := Describe(tc.segment, tc.field, tc.component)
			if got != tc.want {
				t.Errorf("Describe(%s) = %q, want %q", path, got, tc.want)
			}
		})
	}
}

func TestZSegmentsAreKnownAsLocallyDefined(t *testing.T) {
	// A Z-segment is not in the standard and never will be, so reporting it as
	// unknown would be misleading: the sender defined it deliberately. Saying
	// "locally defined" tells the reader to go and ask them.
	seg, ok := LookupSegment("ZAB")
	if !ok {
		t.Fatal("a Z-segment should resolve")
	}
	if !strings.Contains(seg.Description, "locally defined") {
		t.Errorf("a Z-segment should be described as locally defined, got %q", seg.Description)
	}
	if len(seg.Fields) != 0 {
		t.Error("a Z-segment should claim no known fields")
	}
}

func TestDescribeSaysWhichKindOfUnknownItIs(t *testing.T) {
	// "I do not know this segment" and "I know the segment but not that field"
	// are different problems: the first usually means a Z segment, and the second
	// means the dictionary is incomplete. Collapsing them into one message would
	// leave somebody unable to tell which.
	known := Describe("PID", 99, 0)
	if !strings.Contains(known, "not in the dictionary") {
		t.Errorf("a known segment with an unknown field should say so, got %q", known)
	}
	if !strings.Contains(known, "PID-99") {
		t.Errorf("the message should name the path, got %q", known)
	}

	// A Z-segment is deliberately reported as known-but-locally-defined, so an
	// unknown segment has to be one that is not a Z.
	unknown := Describe("QQQ", 1, 0)
	if !strings.Contains(unknown, "unknown segment") {
		t.Errorf("an unknown segment should say so, got %q", unknown)
	}
}

func TestDescribeHandlesAComponentBeyondTheKnownOnes(t *testing.T) {
	// A sender using a component the dictionary does not list still gets a
	// position rather than a bare field name, because "Patient Name" over the
	// seventeenth component of PID-5 would be actively misleading.
	got := Describe("PID", 5, 40)
	if !strings.Contains(got, "component 40") {
		t.Errorf("want the component number, got %q", got)
	}
}

func TestLookupFieldRejectsOutOfRange(t *testing.T) {
	for _, field := range []int{0, -1, -100} {
		if _, ok := LookupField("PID", field); ok {
			t.Errorf("LookupField(PID, %d) should not resolve; fields are 1-based", field)
		}
	}
}

func TestLookupSegmentIsCaseInsensitive(t *testing.T) {
	// Segment names are uppercase in the wild, but a path typed into the message
	// browser search box will not always be.
	for _, name := range []string{"PID", "pid", "Pid"} {
		if _, ok := LookupSegment(name); !ok {
			t.Errorf("LookupSegment(%q) should resolve", name)
		}
	}
}

func TestExplainCodeDecodesTheCommonTables(t *testing.T) {
	cases := []struct{ table, code, want string }{
		// Patient class. "I" reading as "Inpatient" is the difference between a
		// message browser being useful and being a wall of letters.
		{"0004", "I", "Inpatient"},
		{"0004", "O", "Outpatient"},
		{"0004", "E", "Emergency"},

		{"0001", "F", "Female"},
		{"0001", "M", "Male"},

		// Acknowledgement codes. AE and AR mean different things and treating one
		// as the other is a documented way to lose messages.
		{"0008", "AA", "Application accept"},
		{"0008", "AE", "Application error"},
		{"0008", "AR", "Application reject"},

		// Abnormal flags: the reason anybody looks at OBX-8.
		{"0078", "H", "Above high normal"},
		{"0078", "L", "Below low normal"},
		{"0078", "N", "Normal"},

		{"0003", "A01", "Admit / visit notification"},
		{"0003", "A03", "Discharge / end visit"},
		{"0003", "A08", "Update patient information"},
	}

	for _, tc := range cases {
		t.Run(tc.table+"-"+tc.code, func(t *testing.T) {
			got, ok := ExplainCode(tc.table, tc.code)
			if !ok {
				t.Fatalf("ExplainCode(%q, %q) not found", tc.table, tc.code)
			}
			if got != tc.want {
				t.Errorf("ExplainCode(%q, %q) = %q, want %q", tc.table, tc.code, got, tc.want)
			}
		})
	}
}

func TestExplainCodeNormalisesTheInput(t *testing.T) {
	// A code arriving with surrounding whitespace or in lower case is still that
	// code. Failing to decode it would show a raw letter for no good reason.
	for _, code := range []string{"i", "I", " I ", "\tI\n"} {
		got, ok := ExplainCode("0004", code)
		if !ok || got != "Inpatient" {
			t.Errorf("ExplainCode(0004, %q) = %q, %v; want Inpatient", code, got, ok)
		}
	}
}

func TestExplainCodeIsHonestAboutNotKnowing(t *testing.T) {
	// A guess here would be worse than silence. An unrecognised code has to read
	// as unrecognised so somebody knows to check the sender's own table.
	cases := []struct{ table, code string }{
		{"0004", "Z"},        // known table, unknown code
		{"9999", "I"},        // unknown table
		{"", "I"},            // no table
		{"0004", ""},         // no code
		{"0004", "  "},       // whitespace only
		{"0078", "VERYHIGH"}, // a local extension
	}
	for _, tc := range cases {
		if got, ok := ExplainCode(tc.table, tc.code); ok {
			t.Errorf("ExplainCode(%q, %q) = %q; it should not claim to know",
				tc.table, tc.code, got)
		}
	}
}

func TestTablesHaveNoEmptyOrLowercaseKeys(t *testing.T) {
	// Lookup upper-cases the input, so a lowercase key in the table would be
	// unreachable: the entry would exist and never match. That is the kind of
	// defect a table this size hides well.
	for table, values := range Tables {
		if strings.TrimSpace(table) == "" {
			t.Error("a table has an empty name")
		}
		for code, meaning := range values {
			if strings.TrimSpace(code) == "" {
				t.Errorf("table %s has an empty code", table)
			}
			if code != strings.ToUpper(code) {
				t.Errorf("table %s code %q is not uppercase, so lookup can never reach it",
					table, code)
			}
			if strings.TrimSpace(meaning) == "" {
				t.Errorf("table %s code %q has no meaning", table, code)
			}
		}
	}
}

func TestEveryFieldHasANameAndSaneComponents(t *testing.T) {
	// A field with no name defeats the purpose, and a duplicated component name
	// inside one field means two different things would read identically in the
	// viewer.
	for _, name := range KnownSegments() {
		seg, ok := LookupSegment(name)
		if !ok {
			t.Fatalf("KnownSegments listed %q but LookupSegment does not resolve it", name)
		}
		if strings.TrimSpace(seg.Description) == "" {
			t.Errorf("segment %s has no description", name)
		}
		for number, f := range seg.Fields {
			if strings.TrimSpace(f.Name) == "" {
				t.Errorf("%s-%d has no name", name, number)
			}
			seen := map[string]bool{}
			for i, c := range f.Components {
				if strings.TrimSpace(c) == "" {
					t.Errorf("%s-%d component %d has no name", name, number, i+1)
				}
				if seen[c] {
					t.Errorf("%s-%d has two components named %q", name, number, c)
				}
				seen[c] = true
			}
		}
	}
}

func TestFieldTablesPointAtTablesThatExist(t *testing.T) {
	// A field naming a table the package does not carry would show a raw code
	// forever with nothing saying why. Better to know now.
	for _, name := range KnownSegments() {
		seg, _ := LookupSegment(name)
		for number, f := range seg.Fields {
			if f.Table == "" {
				continue
			}
			if _, ok := Tables[f.Table]; !ok {
				t.Errorf("%s-%d names table %q, which the dictionary does not carry",
					name, number, f.Table)
			}
		}
	}
}

func TestKnownSegmentsCoversWhatARealFeedContains(t *testing.T) {
	// These are the segments in an ordinary ADT, ORU and MDM feed. Missing one
	// means the message browser degrades to raw pipes exactly where it is most
	// often used.
	required := []string{
		"MSH", "EVN", "PID", "PV1", "OBX", "OBR", "ORC",
		"NK1", "AL1", "DG1", "MSA", "ERR", "TXA",
	}
	have := map[string]bool{}
	for _, s := range KnownSegments() {
		have[s] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("the dictionary has no %s", want)
		}
	}
}

func TestKnownSegmentsIsSortedAndUnique(t *testing.T) {
	// It is rendered in a list, and an unsorted or duplicated list looks like a
	// bug to whoever reads it.
	names := KnownSegments()
	seen := map[string]bool{}
	for i, n := range names {
		if seen[n] {
			t.Errorf("%s appears twice", n)
		}
		seen[n] = true
		if i > 0 && names[i-1] > n {
			t.Errorf("not sorted: %s comes after %s", n, names[i-1])
		}
	}
}
