package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/biodream-llc/perfuse/internal/queue"
	"github.com/biodream-llc/perfuse/internal/store"
)

// The queue endpoints.
//
// These exist because the alternative is what other engines force on an operator:
// stopping and restarting a channel to shift one stuck message, which also
// interrupts everything that was working. Every action here is scoped to the
// message or the destination that has the problem.

// handleQueue lists queued items, with the per-destination summary alongside.
//
// Both in one response on purpose. A list of forty pending messages does not tell
// you whether the queue is draining; the age of the oldest one does, and making
// somebody load two pages to find out invites them not to.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented,
			"no durable queue is configured on this server")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	items, total, err := q.List(r.Context(), queue.Query{
		Channel:     r.URL.Query().Get("channel"),
		Destination: r.URL.Query().Get("destination"),
		State:       queue.State(r.URL.Query().Get("state")),
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	depth, err := q.Depth(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	now := time.Now().UTC()
	type row struct {
		queue.Item
		AgeSeconds float64 `json:"ageSeconds"`
		DueIn      float64 `json:"dueInSeconds"`
	}
	out := make([]row, 0, len(items))
	for _, it := range items {
		out = append(out, row{
			Item:       it,
			AgeSeconds: it.Age(now).Seconds(),
			// Negative means overdue, which is the normal state for something the
			// worker is about to pick up.
			DueIn: it.NextAttempt.Sub(now).Seconds(),
		})
	}

	// An empty depth list must marshal as [] rather than null.
	//
	// A nil Go slice becomes JSON null, and the browser then does `for (const d of null)` and throws - which blanks the
	// whole dashboard rather than showing an empty queue. The TypeScript type says QueueSnapshot.destinations is an
	// array, so nothing on that side is wrong; the server is what breaks its own declared contract. Same rule as the
	// bulk export manifest, where output and error are always arrays.
	if depth == nil {
		depth = []queue.DepthRow{}
	}

	s.ok(w, map[string]any{
		"items":        out,
		"total":        total,
		"destinations": depth,
		"at":           now,
	})
}

// handleQueueItem returns the stored bytes of one queued message, so somebody can
// see what is actually stuck rather than guessing from its control id.
func (s *Server) handleQueueItem(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented, "no durable queue is configured")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the queue item id must be a number")
		return
	}

	raw, err := q.Payload(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, err.Error())
		return
	}

	s.ok(w, map[string]any{
		"id":      id,
		"raw":     string(raw),
		"size":    len(raw),
		"message": normaliseMessage(string(raw)),
	})
}

