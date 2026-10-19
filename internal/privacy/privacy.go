// Package privacy answers a data subject's requests under the GDPR and the UK GDPR: find everything this server holds about a
// person, give it to them (Articles 15 and 20), erase it (Article 17), and keep what a legal hold covers.
//
// It reaches every place Perfuse keeps patient data: the message store (through its identity index), the delivery queue, the
// FHIR store (the person's Patient resources and everything that refers to them, with their history), stored prior
// authorization requests, and the patient index, whose links widen one identifier into all of the person's numbers. What it
// cannot reach it names: traces sent to a collector, the audit log (kept, as the record of who did what), and backups,
// which keep what was erased until they expire - so every erasure is written to a ledger, as salted hashes of the
// identifiers rather than the identifiers, and can be applied again to a restored copy.
//
// It decides nothing on anybody's behalf. Whether a request must be granted, and whether an exemption applies, is the
// controller's decision; erasure needs a second administrator's approval, and a legal hold refuses it outright.
package privacy

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/identity"
	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/natid"
	"github.com/biodream-llc/perfuse/internal/queue"
)

// Service holds the requests, holds and ledger, and the stores it searches. Any store may be nil: a server without a FHIR
// store has no FHIR records to find.
type Service struct {
	db *sql.DB

	Messages *msgstore.Store
	FHIR     *fhirserver.Store
	Index    *mpi.Store
	Queue    *queue.Store
	// Elsewhere are places personal data goes that this server cannot erase (a trace collector, say), named in every finding.
	Elsewhere []string

	now func() time.Time
}

// Open prepares the tables in the server's own database.
func Open(ctx context.Context, db *sql.DB) (*Service, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS privacy_requests (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, kind TEXT NOT NULL,
			subject TEXT NOT NULL, identifiers TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			created_at TEXT NOT NULL, created_by TEXT NOT NULL, due TEXT NOT NULL, approved_by TEXT NOT NULL DEFAULT '',
			closed_at TEXT NOT NULL DEFAULT '', closed_by TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS privacy_holds (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, identifiers TEXT NOT NULL,
			reason TEXT NOT NULL, until TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, created_by TEXT NOT NULL,
			lifted_at TEXT NOT NULL DEFAULT '', lifted_by TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS privacy_erasures (id INTEGER PRIMARY KEY, tenant TEXT NOT NULL, request_id INTEGER NOT NULL,
			at TEXT NOT NULL, actor TEXT NOT NULL, approver TEXT NOT NULL, salt TEXT NOT NULL, hashes TEXT NOT NULL, counts TEXT NOT NULL)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("preparing the data protection tables: %w", err)
		}
	}
	return &Service{db: db, now: time.Now}, nil
}

func (s *Service) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// ---- the subject and what is held about them ------------------------------------------------------------------------

