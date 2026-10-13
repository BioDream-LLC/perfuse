package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

const fileOptionsMsg = "MSH|^~\\&|APP|FAC|RCV|RFAC|20260101120000||ADT^A01|CTRL42|P|2.5\rPID|1||123||Doe^Jane\r"

func TestAFileNameTemplateWritesOneFilePerMessageAndNeverAPartOne(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSender(config.Destination{Name: "archive", Type: config.DestinationFile, Dir: dir,
		FileName: "${date}-${message_type}-${control_id}"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err := s.Send(context.Background(), []byte(fileOptionsMsg)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("want 2 files (the same control ID twice must not overwrite), got %d", len(entries))
	}
	day := time.Now().UTC().Format("20060102")
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), day+"-ADT_A01-CTRL42") || !strings.HasSuffix(e.Name(), ".hl7") {
			t.Errorf("file %q does not follow the template", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestAppendWritesEveryMessageIntoTheNamedFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSender(config.Destination{Name: "a", Type: config.DestinationFile, Dir: dir,
		FileName: "${date}.hl7", Append: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if err := s.Send(context.Background(), []byte(fileOptionsMsg)); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, time.Now().UTC().Format("20060102")+".hl7"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "MSH|"); n != 3 {
		t.Errorf("want 3 messages in the file, got %d", n)
	}
}

func TestRetainHoursDeletesOldFilesAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "20200101-ADT_A01.hl7")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	sub := filepath.Join(dir, "keep-me")
	_ = os.Mkdir(sub, 0o700)
	_ = os.Chtimes(sub, past, past)

	s, err := NewFileSender(config.Destination{Name: "a", Type: config.DestinationFile, Dir: dir, RetainHours: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(context.Background(), []byte(fileOptionsMsg)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a file older than retain_hours was kept")
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("a folder was deleted: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("want today's file and the folder, got %d entries", len(entries))
	}
}

func TestFileDestinationOptionsLoadAndAreChecked(t *testing.T) {
	good := "name: c\nsource:\n  type: mllp\n  listen: \"127.0.0.1:0\"\ndestinations:\n  - name: a\n    type: file\n    dir: ./x\n" +
		"    file_name: ${date}.hl7\n    append: true\n    retain_hours: 24\n    temp_suffix: .tmp\n"
	if _, err := config.Load(strings.NewReader(good), "c.yaml"); err != nil {
		t.Fatalf("file destination options refused: %v", err)
	}
	bad := strings.Replace(good, "    file_name: ${date}.hl7\n", "", 1)
	if _, err := config.Load(strings.NewReader(bad), "c.yaml"); err == nil || !strings.Contains(err.Error(), "append needs file_name") {
		t.Errorf("append without file_name: %v", err)
	}
}
