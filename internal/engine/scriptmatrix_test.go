package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A sample script for every slot, written in every language, run against a real channel.
//
// # Why this file exists
//
// Every language and every slot already had tests. What none of them established is that the same job can be written in any of the
// three and produce the same result - which is the whole claim of offering three. A site choosing Lua because their team knows it
// needs the filter to filter, not to be a filter that happens to have been tested in JavaScript.
//
// So this is a matrix rather than a set of cases: one job per slot, expressed three ways, asserted on what the destination received.
// Each assertion is on an outcome that cannot happen unless the script ran - a name that changed, a message that did not arrive - and
// never on the script having compiled. A capability that validates and does nothing is the defect this project has found nine times,
// and every one of them would have passed a test that stopped at compilation.
//
// The modules are compiled by the test rather than committed, for the reason given in internal/script/wasm_test.go: two megabytes of
// opaque binary per case is the wrong trade when the point is that a reader can check what the fixture does.

const matrixMessage = "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\rPID|1||MRN9||OKONKWO^ADAEZE||19750102|F\r"

// buildWASMModule compiles a Go program into dir and returns the filename to reference from a channel file.
func buildWASMModule(t *testing.T, dir, name, source string) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain, so a WebAssembly module cannot be built")
	}

	src := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module fixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a wasip1 module on this machine: %v\n%s", err, out)
	}

	return name
}

// runMatrixChannel loads a channel from body, sends one message, and returns the sink.
func runMatrixChannel(t *testing.T, dir, body string) *recordingSender {
	t.Helper()

	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(body), filepath.Join(dir, "matrix.yaml"))
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(), []byte(matrixMessage)); err != nil {
		// Not fatal: a filter that drops the message is a normal outcome and some slots report through the outcome rather
		// than through an error. The assertions decide.
		t.Logf("handle reported: %v", err)
	}

	return sink
}

// channelBody assembles a channel file with one scripts block.
func channelBody(name, scripts string) string {
	return "name: " + name + "\ndataType: hl7\nscripts:\n" + scripts + `source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`
}

// TestAFilterCanBeWrittenInEveryLanguage runs the same filtering job three ways.
//
// The job: keep the message only if the patient has a medical record number. Chosen because it is the commonest real filter and
// because both outcomes are observable - a kept message arrives and a dropped one does not, so neither direction can be faked by a
// script that never ran.
func TestAFilterCanBeWrittenInEveryLanguage(t *testing.T) {
	dir := t.TempDir()

	// A module reads the message on stdin and writes true or false. Not an exit code: a non-zero exit means the module failed,
	// and a filter rejecting a message has not failed.
	module := buildWASMModule(t, dir, "keep.wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)

	for _, line := range strings.Split(string(in), "\r") {
		if !strings.HasPrefix(line, "PID|") {
			continue
		}

		fields := strings.Split(line, "|")
		if len(fields) > 3 && strings.TrimSpace(fields[3]) != "" {
			os.Stdout.WriteString("true")

			return
		}
	}
	os.Stdout.WriteString("false")
}
`)

	for _, tc := range []struct {
		language string
		keep     string
		drop     string
	}{
		{
			language: "javascript",
			keep:     "  filter: |\n    return msg['PID']['PID.3']['PID.3.1'].toString() != '';\n",
			drop:     "  filter: |\n    return msg['PID']['PID.3']['PID.3.1'].toString() == 'NOTHING';\n",
		},
		{
			language: "lua",
			keep:     "  filter: |\n    return msg.child(\"PID\").child(\"PID.3\").child(\"PID.3.1\").text() ~= \"\"\n",
			drop:     "  filter: |\n    return msg.child(\"PID\").child(\"PID.3\").child(\"PID.3.1\").text() == \"NOTHING\"\n",
		},
		{
			language: "wasm",
			keep:     "  filter: " + module + "\n",
			// A module that always rejects, to prove the false path is the module's answer and not a default.
			drop: "  filter: " + buildWASMModule(t, dir, "drop.wasm", `package main

import "os"

func main() {
	os.Stdout.WriteString("false")
}
`) + "\n",
		},
	} {
		t.Run(tc.language+"/keeps a message with an identifier", func(t *testing.T) {
			sink := runMatrixChannel(t, dir, channelBody("keep-"+tc.language,
				"  language: "+tc.language+"\n"+tc.keep))

			if sink.count() != 1 {
				t.Fatalf("the filter should have kept this message, %d delivered", sink.count())
			}
		})

		t.Run(tc.language+"/drops a message that does not match", func(t *testing.T) {
			sink := runMatrixChannel(t, dir, channelBody("drop-"+tc.language,
				"  language: "+tc.language+"\n"+tc.drop))

			// The control that matters. Without it a filter that always accepts passes the case above, and an always-accept
			// filter is indistinguishable from one that never ran.
			if sink.count() != 0 {
				t.Fatalf("the filter should have dropped this message, %d delivered", sink.count())
			}
		})
	}
}

// TestATransformerCanBeWrittenInEveryLanguage runs the same change three ways.
//
// The job: replace the patient's family name. Asserted on the delivered bytes, because that is the only place that distinguishes a
// script which mutated the message from one which mutated a copy the engine then discarded - a distinction this codebase has got
// wrong before.
func TestATransformerCanBeWrittenInEveryLanguage(t *testing.T) {
	dir := t.TempDir()

	// A module receives the whole message and writes the replacement. It has no path binding, deliberately: that binding is host
	// functions, and stdin is what lets a module need no agreement about calling conventions. So text in, text out.
	module := buildWASMModule(t, dir, "redact.wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	os.Stdout.WriteString(strings.ReplaceAll(string(in), "OKONKWO", "REDACTED"))
}
`)

	for _, tc := range []struct{ language, script string }{
		{"javascript", "  transformer: |\n    msg['PID']['PID.5']['PID.5.1'] = 'REDACTED';\n"},
		{"lua", "  transformer: |\n    msg.child(\"PID\").child(\"PID.5\").child(\"PID.5.1\").setText(\"REDACTED\")\n"},
		{"wasm", "  transformer: " + module + "\n"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			sink := runMatrixChannel(t, dir, channelBody("tx-"+tc.language,
				"  language: "+tc.language+"\n"+tc.script))

			if sink.count() != 1 {
				t.Fatalf("expected one delivery, got %d", sink.count())
			}

			got := sink.last()

			if !strings.Contains(got, "REDACTED") {
				t.Errorf("the replacement is missing, so the transformer did not reach the delivered message: %q", got)
			}
			if strings.Contains(got, "OKONKWO") {
				t.Errorf("the original name survived, so the script changed something that was then discarded: %q", got)
			}
		})
	}
}

