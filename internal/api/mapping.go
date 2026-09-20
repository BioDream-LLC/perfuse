package api

import (
	"encoding/json"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/mapper"
	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
)

// POST /api/mapper/suggest — get AI mapping suggestions for a set of source fields.
func (s *Server) handleMapperSuggest(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req mapperRequest
	if !s.decode(w, r, &req) {
		return
	}

	threshold := req.Threshold
	if threshold == 0 {
		threshold = 70
	}

	targets := make([]mapper.TargetField, len(req.Targets))
	for i, t := range req.Targets {
		targets[i] = mapper.TargetField{Name: t.Name, CodeSystem: t.CodeSystem, Pattern: t.Pattern}
	}

	engine := mapper.New(targets, mapper.Config{Threshold: threshold})

	var suggestions []mapperSuggestionGroup
	for _, field := range req.Fields {
		src := mapper.SourceField{
			Name:       field.Name,
			Examples:   field.Examples,
			CodeSystem: field.CodeSystem,
		}
		results := engine.Suggest(src)
		group := mapperSuggestionGroup{
			SourceField: field.Name,
			Suggestions: make([]mapperSuggestion, len(results)),
		}
		for i, sg := range results {
			group.Suggestions[i] = mapperSuggestion{
				Target:     sg.Target.Name,
				Confidence: sg.Confidence,
				Reasoning:  sg.Reasoning,
				Abstained:  sg.Abstained,
			}
		}
		suggestions = append(suggestions, group)
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"suggestions": suggestions,
	})
}

type mapperRequest struct {
	Fields    []mapperField       `json:"fields"`
	Targets   []mapperTargetField `json:"targets"`
	Threshold int                 `json:"threshold"`
}

type mapperField struct {
	Name       string   `json:"name"`
	Examples   []string `json:"examples"`
	CodeSystem string   `json:"codeSystem"`
}

type mapperTargetField struct {
	Name       string `json:"name"`
	CodeSystem string `json:"codeSystem"`
	Pattern    string `json:"pattern"`
}

type mapperSuggestionGroup struct {
	SourceField string             `json:"sourceField"`
	Suggestions []mapperSuggestion `json:"suggestions"`
}

type mapperSuggestion struct {
	Target     string `json:"target"`
	Confidence int    `json:"confidence"`
	Reasoning  string `json:"reasoning"`
	Abstained  bool   `json:"abstained"`
}

// POST /api/recipes/export — export a mapping recipe as a shareable JSON file.
func (s *Server) handleRecipeExport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var recipe profile.MappingRecipe
	if !s.decode(w, r, &recipe) {
		return
	}

	recipe.Format = "perfuse/recipe/v1"
	data, err := profile.MarshalRecipe(&recipe)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(recipe.Name)+".recipe.json\"")
	w.Write(data)
}

// POST /api/recipes/import — import a mapping recipe.
func (s *Server) handleRecipeImport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var raw json.RawMessage
	if !s.decode(w, r, &raw) {
		return
	}

	recipe, err := profile.UnmarshalRecipe(raw)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid recipe format")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"recipe":  recipe,
		"message": "recipe imported successfully",
	})
}

func sanitizeFilename(name string) string {
	out := make([]byte, 0, len(name))
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		} else if c == ' ' {
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "export"
	}
	return string(out)
}
