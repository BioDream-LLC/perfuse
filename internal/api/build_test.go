package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func buildChannel(t *testing.T, h *harness, role string, model any) (int, buildResponse) {
	t.Helper()
	res := h.do(role, http.MethodPost, "/api/channels/build", model)

	var out buildResponse
	if res.Body.Len() > 0 {
		_ = json.Unmarshal(res.Body.Bytes(), &out)
	}
	return res.Code, out
}

// minimalModel is the smallest thing the form can produce that is a real channel.
func minimalModel() buildModel {
	return buildModel{
		Name:   "built-by-form",
		Source: buildSource{Type: "mllp", Listen: "127.0.0.1:16901"},
		Dests: []buildDest{
			{Name: "archive", Type: "file", Dir: "/tmp/built-by-form"},
		},
	}
}

func TestTheSimplestFormProducesAValidChannel(t *testing.T) {
	h := newHarness(t)

	code, out := buildChannel(t, h, "editor", minimalModel())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !out.OK {
		t.Fatalf("not valid: %+v\nyaml:\n%s", out.Problems, out.YAML)
	}
	if out.Summary == nil || out.Summary.Name != "built-by-form" {
		t.Fatalf("summary = %+v", out.Summary)
	}
}

func TestGeneratedYAMLCarriesOnlyWhatWasChosen(t *testing.T) {
	// The generated file is what somebody commits and reviews six months from now. A file
	// padded with empty defaults is unreadable, and it hides which settings were decided
	// from which merely exist.
	h := newHarness(t)

	_, out := buildChannel(t, h, "editor", minimalModel())
	if !out.OK {
		t.Fatalf("not valid: %+v", out.Problems)
	}

	for _, unwanted := range []string{
		"description:", "enabled:", "dataType:", "x12:", "filter:",
		"transformations:", "scripts:", "tls:", "ack:", "retry:", "queue:",
		"timeout:", "http:", "database:", "sftp:", "fhir:", "cda:",
	} {
		if strings.Contains(out.YAML, unwanted) {
			t.Errorf("the generated file carries %q that nobody asked for:\n%s", unwanted, out.YAML)
		}
	}

	// And it does carry what was chosen.
	for _, wanted := range []string{"name: built-by-form", "type: mllp", "listen:", "dir:"} {
		if !strings.Contains(out.YAML, wanted) {
			t.Errorf("the generated file is missing %q:\n%s", wanted, out.YAML)
		}
	}
}

func TestGeneratedYAMLReadsInFlowOrder(t *testing.T) {
	// Identity, then what it receives, then what it does, then where it sends. A file that
	// reads top to bottom as the message flows is one somebody can follow without a guide.
	h := newHarness(t)

	m := minimalModel()
	m.Description = "a channel built from the form"
	m.Filter = "PID-3 exists"
	m.Steps = []buildStep{{Set: &buildSet{Path: "PID-8", Value: "F"}}}

	_, out := buildChannel(t, h, "editor", m)
	if !out.OK {
		t.Fatalf("not valid: %+v\n%s", out.Problems, out.YAML)
	}

	order := []string{"name:", "description:", "source:", "filter:", "transformations:", "destinations:"}
	at := -1
	for _, key := range order {
		i := strings.Index(out.YAML, "\n"+key)
		if key == "name:" {
			i = strings.Index(out.YAML, key)
		}
		if i < 0 {
			t.Fatalf("%q is missing:\n%s", key, out.YAML)
		}
		if i < at {
			t.Errorf("%q appears out of order:\n%s", key, out.YAML)
		}
		at = i
	}
}

