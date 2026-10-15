package sof

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

//go:embed examples/*.json
var examples embed.FS

// Example is a ViewDefinition shipped as a starting point.
type Example struct {
	Name  string          `json:"name"`
	Title string          `json:"title"`
	View  json.RawMessage `json:"view"`
}

// Examples are the shipped example views, by name.
func Examples() []Example {
	entries, _ := examples.ReadDir("examples")
	var out []Example
	for _, e := range entries {
		raw, err := examples.ReadFile("examples/" + e.Name())
		if err != nil {
			continue
		}
		var head struct{ Name, Title string }
		_ = json.Unmarshal(raw, &head)
		out = append(out, Example{Name: head.Name, Title: head.Title, View: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReadResources reads FHIR resources from NDJSON (a bulk export file), a Bundle, a JSON array, or one resource, calling fn
// for each (a Bundle's entries are unwrapped).
func ReadResources(r io.Reader, fn func(map[string]any) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	first, err := br.Peek(1)
	for err == nil && len(first) == 1 && (first[0] == ' ' || first[0] == '\n' || first[0] == '\r' || first[0] == '\t' || first[0] == 0xEF) {
		if first[0] == 0xEF { // a byte order mark
			if _, err := br.Discard(3); err != nil {
				return err
			}
		} else if _, err := br.Discard(1); err != nil {
			return err
		}
		first, err = br.Peek(1)
	}
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	d := json.NewDecoder(br)
	d.UseNumber()
	emit := func(m map[string]any) error {
		if m["resourceType"] == "Bundle" {
			entries, _ := m["entry"].([]any)
			for _, e := range entries {
				em, _ := e.(map[string]any)
				if res, ok := em["resource"].(map[string]any); ok {
					if err := fn(res); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if _, ok := m["resourceType"].(string); !ok {
			return fmt.Errorf("a JSON object without resourceType is not a FHIR resource")
		}
		return fn(m)
	}
	if first[0] == '[' {
		var list []map[string]any
		if err := d.Decode(&list); err != nil {
			return fmt.Errorf("the JSON array cannot be read: %w", err)
		}
		for _, m := range list {
			if err := emit(m); err != nil {
				return err
			}
		}
		return nil
	}
	// One object, or NDJSON: a stream of objects.
	n := 0
	for {
		var m map[string]any
		err := d.Decode(&m)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("resource %d cannot be read: %w", n+1, err)
		}
		n++
		if err := emit(m); err != nil {
			return err
		}
	}
}

// ReadResourcesBytes is ReadResources over bytes.
func ReadResourcesBytes(b []byte, fn func(map[string]any) error) error {
	return ReadResources(bytes.NewReader(b), fn)
}
