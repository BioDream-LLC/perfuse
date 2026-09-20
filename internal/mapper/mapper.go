// Package mapper suggests field mappings between systems with confidence scores.
//
// This is a rules-based intelligent mapping engine. No LLM, no network calls, no external
// dependencies. It runs offline in the same binary and never sends patient data anywhere.
//
// The engine combines multiple signals - fuzzy string matching on field names, healthcare
// identifier pattern recognition, code system lookups, and historical acceptance data - into
// a single confidence score per suggestion. When confidence is below a configurable threshold
// the mapper abstains rather than guessing wrong, because a wrong mapping in healthcare can
// route data to the wrong field and that is a patient-safety defect.
//
// Abstention is the core design decision. A system that always produces an answer trains users
// to stop checking, and the one time it is wrong nobody notices until a lab result lands in a
// note field. A system that says "I don't know" forces a human decision exactly when the human
// is needed.
package mapper

import "sync"

// SourceField describes a field in the source system that needs to be mapped.
type SourceField struct {
	// Name is the display name of the field (e.g. "Patient MRN", "Date of Birth").
	Name string

	// Examples are sample values from real data that help pattern detection.
	Examples []string

	// CodeSystem is the terminology system if known (e.g. "http://loinc.org").
	CodeSystem string
}

// TargetField describes a candidate field in the target system.
type TargetField struct {
	// Name is the display name of the target field.
	Name string

	// CodeSystem is the terminology system if known.
	CodeSystem string

	// Pattern is the expected data pattern (e.g. "npi", "date", "phone").
	Pattern string
}

// Suggestion is a single mapping recommendation from the engine.
type Suggestion struct {
	// Target is the recommended target field.
	Target TargetField

	// Confidence is 0-100 indicating how sure the engine is.
	Confidence int

	// Reasoning explains why this suggestion was made, for auditability.
	Reasoning string

	// Abstained is true when the engine's best guess was below the threshold.
	// When true, Confidence reflects what the score would have been, and Reasoning
	// explains why it was not good enough.
	Abstained bool
}

// Config controls the mapper's behavior.
type Config struct {
	// Threshold is the minimum confidence (0-100) required to make a suggestion
	// without abstaining. Default 70.
	Threshold int

	// HistoricalBoost is how many points a previously-accepted mapping gets added.
	// Default 15.
	HistoricalBoost int
}

func (c Config) threshold() int {
	if c.Threshold <= 0 {
		return 70
	}
	return c.Threshold
}

func (c Config) historicalBoost() int {
	if c.HistoricalBoost <= 0 {
		return 15
	}
	return c.HistoricalBoost
}

// Engine is the mapping suggestion engine.
type Engine struct {
	config  Config
	targets []TargetField

	mu      sync.Mutex
	history map[string]map[string]int // source name -> target name -> accept count
}

// New creates a mapping engine with the given target fields and configuration.
func New(targets []TargetField, config Config) *Engine {
	return &Engine{
		config:  config,
		targets: targets,
		history: make(map[string]map[string]int),
	}
}

// Suggest returns mapping suggestions for a source field, ordered by confidence descending.
//
// If the best suggestion is below the configured threshold, all returned suggestions will
// have Abstained set to true. The suggestions are still returned so a UI can show them as
// grey/uncertain options, but the flag makes clear that the engine does not endorse them.
func (e *Engine) Suggest(src SourceField) []Suggestion {
	if len(e.targets) == 0 {
		return nil
	}

	var suggestions []Suggestion
	for _, t := range e.targets {
		score, reasoning := e.score(src, t)
		suggestions = append(suggestions, Suggestion{
			Target:     t,
			Confidence: score,
			Reasoning:  reasoning,
		})
	}

	// Sort by confidence descending.
	sortSuggestions(suggestions)

	// Apply abstention: if the best score is below threshold, mark all as abstained.
	thresh := e.config.threshold()
	if len(suggestions) > 0 && suggestions[0].Confidence < thresh {
		for i := range suggestions {
			suggestions[i].Abstained = true
			suggestions[i].Reasoning += " [abstained: below threshold]"
		}
	}

	return suggestions
}

// Accept records that a user accepted a mapping, boosting future suggestions.
func (e *Engine) Accept(sourceName, targetName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.history[sourceName] == nil {
		e.history[sourceName] = make(map[string]int)
	}
	e.history[sourceName][targetName]++
}

// historicalScore returns the boost for a source->target pair based on past acceptances.
func (e *Engine) historicalScore(sourceName, targetName string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := e.history[sourceName][targetName]
	if count <= 0 {
		return 0
	}
	// Cap at the configured boost regardless of how many times it was accepted.
	return e.config.historicalBoost()
}

// score computes the confidence for mapping src to target, returning score and reasoning.
func (e *Engine) score(src SourceField, target TargetField) (int, string) {
	signals := scoreSignals(src, target)

	// Add historical boost.
	boost := e.historicalScore(src.Name, target.Name)
	if boost > 0 {
		signals.historical = boost
		signals.reasons = append(signals.reasons, "historical acceptance boost")
	}

	return signals.combine()
}

// sortSuggestions sorts by confidence descending, stable.
func sortSuggestions(s []Suggestion) {
	// Simple insertion sort - suggestion lists are small (tens of targets, not thousands).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Confidence > s[j-1].Confidence; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
