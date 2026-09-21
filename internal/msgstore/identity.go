// Finding a stored message by what somebody knows about it.
//
// The search a person actually needs is "here is a number off a phone call, show me every
// message about it". Before this that meant either a substring match over payloads, which is
// grep and slow, or a filter expression, which requires knowing that identity lives at PID-3
// in HL7 v2 and somewhere else entirely in the other three formats on the same server.
//
// Identity is extracted once when the message is recorded and indexed, so this is a lookup
// rather than a scan. The cost is paid on the recording path, once per message, instead of on
// every search.
package msgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
	"github.com/biodream-llc/perfuse/internal/identity"
)

// maxIdentityValues bounds how many identifiers are stored for one message.
//
// A merge message names two patients and a batch can name many, which is legitimate. A
// payload built to be expensive can name thousands, and the index is written on the delivery
// path, so the bound is what stops a message from choosing how long recording takes.
const maxIdentityValues = 64

// recordIdentity extracts identifiers and stores them for one message.
//
// Failures are logged by the caller's logger and never returned. The message has already been
// accepted and delivered by the time this runs; failing to index it is a degraded search, not
// a lost message, and treating it as fatal would turn an unparseable payload into a delivery
// failure.
func (s *Store) recordIdentity(ctx context.Context, messageID int64, tenantID string, raw []byte) {
	if len(raw) == 0 {
		return
	}

	id := identity.Extract(raw)
	if len(id.Values) == 0 {
		return
	}

	vals := id.Values
	if len(vals) > maxIdentityValues {
		vals = vals[:maxIdentityValues]
	}

	for _, v := range vals {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO message_identity (message_id, tenant_id, kind, value, norm)
			 VALUES (?, ?, ?, ?, ?)`,
			messageID, tenantID, string(v.Kind), v.Text, v.Norm); err != nil {
			// One statement, one chance. Retrying a failing insert here would hold the
			// delivery path open for a search index.
			return
		}
	}
}

// IdentitySearch describes a search for a message by an identifier.
type IdentitySearch struct {
	// Term is what the person typed: an MRN, a name, a date of birth, an accession.
	Term string

	// Kind narrows to one sort of identifier. Empty searches all of them, which is the
	// default because the person often does not know which sort of number they are holding.
	Kind identity.Kind

	// TenantID scopes the search. Empty means the main tenant, matching Query.
	TenantID string

	// Channel, Since and Until narrow the same way the message list does, so a search can be
	// confined to one feed or one afternoon.
	Channel string
	Since   time.Time
	Until   time.Time

	Limit  int
	Offset int
}

// IdentityMatch is one message that matched, with the reason it matched.
type IdentityMatch struct {
	Message Message

	// MatchedKind and MatchedValue are why this message is in the result.
	//
	// Returned because a result list of otherwise identical ADT messages gives no clue which
	// field the search hit, and "it matched the account number, not the MRN" changes what the
	// person does next.
	MatchedKind  identity.Kind
	MatchedValue string
}

// IdentityResult is what an identity search found.
type IdentityResult struct {
	// Matches is initialised rather than nil so it marshals as an empty array. A client doing
	// .length on null crashes on exactly the search that found nothing.
	Matches []IdentityMatch

	// Total is how many messages matched before the page was taken.
	Total int

	// Indexed reports whether identity indexing is switched on.
	//
	// Returned on every search, because the difference between "no message mentions that
	// number" and "nothing has been indexed" is the difference between a clinical answer and
	// a configuration mistake. Without this a site that never enabled indexing sees an empty
	// result and concludes the patient's messages were never received.
	Indexed bool
}

// SearchByIdentity finds messages whose extracted identifiers match a term.
//
// The term is normalised the same way the stored values were, so a name written SMITH^JOHN in
// the message is found by typing "Smith, John" and an MRN written MRN-001234 is found by
// typing mrn001234. Matching is exact on the normalised form rather than a prefix or a
// substring: a substring match on a normalised identifier makes every short number match
// thousands of messages, which is not a search result anybody can use.
func (s *Store) SearchByIdentity(ctx context.Context, q IdentitySearch) (*IdentityResult, error) {
	res := &IdentityResult{Matches: []IdentityMatch{}, Indexed: s.indexIdentity()}

	term := identity.Normalise(q.Term)
	if term == "" {
		return nil, fmt.Errorf("something to search for is required")
	}

	where := []string{"i.norm = ?", "i.tenant_id = ?"}
	args := []any{term, tenantOrDefault(q.TenantID)}

	if q.Kind != "" {
		where = append(where, "i.kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.Channel != "" {
		where = append(where, "m.channel = ?")
		args = append(args, q.Channel)
	}
	if !q.Since.IsZero() {
		where = append(where, "m.received_at >= ?")
		args = append(args, dbtime.Format(q.Since))
	}
	if !q.Until.IsZero() {
		where = append(where, "m.received_at <= ?")
		args = append(args, dbtime.Format(q.Until))
	}
	clause := strings.Join(where, " AND ")

	// Counted over distinct messages, not over index rows. A message that matches on both its
	// MRN and its account number is one message, and reporting two would make the total
	// disagree with the list below it.
	if err := s.querier().QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT i.message_id)
		   FROM message_identity i JOIN messages m ON m.id = i.message_id
		  WHERE `+clause, args...).Scan(&res.Total); err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	// GROUP BY the message so one row comes back per message however many of its identifiers
	// matched, with MIN picking one reason deterministically. Without the grouping a patient
	// whose MRN appears as both an MR and an EPI identifier is listed twice, which reads as
	// duplicate messages and sends somebody looking for a duplication bug that is not there.
	pageArgs := append(append([]any(nil), args...), limit, q.Offset)
	rows, err := s.querier().QueryContext(ctx,
		`SELECT m.id, m.channel, m.received_at, COALESCE(m.control_id,''),
		        COALESCE(m.message_type,''), COALESCE(m.trigger_event,''),
		        COALESCE(m.sender,''), COALESCE(m.remote,''), m.outcome,
		        COALESCE(m.ack_code,''), m.size, m.segments, m.duration_ms,
		        COALESCE(m.error,''), MIN(i.kind), MIN(i.value)
		   FROM message_identity i JOIN messages m ON m.id = i.message_id
		  WHERE `+clause+`
		  GROUP BY m.id
		  ORDER BY m.received_at DESC, m.id DESC
		  LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			m          Message
			receivedAt string
			outcome    string
			kind       string
			value      string
		)
		if err := rows.Scan(&m.ID, &m.Channel, &receivedAt, &m.ControlID, &m.MessageType,
			&m.TriggerEvent, &m.Sender, &m.Remote, &outcome, &m.AckCode, &m.Size,
			&m.Segments, &m.DurationMS, &m.Error, &kind, &value); err != nil {
			return nil, err
		}
		m.ReceivedAt = parseTime(receivedAt)
		m.Outcome = Outcome(outcome)
		res.Matches = append(res.Matches, IdentityMatch{
			Message:      m,
			MatchedKind:  identity.Kind(kind),
			MatchedValue: value,
		})
	}
	return res, rows.Err()
}

// IdentityKinds lists the kinds of identifier that can be searched.
//
// Sorted, because a Go map ranges randomly and a list of search options that reorders on every
// page load looks broken.
func IdentityKinds() []identity.Kind {
	return []identity.Kind{
		identity.KindPatientID,
		identity.KindPatientName,
		identity.KindBirthDate,
		identity.KindAccount,
		identity.KindAccession,
		identity.KindClaim,
		identity.KindStudyUID,
	}
}
