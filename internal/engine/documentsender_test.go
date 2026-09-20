package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The document destination produces something a person prints, signs and files. That changes what counts as a
// bug: a document that is merely wrong gets noticed, and one with a silent gap in it does not.

func docChannel(t *testing.T, doc *config.DocumentDestination) *Channel {
	t.Helper()

	cfg := &config.Channel{
		Name:   "reports",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		// One attempt, for the same reason as in response_test.go: a test that expects a refusal should not sit through the
		// production retry schedule to see it. The default is five attempts with a doubling backoff, which is fifteen seconds of
		// waiting for an answer the test already has.
		Destinations: []config.Destination{{
			Name: "print", Type: config.DestinationDocument, Document: doc,
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

const resultMessage = "MSH|^~\\&|LAB|SITEA|EPIC|SITEB|20260820120000||ORU^R01^ORU_R01|R1|P|2.5.1\r" +
	"PID|1||MRN99^^^SITEA^MR||FROST^IVY^ANNE||19910228|F\r" +
	"OBR|1||ACC1|GLU^Glucose\r" +
	"OBX|1|NM|GLU^Glucose||10.2|mmol/L|3.5-5.5|H\r"

func TestADocumentIsWrittenAsAPDF(t *testing.T) {
	dir := t.TempDir()

	ch := docChannel(t, &config.DocumentDestination{
		Dir:      dir,
		Template: "Patient: ${PID-5.1}, ${PID-5.2}\nResult:  ${OBX-5} ${OBX-6}\nFlag:    ${OBX-8}\n",
		Title:    "Result for ${PID-5.1}",
	})

	if _, err := ch.HandleForTest(context.Background(), []byte(resultMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("outcome = %q, want delivered", got)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1: %v", len(files), names(files))
	}
	if !strings.HasSuffix(files[0].Name(), ".pdf") {
		t.Errorf("file = %q, want a .pdf extension", files[0].Name())
	}

	body, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "%PDF") {
		t.Error("the file is not a PDF")
	}
	// The substituted values must actually be in it.
	if !strings.Contains(string(body), "FROST") {
		t.Error("the patient name was not substituted into the document")
	}
	if !strings.Contains(string(body), "10.2") {
		t.Error("the result was not substituted into the document")
	}
}

func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	// A print watcher picking up a half-written PDF produces a page of nothing, and nobody investigates a blank
	// page.
	dir := t.TempDir()

	ch := docChannel(t, &config.DocumentDestination{
		Dir: dir, Template: "Patient: ${PID-5.1}\n",
	})

	if _, err := ch.HandleForTest(context.Background(), []byte(resultMessage)); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f.Name(), ".part") {
			t.Errorf("a temporary file was left behind: %s", f.Name())
		}
	}
}

func TestAMissingFieldRefusesRatherThanLeavingAGap(t *testing.T) {
	// The decision that matters most here. A discharge summary with a blank where the patient's name should be is
	// a document somebody will print, sign and file, and nothing about it says it is incomplete.
	dir := t.TempDir()

	ch := docChannel(t, &config.DocumentDestination{
		Dir:      dir,
		Template: "Patient: ${PID-5.1}\nNHS number: ${PID-19}\n",
	})

	if _, err := ch.HandleForTest(context.Background(), []byte(resultMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got == Delivered {
		t.Fatal("a document with a missing field was delivered")
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Errorf("a document was written despite the missing field: %v", names(files))
	}
}

func TestTextFormatWritesText(t *testing.T) {
	// A site whose next step is a script or a printer queue does not want a PDF, and offering only PDF would make
	// them produce one and then extract the text back out.
	dir := t.TempDir()

	ch := docChannel(t, &config.DocumentDestination{
		Dir:      dir,
		Format:   config.DocumentText,
		Template: "Patient: ${PID-5.1}\n",
	})

	if _, err := ch.HandleForTest(context.Background(), []byte(resultMessage)); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	// The extension follows the format rather than the template, so a template written for PDF and switched to
	// text does not produce a .pdf full of plain text - which opens in a reader as a corrupt document.
	if !strings.HasSuffix(files[0].Name(), ".txt") {
		t.Errorf("file = %q, want a .txt extension", files[0].Name())
	}

	body, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if string(body) != "Patient: FROST\n" {
		t.Errorf("body = %q", body)
	}
}

func TestAnHTMLTemplateIsRefusedAtLoad(t *testing.T) {
	// Somebody migrating a Mirth Document Writer will bring an HTML template. Finding out that the tags were
	// printed literally after the first ward complains is much worse than being told now.
	cfg := &config.Channel{
		Name:   "reports",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []config.Destination{{
			Name: "print", Type: config.DestinationDocument,
			Document: &config.DocumentDestination{
				Dir:      t.TempDir(),
				Template: "<html><body><p>Patient: ${PID-5.1}</p></body></html>",
			},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("an HTML template was accepted by a destination that renders text")
	}
	if !strings.Contains(err.Error(), "printed literally") {
		t.Errorf("the error does not say what would happen: %v", err)
	}
}

func TestAResultRangeWithAnAngleBracketStillLoads(t *testing.T) {
	// The HTML check must not be so eager that it refuses a real report. "<10 mmol" is ordinary clinical text.
	cfg := &config.Channel{
		Name:   "reports",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []config.Destination{{
			Name: "print", Type: config.DestinationDocument,
			Document: &config.DocumentDestination{
				Dir:      t.TempDir(),
				Template: "Reference: <10 mmol/L\nResult: ${OBX-5}\n",
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a template containing an angle bracket in clinical text was refused: %v", err)
	}
}

func TestAMissingTemplateIsRefused(t *testing.T) {
	// A document with no template is either the raw message, which nobody wants printed, or a blank page
	// delivered to a ward.
	cfg := &config.Channel{
		Name:   "reports",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []config.Destination{{
			Name: "print", Type: config.DestinationDocument,
			Document: &config.DocumentDestination{Dir: t.TempDir()},
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a document destination with no template was accepted")
	}
	if !strings.Contains(err.Error(), "blank page") {
		t.Errorf("the error does not say what would happen: %v", err)
	}
}

func TestNowAndTodayAreAvailableInATemplate(t *testing.T) {
	// A printed report almost always needs the time it was produced, and requiring a transformation step to put
	// it into the message first would be a strange hoop.
	dir := t.TempDir()

	ch := docChannel(t, &config.DocumentDestination{
		Dir: dir, Format: config.DocumentText,
		Template: "Printed: ${today}\nPatient: ${PID-5.1}\n",
	})

	if _, err := ch.HandleForTest(context.Background(), []byte(resultMessage)); err != nil {
		t.Fatal(err)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	body, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if strings.Contains(string(body), "${today}") {
		t.Errorf("today was not substituted: %q", body)
	}
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
