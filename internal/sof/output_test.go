package sof

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const outView = `{"resourceType":"ViewDefinition","resource":"Patient","name":"patients","select":[{"column":[
 {"name":"id","path":"getResourceKey()","type":"id"},
 {"name":"family","path":"name.first().family","type":"string"},
 {"name":"active","path":"active","type":"boolean"},
 {"name":"births","path":"multipleBirth.ofType(integer)","type":"integer"},
 {"name":"weight","path":"extension('http://example.org/w').value.ofType(decimal)","type":"decimal"},
 {"name":"given","path":"name.given","type":"string","collection":true}]}]}`

var outPatients = []string{
	`{"resourceType":"Patient","id":"p1","active":true,"multipleBirthInteger":2,"name":[{"family":"Doe","given":["Jane","Q"]}],
	  "extension":[{"url":"http://example.org/w","valueDecimal":61.5}]}`,
	`{"resourceType":"Patient","id":"p2","active":false,"name":[{"family":"Smith, \"Jr\"","given":["John"]}]}`,
	`{"resourceType":"Patient","id":"p3"}`,
}

func runOut(t *testing.T, format string) []byte {
	t.Helper()
	v, err := Compile([]byte(outView))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format, v.Columns, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range outPatients {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(p))
		d.UseNumber()
		_ = d.Decode(&m)
		rows, err := v.Run(m)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if err := w.Write(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTheOutputFormats(t *testing.T) {
	csv := string(runOut(t, "csv"))
	want := "id,family,active,births,weight,given\np1,Doe,true,2,61.5,\"[\"\"Jane\"\",\"\"Q\"\"]\"\np2,\"Smith, \"\"Jr\"\"\",false,,,\"[\"\"John\"\"]\"\np3,,,,,[]\n"
	if csv != want {
		t.Errorf("csv:\n%s\nwant\n%s", csv, want)
	}
	nd := string(runOut(t, "ndjson"))
	if !strings.HasPrefix(nd, `{"id":"p1","family":"Doe","active":true,"births":2,"weight":61.5,"given":["Jane","Q"]}`+"\n") {
		t.Errorf("ndjson: %s", nd)
	}
	var arr []map[string]any
	if err := json.Unmarshal(runOut(t, "json"), &arr); err != nil || len(arr) != 3 {
		t.Errorf("json: %v %d", err, len(arr))
	}
	pq := runOut(t, "parquet")
	if !bytes.HasPrefix(pq, []byte("PAR1")) || !bytes.HasSuffix(pq, []byte("PAR1")) {
		t.Fatal("not a Parquet file")
	}
}

// DuckDB reads the Parquet file and finds the same rows and types. PERFUSE_DUCKDB names its command-line program.
func TestDuckDBReadsTheParquet(t *testing.T) {
	duck := os.Getenv("PERFUSE_DUCKDB")
	if duck == "" {
		if p, err := exec.LookPath("duckdb"); err == nil {
			duck = p
		}
	}
	if duck == "" {
		t.Skip("no duckdb (set PERFUSE_DUCKDB)")
	}
	f := filepath.Join(t.TempDir(), "p.parquet")
	if err := os.WriteFile(f, runOut(t, "parquet"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(duck, "-csv", "-c", "SELECT id, family, active, births, weight, given, typeof(active), typeof(births), typeof(weight) FROM '"+f+"' ORDER BY id").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := `id,family,active,births,weight,given,typeof(active),typeof(births),typeof(weight)
p1,Doe,true,2,61.5,"[""Jane"",""Q""]",BOOLEAN,BIGINT,DOUBLE
p2,"Smith, ""Jr""",false,NULL,NULL,"[""John""]",BOOLEAN,BIGINT,DOUBLE
p3,NULL,NULL,NULL,NULL,[],BOOLEAN,BIGINT,DOUBLE
`
	if string(out) != want {
		t.Errorf("duckdb read:\n%s\nwant\n%s", out, want)
	}
}

func TestDuckDBReadsALargerParquet(t *testing.T) {
	duck := os.Getenv("PERFUSE_DUCKDB")
	if duck == "" {
		t.Skip("set PERFUSE_DUCKDB")
	}
	v, err := Compile([]byte(outView))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, "parquet", v.Columns, true)
	for i := 0; i < 1003; i++ {
		r := Row{"p" + strings.Repeat("x", i%7), nil, nil, int64(i), nil, []any{}}
		if i%3 == 0 {
			r[1] = "F" + strings.Repeat("y", i%5)
			r[2] = i%2 == 0
			r[4] = json.Number("1.25")
		}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	f := filepath.Join(t.TempDir(), "big.parquet")
	_ = os.WriteFile(f, buf.Bytes(), 0o600)
	out, err := exec.Command(duck, "-csv", "-noheader", "-c", "SELECT count(*), count(family), count(active), sum(births), sum(weight), count(*) FILTER (active) FROM '"+f+"'").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// 335 rows have i%3 == 0; of those 168 are even; births sum 0..1002; weight 335 * 1.25.
	if strings.TrimSpace(string(out)) != "1003,335,335,502503,418.75,168" {
		t.Errorf("duckdb: %s", out)
	}
}

func TestTheExamplesCompileAndRun(t *testing.T) {
	ex := Examples()
	if len(ex) < 5 {
		t.Fatalf("%d examples", len(ex))
	}
	bp := `{"resourceType":"Observation","id":"bp1","status":"final","subject":{"reference":"Patient/pt1"},
	  "effectiveDateTime":"2026-10-01T09:30:00Z","code":{"coding":[{"system":"http://loinc.org","code":"85354-9"}]},
	  "component":[{"code":{"coding":[{"system":"http://loinc.org","code":"8480-6"}]},"valueQuantity":{"value":120,"code":"mm[Hg]"}},
	   {"code":{"coding":[{"system":"http://loinc.org","code":"8462-4"}]},"valueQuantity":{"value":80,"code":"mm[Hg]"}}]}`
	for _, e := range ex {
		v, err := Compile(e.View)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if e.Name == "blood_pressure" {
			var rows []Row
			if err := ReadResourcesBytes([]byte(bp), func(m map[string]any) error {
				r, err := v.Run(m)
				rows = append(rows, r...)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || cell(rows[0][3]) != "120" || cell(rows[0][4]) != "80" || rows[0][1] != "pt1" {
				t.Errorf("blood pressure: %v", rows)
			}
		}
	}
}

func TestResourcesAreReadInEveryShape(t *testing.T) {
	p := `{"resourceType":"Patient","id":"a"}`
	for _, in := range []string{p, p + "\n" + p + "\n", "[" + p + "," + p + "]",
		`{"resourceType":"Bundle","entry":[{"resource":` + p + `},{"resource":` + p + `}]}`, "\ufeff" + p} {
		n := 0
		if err := ReadResourcesBytes([]byte(in), func(map[string]any) error { n++; return nil }); err != nil || n == 0 {
			t.Errorf("%q: %v %d", in, err, n)
		}
	}
	if err := ReadResourcesBytes([]byte(`{"no":"type"}`), func(map[string]any) error { return nil }); err == nil {
		t.Error("an object with no resourceType")
	}
}
