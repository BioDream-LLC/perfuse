package cms0057

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// TestCARINSurvivesTheStore checks the converted resources come back from the FHIR model intact, which is what the FHIR server
// stores them through. A field the model drops would be a field the Patient Access API silently never serves.
func TestCARINSurvivesTheStore(t *testing.T) {
	res := convertFixture(t, "837i.x12", "835i.x12", CARINOptions{NetworkStatus: "innetwork", IdentifierSystem: testSystem})
	for _, e := range res[0].Bundle["entry"].([]any) {
		in := e.(map[string]any)["resource"].(map[string]any)
		raw, _ := json.Marshal(in)
		r, err := fhir.UnmarshalResource(raw)
		if err != nil {
			t.Fatalf("%s: %v", in["resourceType"], err)
		}
		back, err := fhir.Marshal(r, fhir.R4)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(back, &out)
		var norm map[string]any
		_ = json.Unmarshal(raw, &norm)
		for k := range norm {
			if !reflect.DeepEqual(norm[k], out[k]) {
				t.Errorf("%s.%s did not survive the FHIR model:\n in:  %v\n out: %v", in["resourceType"], k, norm[k], out[k])
			}
		}
	}
}