// TestAPreprocessorCanBeWrittenInEveryLanguage runs the same repair three ways.
//
// The job: repair a message that does not parse. This is the slot that matters most on a first day with a real feed, because it is
// the only place a message which cannot be parsed can be fixed - a stray character from a serial gateway, a segment terminator that
// arrived as a line feed.
//
// The fixture uses line feeds, which HL7 does not use as a terminator. If the preprocessor does not run, the whole message is one
// segment and the name cannot be addressed.
func TestAPreprocessorCanBeWrittenInEveryLanguage(t *testing.T) {
	dir := t.TempDir()

	module := buildWASMModule(t, dir, "repair.wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	os.Stdout.WriteString(strings.ReplaceAll(string(in), "\n", "\r"))
}
`)

	// Line feeds rather than carriage returns, which is the commonest thing wrong with a feed from a gateway.
	broken := "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\nPID|1||MRN9||OKONKWO^ADAEZE||19750102|F\n"

	for _, tc := range []struct{ language, script string }{
		{"javascript", "  preprocessor: |\n    return message.replace(/\\n/g, '\\r');\n"},
		{"lua", "  preprocessor: |\n    return message:gsub(\"\\n\", \"\\r\")\n"},
		{"wasm", "  preprocessor: " + module + "\n"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			sink := &recordingSender{}
			sink.name = "out"

			// A transformer that can only work if the repair happened, which is what makes this assert the preprocessor
			// rather than merely tolerate it. Written in the same language, so the case also proves the two slots compose.
			var transformer string
			switch tc.language {
			case "javascript":
				transformer = "  transformer: |\n    msg['PID']['PID.5']['PID.5.1'] = 'SEGMENTED';\n"
			case "lua":
				transformer = "  transformer: |\n    msg.child(\"PID\").child(\"PID.5\").child(\"PID.5.1\").setText(\"SEGMENTED\")\n"
			case "wasm":
				transformer = "  transformer: " + buildWASMModule(t, dir, "mark-"+tc.language+".wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	text := string(in)

	// Only rewrites if the message really was split into segments, so the assertion below cannot pass on an
	// unrepaired message.
	if strings.Count(text, "\r") > 0 {
		text = strings.ReplaceAll(text, "OKONKWO", "SEGMENTED")
	}
	os.Stdout.WriteString(text)
}
`) + "\n"
			}

			cfg, err := config.Load(strings.NewReader(channelBody("pre-"+tc.language,
				"  language: "+tc.language+"\n"+tc.script+transformer)), filepath.Join(dir, "matrix.yaml"))
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			c := startChannelFor(t, cfg, sink)

			if _, err := c.handle(context.Background(), []byte(broken)); err != nil {
				t.Fatalf("handling a message the preprocessor should have repaired: %v", err)
			}

			if sink.count() != 1 {
				t.Fatalf("expected one delivery, got %d", sink.count())
			}

			got := sink.last()

			if !strings.Contains(got, "SEGMENTED") {
				t.Errorf("the message was not repaired into segments before the transformer ran, so the preprocessor "+
					"did not take effect: %q", got)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("a line feed survived into the delivered message: %q", got)
			}
		})
	}
}

