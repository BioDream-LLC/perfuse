package engine

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testOrders = "UNA:+.? '" +
	"UNB+UNOC:3+5412345000013:14+4012345000009:14+261017:1200+REF0001'" +
	"UNH+1+ORDERS:D:96A:UN'BGM+220+PO1+9'NAD+BY+5412345000013::9'UNT+4+1'" +
	"UNH+2+ORDERS:D:96A:UN'BGM+220+PO2+9'NAD+BY+9999999999999::9'UNT+4+2'" +
	"UNH+3+INVOIC:D:96A:UN'BGM+380+INV1+9'UNT+3+3'" +
	"UNZ+3+REF0001'"

// An EDIFACT channel over HTTP: split into one interchange per message, filtered by element paths, answered with a CONTRL;
// an interchange whose envelope disagrees with its contents is refused with a rejecting CONTRL.
func TestAnEDIFACTChannel(t *testing.T) {
	dir := t.TempDir()
	orders, all := filepath.Join(dir, "orders"), filepath.Join(dir, "all")
	addr := freeAddr(t)
	startYAML(t, dir, "edi", `name: edi
dataType: edifact
edifact:
  acknowledge: contrl
filter: 'UNB-2.1 == "5412345000013"'
source:
  type: http
  http:
    listen: "`+addr+`"
    path: /edi
destinations:
  - name: orders
    type: file
    dir: `+orders+`
    filter: 'UNH-2.1 == "ORDERS"'
  - name: all
    type: file
    dir: `+all+`
`)
	post := func(body string) (int, string) {
		res, err := http.Post("http://"+addr+"/edi", "application/EDIFACT", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := post(testOrders)
	if code != http.StatusOK || !strings.Contains(body, "UCI+REF0001+5412345000013:14+4012345000009:14+7'") {
		t.Fatalf("accepted: %d %s", code, body)
	}
	// Not split, so both destinations get the whole interchange; the ORDERS filter matches because it holds orders.
	for _, d := range []string{orders, all} {
		files, _ := filepath.Glob(filepath.Join(d, "*"))
		if len(files) != 1 {
			t.Fatalf("%s: %v", d, files)
		}
		b, _ := os.ReadFile(files[0])
		if string(b) != testOrders {
			t.Errorf("%s: not the sender's bytes: %q", d, b)
		}
		if !strings.HasSuffix(files[0], ".edi") || !strings.Contains(filepath.Base(files[0]), "ORDERS_INVOIC") {
			t.Errorf("file name %s", files[0])
		}
	}
	// A cut-short interchange: refused, answered with a rejecting CONTRL.
	code, body = post(strings.Replace(testOrders, "UNZ+3+REF0001'", "UNZ+4+REF0001'", 1))
	if code != http.StatusBadRequest || !strings.Contains(body, "+4'") {
		t.Errorf("bad envelope: %d %s", code, body)
	}
	// From another sender: excluded by the channel filter, still acknowledged as read.
	code, _ = post(strings.ReplaceAll(testOrders, "5412345000013:14+4012345000009", "1111111111111:14+4012345000009"))
	if code != http.StatusOK {
		t.Errorf("filtered: %d", code)
	}
}

func TestAnEDIFACTChannelSplits(t *testing.T) {
	dir := t.TempDir()
	orders := filepath.Join(dir, "orders")
	addr := freeAddr(t)
	startYAML(t, dir, "split", `name: split
dataType: edifact
edifact:
  split: true
source:
  type: http
  http:
    listen: "`+addr+`"
    path: /edi
destinations:
  - name: orders
    type: file
    dir: `+orders+`
    file_name: "${timestamp}-${control_id}"
    filter: 'UNH-2.1 == "ORDERS" and NAD-2.1 == "5412345000013"'
`)
	res, err := http.Post("http://"+addr+"/edi", "application/EDIFACT", strings.NewReader(testOrders))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode >= 300 {
		t.Fatalf("status %d", res.StatusCode)
	}
	files, _ := filepath.Glob(filepath.Join(orders, "*"))
	if len(files) != 1 {
		t.Fatalf("one order from this buyer: %v", files)
	}
	if !strings.Contains(filepath.Base(files[0]), "-REF0001") {
		t.Errorf("file name %s does not carry the interchange reference", files[0])
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "BGM+220+PO1+9'") || strings.Contains(string(b), "PO2") || !strings.Contains(string(b), "UNZ+1+REF0001'") {
		t.Errorf("split piece: %q", b)
	}
}
