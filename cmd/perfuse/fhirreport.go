package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// feedReport is what perfuse fhir convert -report writes: how a feed's messages converted, in counts and paths only.
//
// It exists so a site can try the converter on its own live feed and send the result back without sending a single
// message. Nothing from a message goes in: not a name, an identifier, a code, a value, a control id, nor the sending
// application or facility. Notes are counted by the wording of the rule before the message's values were put into it,
// and validation findings by rule and path with the repetition numbers taken out.
type feedReport struct {
	messages, converted, failed, invalid int
	types                                map[string]*typeCount
	resources                            map[string]int
	notes                                map[string]int
	findings                             map[string]int
}

type typeCount struct{ messages, converted, invalid int }

func newFeedReport() *feedReport {
	return &feedReport{types: map[string]*typeCount{}, resources: map[string]int{}, notes: map[string]int{},
		findings: map[string]int{}}
}

// repetition matches the numbers that say which repetition a path is about: OBX(3), identifier[0].
var repetition = regexp.MustCompile(`\(\d+\)|\[\d+\]`)

func (r *feedReport) typeOf(kind string) *typeCount {
	if kind == "" {
		kind = "(unparsed)"
	}
	t := r.types[kind]
	if t == nil {
		t = &typeCount{}
		r.types[kind] = t
	}
	return t
}

// failedMessage counts a message that did not parse or convert. The error is not kept: it can quote the message.
func (r *feedReport) failedMessage(kind string) {
	r.messages++
	r.failed++
	r.typeOf(kind).messages++
}

func (r *feedReport) add(result *v2fhir.Result, validation *fhir.ValidationResult) {
	kind := result.MessageType
	if result.TriggerEvent != "" {
		kind += "^" + result.TriggerEvent
	}
	t := r.typeOf(kind)
	r.messages++
	r.converted++
	t.messages++
	t.converted++
	for k, n := range result.ResourceCounts() {
		r.resources[k] += n
	}
	for _, n := range result.Notes {
		r.notes[strings.Join([]string{n.Severity, repetition.ReplaceAllString(n.Source, ""), repetition.ReplaceAllString(n.Target, ""), n.Template}, "\t")]++
	}
	if !validation.Valid() {
		r.invalid++
		t.invalid++
	}
	for _, f := range validation.Findings {
		if f.Severity == fhir.Error {
			r.findings[f.Rule+"\t"+repetition.ReplaceAllString(f.Path, "")]++
		}
	}
}

func (r *feedReport) write(w io.Writer) {
	fmt.Fprintf(w, "# Perfuse v2 to FHIR feed report\n\n")
	fmt.Fprintf(w, "Perfuse %s. Counts and paths only: no message content, identifiers, codes or values.\n\n", version)
	fmt.Fprintf(w, "%d message(s): %d converted, %d did not convert, %d converted to invalid FHIR\n", r.messages, r.converted,
		r.failed, r.invalid)

	fmt.Fprintf(w, "\n## Message types\n\n| Type | Messages | Converted | Invalid FHIR |\n|---|---|---|---|\n")
	for _, k := range sortedKeys(r.types) {
		t := r.types[k]
		fmt.Fprintf(w, "| %s | %d | %d | %d |\n", k, t.messages, t.converted, t.invalid)
	}

	fmt.Fprintf(w, "\n## Resources produced\n\n")
	for _, k := range sortedKeys(r.resources) {
		fmt.Fprintf(w, "- %s: %d\n", k, r.resources[k])
	}

	fmt.Fprintf(w, "\n## Validation errors, by rule and path\n\n")
	if len(r.findings) == 0 {
		fmt.Fprintf(w, "None.\n")
	} else {
		fmt.Fprintf(w, "| Count | Rule | Path |\n|---|---|---|\n")
		for _, k := range byCount(r.findings) {
			p := strings.SplitN(k, "\t", 2)
			fmt.Fprintf(w, "| %d | %s | %s |\n", r.findings[k], p[0], p[1])
		}
	}

	fmt.Fprintf(w, "\n## Mapping notes, by kind\n\nThe wording is each rule's own, with %%s and %%q where the message's values were.\n\n")
	if len(r.notes) == 0 {
		fmt.Fprintf(w, "None.\n")
	} else {
		fmt.Fprintf(w, "| Count | Severity | From | To | Note |\n|---|---|---|---|---|\n")
		for _, k := range byCount(r.notes) {
			p := strings.SplitN(k, "\t", 4)
			fmt.Fprintf(w, "| %d | %s | %s | %s | %s |\n", r.notes[k], p[0], p[1], p[2], strings.ReplaceAll(p[3], "|", "\\|"))
		}
	}
}

func (r *feedReport) save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	r.write(f)
	return f.Close()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// byCount orders keys most frequent first, then alphabetically, so the report reads the same for the same feed.
func byCount(m map[string]int) []string {
	keys := sortedKeys(m)
	sort.SliceStable(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	return keys
}
