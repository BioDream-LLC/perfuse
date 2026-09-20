package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// No array in this response may be null, at any depth.
//
// A nil Go slice marshals as JSON null. The front end declares these fields as arrays and reads .length, so a null throws during a
// render - and an exception during a render does not blank the panel, it blanks the entire application. There is no error on screen and
// nothing in the interface to suggest what happened.
//
// Five of these were found and fixed by sweeping the arrays at the top of each response. This is the sixth, and it was missed because
// usedBy and paths sit inside the elements of one of those arrays. A table loaded by a channel and referenced by none leaves both empty -
// exactly the case the endpoint exists to surface, since an unused mapping table is either a mistake or dead weight.
//
// So this walks the whole structure rather than checking named fields. A depth-limited check would have passed on the bug it was written
// for.
func TestNoNullArraysInTheCodesetsResponse(t *testing.T) {
	// A table nobody uses, which is the case that broke.
	body := codesetsResponse{
		Tables: []tableImpact{{
			Name:      "orphan",
			File:      "shared.codeset.yaml",
			Describes: "a table nobody references any more",
			Entries:   1,
			UsedBy:    []tableUse{},
			Paths:     []string{},
		}},
		Unreferenced: 1,
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("the response does not marshal: %v", err)
	}

	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("the response does not round trip: %v", err)
	}

	for _, path := range nullsIn(tree, "") {
		t.Errorf("%s is null. An empty list must marshal as [] - the interface reads .length on it, "+
			"and a null throws during a render and blanks the whole console", path)
	}
}

// An empty response must also be an empty array, not null.
//
// This is the top-level case that was fixed before. Kept alongside the nested one so a change that reintroduces either is caught,
// because they were introduced by the same habit and will be again.
func TestAnInstallationWithNoTablesReturnsAnEmptyArray(t *testing.T) {
	raw, err := json.Marshal(codesetsResponse{Tables: []tableImpact{}})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if strings.Contains(string(raw), `"tables":null`) {
		t.Errorf("tables is null on an installation with none: %s", raw)
	}
}

// nullsIn returns the paths of every null found anywhere in a decoded JSON tree.
func nullsIn(node any, path string) []string {
	var out []string

	switch v := node.(type) {
	case nil:
		return []string{orRoot(path)}

	case map[string]any:
		// Sorted iteration is unnecessary here because every finding is reported, not just the first.
		for key, child := range v {
			out = append(out, nullsIn(child, path+"."+key)...)
		}

	case []any:
		for i, child := range v {
			out = append(out, nullsIn(child, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}

	return out
}

// orRoot names the top of the tree when a path is empty.
func orRoot(path string) string {
	if path == "" {
		return "the response body"
	}

	return strings.TrimPrefix(path, ".")
}
