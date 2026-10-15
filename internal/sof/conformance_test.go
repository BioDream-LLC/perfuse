package sof

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fixture struct {
	Title     string           `json:"title"`
	Resources []map[string]any `json:"resources"`
	Tests     []struct {
		Title         string           `json:"title"`
		View          map[string]any   `json:"view"`
		Expect        []map[string]any `json:"expect"`
		ExpectError   bool             `json:"expectError"`
		ExpectColumns []string         `json:"expectColumns"`
	} `json:"tests"`
}

// canon writes a row as JSON with numbers in one form, so 5 and 5.0 compare equal as they do in the official runner.
func canon(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "%q:%s", k, canon(x[k]))
		}
		return b.String() + "}"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = canon(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return x.String()
		}
		return fmt.Sprintf("%v", f)
	case int64:
		return fmt.Sprintf("%v", float64(x))
	case float64:
		return fmt.Sprintf("%v", x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeUseNumber(t *testing.T, raw []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		t.Fatal(err)
	}
}

// TestTheOfficialSQLOnFHIRFixtures runs every test of the sql-on-fhir test suite. PERFUSE_SOF_REPORT=FILE writes the
// result in the suite's test-report format, for the implementations page.
func TestTheOfficialSQLOnFHIRFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/sql-on-fhir/*.json")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	report := map[string]any{}
	passed, total := 0, 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fx fixture
		decodeUseNumber(t, raw, &fx)
		var results []map[string]any
		for _, tc := range fx.Tests {
			total++
			name := filepath.Base(f) + "/" + tc.Title
			ok, msg := runFixture(fx.Resources, tc.View, tc.Expect, tc.ExpectError, tc.ExpectColumns)
			results = append(results, map[string]any{"name": tc.Title, "result": map[string]any{"passed": ok}})
			if ok {
				passed++
			} else {
				t.Errorf("%s: %s", name, msg)
			}
		}
		report[filepath.Base(f)] = map[string]any{"tests": results}
	}
	t.Logf("%d of %d passed", passed, total)
	if out := os.Getenv("PERFUSE_SOF_REPORT"); out != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runFixture(resources []map[string]any, view map[string]any, expect []map[string]any, expectError bool, expectColumns []string) (bool, string) {
	v, err := CompileMap(view)
	var rows []map[string]any
	if err == nil {
		for _, r := range resources {
			out, rerr := v.Run(r)
			if rerr != nil {
				err = rerr
				break
			}
			for _, row := range out {
				m := map[string]any{}
				for i, c := range v.Columns {
					m[c.Name] = row[i]
				}
				rows = append(rows, m)
			}
		}
	}
	if expectError {
		if err == nil {
			return false, "expected an error, got rows"
		}
		return true, ""
	}
	if err != nil {
		return false, err.Error()
	}
	if expectColumns != nil {
		var got []string
		for _, c := range v.Columns {
			got = append(got, c.Name)
		}
		if strings.Join(got, ",") != strings.Join(expectColumns, ",") {
			return false, fmt.Sprintf("columns %v, want %v", got, expectColumns)
		}
	}
	if expect == nil {
		return true, ""
	}
	var g, w []string
	for _, r := range rows {
		g = append(g, canon(r))
	}
	for _, r := range expect {
		w = append(w, canon(r))
	}
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		return false, fmt.Sprintf("\n  got  %s\n  want %s", strings.Join(g, "\n       "), strings.Join(w, "\n       "))
	}
	return true, ""
}