// Clean trims identifiers, drops empties and duplicates, and writes national numbers as the patient index stores them.
func Clean(idents []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range idents {
		i = strings.TrimSpace(i)
		if i == "" {
			continue
		}
		if sys, val, ok := strings.Cut(i, "|"); ok {
			c := natid.Canonicalise(sys, val, nil)
			i = c.System + "|" + c.Value
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	sort.Strings(out)
	return out
}

// Finding is everything this server holds about a subject.
type Finding struct {
	// Identifiers are those searched for: the ones given, widened by the patient index to every number of every record
	// linked to the same person.
	Identifiers []string `json:"identifiers"`
	// Indexed says the message store's identity index is on; without it messages cannot be found by identifier at all.
	Indexed  bool                      `json:"indexed"`
	Messages []msgstore.SubjectMessage `json:"messages"`
	Queue    []QueueItem               `json:"queue"`
	Patients []string                  `json:"patients"`
	FHIR     []fhirserver.ResourceRef  `json:"fhir"`
	PAS      []fhirserver.PASRequest   `json:"pas"`
	Persons  []PersonSummary           `json:"persons"`
	Holds    []Hold                    `json:"holds"`
	Notes    []string                  `json:"notes"`
	Counts   map[string]int            `json:"counts"`
	persons  []*mpi.Person
}

// QueueItem is a queued delivery about the subject.
type QueueItem struct {
	ID          int64  `json:"id"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	State       string `json:"state"`
}

// PersonSummary is a person in the patient index.
type PersonSummary struct {
	EUID    string `json:"euid"`
	Records int    `json:"records"`
}

// Find gathers what every store holds about the subject named by identifiers (system|value, or a bare value).
func (s *Service) Find(ctx context.Context, tenant string, idents []string, set mpi.Settings) (*Finding, error) {
	idents = Clean(idents)
	if len(idents) == 0 {
		return nil, errors.New("privacy: name the person by at least one identifier (system|value, or a number)")
	}
	f := &Finding{Messages: []msgstore.SubjectMessage{}, Queue: []QueueItem{}, Patients: []string{}, FHIR: []fhirserver.ResourceRef{},
		PAS: []fhirserver.PASRequest{}, Persons: []PersonSummary{}, Holds: []Hold{}, Notes: []string{}, Counts: map[string]int{}}
	all := map[string]bool{}
	for _, i := range idents {
		all[i] = true
	}
	if s.Index != nil {
		persons, err := s.Index.SubjectPersons(ctx, mpi.TenantOf(tenant), idents, set)
		if err != nil {
			return nil, err
		}
		f.persons = persons
		for _, p := range persons {
			f.Persons = append(f.Persons, PersonSummary{EUID: p.EUID, Records: len(p.Records)})
			for _, r := range p.Records {
				all[r.Key()] = true
				for _, i := range r.Identifiers {
					all[i] = true
				}
			}
		}
	}
	for i := range all {
		f.Identifiers = append(f.Identifiers, i)
	}
	sort.Strings(f.Identifiers)
	norms := msgstore.NormaliseIdentifiers(f.Identifiers)
	if s.Messages != nil {
		f.Indexed = s.Messages.IndexesIdentity()
		msgs, err := s.Messages.SubjectMessages(ctx, tenant, norms, 0)
		if err != nil {
			return nil, err
		}
		f.Messages = msgs
		if !f.Indexed {
			f.Notes = append(f.Notes, "The identity index is off (Settings → Data → Index patient identifiers), so messages "+
				"recorded while it was off cannot be found by identifier; they leave the store at the retention window.")
		}
	}
	if s.Queue != nil {
		want := map[string]bool{}
		for _, n := range norms {
			want[n] = true
		}
		err := s.Queue.ScanPayloads(ctx, func(id int64, ch, dest, state string, raw []byte) error {
			for _, v := range identity.Extract(raw).Values {
				if want[v.Norm] {
					f.Queue = append(f.Queue, QueueItem{id, ch, dest, state})
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if s.FHIR != nil {
		pats, refs, err := s.FHIR.SubjectResources(ctx, f.Identifiers)
		if err != nil {
			return nil, err
		}
		f.Patients, f.FHIR = orEmpty(pats), refs
		if f.FHIR == nil {
			f.FHIR = []fhirserver.ResourceRef{}
		}
		pas, err := s.FHIR.SubjectPAS(ctx, f.Identifiers)
		if err != nil {
			return nil, err
		}
		f.PAS = pas
	}
	holds, err := s.HoldsFor(ctx, tenant, f.Identifiers)
	if err != nil {
		return nil, err
	}
	f.Holds = holds
	f.Notes = append(f.Notes, "The audit log is kept: it records who did what, including this request, and holds identifiers only "+
		"where an action named one.", "Backups made before an erasure still hold what it erased until they expire; the erasure is "+
		"in the ledger, to apply again to a restored copy (Data protection → Apply erasures again).")
	f.Notes = append(f.Notes, s.Elsewhere...)
	f.Counts = map[string]int{"messages": len(f.Messages), "queue": len(f.Queue), "fhir": len(f.FHIR), "pas": len(f.PAS),
		"persons": len(f.Persons)}
	return f, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- access and portability -------------------------------------------------------------------------------------------

// Export writes what was found as a zip: a manifest, every message as received, every FHIR resource with every version,
// prior authorization requests and responses, and the patient index's records. JSON throughout, so it is readable by machine
// as Article 20 asks, with a README saying what each part is.
func (s *Service) Export(ctx context.Context, tenant string, f *Finding, w io.Writer) error {
	z := zip.NewWriter(w)
	put := func(name string, b []byte) error {
		fw, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = fw.Write(b)
		return err
	}
	readme := "What this server holds about the person named in manifest.json, as of " + s.stamp() + ".\n\n" +
		"messages/   each message as it was received, with its record (channel, time, outcome) beside it\n" +
		"fhir/       each FHIR resource about the person, every stored version, oldest first\n" +
		"priorauth/  prior authorization requests and the answers given\n" +
		"index.json  the patient index: the records each system holds and how they were linked\n\n" +
		"Not included: the audit log (who did what on this server), traces sent elsewhere, and backups.\n"
	if err := put("README.txt", []byte(readme)); err != nil {
		return err
	}
	man, _ := json.MarshalIndent(map[string]any{"exported": s.stamp(), "identifiers": f.Identifiers, "counts": f.Counts,
		"notes": f.Notes}, "", "  ")
	if err := put("manifest.json", man); err != nil {
		return err
	}
	if s.Messages != nil {
		for _, m := range f.Messages {
			msg, err := s.Messages.Get(ctx, tenant, m.ID)
			if err != nil {
				continue // pruned since it was found
			}
			meta, _ := json.MarshalIndent(map[string]any{"id": msg.ID, "channel": msg.Channel, "receivedAt": msg.ReceivedAt,
				"type": msg.MessageType, "event": msg.TriggerEvent, "controlId": msg.ControlID, "outcome": msg.Outcome}, "", "  ")
			base := fmt.Sprintf("messages/%d", m.ID)
			if err := put(base+".json", meta); err != nil {
				return err
			}
			if len(msg.Raw) > 0 {
				if err := put(base+".message", msg.Raw); err != nil {
					return err
				}
			}
		}
	}
	if s.FHIR != nil {
		for _, r := range f.FHIR {
			versions, err := s.FHIR.ResourceVersions(ctx, r)
			if err != nil {
				return err
			}
			for i, v := range versions {
				if err := put(fmt.Sprintf("fhir/%s-%s.v%d.json", r.Type, r.ID, i+1), v); err != nil {
					return err
				}
			}
		}
		for _, p := range f.PAS {
			req, resps, err := s.FHIR.PASContent(ctx, p.ID)
			if err != nil {
				continue
			}
			if err := put("priorauth/"+p.ID+".request.json", req); err != nil {
				return err
			}
			for i, r := range resps {
				if err := put(fmt.Sprintf("priorauth/%s.response%d.json", p.ID, i+1), r); err != nil {
					return err
				}
			}
		}
	}
	if len(f.persons) > 0 {
		b, _ := json.MarshalIndent(f.persons, "", "  ")
		if err := put("index.json", b); err != nil {
			return err
		}
	}
	return z.Close()
}

// ---- erasure ------------------------------------------------------------------------------------------------------------

// ErrHeld is an erasure refused because a legal hold covers the person.
var ErrHeld = errors.New("privacy: a legal hold covers this person; lift it before erasing")

// Erase deletes everything found, writes the erasure to the ledger, and answers how much went from each store. A finding
// under a legal hold is refused whole: part of a record erased and part kept under hold serves neither purpose.
func (s *Service) Erase(ctx context.Context, tenant string, requestID int64, f *Finding, actor, approver string) (map[string]int, error) {
	if len(f.Holds) > 0 {
		return nil, ErrHeld
	}
	counts := map[string]int{}
	if s.Queue != nil && len(f.Queue) > 0 {
		ids := make([]int64, len(f.Queue))
		for i, q := range f.Queue {
			ids[i] = q.ID
		}
		n, err := s.Queue.Erase(ctx, ids)
		if err != nil {
			return counts, err
		}
		counts["queue"] = int(n)
	}
	if s.Messages != nil && len(f.Messages) > 0 {
		ids := make([]int64, len(f.Messages))
		for i, m := range f.Messages {
			ids[i] = m.ID
		}
		n, err := s.Messages.EraseMessages(ctx, ids)
		if err != nil {
			return counts, err
		}
		counts["messages"] = int(n)
	}
	if s.FHIR != nil {
		if len(f.FHIR) > 0 {
			n, err := s.FHIR.Expunge(ctx, f.FHIR)
			if err != nil {
				return counts, err
			}
			counts["fhir"] = n
		}
		if len(f.PAS) > 0 {
			ids := make([]string, len(f.PAS))
			for i, p := range f.PAS {
				ids[i] = p.ID
			}
			n, err := s.FHIR.ErasePAS(ctx, ids)
			if err != nil {
				return counts, err
			}
			counts["pas"] = n
		}
	}
	if s.Index != nil {
		for _, p := range f.Persons {
			n, err := s.Index.ErasePerson(ctx, mpi.TenantOf(tenant), actor, p.EUID, fmt.Sprintf("data protection request %d", requestID))
			if err != nil && !errors.Is(err, mpi.ErrNotFound) {
				return counts, err
			}
			counts["persons"]++
			counts["records"] += n
		}
	}
	if err := s.ledger(ctx, tenant, requestID, actor, approver, f.Identifiers, counts); err != nil {
		return counts, err
	}
	return counts, nil
}

// hashIdentifier is how the ledger keeps an identifier: SHA-256 over the entry's salt and the value as the identity index
// reduces it, so the ledger can recognise the person in a restored copy without holding their numbers.
func hashIdentifier(salt, norm string) string {
	h := sha256.Sum256([]byte(salt + "\x00" + norm))
	return hex.EncodeToString(h[:])
}

func (s *Service) ledger(ctx context.Context, tenant string, requestID int64, actor, approver string, idents []string, counts map[string]int) error {
	saltB := make([]byte, 16)
	if _, err := rand.Read(saltB); err != nil {
		return err
	}
	salt := hex.EncodeToString(saltB)
	var hashes []string
	for _, n := range msgstore.NormaliseIdentifiers(idents) {
		hashes = append(hashes, hashIdentifier(salt, n))
	}
	hb, _ := json.Marshal(hashes)
	cb, _ := json.Marshal(counts)
	_, err := s.db.ExecContext(ctx, `INSERT INTO privacy_erasures (tenant, request_id, at, actor, approver, salt, hashes, counts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tenant, requestID, s.stamp(), actor, approver, salt, string(hb), string(cb))
	return err
}

// Erasure is a ledger entry.
type Erasure struct {
	ID        int64          `json:"id"`
	RequestID int64          `json:"requestId"`
	At        string         `json:"at"`
	Actor     string         `json:"actor"`
	Approver  string         `json:"approver"`
	Counts    map[string]int `json:"counts"`
	salt      string
	hashes    []string
}

// Erasures lists the ledger, newest first.
func (s *Service) Erasures(ctx context.Context, tenant string) ([]Erasure, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, request_id, at, actor, approver, salt, hashes, counts FROM privacy_erasures
		WHERE tenant = ? ORDER BY id DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Erasure{}
	for rows.Next() {
		var e Erasure
		var hb, cb string
		if err := rows.Scan(&e.ID, &e.RequestID, &e.At, &e.Actor, &e.Approver, &e.salt, &hb, &cb); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(hb), &e.hashes)
		_ = json.Unmarshal([]byte(cb), &e.Counts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Reapply erases again, from a copy restored from a backup, everything the ledger says was erased. The identifiers the
// stores hold now are hashed with each entry's salt; any that match name a person erased before, who is erased again.
func (s *Service) Reapply(ctx context.Context, tenant, actor string, candidates []string, set mpi.Settings) (map[string]int, error) {
	entries, err := s.Erasures(ctx, tenant)
	if err != nil {
		return nil, err
	}
	total := map[string]int{}
	byNorm := map[string]string{}
	for _, c := range candidates {
		for _, n := range msgstore.NormaliseIdentifiers([]string{c}) {
			byNorm[n] = c
		}
	}
	for _, e := range entries {
		want := map[string]bool{}
		for _, h := range e.hashes {
			want[h] = true
		}
		var match []string
		for n, ident := range byNorm {
			if want[hashIdentifier(e.salt, n)] {
				match = append(match, ident)
			}
		}
		if len(match) == 0 {
			continue
		}
		f, err := s.Find(ctx, tenant, match, set)
		if err != nil {
			return total, err
		}
		f.Holds = nil // the erasure was decided already; a hold placed later is checked when it is placed
		counts, err := s.Erase(ctx, tenant, e.RequestID, f, actor, e.Approver)
		if err != nil {
			return total, err
		}
		for k, v := range counts {
			total[k] += v
		}
	}
	return total, nil
}

// Candidates are the identifiers the stores hold now, for Reapply: every value in the identity index, every Patient
// identifier and every patient index number.
func (s *Service) Candidates(ctx context.Context, tenant string) ([]string, error) {
	var out []string
	add := func(db queryer, q string, args ...any) error {
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err == nil && v != "" {
				out = append(out, v)
			}
		}
		return rows.Err()
	}
	if s.Messages != nil {
		if err := add(s.Messages.Reader(), `SELECT DISTINCT value FROM message_identity WHERE tenant_id = ?`, msgstoreTenant(tenant)); err != nil {
			return nil, err
		}
	}
	if s.FHIR != nil {
		if err := add(s.FHIR.Reader(), `SELECT DISTINCT value FROM fhir_search WHERE resource_type = 'Patient' AND param = 'identifier'`); err != nil {
			return nil, err
		}
	}
	if s.Index != nil {
		if err := add(s.Index.Reader(), `SELECT DISTINCT ident FROM mpi_identifiers WHERE tenant = ?`, mpi.TenantOf(tenant)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func msgstoreTenant(t string) string {
	if t == "" {
		return "main"
	}
	return t
}

// ---- legal holds ----------------------------------------------------------------------------------------------------------

// Hold keeps a person's data from erasure and from the retention window while a claim, an investigation or a statutory
// duty needs it.
type Hold struct {
	ID          int64    `json:"id"`
	Identifiers []string `json:"identifiers"`
	Reason      string   `json:"reason"`
	Until       string   `json:"until,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	CreatedBy   string   `json:"createdBy"`
	LiftedAt    string   `json:"liftedAt,omitempty"`
	LiftedBy    string   `json:"liftedBy,omitempty"`
}

// PlaceHold puts identifiers under a hold.
func (s *Service) PlaceHold(ctx context.Context, tenant, actor string, idents []string, reason, until string) (int64, error) {
	idents = Clean(idents)
	if len(idents) == 0 {
		return 0, errors.New("privacy: a hold names the person by at least one identifier")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("privacy: a hold needs its reason (the claim, investigation or duty it serves)")
	}
	if until != "" {
		if _, err := time.Parse("2006-01-02", until); err != nil {
			return 0, errors.New("privacy: the hold's end date should be YYYY-MM-DD, or empty until lifted")
		}
	}
	b, _ := json.Marshal(idents)
	res, err := s.db.ExecContext(ctx, `INSERT INTO privacy_holds (tenant, identifiers, reason, until, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`, tenant, string(b), strings.TrimSpace(reason), until, s.stamp(), actor)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LiftHold ends a hold.
func (s *Service) LiftHold(ctx context.Context, tenant, actor string, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE privacy_holds SET lifted_at = ?, lifted_by = ? WHERE tenant = ? AND id = ? AND lifted_at = ''`,
		s.stamp(), actor, tenant, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("privacy: no such hold in force")
	}
	return nil
}

// Holds lists holds, those in force first.
func (s *Service) Holds(ctx context.Context, tenant string) ([]Hold, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, identifiers, reason, until, created_at, created_by, lifted_at, lifted_by
		FROM privacy_holds WHERE tenant = ? ORDER BY lifted_at = '' DESC, id DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hold{}
	for rows.Next() {
		var h Hold
		var ib string
		if err := rows.Scan(&h.ID, &ib, &h.Reason, &h.Until, &h.CreatedAt, &h.CreatedBy, &h.LiftedAt, &h.LiftedBy); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ib), &h.Identifiers)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (h Hold) inForce(today string) bool {
	return h.LiftedAt == "" && (h.Until == "" || h.Until >= today)
}

// HoldsFor gives the holds in force that name any of the identifiers.
func (s *Service) HoldsFor(ctx context.Context, tenant string, idents []string) ([]Hold, error) {
	all, err := s.Holds(ctx, tenant)
	if err != nil {
		return nil, err
	}
	norms := map[string]bool{}
	for _, n := range msgstore.NormaliseIdentifiers(idents) {
		norms[n] = true
	}
	today := s.now().UTC().Format("2006-01-02")
	out := []Hold{}
	for _, h := range all {
		if !h.inForce(today) {
			continue
		}
		for _, n := range msgstore.NormaliseIdentifiers(h.Identifiers) {
			if norms[n] {
				out = append(out, h)
				break
			}
		}
	}
	return out, nil
}

// HeldNorms is every identifier under a hold in force, in every tenant, reduced as the identity index stores them: what the
// message store's pruner spares.
func (s *Service) HeldNorms(ctx context.Context) []string {
	rows, err := s.db.QueryContext(ctx, `SELECT identifiers, until FROM privacy_holds WHERE lifted_at = ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	today := s.now().UTC().Format("2006-01-02")
	var idents []string
	for rows.Next() {
		var ib, until string
		if rows.Scan(&ib, &until) != nil || (until != "" && until < today) {
			continue
		}
		var list []string
		_ = json.Unmarshal([]byte(ib), &list)
		idents = append(idents, list...)
	}
	return msgstore.NormaliseIdentifiers(idents)
}

// ---- requests ---------------------------------------------------------------------------------------------------------------

// Request is a data subject's request, from receipt to answer.
type Request struct {
	ID int64 `json:"id"`
	// Kind is access (Article 15, a copy), portability (Article 20, the same copy in a machine-readable form), erasure
	// (Article 17) or restriction (Article 18: kept, not erased, while something is settled - recorded as a hold).
	Kind string `json:"kind"`
	// Subject is how the person is referred to on the request (a name or reference the controller uses), never searched.
	Subject     string   `json:"subject"`
	Identifiers []string `json:"identifiers"`
	Note        string   `json:"note,omitempty"`
	// Status is open, approved (an erasure a second administrator approved), done or refused.
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
	CreatedBy  string `json:"createdBy"`
	Due        string `json:"due"`
	ApprovedBy string `json:"approvedBy,omitempty"`
	ClosedAt   string `json:"closedAt,omitempty"`
	ClosedBy   string `json:"closedBy,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

var kinds = map[string]bool{"access": true, "portability": true, "erasure": true, "restriction": true}

// Create records a request received. It is due one month after receipt (Article 12(3)); an extension is the controller's
// to give, by changing the due date with the reason in the note.
func (s *Service) Create(ctx context.Context, tenant, actor string, r Request) (int64, error) {
	if !kinds[r.Kind] {
		return 0, errors.New("privacy: a request is access, portability, erasure or restriction")
	}
	r.Identifiers = Clean(r.Identifiers)
	if len(r.Identifiers) == 0 {
		return 0, errors.New("privacy: name the person by at least one identifier (system|value, or a number)")
	}
	if strings.TrimSpace(r.Subject) == "" {
		return 0, errors.New("privacy: say who the request is from, as your records refer to them")
	}
	due := r.Due
	if due == "" {
		due = s.now().UTC().AddDate(0, 1, 0).Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", due); err != nil {
		return 0, errors.New("privacy: the due date should be YYYY-MM-DD")
	}
	b, _ := json.Marshal(r.Identifiers)
	res, err := s.db.ExecContext(ctx, `INSERT INTO privacy_requests (tenant, kind, subject, identifiers, note, status, created_at,
		created_by, due) VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?)`, tenant, r.Kind, strings.TrimSpace(r.Subject), string(b),
		strings.TrimSpace(r.Note), s.stamp(), actor, due)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Requests lists requests, open ones first, soonest due first.
func (s *Service) Requests(ctx context.Context, tenant string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, subject, identifiers, note, status, created_at, created_by, due,
		approved_by, closed_at, closed_by, outcome FROM privacy_requests WHERE tenant = ?
		ORDER BY status IN ('done','refused'), due, id`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var r Request
		var ib string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Subject, &ib, &r.Note, &r.Status, &r.CreatedAt, &r.CreatedBy, &r.Due,
			&r.ApprovedBy, &r.ClosedAt, &r.ClosedBy, &r.Outcome); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ib), &r.Identifiers)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get reads one request.
func (s *Service) Get(ctx context.Context, tenant string, id int64) (Request, error) {
	list, err := s.Requests(ctx, tenant)
	if err != nil {
		return Request{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return Request{}, errors.New("privacy: no such request")
}

// Approve is a second administrator's approval of an erasure. The person who recorded the request cannot approve it.
func (s *Service) Approve(ctx context.Context, tenant, actor string, id int64) error {
	r, err := s.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	switch {
	case r.Kind != "erasure":
		return errors.New("privacy: only an erasure needs approving")
	case r.Status != "open":
		return fmt.Errorf("privacy: the request is %s, not open", r.Status)
	case r.CreatedBy == actor:
		return errors.New("privacy: an erasure is approved by a second person, not by whoever recorded it")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE privacy_requests SET status = 'approved', approved_by = ? WHERE tenant = ? AND id = ?`, actor, tenant, id)
	return err
}

// Close finishes a request: done with what was done, or refused with the reason (an exemption, an identity not
// confirmed). Its identifiers are then reduced to their last characters: the request stays as the record of an answer,
// without being a second copy of the person's numbers.
func (s *Service) Close(ctx context.Context, tenant, actor string, id int64, status, outcome string) error {
	if status != "done" && status != "refused" {
		return errors.New("privacy: a request closes as done or refused")
	}
	if status == "refused" && strings.TrimSpace(outcome) == "" {
		return errors.New("privacy: a refusal needs its reason, which the person must be told")
	}
	r, err := s.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	if r.Status == "done" || r.Status == "refused" {
		return fmt.Errorf("privacy: the request is already %s", r.Status)
	}
	masked := make([]string, len(r.Identifiers))
	for i, ident := range r.Identifiers {
		sys, val, ok := strings.Cut(ident, "|")
		if !ok {
			sys, val = "", ident
		}
		masked[i] = strings.TrimPrefix(sys+"|", "|") + natid.Mask(val)
	}
	b, _ := json.Marshal(masked)
	_, err = s.db.ExecContext(ctx, `UPDATE privacy_requests SET status = ?, closed_at = ?, closed_by = ?, outcome = ?, identifiers = ?
		WHERE tenant = ? AND id = ?`, status, s.stamp(), actor, strings.TrimSpace(outcome), string(b), tenant, id)
	return err
}