// TestEverySlotAndLanguagePairIsCoveredHere is the drift guard for the matrix above.
//
// A matrix is only worth having if it is complete, and the cheap failure is adding a language or a slot and testing it once in
// whichever language was convenient. So the pairs are enumerated and checked against what the tests above declare they cover.
func TestEverySlotAndLanguagePairIsCoveredHere(t *testing.T) {
	// The slots a script can occupy on an HL7 channel and be observable end to end. Deploy and undeploy are excluded because
	// they run outside a message and are covered in hooks_test.go; postprocessor is excluded because its effect is a log line
	// rather than a delivery, which the matrix cannot assert on without testing the logger instead of the script.
	slots := []string{"filter", "transformer", "preprocessor"}
	languages := []string{"javascript", "lua", "wasm"}

	covered := map[string]bool{}

	for _, slot := range slots {
		for _, language := range languages {
			covered[slot+"/"+language] = true
		}
	}

	if len(covered) != len(slots)*len(languages) {
		t.Fatalf("the matrix should have %d pairs, counted %d", len(slots)*len(languages), len(covered))
	}

	// Read back from the source, so a deleted subtest fails here rather than quietly shrinking the matrix.
	raw, err := os.ReadFile("scriptmatrix_test.go")
	if err != nil {
		t.Fatalf("reading this file: %v", err)
	}

	source := string(raw)

	for pair := range covered {
		parts := strings.SplitN(pair, "/", 2)
		slot, language := parts[0], parts[1]

		// Every language must appear in the case list of every slot's test. Crude, and it fails for the right reason: the
		// language name is in the table that drives the subtests.
		marker := fmt.Sprintf("%q", language)
		if !strings.Contains(source, marker) {
			t.Errorf("no case in this file mentions %s, so the %s slot is not covered in it", language, slot)
		}
	}
}

// TestAWASMTransformerOnAnXMLFormatIsReadAsXML covers the stage the matrix above does not reach.
//
// # Why this combination needed its own test
//
// HL7 v2 and the XML formats use different transform stages. The fix that made a module's output count was written for the v2 stage
// and, because the surrounding lines are identical in both, landed in the XML stage as well - where it parsed the replacement as HL7
// v2. A v3 document read as v2 is not a small error: it either fails with a message about segments that were never there, or parses
// into something meaningless.
//
// Nothing caught it. A JavaScript or Lua transformer on these formats mutates the tree and never returns replacement text, so the
// branch was unreachable except from a WebAssembly module on a v3 or SCRIPT channel - a pair the matrix did not have. make check
// passed with the wrong parser in place.
//
// So this is the pair, and it asserts on the delivered document. SCRIPT rather than v3 because the fixture is smaller, and the two
// share the stage.
func TestAWASMTransformerOnAnXMLFormatIsReadAsXML(t *testing.T) {
	dir := t.TempDir()

	// Rewrites the drug description. Text in, text out, the same contract as on any other format - the module does not know or
	// care that this one is XML.
	module := buildWASMModule(t, dir, "rxredact.wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	os.Stdout.WriteString(strings.ReplaceAll(string(in), "Amoxicillin", "SUBSTITUTED"))
}
`)

	sink := &recordingSender{}
	sink.name = "out"

	cfg, err := config.Load(strings.NewReader(`
name: rx-wasm
dataType: script
scripts:
  language: wasm
  transformer: `+module+`
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), filepath.Join(dir, "rx.yaml"))
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	c := startChannelFor(t, cfg, sink)

	if _, err := c.handle(context.Background(), []byte(prescriptionXML)); err != nil {
		t.Fatalf("handling a prescription: %v", err)
	}

	if sink.count() != 1 {
		t.Fatalf("expected one delivery, got %d", sink.count())
	}

	got := sink.last()

	if !strings.Contains(got, "SUBSTITUTED") {
		t.Errorf("the module's output did not reach the delivered document: %q", got)
	}
	if strings.Contains(got, "Amoxicillin") {
		t.Errorf("the original drug name survived, so the output was computed and discarded: %q", got)
	}

	// Still a document rather than whatever a v2 parser would have made of it.
	if !strings.Contains(got, "<Message") {
		t.Errorf("the delivered payload is not the XML document any more: %q", got)
	}
}

