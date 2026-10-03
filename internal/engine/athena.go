package engine

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
)

// AthenaRecord is one archived message as Amazon Athena reads it: the fields people filter on, and the message itself.
//
// One JSON object per S3 object, under ${channel}/dt=YYYY-MM-DD/, which is the layout `perfuse athena` writes a table for, with
// partition projection so no crawler or MSCK REPAIR is needed. The message is kept whole, so nothing about the archive depends on having
// chosen the right columns in advance.
type AthenaRecord struct {
	ReceivedAt        string `json:"received_at"`
	Channel           string `json:"channel"`
	MessageType       string `json:"message_type,omitempty"`
	TriggerEvent      string `json:"trigger_event,omitempty"`
	ControlID         string `json:"control_id,omitempty"`
	SendingApp        string `json:"sending_application,omitempty"`
	SendingFacility   string `json:"sending_facility,omitempty"`
	ReceivingFacility string `json:"receiving_facility,omitempty"`
	PatientID         string `json:"patient_id,omitempty"`
	MessageTime       string `json:"message_time,omitempty"`
	Message           string `json:"message"`
}

func athenaRecord(raw []byte, channel string, now time.Time) ([]byte, error) {
	r := AthenaRecord{ReceivedAt: now.Format(time.RFC3339), Channel: channel, Message: string(raw)}
	if m, err := hl7.Parse(raw); err == nil {
		get := func(p string) string { return strings.TrimSpace(m.MustGet(p)) }
		r.MessageType = get("MSH-9.1")
		r.TriggerEvent = get("MSH-9.2")
		r.ControlID = m.ControlID()
		r.SendingApp = get("MSH-3.1")
		r.SendingFacility = get("MSH-4.1")
		r.ReceivingFacility = get("MSH-6.1")
		r.PatientID = get("PID-3.1")
		r.MessageTime = get("MSH-7")
	}
	out, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
