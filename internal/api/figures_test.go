package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/dashboards"
	"github.com/biodream-llc/perfuse/internal/msgstore"
)

// Every tile the dashboards draw from figures has a figure here.
func TestEveryFiguresTileIsComputed(t *testing.T) {
	have := map[string]bool{}
	for _, id := range FigureTiles() {
		have[id] = true
	}
	for _, tile := range dashboards.Tiles {
		if tile.Source == "figures" && !have[tile.ID] {
			t.Errorf("tile %s has no figure", tile.ID)
		}
	}
}

func figuresHarness(t *testing.T) (*harness, *msgstore.Store) {
	t.Helper()
	h := newHarness(t)
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	messages, err := msgstore.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	messages.StorePayloads = true
	h.server.Runtime = NewRuntime(h.server.Channels, messages, nil)
	h.handler = h.server.Handler()
	return h, messages
}

func record(t *testing.T, s *msgstore.Store, channel, typ string, outcome msgstore.Outcome, ms int64, raw string, failedDest bool) {
	t.Helper()
	m := &msgstore.Message{Channel: channel, MessageType: typ, Outcome: outcome, DurationMS: ms, Raw: []byte(raw), ReceivedAt: time.Now().UTC()}
	status := msgstore.DeliveryDelivered
	if failedDest {
		status = msgstore.DeliveryFailed
	}
	m.Deliveries = []msgstore.Delivery{{Destination: "ehr", Status: status}}
	if _, err := s.Record(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func figures(t *testing.T, h *harness, tiles ...string) map[string]Figure {
	t.Helper()
	rec := h.do("viewer", http.MethodGet, "/api/dashboards/figures?tiles="+strings.Join(tiles, ","), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct{ Figures map[string]Figure }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Figures
}

func rowsText(f Figure) string {
	var b strings.Builder
	for _, r := range f.Rows {
		b.WriteString(r.Label + "=" + r.Value + "[" + r.Tone + "];")
	}
	return b.String()
}

const (
	oruCritical = "MSH|^~\\&|LAB|H|EHR|H|20261008120000||ORU^R01|1|P|2.5.1\rPID|1||123\rOBR|1|||K^Potassium||||||||||||||||||||CH\rOBX|1|NM|2823-3^Potassium^LN||7.1|mmol/L|3.5-5.1|HH|||F\r"
	oruNormal   = "MSH|^~\\&|LAB|H|EHR|H|20261008120000||ORU^R01|2|P|2.5.1\rPID|1||123\rOBR|1|||K^Potassium||||||||||||||||||||CH\rOBX|1|NM|2823-3^Potassium^LN||4.1|mmol/L|3.5-5.1|N|||F\r"
	oruImaging  = "MSH|^~\\&|RIS|H|EHR|H|20261008120000||ORU^R01|3|P|2.5.1\rPID|1||123\rOBR|1|||71045^Chest X-ray||||||||||||||||||||RAD\rOBX|1|TX|71045||No acute findings.||||||F\r"
)

func TestTheLabAndImagingFiguresComeFromTheMessagesStored(t *testing.T) {
	h, s := figuresHarness(t)
	record(t, s, "lab-out", "ORU", msgstore.Failed, 900, oruCritical, true)
	record(t, s, "lab-out", "ORU", msgstore.Delivered, 200, oruNormal, false)
	record(t, s, "lab-out", "ORU", msgstore.Delivered, 400, oruNormal, false)
	record(t, s, "ris-out", "ORU", msgstore.Partial, 100, oruImaging, true)

	f := figures(t, h, "lab-critical", "lab-delivery-time", "reports-undelivered")
	if got := rowsText(f["lab-critical"]); got != "lab-out=1 critical results not delivered[bad];" || !strings.Contains(f["lab-critical"].Note, "1 results") {
		t.Errorf("critical: %s %q", got, f["lab-critical"].Note)
	}
	if got := rowsText(f["lab-delivery-time"]); !strings.Contains(got, "lab-out=median 200 ms, 95% within 400 ms (2)") {
		t.Errorf("delivery time: %s", got)
	}
	if got := rowsText(f["reports-undelivered"]); got != "ris-out=1 not delivered[bad];" {
		t.Errorf("imaging reports: %s", got)
	}
}

func TestTheX12FiguresReadTheAcknowledgementsAndRemittances(t *testing.T) {
	h, s := figuresHarness(t)
	isa := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*CLINIC         *261008*1200*^*00501*000000001*0*T*:~"
	ack := isa + "GS*FA*PAYER*CLINIC*20261008*1200*1*X*005010X231A1~ST*999*0001*005010X231A1~AK1*HC*1*005010X222A1~" +
		"AK2*837*0001~IK5*A~AK2*837*0002~IK3*NM1*12**8~IK4*9**7*X~IK5*R*5~AK9*P*2*2*1~SE*9*0001~GE*1*1~IEA*1*000000001~"
	record(t, s, "x12-in", "999", msgstore.Delivered, 10, ack, false)
	claim := isa + "GS*HC*CLINIC*PAYER*20261008*1200*2*X*005010X222A1~ST*837*0001*005010X222A1~BHT*0019*00*1*20261008*1200*CH~" +
		"NM1*41*2*CLINIC*****46*123~NM1*40*2*PAYER*****46*P~HL*1**20*1~NM1*85*2*CLINIC*****XX*1234567893~HL*2*1*22*0~SBR*P*18*******CI~" +
		"NM1*IL*1*DOE*JANE****MI*M1~NM1*PR*2*PAYER*****PI*P~CLM*ACCT-1*100***11:B:1~HI*ABK:J029~LX*1~SV1*HC:99213*100*UN*1***1~" +
		"SE*15*0001~GE*1*2~IEA*1*000000001~"
	record(t, s, "x12-out", "837", msgstore.Delivered, 10, claim, false)
	era := isa + "GS*HP*PAYER*CLINIC*20261008*1200*3*X*005010X221A1~ST*835*0001~BPR*I*80*C*ACH~TRN*1*EFT1*1P~N1*PR*PAYER~N1*PE*CLINIC*XX*1234567893~" +
		"LX*1~CLP*ACCT-1*1*100*80**12*PCN1~CLP*ACCT-9*4*50*0**12*PCN2~SE*9*0001~GE*1*3~IEA*1*000000001~"
	record(t, s, "x12-in", "835", msgstore.Delivered, 10, era, false)

	f := figures(t, h, "x12-999", "x12-835-match", "x12-rejection-reasons")
	if got := rowsText(f["x12-999"]); !strings.Contains(got, "transaction sets acknowledged=2") || !strings.Contains(got, "rejected=1 (50%)[bad]") {
		t.Errorf("999: %s", got)
	}
	if got := rowsText(f["x12-835-match"]); !strings.Contains(got, "matched to a claim sent (837 CLM01)=1 (50%)[bad]") {
		t.Errorf("835 match: %s", got)
	}
	if got := rowsText(f["x12-rejection-reasons"]); !strings.Contains(got, "999 NM1 segment error 8=1") || !strings.Contains(got, "999 element error 7=1") {
		t.Errorf("reasons: %s", got)
	}
}

func TestTheDICOMFiguresUseTheDICOMChannelsOnly(t *testing.T) {
	h, s := figuresHarness(t)
	if err := os.WriteFile(filepath.Join(h.dir, "pacs-in.yaml"), []byte("name: pacs-in\nsource:\n  type: dicom\n  dicom:\n    listen: \"127.0.0.1:11112\"\n    ae_title: PERFUSE\ndestinations:\n  - name: archive\n    type: file\n    dir: ./archive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record(t, s, "pacs-in", "CT", msgstore.Delivered, 1200, "", false)
	record(t, s, "pacs-in", "CT", msgstore.Failed, 300, "", true)
	record(t, s, "pacs-in", "MR", msgstore.Delivered, 2400, "", false)
	record(t, s, "adt-in", "ADT", msgstore.Failed, 10, "", true)
	if _, broken, _ := h.server.Channels.List(); len(broken) > 0 {
		t.Fatal(broken)
	}
	f := figures(t, h, "dicom-modality", "dicom-routing")
	if got := rowsText(f["dicom-modality"]); got != "CT=2 stored, 1 failed (50%)[bad];MR=1 stored, 0 failed (0%)[];" {
		t.Errorf("modality: %s (%s)", got, f["dicom-modality"].Empty)
	}
	if got := rowsText(f["dicom-routing"]); !strings.Contains(got, "pacs-in=median 1.2 s, 95% within 2.4 s (2)") {
		t.Errorf("routing: %s", got)
	}
}

func TestThePrivacyFiguresCountReadsAndTokens(t *testing.T) {
	h, s := figuresHarness(t)
	record(t, s, "adt-in", "ADT", msgstore.Delivered, 10, "MSH|^~\\&|A|B|C|D|20261008||ADT^A01|9|P|2.5\r", false)
	rec := h.do("viewer", http.MethodGet, "/api/messages/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	h.do("admin", http.MethodPost, "/api/tokens", map[string]any{"label": "fleet-watch", "role": "viewer"})
	f := figures(t, h, "privacy-patient-access", "token-use")
	if got := rowsText(f["privacy-patient-access"]); !strings.Contains(got, "1 messages opened") {
		t.Errorf("access: %s", got)
	}
	if got := rowsText(f["token-use"]); !strings.Contains(got, "fleet-watch (viewer)=never used[warn]") {
		t.Errorf("tokens: %s", got)
	}
}
