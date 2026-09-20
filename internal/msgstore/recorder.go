package msgstore

import (
	"context"
	"log/slog"
	"time"

	"github.com/biodream-llc/perfuse/internal/attach"
	"github.com/biodream-llc/perfuse/internal/engine"
)

// Recorder adapts the store to the engine's Recorder interface.
//
// The engine reports what happened and does not know about storage. This is the
// piece that decides to keep it, which is why a deployment that must not hold
// clinical content simply does not install one.
type Recorder struct {
	Store *Store
	Log   *slog.Logger
}

// NewRecorder builds a recorder.
func NewRecorder(store *Store, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{Store: store, Log: log}
}

// RecordMessage stores one message record.
//
// A storage failure is logged and swallowed on purpose. By the time this is
// called the message has already been delivered and acknowledged; failing here
// would either lose the acknowledgement or make the sender resend something that
// arrived. The log is the right place for it, and it is logged at error level so
// it is not invisible.
func (r *Recorder) RecordMessage(ctx context.Context, record engine.MessageRecord) {
	m := &Message{
		Channel:      record.Channel,
		ReceivedAt:   record.ReceivedAt,
		ControlID:    record.ControlID,
		MessageType:  record.MessageType,
		TriggerEvent: record.TriggerEvent,
		Sender:       record.Sender,
		Outcome:      Outcome(record.Outcome),
		AckCode:      record.AckCode,
		Size:         len(record.Raw),
		Segments:     record.Segments,
		DurationMS:   record.Duration.Milliseconds(),
		Error:        record.Error,
		Raw:          record.Raw,
	}

	for _, d := range record.Deliveries {
		m.Deliveries = append(m.Deliveries, Delivery{
			Destination: d.Destination,
			Status:      DeliveryStatus(d.Status),
			Attempts:    d.Attempts,
			DurationMS:  d.Duration.Milliseconds(),
			Error:       d.Error,
		})
	}

	// A short timeout of its own, so a slow database cannot hold a connection
	// goroutine open indefinitely after the work is already done.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	// Payloads first. If they fail, the message is still stored with its tokens - a message with an unresolvable
	// attachment is worth having, because it records that something arrived and what happened to it, and the
	// alternative is losing the message entirely over a payload.
	for _, a := range record.Attachments {
		if err := r.Store.PutAttachment(writeCtx, a); err != nil {
			r.Log.Error("could not store an attachment",
				"err", err, "channel", record.Channel, "digest", a.Digest.Short(), "bytes", a.Size)
		}
	}

	id, err := r.Store.Record(writeCtx, m)
	if err != nil {
		r.Log.Error("could not store a message record",
			"err", err, "channel", record.Channel,
			"control_id", record.ControlID, "outcome", string(record.Outcome))
		return
	}

	// Linked after the message exists, since the link needs its id. A failure here leaves the payload looking
	// orphaned, so the sweep would eventually reclaim it while the message still refers to it by token - which is
	// worth logging loudly, because the symptom appears days later as a message that cannot be reassembled.
	if len(record.Attachments) > 0 {
		digests := make([]attach.Digest, 0, len(record.Attachments))
		for _, a := range record.Attachments {
			digests = append(digests, a.Digest)
		}
		if err := r.Store.LinkAttachments(writeCtx, id, digests); err != nil {
			r.Log.Error("could not link a message to its attachments; the payloads may be swept while the "+
				"message still refers to them",
				"err", err, "channel", record.Channel, "message_id", id)
		}
	}
}
