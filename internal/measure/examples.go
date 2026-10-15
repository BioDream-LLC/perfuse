package measure

import (
	"embed"
	"encoding/json"
	"sort"
)

//go:embed examples/*.json
var examples embed.FS

// Example is an illustrative Measure shipped to start from. None is a CMS eCQM or a HEDIS measure.
type Example struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Measure json.RawMessage `json:"measure"`
}

// Examples are the shipped measures.
func Examples() []Example {
	entries, _ := examples.ReadDir("examples")
	var out []Example
	for _, e := range entries {
		raw, err := examples.ReadFile("examples/" + e.Name())
		if err != nil {
			continue
		}
		var head struct{ ID, Title string }
		_ = json.Unmarshal(raw, &head)
		out = append(out, Example{ID: head.ID, Title: head.Title, Measure: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