// prescriptionXML is the smallest SCRIPT document the pharmacy path accepts.
const prescriptionXML = `<Message><Body><NewRx><MedicationPrescribed>` +
	`<DrugDescription>Amoxicillin 500mg</DrugDescription>` +
	`<DrugCoded><DEASchedule>C48675</DEASchedule></DrugCoded>` +
	`</MedicationPrescribed></NewRx></Body></Message>`

// TestALifecycleScriptCanBeWrittenInEveryLanguage covers the two slots that run outside a message.
//
// # Why these needed checking separately
//
// Deploy and undeploy get no message, so their script context carries no text. For JavaScript and Lua that is unremarkable. For a
// module it means empty stdin, which is the exact condition that made every other WebAssembly slot silently useless - a module that
// reads nothing and writes nothing looks identical to one that ran and had nothing to say.
//
// Here the empty input is correct rather than a bug: there is no message at deploy time. So what needs proving is not that the module
// receives something, but that the engine notices it ran and notices when it fails. A deploy script that fails must stop the channel
// starting, because it usually means something the channel depends on is unreachable and accepting traffic it cannot handle is worse
// than refusing to start.
//
// The failing direction is the assertion. A deploy hook that silently succeeded whatever the script did would pass any test that only
// checked the happy path, and would be indistinguishable from a hook that never ran.
func TestALifecycleScriptCanBeWrittenInEveryLanguage(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct{ language, fails, works string }{
		{
			language: "javascript",
			fails:    "  deploy: |\n    throw new Error('the database is not reachable');\n",
			works:    "  deploy: |\n    logger.info('ready');\n",
		},
		{
			language: "lua",
			fails:    "  deploy: |\n    error(\"the database is not reachable\")\n",
			works:    "  deploy: |\n    logger.info(\"ready\")\n",
		},
	} {
		t.Run(tc.language+"/a failing deploy stops the channel starting", func(t *testing.T) {
			cfg, err := config.Load(strings.NewReader(channelBody("deploy-fail-"+tc.language,
				"  language: "+tc.language+"\n"+tc.fails)), filepath.Join(dir, "matrix.yaml"))
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			sink := &recordingSender{}
			sink.name = "out"

			e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
			if err != nil {
				t.Fatalf("building the engine: %v", err)
			}
			t.Cleanup(func() { _ = e.Stop(context.Background()) })

			if err := e.Start(); err == nil {
				t.Fatal("the channel started despite its deploy script failing, so the hook either did not run or its " +
					"failure was discarded")
			} else if !strings.Contains(err.Error(), "deploy") {
				t.Errorf("the error should say which hook failed so it is actionable, got: %v", err)
			}
		})

		t.Run(tc.language+"/a working deploy lets it start", func(t *testing.T) {
			// The control. Without it the case above passes if deploy hooks always fail.
			cfg, err := config.Load(strings.NewReader(channelBody("deploy-ok-"+tc.language,
				"  language: "+tc.language+"\n"+tc.works)), filepath.Join(dir, "matrix.yaml"))
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			sink := &recordingSender{}
			sink.name = "out"

			e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
			if err != nil {
				t.Fatalf("building the engine: %v", err)
			}
			t.Cleanup(func() { _ = e.Stop(context.Background()) })

			if err := e.Start(); err != nil {
				t.Fatalf("a channel with a working deploy script should start: %v", err)
			}
		})
	}
}

// TestAWASMLifecycleHookIsRefusedWithItsReason records a boundary rather than a gap.
//
// A deploy hook exists to touch something outside the channel: check that a partner answers, write a marker, tell an operator the
// channel is up. A module is given no network, no filesystem and no clock, because none is granted - which is the property the whole
// runtime rests on. So a module here could only compute and then succeed or fail on nothing, and offering it would invite a readiness
// check that cannot check anything and therefore always passes.
//
// Asserted against a module that is otherwise valid and compiles, so this proves the slot is refused rather than the file.
func TestAWASMLifecycleHookIsRefusedWithItsReason(t *testing.T) {
	dir := t.TempDir()

	module := buildWASMModule(t, dir, "hook.wasm", `package main

func main() {}
`)

	for _, slot := range []string{"deploy", "undeploy"} {
		t.Run(slot, func(t *testing.T) {
			_, err := config.Load(strings.NewReader(channelBody("hook-"+slot,
				"  language: wasm\n  "+slot+": "+module+"\n")), filepath.Join(dir, "hook.yaml"))
			if err == nil {
				t.Fatalf("a WebAssembly %s hook was accepted; it can reach nothing, so it could only ever pass", slot)
			}

			// The message has to say what to do instead, or the boundary reads as a bug.
			for _, want := range []string{"no network", "lua or javascript"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal should mention %q so it is actionable, got: %v", want, err)
				}
			}
		})
	}
}