// queueAction is the body for every operation below. One shape rather than four,
// because they differ only in what they do.
type queueAction struct {
	// ID targets one message. Zero means the whole destination, which then has to
	// be named — a bulk action with neither is refused rather than interpreted.
	ID          int64  `json:"id"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
}

func (s *Server) decodeQueueAction(w http.ResponseWriter, r *http.Request) (*queueAction, bool) {
	var body queueAction
	if !s.decode(w, r, &body) {
		return nil, false
	}
	if body.ID == 0 && (body.Channel == "" || body.Destination == "") {
		s.fail(w, r, http.StatusBadRequest,
			"name either an id, or both a channel and a destination")
		return nil, false
	}
	return &body, true
}

// handleQueueRetry brings forward the next attempt.
//
// This is the operation that replaces restarting a channel. It resets the attempt
// counter, because an operator retrying by hand has usually just fixed something,
// and counting the failures from before the fix against the new budget would
// abandon the message immediately.
func (s *Server) handleQueueRetry(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented, "no durable queue is configured")
		return
	}
	body, ok := s.decodeQueueAction(w, r)
	if !ok {
		return
	}

	n, err := q.RetryNow(r.Context(), body.ID, body.Channel, body.Destination)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.auditQueue(r, sess, "queue.retry", queueTarget(body),
		fmt.Sprintf("%d message(s) rescheduled", n))

	// Wake the workers so a manual retry happens now rather than at the next poll.
	// Somebody who has just fixed a receiver and pressed retry should see it move.
	if rt := s.optionalRuntime(sess); rt != nil && rt.Queues != nil {
		rt.Queues.WakeAll()
	}

	s.ok(w, map[string]any{"retried": n})
}

// handleQueueSkip abandons a message without delivering it.
func (s *Server) handleQueueSkip(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented, "no durable queue is configured")
		return
	}
	body, ok := s.decodeQueueAction(w, r)
	if !ok {
		return
	}
	if body.ID == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"skip takes one message id; use drain to abandon a whole destination")
		return
	}

	n, err := q.Skip(r.Context(), body.ID, sess.Username)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, http.StatusNotFound,
			"that message is not in the queue, or has already been delivered")
		return
	}

	// Recorded against the person, not the system. Skipped and failed are separate
	// states precisely so this stays answerable later.
	s.auditQueue(r, sess, "queue.skip", queueTarget(body), "abandoned by hand")
	s.ok(w, map[string]any{"skipped": n})
}

// handleQueueDrain abandons everything waiting for a destination.
//
// Used when a receiver has been down long enough that the backlog is no longer
// clinically useful: replaying a week of stale admissions at once can do more harm
// than never sending them.
func (s *Server) handleQueueDrain(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented, "no durable queue is configured")
		return
	}
	body, ok := s.decodeQueueAction(w, r)
	if !ok {
		return
	}
	if body.Channel == "" || body.Destination == "" {
		s.fail(w, r, http.StatusBadRequest,
			"draining needs both a channel and a destination named explicitly")
		return
	}

	n, err := q.Drain(r.Context(), body.Channel, body.Destination, sess.Username)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	s.auditQueue(r, sess, "queue.drain", queueTarget(body),
		fmt.Sprintf("%d message(s) abandoned", n))
	s.ok(w, map[string]any{"drained": n})
}

// handleQueueRemove deletes a finished item.
func (s *Server) handleQueueRemove(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := s.queueStoreFor(sess)
	if q == nil {
		s.fail(w, r, http.StatusNotImplemented, "no durable queue is configured")
		return
	}
	body, ok := s.decodeQueueAction(w, r)
	if !ok {
		return
	}
	if body.ID == 0 {
		s.fail(w, r, http.StatusBadRequest, "remove takes one message id")
		return
	}

	n, err := q.Remove(r.Context(), body.ID)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if n == 0 {
		// A pending message cannot be deleted. It has to be skipped first, which
		// records that somebody chose to abandon it rather than losing it quietly.
		s.fail(w, r, http.StatusConflict,
			"that message is still waiting to be delivered. Skip it first, so there "+
				"is a record that it was abandoned deliberately")
		return
	}

	s.auditQueue(r, sess, "queue.remove", queueTarget(body), "deleted from the queue")
	s.ok(w, map[string]any{"removed": n})
}

// auditQueue records who did what to the queue.
//
// Every action here either abandons a clinical message or changes when one is
// sent, so none of them should be answerable only from a log file that rotates.
func (s *Server) auditQueue(r *http.Request, sess *store.Session, action, target, detail string) {
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   action,
		Target:   target,
		Detail:   detail,
		IP:       clientIP(r),
	})
}

// instanceQueueStore returns this process's queue regardless of tenant.
//
// Only for the fleet self report, which describes the server rather than a caller. Named so that using it in a request
// handler looks wrong, because there it would be.
func (s *Server) instanceQueueStore() *queue.Store {
	if s.Runtime == nil || s.Runtime.Queues == nil {
		return nil
	}
	return s.Runtime.Queues.Store()
}

// queueStore returns the queue for a tenant.
//
// Takes a session rather than reading the server's runtime, because a queue holds undelivered messages and those are
// patient data. Skip, drain and remove all act through this, so an unscoped version would let one tenant discard
// another's traffic.
func (s *Server) queueStoreFor(sess *store.Session) *queue.Store {
	rt := s.optionalRuntime(sess)
	if rt == nil || rt.Queues == nil {
		return nil
	}
	return rt.Queues.Store()
}

func queueTarget(a *queueAction) string {
	if a.ID != 0 {
		return "queue:" + strconv.FormatInt(a.ID, 10)
	}
	return a.Channel + "/" + a.Destination
}