func TestAValueNeedingQuotesIsQuoted(t *testing.T) {
	// The reason the front end does not assemble YAML. A value that looks like a number,
	// a string with a colon in it, one starting with a quote - each needs different
	// handling, and getting one wrong produces a file that loads as something other than
	// what the form displayed.
	h := newHarness(t)

	m := minimalModel()
	m.Steps = []buildStep{
		{Set: &buildSet{Path: "PID-8", Value: "0123"}},
		{Set: &buildSet{Path: "PID-5", Value: "Smith: John"}},
		{Set: &buildSet{Path: "PID-6", Value: "true"}},
		{Set: &buildSet{Path: "PID-7", Value: "#not-a-comment"}},
		{Set: &buildSet{Path: "PID-9", Value: "*star"}},
	}

	_, out := buildChannel(t, h, "editor", m)
	if !out.OK {
		t.Fatalf("not valid: %+v\n%s", out.Problems, out.YAML)
	}

	// Reloading is the real assertion: the values must come back as the strings that went
	// in, not as numbers, booleans or truncated at a comment.
	_, reloaded := validateYAML(t, h, "editor", out.YAML)
	if !reloaded.OK {
		t.Fatalf("the generated file did not reload: %+v\n%s", reloaded.Problems, out.YAML)
	}

	joined := strings.Join(reloaded.Summary.Steps, "\n")
	for _, want := range []string{`"0123"`, `"Smith: John"`, `"true"`, `"#not-a-comment"`, `"*star"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("value %s did not survive the round trip:\n%s\nyaml:\n%s", want, joined, out.YAML)
		}
	}
}

func TestAnEmptyOptionalBlockIsPrunedNotEmitted(t *testing.T) {
	// A form that shows an optional block leaves an empty object behind when somebody
	// opens it and changes their mind. Emitting "tls: {}" means TLS is configured with no
	// certificate, which fails to load complaining about a missing file rather than saying
	// the obvious thing, which is that TLS was not actually wanted.
	h := newHarness(t)

	m := minimalModel()
	m.Source.TLS = &buildTLS{}
	m.Source.Ack = &buildAck{}
	m.Source.Limits = &buildSrcLimits{}
	m.X12 = &buildX12{}
	m.Scripts = &buildScripts{Transformer: "   \n"}
	m.Dests[0].Retry = &buildRetry{}
	m.Dests[0].Queue = &buildQueue{}
	m.Dests[0].TLS = &buildTLS{}

	_, out := buildChannel(t, h, "editor", m)
	if !out.OK {
		t.Fatalf("not valid: %+v\n%s", out.Problems, out.YAML)
	}
	for _, unwanted := range []string{"tls:", "ack:", "x12:", "scripts:", "retry:", "queue:", "{}"} {
		if strings.Contains(out.YAML, unwanted) {
			t.Errorf("an empty %q survived:\n%s", unwanted, out.YAML)
		}
	}
}

func TestEverySourceTypeCanBeBuilt(t *testing.T) {
	// The builder is only useful if it covers what the engine supports. A source type the
	// form cannot express is one somebody has to hand-write YAML for, which is the thing
	// this is meant to remove.
	h := newHarness(t)

	cases := map[string]buildSource{
		"mllp": {Type: "mllp", Listen: "127.0.0.1:16911"},
		"http": {Type: "http", HTTP: &buildHTTPSrc{Listen: "127.0.0.1:16912", Path: "/in"}},
		"database": {Type: "database", DB: &buildDBSrc{
			Driver: "postgres", DSN: "postgres://u:p@h/d",
			Query: "SELECT id, payload FROM inbound", Column: "payload", KeyColumn: "id",
		}},
		"sftp": {Type: "sftp", SFTP: &buildSFTPSrc{
			Host: "sftp.example.org:22", User: "perfuse", KeyFile: "/tmp/k",
			Dir: "/in", KnownHostsFile: "/tmp/known_hosts", MoveTo: "/in/done",
		}},
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			m := minimalModel()
			m.Name = "src-" + name
			m.Source = src

			_, out := buildChannel(t, h, "editor", m)
			if !out.OK {
				t.Fatalf("%s did not build: %+v\nyaml:\n%s", name, out.Problems, out.YAML)
			}
		})
	}
}

func TestEveryDestinationTypeCanBeBuilt(t *testing.T) {
	h := newHarness(t)

	cases := map[string]buildDest{
		"mllp": {Name: "onward", Type: "mllp", Address: "127.0.0.1:16921"},
		"file": {Name: "archive", Type: "file", Dir: "/tmp/dest-file"},
		"http": {Name: "api", Type: "http", HTTP: &buildHTTPDest{URL: "http://127.0.0.1:16922/in"}},
		"fhir": {Name: "fhir", Type: "fhir", FHIR: &buildFHIRDest{
			URL:                     "http://127.0.0.1:16923/fhir",
			DefaultIdentifierSystem: "urn:oid:1.2.3.4",
		}},
		"cda": {Name: "docs", Type: "cda", CDA: &buildCDADest{Dir: "/tmp/dest-cda"}},
		"database": {Name: "db", Type: "database", DB: &buildDBDest{
			Driver: "postgres", DSN: "postgres://u:p@h/d",
			Statement: "INSERT INTO messages (body) VALUES ($1)", Params: []string{"MSH-10"},
		}},
		"sftp": {Name: "upload", Type: "sftp", SFTP: &buildSFTPDest{
			Host: "sftp.example.org:22", User: "perfuse", KeyFile: "/tmp/k",
			Dir: "/out", KnownHostsFile: "/tmp/known_hosts",
		}},
	}

	for name, dest := range cases {
		t.Run(name, func(t *testing.T) {
			m := minimalModel()
			m.Name = "dest-" + name
			m.Dests = []buildDest{dest}

			_, out := buildChannel(t, h, "editor", m)
			if !out.OK {
				t.Fatalf("%s did not build: %+v\nyaml:\n%s", name, out.Problems, out.YAML)
			}
		})
	}
}

func TestEveryStepKindCanBeBuilt(t *testing.T) {
	h := newHarness(t)

	steps := map[string]buildStep{
		"set":     {Set: &buildSet{Path: "PID-8", Value: "F"}},
		"copy":    {Copy: &buildCopy{From: "PID-3", To: "PID-4"}},
		"clear":   {Clear: &buildPath{Path: "PID-19"}},
		"remove":  {Remove: &buildPath{Path: "PID-19"}},
		"map":     {Map: &buildMap{Path: "PID-8", Table: map[string]string{"1": "M", "2": "F"}}},
		"replace": {Replace: &buildReplace{Path: "PID-3", Pattern: "^0+", With: ""}},
		"pad":     {Pad: &buildPad{Path: "PID-3", Width: 10, With: "0"}},
		"date":    {Date: &buildDate{Path: "PID-7", From: "20060102", To: "2006-01-02"}},
		"trim":    {Trim: &buildPath{Path: "PID-5"}},
		"case":    {Case: &buildCase{Path: "PID-5", To: "upper"}},
	}

	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			m := minimalModel()
			m.Name = "step-" + name
			m.Steps = []buildStep{step}

			_, out := buildChannel(t, h, "editor", m)
			if !out.OK {
				t.Fatalf("%s did not build: %+v\nyaml:\n%s", name, out.Problems, out.YAML)
			}
			if len(out.Summary.Steps) != 1 {
				t.Fatalf("steps = %v", out.Summary.Steps)
			}
			if strings.Contains(out.Summary.Steps[0], "does nothing") {
				t.Errorf("%s produced a step with no action: %q", name, out.Summary.Steps[0])
			}
		})
	}
}

func TestAnInvalidFormReturnsTheYAMLAnyway(t *testing.T) {
	// So the user can see what they built and why it is wrong at the same time. Returning
	// only the error means the one artifact that would explain it is withheld at exactly
	// the moment it is needed.
	h := newHarness(t)

	m := minimalModel()
	m.Filter = "this is not a filter expression ((("

	_, out := buildChannel(t, h, "editor", m)
	if out.OK {
		t.Fatal("a broken filter built clean")
	}
	if out.YAML == "" {
		t.Error("the YAML was withheld on failure")
	}
	if len(out.Problems) == 0 {
		t.Error("no problems were reported")
	}
}

func TestBuildingRoundTripsThroughValidate(t *testing.T) {
	// The builder's output must be something the validator accepts, or the two halves of
	// the same feature disagree.
	h := newHarness(t)

	m := minimalModel()
	m.Description = "round trip"
	m.Filter = "PID-3 exists"
	m.Steps = []buildStep{{Description: "normalise sex", Map: &buildMap{
		Path: "PID-8", Table: map[string]string{"1": "M", "2": "F"}, Default: "U",
	}}}
	m.Source.Ack = &buildAck{When: "on_receipt"}
	m.Dests[0].Queue = &buildQueue{Enabled: true, MaxAttempts: 5}

	_, built := buildChannel(t, h, "editor", m)
	if !built.OK {
		t.Fatalf("build failed: %+v\n%s", built.Problems, built.YAML)
	}

	_, validated := validateYAML(t, h, "editor", built.YAML)
	if !validated.OK {
		t.Fatalf("the built YAML did not validate: %+v\n%s", validated.Problems, built.YAML)
	}
	if validated.Summary.Name != built.Summary.Name {
		t.Errorf("names differ: %q and %q", validated.Summary.Name, built.Summary.Name)
	}
	if !strings.Contains(validated.Summary.Acknowledges, "can still be lost") {
		t.Errorf("the ack setting did not survive: %q", validated.Summary.Acknowledges)
	}
}

func TestAViewerCannotBuild(t *testing.T) {
	h := newHarness(t)
	res := h.do("viewer", http.MethodPost, "/api/channels/build", minimalModel())
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}