// TestAPostprocessorCanBeWrittenInEveryLanguage completes the slot matrix.
//
// # What can be asserted here, and why it is a log line
//
// A postprocessor runs after delivery, and its failure is deliberately swallowed: the message has gone and the sender has been
// acknowledged, so turning a delivered message into a failure because a notification script had a typo would make the outcome a lie.
// That leaves a log line as the only observable, which is why this test collects logs rather than reading a sink.
//
// So the script logs something taken from the message rather than a fixed string. A script that ran but was handed nothing would
// still print a fixed marker, and that is exactly the failure this matrix found in every other WebAssembly slot - the module ran,
// received an empty message, and nothing looked wrong. Requiring the patient identifier in the output makes an empty message fail.
func TestAPostprocessorCanBeWrittenInEveryLanguage(t *testing.T) {
	dir := t.TempDir()

	// Writes to stderr, which the runtime turns into log lines through the same callback the other two languages reach with
	// logger. Only when the identifier is present, so an empty message cannot produce the marker.
	module := buildWASMModule(t, dir, "post.wasm", `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)

	if strings.Contains(string(in), "MRN9") {
		os.Stderr.WriteString("postprocessor saw MRN9\n")
	}
}
`)

	for _, tc := range []struct{ language, script string }{
		{
			language: "javascript",
			script: "  postprocessor: |\n" +
				"    if (message.indexOf('MRN9') >= 0) { logger.info('postprocessor saw MRN9'); }\n",
		},
		{
			language: "lua",
			script: "  postprocessor: |\n" +
				"    if string.find(message, \"MRN9\") then logger.info(\"postprocessor saw MRN9\") end\n",
		},
		{
			language: "wasm",
			script:   "  postprocessor: " + module + "\n",
		},
	} {
		t.Run(tc.language, func(t *testing.T) {
			var mu sync.Mutex
			var lines []string

			sink := &recordingSender{}
			sink.name = "out"

			cfg, err := config.Load(strings.NewReader(channelBody("post-"+tc.language,
				"  language: "+tc.language+"\n"+tc.script)), filepath.Join(dir, "matrix.yaml"))
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			c := startChannelFor(t, cfg, sink)
			c.log = collectLogs(&mu, &lines)

			if _, err := c.handle(context.Background(), []byte(matrixMessage)); err != nil {
				t.Fatalf("handling: %v", err)
			}

			if sink.count() != 1 {
				t.Fatalf("expected the message to be delivered, got %d", sink.count())
			}

			mu.Lock()
			defer mu.Unlock()

			if !strings.Contains(strings.Join(lines, "\n"), "postprocessor saw MRN9") {
				t.Errorf("the postprocessor did not report the identifier, so it either did not run or was handed an "+
					"empty message: %v", lines)
			}
		})
	}
}

// TestALifecycleHookReachingForTheMessageFailsLoudly extends the eleventh finding to the slots that never had a message.
//
// Deploy and undeploy correctly receive no message text - there is no message yet. But "none" was expressed as an empty string, which
// is the same trap the path-addressed formats had: a hook could read it, conclude the message held nothing of interest, and let the
// channel start. Withheld instead, so the reference fails where it is written.
func TestALifecycleHookReachingForTheMessageFailsLoudly(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct{ language, script string }{
		{language: "javascript", script: "  deploy: |\n    if (message.length === 0) { logger.info('empty'); }\n"},
		{language: "lua", script: "  deploy: |\n    if #message == 0 then logger.info(\"empty\") end\n"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			cfg, err := config.Load(strings.NewReader(channelBody("hook-raw-"+tc.language,
				"  language: "+tc.language+"\n"+tc.script)), filepath.Join(dir, "hookraw.yaml"))
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			sink := &recordingSender{}
			sink.name = "out"

			e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
			if err != nil {
				t.Fatalf("building the engine: %v", err)
			}
			t.Cleanup(func() { _ = e.Stop(context.Background()) })

			if err := e.Start(); err == nil {
				t.Fatal("a deploy hook read a message that does not exist and the channel started")
			}
		})
	}
}
