package engine

import (
	"strings"
	"testing"
)

// TestObjectKeysAreNamedForEveryFormat guards the object-store destinations on channels that do not carry HL7 v2.
//
// Naming the object parsed the payload as HL7 v2 unconditionally, so on an X12, NCPDP or raw channel every delivery to
// S3, Google Cloud Storage or Azure Blob failed before anything was written.
func TestObjectKeysAreNamedForEveryFormat(t *testing.T) {
	x12 := "ISA*00*          *00*          *ZZ*SENDER         *ZZ*RECEIVER       *260101*1200*^*00501*000000905*0*T*:~" +
		"GS*HC*S*R*20260101*1200*1*X*005010X222A1~ST*837*0001~SE*2*0001~GE*1*1~IEA*1*000000905~"
	for name, tc := range map[string]struct{ raw, want string }{
		"hl7":   {lsADT, "C1"},
		"x12":   {x12, "000000905"},
		"other": {`{"resourceType":"Patient"}`, "noid-"},
	} {
		key, err := renderObjectKey("in/${control_id}.dat", "ch", []byte(tc.raw))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.HasPrefix(key, "in/"+tc.want) {
			t.Errorf("%s: key %q, want it to start in/%s", name, key, tc.want)
		}
	}
}
